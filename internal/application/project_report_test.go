package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// project_report_test.go: unit tests for the single-call project health digest
// (Projects.Report) over fakeUoW + fakes + FakeClock — no SQLite, no HTTP.
// The full REST surface (param parsing, 404/400 envelopes, JSON shape) is
// covered in internal/interface/http/project_report_test.go.

const reportProjectID = domain.ID("proj-report")

// newReportEnv seeds one project with a fixture of tasks covering every digest
// section and returns the Projects use case on a controllable clock frozen at
// "now" (testStart). The fixture (now = 2026-09-25T18:00:00Z):
//
//   - todo "Todo A"            unassigned, p0                      → workload ""
//   - doing "Live Work"        agent-a, claim taken now-30m ttl 3600s → live until now+30m; event@now-30m
//   - doing "Row Stalled"      unassigned, no events, updated_at now-120m → stalled via ROW fallback
//   - doing "Boundary OK"      agent-a, event@now-60m (exactly the default window) → NOT stalled (strict >)
//   - doing "Event Pref"       agent-a, event@now-90m but updated_at now-10m → stalled with event ts (precedence)
//   - doing "Zombie Claim"     agent-b, lapsed lease (expired 1h59m ago)    → expired_claims AND stalled (idle 120m: a lapsed lease does not exempt)
//   - done  "Done In Window"   agent-a, completed now-5h           → recently_completed (default since = now−24h)
//   - done  "Done Old"         agent-b, completed now-30h          → outside the default window
//   - todo "Blocked One"/"Two" blocked-by doing "The Blocker"      → bottleneck blocks 2
func newReportEnv(t *testing.T) (*Projects, *fakeClock, *fakeUoW) {
	t.Helper()
	fc := newFakeClock(mustTime(t, testStart))
	now := fc.now
	fp := newFakeProjects()
	fp.byID[reportProjectID] = domain.Project{ID: reportProjectID, Name: "Report", CreatedAt: now, UpdatedAt: now}
	uow := newFakeUoW(fp, &fakeEvents{})

	a := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	b := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	seed := func(id string, title string, st domain.Status, pr domain.Priority, assignee *domain.Actor, updated time.Time) {
		uow.tasks.byID[domain.ID(id)] = domain.Task{
			ID: domain.ID(id), Title: title, Description: "", ProjectID: reportProjectID,
			Status: st, Priority: pr, Version: 1, Assignee: assignee,
			CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
		}
	}
	with := func(id string, mutate func(*domain.Task)) {
		tk := uow.tasks.byID[domain.ID(id)]
		mutate(&tk)
		uow.tasks.byID[domain.ID(id)] = tk
	}
	seed("t-todo", "Todo A", domain.StatusTodo, 0, nil, now.Add(-time.Minute))
	seed("t-live", "Live Work", domain.StatusDoing, 3, &a, now.Add(-30*time.Minute))
	with("t-live", func(tk *domain.Task) { tk.Claim = domain.NewClaim(a, now.Add(-30*time.Minute), 3600) }) // taken 30m ago, 1h TTL → expires now+30m
	seed("t-stalled-row", "Row Stalled", domain.StatusDoing, 1, nil, now.Add(-120*time.Minute))
	seed("t-boundary", "Boundary OK", domain.StatusDoing, 2, &a, now.Add(-75*time.Minute))
	seed("t-evtpref", "Event Pref", domain.StatusDoing, 4, &a, now.Add(-10*time.Minute)) // row newer than its only event
	seed("t-zombie", "Zombie Claim", domain.StatusDoing, 2, &b, now.Add(-2*time.Hour))
	with("t-zombie", func(tk *domain.Task) { tk.Claim = domain.NewClaim(b, now.Add(-2*time.Hour), 60) }) // expired an hour and fifty-nine ago
	seed("t-done-in", "Done In Window", domain.StatusDone, 0, &a, now.Add(-5*time.Hour))
	with("t-done-in", func(tk *domain.Task) { tk.CompletedAt = ptrTime(now.Add(-5 * time.Hour)) })
	seed("t-done-out", "Done Old", domain.StatusDone, 2, &b, now.Add(-30*time.Hour))
	with("t-done-out", func(tk *domain.Task) { tk.CompletedAt = ptrTime(now.Add(-30 * time.Hour)) })
	seed("t-blocked1", "Blocked One", domain.StatusTodo, 5, nil, now.Add(-time.Minute))
	seed("t-blocked2", "Blocked Two", domain.StatusTodo, 5, &b, now.Add(-time.Minute))
	seed("t-blocker", "The Blocker", domain.StatusDoing, 0, &a, now.Add(-time.Minute))

	evAt := func(id string, at time.Time) {
		pid := reportProjectID
		tid := domain.ID(id)
		uow.events.events = append(uow.events.events, domain.Event{
			ID: domain.ID("ev-" + id), ProjectID: &pid, TaskID: &tid,
			Actor: a, Kind: domain.EventCommented, At: at,
		})
	}
	evAt("t-live", now.Add(-30*time.Minute))     // claimed event (same instant as the lease)
	evAt("t-boundary", now.Add(-60*time.Minute)) // exactly on the default stalled threshold
	evAt("t-evtpref", now.Add(-90*time.Minute))  // OLDER than the row's updated_at — precedence still applies

	// blocked-by edges: t-blocked1/t-blocked2 are blocked by the open doing task t-blocker.
	for _, dep := range []string{"t-blocked1", "t-blocked2"} {
		if err := uow.relations.Add(context.Background(), domain.Relation{
			ID: domain.ID("rel-" + dep), TaskID: domain.ID(dep), OtherTaskID: "t-blocker",
			Kind: domain.RelationBlockedBy, CreatedAt: now.Add(-time.Minute),
		}); err != nil {
			t.Fatalf("seed relation %s: %v", dep, err)
		}
	}

	return NewProjects(uow, fc, &fakeIDGen{}), fc, uow
}

