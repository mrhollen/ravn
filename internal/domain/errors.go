package domain

import (
	"errors"
	"fmt"
	"time"
)

// Shared error vocabulary. The application layer raises these; adapters return
// them verbatim; the REST adapter maps them to status codes (ARCHITECTURE §5).

// ErrNotFound is returned by repository ports and the event store when an ID
// does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalid marks caller-supplied input that fails validation (unknown actor
// type, out-of-range priority, disallowed comment/artifact/relation kind,
// cross-project relation, ...). Wrap it at the boundary that knows the detail:
//
//	fmt.Errorf("%w: task title is required", ErrInvalid)
var ErrInvalid = errors.New("invalid input")

// ConflictError is returned when a version-guarded write loses to a concurrent
// one. Current carries the fresh row (new Version and UpdatedAt) so callers
// can rebase instead of blind-retrying. The REST adapter maps it to 409 with
// error="version_conflict" carrying the current row.
type ConflictError struct {
	Current Task
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("version conflict: stored version %d, updated at %s",
		e.Current.Version, e.Current.UpdatedAt.UTC().Format(time.RFC3339))
}

// ClaimConflictError is returned when a claim — or a holder-only release /
// heartbeat — collides with a live lease held by someone else. Holder and
// ExpiresAt double as the recovery instruction for agents ("wait until <expiry>,
// or work another task"), so they always travel with the error. The REST
// adapter maps it to 409 with error="already_claimed".
type ClaimConflictError struct {
	Holder    Actor
	ExpiresAt time.Time
}

func (e *ClaimConflictError) Error() string {
	return fmt.Sprintf("task claimed by %s %q until %s",
		e.Holder.Type, e.Holder.Name, e.ExpiresAt.UTC().Format(time.RFC3339))
}
