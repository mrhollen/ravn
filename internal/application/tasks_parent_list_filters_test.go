package application

// tasks_parent_list_filters_test.go: unit tests for the epic-decomposition
// pair — create_task's optional ParentID (child edge written in the same
// transaction) and list_tasks' parent_id / claimed / claimed_by / expired_claim
// filters. Over fakeUoW + fakes + FakeClock; the real SQL twin lives in
// internal/infrastructure/sqlite/tasks_list_filters_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// --- Part A: create_task with an optional parent ----------------------------

func TestTasksCreate_WithParent_WritesChildEdgeAndRelationEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	parent := mustCreateTask(t, svc, "Epic: resume payload", 4, nil)
	child, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Wire the event stream",
		ProjectID: testProjectID,
		Priority:  2,
		ParentID:  parent.ID,
	})
	if err != nil {
		t.Fatalf("Create with ParentID: %v", err)
	}
	if child.Status != domain.StatusTodo || child.Version != 1 {
		t.Errorf("child row = status %s version %d, want todo/v1", child.Status, child.Version)
	}

	// The stored edge is the CANONICAL child row: task_id=child, other_task_id=parent.
	var found bool
	for _, e := range uow.relations.edges {
		if e.TaskID == child.ID && e.OtherTaskID == parent.ID && e.Kind == domain.RelationChild {
			found = true
			if !e.CreatedAt.Equal(fc.now) {
				t.Errorf("edge CreatedAt = %s, want the create instant %s", e.CreatedAt, fc.now)
			}
		}
	}
	if !found {
		t.Fatalf("stored edges = %+v, want (child=%s -> parent=%s, kind=child)", uow.relations.edges, child.ID, parent.ID)
	}

	// Exactly TWO events for the child: created + relation-changed (add).
	var relEvents []domain.Event
	for _, ev := range uow.events.events {
		if ev.TaskID != nil && *ev.TaskID == child.ID && ev.Kind == domain.EventRelationChanged {
			relEvents = append(relEvents, ev)
		}
	}
	if len(relEvents) != 1 {
		t.Fatalf("relation-changed events for child = %d, want 1", len(relEvents))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(relEvents[0].After), &payload); err != nil {
		t.Fatalf("relation event After is not JSON: %v (%s)", err, relEvents[0].After)
	}
	if payload["action"] != "add" || payload["kind"] != string(domain.RelationChild) || payload["other_id"] != string(parent.ID) {
		t.Errorf("relation-changed payload = %v, want action=add kind=child other_id=%s", payload, parent.ID)
	}
	if relEvents[0].Actor.Type != testAgent.Type || relEvents[0].Actor.Name != testAgent.Name {
		t.Errorf("relation-changed actor = %+v, want the acting actor %+v", relEvents[0].Actor, testAgent)
	}

	// SubtaskProgress interoperability: the edge is indistinguishable from a
	// relate_tasks-written one.
	progress, err := NewRelations(uow, fc, &fakeIDGen{}).SubtaskProgress(ctx, []domain.ID{parent.ID})
	if err != nil {
		t.Fatalf("SubtaskProgress: %v", err)
	}
	if got := progress[parent.ID]; got.Total != 1 || got.Done != 0 {
		t.Errorf("SubtaskProgress(parent) = %+v, want total=1 done=0", got)
	}
}

func TestTasksCreate_WithParent_NoEdgeWhenOmitted(t *testing.T) {
	svc, _, uow := newTaskTestEnv(t)
	mustCreateTask(t, svc, "standalone task", 1, nil)
	if len(uow.relations.edges) != 0 {
		t.Errorf("edges = %+v, want none (ParentID omitted)", uow.relations.edges)
	}
}

func TestTasksCreate_WithParent_MissingParent(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	missing := domain.ID("01JMISSINGPARENT00000000000000")
	_, err := svc.Create(ctx, testAgent, TaskInput{Title: "orphan child", ProjectID: testProjectID, ParentID: missing})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want wrapped domain.ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), string(missing)) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %q, want it to name the missing parent id and the problem", err)
	}
	if len(uow.tasks.byID) != 0 || len(uow.relations.edges) != 0 {
		t.Errorf("nothing may be written on invalid parent: tasks=%d edges=%d", len(uow.tasks.byID), len(uow.relations.edges))
	}
}

