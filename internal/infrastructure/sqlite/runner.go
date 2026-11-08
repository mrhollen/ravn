package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every pending embedded migration in filename order — zero-
// padded prefixes make lexical order version order — each in its own
// transaction, recording it in schema_migrations. It is safe to call
// repeatedly: already-applied files are skipped (idempotent). New migrations
// are new files; applied ones are never edited (append-only log).
//
// Each migration runs under BEGIN IMMEDIATE (see applyMigration) so that two
// processes booting concurrently against the same fresh DB file cannot both
// read "not applied" and both execute the DDL — which used to surface as
// "table ... already exists" once the deferred transactions raced past their
// schema_migrations checks. The lock is taken before the applicability check,
// and the check is repeated inside the locked transaction, so exactly one
// applier wins each file; losers observe the recorded row and skip it.
//
// A whole pass is re-run with backoff on transient SQLITE_BUSY errors: when
// several processes open a fresh file simultaneously, one of them switches the
// journal mode to WAL while the others' connections still try their own
// pragma setup, and that exclusive-lock window rejects concurrent statements
// immediately (the busy timeout does not wait it out). Every step here is
// idempotent or resumable from schema_migrations, so re-running the pass is
// safe.
func Migrate(ctx context.Context, db *sql.DB) error {
	return retryTransient(ctx, "migrate", func() error { return migratePass(ctx, db) })
}

func migratePass(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}

	for _, name := range files { // lexical order == version order
		if err := applyMigration(ctx, db, name); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration applies one migration file — or skips it when a concurrent
// applier already recorded it.
//
// The transaction is started with an explicit BEGIN IMMEDIATE instead of the
// driver's default deferred begin. A deferred transaction only upgrades to the
// write (RESERVED) lock at its first write statement, so two processes could
// both pass the schema_migrations check and then collide on the DDL.
// BEGIN IMMEDIATE grabs the RESERVED lock up front: a concurrent applier is
// blocked on that single statement until we commit or roll back, and when it
// finally proceeds it re-reads schema_migrations inside the lock and sees our
// row — so each migration is applied exactly once. (The modernc driver also
// supports a _txlock=immediate DSN option for the same effect; an explicit
// statement was chosen because Migrate only receives a *sql.DB — no path to
// reopen with a different DSN — and keeping it here limits immediate-locking
// to the migration path instead of every app transaction.)
func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	body, err := migrationFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	// Pin the handle's connection for this step so no other statement can
	// interleave on it mid-migration.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn for %s: %w", name, err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate for %s: %w", name, err)
	}
	var committed bool
	defer func() {
		if !committed {
			// Best-effort leave the pooled connection out of an open
			// transaction; if even this fails the caller propagates the
			// original error and closes the handle (Open does so on any
			// Migrate failure), discarding the poisoned connection.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var applied int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&applied); err != nil {
		return fmt.Errorf("check %s: %w", name, err)
	}
	if applied > 0 {
		// A concurrent applier (another process booting against this same
		// file) recorded the migration while we waited for the lock. Commit
		// the empty transaction and move on — skipped, not re-applied.
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("commit skipped %s: %w", name, err)
		}
		committed = true
		return nil
	}

	if _, err := conn.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO schema_migrations(filename, applied_at) VALUES (?, ?)`,
		name, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	committed = true
	return nil
}
