package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// tasks_occ_test.go: unit tests for the optimistic-concurrency guard —
// TaskUpdateInput.ExpectedVersion in Tasks.Update and the optional expected
// version in Tasks.UpdateNotes. The repository-level CAS is the backstop for
// writers whose reads interleave inside one transaction; THESE tests cover the
// application-layer check, which catches what the repository alone can never
// see: a stale BASE VERSION (the caller's read happened before another writer
// committed). The racing-goroutines proof lives in the SQLite/HTTP integration
// tests (concurrent PATCH → exactly one 2xx, the rest 409).

// asConflict asserts err is *domain.ConflictError and returns it.
func asConflict(t *testing.T, err error) *domain.ConflictError {
	t.Helper()
	var conflict *domain.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v (%T), want *domain.ConflictError", err, err)
	}
	return conflict
}

func TestTasksUpdate_ExpectedVersionMismatch_ConflictsInsideTxnRowUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "OCC target", 0, nil)

	// A rival writer moves the row forward (v1 → v2) — this is the state the
	// guarded caller never saw.
	fc.SetNow(fc.now.Add(time.Minute))
	rivalTitle := "moved by a rival"
	rival, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &rivalTitle})
	if err != nil {
		t.Fatalf("rival Update: %v", err)
	}

	// Our caller still believes the row is v1 and tries a guarded write.
	staleWrite := "my stale write"
	_, err = svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &staleWrite, ExpectedVersion: 1})
	conflict := asConflict(t, err)

	// The error carries the FRESH row — the recovery instruction.
	if conflict.Current.Version != rival.Version || conflict.Current.Version != 2 {
		t.Errorf("ConflictError.Current.Version = %d, want %d (the fresh row)", conflict.Current.Version, rival.Version)
	}
	if conflict.Current.Title != rivalTitle {
		t.Errorf("ConflictError.Current.Title = %q, want %q (what the row actually holds now)", conflict.Current.Title, rivalTitle)
	}

	// The stored row is untouched by the rejected write.
	stored, ok := uow.tasks.byID[task.ID]
	if !ok {
		t.Fatal("stored row disappeared")
	}
	if stored.Title != rivalTitle || stored.Version != 2 || stored.Notes != "" {
		t.Errorf("stored row after rejected update = (title %q v%d), want (%q v2) — the stale write must not land",
			stored.Title, stored.Version, rivalTitle)
	}

	// Losers emit NO events: created + the rival's update only.
	if got := len(uow.events.events); got != 2 {
		t.Errorf("events after rejected guarded update = %d, want 2 (created + rival update — losers append nothing)", got)
	}
}

func TestTasksUpdate_ExpectedVersionMismatch_ConflictsEvenWhenPayloadWouldBeNoOp(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "OCC no-op", 0, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	rivalTitle := "rival title"
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &rivalTitle}); err != nil {
		t.Fatalf("rival Update: %v", err)
	}

	// The payload EQUALS the current row (unguarded this would be a silent
	// no-op), but the caller's base is stale. The guard protects the caller's
	// READ STATE, not just field diffs — it must still conflict.
	_, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &rivalTitle, ExpectedVersion: 1})
	conflict := asConflict(t, err)
	if conflict.Current.Version != 2 {
		t.Errorf("ConflictError.Current.Version = %d, want 2", conflict.Current.Version)
	}
}

func TestTasksUpdate_ExpectedVersionMatch_SucceedsAndBumps(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "OCC match", 0, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	newTitle := "guarded write"
	got, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &newTitle, ExpectedVersion: 1})
	if err != nil {
		t.Fatalf("guarded Update with matching version: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2 (a guarded success bumps exactly once)", got.Version)
	}
	if got.Title != newTitle {
		t.Errorf("title = %q, want %q", got.Title, newTitle)
	}

	// And the guard composes with normal versioning: the next reader must now
	// expect version 2.
	again := "second guarded write"
	next, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &again, ExpectedVersion: 2})
	if err != nil {
		t.Fatalf("second guarded Update: %v", err)
	}
	if next.Version != 3 {
		t.Errorf("version = %d, want 3", next.Version)
	}
}

func TestTasksUpdate_ExpectedVersionZero_UnguardedLegacyBehavior(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "OCC legacy", 0, nil)

	// Someone else moves the row forward without anyone knowing.
	fc.SetNow(fc.now.Add(time.Minute))
	rivalTitle := "rival moved it"
	if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Title: &rivalTitle}); err != nil {
		t.Fatalf("rival Update: %v", err)
	}

	// A caller that NEVER re-read (zero ExpectedVersion = the historical
	// behavior of every internal call site) must still succeed — nothing
	// regresses just because the guard exists. The update rebases onto v2.
	p := domain.Priority(3)
	got, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Priority: &p}) // no ExpectedVersion
	if err != nil {
		t.Fatalf("unguarded Update after a concurrent bump: %v (legacy behavior must be preserved)", err)
	}
	if got.Version != 3 || got.Title != rivalTitle {
		t.Errorf("row = (title %q v%d), want (%q v3) — unguarded update rebases onto the latest row",
			got.Title, got.Version, rivalTitle)
	}
}

