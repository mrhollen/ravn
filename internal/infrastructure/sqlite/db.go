// Package sqlite is crowban's persistence adapter and the only place outside
// cmd/* that imports third-party modules — modernc.org/sqlite (pure Go; CGO is
// never enabled, so binaries stay static). The port implementations from later
// slices live here as sibling files.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // driver registered under the name "sqlite"
)

// dsnFor builds the DSN with crowban's connection pragmas: WAL journaling, a
// 5-second busy timeout, and foreign keys ON. foreign_keys is a per-connection
// setting in SQLite, so it must ride the DSN (applied on every new
// connection) rather than being set once at open. The file: URI form also
// accepts ":memory:" for throwaway in-process databases.
func dsnFor(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"
}

// Open opens (creating the file if needed) the SQLite database at path,
// applies dsnFor's pragmas, pins the pool to a single connection —
// SetMaxOpenConns(1): crowban is low-QPS with one writer, and this removes
// BUSY contention entirely — then runs pending embedded migrations. It returns
// the ready handle plus a Close func; on any failure the handle is closed
// before returning nil.
func Open(ctx context.Context, path string) (*sql.DB, func() error, error) {
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	closeDB := func() error { return db.Close() }
	if err := db.PingContext(ctx); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	if err := Migrate(ctx, db); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("migrate sqlite %q: %w", path, err)
	}

	return db, closeDB, nil
}
