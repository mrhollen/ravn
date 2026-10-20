package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// Unit tests for the Relations use case (slice 2D2a): parent/child
// normalization, the ≤1-parent rule, idempotent upserts, removal by composite
// key, event emission, and the no-version-bump child-row rule. Fakes + a fixed
// clock throughout; the SQLite integration tests (uow_relations_test.go) prove
// the same semantics over real SQL, including the DB-level index backstop.

var relateTestActor = domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

var relateTestNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // fixed instant for all asserts

func newRelationEnv(t *testing.T) (*Relations, *Tasks, *fakeUoW, *fakeEvents) {
	t.Helper()
	projects := newFakeProjects()
	events := &fakeEvents{}
	uow := newFakeUoW(projects, events)
	clk := newFakeClock(relateTestNow)
	idg := &fakeIDGen{}
	return NewRelations(uow, clk, idg), NewTasks(uow, clk, idg), uow, events
}

// seedRelationWorld creates one project and n tasks inside it directly in the
// fakes (bypassing Tasks.Create keeps ID/version control exact for asserting
// the no-version-bump rule). Returns the task IDs in creation order.
func seedRelationWorld(t *testing.T, uow *fakeUoW, projectTitle string, titles ...string) (domain.ID, []domain.ID) {
	t.Helper()
	now := relateTestNow
	p := domain.Project{ID: "proj-1", Name: projectTitle, CreatedAt: now, UpdatedAt: now}
	uow.projects.byID[p.ID] = p

	var ids []domain.ID
	for i, title := range titles {
		id := domain.ID("task-" + string(rune('a'+i)))
		uow.tasks.byID[id] = domain.Task{
			ID: id, ProjectID: p.ID, Title: title,
			Status: domain.StatusTodo, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		ids = append(ids, id)
	}
	return p.ID, ids
}

// unmarshalEventJSON renders a compact before/after payload to a map (nil for
// the empty string), for asserting exact event-payload contents.
func unmarshalEventJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal event payload %q: %v", s, err)
	}
	return m
}

// edgeTriple is the identity of a stored relation (the composite upsert key).
type edgeTriple struct {
	src, dst domain.ID
	kind     domain.RelationKind
}

func tripleOf(rel domain.Relation) edgeTriple {
	return edgeTriple{rel.TaskID, rel.OtherTaskID, rel.Kind}
}

