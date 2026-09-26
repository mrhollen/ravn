package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// List pagination constants. They match the REST contract (ARCHITECTURE §5):
// pages of 20 by default, at most 100 rows per page. Values above the max are
// clamped here; TaskRepository.List applies whatever limit it is given.
const (
	defaultListLimit = 20
	maxListLimit     = 100

	// Resume-payload tail limits (GetDetail). Comment limits follow §5's
	// comment_limit contract (default 10, max 50); the event tail defaults to
	// 20 (the slice-2B spec) with a generous 100 cap.
	defaultCommentLimit = 10
	maxCommentLimit     = 50
	defaultEventLimit   = 20
	maxEventLimit       = 100
)

// Claim-lease TTL constants (slice 2C). DefaultClaimTTL applies when a caller
// passes ttlSec = 0 to Claim; maxClaimTTL caps per-call overrides (clamped, not
// rejected) so a misbehaving agent cannot pin a lease open-ended. The default
// is overridable via Tasks.WithDefaultClaimTTL so tests can inject tiny TTLs.
const (
	DefaultClaimTTL = 900 * time.Second // 15 minutes — the documented claim default
	maxClaimTTL     = 3600 * time.Second
)

// Tasks implements the task use cases: create, update, list, get, the one-call
// resume payload (GetDetail), and the atomic TTL-lease lifecycle (Claim,
// Release, Heartbeat). It depends only on the ports in this package; concrete
// wiring happens in the cmd/* composition roots.
type Tasks struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator

	// claimTTL is the default lease duration applied when Claim's ttlSec is 0.
	claimTTL time.Duration
}

// NewTasks returns the Tasks use case bound to uow, clock, and idgen. Use
// cases mint IDs and stamp timestamps before calling ports (ADR); adapters
// store values exactly as given. The default claim TTL is DefaultClaimTTL.
func NewTasks(uow UnitOfWork, clock Clock, idgen IDGenerator) *Tasks {
	return &Tasks{uow: uow, clock: clock, idgen: idgen, claimTTL: DefaultClaimTTL}
}

// WithDefaultClaimTTL returns a copy of s that applies ttl whenever Claim is
// called with ttlSec = 0. Production wiring keeps the DefaultClaimTTL default;
// tests use this to inject tiny TTLs without real time passing.
func (s *Tasks) WithDefaultClaimTTL(ttl time.Duration) *Tasks {
	c := *s
	c.claimTTL = ttl
	return &c
}

// TaskInput carries everything create_task needs (ARCHITECTURE §5 POST /tasks).
// Zero-value fields take their documented defaults. The acting actor is a
// separate parameter, not part of the input.
type TaskInput struct {
	Title       string          // required; trimmed; must be non-empty
	Description string          // markdown; "" allowed
	ProjectID   domain.ID       // required; must name an existing project
	Priority    domain.Priority // 0–5 (validated); 0 = default/lowest
	DueAt       *time.Time      // optional deadline; nil = undated
	Assignee    *domain.Actor   // optional durable owner; nil = unassigned

	// ACs holds the initial acceptance-criterion texts in display order. Each
	// entry is trimmed; empty/whitespace entries are dropped, and the survivors
	// become dense 1-based Seq values. Their IDs are minted here — callers must
	// not pre-populate them.
	ACs []string
}

// TaskUpdateInput carries a partial task update (ARCHITECTURE §5 PATCH
// /tasks/{id}). Pointer semantics: nil = leave unchanged. ClearDue and
// ClearAssignee express the third state plain pointers cannot (explicitly
// clear due_at / assignee); when either is set, the matching value field is
// ignored. An update with no fields set is domain.ErrInvalid — there is
// nothing to write and no event to emit.
type TaskUpdateInput struct {
	Title       *string          // trimmed; must be non-empty
	Description *string          // full markdown replace; "" allowed
	Status      *domain.Status   // must be a canonical status
	Priority    *domain.Priority // must lie in 0–5

	DueAt         *time.Time // explicit deadline value (takes effect unless ClearDue)
	ClearDue      bool       // clear the deadline
	Assignee      *domain.Actor
	ClearAssignee bool // remove the durable owner

	Archived *bool // archive (true) or unarchive (false)
}

