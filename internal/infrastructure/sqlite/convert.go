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

// claimTTLOrNil renders the lease TTL column: present only while a lease is
// attached (mirrors the DDL — "TTL stored at claim; heartbeat reuses it").
func claimTTLOrNil(t *domain.Task) any {
	if t.ClaimedBy == nil || t.ClaimExpiry == nil {
		return nil
	}
	return int(t.ClaimTTLSeconds)
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
