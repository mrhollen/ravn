package sqlite

// Integration tests over a real temp-file SQLite database (ARCHITECTURE §9):
// the Projects use case round-trips through SQLiteUnitOfWork, events land in
// the same transaction as the mutation they records, and a forced failure
// inside Run rolls back every write.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
)

func newProjectUseCase(t *testing.T, db *sql.DB) *application.Projects {
	t.Helper()
	return application.NewProjects(NewUnitOfWork(db), clock.New(), idgen.New())
}

var testActor = domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

func TestProjectsUseCase_RoundTripOverRealSQLite(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
// transaction-bound repositories: every port is wired and a live call inside
// fn (TaskRepository.Get on an unknown id — real since slice 2B) surfaces its
// genuine domain error instead of a stub.
func TestUnitOfWork_ReposAreBoundToTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		if r.Projects == nil || r.Tasks == nil || r.ACs == nil || r.Comments == nil ||
			r.Artifacts == nil || r.Relations == nil || r.Events == nil {
			return errors.New("repositories not wired into Repos")
		}
		gerr := probeBoundRepo(r, ctx)
		if gerr != nil {
			return fmt.Errorf("live call inside Run failed: %w", gerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run with live repository calls: %v", err)
	}
}

// probeBoundRepo makes one read-only call through a wired port from inside the
// transaction and asserts its genuine result: an unknown task id must surface
// as domain.ErrNotFound (proving the repo is bound to a live DB, not a stub).
func probeBoundRepo(r application.Repos, ctx context.Context) error {
	if _, gerr := r.Tasks.Get(ctx, "any-id"); !errors.Is(gerr, domain.ErrNotFound) {
		return fmt.Errorf("TaskRepository.Get(unknown) = %v, want domain.ErrNotFound", gerr)
	}
	return nil
}

func TestProjectsRepo_OpenCodeSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	p := domain.Project{
		ID:                "proj-opencode-1",
		Name:              "OpenCode Integration Test",
		Description:       "testing opencode settings",
		OpenCodeServerURL: "http://localhost:5555",
		OpenCodeDirectory: "/home/test/workspace",
		OpenCodeModel:     "llama.cpp/Qwen Nano",
		OpenCodeAgent:     "build",
		OpenCodeVariant:   "thinking",
		CreatedAt:         time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
	}

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		return r.Projects.Create(ctx, p)
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got domain.Project
	err = uow.Run(ctx, &r, func() error {
		var gerr error
		got, gerr = r.Projects.Get(ctx, p.ID)
		return gerr
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OpenCodeServerURL != p.OpenCodeServerURL || got.OpenCodeDirectory != p.OpenCodeDirectory || got.OpenCodeModel != p.OpenCodeModel || got.OpenCodeAgent != p.OpenCodeAgent || got.OpenCodeVariant != p.OpenCodeVariant {
		t.Fatalf("got OpenCode settings (%q, %q, %q, %q, %q), want (%q, %q, %q, %q, %q)", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel, got.OpenCodeAgent, got.OpenCodeVariant, p.OpenCodeServerURL, p.OpenCodeDirectory, p.OpenCodeModel, p.OpenCodeAgent, p.OpenCodeVariant)
	}

	// Test Update
	p.OpenCodeServerURL = "http://localhost:6666"
	p.OpenCodeDirectory = "/home/test/newdir"
	p.OpenCodeModel = "llama.cpp/Qwen Small"
	p.OpenCodeAgent = "plan"
	p.OpenCodeVariant = "high"
	err = uow.Run(ctx, &r, func() error {
		return r.Projects.Update(ctx, p)
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	err = uow.Run(ctx, &r, func() error {
		var gerr error
		got, gerr = r.Projects.Get(ctx, p.ID)
		return gerr
	})
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.OpenCodeServerURL != "http://localhost:6666" || got.OpenCodeDirectory != "/home/test/newdir" || got.OpenCodeModel != "llama.cpp/Qwen Small" || got.OpenCodeAgent != "plan" || got.OpenCodeVariant != "high" {
		t.Fatalf("got updated settings (%q, %q, %q, %q, %q), want (http://localhost:6666, /home/test/newdir, llama.cpp/Qwen Small, plan, high)", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel, got.OpenCodeAgent, got.OpenCodeVariant)
	}
}

func TestProjectsRepo_ArchivedRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	p1 := domain.Project{
		ID:        "proj-arch-1",
		Name:      "Active Project",
		CreatedAt: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
	}
	p2 := domain.Project{
		ID:        "proj-arch-2",
		Name:      "Archived Project",
		Archived:  true,
		CreatedAt: time.Date(2026, 9, 27, 10, 1, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 27, 10, 1, 0, 0, time.UTC),
	}

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, p1); err != nil {
			return err
		}
		return r.Projects.Create(ctx, p2)
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var openList, archList []domain.Project
	err = uow.Run(ctx, &r, func() error {
		var err error
		openList, err = r.Projects.List(ctx)
		if err != nil {
			return err
		}
		archList, err = r.Projects.List(ctx, application.ProjectFilter{Archived: true})
		return err
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(openList) != 1 || openList[0].ID != p1.ID || openList[0].Archived {
		t.Fatalf("openList = %+v, want only p1 (unarchived)", openList)
	}
	if len(archList) != 1 || archList[0].ID != p2.ID || !archList[0].Archived {
		t.Fatalf("archList = %+v, want only p2 (archived)", archList)
	}

	// Update p1 to archived
	p1.Archived = true
	err = uow.Run(ctx, &r, func() error {
		return r.Projects.Update(ctx, p1)
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	var p1Got domain.Project
	err = uow.Run(ctx, &r, func() error {
		var err error
		p1Got, err = r.Projects.Get(ctx, p1.ID)
		return err
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !p1Got.Archived {
		t.Fatal("expected p1 to be archived")
	}
}
