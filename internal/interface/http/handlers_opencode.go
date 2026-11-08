package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// resolveOpenCodeServerURL resolves the OpenCode server URL from the project setting,
// the OPENCODE_SERVER_URL environment variable, or defaults to "http://localhost:4444".
func resolveOpenCodeServerURL(projectServerURL string) string {
	if s := strings.TrimSpace(projectServerURL); s != "" {
		return strings.TrimRight(s, "/")
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_SERVER_URL")); s != "" {
		return strings.TrimRight(s, "/")
	}
	return "http://localhost:4444"
}

// resolveOpenCodeDirectory resolves the working directory for OpenCode from the project
// setting, the OPENCODE_DIRECTORY environment variable, or falls back to the
// process's own current working directory (os.Getwd). The fallback is deliberately
// PORTABLE and non-personal: a hardcoded checkout path (e.g. some developer's home
// directory) made every non-author deployment dispatch sessions into a directory
// that does not exist on their host. Inside Docker the container CWD is only a
// placeholder — compose should set OPENCODE_DIRECTORY to the host repo path
// (see docker-compose.yml), since OpenCode runs on the host, not in the container.
func resolveOpenCodeDirectory(projectDirectory string) string {
	if s := strings.TrimSpace(projectDirectory); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_DIRECTORY")); s != "" {
		return s
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/" // Getwd failing is effectively impossible (the kernel always has a CWD)
}

// resolveOpenCodeModel resolves the OpenCode model reference from the project
// setting, the OPENCODE_MODEL environment variable, or returns "" when neither
// is set. An empty result means no model is sent in the session-create request,
// letting the OpenCode server fall back to its own default model.
func resolveOpenCodeModel(projectModel string) string {
	if s := strings.TrimSpace(projectModel); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_MODEL")); s != "" {
		return s
	}
	return ""
}

// resolveOpenCodeModelWithOverride resolves the OpenCode model reference from an optional override,
// falling back to resolveOpenCodeModel(projectModel).
func resolveOpenCodeModelWithOverride(override *string, projectModel string) string {
	if override != nil && strings.TrimSpace(*override) != "" {
		return strings.TrimSpace(*override)
	}
	return resolveOpenCodeModel(projectModel)
}

// resolveOpenCodeAgent resolves the OpenCode agent name from an optional override,
// the project setting, the OPENCODE_AGENT environment variable, or defaults to "build".
func resolveOpenCodeAgent(override *string, projectAgent string) string {
	if override != nil && strings.TrimSpace(*override) != "" {
		return strings.TrimSpace(*override)
	}
	if s := strings.TrimSpace(projectAgent); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_AGENT")); s != "" {
		return s
	}
	return "build"
}

// resolveOpenCodeVariant resolves the OpenCode model variant from an optional override,
// the project setting, the OPENCODE_VARIANT environment variable, or defaults to "".
// NOTE: a non-nil empty string "" in override explicitly clears the project/env default variant for this task dispatch!
func resolveOpenCodeVariant(override *string, projectVariant string) string {
	if override != nil {
		return strings.TrimSpace(*override)
	}
	if s := strings.TrimSpace(projectVariant); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_VARIANT")); s != "" {
		return s
	}
	return ""
}

