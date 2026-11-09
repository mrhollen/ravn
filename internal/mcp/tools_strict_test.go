package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// tools_strict_test.go: T6 behavior tests for the identity-aware tool
// registration (RAVN_IDENTITY_OPEN=strict), credential forwarding, and the
// proof that auto mode is byte-for-byte unchanged even through the new wiring.

// strictTaskID is a syntactically valid 26-char Crockford base32 ULID: tools
// validate id args before actor resolution, so policy tests need one.
const strictTaskID = "01C3D4E5F6A7B8C9D0E1F2A3B4"

// --- test stack: identity-aware variant --------------------------------------------

// startIdentityStack mirrors startMCPStackWithToken but registers via
// RegisterToolsWithIdentity (identityOpen selects auto/strict) and installs
// StreamableCredentialOption(token) so an incoming per-agent Bearer token is
// stamped into context and forwarded upstream — exactly what cmd/ravn-mcp does
// in production. The legacy startMCPStack* helpers are left untouched on
// purpose: auto-mode regression coverage must stay byte-identical to pre-T6.
func startIdentityStack(t *testing.T, upstreamURL, token, agentName string, identityOpen bool) *httptest.Server {
	t.Helper()
	if agentName == "" {
		agentName = "strict-agent"
	}
	api := apiclient.New(upstreamURL, token)
	mcs := mcpsrv.NewMCPServer("ravn", "test-version", ServerOptions()...)
	RegisterToolsWithIdentity(mcs, api, apiclient.Actor{Type: "agent", Name: agentName}, identityOpen)
	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
		StreamableCredentialOption(token),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// dialBearer dials the MCP server with a static Authorization header. bearer=""
// dials without credentials (same as dial).
func dialBearer(t *testing.T, mcpURL string, ctx context.Context, bearer string) *client.Client {
	t.Helper()
	var tr *mcptransport.StreamableHTTP
	var err error
	if bearer == "" {
		tr, err = mcptransport.NewStreamableHTTP(mcpURL + "/mcp")
	} else {
		tr, err = mcptransport.NewStreamableHTTP(mcpURL+"/mcp",
			mcptransport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + bearer}))
	}
	if err != nil {
		t.Fatalf("NewStreamableHTTP: %v", err)
	}
	c := client.NewClient(tr)
	initRes, err := c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: "ravn-mcp-strict-test", Version: "0"},
	}})
	if err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	if initRes.ServerInfo.Name != "ravn" {
		t.Fatalf("MCP server name = %q, want %q", initRes.ServerInfo.Name, "ravn")
	}
	return c
}

// strictCall records an upstream call without asserting on auth — the whole
// point is that the Authorization header VARIES (per-agent vs gate token).
type strictCall struct {
	method string
	path   string
	body   string
	auth   string
}

// newStrictUpstream is a permissive capture: it never asserts auth, records
// every /api/v1 call, and answers the few routes these tests route through.
func newStrictUpstream(t *testing.T) (*httptest.Server, *[]strictCall) {
	t.Helper()
	var calls []strictCall
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	}
	record := func(r *http.Request) string {
		var b []byte
		if r.Body != nil {
			b, _ = io.ReadAll(r.Body)
		}
		calls = append(calls, strictCall{method: r.Method, path: r.URL.Path, body: string(b), auth: r.Header.Get("Authorization")})
		return string(b)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok","db":"ok"}`)
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.CommentRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: comment body not JSON: %v (%s)", err, b)
		}
		data, _ := json.Marshal(map[string]any{
			"id": "CNEW", "kind": req.Kind, "body": req.Body,
			"actor": req.Actor, "created_at": "2026-11-09T15:50:00Z",
		})
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.ClaimTaskRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: claim body not JSON: %v (%s)", err, b)
		}
		data, _ := json.Marshal(map[string]any{
			"id": r.PathValue("id"), "title": "Strict target", "status": "doing",
			"claimed_by": req.Actor, "version": 2,
		})
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("GET /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") == "missing" {
			writeJSON(w, http.StatusNotFound, `{"error":"not_found","resource":"project","id":"missing","message":"unknown project"}`)
			return
		}
		writeJSON(w, http.StatusOK, mockGetProjectBody)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func requireNoUpstreamCalls(t *testing.T, calls []strictCall) {
	t.Helper()
	if len(calls) != 0 {
		t.Fatalf("expected NO upstream /api/v1 calls, got %+v", calls)
	}
}

