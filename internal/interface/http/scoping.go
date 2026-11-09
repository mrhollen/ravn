package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// scoping.go holds the multi-user epic T4 REST gates: every decision point that
// turns T3's resolved request Principal + T2's domain policies into allow/deny.
//
// Regression safety (epic AC): in the DEFAULT configuration (Scoping unset or
// global, i.e. !MembershipScoped) every helper below short-circuits to "allow /
// unscoped" WITHOUT touching the identity repository — zero extra queries, zero
// changed bytes on any response. Restrictions appear only once RAVN_SCOPING=membership
// is set explicitly.
//
// Two request-level facts drive everything:
//
//   - The SHARED-TOKEN OPERATOR (Principal.IsOperator) always sees and may do
//     everything, in every scoping mode — it is the deployment's administrative
//     backdoor and its behavior must not change when membership scoping turns on.
//   - A CREDENTIAL principal (session / api_token) carries its resolved Subject;
//     gates evaluate that subject's ownership/membership per project. With no
//     principal at all (RAVN_AUTH=off) or an unresolved/unknown name, membership
//     scoping is FAIL-CLOSED: the request sees nothing and may do nothing — the
//     shared-token path is the only way in there.

// requester is the per-request identity fact bundle computed once by App.requester.
type requester struct {
	operator bool         // shared-token principal: bypasses every scoping decision
	subject  domain.Subject // resolved durable identity (credential principals); zero/unknown for operators & off mode
	name     string         // authoritative acting name (spoof-proofed; declared-actor fallback in token/off modes)
}

// requester resolves the request's acting identity: the T3 principal when present,
// else the raw declared actor (off-mode behavior). declaredName is the handler's
// already-extracted declared actor name (body > X-Actor-Name > ?actor_name/actor).
func (a *App) requester(ctx context.Context, declaredName string) requester {
	req := requester{name: strings.TrimSpace(declaredName)}
	p, ok := application.PrincipalFromContext(ctx)
	if !ok { // auth off / non-gated path: anonymous, attributed to whatever was declared
		return req
	}
	req.operator = p.IsOperator
	fallback := domain.Actor{Name: declaredName}
	req.name = strings.TrimSpace(p.AuthoritativeActor(fallback).Name)
	req.subject = p.Subject
	return req
}

// scopingActive reports whether membership gating applies to this deployment.
func (a *App) scopingActive() bool { return a.Scoping != nil && a.Scoping.MembershipScoped }

// authoritativeActor resolves the ACTING actor for attribution AND gating (T3's
// spoof prevention, first wired in T4): a credential principal always acts as its
// OWN identity — a declared actor naming someone else is ignored — while operator
// and off-mode requests keep the declared actor byte-identically (the principal's
// Actor is zero there, so AuthoritativeActor returns the fallback unchanged). Every
// write handler resolves the acting actor through this before calling use cases,
// so attribution and authorization can never diverge about WHO is acting.
func (a *App) authoritativeActor(ctx context.Context, declared domain.Actor) domain.Actor {
	p, ok := application.PrincipalFromContext(ctx)
	if !ok || !p.Authenticated {
		return declared
	}
	return p.AuthoritativeActor(declared)
}

// visibilityPolicy / authorizationPolicy fold the nil→default rule ONCE per request so
// handlers never interpret the zero ScopingConfig directly (it is restrictive, not default).
func (a *App) visibilityPolicy() *domain.VisibilityPolicy {
	cfg := domain.DefaultScoping()
	if a.Scoping != nil {
		cfg = *a.Scoping
	}
	return domain.NewVisibilityPolicy(cfg)
}

func (a *App) authorizationPolicy() *domain.AuthorizationPolicy {
	cfg := domain.DefaultScoping()
	if a.scopingActive() {
		cfg = *a.Scoping
	}
	return domain.NewAuthorizationPolicy(cfg)
}

// resolveProjectRole folds the acting requester into its effective role in p:
// ResolveInProject does the owner/membership fold against the repository. Under
// membership scoping with NO identity resolver wired (a composition bug), or an
// empty acting name, the result is fail-closed RoleNone. In default mode callers
// never reach this — gate helpers short-circuit first.
func (a *App) resolveProjectRole(ctx context.Context, req requester, p domain.Project) (domain.Subject, domain.Role) {
	if a.Identity == nil || req.name == "" {
		return req.subject, domain.RoleNone
	}
	subj, role, err := a.Identity.ResolveInProject(ctx, req.name, p.ID, p.CreatedBy)
	if err != nil { // I/O failure on the read path: fail closed rather than 500-ing every request
		return subj, domain.RoleNone
	}
	return subj, role
}

