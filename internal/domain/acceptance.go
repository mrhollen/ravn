package domain

import "time"

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
