package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpdateProject_OpenCodeSettings(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	stk := newStack(t)
	p := createProject(t, stk, "Project OpenCode Settings")

	// 1. Initial state has empty opencode fields
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects/%s = %d, want 200", p.ID, code)
	}
	got := mustDecode[projectDTO](t, body)
	if got.OpenCodeServerURL != "" || got.OpenCodeDirectory != "" || got.OpenCodeModel != "" || got.OpenCodeAgent != "" || got.OpenCodeVariant != "" {
		t.Fatalf("initial opencode settings not empty: url=%q dir=%q model=%q agent=%q variant=%q", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel, got.OpenCodeAgent, got.OpenCodeVariant)
	}

	// 2. PUT updates opencode_server_url, opencode_directory, opencode_model, opencode_agent, opencode_variant
	serverURL := "http://localhost:4444"
	dir := "/home/test/workspace"
	modelRef := "llama.cpp/Qwen Nano"
	agentName := "custom-builder"
	variantName := "thinking"
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": serverURL,
		"opencode_directory":  dir,
		"opencode_model":      modelRef,
		"opencode_agent":      agentName,
		"opencode_variant":    variantName,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /projects/%s = %d, want 200 (body %s)", p.ID, code, string(body))
	}
	updated := mustDecode[projectDTO](t, body)
	if updated.OpenCodeServerURL != serverURL || updated.OpenCodeDirectory != dir || updated.OpenCodeModel != modelRef || updated.OpenCodeAgent != agentName || updated.OpenCodeVariant != variantName {
		t.Fatalf("PUT response mismatch: url=%q dir=%q model=%q agent=%q variant=%q, want url=%q dir=%q model=%q agent=%q variant=%q", updated.OpenCodeServerURL, updated.OpenCodeDirectory, updated.OpenCodeModel, updated.OpenCodeAgent, updated.OpenCodeVariant, serverURL, dir, modelRef, agentName, variantName)
	}

	// 3. GET confirms persistence
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET after PUT /projects/%s = %d", p.ID, code)
	}
	got = mustDecode[projectDTO](t, body)
	if got.OpenCodeServerURL != serverURL || got.OpenCodeDirectory != dir || got.OpenCodeModel != modelRef || got.OpenCodeAgent != agentName || got.OpenCodeVariant != variantName {
		t.Fatalf("persisted settings mismatch: url=%q dir=%q model=%q agent=%q variant=%q", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel, got.OpenCodeAgent, got.OpenCodeVariant)
	}

	// 4. PUT clears settings with empty string
	empty := ""
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": empty,
		"opencode_directory":  empty,
		"opencode_model":      empty,
		"opencode_agent":      empty,
		"opencode_variant":    empty,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT clear settings = %d", code)
	}
	cleared := mustDecode[projectDTO](t, body)
	if cleared.OpenCodeServerURL != "" || cleared.OpenCodeDirectory != "" || cleared.OpenCodeModel != "" || cleared.OpenCodeAgent != "" || cleared.OpenCodeVariant != "" {
		t.Fatalf("settings not cleared: url=%q dir=%q model=%q agent=%q variant=%q", cleared.OpenCodeServerURL, cleared.OpenCodeDirectory, cleared.OpenCodeModel, cleared.OpenCodeAgent, cleared.OpenCodeVariant)
	}
}

