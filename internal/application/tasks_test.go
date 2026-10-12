package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// tasks_test.go: unit tests for the Tasks use case over fakeUoW + fakes +
// FakeClock — no SQLite, no HTTP (the highest-value test layer per
// ARCHITECTURE §9). The canonical-order fixture is mirrored by an integration
// test that proves the real SQL encodes the same order.

const testProjectID = domain.ID("proj-1")

func newTaskTestEnv(t *testing.T) (*Tasks, *fakeClock, *fakeUoW) {
	t.Helper()
	fc := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc.now, UpdatedAt: fc.now}
	uow := newFakeUoW(fp, &fakeEvents{})
	svc := NewTasks(uow, fc, &fakeIDGen{})
	return svc, fc, uow
}

// mustCreateTask creates a task through the use case with the given priority
// and due date, advancing nothing — call fc.SetNow between calls to get
// distinct, ascending CreatedAt values.
func mustCreateTask(t *testing.T, svc *Tasks, title string, prio domain.Priority, due *time.Time) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), testAgent, TaskInput{
		Title: title, ProjectID: testProjectID, Priority: prio, DueAt: due,
	})
	if err != nil {
		t.Fatalf("Create %q: %v", title, err)
	}
	return task
}

func TestTasksCreate_MintsIDStampsTimestampsStoresACsAppendsCreatedEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	at := fc.now
	due := at.Add(24 * time.Hour)

	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:       "  Ship the resume payload  ",
		Description: "markdown body",
		ProjectID:   testProjectID,
		Priority:    3,
		DueAt:       &due,
		Assignee:    &domain.Actor{Type: domain.ActorAgent, Name: " worker-b "},
		ACs:         []string{"Build it", "   ", "Test it"}, // whitespace entry must be dropped
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if task.ID == "" {
		t.Fatal("expected a minted ID, got zero value")
	}
	if task.Title != "Ship the resume payload" {
		t.Errorf("title not trimmed: %q", task.Title)
	}
	if task.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo (tasks are born in todo)", task.Status)
	}
	if task.Version != 1 {
		t.Errorf("version = %d, want 1 for a fresh row", task.Version)
	}
	if !task.CreatedAt.Equal(at) || !task.UpdatedAt.Equal(at) {
		t.Errorf("timestamps = (%s, %s), want both %s", task.CreatedAt, task.UpdatedAt, at)
	}
	if task.Assignee == nil || task.Assignee.Type != domain.ActorAgent || task.Assignee.Name != "worker-b" {
		t.Errorf("assignee = %+v, want trimmed agent worker-b", task.Assignee)
	}
	stored, ok := uow.tasks.byID[task.ID]
	if !ok || stored.Version != 1 || stored.Status != domain.StatusTodo {
		t.Fatalf("stored row mismatch: present=%v stored=%+v", ok, stored)
	}

	acs, err := uow.acs.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(acs) != 2 {
		t.Fatalf("stored %d ACs, want 2 (whitespace entry dropped)", len(acs))
	}
	wantTexts := []string{"Build it", "Test it"}
	for i, want := range wantTexts {
		if acs[i].Seq != i+1 || acs[i].Text != want || acs[i].Done {
			t.Errorf("AC[%d] = seq %d text %q done %v, want seq %d text %q done false",
				i, acs[i].Seq, acs[i].Text, acs[i].Done, i+1, want)
		}
		if acs[i].ID == "" || acs[i].TaskID != task.ID {
			t.Errorf("AC[%d] id/task = (%q, %q), want minted id + %q", i, acs[i].ID, acs[i].TaskID, task.ID)
		}
	}

	evs := uow.events.events
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Kind != domain.EventCreated {
		t.Errorf("event kind = %q, want created", ev.Kind)
	}
	if ev.Actor != testAgent || !ev.At.Equal(at) {
		t.Errorf("event (actor=%+v at=%s), want (%+v %s)", ev.Actor, ev.At, testAgent, at)
	}
	if ev.TaskID == nil || *ev.TaskID != task.ID {
		t.Errorf("event task id = %v, want %s", ev.TaskID, task.ID)
	}
	if ev.ProjectID == nil || *ev.ProjectID != testProjectID {
		t.Errorf("event project id = %v, want %s (always set when derivable)", ev.ProjectID, testProjectID)
	}
	if ev.Before != "" || !strings.Contains(ev.After, "Ship the resume payload") {
		t.Errorf("event payloads (%q, %q), want empty before + after carrying the title", ev.Before, ev.After)
	}
}

