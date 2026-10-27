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
//
// NOTE: the driver enables EXTENDED result codes on every connection, so
// Error.Code() carries the extended form (e.g. 2067 = SQLITE_CONSTRAINT_UNIQUE
// = 19|(8<<8) for unique violations). Comparing against the bare primary code
// misses them — mask to the low byte (the primary code) first.
func isConstraintViolation(err error) bool {
	var drvErr *moderncsqlite.Error
	if !errors.As(err, &drvErr) {
		return false
	}
	return drvErr.Code()&0xff == 19 // primary code SQLITE_CONSTRAINT; extended bits carry the subtype
}

// isUniqueConstraintViolation reports whether err is a SQLite UNIQUE constraint
// violation (SQLITE_CONSTRAINT_UNIQUE, extended result code 2067 = 19 | (8 << 8)).
func isUniqueConstraintViolation(err error) bool {
	var drvErr *moderncsqlite.Error
	if !errors.As(err, &drvErr) {
		return false
	}
	// SQLITE_CONSTRAINT_UNIQUE is 2067 (19 | (8 << 8))
	return drvErr.Code() == 2067
}
