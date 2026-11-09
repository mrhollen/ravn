package domain

import (
	"fmt"
	"strings"
)

// scoping.go holds the config-driven VISIBILITY and AUTHORIZATION policies of the
// multi-user epic. Everything here is a PURE value — no I/O, no repositories, no
// clock: callers resolve a name to a Subject (identity repository) and fold it
// with ownership/membership into an effective Role, then ask these policies to
// decide. That keeps the domain testable in isolation and the policy layer free
// of storage, so REST/MCP follow-ups can consume it consistently.

// ScopingConfig drives both visibility and authorization decisions. It is the
// single value a deployment hands to NewVisibilityPolicy / NewAuthorizationPolicy.
// This type carries NO env parsing — wiring it from configuration is a later
// ticket's job; it only declares the two independent axes.
type ScopingConfig struct {
	// Global grants see-all PROJECT visibility (and, by inheritance, everything
	// under a project) to EVERY subject when true, regardless of membership.
	// Default true — today's single-tenant + shared-token behavior. When false,
	// visibility is scoped to explicit per-project membership: an enabled subject
	// must hold a real relationship (effective role other than RoleNone) to see it.
	Global bool

	// MembershipScoped, when true, enforces per-project ROLE GATING on operations:
	// only an enabled subject whose effective role meets the operation's minimum may
	// act (viewer reads; editor non-destructive writes; admin/owner destructive).
	// When false there are NO authorization restrictions — any subject, including a
	// nil/unknown/unresolved one, may perform any operation within scope. Default
	// false. This is the single switch that must be flipped to add real restrictions.
	MembershipScoped bool
}

// DefaultScoping returns the regression-safe configuration: global see-all
// visibility and NO membership gating. This is what existing single-tenant and
// shared-token deployments run in today, so policies built from it yield ZERO
// observable change — a nil/unknown/unresolved identity sees all and acts as it
// does now. The zero value of ScopingConfig is NOT this (zero = restrictive);
// always obtain the default through this constructor.
func DefaultScoping() ScopingConfig { return ScopingConfig{Global: true, MembershipScoped: false} }

// MembershipScoping returns the fully isolated configuration: per-project
// membership-scoped visibility AND role gating on operations. Enabling this on an
// existing deployment requires assigning owners/members to projects — legacy rows
// keep NULL created_by and no membership, so those projects go dark to newly
// authenticated users (they stay visible-but-unattributed only under the default).
func MembershipScoping() ScopingConfig { return ScopingConfig{Global: false, MembershipScoped: true} }

// Role is a subject's effective privilege WITHIN ONE project. Roles form a total
// order (None < Viewer < Editor < Admin < Owner) so policies compare with AtLeast.
// A stored membership row (project_member.role) holds only viewer/editor/admin;
// Owner is derived separately from the project's created_by via EffectiveRole and
// never appears in a membership row.
type Role int

const (
	RoleNone  Role = iota // no relationship to the project (non-member / unresolved subject)
	RoleViewer            // can read the project and its contents
	RoleEditor            // can perform non-destructive writes
	RoleAdmin             // can perform destructive/administrative operations
	RoleOwner             // the project's creator — supersedes every other role
)

// AtLeast reports whether r grants at least the privilege min.
func (r Role) AtLeast(min Role) bool { return r >= min }

// Valid reports whether r is one of the five defined roles.
func (r Role) Valid() bool { return r >= RoleNone && r <= RoleOwner }

// String renders the role for logs and errors.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleAdmin:
		return "admin"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// ParseRole parses a STORED membership-role string (the project_member.role column
// — one of viewer/editor/admin, or empty/none for no row) into its Role value. It is
// the inverse of Role.String for the storage-boundary direction: adapters map the
// database text through this so role vocabulary never leaks into infrastructure. An
// unrecognized value (including "owner", which never appears in a stored row —
// ownership is derived, not persisted) returns an error wrapping ErrInvalid so a
// corrupted or hand-edited row surfaces loudly rather than silently mapping to none.
func ParseRole(s string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return RoleNone, nil
	case "viewer":
		return RoleViewer, nil
	case "editor":
		return RoleEditor, nil
	case "admin":
		return RoleAdmin, nil
	default:
		return RoleNone, fmt.Errorf("%w: unknown stored role %q", ErrInvalid, s)
	}
}

