package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// Vite-shaped dist fixtures (ARCHITECTURE §7: hashed assets/* + index.html).
const (
	indexHTML = "<!doctype html><html><head><title>Rävn</title></head><body>Rävn UI</body></html>\n"
	appJS     = "console.log('ravn app');\n"
	styleCSS  = "body { margin: 0; }\n"
)

// newTestDB opens a temp-file SQLite database (migrations included).
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ravn-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })
	return db
}

// newTestDBPool opens a temp-file SQLite database with dual writer/reader pools.
func newTestDBPool(t *testing.T) *sqlite.DBPool {
	t.Helper()
	ctx := context.Background()
	pool, closePool, err := sqlite.OpenPool(ctx, filepath.Join(t.TempDir(), "ravn-test.db"))
	if err != nil {
		t.Fatalf("open test db pool: %v", err)
	}
	t.Cleanup(func() { closePool() })
	return pool
}

// startTestServer serves the exact router production uses with NO built UI —
// the graceful-degradation (backend-only) configuration. It runs on an
// httptest port so tests never collide with anything else.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-such-dist"), "", t.TempDir()))
	t.Cleanup(srv.Close)
	return srv
}

// startStaticTestServer serves the production router with a Vite-shaped dist:
// index.html plus assets/app.js in a temp directory.
func startStaticTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "app.js"), []byte(appJS), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	srv := httptest.NewServer(newRouter(newTestDB(t), staticDir, "", t.TempDir()))
	t.Cleanup(srv.Close)
	return srv
}

// startStaticTestServerWithCSS is like startStaticTestServer but adds a hashed
// .css bundle as well, so Content-Type detection can be asserted for both MIME
// families. It deliberately does not touch the shared fixture above.
func startStaticTestServerWithCSS(t *testing.T) *httptest.Server {
	t.Helper()
	staticDir := t.TempDir()
	files := map[string]string{
		"index.html":                         indexHTML,
		filepath.Join("assets", "app.js"):    appJS,
		filepath.Join("assets", "style.css"): styleCSS,
	}
	for name, content := range files {
		p := filepath.Join(staticDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	srv := httptest.NewServer(newRouter(newTestDB(t), staticDir, "", t.TempDir()))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body from %s: %v", url, err)
	}
	return res.StatusCode, string(body)
}

func TestHealthzReturnsExactOKJSON(t *testing.T) {
	srv := startTestServer(t)

	code, body := get(t, srv.URL+"/healthz")
	if code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200 (body %s)", code, body)
	}
	want := `{"status":"ok","db":"ok"}`
	if body != want {
		t.Errorf("GET /healthz body = %q, want exactly %q", body, want)
	}
}

func TestRootReturnsPlaceholder(t *testing.T) {
	srv := startTestServer(t)

	code, body := get(t, srv.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (body %s)", code, body)
	}
	if !strings.Contains(body, "ravn server ok") {
		t.Errorf("GET / body = %q, want it to contain %q", body, "ravn server ok")
	}
}

// --- Slice 5a: static serving of the built web UI with SPA fallback ---

func TestStaticRootServesIndexHTML(t *testing.T) {
	srv := startStaticTestServer(t)

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body from /: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if loc := res.Header.Get("Location"); loc != "" {
		t.Errorf("GET / Location = %q, want no redirect", loc)
	}
	if string(body) != indexHTML {
		t.Errorf("GET / body = %q, want the dist index.html content", body)
	}
}

func TestStaticRealAssetServedByteForByte(t *testing.T) {
	srv := startStaticTestServer(t)

	res, err := http.Get(srv.URL + "/assets/app.js")
	if err != nil {
		t.Fatalf("GET /assets/app.js: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body from /assets/app.js: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want a javascript MIME type", ct)
	}
	if string(body) != appJS {
		t.Errorf("asset body = %q, want byte-for-byte %q", body, appJS)
	}
}

