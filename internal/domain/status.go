package domain

// Status is a task's workflow state. Statuses are NAMED DATA — rows of the
// seeded `status` table — not a closed Go enum: Tier-2 adds statuses without a
// code change (no migration needed). The canonical MVP names below are exactly
// what seed migration 0001 inserts, and Task.Status always holds one of them
// in Tier 1.
type Status string

const (
	StatusTodo  Status = "todo"
	StatusDoing Status = "doing"
	StatusDone  Status = "done"
)

// CanonicalStatuses returns the seeded Tier-1 statuses in board-column order.
func CanonicalStatuses() []Status {
	return []Status{StatusTodo, StatusDoing, StatusDone}
}

// IsCanonical reports whether s is one of the seeded Tier-1 statuses.
func (s Status) IsCanonical() bool {
	switch s {
	case StatusTodo, StatusDoing, StatusDone:
		return true
	default:
		return false
	}
}