// Storable reports whether r may be persisted in a membership row (the
// project_member.role column): viewer, editor or admin. RoleNone means "no row"
// and RoleOwner is derived from created_by — neither is ever written; the
// membership use case rejects both at its write boundary.
func (r Role) Storable() bool { return r >= RoleViewer && r <= RoleAdmin }

// CanAssignRole is the hierarchy guard for membership management (multi-user
// T9): may a manager whose effective role in the project is actor grant or set
// the storable role target? Rules:
//
//   - RoleOwner grants ANY storable role. The shared operator token is folded
//     into RoleOwner by the HTTP gate BEFORE this is called, so no separate
//     "operator" flag exists here — ownership and operator are one privilege tier.
//   - everyone else may only grant or set roles AT OR BELOW their own (an admin
//     may assign viewer/editor/admin; an editor may assign viewer/editor).
//
// Pure comparison, no I/O — the same shape as EffectiveRole. The caller is
// responsible for validating that target is Storable; this function says nothing
// about role None/Owner inputs beyond the total order.
func CanAssignRole(actor, target Role) bool {
	if actor.AtLeast(RoleOwner) {
		return true
	}
	return target <= actor
}

// EffectiveRole folds a project's stored membership role and its owner identity
// into the subject's effective role for that project. Rules, in order:
//
//  1. A non-enabled (unknown or disabled) subject has NO role — even if it was
//     once the creator. Disabling an identity revokes its access to what it owns.
//  2. The project owner (ownerID == s.ID) is RoleOwner, which supersedes any
//     stored membership row for that subject.
//  3. Otherwise the stored membership role applies as-is (it never exceeds Admin;
//     a nil/absent owner with no membership yields RoleNone).
//
// This is a PURE fold — it performs NO I/O. ownerID and member are read by the
// caller (the identity/membership repository) and passed in, so the policy layer
// stays free of storage. ownerID is nil for legacy projects (created_by is NULL),
// which means no subject can be their Owner until an owner is explicitly assigned.
func EffectiveRole(ownerID *ID, member Role, s Subject) Role {
	if !s.Enabled() || s.ID.IsZero() {
		return RoleNone
	}
	if ownerID != nil && *ownerID == s.ID {
		return RoleOwner
	}
	if member > RoleAdmin { // defensive: a membership row never stores Owner
		return RoleAdmin
	}
	return member
}

// Operation classifies an action by how much privilege it requires WITHIN a
// visible scope. The classification is static per kind; the AuthorizationPolicy
// decides whether a given (subject, role) may perform it under the active config.
type Operation int

const (
	OpRead        Operation = iota // view project/task/comment/artifact/event content
	OpWrite                        // non-destructive mutation (create/update/comment/assign/claim...)
	OpDestructive                  // destructive/administrative (delete, force-release, archive, member mgmt)
)

// String renders the operation for logs and errors.
func (o Operation) String() string {
	switch o {
	case OpRead:
		return "read"
	case OpWrite:
		return "write"
	case OpDestructive:
		return "destructive"
	default:
		return "unknown"
	}
}

// minRole returns the minimum effective role op requires WHEN membership scoping is
// ON. In the default (non-membership-scoped) config every operation is allowed for
// any subject and these minima are simply never consulted. An out-of-range op maps
// to RoleNone; because only the three defined operations exist, that is unreachable
// in practice — a new operation must add its own minimum here to be gated.
func (o Operation) minRole() Role {
	switch o {
	case OpRead:
		return RoleViewer
	case OpWrite:
		return RoleEditor
	case OpDestructive:
		return RoleAdmin // owner (a superset of admin) satisfies this too
	default:
		return RoleNone
	}
}

// VisibilityPolicy decides whether a subject can SEE ravn data, given the active
// ScopingConfig and the subject's effective role in the owning project. Pure value —
// it performs no I/O and holds no repository; callers resolve the subject (identity
// repository) and its role (membership/ownership), then ask.
//
// Inheritance: a task, and every comment/artifact/event under it, inherits the
// VISIBILITY of its parent project. Every "can see X" method therefore evaluates
// against the SAME rule as CanSeeProject using the subject's effective role in X's
// owning project. Separate entry points (rather than one generic) keep call sites at
// REST/MCP explicit and self-documenting about WHAT is being gated.
type VisibilityPolicy struct {
	cfg ScopingConfig
}

