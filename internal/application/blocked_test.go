package application

// blocked_test.go: slice 2D2b unit tests over fakeUoW + fakes + FakeClock —
// GetDetail's computed Blocked flag / Blockers list (and its flip when a
// blocker completes or is archived) and List's UnblockedOnly filter. The real
// SQLite twin lives in internal/infrastructure/sqlite/uow_unblocked_test.go,
// which proves the SQL encodes the same semantics as these fakes.

import (
	"context"
	"testing"

	"github.com/mrhollen/crowban/internal/domain"
)

func newBlockedTestEnv(t *testing.T) (*Tasks, *Relations, *fakeClock, *fakeUoW) {
	t.Helper()
	fc := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc.now, UpdatedAt: fc.now}
	uow := newFakeUoW(fp, &fakeEvents{})
	return NewTasks(uow, fc, &fakeIDGen{}), NewRelations(uow, fc, &fakeIDGen{}), fc, uow
}

// mustBlock adds the edge "blocked is blocked-by blocker" through the real
// Relations use case (same-project check and event included).
func mustBlock(t *testing.T, rels *Relations, blocked, blocker domain.Task) {
	t.Helper()
	_, _, err := rels.Relate(context.Background(), testAgent, blocked.ID, blocker.ID, domain.RelationBlockedBy, RelateAdd)
	if err != nil {
		t.Fatalf("relate %q blocked-by %q: %v", blocked.Title, blocker.Title, err)
	}
}

func rowIDs(t *testing.T, rows []domain.Task) map[domain.ID]bool {
	t.Helper()
	out := make(map[domain.ID]bool, len(rows))
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

func TestGetDetail_BlockedFlagAndBlockers_FlipWhenBlockerDone(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newBlockedTestEnv(t)

	x := mustCreateTask(t, svc, "Ship feature", 2, nil)         // the blocked task
	y := mustCreateTask(t, svc, "Build the foundation", 3, nil) // its open blocker
	mustBlock(t, rels, x, y)

	detail, err := svc.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if !detail.Blocked {
		t.Errorf("Blocked = false, want true (open blocked-by target %q)", y.Title)
	}
	if len(detail.Blockers) != 1 || detail.Blockers[0].ID != y.ID ||
		detail.Blockers[0].Title != y.Title || detail.Blockers[0].Status != domain.StatusTodo {
		t.Errorf("Blockers = %+v, want the single open blocker (id %s title %q status todo)", detail.Blockers, y.ID, y.Title)
	}

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, y.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("Update blocker to doing: %v", err)
	}
	done := domain.StatusDone
	if _, err := svc.Update(ctx, testAgent, y.ID, TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("Update blocker to done: %v", err)
	}

	detail, err = svc.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail after blocker completed: %v", err)
	}
	if detail.Blocked {
		t.Errorf("Blocked = true after its blocker %q completed", y.Title)
	}
	if len(detail.Blockers) != 0 {
		t.Errorf("Blockers = %+v, want empty once the blocker is done", detail.Blockers)
	}
}

func TestGetDetail_NoBlockerEdges_NotBlocked(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newBlockedTestEnv(t)

	x := mustCreateTask(t, svc, "Lone task", 1, nil) // no relation edges at all

	detail, err := svc.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Blocked {
		t.Errorf("Blocked = true for a task without any blocked-by edges")
	}
	if len(detail.Blockers) != 0 {
		t.Errorf("Blockers = %+v, want empty", detail.Blockers)
	}
}

