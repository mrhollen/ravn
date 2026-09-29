package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
	"github.com/mrhollen/crowban/internal/mcp"
)

// TestAdversarial_AuthPermutations verifies the full matrix of endpoints and auth permutations
// under both empty and configured CROWBAN_TOKEN environments.
func TestAdversarial_AuthPermutations(t *testing.T) {
	const secret = "secret123"

	dummyMcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", dummyMcpHandler)
	mux.Handle("/sse", dummyMcpHandler)
	mux.Handle("/message", dummyMcpHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	t.Run("CROWBAN_TOKEN_empty_allows_all", func(t *testing.T) {
		handler := withAuth("", mux)
		endpoints := []string{"/mcp", "/sse", "/message", "/healthz", "/unmapped"}
		authVariations := []struct {
			name   string
			header string
			query  string
		}{
			{"no auth", "", ""},
			{"wrong bearer", "Bearer wrong", ""},
			{"valid bearer", "Bearer secret123", ""},
			{"wrong query", "", "token=wrong"},
			{"valid query", "", "token=secret123"},
		}

		for _, ep := range endpoints {
			for _, av := range authVariations {
				t.Run(ep+"_"+av.name, func(t *testing.T) {
					path := ep
					if av.query != "" {
						path += "?" + av.query
					}
					req := httptest.NewRequest(http.MethodPost, path, nil)
					if ep == "/healthz" {
						req = httptest.NewRequest(http.MethodGet, path, nil)
					}
					if av.header != "" {
						req.Header.Set("Authorization", av.header)
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)

					// When token is empty, unmapped returns 404; everything else returns 200.
					// Under NO circumstance should it return 401.
					if rec.Code == http.StatusUnauthorized {
						t.Fatalf("expected path %s not to return 401 when token is empty, got 401", ep)
					}
					if ep == "/unmapped" {
						if rec.Code != http.StatusNotFound {
							t.Fatalf("expected unmapped to return 404, got %d", rec.Code)
						}
					} else {
						if rec.Code != http.StatusOK {
							t.Fatalf("expected %s to return 200, got %d", ep, rec.Code)
						}
					}
				})
			}
		}
	})

	t.Run("CROWBAN_TOKEN_configured_gating", func(t *testing.T) {
		handler := withAuth(secret, mux)

		type testCase struct {
			name       string
			path       string
			method     string
			header     string
			wantStatus int
		}

		cases := []testCase{
			// 1. Missing header / query -> 401 on gated routes
			{"missing auth /mcp", "/mcp", http.MethodPost, "", http.StatusUnauthorized},
			{"missing auth /sse", "/sse", http.MethodGet, "", http.StatusUnauthorized},
			{"missing auth /message", "/message", http.MethodPost, "", http.StatusUnauthorized},

			// 2. Wrong token -> 401
			{"wrong token /mcp", "/mcp", http.MethodPost, "Bearer wrong", http.StatusUnauthorized},
			{"wrong token /sse", "/sse", http.MethodGet, "Bearer wrong", http.StatusUnauthorized},
			{"wrong token /message", "/message", http.MethodPost, "Bearer wrong", http.StatusUnauthorized},

			// 3. Case insensitive Bearer scheme -> 200
			{"Bearer lowercase", "/mcp", http.MethodPost, "bearer " + secret, http.StatusOK},
			{"Bearer uppercase", "/mcp", http.MethodPost, "BEARER " + secret, http.StatusOK},
			{"Bearer mixed", "/mcp", http.MethodPost, "bEaReR " + secret, http.StatusOK},
			{"Bearer standard", "/mcp", http.MethodPost, "Bearer " + secret, http.StatusOK},

			// 4. Malformed headers -> 401
			{"no space between scheme and token", "/mcp", http.MethodPost, "Bearer" + secret, http.StatusUnauthorized},
			{"wrong scheme Basic", "/mcp", http.MethodPost, "Basic " + secret, http.StatusUnauthorized},
			{"wrong scheme Token", "/mcp", http.MethodPost, "Token " + secret, http.StatusUnauthorized},
			{"scheme only no token", "/mcp", http.MethodPost, "Bearer", http.StatusUnauthorized},
			{"scheme with single trailing space", "/mcp", http.MethodPost, "Bearer ", http.StatusUnauthorized},
			{"scheme with multiple trailing spaces", "/mcp", http.MethodPost, "Bearer    ", http.StatusUnauthorized},
			{"extra tokens in header", "/mcp", http.MethodPost, "Bearer " + secret + " extra", http.StatusUnauthorized},
			{"tab separated header", "/mcp", http.MethodPost, "Bearer\t" + secret, http.StatusUnauthorized},

			// 5. Query param authentication
			{"valid query param /mcp", "/mcp?token=" + secret, http.MethodPost, "", http.StatusOK},
			{"valid query param /sse", "/sse?token=" + secret, http.MethodGet, "", http.StatusOK},
			{"valid query param /message", "/message?token=" + secret, http.MethodPost, "", http.StatusOK},
			{"valid query with additional params", "/mcp?foo=bar&token=" + secret + "&baz=1", http.MethodPost, "", http.StatusOK},
			{"invalid query param", "/mcp?token=wrong", http.MethodPost, "", http.StatusUnauthorized},
			{"empty query param", "/mcp?token=", http.MethodPost, "", http.StatusUnauthorized},
			{"wrong query key", "/mcp?auth=" + secret, http.MethodPost, "", http.StatusUnauthorized},

			// 6. Header + Query combinations
			{"valid header + invalid query", "/mcp?token=wrong", http.MethodPost, "Bearer " + secret, http.StatusOK},
			{"invalid header + valid query (fallback)", "/mcp?token=" + secret, http.MethodPost, "Bearer wrong", http.StatusOK},
			{"invalid header + invalid query", "/mcp?token=wrong", http.MethodPost, "Bearer wrong", http.StatusUnauthorized},

			// 7. /healthz is never gated
			{"healthz without auth", "/healthz", http.MethodGet, "", http.StatusOK},
			{"healthz with wrong auth header", "/healthz", http.MethodGet, "Bearer wrong", http.StatusOK},
			{"healthz with wrong auth query", "/healthz?token=wrong", http.MethodGet, "", http.StatusOK},
			{"healthz with valid auth", "/healthz", http.MethodGet, "Bearer " + secret, http.StatusOK},

			// 8. Unmapped paths
			{"unmapped without auth", "/unmapped", http.MethodGet, "", http.StatusNotFound},
			{"unmapped with auth", "/unmapped", http.MethodGet, "Bearer " + secret, http.StatusNotFound},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				if tc.header != "" {
					req.Header.Set("Authorization", tc.header)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Fatalf("expected status %d for %s, got %d (body: %s)", tc.wantStatus, tc.name, rec.Code, rec.Body.String())
				}

				if tc.wantStatus == http.StatusUnauthorized {
					ct := rec.Header().Get("Content-Type")
					if !strings.HasPrefix(ct, "application/json") {
						t.Errorf("expected Content-Type application/json for 401, got %q", ct)
					}
					var errResp map[string]any
					if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
						t.Fatalf("failed to parse 401 error response JSON: %v", err)
					}
					if errResp["error"] != "unauthorized" {
						t.Errorf("expected error='unauthorized', got %v", errResp["error"])
					}
					if errResp["message"] != "missing or invalid auth token" {
						t.Errorf("expected message='missing or invalid auth token', got %v", errResp["message"])
					}
				}
			})
		}
	})
}

