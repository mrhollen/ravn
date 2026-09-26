package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := Open(ctx, filepath.Join(t.TempDir(), "crowban-test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeDB() })
	return db
}

func TestOpenAppliesMigrationsAndSeedsStatuses(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// (c) the SELECT 1 ping works.
	if got := Health(ctx, db); got != "ok" {
		t.Errorf("Health = %q, want ok", got)
	}

	// (b) the three statuses are seeded as rows with their full columns.
	rows, err := db.QueryContext(ctx,
		`SELECT name, display_name, sort_order, is_terminal FROM status ORDER BY sort_order`)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	defer rows.Close()

	type wantStatus struct {
		name, display string
		order         int
		terminal      bool
	}
	want := []wantStatus{
		{"todo", "Todo", 0, false},
		{"doing", "Doing", 1, false},
		{"done", "Done", 2, true},
	}
	var got []wantStatus
	for rows.Next() {
		var s wantStatus
		if err := rows.Scan(&s.name, &s.display, &s.order, &s.terminal); err != nil {
			t.Fatalf("scan status row: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate status rows: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("status rows = %d (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("status[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Every table from the §4 DDL exists.
	tables := map[string]bool{}
	trows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	for trows.Next() {
		var name string
		if err := trows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	trows.Close()
	for _, wantTable := range []string{
		"status", "project", "task", "acceptance_criterion",
		"comment", "artifact", "relation", "event", "schema_migrations",
	} {
		if !tables[wantTable] {
			t.Errorf("table %q missing from schema", wantTable)
		}
	}

	// Connection pragmas applied: WAL journaling + foreign keys ON.
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("pragma journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	// The runner recorded exactly the three migrations it applied
	// (0001 + 0002 + 0003).
	var count int
	var filename string
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(filename) FROM schema_migrations`).Scan(&count, &filename); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 3 || filename != "migrations/0001_init.sql" {
		t.Errorf("schema_migrations = (%d rows, MIN=%q), want (3, %q)",
			count, filename, "migrations/0001_init.sql")
	}

	// Slice 2D2a: the ≤1-parent partial unique index now targets the canonical
	// 'child' storage direction (0003); the old kind='parent' guard from 0001
	// stays as a no-op. Both indexes must exist, plus the composite triple
	// unique (a table constraint, surfaced as an auto-index).
	indexes := map[string]bool{}
	istmts, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatalf("query sqlite_master indexes: %v", err)
	}
	for istmts.Next() {
		var name string
		if err := istmts.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	istmts.Close()
	for _, wantIndex := range []string{
		"idx_one_parent",                // 0001, kind='parent' (legacy vocabulary; no-op under the locked model)
		"idx_relation_one_parent_child", // 0003, kind='child' — the enforcing ≤1-parent guard
		"idx_relation_target",           // 0001, incoming-edge lookups
	} {
		if !indexes[wantIndex] {
			t.Errorf("index %q missing from schema", wantIndex)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crowban-test.db")
	db, closeDB, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	// Second run against the same handle: nothing pending, no error.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate on same handle: %v", err)
	}

	// Simulated process restart: a fresh handle on the same file also sees no
	// pending migrations and changes nothing.
	reopened, closeReopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("re-open database: %v", err)
	}
	defer closeReopened()
	if err := Migrate(ctx, reopened); err != nil {
		t.Fatalf("Migrate on re-opened handle: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("schema_migrations rows = %d after repeated runs, want 3 (one per migration)", count)
	}
	var statuses int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM status`).Scan(&statuses); err != nil {
		t.Fatal(err)
	}
	if statuses != 3 {
		t.Errorf("status rows = %d after repeated runs, want 3 (seeds must not duplicate)", statuses)
	}
}

func TestHealthReportsErrorForClosedDB(t *testing.T) {
	ctx := context.Background()
	db, closeDB, err := Open(ctx, filepath.Join(t.TempDir(), "crowban-test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if got := Health(ctx, db); got != "ok" {
		t.Fatalf("Health on live db = %q, want ok", got)
	}
	if err := closeDB(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := Health(ctx, db)
	if !strings.HasPrefix(got, "error:") {
		t.Errorf("Health on closed db = %q, want it to start with error:", got)
	}
}