func TestTasksCreate_InvalidInputWritesNothing(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)

	badType := TaskInput{Title: "T", ProjectID: testProjectID, Assignee: &domain.Actor{Type: "bot", Name: "x"}}
	emptyName := domain.Actor{Type: domain.ActorAgent, Name: "  "}
	badName := TaskInput{Title: "T", ProjectID: testProjectID, Assignee: &emptyName}

	for _, tc := range []struct {
		name string
		in   TaskInput
	}{
		{"empty title", TaskInput{Title: "   ", ProjectID: testProjectID}},
		{"missing project", TaskInput{Title: "T", ProjectID: ""}},
		{"unknown project", TaskInput{Title: "T", ProjectID: domain.ID("ghost")}},
		{"priority out of range", TaskInput{Title: "T", ProjectID: testProjectID, Priority: 6}},
		{"bad actor type", badType},
		{"empty actor name", badName},
	} {
		if _, err := svc.Create(ctx, testAgent, tc.in); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: Create err = %v, want wrapped domain.ErrInvalid", tc.name, err)
		}
	}

	if len(uow.tasks.byID) != 0 || len(uow.events.events) != 0 || len(uow.acs.byTask) != 0 {
		t.Fatalf("invalid input must write nothing: tasks=%d events=%d acs=%d",
			len(uow.tasks.byID), len(uow.events.events), len(uow.acs.byTask))
	}
}

func TestTasksUpdate_StatusOnlyChangeEmitsStatusChangedAndNothingElse(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Status Task", 1, nil) // 1 event (created)

	fc.SetNow(fc.now.Add(5 * time.Minute))
	doing := domain.StatusDoing
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != domain.StatusDoing || updated.Version != 2 {
		t.Fatalf("updated = (status %q version %d), want (doing 2)", updated.Status, updated.Version)
	}

	evs := uow.events.events
	if len(evs) != 2 {
		t.Fatalf("expected 2 events (created + status-changed), got %d", len(evs))
	}
	ev := evs[1]
	if ev.Kind != domain.EventStatusChanged {
		t.Fatalf("event kind = %q, want status-changed", ev.Kind)
	}
	if !strings.Contains(ev.Before, `"status":"todo"`) || !strings.Contains(ev.After, `"status":"doing"`) {
		t.Errorf("payloads (%q → %q), want before todo / after doing", ev.Before, ev.After)
	}
	for _, e := range evs {
		if e.Kind == domain.EventUpdated {
			t.Error("a status-only change must not emit an `updated` event")
		}
	}
}

func TestTasksUpdate_FieldChangeEmitsUpdatedWithChangedFieldsOnly(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title: "Old Title", Description: "keep me", ProjectID: testProjectID, Priority: 0,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	fc.SetNow(fc.now.Add(2 * time.Minute))
	newTitle := "New Title"
	prio := domain.Priority(4)
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &newTitle, Priority: &prio})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "New Title" || updated.Priority != 4 || updated.Version != 2 {
		t.Fatalf("updated = (title %q prio %d v%d), want (New Title 4 2)", updated.Title, updated.Priority, updated.Version)
	}

	evs := uow.events.events
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	ev := evs[1]
	if ev.Kind != domain.EventUpdated {
		t.Fatalf("event kind = %q, want updated", ev.Kind)
	}
	for _, frag := range []string{`"title":"Old Title"`, `"priority":0`} {
		if !strings.Contains(ev.Before, frag) {
			t.Errorf("Before %q missing %s", ev.Before, frag)
		}
	}
	for _, frag := range []string{`"title":"New Title"`, `"priority":4`} {
		if !strings.Contains(ev.After, frag) {
			t.Errorf("After %q missing %s", ev.After, frag)
		}
	}
	// Changed fields ONLY: the untouched description must not appear at all.
	if strings.Contains(ev.Before, "description") || strings.Contains(ev.After, "description") {
		t.Errorf("`updated` payload leaked an unchanged field:\n before=%s\n after=%s", ev.Before, ev.After)
	}
}