// Create validates the input, mints the task ID, stamps timestamps, and stores
// the task row (status=todo, version=1), its initial acceptance criteria, and
// ONE `created` event — all inside a single unit of work. Empty title or a
// missing/unknown project → domain.ErrInvalid; nothing is written on failure.
//
// TODO(slice 2D): duplicate-title warning for open tasks in the same project
// (advisory warnings[] on the response, never an error).
// TODO(slice 2D): parent / blocked-by / related edges at create time — they
// will be written through r.Relations inside this same Run.
func (s *Tasks) Create(ctx context.Context, actor domain.Actor, in TaskInput) (domain.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Task{}, fmt.Errorf("%w: task title must not be empty", domain.ErrInvalid)
	}
	if in.ProjectID.IsZero() {
		return domain.Task{}, fmt.Errorf("%w: project id is required", domain.ErrInvalid)
	}
	if !in.Priority.Valid() {
		return domain.Task{}, fmt.Errorf("%w: priority %d out of range 0–5", domain.ErrInvalid, in.Priority)
	}

	var assignee *domain.Actor
	if in.Assignee != nil {
		a, err := normalizeActor(*in.Assignee)
		if err != nil {
			return domain.Task{}, err
		}
		assignee = &a
	}

	now := s.clock.Now()
	task := domain.Task{
		ID:          s.idgen.NewID(),
		ProjectID:   in.ProjectID,
		Title:       title,
		Description: in.Description,
		Status:      domain.StatusTodo, // tasks are always born in todo
		Priority:    in.Priority,
		DueAt:       copyTime(in.DueAt),
		Assignee:    assignee,
		Version:     1, // fresh task rows start at version 1 (ADR)
		Archived:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	ev := domain.Event{
		ID:        s.idgen.NewID(),
		ProjectID: &task.ProjectID, // always set when derivable
		TaskID:    &task.ID,
		Actor:     actor,
		Kind:      domain.EventCreated,
		After: compactJSON(map[string]any{
			"title":       task.Title,
			"description": task.Description,
			"status":      string(task.Status),
			"priority":    int(task.Priority),
		}),
		At: now,
	}

	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		if _, gerr := r.Projects.Get(ctx, in.ProjectID); errors.Is(gerr, domain.ErrNotFound) {
			return fmt.Errorf("%w: project %s does not exist", domain.ErrInvalid, in.ProjectID)
		} else if gerr != nil {
			return gerr
		}
		if err := r.Tasks.Create(ctx, task); err != nil {
			return err
		}
		criteria, merr := s.mintCriteria(task.ID, in.ACs)
		if merr != nil {
			return merr
		}
		if len(criteria) > 0 {
			if err := r.ACs.Create(ctx, criteria); err != nil {
				return err
			}
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil // adapters store values as-given; task carries the stored row's fields
}

// Update applies a partial field change to an existing task and appends the
// right event(s) in the same unit of work: `status-changed` when the status
// moves (before→after), `updated` for any other changed field (changed fields
// only, before→after), and `assigned` when the durable assignee is set or
// changes. The write itself is version-guarded via TaskRepository.Update: on a
// stale version the *domain.ConflictError carrying the fresh row propagates
// untouched and no event is appended. A no-op update (nothing actually
// differs) returns the current row without bumping the version or emitting an
// event.
func (s *Tasks) Update(ctx context.Context, actor domain.Actor, id domain.ID, in TaskUpdateInput) (domain.Task, error) {
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return domain.Task{}, fmt.Errorf("%w: task title must not be empty", domain.ErrInvalid)
	}
	if in.Status != nil && !(*in.Status).IsCanonical() {
		return domain.Task{}, fmt.Errorf("%w: unknown status %q", domain.ErrInvalid, *in.Status)
	}
	if in.Priority != nil && !in.Priority.Valid() {
		return domain.Task{}, fmt.Errorf("%w: priority %d out of range 0–5", domain.ErrInvalid, *in.Priority)
	}
	var newAssignee *domain.Actor
	if !in.ClearAssignee && in.Assignee != nil {
		a, err := normalizeActor(*in.Assignee)
		if err != nil {
			return domain.Task{}, err
		}
		newAssignee = &a
	}
	hasField := in.Title != nil || in.Description != nil || in.Status != nil ||
		in.Priority != nil || in.DueAt != nil || in.ClearDue || newAssignee != nil ||
		in.ClearAssignee || in.Archived != nil
	if !hasField {
		return domain.Task{}, fmt.Errorf("%w: no task fields to update", domain.ErrInvalid)
	}

	now := s.clock.Now()
	var fresh domain.Task
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		cur, err := r.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}

		next := cur
		fieldBefore := map[string]any{} // `updated` event: before-values of changed fields only
		fieldAfter := map[string]any{}  // ... and their after-values
		statusChanged := in.Status != nil && *in.Status != cur.Status
		assigneeChanged := false

		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t != cur.Title {
				fieldBefore["title"] = cur.Title
				fieldAfter["title"] = t
				next.Title = t
			}
		}
		if in.Description != nil && *in.Description != cur.Description {
			fieldBefore["description"] = cur.Description
			fieldAfter["description"] = *in.Description
			next.Description = *in.Description
		}
		if in.Priority != nil && *in.Priority != cur.Priority {
			fieldBefore["priority"] = int(cur.Priority)
			fieldAfter["priority"] = int(*in.Priority)
			next.Priority = *in.Priority
		}
		if in.ClearDue {
			if cur.DueAt != nil {
				fieldBefore["due_at"] = dueAtJSON(cur.DueAt)
				fieldAfter["due_at"] = nil // cleared
				next.DueAt = nil
			}
		} else if in.DueAt != nil && !timesEqual(cur.DueAt, in.DueAt) {
			fieldBefore["due_at"] = dueAtJSON(cur.DueAt)
			fieldAfter["due_at"] = dueAtJSON(in.DueAt)
			next.DueAt = copyTime(in.DueAt)
		}
		if in.Archived != nil && *in.Archived != cur.Archived {
			fieldBefore["archived"] = cur.Archived
			fieldAfter["archived"] = *in.Archived
			next.Archived = *in.Archived
		}

		if statusChanged {
			wasDone := cur.Status == domain.StatusDone
			next.Status = *in.Status
			nowDone := next.Status == domain.StatusDone
			if !wasDone && nowDone {
				next.CompletedAt = &now // set when status becomes done
			} else if wasDone && !nowDone {
				next.CompletedAt = nil // moving out of done clears the completion stamp
			}
		}

		if in.ClearAssignee {
			if cur.Assignee != nil {
				assigneeChanged = true
				next.Assignee = nil
			}
		} else if newAssignee != nil && !actorEqual(cur.Assignee, newAssignee) {
			assigneeChanged = true
			next.Assignee = newAssignee
		}

		if !statusChanged && !assigneeChanged && len(fieldAfter) == 0 {
			fresh = cur // nothing actually differs: no write, no version bump, no event
			return nil
		}

		next.Version = cur.Version // expected stored version for the optimistic guard
		next.UpdatedAt = now
		stored, err := r.Tasks.Update(ctx, next)
		if err != nil { // includes *domain.ConflictError — propagate without events
			return err
		}

		var evs []domain.Event
		if statusChanged {
			evs = append(evs, domain.Event{
				ID:        s.idgen.NewID(),
				ProjectID: &stored.ProjectID,
				TaskID:    &stored.ID,
				Actor:     actor,
				Kind:      domain.EventStatusChanged,
				Before:    compactJSON(map[string]string{"status": string(cur.Status)}),
				After:     compactJSON(map[string]string{"status": string(stored.Status)}),
				At:        now,
			})
		}
		if len(fieldAfter) > 0 {
			evs = append(evs, domain.Event{
				ID:        s.idgen.NewID(),
				ProjectID: &stored.ProjectID,
				TaskID:    &stored.ID,
				Actor:     actor,
				Kind:      domain.EventUpdated,
				Before:    compactJSON(fieldBefore),
				After:     compactJSON(fieldAfter),
				At:        now,
			})
		}
		if assigneeChanged {
			evs = append(evs, domain.Event{
				ID:        s.idgen.NewID(),
				ProjectID: &stored.ProjectID,
				TaskID:    &stored.ID,
				Actor:     actor,
				Kind:      domain.EventAssigned,
				Before:    compactJSON(map[string]any{"assignee": actorJSON(cur.Assignee)}),
				After:     compactJSON(map[string]any{"assignee": actorJSON(stored.Assignee)}),
				At:        now,
			})
		}
		for _, e := range evs {
			if err := r.Events.Append(ctx, e); err != nil {
				return err
			}
		}
		fresh = stored
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return fresh, nil
}