func TestTasksCreate_WithParent_CrossProjectParent(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc.now, UpdatedAt: fc.now}
	otherProj := domain.ID("proj-2")
	fp.byID[otherProj] = domain.Project{ID: otherProj, Name: "Other", CreatedAt: fc.now, UpdatedAt: fc.now}
	uow := newFakeUoW(fp, &fakeEvents{})
	svc := NewTasks(uow, fc, &fakeIDGen{})

	foreignParent := mustCreateTask(t, svc, "foreign parent", 0, nil)
	// Re-point it into the other project directly (fixture surgery): the use
	// case validates parent.ProjectID != in.ProjectID on whatever is stored.
	stored := uow.tasks.byID[foreignParent.ID]
	stored.ProjectID = otherProj
	uow.tasks.byID[foreignParent.ID] = stored

	_, err := svc.Create(ctx, testAgent, TaskInput{Title: "cross-project child", ProjectID: testProjectID, ParentID: foreignParent.ID})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want wrapped domain.ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), string(otherProj)) || !strings.Contains(err.Error(), string(testProjectID)) {
		t.Errorf("err = %q, want it to name both projects", err)
	}
	if len(uow.tasks.byID) != 1 { // only the pre-seeded parent row exists
		t.Errorf("tasks written = %d, want 1 (validation precedes any write)", len(uow.tasks.byID))
	}
}

func TestTasksCreate_WithParent_ArchivedParent(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	parent := mustCreateTask(t, svc, "done-and-filed epic", 4, nil)
	if err := archiveTask(t, svc, parent.ID); err != nil {
		t.Fatalf("archive parent: %v", err)
	}
	_, err := svc.Create(ctx, testAgent, TaskInput{Title: "child under archived", ProjectID: testProjectID, ParentID: parent.ID})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want wrapped domain.ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), string(parent.ID)) || !strings.Contains(err.Error(), "archived") {
		t.Errorf("err = %q, want it to name the archived parent id", err)
	}
	if len(uow.tasks.byID) != 1 {
		t.Errorf("tasks written = %d, want 1 (validation precedes any write)", len(uow.tasks.byID))
	}
}

// archiveTask moves a task todo→doing→done (the transition map forbids a
// direct todo→done move) and then archives it — all through the use case so no
// fixture surgery is needed for the archived flag.
func archiveTask(t *testing.T, svc *Tasks, id domain.ID) error {
	t.Helper()
	ctx := context.Background()
	for _, next := range []domain.Status{domain.StatusDoing, domain.StatusDone} {
		cur, err := svc.Get(ctx, id)
		if err != nil {
			return err
		}
		st := next
		if _, err := svc.Update(ctx, testAgent, id, TaskUpdateInput{Status: &st, ExpectedVersion: cur.Version}); err != nil {
			return err
		}
	}
	cur, err := svc.Get(ctx, id)
	if err != nil {
		return err
	}
	archived := true
	_, err = svc.Update(ctx, testAgent, id, TaskUpdateInput{Archived: &archived, ExpectedVersion: cur.Version})
	return err
}

// --- Part B: list_tasks filters ---------------------------------------------