func TestTasksUpdate_StatusToDoneStampsCompletedAtAndBackClearsIt(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Done Task", 0, nil)
	if task.CompletedAt != nil {
		t.Fatalf("fresh task has CompletedAt %v, want nil", task.CompletedAt)
	}

	fc.SetNow(fc.now.Add(time.Hour))
	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("Update → doing: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Hour))
	done := domain.StatusDone
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("Update → done: %v", err)
	}
	if updated.CompletedAt == nil || !updated.CompletedAt.Equal(fc.now) {
		t.Errorf("CompletedAt = %v, want the fresh stamp %s", updated.CompletedAt, fc.now)
	}

	fc.SetNow(fc.now.Add(time.Hour))
	todo := domain.StatusTodo
	back, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo})
	if err != nil {
		t.Fatalf("Update → todo: %v", err)
	}
	if back.CompletedAt != nil {
		t.Errorf("moving out of done must clear CompletedAt, got %v", back.CompletedAt)
	}
	if len(uow.events.events) != 4 { // created + 3 status-changed
		t.Errorf("events = %d, want 4 (one status-changed per move)", len(uow.events.events))
	}
}

func TestTasksUpdate_AssigneeChangesEmitAssignedOnly(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Assignee Task", 0, nil) // unassigned

	// (a) set: agent-x → one `assigned` event, null → x.
	fc.SetNow(fc.now.Add(time.Minute))
	x := domain.Actor{Type: domain.ActorAgent, Name: "agent-x"}
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Assignee: &x})
	if err != nil {
		t.Fatalf("Update assign: %v", err)
	}
	if updated.Assignee == nil || updated.Assignee.Name != "agent-x" {
		t.Fatalf("assignee = %+v, want agent-x", updated.Assignee)
	}
	evs := uow.events.events
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	if evs[1].Kind != domain.EventAssigned ||
		!strings.Contains(evs[1].Before, `"assignee":null`) ||
		!strings.Contains(evs[1].After, "agent-x") {
		t.Errorf("assigned event = (kind %q, %q → %q), want assigned null→agent-x", evs[1].Kind, evs[1].Before, evs[1].After)
	}

	// (b) reassign together with a title change: `updated` AND `assigned`.
	fc.SetNow(fc.now.Add(time.Minute))
	y := domain.Actor{Type: domain.ActorHuman, Name: "human-y"}
	newTitle := "Renamed"
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &newTitle, Assignee: &y}); err != nil {
		t.Fatalf("Update reassign: %v", err)
	}
	evs = uow.events.events
	if len(evs) != 4 { // created, assigned, updated, assigned
		t.Fatalf("expected 4 events, got %d (%v)", len(evs), kindsOf(evs))
	}
	if evs[2].Kind != domain.EventUpdated || evs[3].Kind != domain.EventAssigned {
		t.Fatalf("events after reassign = %v, want [updated assigned]", kindsOf(evs))
	}
	if !strings.Contains(evs[3].Before, "agent-x") || !strings.Contains(evs[3].After, "human-y") {
		t.Errorf("reassign payload (%q → %q), want agent-x → human-y", evs[3].Before, evs[3].After)
	}

	// (c) clear: x is gone → `assigned` with null after.
	fc.SetNow(fc.now.Add(time.Minute))
	cleared, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{ClearAssignee: true})
	if err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	if cleared.Assignee != nil {
		t.Fatalf("assignee = %+v, want nil after ClearAssignee", cleared.Assignee)
	}
	evs = uow.events.events
	if evs[4].Kind != domain.EventAssigned || !strings.Contains(evs[4].After, `"assignee":null`) {
		t.Errorf("clear event = (kind %q, %q), want assigned → null", evs[4].Kind, evs[4].After)
	}

	// (d) clearing an already-unassigned task is a no-op: no write, no event.
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{ClearAssignee: true}); err != nil {
		t.Fatalf("no-op clear: %v", err)
	}
	if len(uow.events.events) != 5 {
		t.Errorf("no-op clear must not append an event (got %d)", len(uow.events.events))
	}
}