func TestTasksUpdate_ExpectedVersionMismatch_UnknownTaskStillNot409(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)
	// The not-found check must win over the version check (a ghost ID with a
	// guarded payload is a 404-able miss, never a conflict).
	todo := domain.StatusTodo
	_, err := svc.Update(ctx, testAgent, domain.ID("ghost"), TaskUpdateInput{Status: &todo, ExpectedVersion: 1})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}
}

// --- version-guarded notes (Tasks.UpdateNotes expected-version parameter) ----

func TestTasksUpdateNotes_ExpectedVersionMismatch_ConflictsRowUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "notes OCC", 0, nil)

	// A rival write moves the notes blob forward (v1 → v2).
	fc.SetNow(fc.now.Add(time.Minute))
	rivalNotes := "rival resume context"
	rival, err := svc.UpdateNotes(ctx, testAgent, task.ID, rivalNotes, 0)
	if err != nil {
		t.Fatalf("rival UpdateNotes: %v", err)
	}

	// Our caller still believes the row is v1 and attempts a GUARDED replace.
	staleNotes := "my stale blob"
	_, err = svc.UpdateNotes(ctx, testAgent, task.ID, staleNotes, 1)
	conflict := asConflict(t, err)
	if conflict.Current.Version != rival.Version || conflict.Current.Version != 2 {
		t.Errorf("ConflictError.Current.Version = %d, want %d (the fresh row)", conflict.Current.Version, rival.Version)
	}
	if conflict.Current.Notes != rivalNotes {
		t.Errorf("ConflictError.Current.Notes = %q, want %q", conflict.Current.Notes, rivalNotes)
	}

	stored, ok := uow.tasks.byID[task.ID]
	if !ok || stored.Notes != rivalNotes || stored.Version != 2 {
		t.Fatalf("stored row after rejected guarded notes write = (notes %q v%d), want (%q v2)",
			stored.Notes, stored.Version, rivalNotes)
	}
	noted := eventsOfKind(uow, domain.EventNoted)
	if len(noted) != 1 {
		t.Errorf("noted events = %d, want 1 (the rival's — the loser appends nothing)", len(noted))
	}
}

func TestTasksUpdateNotes_ExpectedVersionMismatch_ConflictsEvenWhenBlobUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "notes OCC no-op", 0, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	rivalNotes := "rival blob"
	if _, err := svc.UpdateNotes(ctx, testAgent, task.ID, rivalNotes, 0); err != nil {
		t.Fatalf("rival UpdateNotes: %v", err)
	}

	// Blob equals the CURRENT one (unguarded this is a silent no-op), but the
	// caller's base version is stale → the guard still conflicts.
	_, err := svc.UpdateNotes(ctx, testAgent, task.ID, rivalNotes, 1)
	conflict := asConflict(t, err)
	if conflict.Current.Version != 2 {
		t.Errorf("ConflictError.Current.Version = %d, want 2", conflict.Current.Version)
	}
}

func TestTasksUpdateNotes_ExpectedVersionMatch_SucceedsAndBumps(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "notes OCC match", 0, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	got, err := svc.UpdateNotes(ctx, testAgent, task.ID, "guarded blob", 1)
	if err != nil {
		t.Fatalf("guarded UpdateNotes with matching version: %v", err)
	}
	if got.Version != 2 || got.Notes != "guarded blob" {
		t.Errorf("row = (notes %q v%d), want (%q v2)", got.Notes, got.Version, "guarded blob")
	}
}

// Zero-version notes writes keep the historical replace-on-write contract:
// identical content is a no-op without bump or event. (Pinned here with the
// explicit 0 argument — the existing collab tests exercise the same path.)
func TestTasksUpdateNotes_ZeroVersion_UnguardedIdenticalNoOp(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "notes legacy no-op", 0, nil)

	got, err := svc.UpdateNotes(ctx, testAgent, task.ID, "", 0)
	if err != nil {
		t.Fatalf("unguarded UpdateNotes: %v", err)
	}
	if got.Version != task.Version || len(eventsOfKind(uow, domain.EventNoted)) != 0 {
		t.Errorf("identical unguarded replace must be a no-op: version %d (want %d), noted events present",
			got.Version, task.Version)
	}
}