// List delegates to the repository (canonical order + pagination) after
// clamping Limit/Offset to the REST contract: default 20, max 100. total is
// the number of matching tasks before Limit/Offset are applied.
func (s *Tasks) List(ctx context.Context, f TaskFilter) ([]domain.Task, int64, error) {
	if f.Limit <= 0 {
		f.Limit = defaultListLimit
	} else if f.Limit > maxListLimit {
		f.Limit = maxListLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	var out []domain.Task
	var total int64
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		rows, t, err := r.Tasks.List(ctx, f)
		out, total = rows, t
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Get returns the task with id or domain.ErrNotFound. Reads run through the
// unit of work too (MVP uniformity).
func (s *Tasks) Get(ctx context.Context, id domain.ID) (domain.Task, error) {
	var stored domain.Task
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		t, err := r.Tasks.Get(ctx, id)
		stored = t
		return err
	})
	if err != nil {
		return domain.Task{}, err
	}
	return stored, nil
}

// UpdateNotes performs a REPLACE-ON-WRITE of the task's durable resume notes
// (ARCHITECTURE §5 PUT /tasks/{id}/notes, MCP update_notes): the whole blob is
// replaced by `notes` — no merge, no base-version check at this layer — and
// ONE `noted` event is appended in the same unit of work. Because it IS a task
// row mutation (version-bump policy), the write goes through the version-
// guarded TaskRepository.Update path: version +1 and updated_at = now, no
// other field touched. A missing task → domain.ErrNotFound; replacing with an
// identical blob is a no-op that returns the current row without bumping or
// emitting (same convention as Update's no-op case).
func (s *Tasks) UpdateNotes(ctx context.Context, actor domain.Actor, id domain.ID, notes string) (domain.Task, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Task{}, err
	}

	now := s.clock.Now()
	var stored domain.Task
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		cur, gerr := r.Tasks.Get(ctx, id)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if cur.Notes == notes {
			stored = cur // replace-on-write of the same blob: nothing to write or record
			return nil
		}
		next := cur
		next.Notes = notes
		next.Version = cur.Version // expected stored version for the optimistic guard
		next.UpdatedAt = now
		fresh, uerr := r.Tasks.Update(ctx, next) // stores version+1 + updated_at, returns the fresh row
		if uerr != nil {
			return uerr // includes *domain.ConflictError — propagate without events
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &fresh.ProjectID,
			TaskID:    &fresh.ID,
			Actor:     a,
			Kind:      domain.EventNoted,
			Before:    compactJSON(map[string]string{"notes": cur.Notes}),
			After:     compactJSON(map[string]string{"notes": notes}),
			At:        now,
		}
		stored = fresh
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Task{}, err
	}
	return stored, nil
}