// TestRelate_AddChildStoresEdgeAndReflectsInGetDetail is the primary happy
// path: child(a→b) persists "a's parent is b", shows up in a's resume payload
// (both directions), appends exactly one relation-changed event with the add
// payload in After, and — the child-row rule — bumps nothing on either task.
func TestRelate_AddChildStoresEdgeAndReflectsInGetDetail(t *testing.T) {
	rels, tasks, uow, events := newRelationEnv(t)
	ctx := context.Background()
	pid, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	rel, created, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd)
	if err != nil {
		t.Fatalf("Relate add child: %v", err)
	}
	if !created {
		t.Errorf("Relate add child created = false, want true")
	}
	want := edgeTriple{a, b, domain.RelationChild}
	if got := tripleOf(rel); got != want {
		t.Errorf("returned edge = %+v, want %+v", got, want)
	}
	if rel.ID == "" || rel.CreatedAt.IsZero() {
		t.Errorf("returned stored edge missing ID/CreatedAt: %+v", rel)
	}

	// The resume payload renders the edge from the subject's side…
	detail, err := tasks.GetDetail(ctx, a, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail(a): %v", err)
	}
	found := false
	for _, e := range detail.Relations {
		if tripleOf(e) == want {
			found = true
		}
	}
	if !found {
		t.Errorf("GetDetail(a).Relations missing child edge %v; got %+v", want, detail.Relations)
	}
	// …and from the other task's side too (ListByTask is both-directional).
	bDetail, err := tasks.GetDetail(ctx, b, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail(b): %v", err)
	}
	found = false
	for _, e := range bDetail.Relations {
		if tripleOf(e) == want {
			found = true
		}
	}
	if !found {
		t.Errorf("GetDetail(b).Relations missing child edge %v; got %+v", want, bDetail.Relations)
	}

	// Child-row rule: relations touch neither task row's version nor updated_at.
	for _, id := range []domain.ID{a, b} {
		got := uow.tasks.byID[id]
		if got.Version != 1 || !got.UpdatedAt.Equal(relateTestNow) {
			t.Errorf("task %s mutated by relation add: version=%d updated_at=%s (want 1 / unchanged)",
				id, got.Version, got.UpdatedAt)
		}
	}

	// Exactly one event, on the stored edge's SOURCE task, with the add payload
	// in After and an empty Before.
	if len(events.events) != 1 {
		t.Fatalf("events = %d (%+v), want exactly 1", len(events.events), events.events)
	}
	ev := events.events[0]
	if ev.Kind != domain.EventRelationChanged {
		t.Errorf("event kind = %q, want relation-changed", ev.Kind)
	}
	if ev.TaskID == nil || *ev.TaskID != a {
		t.Errorf("event TaskID = %v, want %s (stored edge source)", ev.TaskID, a)
	}
	if ev.ProjectID == nil || *ev.ProjectID != pid {
		t.Errorf("event ProjectID = %v, want %s", ev.ProjectID, pid)
	}
	if got := unmarshalEventJSON(t, ev.Before); got != nil {
		t.Errorf("add event Before = %s, want empty", ev.Before)
	}
	wantAfter := map[string]any{"action": "add", "kind": "child", "other_id": string(b)}
	if got := unmarshalEventJSON(t, ev.After); !mapsEqual(got, wantAfter) {
		t.Errorf("add event After = %s, want %v", ev.After, wantAfter)
	}
}

// TestRelate_ParentAliasStoresInverseChildEdge locks down the sugar: "a is a
// parent of b" must store the INVERSE edge (task_id=b, other_task_id=a,
// kind='child') — byte-for-byte the same stored edge as saying "b is a child
// of a". 'parent' itself must never be persisted.
func TestRelate_ParentAliasStoresInverseChildEdge(t *testing.T) {
	rels, _, uow, _ := newRelationEnv(t)
	ctx := context.Background()
	_, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	viaParent, created, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationParent, RelateAdd) // "a is parent of b"
	if err != nil {
		t.Fatalf("Relate add parent: %v", err)
	}
	if !created {
		t.Errorf("Relate add parent created = false, want true")
	}
	want := edgeTriple{b, a, domain.RelationChild} // stored: "b's parent is a"
	if got := tripleOf(viaParent); got != want {
		t.Fatalf("parent-alias stored edge = %+v, want inverse child edge %+v", got, want)
	}

	// Removing it and re-expressing the SAME fact as b-child-of-a must land on
	// the identical stored triple.
	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationParent, RelateRemove); err != nil {
		t.Fatalf("Relate remove parent: %v", err)
	}
	viaChild, created, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationChild, RelateAdd) // "b is child of a"
	if err != nil {
		t.Fatalf("Relate add child: %v", err)
	}
	if !created {
		t.Errorf("Relate add child viaChild created = false, want true")
	}
	if got := tripleOf(viaChild); got != want {
		t.Errorf("child-direction stored edge = %+v, want same inverse edge %+v as the parent alias", got, want)
	}

	for _, e := range uow.relations.edges {
		if e.Kind == domain.RelationParent {
			t.Fatalf("stored row carries kind='parent' (%+v) — never persisted; only a tool-boundary alias", e)
		}
	}
}

