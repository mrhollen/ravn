package httpapi

import (
	"bufio"
	"context"
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
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// events_scoping_test.go proves the multi-user epic T5 SSE per-actor scoping gate:
// both streaming endpoints (GET /api/v1/projects/{id}/events and
// GET /api/v1/events?project_id=) resolve the requesting identity and enforce
// project visibility BEFORE opening the stream, so a non-member gets a JSON 403 with
// zero event leakage while members / owners / operators stream exactly as before.

// newSSEScopingServer wires a full stack — REAL broker + durable event store (so live
// forwarding AND Last-Event-ID replay have actual data to move) — fronted by the
// mode-aware AuthGate and exposed behind an httptest.Server for live SSE reads. mode is
// AuthModeToken for the legacy shared-token/global proof or AuthModeMultiuser for the
// membership isolation proof; scoping is nil (default/global — gate no-op) or
// membershipCfg() (per-project visibility enforced).
func newSSEScopingServer(t *testing.T, mode AuthMode, scoping *domain.ScopingConfig) (*scopingStack, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ravn-sse-scoping.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
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
		IdentityOpen: true,
		Broker:       broker,
		Events:       sqlite.NewEventStore(db), // Last-Event-ID replay path (pool-bound)
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	handler := AuthGate(mode, scopingSharedToken, auth)(mux)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &scopingStack{stack: stack{Handler: handler, App: app}, db: db}, srv
}

// firstEventID returns the durable id of a task's created event (its get_task event
// tail head) so callers can build a realistic Last-Event-ID replay cursor. bearer is
// required because membership-mode read routes are credential-gated.
func firstEventID(t *testing.T, h http.Handler, tid, bearer string) string {
	t.Helper()
	code, body := authReq(t, h, "GET", "/api/v1/tasks/"+tid, nil, bearer)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d: %s", tid, code, body)
	}
	var d struct {
		Events []eventDTO `json:"events"`
	}
	mustJSON(t, body, &d)
	if len(d.Events) == 0 {
		t.Fatalf("task %s has no events in its resume payload", tid)
	}
	return d.Events[0].ID
}

// sseConnectAuthed is the bearer-carrying twin of sseConnect (events_test.go): it opens an
// SSE stream presenting a session/api_token or shared-token Bearer — required in multiuser
// mode, where the gate rejects an unauthenticated connect with 401 before any scoping runs.
func sseConnectAuthed(t *testing.T, srv *httptest.Server, url, bearer, lastEventIDHeader string) (*bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		cancel()
		t.Fatalf("new SSE request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if lastEventIDHeader != "" {
		req.Header.Set("Last-Event-ID", lastEventIDHeader)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect SSE: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("SSE connect status = %d, want 200 (cursor %q)", res.StatusCode, lastEventIDHeader)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream", ct)
	}
	return bufio.NewReader(res.Body), cancel
}

// --- AC3 + global-mode no-op -------------------------------------------------------

// TestSSE_GlobalMode_LegacyTokenStreams proves (AC3) the legacy ?token= query channel on
// the SSE GET route still authenticates and streams in DEFAULT/global mode, and that the
// T5 gate is a guaranteed no-op there: with Scoping unset every authenticated client —
// including the shared-token operator — streams exactly as today (replay/live/frame
// format unchanged). No 403 can appear because checkProject short-circuits to allow.
func TestSSE_GlobalMode_LegacyTokenStreams(t *testing.T) {
	t.Parallel()
	stk, srv := newSSEScopingServer(t, AuthModeToken, nil) // Scoping nil = default/global

	// Seed a project via the shared-token operator principal (token mode).
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("op", map[string]any{"name": "Global SSE Room"}), scopingSharedToken)
	if code != http.StatusCreated {
		t.Fatalf("POST /projects = %d: %s", code, body)
	}
	pid := extractID(t, body)

	// AC3: the ?token= legacy channel on the path-based SSE route streams (200 SSE), and a
	// live event created after connecting is delivered in the unchanged frame format.
	r, cancel := sseConnect(t, srv, srv.URL+"/api/v1/projects/"+pid+"/events?token="+scopingSharedToken, "")
	defer cancel()
	code, tb := authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("op", map[string]any{"project_id": pid, "title": "live work"}), scopingSharedToken)
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d: %s", code, tb)
	}
	tid := extractID(t, tb)
	got := readSSETaskEvents(t, r, 1)
	if taskIDOfSSEFrame(t, got[0]) != tid {
		t.Fatalf("live SSE frame task = %q, want %s", taskIDOfSSEFrame(t, got[0]), tid)
	}

	// The ?project_id= query variant uses the identical gate and streams too.
	r2, cancel2 := sseConnect(t, srv, srv.URL+"/api/v1/events?project_id="+pid+"&token="+scopingSharedToken, "")
	defer cancel2()
	code, tb = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("op", map[string]any{"project_id": pid, "title": "second live work"}), scopingSharedToken)
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks (2nd) = %d: %s", code, tb)
	}
	tid2 := extractID(t, tb)
	got2 := readSSETaskEvents(t, r2, 1)
	if taskIDOfSSEFrame(t, got2[0]) != tid2 {
		t.Fatalf("live SSE frame (query route) task = %q, want %s", taskIDOfSSEFrame(t, got2[0]), tid2)
	}

	// No-token request is still rejected by the (unchanged) legacy gate — a 401, never a
	// stream. This confirms the T5 gate did not loosen or shift auth behavior in global mode.
	res, err := srv.Client().Get(srv.URL + "/api/v1/projects/" + pid + "/events")
	if err != nil {
		t.Fatalf("no-token SSE GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token SSE = %d, want 401 (auth gate unchanged)", res.StatusCode)
	}
}

