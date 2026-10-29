package httpapi

// task_filters_parent_test.go: REST end-to-end coverage for the epic-decomposition
// pair over the full production wiring (newStack = sqlite → UoW → use cases → App
// → mux). Proves: POST /tasks accepts parent_id and persists the child edge;
// invalid parents are rejected 400 WITHOUT creating anything; and GET /tasks
// honors parent_id / claimed / claimed_by / expired_claim (tri-state parsing,
// composition with each other, and total-count agreement).

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPCreateTask_WithParentID(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "epic project")

	parent := makeTask(t, stk, p.ID, "Epic parent", nil)
	child := makeTask(t, stk, p.ID, "child task", map[string]any{"parent_id": parent.ID})

	rows, total := listTasks(t, stk, "?parent_id="+url.QueryEscape(parent.ID))
	if total != 1 || len(rows) != 1 {
		t.Fatalf("GET /tasks?parent_id => total %d rows %d, want exactly the child", total, len(rows))
	}
	if rows[0].ID != child.ID {
		t.Errorf("child row = %s, want %s", rows[0].ID, child.ID)
	}

	// The parent filter never matches leaves or ancestors: the grandparent query
	// (no edge above) is empty.
	if _, total := listTasks(t, stk, "?parent_id="+url.QueryEscape(child.ID)); total != 0 {
		t.Errorf("leaf task matched as parent — total %d, want 0", total)
	}
}

func TestHTTPCreateTask_InvalidParent(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "p1")
	parent := makeTask(t, stk, p.ID, "parent in p1", nil)

	// Unknown parent: 400 + message names the problem; NOTHING is written.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "orphan", "project_id": p.ID,
		"parent_id": "01JMISSINGPARENT00000000000000",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks (unknown parent) = %d, want 400 (body %s)", code, string(body))
	}
	env := mustDecode[errResp](t, body)
	if env.Error != "invalid input" || !strings.Contains(env.Message, "does not exist") {
		t.Errorf("envelope = %+v, want invalid-input naming the missing parent", env)
	}
	if rows, total := listTasks(t, stk, "?project_id="+url.QueryEscape(p.ID)); total != 1 || len(rows) != 1 {
		t.Errorf("after rejected create: total %d rows %d, want only the original task", total, len(rows))
	}

	// Cross-project parent: same 400 shape.
	p2 := createProject(t, stk, "p2")
	parent2 := makeTask(t, stk, p2.ID, "parent in p2", nil)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "straddler", "project_id": p.ID,
		"parent_id": parent2.ID,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks (cross-project parent) = %d, want 400 (body %s)", code, string(body))
	}
	env = mustDecode[errResp](t, body)
	if env.Error != "invalid input" || !strings.Contains(env.Message, "belongs to project") {
		t.Errorf("envelope = %+v, want invalid-input naming the project mismatch", env)
	}

	// Omitted parent keeps working: no edge, plain create stays filter-invisible.
	makeTask(t, stk, p.ID, "standalone", nil)
	if _, total := listTasks(t, stk, "?parent_id="+url.QueryEscape(parent.ID)); total != 0 {
		t.Errorf("standalone task matched parent filter — total %d, want 0", total)
	}
}

func TestHTTPListTasks_ClaimAndExpiredFilters(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	fc := &fakeClock{now: t0}
	stk := newStackWithClock(t, fc)
	p := createProject(t, stk, "claims project")

	open := makeTask(t, stk, p.ID, "open task", nil)
	short := makeTask(t, stk, p.ID, "short lease", nil)
	long := makeTask(t, stk, p.ID, "long lease", nil)

	if code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+string(short.ID)+"/claim", map[string]any{
		"actor": actor("eve"), "ttl_sec": 10,
	}); code != http.StatusOK {
		t.Fatalf("claim short = %d (body %s)", code, string(body))
	}
	if code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+string(long.ID)+"/claim", map[string]any{
		"actor": actor("bob"), "ttl_sec": 3600,
	}); code != http.StatusOK {
		t.Fatalf("claim long = %d (body %s)", code, string(body))
	}

	want := func(ids ...string) []string { return ids }
	cases := []struct {
		name      string
		at        time.Time // fake-clock instant for this case
		query     string
		wantRows  []string
		wantTotal int64
	}{
		{"no filter = all", t0, "", want(open.ID, short.ID, long.ID), 3},
		{"claimed=true", t0, "?claimed=true", want(short.ID, long.ID), 2},
		{"claimed=false", t0, "?claimed=false", want(open.ID), 1},
		{"claimed_by=eve", t0, "?claimed_by=" + url.QueryEscape("eve"), want(short.ID), 1},
		{"claimed=false + claimed_by = empty", t0, "?claimed=false&claimed_by=eve", nil, 0},
		// Two minutes on: the 10s lease is stale, the hour lease still runs.
		{"expired=true after lapse", t0.Add(2 * time.Minute), "?expired_claim=true", want(short.ID), 1},
		{"expired=false after lapse", t0.Add(2 * time.Minute), "?expired_claim=false", want(open.ID, long.ID), 2},
		{"expired=true + claimed_by narrows", t0.Add(2 * time.Minute), "?expired_claim=true&claimed_by=eve", want(short.ID), 1},
		{"expired=false excludes the stale row", t0.Add(2 * time.Minute), "?expired_claim=false&claimed_by=eve", nil, 0},
	}
	for _, tc := range cases {
		fc.now = tc.at
		rows, total := listTasks(t, stk, tc.query)
		got := idsOf(rows)
		assertSameTaskIDs(t, tc.name, got, tc.wantRows)
		if total != tc.wantTotal {
			t.Errorf("%s: total = %d, want %d (COUNT uses the same predicate)", tc.name, total, tc.wantTotal)
		}
	}
}

