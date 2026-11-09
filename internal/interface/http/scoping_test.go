package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// scoping_test.go drives the multi-user epic T4 gates end-to-end against a real
// SQLite-backed App: visibility scoping (AC1), role-gated authorization + spoof
// prevention (AC2), ?mine= filters (AC3), created_by/owner surfacing (AC4), and
// the byte-identical regression battery for default/global mode (AC6). Membership
// rows are seeded via raw SQL — writing memberships is a later epic ticket.

const scopingSharedToken = "s3cret"

type scOwner struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// withActor injects the declared acting name into a write body. The REST
// contract requires actor on mutations; under a session principal T4 spoof-
// proofing overrides whatever is declared, so for those calls the value is a
// placeholder of the expected self-name (it must not leak into attribution).
func withActor(name string, extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		out[k] = v
	}
	out["actor"] = actor(name)
	return out
}

// scopingStack is newGatedStack with the T4 Deps wired (Scoping/Identity/
// IdentityOpen) plus the raw DB handle for membership seeding. dbPath empty → a
// fresh temp DB; pass one shared path to point several stacks at the SAME file
// (the AC6 byte-identity battery). mode AuthModeToken with Scoping nil is how a
// pre-T4 token deployment is reproduced for comparison.
type scopingStack struct {
	stack
	db *sql.DB
}

func newScopingStack(t testing.TB, mode AuthMode, scoping *domain.ScopingConfig, identityOpen bool, dbPath string) *scopingStack {
	t.Helper()
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "ravn-scoping.db")
	}
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()
	auth := application.NewAuth(uow, now, mint, fastTestHasher{}, time.Hour)

	app := NewApp(Deps{
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
	RegisterRoutes(mux, app)
	return &scopingStack{stack: stack{Handler: AuthGate(mode, scopingSharedToken, auth)(mux), App: app}, db: db}
}

// membershipCfg builds the non-nil MembershipScoping pointer the stack builder
// takes (domain.MembershipScoping() returns a value, not addressable in-place).
func membershipCfg() *domain.ScopingConfig { c := domain.MembershipScoping(); return &c }

// --- helpers -------------------------------------------------------------------

func mustCreateAccount(t *testing.T, stk *scopingStack, name, pw string) {
	t.Helper()
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/auth/accounts",
		map[string]any{"name": name, "password": pw}, scopingSharedToken)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create account %s = %d: %s", name, code, body)
	}
}

// mustLogin returns a session bearer for a provisioned account.
func mustLogin(t *testing.T, stk *scopingStack, name, pw string) string {
	t.Helper()
	var out loginOut
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/auth/login",
		map[string]any{"username": name, "password": pw}, "")
	if code != http.StatusOK {
		t.Fatalf("login %s = %d: %s", name, code, body)
	}
	mustJSON(t, body, &out)
	return out.Token
}

func mustIdentityID(t *testing.T, stk *scopingStack, name string) domain.ID {
	t.Helper()
	var id string
	if err := stk.db.QueryRow("SELECT id FROM identity WHERE name = ?", name).Scan(&id); err != nil {
		t.Fatalf("look up identity %s: %v", name, err)
	}
	return domain.ID(id)
}

// seedMember inserts a project_member row directly (membership WRITING is a later
// epic ticket — raw SQL is the only way to confer roles today).
func seedMember(t *testing.T, stk *scopingStack, pid domain.ID, identityName, role string) {
	t.Helper()
	_, err := stk.db.Exec(
		"INSERT INTO project_member (project_id, identity_id, role) VALUES (?, ?, ?)",
		pid, mustIdentityID(t, stk, identityName), role)
	if err != nil {
		t.Fatalf("seed member %s(%s) on %s: %v", identityName, role, pid, err)
	}
}

