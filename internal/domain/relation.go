package domain

import "time"

// RelationKind names a directed edge between two tasks of the same project.
// Inverses such as "blocks" or "parent-of" are computed at read time, never
// stored. Values match the `relation.kind` CHECK constraint exactly.
//
// STORAGE MODEL (ADR 2026-09-26, slice 2D2a): the hierarchy edge is stored in
// ONE canonical direction — kind 'child', where TaskID is the child and
// OtherTaskID its parent: a row (X → Y, 'child') reads "X's parent is Y". The
// partial unique index on (task_id WHERE kind='child') (migration 0003) is
// what enforces ≤1 parent per task. 'parent' is the INVERSE view of that same
// edge: it is accepted as INPUT at the tool boundary and normalized to the
// stored child direction by the application layer before anything is written —
// a persisted row never carries kind='parent'. The other kinds (blocked-by,
// duplicate-of, related) are stored verbatim, directed source→target.
type RelationKind string

const (
	RelationParent      RelationKind = "parent" // input alias only: normalized to an inverse child edge, never persisted
	RelationChild       RelationKind = "child"  // canonical hierarchy storage: TaskID's parent is OtherTaskID
	RelationBlockedBy   RelationKind = "blocked-by"
	RelationDuplicateOf RelationKind = "duplicate-of"
	RelationRelated     RelationKind = "related"
)

// Valid reports whether k is acceptable as a TOOL-BOUNDARY relation kind — one
// of the stored kinds plus the 'parent' alias, which use cases normalize to an
// inverse child edge before persisting. It intentionally accepts more than
// Stored does.
func (k RelationKind) Valid() bool {
	switch k {
	case RelationParent, RelationChild, RelationBlockedBy, RelationDuplicateOf, RelationRelated:
		return true
	default:
		return false
	}
}

// Stored reports whether k may appear in a persisted relation row. Only the
// canonical storage kinds do; RelationParent exists solely as an input alias
// and is normalized away before any write (see type docs).
func (k RelationKind) Stored() bool {
	switch k {
	case RelationChild, RelationBlockedBy, RelationDuplicateOf, RelationRelated:
		return true
	default:
		return false
	}
}

// Relation is a directed edge: TaskID ("source") relates to OtherTaskID
// ("target"). A task may have at most one parent (enforced by the partial
// unique index on kind='child', migration 0003); self-edges are rejected.
type Relation struct {
	ID          ID
	TaskID      ID // source
	OtherTaskID ID // target
	Kind        RelationKind
	CreatedAt   time.Time
}
