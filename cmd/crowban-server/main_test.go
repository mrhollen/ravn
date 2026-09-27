package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	srv := httptest.NewServer(newRouter(newTestDB(t), filepath.Join(t.TempDir(), "no-such-dist")))
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
	srv := httptest.NewServer(newRouter(newTestDB(t), staticDir))
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
			srv := httptest.NewServer(newRouter(newTestDB(t), dir))
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
