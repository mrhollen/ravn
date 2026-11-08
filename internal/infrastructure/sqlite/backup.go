package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors for the hot-backup path. They are exported so the CLI and
// HTTP layers can map failures to distinct outcomes (exit code / 400 vs 409).
var (
	// ErrBackupPathInvalid marks destinations that are empty, relative, or
	// that lexically/symlink-wise resolve OUTSIDE the allowed backup base
	// directory. The message always names what was rejected and why.
	ErrBackupPathInvalid = errors.New("invalid backup destination")

	// ErrBackupExists marks a target file that is already present — VACUUM INTO
	// never overwrites an existing destination, and ravn pre-checks to give a
	// friendly message instead of the raw SQLite "file exists" error.
	ErrBackupExists = errors.New("backup target already exists")
)

// ResolveBackupDest validates dest against baseDir and returns the clean
// absolute path that will be created. This is the security boundary for every
// hot-backup entry point (ravn-server -backup, POST /api/v1/admin/backup): an
// operator or agent may supply any string, but ONLY a file strictly inside
// baseDir may ever be written. Rules, in order:
//  1. dest must be non-empty after trimming and an ABSOLUTE path;
//  2. the cleaned dest must stay inside the cleaned base directory — lexical
//     "../" escapes are rejected here (rel == ".." or a "../…" prefix);
//  3. every on-disk ancestor of dest is resolved through symlinks and must
//     still land inside the symlink-resolved base directory, so a symlinked
//     subdirectory pointing outside (e.g. base/link -> /etc) is rejected;
//  4. dest naming the base directory itself is rejected — it must be a file
//     path UNDER the base.
//
// A not-yet-existing base directory is allowed: BackupTo MkdirAll's it before
// writing, so first-run "backups don't exist yet" works on both entry points.
func ResolveBackupDest(baseDir, dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", fmt.Errorf("%w: destination path is empty", ErrBackupPathInvalid)
	}
	if !filepath.IsAbs(dest) {
		return "", fmt.Errorf("%w: %q is not an absolute path — pass a full path inside the backup base directory", ErrBackupPathInvalid, dest)
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return "", fmt.Errorf("%w: backup base directory is empty (set RAVN_BACKUP_DIR)", ErrBackupPathInvalid)
	}

	cleanBase := filepath.Clean(base)
	destC := filepath.Clean(dest)
	if destC == cleanBase {
		return "", fmt.Errorf("%w: %q names the backup base directory itself — it must be a file path inside it", ErrBackupPathInvalid, dest)
	}

	// Rule 2: lexical containment against the cleaned base.
	rel, err := filepath.Rel(cleanBase, destC)
	if err != nil {
		return "", fmt.Errorf("resolve %q against %q: %w", destC, cleanBase, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves outside the backup base directory %q", ErrBackupPathInvalid, dest, cleanBase)
	}

	// Rule 3: symlink walk over every existing ancestor between base and dest.
	resolvedBase, err := filepath.EvalSymlinks(cleanBase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Base does not exist yet — BackupTo creates it before VACUUM INTO;
			// there is nothing on disk to escape through.
			return destC, nil
		}
		return "", fmt.Errorf("resolve backup base directory %q: %w", cleanBase, err)
	}
	for _, prefix := range ancestorsUnder(cleanBase, destC) {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			continue // not on disk yet — cannot be an escape route
		}
		if !isUnderOrEqual(resolvedBase, resolved) {
			return "", fmt.Errorf("%w: %q resolves outside the backup base directory via symlink", ErrBackupPathInvalid, prefix)
		}
	}
	return destC, nil
}

// BackupTo writes a ZERO-DOWNTIME consistent snapshot of the source database:
// it runs SQLite's "VACUUM 'main' INTO '<out>'" (SQLite >= 3.27, available in
// modernc.org/sqlite) on db, OUTSIDE any transaction — a plain autocommit
// ExecContext, never inside a BEGIN/COMMIT window. While the snapshot is
// written, readers keep serving from the WAL and new writes are only briefly
// serialized behind the copy; the destination file is one complete,
// self-contained SQLite database (same schema, rows and indexes as the source
// at the snapshot instant).
//
// dest must pass ResolveBackupDest against baseDir (absolute, strictly inside
// the base directory); the destination's parent directories are created. An
// existing target file is NEVER overwritten — this is VACUUM INTO's native
// behavior, and it is pre-checked here to return ErrBackupExists with a
// friendly message instead of the raw SQLite error.
//
// Mechanism note: the validated path is embedded as a single-quoted SQL string
// literal (single quotes doubled), not as a bound parameter — VACUUM INTO
// accepts literals only in modernc's prepare path, and embedding is safe
// precisely because ResolveBackupDest has already rejected every destination
// that is not an absolute file path strictly inside the base directory. No
// dedicated-connection fallback was needed: the driver executes the literal
// form via ExecContext on the regular pool handle (pinned to one connection
// for ravn writers, so no concurrent statement can interleave).
//
// It returns the written path and its size in bytes.
func BackupTo(ctx context.Context, db *sql.DB, baseDir, dest string) (string, int64, error) {
	out, err := ResolveBackupDest(baseDir, dest)
	if err != nil {
		return "", 0, err
	}

	if _, statErr := os.Stat(out); statErr == nil {
		return "", 0, fmt.Errorf("%w: %s — refusing to overwrite an existing file (pick a new name)", ErrBackupExists, out)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", 0, fmt.Errorf("stat backup target %q: %w", out, statErr)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", 0, fmt.Errorf("create parent directory for backup %q: %w", filepath.Dir(out), err)
	}

	stmt := "VACUUM 'main' INTO " + quoteSQLLiteral(out)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return "", 0, fmt.Errorf("vacuum into %q: %w", out, err)
	}

	var n int64
	if fi, statErr := os.Stat(out); statErr == nil {
		n = fi.Size()
	}
	return out, n, nil
}

// IntegrityCheck opens file with a FRESH read-only connection (independent of
// the live pool — never shares state with the source) and runs
// PRAGMA integrity_check; it returns nil iff the answer is exactly "ok". Used
// to verify a hot backup before declaring success.
func IntegrityCheck(ctx context.Context, file string) error {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %q: %w", file, err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check on %q: %w", file, err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check on %q failed: %s", file, result)
	}
	return nil
}

// quoteSQLLiteral renders s as a SQLite single-quoted string literal (embedded
// single quotes doubled). Only ever called with paths that ResolveBackupDest
// has already strictly validated.
func quoteSQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ancestorsUnder returns every strict ancestor directory of child that lies
// between base and child (child's parent first, stopping at base — the base
// itself was already resolved by the caller).
func ancestorsUnder(base, child string) []string {
	var out []string
	for p := filepath.Dir(child); ; p = filepath.Dir(p) {
		if p == base || p == "." || p == string(filepath.Separator) {
			break
		}
		out = append(out, p)
	}
	return out
}

// isUnderOrEqual reports whether p equals base or lies inside it.
func isUnderOrEqual(base, p string) bool {
	if p == base {
		return true
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}