func TestTasksUpdate_StaleVersionPropagatesConflictWithoutEvents(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Conflict Task", 0, nil) // stored at version 1

	// Simulate a concurrent writer that already bumped the row to version 2
	// between this writer's Get and Update.
	current := uow.tasks.byID[task.ID]
	current.Version = 2
	uow.tasks.byID[task.ID] = current
	uow.tasks.failUpdate = func(domain.Task) error {
		return &domain.ConflictError{Current: current}
	}

	newTitle := "Lost Race"
	_, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &newTitle})
	var conflict *domain.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Update err = %v, want *domain.ConflictError", err)
	}
	if conflict.Current.Version != 2 || conflict.Current.Title != "Conflict Task" {
		t.Errorf("ConflictError.Current = %+v, want the fresh row (version 2, old title)", conflict.Current)
	}
	if len(uow.events.events) != 1 { // only the original `created`
		t.Fatalf("failed update must append no events, got %d", len(uow.events.events))
	}

	// The fake's own guard: a stale t.Version also fails with ConflictError.
	stale := current
	stale.Version = 1
	if _, err := uow.tasks.Update(ctx, stale); !errors.As(err, &conflict) {
		t.Fatalf("direct Update(stale) err = %v, want *domain.ConflictError", err)
	} else if conflict.Current.Version != 2 {
		t.Errorf("guard conflict Current version = %d, want 2", conflict.Current.Version)
	}
}

func TestTasksUpdate_NoChangesNoBumpNoEvent(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "No Change Task", 0, nil)

	same := task.Title
	got, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &same})
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if got.Version != 1 || got.Title != task.Title {
		t.Errorf("no-op update = (v%d title %q), want (v1 %q)", got.Version, got.Title, task.Title)
	}
	if stored := uow.tasks.byID[task.ID]; stored.Version != 1 {
		t.Errorf("stored version = %d after no-op, want still 1", stored.Version)
	}
	if len(uow.events.events) != 1 {
		t.Errorf("no-op update must emit no event (got %d)", len(uow.events.events))
	}

	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty update err = %v, want wrapped domain.ErrInvalid", err)
	}
}

func TestTasksUpdate_UnknownIDReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, domain.ID("ghost"), TaskUpdateInput{Status: &doing}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update(unknown id) err = %v, want domain.ErrNotFound", err)
	}
}

