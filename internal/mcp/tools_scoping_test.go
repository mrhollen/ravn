package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	httpapi "github.com/mrhollen/ravn/internal/interface/http"
	"github.com/mrhollen/ravn/internal/infrastructure/pwhash"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// tools_scoping_test.go: T6 AC2 end-to-end — a per-agent API token dialed into
// the MCP adapter authenticates to its NAMED identity on a real multiuser
// ravn-server (AuthGate=multiuser) and sees exactly that identity's scoped
// world. The upstream is the REAL SQLite-backed app, not a mock: this is where
// "the token forwards through" meets "the server enforces visibility".

const scopingSharedToken = "mcp-scoping-shared"

// newRealUpstream stands up the genuine ravn-server HTTP stack (SQLite-backed,
// multiuser auth gate) mirroring internal/interface/http's scoping test recipe.
func newRealUpstream(t *testing.T, scoping *domain.ScopingConfig, identityOpen bool) (*httptest.Server, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ravn-mcp-scoping.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()
	auth := application.NewAuth(uow, now, mint, pwhash.New(pwhash.FastParams()), time.Hour)

	app := httpapi.NewApp(httpapi.Deps{
		Projects:     application.NewProjects(uow, now, mint),
		Tasks:        application.NewTasks(uow, now, mint),
		Comments:     application.NewComments(uow, now, mint),
		Artifacts:    application.NewArtifacts(uow, now, mint),
		Acceptance:   application.NewAcceptance(uow, now, mint),
		Relations:    application.NewRelations(uow, now, mint),
		Auth:         auth,
		Scoping:      scoping,
		Identity:     application.NewIdentityResolver(uow),
		IdentityOpen: identityOpen,
		ArtifactsDir: t.TempDir(),
	})
	mux := http.NewServeMux()
	httpapi.RegisterRoutes(mux, app)
	handler := httpapi.AuthGate(httpapi.AuthModeMultiuser, scopingSharedToken, auth)(mux)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, db
}

// membershipCfg builds the non-nil MembershipScoping pointer (domain.MembershipScoping() returns a value).
func membershipCfg(t *testing.T) *domain.ScopingConfig {
	t.Helper()
	c := domain.MembershipScoping()
	return &c
}

// realAPI performs one raw REST call against the upstream — test scaffolding only.
func realAPI(t *testing.T, base string, bearer, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return res.StatusCode, buf.Bytes()
}

// mintAgentToken asks the operator (shared token) for a per-agent API token for
// identity `name` — created as neutral if absent, so no account is needed.
func mintAgentToken(t *testing.T, base, name string) string {
	t.Helper()
	code, body := realAPI(t, base, scopingSharedToken, http.MethodPost, "/api/v1/auth/tokens",
		map[string]any{"name": name, "kind": "agent", "label": name})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("mint token for %s = %d: %s", name, code, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		t.Fatalf("mint response for %s missing token: %s", name, body)
	}
	return out.Token
}

func uniqName(prefix string) string { return fmt.Sprintf("%s-%08x", prefix, time.Now().UnixNano()&0xFFFFFFFF) }

// createProjectViaREST creates a project as the given bearer and returns its
// id. identityName must match the token's own identity (the REST contract
// requires actor on mutations; under a per-agent credential principal the
// declared actor is that identity).
func createProjectViaREST(t *testing.T, base, bearer, identityName, projectName string) string {
	t.Helper()
	code, body := realAPI(t, base, bearer, http.MethodPost, "/api/v1/projects",
		map[string]any{"name": projectName, "actor": map[string]any{"type": "agent", "name": identityName}})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project %s = %d: %s", projectName, code, body)
	}
	return extractIDFromBody(t, body)
}

func createTaskViaREST(t *testing.T, base, bearer, identityName, projectID, title string) string {
	t.Helper()
	code, body := realAPI(t, base, bearer, http.MethodPost, "/api/v1/tasks",
		map[string]any{"project_id": projectID, "title": title, "actor": map[string]any{"type": "agent", "name": identityName}})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create task %s = %d: %s", title, code, body)
	}
	return extractIDFromBody(t, body)
}

// extractIDFromBody tolerates both a bare DTO and a {"task":{...}} envelope.
func extractIDFromBody(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, body)
	}
	if sub, ok := m["task"].(map[string]any); ok {
		m = sub
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("no id in response: %s", body)
	}
	return id
}