func TestStaticSPAFallbackServesIndexForDeepLinks(t *testing.T) {
	srv := startStaticTestServer(t)

	for _, p := range []string{"/some/spa/route", "/projects/whatever/board"} {
		code, body := get(t, srv.URL+p)
		if code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (body %s)", p, code, body)
		}
		if body != indexHTML {
			t.Errorf("GET %s body = %q, want the index.html fallback", p, body)
		}
	}
}

// TestStaticDirectoryRequestIsNeverListed pins down a security property: a
// directory request must never be delegated to http.FileServer, which would
// emit an auto-generated HTML listing for any subdirectory lacking index.html
// (leaking internal asset structure/filenames to arbitrary visitors). Such
// requests fall through to the SPA fallback — 200 with the dist root
// index.html, byte-for-byte.
func TestStaticDirectoryRequestIsNeverListed(t *testing.T) {
	srv := startStaticTestServer(t)

	for _, p := range []string{"/assets/", "/assets"} {
		t.Run(p, func(t *testing.T) {
			res, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatalf("GET %s: %v", p, err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body from %s: %v", p, err)
			}
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200 SPA root index (body %s)", p, res.StatusCode, body)
			}
			if loc := res.Header.Get("Location"); loc != "" {
				t.Errorf("GET %s Location = %q, want no redirect to a listing", p, loc)
			}
			if string(body) != indexHTML {
				t.Errorf("GET %s body = %q, want the dist index.html content byte-for-byte", p, body)
			}
			// Would fail against the pre-fix dirList page: it names every file.
			if strings.Contains(string(body), "app.js") {
				t.Errorf("GET %s body leaks an assets/ filename (auto-generated directory listing?): %q", p, body)
			}
			// stdlib net/http/dirIndex marker — a generated listing, not the SPA index.
			if strings.Contains(string(body), "Directory listing") {
				t.Errorf("GET %s looks like a stdlib auto-generated directory index: %q", p, body)
			}
		})
	}
}

// TestStaticRealAssetContentTypes asserts real static files keep being served
// byte-for-byte with an appropriate Content-Type. It uses a dedicated fixture
// (style.css added) instead of mutating the shared startStaticTestServer.
func TestStaticRealAssetContentTypes(t *testing.T) {
	srv := startStaticTestServerWithCSS(t)

	for _, tc := range []struct {
		path string // static path to fetch
		body string // expected byte-for-byte body
		mime string // required substring of the lowercased Content-Type
	}{
		{"/assets/app.js", appJS, "javascript"},
		{"/assets/style.css", styleCSS, "text/css"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body from %s: %v", tc.path, err)
			}
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200 (body %s)", tc.path, res.StatusCode, body)
			}
			ct := strings.ToLower(res.Header.Get("Content-Type"))
			if ct == "" {
				t.Errorf("GET %s Content-Type is empty", tc.path)
			}
			if !strings.Contains(ct, tc.mime) {
				t.Errorf("GET %s Content-Type = %q, want it to contain %q", tc.path, ct, tc.mime)
			}
			if string(body) != tc.body {
				t.Errorf("GET %s body = %q, want byte-for-byte %q", tc.path, body, tc.body)
			}
		})
	}
}

