package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// startAdminBackupServer serves the production router with a seeded temp DB and
// pins RAVN_BACKUP_DIR to a base directory that does NOT exist yet — so the
// MkdirAll path is exercised exactly as in a fresh deployment.
func startAdminBackupServer(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "backups")
	t.Setenv("RAVN_BACKUP_DIR", base)

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ravn-test.db")
	db, closeDB, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = closeDB() })

	stmts := []string{
		`INSERT INTO project(id,name,description,created_at,updated_at)
		 VALUES ('pA1','Backup Admin Project','','2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`,
		`INSERT INTO task(id,project_id,status,title,priority,version,archived,created_at,updated_at)
		 VALUES ('tA1','pA1','todo','one',0,0,0,'2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`,
		`INSERT INTO task(id,project_id,status,title,priority,version,archived,created_at,updated_at)
		 VALUES ('tA2','pA1','doing','two',5,1,0,'2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	srv := httptest.NewServer(newRouter(db, filepath.Join(t.TempDir(), "no-dist"), token, t.TempDir()))
	t.Cleanup(srv.Close)
	return srv, base
}

// postAdminBackup POSTs a raw body to /api/v1/admin/backup with an optional
// bearer token; returns status + response body.
func postAdminBackup(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/v1/admin/backup", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST admin/backup: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return res.StatusCode, string(raw)
}

// openBackupRO opens a backup file with a fresh read-only connection (the
// modernc "sqlite" driver is already registered process-wide by the sqlite
// package).
func openBackupRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open backup read-only %q: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAdminBackupEndpoint_WritesValidFileInsideBase(t *testing.T) {
	srv, base := startAdminBackupServer(t, "sekret")

	dest := filepath.Join(base, "snapshots", "admin.db") // nested: parent dirs must be created
	code, body := postAdminBackup(t, srv.URL, "sekret", fmt.Sprintf(`{"dest":%q}`, dest))
	if code != http.StatusOK {
		t.Fatalf("POST admin/backup status = %d, want 200 (body %s)", code, body)
	}

	var out struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal success body %q: %v", body, err)
	}
	if out.Path != dest {
		t.Errorf("path = %q, want %q", out.Path, dest)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("backup file missing at %q: %v", dest, err)
	}
	if out.Bytes != fi.Size() {
		t.Errorf("bytes = %d, actual size %d", out.Bytes, fi.Size())
	}
	if fi.Size() == 0 {
		t.Error("backup file is empty")
	}

	// The written file must be a valid SQLite database carrying the rows.
	if err := sqlite.IntegrityCheck(context.Background(), dest); err != nil {
		t.Fatalf("integrity_check on endpoint backup: %v", err)
	}
	bak := openBackupRO(t, dest)
	var n int64
	if err := bak.QueryRow("SELECT COUNT(*) FROM task").Scan(&n); err != nil {
		t.Fatalf("count tasks in backup: %v", err)
	}
	if n != 2 {
		t.Errorf("task count in endpoint backup = %d, want 2", n)
	}
}

func TestAdminBackupEndpoint_AuthGate(t *testing.T) {
	srv, base := startAdminBackupServer(t, "sekret")
	dest := filepath.Join(base, "auth.db")
	payload := fmt.Sprintf(`{"dest":%q}`, dest)

	code, body := postAdminBackup(t, srv.URL, "", payload) // token set, none sent
	if code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401 (body %s)", code, body)
	} else if !strings.Contains(body, "unauthorized") {
		t.Errorf("no-token body %q, want unauthorized envelope", body)
	}

	code, body = postAdminBackup(t, srv.URL, "wrong-token", payload)
	if code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401 (body %s)", code, body)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("unauthenticated attempts must not write files (stat err = %v)", err)
	}

	// Zero-config default: no token configured → the route is open.
	openSrv, openBase := startAdminBackupServer(t, "")
	code, body = postAdminBackup(t, openSrv.URL, "", fmt.Sprintf(`{"dest":%q}`, filepath.Join(openBase, "open.db")))
	if code != http.StatusOK {
		t.Errorf("no-token-mode: status = %d, want 200 (body %s)", code, body)
	}
}

func TestAdminBackupEndpoint_InvalidDest(t *testing.T) {
	srv, base := startAdminBackupServer(t, "sekret")
	parent := filepath.Dir(base) // the directory CONTAINING the base dir

	cases := []struct {
		name string
		body string
	}{
		{"outside base", fmt.Sprintf(`{"dest":%q}`, filepath.Join(parent, "escape.db"))},
		{"traversal into outside", fmt.Sprintf(`{"dest":%q}`, base+"/../escape2.db")},
		{"relative path", `{"dest":"relative/evil.db"}`},
		{"empty body object", `{}`},
		{"malformed JSON", `not json at all`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postAdminBackup(t, srv.URL, "sekret", tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", code, body)
			}
			if !strings.Contains(body, `"error":"invalid_input"`) {
				t.Errorf("body %q, want invalid_input envelope", body)
			}
		})
	}
	if entries, err := os.ReadDir(parent); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), "escape") {
				t.Errorf("escape file written outside base: %s", e.Name())
			}
		}
	}
}

func TestAdminBackupEndpoint_ExistingTarget(t *testing.T) {
	srv, base := startAdminBackupServer(t, "sekret")
	dest := filepath.Join(base, "taken.db")
	if err := os.MkdirAll(base, 0o755); err != nil { // endpoint would MkdirAll itself; test must seed the file first
		t.Fatalf("mkdir base: %v", err)
	}
	sentinel := []byte("do not overwrite\n")
	if err := os.WriteFile(dest, sentinel, 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	code, body := postAdminBackup(t, srv.URL, "sekret", fmt.Sprintf(`{"dest":%q}`, dest))
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", code, body)
	}
	if !strings.Contains(body, `"error":"conflict"`) || !strings.Contains(body, "already exists") {
		t.Errorf("body %q, want conflict envelope naming the existing target", body)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(sentinel) {
		t.Errorf("existing target overwritten: %q", got)
	}
}

// TestResolveBackupBase_DerivesFromDataDir is a regression test: the REAL
// server (OpenPool on a file path) must expose pool.Path so the endpoint's
// default base dir becomes <data dir>/backups — NOT CWD-relative "backups"
// (which a live probe caught before the fix, surfacing as VACUUM-INTO
// "can't make … relative to backups").
func TestResolveBackupBase_DerivesFromDataDir(t *testing.T) {
	clearServerEnv(t) // RAVN_BACKUP_DIR must be unset for the default to kick in
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "nested", "ravn.db")

	ctx := context.Background()
	db, closeDB, err := sqlite.OpenPool(ctx, dataPath)
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	defer func() { _ = closeDB() }()

	if db.Path != dataPath {
		t.Fatalf("pool.Path = %q, want %q (file branch must record the path)", db.Path, dataPath)
	}
	got := resolveBackupBase(db.Path)
	want := filepath.Join(dir, "nested", "backups")
	if got != want {
		t.Errorf("resolveBackupBase(pool.Path) = %q, want %q", got, want)
	}
}

// --- one-shot CLI modes ---------------------------------------------------

func TestRunBackupFlag_OneShotWithoutHTTP(t *testing.T) {
	clearServerEnv(t) // RAVN_BACKUP_DIR pinned empty: the <data dir>/backups default must win
	dataDir := t.TempDir()
	dataPath := filepath.Join(dataDir, "ravn.db")

	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, dataPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project(id,name,description,created_at,updated_at)
		VALUES ('pC1','CLI Backup Project','','2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_ = closeDB()

	dest := filepath.Join(dataDir, "backups", "cli.db") // default base, nested one level
	var buf bytes.Buffer
	code := run([]string{"-data", dataPath, "-backup", dest}, &buf)
	if code != 0 {
		t.Fatalf("run(-backup) exit = %d, want 0 (output: %s)", code, buf.String())
	}
	if !strings.Contains(buf.String(), "backup ok: "+dest) || !strings.Contains(buf.String(), "integrity_check=ok") {
		t.Errorf("one-line result = %q, want \"backup ok: <dest> … integrity_check=ok\"", buf.String())
	}
	if err := sqlite.IntegrityCheck(ctx, dest); err != nil {
		t.Fatalf("cli backup failed integrity check: %v", err)
	}
}

func TestRunBackupFlag_ExistingTargetFails(t *testing.T) {
	clearServerEnv(t)
	dataPath := filepath.Join(t.TempDir(), "ravn.db")
	base := t.TempDir()
	dest := filepath.Join(base, "taken.db")
	if err := os.WriteFile(dest, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	t.Setenv("RAVN_BACKUP_DIR", base)

	var buf bytes.Buffer
	code := run([]string{"-data", dataPath, "-backup", dest}, &buf)
	if code != 1 {
		t.Fatalf("run(-backup existing) exit = %d, want 1 (output: %s)", code, buf.String())
	}
	if !strings.Contains(buf.String(), "already exists") {
		t.Errorf("output %q, want the friendly already-exists message", buf.String())
	}
}

func TestRunBackupAndIntegrityFlagsMutuallyExclusive(t *testing.T) {
	clearServerEnv(t)
	var buf bytes.Buffer
	code := run([]string{"-data", "/tmp/nope.db", "-backup", "/x/b.db", "-integrity-check", "/x/i.db"}, &buf)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (output: %s)", code, buf.String())
	}
	if !strings.Contains(buf.String(), "mutually exclusive") {
		t.Errorf("output %q, want mutual-exclusion message", buf.String())
	}
}

func TestRunIntegrityCheckFlag(t *testing.T) {
	clearServerEnv(t)
	ctx := context.Background()

	goodPath := filepath.Join(t.TempDir(), "good.db")
	_, closeDB, err := sqlite.Open(ctx, goodPath) // migrations seed the schema; rows not needed here
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_ = closeDB()

	var buf bytes.Buffer
	if code := run([]string{"-integrity-check", goodPath}, &buf); code != 0 {
		t.Errorf("run(-integrity-check valid) exit = %d, want 0 (output: %s)", code, buf.String())
	} else if !strings.Contains(buf.String(), "integrity_check ok") {
		t.Errorf("output %q, want \"integrity_check ok\"", buf.String())
	}

	badPath := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(badPath, []byte("garbage, not sqlite"), 0o644); err != nil {
		t.Fatalf("seed bad file: %v", err)
	}
	buf.Reset()
	if code := run([]string{"-integrity-check", badPath}, &buf); code != 1 {
		t.Errorf("run(-integrity-check corrupt) exit = %d, want 1 (output: %s)", code, buf.String())
	} else if !strings.Contains(buf.String(), "FAIL") {
		t.Errorf("output %q, want a FAIL line", buf.String())
	}
}
