package domain

import (
	"fmt"
	"time"
	"unicode/utf8"
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

// MaxACTextLen caps an acceptance criterion's text in RUNES. Criteria render
// as list items in the task drawer; they stay tight like titles — a long blob
// belongs in the task description, not on the checklist.
const MaxACTextLen = 500

// MaxACNoteLen caps the note recorded when checking off a criterion in RUNES.
// Notes are short free-form context ("how it was verified"), not documents.
const MaxACNoteLen = 1024

// ValidateACText reports whether text is within MaxACTextLen runes. Called at
// WRITE paths only (criterion add / reword, plus bulk ACs on task create), so
// legacy rows stored before the cap remain readable and mutable — Validate
// (the completion invariant) deliberately does NOT re-litigate them. Wraps
// ErrInvalid.
func ValidateACText(text string) error {
	if runes := utf8.RuneCountInString(text); runes > MaxACTextLen {
		return fmt.Errorf("%w: acceptance criterion text is %d runes, max is %d", ErrInvalid, runes, MaxACTextLen)
	}
	return nil
}

// ValidateACNote reports whether note is within MaxACNoteLen runes (empty
// allowed — check-off without a note). WRITE paths only (the toggle use case).
// Wraps ErrInvalid.
func ValidateACNote(note string) error {
	if runes := utf8.RuneCountInString(note); runes > MaxACNoteLen {
		return fmt.Errorf("%w: acceptance criterion note is %d runes, max is %d", ErrInvalid, runes, MaxACNoteLen)
	}
	return nil
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
