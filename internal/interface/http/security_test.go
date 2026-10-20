package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	t.Parallel()

	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	handler := SecurityHeadersMiddleware()(dummy)

	req := httptest.NewRequest("GET", "http://ravn.test/api/v1/tasks", nil)
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
	req := httptest.NewRequest("OPTIONS", "http://ravn.test/api/v1/tasks", nil)
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
	req := httptest.NewRequest("OPTIONS", "http://ravn.test/api/v1/tasks", nil)
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
	reqBad := httptest.NewRequest("OPTIONS", "http://ravn.test/api/v1/tasks", nil)
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
	req := httptest.NewRequest("OPTIONS", "http://ravn.test/api/v1/tasks", nil)
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
	reqDirect := httptest.NewRequest("OPTIONS", "http://ravn.test/api/v1/tasks", nil)
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

func TestDecodeJSON_ContentTypeEnforcement(t *testing.T) {
	t.Parallel()

	type dummyBody struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name           string
		contentType    string
		hasHeader      bool
		body           string
		wantStatus     int
		wantSuccess    bool
		wantErrKeyword string
	}{
		{
			name:        "valid application/json",
			contentType: "application/json",
			hasHeader:   true,
			body:        `{"name":"test"}`,
			wantStatus:  http.StatusOK,
			wantSuccess: true,
		},
		{
			name:        "valid application/json with charset utf-8",
			contentType: "application/json; charset=utf-8",
			hasHeader:   true,
			body:        `{"name":"test"}`,
			wantStatus:  http.StatusOK,
			wantSuccess: true,
		},
		{
			name:        "valid application/json case insensitive",
			contentType: "APPLICATION/JSON; CHARSET=UTF-8",
			hasHeader:   true,
			body:        `{"name":"test"}`,
			wantStatus:  http.StatusOK,
			wantSuccess: true,
		},
		{
			name:        "valid application/json with extra parameter",
			contentType: "application/json; boundary=something",
			hasHeader:   true,
			body:        `{"name":"test"}`,
			wantStatus:  http.StatusOK,
			wantSuccess: true,
		},
		{
			name:           "missing Content-Type on non-empty body",
			hasHeader:      false,
			body:           `{"name":"test"}`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "text/plain on non-empty body (simple request)",
			contentType:    "text/plain",
			hasHeader:      true,
			body:           `{"name":"test"}`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "text/plain with charset on non-empty body",
			contentType:    "text/plain; charset=utf-8",
			hasHeader:      true,
			body:           `{"name":"test"}`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "application/x-www-form-urlencoded on non-empty body",
			contentType:    "application/x-www-form-urlencoded",
			hasHeader:      true,
			body:           `name=test`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "multipart/form-data on non-empty body",
			contentType:    "multipart/form-data; boundary=xyz",
			hasHeader:      true,
			body:           `--xyz...`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "invalid media type format",
			contentType:    "invalid/type;;;",
			hasHeader:      true,
			body:           `{"name":"test"}`,
			wantStatus:     http.StatusUnsupportedMediaType,
			wantSuccess:    false,
			wantErrKeyword: "unsupported media type",
		},
		{
			name:           "empty body with Content-Type application/json returns 400 not 415",
			contentType:    "application/json",
			hasHeader:      true,
			body:           "",
			wantStatus:     http.StatusBadRequest,
			wantSuccess:    false,
			wantErrKeyword: "invalid input",
		},
		{
			name:           "empty body with text/plain returns 400 not 415",
			contentType:    "text/plain",
			hasHeader:      true,
			body:           "",
			wantStatus:     http.StatusBadRequest,
			wantSuccess:    false,
			wantErrKeyword: "invalid input",
		},
		{
			name:           "empty body without Content-Type returns 400 not 415",
			hasHeader:      false,
			body:           "",
			wantStatus:     http.StatusBadRequest,
			wantSuccess:    false,
			wantErrKeyword: "invalid input",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req dummyBody
				if !decodeJSON(w, r, &req) {
					return
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(req)
			})

			var req *http.Request
			if tc.body == "" {
				req = httptest.NewRequest("POST", "/test", bytes.NewReader(nil))
			} else {
				req = httptest.NewRequest("POST", "/test", strings.NewReader(tc.body))
			}

			if tc.hasHeader {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if !tc.wantSuccess {
				var errResp map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
					t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
				}
				if errStr, ok := errResp["error"].(string); !ok || !strings.Contains(errStr, tc.wantErrKeyword) {
					t.Errorf("error = %v, want keyword %q", errResp["error"], tc.wantErrKeyword)
				}
			}
		})
	}
}

