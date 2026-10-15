package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	t.Parallel()
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

	// The runner recorded exactly the embedded migrations it applied.
	migrationFiles, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	var count int
	var filename string
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(filename) FROM schema_migrations`).Scan(&count, &filename); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != len(migrationFiles) || filename != "migrations/0001_init.sql" {
		t.Errorf("schema_migrations = (%d rows, MIN=%q), want (%d, %q)",
			count, filename, len(migrationFiles), "migrations/0001_init.sql")
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
	t.Parallel()
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

	migrationFiles, gerr := fs.Glob(migrationFS, "migrations/*.sql")
	if gerr != nil {
		t.Fatalf("glob embedded migrations: %v", gerr)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrationFiles) {
		t.Errorf("schema_migrations rows = %d after repeated runs, want %d (one per migration)", count, len(migrationFiles))
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
	t.Parallel()
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

func TestOpenPool_HealthAndMetricsConcurrentlyDuringActiveWriteTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, closePool, err := OpenPool(ctx, filepath.Join(t.TempDir(), "dual-pool-test.db"))
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	t.Cleanup(func() { closePool() })

	// Insert an initial project so tasks can be created
	_, err = pool.Writer.ExecContext(ctx, `INSERT INTO project (id, name, created_at, updated_at) VALUES ('01M00000000000000000000001', 'Test Project', '2026-10-15T00:00:00Z', '2026-10-15T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// 1. Begin a write transaction on Writer pool and hold it open
	tx, err := pool.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin write tx: %v", err)
	}
	defer tx.Rollback()

	// Perform a write inside the transaction without committing
	_, err = tx.ExecContext(ctx, `INSERT INTO task (id, project_id, status, title, created_at, updated_at) VALUES ('01M00000000000000000000002', '01M00000000000000000000001', 'doing', 'In Flight Task', '2026-10-15T00:00:00Z', '2026-10-15T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert task in tx: %v", err)
	}

	// 2. Demonstrate that the Writer pool connection is fully saturated by tx:
	// A new query against Writer with a short timeout must fail because SetMaxOpenConns(1)
	// has 0 available connections.
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	var dummy int
	err = pool.Writer.QueryRowContext(timeoutCtx, "SELECT 1").Scan(&dummy)
	if err == nil {
		t.Fatalf("expected query on busy writer pool to timeout, but it succeeded")
	}

	// 3. Concurrently, Reader pool queries (health check and metrics) MUST succeed immediately!
	const concurrency = 10
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency*2)

	for i := 0; i < concurrency; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			hCtx, hCancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer hCancel()
			if status := HealthPool(hCtx, pool); status != "ok" {
				errCh <- fmt.Errorf("HealthPool = %q, want 'ok'", status)
			}
		}()
		go func() {
			defer wg.Done()
			mCtx, mCancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer mCancel()
			stats, claims, err := CollectTaskMetricsPool(mCtx, pool, time.Now())
			if err != nil {
				errCh <- fmt.Errorf("CollectTaskMetricsPool: %w", err)
				return
			}
			// Snapshot isolation: reader should not see the uncommitted 'doing' task yet
			if stats["doing"] != 0 {
				errCh <- fmt.Errorf("expected 0 doing tasks (snapshot isolation), got %d", stats["doing"])
			}
			if claims != 0 {
				errCh <- fmt.Errorf("expected 0 active claims, got %d", claims)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent reader error while writer tx was active: %v", err)
	}

	// 4. Commit the write transaction and verify reader now sees it
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	stats, _, err := CollectTaskMetricsPool(ctx, pool, time.Now())
	if err != nil {
		t.Fatalf("CollectTaskMetricsPool after commit: %v", err)
	}
	if stats["doing"] != 1 {
		t.Errorf("expected 1 doing task after commit, got %d", stats["doing"])
	}
}
