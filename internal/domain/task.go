package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

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
	// the durable Assignee above. One all-or-nothing value — see Claim: a
	// partial lease (holder without expiry or vice versa) cannot be attached
	// through any constructor; direct field literals outside this package are
	// impossible because every field is unexported.
	Claim Claim

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
func (t Task) HasLease() bool { return t.Claim.Present() }

// Claimed reports whether a lease row is attached — live or stale. It is the
// presence predicate and an alias of HasLease; callers that need to know the
// lease is still within its TTL must call IsClaimActive(now) instead.
func (t Task) Claimed() bool { return t.HasLease() }

// IsClaimActive reports whether the lease is currently held: a holder and an
// expiry are attached AND now is strictly before the expiry, so at exactly
// the expiry instant the lease is already over. `now` comes from the
// caller's application-layer clock — domain entities must never call
// time.Now().
func (t Task) IsClaimActive(now time.Time) bool { return t.Claim.Active(now) }

// Validate enforces the task's structural invariants:
//
//   - CompletedAt is non-nil if and only if Status == StatusDone — a done task
//     without a completion stamp, or a non-done task carrying one, is corrupt;
//   - the claim lease satisfies its own all-or-nothing invariant (Claim.Validate).
//
// Violations surface as *InvariantError. The use cases call this at their
// mutation sites so an inconsistent row can never reach an adapter; adapters
// additionally reconstruct leases through ClaimFromStored, which degrades a
// corrupt stored row to "unclaimed" on read instead of erroring the whole list.
func (t Task) Validate() error {
	if t.Status == StatusDone && t.CompletedAt == nil {
		return &InvariantError{Entity: "task",
			Detail: fmt.Sprintf("status is %q but completed_at is missing — completion stamps the instant", t.Status)}
	}
	if t.Status != StatusDone && t.CompletedAt != nil {
		return &InvariantError{Entity: "task",
			Detail: fmt.Sprintf("completed_at is set (%s) but status is %q, not %q",
				rfc3339TS(*t.CompletedAt), t.Status, StatusDone)}
	}
	return t.Claim.Validate()
}

// rfc3339TS renders a timestamp in the wire format: RFC3339 in UTC (the exact
// form the REST DTO layer emits — byte-identity is guarded by tests).
func rfc3339TS(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// taskWire is Task's canonical flat wire representation: the summary+detail
// field set of the REST rows (the child collections and the computed blocked
// flag are NOT on the domain entity, so they do not appear here). Key names,
// ordering, and omitempty/null semantics are byte-identical to the pre-
// encapsulation wire format — guarded by TestTaskJSONWireFormat.
type taskWire struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Priority       int        `json:"priority"`
	DueAt          *string    `json:"due_at,omitempty"`
	Assignee       *actorWire `json:"assignee,omitempty"`
	ClaimedBy      *actorWire `json:"claimed_by,omitempty"`
	ClaimExpiresAt *string    `json:"claim_expires_at,omitempty"`
	Version        int        `json:"version"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`

	ProjectID   string `json:"project_id"`
	Description string `json:"description"`
	Notes       string `json:"notes"`
	Archived    bool   `json:"archived"`

	ClaimedAt       *string `json:"claimed_at,omitempty"`
	ClaimTTLSeconds int     `json:"claim_ttl_seconds,omitempty"` // 0 = not claimed
	CompletedAt     *string `json:"completed_at,omitempty"`
}

// actorWire is the wire form of domain.Actor: {"type":"human|agent","name":...}.
type actorWire struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func toActorWire(a *Actor) *actorWire {
	if a == nil {
		return nil
	}
	return &actorWire{Type: string(a.Type), Name: a.Name}
}

// MarshalJSON renders the task in its canonical flat wire form (see taskWire).
// The four lease keys are flattened at the task level exactly as the REST rows
// have always carried them; an unclaimed task omits them entirely.
func (t Task) MarshalJSON() ([]byte, error) {
	w := taskWire{
		ID:              t.ID.String(),
		Title:           t.Title,
		Status:          string(t.Status),
		Priority:        int(t.Priority),
		Assignee:        toActorWire(t.Assignee),
		Version:         t.Version,
		CreatedAt:       rfc3339TS(t.CreatedAt),
		UpdatedAt:       rfc3339TS(t.UpdatedAt),
		ProjectID:       t.ProjectID.String(),
		Description:     t.Description,
		Notes:           t.Notes,
		Archived:        t.Archived,
		ClaimTTLSeconds: t.Claim.TTLSeconds(), // 0 = not claimed (omitempty drops it)
	}
	if t.DueAt != nil {
		v := rfc3339TS(*t.DueAt)
		w.DueAt = &v
	}
	if t.CompletedAt != nil {
		v := rfc3339TS(*t.CompletedAt)
		w.CompletedAt = &v
	}
	if c := t.Claim; c.Present() { // all-or-nothing: present ⇒ every piece is set
		by := toActorWire(c.By())
		at := rfc3339TS(c.At())
		exp := rfc3339TS(c.Expiry())
		w.ClaimedBy = by
		w.ClaimExpiresAt = &exp
		w.ClaimedAt = &at
	}
	return json.Marshal(w)
}