// TestRelate_AddSameEdgeTwiceIsIdempotent is the retry-safety contract: the
// second add of an identical edge succeeds, leaves exactly one row, preserves
// the FIRST writer's ID (the stored row is untouched), and appends NO second
// event — the log records state changes, not retries.
func TestRelate_AddSameEdgeTwiceIsIdempotent(t *testing.T) {
	rels, _, uow, events := newRelationEnv(t)
	ctx := context.Background()
	_, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	first, created, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if !created {
		t.Errorf("first add created = false, want true")
	}
	second, created, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd)
	if err != nil {
		t.Fatalf("second add of identical edge must be a no-op success, got: %v", err)
	}
	if created {
		t.Errorf("second add created = true, want false (idempotent no-op)")
	}
	if len(uow.relations.edges) != 1 {
		t.Fatalf("stored edges after double-add = %d (%+v), want exactly 1", len(uow.relations.edges), uow.relations.edges)
	}
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("no-op add returned %+v, want the stored row from the first write (%+v)", second, first)
	}
	if len(events.events) != 1 {
		t.Errorf("events after double-add = %d, want 1 (the no-op appends none)", len(events.events))
	}
}

// TestRelate_TwoDifferentParents_SecondFailsSameParentNoOp locks the ≤1-parent
// rule at the use case: a second, DIFFERENT parent fails with
// domain.ErrParentConflict naming the existing one and stores nothing (no
// event either — the failed add must leave zero trace); re-adding the SAME
// parent is the idempotent no-op, not an error.
func TestRelate_TwoDifferentParents_SecondFailsSameParentNoOp(t *testing.T) {
	rels, _, uow, events := newRelationEnv(t)
	ctx := context.Background()
	_, ids := seedRelationWorld(t, uow, "world", "X", "B", "C")
	x, b, c := ids[0], ids[1], ids[2]

	if _, _, err := rels.Relate(ctx, relateTestActor, x, b, domain.RelationChild, RelateAdd); err != nil {
		t.Fatalf("first parent (b): %v", err)
	}
	err := func() error {
		_, _, e := rels.Relate(ctx, relateTestActor, x, c, domain.RelationChild, RelateAdd)
		return e
	}()
	if !errors.Is(err, domain.ErrParentConflict) {
		t.Fatalf("second parent (c) error = %v, want domain.ErrParentConflict", err)
	}
	if !strings.Contains(err.Error(), "already has a parent") || !strings.Contains(err.Error(), string(b)) {
		t.Errorf("parent-conflict message %q should say the task already has a parent and name %s", err, b)
	}
	for _, e := range uow.relations.edges {
		if tripleOf(e) == (edgeTriple{x, c, domain.RelationChild}) {
			t.Errorf("failed add stored an edge (%+v)", e)
		}
	}
	if len(events.events) != 1 {
		t.Fatalf("events after failed second parent = %d, want 1 (only the first add)", len(events.events))
	}

	// The SAME parent again: idempotent no-op success, state and log unchanged.
	again, created, err := rels.Relate(ctx, relateTestActor, x, b, domain.RelationChild, RelateAdd)
	if err != nil {
		t.Fatalf("re-adding the same parent must not error, got: %v", err)
	}
	if created {
		t.Errorf("same-parent re-add created = true, want false (idempotent no-op)")
	}
	if len(uow.relations.edges) != 1 || len(events.events) != 1 {
		t.Errorf("after same-parent re-add: edges=%d events=%d, want 1/1", len(uow.relations.edges), len(events.events))
	}
	if again.TaskID != x || again.OtherTaskID != b {
		t.Errorf("same-parent re-add returned %+v, want the stored (x→b) edge", again)
	}
}

