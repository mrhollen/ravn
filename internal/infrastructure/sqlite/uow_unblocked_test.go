package sqlite

// uow_unblocked_test.go: slice 2D2b integration tests over a real temp-file
// SQLite database — the UnblockedOnly NOT EXISTS guard (rows AND total count),
// GetDetail's computed Blocked flag / blocker list flipping when the blocker
// completes, and FindDuplicateCandidates' project-scoped title similarity.
// These prove the SQL encodes exactly what the application-layer fakes assume.

import (
	"context"
	"testing"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
)

func mustCreateTaskIn(t *testing.T, tasks *application.Tasks, p domain.Project, title string) domain.Task {
	t.Helper()
	task, err := tasks.Create(context.Background(), testActor, application.TaskInput{Title: title, ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create %q: %v", title, err)
	}
	return task
}

func assertIDs(t *testing.T, rows []domain.Task, want ...domain.ID) {
	t.Helper()
	if len(rows) != len(want) {
		titles := make([]string, 0, len(rows))
		for _, r := range rows {
			titles = append(titles, r.Title)
		}
		t.Fatalf("rows = %v (%d), want %d tasks", titles, len(rows), len(want))
	}
	wantSet := make(map[domain.ID]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	for _, r := range rows {
		if !wantSet[r.ID] {
			t.Errorf("unexpected row %s (%q)", r.ID, r.Title)
		}
	}
}

func TestUnblockedOnlyFilterAndBlockedFlag(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	rels := application.NewRelations(NewUnitOfWork(db), clock.New(), idgen.New())
	p := mustCreateProject(t, ctx, projects)

	x := mustCreateTaskIn(t, tasks, p, "Blocked feature")
	y := mustCreateTaskIn(t, tasks, p, "Foundation work")
	if _, err := rels.Relate(ctx, testActor, x.ID, y.ID, domain.RelationBlockedBy, application.RelateAdd); err != nil {
		t.Fatalf("Relate blocked-by: %v", err)
	}

	// While the blocker is open: X is excluded from the unblocked list and the
	// total count agrees with the rows (same predicate on both queries).
	rows, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, UnblockedOnly: true})
	if err != nil {
		t.Fatalf("List unblocked: %v", err)
	}
	assertIDs(t, rows, y.ID) // Y itself has no blockers — it stays listed; X does not
	if total != 1 {
		t.Errorf("total = %d, want 1 (count must use the same NOT EXISTS predicate)", total)
	}

	// GetDetail carries the computed flag and the light blocker summary.
	detail, err := tasks.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if !detail.Blocked || len(detail.Blockers) != 1 || detail.Blockers[0].ID != y.ID ||
		detail.Blockers[0].Status != domain.StatusTodo {
		t.Errorf("detail = blocked %v blockers %+v, want true + [y/todo]", detail.Blocked, detail.Blockers)
	}

	// Complete the blocker via the version-guarded update path.
	done := domain.StatusDone
	if _, err := tasks.Update(ctx, testActor, y.ID, application.TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}

	// X re-enters the unblocked list and its flag flips.
	rows, total, err = tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, UnblockedOnly: true})
	if err != nil {
		t.Fatalf("List after completion: %v", err)
	}
	assertIDs(t, rows, y.ID, x.ID) // canonical order: created_at ASC (equal priority/due)
	if total != 2 {
		t.Errorf("total = %d, want 2 after the blocker completed", total)
	}

	detail, err = tasks.GetDetail(ctx, x.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail after completion: %v", err)
	}
	if detail.Blocked || len(detail.Blockers) != 0 {
		t.Errorf("after blocker done: blocked %v blockers %+v, want false/empty", detail.Blocked, detail.Blockers)
	}
}

func TestFindDuplicateCandidates_Integration(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	auth := mustCreateTaskIn(t, tasks, p, "Refactor auth")
	authExt := mustCreateTaskIn(t, tasks, p, "Refactor authentication")
	mustCreateTaskIn(t, tasks, p, "Completely unrelated thing")

	// Case-insensitive, punctuation-tolerant lookup: both refactors surface,
	// the unrelated task does not.
	got, err := tasks.FindDuplicateCandidates(ctx, p.ID, "refactor AUTH!", 0) // default limit (5)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	assertIDs(t, got, auth.ID, authExt.ID) // score 2 (substring) before score 1 (token overlap)

	capped, err := tasks.FindDuplicateCandidates(ctx, p.ID, "refactor AUTH!", 1)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates limit=1: %v", err)
	}
	assertIDs(t, capped, auth.ID)

	// Done tasks are not candidates ("open" = not done/archived).
	done := domain.StatusDone
	if _, err := tasks.Update(ctx, testActor, auth.ID, application.TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("complete candidate: %v", err)
	}
	got, err = tasks.FindDuplicateCandidates(ctx, p.ID, "refactor AUTH!", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates after completion: %v", err)
	}
	assertIDs(t, got, authExt.ID)
}