func TestTasksList_CanonicalOrderAndPaginationTotal(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	due1 := mustTime(t, "2026-10-01T00:00:00Z")
	due2 := mustTime(t, "2026-10-02T00:00:00Z")

	// Creation order is deliberately scrambled vs. the expected canonical order;
	// advancing the clock after each create gives unique ascending CreatedAt.
	type spec struct {
		title string
		prio  domain.Priority
		due   *time.Time
	}
	for _, s := range []spec{
		{"P3b", 3, &due1}, // prio3 with due1 (beats the null-due P3a)
		{"P5d", 5, nil},   // prio5 NULL due → sorts last within prio5 despite earliest-ish creation
		{"P3a", 3, nil},   // prio3 null due → last of prio3
		{"P5a", 5, &due1}, // same (prio, due) as P5b; earlier CreatedAt wins
		{"P5c", 5, &due2}, // later due → after both due1 rows despite mid creation
		{"P5b", 5, &due1},
	} {
		mustCreateTask(t, svc, s.title, s.prio, s.due)
		fc.SetNow(fc.now.Add(time.Hour))
	}

	rows, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 6 {
		t.Fatalf("total = %d, want 6", total)
	}
	want := []string{"P5a", "P5b", "P5c", "P5d", "P3b", "P3a"}
	got := titlesOf(rows)
	if len(got) != len(want) {
		t.Fatalf("List returned %d rows (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q — full: got %v want %v", i, got[i], want[i], got, want)
		}
	}

	// Pagination: the page window moves but total stays 6.
	page1, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID, Limit: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if !sameTitles(page1, []string{"P5a", "P5b"}) || total != 6 {
		t.Errorf("page (0,2) = (%v total %d), want ([P5a P5b] 6)", titlesOf(page1), total)
	}
	page3, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID, Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("List page 3: %v", err)
	}
	if !sameTitles(page3, []string{"P3b", "P3a"}) || total != 6 {
		t.Errorf("page (4,2) = (%v total %d), want ([P3b P3a] 6)", titlesOf(page3), total)
	}

	// Clamp: the repo sees Limit capped at 100, default 20 for zero, floor 0 offset.
	if _, _, err := svc.List(ctx, TaskFilter{Limit: 999}); err != nil {
		t.Fatalf("List clamp hi: %v", err)
	}
	if uow.tasks.lastFilter.Limit != maxListLimit {
		t.Errorf("repo saw Limit = %d for input 999, want %d", uow.tasks.lastFilter.Limit, maxListLimit)
	}
	if _, _, err := svc.List(ctx, TaskFilter{Limit: 0}); err != nil {
		t.Fatalf("List clamp default: %v", err)
	}
	if uow.tasks.lastFilter.Limit != defaultListLimit {
		t.Errorf("repo saw Limit = %d for input 0, want default %d", uow.tasks.lastFilter.Limit, defaultListLimit)
	}
	if _, _, err := svc.List(ctx, TaskFilter{Offset: -5}); err != nil {
		t.Fatalf("List clamp offset: %v", err)
	}
	if uow.tasks.lastFilter.Offset != 0 {
		t.Errorf("repo saw Offset = %d for input -5, want 0", uow.tasks.lastFilter.Offset)
	}
}

func TestTasksList_Filters(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	seedNow := fc.now // rows are created at the env start...
	mk := func(id string, title string, status domain.Status, assignee *domain.Actor, completedAt *time.Time) domain.Task {
		return domain.Task{
			ID: domain.ID(id), ProjectID: testProjectID, Title: title, Status: status,
			Priority: 1, Assignee: assignee, Version: 1,
			CreatedAt: seedNow, UpdatedAt: seedNow, CompletedAt: completedAt,
		}
	}
	fc.SetNow(fc.now.Add(time.Hour)) // ...the completion stamp lands one hour later
	doneAt := fc.now

	humanHana := domain.Actor{Type: domain.ActorHuman, Name: "hana"}
	agentOdo := domain.Actor{Type: domain.ActorAgent, Name: "odo"}
	uow.tasks.byID["f-open"] = mk("f-open", "open todo task", domain.StatusTodo, nil, nil)
	uow.tasks.byID["f-doing"] = mk("f-doing", "doing agent task", domain.StatusDoing, &agentOdo, nil)
	ar := mk("f-archived", "archived done task", domain.StatusDone, &humanHana, &doneAt)
	ar.Archived = true
	uow.tasks.byID["f-archived"] = ar

	count := func(f TaskFilter) int {
		t.Helper()
		rows, _, err := svc.List(ctx, f)
		if err != nil {
			t.Fatalf("List %+v: %v", f, err)
		}
		return len(rows)
	}

	if n := count(TaskFilter{ProjectID: testProjectID}); n != 2 { // archived excluded by default
		t.Errorf("default (open only) = %d rows, want 2", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, Archived: true}); n != 1 {
		t.Errorf("archived-only = %d rows, want 1", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, Status: domain.StatusDoing}); n != 1 {
		t.Errorf("status=doing = %d rows, want 1", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, AssigneeType: domain.ActorAgent}); n != 1 {
		t.Errorf("assignee_type=agent = %d rows, want 1", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, AssigneeName: "hana"}); n != 0 { // hana is archived → excluded
		t.Errorf("assignee_name=hana (open only) = %d rows, want 0", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, Q: "AGENT"}); n != 1 { // case-insensitive LIKE over title
		t.Errorf("q=AGENT = %d rows, want 1", n)
	}
	if n := count(TaskFilter{ProjectID: testProjectID, Archived: true, CompletedAfter: &doneAt}); n != 1 {
		t.Errorf("archived + completed_after boundary (>=) = %d rows, want 1", n)
	}
	beforeDone := doneAt.Add(-time.Nanosecond)
	if n := count(TaskFilter{ProjectID: testProjectID, Archived: true, CompletedBefore: &beforeDone}); n != 0 {
		t.Errorf("archived + completed_before(done-1ns) = %d rows, want 0", n)
	}
	createdNow := fc.now
	if n := count(TaskFilter{ProjectID: testProjectID, CreatedAfter: &createdNow}); n != 0 { // all rows created earlier
		t.Errorf("created_after(now) = %d rows, want 0", n)
	}
}

