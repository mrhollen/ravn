package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// Error contract (locked in slice 3a — the MCP layer propagates these bodies
// verbatim, so "message" doubles as the agent-readable recovery hint):
//
//	domain.ErrNotFound        → 404 {"error":"not found","resource":<type>,...}
//	domain.ErrInvalid         → 400 {"error":"invalid input","message":<detail>}
//	*domain.TransitionError   → 400 {"error":"invalid input","message":"cannot
//	                            transition status from \"…\" to \"…\""} — an
//	                            illegal status move IS malformed caller input, so
//	                            it keeps the SAME 400 "invalid input" shape the old
//	                            wrapped-ErrInvalid form produced (the message is
//	                            pattern-matched by web/src/moves.ts)
//	*domain.ConflictError     → 409 version conflict: the body carries
//	                            "current_version" AND the fresh current task as a
//	                            summary row under "current", so a stale caller can
//	                            rebase without a separate read
//	*domain.ClaimConflictError→ 409 {"error":"claim conflict","holder":{...},
//	                            "expires_at":...} — holder + expiry ARE the
//	                            recovery instruction ("wait until X, or work another
//	                            task"). Always names a LIVE foreign holder (a
//	                            no-lease heartbeat is ErrNotClaimed below)
//	domain.ErrNotClaimed      → 409 {"error":"not claimed","resource":<type>,
//	                            "id":...} — the operation needs an ACTIVE lease but
//	                            the task has none at all; recovery is "claim first"
//	*domain.TaskBlockedError  → 409 {"error":"task blocked","blockers":[<id>...]}
//	                            — a claim or completion (transition to done) on
//	                            a task with open blockers; the IDs name what to
//	                            complete or archive first (or override_blockers)
//	*domain.TaskUnsatisfiedACsError → 409 {"error":"unsatisfied_acceptance_criteria",
//	                            "unmet_acs":[<seq>...]} — completion on a task with
//	                            unsatisfied criteria; complete or override_acs
//	domain.ErrParentConflict  → 409 {"error":"parent conflict",...}
//	anything else             → 500 {"error":"internal"} (logged)
//
// Every /api/v1 response body is JSON — errors included.

// writeAppError maps a use-case failure to its HTTP error envelope. resource
// names the affected entity type ("project"/"task") for the 404/409 bodies;
// id may be the zero ID when unknown (e.g. validation on create).
// writeVersionConflict renders the 409 body for a stale expected version:
// current_version plus the fresh current task as a summary row, so the caller
// can rebase from the response alone. Shared by use-case *ConflictError (a
// race inside the write) and the adapter's read-then-guard pre-check (§5
// PATCH contract).
func writeVersionConflict(w http.ResponseWriter, id domain.ID, cur domain.Task) {
	writeJSONError(w, http.StatusConflict, map[string]any{
		"error":           "version_conflict",
		"resource":        "task",
		"id":              string(id),
		"current_version": cur.Version,
		"updated_at":      rfc3339(cur.UpdatedAt),
		"message": fmt.Sprintf(
			"task %q is at version %d — re-read it and retry with that version",
			string(id), cur.Version),
		"current": toSummary(cur),
	})
}

func writeAppError(w http.ResponseWriter, err error, resource string, id domain.ID) {
	var conflict *domain.ConflictError
	if errors.As(err, &conflict) {
		writeVersionConflict(w, id, conflict.Current)
		return
	}

	var claim *domain.ClaimConflictError
	if errors.As(err, &claim) {
		holder := actorDTO{Type: string(claim.Holder.Type)}
		if claim.Holder.Name != "" {
			holder.Name = claim.Holder.Name
		}
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":      "claim conflict",
			"resource":   resource,
			"id":         string(id),
			"holder":     holder,
			"expires_at": rfc3339(claim.ExpiresAt),
			"message": fmt.Sprintf(
				"%q is held by %s (%s) until %s — wait for the lease to expire or work another task",
				string(id), claim.Holder.Type, claim.Holder.Name, rfc3339(claim.ExpiresAt)),
		})
		return
	}

	if errors.Is(err, domain.ErrNotClaimed) {
		// No lease at all — NOT a claim conflict (there is nobody to wait for).
		// Same 409 family as the claim conflicts so callers that branch on
		// status handle it alongside them; the body tag names the real state.
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    "not claimed",
			"resource": resource,
			"id":       string(id),
			"message":  err.Error(),
		})
		return
	}

	var transition *domain.TransitionError
	if errors.As(err, &transition) {
		// Illegal status move: malformed caller input — the SAME 400 "invalid
		// input" shape the old wrapped-ErrInvalid form produced (status code
		// unchanged; web/src/moves.ts pattern-matches the message text).
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": err.Error(),
		})
		return
	}

	var blocked *domain.TaskBlockedError
	if errors.As(err, &blocked) {
		ids := make([]string, 0, len(blocked.BlockerIDs))
		for _, b := range blocked.BlockerIDs {
			ids = append(ids, string(b))
		}
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    "task blocked",
			"resource": resource,
			"id":       string(id),
			"blockers": ids, // machine-readable: the open blocker IDs (edge-creation order)
			"message": fmt.Sprintf(
				"%q is blocked by %d unresolved blocker(s) (%s) — complete or archive them first, or override with override_blockers=true",
				string(id), len(ids), strings.Join(ids, ", ")),
		})
		return
	}

	var unmetACs *domain.TaskUnsatisfiedACsError
	if errors.As(err, &unmetACs) {
		seqStrs := make([]string, len(unmetACs.UnmetSeqs))
		for i, s := range unmetACs.UnmetSeqs {
			seqStrs[i] = strconv.Itoa(s)
		}
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":     "unsatisfied_acceptance_criteria",
			"resource":  resource,
			"id":        string(id),
			"unmet_acs": unmetACs.UnmetSeqs,
			"message": fmt.Sprintf(
				"%q has %d unsatisfied acceptance criteria (seq %s) — complete criteria first, or override with override_acs=true",
				string(id), len(unmetACs.UnmetSeqs), strings.Join(seqStrs, ", ")),
		})
		return
	}

	if errors.Is(err, domain.ErrParentConflict) {
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    "parent conflict",
			"resource": resource,
			"id":       string(id),
			"message":  err.Error(),
		})
		return
	}

	if errors.Is(err, domain.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, map[string]any{
			"error":    "not found",
			"resource": resource,
			"id":       string(id),
			"message":  fmt.Sprintf("%s %q does not exist — check the id (use the list endpoint)", resource, string(id)),
		})
		return
	}

	if errors.Is(err, domain.ErrInvalid) {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": err.Error(),
		})
		return
	}

	slog.Error("unmapped error from application layer", "err", err, "resource", resource)
	writeJSONError(w, http.StatusInternalServerError, map[string]any{
		"error":   "internal",
		"message": "unexpected server error",
	})
}
