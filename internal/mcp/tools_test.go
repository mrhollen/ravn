package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// --- deterministic mock upstream (simulates crowban-server; fixed data, no sleeps) ---

const mockProjectsBody = `{"projects":[{"id":"P1","name":"Website Redesign","description":"MVP landing page work","created_at":"2026-09-20T09:00:00Z","updated_at":"2026-09-20T09:00:00Z","task_counts":{"todo":3,"doing":1,"done":5}},{"id":"P2","name":"Agent Ops","created_at":"2026-09-19T09:00:00Z","updated_at":"2026-09-19T09:00:00Z","task_counts":{"todo":0,"doing":0,"done":0}}],"total":2}`

const mockDetailBody = `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"due_at":"2026-10-01T00:00:00Z","assignee":{"type":"human","name":"ada"},"claimed_by":{"type":"agent","name":"runner-2"},"claim_expires_at":"2026-09-26T15:53:00Z","version":4,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z","project_id":"P1","description":"Users cannot log in after the password reset flow.","notes":"Last run: migration half-applied, rolled back.","archived":false,"blocked":true,"blockers":[{"id":"T2","title":"Fix DB migration","status":"doing"}],"claimed_at":"2026-09-26T15:48:03Z","claim_ttl_seconds":900,"acceptance_criteria":[{"seq":1,"text":"login works","done":true,"note":"manual test passed","completed_by":{"type":"human","name":"ada"},"completed_at":"2026-09-26T15:40:12Z"}],"comments":[{"id":"C1","kind":"progress","body":"ran the build — green","actor":{"type":"human","name":"ada"},"created_at":"2026-09-26T15:47:55Z"}],"artifacts":[],"relations":[{"task_id":"T1","other_task_id":"T2","kind":"blocked-by","created_at":"2026-09-26T15:49:30Z"}],"events":[{"id":"E1","kind":"claimed","actor":{"type":"agent","name":"runner-2"},"after":"{\"claim\":\"fresh\"}","at":"2026-09-26T15:48:03Z"}]}`

// The exact 409 claim-conflict body a foreign active lease produces (API.md).
// Compared byte-for-byte against the isError text to prove VERBATIM propagation.
const mockConflictBody = `{"error":"claim conflict","resource":"task","id":"HOLDING","holder":{"type":"agent","name":"runner-2"},"expires_at":"2026-09-26T15:53:00Z","message":"\"HOLDING\" is held by agent (runner-2) until 2026-09-26T15:53:00Z — wait for the lease to expire or work another task"}`

type claimCall struct {
	path      string
	body      string
	authorize string
}

// newMockUpstream stands up a fake crowban-server. Every route answers with
// fixed data; POST /tasks/{id}/claim returns the conflict body verbatim for id
// "HOLDING" and echoes the requesting actor as claimed_by otherwise (and
// records every claim call for assertions).
func newMockUpstream(t *testing.T) (*httptest.Server, *[]claimCall) {
	t.Helper()

	var claims []claimCall
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok","db":"ok"}`)
	})
	mux.HandleFunc("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("upstream: Authorization = %q, want Bearer secret-token", got)
		}
		writeJSON(w, http.StatusOK, mockProjectsBody)
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "T1" {
			writeJSON(w, http.StatusNotFound, `{"error":"not found","resource":"task","id":`+jsonString(r.PathValue("id"))+`,"message":"unknown task"}`)
			return
		}
		if q := r.URL.Query().Get("comment_limit"); q != "" && q != "5" {
			t.Errorf("upstream: comment_limit = %q, want 5", q)
		}
		writeJSON(w, http.StatusOK, mockDetailBody)
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		if r.Body != nil {
			b, _ = io.ReadAll(r.Body)
		}
		claims = append(claims, claimCall{path: r.URL.Path, body: string(b), authorize: r.Header.Get("Authorization")})

		id := r.PathValue("id")
		if id == "HOLDING" {
			writeJSON(w, http.StatusConflict, mockConflictBody)
			return
		}
		var req struct {
			Actor apiclient.Actor `json:"actor"`
		}
		_ = json.Unmarshal(b, &req)
		summary := map[string]any{
			"id": id, "title": "Fix login bug", "status": "doing", "priority": 3,
			"version": 5, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T15:48:03Z",
			"claimed_by":       map[string]string{"type": req.Actor.Type, "name": req.Actor.Name},
			"claim_expires_at": "2026-09-26T16:18:03Z",
		}
		data, _ := json.Marshal(summary)
		writeJSON(w, http.StatusOK, string(data))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &claims
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// --- helpers ---------------------------------------------------------------------------

func startMCPStack(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	api := apiclient.New(upstreamURL, "secret-token")
	mcs := mcpsrv.NewMCPServer("crowban", "test-version", ServerOptions()...)
	RegisterTools(mcs, api, apiclient.Actor{Type: "agent", Name: "default-agent"})
	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.HandleFunc("GET /healthz", HealthzHandler(api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, mcpURL string, ctx context.Context) *mcpclient.Client {
	t.Helper()
	tr, err := mcptransport.NewStreamableHTTP(mcpURL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHTTP: %v", err)
	}
	c := mcpclient.NewClient(tr)
	if _, err := c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: "crowban-mcp-test", Version: "0"},
	}}); err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	return c
}

func firstText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want TextContent", res.Content[0])
	}
	return tc.Text
}

func asMap(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("result text is not a JSON object: %v\n%s", err, text)
	}
	return m
}

// --- the round trip ----------------------------------------------------------------------

func TestStreamableHTTPRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, claims := newMockUpstream(t)
	mcpSrv := startMCPStack(t, upstream.URL)

	c := dial(t, mcpSrv.URL, ctx)
	defer func() { _ = c.Close() }()

	// tools/list shows exactly the three pattern tools.
	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(list.Tools) != 3 {
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
		}
		t.Fatalf("tools/list returned %d tools (%v), want 3", len(list.Tools), names)
	}
	want := map[string]bool{"list_projects": false, "get_task": false, "claim_task": false}
	for _, tool := range list.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool %q in tools/list", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tools/list missing %q", name)
		}
	}

	// get_task returns the mock's resume payload (JSON-equivalent).
	res, err := c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": "T1", "comment_limit": 5},
	}})
	if err != nil {
		t.Fatalf("tools/call get_task: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_task returned isError: %s", firstText(t, res))
	}
	gotDetail := asMap(t, firstText(t, res))
	wantDetail := asMap(t, mockDetailBody)
	if !reflect.DeepEqual(gotDetail, wantDetail) {
		t.Errorf("get_task payload mismatch:\n got  %s\n want %s", mustJSON(t, gotDetail), mockDetailBody)
	}

	// claim_task success: actor passes through; Bearer token is forwarded upstream.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name: "claim_task",
		Arguments: map[string]any{
			"id":      "T1",
			"actor":   map[string]any{"name": "runner-2", "type": "agent"},
			"ttl_sec": 1800,
		},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task: %v", err)
	}
	if res.IsError {
		t.Fatalf("claim_task returned isError: %s", firstText(t, res))
	}
	claimed := asMap(t, firstText(t, res))
	holder, _ := claimed["claimed_by"].(map[string]any)
	if holder == nil || holder["name"] != "runner-2" || holder["type"] != "agent" {
		t.Errorf("claimed_by = %v, want agent runner-2", claimed["claimed_by"])
	}

	// claim_task WITHOUT actor → container default used; id HOLDING → 409 whose
	// body must arrive VERBATIM as the isError text (the recovery instruction).
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "claim_task",
		Arguments: map[string]any{"id": "HOLDING"},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task (conflict): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected isError for foreign lease, got success: %s", firstText(t, res))
	}
	if text := firstText(t, res); text != mockConflictBody {
		t.Errorf("conflict body not verbatim:\n got  %s\n want %s", text, mockConflictBody)
	}

	// Verify what the MCP server actually sent upstream for each claim.
	if len(*claims) != 2 {
		t.Fatalf("upstream saw %d claims, want 2: %+v", len(*claims), *claims)
	}
	explicit := (*claims)[0]
	if explicit.authorize != "Bearer secret-token" {
		t.Errorf("claim request Authorization = %q, want Bearer secret-token", explicit.authorize)
	}
	var exp struct {
		Actor  apiclient.Actor `json:"actor"`
		TTLSec *int            `json:"ttl_sec"`
	}
	if err := json.Unmarshal([]byte(explicit.body), &exp); err != nil {
		t.Fatalf("claim body not JSON: %v (%s)", err, explicit.body)
	}
	if exp.Actor.Name != "runner-2" || exp.Actor.Type != "agent" || exp.TTLSec == nil || *exp.TTLSec != 1800 {
		t.Errorf("explicit claim body = %s", explicit.body)
	}

	fallback := (*claims)[1]
	var fb struct {
		Actor apiclient.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(fallback.body), &fb); err != nil {
		t.Fatalf("fallback claim body not JSON: %v (%s)", err, fallback.body)
	}
	if fb.Actor.Name != "default-agent" || fb.Actor.Type != "agent" {
		t.Errorf("fallback actor = %+v, want agent default-agent (CROWBAN_AGENT_NAME semantics)", fb.Actor)
	}

	// list_projects returns the projects+counts envelope.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: "list_projects"}})
	if err != nil {
		t.Fatalf("tools/call list_projects: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_projects returned isError: %s", firstText(t, res))
	}
	projects := asMap(t, firstText(t, res))
	rows, _ := projects["projects"].([]any)
	total, _ := projects["total"].(float64)
	if len(rows) != 2 || int(total) != 2 {
		t.Errorf("list_projects = %s", firstText(t, res))
	}

	// Malformed args (id as a number) → input-schema validation surfaces the
	// failure as an isError TOOL result (SEP-1303), not a protocol error.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "claim_task",
		Arguments: map[string]any{"id": 42},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task (bad arg type): %v", err)
	}
	if !res.IsError {
		t.Errorf("expected input-schema validation failure as isError, got success: %s", firstText(t, res))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

// --- /healthz proxy -------------------------------------------------------------------------

func TestHealthzProxiesUpstream(t *testing.T) {
	upstream, _ := newMockUpstream(t)
	mcpSrv := startMCPStack(t, upstream.URL)

	getStatus := func() int {
		resp, err := http.Get(mcpSrv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := getStatus(); code != http.StatusOK {
		t.Errorf("healthy upstream → /healthz = %d, want 200", code)
	}

	// Upstream down → the MCP container must report 503 so compose's
	// depends_on: service_healthy does not chain through a dead backend.
	upstream.Close()
	code := func() int {
		resp, err := http.Get(mcpSrv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var h struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		}
		if err := json.Unmarshal(body, &h); err != nil {
			t.Fatalf("healthz body not JSON: %v (%s)", err, body)
		}
		if h.Status != "degraded" || !strings.Contains(h.DB, "unreachable") {
			t.Errorf("unreachable upstream body = %+v, want degraded/unreachable", h)
		}
		return resp.StatusCode
	}()
	if code != http.StatusServiceUnavailable {
		t.Errorf("unreachable upstream → /healthz = %d, want 503", code)
	}
}