func TestTasksList_ClaimedAndClaimedByFilters(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	free := mustCreateTask(t, svc, "unclaimed task", 1, nil)
	claimed := mustCreateTask(t, svc, "contended task", 2, nil)
	if _, err := svc.Claim(ctx, testAgent, claimed.ID, 900, false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	bTrue, bFalse := true, false
	cases := []struct {
		name string
		flt  TaskFilter
		want map[domain.ID]bool
	}{
		{"claimed omitted = any", TaskFilter{}, rowIDs(t, []domain.Task{free, claimed})},
		{"claimed=true = lease present", TaskFilter{Claimed: &bTrue}, map[domain.ID]bool{claimed.ID: true}},
		{"claimed=false = unclaimed", TaskFilter{Claimed: &bFalse}, map[domain.ID]bool{free.ID: true}},
		{"claimed_by holder name", TaskFilter{ClaimedBy: testAgent.Name}, map[domain.ID]bool{claimed.ID: true}},
		{"claimed_by unknown name", TaskFilter{ClaimedBy: "nobody"}, map[domain.ID]bool{}},
		{"claimed_by alone does not exclude the unclaimed row's siblings", TaskFilter{Claimed: &bTrue, ClaimedBy: testAgent.Name}, map[domain.ID]bool{claimed.ID: true}},
	}
	for _, tc := range cases {
		rows, total, err := svc.List(ctx, tc.flt)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		if total != int64(len(tc.want)) || len(rows) != len(tc.want) {
			t.Errorf("%s: got %d rows (total %d), want %d — rows=%v", tc.name, len(rows), total, len(tc.want), rowIDs(t, rows))
			continue
		}
		for id := range rowIDs(t, rows) {
			if !tc.want[id] {
				t.Errorf("%s: unexpected task %s in rows", tc.name, id)
			}
		}
	}
}

func TestTasksList_ExpiredClaimFilter(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	at := fc.now
	stale := mustCreateTask(t, svc, "stale-lease task", 1, nil)
	active := mustCreateTask(t, svc, "live-lease task", 2, nil)
	free := mustCreateTask(t, svc, "open task", 3, nil)
	// Two distinct TTLs so the leases lap at different instants.
	if _, err := svc.Claim(ctx, testAgent, stale.ID, 10, false); err != nil { // expires at at+10s
		t.Fatalf("claim stale: %v", err)
	}
	if _, err := svc.Claim(ctx, domain.Actor{Type: domain.ActorHuman, Name: "ada"}, active.ID, 3600, false); err != nil { // expires far ahead
		t.Fatalf("claim active: %v", err)
	}

	bTrue, bFalse := true, false
	cases := []struct {
		name  string
		now   time.Time
		flt   TaskFilter
		want  map[domain.ID]bool
		total int64
	}{
		// At the grant instant BOTH leases are still active (the 10s lease
		// lapses only at at+10s), so expired=false sees all three rows.
		{"before any lapse: expired=true matches nothing", at, TaskFilter{ExpiredClaim: &bTrue}, map[domain.ID]bool{}, 0},
		{"before any lapse: expired=false = unclaimed or active", at, TaskFilter{ExpiredClaim: &bFalse}, rowIDs(t, []domain.Task{stale, active, free}), 3},
	}
	for _, tc := range cases {
		fc.SetNow(tc.now)
		rows, total, err := svc.List(ctx, tc.flt)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		got := rowIDs(t, rows)
		if total != tc.total || len(got) != len(tc.want) {
			t.Errorf("%s: got total=%d rows=%v, want total=%d rows=%v", tc.name, total, got, tc.total, tc.want)
			continue
		}
		for id := range got {
			if !tc.want[id] {
				t.Errorf("%s: unexpected task %s in rows", tc.name, id)
			}
		}
	}

	// Advance past the stale lease's expiry (10s) but not the live one (3600s):
	// exactly the stale row flips into the expired set.
	fc.SetNow(at.Add(2 * time.Minute))
	rows, total, err := svc.List(ctx, TaskFilter{ExpiredClaim: &bTrue})
	if err != nil {
		t.Fatalf("List expired=true after lapse: %v", err)
	}
	got := rowIDs(t, rows)
	if total != 1 || len(got) != 1 || !got[stale.ID] {
		t.Errorf("expired=true after lapse = total %d rows %v, want exactly the stale task %s", total, got, stale.ID)
	}
	rows, total, err = svc.List(ctx, TaskFilter{ExpiredClaim: &bFalse})
	if err != nil {
		t.Fatalf("List expired=false after lapse: %v", err)
	}
	got = rowIDs(t, rows)
	if total != 2 || !got[active.ID] || !got[free.ID] || got[stale.ID] {
		t.Errorf("expired=false after lapse = total %d rows %v, want active + open (not stale)", total, got)
	}

	// Omitted filter is unconstrained: all three rows come back.
	rows, total, err = svc.List(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("List without ExpiredClaim: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Errorf("no filter: total=%d rows=%d, want all 3", total, len(rows))
	}
}

func TestTasksList_ParentIDFilter(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc.now, UpdatedAt: fc.now}
	uow := newFakeUoW(fp, &fakeEvents{})
	svc := NewTasks(uow, fc, &fakeIDGen{})
	rels := NewRelations(uow, fc, &fakeIDGen{})

	parent := mustCreateTask(t, svc, "epic parent", 4, nil)
	viaCreate := mustCreateTaskParented(t, svc, "child via create_task", 2, parent.ID)
	viaRelate := mustCreateTask(t, svc, "child via relate_tasks", 3, nil)
	if _, _, err := rels.Relate(ctx, testAgent, viaRelate.ID, parent.ID, domain.RelationChild, RelateAdd); err != nil {
		t.Fatalf("relate child: %v", err)
	}
	grandchild := mustCreateTaskParented(t, svc, "grandchild (child of a child)", 1, viaCreate.ID)
	unrelated := mustCreateTask(t, svc, "unrelated task", 0, nil)

	rows, total, err := svc.List(ctx, TaskFilter{ParentID: parent.ID})
	if err != nil {
		t.Fatalf("List parent_id: %v", err)
	}
	want := map[domain.ID]bool{viaCreate.ID: true, viaRelate.ID: true}
	got := rowIDs(t, rows)
	if total != int64(len(want)) || !sameIDSet(got, want) {
		t.Errorf("parent_id=%s => total %d rows %v, want exactly the two children %+v", parent.ID, total, got, want)
	}
	if got[grandchild.ID] {
		t.Errorf("grandchild %s matched its grandparent's filter — only DIRECT children may match", grandchild.ID)
	}
	if got[unrelated.ID] || got[parent.ID] {
		t.Errorf("parent or unrelated rows leaked into the children listing")
	}

	// Composes with other filters: parent_id + status narrows further.
	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, viaRelate.ID, TaskUpdateInput{Status: &doing, ExpectedVersion: 1}); err != nil {
		t.Fatalf("move relate-child to doing: %v", err)
	}
	rows, total, err = svc.List(ctx, TaskFilter{ParentID: parent.ID, Status: domain.StatusTodo})
	if err != nil {
		t.Fatalf("List parent_id+status: %v", err)
	}
	got = rowIDs(t, rows)
	if total != 1 || !got[viaCreate.ID] {
		t.Errorf("parent_id+status=todo => total %d rows %v, want only the still-todo create-child", total, got)
	}

	// A parent with no children returns an empty page.
	rows, total, err = svc.List(ctx, TaskFilter{ParentID: grandchild.ID})
	if err != nil {
		t.Fatalf("List parent_id=leaf: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("parent_id=%s (leaf) => total %d rows %v, want empty", grandchild.ID, total, got)
	}
}

func mustCreateTaskParented(t *testing.T, svc *Tasks, title string, prio domain.Priority, parentID domain.ID) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), testAgent, TaskInput{
		Title: title, ProjectID: testProjectID, Priority: prio, ParentID: parentID,
	})
	if err != nil {
		t.Fatalf("Create %q (parent %s): %v", title, parentID, err)
	}
	return task
}