// TestStaticServingDoesNotShadowAPIAndHealthz proves the Go 1.22 mux pattern
// precedence with a built UI mounted: the exact "GET /api/v1/..." and
// "GET /healthz" patterns are more specific than the "GET /" catch-all, so
// they win and the SPA fallback never swallows API or health requests.
func TestStaticServingDoesNotShadowAPIAndHealthz(t *testing.T) {
	srv := startStaticTestServer(t)

	// /api/v1/projects → the API handler (JSON envelope), not index.html.
	code, body := get(t, srv.URL+"/api/v1/projects")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects status = %d, want 200 (body %s)", code, body)
	}
	var list struct {
		Projects []any `json:"projects"`
		Total    int   `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("GET /api/v1/projects body is not the JSON envelope (SPA swallowed it?): %v — %q", err, body)
	}
	if len(list.Projects) != 0 || list.Total != 0 {
		t.Errorf("project list = %d rows (total %d), want empty", len(list.Projects), list.Total)
	}

	// /healthz → the exact §8 JSON contract, not index.html.
	code, body = get(t, srv.URL+"/healthz")
	if code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200 (body %s)", code, body)
	}
	const wantHealth = `{"status":"ok","db":"ok"}`
	if body != wantHealth {
		t.Errorf("GET /healthz body = %q, want exactly %q", body, wantHealth)
	}
}

// Unknown API paths that reach the catch-all must 404 — never fall back to
// index.html.
func TestUnknownAPIPathIsNotFoundNotSPA(t *testing.T) {
	srv := startStaticTestServer(t)

	code, body := get(t, srv.URL+"/api/v1/definitely-not-a-route")
	if code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/definitely-not-a-route status = %d, want 404 (body %s)", code, body)
	}
	if strings.Contains(body, "Rävn UI") {
		t.Errorf("unknown API path returned the SPA index: %q", body)
	}
}

// TestNoStaticDirKeepsPlaceholderRoot covers graceful degradation: with a
// missing or empty static dir the server still boots, / keeps answering with
// the placeholder, and /healthz is unaffected.
func TestNoStaticDirKeepsPlaceholderRoot(t *testing.T) {
	for name, dir := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "does-not-exist"),
		"empty":   t.TempDir(), // exists but holds no index.html
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(newRouter(newTestDB(t), dir, "", t.TempDir()))
			t.Cleanup(srv.Close)

			code, body := get(t, srv.URL+"/")
			if code != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200 (body %s)", code, body)
			}
			if !strings.Contains(body, "ravn server ok") {
				t.Errorf("GET / body = %q, want the placeholder text", body)
			}

			code, body = get(t, srv.URL+"/healthz")
			if code != http.StatusOK || body != `{"status":"ok","db":"ok"}` {
				t.Errorf("GET /healthz = %d %q, want 200 with ok JSON", code, body)
			}
		})
	}
}

func reqWithAuth(t *testing.T, method, url, authHeader, body string) (int, string) {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, url, err)
	}
	return res.StatusCode, string(b)
}

func TestAuthGatingWithToken(t *testing.T) {
	const secret = "production-secret-token"
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), secret, t.TempDir()))
	t.Cleanup(srv.Close)

	// 1. /healthz is NEVER gated: without auth, with invalid auth, and with valid auth
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + secret} {
		code, body := reqWithAuth(t, "GET", srv.URL+"/healthz", auth, "")
		if code != http.StatusOK {
			t.Errorf("GET /healthz (auth %q) status = %d, want 200", auth, code)
		}
		if body != `{"status":"ok","db":"ok"}` {
			t.Errorf("GET /healthz body = %q, want ok JSON", body)
		}
	}

	// 2. Placeholder root "/" is not gated
	code, body := reqWithAuth(t, "GET", srv.URL+"/", "", "")
	if code != http.StatusOK || !strings.Contains(body, "ravn server ok") {
		t.Errorf("GET / status = %d, want 200", code)
	}

	// 3. GET /api/v1/projects without auth -> 401
	code, body = reqWithAuth(t, "GET", srv.URL+"/api/v1/projects", "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/projects without token status = %d, want 401", code)
	}
	const wantErr = `{"error":"unauthorized","message":"missing or invalid auth token"}`
	if strings.TrimSpace(body) != wantErr {
		t.Errorf("GET /api/v1/projects body = %q, want %q", body, wantErr)
	}

	// 4. POST /api/v1/projects without auth -> 401, mutation does not occur
	createPayload := `{"actor":{"type":"agent","name":"bot"},"name":"Secure Project"}`
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "", createPayload)
	if code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/projects without token status = %d, want 401", code)
	}

	// 5. POST /api/v1/projects with invalid auth -> 401
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "Bearer wrong-token", createPayload)
	if code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/projects with wrong token status = %d, want 401", code)
	}

	// 6. Valid token succeeds: GET /api/v1/projects -> 200
	code, body = reqWithAuth(t, "GET", srv.URL+"/api/v1/projects", "Bearer "+secret, "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects with token status = %d, want 200", code)
	}

	// 7. Valid token succeeds: POST /api/v1/projects -> 201
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "Bearer "+secret, createPayload)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects with token status = %d, want 201 (body %s)", code, body)
	}
}

func TestAuthUnsetLeavesAPIUnauthenticated(t *testing.T) {
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), "", t.TempDir()))
	t.Cleanup(srv.Close)

	// GET /api/v1/projects without auth -> 200
	code, body := reqWithAuth(t, "GET", srv.URL+"/api/v1/projects", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects status = %d, want 200 (body %s)", code, body)
	}

	// POST /api/v1/projects without auth -> 201
	createPayload := `{"actor":{"type":"agent","name":"bot"},"name":"Open Dev Project"}`
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "", createPayload)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects status = %d, want 201 (body %s)", code, body)
	}
}

func TestRouterRealtimeEventsEndToEnd(t *testing.T) {
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), "", t.TempDir()))
	t.Cleanup(srv.Close)

	// 1. Create a project
	code, body := reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "",
		`{"actor":{"type":"human","name":"ada"},"name":"SSE Live Project"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects = %d (%s)", code, body)
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &proj); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}

	// 2. Connect client to SSE stream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect to SSE: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	reader := bufio.NewReader(res.Body)

	// 3. Create a task in the project
	taskPayload := fmt.Sprintf(`{"actor":{"type":"agent","name":"bot"},"project_id":"%s","title":"SSE Task 1"}`, proj.ID)
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/tasks", "", taskPayload)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/tasks = %d (%s)", code, body)
	}
	var taskRes struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(body), &taskRes); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}

	// 4. Read SSE event from stream
	type sseRes struct {
		event string
		data  string
		err   error
	}
	ch := make(chan sseRes, 1)
	go func() {
		var curEvent, curData string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				ch <- sseRes{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if curEvent != "" || curData != "" {
					ch <- sseRes{event: curEvent, data: curData}
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				curEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				curData = strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatalf("read SSE event: %v", result.err)
		}
		// Default (unnamed) message frame: no `event:` line; the kind rides in
		// the JSON payload so clients dispatch by payload.kind.
		if result.event != "" {
			t.Errorf("event line = %q, want empty (default message frame)", result.event)
		}
		var payload struct {
			TaskID string `json:"task_id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(result.data), &payload); err != nil {
			t.Fatalf("unmarshal event data: %v (raw %q)", err, result.data)
		}
		if payload.TaskID != taskRes.Task.ID {
			t.Errorf("TaskID = %q, want %q", payload.TaskID, taskRes.Task.ID)
		}
		if payload.Kind != "created" {
			t.Errorf("Kind = %q, want created", payload.Kind)
		}
		if payload.Status != "todo" {
			t.Errorf("Status = %q, want todo", payload.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for realtime SSE event")
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"Debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"  debug  ", slog.LevelDebug},
	}

	for _, tc := range tests {
		got := parseLogLevel(tc.input)
		if got != tc.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	lvl := parseLogLevel("warn")
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: lvl}))

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	out := buf.String()
	if strings.Contains(out, "debug message") {
		t.Errorf("expected debug message to be filtered out at warn level, got: %s", out)
	}
	if strings.Contains(out, "info message") {
		t.Errorf("expected info message to be filtered out at warn level, got: %s", out)
	}
	if !strings.Contains(out, "warn message") {
		t.Errorf("expected warn message to be logged, got: %s", out)
	}
	if !strings.Contains(out, "error message") {
		t.Errorf("expected error message to be logged, got: %s", out)
	}
}

func TestMetricsEndpointInRouter(t *testing.T) {
	srv := startTestServer(t)

	// 1. Initial /metrics request returns Prometheus format by default
	code, body := get(t, srv.URL+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200 (body %s)", code, body)
	}
	if !strings.Contains(body, "# HELP http_requests_total") {
		t.Errorf("GET /metrics body missing http_requests_total HELP: %s", body)
	}

	// 2. Perform a request to /healthz (status 200) and an unknown project (status 404)
	get(t, srv.URL+"/healthz")
	get(t, srv.URL+"/api/v1/projects/01M3J000000000000000000000")

	// 3. Request /metrics?format=json and verify counters
	code, body = get(t, srv.URL+"/metrics?format=json")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics?format=json status = %d, want 200 (body %s)", code, body)
	}
	var snap struct {
		HTTPRequestsTotal    uint64            `json:"http_requests_total"`
		HTTPRequestsInFlight int64             `json:"http_requests_in_flight"`
		HTTPRequestsByStatus map[string]uint64 `json:"http_requests_by_status"`
		HTTPRequestsByMethod map[string]uint64 `json:"http_requests_by_method"`
		UptimeSeconds        float64           `json:"uptime_seconds"`
	}
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("unmarshal /metrics?format=json: %v — body: %s", err, body)
	}

	if snap.HTTPRequestsTotal < 3 {
		t.Errorf("HTTPRequestsTotal = %d, want >= 3", snap.HTTPRequestsTotal)
	}
	if snap.HTTPRequestsByStatus["200"] == 0 {
		t.Errorf("HTTPRequestsByStatus[200] = 0, want > 0")
	}
	if snap.HTTPRequestsByStatus["404"] == 0 {
		t.Errorf("HTTPRequestsByStatus[404] = 0, want > 0")
	}

	// 4. Verify /api/v1/metrics returns JSON by default
	code, body = get(t, srv.URL+"/api/v1/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics status = %d, want 200 (body %s)", code, body)
	}
	var apiSnap struct {
		HTTPRequestsTotal uint64 `json:"http_requests_total"`
	}
	if err := json.Unmarshal([]byte(body), &apiSnap); err != nil {
		t.Fatalf("unmarshal /api/v1/metrics: %v — body: %s", err, body)
	}
}

func TestMetricsTaskCountsAndActiveClaims(t *testing.T) {
	db := newTestDB(t)
	srv := httptest.NewServer(newRouter(db, filepath.Join(t.TempDir(), "no-dist"), "", t.TempDir()))
	t.Cleanup(srv.Close)

	// Create a project
	code, body := reqWithAuth(t, "POST", srv.URL+"/api/v1/projects", "",
		`{"actor":{"type":"human","name":"ada"},"name":"Metrics Observability Project"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects = %d (%s)", code, body)
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &proj); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}

	// Create task 1
	code, _ = reqWithAuth(t, "POST", srv.URL+"/api/v1/tasks", "",
		fmt.Sprintf(`{"actor":{"type":"agent","name":"bot"},"project_id":"%s","title":"Task 1"}`, proj.ID))
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/tasks = %d", code)
	}

	// Create task 2 and claim it
	code, body = reqWithAuth(t, "POST", srv.URL+"/api/v1/tasks", "",
		fmt.Sprintf(`{"actor":{"type":"agent","name":"bot"},"project_id":"%s","title":"Task 2"}`, proj.ID))
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/tasks = %d", code)
	}
	var taskRes struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(body), &taskRes); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}

	code, _ = reqWithAuth(t, "POST", srv.URL+"/api/v1/tasks/"+taskRes.Task.ID+"/claim", "",
		`{"actor":{"type":"agent","name":"bot"},"ttl_sec":3600}`)
	if code != http.StatusOK {
		t.Fatalf("claim task: %d", code)
	}

	// Query /metrics?format=json
	code, body = get(t, srv.URL+"/metrics?format=json")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", code)
	}

	var snap struct {
		TasksByStatus map[string]int64 `json:"tasks_by_status"`
		ActiveClaims  *int64           `json:"active_claims"`
	}
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("unmarshal metrics: %v (%s)", err, body)
	}

	if snap.TasksByStatus["todo"] != 1 {
		t.Errorf("tasks_by_status[todo] = %d, want 1", snap.TasksByStatus["todo"])
	}
	if snap.TasksByStatus["doing"] != 1 {
		t.Errorf("tasks_by_status[doing] = %d, want 1", snap.TasksByStatus["doing"])
	}
	if snap.ActiveClaims == nil || *snap.ActiveClaims != 1 {
		t.Errorf("active_claims = %v, want 1", snap.ActiveClaims)
	}
}