func setMemberRole(t *testing.T, stk *scopingStack, pid domain.ID, identityName, role string) {
	t.Helper()
	res, err := stk.db.Exec(
		"UPDATE project_member SET role = ? WHERE project_id = ? AND identity_id = ?",
		role, pid, mustIdentityID(t, stk, identityName))
	if err != nil {
		t.Fatalf("set member role %s(%s): %v", identityName, role, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("set member role affected %d rows, want 1", n)
	}
}

// extractID pulls the created row's id out of a create response, tolerating both a
// bare DTO and a {"task":{...}}-style envelope.
func extractID(t *testing.T, body []byte) string {
	t.Helper()
	m := jmap(t, jraw(body))
	if sub, ok := m["task"]; ok {
		m = jmap(t, sub)
	}
	id := jstr(m, "id")
	if id == "" {
		t.Fatalf("no id in response %s", body)
	}
	return id
}

// --- tiny json probes (resilient to envelope shape) ------------------------------

func jraw(body []byte) json.RawMessage { return json.RawMessage(body) }

func jmap(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	return m
}

func jstr(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func hasKey(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	return ok && string(raw) != "null"
}

type projectListOut struct {
	Projects []struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		CreatedBy string   `json:"created_by"`
		Owner     *scOwner `json:"owner"`
	} `json:"projects"`
	Total int `json:"total"`
}

type taskListOut struct {
	Tasks []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		CreatedBy string   `json:"created_by"`
		Owner     *scOwner `json:"owner"`
	} `json:"tasks"`
	Total int `json:"total"`
}

func forbiddenBody(t *testing.T, body []byte, resource, required string) {
	t.Helper()
	m := jmap(t, jraw(body))
	if got := jstr(m, "error"); got != "forbidden" {
		t.Fatalf("403 error code = %q, want forbidden: %s", got, body)
	}
	if got := jstr(m, "resource"); got != resource {
		t.Fatalf("403 resource = %q, want %s: %s", got, resource, body)
	}
	if got := jstr(m, "required"); got != required {
		t.Fatalf("403 required = %q, want %s: %s", got, required, body)
	}
	if jstr(m, "message") == "" {
		t.Fatalf("403 message missing: %s", body)
	}
}

// --- AC6: default/global is byte-identical to pre-T4 token mode -------------------

func TestScoping_GlobalModeByteIdenticalToToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	// A: pre-T4 token deployment (no Scoping dep at all). B: multiuser + global —
	// same file, so both stacks read byte-identical rows.
	a := newScopingStack(t, AuthModeToken, nil, true, dbPath)
	// B mirrors production wiring for RAVN_SCOPING=global exactly: Scoping dep NIL
	// (see cmd/ravn-server main.go / ScopingFromEnv), Identity still wired.
	b := newScopingStack(t, AuthModeMultiuser, nil, true, dbPath)

	op := scopingSharedToken
	// Seed through A (operator): two projects, one task with ACs, a comment, a claim.
	code, body := authReq(t, a.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Alpha"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create Alpha = %d: %s", code, body)
	}
	pa := extractID(t, body)
	code, body = authReq(t, a.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Beta"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create Beta = %d: %s", code, body)
	}
	pb := extractID(t, body)

	code, body = authReq(t, a.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{
		"title":               "seed task",
		"project_id":          pa,
		"acceptance_criteria": []string{"one", "two"},
	}), op)
	if code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", code, body)
	}
	tid := extractID(t, body)

	code, _ = authReq(t, a.Handler, "POST", "/api/v1/tasks/"+tid+"/comments", withActor("zed", map[string]any{"kind": "note", "body": "hello"}), op)
	if code != http.StatusCreated {
		t.Fatalf("comment = %d", code)
	}
	code, _ = authReq(t, a.Handler, "POST", "/api/v1/tasks/"+tid+"/claim", withActor("zed", nil), op)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("claim = %d", code)
	}

	battery := []struct{ method, path string }{
		{"GET", "/api/v1/projects"},
		{"GET", "/api/v1/projects/" + pa},
		{"GET", "/api/v1/projects/" + pb},
		{"GET", "/api/v1/projects/" + pa + "/board"},
		{"GET", "/api/v1/projects/" + pb + "/board"},
		{"GET", "/api/v1/tasks?project_id=" + pa + "&limit=50"},
		{"GET", "/api/v1/tasks/" + tid},
	}
	for _, c := range battery {
		ga, ba := authReq(t, a.Handler, c.method, c.path, nil, op)
		gb, bb := authReq(t, b.Handler, c.method, c.path, nil, op)
		if ga != http.StatusOK || gb != http.StatusOK {
			t.Fatalf("%s %s: token=%d global=%d (want 200/200)", c.method, c.path, ga, gb)
		}
		if !bytes.Equal(ba, bb) {
			t.Errorf("%s %s differs between token and multiuser-global:\nTOKEN:  %s\nGLOBAL: %s",
				c.method, c.path, ba, bb)
		}
	}
}

// --- AC1: membership visibility ---------------------------------------------------

