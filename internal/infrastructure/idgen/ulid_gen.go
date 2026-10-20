// Package idgen provides the application.IDGenerator implementation backed by
// oklog/ulid/v2: current-time, monotonic-per-process ULIDs — sortable, 26-char
// Crockford-base32 strings that are JSON-safe and avoid int64 precision loss
// in JS and agent clients. This is one of the only packages allowed to import
// a third-party module outside cmd/*.
package idgen

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// ULIDGenerator implements application.IDGenerator. Each mint reads 10 entropy
// bytes from crypto/rand — cryptographically secure and unpredictable, unlike
// the math/rand source behind ulid.Make — through a mutex-guarded monotonic
// reader, so mints are strictly lexicographically increasing in issue order
// even within the same millisecond. Safe for concurrent use.
type ULIDGenerator struct {
	mint func() (ulid.ULID, error)
}

// overflowRetries bounds how long NewID retries after ErrMonotonicOverflow.
// Hitting the limit requires more than ~2^48 IDs minted within a single
// millisecond, so this is effectively unreachable; it exists to guarantee
// bounded, predictable retry semantics instead of an unbounded loop or panic.
const overflowRetries = 16

// New returns a ready-to-use ULIDGenerator backed by crypto/rand.
func New() *ULIDGenerator { return newULIDGenerator(newLockedMonotonic()) }

// newULIDGenerator builds a ULIDGenerator from an explicit mint function;
// tests inject fakes to force overflow. Production code uses New.
func newULIDGenerator(mint func() (ulid.ULID, error)) *ULIDGenerator {
	return &ULIDGenerator{mint: mint}
}

// lockedMonotonic pairs the millisecond snapshot and the monotonic entropy
// read under one mutex. ulid.LockedMonotonicReader would only lock the
// entropy read, leaving ulid.Now() taken outside the critical section — a
// stale same-millisecond snapshot from another goroutine could then reset the
// monotonic state and break ordering. Taking Now() inside the lock guarantees
// mints are strictly ordered by issue order across goroutines (given a
// non-decreasing clock).
type lockedMonotonic struct {
	mu    sync.Mutex
	inner *ulid.MonotonicEntropy
}

// newLockedMonotonic builds monotonic entropy over crypto/rand with the safe
// locking above. inc == 0 selects the library's secure default increment.
func newLockedMonotonic() func() (ulid.ULID, error) {
	l := &lockedMonotonic{inner: ulid.Monotonic(rand.Reader, 0)}
	return l.mint
}

// mint atomically mints one ULID at the current millisecond.
func (l *lockedMonotonic) mint() (ulid.ULID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ulid.New(ulid.Now(), l.inner)
}

// NewID implements application.IDGenerator. It never panics on monotonic
// overflow: ErrMonotonicOverflow triggers a one-millisecond sleep and retry,
// up to overflowRetries attempts (the monotonic source re-randomizes at each
// millisecond boundary). If the underlying crypto/rand reader persistently
// errors, NewID panics with the wrapped error rather than minting an insecure
// ID.
func (g *ULIDGenerator) NewID() domain.ID {
	var err error
	for attempt := 0; ; attempt++ {
		id, mintErr := g.mint()
		if mintErr == nil {
			return domain.ID(id.String())
		}
		if !errors.Is(mintErr, ulid.ErrMonotonicOverflow) || attempt >= overflowRetries {
			err = mintErr
			break
		}
		time.Sleep(time.Millisecond)
	}
	panic(fmt.Sprintf("idgen: failed to mint ULID after %d attempts: %v", overflowRetries+1, err))
}

var _ application.IDGenerator = (*ULIDGenerator)(nil)
