package sqlite

import (
	"context"
	"database/sql"
	"errors"

	moderncsqlite "modernc.org/sqlite"
)

// querier is the slice of database/sql every repository method needs. Both
// *sql.DB (autocommit, pool path) and *sql.Tx (inside a UnitOfWork.Run) satisfy
// it, so one repo implementation serves both paths: direct reads run against
// the pool, transactional work runs against the tx handed to UnitOfWork.Repos.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// isConstraintViolation reports whether err is a SQLite constraint failure
// (SQLITE_CONSTRAINT, result code 19 — UNIQUE, NOT NULL, CHECK, FK). The
// driver surfaces these as *moderncsqlite.Error; database/sql passes that
// driver error through, so errors.As finds it in the chain.
func isConstraintViolation(err error) bool {
	var drvErr *moderncsqlite.Error
	if !errors.As(err, &drvErr) {
		return false
	}
	return drvErr.Code() == 19 // SQLITE_CONSTRAINT
}