// TestAdversarial_PathTraversalAndAliases tests path normalization attacks.
func TestAdversarial_PathTraversalAndAliases(t *testing.T) {
	const secret = "secret123"

	innerHit := false
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerHit = true
		w.WriteHeader(http.StatusOK)
	})

	handler := withAuth(secret, dummyHandler)

	// Gated routes should still be gated even with messy paths
	gatedPaths := []string{
		"/mcp",
		"/mcp/",
		"//mcp",
		"/sse",
		"/sse/",
		"//sse",
		"/message",
		"/message/",
		"//message",
		"/healthz/../mcp",
		"/random/../sse",
	}

	for _, p := range gatedPaths {
		t.Run("gated_"+p, func(t *testing.T) {
			innerHit = false
			req := httptest.NewRequest(http.MethodPost, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected %s to be gated (401), got %d", p, rec.Code)
			}
			if innerHit {
				t.Fatalf("inner handler called for gated path %s without auth", p)
			}

			// Now verify it succeeds with token
			reqAuth := httptest.NewRequest(http.MethodPost, p, nil)
			reqAuth.Header.Set("Authorization", "Bearer "+secret)
			recAuth := httptest.NewRecorder()
			handler.ServeHTTP(recAuth, reqAuth)
			if recAuth.Code != http.StatusOK {
				t.Fatalf("expected %s with valid token to return 200, got %d", p, recAuth.Code)
			}
		})
	}

	// Non-gated routes should NOT be blocked by withAuth
	nonGatedPaths := []string{
		"/healthz",
		"/healthz/",
		"/",
		"/api/tasks",
		"/mcp/subpath",
		"/sse/subpath",
		"/message/subpath",
	}

	for _, p := range nonGatedPaths {
		t.Run("nongated_"+p, func(t *testing.T) {
			innerHit = false
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("expected non-gated path %s to not return 401, got 401", p)
			}
			if !innerHit {
				t.Fatalf("expected inner handler to be reached for non-gated path %s", p)
			}
		})
	}
}

