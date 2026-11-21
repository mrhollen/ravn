package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestProjectsOverview_RoutePrecedenceAndEmptyDB(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	// Static /projects/overview must match handleProjectsOverview (200 empty list),
	// never handleGetProject (which rejects non-ULID "overview" with 400).
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/overview", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects/overview = %d, want 200 (body %s)", code, string(body))
	}
	var env struct {
		Projects []projectOverviewRowDTO `json:"projects"`
		Total    int                     `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode overview: %v (%s)", err, string(body))
	}
	if env.Projects == nil || len(env.Projects) != 0 || env.Total != 0 {
		t.Fatalf("empty overview = %+v, want non-nil empty projects and total 0", env)
	}

	// Contrast: a non-ULID segment still reaches handleGetProject and fails ParseID with 400.
	code, _ = doReq(t, stk.Handler, "GET", "/api/v1/projects/not-overview", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("GET /api/v1/projects/not-overview = %d, want 400 from handleGetProject", code)
	}
}

func TestProjectsOverview_EndToEndHealthCountsAndFilters(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	N := base.Add(6 * time.Hour) // 18:00:00Z
	fc := &fakeClock{now: base}
	stk := newStackWithClock(t, fc)

	// 1. Project Alpha: rich mix of counts, claims, stalled, needs_attention, archived
	alpha := createProject(t, stk, "Alpha")

	// Empty project Beta (created at base+1h, no tasks → last_activity_at falls back to project updated_at)
	fc.now = base.Add(time.Hour)
	beta := createProject(t, stk, "Beta")

	// Archived project Gamma (archived at base+2h, has one todo task)
	fc.now = base.Add(2 * time.Hour)
	gamma := createProject(t, stk, "Gamma")
	_ = makeTask(t, stk, gamma.ID, "gamma todo", nil)
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+gamma.ID, map[string]any{
		"actor": actor("ada"), "archived": true,
	})
	if code != http.StatusOK {
		t.Fatalf("archive gamma = %d: %s", code, string(body))
	}

	// Seed Alpha tasks at varying times relative to N (18:00:00Z)
	fc.now = N.Add(-120 * time.Minute) // 16:00:00Z
	tZombie := makeTask(t, stk, alpha.ID, "zombie claim", nil)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tZombie.ID+"/claim", map[string]any{
		"actor": actor("bot-1"), "ttl_sec": 60, // expires 16:01:00Z <= N
	})
	if code != http.StatusOK {
		t.Fatalf("claim zombie = %d: %s", code, string(body))
	}

	tSilent := makeTask(t, stk, alpha.ID, "silent doing", nil) // updated 16:00:00Z → idle 120m
	patchTask(t, stk, tSilent.ID, 1, map[string]any{"status": "doing"})

	fc.now = N.Add(-90 * time.Minute) // 16:30:00Z
	tBeat := makeTask(t, stk, alpha.ID, "heartbeat stale", nil)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tBeat.ID+"/claim", map[string]any{
		"actor": actor("bot-2"), "ttl_sec": 3600, // would expire 17:30
	})
	if code != http.StatusOK {
		t.Fatalf("claim beat = %d: %s", code, string(body))
	}
	fc.now = N.Add(-30 * time.Minute) // 17:30:00Z heartbeat refreshes expiry to 18:30:00Z > N, leaves updated_at at 16:30:00Z
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tBeat.ID+"/heartbeat", map[string]any{
		"actor": actor("bot-2"),
	})
	if code != http.StatusOK {
		t.Fatalf("heartbeat beat = %d: %s", code, string(body))
	}

	fc.now = N.Add(-20 * time.Minute) // 17:40:00Z
	tLive := makeTask(t, stk, alpha.ID, "live doing", nil)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tLive.ID+"/claim", map[string]any{
		"actor": actor("bot-3"), "ttl_sec": 3600, // expires 18:40:00Z > N
	})
	if code != http.StatusOK {
		t.Fatalf("claim live = %d: %s", code, string(body))
	}

	tTodoQ := makeTask(t, stk, alpha.ID, "todo question", nil)
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tTodoQ.ID+"/comments", map[string]any{
		"actor": actor("bot-1"), "kind": "question", "body": "need clarification",
	})
	if code != http.StatusCreated {
		t.Fatalf("comment question = %d", code)
	}

	// tLive gets a handoff comment, then a progress comment → NOT needs_attention
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tLive.ID+"/comments", map[string]any{
		"actor": actor("bot-3"), "kind": "handoff", "body": "handing off",
	})
	if code != http.StatusCreated {
		t.Fatalf("comment handoff = %d", code)
	}
	fc.now = N.Add(-15 * time.Minute)
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tLive.ID+"/comments", map[string]any{
		"actor": actor("bot-3"), "kind": "progress", "body": "resumed",
	})
	if code != http.StatusCreated {
		t.Fatalf("comment progress = %d", code)
	}

	// Done task with question comment → excluded from needs_attention; sets latest non-archived task updated_at = N-10m
	fc.now = N.Add(-10 * time.Minute) // 17:50:00Z
	tDone := makeTask(t, stk, alpha.ID, "done task", nil)
	patchTask(t, stk, tDone.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, tDone.ID, 2, map[string]any{"status": "done"})
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tDone.ID+"/comments", map[string]any{
		"actor": actor("bot-1"), "kind": "question", "body": "done q",
	})
	if code != http.StatusCreated {
		t.Fatalf("comment done = %d", code)
	}

	// Archived task at N-1m with handoff comment → completely excluded
	fc.now = N.Add(-1 * time.Minute) // 17:59:00Z
	tArch := makeTask(t, stk, alpha.ID, "archived task", nil)
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tArch.ID+"/comments", map[string]any{
		"actor": actor("bot-1"), "kind": "handoff", "body": "arch h",
	})
	if code != http.StatusCreated {
		t.Fatalf("comment arch = %d", code)
	}
	patchTask(t, stk, tArch.ID, 1, map[string]any{"archived": true})

	// Evaluate overview at N
	fc.now = N
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/overview", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects/overview = %d, want 200 (body %s)", code, string(body))
	}

	// Verify exact JSON fields on wire (zeros present, no omission of health keys)
	var raw struct {
		Projects []map[string]any `json:"projects"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw overview: %v", err)
	}
	if raw.Total != 2 || len(raw.Projects) != 2 {
		t.Fatalf("total = %d (%d rows), want 2", raw.Total, len(raw.Projects))
	}

	var env struct {
		Projects []projectOverviewRowDTO `json:"projects"`
		Total    int                     `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode typed overview: %v", err)
	}

	aRow := env.Projects[0]
	if aRow.ID != alpha.ID {
		t.Fatalf("projects[0].id = %s, want %s", aRow.ID, alpha.ID)
	}
	if aRow.TaskCounts != (statusCountDTO{Todo: 1, Doing: 4, Done: 1}) {
		t.Errorf("alpha task_counts = %+v, want {todo:1 doing:4 done:1}", aRow.TaskCounts)
	}
	if aRow.Health.ActiveClaims != 2 || aRow.Health.ExpiredClaims != 1 || aRow.Health.Stalled != 3 || aRow.Health.NeedsAttention != 1 {
		t.Errorf("alpha health = %+v, want active=2 expired=1 stalled=3 needs_attention=1", aRow.Health)
	}
	if aRow.Health.LastActivityAt != N.Add(-10*time.Minute).Format(time.RFC3339) {
		t.Errorf("alpha last_activity_at = %q, want %s", aRow.Health.LastActivityAt, N.Add(-10*time.Minute).Format(time.RFC3339))
	}

	// Beta (empty): zero counts/health, last_activity_at == Beta's updated_at
	bRow := env.Projects[1]
	if bRow.ID != beta.ID {
		t.Fatalf("projects[1].id = %s, want %s", bRow.ID, beta.ID)
	}
	if bRow.TaskCounts != (statusCountDTO{}) {
		t.Errorf("beta task_counts = %+v, want all zeros", bRow.TaskCounts)
	}
	if bRow.Health != (projectHealthDTO{LastActivityAt: base.Add(time.Hour).Format(time.RFC3339)}) {
		t.Errorf("beta health = %+v, want zeros and last_activity_at=%s", bRow.Health, base.Add(time.Hour).Format(time.RFC3339))
	}

	// Custom stalled_minutes=15 makes tLive (updated N-20m) stalled too → stalled=4
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/overview?stalled_minutes=15", nil)
	if code != http.StatusOK {
		t.Fatalf("GET overview?stalled_minutes=15 = %d: %s", code, string(body))
	}
	env15 := mustDecode[struct {
		Projects []projectOverviewRowDTO `json:"projects"`
	}](t, body)
	if env15.Projects[0].Health.Stalled != 4 {
		t.Errorf("alpha stalled (15m) = %d, want 4", env15.Projects[0].Health.Stalled)
	}

	// stalled_minutes=0 uses default 60m (matches handleProjectReport)
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/overview?stalled_minutes=0", nil)
	if code != http.StatusOK {
		t.Fatalf("GET overview?stalled_minutes=0 = %d: %s", code, string(body))
	}
	env0 := mustDecode[struct {
		Projects []projectOverviewRowDTO `json:"projects"`
	}](t, body)
	if env0.Projects[0].Health.Stalled != 3 {
		t.Errorf("alpha stalled (0 -> default 60m) = %d, want 3", env0.Projects[0].Health.Stalled)
	}

	// ?archived=true returns only Gamma with its todo=1
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/overview?archived=true", nil)
	if code != http.StatusOK {
		t.Fatalf("GET overview?archived=true = %d: %s", code, string(body))
	}
	envArch := mustDecode[struct {
		Projects []projectOverviewRowDTO `json:"projects"`
		Total    int                     `json:"total"`
	}](t, body)
	if envArch.Total != 1 || len(envArch.Projects) != 1 || envArch.Projects[0].ID != gamma.ID {
		t.Fatalf("archived overview = %+v, want only gamma", envArch)
	}
	if envArch.Projects[0].TaskCounts != (statusCountDTO{Todo: 1}) {
		t.Errorf("gamma task_counts = %+v, want todo=1", envArch.Projects[0].TaskCounts)
	}

	// Existing GET /api/v1/projects remains unchanged (no "health" field)
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects = %d: %s", code, string(body))
	}
	var rawList struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(body, &rawList); err != nil {
		t.Fatalf("decode GET /projects: %v", err)
	}
	for _, item := range rawList.Projects {
		if _, hasHealth := item["health"]; hasHealth {
			t.Fatalf("GET /api/v1/projects unexpectedly contains health key: %v", item)
		}
	}
}

func TestProjectsOverview_ScopingMineAndOwners(t *testing.T) {
	t.Parallel()
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "")
	op := scopingSharedToken

	mustCreateAccount(t, stk, "alice", "password123")
	mustCreateAccount(t, stk, "bob", "password123")
	mustCreateAccount(t, stk, "charlie", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")
	tokC := mustLogin(t, stk, "charlie", "password123")

	createAs := func(tok, actorName, projName string) string {
		code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects", withActor(actorName, map[string]any{"name": projName}), tok)
		if code != http.StatusCreated {
			t.Fatalf("create project %s = %d: %s", projName, code, body)
		}
		return extractID(t, body)
	}

	pAlice := createAs(tokA, "alice", "AliceOwned")
	pShared := createAs(op, "operator", "OpsShared")
	pCharlie := createAs(tokC, "charlie", "CharlieSecret")

	// Bob is viewer on pShared only; Charlie's project has sensitive tasks that must never leak to Alice/Bob.
	seedMember(t, stk, domain.ID(pShared), "bob", "viewer")

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/tasks", withActor("charlie", map[string]any{
		"title": "secret work", "project_id": pCharlie,
	}), tokC)
	if code != http.StatusCreated {
		t.Fatalf("charlie create task = %d: %s", code, body)
	}
	tC := extractID(t, body)
	_, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tC+"/claim", withActor("charlie", nil), tokC)
	_, _ = authReq(t, stk.Handler, "POST", "/api/v1/tasks/"+tC+"/comments", withActor("charlie", map[string]any{
		"kind": "question", "body": "secret q",
	}), tokC)

	getOverview := func(tok, query string) struct {
		Projects []projectOverviewRowDTO `json:"projects"`
		Total    int                     `json:"total"`
	} {
		code, b := authReq(t, stk.Handler, "GET", "/api/v1/projects/overview"+query, nil, tok)
		if code != http.StatusOK {
			t.Fatalf("GET overview%s = %d: %s", query, code, b)
		}
		return mustDecode[struct {
			Projects []projectOverviewRowDTO `json:"projects"`
			Total    int                     `json:"total"`
		}](t, b)
	}

	// Alice sees only AliceOwned (with resolved Owner = alice)
	resA := getOverview(tokA, "")
	if resA.Total != 1 || resA.Projects[0].ID != pAlice {
		t.Fatalf("alice overview = %+v, want only [%s]", resA, pAlice)
	}
	if resA.Projects[0].Owner == nil || resA.Projects[0].Owner.Name != "alice" {
		t.Errorf("alice overview owner = %+v, want alice", resA.Projects[0].Owner)
	}

	// Bob sees only OpsShared (no owner since operator-created)
	resB := getOverview(tokB, "")
	if resB.Total != 1 || resB.Projects[0].ID != pShared || resB.Projects[0].Owner != nil {
		t.Fatalf("bob overview = %+v, want only [%s] with nil owner", resB, pShared)
	}

	// Bob ?mine=created sees empty list (created nothing)
	resBMine := getOverview(tokB, "?mine=created")
	if resBMine.Total != 0 || len(resBMine.Projects) != 0 {
		t.Fatalf("bob ?mine=created = %+v, want empty", resBMine)
	}

	// Alice ?mine=created sees AliceOwned
	resAMine := getOverview(tokA, "?mine=created")
	if resAMine.Total != 1 || resAMine.Projects[0].ID != pAlice {
		t.Fatalf("alice ?mine=created = %+v, want [%s]", resAMine, pAlice)
	}

	// Operator sees all 3 including Charlie's health rollup
	resOp := getOverview(op, "")
	if resOp.Total != 3 {
		t.Fatalf("operator overview total = %d, want 3", resOp.Total)
	}
	var foundCharlie *projectOverviewRowDTO
	for i := range resOp.Projects {
		if resOp.Projects[i].ID == pCharlie {
			foundCharlie = &resOp.Projects[i]
		}
	}
	if foundCharlie == nil || foundCharlie.Health.ActiveClaims != 1 || foundCharlie.Health.NeedsAttention != 1 {
		t.Fatalf("operator view of CharlieSecret = %+v, want active_claims=1 needs_attention=1", foundCharlie)
	}
}
