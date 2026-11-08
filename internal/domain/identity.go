package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuthMethod names HOW a principal authenticates to ravn. It is stored on the
// durable identity row and surfaced in resolved identities so authentication
// wiring (a later multi-user ticket) can branch on it; the domain only models
// the value, never the credential itself — credential_hash stays an
// infrastructure column so no secret ever crosses a domain boundary or reaches
// an adapter/event payload.
type AuthMethod string

const (
	AuthPassword    AuthMethod = "password"     // argon2id credential check
	AuthAPI         AuthMethod = "api_token"    // Bearer api_token secret
	AuthSharedToken AuthMethod = "shared_token" // the legacy single RAVN_TOKEN flow
	AuthNone        AuthMethod = "none"         // no credential (e.g. a backfilled legacy actor)
)

// Valid reports whether m is one of the known auth methods.
func (m AuthMethod) Valid() bool {
	switch m {
	case AuthPassword, AuthAPI, AuthSharedToken, AuthNone:
		return true
	default:
		return false
	}
}

// Identity is a DURABLE, named principal — the resolved form behind a
// self-declared actor name. Name is the unique upsert key (it was ravn's old
// free-form actor name); Kind and AuthMethod classify the principal; DisabledAt
// being non-nil means the identity is disabled (locked out) until re-enabled.
// It extends domain.Actor — which stays a pure (type,name) value untouched by
// this type — with a durable id plus auth classification, "used alongside" it
// per ADR 2026-09-26: Actor carries what a client ASSERTED; Identity is what the
// server RESOLVED that assertion to.
type Identity struct {
	ID           ID
	Name         string // stable unique key (was the free-form actor name)
	Kind         ActorType
	AuthMethod   AuthMethod
	DisabledAt   *time.Time // nil = enabled
	CreatedAt    time.Time
	LastActiveAt *time.Time
}

// Enabled reports whether the identity can authenticate/act: a disabled_at
// stamp locks it out (nil = enabled).
func (i Identity) Enabled() bool { return i.DisabledAt == nil }

// Validate enforces the identity's structural invariants. It runs at WRITE
// edges (identity registration / backfill verification); read paths keep
// accepting legacy rows stored before these checks. Errors wrap ErrInvalid.
func (i Identity) Validate() error {
	if i.ID.IsZero() {
		return fmt.Errorf("%w: identity id is required", ErrInvalid)
	}
	name := strings.TrimSpace(i.Name)
	if name == "" {
		return fmt.Errorf("%w: identity name is required", ErrInvalid)
	}
	if !i.Kind.Valid() {
		return fmt.Errorf("%w: unknown identity kind %q", ErrInvalid, i.Kind)
	}
	if !i.AuthMethod.Valid() {
		return fmt.Errorf("%w: unknown auth method %q", ErrInvalid, i.AuthMethod)
	}
	return nil
}

// ErrIdentityUnknown is returned by IdentityRepository.GetByName when NO
// identity row exists for the requested name. It is an OUTCOME, not a failure:
// policies reason about it (a global-mode caller may still see/act exactly as
// today; a membership-mode caller is locked out). It is deliberately distinct
// from ErrIdentityDisabled so callers can tell "never existed" from "existed but
// was disabled."
var ErrIdentityUnknown = errors.New("identity unknown")

// ErrIdentityDisabled is returned by IdentityRepository.GetByName when the name
// resolves to an identity that has been disabled (disabled_at set). Also an
// outcome — distinct from ErrIdentityUnknown. A disabled principal is locked out
// in EVERY mode, global or membership-scoped.
var ErrIdentityDisabled = errors.New("identity disabled")

// IdentityStatus records how a requested name resolved against the identity
// table. The three states are mutually exclusive and drive policy behavior.
type IdentityStatus int

const (
	// SubjectUnknown: no identity row for the name. In global mode such a subject
	// still sees/acts exactly as today; in membership mode it is locked out.
	SubjectUnknown IdentityStatus = iota
	// SubjectDisabled: an identity row exists but is disabled — locked out in
	// every mode (disabled principals cannot authenticate).
	SubjectDisabled
	// SubjectEnabled: a live identity row was found; the subject carries its
	// durable ID and may participate in membership/ownership decisions.
	SubjectEnabled
)

// String renders the status for logs and errors.
func (s IdentityStatus) String() string {
	switch s {
	case SubjectDisabled:
		return "disabled"
	case SubjectEnabled:
		return "enabled"
	default:
		return "unknown"
	}
}

// Subject is the RESOLVED form of an actor name — how it resolved against the
// identity table plus, when enabled, its durable ID. It travels ALONGSIDE
// domain.Actor (the self-declared display/audit principal): Actor carries the
// (type,name) a client asserted; Subject carries trust-relevant resolution state
// and the identity id that visibility/authorization policies reason about.
//
// The zero value is an UNRESOLVED subject (SubjectUnknown, empty name/id) — the
// shape a global-mode caller with no backing identity has. It must be accepted
// exactly as today: see ScopingConfig / the policy docs for the regression-safe
// guarantee that a nil/unknown/unresolved subject sees and acts as it does now.
type Subject struct {
	Name   string // the requested name (trimmed); "" when nothing was asserted
	Status IdentityStatus
	ID     ID // set when Status != SubjectUnknown
}

// Enabled reports whether s resolved to a live identity (Status == SubjectEnabled).
func (s Subject) Enabled() bool { return s.Status == SubjectEnabled }

// Unresolved reports whether s is the zero/unresolved form: nothing asserted or
// nothing found. Policies treat it like SubjectUnknown; the check is provided so
// callers can distinguish "no name came in at all" from a known-but-disabled one.
func (s Subject) Unresolved() bool { return s.Name == "" && !s.Enabled() }

// SubjectFromIdentity builds the resolved form of a FOUND identity, marking it
// enabled or disabled per its disabled_at stamp. Resolvers and tests use this so
// the enabled/disabled split cannot drift from Identity.Enabled.
func SubjectFromIdentity(i Identity) Subject {
	st := SubjectEnabled
	if !i.Enabled() {
		st = SubjectDisabled
	}
	return Subject{Name: i.Name, Status: st, ID: i.ID}
}

// UnknownSubject is the resolved form of a name with no identity row.
func UnknownSubject(name string) Subject { return Subject{Name: name, Status: SubjectUnknown} }
