package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// This file carries the slice-3b routes: the claim lease lifecycle
// (claim/release/heartbeat), collaboration and evidence (typed comments,
// replace-on-write notes, acceptance-criteria toggles, artifact attachments)
// and task relations (add/remove). Responses stay compact — summary rows for
// tasks, single-row DTOs for child collections — consistent with slice 3a.

// --- claim lifecycle ---------------------------------------------------------------

// handleClaimTask → POST /api/v1/tasks/{id}/claim. One atomic CAS in the use
// case: a free, expired (takeover), or same-holder (renewal) lease wins and
// returns 200 with the fresh task (status doing, lease re-stamped); a foreign
// ACTIVE lease loses with 409 whose holder+expiry ARE the recovery
// instruction ("wait until X, or work another task"). ttl_sec omitted/0 →
// server default (900s); negative → 400; values above the cap are clamped to
// 3600 by the use case. Claiming never touches the durable assignee.
func (a *App) handleClaimTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req claimReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	ttl := 0
	if req.TTLSec != nil {
		ttl = *req.TTLSec
	}
	fresh, err := a.Tasks.Claim(ctx, actor, id, ttl)
	if err != nil {
		writeAppError(w, err, "task", id) // ClaimConflictError → 409 holder+expiry
		return
	}
	writeJSON(w, http.StatusOK, toSummary(fresh))
}

// handleReleaseTask → POST /api/v1/tasks/{id}/release. Holder-only against an
// ACTIVE lease — a foreign or expired holder gets 409 with the real holder's
// identity and expiry; releasing a task with no lease at all is an idempotent
// no-op (retry-safe). Success clears the lease, moves status→todo, bumps the
// version and appends a released event carrying the optional note.
func (a *App) handleReleaseTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req releaseReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	fresh, err := a.Tasks.Release(ctx, actor, id, req.Note)
	if err != nil {
		writeAppError(w, err, "task", id) // ClaimConflictError → 409 holder+expiry
		return
	}
	writeJSON(w, http.StatusOK, toSummary(fresh))
}

// handleHeartbeat → POST /api/v1/tasks/{id}/heartbeat. Keep-alive for the
// active self-holder: extends claim_expires_at by the STORED ttl without
// bumping the version and without touching updated_at — a heartbeat is not a
// state change, so it never disturbs a working agent's read base. Foreign or
// expired holder, or no lease at all (zero-value Holder on the 409 body =
// "claim it first"), → 409 claim conflict. The fresh row doubles as the
// liveness answer.
func (a *App) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req heartbeatReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	fresh, err := a.Tasks.Heartbeat(ctx, actor, id)
	if err != nil {
		writeAppError(w, err, "task", id) // ClaimConflictError → 409 holder+expiry
		return
	}
	writeJSON(w, http.StatusOK, toSummary(fresh))
}

// --- comments ----------------------------------------------------------------------

// handleAddComment → POST /api/v1/tasks/{id}/comments. Typed kind + markdown
// body; the comment and exactly one `commented` event are appended in a single
// unit of work. Child-row rule: no task version bump, so a working agent with
// a stale read is not 409'd by someone else's chatter. 201 fresh comment; bad
// kind or empty body → 400; unknown task → 404.
func (a *App) handleAddComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req commentReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	c, err := a.Comments.Add(ctx, actor, id, domain.CommentKind(req.Kind), req.Body)
	if err != nil {
		writeAppError(w, err, "task", id) // bad kind / empty body → 400; unknown task → 404
		return
	}
	writeJSON(w, http.StatusCreated, toComment(c))
}