func TestScoping_MembershipVisibility(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")
	op := scopingSharedToken

	// Operator creates an UNATTRIBUTED legacy project (no owner, no members).
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Legacy"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create Legacy = %d: %s", code, body)
	}
	pLegacy := extractID(t, body)

	// alice (credential principal) creates her own project → attributed.
	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")

	code, body = authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "AliceProj"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create project = %d: %s", code, body)
	}
	pAlice := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")

	// bob before membership: sees NOTHING (legacy has no owner/members; AliceProj is alice's).
	var pl projectListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, tokB)
	mustJSON(t, body, &pl)
	if code != http.StatusOK || pl.Total != 0 {
		t.Fatalf("bob pre-membership /projects total=%d code=%d, want 0/200: %s", pl.Total, code, body)
	}

	// alice sees exactly her project.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, tokA)
	mustJSON(t, body, &pl)
	if pl.Total != 1 || pl.Projects[0].ID != pAlice {
		t.Fatalf("alice /projects total=%d ids=%v, want exactly [%s]", pl.Total, projIDs(pl), pAlice)
	}

	// Conferring bob a viewer seat unlocks AliceProj — but NOT the legacy project.
	seedMember(t, stk, domain.ID(pAlice), "bob", "viewer")
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, tokB)
	mustJSON(t, body, &pl)
	if pl.Total != 1 || pl.Projects[0].ID != pAlice {
		t.Fatalf("bob post-membership /projects total=%d ids=%v, want exactly [%s]", pl.Total, projIDs(pl), pAlice)
	}

	// Operators keep see-all: both projects.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, op)
	mustJSON(t, body, &pl)
	if pl.Total != 2 {
		t.Fatalf("operator /projects total=%d, want 2 (bypass)", pl.Total)
	}

	// Task-level visibility: alice's task inside AliceProj vs the operator's task in Legacy.
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{"title": "in-visibility", "project_id": pAlice}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create task = %d: %s", code, body)
	}
	tIn := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{"title": "legacy-task", "project_id": pLegacy}), op)
	if code != http.StatusCreated {
		t.Fatalf("operator create legacy task = %d: %s", code, body)
	}
	tLeg := extractID(t, body)

	// bob's unscoped list contains ONLY the visible project's task.
	var tl taskListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?limit=50", nil, tokB)
	mustJSON(t, body, &tl)
	if tl.Total != 1 || tl.Tasks[0].ID != tIn {
		t.Fatalf("bob /tasks total=%d ids=%v, want exactly [%s]", tl.Total, taskIDs(tl), tIn)
	}
	// Filtering by an invisible project is scoped to empty (200), not 403.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?project_id="+pLegacy, nil, tokB)
	mustJSON(t, body, &tl)
	if code != http.StatusOK || tl.Total != 0 {
		t.Fatalf("bob /tasks?project_id=legacy = %d/%d, want 200/empty", code, tl.Total)
	}
	// Direct access to an invisible task/project is a 403 with the required role.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tLeg, nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("bob GET invisible task = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "viewer")

	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pLegacy, nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("bob GET invisible project = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "viewer")

	// Board + report inherit the same gate.
	for _, path := range []string{"/api/v1/projects/" + pLegacy + "/board", "/api/v1/projects/" + pLegacy + "/report"} {
		code, body = authReq(t, stk.Handler, "GET", path, nil, tokB)
		if code != http.StatusForbidden {
			t.Fatalf("bob GET %s = %d, want 403: %s", path, code, body)
		}
	}
	// ...and bob CAN read the visible project's board.
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pAlice+"/board", nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("bob GET visible board = %d, want 200", code)
	}
}

func projIDs(pl projectListOut) []string {
	out := make([]string, len(pl.Projects))
	for i, p := range pl.Projects {
		out[i] = p.ID
	}
	return out
}

func taskIDs(tl taskListOut) []string {
	out := make([]string, len(tl.Tasks))
	for i, tsk := range tl.Tasks {
		out[i] = tsk.ID
	}
	return out
}

// --- AC2: membership authorization (role matrix) ---------------------------------

