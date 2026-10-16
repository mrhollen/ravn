package application

// actor_validation_test.go: every write-path use case must reject an invalid
// acting actor with domain.ErrInvalid BEFORE anything is stored or an event is
// recorded. The shapes come from domain.Actor.Validate: bad type, empty name,
// whitespace-only name, name over the rune cap, and control characters. Tasks
// and Projects are required coverage (AC4); Comments / Artifacts / Relations /
// Acceptance share the same normalizeActor gate and are spot-checked too so no
// use case can drift off it silently.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// invalidActorShapes is the canonical set of malformed actors every write path
// must reject. The cap shape uses domain.MaxActorNameLen so the test breaks if
// the constant itself changes to something that would accept a fixture name.
func invalidActorShapes() []struct {
	name string
	a    domain.Actor
} {
	return []struct {
		name string
		a    domain.Actor
	}{
		{"bad_type", domain.Actor{Type: "invalid", Name: "ada"}},
		{"empty_name", domain.Actor{Type: domain.ActorHuman, Name: ""}},
		{"whitespace_only_name", domain.Actor{Type: domain.ActorAgent, Name: " \t\n  "}},
		{"name_over_rune_cap", domain.Actor{Type: domain.ActorAgent, Name: strings.Repeat("w", domain.MaxActorNameLen+1)}},
		{"control_char_in_name", domain.Actor{Type: domain.ActorHuman, Name: "bad\nname"}},
	}
}

// TestTasks_WritePathsRejectInvalidActors runs each malformed actor through
// every Tasks write path (Create, Update, Claim, Release, Heartbeat,
// UpdateNotes) plus the collab use cases that share the same gate. After a
// rejection nothing may be stored and no event appended — validation happens
// before the unit of work opens any writes.
func TestTasks_WritePathsRejectInvalidActors(t *testing.T) {
	ctx := context.Background()

	for _, sh := range invalidActorShapes() {
		t.Run("actor_"+sh.name, func(t *testing.T) {
			svc, fc, uow := newTaskTestEnv(t)
			bad := sh.a

			// Create: rejected before anything is stored or recorded.
			if _, err := svc.Create(ctx, bad, TaskInput{Title: "t", ProjectID: testProjectID}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Create err = %v, want wrapped domain.ErrInvalid", err)
			}
			if len(uow.events.events) != 0 || len(uow.tasks.byID) != 0 {
				t.Fatalf("rejected Create still wrote state (%d events, %d tasks), want none",
					len(uow.events.events), len(uow.tasks.byID))
			}

			// Fixture: a valid task, created by a valid actor.
			task := mustCreateTask(t, svc, "victim", 1, nil)
			eventsAfterFixture := len(uow.events.events) // exactly the fixture's `created`
			fc.SetNow(fc.now.Add(time.Minute))

			// Update: rejected; row untouched.
			desc := "changed"
			if _, err := svc.Update(ctx, bad, task.ID, TaskUpdateInput{Description: &desc}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Update err = %v, want wrapped domain.ErrInvalid", err)
			}

			// Claim / Heartbeat / Release: rejected even on an UNCLAIMED task —
			// validation must precede the idempotent no-op and CAS paths.
			if _, err := svc.Claim(ctx, bad, task.ID, 600, false); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Claim err = %v, want wrapped domain.ErrInvalid", err)
			}
			if _, err := svc.Heartbeat(ctx, bad, task.ID); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Heartbeat err = %v, want wrapped domain.ErrInvalid", err)
			}
			if _, err := svc.Release(ctx, bad, task.ID, "nope"); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Release err = %v, want wrapped domain.ErrInvalid", err)
			}

			// UpdateNotes: rejected.
			if _, err := svc.UpdateNotes(ctx, bad, task.ID, "notes", 0); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("UpdateNotes err = %v, want wrapped domain.ErrInvalid", err)
			}

			// Collab use cases share normalizeActor — spot-check each gate so a
			// regression cannot hide behind "tasks and projects are fine".
			c := NewComments(uow, fc, &fakeIDGen{})
			if _, err := c.Add(ctx, bad, task.ID, domain.CommentNote, "hi"); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Comments.Add err = %v, want wrapped domain.ErrInvalid", err)
			}
			art := NewArtifacts(uow, fc, &fakeIDGen{})
			if _, err := art.Attach(ctx, bad, task.ID, domain.ArtifactLog, "https://example.com/x.log", ""); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Artifacts.Attach err = %v, want wrapped domain.ErrInvalid", err)
			}
			rel := NewRelations(uow, fc, &fakeIDGen{})
			other := mustCreateTask(t, svc, "other task", 1, nil)
			if _, _, err := rel.Relate(ctx, bad, task.ID, other.ID, domain.RelationRelated, RelateAdd); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Relations.Relate err = %v, want wrapped domain.ErrInvalid", err)
			}
			ac := NewAcceptance(uow, fc, &fakeIDGen{})
			if _, err := ac.AddAC(ctx, bad, task.ID, "new criterion"); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Acceptance.AddAC err = %v, want wrapped domain.ErrInvalid", err)
			}

			// Net effect: only the two fixture `created` events exist, and the
			// victim row is untouched (version 1, empty description).
			if got := len(uow.events.events); got != eventsAfterFixture+1 { // +1 for "other task"
				t.Errorf("events after rejections = %d, want %d (fixture creates only)", got, eventsAfterFixture+1)
			}
			stored, err := svc.Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get victim: %v", err)
			}
			if stored.Version != 1 || stored.Description != "" || stored.Notes != "" {
				t.Errorf("rejected writes mutated the row: (v%d desc %q notes %q), want (v1 \"\" \"\")",
					stored.Version, stored.Description, stored.Notes)
			}
			if len(uow.relations.edges) != 0 || len(uow.comments.byTask[task.ID]) != 0 {
				t.Errorf("rejected collab writes left state: %d edges, %d comments",
					len(uow.relations.edges), len(uow.comments.byTask[task.ID]))
			}
		})
	}
}

