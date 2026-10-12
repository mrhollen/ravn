package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// TestChallenger_R5_DomainTransitionPermutations tests all pairwise combinations
// of statuses in domain.CanTransition to verify state machine invariants.
func TestChallenger_R5_DomainTransitionPermutations(t *testing.T) {
	canonicalStatuses := []domain.Status{
		domain.StatusTodo,
		domain.StatusDoing,
		domain.StatusDone,
	}

	invalidStatuses := []domain.Status{
		"",
		"invalid",
		"unknown",
		"TODO",
		"doing ",
		"done\n",
		"blocked",
	}

	allStatuses := append(canonicalStatuses, invalidStatuses...)

	// Define the expected transition matrix
	expectedAllowed := map[[2]domain.Status]bool{
		{domain.StatusTodo, domain.StatusDoing}: true,
		{domain.StatusDoing, domain.StatusDone}: true,
		{domain.StatusDoing, domain.StatusTodo}: true,
		{domain.StatusDone, domain.StatusTodo}:  true,
	}

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := expectedAllowed[[2]domain.Status{from, to}]
			got := domain.CanTransition(from, to)
			if got != want {
				t.Errorf("domain.CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

// TestChallenger_R5_ApplicationStatusTransitions tests all transition pairs
// through the application use case (Tasks.Update) backed by real SQLite storage.
func TestChallenger_R5_ApplicationStatusTransitions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	// Subtest 1: Valid Transitions
	t.Run("Valid transitions sequence", func(t *testing.T) {
		task, err := tasks.Create(ctx, testActor, application.TaskInput{
			ProjectID: p.ID,
			Title:     "Valid Transition Flow",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if task.Status != domain.StatusTodo {
			t.Fatalf("fresh status = %q, want todo", task.Status)
		}

		// todo -> doing
		doing := domain.StatusDoing
		task, err = tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &doing})
		if err != nil {
			t.Fatalf("Update todo -> doing: %v", err)
		}
		if task.Status != domain.StatusDoing {
			t.Errorf("status = %q, want doing", task.Status)
		}

		// doing -> done
		done := domain.StatusDone
		task, err = tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &done})
		if err != nil {
			t.Fatalf("Update doing -> done: %v", err)
		}
		if task.Status != domain.StatusDone {
			t.Errorf("status = %q, want done", task.Status)
		}
		if task.CompletedAt == nil {
			t.Errorf("CompletedAt is nil on done task")
		}

		// done -> todo (reopen)
		todo := domain.StatusTodo
		task, err = tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &todo})
		if err != nil {
			t.Fatalf("Update done -> todo: %v", err)
		}
		if task.Status != domain.StatusTodo {
			t.Errorf("status = %q, want todo", task.Status)
		}
		if task.CompletedAt != nil {
			t.Errorf("CompletedAt not cleared on reopen, got %v", task.CompletedAt)
		}

		// todo -> doing -> todo (give up / release via update)
		task, err = tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &doing})
		if err != nil {
			t.Fatalf("Update todo -> doing: %v", err)
		}
		task, err = tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &todo})
		if err != nil {
			t.Fatalf("Update doing -> todo: %v", err)
		}
		if task.Status != domain.StatusTodo {
			t.Errorf("status = %q, want todo", task.Status)
		}
	})

	// Subtest 2: Disallowed Transitions
	t.Run("Disallowed transitions return domain.ErrInvalid", func(t *testing.T) {
		// todo -> done (skipping doing)
		taskTodo, err := tasks.Create(ctx, testActor, application.TaskInput{
			ProjectID: p.ID,
			Title:     "Skip Doing Test",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		done := domain.StatusDone
		_, err = tasks.Update(ctx, testActor, taskTodo.ID, application.TaskUpdateInput{Status: &done})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Update todo -> done err = %v, want domain.ErrInvalid", err)
		}

		// Move to doing, then done
		doing := domain.StatusDoing
		taskDoing, err := tasks.Update(ctx, testActor, taskTodo.ID, application.TaskUpdateInput{Status: &doing})
		if err != nil {
			t.Fatalf("Update to doing: %v", err)
		}
		taskDone, err := tasks.Update(ctx, testActor, taskDoing.ID, application.TaskUpdateInput{Status: &done})
		if err != nil {
			t.Fatalf("Update to done: %v", err)
		}

		// done -> doing (cannot jump to doing without reopening to todo first)
		_, err = tasks.Update(ctx, testActor, taskDone.ID, application.TaskUpdateInput{Status: &doing})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Update done -> doing err = %v, want domain.ErrInvalid", err)
		}

		// Invalid status strings from any status
		invalidStatuses := []domain.Status{"", "invalid", "unknown", "TODO", "doing ", "done\n", "BLOCKED"}
		for _, inv := range invalidStatuses {
			invCopy := inv
			// from todo
			_, err = tasks.Update(ctx, testActor, taskTodo.ID, application.TaskUpdateInput{Status: &invCopy})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Update todo -> %q err = %v, want domain.ErrInvalid", inv, err)
			}
			// from done
			_, err = tasks.Update(ctx, testActor, taskDone.ID, application.TaskUpdateInput{Status: &invCopy})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Update done -> %q err = %v, want domain.ErrInvalid", inv, err)
			}
		}
	})

	// Subtest 3: Self Transitions Behavior in Tasks.Update
	t.Run("Self transitions behavior in Tasks.Update", func(t *testing.T) {
		task, err := tasks.Create(ctx, testActor, application.TaskInput{
			ProjectID: p.ID,
			Title:     "Self Transition Test",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		todo := domain.StatusTodo
		doing := domain.StatusDoing
		done := domain.StatusDone

		// Case A: todo -> todo
		t.Logf("Checking Tasks.Update todo -> todo")
		resTodo, errTodo := tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &todo})
		t.Logf("Tasks.Update(todo -> todo) result: err=%v, version=%d", errTodo, resTodo.Version)

		// Move to doing
		taskDoing, err := tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &doing})
		if err != nil {
			t.Fatalf("Update to doing: %v", err)
		}

		// Case B: doing -> doing
		t.Logf("Checking Tasks.Update doing -> doing")
		resDoing, errDoing := tasks.Update(ctx, testActor, taskDoing.ID, application.TaskUpdateInput{Status: &doing})
		t.Logf("Tasks.Update(doing -> doing) result: err=%v, version=%d", errDoing, resDoing.Version)

		// Move to done
		taskDone, err := tasks.Update(ctx, testActor, taskDoing.ID, application.TaskUpdateInput{Status: &done})
		if err != nil {
			t.Fatalf("Update to done: %v", err)
		}

		// Case C: done -> done
		t.Logf("Checking Tasks.Update done -> done")
		resDone, errDone := tasks.Update(ctx, testActor, taskDone.ID, application.TaskUpdateInput{Status: &done})
		t.Logf("Tasks.Update(done -> done) result: err=%v, version=%d", errDone, resDone.Version)
	})
}