func sameIDSet(a, b map[domain.ID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

func TestTasksCreate_WithBlockedBy_WritesEdgesAndEvents(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	b1 := mustCreateTask(t, svc, "Migration half-applied", 4, nil)
	b2 := mustCreateTask(t, svc, "API contract frozen", 3, nil)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "New feature on top",
		ProjectID: testProjectID,
		BlockedBy: []domain.ID{b1.ID, b2.ID},
	})
	if err != nil {
		t.Fatalf("Create with BlockedBy: %v", err)
	}

	// N ids in => N CANONICAL blocked-by rows (task_id=new task, other_task_id=blocker),
	// in given order.
	var found []domain.Relation
	for _, e := range uow.relations.edges {
		if e.TaskID == task.ID && e.Kind == domain.RelationBlockedBy {
			found = append(found, e)
		}
	}
	if len(found) != 2 || found[0].OtherTaskID != b1.ID || found[1].OtherTaskID != b2.ID {
		t.Fatalf("stored edges = %+v, want (task -> %s), (task -> %s) in order", uow.relations.edges, b1.ID, b2.ID)
	}
	for _, e := range found {
		if !e.CreatedAt.Equal(fc.now) {
			t.Errorf("edge CreatedAt = %s, want the create instant %s", e.CreatedAt, fc.now)
		}
	}

	// One relation-changed event PER blocker (add), on the new task.
	var relEvents []domain.Event
	for _, ev := range uow.events.events {
		if ev.TaskID != nil && *ev.TaskID == task.ID && ev.Kind == domain.EventRelationChanged {
			relEvents = append(relEvents, ev)
		}
	}
	if len(relEvents) != 2 {
		t.Fatalf("relation-changed events for new task = %d, want one per blocker (2)", len(relEvents))
	}
	for _, ev := range relEvents {
		if !strings.Contains(ev.After, `"kind":"blocked-by"`) ||
			(!strings.Contains(ev.After, string(b1.ID)) && !strings.Contains(ev.After, string(b2.ID))) {
			t.Errorf("event payload %s not a blocked-by add", ev.After)
		}
	}
}

