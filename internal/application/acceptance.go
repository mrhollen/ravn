package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// Acceptance implements the acceptance-criterion toggle use case (ARCHITECTURE
// §5 PATCH /tasks/{id}/ac/{seq}, MCP complete_ac): confirm the parent task and
// the criterion addressed by its 1-based seq exist, toggle the done flag with
// who/when recorded on transition, and append exactly one `ac-toggled` event —
// all in a single unit of work.
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
