package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegression_MigratedSchema_SharedTokenAuthUnchanged pins AC3 of the
// multi-user T1 ticket: with migration 0011 applied, a deployment using the
// legacy shared RAVN_TOKEN behaves byte-for-byte as before — the correct
// Bearer token is admitted (reads AND writes), missing or wrong tokens get the
// standard 401 "unauthorized" envelope, and name-keyed actors keep flowing
// through the API unchanged. The auth middleware never touches the database
// (it compares the Bearer header against the configured secret in constant
// time), so this test proves that the additive schema did not disturb the
// single-tenant contract end to end: newStack opens its DB via sqlite.Open,
// which applies the full migration set including 0011.
func TestRegression_MigratedSchema_SharedTokenAuthUnchanged(t *testing.T) {
	t.Parallel()
	const secret = "legacy-shared-secret"
	stk := newAuthTestStack(t, secret)

	do := func(method, path string, body any, auth string) (int, []byte) {
		t.Helper()
		var req *http.Request
		var err error
		if body == nil {
			req, err = http.NewRequest(method, "http://ravn.test"+path, nil)
		} else {
			req, err = http.NewRequest(method, "http://ravn.test"+path, jsonBytesReader(t, body))
		}
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res := httptest.NewRecorder()
		stk.Handler.ServeHTTP(res, req)
		return res.Code, res.Body.Bytes()
	}

	// Unauthenticated / mis-authenticated requests are rejected exactly as before.
	for _, tc := range []struct {
		name string
		auth string
	}{
		{"missing auth header", ""},
		{"wrong token", "Bearer something-else"},
		{"empty bearer", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do("GET", "/api/v1/projects", nil, tc.auth)
			if code != http.StatusUnauthorized {
				t.Fatalf("GET /api/v1/projects = %d, want 401 (body %s)", code, body)
			}
			errBody := mustDecode[errResp](t, body)
			if errBody.Error != "unauthorized" || errBody.Message != "missing or invalid auth token" {
				t.Errorf("401 envelope = %+v, want the standard unauthorized error", errBody)
			}

			code, _ = do("POST", "/api/v1/projects", map[string]any{
				"actor": actor("hollen"), "name": "Must Not Exist",
			}, tc.auth)
			if code != http.StatusUnauthorized {
				t.Fatalf("POST /api/v1/projects = %d, want 401 (mutation must be blocked)", code)
			}
		})
	}

	// The correct legacy token still authenticates, for reads and writes.
	code, body := do("GET", "/api/v1/projects", nil, secret)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects with shared token = %d, want 200 (body %s)", code, body)
	}

	code, body = do("POST", "/api/v1/projects", map[string]any{
		"actor": actor("hollen"), "name": "Legacy Flow Project",
	}, secret)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects with shared token = %d, want 201 (body %s)", code, body)
	}

	code, body = do("GET", "/api/v1/projects", nil, secret)
	if code != http.StatusOK {
		t.Fatalf("re-GET /api/v1/projects = %d, want 200", code)
	}
	var list struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
		Total int `json:"total"`
	}
	list = mustDecode[struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
		Total int `json:"total"`
	}](t, body)
	if list.Total != 1 || len(list.Projects) != 1 || list.Projects[0].Name != "Legacy Flow Project" {
		t.Errorf("project list after create = %+v, want exactly the created project", list)
	}
}
