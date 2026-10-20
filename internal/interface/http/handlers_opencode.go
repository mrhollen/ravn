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
	"sort"
	"strings"
	"time"

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
// setting, the OPENCODE_DIRECTORY environment variable, or defaults to "/home/hollen/Code/ravn".
func resolveOpenCodeDirectory(projectDirectory string) string {
	if s := strings.TrimSpace(projectDirectory); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("OPENCODE_DIRECTORY")); s != "" {
		return s
	}
	return "/home/hollen/Code/ravn"
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

// formatOpenCodePrompt formats the autonomous agent prompt with task details,
// acceptance criteria, and explicit instructions to interact with CrowBan via
// MCP — including clarification that the task has already been claimed on the
// agent's behalf under 'opencode', recommendations to attach test artifacts,
// and instructions to commit and push changes upon completion.
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
	sb.WriteString("Instructions: You are assigned to autonomously work on and complete this task in CrowBan. This task has already been automatically claimed on your behalf under actor 'opencode' before this session started, granting you explicit permission to work on it (you do not need to claim it again, and must not halt if you see the 'opencode' lease; if you refresh or release the lease, use actor 'opencode'). Use the CrowBan MCP tools (or REST API) as you progress: update notes with your plan and progress, complete acceptance criteria using complete_ac as you verify them, and mark the task done when all criteria are met. When tests or verification commands produce output, upload it as evidence with attach_artifact (kind command-output/log/screenshot, ref = URL or data_base64 = base64 file content) so a human can verify your work — encouraged, not required. Once all acceptance criteria are verified and tests pass, commit and push your code changes to the git repository.")
	return sb.String()
}

// handleOpenCodeStart → POST /api/v1/tasks/{id}/opencode/start.
// Dispatches an autonomous OpenCode session for the given task.
func (a *App) handleOpenCodeStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req openCodeStartReq
	if r.Body != nil && r.Body != http.NoBody {
		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid input",
				"message": "failed to read request body",
			})
			return
		}
		if len(bytes.TrimSpace(bodyBytes)) > 0 {
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, map[string]any{
					"error":   "invalid input",
					"message": fmt.Sprintf("invalid json body: %v", err),
				})
				return
			}
		}
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

	agentName := resolveOpenCodeAgent(req.Agent, proj.OpenCodeAgent)
	if err := domain.ValidateOpenCodeAgent(agentName); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
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
				"error":   "invalid input",
				"message": fmt.Sprintf("invalid opencode model: %v", err),
			})
			return
		}
	}

	variant := resolveOpenCodeVariant(req.Variant, proj.OpenCodeVariant)
	if variant != "" {
		if err := domain.ValidateOpenCodeVariant(variant); err != nil {
			writeJSONError(w, http.StatusBadRequest, map[string]any{
				"error":   "invalid input",
				"message": fmt.Sprintf("invalid opencode variant: %v", err),
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

	// Step 5: Return 200 with session_id, session_url, agent, model, variant
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
			"error":   "invalid input",
			"message": fmt.Sprintf("invalid opencode server URL: %v", err),
		})
		return
	}
	if err := application.ValidateOpenCodeDirectory(dir); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
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
