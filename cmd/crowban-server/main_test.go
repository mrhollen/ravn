package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

// Vite-shaped dist fixtures (ARCHITECTURE §7: hashed assets/* + index.html).
const (
	indexHTML = "<!doctype html><html><head><title>crowban</title></head><body>crowban UI</body></html>\n"
	appJS     = "console.log('crowban app');\n"
)

// newTestDB opens a temp-file SQLite database (migrations included).
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })
	return db
}

// startTestServer serves the exact router production uses with NO built UI —
// the graceful-degradation (backend-only) configuration. It runs on an
// httptest port so tests never collide with anything else.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-such-dist"), ""))
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
	srv := httptest.NewServer(newRouter(newTestDB(t), staticDir, ""))
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
	if !strings.Contains(body, "crowban server ok") {
		t.Errorf("GET / body = %q, want it to contain %q", body, "crowban server ok")
	}
}

// --- Slice 5a: static serving of the built web UI with SPA fallback ---

func TestStaticRootServesIndexHTML(t *testing.T) {
	srv := startStaticTestServer(t)

	code, body := get(t, srv.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (body %s)", code, body)
	}
	if body != indexHTML {
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
	if strings.Contains(body, "crowban UI") {
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
			srv := httptest.NewServer(newRouter(newTestDB(t), dir, ""))
			t.Cleanup(srv.Close)

			code, body := get(t, srv.URL+"/")
			if code != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200 (body %s)", code, body)
			}
			if !strings.Contains(body, "crowban server ok") {
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
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), secret))
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
	if code != http.StatusOK || !strings.Contains(body, "crowban server ok") {
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
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), ""))
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
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-dist"), ""))
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
		if result.event != "created" {
			t.Errorf("event = %q, want created", result.event)
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
	srv := httptest.NewServer(newRouter(db, filepath.Join(t.TempDir(), "no-dist"), ""))
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
		`{"actor":{"type":"agent","name":"bot"},"ttl_seconds":3600}`)
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
	if strings.Contains(body, "crowban UI") {
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
	if strings.Contains(body, "crowban UI") {
		t.Errorf("GET /api/v1/metrics returned the SPA index: %q", body)
	}
	var snap struct {
		HTTPRequestsTotal uint64 `json:"http_requests_total"`
	}
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("GET /api/v1/metrics returned invalid JSON: %v (%s)", err, body)
	}
}
