package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// Acceptance implements the acceptance-criterion use cases (ARCHITECTURE §5):
// the toggle (PATCH /tasks/{id}/acceptance-criteria/{seq}, MCP complete_ac) and
// the post-create editing trio (POST …/acceptance-criteria, PATCH … with a
// `text` field, DELETE …). Each confirms the parent task exists, mutates ONE
// child row, and appends exactly one event (`ac-toggled`, `ac-added`,
// `ac-updated`, or `ac-removed`) — all in a single unit of work.
//
// Version-bump policy (ARCHITECTURE §4): toggling a criterion is a CHILD-ROW
// mutation — it touches only the acceptance_criterion table plus the event
// log. The parent task row's version and updated_at are never touched, so a
// working agent holding a stale read cannot be 409'd by a check-off.
type Acceptance struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator
}

// NewAcceptance returns the Acceptance use case bound to uow, clock, and
// idgen. The toggle mints no criterion IDs (criteria keep their originals);
// the idgen covers the appended event row's primary key, and the clock
// supplies the completion instant recorded on transition (ADR).
func NewAcceptance(uow UnitOfWork, clock Clock, idgen IDGenerator) *Acceptance {
	return &Acceptance{uow: uow, clock: clock, idgen: idgen}
}

// ToggleAC flips the criterion at 1-based seq under taskID to done (or
// un-flips it when done is false) and records who/when on the transition:
// checking stamps CompletedBy/CompletedAt and stores note; unchecking clears
// all three completion fields again. It appends ONE `ac-toggled` event whose
// before→after payloads carry seq and the old/new done value. The parent
// task's version is NOT bumped (child-row rule). Unknown task or unknown seq
// → domain.ErrNotFound; nothing is written in that case.
func (s *Acceptance) ToggleAC(ctx context.Context, actor domain.Actor, taskID domain.ID, seq int, done bool, note string) (domain.AcceptanceCriterion, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	note = strings.TrimSpace(note)

	now := s.clock.Now()
	var fresh domain.AcceptanceCriterion
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		acs, lerr := r.ACs.ListByTask(ctx, taskID)
		if lerr != nil {
			return lerr
		}
		var cur *domain.AcceptanceCriterion
		for i := range acs {
			if acs[i].Seq == seq {
				cur = &acs[i]
				break
			}
		}
		if cur == nil {
			return fmt.Errorf("%w: task %s has no acceptance criterion at seq %d", domain.ErrNotFound, taskID, seq)
		}
		oldDone := cur.Done
		toggled, serr := r.ACs.SetDone(ctx, taskID, seq, done, note, a, now)
		if serr != nil {
			return serr // ErrNotFound if the row vanished between read and write (same tx — defensive)
		}
		fresh = toggled // assign to the captured variable (`:=` here would shadow it)
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &t.ID,
			Actor:     a,
			Kind:      domain.EventACToggled,
			Before:    compactJSON(map[string]any{"seq": seq, "done": oldDone}),
			After:     compactJSON(map[string]any{"seq": seq, "done": fresh.Done}),
			At:        now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	return fresh, nil
}

// nextSeqAfter returns max(seq)+1 over the given criteria (1 when there are
// none). Seq is monotonic: appends never reuse or renumber surviving values.
func nextSeqAfter(acs []domain.AcceptanceCriterion) int {
	next := 1
	for _, c := range acs {
		if c.Seq >= next {
			next = c.Seq + 1
		}
	}
	return next
}

