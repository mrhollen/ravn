package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// InvariantError reports a domain entity that violates one of its structural
// invariants — e.g. a done task without a completion stamp, a partial claim
// lease, or an acceptance criterion whose Done flag disagrees with its
// completed_by/completed_at pair. It is distinct from ErrInvalid (malformed
// CALLER input): invariant violations are internal-consistency failures that
// the Validate() methods on Task / AcceptanceCriterion / Claim surface and
// the use cases assert against at their mutation sites, so an inconsistent row
// can never reach an adapter.
type InvariantError struct {
	Entity string // "task" | "claim lease" | "acceptance criterion"
	Detail string // which pieces are set/missing and why that is inconsistent
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("%s invariant violated: %s", e.Entity, e.Detail)
}

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

// TaskBlockedError is returned when a claim or a transition to StatusDone is
// refused because the task has OPEN blockers — outgoing blocked-by edges whose
// targets are not done and not archived (the exact predicate List's UnblockedOnly
// guard encodes). Renewals by the current active lease holder are exempt: the
// claim gate only guards fresh starts. BlockerIDs carries the open blocker IDs in
// edge-creation order; it is the machine-readable "why" that travels with the
// error. The REST adapter maps it to 409 with error="task blocked" plus the blocker list.
type TaskBlockedError struct {
	TaskID     ID
	BlockerIDs []ID
}

func (e *TaskBlockedError) Error() string {
	ids := make([]string, len(e.BlockerIDs))
	for i, id := range e.BlockerIDs {
		ids[i] = string(id)
	}
	return fmt.Sprintf("task %s is blocked by open blocker(s): %s",
		e.TaskID, strings.Join(ids, ", "))
}

// TaskUnsatisfiedACsError is returned when a transition to StatusDone is refused
// because the task has unsatisfied acceptance criteria (and override_acs is false).
// UnmetSeqs carries the 1-based sequence numbers of the incomplete criteria.
type TaskUnsatisfiedACsError struct {
	TaskID    ID
	UnmetSeqs []int
}

func (e *TaskUnsatisfiedACsError) Error() string {
	seqStrs := make([]string, len(e.UnmetSeqs))
	for i, s := range e.UnmetSeqs {
		seqStrs[i] = strconv.Itoa(s)
	}
	return fmt.Sprintf("task %s has %d unsatisfied acceptance criteria (seq %s)",
		e.TaskID, len(e.UnmetSeqs), strings.Join(seqStrs, ", "))
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