func TestScoping_MembershipAuthorization(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")
	op := scopingSharedToken

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Gated"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{
		"title":               "gated task",
		"project_id":          pid,
		"acceptance_criteria": []string{"one"},
	}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create task = %d: %s", code, body)
	}
	tid := extractID(t, body)

	// Operator attaches a url artifact (no file I/O) for the destructive-removal tests.
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/artifacts",
		withActor("operator", map[string]any{"kind": "url", "ref": "https://example.com/evidence", "summary": "seed"}), op)
	if code != http.StatusCreated {
		t.Fatalf("attach artifact = %d: %s", code, body)
	}
	aid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")
	seedMember(t, stk, domain.ID(pid), "bob", "viewer")

	// Viewer: reads pass, writes are 403 with required=editor.
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tid, nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("bob viewer GET task = %d, want 200", code)
	}
	denials := []struct{ method, path string; body any }{
		{"POST", "/api/v1/tasks/" + tid + "/claim", withActor("bob", nil)},
		{"POST", "/api/v1/tasks/" + tid + "/comments", withActor("bob", map[string]any{"kind": "note", "body": "x"})},
		{"PUT", "/api/v1/tasks/" + tid + "/notes", withActor("bob", map[string]any{"notes": "# x"})},
		{"POST", "/api/v1/tasks/" + tid + "/acceptance-criteria", withActor("bob", map[string]any{"text": "two"})},
	}
	for _, c := range denials {
		code, body = authReq(t, stk.Handler, c.method, c.path, c.body, tokB)
		if code != http.StatusForbidden {
			t.Fatalf("viewer %s %s = %d, want 403: %s", c.method, c.path, code, body)
		}
		forbiddenBody(t, body, "project", "editor")
	}
	// Destructive ops need admin even for editors: assert as viewer first (required=admin).
	code, body = authReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+tid+"/acceptance-criteria/1?actor_name=bob", nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("viewer remove AC = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "admin")
	code, body = authReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+tid+"/artifacts/"+aid+"?actor_name=bob", nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("viewer remove artifact = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "admin")

	// Editor: writes unlock (claim succeeds, attributed to bob), destructive still denied.
	setMemberRole(t, stk, domain.ID(pid), "bob", "editor")
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/claim", withActor("bob", nil), tokB)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("editor claim = %d, want 2xx: %s", code, body)
	}
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tid, nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("detail after claim = %d: %s", code, body)
	}
	detail := jmap(t, jraw(body))
	cbRaw, ok := detail["claimed_by"]
	if !ok || string(cbRaw) == "null" {
		t.Fatalf("claim did not record holder: %v", detail)
	}
	if got := jstr(jmap(t, cbRaw), "name"); got != "bob" {
		t.Fatalf("editor claimed_by.name = %q, want bob: %s", got, cbRaw)
	}
	code, body = authReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+tid+"/acceptance-criteria/1?actor_name=bob", nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("editor remove AC = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "admin")

	// Admin: destructive unlocks.
	setMemberRole(t, stk, domain.ID(pid), "bob", "admin")
	code, _ = authReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+tid+"/acceptance-criteria/1?actor_name=bob", nil, tokB)
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("admin remove AC = %d, want 2xx: %s", code, body)
	}
	code, _ = authReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+tid+"/artifacts/"+aid+"?actor_name=bob", nil, tokB)
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("admin remove artifact = %d, want 2xx: %s", code, body)
	}

	// Operators bypass everything — even writes on a project bob alone owns.
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/acceptance-criteria", withActor("operator", map[string]any{"text": "again"}), op)
	if code != http.StatusCreated {
		t.Fatalf("operator add AC = %d, want 201: ", code)
	}
}

// --- AC2+AC4: project creation attribution + identity-open gate -------------------

