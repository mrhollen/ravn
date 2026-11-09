package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// identity_test.go: unit coverage for the T6 credential extraction/context
// helpers that let one ravn-mcp process serve many per-agent identities.

func TestBearerToken_Extraction(t *testing.T) {
	tests := []struct {
		name   string
		header string // "" = absent
		query  string // ?token= value; "" = absent
		want   string
	}{
		{name: "standard header", header: "Bearer abc", want: "abc"},
		{name: "lowercase scheme", header: "bearer xyz", want: "xyz"},
		{name: "mixed case scheme", header: "BeArEr mixed", want: "mixed"},
		{name: "padded token trimmed", header: "Bearer   padded  ", want: "padded"},
		{name: "empty bearer falls to query", header: "Bearer ", query: "q1", want: "q1"},
		{name: "malformed header scheme falls to query", header: "Basic Zm9v", query: "q2", want: "q2"},
		{name: "no header, query present", query: "q3", want: "q3"},
		{name: "nothing at all", want: ""},
		{name: "header wins over query", header: "Bearer h1", query: "q4", want: "h1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			if tt.query != "" {
				q := r.URL.Query()
				q.Set("token", tt.query)
				r.URL.RawQuery = q.Encode()
			}
			if got := bearerToken(r); got != tt.want {
				t.Errorf("bearerToken = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCredentialContextFunc(t *testing.T) {
	r := func(bearer string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return req
	}

	t.Run("no credential leaves context untouched", func(t *testing.T) {
		fn := credentialContextFunc("shared")
		ctx := fn(context.Background(), r(""))
		if _, ok := CredentialFromContext(ctx); ok {
			t.Error("credential present, want absent")
		}
		if tok, ok := apiclient.AuthTokenFromContext(ctx); ok {
			t.Errorf("auth token present (%q), want absent", tok)
		}
	})

	t.Run("per-agent token stamps override and non-shared credential", func(t *testing.T) {
		fn := credentialContextFunc("shared")
		ctx := fn(context.Background(), r("agent-1"))
		cred, ok := CredentialFromContext(ctx)
		if !ok || cred.Token != "agent-1" || cred.Shared {
			t.Errorf("credential = %+v (ok=%v), want token=agent-1 shared=false", cred, ok)
		}
		if tok, ok := apiclient.AuthTokenFromContext(ctx); !ok || tok != "agent-1" {
			t.Errorf("auth token override = (%q,%v), want agent-1", tok, ok)
		}
	})

	t.Run("gate token stamps shared credential but still forwards", func(t *testing.T) {
		fn := credentialContextFunc("shared")
		ctx := fn(context.Background(), r("shared"))
		cred, ok := CredentialFromContext(ctx)
		if !ok || cred.Token != "shared" || !cred.Shared {
			t.Errorf("credential = %+v (ok=%v), want token=shared shared=true", cred, ok)
		}
		// Even the shared credential overrides the static token with itself —
		// forwarding stays correct when the client echoes the gate token.
		if tok, _ := apiclient.AuthTokenFromContext(ctx); tok != "shared" {
			t.Errorf("auth token override = %q, want shared", tok)
		}
	})

	t.Run("no gate configured: any bearer is non-shared", func(t *testing.T) {
		fn := credentialContextFunc("")
		ctx := fn(context.Background(), r("agent-9"))
		cred, ok := CredentialFromContext(ctx)
		if !ok || cred.Shared {
			t.Errorf("credential = %+v (ok=%v), want shared=false", cred, ok)
		}
	})

	t.Run("query fallback token is stamped like a header", func(t *testing.T) {
		fn := credentialContextFunc("shared")
		req := httptest.NewRequest(http.MethodPost, "/message?sessionId=1&token=qtok", nil)
		ctx := fn(context.Background(), req)
		cred, ok := CredentialFromContext(ctx)
		if !ok || cred.Token != "qtok" || cred.Shared {
			t.Errorf("credential = %+v (ok=%v), want token=qtok shared=false", cred, ok)
		}
	})

	t.Run("inherited by derived contexts", func(t *testing.T) {
		fn := credentialContextFunc("shared")
		type k struct{}
		base := fn(context.Background(), r("agent-2"))
		child := context.WithValue(base, k{}, 1)
		if cred, ok := CredentialFromContext(child); !ok || cred.Token != "agent-2" {
			t.Errorf("derived credential = %+v (ok=%v), want agent-2", cred, ok)
		}
	})
}

func TestCredentialOptionConstructors(t *testing.T) {
	// Smoke: both option constructors are non-nil and applicable to their
	// transports' server types (compile-time proof they return the right
	// option kind).
	if StreamableCredentialOption("x") == nil || SSECredentialOption("x") == nil {
		t.Fatal("option constructors must not return nil")
	}
}
