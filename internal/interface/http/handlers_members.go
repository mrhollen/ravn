package httpapi

// handlers_members.go — membership management endpoints (multi-user epic T9):
//
//	GET    /api/v1/projects/{id}/members          list stored members + derived owner
//	POST   /api/v1/projects/{id}/members          add or re-grant a member {username, role}
//	PATCH  /api/v1/projects/{id}/members/{name}   set an existing member's role {role}
//	DELETE /api/v1/projects/{id}/members/{name}   remove the stored membership row
//
// Gate order per verb (identical shape, so every client sees a predictable ladder):
//
//  1. malformed path id                     → 400 invalid_input        (pathID)
//  2. RAVN_SCOPING not membership           → 403 reason=membership_scoping_disabled (writeMembersDisabled)
//  3. feature on but unwired (no DB)        → 404 "not enabled"        (writeMembersNotEnabled)
//  4. unknown project                       → 404 not_found            (manageMembers / gateProject)
//  5. visibility / admin-or-owner denial    → 403 forbidden            (checkProjectManager / checkProject)
//
// Gating: GET is at the read line (any visible member); POST/PATCH/DELETE require
// ADMIN-OR-OWNER (checkProjectManager). The shared operator token passes every gate
// and arrives at the use case as RoleOwner, which exempts it from both management
// guards (hierarchy + last-admin). Attribution is NOT re-declared: credential
// principals act as their own identity through requester/resolveProjectRole; no
// actor field exists in any of these request or response bodies.
//
// Outcomes from the use case map onto:
//
//	domain.ErrIdentityUnknown  → 404 {"resource":"identity"} — provision via POST /auth/accounts first
//	domain.ErrIdentityDisabled → 403 (Grant/PATCH only; DELETE tolerates disabled rows)
//	domain.ErrCannotAssign     → 403 hierarchy refusal ("at or below your own role")
//	domain.ErrLastAdmin        → 409 last-admin refusal (ownerless project would lose all admins)
//	domain.ErrNotFound         → 404 {"resource":"member"} — not a member of this project
//	anything else              → writeAppError's normal map (400 invalid / 500 internal)

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
)

// --- request / response DTOs -------------------------------------------------

// memberMutationReqs deliberately carry an OPTIONAL actor field for WIRE COMPATIBILITY:
// every other mutation endpoint in this API accepts it, and shared client code injects
// it into all mutation bodies. It is VALIDATED (malformed → 400 like everywhere else)
// but IGNORED: these verbs always act as the AUTHENTICATED principal (a credential
// session naming someone else cannot manage memberships — spoofing is impossible at
// this layer by construction), and member rows store no attribution.
type addMemberReq struct {
	Username string `json:"username"` // required; an existing ENABLED identity name (no auto-provisioning)
	Role     string `json:"role"`     // required; one of "viewer" | "editor" | "admin" (case-insensitive, trimmed)
	Actor    *actorInput
}

type setMemberRoleReq struct {
	Role  string `json:"role"` // required; one of "viewer" | "editor" | "admin"
	Actor *actorInput
}

// validateDeclaredActor enforces the shared actor-shape contract when the client sent
// one (nil = absent, which is fine). The validated value is discarded on purpose.
func validateDeclaredActor(w http.ResponseWriter, a *actorInput, resource string, id domain.ID) bool {
	if a == nil {
		return true
	}
	if _, err := a.toDomain(); err != nil {
		writeAppError(w, err, resource, id)
		return false
	}
	return true
}

// memberEntryDTO is one stored membership row in the list response. It carries no
// identity kind on purpose (ticket shape {username, role}); if the UI later needs it,
// that is an additive field, not a break.
type memberEntryDTO struct {
	Username string `json:"username"`
	Role     string `json:"role"` // "viewer" | "editor" | "admin"
}

type membersListDTO struct {
	Owner   *actorDTO        `json:"owner"`   // derived from the project's created_by — NOT a stored row; nil for legacy unattributed projects (or an owner whose identity lookup omitted them)
	Members []memberEntryDTO `json:"members"` // stored rows only, sorted by username (stable on ties)
}

type memberRespDTO struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Created  bool   `json:"created"` // true when a NEW row was inserted (POST → 201); false on re-grant (→ 200)
}

// --- preflight ----------------------------------------------------------------

