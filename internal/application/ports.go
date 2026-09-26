// Package application holds crowban's use cases and the ports they depend on.
// It imports domain (and stdlib) only — never infrastructure: this file is the
// dependency-inversion seam of the onion. Concrete adapters live in
// internal/infrastructure/** and are wired into these interfaces by the cmd/*
// composition roots.
//
// This slice defines the ports only; the use cases themselves (projects,
// tasks, claim, comments, acceptance, artifacts, relations) land in later
// slices and plug straight into these signatures.
package application

import (
	"context"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// Conventions shared by all ports below:
//
//   - Use cases mint IDs (IDGenerator) and stamp timestamps (Clock) before
//     calling ports; adapters store rows exactly as given and never invent
//     values of their own. Where an adapter must stamp atomically (claim
//     lease fields), the "now" is injected as a parameter so tests stay
//     deterministic.
//   - Missing IDs surface as domain.ErrNotFound; version-guarded writes fail
//     with *domain.ConflictError; live-lease collisions with
//     *domain.ClaimConflictError (see internal/domain/errors.go).
//   - All timestamps cross the storage boundary as RFC3339 UTC strings
//     (ARCHITECTURE §4); adapters convert.
//   - Transactional note: create_task must write the task row, its initial
//     criteria, and its relations atomically (ARCHITECTURE §5). The exact seam
//     for that is a later-slice decision (context-carried tx handle vs.
//     composite repository methods); use cases must stay agnostic to whichever
//     lands.

// Clock returns "now" for any operation that needs the current instant, so
// use cases never call time.Now directly and tests can control time.
// Implementations return UTC.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints fresh entity IDs. Implementations produce sortable,
// JSON-safe string identifiers (ULIDs) that avoid int64 precision loss in JS
// and agent clients; domain.ID stays agnostic to how values are produced.
type IDGenerator interface {
	NewID() domain.ID
}

// ProjectRepository persists projects. Get returns domain.ErrNotFound for
// missing IDs; Name is unique (duplicate names fail with a wrapped
// domain.ErrInvalid).
type ProjectRepository interface {
	Create(ctx context.Context, p domain.Project) error
	Get(ctx context.Context, id domain.ID) (domain.Project, error)
	// List returns all projects in stable name ASC order. Per-status task
	// counts are computed by use cases from TaskRepository, not here.
	List(ctx context.Context) ([]domain.Project, error)
	// Update renames and/or rewrites the description; it is not
	// version-guarded (the project row has no version column).
	Update(ctx context.Context, p domain.Project) error
}

// TaskFilter narrows TaskRepository.List. Zero values mean "unconstrained"
// except: Status "" = any status, and Archived false = open tasks only (the
// REST default; pass true explicitly to list archived tasks). Ordering is
// always canonical — priority DESC, due_at ASC nulls-last, created_at ASC —
// which adapters enforce.
type TaskFilter struct {
	ProjectID    domain.ID        // "" = any project
	Status       domain.Status    // "" = any status
	AssigneeType domain.ActorType // "" = any durable assignee type
	AssigneeName string           // "" = any durable assignee name

	UnblockedOnly bool // restrict to tasks with no open (non-done) blocked-by target

	Q string // case-insensitive LIKE over title+description+notes

	CreatedAfter    *time.Time
	CreatedBefore   *time.Time
	CompletedAfter  *time.Time
	CompletedBefore *time.Time

	Limit  int // 0 = use the default (20); values above the max (100) are clamped by use cases
	Offset int // row offset for pagination
}

// TaskRepository persists tasks and their ephemeral claim leases. Concurrency
// notes:
//
//   - Update is version-guarded (optimistic concurrency): it applies only when
//     the stored row's Version equals t.Version, then stores t's fields with
//     Version = t.Version + 1 (version-bump policy). Status transitions are
//     validated by use cases, not here.
//   - Claim/Release/Heartbeat encode the atomic lease CAS; losers and
//     non-holders get *domain.ClaimConflictError carrying holder + expiry.
type TaskRepository interface {
	// Create stores a new task; t.Version must be 0 and CreatedAt/UpdatedAt
	// set by the caller. Initial criteria/relations are written by their own
	// ports in the same logical transaction (see package note).
	Create(ctx context.Context, t domain.Task) error

	Get(ctx context.Context, id domain.ID) (domain.Task, error)

	// List returns summary rows matching f in canonical order plus the total
	// number of tasks matching f before Limit/Offset are applied.
	List(ctx context.Context, f TaskFilter) ([]domain.Task, int64, error)

	// Update performs the version-guarded write above and returns the fresh
	// row. It returns *domain.ConflictError (carrying the fresh row) when the
	// guard fails and domain.ErrNotFound when the ID does not exist.
	Update(ctx context.Context, t domain.Task) (domain.Task, error)

	// Claim atomically acquires or refreshes a lease on taskID for actor with
	// a TTL of ttlSec seconds as of now: it succeeds when the lease is free,
	// expired, or already held by actor (idempotent refresh — no spurious
	// conflict). On success the row gets status=doing, assignee set if
	// unassigned, lease fields set, and Version +1. Losers get
	// *domain.ClaimConflictError{Holder, ExpiresAt}.
	Claim(ctx context.Context, taskID domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error)

	// Release clears the lease and moves the task back to todo, bumping
	// Version. Holder-only; a foreign holder gets *domain.ClaimConflictError.
	Release(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error)

	// Heartbeat extends claim_expires_at by the stored claim TTL as of now.
	// Holder-only; does NOT bump Version (child-row rule). Foreign or expired
	// holders get *domain.ClaimConflictError.
	Heartbeat(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error)
}

// AcceptanceRepository persists a task's ordered acceptance-criteria checklist.
// Toggling never bumps the task version (child-row rule).
type AcceptanceRepository interface {
	// Create stores initial criteria for a freshly created task; the caller
	// sets IDs and dense 1-based Seq values.
	Create(ctx context.Context, criteria []domain.AcceptanceCriterion) error

	// ListByTask returns the criteria ordered by Seq ASC (empty slice if none).
	ListByTask(ctx context.Context, taskID domain.ID) ([]domain.AcceptanceCriterion, error)

	// SetDone toggles one criterion addressed by its 1-based seq and records
	// who did it and when; completion fields are cleared again when done is
	// false. Returns the fresh row; unknown (taskID, seq) → domain.ErrNotFound.
	SetDone(ctx context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error)
}

// CommentRepository appends and reads a task's comment thread. Comments are
// immutable after insert (append-only).
type CommentRepository interface {
	Add(ctx context.Context, c domain.Comment) error
	// ListByTask returns up to limit comments newest first (created_at DESC),
	// skipping offset. Use cases clamp limit (default 10, max 50).
	ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, error)
}

