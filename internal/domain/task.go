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

// HasLease reports whether a lease row is attached, regardless of its TTL:
// true for both LIVE and stale ("zombie") leases. Pure presence — it never
// consults the clock (domain entities have no time source). Use
// IsClaimActive(now) when liveness matters.
func (t Task) HasLease() bool { return t.ClaimedBy != nil && t.ClaimExpiry != nil }

// Claimed reports whether a lease row is attached — live or stale. It is the
// presence predicate and an alias of HasLease; callers that need to know the
// lease is still within its TTL must call IsClaimActive(now) instead.
func (t Task) Claimed() bool { return t.HasLease() }

// IsClaimActive reports whether the lease is currently held: a holder and an
// expiry are attached AND now is strictly before the expiry, so at exactly
// the expiry instant the lease is already over. `now` comes from the
// caller's application-layer clock — domain entities must never call
// time.Now().
func (t Task) IsClaimActive(now time.Time) bool { return t.HasLease() && now.Before(*t.ClaimExpiry) }
