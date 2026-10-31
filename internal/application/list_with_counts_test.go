package application

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// list_with_counts_test.go: unit tests for Projects.ListWithCounts over the
// in-memory fakes — no SQLite, no HTTP. It pins the fold logic that turns the
// dense (project, status) aggregates from TaskRepository.OpenTaskCounts into a
// per-project OpenTasks map with every canonical status always present (zero
// when empty), which is exactly where an off-by-one or a missed zero-fill would
// surface.

func newLWCEnv(t *testing.T, at time.Time) (*Projects, *fakeUoW) {
	t.Helper()
	uow := newFakeUoW(newFakeProjects(), &fakeEvents{})
	svc := NewProjects(uow, newFakeClock(at), &fakeIDGen{})
	return svc, uow
}

// assertLWC checks the row order AND that every row's OpenTasks map equals the
// expected per-status counts (which must always carry todo/doing/done).
func assertLWC(t *testing.T, rows []ProjectRow, order []domain.ID, want map[domain.ID]map[domain.Status]int64) {
	t.Helper()
	if len(rows) != len(order) {
		t.Fatalf("len(rows) = %d, want %d", len(rows), len(order))
	}
	for i, id := range order {
		if rows[i].Project.ID != id {
			t.Errorf("rows[%d].Project.ID = %q, want %q (order not created_at ASC?)", i, rows[i].Project.ID, id)
		}
	}
	for _, r := range rows {
		got := r.OpenTasks
		exp, ok := want[r.Project.ID]
		if !ok {
			t.Errorf("row for %q has no expected counts", r.Project.ID)
			continue
		}
		// The wire shape is fixed: every canonical status is present, zero-filled.
		for _, st := range domain.CanonicalStatuses() {
			if _, present := got[st]; !present {
				t.Errorf("OpenTasks for %q missing status %q (must be zero-filled)", r.Project.ID, st)
			}
		}
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("OpenTasks[%s] = %v, want %v", r.Project.ID, got, exp)
		}
	}
}

func TestProjectsListWithCounts_EmptyDatabase_ReturnsNoRows(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, testStart)
	svc, _ := newLWCEnv(t, at)
	rows, err := svc.ListWithCounts(ctx)
	if err != nil {
		t.Fatalf("ListWithCounts: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty for a project with no tasks", rows)
	}
}

func TestProjectsListWithCounts_MultiProjectFanOut_ZeroFillsAndExcludesArchivedTasks(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, testStart)
	svc, uow := newLWCEnv(t, at)

	// Seed projects with distinct created_at so the created_at ASC order is
	// observable (p1 < p2 < p3).
	uow.projects.byID["p1"] = domain.Project{ID: "p1", Name: "P1", CreatedAt: at, UpdatedAt: at}
	uow.projects.byID["p2"] = domain.Project{ID: "p2", Name: "P2", CreatedAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour)}
	uow.projects.byID["p3"] = domain.Project{ID: "p3", Name: "P3", CreatedAt: at.Add(2 * time.Hour), UpdatedAt: at.Add(2 * time.Hour)}

	// p1: two todo, one doing, one done, one archived (must be excluded).
	for _, id := range []domain.ID{"t1", "t2"} {
		uow.tasks.byID[id] = domain.Task{ID: id, ProjectID: "p1", Title: string(id), Status: domain.StatusTodo, Version: 1, CreatedAt: at, UpdatedAt: at}
	}
	uow.tasks.byID["t3"] = domain.Task{ID: "t3", ProjectID: "p1", Title: "doing", Status: domain.StatusDoing, Version: 1, CreatedAt: at, UpdatedAt: at}
	uow.tasks.byID["t4"] = domain.Task{ID: "t4", ProjectID: "p1", Title: "done", Status: domain.StatusDone, Version: 1, CreatedAt: at, UpdatedAt: at}
	uow.tasks.byID["t5"] = domain.Task{ID: "t5", ProjectID: "p1", Title: "archived", Status: domain.StatusTodo, Archived: true, Version: 1, CreatedAt: at, UpdatedAt: at}

	// p2: two doing.
	for _, id := range []domain.ID{"u1", "u2"} {
		uow.tasks.byID[id] = domain.Task{ID: id, ProjectID: "p2", Title: string(id), Status: domain.StatusDoing, Version: 1, CreatedAt: at, UpdatedAt: at}
	}

	// p3: intentionally has NO tasks.

	rows, err := svc.ListWithCounts(ctx)
	if err != nil {
		t.Fatalf("ListWithCounts: %v", err)
	}

	assertLWC(t, rows, []domain.ID{"p1", "p2", "p3"}, map[domain.ID]map[domain.Status]int64{
		"p1": {domain.StatusTodo: 2, domain.StatusDoing: 1, domain.StatusDone: 1}, // t5 archived excluded → todo 2 not 3
		"p2": {domain.StatusTodo: 0, domain.StatusDoing: 2, domain.StatusDone: 0}, // zero-filled todo/done
		"p3": {domain.StatusTodo: 0, domain.StatusDoing: 0, domain.StatusDone: 0}, // no tasks → all zeros
	})
}

func TestProjectsListWithCounts_DoneOnlyProject_CountsUnderDone(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, testStart)
	svc, uow := newLWCEnv(t, at)

	uow.projects.byID["pd"] = domain.Project{ID: "pd", Name: "DoneOnly", CreatedAt: at, UpdatedAt: at}
	for _, id := range []domain.ID{"d1", "d2", "d3"} {
		uow.tasks.byID[id] = domain.Task{ID: id, ProjectID: "pd", Title: string(id), Status: domain.StatusDone, Version: 1, CreatedAt: at, UpdatedAt: at}
	}

	rows, err := svc.ListWithCounts(ctx)
	if err != nil {
		t.Fatalf("ListWithCounts: %v", err)
	}

	assertLWC(t, rows, []domain.ID{"pd"}, map[domain.ID]map[domain.Status]int64{
		"pd": {domain.StatusTodo: 0, domain.StatusDoing: 0, domain.StatusDone: 3}, // done tasks count under "done"
	})
}
