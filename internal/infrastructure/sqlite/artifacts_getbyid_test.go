package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// TestArtifactRepo_GetByID_RoundTrip seeds project/task/artifact through the
// UnitOfWork and proves GetByID returns the seeded domain row for a hit and a
// wrapped domain.ErrNotFound for a miss — the read path behind T4's
// GET /artifacts/{id} ownership resolution. A read appends no event and never
// touches the parent task row's version.
func TestArtifactRepo_GetByID_RoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	const (
		taskID   = domain.ID("g-task-1")
		artifact = domain.ID("g-art-1")
	)
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("g-proj", "Get Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "g-proj", "Get Task", domain.StatusTodo, false)))
		mustCreate(t, r.Artifacts.Add(ctx, domain.Artifact{
			ID: artifact, TaskID: taskID, Kind: domain.ArtifactURL,
			Ref: "https://evidence.local/x", Summary: "round-trip", Actor: edgeActor,
		}))
	})

	var got domain.Artifact
	r := application.Repos{}
	if err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		var err error
		got, err = r.Artifacts.GetByID(ctx, artifact)
		return err
	}); err != nil {
		t.Fatalf("GetByID hit: %v", err)
	}
	if got.ID != artifact || got.TaskID != taskID || got.Kind != domain.ArtifactURL ||
		got.Ref != "https://evidence.local/x" || got.Summary != "round-trip" || got.Actor != edgeActor {
		t.Fatalf("GetByID = %+v, want the seeded row (id=%s task=%s)", got, artifact, taskID)
	}

	var missingErr error
	if err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, missingErr = r.Artifacts.GetByID(ctx, domain.ID("g-missing"))
		return nil
	}); err != nil {
		t.Fatalf("GetByID miss transaction: %v", err)
	}
	if !errors.Is(missingErr, domain.ErrNotFound) {
		t.Fatalf("GetByID miss err = %v, want wrapped domain.ErrNotFound", missingErr)
	}

	// The read must not bump the parent task's version (rollupTask seeds it at 1).
	var v int
	if err := db.QueryRowContext(ctx, "SELECT version FROM task WHERE id = ?", string(taskID)).Scan(&v); err != nil {
		t.Fatalf("fetch task version: %v", err)
	}
	if v != 1 {
		t.Fatalf("task version after GetByID = %d, want 1 (reads never bump)", v)
	}
}
