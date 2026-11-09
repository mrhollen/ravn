package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
)

// Memberships manages a project's stored membership rows (multi-user T9): it is the
// ONLY writer of project_member in the codebase, and the single place the two
// management guards live — the hierarchy guard (you may only assign roles at or below
// your own; owner/operator excepted) and the last-admin guard (a change must never
// leave an ownerless project with no stored admin). It holds NO HTTP concerns: names
// resolve through the identity port, role inputs are already Storable-validated at the
// boundary before this runs for free-form strings, and actorRole is the actor's
// EFFECTIVE role in the target project as computed by the gate — the shared operator
// token arrives here as RoleOwner, which is what exempts it from both guards.
// Disabling an identity never auto-removes its rows; removal is explicit (Remove).
type Memberships struct {
	uow UnitOfWork
}

// NewMemberships returns a Memberships use case bound to uow. Like the other use
// cases it takes no clock or idgen: membership rows carry no timestamps of their own.
func NewMemberships(uow UnitOfWork) *Memberships { return &Memberships{uow: uow} }

// List returns the project's stored membership rows in stable identity_id order. It is
// a raw view: disabled identities' rows are included (the adapter renders names; access
// filtering happened at the gate, before this runs). The project owner is NOT a stored
// row (ownership derives from created_by) and therefore never appears here — callers
// render the owner separately from p.CreatedBy.
func (m *Memberships) List(ctx context.Context, p domain.Project) ([]domain.ProjectMember, error) {
	var repos Repos
	var out []domain.ProjectMember
	err := m.uow.Run(ctx, &repos, func() error {
		rows, e := repos.Identities.ListMembers(ctx, p.ID)
		if e != nil {
			return e
		}
		out = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Grant upserts the stored membership of targetName in p with targetRole and reports
// whether a NEW row was created (POST semantics: 201 first time, 200 on re-grant).
// Guards, in order: targetName must resolve to an ENABLED identity — unknown names are
// refused (wrapped ErrIdentityUnknown): membership does NOT auto-provision identities,
// provisioning is POST /auth/accounts; disabled identities are refused (wrapped
// ErrIdentityDisabled); the hierarchy guard may refuse (wrapped ErrCannotAssign); and
// losing a sole admin's status on an ownerless project is refused (wrapped
// ErrLastAdmin). The reads and the write run in ONE unit of work.
func (m *Memberships) Grant(ctx context.Context, p domain.Project, targetName string, targetRole, actorRole domain.Role) (domain.ProjectMember, bool, error) {
	if !targetRole.Storable() {
		return domain.ProjectMember{}, false, fmt.Errorf("%w: role %q is not assignable — storable roles are viewer/editor/admin", domain.ErrInvalid, targetRole.String())
	}
	name := strings.TrimSpace(targetName)
	if name == "" {
		return domain.ProjectMember{}, false, fmt.Errorf("%w: username is required", domain.ErrInvalid)
	}
	var repos Repos
	var member domain.ProjectMember
	var created bool
	err := m.uow.Run(ctx, &repos, func() error {
		subj, re := resolveTargetEnabled(ctx, name, &repos)
		if re != nil {
			return re
		}
		current, e := repos.Identities.MemberRole(ctx, p.ID, subj.ID)
		if e != nil {
			return e
		}
		re = assignmentGuard(ctx, p, subj, current, targetRole, actorRole, &repos)
		if re != nil {
			return re
		}
		created = current == domain.RoleNone
		member = domain.ProjectMember{ProjectID: p.ID, IdentityID: subj.ID, Role: targetRole}
		return repos.Identities.UpsertMember(ctx, p.ID, subj.ID, targetRole)
	})
	if err != nil {
		return domain.ProjectMember{}, false, err
	}
	return member, created, nil
}

// SetRole changes the stored role of an EXISTING member (PATCH semantics): unlike
// Grant it refuses a target with no membership row — wrapped ErrNotFound ("not a
// member" is an outcome distinct from "identity unknown") — and likewise rejects
// disabled targets. The same hierarchy and last-admin guards apply as in Grant, with
// "current" being the role about to be replaced (so admin→viewer demotions trip the
// last-admin check, while granting a fresh row at viewer never does).
func (m *Memberships) SetRole(ctx context.Context, p domain.Project, targetName string, newRole, actorRole domain.Role) (domain.ProjectMember, error) {
	if !newRole.Storable() {
		return domain.ProjectMember{}, fmt.Errorf("%w: role %q is not assignable — storable roles are viewer/editor/admin", domain.ErrInvalid, newRole.String())
	}
	name := strings.TrimSpace(targetName)
	if name == "" {
		return domain.ProjectMember{}, fmt.Errorf("%w: username is required", domain.ErrInvalid)
	}
	var repos Repos
	var member domain.ProjectMember
	err := m.uow.Run(ctx, &repos, func() error {
		subj, re := resolveTargetEnabled(ctx, name, &repos)
		if re != nil {
			return re
		}
		current, e := repos.Identities.MemberRole(ctx, p.ID, subj.ID)
		if e != nil {
			return e
		}
		if current == domain.RoleNone {
			return fmt.Errorf("%w: identity %q is not a member of this project", domain.ErrNotFound, name)
		}
		re = assignmentGuard(ctx, p, subj, current, newRole, actorRole, &repos)
		if re != nil {
			return re
		}
		member = domain.ProjectMember{ProjectID: p.ID, IdentityID: subj.ID, Role: newRole}
		return repos.Identities.UpsertMember(ctx, p.ID, subj.ID, newRole)
	})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return member, nil
}

// Remove deletes the stored membership row of targetName in p (DELETE semantics).
// Unlike Grant/SetRole it ALLOWS removing a disabled identity's row — that is how an
// admin tidies up someone who was later disabled (their access is already revoked by
// the disable; only the row lingers). Unknown targets are refused with wrapped
// ErrIdentityUnknown and non-members with wrapped ErrNotFound. The last-admin guard
// applies exactly as for a demotion: removing a sole stored admin on an ownerless
// project, by anyone but an owner/operator, is refused with wrapped ErrLastAdmin.
func (m *Memberships) Remove(ctx context.Context, p domain.Project, targetName string, actorRole domain.Role) error {
	name := strings.TrimSpace(targetName)
	if name == "" {
		return fmt.Errorf("%w: username is required", domain.ErrInvalid)
	}
	var repos Repos
	err := m.uow.Run(ctx, &repos, func() error {
		id, e := repos.Identities.GetByName(ctx, name)
		switch {
		case errors.Is(e, domain.ErrIdentityUnknown):
			return fmt.Errorf("%w: %q", domain.ErrIdentityUnknown, name)
		case e != nil && !errors.Is(e, domain.ErrIdentityDisabled):
			return e // unexpected I/O failure propagates; disabled is TOLERATED (see doc)
		}
		subj := domain.SubjectFromIdentity(id)
		current, me := repos.Identities.MemberRole(ctx, p.ID, subj.ID)
		if me != nil {
			return me
		}
		if current == domain.RoleNone {
			return fmt.Errorf("%w: identity %q is not a member of this project", domain.ErrNotFound, name)
		}
		if current == domain.RoleAdmin { // the only case where an admin status could be lost
			if re := lastAdminLoss(ctx, p, subj, actorRole, &repos); re != nil {
				return re
			}
		}
		return repos.Identities.RemoveMember(ctx, p.ID, subj.ID)
	})
	return err
}

// resolveTargetEnabled resolves name to an ENABLED subject or returns the wrapped
// outcome error. Shared by Grant/SetRole (both refuse disabled targets); Remove
// resolves inline because it tolerates them. Unknown and disabled are distinct
// sentinels so the REST adapter can answer 404-with-provisioning-hint vs 403.
func resolveTargetEnabled(ctx context.Context, name string, repos *Repos) (domain.Subject, error) {
	id, err := repos.Identities.GetByName(ctx, name)
	switch {
	case errors.Is(err, domain.ErrIdentityUnknown):
		return domain.Subject{}, fmt.Errorf("%w: %q", domain.ErrIdentityUnknown, name)
	case errors.Is(err, domain.ErrIdentityDisabled):
		return domain.SubjectFromIdentity(id), err
	default:
		if err != nil {
			return domain.Subject{}, err // unexpected I/O failure — propagate verbatim
		}
		return domain.SubjectFromIdentity(id), nil
	}
}

// assignmentGuard enforces both management guards for a pending write that sets
// subj's stored role to newRole (current is the role BEFORE the change — RoleNone for
// a fresh grant). Hierarchy first: an owner/operator (actorRole >= RoleOwner) may set
// any storable role; anyone else only at-or-below their own. Then last-admin: only a
// change that STRIPS stored-admin status (current admin → newRole below admin) can
// leave the project unmanageable, and only on ownerless projects does it actually.
func assignmentGuard(ctx context.Context, p domain.Project, subj domain.Subject, current, newRole, actorRole domain.Role, repos *Repos) error {
	if !domain.CanAssignRole(actorRole, newRole) {
		return fmt.Errorf("%w: acting role %q may only assign roles at or below its own; requested %q", domain.ErrCannotAssign, actorRole.String(), newRole.String())
	}
	if !(current == domain.RoleAdmin && newRole < domain.RoleAdmin) {
		return nil // no admin status lost: fresh grant, promotion, same-role re-set
	}
	return lastAdminLoss(ctx, p, subj, actorRole, repos)
}

// lastAdminLoss reports ErrLastAdmin when the caller's change (removal or demotion of
// a stored-admin row) would leave NO effective administrator behind. Called only for
// admin-status-losing changes. Exemptions, in order: owner/operator actors manage by
// design; projects WITH an owner stay manageable because ownership derives from
// created_by and no membership mutation can remove it; and finally any OTHER stored
// admin surviving the change is sufficient. (Known limitation, documented for T8: a
// DISABLED owner still counts here — reviving that edge requires re-enabling them.)
func lastAdminLoss(ctx context.Context, p domain.Project, subj domain.Subject, actorRole domain.Role, repos *Repos) error {
	if actorRole.AtLeast(domain.RoleOwner) {
		return nil
	}
	if p.CreatedBy != nil {
		return nil
	}
	members, err := repos.Identities.ListMembers(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, mem := range members {
		if mem.Role == domain.RoleAdmin && mem.IdentityID != subj.ID {
			return nil // another stored admin survives the change
		}
	}
	return fmt.Errorf("%w: %q holds the project's only stored-admin role and the project has no owner — promote another member to admin first, or act with the shared operator token", domain.ErrLastAdmin, subj.Name)
}