// membersPreflight runs the shared T9 steps 1-3 for all four verbs. It returns the
// parsed project id and true when the handler may continue; any written response means
// stop. Step 4+ differ per verb (read vs admin gate) and run in each handler.
func (a *App) membersPreflight(w http.ResponseWriter, r *http.Request) (domain.ID, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return id, false
	}
	if !a.scopingActive() {
		writeMembersDisabled(w)
		return id, false
	}
	if a.Memberships == nil || a.Identity == nil {
		writeMembersNotEnabled(w)
		return id, false
	}
	return id, true
}

// parseMemberRole validates the free-form role strings these endpoints accept:
// case-insensitive exact match after trim onto viewer/editor/admin. "owner" gets its
// own explanatory refusal (ownership is derived from created_by, not assignable);
// empty/"none"/anything else is a plain invalid_input. The stored vocabulary can never
// receive owner/none through this adapter.
func parseMemberRole(raw string) (domain.Role, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "viewer":
		return domain.RoleViewer, nil
	case "editor":
		return domain.RoleEditor, nil
	case "admin":
		return domain.RoleAdmin, nil
	case "owner":
		return 0, fmt.Errorf("%w: role \"owner\" cannot be assigned — ownership derives from project creation (created_by) and is not a settable membership role", domain.ErrInvalid)
	case "", "none":
		return 0, fmt.Errorf("%w: role must be one of viewer, editor or admin", domain.ErrInvalid)
	default:
		return 0, fmt.Errorf("%w: role %q is not one of viewer, editor or admin", domain.ErrInvalid, raw)
	}
}

// writeMemberError maps the use-case outcome errors for POST/PATCH/DELETE onto their
// envelopes (see file doc). GET has no target name and only reaches I/O failures, so
// it goes straight to writeAppError.
func writeMemberError(w http.ResponseWriter, err error, username string) {
	switch {
	case errors.Is(err, domain.ErrIdentityUnknown):
		writeJSONError(w, http.StatusNotFound, map[string]any{
			"error":    ErrCodeNotFound,
			"resource": "identity",
			"message":  fmt.Sprintf("identity %q does not exist — provision it first via POST /api/v1/auth/accounts (shared operator token required)", username),
		})
	case errors.Is(err, domain.ErrIdentityDisabled):
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":    ErrCodeForbidden,
			"resource": "members",
			"message":  fmt.Sprintf("identity %q is disabled and cannot be granted or demoted; removing its row via DELETE remains possible", username),
		})
	case errors.Is(err, domain.ErrCannotAssign):
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":    ErrCodeForbidden,
			"resource": "members",
			"message":  err.Error(),
		})
	case errors.Is(err, domain.ErrLastAdmin):
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    ErrCodeConflict,
			"resource": "members",
			"message":  err.Error(),
		})
	case errors.Is(err, domain.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, map[string]any{
			"error":    ErrCodeNotFound,
			"resource": "member",
			"message":  fmt.Sprintf("identity %q is not a member of this project — see GET /api/v1/projects/{id}/members for who is", username),
		})
	default:
		writeAppError(w, err, "members", domain.ID(""))
	}
}

// --- handlers -----------------------------------------------------------------

// handleListMembers (GET): any visible member may read the roster. The owner is
// rendered from created_by through the SAME batched GetByIds display path used for
// project rows — a disabled former owner still renders (attribution, not auth); an
// unknown id simply omits them. Members are stored rows sorted by username; the owner
// may also appear among them if they hold a stored row (common for self-grants) — that
// is expected: their role column then shows the STORED role, and ownership is conveyed
// by the separate "owner" field.
func (a *App) handleListMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := a.membersPreflight(w, r)
	if !ok {
		return
	}
	p, err := a.Projects.Get(r.Context(), id)
	if err != nil {
		writeAppError(w, err, "project", id) // 404 unknown project — before the gate (same order as gateProject)
		return
	}
	// Read-line gate: operators pass through untouched; membership mode resolves the
	// requester and 403s non-visible subjects with required=viewer.
	if _, _, okc := a.checkProject(w, r, declaredName(r), p, domain.OpRead); !okc {
		return
	}

	members, err := a.Memberships.List(r.Context(), p)
	if err != nil {
		writeAppError(w, err, "project", id)
		return
	}

	// One batched display lookup for every identity we need to name: member rows + owner.
	ids := make([]domain.ID, 0, len(members)+1)
	seen := make(map[domain.ID]struct{}, len(members)+1)
	for _, m := range members {
		if _, dup := seen[m.IdentityID]; !dup {
			seen[m.IdentityID] = struct{}{}
			ids = append(ids, m.IdentityID)
		}
	}
	if p.CreatedBy != nil && *p.CreatedBy != "" {
		if _, dup := seen[*p.CreatedBy]; !dup {
			seen[*p.CreatedBy] = struct{}{}
			ids = append(ids, *p.CreatedBy)
		}
	}
	resolved, err := a.Identity.GetByIds(r.Context(), ids)
	if err != nil { // display-path failure must not 500 the roster when members exist: degrade to empty names only if the use case itself is fine? — NO: keep it honest, it IS an I/O failure
		writeAppError(w, err, "project", id)
		return
	}

	entries := make([]memberEntryDTO, 0, len(members))
	for _, m := range members {
		i, okI := resolved[m.IdentityID]
		if !okI {
			continue // row whose identity vanished (should not happen — FK cascade) : omit rather than render blank
		}
		entries = append(entries, memberEntryDTO{Username: i.Name, Role: m.Role.String()})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Username < entries[j].Username })

	var owner *actorDTO
	if p.CreatedBy != nil && *p.CreatedBy != "" {
		if i, okI := resolved[*p.CreatedBy]; okI {
			owner = &actorDTO{Type: string(i.Kind), Name: i.Name}
		}
	}
	writeJSON(w, http.StatusOK, membersListDTO{Owner: owner, Members: entries})
}