// TestRelate_RemoveDeletesEdge covers plain removal plus the parent-alias
// inverse: removing "b as a's parent" deletes the same row as removing
// "a is b's child". A remove of an absent edge is ErrNotFound with no event.
func TestRelate_RemoveDeletesEdge(t *testing.T) {
	rels, _, uow, events := newRelationEnv(t)
	ctx := context.Background()
	_, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, created, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateRemove)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if created {
		t.Errorf("remove created = true, want false")
	}
	want := edgeTriple{a, b, domain.RelationChild}
	if t3 := tripleOf(got); t3 != want {
		t.Errorf("returned removed edge = %+v, want %+v (ID/CreatedAt zero by contract)", got, want)
	}
	if len(uow.relations.edges) != 0 {
		t.Errorf("edges after remove = %+v, want none", uow.relations.edges)
	}
	// Two events: add (After set) then remove (Before set), both on the source task.
	if len(events.events) != 2 {
		t.Fatalf("events = %d (%+v), want 2", len(events.events), events.events)
	}
	rm := events.events[1]
	if rm.Kind != domain.EventRelationChanged || rm.TaskID == nil || *rm.TaskID != a {
		t.Errorf("remove event = %+v, want relation-changed on %s", rm, a)
	}
	wantBefore := map[string]any{"action": "remove", "kind": "child", "other_id": string(b)}
	if gotB := unmarshalEventJSON(t, rm.Before); !mapsEqual(gotB, wantBefore) {
		t.Errorf("remove event Before = %s, want %v", rm.Before, wantBefore)
	}
	if rm.After != "" {
		t.Errorf("remove event After = %q, want empty (edge absent)", rm.After)
	}

	// Removing the SAME edge again: ErrNotFound, no third event.
	_, _, err = rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateRemove)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove of absent edge = %v, want domain.ErrNotFound", err)
	}
	if len(events.events) != 2 {
		t.Errorf("events after double-remove = %d, want 2", len(events.events))
	}

	// Parent-alias remove hits the INVERSE stored row: "a is parent of b" was
	// stored (b→a, child); removing it via the alias must clear that row.
	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationParent, RelateAdd); err != nil {
		t.Fatalf("add via parent alias: %v", err)
	}
	if len(uow.relations.edges) != 1 || tripleOf(uow.relations.edges[0]) != (edgeTriple{b, a, domain.RelationChild}) {
		t.Fatalf("precondition: stored edge = %+v, want inverse child (b→a)", uow.relations.edges)
	}
	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationParent, RelateRemove); err != nil {
		t.Fatalf("remove via parent alias: %v", err)
	}
	if len(uow.relations.edges) != 0 {
		t.Errorf("edges after parent-alias remove = %+v, want none", uow.relations.edges)
	}
}

// TestRelate_NoVersionBump makes the child-row rule an explicit assertion on
// its own: a full add+remove cycle leaves both tasks' version and updated_at
// untouched.
func TestRelate_NoVersionBump(t *testing.T) {
	rels, _, uow, _ := newRelationEnv(t)
	ctx := context.Background()
	_, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	before := map[domain.ID]domain.Task{}
	for _, id := range []domain.ID{a, b} {
		before[id] = uow.tasks.byID[id]
	}
	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateRemove); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, id := range []domain.ID{a, b} {
		now := uow.tasks.byID[id]
		if now.Version != before[id].Version || !now.UpdatedAt.Equal(before[id].UpdatedAt) {
			t.Errorf("task %s after relate cycle: version=%d updated_at=%s; want %d / %s (child rows never bump)",
				id, now.Version, now.UpdatedAt, before[id].Version, before[id].UpdatedAt)
		}
	}
}