// --- AC1 + AC4: membership visibility gate with no leakage ------------------------

// TestSSE_Membership_NonMemberDenied_NoLeak proves (AC1/AC2/AC4) that in MEMBERSHIP mode
// a non-member of the target project is rejected with 403 and receives NO event frames —
// not even replayable ones — while the project's owner streams the SAME replay cursor
// normally. The identical (project, cursor) pair gets opposite outcomes only because of the
// visibility gate, which fires before any frame or replayed event is emitted.
func TestSSE_Membership_NonMemberDenied_NoLeak(t *testing.T) {
	t.Parallel()
	stk, srv := newSSEScopingServer(t, AuthModeMultiuser, membershipCfg())

	mustCreateAccount(t, stk, "alice", "alice-password-1") // owner of the room
	mustCreateAccount(t, stk, "bob", "bob-password-2")     // non-member: no relationship at all
	tokA := mustLogin(t, stk, "alice", "alice-password-1")
	tokB := mustLogin(t, stk, "bob", "bob-password-2")

	// alice owns the project (created_by = her identity → RoleOwner).
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor("alice", map[string]any{"name": "Alice Room"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice POST /projects = %d: %s", code, body)
	}
	pid := extractID(t, body)

	// Seed a durable + published event (E1 = the replay cursor), then a SECOND one (E2) that
	// sits AFTER the cursor so a reconnecting client has a real missed event to replay.
	code, tb := authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{"project_id": pid, "title": "seeded work"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice POST /tasks = %d: %s", code, tb)
	}
	e1 := firstEventID(t, stk.Handler, extractID(t, tb), tokA)
	code, tb = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("alice", map[string]any{"project_id": pid, "title": "missed work"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice POST /tasks (2nd) = %d: %s", code, tb)
	}
	tidMissed := extractID(t, tb)

	// NON-MEMBER via the path-based route with Last-Event-ID replay: bob is authenticated
	// (a valid session token) but holds no relationship to the project, so he must be
	// rejected with 403 JSON — NOT a text/event-stream — and receive NO "id:"/"data:" frames
	// anywhere in the body (zero leakage). The bearer keeps him past the auth gate so the
	// failure we assert is the VISIBILITY gate, not an auth 401.
	assertSSEDeniedNoLeak(t, srv, tokB, "/api/v1/projects/"+pid+"/events", e1, http.Header{"Last-Event-ID": {e1}})

	// NON-MEMBER via the ?project_id= query route with a ?last_event_id= cursor: same 403.
	assertSSEDeniedNoLeak(t, srv, tokB, "/api/v1/events?project_id="+pid+"&last_event_id="+e1, e1, nil)

	// The SAME project + cursor for the OWNER streams and replays the missed event (E2) in
	// the unchanged frame format — proving the denial above is the gate, not absent data.
	url := srv.URL + "/api/v1/projects/" + pid + "/events"
	r, cancel := sseConnectAuthed(t, srv, url, tokA, e1)
	defer cancel()
	frames := readSSETaskEvents(t, r, 1)
	if taskIDOfSSEFrame(t, frames[0]) != tidMissed {
		t.Fatalf("owner replay frame task = %q, want the missed event %s", taskIDOfSSEFrame(t, frames[0]), tidMissed)
	}

	// The shared-token OPERATOR also streams an invisible (to bob) project under membership —
	// the administrative backdoor's behavior must not change when scoping turns on.
	op, cancelOp := sseConnectAuthed(t, srv, url, scopingSharedToken, "")
	defer cancelOp()
	code, tb = authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("op", map[string]any{"project_id": pid, "title": "operator live"}), scopingSharedToken)
	if code != http.StatusCreated {
		t.Fatalf("op POST /tasks = %d: %s", code, tb)
	}
	got := readSSETaskEvents(t, op, 1)
	if taskIDOfSSEFrame(t, got[0]) != extractID(t, tb) {
		t.Fatalf("operator live frame task mismatch")
	}
}

// assertSSEDeniedNoLeak issues an authenticated GET against the (relative to srv.URL) SSE
// endpoint and asserts the response is a JSON 403 scoping denial — not a text/event-stream
// — and that the body contains NO SSE frame markers (proves no event was leaked, including
// via replay). bearer is the requesting identity's token (so the assertion targets the
// VISIBILITY gate, past auth); extraHdr may be nil.
func assertSSEDeniedNoLeak(t *testing.T, srv *httptest.Server, bearer, pathWithQuery, cursor string, extraHdr http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+pathWithQuery, nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range extraHdr {
		for _, x := range v {
			req.Header.Add(k, x)
		}
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE request %s: %v", pathWithQuery, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("%s: non-member SSE = %d, want 403 (cursor %q)", pathWithQuery, res.StatusCode, cursor)
	}
	if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Fatalf("%s: denial carries Content-Type %q — the stream must never open", pathWithQuery, ct)
	}
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 512)
	for {
		n, rerr := res.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if rerr != nil {
			break // io.EOF or timeout: the body of a JSON 403 is small and complete
		}
	}
	body := string(buf)
	for _, marker := range []string{"id: ", "data: ", ": connected", ": ping"} {
		if strings.Contains(body, marker) {
			t.Fatalf("%s: non-member 403 body leaked an SSE frame (%q present): %s", pathWithQuery, marker, body)
		}
	}
	var en errResp
	mustJSON(t, []byte(body), &en)
	if en.Error != "forbidden" {
		t.Fatalf("%s: non-member 403 body = %q, want error \"forbidden\"", pathWithQuery, body)
	}
}