// TestChallenger_R5_ClaimLeaseClearing_RawSQLite directly executes SQL queries
// against SQLite to verify that transitioning a task out of doing (doing -> done or doing -> todo)
// sets all 5 claim lease columns to literal SQL NULL.
func TestChallenger_R5_ClaimLeaseClearing_RawSQLite(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	claimActor := domain.Actor{Type: domain.ActorAgent, Name: "challenger-agent"}

	// Helper to inspect raw SQL columns
	inspectRawLeaseCols := func(t *testing.T, taskID domain.ID) (claimedByType, claimedByName, claimedAt, claimExpiresAt sql.NullString, claimTTLSec sql.NullInt64) {
		t.Helper()
		row := db.QueryRowContext(ctx, `
			SELECT claimed_by_type, claimed_by_name, claimed_at, claim_expires_at, claim_ttl_sec
			FROM task WHERE id = ?`, string(taskID))
		err := row.Scan(&claimedByType, &claimedByName, &claimedAt, &claimExpiresAt, &claimTTLSec)
		if err != nil {
			t.Fatalf("raw SQLite query task %s lease columns: %v", taskID, err)
		}
		return
	}

	t.Run("doing -> done clears all 5 lease columns in SQLite", func(t *testing.T) {
		task, err := tasks.Create(ctx, testActor, application.TaskInput{
			ProjectID: p.ID,
			Title:     "Lease Clearing Doing to Done",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Claim the task
		claimed, err := tasks.Claim(ctx, claimActor, task.ID, 900, false)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.Claimed() || claimed.ClaimedBy == nil {
			t.Fatalf("claimed domain state invalid: %+v", claimed)
		}

		// Verify raw SQLite has non-null lease
		cType, cName, cAt, cExp, cTTL := inspectRawLeaseCols(t, task.ID)
		if !cType.Valid || cType.String != "agent" {
			t.Errorf("raw SQLite claimed_by_type = %v, want 'agent'", cType)
		}
		if !cName.Valid || cName.String != claimActor.Name {
			t.Errorf("raw SQLite claimed_by_name = %v, want %q", cName, claimActor.Name)
		}
		if !cAt.Valid {
			t.Errorf("raw SQLite claimed_at is NULL, want timestamp")
		}
		if !cExp.Valid {
			t.Errorf("raw SQLite claim_expires_at is NULL, want timestamp")
		}
		if !cTTL.Valid || cTTL.Int64 != 900 {
			t.Errorf("raw SQLite claim_ttl_sec = %v, want 900", cTTL)
		}

		// Update to done
		done := domain.StatusDone
		updated, err := tasks.Update(ctx, claimActor, task.ID, application.TaskUpdateInput{Status: &done})
		if err != nil {
			t.Fatalf("Update to done: %v", err)
		}
		if updated.Status != domain.StatusDone {
			t.Fatalf("updated status = %q, want done", updated.Status)
		}
		if updated.Claimed() || updated.ClaimedBy != nil || updated.ClaimExpiry != nil || updated.ClaimTTLSeconds != 0 {
			t.Errorf("domain task still has lease after moving to done: %+v", updated)
		}

		// Inspect raw SQLite columns - ALL 5 MUST BE SQL NULL
		cTypeAfter, cNameAfter, cAtAfter, cExpAfter, cTTLAfter := inspectRawLeaseCols(t, task.ID)
		if cTypeAfter.Valid {
			t.Errorf("raw SQLite claimed_by_type is NOT NULL: %q", cTypeAfter.String)
		}
		if cNameAfter.Valid {
			t.Errorf("raw SQLite claimed_by_name is NOT NULL: %q", cNameAfter.String)
		}
		if cAtAfter.Valid {
			t.Errorf("raw SQLite claimed_at is NOT NULL: %q", cAtAfter.String)
		}
		if cExpAfter.Valid {
			t.Errorf("raw SQLite claim_expires_at is NOT NULL: %q", cExpAfter.String)
		}
		if cTTLAfter.Valid {
			t.Errorf("raw SQLite claim_ttl_sec is NOT NULL: %d", cTTLAfter.Int64)
		}
	})

	t.Run("doing -> todo clears all 5 lease columns in SQLite", func(t *testing.T) {
		task, err := tasks.Create(ctx, testActor, application.TaskInput{
			ProjectID: p.ID,
			Title:     "Lease Clearing Doing to Todo",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Claim the task
		claimed, err := tasks.Claim(ctx, claimActor, task.ID, 600, false)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.Claimed() {
			t.Fatalf("task not claimed")
		}

		// Update to todo
		todo := domain.StatusTodo
		updated, err := tasks.Update(ctx, claimActor, task.ID, application.TaskUpdateInput{Status: &todo})
		if err != nil {
			t.Fatalf("Update to todo: %v", err)
		}
		if updated.Status != domain.StatusTodo {
			t.Fatalf("updated status = %q, want todo", updated.Status)
		}

		// Inspect raw SQLite columns - ALL 5 MUST BE SQL NULL
		cTypeAfter, cNameAfter, cAtAfter, cExpAfter, cTTLAfter := inspectRawLeaseCols(t, task.ID)
		if cTypeAfter.Valid {
			t.Errorf("raw SQLite claimed_by_type is NOT NULL: %q", cTypeAfter.String)
		}
		if cNameAfter.Valid {
			t.Errorf("raw SQLite claimed_by_name is NOT NULL: %q", cNameAfter.String)
		}
		if cAtAfter.Valid {
			t.Errorf("raw SQLite claimed_at is NOT NULL: %q", cAtAfter.String)
		}
		if cExpAfter.Valid {
			t.Errorf("raw SQLite claim_expires_at is NOT NULL: %q", cExpAfter.String)
		}
		if cTTLAfter.Valid {
			t.Errorf("raw SQLite claim_ttl_sec is NOT NULL: %d", cTTLAfter.Int64)
		}
	})
}

// TestChallenger_R5_ReopenAndSubsequentClaim tests that once a task is completed (done)
// and then reopened (todo), subsequent claims by another agent succeed cleanly.
func TestChallenger_R5_ReopenAndSubsequentClaim(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	agent1 := domain.Actor{Type: domain.ActorAgent, Name: "agent-first"}
	agent2 := domain.Actor{Type: domain.ActorAgent, Name: "agent-second"}

	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		ProjectID: p.ID,
		Title:     "Reopen and Claim Test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 1. Agent 1 claims task
	claimed1, err := tasks.Claim(ctx, agent1, task.ID, 300, false)
	if err != nil {
		t.Fatalf("Agent 1 Claim: %v", err)
	}
	if claimed1.ClaimedBy.Name != agent1.Name {
		t.Fatalf("holder = %q, want %q", claimed1.ClaimedBy.Name, agent1.Name)
	}

	// 2. Move to done
	done := domain.StatusDone
	completed, err := tasks.Update(ctx, agent1, task.ID, application.TaskUpdateInput{Status: &done})
	if err != nil {
		t.Fatalf("Update to done: %v", err)
	}
	if completed.Status != domain.StatusDone {
		t.Fatalf("status = %q, want done", completed.Status)
	}
	if completed.CompletedAt == nil {
		t.Fatalf("CompletedAt is nil on completed task")
	}

	// 3. Reopen task to todo
	todo := domain.StatusTodo
	reopened, err := tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &todo})
	if err != nil {
		t.Fatalf("Reopen task: %v", err)
	}
	if reopened.Status != domain.StatusTodo {
		t.Fatalf("reopened status = %q, want todo", reopened.Status)
	}
	if reopened.CompletedAt != nil {
		t.Fatalf("reopened CompletedAt = %v, want nil", reopened.CompletedAt)
	}
	if reopened.Claimed() {
		t.Fatalf("reopened task still reports Claimed()")
	}

	// Verify raw SQLite completed_at is NULL
	var rawCompletedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT completed_at FROM task WHERE id = ?`, string(task.ID)).Scan(&rawCompletedAt); err != nil {
		t.Fatalf("query completed_at: %v", err)
	}
	if rawCompletedAt.Valid {
		t.Errorf("raw SQLite completed_at is NOT NULL on reopened task: %q", rawCompletedAt.String)
	}

	// 4. Agent 2 claims the reopened task
	claimed2, err := tasks.Claim(ctx, agent2, task.ID, 600, false)
	if err != nil {
		t.Fatalf("Agent 2 Claim on reopened task failed: %v", err)
	}
	if claimed2.Status != domain.StatusDoing {
		t.Errorf("claimed2 status = %q, want doing", claimed2.Status)
	}
	if claimed2.ClaimedBy == nil || claimed2.ClaimedBy.Name != agent2.Name {
		t.Errorf("claimed2 holder = %+v, want %q", claimed2.ClaimedBy, agent2.Name)
	}
	if claimed2.ClaimTTLSeconds != 600 {
		t.Errorf("claimed2 TTL = %d, want 600", claimed2.ClaimTTLSeconds)
	}

	// Verify raw SQLite has Agent 2's lease
	var rawHolder sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT claimed_by_name FROM task WHERE id = ?`, string(task.ID)).Scan(&rawHolder); err != nil {
		t.Fatalf("query claimed_by_name: %v", err)
	}
	if !rawHolder.Valid || rawHolder.String != agent2.Name {
		t.Errorf("raw SQLite holder = %v, want %q", rawHolder, agent2.Name)
	}
}

// TestChallenger_R5_NonStatusUpdatePreservesActiveLease tests that modifying other
// fields (title, description, priority, notes) while in doing does NOT clear the active lease.
func TestChallenger_R5_NonStatusUpdatePreservesActiveLease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	agent := domain.Actor{Type: domain.ActorAgent, Name: "lease-holder-agent"}
	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		ProjectID: p.ID,
		Title:     "Preserve Lease Test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	claimed, err := tasks.Claim(ctx, agent, task.ID, 900, false)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	claimExpiryBefore := *claimed.ClaimExpiry

	newTitle := "Updated Title"
	newDesc := "Updated Description"
	newPrio := domain.Priority(5)
	updated, err := tasks.Update(ctx, agent, task.ID, application.TaskUpdateInput{
		Title:       &newTitle,
		Description: &newDesc,
		Priority:    &newPrio,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if updated.Status != domain.StatusDoing {
		t.Errorf("status = %q, want doing", updated.Status)
	}
	if !updated.Claimed() || updated.ClaimedBy == nil || updated.ClaimedBy.Name != agent.Name {
		t.Errorf("lease was lost after metadata update: %+v", updated)
	}
	if updated.ClaimExpiry == nil || !updated.ClaimExpiry.Equal(claimExpiryBefore) {
		t.Errorf("claim expiry changed: before=%v after=%v", claimExpiryBefore, updated.ClaimExpiry)
	}

	// Verify in SQLite
	var dbHolder sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT claimed_by_name FROM task WHERE id = ?`, string(task.ID)).Scan(&dbHolder); err != nil {
		t.Fatalf("query claimed_by_name: %v", err)
	}
	if !dbHolder.Valid || dbHolder.String != agent.Name {
		t.Errorf("raw SQLite holder = %v, want %q", dbHolder, agent.Name)
	}
}

// TestChallenger_R5_ClaimDirectlyOnDoneTask tests what happens if an agent
// attempts to claim a task that is currently in "done" status without reopening it first.
func TestChallenger_R5_ClaimDirectlyOnDoneTask(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	agent1 := domain.Actor{Type: domain.ActorAgent, Name: "agent-1"}
	agent2 := domain.Actor{Type: domain.ActorAgent, Name: "agent-2"}

	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		ProjectID: p.ID,
		Title:     "Claim on Done Task Test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Move to doing then done
	if _, err := tasks.Claim(ctx, agent1, task.ID, 300, false); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	done := domain.StatusDone
	if _, err := tasks.Update(ctx, agent1, task.ID, application.TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("Update to done: %v", err)
	}

	// Now try to Claim directly while in done:
	// Does the application/store allow or reject claiming a done task?
	got, claimErr := tasks.Claim(ctx, agent2, task.ID, 600, false)
	t.Logf("Empirical observation: Claim on 'done' task result: err=%v, status=%q, completed_at=%v",
		claimErr, got.Status, got.CompletedAt)
}
