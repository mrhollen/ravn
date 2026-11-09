package application

// artifacts_get_test.go: unit tests for Artifacts.Get — the T4 read use case
// behind GET /artifacts/{id}'s membership gate. A read round-trips the stored
// row, surfaces domain.ErrNotFound for unknown ids, appends no event, and never
// touches the parent task row's version or updated_at.

import (
	"context"
	"errors"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

func mustGetTask(t *testing.T, svc *Tasks, id domain.ID) domain.Task {
	t.Helper()
	task, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get task %s: %v", id, err)
	}
	return task
}

func TestArtifactsGet_RoundTripStoredRow(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Evidence read", 0, nil)
	arts := NewArtifacts(uow, fc, &fakeIDGen{})

	want, err := arts.Attach(ctx, testAgent, task.ID, domain.ArtifactURL, "https://evidence.local/x", "proof")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, err := arts.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want the attached row %+v", got, want)
	}

	// A read appends no event and bumps nothing on the parent task.
	baseVersion, baseUpdated := mustGetTask(t, svc, task.ID).Version, mustGetTask(t, svc, task.ID).UpdatedAt
	eventsBefore := len(uow.events.events)
	if _, err := arts.Get(ctx, want.ID); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	fresh := mustGetTask(t, svc, task.ID)
	if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
		t.Fatalf("task after read = v%d/%s, want unchanged v%d/%s", fresh.Version, fresh.UpdatedAt, baseVersion, baseUpdated)
	}
	if len(uow.events.events) != eventsBefore {
		t.Fatalf("Get appended %d event(s), want none", len(uow.events.events)-eventsBefore)
	}
}

func TestArtifactsGet_UnknownIDIsNotFound(t *testing.T) {
	_, fc, uow := newTaskTestEnv(t)
	arts := NewArtifacts(uow, fc, &fakeIDGen{})

	_, err := arts.Get(context.Background(), domain.ID("no-such-artifact"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing id → err = %v, want domain.ErrNotFound", err)
	}
}
