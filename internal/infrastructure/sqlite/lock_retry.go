package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

// isTransientLockError reports whether err is a transient SQLite lock failure:
// SQLITE_BUSY (primary code 5, including extended variants such as
// SQLITE_BUSY_SNAPSHOT 261). The conflicting hold is short-lived — a write
// transaction or, notably, another connection's rollback→WAL journal-mode
// switch, which takes an exclusive lock that the busy timeout does not wait
// out — so bounded retries with backoff are safe.
func isTransientLockError(err error) bool {
	var drvErr *moderncsqlite.Error
	if !errors.As(err, &drvErr) {
		return false
	}
	return drvErr.Code()&0xff == 5 // primary code SQLITE_BUSY
}

// lockRetryMaxAttempts and lockRetryBaseBackoff are vars (not consts) so tests
// can shrink the retry window without waiting out the full ~4 s production
// worst case; their default values are the production behavior.
var (
	lockRetryMaxAttempts = 16
	lockRetryBaseBackoff = 25 * time.Millisecond
)

const lockRetryMaxBackoff = 250 * time.Millisecond

// retryTransient runs fn, re-running it with capped exponential backoff on
// transient lock errors. Any other error, and context cancellation, are
// returned immediately. Worst-case total wait is ~4 s (25+50+100+200+12*250 ms).
func retryTransient(ctx context.Context, what string, fn func() error) error {
	backoff := lockRetryBaseBackoff
	var lastErr error
	for attempt := 0; ; attempt++ {
		if lastErr = fn(); lastErr == nil || !isTransientLockError(lastErr) {
			return lastErr
		}
		if attempt+1 >= lockRetryMaxAttempts {
			return fmt.Errorf("%s after %d attempts: %w", what, attempt+1, lastErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > lockRetryMaxBackoff {
			backoff = lockRetryMaxBackoff
		}
	}
}
