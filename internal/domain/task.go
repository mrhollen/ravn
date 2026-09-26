package domain

import "time"

// Task is the unit of work. It deliberately has no parent/child fields: the
// hierarchy is expressed as Relation edges (RelationParent / RelationChild),
// and the computed "blocked" flag derives at read time from outgoing
// blocked-by edges — it is never stored on the task.
type Task struct {
	ID          ID
	ProjectID   ID
	Title       string
	Description string // markdown
	Status      Status
	Priority    Priority

	DueAt    *time.Time // optional deadline; nulls sort last in canonical order
	Assignee *Actor     // durable ownership (human|agent); nil = unassigned
	Notes    string     // markdown, durable resume context; replace-on-write

	Version  int // bumped ONLY by task-row mutations (version-bump policy)
	Archived bool

	// Ephemeral claim lease: set by claim, cleared by release. Distinct from
	// the durable Assignee above.
	ClaimedBy       *Actor // nil = unclaimed
	ClaimedAt       *time.Time
	ClaimExpiry     *time.Time
	ClaimTTLSeconds int // TTL stored at claim time; 0 = not claimed. Heartbeats reuse it

	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time // set when status becomes done
	LastActivity time.Time  // most recent mutation of the task or its child rows; derived by adapters (no dedicated column in the MVP schema)
}

// Done reports whether the task is in the terminal done status.
func (t Task) Done() bool { return t.Status == StatusDone }

// Claimed reports whether a lease (live or stale) is currently attached.
func (t Task) Claimed() bool { return t.ClaimedBy != nil && t.ClaimExpiry != nil }
