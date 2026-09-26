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
	Kind      string // created|updated|status-changed|claimed|released|heartbeat|noted|commented|ac-toggled|artifact-added|relation-added|relation-removed|project-created|project-updated
	Before    string // compact JSON of changed values before ("" if none)
	After     string // compact JSON of changed values after ("" if none)
	At        time.Time
}
