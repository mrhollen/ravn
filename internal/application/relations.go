package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// RelateAction selects which side of relate_tasks the use case performs. The
// pair maps 1:1 onto the MCP tool's action and REST's POST/DELETE /tasks/{id}/
// relations (ARCHITECTURE §5).
type RelateAction string

const (
	RelateAdd    RelateAction = "add" // upsert the edge (idempotent)
	RelateRemove RelateAction = "remove"
)

func (a RelateAction) valid() bool {
	switch a {
	case RelateAdd, RelateRemove:
		return true
	default:
		return false
	}
}

// Relations implements the relate_tasks use case (ARCHITECTURE §5 POST/DELETE
// /tasks/{id}/relations): adding and removing directed edges between two tasks
// of one project. It depends only on the ports in this package; concrete
// wiring happens in the cmd/* composition roots.
type Relations struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator
}

// NewRelations returns the Relations use case bound to uow, clock, and idgen.
// Like its sibling use cases it mints IDs (relation rows AND their events) and
// stamps timestamps before calling ports (ADR); adapters store values exactly
// as given.
func NewRelations(uow UnitOfWork, clock Clock, idgen IDGenerator) *Relations {
	return &Relations{uow: uow, clock: clock, idgen: idgen}
}

// Relate adds or removes the relation between tasks a (subject) and b (object)
// of kind. Kind is any tool-boundary value — RelationParent included; it is
// normalized to the INVERSE child edge before anything is written ("a is a
// parent of b" ⇔ stored row task_id=b, other_task_id=a, kind='child' = "b's
// parent is a"). Every OTHER stored kind (blocked-by, duplicate-of, related)
// is stored verbatim, directed a→b.
//
// Add semantics (action = RelateAdd): upsert keyed by
// (task_id, other_task_id, kind). Adding an identical existing edge is a
// no-op success — it returns the stored row untouched and appends NO event,
// mirroring the no-op convention of Update/UpdateNotes (retry-safe; the log
// records state changes, not retries). The ≤1-parent rule applies to child
// edges: giving a task that already has a DIFFERENT parent a new one fails
// with domain.ErrParentConflict (a wrapped sentinel whose message names the
// existing parent); re-adding the SAME parent is the idempotent no-op above,
// not an error. An EXCEPTION to the rule: if the existing parent's task is
// ARCHIVED it no longer blocks — a reassignment onto a different active
// parent removes the stale child edge (remove event) and stores the new one
// in the same unit of work, so a task whose parent got archived can always
// take a new one. Validation failures: unknown action or kind → ErrInvalid;
// a == b (self-relation) → ErrInvalid; either task missing → ErrNotFound;
// cross-project pair → ErrInvalid; and for ADDS only, either endpoint
// archived → ErrInvalid naming the archived task(s). REMOVES are NOT subject
// to the archived guard: an existing edge touching an archived task can
// always be cleaned up (e.g. unrelating a stale parent), so delete stays
// possible even after the related task is archived.
//
// Remove semantics (action = RelateRemove): deletes exactly the stored edge —
// for the 'parent' alias that is the inverse child edge, so removing "b as a's
// parent" and removing "a as b's child" delete the same row. A missing edge →
// domain.ErrNotFound; no event is appended (nothing changed). The returned
// relation carries the deleted edge's (TaskID, OtherTaskID, Kind) with zero
// ID/CreatedAt — callers never address relations by ID anyway.
//
// Events: every real change appends ONE relation-changed event in the SAME
// unit of work as the row write (same-tx rule). The event attaches to the
// source task of the stored edge — the task whose outgoing edge set changed —
// and its JSON payload is {"action":"add"|"remove","kind":<stored kind>,
// "other_id":<target>} (After for adds, Before for removes; the other side is
// empty). Relations are child rows: NO task version bump and NO updated_at
// change (version-bump policy).
func (s *Relations) Relate(ctx context.Context, actor domain.Actor, aID, bID domain.ID, kind domain.RelationKind, action RelateAction) (domain.Relation, bool, error) {
	if !action.valid() {
		return domain.Relation{}, false, fmt.Errorf("%w: unknown relation action %q", domain.ErrInvalid, action)
	}
	if !kind.Valid() {
		return domain.Relation{}, false, fmt.Errorf("%w: unknown relation kind %q", domain.ErrInvalid, kind)
	}
	if aID == bID { // includes both-zero; self-edges are meaningless and rejected by the schema too
		return domain.Relation{}, false, fmt.Errorf("%w: a task cannot relate to itself (%s)", domain.ErrInvalid, aID)
	}
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Relation{}, false, err
	}

	src, dst, storedKind := normalizeRelation(aID, bID, kind)

	now := s.clock.Now()
	var out domain.Relation
	var created bool
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		at, terr := r.Tasks.Get(ctx, aID) // existence + project for BOTH ends
		if terr != nil {
			return terr // ErrNotFound for unknown subjects
		}
		bt, terr := r.Tasks.Get(ctx, bID)
		if terr != nil {
			return terr // ErrNotFound for unknown objects
		}
		if at.ProjectID != bt.ProjectID {
			return fmt.Errorf("%w: tasks %s and %s are in different projects", domain.ErrInvalid, aID, bID)
		}
		// Archived-task guard (adds only): no NEW relation may involve an
		// archived task — neither as subject nor object. Removes still work
		// past archived endpoints so stale edges stay cleanable.
		if action == RelateAdd && (at.Archived || bt.Archived) {
			var archived []string
			if at.Archived {
				archived = append(archived, string(aID))
			}
			if bt.Archived {
				archived = append(archived, string(bID))
			}
			return fmt.Errorf("%w: cannot add a relation involving archived task %s", domain.ErrInvalid, strings.Join(archived, ", "))
		}
		srcTask := &at
		if src == bID { // parent alias flipped the stored direction; the event/parent-check source is task b
			srcTask = &bt
		}

		switch action {
		case RelateAdd:
			return s.addEdge(ctx, &r, a, src, dst, storedKind, now, *srcTask, &out, &created)
		default: // RelateRemove
			return s.removeEdge(ctx, &r, a, src, dst, storedKind, now, *srcTask, &out)
		}
	})
	if err != nil {
		return domain.Relation{}, false, err
	}
	return out, created, nil
}