// TaskDetail is the one-call resume payload (ARCHITECTURE §5 GET /tasks/{id}):
// the full task row plus everything an agent needs to pick the work back up —
// ordered acceptance criteria, a comment tail (newest first), all artifacts
// (oldest first), every relation edge touching the task in either direction
// (raw edges; the computed "blocked" flag lands with relations in slice 2D),
// and an event tail (newest first).
type TaskDetail struct {
	Task               domain.Task
	AcceptanceCriteria []domain.AcceptanceCriterion
	Comments           []domain.Comment
	Artifacts          []domain.Artifact
	Relations          []domain.Relation
	Events             []domain.Event
}

// GetDetail assembles the resume payload in a single read-only unit of work.
// commentLimit/eventLimit clamp to their documented ranges (0 = default: 10
// comments / 20 events; max 50 / 100). Missing task → domain.ErrNotFound.
func (s *Tasks) GetDetail(ctx context.Context, id domain.ID, commentLimit, eventLimit int) (TaskDetail, error) {
	if commentLimit <= 0 {
		commentLimit = defaultCommentLimit
	} else if commentLimit > maxCommentLimit {
		commentLimit = maxCommentLimit
	}
	if eventLimit <= 0 {
		eventLimit = defaultEventLimit
	} else if eventLimit > maxEventLimit {
		eventLimit = maxEventLimit
	}

	var d TaskDetail
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		t, err := r.Tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		d.Task = t
		acs, err := r.ACs.ListByTask(ctx, id)
		if err != nil {
			return err
		}
		d.AcceptanceCriteria = acs
		cs, err := r.Comments.ListByTask(ctx, id, commentLimit, 0)
		if err != nil {
			return err
		}
		d.Comments = cs
		arts, err := r.Artifacts.ListByTask(ctx, id)
		if err != nil {
			return err
		}
		d.Artifacts = arts
		rels, err := r.Relations.ListByTask(ctx, id)
		if err != nil {
			return err
		}
		d.Relations = rels
		evs, err := r.Events.ListByTask(ctx, id, eventLimit, 0)
		if err != nil {
			return err
		}
		d.Events = evs
		return nil
	})
	if err != nil {
		return TaskDetail{}, err
	}
	return d, nil
}