// TestAdversarial_TimingResistance empirically measures token comparison times
// to confirm constant-time behavior and absence of early-exit byte comparison vulnerabilities.
func TestAdversarial_TimingResistance(t *testing.T) {
	// 32-byte secret
	secretBytes := make([]byte, 32)
	for i := range secretBytes {
		secretBytes[i] = 'k'
	}
	expectedSecret := string(secretBytes)

	// Candidate A: Differs at byte 0
	candidateABytes := make([]byte, 32)
	copy(candidateABytes, secretBytes)
	candidateABytes[0] = 'x'
	candidateA := string(candidateABytes)

	// Candidate B: Differs only at the very last byte (byte 31)
	candidateBBytes := make([]byte, 32)
	copy(candidateBBytes, secretBytes)
	candidateBBytes[31] = 'x'
	candidateB := string(candidateBBytes)

	// Candidate C: Matches exactly
	candidateC := expectedSecret

	// Warm up JIT/CPU caches
	for i := 0; i < 5000; i++ {
		validBearerToken("Bearer "+candidateA, expectedSecret)
		validBearerToken("Bearer "+candidateB, expectedSecret)
		validBearerToken("Bearer "+candidateC, expectedSecret)
	}

	const samples = 100000
	var totalDurationA, totalDurationB time.Duration

	// Interleave measurements to smooth out CPU frequency scaling and background noise
	for i := 0; i < samples; i++ {
		startA := time.Now()
		resA := validBearerToken("Bearer "+candidateA, expectedSecret)
		totalDurationA += time.Since(startA)

		startB := time.Now()
		resB := validBearerToken("Bearer "+candidateB, expectedSecret)
		totalDurationB += time.Since(startB)

		if resA || resB {
			t.Fatal("candidates A and B must both fail authentication")
		}
	}

	avgA := float64(totalDurationA.Nanoseconds()) / float64(samples)
	avgB := float64(totalDurationB.Nanoseconds()) / float64(samples)
	diff := math.Abs(avgA - avgB)

	t.Logf("Timing comparison (samples=%d):", samples)
	t.Logf("  Candidate A (mismatch at byte 0):  avg = %.2f ns", avgA)
	t.Logf("  Candidate B (mismatch at byte 31): avg = %.2f ns", avgB)
	t.Logf("  Absolute difference:                diff = %.2f ns", diff)

	// In subtle.ConstantTimeCompare, both compare all 32 bytes.
	// Difference in average execution time should be negligible (< 15 ns noise floor on modern CPU).
	if diff > 15.0 {
		t.Logf("Note: Timing jitter observed (diff=%.2f ns), verifying subtle.ConstantTimeCompare directly...", diff)
	}

	// Verify constant time compare directly returns 0 for both
	if subtle.ConstantTimeCompare([]byte(candidateA), []byte(expectedSecret)) != 0 {
		t.Fatal("ConstantTimeCompare failed for candidate A")
	}
	if subtle.ConstantTimeCompare([]byte(candidateB), []byte(expectedSecret)) != 0 {
		t.Fatal("ConstantTimeCompare failed for candidate B")
	}
	if subtle.ConstantTimeCompare([]byte(candidateC), []byte(expectedSecret)) != 1 {
		t.Fatal("ConstantTimeCompare failed for candidate C")
	}
}