func TestOpenCodeStart_Success(t *testing.T) {
	var sessionCreated atomic.Bool
	var promptDispatched atomic.Bool
	var receivedPromptText string
	var receivedDirectory string
	var receivedModelProviderID string
	var receivedModelID string

	expectedSessionID := "test-session-12345"

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			receivedDirectory = r.URL.Query().Get("directory")
			var req struct {
				Title string `json:"title"`
				Agent string `json:"agent"`
				Model *struct {
					ID         string `json:"id"`
					ProviderID string `json:"providerID"`
				} `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Agent != "build" {
				http.Error(w, "expected agent 'build'", http.StatusBadRequest)
				return
			}
			if !strings.HasPrefix(req.Title, "Rävn: ") {
				http.Error(w, "expected title prefix 'Rävn: '", http.StatusBadRequest)
				return
			}
			if req.Model != nil {
				receivedModelProviderID = req.Model.ProviderID
				receivedModelID = req.Model.ID
			}
			sessionCreated.Store(true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    expectedSessionID,
				"title": req.Title,
			})

		case r.Method == http.MethodPost && r.URL.Path == "/session/"+expectedSessionID+"/prompt_async":
			if r.URL.Query().Get("directory") != receivedDirectory {
				http.Error(w, "directory mismatch", http.StatusBadRequest)
				return
			}
			var req struct {
				Agent string `json:"agent"`
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Agent != "build" {
				http.Error(w, "expected agent 'build'", http.StatusBadRequest)
				return
			}
			if len(req.Parts) == 0 || req.Parts[0].Type != "text" {
				http.Error(w, "missing text part", http.StatusBadRequest)
				return
			}
			receivedPromptText = req.Parts[0].Text
			promptDispatched.Store(true)
			w.WriteHeader(http.StatusAccepted)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	stk := newStack(t)
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	stk.App.HTTPClient = mockServer.Client()

	// 1. Create project with opencode settings pointing to mock server
	p := createProject(t, stk, "OpenCode Session Test")
	customDir := "/home/test/my-repo"
	modelRef := "llama.cpp/Qwen Nano"
	code, _ := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
		"opencode_directory":  customDir,
		"opencode_model":      modelRef,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT project opencode settings = %d", code)
	}

	// 2. Create task with ACs
	task := makeTask(t, stk, p.ID, "Implement cool feature", map[string]any{
		"acceptance_criteria": []string{
			"Feature works on Linux",
			"Unit tests pass 100%",
		},
	})
	taskID := task.ID

	// 3. Trigger OpenCode start
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/opencode/start", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /tasks/%s/opencode/start = %d, want 200 (body %s)", taskID, code, string(body))
	}

	var resp openCodeStartResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if resp.SessionID != expectedSessionID {
		t.Errorf("session_id = %q, want %q", resp.SessionID, expectedSessionID)
	}
	expectedSessionURL := fmt.Sprintf("%s/session/%s", mockServer.URL, expectedSessionID)
	if resp.SessionURL != expectedSessionURL {
		t.Errorf("session_url = %q, want %q", resp.SessionURL, expectedSessionURL)
	}

	// 4. Verify OpenCode API interactions
	if !sessionCreated.Load() {
		t.Error("OpenCode session was not created")
	}
	if !promptDispatched.Load() {
		t.Error("OpenCode prompt was not dispatched")
	}
	if receivedDirectory != customDir {
		t.Errorf("received directory = %q, want %q", receivedDirectory, customDir)
	}
	// The resolved model must ride on the session-create body so the dispatched
	// session doesn't fall back to the server default (the 401 auth-error bug).
	if receivedModelProviderID != "llama.cpp" {
		t.Errorf("received model providerID = %q, want llama.cpp", receivedModelProviderID)
	}
	if receivedModelID != "Qwen Nano" {
		t.Errorf("received model ID = %q, want Qwen Nano", receivedModelID)
	}

	// 5. Verify Prompt contents
	if !strings.Contains(receivedPromptText, "Task ID: "+taskID) {
		t.Errorf("prompt missing task ID: %s", receivedPromptText)
	}
	if !strings.Contains(receivedPromptText, "Title: Implement cool feature") {
		t.Errorf("prompt missing task title: %s", receivedPromptText)
	}
	if !strings.Contains(receivedPromptText, "Feature works on Linux") || !strings.Contains(receivedPromptText, "Unit tests pass 100%") {
		t.Errorf("prompt missing acceptance criteria: %s", receivedPromptText)
	}
	expectedInstructions := "Instructions: You are assigned to autonomously work on and complete this task in Rävn. This task has already been automatically claimed on your behalf under actor 'opencode' before this session started, granting you explicit permission to work on it (you do not need to claim it again, and must not halt if you see the 'opencode' lease; if you refresh or release the lease, use actor 'opencode'). Use the Rävn MCP tools (or REST API) as you progress: update notes with your plan and progress, complete acceptance criteria using complete_ac as you verify them, and mark the task done when all criteria are met. When tests or verification commands produce output, upload it as evidence with attach_artifact (kind command-output/log/screenshot, ref = URL or data_base64 = base64 file content) so a human can verify your work — encouraged, not required. Once all acceptance criteria are verified and tests pass, commit and push your code changes to the git repository."
	if !strings.Contains(receivedPromptText, expectedInstructions) {
		t.Errorf("prompt missing required instructions: %s", receivedPromptText)
	}

	// 6. Verify task detail has artifact and claim
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+taskID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d", taskID, code)
	}
	detail := mustDecode[taskDetailDTO](t, body)

	// Artifact check
	if len(detail.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(detail.Artifacts))
	}
	art := detail.Artifacts[0]
	if art.Kind != "url" {
		t.Errorf("artifact kind = %q, want 'url'", art.Kind)
	}
	if art.Ref != expectedSessionURL {
		t.Errorf("artifact ref = %q, want %q", art.Ref, expectedSessionURL)
	}
	if art.Summary != "OpenCode autonomous session" {
		t.Errorf("artifact summary = %q, want 'OpenCode autonomous session'", art.Summary)
	}

	// Claim check (since task was unclaimed, now claimed by opencode with TTL 3600)
	if detail.ClaimedBy == nil || detail.ClaimedBy.Name != "opencode" || detail.ClaimedBy.Type != "agent" {
		t.Errorf("claimed_by = %+v, want agent 'opencode'", detail.ClaimedBy)
	}
	if detail.Status != "doing" {
		t.Errorf("status = %q, want 'doing'", detail.Status)
	}
	if detail.ClaimExpiresAt == nil {
		t.Error("claim_expires_at is nil")
	}
}

func TestOpenCodeStart_DoesNotOverwriteExistingClaim(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sess-existing-claim"})
	}))
	defer mockServer.Close()

	stk := newStack(t)
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	stk.App.HTTPClient = mockServer.Client()

	p := createProject(t, stk, "Claim Preservation Test")
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
	})

	taskID := makeTask(t, stk, p.ID, "Task already claimed", nil).ID

	// Claim task as agent-backend
	code, _ := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/claim", map[string]any{
		"actor":   map[string]any{"type": "agent", "name": "agent-backend"},
		"ttl_sec": 900,
	})
	if code != http.StatusOK {
		t.Fatalf("POST /tasks/%s/claim = %d", taskID, code)
	}

	// Start OpenCode
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/opencode/start", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /tasks/%s/opencode/start = %d (body %s)", taskID, code, string(body))
	}

	// Verify task detail: claim should STILL belong to agent-backend
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+taskID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d", taskID, code)
	}
	detail := mustDecode[taskDetailDTO](t, body)
	if detail.ClaimedBy == nil || detail.ClaimedBy.Name != "agent-backend" {
		t.Errorf("claim was overwritten! ClaimedBy = %+v, want agent-backend", detail.ClaimedBy)
	}
	// Artifact should still be attached
	if len(detail.Artifacts) != 1 {
		t.Errorf("artifacts count = %d, want 1", len(detail.Artifacts))
	}
}

func TestOpenCodeStart_ResolutionDefaults(t *testing.T) {
	// 1. Defaults when project and env vars are empty
	t.Setenv("OPENCODE_SERVER_URL", "")
	t.Setenv("OPENCODE_DIRECTORY", "")
	t.Setenv("OPENCODE_MODEL", "")
	if url := resolveOpenCodeServerURL(""); url != "http://localhost:4444" {
		t.Errorf("default server URL = %q, want http://localhost:4444", url)
	}
	if dir := resolveOpenCodeDirectory(""); dir != "/home/hollen/Code/ravn" {
		t.Errorf("default directory = %q, want /home/hollen/Code/ravn", dir)
	}
	if model := resolveOpenCodeModel(""); model != "llama.cpp/Qwen Medium Long Context" {
		t.Errorf("default model = %q, want llama.cpp/Qwen Medium Long Context", model)
	}

	// 2. Env vars when project settings are empty
	t.Setenv("OPENCODE_SERVER_URL", "http://env-host:9999/")
	t.Setenv("OPENCODE_DIRECTORY", "/env/workdir")
	t.Setenv("OPENCODE_MODEL", "llama.cpp/Qwen Nano")
	if url := resolveOpenCodeServerURL(""); url != "http://env-host:9999" {
		t.Errorf("env server URL = %q, want http://env-host:9999", url)
	}
	if dir := resolveOpenCodeDirectory("/env/workdir"); dir != "/env/workdir" {
		t.Errorf("env directory = %q, want /env/workdir", dir)
	}
	if model := resolveOpenCodeModel(""); model != "llama.cpp/Qwen Nano" {
		t.Errorf("env model = %q, want llama.cpp/Qwen Nano", model)
	}

	// 3. Project settings take precedence over env vars
	if url := resolveOpenCodeServerURL("http://proj-host:8888/"); url != "http://proj-host:8888" {
		t.Errorf("project server URL = %q, want http://proj-host:8888", url)
	}
	if dir := resolveOpenCodeDirectory("/proj/workdir"); dir != "/proj/workdir" {
		t.Errorf("project directory = %q, want /proj/workdir", dir)
	}
	if model := resolveOpenCodeModel("openrouter/deepseek/deepseek-chat-v3-0324"); model != "openrouter/deepseek/deepseek-chat-v3-0324" {
		t.Errorf("project model = %q, want the project value", model)
	}
}

func TestParseOpenCodeModel(t *testing.T) {
	cases := []struct {
		ref            string
		wantProviderID string
		wantModelID    string
		wantErr        bool
	}{
		{ref: "llama.cpp/Qwen Nano", wantProviderID: "llama.cpp", wantModelID: "Qwen Nano"},
		{ref: "openrouter/deepseek/deepseek-chat-v3-0324", wantProviderID: "openrouter", wantModelID: "deepseek/deepseek-chat-v3-0324"},
		{ref: "no-slash", wantErr: true},
		{ref: "/missing-provider", wantErr: true},
		{ref: "provider/", wantErr: true},
	}
	for _, tc := range cases {
		providerID, modelID, err := parseOpenCodeModel(tc.ref)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseOpenCodeModel(%q) = (%q, %q), want error", tc.ref, providerID, modelID)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOpenCodeModel(%q) unexpected error: %v", tc.ref, err)
			continue
		}
		if providerID != tc.wantProviderID || modelID != tc.wantModelID {
			t.Errorf("parseOpenCodeModel(%q) = (%q, %q), want (%q, %q)", tc.ref, providerID, modelID, tc.wantProviderID, tc.wantModelID)
		}
	}
}

func TestOpenCodeStart_InvalidModelRef(t *testing.T) {
	// OPENCODE_ALLOW_PRIVATE_IPS lets the dispatch handler's URL check pass the
	// localhost default so the test reaches the model parse guard. The env-var
	// model fallback (OPENCODE_MODEL) feeds parseOpenCodeModel exactly like a
	// stored project value would — no HTTP contact happens: parsing fails first.
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	t.Setenv("OPENCODE_MODEL", "not-a-model-ref")
	stk := newStack(t)

	p := createProject(t, stk, "Invalid Model Project")

	// 1. The update write path rejects the value at save time (the whole PUT is
	// atomic, so nothing is stored).
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":          actor("admin"),
		"opencode_model": "not-a-model-ref",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT with malformed opencode_model = %d, want 400 (body %s)", code, string(body))
	}
	if !strings.Contains(string(body), "providerID/modelID") {
		t.Errorf("PUT error body missing model-ref rule: %s", string(body))
	}
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects/%s = %d, want 200 (body %s)", p.ID, code, string(body))
	}
	stored := mustDecode[projectDTO](t, body)
	if stored.OpenCodeModel != "" {
		t.Errorf("rejected model must not be stored: got %q", stored.OpenCodeModel)
	}

	// 2. Dispatch still guards the env-var fallback for values that bypass the
	// update path (legacy rows / OPENCODE_MODEL).
	taskID := makeTask(t, stk, p.ID, "Task with bad model ref", nil).ID

	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/opencode/start", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("POST with malformed opencode model fallback = %d, want 400 (body %s)", code, string(body))
	}
	if !strings.Contains(string(body), "invalid opencode model") {
		t.Errorf("error body missing 'invalid opencode model': %s", string(body))
	}
}

func TestOpenCodeStart_ErrorHandling(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	stk := newStack(t)

	// 1. Task not found -> 404
	code, _ := doReq(t, stk.Handler, "POST", "/api/v1/tasks/01HNONEXISTENT00000000000000/opencode/start", nil)
	if code != http.StatusNotFound {
		t.Fatalf("POST non-existent task = %d, want 404", code)
	}

	// 2. OpenCode server returns 500 on create session -> 502 Bad Gateway
	mockFailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server crashed with sensitive stack trace", http.StatusInternalServerError)
	}))
	defer mockFailServer.Close()

	stk.App.HTTPClient = mockFailServer.Client()

	p := createProject(t, stk, "Error Handling Project")
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockFailServer.URL,
	})
	taskID := makeTask(t, stk, p.ID, "Task for error test", nil).ID

	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/opencode/start", nil)
	if code != http.StatusBadGateway {
		t.Fatalf("POST with failing OpenCode server = %d, want 502 (body %s)", code, string(body))
	}
	if strings.Contains(string(body), "sensitive stack trace") {
		t.Fatalf("502 response leaked body snippet from upstream: %s", string(body))
	}
}

func TestOpenCode_SSRFProtection(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "SSRF Project")

	// 1. Prohibited cloud metadata
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": "http://169.254.169.254/latest/meta-data/",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT metadata IP = %d, want 400 (body %s)", code, string(body))
	}
	if !strings.Contains(string(body), "prohibited metadata") {
		t.Errorf("expected prohibited metadata error, got %s", string(body))
	}

	// 2. Private IP rejected by default
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": "http://10.0.0.5:4444",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT unconfigured private IP = %d, want 400 (body %s)", code, string(body))
	}

	// 3. Private IP allowed when in OPENCODE_ALLOWED_HOSTS
	t.Setenv("OPENCODE_ALLOWED_HOSTS", "10.0.0.5:4444")
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": "http://10.0.0.5:4444",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT allowlisted private IP = %d, want 200 (body %s)", code, string(body))
	}

	// 4. Invalid scheme rejected
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": "file:///etc/passwd",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT file:// scheme = %d, want 400 (body %s)", code, string(body))
	}
}

func TestOpenCode_PathTraversalProtection(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Path Traversal Project")

	// 1. Directory with .. rejected
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":              actor("admin"),
		"opencode_directory": "../../etc",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT directory with .. = %d, want 400 (body %s)", code, string(body))
	}
	if !strings.Contains(string(body), "path traversal") {
		t.Errorf("expected path traversal error, got %s", string(body))
	}

	// 2. Relative directory rejected
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":              actor("admin"),
		"opencode_directory": "relative/path",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT relative directory = %d, want 400 (body %s)", code, string(body))
	}

	// 3. Clean absolute directory accepted
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":              actor("admin"),
		"opencode_directory": "/home/test/workspace",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT absolute directory = %d, want 200 (body %s)", code, string(body))
	}
}

func TestOpenCodeStart_PromptAsyncErrorNoLeakage(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "session-xyz"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/prompt_async"):
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("DATABASE_CONNECTION_ERROR_SECRET_KEY=supersecret123"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	stk := newStack(t)
	stk.App.HTTPClient = mockServer.Client()

	p := createProject(t, stk, "Leakage Project")
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
	})
	taskID := makeTask(t, stk, p.ID, "Prompt Fail Task", nil).ID

	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskID+"/opencode/start", nil)
	if code != http.StatusBadGateway {
		t.Fatalf("POST prompt fail = %d, want 502 (body %s)", code, string(body))
	}
	if strings.Contains(string(body), "supersecret123") {
		t.Fatalf("502 response leaked body snippet from prompt_async: %s", string(body))
	}
}

func TestOpenCodeStart_ResolutionOrder(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")

	var lastSessionBody map[string]any
	var lastPromptBody map[string]any

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			lastSessionBody = m
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "sess-res-123",
				"title": m["title"],
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/prompt_async"):
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			lastPromptBody = m
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	stk := newStack(t)
	stk.App.HTTPClient = mockServer.Client()

	p := createProject(t, stk, "Resolution Project")
	// Set server URL so start can connect
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
	})

	// 1. Built-in defaults (no request body, no project settings, no env vars)
	t.Run("built-in defaults", func(t *testing.T) {
		t.Setenv("OPENCODE_AGENT", "")
		t.Setenv("OPENCODE_MODEL", "")
		t.Setenv("OPENCODE_VARIANT", "")

		task := makeTask(t, stk, p.ID, "Task 1", nil)
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/opencode/start", nil)
		if code != http.StatusOK {
			t.Fatalf("POST start = %d: %s", code, string(body))
		}
		var resp openCodeStartResponse
		_ = json.Unmarshal(body, &resp)
		if resp.Agent != "build" {
			t.Errorf("resp.Agent = %q, want build", resp.Agent)
		}
		if resp.Model != "llama.cpp/Qwen Medium Long Context" {
			t.Errorf("resp.Model = %q, want llama.cpp/Qwen Medium Long Context", resp.Model)
		}
		if resp.Variant != "" {
			t.Errorf("resp.Variant = %q, want empty", resp.Variant)
		}

		if lastSessionBody["agent"] != "build" {
			t.Errorf("session agent = %v, want build", lastSessionBody["agent"])
		}
		modelMap, ok := lastSessionBody["model"].(map[string]any)
		if !ok || modelMap["providerID"] != "llama.cpp" || modelMap["id"] != "Qwen Medium Long Context" {
			t.Errorf("session model = %+v", lastSessionBody["model"])
		}
		if _, hasVariant := modelMap["variant"]; hasVariant {
			t.Errorf("session model should not have variant: %+v", modelMap)
		}

		if lastPromptBody["agent"] != "build" {
			t.Errorf("prompt agent = %v, want build", lastPromptBody["agent"])
		}
		if _, hasVariant := lastPromptBody["variant"]; hasVariant {
			t.Errorf("prompt should not have variant: %+v", lastPromptBody)
		}
	})

	// 2. Env var fallback
	t.Run("env var fallback", func(t *testing.T) {
		t.Setenv("OPENCODE_AGENT", "env-agent")
		t.Setenv("OPENCODE_MODEL", "env-provider/env-model")
		t.Setenv("OPENCODE_VARIANT", "env-variant")

		task := makeTask(t, stk, p.ID, "Task 2", nil)
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/opencode/start", map[string]any{})
		if code != http.StatusOK {
			t.Fatalf("POST start = %d: %s", code, string(body))
		}
		var resp openCodeStartResponse
		_ = json.Unmarshal(body, &resp)
		if resp.Agent != "env-agent" || resp.Model != "env-provider/env-model" || resp.Variant != "env-variant" {
			t.Errorf("resp mismatch: got agent=%q model=%q variant=%q", resp.Agent, resp.Model, resp.Variant)
		}

		if lastSessionBody["agent"] != "env-agent" {
			t.Errorf("session agent = %v, want env-agent", lastSessionBody["agent"])
		}
		modelMap, ok := lastSessionBody["model"].(map[string]any)
		if !ok || modelMap["variant"] != "env-variant" || modelMap["providerID"] != "env-provider" || modelMap["id"] != "env-model" {
			t.Errorf("session model = %+v", lastSessionBody["model"])
		}
		if lastPromptBody["agent"] != "env-agent" || lastPromptBody["variant"] != "env-variant" {
			t.Errorf("prompt mismatch: %+v", lastPromptBody)
		}
	})

	// 3. Project settings override env vars
	t.Run("project settings override env vars", func(t *testing.T) {
		t.Setenv("OPENCODE_AGENT", "env-agent")
		t.Setenv("OPENCODE_MODEL", "env-provider/env-model")
		t.Setenv("OPENCODE_VARIANT", "env-variant")

		_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
			"actor":            actor("admin"),
			"opencode_agent":   "proj-agent",
			"opencode_model":   "proj-provider/proj-model",
			"opencode_variant": "proj-variant",
		})

		task := makeTask(t, stk, p.ID, "Task 3", nil)
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/opencode/start", nil)
		if code != http.StatusOK {
			t.Fatalf("POST start = %d: %s", code, string(body))
		}
		var resp openCodeStartResponse
		_ = json.Unmarshal(body, &resp)
		if resp.Agent != "proj-agent" || resp.Model != "proj-provider/proj-model" || resp.Variant != "proj-variant" {
			t.Errorf("resp mismatch: got agent=%q model=%q variant=%q", resp.Agent, resp.Model, resp.Variant)
		}

		if lastSessionBody["agent"] != "proj-agent" {
			t.Errorf("session agent = %v, want proj-agent", lastSessionBody["agent"])
		}
		modelMap := lastSessionBody["model"].(map[string]any)
		if modelMap["variant"] != "proj-variant" || modelMap["providerID"] != "proj-provider" || modelMap["id"] != "proj-model" {
			t.Errorf("session model = %+v", modelMap)
		}
		if lastPromptBody["agent"] != "proj-agent" || lastPromptBody["variant"] != "proj-variant" {
			t.Errorf("prompt mismatch: %+v", lastPromptBody)
		}
	})

	// 4. Request body override
	t.Run("request body override", func(t *testing.T) {
		task := makeTask(t, stk, p.ID, "Task 4", nil)
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/opencode/start", map[string]any{
			"agent":   "req-agent",
			"model":   "req-provider/req-model",
			"variant": "req-variant",
		})
		if code != http.StatusOK {
			t.Fatalf("POST start = %d: %s", code, string(body))
		}
		var resp openCodeStartResponse
		_ = json.Unmarshal(body, &resp)
		if resp.Agent != "req-agent" || resp.Model != "req-provider/req-model" || resp.Variant != "req-variant" {
			t.Errorf("resp mismatch: got agent=%q model=%q variant=%q", resp.Agent, resp.Model, resp.Variant)
		}

		if lastSessionBody["agent"] != "req-agent" {
			t.Errorf("session agent = %v, want req-agent", lastSessionBody["agent"])
		}
		modelMap := lastSessionBody["model"].(map[string]any)
		if modelMap["variant"] != "req-variant" || modelMap["providerID"] != "req-provider" || modelMap["id"] != "req-model" {
			t.Errorf("session model = %+v", modelMap)
		}
		if lastPromptBody["agent"] != "req-agent" || lastPromptBody["variant"] != "req-variant" {
			t.Errorf("prompt mismatch: %+v", lastPromptBody)
		}
	})

	// 5. Explicit empty-string variant override clears project default variant
	t.Run("explicit empty-string variant override clears project default", func(t *testing.T) {
		task := makeTask(t, stk, p.ID, "Task 5", nil)
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/opencode/start", map[string]any{
			"variant": "",
		})
		if code != http.StatusOK {
			t.Fatalf("POST start = %d: %s", code, string(body))
		}
		var resp openCodeStartResponse
		_ = json.Unmarshal(body, &resp)
		if resp.Variant != "" {
			t.Errorf("resp.Variant = %q, want empty", resp.Variant)
		}

		modelMap := lastSessionBody["model"].(map[string]any)
		if _, hasVariant := modelMap["variant"]; hasVariant {
			t.Errorf("session model should not have variant when explicitly cleared: %+v", modelMap)
		}
		if _, hasVariant := lastPromptBody["variant"]; hasVariant {
			t.Errorf("prompt should not have variant when explicitly cleared: %+v", lastPromptBody)
		}
	})
}

func TestOpenCodeOptions_Endpoints(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")

	hiddenTrue := true
	mockAgents := []map[string]any{
		{"name": "build", "mode": "primary", "model": map[string]string{"providerID": "openai", "modelID": "gpt-4o"}},
		{"name": "explore", "mode": "subagent", "hidden": &hiddenTrue},
		{"name": "", "mode": "invalid"},
		{"name": "coder", "mode": "primary", "model": map[string]string{"providerID": "anthropic", "id": "claude-3-7-sonnet"}},
		{"name": "architect", "mode": "subagent", "variant": "deep"},
	}

	mockConfig := map[string]any{
		"providers": []map[string]any{
			{
				"id":   "openai",
				"name": "OpenAI",
				"models": map[string]any{
					"gpt-4o": map[string]any{
						"name":     "GPT-4o",
						"variants": map[string]any{"low": nil, "high": nil},
					},
					"o3-mini": map[string]any{
						"name":     "o3-mini",
						"variants": map[string]any{},
					},
				},
			},
			{
				"id":   "anthropic",
				"name": "Anthropic",
				"models": map[string]any{
					"claude-3-7-sonnet": map[string]any{
						"name":     "Claude 3.7 Sonnet",
						"variants": map[string]any{"thinking": nil},
					},
				},
			},
		},
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agent":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mockAgents)
		case r.Method == http.MethodGet && r.URL.Path == "/config/providers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mockConfig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	stk := newStack(t)
	stk.App.HTTPClient = mockServer.Client()

	p := createProject(t, stk, "Options Project")
	_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": mockServer.URL,
		"opencode_directory":  "/home/test/worktree",
		"opencode_agent":      "coder",
		"opencode_model":      "openai/gpt-4o",
		"opencode_variant":    "high",
	})

	// 1. Success case
	t.Run("success parsing agents and models", func(t *testing.T) {
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/opencode/options", nil)
		if code != http.StatusOK {
			t.Fatalf("GET options = %d: %s", code, string(body))
		}
		var resp openCodeOptionsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode options resp: %v", err)
		}
		if !resp.Reachable {
			t.Errorf("reachable = false, want true")
		}
		if resp.ResolvedAgent != "coder" || resp.ResolvedModel != "openai/gpt-4o" || resp.ResolvedVariant != "high" {
			t.Errorf("resolved mismatch: agent=%q model=%q variant=%q", resp.ResolvedAgent, resp.ResolvedModel, resp.ResolvedVariant)
		}

		// Agents should filter out hidden and empty, sort "build" first then alphabetically
		// Expecting: build, architect, coder
		if len(resp.Agents) != 3 {
			t.Fatalf("expected 3 agents, got %d: %+v", len(resp.Agents), resp.Agents)
		}
		if resp.Agents[0].Name != "build" || resp.Agents[0].Model != "openai/gpt-4o" {
			t.Errorf("agents[0] = %+v, want build (openai/gpt-4o)", resp.Agents[0])
		}
		if resp.Agents[1].Name != "architect" || resp.Agents[1].Variant != "deep" {
			t.Errorf("agents[1] = %+v, want architect (variant: deep)", resp.Agents[1])
		}
		if resp.Agents[2].Name != "coder" || resp.Agents[2].Model != "anthropic/claude-3-7-sonnet" {
			t.Errorf("agents[2] = %+v, want coder", resp.Agents[2])
		}

		// Models should be sorted by ref
		// Expecting: anthropic/claude-3-7-sonnet, openai/gpt-4o, openai/o3-mini
		if len(resp.Models) != 3 {
			t.Fatalf("expected 3 models, got %d: %+v", len(resp.Models), resp.Models)
		}
		if resp.Models[0].Ref != "anthropic/claude-3-7-sonnet" || len(resp.Models[0].Variants) != 1 || resp.Models[0].Variants[0] != "thinking" {
			t.Errorf("models[0] = %+v", resp.Models[0])
		}
		if resp.Models[1].Ref != "openai/gpt-4o" || len(resp.Models[1].Variants) != 2 || resp.Models[1].Variants[0] != "high" || resp.Models[1].Variants[1] != "low" {
			t.Errorf("models[1] = %+v", resp.Models[1])
		}
		if resp.Models[2].Ref != "openai/o3-mini" || len(resp.Models[2].Variants) != 0 {
			t.Errorf("models[2] = %+v", resp.Models[2])
		}
	})

	// 2. Query overrides
	t.Run("query overrides server_url and directory", func(t *testing.T) {
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/opencode/options?server_url="+mockServer.URL+"&directory=/home/override", nil)
		if code != http.StatusOK {
			t.Fatalf("GET options with query = %d: %s", code, string(body))
		}
		var resp openCodeOptionsResponse
		_ = json.Unmarshal(body, &resp)
		if !resp.Reachable {
			t.Errorf("reachable = false, want true")
		}
	})

	// 3. Graceful offline degradation
	t.Run("offline graceful degradation returns 200 with reachable false", func(t *testing.T) {
		pOffline := createProject(t, stk, "Offline Project")
		_, _ = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+pOffline.ID, map[string]any{
			"actor":               actor("admin"),
			"opencode_server_url": "http://127.0.0.1:54321", // unused port
		})
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+pOffline.ID+"/opencode/options", nil)
		if code != http.StatusOK {
			t.Fatalf("GET options offline = %d, want 200: %s", code, string(body))
		}
		var resp openCodeOptionsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode offline resp: %v", err)
		}
		if resp.Reachable {
			t.Errorf("expected reachable false, got true")
		}
		if len(resp.Agents) != 0 || len(resp.Models) != 0 {
			t.Errorf("expected empty agents and models, got %d agents, %d models", len(resp.Agents), len(resp.Models))
		}
		if resp.ResolvedAgent != "build" {
			t.Errorf("resolved agent = %q, want default build", resp.ResolvedAgent)
		}
	})

	// 4. SSRF and Path Traversal Rejections
	t.Run("SSRF and directory path traversal rejected with 400", func(t *testing.T) {
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/opencode/options?server_url=http://169.254.169.254", nil)
		if code != http.StatusBadRequest {
			t.Fatalf("GET options with metadata IP = %d, want 400: %s", code, string(body))
		}

		code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/opencode/options?directory=../../etc", nil)
		if code != http.StatusBadRequest {
			t.Fatalf("GET options with traversal = %d, want 400: %s", code, string(body))
		}
	})
}
