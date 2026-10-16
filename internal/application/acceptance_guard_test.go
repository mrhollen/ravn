package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mrhollen/crowban/internal/domain"
)

// acceptance_guard_test.go — the settled-parent rule (frozenACError): while
// the parent task is completed (status=done) or archived, EVERY AC mutation
// is rejected with a wrapped domain.ErrInvalid and nothing is written; after
// reopen (done→todo) / unarchive the same mutations succeed again.

// newGuardedEnv returns the standard env plus a task seeded with exactly two
// criteria (seq 1, 2) so the matrix can exercise both toggle directions.
func newGuardedEnv(t *testing.T) (*Tasks, *Acceptance, *fakeClock, *fakeUoW, domain.Task) {
	t.Helper()
	svc, fc, uow := newTaskTestEnv(t)
	task, err := svc.Create(context.Background(), testAgent, TaskInput{
		Title:     "Guarded criteria",
		ProjectID: testProjectID,
		ACs:       []string{"Criterion one", "Criterion two"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	acs := NewAcceptance(uow, fc, &fakeIDGen{})
	return svc, acs, fc, uow, task
}

// mustSettleDone completes both seeded criteria and moves the task to done
// through the validated todo→doing→done path (the only route now that the
// AC gate is unconditional).
func mustSettleDone(t *testing.T, svc *Tasks, acs *Acceptance, task domain.Task) {
	t.Helper()
	ctx := context.Background()
	for seq := 1; seq <= 2; seq++ {
		if _, err := acs.ToggleAC(ctx, testAgent, task.ID, seq, true, "done"); err != nil {
			t.Fatalf("complete AC %d: %v", seq, err)
		}
	}
	mustDone(t, svc, task.ID)
}

// mustSettleArchived marks the (todo-state) task archived via Update.
func mustSettleArchived(t *testing.T, svc *Tasks, id domain.ID) {
	t.Helper()
	archived := true
	if _, err := svc.Update(context.Background(), testAgent, id, TaskUpdateInput{Archived: &archived}); err != nil {
		t.Fatalf("archive %s: %v", id, err)
	}
}

// TestACMutations_RejectedOnSettledTasks is the rejection matrix: 5 operations
// (add / edit / toggle-uncheck / toggle-check / remove) × 2 settled states
// (completed / archived). Every attempt must be refused with a wrapped
// domain.ErrInvalid naming the right recovery, and NO row or event may change.
func TestACMutations_RejectedOnSettledTasks(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		settle     func(*testing.T, *Tasks, *Acceptance, domain.Task)
		wantPhrase string // recovery instruction to pin in the error message
	}{
		{"completed", mustSettleDone, "reopen the task first"},
		{"archived", func(t *testing.T, svc *Tasks, _ *Acceptance, task domain.Task) {
			mustSettleArchived(t, svc, task.ID)
		}, "unarchive it first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, acs, _, uow, task := newGuardedEnv(t)
			tc.settle(t, svc, acs, task)

			eventsBefore := len(uow.events.events)
			mutations := []struct {
				name string
				run  func() error
			}{
				{"AddAC", func() error { _, err := acs.AddAC(ctx, testAgent, task.ID, "Late criterion"); return err }},
				{"EditACText", func() error { _, err := acs.EditACText(ctx, testAgent, task.ID, 1, "Rewritten"); return err }},
				{"ToggleAC_uncheck", func() error { _, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, false, ""); return err }},
				// Checking an ALREADY-DONE criterion on a completed task is blocked
				// by design under the settled-parent rule (reopen first).
				{"ToggleAC_check", func() error { _, err := acs.ToggleAC(ctx, testAgent, task.ID, 2, true, "late"); return err }},
				{"RemoveAC", func() error { return acs.RemoveAC(ctx, testAgent, task.ID, 1) }},
			}
			for _, m := range mutations {
				t.Run(m.name, func(t *testing.T) {
					err := m.run()
					if !errors.Is(err, domain.ErrInvalid) {
						t.Fatalf("%s: err = %v (%T), want wrapped domain.ErrInvalid", m.name, err, err)
					}
					if !strings.Contains(err.Error(), tc.wantPhrase) {
						t.Errorf("%s: error %q must instruct: %s", m.name, err, tc.wantPhrase)
					}
				})
			}

			// No mutation and no event survived any of the attempts.
			got, lerr := uow.acs.ListByTask(ctx, task.ID)
			if lerr != nil {
				t.Fatalf("ListByTask after rejected mutations: %v", lerr)
			}
			if len(got) != 2 {
				t.Fatalf("AC list = %d items after rejected mutations, want 2 untouched", len(got))
			}
			for i, ac := range got {
				wantDone := tc.name == "completed" // settled-done fixture keeps both met; archived keeps both open
				if ac.Done != wantDone {
					t.Errorf("AC seq %d Done = %v, want %v (unmutated)", i+1, ac.Done, wantDone)
				}
			}
			if len(uow.events.events) != eventsBefore {
				t.Errorf("events grew from %d to %d; rejections must emit nothing", eventsBefore, len(uow.events.events))
			}
		})
	}
}

