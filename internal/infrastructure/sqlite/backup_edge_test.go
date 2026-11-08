package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupTo_CreatesDeepParentDirectories(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	base := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	// Nothing under the base exists yet: every ancestor must be skipped by the
	// symlink walk (not-on-disk → not an escape route) and then created.
	dest := filepath.Join(base, "deep", "deeper", "still.db")
	out, size, err := BackupTo(ctx, src, base, dest)
	if err != nil {
		t.Fatalf("BackupTo with deep missing parents = %v, want success (parents are MkdirAll'd)", err)
	}
	if out != dest || size <= 0 {
		t.Errorf("BackupTo = (%q, %d), want (%q, > 0)", out, size, dest)
	}
	if err := IntegrityCheck(ctx, out); err != nil {
		t.Errorf("integrity check on deep backup: %v", err)
	}
}

func TestBackupTo_RefusesWhenAncestorExistsAsFile(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	base := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("a file where a directory must be"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	// The destination is lexically inside the base, so ResolveBackupDest lets
	// it through; BackupTo's existence stat then fails with ENOTDIR — a plain
	// reported error, neither ErrBackupExists nor a MkdirAll path.
	dest := filepath.Join(blocker, "x.db")
	out, size, err := BackupTo(ctx, src, base, dest)
	if err == nil {
		t.Fatalf("BackupTo over a file-named ancestor = (%q, %d), want an error", out, size)
	}
	if !strings.Contains(err.Error(), "stat backup target") {
		t.Errorf("error = %q, want the stat-target branch naming the ENOTDIR failure", err)
	}
}

func TestBackupTo_VacuumFailureWhenParentDirIsNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission bits; VACUUM INTO would succeed")
	}
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	base := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	parent := filepath.Join(base, "readonly")
	// Plain Mkdir (not MkdirAll): MkdirAll would stamp 0o500 onto `base` too,
	// which is exactly the failure we do NOT want to exercise here.
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatalf("mkdir readonly parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) }) // so t.TempDir cleanup can remove it

	_, _, err := BackupTo(ctx, src, base, filepath.Join(parent, "never.db"))
	if err == nil {
		t.Fatal("BackupTo into a non-writable parent dir = success, want the VACUUM INTO failure")
	}
	if !strings.Contains(err.Error(), "vacuum into") {
		t.Errorf("error = %q, want it to name the failed vacuum-into", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "never.db")); !os.IsNotExist(statErr) {
		t.Errorf("backup file exists despite failure: stat err=%v, want not-exist", statErr)
	}
}

// TestIntegrityCheck_FaultyDatabaseReportsDetail pins the result != "ok" branch:
// a file that OPENS as a valid SQLite database but whose btree pages are
// damaged must fail integrity_check with the fault text in the error.
func TestIntegrityCheck_FaultyDatabaseReportsDetail(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	// Plain driver connection (no ravn pragmas, rollback journal): on close the
	// whole database is in the main file with real leaf pages.
	path := filepath.Join(tmp, "faulty-source.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	must := func(q string, a ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, a...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	must(`CREATE TABLE t (a INTEGER PRIMARY KEY, b TEXT)`)
	var rows strings.Builder
	for i := 0; i < 200; i++ {
		rows.WriteString("(" + strconv.Itoa(i) + `,'` + strings.Repeat("x", 64) + `'),`)
	}
	must(`INSERT INTO t (a, b) VALUES ` + strings.TrimSuffix(rows.String(), ","))
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source db: %v", err)
	}
	pageSize := int(data[16])<<8 | int(data[17])
	if pageSize == 1 {
		pageSize = 65536
	}
	faulty := filepath.Join(tmp, "faulty.db")
	damaged := make([]byte, len(data))
	copy(damaged, data)
	// Zero the second page (the first btree page after the header): verified to
	// yield a btreeInitPage fault while the file still opens as a database.
	s, e := pageSize, 2*pageSize
	for i := s; i < e && i < len(damaged); i++ {
		damaged[i] = 0
	}
	if err := os.WriteFile(faulty, damaged, 0o644); err != nil {
		t.Fatalf("write faulty copy: %v", err)
	}

	err = IntegrityCheck(ctx, faulty)
	if err == nil {
		t.Fatal("IntegrityCheck on page-faulted database = nil, want an error")
	}
	if !strings.Contains(err.Error(), "integrity_check") || !strings.Contains(err.Error(), "failed") {
		t.Errorf("error = %q, want the failed-check detail (which names the fault), not just an open failure", err)
	}
}

// TestIsUnderOrEqual is a direct table test of the containment helper that the
// symlink walk relies on: exact match, strict descendant, and look-alike
// prefixes must all classify correctly.
func TestIsUnderOrEqual(t *testing.T) {
	cases := []struct {
		base, p string
		want    bool
	}{
		{"/base", "/base", true},                 // equal: allowed
		{"/base", "/base/nested/file.db", true},  // strict descendant: allowed
		{"/base", "/base-other/x.db", false},     // look-alike prefix: rejected
		{"/base", "/elsewhere/base/x.db", false}, // base not at start: rejected
		{"/var/backups", "/var", false},          // ancestor of base: rejected
	}
	for _, tc := range cases {
		if got := isUnderOrEqual(tc.base, tc.p); got != tc.want {
			t.Errorf("isUnderOrEqual(%q, %q) = %v, want %v", tc.base, tc.p, got, tc.want)
		}
	}
}
