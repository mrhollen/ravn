package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

func newAuthTestStack(t *testing.T, token string) stack {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-auth-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()

	app := NewApp(Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","db":"ok"}`)) //nolint:errcheck
	})
	mux.HandleFunc("GET /non-api", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	handler := WithAuth(token, mux)
	return stack{Handler: handler, App: app}
}

func jsonBytesReader(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

func TestAuthMiddleware_DisabledWhenTokenEmpty(t *testing.T) {
	stk := newAuthTestStack(t, "")

	// GET /api/v1/projects without auth header succeeds
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects = %d, want 200 (body %s)", code, body)
	}

	// POST /api/v1/projects without auth header succeeds
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/projects", map[string]any{
		"actor": actor("ada"), "name": "Unauth Project",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects = %d, want 201 (body %s)", code, body)
	}
}

func TestAuthMiddleware_RejectsMissingOrInvalidToken(t *testing.T) {
	const secret = "production-secret-token"
	stk := newAuthTestStack(t, secret)

	cases := []struct {
		name       string
		authHeader string
	}{
		{"missing auth header", ""},
		{"wrong token", "Bearer wrong-token"},
		{"empty bearer", "Bearer "},
		{"bearer with whitespace only", "Bearer    "},
		{"basic auth scheme", "Basic dXNlcjpwYXNz"},
		{"custom scheme", "Token production-secret-token"},
		{"bearer prefix missing space", "Bearer" + secret},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// GET /api/v1/projects
			req := httptest.NewRequest("GET", "http://crowban.test/api/v1/projects", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			res := httptest.NewRecorder()
			stk.Handler.ServeHTTP(res, req)

			if res.Code != http.StatusUnauthorized {
				t.Fatalf("GET status = %d, want 401 (body %s)", res.Code, res.Body.String())
			}
			if ct := res.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			errBody := mustDecode[errResp](t, res.Body.Bytes())
			if errBody.Error != "unauthorized" {
				t.Errorf("error = %q, want unauthorized", errBody.Error)
			}
			if errBody.Message != "missing or invalid auth token" {
				t.Errorf("message = %q, want 'missing or invalid auth token'", errBody.Message)
			}

			// POST /api/v1/projects (mutation attempt)
			postReq := httptest.NewRequest("POST", "http://crowban.test/api/v1/projects",
				jsonBytesReader(t, map[string]any{
					"actor": actor("ada"), "name": "Should Not Exist",
				}))
			if tc.authHeader != "" {
				postReq.Header.Set("Authorization", tc.authHeader)
			}
			postRes := httptest.NewRecorder()
			stk.Handler.ServeHTTP(postRes, postReq)

			if postRes.Code != http.StatusUnauthorized {
				t.Fatalf("POST status = %d, want 401 (body %s)", postRes.Code, postRes.Body.String())
			}
		})
	}

	// Verify the project was NOT created (mutation did not occur)
	validReq := httptest.NewRequest("GET", "http://crowban.test/api/v1/projects", nil)
	validReq.Header.Set("Authorization", "Bearer "+secret)
	validRes := httptest.NewRecorder()
	stk.Handler.ServeHTTP(validRes, validReq)
	if validRes.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects = %d, want 200", validRes.Code)
	}
	var list struct {
		Projects []projectDTO `json:"projects"`
		Total    int          `json:"total"`
	}
	list = mustDecode[struct {
		Projects []projectDTO `json:"projects"`
		Total    int          `json:"total"`
	}](t, validRes.Body.Bytes())
	if list.Total != 0 || len(list.Projects) != 0 {
		t.Errorf("expected 0 projects, got %d (mutation occurred despite 401!)", list.Total)
	}
}

func TestAuthMiddleware_AcceptsValidBearerToken(t *testing.T) {
	const secret = "correct-token-42"
	stk := newAuthTestStack(t, secret)

	validHeaders := []string{
		"Bearer " + secret,
		"bearer " + secret,
		"Bearer   " + secret,
		"  Bearer " + secret + "  ",
	}

	for i, h := range validHeaders {
		t.Run("header: "+h, func(t *testing.T) {
			req := httptest.NewRequest("POST", "http://crowban.test/api/v1/projects",
				jsonBytesReader(t, map[string]any{
					"actor": actor("ada"), "name": "Valid Auth Project " + string(rune('A'+i)),
				}))
			req.Header.Set("Authorization", h)
			res := httptest.NewRecorder()
			stk.Handler.ServeHTTP(res, req)

			if res.Code != http.StatusCreated {
				t.Fatalf("POST status = %d, want 201 (body %s)", res.Code, res.Body.String())
			}
		})
	}
}

func TestAuthMiddleware_HealthzAndNonAPINeverGated(t *testing.T) {
	const secret = "production-secret-token"
	stk := newAuthTestStack(t, secret)

	for _, tc := range []struct {
		name       string
		path       string
		authHeader string
	}{
		{"healthz no auth", "/healthz", ""},
		{"healthz invalid auth", "/healthz", "Bearer invalid-token"},
		{"healthz valid auth", "/healthz", "Bearer " + secret},
		{"non-api no auth", "/non-api", ""},
		{"non-api invalid auth", "/non-api", "Bearer invalid-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://crowban.test"+tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			res := httptest.NewRecorder()
			stk.Handler.ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200 (never gated)", tc.path, res.Code)
			}
		})
	}
}

func TestValidBearerToken(t *testing.T) {
	const secret = "s3cr3t"
	tests := []struct {
		header   string
		expected string
		want     bool
	}{
		{"Bearer s3cr3t", secret, true},
		{"bearer s3cr3t", secret, true},
		{"BEARER s3cr3t", secret, true},
		{"Bearer  s3cr3t ", secret, true},
		{"Bearer wrong", secret, false},
		{"Bearer", secret, false},
		{"Bearer ", secret, false},
		{"", secret, false},
		{"Bearer s3cr3t", "", false},
		{"Basic s3cr3t", secret, false},
		{"Bearer s3cr3t extra", secret, false},
	}

	for _, tt := range tests {
		got := validBearerToken(tt.header, tt.expected)
		if got != tt.want {
			t.Errorf("validBearerToken(%q, %q) = %v, want %v", tt.header, tt.expected, got, tt.want)
		}
	}
}

func TestIsAPIRoute(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/v1/projects", true},
		{"/api/v1/tasks/123/claim", true},
		{"/api", true},
		{"/api/", true},
		{"/api/something", true},
		{"/healthz", false},
		{"/healthz/", false},
		{"/", false},
		{"/assets/app.js", false},
		{"/projects/123", false},
		{"/apinotapi", false},
		{"/api/../healthz", false},
		{"/healthz/../api/v1/projects", true},
	}

	for _, tt := range tests {
		got := isAPIRoute(tt.path)
		if got != tt.want {
			t.Errorf("isAPIRoute(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestAuthMiddleware_QueryParamToken(t *testing.T) {
	const secret = "query-token-123"
	stk := newAuthTestStack(t, secret)

	// Valid token in query param succeeds without Authorization header
	req := httptest.NewRequest("GET", "http://crowban.test/api/v1/projects?token="+secret, nil)
	res := httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects?token=... = %d, want 200", res.Code)
	}

	// Invalid token in query param fails with 401
	req = httptest.NewRequest("GET", "http://crowban.test/api/v1/projects?token=wrong", nil)
	res = httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/projects?token=wrong = %d, want 401", res.Code)
	}
}