// TestACMutations_AllowedAgainAfterReopenAndUnarchive pins the flip side of
// the rule: the refusal is about the SETTLED state, not a permanent freeze —
// reopen (done→todo) or unarchive restores full mutability.
func TestACMutations_AllowedAgainAfterReopenAndUnarchive(t *testing.T) {
	t.Run("after reopen done to todo", func(t *testing.T) {
		ctx := context.Background()
		svc, acs, _, uow, task := newGuardedEnv(t)
		mustSettleDone(t, svc, acs, task)

		if err := acs.RemoveAC(ctx, testAgent, task.ID, 1); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("on completed task: err = %v (%T), want wrapped domain.ErrInvalid", err, err)
		}

		todo := domain.StatusTodo
		if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Status: &todo}); err != nil {
			t.Fatalf("reopen to todo: %v", err)
		}

		// All four mutations succeed again.
		if _, err := acs.AddAC(ctx, testAgent, task.ID, "Late criterion"); err != nil {
			t.Errorf("AddAC after reopen: %v", err)
		}
		if _, err := acs.EditACText(ctx, testAgent, task.ID, 2, "Criterion two (edited)"); err != nil {
			t.Errorf("EditACText after reopen: %v", err)
		}
		got, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, false, "")
		if err != nil {
			t.Fatalf("ToggleAC uncheck after reopen: %v", err)
		}
		if got.Done {
			t.Errorf("AC seq 1 Done = true after uncheck, want false")
		}
		if err := acs.RemoveAC(ctx, testAgent, task.ID, 3); err != nil { // the newly added criterion
			t.Errorf("RemoveAC after reopen: %v", err)
		}

		list, lerr := uow.acs.ListByTask(ctx, task.ID)
		if lerr != nil {
			t.Fatalf("ListByTask: %v", lerr)
		}
		if len(list) != 2 { // seq 1 (now unmet) + edited seq 2
			t.Fatalf("final AC list = %d items, want 2", len(list))
		}
	})

	t.Run("after unarchive", func(t *testing.T) {
		ctx := context.Background()
		svc, acs, _, uow, task := newGuardedEnv(t)
		mustSettleArchived(t, svc, task.ID)

		if err := acs.RemoveAC(ctx, testAgent, task.ID, 1); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("on archived task: err = %v (%T), want wrapped domain.ErrInvalid", err, err)
		}

		notArchived := false
		if _, err := svc.Update(ctx, testAgent, task.ID, TaskUpdateInput{Archived: &notArchived}); err != nil {
			t.Fatalf("unarchive: %v", err)
		}

		if _, err := acs.AddAC(ctx, testAgent, task.ID, "Late criterion"); err != nil {
			t.Errorf("AddAC after unarchive: %v", err)
		}
		list, lerr := uow.acs.ListByTask(ctx, task.ID)
		if lerr != nil {
			t.Fatalf("ListByTask: %v", lerr)
		}
		if len(list) != 3 { // the two seeded + the late addition
			t.Fatalf("final AC list = %d items, want 3", len(list))
		}
	})
}
