package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claim is a task's EPHEMERAL claim lease, modeled as one all-or-nothing value:
// the holder plus the grant instant, TTL, and expiry. The zero value is the
// unclaimed state — there is no such thing as a partial lease (a holder without
// an expiry, or vice versa, cannot exist). The only sanctioned ways to attach
// a lease are NewClaim (fresh period: expiry derived as at+ttl) and
// ClaimFromStored (rebuild from persisted columns); the only way to remove one
// is Released(), which returns the zero value.
//
// Wire format is sacred: the REST summary/detail rows carry FLAT keys
// claimed_by / claimed_at / claim_expires_at / claim_ttl_seconds, consumed by
// the apiclient, MCP tooling, and the E2E suite. Claim's MarshalJSON/
// UnmarshalJSON render exactly those flat keys (null when unclaimed), and
// Task.MarshalJSON flattens them into the task row — see task.go.
type Claim struct {
	by        *Actor    // nil = unclaimed
	at        time.Time // grant instant; zero when unclaimed
	expiresAt time.Time // expiry; zero when unclaimed
	ttlSecs   int       // TTL stored at grant; 0 when unclaimed (heartbeats reuse it)
}

// NewClaim mints a fresh lease period for by starting at at with the given
// TTL in seconds: the expiry is derived as at + ttl, exactly as a claim CAS
// re-stamps the row. A non-positive TTL produces a lease that fails Validate
// (callers mint only after the use case has normalized the TTL). The actor is
// copied — callers' inputs are never aliased.
func NewClaim(by Actor, at time.Time, ttlSeconds int) Claim {
	b := by
	return Claim{by: &b, at: at, expiresAt: at.Add(time.Duration(ttlSeconds) * time.Second), ttlSecs: ttlSeconds}
}

// ClaimFromStored reconstructs a lease from its four stored projections — the
// inverse of the adapter's column layout. A heartbeat re-stamps only the
// expiry, so the stored expiry is NOT necessarily at+ttl; pass all three
// values through as given. All-or-nothing: when ANY projection is nil (or the
// TTL is non-positive) the lease is incomplete and the zero value is returned
// — a partial lease cannot be constructed. Adapters call this on every read so
// a corrupt row degrades to "unclaimed" instead of smuggling a half-lease
// into the domain.
func ClaimFromStored(by *Actor, at, expiresAt *time.Time, ttlSeconds *int) Claim {
	if by == nil || at == nil || expiresAt == nil || ttlSeconds == nil || *ttlSeconds <= 0 {
		return Claim{}
	}
	b := *by
	a := *at
	e := *expiresAt
	return Claim{by: &b, at: a, expiresAt: e, ttlSecs: *ttlSeconds}
}

// Present reports whether a lease row is attached — live or stale ("zombie").
// Pure presence; it never consults the clock (domain entities have no time
// source). On an all-or-nothing value this is equivalent to by != nil.
func (c Claim) Present() bool { return c.by != nil }

// Released returns the zero value: the lease cleared and the task unclaimed.
func (c Claim) Released() Claim { return Claim{} }

// ExtendedTo returns a copy of the lease with ONLY the expiry re-stamped to
// newExpiry — the heartbeat's exact effect on a stored row (holder, grant
// instant, and TTL are kept). Extending an unclaimed lease is a no-op that
// returns the zero value.
func (c Claim) ExtendedTo(newExpiry time.Time) Claim {
	if !c.Present() {
		return Claim{}
	}
	c.expiresAt = newExpiry
	return c
}

// By returns the lease holder, or nil when the task is unclaimed.
func (c Claim) By() *Actor { return c.by }

// At returns the grant instant (zero time when unclaimed).
func (c Claim) At() time.Time { return c.at }

// Expiry returns the expiry instant (zero time when unclaimed).
func (c Claim) Expiry() time.Time { return c.expiresAt }

// TTLSeconds returns the TTL stored at grant time (0 when unclaimed;
// heartbeats reuse it).
func (c Claim) TTLSeconds() int { return c.ttlSecs }

// Active reports whether the lease is currently held: a lease row is present
// AND now is strictly before the expiry, so at exactly the expiry instant the
// lease is already over. `now` comes from the caller's application-layer clock
// — domain entities must never call time.Now().
func (c Claim) Active(now time.Time) bool { return c.Present() && now.Before(c.expiresAt) }