// AddAC appends a new criterion to an existing task at seq max(seq)+1 (dense
// when nothing was deleted; gaps otherwise) and returns the fresh row. Text is
// trimmed; whitespace-only → domain.ErrInvalid (a checklist row with no words
// verifies nothing). Unknown task → domain.ErrNotFound. Appends ONE `ac-added`
// event (After carries seq + text); the parent task's version is NOT bumped
// (child-row rule); nothing is written on failure.
func (s *Acceptance) AddAC(ctx context.Context, actor domain.Actor, taskID domain.ID, text string) (domain.AcceptanceCriterion, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return domain.AcceptanceCriterion{}, fmt.Errorf("%w: acceptance criterion text must not be empty", domain.ErrInvalid)
	}

	now := s.clock.Now()
	fresh := domain.AcceptanceCriterion{ID: s.idgen.NewID(), TaskID: taskID, Text: text}
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		acs, lerr := r.ACs.ListByTask(ctx, taskID)
		if lerr != nil {
			return lerr
		}
		fresh.Seq = nextSeqAfter(acs)
		if err := r.ACs.Add(ctx, fresh); err != nil {
			return err // duplicate (task_id, seq) inside one tx would be a UoW race — surface it
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &fresh.TaskID,
			Actor:     a,
			Kind:      domain.EventACAdded,
			After:     compactJSON(map[string]any{"seq": fresh.Seq, "text": text}),
			At:        now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	return fresh, nil
}

// EditACText rewords the criterion at 1-based seq. Text is trimmed;
// whitespace-only → domain.ErrInvalid. An edit to IDENTICAL text is a no-op:
// the stored row is returned unchanged and NO event is appended (the log
// records state changes, not retries — same convention as UpdateNotes and
// relation re-adds). A real change appends ONE `ac-updated` event whose
// Before/After carry seq + old/new text. Unknown task or seq →
// domain.ErrNotFound. The parent task's version is NOT bumped (child-row rule).
func (s *Acceptance) EditACText(ctx context.Context, actor domain.Actor, taskID domain.ID, seq int, text string) (domain.AcceptanceCriterion, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return domain.AcceptanceCriterion{}, fmt.Errorf("%w: acceptance criterion text must not be empty", domain.ErrInvalid)
	}

	now := s.clock.Now()
	var fresh domain.AcceptanceCriterion
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		acs, lerr := r.ACs.ListByTask(ctx, taskID)
		if lerr != nil {
			return lerr
		}
		var cur *domain.AcceptanceCriterion
		for i := range acs {
			if acs[i].Seq == seq {
				cur = &acs[i]
				break
			}
		}
		if cur == nil {
			return fmt.Errorf("%w: task %s has no acceptance criterion at seq %d", domain.ErrNotFound, taskID, seq)
		}
		if cur.Text == text { // identical reword: no-op by convention (no event, no write)
			fresh = *cur
			return nil
		}
		oldText := cur.Text
		updated, uerr := r.ACs.UpdateText(ctx, taskID, seq, text)
		if uerr != nil {
			return uerr // ErrNotFound if the row vanished between read and write (same tx — defensive)
		}
		fresh = updated
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &taskID,
			Actor:     a,
			Kind:      domain.EventACUpdated,
			Before:    compactJSON(map[string]any{"seq": seq, "text": oldText}),
			After:     compactJSON(map[string]any{"seq": seq, "text": text}),
			At:        now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	return fresh, nil
}

// RemoveAC deletes the criterion at 1-based seq and appends ONE `ac-removed`
// event whose Before carries the deleted seq + text (nothing to carry in After
// — the row is gone). Surviving criteria keep their seq values (gaps stay; no
// renumbering). Unknown task or seq → domain.ErrNotFound. The parent task's
// version is NOT bumped (child-row rule); nothing is written on failure.
func (s *Acceptance) RemoveAC(ctx context.Context, actor domain.Actor, taskID domain.ID, seq int) error {
	a, err := normalizeActor(actor)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		removed, derr := r.ACs.Delete(ctx, taskID, seq)
		if derr != nil {
			return derr // ErrNotFound for unknown seq
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &taskID,
			Actor:     a,
			Kind:      domain.EventACRemoved,
			Before:    compactJSON(map[string]any{"seq": removed.Seq, "text": removed.Text}),
			At:        now,
		}
		return r.Events.Append(ctx, ev)
	})
	return err
}
