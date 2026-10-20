package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
	"github.com/mrhollen/ravn/internal/mcp"
)

func TestStatusRecorder_Flush(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	if _, ok := any(sr).(http.Flusher); !ok {
		t.Fatal("expected statusRecorder to implement http.Flusher")
	}
	sr.Flush()
	if !rec.Flushed {
		t.Fatal("expected Flush() to propagate to wrapped ResponseWriter")
	}
}

func TestMessageHandlerWithSessionFallback(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		header        string
		wantSessionID string
	}{
		{
			name:          "standard sessionId in query",
			url:           "/message?sessionId=session-1",
			wantSessionID: "session-1",
		},
		{
			name:          "fallback from Mcp-Session-Id header",
			url:           "/message",
			header:        "session-header-1",
			wantSessionID: "session-header-1",
		},
		{
			name:          "fallback from session_id query param",
			url:           "/message?session_id=session-param-1",
			wantSessionID: "session-param-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotSessionID string
			innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSessionID = r.URL.Query().Get("sessionId")
				w.WriteHeader(http.StatusOK)
			})

			handler := messageHandlerWithSessionFallback(innerHandler)

			req := httptest.NewRequest(http.MethodPost, tc.url, nil)
			if tc.header != "" {
				req.Header.Set("Mcp-Session-Id", tc.header)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if gotSessionID != tc.wantSessionID {
				t.Fatalf("expected sessionId %q, got %q", tc.wantSessionID, gotSessionID)
			}
		})
	}
}

func TestMuxRouting_StreamableAndSSE(t *testing.T) {
	client := apiclient.New("http://ravn-server:8080", "")
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}

	mcs := mcpsrv.NewMCPServer("ravn", "test", mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)
	mcp.RegisterPrompts(mcs, client, defaultActor)
	mcp.RegisterResources(mcs, client)

	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithDisableLocalhostProtection(true),
	)

	sse := mcpsrv.NewSSEServer(mcs,
		mcpsrv.WithKeepAlive(false),
		mcpsrv.WithSSEDisableLocalhostProtection(true),
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", messageHandlerWithSessionFallback(sse.MessageHandler()))

	ts := httptest.NewServer(withLogging(nil, withModernMCPCompat(mux)))
	defer ts.Close()

	// 1. POST /mcp handles initialize
	initPayload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(initPayload))
	if err != nil {
		t.Fatalf("POST /mcp failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected POST /mcp to return 200, got %d", resp.StatusCode)
	}
	var initResp struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		t.Fatalf("failed to decode initialize response: %v", err)
	}
	resp.Body.Close()
	if initResp.Result.ServerInfo.Name != "ravn" {
		t.Fatalf("expected serverInfo.name %q, got %q", "ravn", initResp.Result.ServerInfo.Name)
	}

	// 2. POST /mcp handles notifications/roots/list_changed without 400 Bad Request
	notifPayload := `{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`
	resp, err = http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(notifPayload))
	if err != nil {
		t.Fatalf("POST /mcp notification failed: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected POST /mcp notification to return 202 Accepted, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. POST /mcp with Mcp-Protocol-Version: 2026-07-28 header and NO _meta in body
	// (exactly what modelcontextprotocol/go_sdk sends) is accepted via withModernMCPCompat
	reqModern, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(notifPayload))
	reqModern.Header.Set("Content-Type", "application/json")
	reqModern.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	reqModern.Header.Set("Mcp-Method", "notifications/roots/list_changed")
	respModern, err := http.DefaultClient.Do(reqModern)
	if err != nil {
		t.Fatalf("POST /mcp modern notification failed: %v", err)
	}
	if respModern.StatusCode != http.StatusAccepted {
		t.Fatalf("expected POST /mcp modern notification to return 202 Accepted, got %d", respModern.StatusCode)
	}
	respModern.Body.Close()

	// 4. GET /mcp provides listening streamable SSE stream (does not return legacy endpoint event)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected GET /mcp to return 200 OK, got %d", resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
		}
	}

	// 4. GET /sse provides legacy SSE endpoint
	ctxSSE, cancelSSE := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelSSE()
	reqSSE, _ := http.NewRequestWithContext(ctxSSE, http.MethodGet, ts.URL+"/sse", nil)
	reqSSE.Header.Set("Accept", "text/event-stream")
	respSSE, err := http.DefaultClient.Do(reqSSE)
	if err == nil {
		defer respSSE.Body.Close()
		if respSSE.StatusCode != http.StatusOK {
			t.Fatalf("expected GET /sse to return 200 OK, got %d", respSSE.StatusCode)
		}
	}

	// 5. POST /mcp handles prompts/list
	promptListPayload := `{"jsonrpc":"2.0","id":5,"method":"prompts/list"}`
	respPrompt, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(promptListPayload))
	if err != nil {
		t.Fatalf("POST /mcp prompts/list failed: %v", err)
	}
	if respPrompt.StatusCode != http.StatusOK {
		t.Fatalf("expected POST /mcp prompts/list to return 200, got %d", respPrompt.StatusCode)
	}
	respPrompt.Body.Close()

	// 6. POST /mcp handles resources/list
	resListPayload := `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`
	respRes, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(resListPayload))
	if err != nil {
		t.Fatalf("POST /mcp resources/list failed: %v", err)
	}
	if respRes.StatusCode != http.StatusOK {
		t.Fatalf("expected POST /mcp resources/list to return 200, got %d", respRes.StatusCode)
	}
	respRes.Body.Close()
}

