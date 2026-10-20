package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
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
//
// Settled-parent rule (frozenACError): EVERY mutation — add, edit, toggle in
// both directions, remove — is rejected with domain.ErrInvalid while the
// parent task is completed (status=done) or archived. A done task must not
// drift into "completed with incomplete criteria", and an archived record is
// immutable; clients must reopen (done→todo per the transition map) and/or
// unarchive before touching criteria again. Note: checking an already-done
// criterion on a done task is blocked by design under this rule — reopen
// first.
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

// frozenACError is the shared rejection for every AC mutation when the parent
// task is SETTLED: completed (status=done) or archived. Mutating criteria
// there would resurrect a semantic contradiction — a done task left with open
// or rewritten criteria, or edits to an archived record — so ALL mutations
// (add / edit / toggle in BOTH directions / remove) are refused until the
// task is reopened (done→todo per the transition map) and/or unarchived.
// The message doubles as the recovery instruction; it wraps domain.ErrInvalid
// so REST renders the standard 400 invalid-input body and MCP surfaces it
// verbatim. Archived is checked first (mirrors Claim's guard order), so a
// done+archived task is told to unarchive before the reopen rule applies.
// The clause carries its own preposition ("toggling … on", "adding … to").
func frozenACError(t domain.Task, clause string) error {
	if t.Archived {
		return fmt.Errorf("%w: %s archived task %s — unarchive it first", domain.ErrInvalid, clause, t.ID)
	}
	if t.Status == domain.StatusDone {
		return fmt.Errorf("%w: %s completed task %s — reopen the task first", domain.ErrInvalid, clause, t.ID)
	}
	return nil
}

// ToggleAC flips the criterion at 1-based seq under taskID to done (or
// un-flips it when *done is false) and records who/when on the transition:
// checking stamps CompletedBy/CompletedAt and stores note; unchecking clears
// all three completion fields again. When done is NIL — a PATCH that omits
// "done" — the current done state AND its completion fields are PRESERVED:
// only the note is rewritten via ACs.UpdateNote (SetDone(true) would re-stamp
// CompletedBy/CompletedAt). A nil-done call whose trimmed note equals the
// stored note is a no-op: the stored row is returned unchanged and NO event is
// appended (same convention as identical text rewords). Each real write
// appends ONE `ac-toggled` event whose before→after payloads carry seq and the
// old/new done value — identical on the preserve path, matching note-carrying
// toggles. The parent task's version is NOT bumped (child-row rule). Unknown
// task or unknown seq → domain.ErrNotFound, and an incoming note over
// domain.MaxACNoteLen runes → domain.ErrInvalid; nothing is written in either
// case.
func (s *Acceptance) ToggleAC(ctx context.Context, actor domain.Actor, taskID domain.ID, seq int, done *bool, note string) (domain.AcceptanceCriterion, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	note = strings.TrimSpace(note)
	// Bound check on the INCOMING note only (write path): a stored legacy note
	// over the cap stays readable and mutable — an uncheck or re-check without
	// a note must not re-litigate it. Empty passes (check-off without a note).
	if err := domain.ValidateACNote(note); err != nil {
		return domain.AcceptanceCriterion{}, err
	}

	now := s.clock.Now()
	var fresh domain.AcceptanceCriterion
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if err := frozenACError(t, "toggling an acceptance criterion on"); err != nil {
			return err // settled parent: reopen/unarchive first (no write, no event)
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
		var toggled domain.AcceptanceCriterion
		var werr error
		if done != nil { // explicit true/false: EXACTLY the legacy toggle (stamp/clear completion fields)
			toggled, werr = r.ACs.SetDone(ctx, taskID, seq, *done, note, a, now)
		} else if note == cur.Note { // omitted "done" + unchanged note: no-op by convention (no write, no event)
			fresh = *cur
			return nil
		} else { // omitted "done" + new note: preserve done AND completion fields; rewrite the note only
			toggled, werr = r.ACs.UpdateNote(ctx, taskID, seq, note)
		}
		if werr != nil {
			return werr // ErrNotFound if the row vanished between read and write (same tx — defensive)
		}
		fresh = toggled                            // assign to the captured variable (`:=` here would shadow it)
		if verr := fresh.Validate(); verr != nil { // fail closed: a desynchronized completion row never reaches an adapter
			return verr
		}
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
// trimmed; whitespace-only or over domain.MaxACTextLen runes →
// domain.ErrInvalid (a checklist row with no words verifies nothing, and a
// long blob belongs in the task description). Unknown task →
// domain.ErrNotFound. Appends ONE `ac-added`
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
	if err := domain.ValidateACText(text); err != nil {
		return domain.AcceptanceCriterion{}, err
	}

	now := s.clock.Now()
	fresh := domain.AcceptanceCriterion{ID: s.idgen.NewID(), TaskID: taskID, Text: text}
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if err := frozenACError(t, "adding an acceptance criterion to"); err != nil {
			return err // settled parent: reopen/unarchive first (no write, no event)
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
// whitespace-only or over domain.MaxACTextLen runes → domain.ErrInvalid (only
// the INCOMING value is checked, so legacy rows stay mutable). An edit to
// IDENTICAL text is a no-op:
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
	if err := domain.ValidateACText(text); err != nil { // incoming value only: legacy rows stay mutable
		return domain.AcceptanceCriterion{}, err
	}

	now := s.clock.Now()
	var fresh domain.AcceptanceCriterion
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if err := frozenACError(t, "editing an acceptance criterion on"); err != nil {
			return err // settled parent: reopen/unarchive first (no write, no event)
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
		if err := frozenACError(t, "removing an acceptance criterion from"); err != nil {
			return err // settled parent: reopen/unarchive first (no write, no event)
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

// Progress returns the (total, done) acceptance-criterion aggregate for each
// of the given tasks — in ONE grouped read (no N+1). It backs the board
// snapshot's per-card AC-progress badge: counts only, no criterion text.
// Tasks with no criteria at all are omitted from the result map; empty input
// returns an empty map without querying. The read runs through the unit of
// work for uniformity and is read-only: it appends no events and touches
// nothing. (Task existence is not checked — unknown tasks simply stay absent.)
func (s *Acceptance) Progress(ctx context.Context, taskIDs []domain.ID) (map[domain.ID]TaskACProgress, error) {
	if len(taskIDs) == 0 {
		return map[domain.ID]TaskACProgress{}, nil
	}
	var out map[domain.ID]TaskACProgress
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		progress, perr := r.ACs.ProgressByTasks(ctx, taskIDs)
		if perr != nil {
			return perr
		}
		out = progress
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