// projectAccess answers (visible, subject, role) for one already-fetched project.
// Default mode and operators get (true, subj, RoleOwner): see-all plus owner passes
// every later authorization check without a second lookup. Membership mode resolves
// the real subject+role ONCE (callers reuse it for authorize — no double query) and
// asks the visibility policy.
func (a *App) projectAccess(ctx context.Context, req requester, p domain.Project) (bool, domain.Subject, domain.Role) {
	if !a.scopingActive() || req.operator {
		return true, req.subject, domain.RoleOwner
	}
	subj, role := a.resolveProjectRole(ctx, req, p)
	return a.visibilityPolicy().CanSeeProject(subj, role), subj, role
}

// authorize answers whether the requester may perform op while holding role in the
// target project. Default mode and operators pass unconditionally; membership mode
// asks the authorization policy with the resolved subject (pure — no I/O here).
func (a *App) authorize(req requester, subj domain.Subject, role domain.Role, op domain.Operation) bool {
	if !a.scopingActive() || req.operator {
		return true
	}
	return a.authorizationPolicy().Can(subj, role, op)
}

// requiredRoleName renders the minimum role an operation demands in membership mode —
// it mirrors domain's private minRole (read→viewer, write→editor, destructive→admin).
func requiredRoleName(op domain.Operation) string {
	switch op {
	case domain.OpRead:
		return "viewer"
	case domain.OpWrite:
		return "editor"
	default: // OpDestructive
		return "admin"
	}
}

// writeForbiddenRole renders the 403 envelope for a scoping denial:
//   {"error":"forbidden","resource":<type>,"required":"viewer|editor|admin","message":...}
// The "required" token is machine-readable (the web UI can render upgrade hints from
// it); the message is the agent/human-actionable form. Both always present — matching
// every other error envelope in this adapter.
func writeForbiddenRole(w http.ResponseWriter, op domain.Operation, resource string) {
	writeJSONError(w, http.StatusForbidden, map[string]any{
		"error":    ErrCodeForbidden,
		"resource": resource,
		"required": requiredRoleName(op),
		"message": fmt.Sprintf(
			"this %s operation requires at least the %q role in this %s (you hold less or no relationship to it)",
			op, requiredRoleName(op), resource),
	})
}

// checkProject applies the visibility + authorization gates to an ALREADY-FETCHED
// project row — for handlers whose pre-T4 flow already performs the fetch (get/board/
// update). Default mode and operators return (subject, RoleOwner, true) WITHOUT any
// additional lookup, so a default deployment's query pattern is byte-identical to
// pre-T4. Membership mode resolves subject+role ONCE (callers reuse it — no double
// query) and writes 403 (forbidden/required) on denial.
func (a *App) checkProject(w http.ResponseWriter, r *http.Request, declaredName string, p domain.Project, op domain.Operation) (domain.Subject, domain.Role, bool) {
	ctx := r.Context()
	req := a.requester(ctx, declaredName)
	if !a.scopingActive() || req.operator {
		return req.subject, domain.RoleOwner, true
	}
	subj, role := a.resolveProjectRole(ctx, req, p)
	if !a.visibilityPolicy().CanSeeProject(subj, role) {
		writeForbiddenRole(w, domain.OpRead, "project")
		return subj, role, false
	}
	if !a.authorize(req, subj, role, op) {
		writeForbiddenRole(w, op, "project")
		return subj, role, false
	}
	return subj, role, true
}

// gateProject is the fetch-then-check composition for handlers whose pre-T4 flow did
// NOT already fetch the project (create-task, report, collab writes...). In default
// mode it short-circuits with ZERO queries — unknown ids still 404 exactly as before,
// via the use case's own error path. Membership mode fetches first so an invisible or
// under-privileged request is rejected BEFORE any write-side validation runs.
func (a *App) gateProject(w http.ResponseWriter, r *http.Request, declaredName string, pid domain.ID, op domain.Operation) (domain.Project, bool) {
	if !a.scopingActive() {
		var p domain.Project // no fetch in default mode: the use case keeps its pre-T4 404 behavior
		return p, true
	}
	p, err := a.Projects.Get(r.Context(), pid)
	if err != nil {
		writeAppError(w, err, "project", pid) // 404 unknown project
		return p, false
	}
	_, _, ok := a.checkProject(w, r, declaredName, p, op)
	return p, ok
}

// checkTask gates an ALREADY-FETCHED task against its owning project (tasks have no
// independent roles — everything derives from the project relationship). Default mode
// and operators return true with zero lookups.
func (a *App) checkTask(w http.ResponseWriter, r *http.Request, declaredName string, t domain.Task, op domain.Operation) bool {
	ctx := r.Context()
	req := a.requester(ctx, declaredName)
	if !a.scopingActive() || req.operator {
		return true
	}
	p, err := a.Projects.Get(ctx, t.ProjectID)
	if err != nil { // parent vanished concurrently: surface it as the (now stale) project 404
		writeAppError(w, err, "project", t.ProjectID)
		return false
	}
	_, _, ok := a.checkProject(w, r, declaredName, p, op)
	return ok
}

