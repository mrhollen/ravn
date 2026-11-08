package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// seedBackupFixture inserts one project, three tasks (one per status, with a
// live claim on the doing row), two acceptance criteria and one comment via
// raw SQL so every backup test starts from non-trivial rows across five
// tables (plus the seeded status + schema_migrations).
func seedBackupFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`INSERT INTO project(id,name,description,created_at,updated_at)
		 VALUES ('p001','Alpha Backup Project','markdown desc','2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`,
		`INSERT INTO task(id,project_id,status,title,priority,due_at,assignee_type,assignee_name,version,archived,created_at,updated_at)
		 VALUES ('t001','p001','todo','first task',3,'2026-11-09T00:00:00Z','human','sam',0,0,'2026-11-08T00:00:00Z','2026-11-08T00:00:00Z')`,
		`INSERT INTO task(id,project_id,status,title,priority,assignee_type,assignee_name,claimed_by_type,claimed_by_name,claimed_at,claim_expires_at,claim_ttl_sec,version,archived,created_at,updated_at)
		 VALUES ('t002','p001','doing','second task',1,'agent','agent-a','agent','agent-a','2026-11-08T01:00:00Z','2026-11-08T01:15:00Z',900,4,0,'2026-11-08T00:00:00Z','2026-11-08T01:00:00Z')`,
		`INSERT INTO task(id,project_id,status,title,priority,version,archived,created_at,updated_at,completed_at)
		 VALUES ('t003','p001','done','third task',2,7,0,'2026-11-08T00:00:00Z','2026-11-08T02:00:00Z','2026-11-08T02:00:00Z')`,
		`INSERT INTO acceptance_criterion(id,task_id,seq,text,done,note)
		 VALUES ('a001','t001',1,'write the thing',0,'')`,
		`INSERT INTO acceptance_criterion(id,task_id,seq,text,done,note,completed_by_type,completed_by_name,completed_at)
		 VALUES ('a002','t002',1,'prove it works',1,'verified','agent','agent-a','2026-11-08T02:00:00Z')`,
		`INSERT INTO comment(id,task_id,kind,body,actor_type,actor_name,created_at)
		 VALUES ('c001','t003','decision','we keep the gate','human','sam','2026-11-08T02:05:00Z')`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

// schemaSnapshot captures the whole database shape (table + index DDL from
// sqlite_master, skipping the sqlite_* internals and auto-indexes which are
// derived deterministically from table DDL) plus a per-table row count. Both
// sources must be identical for a correct backup.
type schemaSnapshot struct {
	objects map[string]string // "table:project" / "index:idx_task_order" -> sql
	counts  map[string]int64  // table name -> COUNT(*)
}

