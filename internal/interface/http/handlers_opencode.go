package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
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
// setting, the OPENCODE_DIRECTORY environment variable, or defaults to "/home/hollen/Code/crowban".
func resolveOpenCodeDirectory(projectDirectory string) string {
	if s := strings.TrimSpace(projectDirectory); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_DIRECTORY")); s != "" {
		return s
	}
	return "/home/hollen/Code/crowban"
}

// resolveOpenCodeModel resolves the OpenCode model reference from the project
// setting, the OPENCODE_MODEL environment variable, or defaults to
// "llama.cpp/Qwen Medium Long Context". When a server's fallback model requires
// an API key the dispatched session dies with an Authentication Error immediately,
// so a local working model is used as default.
func resolveOpenCodeModel(projectModel string) string {
	if s := strings.TrimSpace(projectModel); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_MODEL")); s != "" {
		return s
	}
	return "llama.cpp/Qwen Medium Long Context"
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

// formatOpenCodePrompt formats the autonomous agent prompt with task details,
// acceptance criteria, and explicit instructions to interact with CrowBan via
// MCP — including the encouraged (not required) recommendation to attach test
// artifacts as evidence for human verification.
func formatOpenCodePrompt(task domain.Task, acs []domain.AcceptanceCriterion) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task ID: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	sb.WriteString(fmt.Sprintf("Description: %s\n", task.Description))
	sb.WriteString("Acceptance Criteria:\n")
	if len(acs) == 0 {
		sb.WriteString("(None)\n")
	} else {
		for _, ac := range acs {
			box := "[ ]"
			if ac.Done {
				box = "[x]"
			}
			sb.WriteString(fmt.Sprintf("- %s Seq %d: %s\n", box, ac.Seq, ac.Text))
		}
	}
	sb.WriteString("Instructions: You are assigned to autonomously work on and complete this task in CrowBan. Use the CrowBan MCP tools (or REST API) as you progress: claim the task, update notes with your plan and progress, complete acceptance criteria using complete_ac as you verify them, and mark the task done when all criteria are met. When tests or verification commands produce output, upload it as evidence with attach_artifact (kind command-output/log/screenshot, ref = file path or URL) so a human can verify your work — encouraged, not required.")
	return sb.String()
}

