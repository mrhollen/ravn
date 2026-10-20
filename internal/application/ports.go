// Package application holds ravn's use cases and the ports they depend on.
// It imports domain (and stdlib) only — never infrastructure: this file is the
// dependency-inversion seam of the onion. Concrete adapters live in
// internal/infrastructure/** and are wired into these interfaces by the cmd/*
// composition roots.
//
// Slices 2A–2D2b have landed the Projects, Tasks (incl. the claim-lease
// lifecycle), Comments, Acceptance, Artifacts, and Relations use cases against
// these ports — plus the computed blocked/unblocked semantics of slice 2D2b
// (TaskRepository.BlockersOf + TaskFilter.UnblockedOnly).
package application

import (
	"context"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
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
//   - Event Before/After payloads are compact JSON built with encoding/json —
//     pure data shaping, not I/O — so it is allowed alongside context/time/
//     errors/fmt in use cases despite the narrow stdlib import list in §2.
//   - Transactional note: every atomic group of writes (e.g. create_task's
//     task row + initial criteria + relations, ARCHITECTURE §5) runs inside one
//     UnitOfWork.Run (uow.go), which executes the work in a single database
//     transaction. RULE: every mutation's EventStore.Append happens in the SAME
//     transaction as the write it records — data and event commit or roll back
//     together. Use cases stay agnostic to how transactions are implemented.

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
	// List returns all projects matching filter ordered by created_at ASC (id ASC as
	// deterministic tie-break). Per-status task counts are computed by use
	// cases from TaskRepository, not here.
	List(ctx context.Context, filter ...ProjectFilter) ([]domain.Project, error)
	// Update renames and/or rewrites the description; it is not
	// version-guarded (the project row has no version column).
	Update(ctx context.Context, p domain.Project) error
}

// ProjectFilter narrows ProjectRepository.List.
// Archived selects which archive state is listed: false (zero value) =
// open projects only, the REST default; true = archived projects only.
type ProjectFilter struct {
	Archived bool
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

	// Priority matches tasks with exactly this priority (0–5). Pointer because
	// 0 is both a valid priority and the zero value; nil = any priority.
	Priority *domain.Priority

	// DueBefore / DueAfter bound the deadline window on due_at: only tasks
	// whose due_at is strictly before DueBefore / at-or-after DueAfter match.
	// Tasks without a deadline never match either bound (SQL NULL semantics).
	DueBefore *time.Time
	DueAfter  *time.Time

	// Archived selects which archive state is listed: false (zero value) =
	// open tasks only, the REST default; true = archived tasks only.
	Archived bool

	// UnblockedOnly restricts to tasks that can actually be started: a task is
	// EXCLUDED when it has an outgoing blocked-by edge whose target is not done
	// and not archived (slice 2D2b). Only DIRECT blockers count — no transitive
	// closure in MVP. The total returned by List reflects the SAME predicate as
	// the rows, and the filter composes with every other field. "Unblocked"
	// does not itself exclude done tasks; callers combine it with status
	// filters for actionable open work.
	UnblockedOnly bool

	Q string // case-insensitive LIKE over title+description+notes

	CreatedAfter    *time.Time
	CreatedBefore   *time.Time
	CompletedAfter  *time.Time
	CompletedBefore *time.Time

	Limit  int // 0 = use the default (20); values above the max (100) are clamped by use cases
	Offset int // row offset for pagination
}

// TaskCountRow is one (project, status) aggregate produced by
// TaskRepository.OpenTaskCounts. It is a port-level result shape, not a
// domain entity: nothing persists it — the Projects use case folds rows into
// its per-project counts (ARCHITECTURE §5: GET /projects "rows + per-status
// task counts").
type TaskCountRow struct {
	ProjectID domain.ID
	Status    domain.Status
	Count     int64
}

// TaskTitle is one (id, title) pair produced by TaskRepository.OpenTitles. It
// is a port-level result shape, not a domain entity: the duplicate-advisory
// scan needs titles only — materializing full rows for every open task of a
// big project would be needless memory traffic the scorer never reads.
type TaskTitle struct {
	ID    domain.ID
	Title string
}

