package domain

// Priority is a task's urgency on the 0–5 scale; higher values sort first in
// the canonical "what's next" ordering. Zero is both the default and the
// lowest priority (matches the schema CHECK priority BETWEEN 0 AND 5).
type Priority int

const (
	PriorityLowest  Priority = 0
	PriorityHighest Priority = 5
)

// Valid reports whether p lies within [PriorityLowest, PriorityHighest].
func (p Priority) Valid() bool { return p >= PriorityLowest && p <= PriorityHighest }