func TestScoping_ProjectCreationAttributionAndIdentityOpen(t *testing.T) {
	open := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")
	mustCreateAccount(t, open, "alice", "password123")
	tokA := mustLogin(t, open, "alice", "password123")

	// Credential principal in membership+open: created with attribution + owner.
	code, body := authReq(t, open.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Mine"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create (open) = %d: %s", code, body)
	}
	m := jmap(t, jraw(body))
	wantID := mustIdentityID(t, open, "alice")
	if got := jstr(m, "created_by"); got != string(wantID) {
		t.Fatalf("created_by = %q, want %q: %s", got, wantID, body)
	}
	ownerRaw, ok := m["owner"]
	if !ok || string(ownerRaw) == "null" {
		t.Fatalf("owner missing on alice-created project: %s", body)
	}
	om := jmap(t, ownerRaw)
	if jstr(om, "name") != "alice" {
		t.Fatalf("owner.name = %q, want alice: %s", jstr(om, "name"), body)
	}

	// Operator creates unattributed (legacy bytes): no created_by / owner keys at all.
	code, body = authReq(t, open.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Ops"}), scopingSharedToken)
	if code != http.StatusCreated {
		t.Fatalf("operator create = %d: %s", code, body)
	}
	m = jmap(t, jraw(body))
	if hasKey(m, "created_by") || hasKey(m, "owner") {
		t.Fatalf("operator-created project must omit attribution keys: %s", body)
	}

	// Membership + identity-open strict: credential principals cannot create projects.
	closed := newScopingStack(t, AuthModeMultiuser, membershipCfg(), false, "")
	mustCreateAccount(t, closed, "alice", "password123")
	tokA = mustLogin(t, closed, "alice", "password123")
	code, body = authReq(t, closed.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Nope"}), tokA)
	if code != http.StatusForbidden {
		t.Fatalf("credential create with identity-open=false = %d, want 403: %s", code, body)
	}

	// Global mode (nil Scoping): attribution STILL stamps for credential principals —
	// the open/closed gate applies to membership deployments only.
	global := newScopingStack(t, AuthModeMultiuser, nil, false, "")
	mustCreateAccount(t, global, "alice", "password123")
	tokA = mustLogin(t, global, "alice", "password123")
	code, body = authReq(t, global.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Global"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("global-mode credential create = %d, want 201: %s", code, body)
	}
	m = jmap(t, jraw(body))
	if got := jstr(m, "created_by"); got != string(mustIdentityID(t, global, "alice")) {
		t.Fatalf("global created_by = %q: %s", got, body)
	}
}

// --- AC3: ?mine= filters -----------------------------------------------------------

func TestScoping_MineFiltersTasks(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, nil, true, "") // global mode: mine works everywhere
	op := scopingSharedToken

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Pool"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	mustCreateAccount(t, stk, "bob", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")

	// T1: operator-created, assigned to bob. T2: alice-created (attribution).
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{
		"title":      "bob's task",
		"project_id": pid,
		"assignee":   map[string]string{"name": "bob"},
	}), op)
	if code != http.StatusCreated {
		t.Fatalf("create T1 = %d: %s", code, body)
	}
	t1 := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{
		"title":      "alice's task",
		"project_id": pid,
	}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create T2 = %d: %s", code, body)
	}
	t2 := extractID(t, body)

	// bob commented on T1 → activity row.
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+t1+"/comments", withActor("bob", map[string]any{"kind": "note", "body": "hi"}), tokB)
	if code != http.StatusCreated {
		t.Fatalf("bob comment = %d", code)
	}

	list := func(tok, query string) taskListOut {
		var tl taskListOut
		code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?limit=50&"+query, nil, tok)
		if code != http.StatusOK {
			t.Fatalf("GET /tasks?%s = %d: %s", query, code, body)
		}
		mustJSON(t, body, &tl)
		return tl
	}

	tl := list(tokB, "mine=assigned")
	if len(tl.Tasks) != 1 || tl.Tasks[0].ID != t1 {
		t.Fatalf("bob ?mine=assigned = %v, want [%s]", taskIDs(tl), t1)
	}
	tl = list(tokB, "mine=activity")
	if !contains(taskIDs(tl), t1) {
		t.Fatalf("bob ?mine=activity = %v, want to include %s", taskIDs(tl), t1)
	}
	tl = list(tokB, "mine=created")
	if tl.Total != 0 {
		t.Fatalf("bob ?mine=created total=%d, want 0 (honest empty — nothing attributed)", tl.Total)
	}

	tl = list(tokA, "mine=created")
	if !contains(taskIDs(tl), t2) || contains(taskIDs(tl), t1) {
		t.Fatalf("alice ?mine=created = %v, want exactly [%s]", taskIDs(tl), t2)
	}
	tl = list(tokA, "mine=assigned")
	if tl.Total != 0 {
		t.Fatalf("alice ?mine=assigned total=%d, want 0", tl.Total)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestScoping_MineFiltersProjectsAndMalformed(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, nil, true, "")
	op := scopingSharedToken

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Ops"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create Ops = %d: %s", code, body)
	}
	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "AliceProj"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create AliceProj = %d: %s", code, body)
	}

	var pl projectListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects?mine=created", nil, tokA)
	mustJSON(t, body, &pl)
	if pl.Total != 1 || pl.Projects[0].Name != "AliceProj" {
		t.Fatalf("alice /projects?mine=created = %d/%v, want exactly AliceProj: %s", pl.Total, projIDs(pl), body)
	}

	malformed := []struct{ path string }{
		{"/api/v1/projects?mine=assigned"},     // projects only support created
		{"/api/v1/projects?mine=bogus"},
		{"/api/v1/tasks?mine=bogus"},                 // tasks don't know bogus
		{"/api/v1/tasks?mine=assigned&mine=created"}, // multiple distinct values
		{"/api/v1/tasks?mine=assigned&assignee_name=bob"}, // explicit filter conflict
	}
	for _, c := range malformed {
		code, body = authReq(t, stk.Handler, "GET", c.path, nil, tokA)
		if code != http.StatusBadRequest {
			t.Fatalf("GET %s = %d, want 400: %s", c.path, code, body)
		}
	}

	// Duplicates of the SAME value collapse — not an error.
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/tasks?mine=created&mine=created", nil, tokA)
	if code != http.StatusOK {
		t.Fatalf("duplicate ?mine=created = %d, want 200", code)
	}
}

