package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// db_edge_test.go pins the pool-shape and directory-creation edge paths of
// Open/OpenPool that the happy-path tests never touch: the ":memory:" shortcut
// where Reader and Writer MUST be the same handle (two pools would give two
// independent in-memory databases), and the MkdirAll failure when the data path
// is nested under an existing regular file.

func TestOpenPool_Memory_SharesSingleHandle(t *testing.T) {
	ctx := context.Background()
	pool, closeAll, err := OpenPool(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenPool(:memory:) = %v, want success", err)
	}
	defer func() {
		if cerr := closeAll(); cerr != nil {
			t.Errorf("cleanup: %v", cerr)
		}
	}()

	if pool.Writer == nil || pool.Reader == nil {
		t.Fatalf("pool = %+v, want both handles set", pool)
	}
	if pool.Reader != pool.Writer {
		t.Fatal("Reader and Writer are different handles for :memory: — reads would hit a second empty in-memory database")
	}
	if pool.Path != ":memory:" {
		t.Errorf("pool.Path = %q, want \":memory:\" so the composition root can tell it is not file-backed", pool.Path)
	}

	var one int
	if err := pool.Reader.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("shared handle query: %v", err)
	}
	// Writes must land where reads see them — the whole point of a single handle.
	if _, err := pool.Writer.ExecContext(ctx, `CREATE TABLE shared_probe (x INTEGER)`); err != nil {
		t.Fatalf("writer create table: %v", err)
	}
	var n int
	if err := pool.Reader.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'shared_probe'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("reader sees shared table = (%d, %v), want (1, nil)", n, err)
	}
}

func TestOpen_FailsCleanlyWhenParentPathIsAFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	holder := filepath.Join(tmp, "holder")
	if err := os.WriteFile(holder, []byte("a regular file"), 0o644); err != nil {
		t.Fatalf("write holder file: %v", err)
	}

	_, _, err := Open(ctx, filepath.Join(holder, "nested.db"))
	if err == nil {
		t.Fatal("Open with a file-named parent = success, want the MkdirAll failure")
	}
	if !strings.Contains(err.Error(), "create parent directory") {
		t.Errorf("error = %q, want it to name the failed parent-directory creation", err)
	}

	// OpenPool delegates to Open and must surface the same failure.
	pool, _, perr := OpenPool(ctx, filepath.Join(holder, "nested.db"))
	if perr == nil {
		t.Errorf("OpenPool with a file-named parent = pool %v, want an error", pool)
	}
}
