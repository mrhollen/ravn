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
	if got.OpenCodeServerURL != "" || got.OpenCodeDirectory != "" || got.OpenCodeModel != "" {
		t.Fatalf("initial opencode settings not empty: url=%q dir=%q model=%q", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel)
	}

	// 2. PUT updates opencode_server_url and opencode_directory
	serverURL := "http://localhost:4444"
	dir := "/home/test/workspace"
	modelRef := "llama.cpp/Qwen Nano"
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": serverURL,
		"opencode_directory":  dir,
		"opencode_model":      modelRef,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /projects/%s = %d, want 200 (body %s)", p.ID, code, string(body))
	}
	updated := mustDecode[projectDTO](t, body)
	if updated.OpenCodeServerURL != serverURL || updated.OpenCodeDirectory != dir || updated.OpenCodeModel != modelRef {
		t.Fatalf("PUT response mismatch: url=%q dir=%q model=%q, want url=%q dir=%q model=%q", updated.OpenCodeServerURL, updated.OpenCodeDirectory, updated.OpenCodeModel, serverURL, dir, modelRef)
	}

	// 3. GET confirms persistence
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET after PUT /projects/%s = %d", p.ID, code)
	}
	got = mustDecode[projectDTO](t, body)
	if got.OpenCodeServerURL != serverURL || got.OpenCodeDirectory != dir || got.OpenCodeModel != modelRef {
		t.Fatalf("persisted settings mismatch: url=%q dir=%q model=%q", got.OpenCodeServerURL, got.OpenCodeDirectory, got.OpenCodeModel)
	}

	// 4. PUT clears settings with empty string
	empty := ""
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p.ID, map[string]any{
		"actor":               actor("admin"),
		"opencode_server_url": empty,
		"opencode_directory":  empty,
		"opencode_model":      empty,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT clear settings = %d", code)
	}
	cleared := mustDecode[projectDTO](t, body)
	if cleared.OpenCodeServerURL != "" || cleared.OpenCodeDirectory != "" || cleared.OpenCodeModel != "" {
		t.Fatalf("settings not cleared: url=%q dir=%q model=%q", cleared.OpenCodeServerURL, cleared.OpenCodeDirectory, cleared.OpenCodeModel)
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
			if !strings.HasPrefix(req.Title, "CrowBan: ") {
				http.Error(w, "expected title prefix 'CrowBan: '", http.StatusBadRequest)
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
	if !strings.Contains(receivedPromptText, "Instructions: You are assigned to autonomously work on and complete this task in CrowBan. Use the CrowBan MCP tools (or REST API) as you progress: claim the task, update notes with your plan and progress, complete acceptance criteria using complete_ac as you verify them, and mark the task done when all criteria are met. When tests or verification commands produce output, upload it as evidence with attach_artifact (kind command-output/log/screenshot, ref = URL or data_base64 = base64 file content) so a human can verify your work — encouraged, not required.") {
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
	if dir := resolveOpenCodeDirectory(""); dir != "/home/hollen/Code/crowban" {
		t.Errorf("default directory = %q, want /home/hollen/Code/crowban", dir)
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