// TestRelate_ValidationErrors covers every rejection path: unknown action or
// kind, self-relations, missing tasks (either end), and cross-project pairs —
// all with zero writes and zero events.
func TestRelate_ValidationErrors(t *testing.T) {
	rels, _, uow, events := newRelationEnv(t)
	ctx := context.Background()
	pid1, ids := seedRelationWorld(t, uow, "world", "A", "B")
	a, b := ids[0], ids[1]

	// A second project with its own task for the cross-project case.
	pid2 := domain.ID("proj-2")
	uow.projects.byID[pid2] = domain.Project{ID: pid2, Name: "other", CreatedAt: relateTestNow, UpdatedAt: relateTestNow}
	other := domain.Task{ID: "task-other", ProjectID: pid2, Title: "Other", Status: domain.StatusTodo, Version: 1, CreatedAt: relateTestNow, UpdatedAt: relateTestNow}
	uow.tasks.byID[other.ID] = other

	cases := []struct {
		name    string
		call    func() error
		want    error
		excerpt string
	}{
		{
			name: "unknown action",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAction("upsert"))
				return e
			},
			want: domain.ErrInvalid, excerpt: `unknown relation action "upsert"`,
		},
		{
			name: "unknown kind",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, a, b, domain.RelationKind("parent-of"), RelateAdd)
				return e
			},
			want: domain.ErrInvalid, excerpt: `unknown relation kind "parent-of"`,
		},
		{
			name: "self-relation",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, a, a, domain.RelationChild, RelateAdd)
				return e
			},
			want:    domain.ErrInvalid,
			excerpt: "cannot relate to itself",
		},
		{
			name: "missing subject",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, "ghost-a", b, domain.RelationChild, RelateAdd)
				return e
			},
			want:    domain.ErrNotFound,
			excerpt: "",
		},
		{
			name: "missing object",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, a, "ghost-b", domain.RelationChild, RelateAdd)
				return e
			},
			want:    domain.ErrNotFound,
			excerpt: "",
		},
		{
			name: "cross-project pair",
			call: func() error {
				_, _, e := rels.Relate(ctx, relateTestActor, a, other.ID, domain.RelationChild, RelateAdd)
				return e
			},
			want:    domain.ErrInvalid,
			excerpt: "different projects",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Relate error = %v, want errors.Is %v", err, tc.want)
			}
			if tc.excerpt != "" && !strings.Contains(err.Error(), tc.excerpt) {
				t.Errorf("error message %q should contain %q", err, tc.excerpt)
			}
		})
	}
	if len(uow.relations.edges) != 0 || len(events.events) != 0 {
		t.Errorf("validation failures must write nothing: edges=%+v events=%d", uow.relations.edges, len(events.events))
	}
	_ = pid1 // project id asserted via events in other tests; kept for seed symmetry
}

// mapsEqual compares two decoded JSON payloads exactly (both non-nil here).
func mapsEqual(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, wv := range want {
		if gv, ok := got[k]; !ok || gv != wv {
			return false
		}
	}
	return true
}

// TestRelate_CycleDetection_BlockedByDirectAndTransitive satisfies AC 1:
// Attempting to create a circular blocked-by dependency (direct A<->B or
// transitive A->B->C->A or longer chains) returns ErrInvalid, writes nothing,
// and appends no events.
func TestRelate_CycleDetection_BlockedByDirectAndTransitive(t *testing.T) {
	t.Run("direct cycle A<->B", func(t *testing.T) {
		rels, _, uow, events := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("first relation (A blocked-by B): %v", err)
		}

		_, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationBlockedBy, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("direct cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") || !strings.Contains(err.Error(), "blocked-by") {
			t.Errorf("error message %q should mention cycle detected and blocked-by", err)
		}

		if len(uow.relations.edges) != 1 {
			t.Errorf("stored edges = %d, want exactly 1 after rejected cycle", len(uow.relations.edges))
		}
		if len(events.events) != 1 {
			t.Errorf("events = %d, want exactly 1 after rejected cycle", len(events.events))
		}
	})

	t.Run("transitive cycle A->B->C->A", func(t *testing.T) {
		rels, _, uow, events := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C")
		a, b, c := ids[0], ids[1], ids[2]

		// A is blocked by B, B is blocked by C
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("A blocked-by B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, b, c, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("B blocked-by C: %v", err)
		}

		// C is blocked by A completes the cycle: A -> B -> C -> A
		_, _, err := rels.Relate(ctx, relateTestActor, c, a, domain.RelationBlockedBy, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("transitive cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") || !strings.Contains(err.Error(), "blocked-by") {
			t.Errorf("error message %q should mention cycle detected and blocked-by", err)
		}

		if len(uow.relations.edges) != 2 {
			t.Errorf("stored edges = %d, want exactly 2 after rejected transitive cycle", len(uow.relations.edges))
		}
		if len(events.events) != 2 {
			t.Errorf("events = %d, want exactly 2 after rejected transitive cycle", len(events.events))
		}
	})

	t.Run("four-node transitive chain A->B->C->D->A", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C", "D")
		a, b, c, d := ids[0], ids[1], ids[2], ids[3]

		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("A blocked-by B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, b, c, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("B blocked-by C: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, c, d, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("C blocked-by D: %v", err)
		}

		_, _, err := rels.Relate(ctx, relateTestActor, d, a, domain.RelationBlockedBy, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("4-node cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") {
			t.Errorf("error message %q should mention cycle detected", err)
		}
	})
}