func ptrTime(tm time.Time) *time.Time { return &tm }

// TestReport_FullDigest is the reference run on the fixture with DEFAULT options:
// it pins every digest section, both fallbacks/precedences of "last activity",
// the strict stalled boundary, and every documented ordering.
func TestReport_FullDigest(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newReportEnv(t)

	rep, err := svc.Report(ctx, reportProjectID, ReportOptions{}) // zero value = both defaults (60 min / now−24h)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	now := fc.now

	if rep.ProjectID != reportProjectID {
		t.Errorf("ProjectID = %s, want %s", rep.ProjectID, reportProjectID)
	}

	wantStatus := map[domain.Status]int{domain.StatusTodo: 3, domain.StatusDoing: 6, domain.StatusDone: 2}
	for st, want := range wantStatus {
		if got := rep.Counts.ByStatus[st]; got != want {
			t.Errorf("counts.by_status[%s] = %d, want %d", st, got, want)
		}
	}
	if len(rep.Counts.ByStatus) != 3 {
		t.Errorf("counts.by_status has %d keys, want exactly 3 (%v)", len(rep.Counts.ByStatus), rep.Counts.ByStatus)
	}
	wantPriority := map[int]int{0: 3, 1: 1, 2: 3, 3: 1, 4: 1, 5: 2}
	for p, want := range wantPriority {
		if got := rep.Counts.ByPriority[p]; got != want {
			t.Errorf("counts.by_priority[%d] = %d, want %d", p, got, want)
		}
	}
	if len(rep.Counts.ByPriority) != 6 {
		t.Errorf("counts.by_priority has %d keys, want exactly 6 (%v)", len(rep.Counts.ByPriority), rep.Counts.ByPriority)
	}

	if len(rep.ActiveClaims) != 1 {
		t.Fatalf("active_claims = %v, want exactly the live lease", rep.ActiveClaims)
	}
	live := rep.ActiveClaims[0]
	if live.ID != "t-live" || live.Title != "Live Work" {
		t.Errorf("active_claims[0] = %+v, want t-live/Live Work", live)
	}
	if live.ClaimedBy == nil || live.ClaimedBy.Name != "agent-a" {
		t.Errorf("active_claims[0].claimed_by = %+v, want agent-a", live.ClaimedBy)
	}
	if !live.ClaimExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("claim_expires_at = %s, want %s", live.ClaimExpiresAt, now.Add(30*time.Minute))
	}
	if live.RemainingSec != 1800 {
		t.Errorf("remaining_sec = %d, want 1800", live.RemainingSec)
	}

	if len(rep.ExpiredClaims) != 1 || rep.ExpiredClaims[0].ID != "t-zombie" {
		t.Errorf("expired_claims = %v, want [t-zombie]", rep.ExpiredClaims)
	}

	// Stalled: most idle first (oldest activity; id breaks same-instant ties).
	// t-stalled-row and t-zombie tie at 120m idle — id ASC puts t-stalled-row first.
	// t-evtpref (90m) is last. t-boundary's exactly-60m idle must NOT appear (strict > threshold).
	wantStalled := []struct {
		id      domain.ID
		lastAt  time.Time
		idleMin int
	}{
		{"t-stalled-row", now.Add(-120 * time.Minute), 120}, // ROW fallback: no events at all
		{"t-zombie", now.Add(-2 * time.Hour), 120},          // lapsed lease does not exempt a doing task from stalled
		{"t-evtpref", now.Add(-90 * time.Minute), 90},       // EVENT precedence: event older than updated_at still wins
	}
	if len(rep.StalledTasks) != len(wantStalled) {
		t.Fatalf("stalled_tasks = %v, want %d rows (%+v)", rep.StalledTasks, len(wantStalled), wantStalled)
	}
	for i, want := range wantStalled {
		got := rep.StalledTasks[i]
		if got.ID != want.id {
			t.Errorf("stalled_tasks[%d].id = %s, want %s", i, got.ID, want.id)
		}
		if !got.LastActivityAt.Equal(want.lastAt) {
			t.Errorf("stalled_tasks[%d].last_activity_at = %s, want %s", i, got.LastActivityAt, want.lastAt)
		}
		if got.IdleMinutes != want.idleMin {
			t.Errorf("stalled_tasks[%d].idle_minutes = %d, want %d", i, got.IdleMinutes, want.idleMin)
		}
	}

	// Bottleneck: the open doing task blocks two open todos; inverse group lists both.
	if len(rep.Bottlenecks) != 1 || rep.Bottlenecks[0].ID != "t-blocker" || rep.Bottlenecks[0].Blocks != 2 {
		t.Fatalf("bottlenecks = %v, want [t-blocker blocks=2]", rep.Bottlenecks)
	}
	if len(rep.OpenBlockedBy) != 1 {
		t.Fatalf("open_blocked_by = %v, want one group", rep.OpenBlockedBy)
	}
	group := rep.OpenBlockedBy[0]
	if group.Blocker.ID != "t-blocker" {
		t.Errorf("open_blocked_by[0].blocker = %+v, want t-blocker", group.Blocker)
	}
	if len(group.Blocked) != 2 || group.Blocked[0].ID != "t-blocked1" || group.Blocked[1].ID != "t-blocked2" {
		t.Errorf("open_blocked_by[0].blocked = %v, want [t-blocked1 t-blocked2] (title ASC)", group.Blocked)
	}

	// Recently completed: inside the default window (now−5h), newest first; the now−30h task is out.
	if len(rep.RecentlyCompleted) != 1 || rep.RecentlyCompleted[0].ID != "t-done-in" {
		t.Fatalf("recently_completed = %v, want [t-done-in]", rep.RecentlyCompleted)
	}
	rc := rep.RecentlyCompleted[0]
	if !rc.CompletedAt.Equal(now.Add(-5*time.Hour)) || rc.Assignee == nil || rc.Assignee.Name != "agent-a" {
		t.Errorf("recently_completed[0] = %+v, want completed_at now-5h assignee agent-a", rc)
	}

	// Workload: active (todo+doing) only, unassigned under "".
	wantWorkload := map[string]int{"": 3, "agent-a": 4, "agent-b": 2}
	for name, want := range wantWorkload {
		if got := rep.Workload[name]; got != want {
			t.Errorf("workload[%q] = %d, want %d", name, got, want)
		}
	}
	if len(rep.Workload) != len(wantWorkload) {
		t.Errorf("workload has %d keys, want %d (%v)", len(rep.Workload), len(wantWorkload), rep.Workload)
	}
}

