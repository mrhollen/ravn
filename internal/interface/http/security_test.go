package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	t.Parallel()

	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	handler := SecurityHeadersMiddleware()(dummy)

	req := httptest.NewRequest("GET", "http://crowban.test/api/v1/tasks", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want %q", got, "strict-origin-when-cross-origin")
	}
}

func TestCORSMiddleware_DefaultWildcardPreflight(t *testing.T) {
	t.Parallel()

	called := false
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := CORSMiddleware(DefaultCORSOptions())(dummy)

	// OPTIONS preflight request
	req := httptest.NewRequest("OPTIONS", "http://crowban.test/api/v1/tasks", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight status = %d, want 204", rec.Code)
	}
	if called {
		t.Errorf("OPTIONS preflight should not have invoked inner handler")
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("Access-Control-Allow-Methods is empty")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Errorf("Access-Control-Allow-Headers is empty")
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "86400" {
		t.Errorf("Access-Control-Max-Age = %q, want 86400", got)
	}
}

func TestCORSMiddleware_ConfiguredOrigins(t *testing.T) {
	t.Parallel()

	opts := CORSOptions{
		AllowedOrigins: []string{"http://localhost:5173", "https://crowban.lan"},
		AllowedMethods: []string{"GET", "POST", "DELETE"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         3600,
	}

	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := CORSMiddleware(opts)(dummy)

	// Allowed origin
	req := httptest.NewRequest("OPTIONS", "http://crowban.test/api/v1/tasks", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin = %q, want http://localhost:5173", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}

	// Disallowed origin
	reqBad := httptest.NewRequest("OPTIONS", "http://crowban.test/api/v1/tasks", nil)
	reqBad.Header.Set("Origin", "http://malicious.example.com")
	recBad := httptest.NewRecorder()
	handler.ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recBad.Code)
	}
	if got := recBad.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
}

func TestCORSMiddleware_PreflightBypassesAuth(t *testing.T) {
	t.Parallel()

	const secret = "super-secret-token"

	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap dummy with AuthMiddleware (which gates /api/*) and then CORSMiddleware
	authHandler := AuthMiddleware(secret)(dummy)
	handler := CORSMiddleware(DefaultCORSOptions())(authHandler)

	// Preflight OPTIONS to /api/v1/tasks with NO auth header
	req := httptest.NewRequest("OPTIONS", "http://crowban.test/api/v1/tasks", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight status = %d, want 204 (bypasses auth)", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}

	// Direct OPTIONS to AuthMiddleware also bypasses auth
	reqDirect := httptest.NewRequest("OPTIONS", "http://crowban.test/api/v1/tasks", nil)
	recDirect := httptest.NewRecorder()
	authHandler.ServeHTTP(recDirect, reqDirect)

	if recDirect.Code != http.StatusOK {
		t.Fatalf("Direct OPTIONS status on AuthMiddleware = %d, want 200 (bypasses 401)", recDirect.Code)
	}
}

func TestParseCORSOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{"*"}},
		{"*", []string{"*"}},
		{"http://localhost:5173", []string{"http://localhost:5173"}},
		{"http://localhost:5173, https://example.com", []string{"http://localhost:5173", "https://example.com"}},
		{"  http://a.com ,  http://b.com  ", []string{"http://a.com", "http://b.com"}},
	}

	for _, tt := range tests {
		got := ParseCORSOrigins(tt.input)
		if len(got) != len(tt.want) {
			t.Fatalf("ParseCORSOrigins(%q) len = %d, want %d", tt.input, len(got), len(tt.want))
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseCORSOrigins(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