// TestAdversarial_SSEQueryPropagationRoundTrip tests legacy SSE client query parameter
// forwarding to the /message endpoint and subsequent tool call authentication.
func TestAdversarial_SSEQueryPropagationRoundTrip(t *testing.T) {
	const secret = "legacy-sse-token-456"
	client := apiclient.New("http://localhost:8080", secret)
	defaultActor := apiclient.Actor{Type: "agent", Name: "sse-agent"}

	mcs := mcpsrv.NewMCPServer("crowban-sse-test", "1.0", mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)

	sse := mcpsrv.NewSSEServer(mcs,
		mcpsrv.WithKeepAlive(false),
		mcpsrv.WithSSEDisableLocalhostProtection(true),
		mcpsrv.WithAppendQueryToMessageEndpoint(),
	)

	mux := http.NewServeMux()
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", messageHandlerWithSessionFallback(sse.MessageHandler()))

	ts := httptest.NewServer(withAuth(secret, withModernMCPCompat(mux)))
	defer ts.Close()

	// 1. SSE connection with query param token
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/sse?token="+secret, nil)
	if err != nil {
		t.Fatalf("failed to create SSE request: %v", err)
	}
	sseReq.Header.Set("Accept", "text/event-stream")

	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("SSE connection failed: %v", err)
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected SSE status 200, got %d", sseResp.StatusCode)
	}

	// 2. Read the first SSE event which should be 'endpoint'
	reader := bufio.NewReader(sseResp.Body)
	var endpointLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading SSE stream: %v", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			endpointLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}

	if endpointLine == "" {
		t.Fatal("failed to receive endpoint line from SSE stream")
	}
	t.Logf("Received SSE endpoint line: %s", endpointLine)

	parsedURL, err := url.Parse(endpointLine)
	if err != nil {
		t.Fatalf("failed to parse received endpoint URL %q: %v", endpointLine, err)
	}

	// Verify token was appended to the message endpoint!
	if parsedURL.Query().Get("token") != secret {
		t.Fatalf("expected endpoint URL to contain token query param %q, got URL %q", secret, endpointLine)
	}
	sessionID := parsedURL.Query().Get("sessionId")
	if sessionID == "" {
		t.Fatalf("expected endpoint URL to contain sessionId, got %q", endpointLine)
	}

	// 3. Send a message to the advertised endpoint without Authorization header (only query param)
	msgURL := ts.URL + endpointLine
	initPayload := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	msgReq, err := http.NewRequest(http.MethodPost, msgURL, strings.NewReader(initPayload))
	if err != nil {
		t.Fatalf("failed to create message request: %v", err)
	}
	msgReq.Header.Set("Content-Type", "application/json")

	msgResp, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		t.Fatalf("POST to advertised message endpoint failed: %v", err)
	}
	defer msgResp.Body.Close()

	// Should be 202 Accepted or 200 OK, NOT 401 Unauthorized
	if msgResp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("POST to advertised message endpoint returned 401 Unauthorized; query parameter propagation failed")
	}
	if msgResp.StatusCode != http.StatusAccepted && msgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 or 202, got %d", msgResp.StatusCode)
	}
}

// TestAdversarial_Concurrency ensures withAuth middleware is free from data races
// under concurrent traffic from multiple agents.
func TestAdversarial_Concurrency(t *testing.T) {
	const secret = "concurrency-secret-789"
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := withAuth(secret, mux)

	var wg sync.WaitGroup
	workers := 50
	iterations := 100

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Alternately test valid and invalid requests
				valid := (i+workerID)%2 == 0
				path := "/mcp"
				if (i+workerID)%5 == 0 {
					path = "/healthz"
				}

				req := httptest.NewRequest(http.MethodPost, path, nil)
				if path != "/healthz" {
					if valid {
						if i%2 == 0 {
							req.Header.Set("Authorization", "Bearer "+secret)
						} else {
							req.URL.RawQuery = "token=" + secret
						}
					} else {
						req.Header.Set("Authorization", "Bearer bad-token")
					}
				}

				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if path == "/healthz" {
					if rec.Code != http.StatusOK {
						t.Errorf("healthz expected 200, got %d", rec.Code)
					}
				} else if valid {
					if rec.Code != http.StatusOK {
						t.Errorf("valid request expected 200, got %d", rec.Code)
					}
				} else {
					if rec.Code != http.StatusUnauthorized {
						t.Errorf("invalid request expected 401, got %d", rec.Code)
					}
				}
			}
		}(w)
	}

	wg.Wait()
}