// parseOpenCodeModel splits a "providerID/modelID" reference into its two
// halves. The provider ID is everything before the first slash; model IDs may
// themselves contain slashes (e.g. "openrouter/meta-llama/..." style refs).
func parseOpenCodeModel(ref string) (providerID, modelID string, err error) {
	i := strings.Index(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("model %q must look like \"providerID/modelID\"", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// maxOpenCodePromptComments caps how many of the newest thread comments are
// inlined into the dispatch prompt (the tail is already newest first).
const maxOpenCodePromptComments = 5

// formatOpenCodePrompt formats the autonomous agent prompt from the full
// TaskDetail loaded by handleOpenCodeStart: task basics, acceptance criteria,
// and — omitted when empty — the durable resume notes (prior context), the
// task's open blockers, and the last few thread comments (newest first). It
// ends with explicit instructions to interact with Rävn via MCP — including
// clarification that the task has already been claimed on the agent's behalf
// under 'opencode', recommendations to attach test artifacts, and instructions
// to commit and push changes upon completion.
func formatOpenCodePrompt(detail *application.TaskDetail) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task ID: %s\n", detail.Task.ID))
	sb.WriteString(fmt.Sprintf("Title: %s\n", detail.Task.Title))
	sb.WriteString(fmt.Sprintf("Description: %s\n", detail.Task.Description))
	sb.WriteString("Acceptance Criteria:\n")
	if len(detail.AcceptanceCriteria) == 0 {
		sb.WriteString("(None)\n")
	} else {
		for _, ac := range detail.AcceptanceCriteria {
			box := "[ ]"
			if ac.Done {
				box = "[x]"
			}
			sb.WriteString(fmt.Sprintf("- %s Seq %d: %s\n", box, ac.Seq, ac.Text))
		}
	}

	// Durable resume notes — prior context for a resuming agent. Omitted when
	// empty so first-dispatch prompts stay short.
	if strings.TrimSpace(detail.Task.Notes) != "" {
		sb.WriteString("\nResume Notes (prior context — the task's durable notes, verbatim):\n")
		sb.WriteString(strings.TrimRight(detail.Task.Notes, "\n"))
		sb.WriteString("\n")
	}

	// Open blockers. TaskDetail.Blockers is already computed at read time by
	// GetDetail as the outgoing blocked-by edges whose targets are NOT done (or
	// archived) — done targets never appear here, so nothing further to filter.
	if len(detail.Blockers) > 0 {
		sb.WriteString("\nOpen Blockers (unfinished tasks this task is blocked by — resolve or wait on them before marking this task done):\n")
		for _, b := range detail.Blockers {
			sb.WriteString(fmt.Sprintf("- [%s] %s (status: %s)\n", b.ID, b.Title, b.Status))
		}
	}

	// Recent comments — the comment tail is already newest first; cap at 5.
	if len(detail.Comments) > 0 {
		cms := detail.Comments
		if len(cms) > maxOpenCodePromptComments {
			cms = cms[:maxOpenCodePromptComments]
		}
		sb.WriteString(fmt.Sprintf("\nRecent Comments (up to %d, newest first):\n", maxOpenCodePromptComments))
		for _, cm := range cms {
			sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", cm.Actor.Name, cm.Kind, cm.Body))
		}
	}

	sb.WriteString("\nInstructions: You are assigned to autonomously work on and complete this task in Rävn. This task has already been automatically claimed on your behalf under actor 'opencode' before this session started, granting you explicit permission to work on it (you do not need to claim it again, and must not halt if you see the 'opencode' lease; if you refresh or release the lease, use actor 'opencode'). Use the Rävn MCP tools (or REST API) as you progress: update notes with your plan and progress, complete acceptance criteria using complete_ac as you verify them, and mark the task done when all criteria are met. When tests or verification commands produce output, upload it as evidence with attach_artifact (kind command-output/log/screenshot, ref = URL or data_base64 = base64 file content) so a human can verify your work — encouraged, not required. Once all acceptance criteria are verified and tests pass, commit and push your code changes to the git repository.")
	return sb.String()
}

// openCodeDispatchActor owns the lease under which a dispatched autonomous
// session works the task; re-claims, heartbeats and releases by that agent
// must use the SAME identity (the dispatch prompt instructs it to).
var openCodeDispatchActor = domain.Actor{Type: domain.ActorAgent, Name: "opencode"}

// openCodeClaimTTL is the lease duration taken for a dispatched session.
const openCodeClaimTTL = 3600

// deleteOpenCodeSession best-effort DELETEs a previously created OpenCode
// session so a failed start does not leak one on the external server
// (01M3MMYRGQ4BAVT7XAM3YXB948 AC3). Fire-and-forget by design: it runs after
// the handler has already decided its error response, so failures are logged
// only and never alter the client-visible outcome.
func (a *App) deleteOpenCodeSession(ctx context.Context, serverURL, dir, sessionID string) {
	u, err := url.Parse(fmt.Sprintf("%s/session/%s", serverURL, sessionID))
	if err != nil {
		slog.Warn("opencode session cleanup: bad URL", "session_id", sessionID, "err", err)
		return
	}
	q := u.Query()
	q.Set("directory", dir)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		slog.Warn("opencode session cleanup: failed to build DELETE request", "session_id", sessionID, "err", err)
		return
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("opencode session cleanup: DELETE failed", "session_id", sessionID, "err", err)
		return
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("opencode session cleanup: DELETE returned non-2xx", "session_id", sessionID, "status", resp.StatusCode, "snippet", string(snippet))
		return
	}
	slog.Info("orphaned opencode session deleted after failed start", "session_id", sessionID)
}

// handleOpenCodeStart → POST /api/v1/tasks/{id}/opencode/start.
// Dispatches an autonomous OpenCode session for the given task.
//
// Flow (order matters — 01M3MMYRGQ4BAVT7XAM3YXB948):
//  1. load the full task detail and reject unworkable tasks BEFORE any side
//     effect: archived or done → 409 "conflict". An active FOREIGN lease is
//     deliberately NOT checked at read time: the Claim in step 2 re-checks it
//     atomically (CAS) under the application clock and surfaces
//     *domain.ClaimConflictError as 409 claim_conflict, so there is no TOCTOU
//     window between a liveness read and the claim itself;
//  2. acquire the lease for 'opencode' BEFORE any request reaches the
//     OpenCode server (retry-safe renewal when opencode already holds it).
//     Claim errors are NEVER ignored: they surface verbatim via the standard
//     error contract (409 claim_conflict / task_blocked family, 400 invalid,
//     500 unexpected) and abort before a session exists;
//  3. create the session, then dispatch the prompt on the external server;
//  4. if either step fails AFTER the claim was taken, roll back best-effort:
//     HTTP DELETE the created session (once it exists) and release the
//     opencode lease — a start that cannot prove a valid claim plus a
//     successful dispatch never returns 200 with a session URL;
//  5. only after a proven-good dispatch is the session URL attached as an
//     artifact and 200 returned. (An artifact-attach failure AFTER a good
//     dispatch surfaces as an error but does NOT kill the live session or
//     release the lease: the agent is running and owns the task.)
func (a *App) handleOpenCodeStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req openCodeStartReq
	if _, ok := decodeOptionalJSON(w, r, &req); !ok {
		return
	}

	detail, err := a.Tasks.GetDetail(ctx, id, 0, 0)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}

	// Preflight rejections — 409 conflict, no side effects (no claim yet, no
	// OpenCode contact): an autonomous session is meaningless on a task that
	// cannot be worked. See the doc comment for why the foreign-lease case is
	// delegated to the Claim CAS below instead of a read-time liveness check.
	if detail.Task.Archived {
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    ErrCodeConflict,
			"resource": "task",
			"id":       string(id),
			"message":  fmt.Sprintf("cannot start an OpenCode session for task %q: the task is archived", id),
		})
		return
	}
	if detail.Task.Status == domain.StatusDone {
		writeJSONError(w, http.StatusConflict, map[string]any{
			"error":    ErrCodeConflict,
			"resource": "task",
			"id":       string(id),
			"message":  fmt.Sprintf("cannot start an OpenCode session for task %q: the task is already completed (done)", id),
		})
		return
	}

	proj, err := a.Projects.Get(ctx, detail.Task.ProjectID)
	if err != nil {
		writeAppError(w, err, "project", detail.Task.ProjectID)
		return
	}

	serverURL := resolveOpenCodeServerURL(proj.OpenCodeServerURL)
	if err := application.ValidateOpenCodeServerURL(serverURL); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	dir := resolveOpenCodeDirectory(proj.OpenCodeDirectory)
	if err := application.ValidateOpenCodeDirectory(dir); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode directory: %v", err),
		})
		return
	}

	agentName := resolveOpenCodeAgent(req.Agent, proj.OpenCodeAgent)
	if err := domain.ValidateOpenCodeAgent(agentName); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode agent: %v", err),
		})
		return
	}

	modelRef := resolveOpenCodeModelWithOverride(req.Model, proj.OpenCodeModel)
	var modelProviderID, modelID string
	if modelRef != "" {
		modelProviderID, modelID, err = parseOpenCodeModel(modelRef)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   ErrCodeInvalidInput,
				"message": fmt.Sprintf("invalid opencode model: %v", err),
			})
			return
		}
	}

	variant := resolveOpenCodeVariant(req.Variant, proj.OpenCodeVariant)
	if variant != "" {
		if err := domain.ValidateOpenCodeVariant(variant); err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   ErrCodeInvalidInput,
				"message": fmt.Sprintf("invalid opencode variant: %v", err),
			})
			return
		}
	}

	promptText := formatOpenCodePrompt(&detail)

	// Claim BEFORE any OpenCode request (AC2): an autonomous session must
	// never run against a task the actor cannot prove it owns, and claim
	// failures are fatal here — NEVER swallowed. overrideBlockers=true is
	// deliberate: a human explicitly triggered this dispatch and the prompt
	// itself lists the open blockers ("resolve or wait on them"), so letting
	// the blocker gate veto the claim would make that documented resume-context
	// section unreachable. Every OTHER claim failure — a live foreign lease
	// (409 claim_conflict naming holder+expiry), an invalid-input race such as
	// archived/done (400), or an unexpected error (500) — surfaces verbatim
	// through writeAppError and aborts before any session is created.
	if _, cerr := a.Tasks.Claim(ctx, openCodeDispatchActor, id, openCodeClaimTTL, true); cerr != nil {
		slog.Warn("opencode start: failed to claim task", "task_id", id, "err", cerr)
		writeAppError(w, cerr, "task", id)
		return
	}

	// Best-effort rollback for every failure path below (AC3): release the
	// lease taken above and — once a session exists — delete it. Rollback
	// failures are logged only; they never alter the error already decided.
	releaseClaim := func() {
		if _, rerr := a.Tasks.Release(ctx, openCodeDispatchActor, id, "opencode start failed after claiming — rolling back lease"); rerr != nil {
			slog.Warn("opencode start: failed to release claim on rollback", "task_id", id, "err", rerr)
		}
	}
	var sessionID string // set once the session exists; empty = nothing to delete

	// Step 1: Create OpenCode session
	sessionURLParsed, err := url.Parse(serverURL + "/session")
	if err != nil {
		releaseClaim()
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	q := sessionURLParsed.Query()
	q.Set("directory", dir)
	sessionURLParsed.RawQuery = q.Encode()

	// When a model resolves (non-empty), it is set on the session so all messages
	// use that provider/model, with an optional variant attached to the model map.
	// When empty, the "model" field is omitted entirely and the OpenCode server
	// falls back to its own default model.
	sessionCreateBody := map[string]any{
		"title": "Rävn: " + detail.Task.Title,
		"agent": agentName,
	}
	if modelProviderID != "" {
		modelMap := map[string]string{
			"id":         modelID,
			"providerID": modelProviderID,
		}
		if variant != "" {
			modelMap["variant"] = variant
		}
		sessionCreateBody["model"] = modelMap
	}
	sessionReqBody, err := json.Marshal(sessionCreateBody)
	if err != nil {
		releaseClaim()
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": "failed to encode session request body",
		})
		return
	}

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sessionURLParsed.String(), bytes.NewReader(sessionReqBody))
	if err != nil {
		releaseClaim()
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": fmt.Sprintf("failed to create http request: %v", err),
		})
		return
	}
	createReq.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTPClient.Do(createReq)
	if err != nil {
		slog.Warn("failed to connect to opencode server", "err", err)
		releaseClaim()
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   ErrCodeOpencodeError,
			"message": "failed to connect to opencode server",
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("opencode create session returned non-2xx status", "status", resp.StatusCode, "snippet", string(bodySnippet))
		releaseClaim()
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   ErrCodeOpencodeError,
			"message": fmt.Sprintf("opencode create session returned status %d", resp.StatusCode),
		})
		return
	}

	var sessionResp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil || sessionResp.ID == "" {
		// No usable session ID was returned, so nothing can be deleted; the
		// lease taken above still has to go back.
		releaseClaim()
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   ErrCodeOpencodeError,
			"message": "opencode returned invalid session response",
		})
		return
	}
	sessionID = sessionResp.ID

	// Step 2: Dispatch prompt asynchronously. Every failure from here on runs
	// the FULL rollback (delete the created session AND release the lease).
	promptURLParsed, err := url.Parse(fmt.Sprintf("%s/session/%s/prompt_async", serverURL, sessionID))
	if err != nil {
		a.deleteOpenCodeSession(ctx, serverURL, dir, sessionID)
		releaseClaim()
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": fmt.Sprintf("invalid prompt URL: %v", err),
		})
		return
	}
	pq := promptURLParsed.Query()
	pq.Set("directory", dir)
	promptURLParsed.RawQuery = pq.Encode()

	promptMap := map[string]any{
		"agent": agentName,
		"parts": []map[string]string{
			{
				"type": "text",
				"text": promptText,
			},
		},
	}
	if variant != "" {
		promptMap["variant"] = variant
	}
	promptReqBody, err := json.Marshal(promptMap)
	if err != nil {
		a.deleteOpenCodeSession(ctx, serverURL, dir, sessionID)
		releaseClaim()
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": "failed to encode prompt request body",
		})
		return
	}

	dispatchReq, err := http.NewRequestWithContext(ctx, http.MethodPost, promptURLParsed.String(), bytes.NewReader(promptReqBody))
	if err != nil {
		a.deleteOpenCodeSession(ctx, serverURL, dir, sessionID)
		releaseClaim()
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   ErrCodeInternal,
			"message": fmt.Sprintf("failed to create prompt request: %v", err),
		})
		return
	}
	dispatchReq.Header.Set("Content-Type", "application/json")

	dispatchResp, err := a.HTTPClient.Do(dispatchReq)
	if err != nil {
		slog.Warn("failed to dispatch prompt to opencode", "err", err)
		a.deleteOpenCodeSession(ctx, serverURL, dir, sessionID)
		releaseClaim()
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   ErrCodeOpencodeError,
			"message": "failed to dispatch prompt to opencode",
		})
		return
	}
	defer dispatchResp.Body.Close()

	if dispatchResp.StatusCode < 200 || dispatchResp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(dispatchResp.Body, 1024))
		slog.Warn("opencode prompt_async returned non-2xx status", "status", dispatchResp.StatusCode, "snippet", string(bodySnippet))
		a.deleteOpenCodeSession(ctx, serverURL, dir, sessionID)
		releaseClaim()
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   ErrCodeOpencodeError,
			"message": fmt.Sprintf("opencode prompt_async returned status %d", dispatchResp.StatusCode),
		})
		return
	}

	// Step 3: Attach session URL artifact to task — only AFTER a proven-good
	// dispatch. An attach failure here does NOT roll back: the agent is live
	// and owns the lease, so killing the session over bookkeeping would be
	// worse than surfacing the error (see the doc comment).
	openCodeSessionURL := fmt.Sprintf("%s/session/%s", serverURL, sessionID)
	if _, err := a.Artifacts.Attach(ctx, openCodeDispatchActor, detail.Task.ID, domain.ArtifactURL, openCodeSessionURL, "OpenCode autonomous session"); err != nil {
		writeAppError(w, err, "task", detail.Task.ID)
		return
	}

	// Step 4: Return 200 with session_id, session_url, agent, model, variant.
	// The claim was already acquired (and proven) in the pre-dispatch step —
	// no post-hoc claim and never a 200 for an unclaimed task.
	writeJSON(w, http.StatusOK, openCodeStartResponse{
		SessionID:  sessionID,
		SessionURL: openCodeSessionURL,
		Agent:      agentName,
		Model:      modelRef,
		Variant:    variant,
	})
}

