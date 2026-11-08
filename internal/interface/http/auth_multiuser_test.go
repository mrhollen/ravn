package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// auth_multiuser_test.go drives the T3 AuthGate end-to-end against a real SQLite-backed App:
// off (passthrough), token (shared bearer + operator act-as provisioning), and multiuser
// (public login → session, per-agent api_token lifecycle, credential≠operator, idempotent
// revocation, and no accidental query-string auth on regular API routes). It uses a fast
// deterministic hasher — pwhash itself is covered by its own package tests.

type fastTestHasher struct{}

func (fastTestHasher) Hash(pw string) (string, error) { return "$t$" + pw, nil }
func (fastTestHasher) Check(stored, pw string) bool   { return stored == "$t$"+pw }

// newGatedStack wires the full production stack (real sqlite uow + every use case + a real
// Auth) and returns it wrapped in AuthGate for the requested mode/shared token.
func newGatedStack(t testing.TB, mode AuthMode, sharedToken string) stack {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ravn-gate.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()
	auth := application.NewAuth(uow, now, mint, fastTestHasher{}, time.Hour)

	app := NewApp(Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
		Auth:       auth,
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	return stack{Handler: AuthGate(mode, sharedToken, auth)(mux), App: app}
}

// authReq is doReq with an optional Bearer token attached.
func authReq(t testing.TB, h http.Handler, method, path string, body any, bearer string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://ravn.test"+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

type loginOut struct {
	Token    string `json:"token"`
	Identity struct {
		Name string `json:"name"`
	} `json:"identity"`
}

type mintOut struct {
	Token string `json:"token"`
	Meta  struct {
		ID string `json:"id"`
	} `json:"meta"`
}

func mustJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode response %q: %v", string(body), err)
	}
}

// --- off -------------------------------------------------------------------------

func TestGateOffPassthrough(t *testing.T) {
	stk := newGatedStack(t, AuthModeOff, "")

	// No bearer required on an ordinary API route.
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, ""); code != http.StatusOK {
		t.Fatalf("off mode GET /projects = %d, want 200 (passthrough)", code)
	}
	// Provisioning is reachable but the anonymous principal is not an operator → 403.
	if code, _ := authReq(t, stk.Handler, "POST", "/api/v1/auth/accounts",
		map[string]any{"name": "x", "password": "password123"}, ""); code != http.StatusForbidden {
		t.Fatalf("off mode provisioning = %d, want 403 (no operator principal)", code)
	}
}

// --- token -----------------------------------------------------------------------

func TestGateTokenMode(t *testing.T) {
	const tok = "s3cret"
	stk := newGatedStack(t, AuthModeToken, tok)

	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("token mode without bearer = %d, want 401", code)
	}
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("token mode wrong bearer = %d, want 401", code)
	}
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, tok); code != http.StatusOK {
		t.Fatalf("token mode correct bearer = %d, want 200", code)
	}

	// The shared token is an operator: it may provision accounts (gate passes + IsOperator).
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/auth/accounts",
		map[string]any{"name": "alice", "password": "password123"}, tok)
	if code != http.StatusCreated {
		t.Fatalf("operator account create = %d (body %s), want 201", code, body)
	}
}

// --- multiuser -------------------------------------------------------------------

func TestGateMultiuserFullFlow(t *testing.T) {
	const opToken = "s3cret"
	stk := newGatedStack(t, AuthModeMultiuser, opToken)

	// Anonymous access to a normal API route is rejected.
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("multiuser anonymous GET /projects = %d, want 401", code)
	}

	// The operator (shared token) bootstraps a human account — works even pre-login.
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/auth/accounts",
		map[string]any{"name": "alice", "password": "password123"}, opToken)
	if code != http.StatusCreated {
		t.Fatalf("operator account create = %d (body %s), want 201", code, body)
	}

	// /auth/login is PUBLIC in multiuser (no bearer). Wrong password → 401; right → session.
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/auth/login",
		map[string]any{"username": "alice", "password": "wrong"}, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("login wrong password = %d, want 401", code)
	}
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/auth/login",
		map[string]any{"username": "alice", "password": "password123"}, "")
	if code != http.StatusOK {
		t.Fatalf("login = %d (body %s), want 200", code, body)
	}
	var lo loginOut
	mustJSON(t, body, &lo)
	if lo.Token == "" || lo.Identity.Name != "alice" {
		t.Fatalf("login response = %+v, want alice + token", lo)
	}
	session := lo.Token

	// The session bearer authenticates as a credential principal (not an operator).
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, session); code != http.StatusOK {
		t.Fatalf("session GET /projects = %d, want 200", code)
	}
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/auth/session", nil, session)
	if code != http.StatusOK {
		t.Fatalf("session inspect = %d (body %s), want 200", code, body)
	}

	// A credential principal may NOT provision accounts → 403 (spoof prevention).
	code, _ = authReq(t, stk.Handler, "POST", "/api/v1/auth/accounts",
		map[string]any{"name": "mallory", "password": "password123"}, session)
	if code != http.StatusForbidden {
		t.Fatalf("credential provisioning = %d, want 403 (operator-only)", code)
	}

	// Mint a per-agent api_token as the operator; it authenticates independently.
	code, body = authReq(t, stk.Handler, "POST", "/api/v1/auth/tokens",
		map[string]any{"name": "ci-bot", "kind": "agent", "label": "ci"}, opToken)
	if code != http.StatusCreated {
		t.Fatalf("operator mint token = %d (body %s), want 201", code, body)
	}
	var mo mintOut
	mustJSON(t, body, &mo)
	if mo.Token == "" || mo.Meta.ID == "" {
		t.Fatalf("mint response = %+v, want token + meta id", mo)
	}
	apiToken := mo.Token

	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, apiToken); code != http.StatusOK {
		t.Fatalf("api_token GET /projects = %d, want 200", code)
	}
	// The operator lists this agent's tokens.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/auth/tokens?name=ci-bot", nil, opToken)
	if code != http.StatusOK {
		t.Fatalf("list tokens = %d (body %s), want 200", code, body)
	}
	var rows []map[string]any
	mustJSON(t, body, &rows)
	if len(rows) != 1 {
		t.Fatalf("token list length = %d, want 1", len(rows))
	}

	// Revocation is idempotent and kills the token.
	code, _ = authReq(t, stk.Handler, "DELETE", "/api/v1/auth/tokens/"+mo.Meta.ID, nil, opToken)
	if code != http.StatusNoContent {
		t.Fatalf("revoke api_token = %d, want 204", code)
	}
	code, _ = authReq(t, stk.Handler, "DELETE", "/api/v1/auth/tokens/"+mo.Meta.ID, nil, opToken)
	if code != http.StatusNoContent {
		t.Fatalf("second revoke (idempotent) = %d, want 204", code)
	}
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects", nil, apiToken); code != http.StatusUnauthorized {
		t.Fatalf("revoked api_token GET /projects = %d, want 401", code)
	}

	// Session revocation is idempotent and blocks the session.
	if code, _ := authReq(t, stk.Handler, "DELETE", "/api/v1/auth/session", nil, session); code != http.StatusNoContent {
		t.Fatalf("revoke session = %d, want 204", code)
	}
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/auth/session", nil, session); code != http.StatusUnauthorized {
		t.Fatalf("revoked session inspect = %d, want 401", code)
	}

	// Query-string ?token= is NOT an auth channel on a regular (non-SSE) API route.
	if code, _ := authReq(t, stk.Handler, "GET", "/api/v1/projects?token="+opToken, nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("query-token on non-SSE route = %d, want 401 (no query auth leak)", code)
	}
}
