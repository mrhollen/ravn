package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
	"github.com/mrhollen/crowban/internal/mcp"
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
	client := apiclient.New("http://localhost:8080", "")
	defaultActor := apiclient.Actor{Type: "agent", Name: "test-agent"}

	mcs := mcpsrv.NewMCPServer("crowban", "test", mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)
	mcp.RegisterPrompts(mcs, client, defaultActor)

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
	resp.Body.Close()

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
}
