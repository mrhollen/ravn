package domain

// ActorType is the kind of principal that acts on crowban data: a human or an
// AI agent. Both are stored as plain (type, name) pairs; crowban does no
// identity resolution in the MVP.
type ActorType string

const (
	ActorHuman ActorType = "human"
	ActorAgent ActorType = "agent"
)

// Valid reports whether t is one of the known actor types.
func (t ActorType) Valid() bool {
	switch t {
	case ActorHuman, ActorAgent:
		return true
	default:
		return false
	}
}

// Actor names a principal as a typed (type, name) pair. It models both durable
// task ownership (Task.Assignee — stored as a pointer, nil = unassigned) and
// the actor behind comments, artifacts, relations, and events.
type Actor struct {
	Type ActorType
	Name string
}