// Claim acquires — or idempotently renews — actor's TTL lease on the task in
// ONE unit of work: the atomic CAS write (TaskRepository.Claim) plus exactly one
// `claimed` event. Winning conditions (ARCHITECTURE §4–5, ADR 2026-09-26):
// the lease is free, EXPIRED (takeover / zombie-heal), or already held by actor
// (idempotent re-claim). Each win re-stamps claimed_at/claim_expires_at to
// now/now+ttl, forces status→doing, and bumps version; the `claimed` event's
// After JSON tags the outcome as "fresh", "takeover", or "renewal". Losing to a
// DIFFERENT live holder propagates *domain.ClaimConflictError{Holder, ExpiresAt}
// untouched — its body is the agent's recovery instruction ("wait until X, or
// work another task") — and no event is appended (nothing was mutated). Claim
// NEVER touches the durable Assignee: claimed_by is a separate ephemeral lease
// field; assignment stays in Update (which emits `assigned`).
//
// ttlSec: 0 = DefaultClaimTTL (or the WithDefaultClaimTTL override), >max clamps
// to max, <0 → domain.ErrInvalid.
func (s *Tasks) Claim(ctx context.Context, actor domain.Actor, id domain.ID, ttlSec int) (domain.Task, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Task{}, err
	}
	if ttlSec < 0 {
		return domain.Task{}, fmt.Errorf("%w: claim ttl %d must be >= 0 seconds (0 = default)", domain.ErrInvalid, ttlSec)
	}
	ttl := s.claimTTL
	if ttlSec > 0 {
		ttl = time.Duration(ttlSec) * time.Second
		if ttl > maxClaimTTL { // clamp oversized overrides, don't reject them
			ttl = maxClaimTTL
		}
	}

	now := s.clock.Now()
	var stored domain.Task
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		cur, gerr := r.Tasks.Get(ctx, id) // pre-state for the event payload + outcome tag
		if gerr != nil {
			return gerr // ErrNotFound for unknown IDs; read and CAS share this one tx
		}
		outcome := claimOutcome(cur, a, now)
		st, cerr := r.Tasks.Claim(ctx, id, a, int(ttl.Seconds()), now)
		if cerr != nil {
			return cerr // *domain.ClaimConflictError — propagate verbatim, no event (no mutation)
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &st.ProjectID,
			TaskID:    &st.ID,
			Actor:     a,
			Kind:      domain.EventClaimed, // the claimed event implies doing — no separate status-changed
			Before: compactJSON(map[string]any{
				"status": string(cur.Status),
				"holder": actorJSON(cur.ClaimedBy),
			}),
			After: compactJSON(map[string]any{
				"status":           string(st.Status),
				"holder":           map[string]string{"type": string(a.Type), "name": a.Name},
				"claim_expires_at": dueAtJSON(st.ClaimExpiry),
				"ttl_sec":          int(ttl.Seconds()),
				"claim":            outcome, // fresh | takeover | renewal
			}),
			At: now,
		}
		stored = st
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Task{}, err
	}
	return stored, nil
}