// TestRelate_CycleDetection_ParentChildDirectAndTransitive satisfies AC 2:
// Attempting to create a circular parent-child hierarchy (direct or transitive,
// expressed via RelationChild or the RelationParent alias) returns ErrInvalid,
// writes nothing, and appends no events.
func TestRelate_CycleDetection_ParentChildDirectAndTransitive(t *testing.T) {
	t.Run("direct cycle via child relation", func(t *testing.T) {
		rels, _, uow, events := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		// A is child of B
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("A child-of B: %v", err)
		}

		// Attempt B is child of A (making B parent of A and child of A)
		_, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationChild, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("direct child cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") || !strings.Contains(err.Error(), "child") {
			t.Errorf("error message %q should mention cycle detected and child", err)
		}

		if len(uow.relations.edges) != 1 {
			t.Errorf("stored edges = %d, want exactly 1 after rejected child cycle", len(uow.relations.edges))
		}
		if len(events.events) != 1 {
			t.Errorf("events = %d, want exactly 1 after rejected child cycle", len(events.events))
		}
	})

	t.Run("direct cycle via parent alias", func(t *testing.T) {
		rels, _, uow, events := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		// B is parent of A (stored: A is child of B)
		if _, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationParent, RelateAdd); err != nil {
			t.Fatalf("B parent-of A: %v", err)
		}

		// Attempt A is parent of B (stored: B is child of A)
		_, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationParent, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("direct parent cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") {
			t.Errorf("error message %q should mention cycle detected", err)
		}

		if len(uow.relations.edges) != 1 {
			t.Errorf("stored edges = %d, want exactly 1 after rejected parent cycle", len(uow.relations.edges))
		}
		if len(events.events) != 1 {
			t.Errorf("events = %d, want exactly 1 after rejected parent cycle", len(events.events))
		}
	})

	t.Run("transitive cycle via child relation", func(t *testing.T) {
		rels, _, uow, events := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C")
		a, b, c := ids[0], ids[1], ids[2]

		// A is child of B, B is child of C (C -> B -> A)
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("A child-of B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, b, c, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("B child-of C: %v", err)
		}

		// Attempt C is child of A (cycle: C child of A, A child of B, B child of C)
		_, _, err := rels.Relate(ctx, relateTestActor, c, a, domain.RelationChild, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("transitive child cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") || !strings.Contains(err.Error(), "child") {
			t.Errorf("error message %q should mention cycle detected and child", err)
		}

		if len(uow.relations.edges) != 2 {
			t.Errorf("stored edges = %d, want exactly 2 after rejected transitive child cycle", len(uow.relations.edges))
		}
		if len(events.events) != 2 {
			t.Errorf("events = %d, want exactly 2 after rejected transitive child cycle", len(events.events))
		}
	})

	t.Run("transitive cycle via parent alias", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C")
		a, b, c := ids[0], ids[1], ids[2]

		// C is parent of B, B is parent of A
		if _, _, err := rels.Relate(ctx, relateTestActor, c, b, domain.RelationParent, RelateAdd); err != nil {
			t.Fatalf("C parent-of B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationParent, RelateAdd); err != nil {
			t.Fatalf("B parent-of A: %v", err)
		}

		// Attempt A is parent of C
		_, _, err := rels.Relate(ctx, relateTestActor, a, c, domain.RelationParent, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("transitive parent alias cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") {
			t.Errorf("error message %q should mention cycle detected", err)
		}
	})

	t.Run("mixed child and parent alias transitive cycle", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C")
		a, b, c := ids[0], ids[1], ids[2]

		// B parent of A (parent alias) -> A child of B
		if _, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationParent, RelateAdd); err != nil {
			t.Fatalf("B parent-of A: %v", err)
		}
		// B child of C (child kind) -> B child of C
		if _, _, err := rels.Relate(ctx, relateTestActor, b, c, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("B child-of C: %v", err)
		}

		// Attempt C child of A
		_, _, err := rels.Relate(ctx, relateTestActor, c, a, domain.RelationChild, RelateAdd)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("mixed transitive cycle error = %v, want domain.ErrInvalid", err)
		}
		if !strings.Contains(err.Error(), "cycle detected") {
			t.Errorf("error message %q should mention cycle detected", err)
		}
	})
}

