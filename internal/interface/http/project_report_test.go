package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// project_report_test.go: end-to-end tests for GET /api/v1/projects/{id}/report over
// the FULL production wiring (real SQLite, real use cases, clock injected) — this
// layer pins what the unit tests cannot: the SQLite MaxAtByTasks query, RFC3339 /
// stalled_minutes parameter parsing, the exact JSON wire shape, and the 404/400
// envelopes. The digest semantics themselves are pinned in
// internal/application/project_report_test.go.

const reportBase = "2026-09-25T18:00:00Z" // report instant N = base + 30h

func TestProjectReport_EndToEnd(t *testing.T) {
	base, err := time.Parse(time.RFC3339, reportBase)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	N := base.Add(30 * time.Hour) // the instant every digest window is measured against
	fc := &fakeClock{now: base}
	stk := newStackWithClock(t, fc)
	setAgo := func(minutes int) { fc.now = N.Add(-time.Duration(minutes) * time.Minute) } // "N ago" in minutes

	// --- seed (clock advances chronologically; all stamps land where claimed) --------
	p := createProject(t, stk, "Digest")
	doneOut := makeTask(t, stk, p.ID, "Old Done", nil)        // p0, unassigned
	zombie := makeTask(t, stk, p.ID, "Zombie Lease", nil)     // p0, unassigned
	stalled := makeTask(t, stk, p.ID, "Silent Worker", map[string]any{"priority": 1})
	beating := makeTask(t, stk, p.ID, "Beating Heart", map[string]any{
		"priority": 2, "assignee": actor("agent-h"),
	})
	live := makeTask(t, stk, p.ID, "Live Work", map[string]any{
		"priority": 3, "assignee": actor("agent-a"),
	})
	blocker := makeTask(t, stk, p.ID, "Foundation", map[string]any{"priority": 4}) // open (todo) blocker
	dep := makeTask(t, stk, p.ID, "Dependent", nil)                               // p0, unassigned
	doneIn := makeTask(t, stk, p.ID, "Fresh Done", nil)                            // p0, unassigned

	if _, _, err := stk.App.Relations.Relate(context.Background(),
		domain.Actor{Type: domain.ActorAgent, Name: "eve"}, domain.ID(dep.ID), domain.ID(blocker.ID),
		domain.RelationBlockedBy, application.RelateAdd); err != nil {
		t.Fatalf("seed blocked-by edge: %v", err)
	}

	// Old Done → done at base (30h before N): outside the default 24h window.
	patchTask(t, stk, doneOut.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, doneOut.ID, 2, map[string]any{"status": "done"})

	// Zombie Lease: claimed at base with the DEFAULT 900s TTL → lapsed ~30h ago.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+zombie.ID+"/claim", map[string]any{"actor": actor("agent-b")})
	if code != http.StatusOK {
		t.Fatalf("claim zombie = %d (body %s)", code, string(body))
	}

	setAgo(300) // N−5h
	patchTask(t, stk, doneIn.ID, 1, map[string]any{"status": "doing"})
	fc.now = N.Add(-299*time.Minute) // completed_at = N−4h59m → INSIDE the default window
	patchTask(t, stk, doneIn.ID, 2, map[string]any{"status": "done"})

	setAgo(130) // last real activity of Silent Worker
	patchTask(t, stk, stalled.ID, 1, map[string]any{"status": "doing"})

	// Beating Heart: claimed N−70m (ttl 3600s → would lapse at N−10m)…
	setAgo(70)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+beating.ID+"/claim", map[string]any{
		"actor": actor("agent-h"), "ttl_sec": 3600,
	})
	if code != http.StatusOK {
		t.Fatalf("claim beating = %d (body %s)", code, string(body))
	}
	// …and kept alive by a heartbeat at N−50m. The heartbeat re-stamps the expiry to
	// now+storedTTL (= N+10m) but emits NO event and bumps NO row — so the lease is
	// LIVE at N while "last activity" is still the claim at N−70m.
	setAgo(50)
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+beating.ID+"/heartbeat", map[string]any{"actor": actor("agent-h")})
	if code != http.StatusOK {
		t.Fatalf("heartbeat beating = %d (body %s)", code, string(body))
	}

	setAgo(30) // Live Work: claimed N−30m with a 1h TTL → live until N+30m
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+live.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "ttl_sec": 3600,
	})
	if code != http.StatusOK {
		t.Fatalf("claim live = %d (body %s)", code, string(body))
	}

	// --- default digest at N -----------------------------------------------------------
	fc.now = N
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report", nil)
	if code != http.StatusOK {
		t.Fatalf("GET report = %d, want 200 (body %s)", code, string(body))
	}
	rep := mustDecode[projectReportDTO](t, body)

	if rep.ProjectID != p.ID {
		t.Errorf("project_id = %q, want %q", rep.ProjectID, p.ID)
	}
	if len(rep.Counts.ByStatus) != 3 || rep.Counts.ByStatus["todo"] != 2 ||
		rep.Counts.ByStatus["doing"] != 4 || rep.Counts.ByStatus["done"] != 2 {
		t.Errorf("counts.by_status = %v, want exactly {todo:2 doing:4 done:2}", rep.Counts.ByStatus)
	}
	if len(rep.Counts.ByPriority) != 6 { // integer priorities render as string keys "0".."5"
		t.Errorf("counts.by_priority has %d keys, want 6 (%v)", len(rep.Counts.ByPriority), rep.Counts.ByPriority)
	}
	for k, want := range map[string]int{"0": 4, "1": 1, "2": 1, "3": 1, "4": 1, "5": 0} { // four default-priority tasks (Old Done, Zombie, Dependent, Fresh Done)
		if got := rep.Counts.ByPriority[k]; got != want {
			t.Errorf("counts.by_priority[%q] = %d, want %d", k, got, want)
		}
	}

	// Active claims: most-urgent (soonest expiry) first — Beating (N+10m) before Live (N+30m).
	if len(rep.ActiveClaims) != 2 {
		t.Fatalf("active_claims = %v, want exactly the two live leases", rep.ActiveClaims)
	}
	wantClaims := []struct {
		id       string
		holder   string
		expires  time.Time
		remaining int64
	}{
		{beating.ID, "agent-h", N.Add(10 * time.Minute), 600},  // heartbeat re-stamped it to now+storedTTL
		{live.ID, "agent-a", N.Add(30 * time.Minute), 1800},
	}
	for i, want := range wantClaims {
		got := rep.ActiveClaims[i]
		if got.ID != want.id || got.ClaimedBy == nil || got.ClaimedBy.Name != want.holder {
			t.Errorf("active_claims[%d] = %+v, want %s held by %s", i, got, want.id, want.holder)
		}
		exp, perr := time.Parse(time.RFC3339, got.ClaimExpiresAt)
		if perr != nil || !exp.Equal(want.expires) {
			t.Errorf("active_claims[%d].claim_expires_at = %q, want %s", i, got.ClaimExpiresAt, want.expires.Format(time.RFC3339))
		}
		if got.RemainingSec != want.remaining {
			t.Errorf("active_claims[%d].remaining_sec = %d, want %d", i, got.RemainingSec, want.remaining)
		}
	}

	if len(rep.ExpiredClaims) != 1 || rep.ExpiredClaims[0].ID != zombie.ID {
		t.Errorf("expired_claims = %v, want exactly [zombie]", rep.ExpiredClaims)
	}

	// Stalled: most idle first. Zombie (claimed at base → 1800m), Silent Worker
	// (status event N−130m — the SQLite MaxAtByTasks must pick it over the older
	// created event), Beating (claim N−70m — the N−50m HEARTBEAT must NOT count).
	wantStalled := []struct {
		id        string
		lastAt    time.Time
		idleMin   int
	}{
		{zombie.ID, base, 1800},
		{stalled.ID, N.Add(-130 * time.Minute), 130},
		{beating.ID, N.Add(-70 * time.Minute), 70},
	}
	if len(rep.StalledTasks) != len(wantStalled) {
		t.Fatalf("stalled_tasks = %v, want %d rows", rep.StalledTasks, len(wantStalled))
	}
	for i, want := range wantStalled {
		got := rep.StalledTasks[i]
		if got.ID != want.id {
			t.Errorf("stalled_tasks[%d].id = %s, want %s", i, got.ID, want.id)
		}
		last, perr := time.Parse(time.RFC3339, got.LastActivityAt)
		if perr != nil || !last.Equal(want.lastAt) {
			t.Errorf("stalled_tasks[%d].last_activity_at = %q, want %s", i, got.LastActivityAt, want.lastAt.Format(time.RFC3339))
		}
		if got.IdleMinutes != want.idleMin {
			t.Errorf("stalled_tasks[%d].idle_minutes = %d, want %d", i, got.IdleMinutes, want.idleMin)
		}
	}

	if len(rep.Bottlenecks) != 1 || rep.Bottlenecks[0].ID != blocker.ID || rep.Bottlenecks[0].Blocks != 1 {
		t.Errorf("bottlenecks = %v, want [Foundation blocks=1]", rep.Bottlenecks)
	}
	if len(rep.OpenBlockedBy) != 1 || rep.OpenBlockedBy[0].Blocker.ID != blocker.ID ||
		len(rep.OpenBlockedBy[0].Blocked) != 1 || rep.OpenBlockedBy[0].Blocked[0].ID != dep.ID {
		t.Errorf("open_blocked_by = %v, want [Foundation → Dependent]", rep.OpenBlockedBy)
	}

	if len(rep.RecentlyCompleted) != 1 || rep.RecentlyCompleted[0].ID != doneIn.ID {
		t.Errorf("recently_completed (default window) = %v, want exactly [Fresh Done]", rep.RecentlyCompleted)
	} else if ca, perr := time.Parse(time.RFC3339, rep.RecentlyCompleted[0].CompletedAt); perr != nil || !ca.Equal(N.Add(-299*time.Minute)) {
		t.Errorf("recently_completed[0].completed_at = %q, want %s", rep.RecentlyCompleted[0].CompletedAt, N.Add(-299*time.Minute).Format(time.RFC3339))
	}

	wantWorkload := map[string]int{"": 4, "agent-a": 1, "agent-h": 1} // unassigned active tasks bucket under ""
	if len(rep.Workload) != len(wantWorkload) {
		t.Errorf("workload = %v, want %d keys", rep.Workload, len(wantWorkload))
	}
	for name, want := range wantWorkload {
		if got := rep.Workload[name]; got != want {
			t.Errorf("workload[%q] = %d, want %d", name, got, want)
		}
	}

	// --- explicit tuning parameters ----------------------------------------------------
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report?since="+reportBase+"&stalled_minutes=2000", nil)
	if code != http.StatusOK {
		t.Fatalf("GET report with since/stalled_minutes = %d (body %s)", code, string(body))
	}
	rep2 := mustDecode[projectReportDTO](t, body)
	// since = base admits BOTH done tasks, newest first.
	if len(rep2.RecentlyCompleted) != 2 || rep2.RecentlyCompleted[0].ID != doneIn.ID || rep2.RecentlyCompleted[1].ID != doneOut.ID {
		t.Errorf("recently_completed (since=base) = %v, want [Fresh Done, Old Done] newest first", rep2.RecentlyCompleted)
	}
	// stalled_minutes=2000 > every idle time (max 1800) → empty section.
	if len(rep2.StalledTasks) != 0 {
		t.Errorf("stalled_tasks (window 2000m) = %v, want empty", rep2.StalledTasks)
	}

	// --- error envelopes ------------------------------------------------------------------
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/0734567890abcdefghij00000000/report", nil)
	if code != http.StatusNotFound {
		t.Fatalf("report for unknown project = %d, want 404 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "not found" || e.Resource != "project" {
		t.Errorf("404 body = %+v, want error \"not found\" resource \"project\"", e)
	}
	for _, q := range []string{"?since=not-a-date", "?stalled_minutes=-5", "?stalled_minutes=abc"} {
		code, b := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/report"+q, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("GET report%s = %d, want 400 (body %s)", q, code, string(b))
		}
		e = mustDecode[errResp](t, b)
		if e.Error != "invalid input" {
			t.Errorf("GET report%s error = %+v, want \"invalid input\"", q, e)
		}
	}
}