// Release clears actor's lease and moves the task back to todo in ONE unit of
// work: the holder-only CAS write (TaskRepository.Release) plus one `released`
// event. Only the CURRENT active holder may release; a foreign holder — or an
// expired self-lease — gets *domain.ClaimConflictError{Holder, ExpiresAt} with
// nothing mutated (chosen over domain.ErrInvalid on purpose: this is a
// coordination collision whose body carries the live holder + expiry as the
// recovery instruction, not malformed input). Releasing a task with NO lease at
// all is an idempotent no-op returning the fresh row (retry-safe), emitting no
// event and bumping nothing. The note (if any) lands in the event summary, not
// in the task row. The durable Assignee is untouched — like claim, release
// deals only with the ephemeral lease.
func (s *Tasks) Release(ctx context.Context, actor domain.Actor, id domain.ID, note string) (domain.Task, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Task{}, err
	}
	note = strings.TrimSpace(note)

	now := s.clock.Now()
	var stored domain.Task
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		cur, gerr := r.Tasks.Get(ctx, id) // pre-state for the event payload
		if gerr != nil {
			return gerr
		}
		st, rerr := r.Tasks.Release(ctx, id, a, now)
		if rerr != nil {
			return rerr // *domain.ClaimConflictError — propagate verbatim, no event (no mutation)
		}
		if cur.ClaimedBy == nil || cur.ClaimExpiry == nil {
			stored = st // no-op release of an unclaimed task: nothing to record
			return nil
		}
		after := map[string]any{
			"status": string(st.Status),
			"holder": actorJSON(st.ClaimedBy), // null — the lease is cleared
		}
		if note != "" {
			after["note"] = note
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &st.ProjectID,
			TaskID:    &st.ID,
			Actor:     a,
			Kind:      domain.EventReleased,
			Before: compactJSON(map[string]any{
				"status":           string(cur.Status),
				"holder":           actorJSON(cur.ClaimedBy),
				"claim_expires_at": dueAtJSON(cur.ClaimExpiry),
			}),
			After: compactJSON(after),
			At:    now,
		}
		stored = st
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Task{}, err
	}
	return stored, nil
}