// handleOpenCodeOptions → GET /api/v1/projects/{id}/opencode/options.
// Queries the configured OpenCode server for available agents and models.
func (a *App) handleOpenCodeOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	proj, err := a.Projects.Get(ctx, id)
	if err != nil {
		writeAppError(w, err, "project", id)
		return
	}

	serverURLParam := strings.TrimSpace(r.URL.Query().Get("server_url"))
	var serverURL string
	if serverURLParam != "" {
		serverURL = strings.TrimRight(serverURLParam, "/")
	} else {
		serverURL = resolveOpenCodeServerURL(proj.OpenCodeServerURL)
	}

	dirParam := strings.TrimSpace(r.URL.Query().Get("directory"))
	var dir string
	if dirParam != "" {
		dir = dirParam
	} else {
		dir = resolveOpenCodeDirectory(proj.OpenCodeDirectory)
	}

	if err := application.ValidateOpenCodeServerURL(serverURL); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	if err := application.ValidateOpenCodeDirectory(dir); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": fmt.Sprintf("invalid opencode directory: %v", err),
		})
		return
	}

	resolvedAgent := resolveOpenCodeAgent(nil, proj.OpenCodeAgent)
	resolvedModel := resolveOpenCodeModel(proj.OpenCodeModel)
	resolvedVariant := resolveOpenCodeVariant(nil, proj.OpenCodeVariant)

	agents := make([]openCodeAgentOptionDTO, 0)
	models := make([]openCodeModelOptionDTO, 0)

	// Upstream query 1: GET {serverURL}/agent?directory={dir}
	agentURLParsed, err := url.Parse(serverURL + "/agent")
	if err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}
	aq := agentURLParsed.Query()
	aq.Set("directory", dir)
	agentURLParsed.RawQuery = aq.Encode()

	agentReq, err := http.NewRequestWithContext(ctx, http.MethodGet, agentURLParsed.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}

	agentResp, err := a.HTTPClient.Do(agentReq)
	if err != nil || agentResp.StatusCode < 200 || agentResp.StatusCode >= 300 {
		if agentResp != nil {
			_ = agentResp.Body.Close()
		}
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}
	defer agentResp.Body.Close()

	type rawAgent struct {
		Name   string `json:"name"`
		Mode   string `json:"mode"`
		Hidden *bool  `json:"hidden"`
		Model  *struct {
			ProviderID string `json:"providerID"`
			ModelID    string `json:"modelID"`
			ID         string `json:"id"`
		} `json:"model"`
		Variant *string `json:"variant"`
	}

	var rawAgents []rawAgent
	if err := json.NewDecoder(agentResp.Body).Decode(&rawAgents); err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}

	for _, a := range rawAgents {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		if a.Hidden != nil && *a.Hidden {
			continue
		}
		opt := openCodeAgentOptionDTO{
			Name: name,
			Mode: a.Mode,
		}
		if a.Model != nil {
			mID := a.Model.ModelID
			if mID == "" {
				mID = a.Model.ID
			}
			if a.Model.ProviderID != "" && mID != "" {
				opt.Model = a.Model.ProviderID + "/" + mID
			}
		}
		if a.Variant != nil {
			opt.Variant = *a.Variant
		}
		agents = append(agents, opt)
	}

	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Name == "build" && agents[j].Name != "build" {
			return true
		}
		if agents[i].Name != "build" && agents[j].Name == "build" {
			return false
		}
		return agents[i].Name < agents[j].Name
	})

	// Upstream query 2: GET {serverURL}/config/providers?directory={dir}
	configURLParsed, err := url.Parse(serverURL + "/config/providers")
	if err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}
	cq := configURLParsed.Query()
	cq.Set("directory", dir)
	configURLParsed.RawQuery = cq.Encode()

	configReq, err := http.NewRequestWithContext(ctx, http.MethodGet, configURLParsed.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}

	configResp, err := a.HTTPClient.Do(configReq)
	if err != nil || configResp.StatusCode < 200 || configResp.StatusCode >= 300 {
		if configResp != nil {
			_ = configResp.Body.Close()
		}
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}
	defer configResp.Body.Close()

	var rawConfig struct {
		Providers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				Name     string         `json:"name"`
				Variants map[string]any `json:"variants"`
			} `json:"models"`
		} `json:"providers"`
	}

	if err := json.NewDecoder(configResp.Body).Decode(&rawConfig); err != nil {
		writeJSON(w, http.StatusOK, openCodeOptionsResponse{
			Reachable:       false,
			ResolvedAgent:   resolvedAgent,
			ResolvedModel:   resolvedModel,
			ResolvedVariant: resolvedVariant,
			Agents:          agents,
			Models:          models,
		})
		return
	}

	for _, prov := range rawConfig.Providers {
		provID := strings.TrimSpace(prov.ID)
		if provID == "" {
			continue
		}
		for modelID, modelData := range prov.Models {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			variants := make([]string, 0, len(modelData.Variants))
			for vKey := range modelData.Variants {
				variants = append(variants, vKey)
			}
			sort.Strings(variants)

			models = append(models, openCodeModelOptionDTO{
				Ref:          provID + "/" + modelID,
				ProviderID:   provID,
				ProviderName: prov.Name,
				ModelID:      modelID,
				ModelName:    modelData.Name,
				Variants:     variants,
			})
		}
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].Ref < models[j].Ref
	})

	writeJSON(w, http.StatusOK, openCodeOptionsResponse{
		Reachable:       true,
		ResolvedAgent:   resolvedAgent,
		ResolvedModel:   resolvedModel,
		ResolvedVariant: resolvedVariant,
		Agents:          agents,
		Models:          models,
	})
}