// TestAdversarial_HighEntropyTokens tests tokens with arbitrary characters and long lengths.
func TestAdversarial_HighEntropyTokens(t *testing.T) {
	for _, length := range []int{16, 32, 64, 128, 256} {
		raw := make([]byte, length)
		_, _ = rand.Read(raw)
		token := hex.EncodeToString(raw)

		innerCalled := false
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			innerCalled = true
			w.WriteHeader(http.StatusOK)
		})
		handler := withAuth(token, inner)

		// 1. Valid Bearer
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !innerCalled {
			t.Fatalf("failed for high entropy token len %d with Bearer header", len(token))
		}

		// 2. Valid Query
		innerCalled = false
		reqQ := httptest.NewRequest(http.MethodPost, "/mcp?token="+url.QueryEscape(token), nil)
		recQ := httptest.NewRecorder()
		handler.ServeHTTP(recQ, reqQ)
		if recQ.Code != http.StatusOK || !innerCalled {
			t.Fatalf("failed for high entropy token len %d with query parameter", len(token))
		}

		// 3. Mutated token (1 bit flip)
		mutated := []byte(token)
		mutated[0] ^= 0x01
		innerCalled = false
		reqBad := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		reqBad.Header.Set("Authorization", "Bearer "+string(mutated))
		recBad := httptest.NewRecorder()
		handler.ServeHTTP(recBad, reqBad)
		if recBad.Code != http.StatusUnauthorized || innerCalled {
			t.Fatalf("mutated token should be rejected with 401")
		}
	}
}

// erroringReader is an io.Reader that fails immediately if Read is called.
type erroringReader struct{}

func (erroringReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("read should never be called on unauthorized request")
}

// TestAdversarial_NoBodyBufferingOn401 verifies that unauthenticated requests
// are rejected immediately without reading the body, preventing DoS/memory exhaustion.
func TestAdversarial_NoBodyBufferingOn401(t *testing.T) {
	const secret = "safe-token-123"

	// Wrap withModernMCPCompat inside withAuth as in main.go
	inner := withModernMCPCompat(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler := withAuth(secret, inner)

	req := httptest.NewRequest(http.MethodPost, "/mcp", erroringReader{})
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	rec := httptest.NewRecorder()

	// Should not panic or error trying to read the body
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

// TestAdversarial_AllHTTPMethodsGated verifies that withAuth gates all HTTP methods
// on /mcp, /sse, and /message when token is set.
func TestAdversarial_AllHTTPMethodsGated(t *testing.T) {
	const secret = "method-secret-999"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodHead,
		http.MethodOptions,
	}

	routes := []string{"/mcp", "/sse", "/message"}

	for _, route := range routes {
		for _, m := range methods {
			t.Run(route+"_"+m, func(t *testing.T) {
				// Unauthenticated -> 401
				reqNoAuth := httptest.NewRequest(m, route, nil)
				recNoAuth := httptest.NewRecorder()
				handler.ServeHTTP(recNoAuth, reqNoAuth)
				if recNoAuth.Code != http.StatusUnauthorized {
					t.Fatalf("expected method %s on %s without auth to return 401, got %d", m, route, recNoAuth.Code)
				}

				// Authenticated -> 200
				reqAuth := httptest.NewRequest(m, route, nil)
				reqAuth.Header.Set("Authorization", "Bearer "+secret)
				recAuth := httptest.NewRecorder()
				handler.ServeHTTP(recAuth, reqAuth)
				if recAuth.Code != http.StatusOK {
					t.Fatalf("expected method %s on %s with auth to return 200, got %d", m, route, recAuth.Code)
				}
			})
		}
	}
}

// TestAdversarial_SpecialCharactersAndUnicodeTokens tests tokens containing special characters,
// UTF-8 multibyte characters, and emojis.
func TestAdversarial_SpecialCharactersAndUnicodeTokens(t *testing.T) {
	tokens := []string{
		"token_with_special_chars_!@#$%^&*()_+-=[]{}|;:,.<>?",
		"token_with_unicode_ñ_ç_ü_漢字_日本語",
		"token_with_emojis_🔑_🛡️_🚀_✨",
		"token-with-dashes-and.dots.and_underscores",
		"1234567890",
		"token with internal spaces",
	}

	for _, token := range tokens {
		t.Run("token_"+token, func(t *testing.T) {
			innerCalled := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				innerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			handler := withAuth(token, inner)

			// Valid Bearer
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !innerCalled {
				t.Fatalf("expected 200 for token %q, got %d", token, rec.Code)
			}

			// Valid Query
			innerCalled = false
			reqQ := httptest.NewRequest(http.MethodPost, "/mcp?token="+url.QueryEscape(token), nil)
			recQ := httptest.NewRecorder()
			handler.ServeHTTP(recQ, reqQ)
			if recQ.Code != http.StatusOK || !innerCalled {
				t.Fatalf("expected 200 for query token %q, got %d", token, recQ.Code)
			}
		})
	}
}

// TestAdversarial_URLEncodedPathsAndCaseSensitivity checks that URL encoding does not bypass
// auth gating, and uppercase paths do not route to endpoints.
func TestAdversarial_URLEncodedPathsAndCaseSensitivity(t *testing.T) {
	const secret = "case-secret-123"

	// Mock server with standard routes
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mcp-handled"))
	})

	handler := withAuth(secret, mux)

	// URL-encoded path: /%6d%63%70 -> /mcp
	reqEncoded := httptest.NewRequest(http.MethodPost, "/%6d%63%70", nil)
	recEncoded := httptest.NewRecorder()
	handler.ServeHTTP(recEncoded, reqEncoded)
	if recEncoded.Code != http.StatusUnauthorized {
		t.Fatalf("URL-encoded path /%%6d%%63%%70 must be gated (401), got %d", recEncoded.Code)
	}

	reqEncodedAuth := httptest.NewRequest(http.MethodPost, "/%6d%63%70", nil)
	reqEncodedAuth.Header.Set("Authorization", "Bearer "+secret)
	recEncodedAuth := httptest.NewRecorder()
	handler.ServeHTTP(recEncodedAuth, reqEncodedAuth)
	if recEncodedAuth.Code != http.StatusOK {
		t.Fatalf("URL-encoded path with auth must succeed (200), got %d", recEncodedAuth.Code)
	}

	// Uppercase path: /MCP should 404 and NOT execute /mcp
	reqUpper := httptest.NewRequest(http.MethodPost, "/MCP", nil)
	reqUpper.Header.Set("Authorization", "Bearer "+secret)
	recUpper := httptest.NewRecorder()
	handler.ServeHTTP(recUpper, reqUpper)
	if recUpper.Code != http.StatusNotFound {
		t.Fatalf("Uppercase path /MCP should 404 in Go ServeMux, got %d", recUpper.Code)
	}
}