func TestTasksGet_ReturnsStoredRowOrNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Get Me", 2, nil)

	got, err := svc.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Get Me" || got.Version != 1 {
		t.Errorf("Get = %+v, want the stored row (title %q v1)", got, task.Title)
	}
	if _, err := svc.Get(ctx, domain.ID("ghost")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown) err = %v, want domain.ErrNotFound", err)
	}
}

func TestTasksGetDetail_AggregatesTheResumePayload(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title: "Resume Me", ProjectID: testProjectID, Priority: 3,
		ACs: []string{"First criterion", "Second criterion"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Relation endpoints (raw rows are fine — only their IDs matter here).
	uow.tasks.byID["rel-out"] = domain.Task{ID: "rel-out", ProjectID: testProjectID, Title: "Outgoing endpoint", Status: domain.StatusDoing, Version: 1, CreatedAt: fc.now, UpdatedAt: fc.now}
	uow.tasks.byID["rel-in"] = domain.Task{ID: "rel-in", ProjectID: testProjectID, Title: "Incoming endpoint", Status: domain.StatusTodo, Version: 1, CreatedAt: fc.now, UpdatedAt: fc.now}
	relAt := fc.now.Add(2 * time.Hour)
	if err := uow.relations.Add(ctx, domain.Relation{ID: "r-1", TaskID: task.ID, OtherTaskID: "rel-out", Kind: domain.RelationBlockedBy, CreatedAt: relAt}); err != nil {
		t.Fatalf("Add outgoing edge: %v", err)
	}
	if err := uow.relations.Add(ctx, domain.Relation{ID: "r-2", TaskID: "rel-in", OtherTaskID: task.ID, Kind: domain.RelationRelated, CreatedAt: relAt}); err != nil {
		t.Fatalf("Add incoming edge: %v", err)
	}

	// Comment tail + artifacts at distinct times.
	for i, kind := range []domain.CommentKind{domain.CommentProgress, domain.CommentQuestion, domain.CommentDecision} {
		ts := fc.now.Add(time.Duration(i+1) * time.Minute)
		c := domain.Comment{ID: domain.ID(fmt.Sprintf("c-%d", i+1)), TaskID: task.ID, Kind: kind, Body: "body", Actor: testAgent, CreatedAt: ts}
		if err := uow.comments.Add(ctx, c); err != nil {
			t.Fatalf("Add comment %d: %v", i+1, err)
		}
	}
	a1 := domain.Artifact{ID: "a-1", TaskID: task.ID, Kind: domain.ArtifactLog, Ref: "/tmp/a.log", Summary: "older", Actor: testAgent, CreatedAt: fc.now.Add(20 * time.Minute)}
	a2 := domain.Artifact{ID: "a-2", TaskID: task.ID, Kind: domain.ArtifactURL, Ref: "https://example.com", Summary: "newer", Actor: testAgent, CreatedAt: fc.now.Add(30 * time.Minute)}
	if err := uow.artifacts.Add(ctx, a1); err != nil {
		t.Fatalf("Add artifact 1: %v", err)
	}
	if err := uow.artifacts.Add(ctx, a2); err != nil {
		t.Fatalf("Add artifact 2: %v", err)
	}

	d, err := svc.GetDetail(ctx, task.ID, 0, 0) // zero limits → defaults (10 comments / 20 events)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if d.Task.ID != task.ID || d.Task.Version != 1 {
		t.Errorf("detail task = %+v, want the created row (v1)", d.Task)
	}
	if len(d.AcceptanceCriteria) != 2 ||
		d.AcceptanceCriteria[0].Seq != 1 || d.AcceptanceCriteria[0].Text != "First criterion" ||
		d.AcceptanceCriteria[1].Seq != 2 || d.AcceptanceCriteria[1].Text != "Second criterion" {
		t.Errorf("ACs = %+v, want the two criteria in seq order", d.AcceptanceCriteria)
	}
	if len(d.Comments) != 3 || d.Comments[0].ID != "c-3" || d.Comments[1].ID != "c-2" || d.Comments[2].ID != "c-1" {
		t.Errorf("comments = %v, want newest-first tail [c-3 c-2 c-1]", commentIDs(d.Comments))
	}
	if len(d.Artifacts) != 2 || d.Artifacts[0].ID != "a-1" || d.Artifacts[1].ID != "a-2" {
		t.Errorf("artifacts = %v, want oldest-first [a-1 a-2]", artifactIDs(d.Artifacts))
	}
	if len(d.Relations) != 2 {
		t.Fatalf("relations = %d edges, want 2 (incoming + outgoing)", len(d.Relations))
	}
	kinds := map[domain.ID]domain.RelationKind{} // endpoint (either direction) → stored kind
	for _, rel := range d.Relations {
		other := rel.OtherTaskID // outgoing edge: task → other
		if rel.TaskID != task.ID {
			other = rel.TaskID // incoming edge: other → task
		}
		kinds[other] = rel.Kind
	}
	if kinds[domain.ID("rel-out")] != domain.RelationBlockedBy || kinds[domain.ID("rel-in")] != domain.RelationRelated {
		t.Errorf("relation edges = %v, want outgoing blocked-by + incoming related", d.Relations)
	}
	if len(d.Events) != 1 || d.Events[0].Kind != domain.EventCreated {
		t.Errorf("events = %+v, want the single created event newest-first", d.Events)
	}

	// Explicit limits shrink the tails.
	d2, err := svc.GetDetail(ctx, task.ID, 2, 1)
	if err != nil {
		t.Fatalf("GetDetail limited: %v", err)
	}
	if len(d2.Comments) != 2 || d2.Comments[0].ID != "c-3" || d2.Comments[1].ID != "c-2" {
		t.Errorf("limited comments = %v, want [c-3 c-2]", commentIDs(d2.Comments))
	}

	if _, err := svc.GetDetail(ctx, domain.ID("ghost"), 0, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetDetail(unknown) err = %v, want domain.ErrNotFound", err)
	}
}