// --- strict mode ------------------------------------------------------------------

// TestStrict_OmittedActorWithPerAgentCredential proves AC2 at the adapter
// level: with RAVN_IDENTITY_OPEN=strict a mutating call that omits `actor` is
// accepted because the request carried a per-agent credential, forwarded
// upstream as THAT credential (not the gate token), and attributed to the
// container-default name.
func TestStrict_OmittedActorWithPerAgentCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, calls := newStrictUpstream(t)
	mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", false) // strict

	c := dialBearer(t, mcpSrv.URL, ctx, "agent-alice")
	res := callTool(t, c, ctx, "add_comment", map[string]any{
		"id": strictTaskID, "kind": "note", "body": "hi",
	})
	if res.IsError {
		t.Fatalf("expected success with per-agent credential, got error: %s", firstText(t, res))
	}

	cc := findStrictCall(t, *calls, http.MethodPost, 	"/api/v1/tasks/" + strictTaskID + "/comments")
	if cc.auth != "Bearer agent-alice" {
		t.Errorf("upstream Authorization = %q, want Bearer agent-alice (per-agent token forwarded)", cc.auth)
	}
	var body struct {
		Actor apiclient.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(cc.body), &body); err != nil {
		t.Fatalf("upstream body not JSON: %v (%s)", err, cc.body)
	}
	if body.Actor.Type != "agent" || body.Actor.Name != "mcp-agent" {
		t.Errorf("forwarded actor = %+v, want agent/mcp-agent (container default fallback)", body.Actor)
	}
}

// TestStrict_OmittedActorWithoutPerAgentCredential proves the strict gate:
// with no credential — or only the SHARED gate token (no per-agent identity
// behind it) — an omitted actor is a clear isError BEFORE any upstream
// round-trip.
func TestStrict_OmittedActorWithoutPerAgentCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tests := []struct {
		name   string
		bearer string // "" = dial with no credentials at all
	}{
		{name: "no credential", bearer: ""},
		{name: "shared gate token only", bearer: "gate-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, calls := newStrictUpstream(t)
			mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", false) // strict

			c := dialBearer(t, mcpSrv.URL, ctx, tt.bearer)
			res := callTool(t, c, ctx, "claim_task", map[string]any{"id": strictTaskID})
			if !res.IsError {
				t.Fatalf("expected isError, got: %s", firstText(t, res))
			}
			text := firstText(t, res)
			for _, want := range []string{`"error":"invalid_input"`, "strict identity mode", "no per-agent API token"} {
				if !strings.Contains(text, want) {
					t.Errorf("error text %s lacks %q", text, want)
				}
			}
			requireNoUpstreamCalls(t, *calls)
		})
	}
}

// TestStrict_MalformedActorErrors proves that under strict a present-but-
// malformed `actor` is rejected (no silent fallback) before any upstream call.
func TestStrict_MalformedActorErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tests := []struct {
		name  string
		actor any
	}{
		{name: "empty name", actor: map[string]any{"name": "", "type": "agent"}},
		// NOTE: a non-object `actor` (e.g. a string) is rejected earlier by the
		// tool's input schema (mcp-go validation), identically in auto and
		// strict mode — no policy code path to exercise, so not tabled here.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, calls := newStrictUpstream(t)
			mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", false) // strict

			c := dialBearer(t, mcpSrv.URL, ctx, "agent-alice")
			res := callTool(t, c, ctx, "claim_task", map[string]any{"id": strictTaskID, "actor": tt.actor})
			if !res.IsError {
				t.Fatalf("expected isError for malformed actor, got: %s", firstText(t, res))
			}
			text := firstText(t, res)
			for _, want := range []string{`"error":"invalid_input"`, "strict identity mode", "malformed"} {
				if !strings.Contains(text, want) {
					t.Errorf("error text %s lacks %q", text, want)
				}
			}
			requireNoUpstreamCalls(t, *calls)
		})
	}
}