func TestHTTPListTasks_MalformedFilterParams(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "strict project")
	makeTask(t, stk, p.ID, "task", nil)

	for _, q := range []string{"?claimed=banana", "?expired_claim=yes"} {
		code, body := doReq(t, stk.Handler, "GET", "/api/v1/tasks"+q, nil)
		if code != http.StatusBadRequest {
			t.Errorf("GET /tasks%s = %d, want 400 (body %s)", q, code, string(body))
		}
		if !strings.Contains(string(body), "invalid input") {
			t.Errorf("GET /tasks%s envelope %q missing invalid-input error", q, string(body))
		}
	}
}

func TestParseTaskFilter_NewParams(t *testing.T) {
	v := url.Values{}
	flt, err := parseTaskFilter(v)
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if flt.Claimed != nil || flt.ExpiredClaim != nil || flt.ParentID != "" || flt.ClaimedBy != "" {
		t.Errorf("zero query => %+v, want all new fields unset", flt)
	}

	bTrue, bFalse := true, false
	boolCase := func(name, val string, want *bool, wantErr bool) {
		v = url.Values{}
		v.Set(name, val)
		flt, err := parseTaskFilter(v)
		if (err != nil) != wantErr {
			t.Errorf("parse %s=%q: err = %v, wantErr %v", name, val, err, wantErr)
			return
		}
		if wantErr {
			return
		}
		var got *bool
		if name == "claimed" {
			got = flt.Claimed
		} else {
			got = flt.ExpiredClaim
		}
		if got == nil || (want != nil && *got != *want) {
			t.Errorf("%s=%q => %v, want %v", name, val, got, want)
		}
	}
	boolCase("claimed", "true", &bTrue, false)
	boolCase("claimed", "1", &bTrue, false)
	boolCase("claimed", "false", &bFalse, false)
	boolCase("claimed", "0", &bFalse, false)
	boolCase("claimed", "banana", nil, true)
	boolCase("expired_claim", "true", &bTrue, false)
	boolCase("expired_claim", "nope", nil, true)

	strCase := func(name, val string) {
		v = url.Values{}
		v.Set(name, val)
		flt, err := parseTaskFilter(v)
		if err != nil {
			t.Fatalf("parse %s=%q: %v", name, val, err)
		}
		var got string
		if name == "claimed_by" {
			got = flt.ClaimedBy
		} else {
			got = string(flt.ParentID)
		}
		if got != val {
			t.Errorf("%s = %q, want %q", name, got, val)
		}
	}
	strCase("claimed_by", "ada")
	strCase("parent_id", "01JABCXYZ")

	// Composes with existing params.
	v = url.Values{}
	v.Set("project_id", "p")
	v.Set("priority", "5")
	v.Set("claimed", "false")
	flt, err = parseTaskFilter(v)
	if err != nil {
		t.Fatalf("parse combined: %v", err)
	}
	if flt.ProjectID != "p" || flt.Priority == nil || *flt.Priority != 5 || flt.Claimed == nil || *flt.Claimed {
		t.Errorf("combined parse => %+v, want p + priority 5 + claimed=false", flt)
	}
}

// assertSameTaskIDs compares two ID sets (order-insensitive; rows arrive in
// canonical order but the fixtures mint in arbitrary order per case).
func assertSameTaskIDs(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: ids = %v (%d), want %v (%d)", name, got, len(got), want, len(want))
		return
	}
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	for _, id := range got {
		if !wantSet[id] {
			t.Errorf("%s: unexpected id %s in %v", name, id, got)
		}
	}
}

func getTaskDetail(t *testing.T, stk stack, id string) taskDetailDTO {
	t.Helper()
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d (body %s)", id, code, string(body))
	}
	return mustDecode[taskDetailDTO](t, body)
}

func blockedByEdgesOf(d taskDetailDTO, childID string) []relationDTO {
	var out []relationDTO
	for _, r := range d.Relations {
		if r.TaskID == childID && r.Kind == "blocked-by" {
			out = append(out, r)
		}
	}
	return out
}