// UnmarshalJSON parses the canonical flat wire form back into a Task. A task
// with status done (and no completed_at) or a non-done task carrying one is
// rejected, as is any partial lease — the result of a successful unmarshal is
// always a task that passes Validate().
func (t *Task) UnmarshalJSON(data []byte) error {
	var w taskWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	t.ID = ID(w.ID)
	t.ProjectID = ID(w.ProjectID)
	t.Title = w.Title
	t.Description = w.Description
	t.Status = Status(w.Status)
	t.Priority = Priority(w.Priority)
	t.Version = w.Version
	t.Archived = w.Archived
	t.Notes = w.Notes

	var createdAt, updatedAt time.Time
	if err := func() error {
		var err error
		if createdAt, err = time.Parse(time.RFC3339, w.CreatedAt); err != nil {
			return fmt.Errorf("parse created_at %q: %w", w.CreatedAt, err)
		}
		if updatedAt, err = time.Parse(time.RFC3339, w.UpdatedAt); err != nil {
			return fmt.Errorf("parse updated_at %q: %w", w.UpdatedAt, err)
		}
		t.CreatedAt = createdAt
		t.UpdatedAt = updatedAt
		return nil
	}(); err != nil {
		return err
	}

	var dueAt *time.Time
	if w.DueAt != nil {
		ts, err := time.Parse(time.RFC3339, *w.DueAt)
		if err != nil {
			return fmt.Errorf("parse due_at %q: %w", *w.DueAt, err)
		}
		dueAt = &ts
	}
	t.DueAt = dueAt

	a := actorToDomain(w.Assignee)
	t.Assignee = a

	var completedAt *time.Time
	if w.CompletedAt != nil {
		ts, err := time.Parse(time.RFC3339, *w.CompletedAt)
		if err != nil {
			return fmt.Errorf("parse completed_at %q: %w", *w.CompletedAt, err)
		}
		completedAt = &ts
	}
	t.CompletedAt = completedAt

	leaseSet := w.ClaimedBy != nil || w.ClaimedAt != nil || w.ClaimExpiresAt != nil
	if leaseSet { // any piece ⇒ every piece must be present (all-or-nothing)
		if w.ClaimedBy == nil || w.ClaimedAt == nil || w.ClaimExpiresAt == nil {
			return &InvariantError{Entity: "claim lease",
				Detail: fmt.Sprintf("partial lease in JSON: claimed_by set=%v claimed_at present=%v claim_expires_at present=%v ttl=%d",
					w.ClaimedBy != nil, w.ClaimedAt != nil, w.ClaimExpiresAt != nil, w.ClaimTTLSeconds)}
		}
		at, err := time.Parse(time.RFC3339, *w.ClaimedAt)
		if err != nil {
			return fmt.Errorf("parse claimed_at %q: %w", *w.ClaimedAt, err)
		}
		exp, err := time.Parse(time.RFC3339, *w.ClaimExpiresAt)
		if err != nil {
			return fmt.Errorf("parse claim_expires_at %q: %w", *w.ClaimExpiresAt, err)
		}
		ttl := w.ClaimTTLSeconds
		t.Claim = ClaimFromStored(actorToDomain(w.ClaimedBy), &at, &exp, &ttl)
		if !t.Claim.Present() { // ttl was non-positive
			return &InvariantError{Entity: "claim lease",
				Detail: fmt.Sprintf("ttl of %d seconds must be > 0 on an attached lease", ttl)}
		}
	} else {
		t.Claim = Claim{}
	}

	if err := t.Validate(); err != nil { // done ⟺ completed_at (the lease was just validated)
		return err
	}
	return nil
}

func actorToDomain(w *actorWire) *Actor {
	if w == nil {
		return nil
	}
	return &Actor{Type: ActorType(w.Type), Name: w.Name}
}
