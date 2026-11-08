package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// assertMigratedOnce verifies the end state of a concurrent-startup race:
// every embedded migration recorded exactly once, seeds intact, core tables
// present. Any "table ... already exists" / primary-key duplicate would have
// shown up either as an error in Migrate or here.
func assertMigratedOnce(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(files) {
		t.Errorf("schema_migrations rows = %d, want %d (exactly one per embedded migration)", count, len(files))
	}
	var dupes int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT filename FROM schema_migrations GROUP BY filename HAVING COUNT(*) > 1)`).Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != 0 {
		t.Errorf("duplicate schema_migrations rows: %d", dupes)
	}
	var statuses int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM status`).Scan(&statuses); err != nil {
		t.Fatal(err)
	}
	if statuses != 3 {
		t.Errorf("status seed rows = %d, want 3", statuses)
	}
	for _, table := range []string{"project", "task"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %q present %d times, want exactly 1", table, n)
		}
	}
}

// TestMigrateConcurrentHandlesNoDuplicates simulates several processes
// booting simultaneously against one fresh DB file: independent *sql.DB
// handles (each with its own SQLite connection at the OS-file level) run
// Migrate concurrently from a barrier. With deferred transactions, more than
// one of them can pass the schema_migrations check before any commits and
// then fail re-creating tables; under BEGIN IMMEDIATE only one proceeds per
// migration and the rest skip it.
func TestMigrateConcurrentHandlesNoDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-handles.db")

	const handles = 4
	errs := make([]error, handles)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := sql.Open("sqlite", dsnFor(path))
			if err != nil {
				errs[i] = err
				return
			}
			db.SetMaxOpenConns(1)
			errs[i] = Migrate(ctx, db)
			_ = db.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Migrate %d: %v", i, err)
		}
	}

	db, closeDB, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("re-open for verification: %v", err)
	}
	defer closeDB()
	assertMigratedOnce(t, db)
}

// TestMigrateConcurrentProcessesNoDuplicates is the true multi-process variant:
// it re-executes this test binary as children (one per OS process), each of
// which opens the same fresh DB file through Open — the production startup
// path — while its siblings do the same. Asserts no child fails and that the
// resulting schema is exactly once migrated. The race window is real: all
// children create the empty file at nearly the same instant, before any of
// them has recorded a single migration.
func TestMigrateConcurrentProcessesNoDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-processes.db")

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	const nChildren = 4
	type child struct {
		cmd    *exec.Cmd
		output *bytes.Buffer // stdout+stderr; go test writes its failure report to stdout
	}
	children := make([]child, nChildren)
	for i := range children {
		var out bytes.Buffer
		cmd := exec.Command(exe, "-test.run=TestMigrateSubprocessHelper", "-test.count=1")
		cmd.Env = append(os.Environ(), "RAVN_MIGRATE_HELPER_DB="+path)
		cmd.Stdout = &out
		cmd.Stderr = &out
		children[i] = child{cmd: cmd, output: &out}
	}
	// Start every child before waiting on any: maximizes overlap of their
	// migrations on the same empty file.
	for i := range children {
		if err := children[i].cmd.Start(); err != nil {
			t.Fatalf("start child process %d: %v", i, err)
		}
	}
	for i := range children {
		if err := children[i].cmd.Wait(); err != nil {
			t.Errorf("child process %d failed: %v\noutput:\n%s", i, err, children[i].output.String())
		}
	}

	db, closeDB, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open migrated db for verification: %v", err)
	}
	defer closeDB()
	assertMigratedOnce(t, db)
}

// TestMigrateSubprocessHelper is the child side of
// TestMigrateConcurrentProcessesNoDuplicates. It runs only when re-executed by
// that test with RAVN_MIGRATE_HELPER_DB set; in a normal suite run it skips.
func TestMigrateSubprocessHelper(t *testing.T) {
	path := os.Getenv("RAVN_MIGRATE_HELPER_DB")
	if path == "" {
		t.Skip("subprocess helper — driven by TestMigrateConcurrentProcessesNoDuplicates")
	}
	ctx := context.Background()
	_, closeDB, err := Open(ctx, path) // production entry point: mkdir + ping + Migrate
	if err != nil {
		t.Fatalf("Open (migrates the db): %v", err)
	}
	defer closeDB()
}