// gateTask is the fetch-then-check task twin of gateProject: default mode short-
// circuits with ZERO queries (the use case keeps its pre-T4 404 behavior); membership
// mode fetches then gates against the owning project.
func (a *App) gateTask(w http.ResponseWriter, r *http.Request, declaredName string, tid domain.ID, op domain.Operation) (domain.Task, bool) {
	if !a.scopingActive() {
		var t domain.Task // no fetch in default mode: pre-T4 behavior unchanged
		return t, true
	}
	t, err := a.Tasks.Get(r.Context(), tid)
	if err != nil {
		writeAppError(w, err, "task", tid) // 404 unknown task
		return t, false
	}
	ok := a.checkTask(w, r, declaredName, t, op)
	return t, ok
}

// listScope returns the repository-level visibility predicate for a LIST request:
// nil = unscoped (default mode / operator — the query stays byte-identical to
// pre-T4), a non-nil scope with the subject's id, or a zero-id scope = deny-all
// (unresolved/anonymous under membership scoping sees an empty list).
func (a *App) listScope(ctx context.Context, declaredName string) *application.VisibilityScope {
	if !a.scopingActive() {
		return nil
	}
	req := a.requester(ctx, declaredName)
	if req.operator {
		return nil // operators keep see-all in every mode
	}
	if req.subject.Enabled() && req.subject.ID != "" {
		return &application.VisibilityScope{IdentityID: req.subject.ID}
	}
	z := domain.ID("") // deny-all: the SQL predicates match nothing for an unknown identity
	return &application.VisibilityScope{IdentityID: z}
}

// creatorFilterID resolves ?mine=created to a Creator filter value. Credential
// principals contribute their own subject id directly (no lookup). Otherwise the
// acting NAME is resolved through the identity repository — in token/off modes a
// free-form declared actor with no identity row simply has nothing they created
// (legacy rows are unattributed), which surfaces as an empty list via the zero-id
// Creator value. An error is returned only for malformed requests (no acting name
// at all, or scoping/mine requested without a resolver wired).
func (a *App) creatorFilterID(ctx context.Context, declaredName string) (*domain.ID, error) {
	req := a.requester(ctx, declaredName)
	if !req.operator && req.subject.Enabled() && req.subject.ID != "" {
		return &req.subject.ID, nil // credential principal: own identity, zero lookups
	}
	name := req.name
	if name == "" {
		var z domain.ID
		if a.scopingActive() {
			return &z, nil // anonymous under membership scoping: created nothing visible — not an error
		}
		return nil, fmt.Errorf("%w: ?mine=created requires an acting actor name", domain.ErrInvalid)
	}
	if a.Identity == nil {
		var z domain.ID
		return &z, nil // no resolver wired (shared-token deployment): nothing is ever attributed, so the honest answer is empty
	}
	subj, err := a.Identity.Resolve(ctx, name)
	if err != nil { // unknown or disabled name → no attribution possible
		var z domain.ID
		return &z, nil
	}
	id := subj.ID
	return &id, nil
}

// actorFilterName resolves ?mine=assigned / ?mine=activity to the acting NAME to
// filter stored rows by. Unlike creatorFilterID these filters match the free-form
// name columns persisted on tasks (assignee_name) and events (actor_name), so they
// work for legacy data in EVERY auth mode — a credential principal resolves to its
// own identity name, an operator/off-mode request to its declared actor. An empty
// result is "honest empty list", not an error: anonymous under membership scoping
// simply has nothing assigned to or done by them. The only hard error is an
// explicit ?mine= in token/off mode with no acting name at all (the caller cannot
// be filtered).
func (a *App) actorFilterName(ctx context.Context, declaredName string) (string, error) {
	req := a.requester(ctx, declaredName)
	if req.name == "" {
		if a.scopingActive() {
			return "", nil // anonymous under membership scoping: empty list, not an error
		}
		return "", fmt.Errorf("%w: ?mine=assigned/activity requires an acting actor name", domain.ErrInvalid)
	}
	return req.name, nil
}

// parseMine validates the ?mine= query parameter. Multiple distinct values are a
// 400 (ambiguous); empty/absent is "" (no mine filter). The caller restricts which
// tokens are allowed for its endpoint (tasks: assigned|created|activity; projects: created).
func parseMine(q map[string][]string) (string, error) {
	seen := make(map[string]bool)
	for _, v := range q["mine"] {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		seen[v] = true
	}
	if len(seen) > 1 { // distinct values are ambiguous; duplicates collapse (not an error)
		return "", fmt.Errorf("%w: ?mine takes at most one value", domain.ErrInvalid)
	}
	for k := range seen { // exactly one key when len==1
		return k, nil
	}
	return "", nil // absent/empty → no mine filter
}