func TestTasksCreate_WithBlockedBy_RejectsInvalid(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	seeded := mustCreateTask(t, svc, "existing blocker", 4, nil)

	cases := []struct {
		name    string
		in      TaskInput
		wantSub string
	}{
		{"missing blocker", TaskInput{Title: "orphan blocked", ProjectID: testProjectID, BlockedBy: []domain.ID{"01JMISSINGBLOCKER0000000000000"}}, "does not exist"},
		{"empty id rejected (not skipped)", TaskInput{Title: "blank blocker", ProjectID: testProjectID, BlockedBy: []domain.ID{seeded.ID, ""}}, "must not contain an empty task id"},
	}
	for _, tc := range cases {
		before := len(uow.tasks.byID)
		_, err := svc.Create(ctx, testAgent, tc.in)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: err = %v, want wrapped domain.ErrInvalid", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: err = %q, want it to name the problem (%q)", tc.name, err, tc.wantSub)
		}
		if len(uow.tasks.byID) != before || len(uow.relations.edges) != 0 {
			t.Errorf("%s: nothing may be written (tasks %d->%d edges=%d)", tc.name, before, len(uow.tasks.byID), len(uow.relations.edges))
		}
	}

	// Cross-project blocker (fixture surgery, same as the parent twin).
	fc2 := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc2.now, UpdatedAt: fc2.now}
	otherProj := domain.ID("proj-2")
	fp.byID[otherProj] = domain.Project{ID: otherProj, Name: "Other", CreatedAt: fc2.now, UpdatedAt: fc2.now}
	uow2 := newFakeUoW(fp, &fakeEvents{})
	svc2 := NewTasks(uow2, fc2, &fakeIDGen{})
	foreign := mustCreateTask(t, svc2, "foreign blocker", 0, nil)
	stored := uow2.tasks.byID[foreign.ID]
	stored.ProjectID = otherProj
	uow2.tasks.byID[foreign.ID] = stored
	_, err := svc2.Create(ctx, testAgent, TaskInput{Title: "cross-project blocked", ProjectID: testProjectID, BlockedBy: []domain.ID{foreign.ID}})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), string(otherProj)) {
		t.Errorf("cross-project blocker: err = %v, want ErrInvalid naming project %s", err, otherProj)
	}

	// Archived blocker.
	svc3, _, uow3 := newTaskTestEnv(t)
	stale := mustCreateTask(t, svc3, "done-and-filed blocker", 4, nil)
	if err := archiveTask(t, svc3, stale.ID); err != nil {
		t.Fatalf("archive blocker: %v", err)
	}
	before := len(uow3.tasks.byID)
	_, err = svc3.Create(ctx, testAgent, TaskInput{Title: "blocked by archived", ProjectID: testProjectID, BlockedBy: []domain.ID{stale.ID}})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "archived") {
		t.Errorf("archived blocker: err = %v, want ErrInvalid naming archived task", err)
	}
	if len(uow3.tasks.byID) != before || len(uow3.relations.edges) != 0 {
		t.Errorf("archived blocker: nothing may be written (tasks %d->%d edges=%d)", before, len(uow3.tasks.byID), len(uow3.relations.edges))
	}
}