// TaskACProgress is one (task, total, done) aggregate produced by
// AcceptanceCriterionRepository.ProgressByTasks. It is a port-level result
// shape, not a domain entity: nothing persists it — board snapshots fold it
// into summary-row ac_total/ac_done fields so cards can render checklist
// progress without loading every task's full criterion list.
type TaskACProgress struct {
	Total int64
	Done  int64
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
	// Create stores a new task exactly as given: IDs and timestamps are
	// minted/stamped by the use case (ADR), which sets Version = 1 for fresh
	// rows. The write is idempotent by unique ID — re-inserting an existing
	// ID leaves the stored row untouched and returns nil, so agent retries
	// cannot corrupt state. Initial criteria/relations are written by their
	// own ports in the same logical transaction (see package note).
	Create(ctx context.Context, t domain.Task) error

	Get(ctx context.Context, id domain.ID) (domain.Task, error)

	// List returns summary rows matching f in canonical order plus the total
	// number of tasks matching f before Limit/Offset are applied. When
	// f.UnblockedOnly is set both the rows and the total honor it (same
	// predicate — slice 2D2b).
	List(ctx context.Context, f TaskFilter) ([]domain.Task, int64, error)

	// OpenTitles returns the (id, title) of EVERY open task in projectID —
	// not done and not archived, the system-wide "open" predicate
	// (ARCHITECTURE §4) — in canonical order. UNBOUNDED: it is the cheap,
	// titles-only pre-selection behind FindDuplicateCandidates, whose scan
	// must consider every open row of the project (no priority-page
	// truncation) while materializing only the two columns the scorer reads.
	OpenTitles(ctx context.Context, projectID domain.ID) ([]TaskTitle, error)

	// OpenTaskCounts returns one row per (project, status) pair that has at
	// least one open (non-archived) task, computed as ONE grouped query:
	// SELECT project_id, status, COUNT(*) FROM task WHERE archived = 0
	// GROUP BY project_id, status. Pairs with zero open tasks simply don't
	// appear; callers fill zero counts for the remaining canonical statuses.
	OpenTaskCounts(ctx context.Context) ([]TaskCountRow, error)

	// BlockersOf returns taskID's OPEN blockers: the targets of its outgoing
	// blocked-by edges whose target is not done and not archived (the exact
	// predicate List's UnblockedOnly guard encodes), in edge-creation order.
	// Only DIRECT blockers count — no transitive closure (MVP). A non-empty
	// result means the task is blocked. Rows are light summaries: only ID,
	// Title and Status are populated; every other field of domain.Task is
	// zero.
	BlockersOf(ctx context.Context, taskID domain.ID) ([]domain.Task, error)

	// Update performs the version-guarded write above and returns the fresh
	// row. It returns *domain.ConflictError (carrying the fresh row) when the
	// guard fails and domain.ErrNotFound when the ID does not exist.
	Update(ctx context.Context, t domain.Task) (domain.Task, error)

	// Claim atomically acquires or refreshes a lease on taskID for actor with
	// a TTL of ttlSec seconds as of now: it succeeds when the lease is free,
	// expired (takeover / zombie-heal), or already held by actor (idempotent
	// refresh — no spurious conflict). On success the row gets status=doing,
	// lease fields re-stamped to now/now+ttlSec, and Version +1. The durable
	// Assignee is NEVER touched (claimed_by is a separate ephemeral field;
	// assignment stays in Update — ADR 2026-09-26). Losers get
	// *domain.ClaimConflictError{Holder, ExpiresAt} with nothing mutated.
	Claim(ctx context.Context, taskID domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error)

	// Release clears the lease and moves the task back to todo, bumping
	// Version. Holder-only against an active or expired lease; a foreign holder
	// gets *domain.ClaimConflictError{Holder, ExpiresAt} with nothing mutated.
	// When force is true, an administrative release clears any lease regardless
	// of holder. A task with no lease at all is an idempotent no-op returning
	// the fresh row (retry-safe). The durable Assignee is never touched.
	Release(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time, force bool) (domain.Task, error)

	// Heartbeat extends claim_expires_at by the stored claim TTL as of now.
	// Holder-only with an ACTIVE lease; a foreign or expired holder — or no
	// lease at all (zero-value Holder on the error) — gets
	// *domain.ClaimConflictError{Holder, ExpiresAt}. Does NOT bump Version and
	// does NOT touch updated_at: keep-alive is not a state change. Returns the
	// fresh row (doubles as a health check).
	Heartbeat(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error)
}