// TestStrict_WellFormedActorHonored proves strict never censors a well-formed
// explicit actor: it is forwarded as-is even when the credential is the shared
// gate token (the ravn-server's authoritative-actor rules apply on top).
func TestStrict_WellFormedActorHonored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, calls := newStrictUpstream(t)
	mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", false) // strict

	c := dialBearer(t, mcpSrv.URL, ctx, "gate-token")
	res := callTool(t, c, ctx, "add_comment", map[string]any{
		"id":   strictTaskID,
		"kind": "note",
		"body": "hi",
		"actor": map[string]any{"name": "bob", "type": "human"},
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", firstText(t, res))
	}

	cc := findStrictCall(t, *calls, http.MethodPost, 	"/api/v1/tasks/" + strictTaskID + "/comments")
	if cc.auth != "Bearer gate-token" {
		t.Errorf("upstream Authorization = %q, want Bearer gate-token", cc.auth)
	}
	var body struct {
		Actor apiclient.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(cc.body), &body); err != nil {
		t.Fatalf("upstream body not JSON: %v (%s)", err, cc.body)
	}
	if body.Actor.Type != "human" || body.Actor.Name != "bob" {
		t.Errorf("forwarded actor = %+v, want human/bob (explicit actor honored verbatim)", body.Actor)
	}
}

// TestStrict_ReadToolsIgnorePolicy proves strict mode imposes nothing on read
// tools: a bare dial with no credential reads fine.
func TestStrict_ReadToolsIgnorePolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, calls := newStrictUpstream(t)
	mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", false) // strict

	c := dialBearer(t, mcpSrv.URL, ctx, "")
	res := callTool(t, c, ctx, "get_project", map[string]any{"id": "01C2D3E4F5A6B7C8D9E0F1A2B3"})
	if res.IsError {
		t.Fatalf("read tool under strict with no credential must not error: %s", firstText(t, res))
	}
	asMap(t, firstText(t, res)) // valid project JSON
	if len(*calls) != 1 {
		t.Errorf("expected exactly one upstream call (the GET), got %+v", *calls)
	}
}

// TestAuto_ThroughNewWiringIsUnchanged is the AC1 regression guard: with
// identityOpen=true the same strict-stack wiring must reproduce the pre-T6
// behavior byte-for-byte — omitted AND malformed actors fall back to the
// container default, and success is the outcome in both cases.
func TestAuto_ThroughNewWiringIsUnchanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tests := []struct {
		name        string
		actor       any // nil = omit
		bearer      string
		wantActorNm string
	}{
		{name: "omitted actor falls back", actor: nil, bearer: "", wantActorNm: "mcp-agent"},
		{name: "malformed actor still falls back (pre-T6 behavior)", actor: map[string]any{"name": ""}, bearer: "some-other-token", wantActorNm: "mcp-agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream, calls := newStrictUpstream(t)
			mcpSrv := startIdentityStack(t, upstream.URL, "gate-token", "mcp-agent", true) // AUTO

			args := map[string]any{"id": strictTaskID, "kind": "note", "body": "hi"}
			if tt.actor != nil {
				args["actor"] = tt.actor
			}
			c := dialBearer(t, mcpSrv.URL, ctx, tt.bearer)
			res := callTool(t, c, ctx, "add_comment", args)
			if res.IsError {
				t.Fatalf("auto mode must not error here: %s", firstText(t, res))
			}

			cc := findStrictCall(t, *calls, http.MethodPost, 	"/api/v1/tasks/" + strictTaskID + "/comments")
			var body struct {
				Actor apiclient.Actor `json:"actor"`
			}
			if err := json.Unmarshal([]byte(cc.body), &body); err != nil {
				t.Fatalf("upstream body not JSON: %v (%s)", err, cc.body)
			}
			if body.Actor.Name != tt.wantActorNm || body.Actor.Type != "agent" {
				t.Errorf("forwarded actor = %+v, want agent/%s (auto fallback unchanged)", body.Actor, tt.wantActorNm)
			}
			// Credential forwarding is orthogonal to the policy: a present
			// per-agent Bearer still rides upstream; absent → static gate token.
			wantAuth := "Bearer gate-token"
			if tt.bearer != "" {
				wantAuth = "Bearer " + tt.bearer
			}
			if cc.auth != wantAuth {
				t.Errorf("upstream Authorization = %q, want %q", cc.auth, wantAuth)
			}
		})
	}
}

func findStrictCall(t *testing.T, calls []strictCall, method, path string) strictCall {
	t.Helper()
	for _, cc := range calls {
		if cc.method == method && cc.path == path {
			return cc
		}
	}
	t.Fatalf("no captured %s %s (upstream saw: %+v)", method, path, calls)
	return strictCall{}
}