func TestTasksCreate_WithBlockedBy_DedupesPreservingOrder(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	a := mustCreateTask(t, svc, "blocker A", 4, nil)
	b := mustCreateTask(t, svc, "blocker B", 3, nil)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "duplicated list",
		ProjectID: testProjectID,
		BlockedBy: []domain.ID{a.ID, b.ID, a.ID, b.ID},
	})
	if err != nil {
		t.Fatalf("Create with duplicate BlockedBy: %v", err)
	}
	var got []domain.ID
	for _, e := range uow.relations.edges {
		if e.TaskID == task.ID && e.Kind == domain.RelationBlockedBy {
			got = append(got, e.OtherTaskID)
		}
	}
	if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Errorf("deduped edges = %v, want exactly [A B] in first-seen order", got)
	}
	var relEvents int
	for _, ev := range uow.events.events {
		if ev.TaskID != nil && *ev.TaskID == task.ID && ev.Kind == domain.EventRelationChanged {
			relEvents++
		}
	}
	if relEvents != 2 {
		t.Errorf("relation events = %d, want one per unique blocker", relEvents)
	}
}

func TestTasksCreate_WithParentAndBlockedBy_CombinedAtomic(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	parent := mustCreateTask(t, svc, "epic", 5, nil)
	blocker := mustCreateTask(t, svc, "foundation migration", 4, nil)

	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "feature behind both",
		ProjectID: testProjectID,
		ParentID:  parent.ID,
		BlockedBy: []domain.ID{blocker.ID},
	})
	if err != nil {
		t.Fatalf("Create with ParentID + BlockedBy: %v", err)
	}

	kindSeen := map[domain.RelationKind]bool{}
	for _, e := range uow.relations.edges {
		if e.TaskID == task.ID && (e.Kind == domain.RelationChild || e.Kind == domain.RelationBlockedBy) {
			kindSeen[e.Kind] = true
		}
	}
	if !kindSeen[domain.RelationChild] || !kindSeen[domain.RelationBlockedBy] {
		t.Errorf("edges = %+v, want BOTH the child and blocked-by rows", uow.relations.edges)
	}
	var relEvents int
	for _, ev := range uow.events.events {
		if ev.TaskID != nil && *ev.TaskID == task.ID && ev.Kind == domain.EventRelationChanged {
			relEvents++
		}
	}
	if relEvents != 2 {
		t.Errorf("relation events = %d, want one per edge (child + blocked-by)", relEvents)
	}

	// Atomicity: a valid parent with an INVALID blocker writes nothing.
	beforeTasks := len(uow.tasks.byID)
	beforeEdges := len(uow.relations.edges)
	_, err = svc.Create(ctx, testAgent, TaskInput{
		Title: "partial attempt", ProjectID: testProjectID, ParentID: parent.ID,
		BlockedBy: []domain.ID{"01JMISSINGBLOCKER0000000000000"},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want wrapped domain.ErrInvalid", err)
	}
	if len(uow.tasks.byID) != beforeTasks {
		t.Errorf("task rows changed on invalid blocker (%d -> %d) — nothing may be written", beforeTasks, len(uow.tasks.byID))
	}
	if len(uow.relations.edges) != beforeEdges {
		t.Errorf("edges changed on invalid blocker (%d -> %d) — no partial edges", beforeEdges, len(uow.relations.edges))
	}
}