// TestProjects_WritePathsRejectInvalidActors is the Projects twin of the test
// above (AC4's projects path): Create and Update must both reject a malformed
// acting actor before recording any event or writing any row.
func TestProjects_WritePathsRejectInvalidActors(t *testing.T) {
	ctx := context.Background()

	for _, sh := range invalidActorShapes() {
		t.Run("actor_"+sh.name, func(t *testing.T) {
			svc, _, fp, fe := newProjectTestEnv(t)
			bad := sh.a

			// Create: rejected before anything is stored or recorded.
			if _, err := svc.Create(ctx, bad, "Demo", ""); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Create err = %v, want wrapped domain.ErrInvalid", err)
			}
			if len(fe.events) != 0 || len(fp.byID) != 0 {
				t.Fatalf("rejected Create still wrote state (%d events, %d projects), want none",
					len(fe.events), len(fp.byID))
			}

			// Fixture: a valid project, created by a valid actor.
			p, err := svc.Create(ctx, testAgent, "Demo", "")
			if err != nil {
				t.Fatalf("fixture Create: %v", err)
			}
			eventsAfterFixture := len(fe.events) // exactly the fixture's `created`

			// Update with a REAL field change: the invalid actor must be
			// rejected regardless of the payload.
			newName := "Renamed"
			if _, err := svc.Update(ctx, bad, p.ID, ProjectUpdate{Name: &newName}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("Update err = %v, want wrapped domain.ErrInvalid", err)
			}

			stored, gerr := fp.Get(ctx, p.ID)
			if gerr != nil {
				t.Fatalf("Get fixture project: %v", gerr)
			}
			if stored.Name != "Demo" {
				t.Errorf("rejected Update renamed the project to %q, want %q unchanged", stored.Name, "Demo")
			}
			if len(fe.events) != eventsAfterFixture {
				t.Errorf("events after rejection = %d, want %d (fixture create only)", len(fe.events), eventsAfterFixture)
			}
		})
	}
}
