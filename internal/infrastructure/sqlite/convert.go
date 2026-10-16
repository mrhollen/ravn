package sqlite

import (
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// TimestampLayout formats timestamps as fixed-width, zero-padded 9-digit UTC
// RFC3339 strings ("2006-01-02T15:04:05.000000000Z"). Fixed 30-character length
// guarantees that SQLite lexicographical text comparisons (<, <=, >, >=, ORDER BY,
// BETWEEN) exactly match chronological time comparison across whole-second and
// sub-second instants.
const TimestampLayout = "2006-01-02T15:04:05.000000000Z"

// Timestamps cross the storage boundary as fixed-width RFC3339 UTC strings
// (ARCHITECTURE §4 — trivially readable for agents, no float issues). All repo
// code formats and parses through these two helpers so neither direction can
// drift. Formatting uses TimestampLayout so sub-second precision survives the
// round-trip losslessly and sorts correctly in SQLite text comparisons; parsing
// uses RFC3339Nano so both fixed-width strings and legacy unpadded timestamps
// parse transparently.
func formatTS(t time.Time) string { return t.UTC().Format(TimestampLayout) }

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

// tsOrNil renders an optional timestamp as a SQL argument: NULL when the
// pointer is nil, RFC3339 UTC otherwise. The inverse of parseTS at scan sites.
func tsOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTS(*t)
}

// actorTypeOrNil / actorNameOrNil render the nullable (type, name) actor pair
// used for assignee and claim-holder columns: both halves go NULL together when
// the actor is absent.
func actorTypeOrNil(a *domain.Actor) any {
	if a == nil {
		return nil
	}
	return string(a.Type)
}

func actorNameOrNil(a *domain.Actor) any {
	if a == nil {
		return nil
	}
	return a.Name
}

// claimLeaseArgs renders the task row's five lease columns in schema order —
// (claimed_by_type, claimed_by_name, claimed_at, claim_expires_at,
// claim_ttl_sec) — all-or-nothing from the domain's Claim value: present while
// a lease is attached (any state, live or stale), all NULL together when the
// task is unclaimed. A partial claim cannot be rendered because the type does
// not admit one; this stays correct by construction rather than by check.
func claimLeaseArgs(c domain.Claim) [5]any {
	var out [5]any // all nils = unclaimed (the zero Claim)
	if !c.Present() {
		return out
	}
	out[0] = string(c.By().Type)
	out[1] = c.By().Name
	at := c.At()
	exp := c.Expiry()
	out[2] = formatTS(at)
	out[3] = formatTS(exp)
	out[4] = c.TTLSeconds()
	return out
}

// boolToInt renders a Go bool as the schema's INTEGER 0/1 form.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// actorOrNil renders a nullable (type, name) pair back into a *domain.Actor;
// NULL (either half missing) round-trips to nil.
func actorOrNil(typeS, nameS *string) *domain.Actor {
	if typeS == nil || nameS == nil {
		return nil
	}
	return &domain.Actor{Type: domain.ActorType(*typeS), Name: *nameS}
}
