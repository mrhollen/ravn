package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveBackupDest_RejectsEscapes pins the hostile-input contract of the
// backup destination validator: empty, relative, traversal and outside-base
// paths must all be refused with ErrBackupPathInvalid — and no file may be
// created anywhere outside the base directory.
func TestResolveBackupDest_RejectsEscapes(t *testing.T) {
	base := filepath.Join(t.TempDir(), "backups") // exists? make it NOT exist: fresh subdir of a temp dir that itself exists
	_ = os.RemoveAll(base)

	cases := []struct {
		name  string
		dest  string
		check func(err error) bool // extra assertion on the error text
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"relative", "backups/evil.db", nil},
		{"bare filename", "evil.db", nil},
		{"outside sibling", filepath.Join(t.TempDir(), "sibling.db"), nil},
		{"traversal dotdot", base + "/../escape.db", func(err error) bool { return strings.Contains(err.Error(), "outside") }},
		{"traversal deep", filepath.Join(base, "..", "..", "etc", "passwd"), nil},
		{"base itself", base, func(err error) bool { return strings.Contains(err.Error(), "itself") }},
		{"NUL byte", "/tmp/x\x00y.db", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ResolveBackupDest(base, tc.dest)
			if !errors.Is(err, ErrBackupPathInvalid) {
				t.Fatalf("ResolveBackupDest(%q) = %q, %v — want ErrBackupPathInvalid", tc.dest, out, err)
			}
			if tc.check != nil && !tc.check(err) {
				t.Errorf("error %q failed extra assertion", err)
			}
		})
	}

	// A valid path (inside base, parent not yet created) must pass.
	ok := filepath.Join(base, "nested", "ok.db")
	out, err := ResolveBackupDest(base, ok)
	if err != nil {
		t.Fatalf("valid nested path rejected: %v", err)
	}
	if out != ok {
		t.Errorf("returned %q, want %q", out, ok)
	}

	// Empty base dir is also a rejection, not a panic.
	if _, err := ResolveBackupDest("", "/abs/x.db"); !errors.Is(err, ErrBackupPathInvalid) {
		t.Errorf("empty base: got %v, want ErrBackupPathInvalid", err)
	}
}

// TestBackupTo_SymlinkEscapeRejected proves the walk through on-disk ancestors:
// a symlinked subdirectory inside the base pointing OUTSIDE must be refused,
// and nothing may land in the escaped directory.
func TestBackupTo_SymlinkEscapeRejected(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	evilDir := filepath.Join(t.TempDir(), "evil")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatalf("mkdir evil: %v", err)
	}
	base := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	link := filepath.Join(base, "innocent-looking")
	if err := os.Symlink(evilDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dest := filepath.Join(link, "pwned.db")
	_, _, err := BackupTo(ctx, src, base, dest)
	if !errors.Is(err, ErrBackupPathInvalid) {
		t.Fatalf("BackupTo via symlink escape = %v, want ErrBackupPathInvalid", err)
	}
	if entries, _ := os.ReadDir(evilDir); len(entries) != 0 {
		t.Errorf("escaped directory is not empty: %v", entries)
	}

	// A symlink inside the base that points to another directory INSIDE the
	// base is legitimate (e.g. volume re-mounts) and must NOT be rejected.
	inner := filepath.Join(base, "real")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	if err := os.Symlink(inner, filepath.Join(base, "alias")); err != nil {
		t.Fatalf("inner symlink: %v", err)
	}
	out, _, err := BackupTo(ctx, src, base, filepath.Join(base, "alias", "fine.db"))
	if err != nil {
		t.Fatalf("BackupTo via in-base alias rejected: %v", err)
	}
	if !strings.HasPrefix(out, base+string(filepath.Separator)) {
		t.Errorf("in-base alias destination = %q, want under %q", out, base)
	}
}

// TestBackupTo_NeverWritesOutsideBase runs BackupTo with a hostile destination
// and verifies NO file appeared outside the base directory afterwards.
func TestBackupTo_NeverWritesOutsideBase(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t)
	seedBackupFixture(t, src)

	tmp := t.TempDir()
	base := filepath.Join(tmp, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	before := listFiles(t, tmp)

	for _, dest := range []string{
		filepath.Join(tmp, "outside.db"),         // sibling of base
		base + "/../../elsewhere.db",             // deep traversal back out
		filepath.Join(base, "..", "traverse.db"), // single traversal step
	} {
		if _, _, err := BackupTo(ctx, src, base, dest); !errors.Is(err, ErrBackupPathInvalid) {
			t.Errorf("dest %q: got %v, want ErrBackupPathInvalid", dest, err)
		}
	}

	after := listFiles(t, tmp)
	if len(after) != len(before) {
		t.Errorf("files outside base changed after hostile attempts: before=%v after=%v", before, after)
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", dir, err)
	}
	return out
}