// addEdge implements RelateAdd inside the caller's unit of work. It first
// distinguishes the idempotent no-op (identical edge already stored → return
// it, no event) from a real change; for child edges it then enforces the
// ≤1-parent rule against the current edge set before upserting — with one
// exception: an existing child edge whose parent task is ARCHIVED is stale
// residue (the archived guard forbids touching it further), so reassignment
// past it REMOVES that row (remove event, same tx) and proceeds with the new
// edge. For blocked-by and child relations, it enforces cycle detection so
// circular dependencies or hierarchies cannot be formed. The repository
// additionally backstops the parent rule with the partial unique index (its
// ErrParentConflict is propagated verbatim if a concurrent writer wins the
// check-then-write race — after the stale removal above, at most one live
// child row per task exists, so the replacement insert cannot violate it).
func (s *Relations) addEdge(ctx context.Context, r *Repos, actor domain.Actor, src, dst domain.ID, kind domain.RelationKind, now time.Time, srcTask domain.Task, out *domain.Relation, created *bool) error {
	edges, err := r.Relations.ListByTask(ctx, src)
	if err != nil {
		return err
	}
	var dropped domain.Relation // removeEdge's out param for stale-edge replacement
	for _, e := range edges {
		switch {
		case e.TaskID == src && e.OtherTaskID == dst && e.Kind == kind:
			*out = e // identical edge already stored: idempotent no-op, nothing written, no event
			*created = false
			return nil
		case e.TaskID == src && e.Kind == domain.RelationChild && kind == domain.RelationChild && e.OtherTaskID != dst:
			if parent, perr := r.Tasks.Get(ctx, e.OtherTaskID); perr == nil && parent.Archived {
				// Stale edge to an archived parent: replace it so reassignment
				// is possible (the ≤1-parent rule + DB unique index allow only
				// one stored child row per task). removeEvent appended here;
				// the add event for the new parent follows below.
				if err := s.removeEdge(ctx, r, actor, src, e.OtherTaskID, domain.RelationChild, now, srcTask, &dropped); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("%w: task %s already has a parent (%s)", domain.ErrParentConflict, src, e.OtherTaskID)
		}
	}

	if kind == domain.RelationBlockedBy || kind == domain.RelationChild {
		reachable, err := s.isReachable(ctx, r, dst, src, kind)
		if err != nil {
			return err
		}
		if reachable {
			return fmt.Errorf("%w: cycle detected: adding %s relation between %s and %s would create a circular dependency", domain.ErrInvalid, kind, src, dst)
		}
	}

	rel := domain.Relation{
		ID:          s.idgen.NewID(),
		TaskID:      src,
		OtherTaskID: dst,
		Kind:        kind,
		CreatedAt:   now,
	}
	if err := r.Relations.Add(ctx, rel); err != nil { // includes ErrParentConflict from the index backstop
		return err
	}

	stored, err := s.storedEdge(ctx, r, src, dst, kind)
	if err != nil {
		return err
	}
	*out = stored
	*created = true

	ev := domain.Event{
		ID:        s.idgen.NewID(),
		ProjectID: &srcTask.ProjectID,
		TaskID:    &src, // the task whose outgoing edge set changed
		Actor:     actor,
		Kind:      domain.EventRelationChanged,
		After: compactJSON(map[string]any{
			"action":   string(RelateAdd),
			"kind":     string(kind),
			"other_id": string(dst),
		}),
		At: now,
	}
	return r.Events.Append(ctx, ev)
}

// removeEdge implements RelateRemove inside the caller's unit of work. The
// repository reports ErrNotFound for a missing edge — propagated untouched (no
// event: nothing changed). A successful delete appends the relation-changed
// event with the edge in its Before JSON (After is empty: the post-state is
// "edge absent").
func (s *Relations) removeEdge(ctx context.Context, r *Repos, actor domain.Actor, src, dst domain.ID, kind domain.RelationKind, now time.Time, srcTask domain.Task, out *domain.Relation) error {
	if err := r.Relations.Remove(ctx, src, dst, kind); err != nil {
		return err // ErrNotFound when the edge does not exist
	}
	*out = domain.Relation{TaskID: src, OtherTaskID: dst, Kind: kind} // ID/CreatedAt unknown — callers don't need them

	ev := domain.Event{
		ID:        s.idgen.NewID(),
		ProjectID: &srcTask.ProjectID,
		TaskID:    &src,
		Actor:     actor,
		Kind:      domain.EventRelationChanged,
		Before: compactJSON(map[string]any{
			"action":   string(RelateRemove),
			"kind":     string(kind),
			"other_id": string(dst),
		}),
		At: now,
	}
	return r.Events.Append(ctx, ev)
}

// storedEdge re-reads the edge just written and returns the STORED row — its
// authoritative ID/created_at (the caller's values on a fresh insert; the first
// writer's on an upsert race). Missing after a successful Add is an internal
// invariant breach, surfaced as a wrapped error rather than swallowed.
func (s *Relations) storedEdge(ctx context.Context, r *Repos, src, dst domain.ID, kind domain.RelationKind) (domain.Relation, error) {
	edges, err := r.Relations.ListByTask(ctx, src)
	if err != nil {
		return domain.Relation{}, err
	}
	for _, e := range edges {
		if e.TaskID == src && e.OtherTaskID == dst && e.Kind == kind {
			return e, nil
		}
	}
	return domain.Relation{}, fmt.Errorf("relation (%s -> %s, %s) vanished after successful upsert", src, dst, kind)
}

// normalizeRelation maps a tool-boundary (subject, object, kind) to the stored
// directed edge. 'parent' ("a is a parent of b") is the inverse of the stored
// hierarchy direction: it lands as the child row task_id=b, other_task_id=a —
// "b's parent is a". Every valid non-parent kind stores verbatim (callers
// validated with RelationKind.Valid() first).
func normalizeRelation(aID, bID domain.ID, kind domain.RelationKind) (src, dst domain.ID, stored domain.RelationKind) {
	if kind == domain.RelationParent {
		return bID, aID, domain.RelationChild
	}
	return aID, bID, kind
}

// isReachable checks whether target can be reached from start via a directed chain
// of edges of the given kind using breadth-first search.
func (s *Relations) isReachable(ctx context.Context, r *Repos, start, target domain.ID, kind domain.RelationKind) (bool, error) {
	if start == target {
		return true, nil
	}
	visited := make(map[domain.ID]bool)
	queue := []domain.ID{start}
	visited[start] = true

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		edges, err := r.Relations.ListByTask(ctx, curr)
		if err != nil {
			return false, err
		}
		for _, e := range edges {
			if e.TaskID == curr && e.Kind == kind {
				if e.OtherTaskID == target {
					return true, nil
				}
				if !visited[e.OtherTaskID] {
					visited[e.OtherTaskID] = true
					queue = append(queue, e.OtherTaskID)
				}
			}
		}
	}
	return false, nil
}

// SubtaskProgress returns (total, done) child task aggregates for each given
// parent task ID in parentIDs: a child task counts if it has a stored
// (child_id, parent_id, 'child') edge and is not archived.
func (s *Relations) SubtaskProgress(ctx context.Context, parentIDs []domain.ID) (map[domain.ID]TaskSubtaskProgress, error) {
	if len(parentIDs) == 0 {
		return map[domain.ID]TaskSubtaskProgress{}, nil
	}
	var out map[domain.ID]TaskSubtaskProgress
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		progress, perr := r.Relations.SubtaskProgressByParents(ctx, parentIDs)
		if perr != nil {
			return perr
		}
		out = progress
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
