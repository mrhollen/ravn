package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- Claim: all-or-nothing lease -------------------------------------------

// TestClaimValidate_AllOrNothing pins the core invariant of the encapsulated
// lease: a Claim is either fully absent (the zero value) or fully present with
// a positive TTL. Every partial combination — which only this package can
// fabricate, since every field is unexported — must be rejected with an
// *InvariantError that names what is set and what is missing.
func TestClaimValidate_AllOrNothing(t *testing.T) {
	by := Actor{Type: ActorAgent, Name: "worker-7"}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	t.Run("zero value is valid", func(t *testing.T) {
		if err := (Claim{}).Validate(); err != nil {
			t.Errorf("unclaimed Claim.Validate() = %v, want nil", err)
		}
	})

	t.Run("freshly minted lease is valid", func(t *testing.T) {
		if err := NewClaim(by, now, 900).Validate(); err != nil {
			t.Errorf("NewClaim(...).Validate() = %v, want nil", err)
		}
	})

	t.Run("lease rebuilt from stored columns is valid", func(t *testing.T) {
		c := ClaimFromStored(&by, timePtr(now), timePtr(now.Add(900*time.Second)), intPtr(900))
		if err := c.Validate(); err != nil {
			t.Errorf("ClaimFromStored(...).Validate() = %v, want nil", err)
		}
	})

	cases := []struct {
		name string
		c    Claim
	}{
		{"holder only", Claim{by: &by}},
		{"grant instant only", Claim{at: now}},
		{"expiry only", Claim{expiresAt: now.Add(time.Minute)}},
		{"ttl only", Claim{ttlSecs: 900}},
		{"holder and grant, no expiry or ttl", Claim{by: &by, at: now}},
		{"grant and expiry, no holder", Claim{at: now, expiresAt: now.Add(time.Minute)}},
		{"holder and expiry, no grant or ttl", Claim{by: &by, expiresAt: now.Add(time.Minute)}},
	}
	for _, tc := range cases {
		t.Run("partial: "+tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			ie, ok := errAsInvariant(err)
			if !ok {
				t.Fatalf("Validate() = %v (%T), want *InvariantError", err, err)
			}
			if ie.Entity != "claim lease" {
				t.Errorf("Entity = %q, want %q", ie.Entity, "claim lease")
			}
			for _, word := range []string{"set but", "missing"} {
				if !strings.Contains(ie.Detail, word) {
					t.Errorf("Detail %q does not name %q", ie.Detail, word)
				}
			}
		})
	}

	t.Run("attached lease with non-positive ttl is invalid", func(t *testing.T) {
		for _, ttl := range []int{0, -5} {
			c := Claim{by: &by, at: now, expiresAt: now.Add(time.Minute), ttlSecs: ttl}
			err := c.Validate()
			ie, ok := errAsInvariant(err)
			if !ok {
				t.Fatalf("Validate() with ttl %d = %v (%T), want *InvariantError", ttl, err, err)
			}
			if !strings.Contains(ie.Detail, "ttl") {
				t.Errorf("Detail %q does not mention the bad ttl", ie.Detail)
			}
		}
	})

	t.Run("stale (zombie) lease is still valid presence", func(t *testing.T) {
		// Past expiry must NOT trip Validate: lazy expiry means a zombie row is
		// a legitimate domain state until someone claims or releases it.
		c := ClaimFromStored(&by, timePtr(now.Add(-2*time.Minute)), timePtr(now.Add(-time.Minute)), intPtr(60))
		if !c.Present() || c.Active(now) {
			t.Fatalf("setup: zombie lease want present and inactive (present=%v active=%v)", c.Present(), c.Active(now))
		}
		if err := c.Validate(); err != nil {
			t.Errorf("zombie Validate() = %v, want nil", err)
		}
	})
}

