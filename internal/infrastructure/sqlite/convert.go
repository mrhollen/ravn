package sqlite

import (
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// Timestamps cross the storage boundary as RFC3339 UTC strings
// (ARCHITECTURE §4 — trivially readable for agents, no float issues). All repo
// code formats and parses through these two helpers so neither direction can
// drift. Formatting uses RFC3339Nano so sub-second precision survives the
// round-trip losslessly (whole-second instants render exactly like the plain
// RFC3339 form); parsing accepts fractional seconds either way.
func formatTS(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

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