// handleListComments → GET /api/v1/tasks/{id}/comments?limit&offset (UI extra).
// Paged comment thread newest first plus `total` (before paging) and the
// effective limit/offset — the standard offset/limit envelope. limit defaults
// to 20, clamped to 100; offset defaults to 0. Unknown task → 404.
func (a *App) handleListComments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePageParams(r.URL.Query())
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	rows, total, err := a.Comments.List(ctx, id, limit, offset)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	out := make([]commentDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, toComment(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"comments": out,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

// --- notes --------------------------------------------------------------------------

// handleUpdateNotes → PUT /api/v1/tasks/{id}/notes. Replace-on-write of the
// WHOLE notes blob — deliberately NO caller-supplied base version (§5's
// required `version` was dropped for this endpoint: notes are a single-writer
// resume scratchpad, and demanding a re-read before every edit would only add
// round-trips). The use case's internal read-then-write guard still turns a
// genuinely CONCURRENT write into 409 version_conflict. Identical content is
// a no-op that returns the fresh row without bumping; a real change bumps the
// version and appends a `noted` event. Unknown task → 404.
func (a *App) handleUpdateNotes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req notesReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	fresh, err := a.Tasks.UpdateNotes(ctx, actor, id, req.Notes, 0) // 0 = legacy replace-on-write (no base-version guard)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	writeJSON(w, http.StatusOK, toSummary(fresh))
}

// --- acceptance criteria --------------------------------------------------------------

// parseACSeq extracts the {seq} path parameter of an acceptance-criteria route.
// Non-numeric or non-positive → 400 (a checklist row is addressed by a 1-based
// integer; anything else is malformed input, not a lookup miss).
func parseACSeq(w http.ResponseWriter, r *http.Request) (int, bool) {
	rawSeq := r.PathValue("seq")
	seq, err := strconv.Atoi(rawSeq)
	if err != nil || seq < 1 {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": fmt.Sprintf("acceptance-criteria seq must be a positive integer, got %q", rawSeq),
		})
		return 0, false
	}
	return seq, true
}

// handleToggleAC → PATCH /api/v1/tasks/{id}/acceptance-criteria/{seq}. Two
// shapes share the endpoint: the complete_ac toggle (done + optional note) and
// a text reword — when "text" is present it takes precedence and done/note are
// ignored. Child-row rule: no task version bump either way. 200 with the
// updated criterion row; unknown task OR unknown seq → 404; empty/absent actor
// or whitespace-only text → 400.
func (a *App) handleToggleAC(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	seq, ok := parseACSeq(w, r)
	if !ok {
		return
	}
	var req acToggleReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	var c domain.AcceptanceCriterion
	if req.Text != nil { // reword takes precedence over the toggle fields
		c, err = a.Acceptance.EditACText(ctx, actor, id, seq, *req.Text)
	} else {
		c, err = a.Acceptance.ToggleAC(ctx, actor, id, seq, req.Done, req.Note)
	}
	if err != nil {
		writeAppError(w, err, "task", id) // unknown task or seq → 404; empty text → 400
		return
	}
	writeJSON(w, http.StatusOK, toAC(c))
}

// handleAddAC → POST /api/v1/tasks/{id}/acceptance-criteria. Appends a new
// criterion at seq max(seq)+1 (monotonic; callers never choose the seq). Child-
// row rule: no task version bump; one `ac-added` event in the same tx. 201 with
// the fresh row; whitespace-only text or bad actor → 400; unknown task → 404.
func (a *App) handleAddAC(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req acCreateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	c, err := a.Acceptance.AddAC(ctx, actor, id, req.Text)
	if err != nil {
		writeAppError(w, err, "task", id) // empty text → 400; unknown task → 404
		return
	}
	writeJSON(w, http.StatusCreated, toAC(c))
}

// handleRemoveAC → DELETE /api/v1/tasks/{id}/acceptance-criteria/{seq}. Removes
// exactly the criterion at the 1-based seq (path value; non-positive/non-numeric
// → 400). The body is optional JSON but MUST carry a valid actor: a removal
// appends an `ac-removed` event that records WHO removed it, so no-body/empty-
// actor → 400 rather than a fabricated identity in the audit log. 204 on
// success (the deleted seq + text ride on the event's Before payload); unknown
// task or seq → 404. Surviving criteria keep their seqs — no renumbering.
func (a *App) handleRemoveAC(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	seq, ok := parseACSeq(w, r)
	if !ok {
		return
	}
	var rawBody []byte
	if r.Body != nil {
		var err error
		rawBody, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid input",
				"message": "unreadable request body: " + err.Error(),
			})
			return
		}
	}
	var req acRemoveReq
	if len(rawBody) == 0 {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": `actor is required for AC removal: send a JSON body with {"actor":{"name":"..."}}`,
		})
		return
	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "malformed JSON body: " + err.Error(),
		})
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}

	if err := a.Acceptance.RemoveAC(ctx, actor, id, seq); err != nil {
		writeAppError(w, err, "task", id) // unknown task or seq → 404
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- artifacts -----------------------------------------------------------------------

// handleAttachArtifact → POST /api/v1/tasks/{id}/artifacts. Evidence attach:
// typed kind + ref (URL or filesystem path) + optional one-line summary.
// Append-only with one `artifact-added` event in the same unit of work;
// child-row rule — no task version bump. 201 fresh artifact; bad kind or empty
// ref → 400; unknown task → 404.
func (a *App) handleAttachArtifact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req artifactReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	art, err := a.Artifacts.Attach(ctx, actor, id, domain.ArtifactKind(req.Kind), req.Ref, req.Summary)
	if err != nil {
		writeAppError(w, err, "task", id) // bad kind / empty ref → 400; unknown task → 404
		return
	}
	writeJSON(w, http.StatusCreated, toArtifact(art))
}

