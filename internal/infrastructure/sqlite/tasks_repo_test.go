package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
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
	byGot := got.Claim.By()
	if byGot == nil || byGot.Name != agentB.Name {
		t.Errorf("holder after takeover = %+v, want agent-b", byGot)
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
	if released.Status != domain.StatusTodo || released.Claim.Present() {
		t.Errorf("task after release = status %q holder %+v expiry %v, want todo and nil lease",
			released.Status, released.Claim.By(), released.Claim.Expiry())
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
	if storedDone.Claim.Present() {
		t.Errorf("stored lease present (holder %+v expiry %v), want none", storedDone.Claim.By(), storedDone.Claim.Expiry())
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
	if released.Claim.Present() {
		t.Errorf("lease present after release (holder %+v expiry %v), want none", released.Claim.By(), released.Claim.Expiry())
	}
}

func TestRelease_ExpiredLease_OriginalHolderSucceeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Expired release", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// Advance time past the 30s TTL
	tc.now = base.Add(45 * time.Second)

	// Original holder releases expired lease — must succeed
	released, err := tasks.Release(ctx, agentA, task.ID, "done after lease expired")
	if err != nil {
		t.Fatalf("Release by original holder of expired lease failed: %v", err)
	}
	if released.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", released.Status)
	}
	if released.Claim.Present() {
		t.Errorf("lease present after release (holder %+v expiry %v), want none", released.Claim.By(), released.Claim.Expiry())
	}
	if released.Version != 3 {
		t.Errorf("version after release = %d, want 3", released.Version)
	}
}

func TestRelease_ExpiredLease_RivalFailsWithConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Expired rival release", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// Advance time past the 30s TTL
	tc.now = base.Add(45 * time.Second)

	// Rival tries to release expired lease — must fail with ClaimConflictError
	_, err = tasks.Release(ctx, agentB, task.ID, "rival release")
	if err == nil {
		t.Fatalf("expected ClaimConflictError when rival releases expired lease, got nil")
	}
	if !isClaimConflict(err) {
		t.Fatalf("expected isClaimConflict(err) == true, got %v", err)
	}

	var cc *domain.ClaimConflictError
	if errors.As(err, &cc) {
		if cc.Holder.Name != agentA.Name {
			t.Errorf("conflict holder = %q, want %q", cc.Holder.Name, agentA.Name)
		}
	} else {
		t.Fatalf("err is not *domain.ClaimConflictError")
	}

	// Stored row is untouched
	stored, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	byStored := stored.Claim.By()
	if stored.Status != domain.StatusDoing || byStored == nil || byStored.Name != agentA.Name {
		t.Errorf("stored task mutated by failed release: status=%q holder=%+v", stored.Status, byStored)
	}
}

func TestRelease_Force_ClearsLeaseForAnyActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	admin := domain.Actor{Type: domain.ActorHuman, Name: "admin-operator"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Force release test", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// Advance time past expiry
	tc.now = base.Add(45 * time.Second)

	// Admin force releases zombie lease
	released, err := tasks.Release(ctx, admin, task.ID, "clearing zombie", true)
	if err != nil {
		t.Fatalf("force release failed: %v", err)
	}
	if released.Status != domain.StatusTodo || released.Claim.Present() {
		t.Errorf("released task status %q, holder %+v expiry %v, want todo and none",
			released.Status, released.Claim.By(), released.Claim.Expiry())
	}
	if released.Version != 3 {
		t.Errorf("version = %d, want 3", released.Version)
	}

	// Repeated force release on now-unclaimed task is an idempotent no-op
	noop, err := tasks.Release(ctx, admin, task.ID, "repeat force", true)
	if err != nil {
		t.Fatalf("idempotent force release failed: %v", err)
	}
	if noop.Version != 3 || noop.Claim.Present() {
		t.Errorf("idempotent force release mutated row: v=%d holder=%+v expiry=%v",
			noop.Version, noop.Claim.By(), noop.Claim.Expiry())
	}
}

func TestExplainQueryPlan_TaskOrderIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	// AC 2: EXPLAIN QUERY PLAN confirms that SELECT ... ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC
	// scans idx_task_order directly without 'USE TEMP B-TREE FOR LAST 3 TERMS OF ORDER BY'.
	query := `EXPLAIN QUERY PLAN SELECT id FROM task WHERE project_id = ? AND archived = 0 ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC`
	rows, err := db.QueryContext(ctx, query, "01M00000000000000000000001")
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain rows: %v", err)
	}

	fullPlan := strings.Join(details, "\n")
	t.Logf("Query plan:\n%s", fullPlan)

	if !strings.Contains(fullPlan, "idx_task_order") {
		t.Errorf("expected plan to use idx_task_order, got: %s", fullPlan)
	}
	if strings.Contains(fullPlan, "USE TEMP B-TREE") {
		t.Errorf("plan unexpectedly contains temp B-tree filesort: %s", fullPlan)
	}
}

func TestMigration0009_TaskOrderIndex_EliminatesTempBTree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	// Explicitly recreate the old index without (due_at IS NULL) to simulate pre-0009 state
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_task_order`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX idx_task_order ON task(project_id, priority DESC, due_at ASC, created_at ASC) WHERE archived = 0`); err != nil {
		t.Fatalf("recreate old index: %v", err)
	}

	query := `EXPLAIN QUERY PLAN SELECT id FROM task WHERE project_id = ? AND archived = 0 ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC`

	explain := func() string {
		rows, err := db.QueryContext(ctx, query, "01M00000000000000000000001")
		if err != nil {
			t.Fatalf("explain query plan: %v", err)
		}
		defer rows.Close()

		var details []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatalf("scan explain row: %v", err)
			}
			details = append(details, detail)
		}
		return strings.Join(details, "\n")
	}

	// 1. With the old index, SQLite must emit USE TEMP B-TREE FOR LAST 3 TERMS OF ORDER BY
	oldPlan := explain()
	if !strings.Contains(oldPlan, "USE TEMP B-TREE FOR LAST 3 TERMS OF ORDER BY") {
		t.Fatalf("expected old index plan to use temp b-tree, got:\n%s", oldPlan)
	}

	// 2. Apply migration 0009 script
	m0009, err := migrationFS.ReadFile("migrations/0009_task_order_due_nulls.sql")
	if err != nil {
		t.Fatalf("read 0009 migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(m0009)); err != nil {
		t.Fatalf("execute 0009 migration: %v", err)
	}

	// 3. With the new index, SQLite must NOT emit USE TEMP B-TREE and must scan idx_task_order directly
	newPlan := explain()
	if !strings.Contains(newPlan, "idx_task_order") {
		t.Errorf("expected new index plan to use idx_task_order, got:\n%s", newPlan)
	}
	if strings.Contains(newPlan, "USE TEMP B-TREE") {
		t.Errorf("expected new index plan to eliminate temp b-tree, got:\n%s", newPlan)
	}
}