// Heartbeat extends actor's ACTIVE lease to now + the STORED TTL and returns
// the fresh row — one call doubles as a health check. Deliberately NO version
// bump, NO updated_at change, and NO event: a keep-alive is not a state change,
// must never cause 409s against a working agent's stale read (version-bump
// policy), and would only be noise in the activity feed. Only the current
// holder of an ACTIVE (unexpired) lease succeeds; a foreign holder or an expired
// lease — including actor's own zombie lease — propagates
// *domain.ClaimConflictError{Holder, ExpiresAt} untouched. A task with no lease
// at all is also a conflict carrying a zero-value Holder: the recovery
// instruction there is "claim it first".
func (s *Tasks) Heartbeat(ctx context.Context, actor domain.Actor, id domain.ID) (domain.Task, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Task{}, err
	}

	now := s.clock.Now()
	var stored domain.Task
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		st, herr := r.Tasks.Heartbeat(ctx, id, a, now)
		if herr != nil {
			return herr // *domain.ClaimConflictError — propagate verbatim; never append events here
		}
		stored = st
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return stored, nil
}

// sameLeaseHolder reports whether actor holds cur's current lease. The NAME is
// the lease-holder key (MVP single-tenant, self-declared names — ADR 2026-09-26);
// the stored claimed_by_type is display-only and may legitimately differ across
// re-claims by the same named actor. This must stay in lockstep with the SQL
// CAS's `claimed_by_name = ?` conditions — it is what keeps the fake adapter,
// the real adapter, and the event classification agreeing on "who is home".
func sameLeaseHolder(cur *domain.Task, actor domain.Actor) bool {
	return cur.ClaimedBy != nil && cur.ClaimedBy.Name == actor.Name
}

// claimOutcome tags a successful claim for the `claimed` event summary (ADR
// 2026-09-26): "fresh" when no prior holder exists; "renewal" when the same
// named actor re-claimed its own ACTIVE lease (idempotent); "takeover" whenever
// a prior lease had EXPIRED — zombie-heal by another actor or the previous
// holder reclaiming their own lapsed lease alike. Classification reads cur at
// the start of the same transaction as the CAS, so under serialized writes it
// can never disagree with the winner the CAS actually elects.
func claimOutcome(cur domain.Task, actor domain.Actor, now time.Time) string {
	if cur.ClaimedBy == nil || cur.ClaimExpiry == nil {
		return "fresh"
	}
	if !cur.ClaimExpiry.Before(now) && sameLeaseHolder(&cur, actor) { // active self-lease (name key)
		return "renewal"
	}
	return "takeover"
}

// --- helpers shared by the task use cases ---------------------------------------

// normalizeActor trims the actor name and validates the type; it returns a
// copy so callers' inputs are never mutated.
func normalizeActor(a domain.Actor) (domain.Actor, error) {
	a.Name = strings.TrimSpace(a.Name)
	if !a.Type.Valid() {
		return domain.Actor{}, fmt.Errorf("%w: unknown actor type %q", domain.ErrInvalid, a.Type)
	}
	if a.Name == "" {
		return domain.Actor{}, fmt.Errorf("%w: actor name must not be empty", domain.ErrInvalid)
	}
	return a, nil
}

// mintCriteria turns the raw AC texts into stored criteria: trimmed, dense
// 1-based seq, fresh IDs. Whitespace-only entries are dropped.
func (s *Tasks) mintCriteria(taskID domain.ID, texts []string) ([]domain.AcceptanceCriterion, error) {
	out := make([]domain.AcceptanceCriterion, 0, len(texts))
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, domain.AcceptanceCriterion{
			ID:     s.idgen.NewID(),
			TaskID: taskID,
			Seq:    len(out) + 1,
			Text:   text,
		})
	}
	return out, nil
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	tc := *t
	return &tc
}

func timesEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func actorEqual(a, b *domain.Actor) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Type == b.Type && a.Name == b.Name
}

// dueAtJSON renders an optional deadline for event payloads: RFC3339 or null.
func dueAtJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// actorJSON renders an optional assignee for event payloads: {"type","name"}
// or null when unassigned.
func actorJSON(a *domain.Actor) any {
	if a == nil {
		return nil
	}
	return map[string]string{"type": string(a.Type), "name": a.Name}
}
