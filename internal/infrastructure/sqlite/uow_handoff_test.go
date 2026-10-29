package sqlite

// Integration tests over a real temp-file SQLite database for the atomic
// lease-conditional handoff (application.Tasks.HandOff → taskRepo.Handoff),
// mirroring uow_claim_test.go. It proves the real CAS SQL encodes exactly what
// the application-layer fakes assume: ONE transaction writes notes/assignee + one
// comment + the lease clear/todo transition with EXACTLY ONE version bump; a
// foreign active lease and an absent/expired lease both touch 0 rows, classify,
// and mutate nothing. The injectable clock drives expiry — no sleeps.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// handoffCommentCount returns the number of comment rows for a task.
func handoffCommentCount(t *testing.T, db *sql.DB, ctx context.Context, taskID domain.ID) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM comment WHERE task_id = ?`, string(taskID)).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n
}

// eventKindCounts returns per-kind event counts for a task straight from the
// event table (the durable ledger that must agree with the row).
func eventKindCounts(t *testing.T, db *sql.DB, ctx context.Context, taskID domain.ID) map[string]int {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM event WHERE task_id = ? GROUP BY kind`, string(taskID))
	if err != nil {
		t.Fatalf("event kinds: %v", err)
	}
	defer rows.Close()
	m := make(map[string]int)
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatalf("scan event kind: %v", err)
		}
		m[k]++
	}
	return m
}

func TestHandOffRealSQL_AtomicCommit_SingleBumpAndEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "handoff-holder-a"}
	humanRecv := domain.Actor{Type: domain.ActorHuman, Name: "human-receiver"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Real-SQL handoff", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := tasks.Claim(ctx, agentA, task.ID, 900, false) // v2 doing, lease held by A
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}

	tc.now = base.Add(30 * time.Second) // still inside the 900s window — no sleep needed
	res, err := tasks.HandOff(ctx, agentA, task.ID, application.HandOffInput{
		Kind:     domain.CommentHandoff,
		Body:     "Passing to human-receiver over real SQL.",
		Notes:    "Real-SQL resume blob for the next run",
		Assignee: &humanRecv,
	})
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}

	// Row effects: doing→todo, lease cleared, assignee = human-receiver, notes
	// replaced, and EXACTLY ONE version bump (claim left v2 → handoff must be v3).
	got := res.Task
	if got.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", got.Status)
	}
	if got.Version != held.Version+1 || got.Version != 3 {
		t.Errorf("version = %d (held %d), want exactly one bump to 3", got.Version, held.Version)
	}
	if got.Claim.Present() {
		t.Errorf("lease not cleared: holder=%+v expiry=%v", got.Claim.By(), got.Claim.Expiry())
	}
	if got.Assignee == nil || got.Assignee.Type != domain.ActorHuman || got.Assignee.Name != humanRecv.Name {
		t.Errorf("assignee = %+v, want human human-receiver", got.Assignee)
	}
	if got.Notes != "Real-SQL resume blob for the next run" {
		t.Errorf("notes = %q, want the replaced blob", got.Notes)
	}

	// Exactly ONE comment, kind handoff, body forwarded verbatim, actor = A.
	if n := handoffCommentCount(t, db, ctx, task.ID); n != 1 {
		t.Fatalf("comments = %d, want exactly 1", n)
	}
	var cKind, cBody, cType, cName string
	if err := db.QueryRowContext(ctx,
		`SELECT kind, body, actor_type, actor_name FROM comment WHERE task_id = ? ORDER BY rowid DESC LIMIT 1`,
		string(task.ID)).Scan(&cKind, &cBody, &cType, &cName); err != nil {
		t.Fatalf("read comment: %v", err)
	}
	if cKind != string(domain.CommentHandoff) || cBody != "Passing to human-receiver over real SQL." ||
		cType != string(domain.ActorAgent) || cName != agentA.Name {
		t.Errorf("comment = (kind %q body %q actor %s/%s)", cKind, cBody, cType, cName)
	}

	// The durable event ledger agrees: created + claimed (from setup) plus exactly
	// one each of noted/assigned/commented/released from the handoff.
	kinds := eventKindCounts(t, db, ctx, task.ID)
	for _, k := range []string{"created", "claimed", "noted", "assigned", "commented", "released"} {
		if kinds[k] != 1 {
			t.Errorf("event kind %q count = %d, want exactly 1 (ledger: %v)", k, kinds[k], kinds)
		}
	}
	if len(kinds) != 6 {
		t.Errorf("unexpected extra event kinds: %v", kinds)
	}
}

