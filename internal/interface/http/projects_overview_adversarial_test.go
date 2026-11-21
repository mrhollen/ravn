package httpapi

import (
	"net/http"
	"testing"
)

func TestAdversarial_ProjectsOverview_InputsAuthAndMethods(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "AdversarialOverview")
	t1 := makeTask(t, stk, p.ID, "doing task", nil)
	patchTask(t, stk, t1.ID, 1, map[string]any{"status": "doing"})

	t.Run("hostile query params return 400 invalid_input", func(t *testing.T) {
		cases := []struct {
			name  string
			query string
		}{
			{"negative stalled_minutes", "?stalled_minutes=-1"},
			{"large negative stalled_minutes", "?stalled_minutes=-999999"},
			{"alpha stalled_minutes", "?stalled_minutes=abc"},
			{"float stalled_minutes", "?stalled_minutes=1.5"},
			{"NaN stalled_minutes", "?stalled_minutes=NaN"},
			{"whitespace stalled_minutes", "?stalled_minutes=%205%20"},
			{"int64 overflow stalled_minutes", "?stalled_minutes=999999999999999999999999999"},
			{"sql injection stalled_minutes", "?stalled_minutes=60%20OR%201=1"},
			{"mine=assigned unsupported for projects", "?mine=assigned"},
			{"mine=activity unsupported for projects", "?mine=activity"},
			{"mine=bogus", "?mine=bogus"},
			{"mine multiple distinct values", "?mine=created&mine=assigned"},
			{"mine=created without actor in auth-off mode", "?mine=created"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/overview"+tc.query, nil)
				if code != http.StatusBadRequest {
					t.Fatalf("GET /projects/overview%s = %d, want 400 (body %s)", tc.query, code, string(body))
				}
				e := mustDecode[errResp](t, body)
				if e.Error != ErrCodeInvalidInput {
					t.Errorf("GET /projects/overview%s error = %q, want %q", tc.query, e.Error, ErrCodeInvalidInput)
				}
			})
		}
	})

	t.Run("huge int64 stalled_minutes clamps safely without overflow wrap", func(t *testing.T) {
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/overview?stalled_minutes=999999999999", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /projects/overview?stalled_minutes=999999999999 = %d, want 200 (body %s)", code, string(body))
		}
		env := mustDecode[struct {
			Projects []projectOverviewRowDTO `json:"projects"`
		}](t, body)
		if len(env.Projects) != 1 || env.Projects[0].Health.Stalled != 0 {
			t.Errorf("huge stalled_minutes wrapped or miscounted: %+v", env.Projects)
		}
	})

	t.Run("non-GET HTTP methods on projects/overview are rejected cleanly", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
			code, _ := doReq(t, stk.Handler, method, "/api/v1/projects/overview", map[string]any{"actor": actor("ada")})
			if code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /api/v1/projects/overview = %d, want 405 Method Not Allowed", method, code)
			}
		}
		// PUT matches PUT /projects/{id} with id="overview", which boundary validation rejects as 400 invalid_input.
		code, body := doReq(t, stk.Handler, http.MethodPut, "/api/v1/projects/overview", map[string]any{"actor": actor("ada"), "name": "x"})
		if code != http.StatusBadRequest {
			t.Fatalf("PUT /api/v1/projects/overview = %d, want 400 invalid_input (body %s)", code, string(body))
		}
	})

	t.Run("token auth rejects missing, invalid, and query-param tokens", func(t *testing.T) {
		const secret = "overview-secret"
		gated := AuthGate(AuthModeToken, secret, nil)(stk.Handler)

		for _, tc := range []struct {
			name string
			path string
			auth string
		}{
			{"missing bearer", "/api/v1/projects/overview", ""},
			{"wrong bearer", "/api/v1/projects/overview", "Bearer nope"},
			{"query token rejected on non-SSE route", "/api/v1/projects/overview?token=" + secret, ""},
		} {
			code, _ := authReq(t, gated, "GET", tc.path, nil, tc.auth)
			if code != http.StatusUnauthorized {
				t.Fatalf("%s = %d, want 401", tc.name, code)
			}
		}

		code, _ := authReq(t, gated, "GET", "/api/v1/projects/overview", nil, secret)
		if code != http.StatusOK {
			t.Fatalf("valid bearer = %d, want 200", code)
		}
	})
}