// --- tiny assertion helpers ------------------------------------------------------

func titlesOf(tasks []domain.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.Title
	}
	return out
}

func sameTitles(got []domain.Task, want []string) bool {
	g := titlesOf(got)
	if len(g) != len(want) {
		return false
	}
	for i := range want {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func kindsOf(evs []domain.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Kind)
	}
	return out
}

func commentIDs(cs []domain.Comment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c.ID)
	}
	return out
}

func artifactIDs(as []domain.Artifact) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a.ID)
	}
	return out
}

func TestTasksUpdate_StatusTransitionsValidation(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Transition Test", 0, nil)

	done := domain.StatusDone
	doing := domain.StatusDoing
	todo := domain.StatusTodo
	bogus := domain.Status("bogus")

	// 1. Invalid: todo -> done (skipping doing)
	_, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Update todo -> done err = %v, want domain.ErrInvalid", err)
	}

	// 2. Invalid: unknown status
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &bogus})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Update todo -> bogus err = %v, want domain.ErrInvalid", err)
	}

	// 3. Valid: todo -> doing
	task, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing})
	if err != nil {
		t.Fatalf("Update todo -> doing: %v", err)
	}
	if task.Status != domain.StatusDoing {
		t.Errorf("status = %q, want doing", task.Status)
	}

	// 4. Valid: doing -> done
	task, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("Update doing -> done: %v", err)
	}
	if task.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", task.Status)
	}

	// 5. Invalid: done -> doing (cannot skip todo)
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Update done -> doing err = %v, want domain.ErrInvalid", err)
	}

	// 6. Valid: done -> todo (reopen)
	task, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo})
	if err != nil {
		t.Fatalf("Update done -> todo: %v", err)
	}
	if task.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", task.Status)
	}

	// 7. No-op: todo -> todo
	unchanged, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo})
	if err != nil {
		t.Fatalf("Update todo -> todo: %v", err)
	}
	if unchanged.Version != task.Version {
		t.Errorf("version bumped on no-op status update: %d -> %d", task.Version, unchanged.Version)
	}
}