// handleOpenCodeStart → POST /api/v1/tasks/{id}/opencode/start.
// Dispatches an autonomous OpenCode session with agent 'build' for the given task.
func (a *App) handleOpenCodeStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	detail, err := a.Tasks.GetDetail(ctx, id, 0, 0)
	if err != nil {
		writeAppError(w, err, "task", id)
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
			"error":   "invalid input",
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	dir := resolveOpenCodeDirectory(proj.OpenCodeDirectory)
	if err := application.ValidateOpenCodeDirectory(dir); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": fmt.Sprintf("invalid opencode directory: %v", err),
		})
		return
	}
	modelRef := resolveOpenCodeModel(proj.OpenCodeModel)
	var modelProviderID, modelID string
	if modelRef != "" {
		modelProviderID, modelID, err = parseOpenCodeModel(modelRef)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid input",
				"message": fmt.Sprintf("invalid opencode model: %v", err),
			})
			return
		}
	}
	promptText := formatOpenCodePrompt(detail.Task, detail.AcceptanceCriteria)

	// Step 1: Create OpenCode session
	sessionURLParsed, err := url.Parse(serverURL + "/session")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	q := sessionURLParsed.Query()
	q.Set("directory", dir)
	sessionURLParsed.RawQuery = q.Encode()

	// The model is set on the session (not just the prompt): sessions created
	// without one fall back to the OpenCode server's default model, which on
	// some hosts points at a key-gated provider and dies with a 401
	// "Authentication Error" before doing any work.
	sessionCreateBody := map[string]any{
		"title": "CrowBan: " + detail.Task.Title,
		"agent": "build",
	}
	if modelProviderID != "" {
		sessionCreateBody["model"] = map[string]string{
			"id":         modelID,
			"providerID": modelProviderID,
		}
	}
	sessionReqBody, err := json.Marshal(sessionCreateBody)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": "failed to encode session request body",
		})
		return
	}

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sessionURLParsed.String(), bytes.NewReader(sessionReqBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": fmt.Sprintf("failed to create http request: %v", err),
		})
		return
	}
	createReq.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTPClient.Do(createReq)
	if err != nil {
		slog.Warn("failed to connect to opencode server", "err", err)
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   "opencode_error",
			"message": "failed to connect to opencode server",
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Warn("opencode create session returned non-2xx status", "status", resp.StatusCode, "snippet", string(bodySnippet))
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   "opencode_error",
			"message": fmt.Sprintf("opencode create session returned status %d", resp.StatusCode),
		})
		return
	}

	var sessionResp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil || sessionResp.ID == "" {
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   "opencode_error",
			"message": "opencode returned invalid session response",
		})
		return
	}
	sessionID := sessionResp.ID

	// Step 2: Dispatch prompt asynchronously
	promptURLParsed, err := url.Parse(fmt.Sprintf("%s/session/%s/prompt_async", serverURL, sessionID))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": fmt.Sprintf("invalid prompt URL: %v", err),
		})
		return
	}
	pq := promptURLParsed.Query()
	pq.Set("directory", dir)
	promptURLParsed.RawQuery = pq.Encode()

	promptReqBody, err := json.Marshal(map[string]any{
		"agent": "build",
		"parts": []map[string]string{
			{
				"type": "text",
				"text": promptText,
			},
		},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": "failed to encode prompt request body",
		})
		return
	}

	dispatchReq, err := http.NewRequestWithContext(ctx, http.MethodPost, promptURLParsed.String(), bytes.NewReader(promptReqBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, map[string]any{
			"error":   "internal",
			"message": fmt.Sprintf("failed to create prompt request: %v", err),
		})
		return
	}
	dispatchReq.Header.Set("Content-Type", "application/json")

	dispatchResp, err := a.HTTPClient.Do(dispatchReq)
	if err != nil {
		slog.Warn("failed to dispatch prompt to opencode", "err", err)
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   "opencode_error",
			"message": "failed to dispatch prompt to opencode",
		})
		return
	}
	defer dispatchResp.Body.Close()

	if dispatchResp.StatusCode < 200 || dispatchResp.StatusCode >= 300 {
		bodySnippet, _ := io.ReadAll(io.LimitReader(dispatchResp.Body, 1024))
		slog.Warn("opencode prompt_async returned non-2xx status", "status", dispatchResp.StatusCode, "snippet", string(bodySnippet))
		writeJSONError(w, http.StatusBadGateway, map[string]any{
			"error":   "opencode_error",
			"message": fmt.Sprintf("opencode prompt_async returned status %d", dispatchResp.StatusCode),
		})
		return
	}

	// Step 3: Attach session URL artifact to task
	openCodeSessionURL := fmt.Sprintf("%s/session/%s", serverURL, sessionID)
	opencodeActor := domain.Actor{Type: domain.ActorAgent, Name: "opencode"}
	if _, err := a.Artifacts.Attach(ctx, opencodeActor, detail.Task.ID, domain.ArtifactURL, openCodeSessionURL, "OpenCode autonomous session"); err != nil {
		writeAppError(w, err, "task", detail.Task.ID)
		return
	}

	// Step 4: If no ACTIVE lease is attached (unclaimed or zombie), claim it for opencode with TTL 3600.
	// No blocker override: a task with open blockers fails the gate and simply
	// stays unclaimed here (the error is deliberately swallowed, as before).
	now := time.Now()
	isClaimed := detail.Task.IsClaimActive(now) // liveness check; an expired lease is takeover-eligible by Claim
	if !isClaimed {
		_, _ = a.Tasks.Claim(ctx, opencodeActor, detail.Task.ID, 3600, false)
	}

	// Step 5: Return 200 with session_id and session_url
	writeJSON(w, http.StatusOK, openCodeStartResponse{
		SessionID:  sessionID,
		SessionURL: openCodeSessionURL,
	})
}