// ArtifactRepository appends and reads a task's evidence attachments
// (append-only; kind/ref/summary are never edited).
type ArtifactRepository interface {
	Add(ctx context.Context, a domain.Artifact) error
	// ListByTask returns all artifacts ordered by created_at ASC.
	ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Artifact, error)
}

// RelationRepository persists the directed task edges (hierarchy, blocked-by,
// duplicate-of, related).
type RelationRepository interface {
	// Add upserts the edge keyed by (TaskID, OtherTaskID, Kind) so retrying
	// agents converge; the caller validates the same-project rule and the
	// one-parent rule before calling.
	Add(ctx context.Context, r domain.Relation) error

	// Remove deletes exactly that edge (the REST DELETE is keyed by the same
	// composite — relation IDs are never exposed); domain.ErrNotFound if it
	// does not exist.
	Remove(ctx context.Context, taskID, otherTaskID domain.ID, kind domain.RelationKind) error

	// ListByTask returns every edge touching taskID in either direction, so
	// get_task can render the relations section and compute "blocked".
	ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Relation, error)
}

// EventStore is the append-only activity log (the `event` table). Use cases
// append one event per mutation AFTER the mutation succeeds; readers page
// newest first.
type EventStore interface {
	Append(ctx context.Context, e domain.Event) error

	// ListByTask returns up to limit events for the task, newest first (ts
	// DESC), skipping offset.
	ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Event, error)

	// ListByProject returns up to limit events project-wide, newest first
	// (ts DESC), skipping offset.
	ListByProject(ctx context.Context, projectID domain.ID, limit, offset int) ([]domain.Event, error)
}