// TestReport_CustomOptions pins that a wider stalled window empties the section
// and that an explicit since bound admits tasks outside the default 24h window.
func TestReport_CustomOptions(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newReportEnv(t)

	since := ptrTime(fc.now.Add(-40 * time.Hour))
	rep, err := svc.Report(ctx, reportProjectID, ReportOptions{StalledMinutes: 180, Since: since})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	if len(rep.StalledTasks) != 0 {
		t.Errorf("stalled_tasks with a 180m window = %v, want empty (fixture max idle is 120m)", rep.StalledTasks)
	}
	// now−5h AND now−30h are both inside the explicit now−40h window, newest first.
	if len(rep.RecentlyCompleted) != 2 || rep.RecentlyCompleted[0].ID != "t-done-in" || rep.RecentlyCompleted[1].ID != "t-done-out" {
		t.Errorf("recently_completed = %v, want [t-done-in t-done-out] newest first", rep.RecentlyCompleted)
	}
}

// TestReport_DefaultsResolvedAgainstClock pins the zero-option defaults (60 min;
// now−24h against the application clock, not wall time): a task completed exactly
// 24h ago is still IN the window (>= since), one minute older is out.
func TestReport_DefaultsResolvedAgainstClock(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newReportEnv(t)

	inEdge := ptrTime(svc.clock.Now().Add(-24 * time.Hour)) // exactly on the default boundary → included (>=)
	outEdge := ptrTime(svc.clock.Now().Add(-24*time.Hour - time.Minute))
	seedDone := func(id string, at *time.Time) {
		uow.tasks.byID[domain.ID(id)] = domain.Task{
			ID: domain.ID(id), Title: id, ProjectID: reportProjectID, Status: domain.StatusDone,
			Version: 1, CreatedAt: svc.clock.Now(), UpdatedAt: svc.clock.Now(), CompletedAt: at,
		}
	}
	seedDone("t-edge-in", inEdge)
	seedDone("t-edge-out", outEdge)

	rep, err := svc.Report(ctx, reportProjectID, ReportOptions{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	// In-window, newest first: t-done-in (now-5h) then t-edge-in (exactly now-24h, inclusive).
	// Excluded: t-done-out (now-30h), t-edge-out (one minute past the boundary).
	wantIDs := []domain.ID{"t-done-in", "t-edge-in"}
	if len(rep.RecentlyCompleted) != len(wantIDs) {
		t.Fatalf("recently_completed = %v, want %d rows (%v)", rep.RecentlyCompleted, len(wantIDs), wantIDs)
	}
	for i, id := range wantIDs {
		if rep.RecentlyCompleted[i].ID != id {
			t.Errorf("recently_completed[%d] = %s, want %s", i, rep.RecentlyCompleted[i].ID, id)
		}
	}
}

// TestReport_UnknownProject pins the 404 contract at the use-case level:
// domain.ErrNotFound before any task/relation/event work happens.
func TestReport_UnknownProject(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newReportEnv(t)

	_, err := svc.Report(ctx, "no-such-project", ReportOptions{})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Report(unknown project) = %v, want domain.ErrNotFound", err)
	}
}

// TestReport_HeartbeatsNeverCount pins the documented blind spot: a doing task
// whose lease is kept alive ONLY by heartbeats (no event, no row bump) still
// counts as stalled once its last REAL activity passes the window — while an
// actively-claimed task instead shows up in active_claims.
func TestReport_HeartbeatsNeverCount(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newReportEnv(t)

	now := mustTime(t, testStart)
	// Remove the fixture's doing tasks so this is a clean two-task probe: one
	// claimed-and-heartbeated (appears in active_claims), one silently idle for 3h.
	for id := range uow.tasks.byID {
		delete(uow.tasks.byID, id)
	}
	keeper := domain.Actor{Type: domain.ActorAgent, Name: "keeper"}
	uow.tasks.byID["t-keeper"] = domain.Task{
		ID: "t-keeper", Title: "Keeper", ProjectID: reportProjectID, Status: domain.StatusDoing,
		Version: 1, CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour), // last REAL activity 4h ago
		Claim: domain.NewClaim(keeper, now.Add(-2*time.Hour), 14400), // lease taken now-2h, 4h TTL → live until now+2h (heartbeats kept it alive)
	}
	uow.tasks.byID["t-silent"] = domain.Task{
		ID: "t-silent", Title: "Silent", ProjectID: reportProjectID, Status: domain.StatusDoing,
		Version: 1, CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour), // last real activity 3h ago
	}

	rep, err := svc.Report(ctx, reportProjectID, ReportOptions{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(rep.ActiveClaims) != 1 || rep.ActiveClaims[0].ID != "t-keeper" || rep.ActiveClaims[0].RemainingSec != 7200 {
		t.Errorf("active_claims = %v, want [t-keeper live 7200s] (the lease is alive even though only heartbeats touched it)", rep.ActiveClaims)
	}
	// The documented blind spot: a heartbeat-only task still counts as stalled
	// because heartbeats emit no event and bump no row — both rows appear, most
	// idle first. t-keeper (4h) before t-silent (3h).
	if len(rep.StalledTasks) != 2 || rep.StalledTasks[0].ID != "t-keeper" || rep.StalledTasks[0].IdleMinutes != 240 ||
		rep.StalledTasks[1].ID != "t-silent" || rep.StalledTasks[1].IdleMinutes != 180 {
		t.Errorf("stalled_tasks = %v, want [t-keeper idle 240, t-silent idle 180] (heartbeats never count as activity)", rep.StalledTasks)
	}
	if len(rep.ExpiredClaims) != 0 {
		t.Errorf("expired_claims = %v, want empty", rep.ExpiredClaims)
	}
}