// TestRelate_CycleDetection_NonCyclicRelationsSucceed satisfies AC 3:
// Non-cyclic relations (diamond DAGs, multiple children per parent, multi-step chains,
// idempotent re-adds, and non-cyclic relations after removal) continue to add
// successfully without regression.
func TestRelate_CycleDetection_NonCyclicRelationsSucceed(t *testing.T) {
	t.Run("blocked-by diamond DAG", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B", "C", "D")
		a, b, c, d := ids[0], ids[1], ids[2], ids[3]

		// A blocked-by B and C
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("A blocked-by B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, a, c, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("A blocked-by C: %v", err)
		}

		// B and C both blocked-by D (DAG diamond)
		if _, _, err := rels.Relate(ctx, relateTestActor, b, d, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("B blocked-by D: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, c, d, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("C blocked-by D: %v", err)
		}

		if len(uow.relations.edges) != 4 {
			t.Fatalf("diamond DAG edges = %d, want 4", len(uow.relations.edges))
		}
	})

	t.Run("multiple children under same parent", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "Parent", "Child1", "Child2")
		p, c1, c2 := ids[0], ids[1], ids[2]

		if _, _, err := rels.Relate(ctx, relateTestActor, c1, p, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("c1 child-of p: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, c2, p, domain.RelationChild, RelateAdd); err != nil {
			t.Fatalf("c2 child-of p: %v", err)
		}

		if len(uow.relations.edges) != 2 {
			t.Fatalf("sibling children edges = %d, want 2", len(uow.relations.edges))
		}
	})

	t.Run("idempotent re-add succeeds", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("first add: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("idempotent second add should succeed, got: %v", err)
		}

		if len(uow.relations.edges) != 1 {
			t.Fatalf("edges = %d, want 1", len(uow.relations.edges))
		}
	})

	t.Run("edge removal allows previously cyclic direction", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		// A blocked-by B
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("A blocked-by B: %v", err)
		}

		// Remove A blocked-by B
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationBlockedBy, RelateRemove); err != nil {
			t.Fatalf("remove A blocked-by B: %v", err)
		}

		// Now B blocked-by A must succeed (no cycle exists anymore)
		if _, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationBlockedBy, RelateAdd); err != nil {
			t.Fatalf("B blocked-by A after removal should succeed, got: %v", err)
		}

		if len(uow.relations.edges) != 1 {
			t.Fatalf("edges = %d, want 1", len(uow.relations.edges))
		}
	})

	t.Run("duplicate-of and related do not enforce cycle check", func(t *testing.T) {
		rels, _, uow, _ := newRelationEnv(t)
		ctx := context.Background()
		_, ids := seedRelationWorld(t, uow, "world", "A", "B")
		a, b := ids[0], ids[1]

		// Mutual related
		if _, _, err := rels.Relate(ctx, relateTestActor, a, b, domain.RelationRelated, RelateAdd); err != nil {
			t.Fatalf("A related B: %v", err)
		}
		if _, _, err := rels.Relate(ctx, relateTestActor, b, a, domain.RelationRelated, RelateAdd); err != nil {
			t.Fatalf("B related A: %v", err)
		}

		if len(uow.relations.edges) != 2 {
			t.Fatalf("related edges = %d, want 2", len(uow.relations.edges))
		}
	})
}