// TestClaimConstructors_CannotForgePartials pins the construction contract:
// both sanctioned constructors either produce a fully valid lease or fall back
// to the zero value — never a partial.
func TestClaimConstructors_CannotForgePartials(t *testing.T) {
	by := Actor{Type: ActorHuman, Name: "ops"}
	now := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)

	t.Run("ClaimFromStored drops any row missing one projection", func(t *testing.T) {
		at := now
		exp := now.Add(5 * time.Minute)
		ttl := 300
		zeros := []Claim{
			ClaimFromStored(nil, &at, &exp, &ttl),       // no holder
			ClaimFromStored(&by, nil, &exp, &ttl),       // no grant instant
			ClaimFromStored(&by, &at, nil, &ttl),        // no expiry
			ClaimFromStored(&by, &at, &exp, nil),        // no ttl
			ClaimFromStored(&by, &at, &exp, intPtr(0)),  // zero ttl
			ClaimFromStored(&by, &at, &exp, intPtr(-1)), // negative ttl
		}
		for i, c := range zeros {
			if c.Present() {
				t.Errorf("case %d: constructor returned a lease from incomplete projections (by=%v at=%v exp=%v ttl=%d)",
					i, c.By(), c.At(), c.Expiry(), c.TTLSeconds())
			}
			if err := c.Validate(); err != nil {
				t.Errorf("case %d: zero fallback must validate clean, got %v", i, err)
			}
		}
	})

	t.Run("NewClaim copies the actor (no aliasing)", func(t *testing.T) {
		c := NewClaim(by, now, 60)
		gotBy := *c.By()
		gotBy.Name = "mangled"
		if c.By().Name != by.Name {
			t.Errorf("NewClaim aliased the caller's actor: holder is now %q", c.By().Name)
		}
	})

	t.Run("NewClaim derives expiry as at+ttl", func(t *testing.T) {
		c := NewClaim(by, now, 60)
		want := now.Add(60 * time.Second)
		if !c.Expiry().Equal(want) || c.At() != now || c.TTLSeconds() != 60 {
			t.Errorf("lease = (at %v exp %v ttl %d), want at=now exp=%v ttl=60", c.At(), c.Expiry(), c.TTLSeconds(), want)
		}
	})

	t.Run("ExtendedTo re-stamps only the expiry", func(t *testing.T) {
		c := NewClaim(by, now, 60)
		newExp := now.Add(90 * time.Second)
		e := c.ExtendedTo(newExp)
		if !e.Expiry().Equal(newExp) || e.At() != now || e.TTLSeconds() != 60 || e.By() == nil || e.By().Name != by.Name {
			t.Errorf("ExtendedTo = (by %v at %v exp %v ttl %d), want holder/grant/ttl kept with new expiry",
				e.By(), e.At(), e.Expiry(), e.TTLSeconds())
		}
		if !c.Expiry().Equal(now.Add(60 * time.Second)) {
			t.Errorf("ExtendedTo mutated the receiver: expiry now %v", c.Expiry())
		}
		if got := (Claim{}).ExtendedTo(newExp); got.Present() {
			t.Errorf("extending an unclaimed lease must stay zero, got %+v", got)
		}
	})

	t.Run("Released clears to the zero value", func(t *testing.T) {
		if got := NewClaim(by, now, 60).Released(); got.Present() {
			t.Errorf("Released() = %+v, want the zero value", got)
		}
	})
}

// --- Task: completion invariant ---------------------------------------------