func TestWithAuth_DisabledWhenTokenEmpty(t *testing.T) {
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := withAuth("", inner)

	for _, p := range []string{"/mcp", "/sse", "/message", "/healthz"} {
		innerCalled = false
		req := httptest.NewRequest(http.MethodPost, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected path %s with empty token to return 200, got %d", p, rec.Code)
		}
		if !innerCalled {
			t.Fatalf("expected inner handler to be called for %s", p)
		}
	}
}

func TestWithAuth_RejectsMissingOrInvalidToken(t *testing.T) {
	const secret = "mcp-secret-token-123"
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	cases := []struct {
		name       string
		path       string
		authHeader string
	}{
		{"missing auth header /mcp", "/mcp", ""},
		{"missing auth header /sse", "/sse", ""},
		{"missing auth header /message", "/message", ""},
		{"wrong token", "/mcp", "Bearer wrong-token"},
		{"empty bearer", "/mcp", "Bearer "},
		{"whitespace bearer", "/mcp", "Bearer    "},
		{"basic auth scheme", "/mcp", "Basic dXNlcjpwYXNz"},
		{"custom scheme", "/mcp", "Token " + secret},
		{"missing space in bearer", "/mcp", "Bearer" + secret},
		{"invalid query token", "/mcp?token=wrong-token", ""},
		{"empty query token", "/mcp?token=", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			innerCalled = false
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
			}
			if innerCalled {
				t.Fatalf("inner handler was called on unauthorized request")
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to decode error body: %v", err)
			}
			if body["error"] != "unauthorized" {
				t.Errorf("expected error field to be 'unauthorized', got %v", body["error"])
			}
			if body["message"] != "missing or invalid auth token" {
				t.Errorf("expected message 'missing or invalid auth token', got %v", body["message"])
			}
		})
	}
}

func TestWithAuth_AcceptsValidBearerToken(t *testing.T) {
	const secret = "mcp-secret-token-123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	headers := []string{
		"Bearer " + secret,
		"bearer " + secret,
		"BEARER " + secret,
		"Bearer  " + secret + " ",
		"  Bearer " + secret + "  ",
	}

	for _, p := range []string{"/mcp", "/sse", "/message"} {
		for _, h := range headers {
			t.Run(p+"_"+h, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, p, nil)
				req.Header.Set("Authorization", h)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("expected 200 OK for %s with header %q, got %d", p, h, rec.Code)
				}
			})
		}
	}
}

