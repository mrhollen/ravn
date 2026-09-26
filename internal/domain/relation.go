package domain

import "time"

// RelationKind is a directed edge between two tasks of the same project.
// Parent/child are the two directions of one hierarchy edge — unified into
// relations, since task has no parent column; inverses such as "blocks" or
// "parent-of" are computed, never stored. Values match the `relation.kind`
// CHECK constraint exactly.
type RelationKind string

const (
	RelationParent      RelationKind = "parent"
	RelationChild       RelationKind = "child"
	RelationBlockedBy   RelationKind = "blocked-by"
	RelationDuplicateOf RelationKind = "duplicate-of"
	RelationRelated     RelationKind = "related"
)

// Valid reports whether k is one of the allowed relation kinds.
func (k RelationKind) Valid() bool {
	switch k {
	case RelationParent, RelationChild, RelationBlockedBy, RelationDuplicateOf, RelationRelated:
		return true
	default:
		return false
	}
}

// Relation is a directed edge: TaskID ("source") relates to OtherTaskID
// ("target"). A task may have at most one parent (enforced by the partial
// unique index on kind='parent'); self-edges are rejected.
type Relation struct {
	ID          ID
	TaskID      ID // source
	OtherTaskID ID // target
	Kind        RelationKind
	CreatedAt   time.Time
}