// Validate enforces the all-or-nothing invariant: either the lease is fully
// absent (the zero value — always valid) or fully present with a positive TTL.
// Any partial combination (holder without expiry, expiry without holder,
// missing grant instant, ttl <= 0 on an attached lease) is rejected with an
// *InvariantError naming the pieces that are set and the ones that are missing.
func (c Claim) Validate() error {
	bySet := c.by != nil
	atSet := !c.at.IsZero()
	expSet := !c.expiresAt.IsZero()
	ttlSet := c.ttlSecs != 0

	n := 0
	for _, set := range []bool{bySet, atSet, expSet, ttlSet} {
		if set {
			n++
		}
	}
	switch n {
	case 0: // unclaimed — the zero value is always valid
		return nil
	case 4:
		if c.ttlSecs <= 0 {
			return &InvariantError{Entity: "claim lease",
				Detail: fmt.Sprintf("ttl of %d seconds must be > 0 on an attached lease", c.ttlSecs)}
		}
		return nil
	default:
		var set, missing []string
		if bySet {
			set = append(set, "holder")
		} else {
			missing = append(missing, "holder")
		}
		if atSet {
			set = append(set, "claimed_at")
		} else {
			missing = append(missing, "claimed_at")
		}
		if expSet {
			set = append(set, "claim_expires_at")
		} else {
			missing = append(missing, "claim_expires_at")
		}
		if ttlSet {
			set = append(set, "ttl")
		} else {
			missing = append(missing, "ttl")
		}
		return &InvariantError{Entity: "claim lease",
			Detail: fmt.Sprintf("partial lease: %s set but %s missing — a lease is all-or-nothing",
				strings.Join(set, ", "), strings.Join(missing, ", "))}
	}
}

// claimWire is the flat wire form of an attached lease — the exact key names
// the REST rows have always used. Unclaimed claims marshal to JSON null (see
// MarshalJSON); inside a task row the keys are simply omitted (Task's omitempty
// semantics) rather than emitted as nulls.
type claimWire struct {
	ClaimedBy       actorWire `json:"claimed_by"`
	ClaimedAt       string    `json:"claimed_at"`
	ClaimExpiresAt  string    `json:"claim_expires_at"`
	ClaimTTLSeconds int       `json:"claim_ttl_seconds"`
}

// MarshalJSON renders the lease as its flat wire object, or null when the task
// is unclaimed. The key names and values are byte-identical to the pre-
// encapsulation REST wire format (guarded by TestClaimJSONWireFormat).
func (c Claim) MarshalJSON() ([]byte, error) {
	if !c.Present() {
		return []byte("null"), nil
	}
	w := claimWire{
		ClaimedBy:       actorWire{Type: string(c.by.Type), Name: c.by.Name},
		ClaimedAt:       rfc3339TS(c.at),
		ClaimExpiresAt:  rfc3339TS(c.expiresAt),
		ClaimTTLSeconds: c.ttlSecs,
	}
	return json.Marshal(w)
}

// UnmarshalJSON accepts null (→ the zero/unclaimed value) or the full flat
// object. A partial object — any of claimed_by / claimed_at / claim_expires_at
// missing, or a non-positive ttl — is rejected: a partial lease cannot be
// constructed through the wire either.
func (c *Claim) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		*c = Claim{}
		return nil
	}
	var w claimWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.ClaimedBy.Type == "" && w.ClaimedBy.Name == "" || w.ClaimedAt == "" || w.ClaimExpiresAt == "" {
		return &InvariantError{Entity: "claim lease",
			Detail: fmt.Sprintf("partial lease in JSON: claimed_by=%+v claimed_at=%q claim_expires_at=%q ttl=%d — a lease is all-or-nothing",
				w.ClaimedBy, w.ClaimedAt, w.ClaimExpiresAt, w.ClaimTTLSeconds)}
	}
	at, err := time.Parse(time.RFC3339, w.ClaimedAt)
	if err != nil {
		return fmt.Errorf("parse claimed_at %q: %w", w.ClaimedAt, err)
	}
	exp, err := time.Parse(time.RFC3339, w.ClaimExpiresAt)
	if err != nil {
		return fmt.Errorf("parse claim_expires_at %q: %w", w.ClaimExpiresAt, err)
	}
	ttl := w.ClaimTTLSeconds
	*c = ClaimFromStored(&Actor{Type: ActorType(w.ClaimedBy.Type), Name: w.ClaimedBy.Name}, &at, &exp, &ttl)
	if !c.Present() { // ClaimFromStored rejected a non-positive ttl
		return &InvariantError{Entity: "claim lease",
			Detail: fmt.Sprintf("ttl of %d seconds must be > 0 on an attached lease", ttl)}
	}
	return nil
}
