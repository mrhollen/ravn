package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

func asTaskUnsatisfiedACs(t *testing.T, err error) *domain.TaskUnsatisfiedACsError {
	t.Helper()
	var ue *domain.TaskUnsatisfiedACsError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want *domain.TaskUnsatisfiedACsError", err, err)
	}
	return ue
}

func TestUpdateDone_BlockedTask_RejectedWithTaskBlockedError(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Needs foundation", 2, nil)
	y := mustCreateTask(t, svc, "Build foundation", 3, nil)
	mustBlock(t, rels, x, y)

	// Move x to doing
	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Minute))
	eventsBefore := len(uow.events.events)

	// Attempt move doing -> done while blocked
	done := domain.StatusDone
	_, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{Status: &done})
	tb := asTaskBlocked(t, err)
	if tb.TaskID != x.ID {
		t.Errorf("error TaskID = %s, want %s", tb.TaskID, x.ID)
	}
	if len(tb.BlockerIDs) != 1 || tb.BlockerIDs[0] != y.ID {
		t.Fatalf("error BlockerIDs = %v, want [%s]", tb.BlockerIDs, y.ID)
	}

	// Verify no row mutation and no event emitted
	stored, err := svc.Get(ctx, x.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != domain.StatusDoing {
		t.Errorf("status = %q, want doing (unmutated)", stored.Status)
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("events grew from %d to %d; rejection must emit nothing", eventsBefore, len(uow.events.events))
	}
}

func TestUpdateDone_BlockedTask_OverrideBlockersSucceeds(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Needs foundation", 2, nil)
	y := mustCreateTask(t, svc, "Build foundation", 3, nil)
	mustBlock(t, rels, x, y)

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	done := domain.StatusDone
	updated, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{
		Status:           &done,
		OverrideBlockers: true,
	})
	if err != nil {
		t.Fatalf("Update with OverrideBlockers: %v", err)
	}
	if updated.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", updated.Status)
	}
}

func TestUpdateDone_ResolvedBlockers_SucceedsNormally(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Needs foundation", 2, nil)
	y := mustCreateTask(t, svc, "Build foundation", 3, nil)
	mustBlock(t, rels, x, y)

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	// Resolve blocker y by completing it
	mustDone(t, svc, y.ID)

	done := domain.StatusDone
	updated, err := svc.Update(ctx, testAgent, x.ID, TaskUpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("Update after blocker resolved: %v", err)
	}
	if updated.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", updated.Status)
	}
}

func TestUpdateDone_UnmetAcceptanceCriteria_RejectedWithTaskUnsatisfiedACsError(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Feature with criteria",
		ProjectID: testProjectID,
		ACs:       []string{"Write tests", "Implement logic", "Update docs"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Minute))
	eventsBefore := len(uow.events.events)

	// All 3 criteria are incomplete
	done := domain.StatusDone
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	ue := asTaskUnsatisfiedACs(t, err)
	if ue.TaskID != task.ID {
		t.Errorf("TaskID = %s, want %s", ue.TaskID, task.ID)
	}
	if len(ue.UnmetSeqs) != 3 || ue.UnmetSeqs[0] != 1 || ue.UnmetSeqs[1] != 2 || ue.UnmetSeqs[2] != 3 {
		t.Fatalf("UnmetSeqs = %v, want [1, 2, 3]", ue.UnmetSeqs)
	}

	// Verify no row mutation or event
	stored, err := svc.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != domain.StatusDoing {
		t.Errorf("stored status = %q, want doing", stored.Status)
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("events grew from %d to %d; rejection must emit nothing", eventsBefore, len(uow.events.events))
	}

	// Toggle AC 1 to done
	acSvc := NewAcceptance(uow, fc, &fakeIDGen{})
	if _, err := acSvc.ToggleAC(ctx, testAgent, task.ID, 1, true, "tested"); err != nil {
		t.Fatalf("ToggleAC 1: %v", err)
	}

	// Now only criteria 2 and 3 are unmet
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	ue = asTaskUnsatisfiedACs(t, err)
	if len(ue.UnmetSeqs) != 2 || ue.UnmetSeqs[0] != 2 || ue.UnmetSeqs[1] != 3 {
		t.Fatalf("UnmetSeqs = %v, want [2, 3]", ue.UnmetSeqs)
	}
}

func TestUpdateDone_UnmetAcceptanceCriteria_OverrideACsSucceeds(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Feature with criteria",
		ProjectID: testProjectID,
		ACs:       []string{"Write tests"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	done := domain.StatusDone
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{
		Status:      &done,
		OverrideACs: true,
	})
	if err != nil {
		t.Fatalf("Update with OverrideACs: %v", err)
	}
	if updated.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", updated.Status)
	}
}

func TestUpdateDone_AllCriteriaCompleted_SucceedsNormally(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Feature with criteria",
		ProjectID: testProjectID,
		ACs:       []string{"Write tests", "Run tests"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	// Toggle all criteria to done
	acSvc := NewAcceptance(uow, fc, &fakeIDGen{})
	if _, err := acSvc.ToggleAC(ctx, testAgent, task.ID, 1, true, "done"); err != nil {
		t.Fatalf("ToggleAC 1: %v", err)
	}
	if _, err := acSvc.ToggleAC(ctx, testAgent, task.ID, 2, true, "done"); err != nil {
		t.Fatalf("ToggleAC 2: %v", err)
	}

	done := domain.StatusDone
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("Update when criteria satisfied: %v", err)
	}
	if updated.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", updated.Status)
	}
}

func TestUpdateDone_BlockedAndUnmetACs_BlockerCheckedFirst(t *testing.T) {
	ctx := context.Background()
	svc, rels, _, _ := newClaimBlockedEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Blocked and incomplete ACs",
		ProjectID: testProjectID,
		ACs:       []string{"Write tests"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	blocker := mustCreateTask(t, svc, "Blocker", 3, nil)
	mustBlock(t, rels, task, blocker)

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	done := domain.StatusDone

	// 1. Both gates active -> blocker error first
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &done})
	asTaskBlocked(t, err)

	// 2. Override blockers -> AC error next
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{
		Status:           &done,
		OverrideBlockers: true,
	})
	asTaskUnsatisfiedACs(t, err)

	// 3. Both overridden -> success
	updated, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{
		Status:           &done,
		OverrideBlockers: true,
		OverrideACs:      true,
	})
	if err != nil {
		t.Fatalf("Update with both overrides: %v", err)
	}
	if updated.Status != domain.StatusDone {
		t.Errorf("status = %q, want done", updated.Status)
	}
}

func TestUpdateDone_ReopenToTodo_SkipsDoneGates(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:     "Feature",
		ProjectID: testProjectID,
		ACs:       []string{"AC 1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	doing := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move to doing: %v", err)
	}

	// Move to done with OverrideACs
	done := domain.StatusDone
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{
		Status:      &done,
		OverrideACs: true,
	}); err != nil {
		t.Fatalf("move to done: %v", err)
	}

	// Reopen to todo: should succeed without error
	todo := domain.StatusTodo
	reopened, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo})
	if err != nil {
		t.Fatalf("reopen to todo: %v", err)
	}
	if reopened.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", reopened.Status)
	}
}