func TestTasksUpdate_ClearsActiveClaimLeaseWhenLeavingDoing(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)

	t.Run("doing -> done clears active claim lease", func(t *testing.T) {
		task := mustCreateTask(t, svc, "Claimed Task", 0, nil)
		claimed, err := svc.Claim(ctx, testAgent, task.ID, 900, false)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.Claimed() || claimed.ClaimedBy == nil {
			t.Fatalf("task not claimed: %+v", claimed)
		}

		done := domain.StatusDone
		updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
		if err != nil {
			t.Fatalf("Update to done: %v", err)
		}
		if updated.Status != domain.StatusDone {
			t.Errorf("status = %q, want done", updated.Status)
		}
		if updated.Claimed() {
			t.Errorf("updated.Claimed() = true, want false")
		}
		if updated.ClaimedBy != nil {
			t.Errorf("ClaimedBy = %+v, want nil", updated.ClaimedBy)
		}
		if updated.ClaimedAt != nil {
			t.Errorf("ClaimedAt = %v, want nil", updated.ClaimedAt)
		}
		if updated.ClaimExpiry != nil {
			t.Errorf("ClaimExpiry = %v, want nil", updated.ClaimExpiry)
		}
		if updated.ClaimTTLSeconds != 0 {
			t.Errorf("ClaimTTLSeconds = %d, want 0", updated.ClaimTTLSeconds)
		}
		if updated.CompletedAt == nil {
			t.Errorf("CompletedAt is nil on done task")
		}

		// Re-fetch via Get to ensure persistence state
		fresh, err := svc.Get(ctx, task.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if fresh.ClaimedBy != nil || fresh.ClaimExpiry != nil {
			t.Errorf("persisted task has active lease: ClaimedBy=%+v ClaimExpiry=%v", fresh.ClaimedBy, fresh.ClaimExpiry)
		}
	})

	t.Run("doing -> todo clears active claim lease", func(t *testing.T) {
		task := mustCreateTask(t, svc, "Claimed Task 2", 0, nil)
		claimed, err := svc.Claim(ctx, testAgent, task.ID, 900, false)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.Claimed() {
			t.Fatalf("task not claimed")
		}

		todo := domain.StatusTodo
		updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo})
		if err != nil {
			t.Fatalf("Update to todo: %v", err)
		}
		if updated.Status != domain.StatusTodo {
			t.Errorf("status = %q, want todo", updated.Status)
		}
		if updated.Claimed() || updated.ClaimedBy != nil || updated.ClaimExpiry != nil || updated.ClaimTTLSeconds != 0 {
			t.Errorf("lease not cleared on moving doing -> todo: %+v", updated)
		}
	})

	t.Run("updating other fields in doing preserves active claim lease", func(t *testing.T) {
		task := mustCreateTask(t, svc, "Claimed Task 3", 0, nil)
		claimed, err := svc.Claim(ctx, testAgent, task.ID, 900, false)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.Claimed() {
			t.Fatalf("task not claimed")
		}

		newTitle := "Updated Title While Doing"
		updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &newTitle})
		if err != nil {
			t.Fatalf("Update title: %v", err)
		}
		if !updated.Claimed() || updated.ClaimedBy == nil {
			t.Errorf("lease was cleared when updating title in doing: %+v", updated)
		}
		if updated.ClaimedBy.Name != testAgent.Name {
			t.Errorf("ClaimedBy = %q, want %q", updated.ClaimedBy.Name, testAgent.Name)
		}
	})
}
