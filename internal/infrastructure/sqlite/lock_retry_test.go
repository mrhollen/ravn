package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

// realBusyError produces a genuine SQLITE_BUSY *moderncsqlite.Error: one raw
// handle takes BEGIN EXCLUSIVE (a rollback-journal exclusive lock blocks even
// readers), and a second handle — deliberately opened WITHOUT dsnFor's
// busy_timeout pragma so it does not wait — fails its first read. The captured
// driver error is what retryTransient must recognize in production races.
func realBusyError(t *testing.T) error {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy.db")

	hold, err := sql.Open("sqlite", "file:"+path) // no pragmas: busy_timeout stays 0
	if err != nil {
		t.Fatalf("open hold db: %v", err)
	}
	defer hold.Close()
	hold.SetMaxOpenConns(1)
	for _, stmt := range []string{`CREATE TABLE x (a INTEGER)`, `BEGIN EXCLUSIVE`} {
		if _, err := hold.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if _, err := hold.ExecContext(ctx, `INSERT INTO x VALUES (1)`); err != nil {
		t.Fatalf("insert under exclusive tx: %v", err)
	}

	read, err := sql.Open("sqlite", "file:"+path) // no pragmas here either
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	defer read.Close()
	var n int
	err = read.QueryRowContext(ctx, `SELECT 1`).Scan(&n)

	if _, rbErr := hold.ExecContext(ctx, `ROLLBACK`); rbErr != nil {
		t.Fatalf("rollback exclusive tx: %v", rbErr)
	}
	return err
}

func TestIsTransientLockError(t *testing.T) {
	t.Parallel()
	busy := realBusyError(t)
	if busy == nil {
		t.Fatal("fixture did not produce an error")
	}
	var drvErr *moderncsqlite.Error
	if !errors.As(busy, &drvErr) || drvErr.Code()&0xff != 5 {
		t.Fatalf("fixture produced %T %q (code %v), want a primary-code-5 driver error", busy, busy, safeCode(busy))
	}
	if !isTransientLockError(busy) {
		t.Errorf("isTransientLockError(real SQLITE_BUSY) = false, want true")
	}
	for _, other := range []error{
		errors.New("table status already exists"),
		moderncsqlite.ErrPageCacheConflict, // a real non-busy driver error value
	} {
		if isTransientLockError(other) {
			t.Errorf("isTransientLockError(%v) = true, want false", other)
		}
	}
}

// safeCode renders the driver code for diagnostics without a type assertion.
func safeCode(err error) int {
	var drvErr *moderncsqlite.Error
	if errors.As(err, &drvErr) {
		return drvErr.Code()
	}
	return -1
}

func TestRetryTransientRerunsOnBusyUntilSuccess(t *testing.T) {
	t.Parallel()
	busy := realBusyError(t)
	if busy == nil {
		t.Fatal("fixture did not produce an error")
	}
	calls := 0
	start := time.Now()
	err := retryTransient(context.Background(), "test", func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryTransient returned %v, want nil after success on attempt 3", err)
	}
	if calls != 3 {
		t.Errorf("fn called %d times, want 3 (two busy, one success)", calls)
	}
	// Backoff is 25ms then 50ms; allow margin for scheduling.
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Errorf("returned after %v, expected at least ~75ms of backoff", elapsed)
	}
}

func TestRetryTransientStopsOnNonBusyError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("table status already exists")
	calls := 0
	err := retryTransient(context.Background(), "test", func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("retryTransient = %v, want the original non-busy error unchanged", err)
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1 (no retry for non-busy errors)", calls)
	}
}

func TestRetryTransientHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	busy := realBusyError(t)
	if busy == nil {
		t.Fatal("fixture did not produce an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the post-failure backoff select must notice
	err := retryTransient(ctx, "test", func() error { return busy })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("retryTransient = %v, want context.Canceled", err)
	}
}
