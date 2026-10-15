// Package sqlite is crowban's persistence adapter and the only place outside
// cmd/* that imports third-party modules — modernc.org/sqlite (pure Go; CGO is
// never enabled, so binaries stay static). The port implementations from later
// slices live here as sibling files.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // driver registered under the name "sqlite"
)

// DefaultReaderMaxOpenConns is the default pool size for concurrent read queries,
// health checks, and metrics in WAL mode.
const DefaultReaderMaxOpenConns = 5

// DefaultReaderMaxIdleConns is the default number of idle connections kept open
// in the reader pool to eliminate connection-setup overhead on probes.
const DefaultReaderMaxIdleConns = 2

// DBPool holds separate connection pools for SQLite operating in WAL mode:
//   - Writer: pinned to SetMaxOpenConns(1) to serialize write transactions and
//     completely eliminate SQLITE_BUSY contention among writers.
//   - Reader: concurrent pool (SetMaxOpenConns(DefaultReaderMaxOpenConns)) allowing
//     health checks (/healthz), metrics scrapes (/metrics), and event replays to execute
//     concurrently without queuing behind long-running write transactions.
type DBPool struct {
	Writer *sql.DB
	Reader *sql.DB
}

// Close closes both writer and reader database handles.
func (p *DBPool) Close() error {
	var errs []error
	if p.Writer != nil {
		if err := p.Writer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close writer: %w", err))
		}
	}
	if p.Reader != nil && p.Reader != p.Writer {
		if err := p.Reader.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close reader: %w", err))
		}
	}
	return errors.Join(errs...)
}

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

// OpenPool opens the SQLite database at path with a dual connection pool:
//  1. Writer pool: SetMaxOpenConns(1) to serialize writes; runs pending migrations.
//  2. Reader pool: SetMaxOpenConns(DefaultReaderMaxOpenConns) to allow non-blocking
//     concurrent reads, health probes, and metrics table scans.
//
// It returns the ready *DBPool and a cleanup Close func.
func OpenPool(ctx context.Context, path string) (*DBPool, func() error, error) {
	writer, closeWriter, err := Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}

	if path == ":memory:" {
		pool := &DBPool{Writer: writer, Reader: writer}
		return pool, closeWriter, nil
	}

	reader, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		_ = closeWriter()
		return nil, nil, fmt.Errorf("open sqlite reader %q: %w", path, err)
	}

	reader.SetMaxOpenConns(DefaultReaderMaxOpenConns)
	reader.SetMaxIdleConns(DefaultReaderMaxIdleConns)

	pool := &DBPool{
		Writer: writer,
		Reader: reader,
	}
	closeAll := func() error {
		return pool.Close()
	}

	if err := reader.PingContext(ctx); err != nil {
		_ = closeAll()
		return nil, nil, fmt.Errorf("ping sqlite reader %q: %w", path, err)
	}

	return pool, closeAll, nil
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