func TestHTTPCreateTask_WithBlockedBy(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "blocked project")

	b1 := makeTask(t, stk, p.ID, "Migration half-applied", nil)
	b2 := makeTask(t, stk, p.ID, "API contract frozen", nil)
	child := makeTask(t, stk, p.ID, "feature on top", map[string]any{
		"blocked_by": []string{b1.ID, b2.ID},
	})

	d := getTaskDetail(t, stk, string(child.ID))
	edges := blockedByEdgesOf(d, string(child.ID))
	if len(edges) != 2 {
		t.Fatalf("detail relations = %+v, want exactly two blocked-by edges from %s", d.Relations, child.ID)
	}
	wantSet := map[string]bool{b1.ID: true, b2.ID: true}
	for _, e := range edges {
		if !wantSet[e.OtherTaskID] {
			t.Fatalf("unexpected blocked-by target %s (edges %+v)", e.OtherTaskID, edges)
		}
		wantSet[e.OtherTaskID] = true
	}
	// The computed blocker gate reflects the freshly written edges end-to-end.
	if !d.Blocked {
		t.Errorf("detail.blocked = false, want true (two open blockers)")
	}

	// Duplicates dedupe to one edge per unique blocker.
	dup := makeTask(t, stk, p.ID, "duplicate list", map[string]any{
		"blocked_by": []string{b1.ID, b2.ID, b1.ID},
	})
	if edges := blockedByEdgesOf(getTaskDetail(t, stk, string(dup.ID)), string(dup.ID)); len(edges) != 2 {
		t.Errorf("deduped edges = %+v, want one per unique blocker (2)", edges)
	}

	// parent_id + blocked_by compose: both edge kinds on the same task.
	epic := makeTask(t, stk, p.ID, "epic", nil)
	both := makeTask(t, stk, p.ID, "feature behind both", map[string]any{
		"parent_id":  epic.ID,
		"blocked_by": []string{b2.ID},
	})
	dBoth := getTaskDetail(t, stk, string(both.ID))
	kinds := map[string]bool{}
	for _, r := range dBoth.Relations {
		if r.TaskID == both.ID {
			kinds[r.Kind] = true
		}
	}
	if !kinds["child"] || !kinds["blocked-by"] {
		t.Errorf("combined relations = %+v, want BOTH child and blocked-by kinds", dBoth.Relations)
	}
}

func TestHTTPCreateTask_InvalidBlockedBy(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "strict project")
	seeded := makeTask(t, stk, p.ID, "existing blocker", nil)

	cases := []struct {
		name    string
		blocked map[string]any
		wantSub string
	}{
		{"unknown blocker", map[string]any{"blocked_by": []string{"01JMISSINGBLOCKER0000000000000"}}, "does not exist"},
		{"blank entry rejected", map[string]any{"blocked_by": []string{seeded.ID, ""}}, "empty task id"},
	}
	for _, tc := range cases {
		body := map[string]any{"actor": actor("eve"), "title": "orphan blocked", "project_id": p.ID}
		for k, v := range tc.blocked {
			body[k] = v
		}
		code, resp := doReq(t, stk.Handler, "POST", "/api/v1/tasks", body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: POST /tasks = %d, want 400 (body %s)", tc.name, code, string(resp))
		}
		env := mustDecode[errResp](t, resp)
		if env.Error != "invalid input" || !strings.Contains(env.Message, tc.wantSub) {
			t.Errorf("%s: envelope = %+v, want invalid-input containing %q", tc.name, env, tc.wantSub)
		}
		// Nothing written on rejection (the original row only).
		if rows, total := listTasks(t, stk, "?project_id="+url.QueryEscape(p.ID)); total != 1 || len(rows) != 1 {
			t.Errorf("%s: after rejected create: total %d rows %d, want only the seeded task", tc.name, total, len(rows))
		}
	}

	// Cross-project blocker: same 400 shape as the cross-project parent twin.
	p2 := createProject(t, stk, "other project")
	foreign := makeTask(t, stk, p2.ID, "blocker in p2", nil)
	code, resp := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "straddler blocked", "project_id": p.ID,
		"blocked_by": []string{foreign.ID},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks (cross-project blocker) = %d, want 400 (body %s)", code, string(resp))
	}
	env := mustDecode[errResp](t, resp)
	if env.Error != "invalid input" || !strings.Contains(env.Message, "belongs to project") {
		t.Errorf("cross-project envelope = %+v, want invalid-input naming the project mismatch", env)
	}

	// Omitted blocked_by keeps working: plain create stays unblocked.
	plain := makeTask(t, stk, p.ID, "standalone", nil)
	if d := getTaskDetail(t, stk, string(plain.ID)); d.Blocked || len(d.Relations) != 0 {
		t.Errorf("standalone detail = (blocked %v, relations %+v), want false/none", d.Blocked, d.Relations)
	}
}
