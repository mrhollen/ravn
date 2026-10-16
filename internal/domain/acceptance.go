package domain

import (
	"fmt"
	"time"
)

// AcceptanceCriterion is one row of a task's ordered checklist. Seq is the
// 1-based display order and the handle complete_ac targets; (TaskID, Seq) is
// unique. Toggling a criterion does NOT bump the task's version (child-row
// rule), though it still appends an event.
type AcceptanceCriterion struct {
	ID          ID
	TaskID      ID
	Seq         int // 1-based display order
	Text        string
	Done        bool
	Note        string // optional note recorded on check
	CompletedBy *Actor // nil until checked; cleared again on uncheck
	CompletedAt *time.Time
}

// Validate enforces the criterion's completion invariant: Done == true
// requires BOTH CompletedBy and CompletedAt to be set (a check-off records who
// and when); Done == false requires both to be nil (an uncheck clears the
// leftovers). Any desynchronized combination is rejected with an *InvariantError.
func (c AcceptanceCriterion) Validate() error {
	if c.Done {
		switch {
		case c.CompletedBy == nil && c.CompletedAt == nil:
			return &InvariantError{Entity: "acceptance criterion",
				Detail: fmt.Sprintf("seq %d is done but neither completed_by nor completed_at is set", c.Seq)}
		case c.CompletedBy == nil:
			return &InvariantError{Entity: "acceptance criterion",
				Detail: fmt.Sprintf("seq %d is done with completed_at %s but no completed_by",
					c.Seq, timeOrZero(c.CompletedAt))}
		case c.CompletedAt == nil: // by set, at missing
			return &InvariantError{Entity: "acceptance criterion",
				Detail: fmt.Sprintf("seq %d is done with completed_by (%s) but no completed_at",
					c.Seq, c.CompletedBy.Name)}
		default: // both set — a valid check-off
			return nil
		}
	}
	if c.CompletedBy != nil || c.CompletedAt != nil {
		return &InvariantError{Entity: "acceptance criterion",
			Detail: fmt.Sprintf("seq %d is undone but still carries completed_by=%v completed_at=%s — uncheck must clear both",
				c.Seq, actorRef(c.CompletedBy), timeOrZero(c.CompletedAt))}
	}
	return nil
}

func timeOrZero(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return rfc3339TS(*t)
}

func actorRef(a *Actor) string {
	if a == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %q", a.Type, a.Name)
}
