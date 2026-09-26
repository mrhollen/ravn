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
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}

	for _, name := range files { // lexical order == version order
		var applied int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(filename, applied_at) VALUES (?, ?)`,
			name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}
