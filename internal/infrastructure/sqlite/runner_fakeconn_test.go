package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
)

// The fake driver below lets Migrate run its real code paths (migratePass →
// applyMigration → BEGIN IMMEDIATE → in-tx re-check → DDL → record → COMMIT,
// plus the deferred best-effort ROLLBACK) against a database whose statements
// fail on scripted substrings. Every failure here is a NON-transient error, so
// retryTransient must not retry — each scenario exercises exactly one pass.

var errFakeStmt = errors.New("fake statement failure")

// fakeMigrateConn implements driver.Conn with just enough surface for
// database/sql's Prepare/Exec/Query fallback (no ExecerContext/QueryerContext,
// so every statement round-trips through Prepare).
type fakeMigrateConn struct {
	failOn   []string // substring match: matching statements fail
	countVal int64    // single-column value for SELECT COUNT(*) rows
}

func (c *fakeMigrateConn) fails(q string) bool {
	for _, s := range c.failOn {
		if strings.Contains(q, s) {
			return true
		}
	}
	return false
}

func (c *fakeMigrateConn) Begin() (driver.Tx, error) { return fakeTx{}, nil }
func (c *fakeMigrateConn) Close() error              { return nil }
func (c *fakeMigrateConn) Prepare(q string) (driver.Stmt, error) {
	if c.fails(q) {
		return nil, errFakeStmt
	}
	return &fakeMigrateStmt{conn: c, q: q}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeMigrateStmt struct {
	conn *fakeMigrateConn
	q    string
}

func (s *fakeMigrateStmt) Close() error  { return nil }
func (s *fakeMigrateStmt) NumInput() int { return -1 } // accept any arg count

func (s *fakeMigrateStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.conn.fails(s.q) {
		return nil, errFakeStmt
	}
	return driver.RowsAffected(1), nil
}

func (s *fakeMigrateStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.conn.fails(s.q) {
		return nil, errFakeStmt
	}
	return &fakeMigrateRows{val: s.conn.countVal}, nil
}

// fakeMigrateRows is a one-row, one-column result set (the applied COUNT).
type fakeMigrateRows struct {
	val  int64
	done bool
}

func (r *fakeMigrateRows) Columns() []string { return []string{"applied"} }
func (r *fakeMigrateRows) Close() error      { return nil }
func (r *fakeMigrateRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.val
	return nil
}

// fakeConnector hands every connection the same scripted rules; when
// failAfterFirst is set, only the FIRST Connect succeeds (each statement opens
// a fresh connection because SetMaxIdleConns(0), so later statements — e.g.
// the db.Conn pin in applyMigration — hit the failing second connect).
type fakeConnector struct {
	failOn         []string
	countVal       int64
	failAfterFirst bool
	connects       int
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.connects++
	if c.failAfterFirst && c.connects > 1 {
		return nil, errors.New("second connect fails by script")
	}
	return &fakeMigrateConn{failOn: c.failOn, countVal: c.countVal}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	return nil, errors.New("fake driver: connections only come from the scripted connector")
}

func newFakeDB(t *testing.T, c *fakeConnector) *sql.DB {
	t.Helper()
	db := sql.OpenDB(c)
	// Idle conns are closed after each statement, so every statement — and the
	// db.Conn pin in applyMigration — opens a fresh scripted connection.
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustContain(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Migrate error = %v (%T), want it to contain %q", err, err, want)
	}
}

func firstMigrationBody(t *testing.T) string {
	t.Helper()
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("embedded migrations missing: %v", err)
	}
	body, err := migrationFS.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read first migration: %v", err)
	}
	return string(body)
}

func TestMigrate_BeginImmediateFailure(t *testing.T) {
	db := newFakeDB(t, &fakeConnector{failOn: []string{"BEGIN IMMEDIATE"}})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "begin immediate")
}

func TestMigrate_InTxRecheckFailure(t *testing.T) {
	// The schema_migrations COUNT inside the locked transaction fails.
	db := newFakeDB(t, &fakeConnector{failOn: []string{"FROM schema_migrations WHERE"}})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "check migrations")
}

func TestMigrate_MigrationBodyFailure(t *testing.T) {
	// The first DDL body fails; the matching substring is the WHOLE body so no
	// other statement (BEGIN/COMMIT/COUNT/record INSERT) can collide with it.
	body := firstMigrationBody(t)
	db := newFakeDB(t, &fakeConnector{failOn: []string{body}})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "apply migrations")
}

func TestMigrate_RecordInsertFailure(t *testing.T) {
	db := newFakeDB(t, &fakeConnector{failOn: []string{"INTO schema_migrations"}})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "record migrations")
}

func TestMigrate_CommitAfterApplyFailure(t *testing.T) {
	db := newFakeDB(t, &fakeConnector{failOn: []string{"COMMIT"}})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "commit migrations")
}

func TestMigrate_SkipPath_CommitFailure(t *testing.T) {
	// COUNT reports the migration already applied (a concurrent applier won),
	// so applyMigration takes the skip branch — whose COMMIT is scripted to fail.
	db := newFakeDB(t, &fakeConnector{failOn: []string{"COMMIT"}, countVal: 1})
	err := Migrate(context.Background(), db)
	mustContain(t, err, "commit skipped")
}

func TestMigrate_SkipPath_AllApplied_NoOp(t *testing.T) {
	// Sanity: every migration already recorded → clean no-op pass.
	db := newFakeDB(t, &fakeConnector{countVal: 1})
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate with all migrations pre-recorded = %v, want nil", err)
	}
}

func TestMigrate_ConnAcquireFailure(t *testing.T) {
	// migratePass's CREATE TABLE takes the first (only) connection; the
	// db.Conn pin in applyMigration must then fail to connect.
	c := &fakeConnector{failAfterFirst: true}
	db := newFakeDB(t, c)
	err := Migrate(context.Background(), db)
	mustContain(t, err, "acquire migration conn")
	if c.connects < 2 {
		t.Errorf("connector used %d connects, want >= 2 (one for CREATE TABLE, one failing for the pin)", c.connects)
	}
}