func TestWithAuth_AcceptsQueryParamFallback(t *testing.T) {
	const secret = "mcp-secret-token-123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	for _, p := range []string{"/sse?token=" + secret, "/mcp?token=" + secret, "/message?sessionId=123&token=" + secret} {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", p, rec.Code)
			}
		})
	}
}

func TestWithAuth_HealthzNeverGated(t *testing.T) {
	const secret = "mcp-secret-token-123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	cases := []struct {
		name   string
		header string
	}{
		{"no auth", ""},
		{"invalid auth", "Bearer wrong-token"},
		{"valid auth", "Bearer " + secret},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected /healthz to return 200 OK, got %d", rec.Code)
			}
		})
	}
}

func TestValidBearerToken(t *testing.T) {
	const secret = "mysecret"
	tests := []struct {
		header   string
		expected string
		want     bool
	}{
		{"Bearer mysecret", secret, true},
		{"bearer mysecret", secret, true},
		{"BEARER mysecret", secret, true},
		{"Bearer  mysecret ", secret, true},
		{"Bearer wrong", secret, false},
		{"Bearer", secret, false},
		{"Bearer ", secret, false},
		{"", secret, false},
		{"Bearer mysecret", "", false},
		{"Basic mysecret", secret, false},
		{"Bearer mysecret extra", secret, false},
	}

	for _, tt := range tests {
		got := validBearerToken(tt.header, tt.expected)
		if got != tt.want {
			t.Errorf("validBearerToken(%q, %q) = %v, want %v", tt.header, tt.expected, got, tt.want)
		}
	}
}

