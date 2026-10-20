package sqlite

// Integration tests over a real temp-file SQLite database for the slice-2D2a
// relation use case: the ≤1-parent partial unique index (migration 0003) must
// block a second 'child' row at the DB level even when the application layer's
// pre-check is bypassed, the composite upsert must be idempotent on real SQL,
// and add/remove must commit their relation-changed event in the same
// transaction as the edge row — with no task version bump.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
)

func newRelationUseCase(t *testing.T, db *sql.DB) (*application.Relations, *application.Tasks, *application.Projects) {
	t.Helper()
	uow := NewUnitOfWork(db) // one tx-bound Repos per Run; sharing uow/clock/idgen is safe
	return application.NewRelations(uow, clock.New(), idgen.New()),
		application.NewTasks(uow, clock.New(), idgen.New()),
		application.NewProjects(uow, clock.New(), idgen.New())
}

// childEdgeCountAndTarget returns (count, other_task_id) of the kind='child'
// edges leaving taskID — "which parent does this task point at".
func childEdgeCountAndTarget(t *testing.T, db *sql.DB, ctx context.Context, taskID domain.ID) (int, string) {
	t.Helper()
	var n int
	var other string
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(other_task_id, '') FROM relation WHERE task_id = ? AND kind = 'child'`,
		string(taskID)).Scan(&n, &other)
	if err != nil {
		t.Fatalf("query child edges of %s: %v", taskID, err)
	}
	return n, other
}

// rawInsertChildEdge bypasses every application check and tries to insert a
// second 'child' edge directly — the probe that proves the partial unique
// index (not just the use case's pre-check) enforces ≤1 parent.
func rawInsertChildEdge(t *testing.T, db *sql.DB, ctx context.Context, src, dst domain.ID, id string) error {
	t.Helper()
	_, err := db.ExecContext(ctx,
		`INSERT INTO relation (id, task_id, other_task_id, kind, created_at) VALUES (?, ?, ?, 'child', ?)`,
		id, string(src), string(dst), time.Now().UTC().Format(time.RFC3339))
	return err
}

// TestRelations_RealSQL_SecondParentBlockedAtDBLevel is the slice's core
// scenario: A child-of B succeeds; A child-of C fails with
// domain.ErrParentConflict; and the partial UNIQUE index blocks a second
// 'child' row for A even on a raw INSERT that skips the use case entirely.
func TestRelations_RealSQL_SecondParentBlockedAtDBLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	relations, tasks, projects := newRelationUseCase(t, db)

	p := mustCreateProject(t, ctx, projects)
	var a, b, c domain.Task
	for _, title := range []string{"A", "B", "C"} {
		var created domain.Task
		var err error
		created, err = tasks.Create(ctx, testActor, application.TaskInput{Title: title, ProjectID: p.ID})
		if err != nil {
			t.Fatalf("create task %s: %v", title, err)
		}
		switch title {
		case "A":
			a = created
		case "B":
			b = created
		case "C":
			c = created
		}
	}

	// (1) A child-of B — the first parent, allowed.
	stored, _, err := relations.Relate(ctx, testActor, a.ID, b.ID, domain.RelationChild, application.RelateAdd)
	if err != nil {
		t.Fatalf("relate A child-of B: %v", err)
	}
	if stored.TaskID != a.ID || stored.OtherTaskID != b.ID || stored.Kind != domain.RelationChild {
		t.Fatalf("stored edge = %+v, want (A -> B, child)", stored)
	}

	// (2) A child-of C — a SECOND, different parent, rejected by the ≤1-parent rule.
	_, _, err = relations.Relate(ctx, testActor, a.ID, c.ID, domain.RelationChild, application.RelateAdd)
	if !errors.Is(err, domain.ErrParentConflict) {
		t.Fatalf("second parent error = %v, want domain.ErrParentConflict", err)
	}
	if !strings.Contains(err.Error(), "already has a parent") {
		t.Errorf("error message %q should say the task already has a parent", err)
	}

	// (3) DB-level verification: A has exactly ONE child edge, pointing at B.
	n, other := childEdgeCountAndTarget(t, db, ctx, a.ID)
	if n != 1 || other != string(b.ID) {
		t.Fatalf("relation rows for A = (%d, %q), want exactly one 'child' edge to %s", n, other, b.ID)
	}

	// (4) The UNIQUE(partial) index itself: a raw INSERT of a second child row,
	// skipping the use case's pre-check, must fail with a SQLite constraint
	// violation — and leave A's edge set unchanged.
	if err := rawInsertChildEdge(t, db, ctx, a.ID, c.ID, "raw-child-1"); !isConstraintViolation(err) {
		t.Fatalf("raw second-child INSERT error = %v, want SQLITE_CONSTRAINT from the partial unique index", err)
	}
	if n, other := childEdgeCountAndTarget(t, db, ctx, a.ID); n != 1 || other != string(b.ID) {
		t.Fatalf("after blocked raw insert A has (%d, %q) child edges, want still (1, %s)", n, other, b.ID)
	}

	// The index name exists in sqlite_master — migration 0003 landed.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_relation_one_parent_child'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("index idx_relation_one_parent_child present %d times, want exactly 1", count)
	}
}

// TestRelations_RealSQL_UpsertIdempotentOneRow proves the retry-safe upsert on
// real SQL: adding the same edge twice through the use case converges to a
// single row and a single event, and the composite triple unique (0001 table
// constraint) rejects a blind duplicate INSERT.
func TestRelations_RealSQL_UpsertIdempotentOneRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	relations, tasks, projects := newRelationUseCase(t, db)

	p := mustCreateProject(t, ctx, projects)
	a, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "A", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "B", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := relations.Relate(ctx, testActor, a.ID, b.ID, domain.RelationChild, application.RelateAdd); err != nil {
			t.Fatalf("add #%d: %v", i+1, err)
		}
	}

	var rows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM relation WHERE task_id = ? AND other_task_id = ? AND kind = 'child'`,
		string(a.ID), string(b.ID)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("stored edge rows after double add = %d, want exactly 1", rows)
	}

	// Only the FIRST add is a state change: one relation-changed event.
	var events int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE kind = 'relation-changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("relation-changed events after double add = %d, want 1 (the no-op appends none)", events)
	}

	// The composite triple unique is what the upsert's ON CONFLICT leans on: a
	// blind duplicate INSERT must fail as a constraint violation.
	_, err = db.ExecContext(ctx,
		`INSERT INTO relation (id, task_id, other_task_id, kind, created_at) VALUES ('dup-1', ?, ?, 'child', ?)`,
		string(a.ID), string(b.ID), time.Now().UTC().Format(time.RFC3339))
	if !isConstraintViolation(err) {
		t.Fatalf("blind duplicate INSERT error = %v, want SQLITE_CONSTRAINT (UNIQUE(task_id, other_task_id, kind))", err)
	}
}

