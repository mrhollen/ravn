package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

func TestTasksList_CanonicalOrder_SubsecondDueDates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	due1 := base                             // 12:00:00.000000000Z
	due2 := base.Add(500 * time.Millisecond) // 12:00:00.500000000Z
	due3 := base.Add(1 * time.Second)        // 12:00:01.000000000Z

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		// Insert in reverse order to ensure DB doesn't just return insertion order
		for _, s := range []struct {
			id    string
			title string
			due   time.Time
		}{
			{"task-3", "Due 1s later", due3},
			{"task-2", "Due 500ms later", due2},
			{"task-1", "Due at whole second", due1},
		} {
			row := domain.Task{
				ID: domain.ID(s.id), ProjectID: p.ID, Title: s.title,
				Status: domain.StatusTodo, Priority: domain.Priority(5), DueAt: &s.due,
				Version: 1, CreatedAt: base, UpdatedAt: base,
			}
			if err := r.Tasks.Create(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	rows, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	wantIDs := []domain.ID{"task-1", "task-2", "task-3"}
	for i, wantID := range wantIDs {
		if rows[i].ID != wantID {
			t.Errorf("row[%d] = %s (due %v), want %s (due %v)",
				i, rows[i].ID, rows[i].DueAt, wantID, wantIDs[i])
		}
	}
}

func TestClaim_SubsecondPrecision_NoPrematureStealing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 500000000, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Subsecond lease", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Agent A claims with 30s TTL. Expiry = 12:00:30.500000000Z.
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// At 12:00:30.000000000Z (whole second, 500ms BEFORE expiration):
	// Agent A's lease is still active! Agent B must NOT be able to steal it.
	tc.now = time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	_, err = tasks.Claim(ctx, agentB, task.ID, 30, false)
	if err == nil {
		t.Fatalf("Agent B stole active lease 500ms before expiration! (premature steal bug)")
	}
	if !isClaimConflict(err) {
		t.Fatalf("expected ClaimConflictError, got %v", err)
	}

	// At 12:00:30.600000000Z (100ms AFTER expiration):
	// Agent B's takeover must succeed (zombie heal).
	tc.now = time.Date(2026, 10, 1, 12, 0, 30, 600000000, time.UTC)
	got, err := tasks.Claim(ctx, agentB, task.ID, 30, false)
	if err != nil {
		t.Fatalf("Agent B takeover after expiration failed: %v", err)
	}
	if got.ClaimedBy == nil || got.ClaimedBy.Name != agentB.Name {
		t.Errorf("holder after takeover = %+v, want agent-b", got.ClaimedBy)
	}
}

func TestRelease_SubsecondActiveLease_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 500000000, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Subsecond release", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Claim expires at 12:00:30.500000000Z
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// Agent A calls Release at 12:00:30.000000000Z (whole second, lease still active)
	tc.now = time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	released, err := tasks.Release(ctx, agentA, task.ID, "")
	if err != nil {
		t.Fatalf("Release of active lease at whole second failed: %v", err)
	}
	if released.Status != domain.StatusTodo || released.ClaimedBy != nil {
		t.Errorf("task after release = status %q holder %+v, want todo and nil holder", released.Status, released.ClaimedBy)
	}
}

func TestClaimAndHeartbeat_DoneAndArchivedTasks_RejectedOnSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tc := &testClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	// 1. Task completed (status = done)
	taskDone, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Done task", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, taskDone.ID, 30, false); err != nil {
		t.Fatalf("claim: %v", err)
	}
	doneSt := domain.StatusDone
	if _, err := tasks.Update(ctx, agentA, taskDone.ID, application.TaskUpdateInput{Status: &doneSt}); err != nil {
		t.Fatalf("update to done: %v", err)
	}

	// Claiming done task must fail with ErrInvalid and not mutate status
	_, err = tasks.Claim(ctx, agentB, taskDone.ID, 30, false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("claim done task err = %v, want ErrInvalid", err)
	}
	storedDone, _ := tasks.Get(ctx, taskDone.ID)
	if storedDone.Status != domain.StatusDone {
		t.Errorf("stored done status = %q, want done", storedDone.Status)
	}
	if storedDone.ClaimedBy != nil {
		t.Errorf("stored ClaimedBy = %+v, want nil", storedDone.ClaimedBy)
	}

	// Heartbeating done task must fail with ErrInvalid
	_, err = tasks.Heartbeat(ctx, agentA, taskDone.ID)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("heartbeat done task err = %v, want ErrInvalid", err)
	}

	// 2. Task archived
	taskArchived, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Archived task", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	arch := true
	if _, err := tasks.Update(ctx, agentA, taskArchived.ID, application.TaskUpdateInput{Archived: &arch}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Claiming archived task must fail with ErrInvalid
	_, err = tasks.Claim(ctx, agentA, taskArchived.ID, 30, false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("claim archived task err = %v, want ErrInvalid", err)
	}
	storedArchived, _ := tasks.Get(ctx, taskArchived.ID)
	if !storedArchived.Archived {
		t.Error("task must remain archived")
	}
	if storedArchived.Status != domain.StatusTodo {
		t.Errorf("stored archived status = %q, want todo", storedArchived.Status)
	}

	// Heartbeating archived task must fail with ErrInvalid
	_, err = tasks.Heartbeat(ctx, agentA, taskArchived.ID)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("heartbeat archived task err = %v, want ErrInvalid", err)
	}
}

func TestRelease_DoneTask_PreservesDoneStatusOnSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tc := &testClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Release done task", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, task.ID, 60, false); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Set status to done directly in SQLite while keeping active lease (simulating pre-update state or legacy claim)
	_, err = db.ExecContext(ctx, "UPDATE task SET status = 'done' WHERE id = ?", string(task.ID))
	if err != nil {
		t.Fatalf("direct db update to done: %v", err)
	}

	released, err := tasks.Release(ctx, agentA, task.ID, "finished")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.Status != domain.StatusDone {
		t.Errorf("status after release = %q, want done (not reverted to todo)", released.Status)
	}
	if released.ClaimedBy != nil {
		t.Errorf("holder after release = %+v, want nil", released.ClaimedBy)
	}
}