func TestIsMCPRoute(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/mcp", true},
		{"/mcp/", true},
		{"/sse", true},
		{"/sse/", true},
		{"/message", true},
		{"/message/", true},
		{"/healthz", false},
		{"/healthz/", false},
		{"/", false},
		{"/random", false},
		{"/healthz/../mcp", true},
		{"/mcp/subpath", false},
	}

	for _, tt := range tests {
		got := isMCPRoute(tt.path)
		if got != tt.want {
			t.Errorf("isMCPRoute(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestWithAuth_EndToEndStack(t *testing.T) {
	const secret = "stack-secret-xyz"
	client := apiclient.New("http://ravn-server:8080", secret)
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}

	mcs := mcpsrv.NewMCPServer("ravn", "test", mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)

	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
		mcpsrv.WithDisableLocalhostProtection(true),
	)

	sse := mcpsrv.NewSSEServer(mcs,
		mcpsrv.WithKeepAlive(false),
		mcpsrv.WithSSEDisableLocalhostProtection(true),
		mcpsrv.WithAppendQueryToMessageEndpoint(),
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", messageHandlerWithSessionFallback(sse.MessageHandler()))
	mux.HandleFunc("GET /healthz", mcp.HealthzHandler(client))

	ts := httptest.NewServer(withLogging(nil, withAuth(secret, withModernMCPCompat(mux))))
	defer ts.Close()

	// 1. Unauthenticated POST /mcp -> 401
	initPayload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(initPayload))
	if err != nil {
		t.Fatalf("POST /mcp failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated POST /mcp, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Unauthenticated GET /sse -> 401
	resp, err = http.Get(ts.URL + "/sse")
	if err != nil {
		t.Fatalf("GET /sse failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated GET /sse, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Authenticated POST /mcp with Bearer header -> 200
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(initPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated POST /mcp failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for authenticated POST /mcp, got %d", resp.StatusCode)
	}
	var authInitResp struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authInitResp); err != nil {
		t.Fatalf("failed to decode authenticated initialize response: %v", err)
	}
	resp.Body.Close()
	if authInitResp.Result.ServerInfo.Name != "ravn" {
		t.Fatalf("expected serverInfo.name %q, got %q", "ravn", authInitResp.Result.ServerInfo.Name)
	}

	// 4. Authenticated GET /sse with ?token= query param -> 200
	ctxSSE, cancelSSE := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelSSE()
	reqSSE, _ := http.NewRequestWithContext(ctxSSE, http.MethodGet, ts.URL+"/sse?token="+secret, nil)
	reqSSE.Header.Set("Accept", "text/event-stream")
	respSSE, err := http.DefaultClient.Do(reqSSE)
	if err == nil {
		defer respSSE.Body.Close()
		if respSSE.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for GET /sse?token=..., got %d", respSSE.StatusCode)
		}
	}

	// 5. GET /healthz without auth -> 200 or 503 (HealthzHandler checks upstream, but never 401)
	respHealth, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	if respHealth.StatusCode == http.StatusUnauthorized {
		t.Fatalf("expected /healthz to NOT be 401 Unauthorized, got 401")
	}
	respHealth.Body.Close()
}

type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestWithModernMCPCompat_PayloadLimits(t *testing.T) {
	innerCalled := false
	var capturedBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		if r.Body != nil {
			capturedBody, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := withModernMCPCompat(inner)

	t.Run("normal request within limit has protocolVersion backfilled", func(t *testing.T) {
		innerCalled = false
		capturedBody = nil
		payload := `{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		if !innerCalled {
			t.Fatal("expected inner handler to be called")
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("failed to parse backfilled body: %v", err)
		}
		params := parsed["params"].(map[string]any)
		meta := params["_meta"].(map[string]any)
		if meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" {
			t.Fatalf("expected protocolVersion '2026-07-28', got %v", meta["io.modelcontextprotocol/protocolVersion"])
		}
	})

	t.Run("content length exceeding limit is rejected with 413", func(t *testing.T) {
		innerCalled = false
		t.Setenv("MCP_MAX_PAYLOAD_BYTES", "1024")
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 2048)))
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 Payload Too Large, got %d", rec.Code)
		}
		if innerCalled {
			t.Fatal("inner handler should not be called on 413")
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal 413 response: %v", err)
		}
		if resp["error"] != "payload_too_large" {
			t.Fatalf("expected error 'payload_too_large', got %v", resp["error"])
		}
	})

	t.Run("infinite streaming body bounded by LimitReader and rejected with 413", func(t *testing.T) {
		innerCalled = false
		t.Setenv("MCP_MAX_PAYLOAD_BYTES", "512")
		// Unknown Content-Length (-1) with infinite stream
		req := httptest.NewRequest(http.MethodPost, "/mcp", infiniteReader{})
		req.ContentLength = -1
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 Payload Too Large, got %d", rec.Code)
		}
		if innerCalled {
			t.Fatal("inner handler should not be called on 413")
		}
	})

	t.Run("request exceeding default limit without env var is rejected with 413", func(t *testing.T) {
		innerCalled = false
		// Large payload (> 4MB)
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.ContentLength = int64(defaultMaxMCPPayloadBytes + 1)
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 Payload Too Large, got %d", rec.Code)
		}
		if innerCalled {
			t.Fatal("inner handler should not be called on 413")
		}
	})
}

func TestUpstreamAndAgentDefaults(t *testing.T) {
	if got := envOr("RAVN_API_URL", "http://ravn-server:8080"); got != "http://ravn-server:8080" {
		t.Fatalf("expected default RAVN_API_URL %q, got %q", "http://ravn-server:8080", got)
	}
	if got := envOr("RAVN_AGENT_NAME", "agent"); got != "agent" {
		t.Fatalf("expected default RAVN_AGENT_NAME %q, got %q", "agent", got)
	}

	t.Setenv("RAVN_API_URL", "http://custom-ravn-server:8080")
	if got := envOr("RAVN_API_URL", "http://ravn-server:8080"); got != "http://custom-ravn-server:8080" {
		t.Fatalf("expected overridden RAVN_API_URL %q, got %q", "http://custom-ravn-server:8080", got)
	}
}
