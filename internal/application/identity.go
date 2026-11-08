package application

import (
	"context"
	"errors"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
)

// IdentityResolver resolves an actor name into its durable identity and folds the
// subject's role within a project into a form the visibility/authorization policies
// can reason about. It is the read-side seam the multi-user epic builds authorization
// on (epic T2). It performs NO writes, gates NOTHING itself, and holds no policy —
// it only produces (Subject, Role) facts. Authentication wiring and the actual
// REST/MCP gating that consumes these facts are later epic tickets; they call
// Resolve / ResolveInProject, then ask domain.NewVisibilityPolicy / NewAuthorizationPolicy.
type IdentityResolver struct {
	uow UnitOfWork
}

// NewIdentityResolver returns an IdentityResolver bound to uow (the same seam every
// use case uses; reads run through the unit of work for MVP uniformity). The resolver
// does not need a clock or idgen — it only reads and never mints.
func NewIdentityResolver(uow UnitOfWork) *IdentityResolver { return &IdentityResolver{uow: uow} }

// Resolve maps a name to its resolved Subject. name is trimmed before lookup (the
// identity table stores trimmed names, matching normalizeActor's write-time policy).
//
// It NEVER returns domain.ErrIdentityUnknown or domain.ErrIdentityDisabled — those
// outcomes are encoded in the returned Subject.Status so policies can reason about
// them — but it DOES surface unexpected I/O errors (e.g. a closed handle) as a non-nil
// error, so callers can tell a real failure from "not found." The returned error is
// nil in all three resolution outcomes (unknown/disabled/enabled).
func (r *IdentityResolver) Resolve(ctx context.Context, name string) (domain.Subject, error) {
	name = strings.TrimSpace(name)
	var repos Repos
	var subj domain.Subject
	var re error
	err := r.uow.Run(ctx, &repos, func() error {
		id, e := repos.Identities.GetByName(ctx, name)
		subj, re = resolveIdentityOutcome(name, id, e)
		return re // non-nil only for an unexpected I/O failure
	})
	if err != nil {
		return domain.Subject{}, err
	}
	return subj, nil
}

// ResolveInProject resolves name to its Subject AND folds it with the project's owner
// (createdBy) and stored membership into the subject's effective role there — the two
// facts a policy needs to gate one request. It is the single read path REST/MCP follow-
// ups use: get (subject, role), then ask visibility.CanSeeX(subject, role) and
// authorization.Can(subject, role, op).
//
// createdBy is the project's created_by identity id (*domain.ID); pass nil for legacy
// projects whose created_by is NULL — no subject can then be their Owner, which is why
// enabling membership on an existing deployment "goes dark" until owners/members are
// assigned. role is RoleNone for any non-enabled (unknown/disabled) subject, even if it
// was once the creator. The two port reads run in ONE unit of work (one transaction).
func (r *IdentityResolver) ResolveInProject(ctx context.Context, name string, projectID domain.ID, createdBy *domain.ID) (subject domain.Subject, role domain.Role, err error) {
	name = strings.TrimSpace(name)
	var repos Repos
	var subj domain.Subject
	var effRole domain.Role
	var re error
	runErr := r.uow.Run(ctx, &repos, func() error {
		id, e := repos.Identities.GetByName(ctx, name)
		subj, re = resolveIdentityOutcome(name, id, e)
		if re != nil || !subj.Enabled() { // unknown/disabled ⇒ no role; stop (no membership read)
			return re
		}
		member, me := repos.Identities.MemberRole(ctx, projectID, subj.ID)
		if me != nil {
			return me
		}
		effRole = domain.EffectiveRole(createdBy, member, subj)
		return nil
	})
	if runErr != nil {
		return domain.Subject{}, domain.RoleNone, runErr
	}
	subject, role = subj, effRole
	err = nil
	return subject, role, err
}

// GetByIds resolves identity ids to their rows in ONE unit of work for DISPLAY
// purposes (rendering the created_by/owner fields of list responses, multi-user
// epic T4). It is not a policy input — visibility/authorization resolve through
// Resolve / ResolveInProject — and it silently omits unknown or disabled ids
// (a legacy backfilled row that was later removed must degrade to unattributed,
// not error the response). Empty/duplicate inputs are handled: duplicates are
// collapsed before the query. Only unexpected I/O failures return a non-nil error.
func (r *IdentityResolver) GetByIds(ctx context.Context, ids []domain.ID) (map[domain.ID]domain.Identity, error) {
	seen := make(map[domain.ID]struct{}, len(ids))
	uniq := make([]domain.ID, 0, len(ids))
	for _, id := range ids { // skip the zero id: it is the NULL created_by sentinel, not an identity
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	out := make(map[domain.ID]domain.Identity, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	var repos Repos
	err := r.uow.Run(ctx, &repos, func() error {
		m, e := repos.Identities.GetByIds(ctx, uniq)
		if e != nil {
			return e
		}
		for k, v := range m {
			out[k] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveIdentityOutcome maps an IdentityRepository.GetByName result to a resolved
// Subject plus any PROPAGATED error. The two expected "outcome" sentinels are folded
// into the Subject (not returned as errors); anything else is returned verbatim so the
// resolver distinguishes a real I/O failure from "not found." Returns (subject, nil)
// for unknown/disabled/enabled alike.
func resolveIdentityOutcome(name string, id domain.Identity, err error) (domain.Subject, error) {
	switch {
	case errors.Is(err, domain.ErrIdentityUnknown):
		return domain.UnknownSubject(name), nil
	case errors.Is(err, domain.ErrIdentityDisabled):
		// Row exists but is disabled: the subject carries the disabled status (id set)
		// and this is an OUTCOME, not an I/O failure.
		return domain.SubjectFromIdentity(id), nil
	default:
		if err != nil {
			return domain.Subject{}, err // unexpected I/O failure — propagate
		}
		return domain.SubjectFromIdentity(id), nil // enabled (err == nil)
	}
}
