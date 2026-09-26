package domain

import "time"

// Event is one append-only activity record: who did what, a compact before→
// after summary of the changed values, and when. Every mutation appends at
// least one event; events are never updated or edited (they cascade only).
type Event struct {
	ID        ID
	ProjectID *ID // set whenever derivable — always for task-level events
	TaskID    *ID // nil only for pure project-level events
	Actor     Actor
	Kind      EventKind
	Before    string // compact JSON of changed values before ("" if none)
	After     string // compact JSON of changed values after ("" if none)
	At        time.Time
}

// EventKind classifies what a mutation did. The canonical set is locked in
// the ADR section of docs/ARCHITECTURE.md: use cases emit exactly these kinds,
// and adapters store them verbatim in event.kind (no DB CHECK constrains the
// column — this constant set IS the contract). Project-level events reuse the
// generic Created/Updated kinds with TaskID nil; RelationChanged covers both
// edge additions and removals (Before/After carry the edge), which supersedes
// the fine-grained relation-added/relation-removed vocabulary in the seed
// migration's comment.
type EventKind string

const (
	EventCreated         EventKind = "created"          // entity first stored
	EventUpdated         EventKind = "updated"          // field values changed (incl. project rename/description)
	EventStatusChanged   EventKind = "status-changed"   // task moved between workflow statuses
	EventClaimed         EventKind = "claimed"          // lease acquired (or refreshed by the holder)
	EventReleased        EventKind = "released"         // lease released, status back to todo
	EventHeartbeat       EventKind = "heartbeat"        // lease extended; no version bump
	EventAssigned        EventKind = "assigned"         // durable assignee changed
	EventNoted           EventKind = "noted"            // task notes replaced (durable resume context)
	EventCommented       EventKind = "commented"        // a typed comment was appended
	EventACToggled       EventKind = "ac-toggled"       // an acceptance criterion was checked/unchecked
	EventArtifactAdded   EventKind = "artifact-added"   // an evidence artifact was attached
	EventRelationChanged EventKind = "relation-changed" // a relation edge was added or removed
)

// AllEventKinds lists every canonical event kind, in stable order, for tests
// and future enum exposure over REST/MCP.
var AllEventKinds = []EventKind{
	EventCreated,
	EventUpdated,
	EventStatusChanged,
	EventClaimed,
	EventReleased,
	EventHeartbeat,
	EventAssigned,
	EventNoted,
	EventCommented,
	EventACToggled,
	EventArtifactAdded,
	EventRelationChanged,
}

// Valid reports whether k is one of the canonical event kinds. Use cases must
// only emit valid kinds; adapters store whatever they are given as-is (the
// column is unconstrained), so validation belongs at the emission site.
func (k EventKind) Valid() bool {
	for _, v := range AllEventKinds {
		if k == v {
			return true
		}
	}
	return false
}