// handleAddMember (POST): upsert-grant. 201 + created:true for a fresh row; 200 +
// created:false on re-grant (idempotent — same role or a different one both "succeed",
// subject to the guards). The response echoes the normalized (trimmed) username and the
// STABLE canonical role string, not the raw request casing.
func (a *App) handleAddMember(w http.ResponseWriter, r *http.Request) {
	id, ok := a.membersPreflight(w, r)
	if !ok {
		return
	}
	var req addMemberReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if !validateDeclaredActor(w, req.Actor, "members", id) { // wire compatibility: accept + validate, then ignore (principal is authoritative)
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		writeAppError(w, fmt.Errorf("%w: username is required", domain.ErrInvalid), "members", id)
		return
	}
	role, err := parseMemberRole(req.Role)
	if err != nil {
		writeAppError(w, err, "members", id)
		return
	}
	p, actorRole, okm := a.manageMembers(w, r, declaredName(r), id)
	if !okm {
		return
	}
	_, created, err := a.Memberships.Grant(r.Context(), p, username, role, actorRole)
	if err != nil {
		writeMemberError(w, err, username)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, memberRespDTO{Username: username, Role: role.String(), Created: created})
}

// handleUpdateMemberRole (PATCH /members/{username}): role change for an EXISTING
// member only — a target with no stored row is 404 "not a member" (use POST to add).
// PATCH (not PUT): the endpoint updates a single field of an identified resource, and
// it matches every other role/field-partial-update verb in this API (tasks, ACs).
func (a *App) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	id, ok := a.membersPreflight(w, r)
	if !ok {
		return
	}
	username := strings.TrimSpace(r.PathValue("username"))
	if username == "" {
		writeAppError(w, fmt.Errorf("%w: username is required", domain.ErrInvalid), "members", id)
		return
	}
	var req setMemberRoleReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// Wire compatibility: accept + validate a declared actor, then ignore it (the
	// authenticated principal is authoritative for these verbs).
	if !validateDeclaredActor(w, req.Actor, "members", id) {
		return
	}
	role, err := parseMemberRole(req.Role)
	if err != nil {
		writeAppError(w, err, "members", id)
		return
	}
	p, actorRole, okm := a.manageMembers(w, r, declaredName(r), id)
	if !okm {
		return
	}
	_, err = a.Memberships.SetRole(r.Context(), p, username, role, actorRole)
	if err != nil {
		writeMemberError(w, err, username)
		return
	}
	writeJSON(w, http.StatusOK, memberRespDTO{Username: username, Role: role.String(), Created: false})
}

// handleRemoveMember (DELETE /members/{username}): removes the stored row. 204 with no
// body (matching every other delete verb here). Removing a disabled identity's row is
// explicitly allowed; removing a non-member or unknown name is a distinct 404 each.
func (a *App) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	id, ok := a.membersPreflight(w, r)
	if !ok {
		return
	}
	username := strings.TrimSpace(r.PathValue("username"))
	if username == "" {
		writeAppError(w, fmt.Errorf("%w: username is required", domain.ErrInvalid), "members", id)
		return
	}
	p, actorRole, okm := a.manageMembers(w, r, declaredName(r), id)
	if !okm {
		return
	}
	err := a.Memberships.Remove(r.Context(), p, username, actorRole)
	if err != nil {
		writeMemberError(w, err, username)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