func TestTaskValidate_CompletedAtInvariant(t *testing.T) {
	when := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	t.Run("done without a completion stamp is corrupt", func(t *testing.T) {
		ta := Task{ID: ID("a"), Status: StatusDone}
		err := ta.Validate()
		ie, ok := errAsInvariant(err)
		if !ok {
			t.Fatalf("Validate() = %v (%T), want *InvariantError", err, err)
		}
		if ie.Entity != "task" {
			t.Errorf("Entity = %q, want task", ie.Entity)
		}
	})

	t.Run("non-done carrying a completion stamp is corrupt", func(t *testing.T) {
		for _, st := range []Status{StatusTodo, StatusDoing} {
			ta := Task{ID: ID("b"), Status: st, CompletedAt: &when}
			if err := ta.Validate(); err == nil {
				t.Errorf("status %q with completed_at set: Validate() = nil, want *InvariantError", st)
			} else if ie, ok := errAsInvariant(err); !ok || ie.Entity != "task" {
				t.Errorf("status %q: err = %v (%T), want *InvariantError entity task", st, err, err)
			}
		}
	})

	t.Run("consistent pairs validate clean", func(t *testing.T) {
		cases := []Task{
			{ID: ID("c"), Status: StatusDone, CompletedAt: &when},
			{ID: ID("d"), Status: StatusTodo},
			{ID: ID("e"), Status: StatusDoing},
			{ID: ID("f"), Status: StatusTodo, Claim: NewClaim(Actor{Type: ActorAgent, Name: "w"}, when, 60)},
		}
		for i, ta := range cases {
			if err := ta.Validate(); err != nil {
				t.Errorf("case %d: Validate() = %v, want nil", i, err)
			}
		}
	})

	t.Run("a bad lease fails Task.Validate via delegation", func(t *testing.T) {
		ta := Task{ID: ID("g"), Status: StatusTodo, Claim: Claim{ttlSecs: 900}} // partial: ttl only
		err := ta.Validate()
		if ie, ok := errAsInvariant(err); !ok || ie.Entity != "claim lease" {
			t.Errorf("Validate() = %v (%T), want *InvariantError entity claim lease", err, err)
		}
	})
}

// --- AcceptanceCriterion: check-off invariant ---------------------------------