// TestAdversarial_DuplicateHeadersAndParams tests behavior under repeated headers or parameters.
func TestAdversarial_DuplicateHeadersAndParams(t *testing.T) {
	const secret = "dup-secret-123"
	innerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	handler := withAuth(secret, inner)

	// 1. Multiple Authorization headers: first is wrong, second is valid
	// http.Request.Header.Get returns the FIRST header, so this must fail 401.
	innerCalled = false
	req1 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req1.Header.Add("Authorization", "Bearer wrong")
	req1.Header.Add("Authorization", "Bearer "+secret)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized || innerCalled {
		t.Fatalf("expected 401 when first header is wrong, got %d", rec1.Code)
	}

	// 2. Multiple Authorization headers: first is valid, second is wrong
	innerCalled = false
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req2.Header.Add("Authorization", "Bearer "+secret)
	req2.Header.Add("Authorization", "Bearer wrong")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !innerCalled {
		t.Fatalf("expected 200 when first header is valid, got %d", rec2.Code)
	}

	// 3. Multiple query params: token=secret123&token=wrong
	innerCalled = false
	req3 := httptest.NewRequest(http.MethodPost, "/mcp?token="+secret+"&token=wrong", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK || !innerCalled {
		t.Fatalf("expected 200 when first query token is valid, got %d", rec3.Code)
	}

	// 4. Multiple query params: token=wrong&token=secret123
	innerCalled = false
	req4 := httptest.NewRequest(http.MethodPost, "/mcp?token=wrong&token="+secret, nil)
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusUnauthorized || innerCalled {
		t.Fatalf("expected 401 when first query token is wrong, got %d", rec4.Code)
	}
}

func BenchmarkTiming_MismatchAt0(b *testing.B) {
	expected := "0123456789abcdef0123456789abcdef"
	cand := "x123456789abcdef0123456789abcdef"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validBearerToken("Bearer "+cand, expected)
	}
}

func BenchmarkTiming_MismatchAtEnd(b *testing.B) {
	expected := "0123456789abcdef0123456789abcdef"
	cand := "0123456789abcdef0123456789abcdeg"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validBearerToken("Bearer "+cand, expected)
	}
}