// --- relations -----------------------------------------------------------------------

// storedEdgeFor mirrors application.normalizeRelation: 'parent' ("{id} is a
// parent of {other}") stores as the INVERSE child edge (other → id, child);
// every other valid kind stores verbatim, directed id → other. It exists so
// the add handler can pre-classify 201-new vs 200-upsert from the task's
// current edge set; if the use case's mapping ever changes, this flips with it.
func storedEdgeFor(id, other domain.ID, kind domain.RelationKind) (src, dst domain.ID, stored domain.RelationKind) {
	if kind == domain.RelationParent {
		return other, id, domain.RelationChild
	}
	return id, other, kind
}

// handleAddRelation → POST /api/v1/tasks/{id}/relations. Body: actor, other_id,
// kind (any tool-boundary value: child|parent|blocked-by|duplicate-of|related).
// A new edge → 201 with the stored row; re-adding an EXISTING identical edge is
// the idempotent upsert no-op → 200 with that same stored row and NO event
// (retry-safe for agents — §5). 409 parent-conflict when a task that already
// has a different parent gets a second one; 400 self-relation, invalid kind,
// or cross-project pair; 404 unknown task (either end). The response row always
// carries the STORED kind — 'parent' never appears on the wire.
func (a *App) handleAddRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req relateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	otherID := domain.ID(req.OtherID)
	if otherID.IsZero() {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "other_id is required: the task this edge points at",
		})
		return
	}
	kind := domain.RelationKind(req.Kind)

	// Pre-check for the 201-new vs 200-upsert status (the use case does not
	// distinguish; §5's contract requires both). The check is best-effort — a
	// concurrent writer winning it changes at most the status code, never the row.
	detail, err := a.Tasks.GetDetail(ctx, id, 0, 0)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	src, dst, stored := storedEdgeFor(id, otherID, kind)
	existing := false
	for _, e := range detail.Relations {
		if e.Kind == stored && e.TaskID == src && e.OtherTaskID == dst {
			existing = true
			break
		}
	}

	rel, err := a.Relations.Relate(ctx, actor, id, otherID, kind, application.RelateAdd)
	if err != nil {
		writeAppError(w, err, "task", id) // self/kind/cross-project → 400; unknown → 404; second parent → 409
		return
	}
	code := http.StatusCreated
	if existing {
		code = http.StatusOK // idempotent re-add of the same edge
	}
	writeJSON(w, code, toRelation(rel))
}

// handleRemoveRelation → DELETE /api/v1/tasks/{id}/relations/{other_id}/{kind}.
// Keyed by the composite — relation ids are never on the wire. 204 on remove
// (the edge's Before JSON rides on the appended relation-changed event); 404
// when the edge is missing; 400 self-relation/invalid kind/unknown task. The
// body is optional JSON but MUST carry a valid actor: a removal appends an
// event that records WHO removed it, so no-body/empty-actor → 400 invalid
// input rather than a fabricated identity in the audit log.
func (a *App) handleRemoveRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	otherID := domain.ID(r.PathValue("other_id"))
	if otherID.IsZero() {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "missing path other_id",
		})
		return
	}
	kind := domain.RelationKind(r.PathValue("kind"))

	// A client sending no body at all can present a nil r.Body (that is what
	// http.NewRequest does for a nil body) — treat it as "no body", not as a
	// read error.
	var rawBody []byte
	if r.Body != nil {
		var err error
		rawBody, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid input",
				"message": "unreadable request body: " + err.Error(),
			})
			return
		}
	}
	var req removeRelationReq
	if len(rawBody) == 0 {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "actor is required for relation removal: send a JSON body with {\"actor\":{\"name\":\"...\"}}",
		})
		return
	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "malformed JSON body: " + err.Error(),
		})
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}

	if _, err := a.Relations.Relate(ctx, actor, id, otherID, kind, application.RelateRemove); err != nil {
		writeAppError(w, err, "task", id) // missing edge → 404; self/kind/unknown task → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
