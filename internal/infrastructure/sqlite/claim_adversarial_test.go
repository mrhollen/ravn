package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// TestStatusUpdate_ClearsClaimLeaseColumnsOnRealSQL directly executes SQL queries
// against SQLite to verify that transitioning a task out of doing (doing -> done or doing -> todo)
// sets all 5 claim lease columns to literal SQL NULL.
func TestStatusUpdate_ClearsClaimLeaseColumnsOnRealSQL(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	claimActor := domain.Actor{Type: domain.ActorAgent, Name: "lease-clearing-agent"}

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
		if !claimed.Claimed() || claimed.Claim.By() == nil {
			t.Fatalf("claimed domain state invalid: present=%v holder=%+v", claimed.Claim.Present(), claimed.Claim.By())
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
		if updated.Claimed() || updated.Claim.Present() {
			t.Errorf("domain task still has lease after moving to done: present=%v holder=%+v expiry=%v ttl=%d",
				updated.Claim.Present(), updated.Claim.By(), updated.Claim.Expiry(), updated.Claim.TTLSeconds())
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

// TestReopen_AllowsFreshClaimByAnotherAgent tests that once a task is completed (done)
// and then reopened (todo), subsequent claims by another agent succeed cleanly.
func TestReopen_AllowsFreshClaimByAnotherAgent(t *testing.T) {
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
	byClaimed1 := claimed1.Claim.By()
	if byClaimed1 == nil || byClaimed1.Name != agent1.Name {
		t.Fatalf("holder = %+v, want %q", byClaimed1, agent1.Name)
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
	byClaimed2 := claimed2.Claim.By()
	if byClaimed2 == nil || byClaimed2.Name != agent2.Name {
		t.Errorf("claimed2 holder = %+v, want %q", byClaimed2, agent2.Name)
	}
	if claimed2.Claim.TTLSeconds() != 600 {
		t.Errorf("claimed2 TTL = %d, want 600", claimed2.Claim.TTLSeconds())
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

// TestNonStatusUpdate_PreservesActiveLease tests that modifying other
// fields (title, description, priority, notes) while in doing does NOT clear the active lease.
func TestNonStatusUpdate_PreservesActiveLease(t *testing.T) {
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
	claimExpiryBefore := claimed.Claim.Expiry()

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
	byUpdated := updated.Claim.By()
	if !updated.Claimed() || byUpdated == nil || byUpdated.Name != agent.Name {
		t.Errorf("lease was lost after metadata update: present=%v holder=%+v", updated.Claim.Present(), byUpdated)
	}
	if !updated.Claim.Present() || !updated.Claim.Expiry().Equal(claimExpiryBefore) {
		t.Errorf("claim expiry changed: before=%v after=%v", claimExpiryBefore, updated.Claim.Expiry())
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
