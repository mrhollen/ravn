package sqlite

// Integration tests proving domain.Task.LastActivity is derived on read as the
// MAX of the task row's updated_at and the latest child-row timestamp (comment,
// artifact, acceptance-criterion completion) — never by mutating the parent
// row. Every step appends a strictly newer child row and re-reads via Get so
// scanTask runs; Version and UpdatedAt must stay frozen throughout (derivation,
// not bumping). Controlled base + 1h increments keep each comparison
// unambiguous regardless of DB precision.

import (
	"context"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

func TestTaskLastActivity_DerivedFromChildRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	base := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	// Seed the task row directly so UpdatedAt is a controlled instant (the use
	// case's wall-clock stamp would blur every comparison below). Run fills r
	// with the transactional repos before calling the closure.
	taskID := domain.ID("last-act-task")
	var seed application.Repos
	err := NewUnitOfWork(db).Run(ctx, &seed, func() error {
		row := domain.Task{
			ID: taskID, ProjectID: p.ID, Title: "Child activity",
			Status: domain.StatusTodo, Version: 1, CreatedAt: base, UpdatedAt: base,
		}
		return seed.Tasks.Create(ctx, row)
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}

	get := func() domain.Task {
		t.Helper()
		got, err := tasks.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("Get after seed: %v", err)
		}
		return got
	}

	insert := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("insert child row (%s): %v", query, err)
		}
	}

	assertDerivedFrom := func(want time.Time, step string) domain.Task {
		t.Helper()
		got := get()
		if !got.LastActivity.Equal(want) {
			t.Errorf("%s: LastActivity = %s, want %s", step, got.LastActivity.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
		}
		// The whole point of the derivation: child rows never touch the parent.
		if !got.UpdatedAt.Equal(base) {
			t.Errorf("%s: UpdatedAt = %s, want unchanged %s", step, got.UpdatedAt.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano))
		}
		if got.Version != 1 {
			t.Errorf("%s: Version = %d, want unchanged 1", step, got.Version)
		}
		return got
	}

	// Baseline: no child rows => MAX is just updated_at (also the sanity case
	// for a brand-new task).
	got := get()
	if !got.LastActivity.Equal(base) || !got.LastActivity.Equal(got.UpdatedAt) {
		t.Fatalf("baseline: LastActivity = %s, want UpdatedAt %s", got.LastActivity.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano))
	}

	// Comment at +1h => the new max, strictly after updated_at.
	commentTS := base.Add(1 * time.Hour)
	insert(`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		VALUES ('last-act-c', ?, 'progress', 'a child row', 'agent', 'agent-a', ?)`,
		string(taskID), formatTS(commentTS))
	got = assertDerivedFrom(commentTS, "after comment")
	if !got.LastActivity.After(got.UpdatedAt) {
		t.Errorf("after comment: LastActivity %s is not after UpdatedAt %s", got.LastActivity, got.UpdatedAt)
	}

	// Artifact at +2h => replaces the comment as max.
	artifactTS := base.Add(2 * time.Hour)
	insert(`INSERT INTO artifact (id, task_id, kind, ref, summary, actor_type, actor_name, created_at)
		VALUES ('last-act-a', ?, 'log', 'https://example.com/log.txt', '', 'agent', 'agent-a', ?)`,
		string(taskID), formatTS(artifactTS))
	assertDerivedFrom(artifactTS, "after artifact")

	// AC completion at +3h (done = 1 with completed_at set) => the new max.
	acTS := base.Add(3 * time.Hour)
	insert(`INSERT INTO acceptance_criterion (id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at)
		VALUES ('last-act-ac', ?, 1, 'Prove it', 1, '', 'agent', 'agent-a', ?)`,
		string(taskID), formatTS(acTS))
	assertDerivedFrom(acTS, "after AC completion")

	// Older child rows must not move the max backwards.
	older := base.Add(2 * time.Hour).Add(5 * time.Minute)
	insert(`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		VALUES ('last-act-c2', ?, 'note', 'an older note', 'agent', 'agent-a', ?)`,
		string(taskID), formatTS(older))
	assertDerivedFrom(acTS, "after older comment")

	// Sanity: a brand-new task with NO children yields LastActivity == UpdatedAt.
	base2 := base.Add(6 * time.Hour)
	taskID2 := domain.ID("last-act-empty")
	var seed2 application.Repos
	if err := NewUnitOfWork(db).Run(ctx, &seed2, func() error {
		row := domain.Task{
			ID: taskID2, ProjectID: p.ID, Title: "No children",
			Status: domain.StatusTodo, Version: 1, CreatedAt: base2, UpdatedAt: base2,
		}
		return seed2.Tasks.Create(ctx, row)
	}); err != nil {
		t.Fatalf("seed empty task: %v", err)
	}
	got, err = tasks.Get(ctx, taskID2)
	if err != nil {
		t.Fatalf("Get empty task: %v", err)
	}
	if !got.LastActivity.Equal(got.UpdatedAt) {
		t.Errorf("empty task: LastActivity = %s, want UpdatedAt %s", got.LastActivity.Format(time.RFC3339Nano), got.UpdatedAt.Format(time.RFC3339Nano))
	}
}