func TestGetDetail_DoneOrArchivedTargetsDoNotBlock(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newBlockedTestEnv(t)

	x := mustCreateTask(t, svc, "Depends on two", 2, nil)
	doneTarget := mustCreateTask(t, svc, "Already finished", 1, nil)
	archivedTarget := mustCreateTask(t, svc, "Archived mid-flight", 1, nil)

	doingStatus := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, doneTarget.ID, TaskUpdateInput{Status: &doingStatus}); err != nil {
		t.Fatalf("move target to doing: %v", err)
	}
	doneStatus := domain.StatusDone
	if _, err := svc.Update(ctx, testAgent, doneTarget.ID, TaskUpdateInput{Status: &doneStatus}); err != nil {
		t.Fatalf("complete target: %v", err)
	}
	archive := true
	if _, err := svc.Update(ctx, testAgent, archivedTarget.ID, TaskUpdateInput{Archived: &archive}); err != nil {
		t.Fatalf("archive target: %v", err)
	}

	mustBlock(t, rels, x, doneTarget)
	mustBlock(t, rels, x, archivedTarget)

	// Locked-down semantics: only NOT-done AND not-archived targets block. Both
	// of x's blockers are resolved, so it is startable despite two outgoing
	// blocked-by edges.
	detail, err := svc.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Blocked || len(detail.Blockers) != 0 {
		t.Errorf("Blocked = %v Blockers = %+v, want false/empty (done and archived targets do not block)",
			detail.Blocked, detail.Blockers)
	}

	// Sanity: the raw edge list still carries both edges — the flag is derived,
	// not stored.
	if len(detail.Relations) != 2 {
		t.Errorf("Relations = %d edges, want both outgoing blocked-by edges visible", len(detail.Relations))
	}
}

func TestList_UnblockedOnly_ExcludesTasksWithOpenBlockers(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newBlockedTestEnv(t)

	a := mustCreateTask(t, svc, "Needs foundation", 2, nil) // blocked by b (open)
	b := mustCreateTask(t, svc, "Build foundation", 3, nil) // open; no blockers of its own
	c := mustCreateTask(t, svc, "Independent work", 1, nil) // no edges at all
	mustBlock(t, rels, a, b)

	// Unfiltered sanity: everything is listed.
	all, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 || total != 3 {
		t.Fatalf("unfiltered List = %d rows / total %d, want 3/3", len(all), total)
	}

	// With UnblockedOnly the locked-down predicate excludes exactly the tasks
	// that HAVE an unresolved blocker: a is out; b (open but itself unblocked)
	// and c stay in — being somebody's blocker does not make one blocked.
	rows, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID, UnblockedOnly: true})
	if err != nil {
		t.Fatalf("List UnblockedOnly: %v", err)
	}
	got := rowIDs(t, rows)
	if got[a.ID] {
		t.Errorf("task with an open blocker (%q) is in the unblocked list", a.Title)
	}
	if !got[b.ID] || !got[c.ID] {
		t.Errorf("unblocked list = %v, want both %q and %q present", rowIDs(t, rows), b.Title, c.Title)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (same predicate as the rows)", total)
	}

	// Complete the blocker: a re-enters, and everything composes with the other
	// filters — an "actionable" query (open + todo + unblocked) drops b (now
	// done) and keeps a (freshly unblocked) before c (canonical priority order).
	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, b.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move blocker to doing: %v", err)
	}
	done := domain.StatusDone
	if _, err := svc.Update(ctx, testAgent, b.ID, TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}

	rows, total, err = svc.List(ctx, TaskFilter{ProjectID: testProjectID, UnblockedOnly: true})
	if err != nil {
		t.Fatalf("List after completion: %v", err)
	}
	got = rowIDs(t, rows)
	if !got[a.ID] || !got[b.ID] || !got[c.ID] || total != 3 {
		t.Errorf("after blocker done: rows = %d total = %d (%v), want all three / 3", len(rows), total, got)
	}

	actionable, actionableTotal, err := svc.List(ctx, TaskFilter{
		ProjectID: testProjectID, Status: domain.StatusTodo, UnblockedOnly: true,
	})
	if err != nil {
		t.Fatalf("List todo+unblocked: %v", err)
	}
	if len(actionable) != 2 || actionableTotal != 2 {
		t.Fatalf("actionable list = %d rows / total %d, want 2/2", len(actionable), actionableTotal)
	}
	if actionable[0].ID != a.ID || actionable[1].ID != c.ID { // priority 2 before 1
		t.Errorf("actionable order = [%s, %s], want %q (prio 2) then %q (prio 1)",
			actionable[0].Title, actionable[1].Title, a.Title, c.Title)
	}
}