// NewVisibilityPolicy builds a policy from cfg. Pass DefaultScoping() for the
// regression-safe see-all behavior, or MembershipScoping() for full isolation.
func NewVisibilityPolicy(cfg ScopingConfig) *VisibilityPolicy { return &VisibilityPolicy{cfg: cfg} }

// CanSeeProject reports whether s can see a project given its effective role in it.
// A disabled subject is denied in EVERY config (disabled principals cannot authenticate).
// Otherwise, under Global (the default) every remaining subject — enabled OR unknown/
// unresolved — sees everything (today's behavior). Under membership scoping the subject
// must be enabled AND hold a real relationship to the project (effective role != none).
func (p *VisibilityPolicy) CanSeeProject(s Subject, role Role) bool {
	if s.Status == SubjectDisabled {
		return false // locked out in every mode, global included
	}
	if p.cfg.Global {
		return true // see-all default: enabled + unknown/unresolved subjects all see, as today
	}
	return s.Enabled() && role != RoleNone
}

// CanSeeTask reports whether s can see a task. A task is visible iff its PARENT
// PROJECT is visible; role is the subject's effective role in that project.
func (p *VisibilityPolicy) CanSeeTask(s Subject, role Role) bool { return p.CanSeeProject(s, role) }

// CanSeeComment reports whether s can see a comment — it inherits the visibility of
// the parent task (hence its owning project); role is the subject's effective role in
// that project.
func (p *VisibilityPolicy) CanSeeComment(s Subject, role Role) bool { return p.CanSeeProject(s, role) }

// CanSeeArtifact reports whether s can see an artifact — it inherits the visibility of
// the parent task (hence its owning project); role is the subject's effective role in
// that project.
func (p *VisibilityPolicy) CanSeeArtifact(s Subject, role Role) bool { return p.CanSeeProject(s, role) }

// CanSeeEvent reports whether s can see an event — it inherits the visibility of the
// parent task/project; role is the subject's effective role in that project.
func (p *VisibilityPolicy) CanSeeEvent(s Subject, role Role) bool { return p.CanSeeProject(s, role) }

// AuthorizationPolicy decides whether a subject MAY PERFORM an operation within a
// scope it can see, under the active ScopingConfig. Pure value, like VisibilityPolicy.
//
// Regression safety: when MembershipScoped is false (the default), EVERY operation is
// allowed for ANY subject — including nil/unknown/unresolved ones — so existing
// single-tenant + shared-token deployments observe zero change. Restrictions appear ONLY
// once membership scoping is explicitly enabled.
type AuthorizationPolicy struct {
	cfg ScopingConfig
}

// NewAuthorizationPolicy builds a policy from cfg (same ScopingConfig value the
// visibility policy uses, so both axes are driven by one deployment decision).
func NewAuthorizationPolicy(cfg ScopingConfig) *AuthorizationPolicy { return &AuthorizationPolicy{cfg: cfg} }

// Can reports whether s, holding effective role `role` in the target's project, may
// perform op under this policy's config. A disabled subject is denied in EVERY config
// (it cannot authenticate, so gating it is non-negotiable and mode-independent).
//
//   - !MembershipScoped → true for every remaining (s, role, op): no gating, today's
//     behavior — enabled AND unknown/unresolved subjects all act as they do now.
//   - MembershipScoped  → s must be enabled AND its role meet op's minimum: reads need
//     at least viewer, non-destructive writes at least editor, destructive at least
//     admin — and the owner (created_by), being a superset of admin, passes all three.
func (p *AuthorizationPolicy) Can(s Subject, role Role, op Operation) bool {
	if s.Status == SubjectDisabled {
		return false // locked out in every mode, default included
	}
	if !p.cfg.MembershipScoped {
		return true // default: no gating — enabled + unknown/unresolved subjects act as today
	}
	return s.Enabled() && role.AtLeast(op.minRole())
}