func TestHandOffRealSQL_ForeignHolderConflict_NoMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "handoff-holder-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "rival-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Foreign conflict", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := tasks.Claim(ctx, agentA, task.ID, 900, false) // v2 doing, held by A (live)
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// A NON-holder's handoff must surface a *ClaimConflictError naming the live
	// holder AND mutate nothing (0-row CAS).
	_, err = tasks.HandOff(ctx, agentB, task.ID, application.HandOffInput{Assignee: &agentB})
	var cc *domain.ClaimConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("handoff by non-holder err = %v (%T), want *domain.ClaimConflictError", err, err)
	}
	if cc.Holder.Name != agentA.Name {
		t.Errorf("conflict holder = %+v, want %s", cc.Holder, agentA.Name)
	}

	stale, gerr := tasks.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if stale.Version != held.Version || stale.Status != domain.StatusDoing ||
		stale.Claim.By() == nil || stale.Claim.By().Name != agentA.Name {
		t.Errorf("failed handoff mutated the row: (v%d %q holder %+v), want unchanged (v2 doing agent-a)",
			stale.Version, stale.Status, stale.Claim.By())
	}
	if n := handoffCommentCount(t, db, ctx, task.ID); n != 0 {
		t.Errorf("comment written on conflict: %d", n)
	}
	// Event ledger unchanged by the failed attempt: only created + claimed.
	kinds := eventKindCounts(t, db, ctx, task.ID)
	if kinds["created"] != 1 || kinds["claimed"] != 1 || len(kinds) != 2 {
		t.Errorf("failed handoff appended events: %v", kinds)
	}
}

func TestHandOffRealSQL_NotClaimed_ErrNotClaimed_NoMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tc := &testClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "handoff-holder-a"}
	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Never claimed", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A task with NO lease at all → NOT a conflict but a specific ErrNotClaimed,
	// and nothing is mutated (0-row CAS).
	_, err = tasks.HandOff(ctx, agentA, task.ID, application.HandOffInput{})
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("handoff on unclaimed task err = %v (%T), want wrapped domain.ErrNotClaimed", err, err)
	}
	var cc *domain.ClaimConflictError
	if errors.As(err, &cc) {
		t.Fatalf("unclaimed handoff must NOT be a ClaimConflictError: %+v", cc)
	}

	stored, gerr := tasks.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if stored.Version != 1 || stored.Claim.Present() || stored.Status != domain.StatusTodo {
		t.Errorf("failed handoff mutated the row: (v%d holder=%v status=%s), want (v1 unclaimed todo)",
			stored.Version, stored.Claim.By(), stored.Status)
	}
	if n := handoffCommentCount(t, db, ctx, task.ID); n != 0 {
		t.Errorf("comment written on not-claimed: %d", n)
	}
	kinds := eventKindCounts(t, db, ctx, task.ID)
	if kinds["created"] != 1 || len(kinds) != 1 {
		t.Errorf("not-claimed handoff appended events: %v (want only created)", kinds)
	}
}

func TestHandOffRealSQL_ExpiredLease_ErrNotClaimed_NoMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "handoff-holder-a"}
	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Expired handoff", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := tasks.Claim(ctx, agentA, task.ID, 30, false) // expires base+30s
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// Advance PAST the expiry (no sleep). The CAS guard `claim_expires_at >= now`
	// no longer matches → 0 rows → ErrNotClaimed, with nothing mutated.
	tc.now = base.Add(45 * time.Second)
	_, err = tasks.HandOff(ctx, agentA, task.ID, application.HandOffInput{})
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("handoff after expiry err = %v (%T), want wrapped domain.ErrNotClaimed", err, err)
	}

	stored, gerr := tasks.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if stored.Version != held.Version || !stored.Claim.Present() || stored.Claim.By().Name != agentA.Name {
		t.Errorf("failed handoff mutated the row: (v%d holder=%+v), want unchanged (v%d dead-lease agent-a)",
			stored.Version, stored.Claim.By(), held.Version)
	}
	if n := handoffCommentCount(t, db, ctx, task.ID); n != 0 {
		t.Errorf("comment written on expired lease: %d", n)
	}
}