func TestCSRF_CrossSiteSimpleRequestsCannotTriggerMutations(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	// Wrap stk.Handler with CORSMiddleware (DefaultCORSOptions has wildcard "*")
	corsHandler := CORSMiddleware(DefaultCORSOptions())(stk.Handler)

	const crossOrigin = "http://malicious-site.example.com"

	// 1. Attempt CSRF project creation via simple request (Content-Type: text/plain)
	t.Run("POST /projects with text/plain rejected and no mutation", func(t *testing.T) {
		body := `{"actor":{"name":"attacker","type":"human"},"name":"Pwned Project"}`
		req := httptest.NewRequest("POST", "http://ravn.test/api/v1/projects", strings.NewReader(body))
		req.Header.Set("Origin", crossOrigin)
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()

		corsHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("POST /projects status = %d, want 415 (body %s)", rec.Code, rec.Body.String())
		}

		// Verify project was not created
		listReq := httptest.NewRequest("GET", "http://ravn.test/api/v1/projects", nil)
		listRec := httptest.NewRecorder()
		corsHandler.ServeHTTP(listRec, listReq)

		var list struct {
			Projects []projectDTO `json:"projects"`
			Total    int          `json:"total"`
		}
		if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode projects list: %v", err)
		}
		if list.Total != 0 {
			t.Errorf("expected 0 projects, got %d (CSRF created project!)", list.Total)
		}
	})

	// 2. Attempt CSRF task creation via simple request (Content-Type: application/x-www-form-urlencoded)
	t.Run("POST /tasks with form-urlencoded rejected and no mutation", func(t *testing.T) {
		proj := createProject(t, stk, "Legit Project")

		body := `{"actor":{"name":"attacker"},"project_id":"` + proj.ID + `","title":"Pwned Task"}`
		req := httptest.NewRequest("POST", "http://ravn.test/api/v1/tasks", strings.NewReader(body))
		req.Header.Set("Origin", crossOrigin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		corsHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("POST /tasks status = %d, want 415 (body %s)", rec.Code, rec.Body.String())
		}

		// Verify task was not created
		listReq := httptest.NewRequest("GET", "http://ravn.test/api/v1/tasks?project_id="+proj.ID, nil)
		listRec := httptest.NewRecorder()
		corsHandler.ServeHTTP(listRec, listReq)

		var list struct {
			Tasks []taskSummaryDTO `json:"tasks"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode tasks list: %v", err)
		}
		if list.Total != 0 {
			t.Errorf("expected 0 tasks, got %d (CSRF created task!)", list.Total)
		}
	})

	// 3. Attempt CSRF task mutation via simple request (PATCH /tasks/{id} with text/plain)
	t.Run("PATCH /tasks/{id} with text/plain rejected and no mutation", func(t *testing.T) {
		proj := createProject(t, stk, "Project For Patch")
		task := newTask(t, stk, proj.ID, "Original Title")

		body := `{"actor":{"name":"attacker"},"title":"Hacked Title","version":1}`
		req := httptest.NewRequest("PATCH", "http://ravn.test/api/v1/tasks/"+task.ID, strings.NewReader(body))
		req.Header.Set("Origin", crossOrigin)
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()

		corsHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("PATCH /tasks/{id} status = %d, want 415 (body %s)", rec.Code, rec.Body.String())
		}

		detail := getDetail(t, stk, task.ID)
		if detail.Title != "Original Title" {
			t.Errorf("task title was mutated to %q, want %q", detail.Title, "Original Title")
		}
	})

	// 4. Legitimate request with Content-Type: application/json; charset=utf-8 succeeds
	t.Run("POST /tasks with application/json; charset=utf-8 succeeds", func(t *testing.T) {
		proj := createProject(t, stk, "Legit UTF8 Project")

		body := `{"actor":{"name":"alice"},"project_id":"` + proj.ID + `","title":"Legitimate Task"}`
		req := httptest.NewRequest("POST", "http://ravn.test/api/v1/tasks", strings.NewReader(body))
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		rec := httptest.NewRecorder()

		corsHandler.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /tasks status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
	})
}