func TestStaticServingDoesNotShadowMetrics(t *testing.T) {
	srv := startStaticTestServer(t)

	// GET /metrics should return Prometheus metrics, NOT index.html
	code, body := get(t, srv.URL+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", code)
	}
	if strings.Contains(body, "Rävn UI") {
		t.Errorf("GET /metrics returned the SPA index: %q", body)
	}
	if !strings.Contains(body, "http_requests_total") {
		t.Errorf("GET /metrics body missing http_requests_total: %q", body)
	}

	// GET /api/v1/metrics should return JSON, NOT index.html
	code, body = get(t, srv.URL+"/api/v1/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics status = %d, want 200", code)
	}
	if strings.Contains(body, "Rävn UI") {
		t.Errorf("GET /api/v1/metrics returned the SPA index: %q", body)
	}
	var snap struct {
		HTTPRequestsTotal uint64 `json:"http_requests_total"`
	}
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("GET /api/v1/metrics returned invalid JSON: %v (%s)", err, body)
	}
}

func TestHTTPServer_ConfiguredTimeouts(t *testing.T) {
	srv := newHTTPServer(":8080", http.NotFoundHandler())
	if srv.ReadHeaderTimeout < 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want >= 5s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout < 15*time.Second {
		t.Errorf("ReadTimeout = %v, want >= 15s", srv.ReadTimeout)
	}
	if srv.IdleTimeout < 60*time.Second {
		t.Errorf("IdleTimeout = %v, want >= 60s", srv.IdleTimeout)
	}
}