func TestAcceptanceCriterionValidate(t *testing.T) {
	when := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	checker := Actor{Type: ActorHuman, Name: "reviewer"}

	mustFail := func(name string, ac AcceptanceCriterion) {
		t.Run(name, func(t *testing.T) {
			err := ac.Validate()
			ie, ok := errAsInvariant(err)
			if !ok {
				t.Fatalf("Validate() = %v (%T), want *InvariantError", err, err)
			}
			if ie.Entity != "acceptance criterion" {
				t.Errorf("Entity = %q, want acceptance criterion", ie.Entity)
			}
		})
	}
	mustPass := func(name string, ac AcceptanceCriterion) {
		t.Run(name, func(t *testing.T) {
			if err := ac.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}

	base := func() AcceptanceCriterion {
		return AcceptanceCriterion{ID: ID("ac"), TaskID: ID("tk"), Seq: 2, Text: "verify the build"}
	}

	mustFail("checked with neither by nor at", func() AcceptanceCriterion { ac := base(); ac.Done = true; return ac }())
	mustFail("checked with at but no by", func() AcceptanceCriterion { ac := base(); ac.Done = true; ac.CompletedAt = &when; return ac }())
	mustFail("checked with by but no at", func() AcceptanceCriterion { ac := base(); ac.Done = true; ac.CompletedBy = &checker; return ac }())
	mustPass("checked with both", func() AcceptanceCriterion {
		ac := base()
		ac.Done = true
		ac.CompletedBy = &checker
		ac.CompletedAt = &when
		return ac
	}())
	mustFail("unchecked still carrying by", func() AcceptanceCriterion { ac := base(); ac.CompletedBy = &checker; return ac }())
	mustFail("unchecked still carrying at", func() AcceptanceCriterion { ac := base(); ac.CompletedAt = &when; return ac }())
	mustPass("unchecked and clean", base())
}

// --- Wire format: byte-identity pin -------------------------------------------

// TestTaskJSONWireFormat pins the flat REST wire form of a Task. These exact
// key names and values are consumed by the apiclient, MCP tooling, and E2E
// suite; any drift breaks them. An unclaimed task omits all four lease keys;
// a claimed task flattens them at the task level.
func TestTaskJSONWireFormat(t *testing.T) {
	created := time.Date(2026, 10, 1, 7, 15, 30, 0, time.UTC)
	updated := created.Add(40 * time.Minute)

	t.Run("unclaimed task omits the lease keys", func(t *testing.T) {
		ta := Task{
			ID: ID("task-1"), ProjectID: ID("proj-9"), Title: "Ship it", Status: StatusTodo, Priority: 3,
			Version: 2, CreatedAt: created, UpdatedAt: updated, Notes: "resume here",
		}
		got, err := json.Marshal(ta)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		for _, key := range []string{`"claimed_by"`, `"claimed_at"`, `"claim_expires_at"`, `"claim_ttl_seconds"`} {
			if strings.Contains(string(got), key) {
				t.Errorf("unclaimed task JSON carries %s: %s", key, got)
			}
		}
		var probe map[string]any
		if err := json.Unmarshal(got, &probe); err != nil {
			t.Fatalf("round-trip parse: %v", err)
		}
		if _, present := probe["claimed_by"]; present {
			t.Errorf("decode sees claimed_by on an unclaimed task: %s", got)
		}
	})

	t.Run("claimed task flattens the lease at task level", func(t *testing.T) {
		holder := Actor{Type: ActorAgent, Name: "worker-7"}
		at := created.Add(time.Hour)
		exp := at.Add(900 * time.Second)
		ta := Task{
			ID: ID("task-2"), ProjectID: ID("proj-9"), Title: "Claimed", Status: StatusDoing, Priority: 1,
			Version: 5, CreatedAt: created, UpdatedAt: updated, Claim: NewClaim(holder, at, 900),
		}
		got, err := json.Marshal(ta)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var probe struct {
			ID             string     `json:"id"`
			Status         string     `json:"status"`
			ClaimedBy      *actorWire `json:"claimed_by"`
			ClaimedAt      *string    `json:"claimed_at"`
			ClaimExpiresAt *string    `json:"claim_expires_at"`
			ClaimTTL       int        `json:"claim_ttl_seconds"`
		}
		if err := json.Unmarshal(got, &probe); err != nil {
			t.Fatalf("round-trip parse: %v", err)
		}
		if probe.ClaimedBy == nil || probe.ClaimedBy.Type != "agent" || probe.ClaimedBy.Name != "worker-7" {
			t.Errorf("claimed_by = %+v, want agent worker-7", probe.ClaimedBy)
		}
		if probe.ClaimedAt == nil || *probe.ClaimedAt != rfc3339TS(at) {
			t.Errorf("claimed_at = %v, want %s", probe.ClaimedAt, rfc3339TS(at))
		}
		if probe.ClaimExpiresAt == nil || *probe.ClaimExpiresAt != rfc3339TS(exp) {
			t.Errorf("claim_expires_at = %v, want %s", probe.ClaimExpiresAt, rfc3339TS(exp))
		}
		if probe.ClaimTTL != 900 {
			t.Errorf("claim_ttl_seconds = %d, want 900", probe.ClaimTTL)
		}

		// Round-trip: parse back into a Task — it must validate and re-marshal
		// byte-identically.
		var back Task
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("UnmarshalJSON: %v", err)
		}
		if err := back.Validate(); err != nil {
			t.Errorf("round-tripped task fails Validate: %v", err)
		}
		if !back.Claim.Present() || back.Claim.By().Name != "worker-7" || !back.Claim.Expiry().Equal(exp) || back.Claim.TTLSeconds() != 900 {
			t.Errorf("round-tripped lease wrong: present=%v by=%+v exp=%v ttl=%d",
				back.Claim.Present(), back.Claim.By(), back.Claim.Expiry(), back.Claim.TTLSeconds())
		}
		again, err := json.Marshal(back)
		if err != nil {
			t.Fatalf("re-Marshal: %v", err)
		}
		if string(again) != string(got) {
			t.Errorf("re-marshal differs from original:\n got  %s\n want %s", again, got)
		}
	})

	t.Run("done task carries completed_at and validates on round-trip", func(t *testing.T) {
		done := time.Date(2026, 10, 1, 8, 15, 30, 0, time.UTC) // matches the wire literal below
		raw := `{"id":"task-3","title":"Finished","status":"done","priority":0,` +
			`"version":9,"created_at":"2026-10-01T07:15:30Z","updated_at":"2026-10-01T07:55:30Z",` +
			`"project_id":"proj-9","description":"","notes":"","archived":false,` +
			`"completed_at":"2026-10-01T08:15:30Z"}`
		var ta Task
		if err := json.Unmarshal([]byte(raw), &ta); err != nil {
			t.Fatalf("UnmarshalJSON: %v", err)
		}
		if !ta.Done() || ta.CompletedAt == nil || !ta.CompletedAt.Equal(done) {
			t.Errorf("round-trip done task = (done %v completed_at %v)", ta.Done(), ta.CompletedAt)
		}
	})
}

// TestTaskJSONRejectsCorruptRows pins the fail-closed unmarshal behavior: a
// partial lease in JSON or a desynchronized completion stamp is rejected, so a
// successful Unmarshal always yields a task that passes Validate.
func TestTaskJSONRejectsCorruptRows(t *testing.T) {
	base := `"id":"task-4","title":"Broken","status":"doing","priority":0,"version":1,` +
		`"created_at":"2026-10-01T07:15:30Z","updated_at":"2026-10-01T07:15:30Z",` +
		`"project_id":"proj-9","description":"","notes":"","archived":false`

	cases := []struct {
		name string
		json string
	}{
		{"holder without expiry or grant", `{` + base + `,"claimed_by":{"type":"agent","name":"w"}}`},
		{"grant and expiry, no holder", `{` + base + `,"claimed_at":"2026-10-01T08:00:00Z","claim_expires_at":"2026-10-01T09:00:00Z"}`},
		{"full lease with zero ttl", `{` + base + `,"claimed_by":{"type":"agent","name":"w"},"claimed_at":"2026-10-01T08:00:00Z","claim_expires_at":"2026-10-01T09:00:00Z"}`},
		{"done without completed_at", `{"id":"task-5","title":"Bad done","status":"done","priority":0,"version":1,` +
			`"created_at":"2026-10-01T07:15:30Z","updated_at":"2026-10-01T07:15:30Z",` +
			`"project_id":"proj-9","description":"","notes":"","archived":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ta Task
			err := json.Unmarshal([]byte(tc.json), &ta)
			if err == nil {
				t.Fatalf("UnmarshalJSON succeeded on corrupt row %s", tc.json)
			}
			var ie *InvariantError
			if !errors.As(err, &ie) {
				t.Errorf("err = %v (%T), want *InvariantError", err, err)
			}
		})
	}
}

// TestClaimJSONWireFormat pins Claim's standalone flat object form: null when
// unclaimed, the four flat keys when attached, and a partial object rejected.
func TestClaimJSONWireFormat(t *testing.T) {
	holder := Actor{Type: ActorAgent, Name: "worker-7"}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	t.Run("unclaimed marshals to null", func(t *testing.T) {
		got, err := json.Marshal(Claim{})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(got) != "null" {
			t.Errorf("Marshal(unclaimed) = %s, want null", got)
		}
	})

	t.Run("attached marshals to the flat object", func(t *testing.T) {
		c := NewClaim(holder, at, 300)
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var probe map[string]any
		if err := json.Unmarshal(got, &probe); err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, key := range []string{"claimed_by", "claimed_at", "claim_expires_at", "claim_ttl_seconds"} {
			if _, ok := probe[key]; !ok {
				t.Errorf("flat object missing %s: %s", key, got)
			}
		}
	})

	t.Run("null unmarshals to the zero value", func(t *testing.T) {
		var c Claim
		if err := json.Unmarshal([]byte("null"), &c); err != nil {
			t.Fatalf("Unmarshal(null): %v", err)
		}
		if c.Present() {
			t.Errorf("unclaimed claim became present: %+v", c)
		}
	})

	t.Run("partial object is rejected", func(t *testing.T) {
		var c Claim
		err := json.Unmarshal([]byte(`{"claimed_by":{"type":"agent","name":"w"},"claim_ttl_seconds":60}`), &c)
		if err == nil {
			t.Fatalf("Unmarshal(partial) succeeded: %+v", c)
		}
		var ie *InvariantError
		if !errors.As(err, &ie) {
			t.Errorf("err = %v (%T), want *InvariantError", err, err)
		}
	})
}
