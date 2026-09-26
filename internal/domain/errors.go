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

// ErrParentConflict is the ≤1-parent rule (ARCHITECTURE §5: "one-parent rule",
// REST 409): a task that already has a parent — an existing stored kind='child'
// edge out of it — is given a DIFFERENT one. It is the domain translation of
// the UNIQUE(task_id) partial index on relation WHERE kind='child' (migration
// 0003); the SQLite adapter maps that constraint violation onto this sentinel
// when a concurrent writer wins the race between the use case's pre-check and
// its upsert. Re-setting the SAME parent is NOT a conflict — that path is the
// idempotent upsert no-op in RelationRepository.Add, which returns nil. Wrap it
// with the existing parent so the error stays actionable:
//
//	fmt.Errorf("%w: task %s already has a parent (%s)", ErrParentConflict, ...)
var ErrParentConflict = errors.New("task already has a parent")