func snapshotSchema(t *testing.T, ctx context.Context, db *sql.DB) schemaSnapshot {
	t.Helper()
	snap := schemaSnapshot{objects: map[string]string{}, counts: map[string]int64{}}

	rows, err := db.QueryContext(ctx,
		`SELECT type, name, COALESCE(sql,'') FROM sqlite_master
		 WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("snapshot sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			rows.Close()
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		snap.objects[typ+":"+name] = sqlText
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	rows.Close()

	sort.Strings(tables)
	for _, table := range tables {
		var n int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+"`"+table+"`").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		snap.counts[table] = n
	}
	return snap
}

func (s schemaSnapshot) equal(other schemaSnapshot) string {
	if !reflect.DeepEqual(s.objects, other.objects) {
		var missing, extra []string
		for k := range s.objects {
			if _, ok := other.objects[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range other.objects {
			if src, ok := s.objects[k]; !ok {
				extra = append(extra, k)
			} else if src != other.objects[k] {
				missing = append(missing, "DIFF "+k)
			}
		}
		return fmt.Sprintf("schema objects differ (source-only/changed: %v, backup-only: %v)", missing, extra)
	}
	if !reflect.DeepEqual(s.counts, other.counts) {
		return fmt.Sprintf("row counts differ: source=%v backup=%v", s.counts, other.counts)
	}
	return ""
}

// openBackupReadonly opens a backup file with its own fresh connection so the
// assertions never share state with the live database.
func openBackupReadonly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open backup %q: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBackupTo_ProducesConsistentIdenticalCopy(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	// Base dir deliberately DOES NOT exist yet (first run), and the target is
	// nested — both must be created by BackupTo.
	base := filepath.Join(t.TempDir(), "backups")
	dest := filepath.Join(base, "snapshots", "backup.db")

	out, n, err := BackupTo(ctx, src, base, dest)
	if err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	if out != dest {
		t.Errorf("returned path = %q, want %q", out, dest)
	}
	fi, statErr := os.Stat(dest)
	if statErr != nil {
		t.Fatalf("backup file missing at %q: %v", dest, statErr)
	}
	if n != fi.Size() {
		t.Errorf("reported size = %d, actual %d", n, fi.Size())
	}
	if n == 0 {
		t.Error("backup file is empty")
	}

	// Fresh connection to the backup: integrity + identical schema + rows.
	bak := openBackupReadonly(t, dest)
	var result string
	if err := bak.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		t.Fatalf("integrity_check on backup: %v", err)
	}
	if result != "ok" {
		t.Fatalf("backup integrity_check = %q, want ok", result)
	}

	want := snapshotSchema(t, ctx, src)
	got := snapshotSchema(t, ctx, bak)
	if diff := want.equal(got); diff != "" {
		t.Errorf("backup diverges from source: %s", diff)
	}

	// Sanity: the fixture rows are actually present in the copy.
	for table, min := range map[string]int64{"project": 1, "task": 3, "acceptance_criterion": 2, "comment": 1, "status": 3} {
		if got.counts[table] < min {
			t.Errorf("backup %s count = %d, want >= %d", table, got.counts[table], min)
		}
	}
}

func TestBackupTo_ExistingTargetFailsAndPreservesContent(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	base := t.TempDir()
	dest := filepath.Join(base, "taken.db")
	sentinel := []byte("pre-existing content — must survive untouched\n")
	if err := os.WriteFile(dest, sentinel, 0o644); err != nil {
		t.Fatalf("seed existing target: %v", err)
	}

	_, _, err := BackupTo(ctx, src, base, dest)
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("BackupTo on existing target = %v, want ErrBackupExists", err)
	}
	got, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("re-read target: %v", rerr)
	}
	if string(got) != string(sentinel) {
		t.Errorf("existing target content changed: %q", got)
	}
}

// TestBackupTo_HotDuringConcurrentWrites is the zero-downtime guarantee: while
// a writer keeps mutating the live DB, BackupTo must produce a snapshot that
// is internally consistent (integrity ok), schema-identical, and whose row
// count lies between the pre-backup and post-backup counts — i.e. a single
// coherent instant of the WAL timeline, never a torn mix.
func TestBackupTo_HotDuringConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	pool, closePool, err := OpenPool(ctx, filepath.Join(t.TempDir(), "ravn-test.db"))
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	defer func() { _ = closePool() }()

	seedBackupFixture(t, pool.Writer)

	countTasks := func() int64 {
		var n int64
		if err := pool.Writer.QueryRowContext(ctx, "SELECT COUNT(*) FROM task").Scan(&n); err != nil {
			t.Fatalf("count tasks: %v", err)
		}
		return n
	}
	pre := countTasks()

	stop := make(chan struct{})
	var failed error
	go func() {
		for i := 0; ; i++ {
			if _, err := pool.Writer.ExecContext(ctx,
				`INSERT INTO task(id,project_id,status,title,version,archived,created_at,updated_at)
				 VALUES (?, 'p001','todo','hot write',0,0,'2026-11-08T03:00:00Z','2026-11-08T03:00:00Z')`,
				fmt.Sprintf("hot%04d", i)); err != nil {
				failed = err
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()

	base := t.TempDir()
	dest := filepath.Join(base, "hot.db")

	// Let a few hot writes land, then take the snapshot mid-stream on the
	// (single-connection) writer handle — inserts serialize behind it.
	time.Sleep(15 * time.Millisecond) // pacing only; well under the 3s test rule
	if _, _, err := BackupTo(ctx, pool.Writer, base, dest); err != nil {
		close(stop)
		t.Fatalf("BackupTo during concurrent writes: %v", err)
	}
	post := countTasks()
	close(stop)

	if failed != nil {
		t.Fatalf("concurrent writer stopped: %v", failed)
	}

	bak := openBackupReadonly(t, dest)
	var result string
	if err := bak.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		t.Fatalf("integrity_check on hot backup: %v", err)
	}
	if result != "ok" {
		t.Fatalf("hot backup integrity_check = %q, want ok", result)
	}
	var n int64
	if err := bak.QueryRowContext(ctx, "SELECT COUNT(*) FROM task").Scan(&n); err != nil {
		t.Fatalf("count tasks in hot backup: %v", err)
	}
	if n < pre || n > post {
		t.Errorf("hot backup task count = %d, want between pre=%d and post=%d", n, pre, post)
	}
}

func TestIntegrityCheck_OKAndCorrupt(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	base := t.TempDir()
	dest := filepath.Join(base, "check.db")
	if _, _, err := BackupTo(ctx, src, base, dest); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	if err := IntegrityCheck(ctx, dest); err != nil {
		t.Errorf("IntegrityCheck on valid backup = %v, want nil", err)
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if err := IntegrityCheck(ctx, corrupt); err == nil {
		t.Error("IntegrityCheck on corrupt file = nil, want error")
	} else if err.Error() == "" {
		t.Error("empty error from corrupt-file check")
	}

	missing := filepath.Join(t.TempDir(), "absent.db")
	if err := IntegrityCheck(ctx, missing); err == nil {
		t.Error("IntegrityCheck on missing file = nil, want error")
	}
}