func TestHTTPServer_IdleTimeoutClosesKeepAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := newHTTPServerWithTimeouts(ln.Addr().String(), handler, 5*time.Second, 15*time.Second, 100*time.Millisecond)
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send an initial request on keep-alive connection
	req := "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write req: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	resp.Body.Close()

	// Wait longer than IdleTimeout (100ms)
	time.Sleep(200 * time.Millisecond)

	// Connection should now be closed by server
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 10)
	_, err = conn.Read(buf)
	if err == nil {
		t.Errorf("expected connection to be closed after idle timeout, but read succeeded")
	}
}

func TestHTTPServer_ReadTimeoutSeversSlowBody(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var readErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		if readErr != nil {
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := newHTTPServerWithTimeouts(ln.Addr().String(), handler, 5*time.Second, 100*time.Millisecond, 1*time.Second)
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send headers declaring 100 bytes of body
	req := "POST / HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 100\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write headers: %v", err)
	}

	// Send 1 byte
	if _, err := conn.Write([]byte("a")); err != nil {
		t.Fatalf("write 1 byte: %v", err)
	}

	// Sleep longer than ReadTimeout (100ms) before sending remaining bytes
	time.Sleep(200 * time.Millisecond)

	// Attempting to write more bytes or read should observe severed connection
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 128)
	conn.Read(buf) // Drain any EOF or error
	if readErr == nil {
		t.Errorf("expected readErr to be non-nil due to ReadTimeout, got nil")
	}
}