func TestScoping_MineTokenModeDegradation(t *testing.T) {
	// Pre-T4 token deployment: mine filters fall back to the acting NAME (works on
	// stored name columns) and honest-empty attribution. No session principals here.
	stk := newScopingStack(t, AuthModeToken, nil, true, "")
	op := scopingSharedToken

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Pool"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{
		"title":      "bob-assigned",
		"project_id": pid,
		"assignee":   map[string]string{"name": "bob"},
	}), op)
	if code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", code, body)
	}
	t1 := extractID(t, body)

	var tl taskListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?actor_name=bob&mine=assigned", nil, op)
	mustJSON(t, body, &tl)
	if len(tl.Tasks) != 1 || tl.Tasks[0].ID != t1 {
		t.Fatalf("token mode ?mine=assigned&actor_name=bob = %v, want [%s]", taskIDs(tl), t1)
	}

	// The shared token carries its OWN authoritative name — an operator asking
	// ?mine=assigned without declaring a name is filtered by that name, which owns
	// nothing → honest empty 200 (the backdoor never errors).
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?mine=assigned", nil, op)
	mustJSON(t, body, &tl)
	if code != http.StatusOK || tl.Total != 0 {
		t.Fatalf("token mode operator ?mine=assigned (no declared name) = %d/%d, want 200/empty: %s", code, tl.Total, body)
	}

	// No principal at all (RAVN_AUTH=off): a mine filter that cannot resolve an acting
	// name is an unfilterable request → 400.
	off := newScopingStack(t, AuthModeOff, nil, true, "")
	code, body = authReq(t, off.Handler, "GET", "/api/v1/tasks?mine=assigned", nil, "")
	if code != http.StatusBadRequest {
		t.Fatalf("off mode ?mine=assigned w/o actor = %d, want 400: %s", code, body)
	}

	// created needs an identity row for the acting name — "zed" has none → honest empty.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?actor_name=zed&mine=created", nil, op)
	mustJSON(t, body, &tl)
	if code != http.StatusOK || tl.Total != 0 {
		t.Fatalf("token mode ?mine=created w/o identity row = %d/%d, want 200/empty: %s", code, tl.Total, body)
	}
}

// --- AC4: created_by / owner surfacing + degradation -------------------------------

