package sqlite

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
)

// seqID returns a fixed 26-character Crockford-base32 id (ULID-shaped, all
// digits) that sorts lexicographically in n order — deterministic ordering
// without relying on ULID minting being monotonic within a millisecond.
func seqID(n int) domain.ID {
	return domain.ID(strings.Repeat("0", 20) + fmt.Sprintf("%06d", n))
}

// TestEventStore_ListAfterProject proves the SSE replay read: only events of
// the given project strictly after the cursor, in ascending (chronological)
// id order — other projects and NULL-project rows never match. It also covers
// the exported pool-bound constructor used by cmd/ravn-server for replay.
func TestEventStore_ListAfterProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	mint := idgen.New() // real ULIDs for the project ids (used as equality filters only)
	projA := mint.NewID()
	projB := mint.NewID()

	at := time.Date(2026, 10, 12, 5, 0, 0, 0, time.UTC)
	actor := domain.Actor{Type: domain.ActorAgent, Name: "replay-test"}

	// Seed in exact id order (seqID keeps it lexicographically monotonic).
	// Project A ascending: a0,a1,a2,a3,a4,a5 · Project B: b1,b2 · one orphan.
	seed := func(n int, proj *domain.ID) domain.Event {
		id := seqID(n)
		return domain.Event{ID: id, ProjectID: proj, Actor: actor, Kind: domain.EventCreated, At: at}
	}
	a0, b1, orphan, a1, a2, b2, a3, a4, a5 := seed(1, &projA), seed(2, &projB), seed(3, nil),
		seed(4, &projA), seed(5, &projA), seed(6, &projB), seed(7, &projA), seed(8, &projA), seed(9, &projA)

	uow := NewUnitOfWork(db)
	var r application.Repos
	if err := uow.Run(ctx, &r, func() error {
		// event.project_id carries a real FK — the two projects must exist.
		for _, p := range []domain.Project{
			{ID: projA, Name: "Replay Project A", CreatedAt: at, UpdatedAt: at},
			{ID: projB, Name: "Replay Project B", CreatedAt: at, UpdatedAt: at},
		} {
			if err := r.Projects.Create(ctx, p); err != nil {
				return err
			}
		}
		for _, ev := range []domain.Event{a0, b1, orphan, a1, a2, b2, a3, a4, a5} {
			if err := r.Events.Append(ctx, ev); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	store := NewEventStore(db) // pool-bound path, exactly as the server wires it
	var _ application.EventStore = store

	idsOf := func(events []domain.Event) []string {
		out := make([]string, len(events))
		for i, e := range events {
			out[i] = string(e.ID)
		}
		return out
	}

	t.Run("strictly after cursor, ascending", func(t *testing.T) {
		got, err := store.ListAfterProject(ctx, projA, a0.ID, 0)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		want := []string{a1.ID.String(), a2.ID.String(), a3.ID.String(), a4.ID.String(), a5.ID.String()}
		if !slices.Equal(idsOf(got), want) {
			t.Fatalf("got %v, want %v (cursor must be exclusive, other projects and orphans excluded)", idsOf(got), want)
		}
	})

	t.Run("returns all project events for a cursor before everything", func(t *testing.T) {
		zeros := strings.Repeat("0", 26) // valid Crockford; sorts before any real id
		got, err := store.ListAfterProject(ctx, projA, domain.ID(zeros), 0)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		want := []string{a0.ID.String(), a1.ID.String(), a2.ID.String(), a3.ID.String(), a4.ID.String(), a5.ID.String()}
		if !slices.Equal(idsOf(got), want) {
			t.Fatalf("got %v, want %v", idsOf(got), want)
		}
	})

	t.Run("other project cursor scope", func(t *testing.T) {
		got, err := store.ListAfterProject(ctx, projB, b1.ID, 0)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		want := []string{b2.ID.String()}
		if !slices.Equal(idsOf(got), want) {
			t.Fatalf("got %v, want %v", idsOf(got), want)
		}
	})

	t.Run("cursor at newest event returns empty, not error", func(t *testing.T) {
		got, err := store.ListAfterProject(ctx, projA, a5.ID, 0)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d events, want 0: %v", len(got), idsOf(got))
		}
	})

	t.Run("limit caps the replay burst", func(t *testing.T) {
		zeros := strings.Repeat("0", 26)
		got, err := store.ListAfterProject(ctx, projA, domain.ID(zeros), 3)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		want := []string{a0.ID.String(), a1.ID.String(), a2.ID.String()}
		if !slices.Equal(idsOf(got), want) {
			t.Fatalf("got %v, want %v (limit applies to the oldest-first order)", idsOf(got), want)
		}
	})

	t.Run("round-trips domain fields", func(t *testing.T) {
		got, err := store.ListAfterProject(ctx, projA, a0.ID, 1)
		if err != nil {
			t.Fatalf("ListAfterProject: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1: %+v", len(got), got)
		}
		e := got[0] // field-wise compare; ProjectID/TaskID are allocated pointers
		want := a1
		if e.ID != want.ID || e.Kind != want.Kind || e.Actor != want.Actor ||
			e.Before != want.Before || e.After != want.After || !e.At.Equal(want.At) {
			t.Fatalf("got %+v, want %+v", e, want)
		}
		if e.ProjectID == nil || *e.ProjectID != projA {
			t.Fatalf("ProjectID = %v, want %v", e.ProjectID, projA)
		}
	})
}
