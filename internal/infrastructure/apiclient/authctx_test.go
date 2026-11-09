package apiclient

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// authctx_test.go covers the T6 per-request bearer-token override: one shared
// Client forwarding different credentials per request context, with plain
// contexts keeping the static construction-time token byte-for-byte.

// TestAuthTokenFor_Precedence pins the resolution rule at the wire level: a
// ctx override wins over the static token; an absent/empty/whitespace override
// is a no-op; neither set → no Authorization header at all (unchanged default).
func TestAuthTokenFor_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		static     string
		override   string
		wantHeader string
	}{
		{name: "static only", static: "shared", wantHeader: "Bearer shared"},
		{name: "override wins over static", static: "shared", override: "agent-1", wantHeader: "Bearer agent-1"},
		{name: "override without static", static: "", override: "agent-2", wantHeader: "Bearer agent-2"},
		{name: "neither set sends no header", static: "", wantHeader: ""},
		{name: "empty override is a no-op, static kept", static: "shared", override: "", wantHeader: "Bearer shared"},
		{name: "whitespace-only override is a no-op", static: "shared", override: "   ", wantHeader: "Bearer shared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			c, _ := newTestServer(t, tt.static, capturing(t, func(cap *capture, w http.ResponseWriter) {
				got = cap.header.Get("Authorization")
				writeJSONTest(w, http.StatusOK, projectsBody)
			}))
			if _, err := c.ListProjects(WithAuthToken(context.Background(), tt.override)); err != nil {
				t.Fatalf("ListProjects: %v", err)
			}
			if got != tt.wantHeader {
				t.Errorf("Authorization = %q, want %q", got, tt.wantHeader)
			}
		})
	}
}

// TestAuthTokenFor_UploadPathHonorsOverride proves the multipart path (which
// builds its own request outside doPathWithHeaders) honors the same rule.
func TestAuthTokenFor_UploadPathHonorsOverride(t *testing.T) {
	c, _ := newTestServer(t, "shared", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if got := cap.header.Get("Authorization"); got != "Bearer per-agent" {
			t.Errorf("upload Authorization = %q, want Bearer per-agent", got)
		}
		writeJSONTest(w, http.StatusCreated, `{"id":"A1","kind":"log","ref":"/artifacts/A1"}`)
	}))
	ctx := WithAuthToken(context.Background(), "per-agent")
	if _, err := c.UploadArtifact(ctx, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "x.log", strings.NewReader("data"), "log", "", Actor{Name: "ada", Type: "human"}); err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
}

// TestAuthTokenFor_InheritedByDerivedContexts pins context semantics: a value
// stamped on the base context (one MCP request's ctx) is visible to every
// derived child context a handler chain may create.
func TestAuthTokenFor_InheritedByDerivedContexts(t *testing.T) {
	c, _ := newTestServer(t, "shared", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if got := cap.header.Get("Authorization"); got != "Bearer per-agent" {
			t.Errorf("derived-ctx Authorization = %q, want Bearer per-agent", got)
		}
		writeJSONTest(w, http.StatusOK, projectsBody)
	}))
	type childKey struct{}
	base := WithAuthToken(context.Background(), "per-agent")
	child := context.WithValue(base, childKey{}, 1) // realistic derived ctx (per-request values layered on top)
	if _, err := c.ListProjects(child); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
}

func TestAuthTokenContextUnit(t *testing.T) {
	tests := []struct {
		name    string
		stamp   string
		wantTok string
		wantOK  bool
	}{
		{name: "never stamped", wantOK: false},
		{name: "stamped", stamp: "abc", wantTok: "abc", wantOK: true},
		{name: "empty not stamped", stamp: "", wantOK: false},
		{name: "whitespace not stamped", stamp: " \t\n", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.stamp != "" {
				ctx = WithAuthToken(ctx, tt.stamp)
			}
			got, ok := AuthTokenFromContext(ctx)
			if ok != tt.wantOK || got != tt.wantTok {
				t.Errorf("AuthTokenFromContext = (%q, %v), want (%q, %v)", got, ok, tt.wantTok, tt.wantOK)
			}
		})
	}
}
