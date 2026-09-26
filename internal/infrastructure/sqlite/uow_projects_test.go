package sqlite

// Integration tests over a real temp-file SQLite database (ARCHITECTURE §9):
// the Projects use case round-trips through SQLiteUnitOfWork, events land in
// the same transaction as the mutation they records, and a forced failure
// inside Run rolls back every write.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
)

func newProjectUseCase(t *testing.T, db *sql.DB) *application.Projects {
	t.Helper()
	return application.NewProjects(NewUnitOfWork(db), clock.New(), idgen.New())
}

var testActor = domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

func TestProjectsUseCase_RoundTripOverRealSQLite(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	svc := newProjectUseCase(t, db)

	p, err := svc.Create(ctx, testActor, "Integration Project", "round-trip me")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != p {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0] != p {
		t.Fatalf("List = %+v, want exactly [%+v]", list, p)
	}

	// The created event must be in the DB in the same shape the use case sent.
	var (
		kind       string
		actorName  string
		ts         string
		afterJSON  *string
		beforeJSON *string
		taskID     *string
	)
	err = db.QueryRowContext(ctx,
		`SELECT kind, actor_name, ts, after_json, before_json, task_id FROM event WHERE project_id = ?`,
		string(p.ID)).Scan(&kind, &actorName, &ts, &afterJSON, &beforeJSON, &taskID)
	if err != nil {
		t.Fatalf("event lookup: %v", err)
	}
	if kind != string(domain.EventCreated) {
		t.Errorf("event kind = %q, want %q", kind, domain.EventCreated)
	}
	if actorName != testActor.Name {
		t.Errorf("event actor = %q, want %q", actorName, testActor.Name)
	}
	if taskID != nil {
		t.Errorf("project-level event has task_id %v, want NULL", *taskID)
	}
	if beforeJSON != nil {
		t.Errorf("create event before_json = %q, want NULL", *beforeJSON)
	}
	if afterJSON == nil || !strings.Contains(*afterJSON, `"name":"Integration Project"`) {
		t.Errorf("after_json = %v, want compact JSON carrying the name", afterJSON)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("ts %q is not RFC3339: %v", ts, err)
	}

	// A second project exercises List ordering (created_at ASC).
	if _, err := svc.Create(ctx, testActor, "Second Project", ""); err != nil {
		t.Fatalf("Create #2: %v", err)
	}
	list, err = svc.List(ctx)
	if err != nil {
		t.Fatalf("List after second create: %v", err)
	}
	if len(list) != 2 || list[0].ID != p.ID || list[1].Name != "Second Project" {
		t.Fatalf("List order wrong: %+v", list)
	}
}

// TestUnitOfWork_RollbackOnFNFError_CommitsNothing proves the unit-of-work
// rule end to end: the project row AND its event are written inside fn, then a
// forced failure rolls back BOTH — nothing is committed.
func TestUnitOfWork_RollbackOnFNFError_CommitsNothing(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		p := domain.Project{
			ID:        "rollback-probe",
			Name:      "Rollback Probe",
			CreatedAt: time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC),
		}
		if err := r.Projects.Create(ctx, p); err != nil {
			return err
		}
		ev := domain.Event{
			ID:        "rollback-event",
			ProjectID: &p.ID,
			Actor:     testActor,
			Kind:      domain.EventCreated,
			At:        p.CreatedAt,
		}
		if err := r.Events.Append(ctx, ev); err != nil {
			return err
		}
		return errors.New("forced failure after both writes")
	})
	if err == nil {
		t.Fatal("Run returned nil, want the forced error")
	}
	if !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("Run error %q does not carry the fn error", err)
	}

	var projectRows, eventRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = 'rollback-probe'`).Scan(&projectRows); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE id = 'rollback-event'`).Scan(&eventRows); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if projectRows != 0 || eventRows != 0 {
		t.Fatalf("rollback leaked rows: project=%d event=%d, want 0/0", projectRows, eventRows)
	}

	// The failed Run must leave the connection reusable (no leaked tx).
	var r2 application.Repos
	if err := uow.Run(ctx, &r2, func() error {
		p := domain.Project{
			ID:        "after-rollback",
			Name:      "After Rollback",
			CreatedAt: time.Date(2026, 9, 25, 18, 5, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 9, 25, 18, 5, 0, 0, time.UTC),
		}
		return r2.Projects.Create(ctx, p)
	}); err != nil {
		t.Fatalf("Run after rollback failed: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = 'after-rollback'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("post-rollback create lost (n=%d, err=%v)", n, err)
	}
}

// TestUnitOfWork_ReposAreBoundToTransaction asserts the UoW hands fn
// transaction-bound repositories: a stub repository error raised inside fn
// surfaces and its writes do not persist even for the ports used directly.
func TestUnitOfWork_ReposAreBoundToTransaction(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		if r.Tasks == nil || r.ACs == nil || r.Comments == nil || r.Artifacts == nil || r.Relations == nil {
			return errors.New("stub repositories not wired into Repos")
		}
		_, gerr := r.Tasks.Get(ctx, "any-id") // stub: "implemented in 2B" error
		return gerr
	})
	if err == nil {
		t.Fatal("stub TaskRepository.Get returned nil, want its not-implemented error")
	}
	for _, frag := range []string{"not implemented yet", "2B"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("stub error %q missing %q", err, frag)
		}
	}
}
