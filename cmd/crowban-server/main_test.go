package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

// startTestServer opens a temp-file SQLite database (migrations included) and
// serves the exact router production uses, on an httptest port so tests never
// collide with anything else.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	srv := httptest.NewServer(newRouter(db))
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