// AcceptanceCriterionRepository persists a task's ordered acceptance-criteria
// checklist. Every mutation here is child-row: none of them bumps the parent
// task version. Seq values are MONOTONIC — appends mint max(seq)+1, deletes
// leave gaps; surviving seqs are never renumbered.
type AcceptanceCriterionRepository interface {
	// Create stores initial criteria for a freshly created task; the caller
	// sets IDs and dense 1-based Seq values.
	Create(ctx context.Context, criteria []domain.AcceptanceCriterion) error

	// ListByTask returns the criteria ordered by Seq ASC (empty slice if none).
	ListByTask(ctx context.Context, taskID domain.ID) ([]domain.AcceptanceCriterion, error)

	// SetDone toggles one criterion addressed by its 1-based seq and records
	// who did it and when; completion fields are cleared again when done is
	// false. Returns the fresh row; unknown (taskID, seq) → domain.ErrNotFound.
	SetDone(ctx context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error)

	// Add stores ONE pre-minted criterion (ID and next Seq set by the use
	// case). Constraint failures — duplicate (task_id, seq) or an unknown task
	// (FK) — surface as a wrapped domain.ErrInvalid.
	Add(ctx context.Context, c domain.AcceptanceCriterion) error

	// UpdateText rewords exactly the criterion at (taskID, seq); done/note/
	// completion fields are untouched. Returns the fresh row; unknown pair →
	// domain.ErrNotFound.
	UpdateText(ctx context.Context, taskID domain.ID, seq int, text string) (domain.AcceptanceCriterion, error)

	// Delete removes exactly the criterion at (taskID, seq) and returns the
	// DELETED row so the caller can carry it on an event payload. Surviving
	// rows keep their seq values (gaps stay). Unknown pair → domain.ErrNotFound.
	Delete(ctx context.Context, taskID domain.ID, seq int) (domain.AcceptanceCriterion, error)

	// ProgressByTasks returns one TaskACProgress per given task — the total
	// criterion count and how many are done — computed by ONE grouped query
	// over all the passed ids (no N+1). Tasks with NO criteria at all are
	// omitted from the map; callers treat a missing key as zero/zero. Empty
	// input returns an empty map without querying. This is the batch read
	// behind the board snapshot's per-card AC-progress badge.
	ProgressByTasks(ctx context.Context, taskIDs []domain.ID) (map[domain.ID]TaskACProgress, error)
}

// CommentRepository appends and reads a task's comment thread. Comments are
// immutable after insert (append-only).
type CommentRepository interface {
	Add(ctx context.Context, c domain.Comment) error
	// ListByTask returns up to limit comments newest first (created_at DESC),
	// skipping offset. Use cases clamp limit (default 10, max 50).
	ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, error)
	// CountByTask returns the total number of comments on taskID BEFORE any
	// paging — the `total` of the paged GET /tasks/{id}/comments envelope.
	CountByTask(ctx context.Context, taskID domain.ID) (int64, error)

	// CountsByTasks returns the total comment count for EACH given task,
	// computed by ONE grouped query over all the passed ids (no N+1). Tasks
	// with zero comments are omitted from the map; callers treat a missing key
	// as zero. Empty input returns an empty map without querying. This is the
	// batch read behind the board snapshot's per-card comment-count badge.
	CountsByTasks(ctx context.Context, taskIDs []domain.ID) (map[domain.ID]int64, error)
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

	// ListAfterProject returns the project's events strictly AFTER after
	// (id > after — ULID order is chronological), oldest first, up to limit.
	// It backs SSE Last-Event-ID replay: a reconnecting client asks for
	// exactly what it missed since its last delivered event id. Events with
	// no project never match. limit <= 0 means unbounded.
	ListAfterProject(ctx context.Context, projectID domain.ID, after domain.ID, limit int) ([]domain.Event, error)
}

// EventPublisher receives committed domain events for fan-out.
type EventPublisher interface {
	Publish(e domain.Event)
}

// EventSubscriber provides subscription channels for streaming domain events.
type EventSubscriber interface {
	// Subscribe returns a channel receiving events scoped to projectID (or all
	// projects if projectID is empty), along with a cancel function for cleanup.
	Subscribe(projectID domain.ID) (events <-chan domain.Event, cancel func())
}

// EventBroker coordinates in-memory event publishing and subscriptions.
type EventBroker interface {
	EventPublisher
	EventSubscriber
	Close() error
}
