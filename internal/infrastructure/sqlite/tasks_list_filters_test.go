package sqlite

// tasks_list_filters_test.go: integration tests over a real temp-file SQLite
// database for the epic-decomposition pair — create_task's optional ParentID
// (child edge + relation event written in the SAME transaction) and list_tasks'
// parent_id / claimed / claimed_by / expired_claim filters. These prove the SQL
// encodes exactly what the application-layer fakes assume (the fake twins live
// in internal/application/tasks_parent_list_filters_test.go).

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

func TestSQLCreateWithParent_WritesChildEdgeAndEventAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	parent := mustCreateTaskIn(t, tasks, p, "Epic parent")
	child, err := tasks.Create(ctx, testActor, application.TaskInput{
		Title: "Child task", ProjectID: p.ID, ParentID: parent.ID,
	})
	if err != nil {
		t.Fatalf("Create with ParentID: %v", err)
	}

	// The stored row is the CANONICAL child edge: task_id=child, other_task_id=parent.
	var kind, otherTaskID string
	err = db.QueryRowContext(ctx,
		`SELECT kind, other_task_id FROM relation WHERE task_id = ?`, string(child.ID)).Scan(&kind, &otherTaskID)
	if err != nil {
		t.Fatalf("relation row for new child: %v", err)
	}
	if kind != string(domain.RelationChild) || otherTaskID != string(parent.ID) {
		t.Errorf("stored edge = (%s -> %s), want (child=%s, parent=%s)", kind, otherTaskID, child.ID, parent.ID)
	}

	// Both the created and the relation-changed events landed in the same tx.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE task_id = ? AND kind IN (?, ?)`,
		string(child.ID), string(domain.EventCreated), string(domain.EventRelationChanged)).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 2 {
		t.Errorf("events for child = %d, want 2 (created + relation-changed)", n)
	}

	// An invalid parent writes NOTHING (task row included).
	before := taskCount(t, db)
	_, err = tasks.Create(ctx, testActor, application.TaskInput{
		Title: "orphan", ProjectID: p.ID, ParentID: domain.ID("01JMISSINGPARENT00000000000000"),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing parent: err = %v, want wrapped domain.ErrInvalid", err)
	}
	if after := taskCount(t, db); before != after {
		t.Errorf("task rows changed on invalid parent (%d -> %d) — nothing may be written", before, after)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %q, want it to name the missing parent", err)
	}
}

func TestSQLParentIDFilter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	rels := application.NewRelations(NewUnitOfWork(db), clock.New(), idgen.New())
	p := mustCreateProject(t, ctx, projects)

	parent := mustCreateTaskIn(t, tasks, p, "epic")
	viaCreate, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "child via create", ProjectID: p.ID, ParentID: parent.ID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	viaRelate := mustCreateTaskIn(t, tasks, p, "child via relate")
	if _, _, err := rels.Relate(ctx, testActor, viaRelate.ID, parent.ID, domain.RelationChild, application.RelateAdd); err != nil {
		t.Fatalf("relate child: %v", err)
	}
	grandchild, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "grandchild", ProjectID: p.ID, ParentID: viaCreate.ID})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	mustCreateTaskIn(t, tasks, p, "unrelated")

	rows, total, err := tasks.List(ctx, application.TaskFilter{ParentID: parent.ID})
	if err != nil {
		t.Fatalf("List parent_id: %v", err)
	}
	assertIDs(t, rows, viaCreate.ID, viaRelate.ID)
	if total != 2 {
		t.Errorf("total = %d, want 2 (COUNT must apply the same EXISTS predicate)", total)
	}
	for _, r := range rows {
		if r.ID == grandchild.ID {
			t.Errorf("grandchild matched its grandparent's filter — only DIRECT children may match")
		}
	}

	// A leaf parent yields an empty page.
	rows, total, err = tasks.List(ctx, application.TaskFilter{ParentID: grandchild.ID})
	if err != nil {
		t.Fatalf("List parent_id=leaf: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("leaf parent => total %d rows %d, want empty", total, len(rows))
	}

	// Composes with project scoping (no cross-project leakage by construction —
	// other_task_id equality already pins the endpoint, but the combination is
	// the realistic agent query).
	rows, total, err = tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, ParentID: parent.ID})
	if err != nil {
		t.Fatalf("List project+parent: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Errorf("project+parent => total %d rows %d, want both children", total, len(rows))
	}
}

func TestSQLClaimedAndClaimedByFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	free := mustCreateTaskIn(t, tasks, p, "unclaimed")
	agentHeld := mustCreateTaskIn(t, tasks, p, "held by agent-a")
	humanHeld := mustCreateTaskIn(t, tasks, p, "held by ada")
	if _, err := tasks.Claim(ctx, testActor, agentHeld.ID, 900, false); err != nil {
		t.Fatalf("claim (agent): %v", err)
	}
	if _, err := tasks.Claim(ctx, domain.Actor{Type: domain.ActorHuman, Name: "ada"}, humanHeld.ID, 900, false); err != nil {
		t.Fatalf("claim (human): %v", err)
	}

	bTrue, bFalse := true, false
	cases := []struct {
		name string
		flt  application.TaskFilter
		want []domain.ID
	}{
		{"claimed omitted = any", application.TaskFilter{}, []domain.ID{free.ID, agentHeld.ID, humanHeld.ID}},
		{"claimed=true = lease present", application.TaskFilter{Claimed: &bTrue}, []domain.ID{agentHeld.ID, humanHeld.ID}},
		{"claimed=false = unclaimed", application.TaskFilter{Claimed: &bFalse}, []domain.ID{free.ID}},
		{"claimed_by=agent-a", application.TaskFilter{ClaimedBy: "agent-a"}, []domain.ID{agentHeld.ID}},
		{"claimed_by=ada", application.TaskFilter{ClaimedBy: "ada"}, []domain.ID{humanHeld.ID}},
		{"claimed_by=unknown", application.TaskFilter{ClaimedBy: "nobody"}, nil},
		{"claimed=true + claimed_by narrows", application.TaskFilter{Claimed: &bTrue, ClaimedBy: "ada"}, []domain.ID{humanHeld.ID}},
	}
	for _, tc := range cases {
		rows, total, err := tasks.List(ctx, tc.flt)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		assertIDs(t, rows, tc.want...)
		if total != int64(len(tc.want)) {
			t.Errorf("%s: total = %d, want %d (COUNT uses the same predicate)", tc.name, total, len(tc.want))
		}
	}
}

func TestSQLExpiredClaimFilter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	stale := mustCreateTaskIn(t, tasks, p, "stale lease")
	active := mustCreateTaskIn(t, tasks, p, "live lease")
	free := mustCreateTaskIn(t, tasks, p, "open")
	if _, err := tasks.Claim(ctx, testActor, stale.ID, 900, false); err != nil {
		t.Fatalf("claim stale: %v", err)
	}
	if _, err := tasks.Claim(ctx, domain.Actor{Type: domain.ActorHuman, Name: "ada"}, active.ID, 900, false); err != nil {
		t.Fatalf("claim active: %v", err)
	}

	// Backdate ONE lease's expiry into the past (fixture surgery on the stored
	// column — deterministic without sleeps; the filter must read the column as
	// written, relative to the server now threaded by the use case).
	if _, err := db.ExecContext(ctx, `UPDATE task SET claim_expires_at = ? WHERE id = ?`,
		formatTS(time.Now().Add(-time.Hour)), string(stale.ID)); err != nil {
		t.Fatalf("backdate expiry: %v", err)
	}

	bTrue, bFalse := true, false
	cases := []struct {
		name string
		flt  application.TaskFilter
		want []domain.ID
	}{
		{"expired omitted = any", application.TaskFilter{}, []domain.ID{stale.ID, active.ID, free.ID}},
		{"expired=true = stale lease only", application.TaskFilter{ExpiredClaim: &bTrue}, []domain.ID{stale.ID}},
		{"expired=false = unclaimed or still active", application.TaskFilter{ExpiredClaim: &bFalse}, []domain.ID{active.ID, free.ID}},
		{"expired=true + claimed_by narrows", application.TaskFilter{ExpiredClaim: &bTrue, ClaimedBy: "agent-a"}, []domain.ID{stale.ID}},
		{"expired=false + claimed_by excludes the stale row", application.TaskFilter{ExpiredClaim: &bFalse, ClaimedBy: "agent-a"}, nil},
	}
	for _, tc := range cases {
		rows, total, err := tasks.List(ctx, tc.flt)
		if err != nil {
			t.Fatalf("%s: List: %v", tc.name, err)
		}
		assertIDs(t, rows, tc.want...)
		if total != int64(len(tc.want)) {
			t.Errorf("%s: total = %d, want %d (COUNT uses the same predicate)", tc.name, total, len(tc.want))
		}
	}
}

func taskCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM task`).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return n
}