// TestRelations_RealSQL_AddRemoveRoundTripNoVersionBump walks add → verify in
// GetDetail → remove on real SQL: each change commits its relation-changed
// row in the same transaction as the edge, and neither task's version or
// updated_at ever moves (child-row rule). The parent alias stores no
// kind='parent' row at any point.
func TestRelations_RealSQL_AddRemoveRoundTripNoVersionBump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	relations, tasks, projects := newRelationUseCase(t, db)

	p := mustCreateProject(t, ctx, projects)
	a, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "A", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "B", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	aBefore, bBefore := a.Version, b.Version // Tasks.Create returns the stored row (version 1)

	// Add via the PARENT alias ("A is parent of B") so the inverse-child
	// normalization is exercised on real SQL too.
	stored, _, err := relations.Relate(ctx, testActor, a.ID, b.ID, domain.RelationParent, application.RelateAdd)
	if err != nil {
		t.Fatalf("relate (parent alias): %v", err)
	}
	if stored.TaskID != b.ID || stored.OtherTaskID != a.ID || stored.Kind != domain.RelationChild {
		t.Fatalf("stored edge = (task=%s other=%s kind=%s), want (B -> A, child)", stored.TaskID, stored.OtherTaskID, stored.Kind)
	}
	var parentRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relation WHERE kind = 'parent'`).Scan(&parentRows); err != nil {
		t.Fatal(err)
	}
	if parentRows != 0 {
		t.Fatalf("kind='parent' rows = %d, want 0 (the alias is normalized to child before any write)", parentRows)
	}

	// The edge is visible in the resume payload and NO version/updated_at bump happened.
	detail, err := tasks.GetDetail(ctx, b.ID, 0, 0) // B's side: its outgoing child edge names A as its parent
	if err != nil {
		t.Fatalf("GetDetail(B): %v", err)
	}
	found := false
	for _, e := range detail.Relations {
		if e.TaskID == b.ID && e.OtherTaskID == a.ID && e.Kind == domain.RelationChild {
			found = true
		}
	}
	if !found {
		t.Errorf("GetDetail(B).Relations missing (B -> A, child) in %+v", detail.Relations)
	}

	aAfter, err := tasks.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	bAfter, err := tasks.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if aAfter.Version != aBefore || bAfter.Version != bBefore ||
		!aAfter.UpdatedAt.Equal(a.UpdatedAt) || !bAfter.UpdatedAt.Equal(b.UpdatedAt) {
		t.Errorf("versions/updated_at after add: A (%d, %s), B (%d, %s); want unchanged (A %d / B %d)",
			aAfter.Version, aAfter.UpdatedAt, bAfter.Version, bAfter.UpdatedAt, aBefore, bBefore)
	}

	// The event committed alongside the edge in the same transaction.
	var events int
	var taskID string
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(task_id), '') FROM event WHERE kind = 'relation-changed'`).Scan(&events, &taskID); err != nil {
		t.Fatal(err)
	}
	if events != 1 || taskID != string(b.ID) { // event attaches to the stored edge's SOURCE (B for the inverse child)
		t.Fatalf("relation-changed events = (%d, task %s), want (1, %s)", events, taskID, b.ID)
	}

	// Remove via the same alias: deletes the inverse child row, appends a
	// second event, and still bumps nothing.
	if _, _, err := relations.Relate(ctx, testActor, a.ID, b.ID, domain.RelationParent, application.RelateRemove); err != nil {
		t.Fatalf("remove (parent alias): %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relation`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("relation rows after remove = %d, want 0", rows)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE kind = 'relation-changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("relation-changed events after remove = %d, want 2 (add + remove)", events)
	}

	aFin, err := tasks.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get(A): %v", err)
	}
	bFin, err := tasks.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get(B): %v", err)
	}
	if aFin.Version != aBefore || bFin.Version != bBefore {
		t.Errorf("versions after add+remove: A=%d B=%d, want both %d (relations never bump)", aFin.Version, bFin.Version, aBefore)
	}
}