func TestScoping_AttributionSurfacingAndDegradation(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, nil, true, "")
	op := scopingSharedToken

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("operator", map[string]any{"name": "Legacy"}), op)
	if code != http.StatusCreated {
		t.Fatalf("create legacy = %d: %s", code, body)
	}
	pLegacy := extractID(t, body)

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "AliceProj"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create AliceProj = %d: %s", code, body)
	}
	pAlice := extractID(t, body)

	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("zed", map[string]any{"title": "L", "project_id": pLegacy}), op)
	if code != http.StatusCreated {
		t.Fatalf("create L = %d: %s", code, body)
	}
	tLeg := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{"title": "A", "project_id": pAlice}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create A = %d: %s", code, body)
	}
	tA := extractID(t, body)

	var tl taskListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks?limit=50", nil, tokA)
	mustJSON(t, body, &tl)
	if code != http.StatusOK || tl.Total != 2 {
		t.Fatalf("/tasks total=%d, want 2: %s", tl.Total, body)
	}
	for _, row := range tl.Tasks {
		switch row.ID {
		case tA:
			want := string(mustIdentityID(t, stk, "alice"))
			if row.CreatedBy != want {
				t.Fatalf("task A created_by = %q, want %q", row.CreatedBy, want)
			}
			if row.Owner == nil || row.Owner.Name != "alice" {
				t.Fatalf("task A owner = %+v, want name=alice", row.Owner)
			}
		case tLeg:
			if row.CreatedBy != "" || row.Owner != nil {
				t.Fatalf("legacy task must omit attribution: %+v", row)
			}
		}
	}

	// Detail view carries the same fields.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tA, nil, tokA)
	if code != http.StatusOK {
		t.Fatalf("detail GET = %d: %s", code, body)
	}
	detail := jmap(t, jraw(body))
	if got := jstr(detail, "created_by"); got != string(mustIdentityID(t, stk, "alice")) {
		t.Fatalf("detail created_by = %q: %v", got, detail)
	}

	// Degradation: the creator is later DELETED, leaving a dangling created_by. The
	// created_by references have no ON DELETE clause, so simulate the dangling id:
	// drop FK enforcement on this single pooled connection (SetMaxOpenConns(1) makes
	// the pragma sticky) and delete alice outright — rows keep their created_by, and
	// fillOwners can resolve nothing, so owner is omitted.
	if _, err := stk.db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable FK enforcement: %v", err)
	}
	if _, err := stk.db.Exec("DELETE FROM identity WHERE name = 'alice'"); err != nil {
		t.Fatalf("delete alice identity: %v", err)
	}
	var pl projectListOut
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects?limit=50", nil, op)
	mustJSON(t, body, &pl)
	for _, p := range pl.Projects {
		if p.ID == pAlice && (p.CreatedBy == "" || p.Owner != nil) {
			t.Fatalf("disabled-creator project must keep created_by and omit owner: %+v", p)
		}
		if p.ID == pLegacy && (p.CreatedBy != "" || p.Owner != nil) {
			t.Fatalf("legacy project must have no attribution: %+v", p)
		}
	}

	// Unknown artifact ids 404 in every mode.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/artifacts/00000000000000000000000000", nil, op)
	if code != http.StatusNotFound {
		t.Fatalf("unknown artifact = %d, want 404: %s", code, body)
	}
}

// --- AC2: authoritative actor prevents declaration spoofing --------------------------

func TestScoping_AuthoritativeActorPreventsSpoofing(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Spoof"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{"title": "s", "project_id": pid}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", code, body)
	}
	tid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	seedMember(t, stk, domain.ID(pid), "bob", "viewer")

	// bob (viewer) declaring his OWN name still does not confer editor rights — the
	// gate acts on the session identity's role.
	tokB := mustLogin(t, stk, "bob", "password123")
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/claim", withActor("bob", nil), tokB)
	if code != http.StatusForbidden {
		t.Fatalf("bob claim (viewer role) = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "project", "editor")

	// alice (owner) declares someone else's name on her claim — it is IGNORED and the
	// claim records ALICE.
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/claim", withActor("bob", nil), tokA)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("alice claim (declaring bob) = %d, want 2xx", code)
	}
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tid, nil, tokA)
	if code != http.StatusOK {
		t.Fatalf("detail GET after claim = %d: %s", code, body)
	}
	detail := jmap(t, jraw(body))
	cb, ok := detail["claimed_by"]
	if !ok || string(cb) == "null" {
		t.Fatalf("claim did not record holder: %v", detail)
	}
	if got := jstr(jmap(t, cb), "name"); got != "alice" {
		t.Fatalf("claimed_by.name = %q, want alice (declared name must be ignored)", got)
	}

	// Same for comments: the poster of record is the session identity.
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tid+"/comments", withActor("zed", map[string]any{"kind": "note", "body": "hello"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice comment (declaring zed) = %d, want 201", code)
	}
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/tasks/"+tid, nil, tokA)
	if code != http.StatusOK {
		t.Fatalf("detail GET after comment = %d: %s", code, body)
	}
	detail = jmap(t, jraw(body))
	comms, ok := detail["comments"]
	if !ok {
		t.Fatalf("detail has no comments: %v", detail)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(comms, &list); err != nil || len(list) == 0 {
		t.Fatalf("decode comments: %v (%s)", err, comms)
	}
	last := list[len(list)-1]
	actorRaw, _ := last["actor"]
	if got := jstr(jmap(t, actorRaw), "name"); got != "alice" {
		t.Fatalf("comment poster = %q, want alice (declared zed must be ignored)", got)
	}
}
