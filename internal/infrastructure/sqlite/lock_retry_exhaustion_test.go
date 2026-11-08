package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// withRetryTuning temporarily shrinks the retry window (attempts + base
// backoff) so exhaustion paths finish in milliseconds instead of ~4 s. It
// restores both values on cleanup. Tests that use it must NOT call
// t.Parallel: they mutate package-level tuning and could otherwise race the
// timing assertions in TestRetryTransientRerunsOnBusyUntilSuccess.
func withRetryTuning(t *testing.T, maxAttempts int, base time.Duration) {
	t.Helper()
	oldMax := lockRetryMaxAttempts
	oldBase := lockRetryBaseBackoff
	lockRetryMaxAttempts = maxAttempts
	lockRetryBaseBackoff = base
	t.Cleanup(func() {
		lockRetryMaxAttempts = oldMax
		lockRetryBaseBackoff = oldBase
	})
}

func TestRetryTransientExhaustsAfterMaxAttempts(t *testing.T) {
	withRetryTuning(t, 4, time.Millisecond)
	busy := realBusyError(t)
	if busy == nil {
		t.Fatal("fixture did not produce an error")
	}
	calls := 0
	err := retryTransient(context.Background(), "exhaust", func() error {
		calls++
		return busy
	})
	if err == nil {
		t.Fatal("retryTransient = nil, want exhaustion error after all attempts are spent on transient lock errors")
	}
	if !strings.Contains(err.Error(), "after 4 attempts") {
		t.Errorf("exhaustion error = %q, want it to name the attempt count (4)", err)
	}
	if !errors.Is(err, busy) && !strings.Contains(err.Error(), busy.Error()) {
		t.Errorf("exhaustion error %q does not carry the underlying lock error %q", err, busy)
	}
	if calls != 4 {
		t.Errorf("fn called %d times, want exactly maxAttempts (4)", calls)
	}
}

func TestRetryTransientCapsBackoffAtMax(t *testing.T) {
	// Base above the cap: the FIRST post-failure doubling must already clamp
	// to lockRetryMaxBackoff, proving the `backoff > max` branch executes.
	withRetryTuning(t, 8, 300*time.Millisecond)
	busy := realBusyError(t)
	if busy == nil {
		t.Fatal("fixture did not produce an error")
	}
	calls := 0
	start := time.Now()
	err := retryTransient(context.Background(), "cap", func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryTransient = %v, want success on attempt 3", err)
	}
	if calls != 3 {
		t.Errorf("fn called %d times, want 3 (two busy, one success)", calls)
	}
	// Sleeps: base(300ms) then capped(250ms). If the cap branch were dead the
	// second sleep would be 600ms; bound the total well under that.
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("returned after %v, want <= ~900ms (300 + capped 250); backoff cap likely not applied", elapsed)
	}
}