func TestGracefulShutdown_WithActiveSSEStreams(t *testing.T) {
	db := newTestDB(t)
	broker := pubsub.NewBroker()
	router := newRouterWithBroker(db, filepath.Join(t.TempDir(), "no-dist"), "", t.TempDir(), broker)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srv := newHTTPServer(ln.Addr().String(), router)
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	// Create a project so we can stream events for it
	code, pBody := reqWithAuth(t, "POST", "http://"+ln.Addr().String()+"/api/v1/projects", "", `{"name":"Shutdown Project","actor":{"name":"test-actor","type":"agent"}}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects = %d (%s)", code, pBody)
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(pBody), &proj); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}

	// Connect an SSE client to /api/v1/projects/{id}/events
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/projects/" + proj.ID + "/events")
	if err != nil {
		t.Fatalf("GET /api/v1/projects/%s/events: %v", proj.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	// Read initial SSE message ": connected\n\n"
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, ": connected") {
		t.Fatalf("expected ': connected', got %q, err: %v", line, err)
	}

	// Active SSE stream is now connected. Verify broker has subscriber.
	if broker.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", broker.SubscriberCount())
	}

	// Initiate shutdown: close the broker to unblock SSE handlers
	if err := broker.Close(); err != nil {
		t.Fatalf("broker.Close(): %v", err)
	}

	// srv.Shutdown should complete quickly without timing out
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		t.Fatalf("srv.Shutdown failed: %v", shutdownErr)
	}

	// SSE client should now observe end-of-stream (EOF)
	_, readErr := io.ReadAll(resp.Body)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		t.Logf("client read error after shutdown: %v", readErr)
	}
}

func TestHTTPServer_SecurityHeadersAndCORS(t *testing.T) {
	t.Parallel()
	const secret = "auth-token-xyz"
	db := newTestDB(t)
	staticDir := filepath.Join(t.TempDir(), "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<!doctype html><html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	router := newRouterWithBrokerAndCORS(db, staticDir, secret, t.TempDir(), pubsub.NewBroker(), "http://localhost:5173")
	srv := httptest.NewServer(router)
	defer srv.Close()

	// 1. All responses carry standard security headers (AC 1)
	endpoints := []string{"/healthz", "/metrics", "/"}
	for _, ep := range endpoints {
		res, err := http.Get(srv.URL + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		res.Body.Close()
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", ep, got)
		}
		if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("GET %s X-Frame-Options = %q, want DENY", ep, got)
		}
		if got := res.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
			t.Errorf("GET %s Referrer-Policy = %q, want strict-origin-when-cross-origin", ep, got)
		}
	}

	// 2. Preflight OPTIONS to /api/* bypasses auth, returns 204 with CORS headers (AC 2, AC 3)
	req, err := http.NewRequest("OPTIONS", srv.URL+"/api/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/v1/tasks status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin = %q, want http://localhost:5173", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Errorf("Access-Control-Allow-Methods is empty")
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got == "" {
		t.Errorf("Access-Control-Allow-Headers is empty")
	}
	if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("OPTIONS X-Frame-Options = %q, want DENY", got)
	}
}

func TestHealthzAndMetricsResilientToActiveWriteTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTestDBPool(t)

	// Create a router with the dual pool
	router := newRouterWithPool(pool, filepath.Join(t.TempDir(), "no-dist"), "", t.TempDir())
	srv := httptest.NewServer(router)
	defer srv.Close()

	// 1. Initial health check and metrics succeed
	code, body := get(t, srv.URL+"/healthz")
	if code != http.StatusOK || body != `{"status":"ok","db":"ok"}` {
		t.Fatalf("initial GET /healthz = %d %q, want 200 ok", code, body)
	}

	code, body = get(t, srv.URL+"/metrics")
	if code != http.StatusOK || !strings.Contains(body, "http_requests_total") {
		t.Fatalf("initial GET /metrics = %d %q, want 200 with http_requests_total", code, body)
	}

	// 2. Insert a project and commit it so the DB has data
	_, err := pool.Writer.ExecContext(ctx, `INSERT INTO project (id, name, created_at, updated_at) VALUES ('01M00000000000000000000001', 'Active Tx Project', '2026-10-15T00:00:00Z', '2026-10-15T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// 3. Begin an explicit write transaction on pool.Writer and hold it open
	tx, err := pool.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	defer tx.Rollback()

	// Mutate task table inside the transaction
	_, err = tx.ExecContext(ctx, `INSERT INTO task (id, project_id, status, title, created_at, updated_at) VALUES ('01M00000000000000000000002', '01M00000000000000000000001', 'todo', 'Concurrent Health Test Task', '2026-10-15T00:00:00Z', '2026-10-15T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert task in write tx: %v", err)
	}

	// 4. Verify pool.Writer is indeed blocked for any new query:
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	var dummy int
	err = pool.Writer.QueryRowContext(shortCtx, "SELECT 1").Scan(&dummy)
	if err == nil {
		t.Fatalf("expected writer query to time out while tx is active")
	}

	// 5. While tx is STILL HOLDING the writer connection, perform concurrent /healthz and /metrics requests!
	const requestCount = 10
	var wg sync.WaitGroup
	errCh := make(chan error, requestCount*3)

	for i := 0; i < requestCount; i++ {
		wg.Add(3)

		// Concurrent /healthz
		go func() {
			defer wg.Done()
			reqCtx, reqCancel := context.WithTimeout(ctx, 1*time.Second)
			defer reqCancel()

			req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/healthz", nil)
			res, err := srv.Client().Do(req)
			if err != nil {
				errCh <- fmt.Errorf("GET /healthz failed: %w", err)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("GET /healthz status = %d (body: %s), want 200", res.StatusCode, string(b))
				return
			}
			if string(b) != `{"status":"ok","db":"ok"}` {
				errCh <- fmt.Errorf("GET /healthz body = %q, want ok JSON", string(b))
			}
		}()

		// Concurrent /metrics (Prometheus)
		go func() {
			defer wg.Done()
			reqCtx, reqCancel := context.WithTimeout(ctx, 1*time.Second)
			defer reqCancel()

			req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/metrics", nil)
			res, err := srv.Client().Do(req)
			if err != nil {
				errCh <- fmt.Errorf("GET /metrics failed: %w", err)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("GET /metrics status = %d, want 200", res.StatusCode)
				return
			}
			if !strings.Contains(string(b), "http_requests_total") {
				errCh <- fmt.Errorf("GET /metrics missing http_requests_total")
			}
		}()

		// Concurrent /metrics?format=json
		go func() {
			defer wg.Done()
			reqCtx, reqCancel := context.WithTimeout(ctx, 1*time.Second)
			defer reqCancel()

			req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/metrics?format=json", nil)
			res, err := srv.Client().Do(req)
			if err != nil {
				errCh <- fmt.Errorf("GET /metrics?format=json failed: %w", err)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("GET /metrics?format=json status = %d, want 200", res.StatusCode)
				return
			}
			var snap struct {
				TasksByStatus map[string]int64 `json:"tasks_by_status"`
			}
			if err := json.Unmarshal(b, &snap); err != nil {
				errCh <- fmt.Errorf("unmarshal json metrics: %w", err)
				return
			}
			// Snapshot isolation: reader does not see the uncommitted task
			if snap.TasksByStatus["todo"] != 0 {
				errCh <- fmt.Errorf("expected 0 todo tasks before commit, got %d", snap.TasksByStatus["todo"])
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("error during concurrent requests with active write tx: %v", err)
	}

	// 6. Commit the transaction and verify /metrics?format=json now sees 1 todo task
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	code, body = get(t, srv.URL+"/metrics?format=json")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics?format=json after commit = %d", code)
	}
	var snapAfter struct {
		TasksByStatus map[string]int64 `json:"tasks_by_status"`
	}
	if err := json.Unmarshal([]byte(body), &snapAfter); err != nil {
		t.Fatalf("unmarshal json metrics after commit: %v", err)
	}
	if snapAfter.TasksByStatus["todo"] != 1 {
		t.Errorf("expected 1 todo task after commit, got %d", snapAfter.TasksByStatus["todo"])
	}
}