// TestScoping_PerAgentTokenGlobal proves the baseline: under GLOBAL scoping a
// per-agent token dialed into the MCP adapter authenticates, forwards, and its
// list_projects sees every project — exactly what global mode means.
func TestScoping_PerAgentTokenGlobal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	upstream, _ := newRealUpstream(t, nil, true) // global scoping (nil cfg)
	aliceTok := mintAgentToken(t, upstream.URL, "alice")
	bobTok := mintAgentToken(t, upstream.URL, "bob")

	alphaName, betaName := uniqName("Alpha"), uniqName("Beta")
	createProjectViaREST(t, upstream.URL, aliceTok, "alice", alphaName)
	createProjectViaREST(t, upstream.URL, bobTok, "bob", betaName)

	// MCP side: auto mode (the rollout default), no static token — every call
	// below carries its own per-agent Bearer via the transport header.
	mcpSrv := startIdentityStack(t, upstream.URL, "", "mcp-agent", true)
	c := dialBearer(t, mcpSrv.URL, ctx, aliceTok)

	res := callTool(t, c, ctx, "list_projects", map[string]any{})
	if res.IsError {
		t.Fatalf("list_projects as alice: %s", firstText(t, res))
	}
	m := asMap(t, firstText(t, res))
	if got := m["total"].(float64); got != 2 {
		t.Errorf("global scoping: list_projects total = %v, want 2", got)
	}
	text := firstText(t, res)
	if !strings.Contains(text, alphaName) || !strings.Contains(text, betaName) {
		t.Errorf("global scoping: expected both projects in result, got: %s", text)
	}
}

// TestScoping_PerAgentTokenMembership is the T6 AC2 proof proper: under
// MEMBERSHIP scoping each per-agent token sees only its OWN world — alice's
// MCP client lists only her project and gets 403 on bob's task, while bob's
// own client reads it fine. One shared adapter process serves both identities.
func TestScoping_PerAgentTokenMembership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	upstream, _ := newRealUpstream(t, membershipCfg(t), true) // membership scoping
	aliceTok := mintAgentToken(t, upstream.URL, "alice")
	bobTok := mintAgentToken(t, upstream.URL, "bob")

	alphaName := uniqName("Alpha")
	createProjectViaREST(t, upstream.URL, aliceTok, "alice", alphaName)
	betaName := uniqName("Beta")
	betaID := createProjectViaREST(t, upstream.URL, bobTok, "bob", betaName)
	secretTitle := uniqName("SecretTask")
	secretID := createTaskViaREST(t, upstream.URL, bobTok, "bob", betaID, secretTitle)

	mcpSrv := startIdentityStack(t, upstream.URL, "", "mcp-agent", true) // auto mode

	t.Run("alice sees only her project", func(t *testing.T) {
		c := dialBearer(t, mcpSrv.URL, ctx, aliceTok)
		res := callTool(t, c, ctx, "list_projects", map[string]any{})
		if res.IsError {
			t.Fatalf("list_projects as alice: %s", firstText(t, res))
		}
		m := asMap(t, firstText(t, res))
		if got := m["total"].(float64); got != 1 {
			t.Errorf("membership: alice list_projects total = %v, want 1", got)
		}
		text := firstText(t, res)
		if !strings.Contains(text, alphaName) {
			t.Errorf("alice's project missing from her own listing: %s", text)
		}
		if strings.Contains(text, betaName) {
			t.Errorf("bob's project leaked into alice's listing: %s", text)
		}
	})

	t.Run("alice gets 403 on bob's task", func(t *testing.T) {
		c := dialBearer(t, mcpSrv.URL, ctx, aliceTok)
		res := callTool(t, c, ctx, "get_task", map[string]any{"id": secretID})
		if !res.IsError {
			t.Fatalf("expected isError (403), got success: %s", firstText(t, res))
		}
		text := firstText(t, res)
		if !strings.Contains(text, `"error":"forbidden"`) {
			t.Errorf("expected forbidden envelope verbatim, got: %s", text)
		}
	})

	t.Run("bob reads his own task", func(t *testing.T) {
		c := dialBearer(t, mcpSrv.URL, ctx, bobTok)
		res := callTool(t, c, ctx, "get_task", map[string]any{"id": secretID})
		if res.IsError {
			t.Fatalf("bob's own task must be readable: %s", firstText(t, res))
		}
		m := asMap(t, firstText(t, res))
		if got, _ := m["title"].(string); !strings.Contains(got, "SecretTask") {
			t.Errorf("task title = %q, want to contain SecretTask", got)
		}
	})
}
