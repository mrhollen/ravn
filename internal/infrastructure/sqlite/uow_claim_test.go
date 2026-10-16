package sqlite

// Integration tests over a real temp-file SQLite database for slice 2C: the
// atomic TTL-lease lifecycle (Claim / Release / Heartbeat) through the real
// SQLiteUnitOfWork + taskRepo. The centerpiece is the MANDATORY race test: N
// distinct agents concurrently claiming one task → EXACTLY ONE winner (the core
// duplicate-work-prevention assertion). The lazy-expiry, heartbeat, and release
// tests prove the real CAS SQL encodes exactly what the application-layer fakes
// assume — including the no-sweeper decision (expired leases stay doing on disk
// until a takeover claim heals them).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
)

// testClock is a deterministic Clock for the lazy-expiry / heartbeat tests
// (single-goroutine use only — never share it across goroutines).
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func newClaimUseCase(t *testing.T, db *sql.DB, tc application.Clock) (*application.Tasks, *application.Projects) {
	t.Helper()
	uow := NewUnitOfWork(db)
	return application.NewTasks(uow, tc, idgen.New()),
		application.NewProjects(uow, clock.New(), idgen.New())
}

func isClaimConflict(err error) bool {
	var cc *domain.ClaimConflictError
	return errors.As(err, &cc)
}

// eventCounts returns (claimedEvents, totalEvents) for a task straight from the
// event table.
func eventCounts(t *testing.T, db *sql.DB, ctx context.Context, taskID domain.ID) (int, int) {
	t.Helper()
	var claimed, total int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE task_id = ? AND kind = 'claimed'`, string(taskID)).Scan(&claimed); err != nil {
		t.Fatalf("count claimed events: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE task_id = ?`, string(taskID)).Scan(&total); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return claimed, total
}

// --- the MANDATORY race test ---------------------------------------------------

func TestClaimRacingAgents_ExactlyOneWinnerPerTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		rounds  = 4 // the whole race is repeated on independent tasks/DBs; each round must show exactly one winner
		workers = 16
	)
	totalWinners := 0

	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			db := openTestDB(t)
			tasks, projects := newClaimUseCase(t, db, clock.New()) // real clock — the race is about ordering, not time
			p := mustCreateProject(t, ctx, projects)

			task, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "Race target", ProjectID: p.ID})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			var (
				wg      sync.WaitGroup
				results = make([]error, workers)         // one slot per goroutine — no shared mutation
				winners = make([]*domain.Actor, workers) // per-slot identity; non-nil only for the winner
			)
			for i := 0; i < workers; i++ {
				i := i
				wg.Add(1)
				go func() {
					defer wg.Done()
					actor := domain.Actor{Type: domain.ActorAgent, Name: fmt.Sprintf("racer-%d", i)} // DISTINCT actor names per racer
					got, cerr := tasks.Claim(ctx, actor, task.ID, 600, false)
					results[i] = cerr
					if cerr == nil {
						winners[i] = got.Claim.By()
					}
				}()
			}
			wg.Wait()

			winCount, conflictCount := 0, 0
			for i, err := range results {
				switch {
				case err == nil:
					winCount++
					if winners[i] == nil || winners[i].Name != fmt.Sprintf("racer-%d", i) {
						t.Errorf("round %d: winner identity mismatch at slot %d: %+v", round, i, winners[i])
					}
				case isClaimConflict(err):
					conflictCount++
				default:
					t.Errorf("round %d racer %d: unexpected error %v (want nil or *domain.ClaimConflictError)", round, i, err)
				}
			}
			if winCount != 1 {
				t.Fatalf("round %d: %d winners out of %d racers — want EXACTLY ONE (two agents would do duplicate work)",
					round, winCount, workers)
			}
			if conflictCount != workers-1 {
				t.Errorf("round %d: %d racers got ClaimConflictError, want %d", round, conflictCount, workers-1)
			}
			totalWinners += winCount

			// After settling: exactly one holder, status doing, ONE version bump (v1→v2).
			got, err := tasks.Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get after race: %v", err)
			}
			if got.Status != domain.StatusDoing || got.Claim.By() == nil {
				t.Fatalf("settled row = (status %q holder %+v), want (doing, exactly one holder)", got.Status, got.Claim.By())
			}
			if got.Version != 2 {
				t.Errorf("settled version = %d, want 2 (exactly one lease write won the CAS)", got.Version)
			}

			// The event ledger agrees with the row: created + exactly ONE claimed.
			claimedEvents, totalEvents := eventCounts(t, db, ctx, task.ID)
			if claimedEvents != 1 || totalEvents != 2 {
				t.Errorf("event ledger = (%d claimed, %d total), want (1, 2) — losers must not emit", claimedEvents, totalEvents)
			}
		})
	}

	if totalWinners != rounds {
		t.Fatalf("racing-claim race: %d winners across %d rounds of %d racers, want exactly %d (one per round)",
			totalWinners, rounds, workers, rounds)
	}
	t.Logf("racing-claim race: exactly 1 winner in each of %d rounds x %d distinct agents (%d total winners) — multi-agent safety holds",
		rounds, workers, totalWinners)
}

// --- lazy expiry (no sweeper) ---------------------------------------------------

func TestClaimLazyExpiry_NoSweeper_TakeoverAfterClockAdvance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Lazy expiry", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := tasks.Claim(ctx, agentA, task.ID, 30, false); err != nil { // expires base+30s
		t.Fatalf("claim A: %v", err)
	}

	// Advance PAST the expiry. MVP has NO background sweeper (ADR 2026-09-26):
	// nothing flips expired→todo — the row stays status=doing with a dead lease
	// on disk ...
	tc.now = base.Add(45 * time.Second)
	stale, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	byStale := stale.Claim.By()
	if stale.Status != domain.StatusDoing || byStale == nil || byStale.Name != agentA.Name {
		t.Fatalf("expired row = (status %q holder %+v), want still doing + holder agent-a on disk (lazy expiry, no sweeper)",
			stale.Status, byStale)
	}
	if !stale.Claim.Present() || !stale.Claim.Expiry().Before(tc.now) {
		t.Fatalf("setup broken: lease should be expired at %s", tc.now)
	}

	// ... but it IS re-claimable: agent B's claim takes over (zombie heal).
	got, err := tasks.Claim(ctx, agentB, task.ID, 30, false)
	if err != nil {
		t.Fatalf("takeover by B after A's lease expired must win: %v", err)
	}
	byGot := got.Claim.By()
	if byGot == nil || byGot.Name != agentB.Name {
		t.Errorf("holder after takeover = %+v, want agent-b", byGot)
	}
	if got.Version != 3 {
		t.Errorf("version after takeover = %d, want 3 (create=1, claim A=2, takeover B=3)", got.Version)
	}

	// The mandatory final assertion: the settled holder is B.
	final, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	byFinal := final.Claim.By()
	if byFinal == nil || byFinal.Name != agentB.Name || final.Status != domain.StatusDoing {
		t.Fatalf("final state = (status %q holder %+v), want (doing agent-b)", final.Status, byFinal)
	}

	// The event ledger: created + claimed(fresh A) + claimed(takeover B).
	claimedEvents, totalEvents := eventCounts(t, db, ctx, task.ID)
	if claimedEvents != 2 || totalEvents != 3 {
		t.Fatalf("event ledger = (%d claimed, %d total), want (2, 3)", claimedEvents, totalEvents)
	}
	var lastKind, lastAfter string
	err = db.QueryRowContext(ctx,
		`SELECT kind, after_json FROM event WHERE task_id = ? AND kind = 'claimed' ORDER BY rowid DESC LIMIT 1`,
		string(task.ID)).Scan(&lastKind, &lastAfter)
	if err != nil {
		t.Fatalf("last claimed event: %v", err)
	}
	if !strings.Contains(lastAfter, `"claim":"takeover"`) || !strings.Contains(lastAfter, "agent-b") {
		t.Errorf("takeover event after_json = %q, want tagged takeover with holder agent-b", lastAfter)
	}
}

// --- heartbeat over real SQL -----------------------------------------------------

func TestHeartbeatRealSQL_ExtendsLeaseWithoutBumpOrEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Keep alive", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := tasks.Claim(ctx, agentA, task.ID, 120, false) // v2, expires base+120s
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	_, eventsBefore := eventCounts(t, db, ctx, task.ID)

	// (a) The ACTIVE holder's heartbeat extends to now + STORED ttl. No version
	// bump, no event, no updated_at change (keep-alive is not a state change).
	tc.now = base.Add(60 * time.Second)
	beaten, err := tasks.Heartbeat(ctx, agentA, task.ID)
	if err != nil {
		t.Fatalf("heartbeat by active holder: %v", err)
	}
	wantExpiry := tc.now.Add(120 * time.Second) // base+180s — extended from the FRESH now with the stored ttl
	if !beaten.Claim.Present() || !beaten.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("expiry after heartbeat = %v, want %s", beaten.Claim.Expiry(), wantExpiry)
	}
	if beaten.Version != 2 || held.Version != 2 {
		t.Errorf("version after heartbeat = %d (held %d), want UNCHANGED 2", beaten.Version, held.Version)
	}
	_, eventsAfter := eventCounts(t, db, ctx, task.ID)
	if eventsAfter != eventsBefore {
		t.Errorf("heartbeat appended an event (%d → %d); keep-alives must be silent over real SQL too",
			eventsBefore, eventsAfter)
	}

	// (b) A non-holder heartbeating gets a conflict naming the live holder.
	if _, err := tasks.Heartbeat(ctx, agentB, task.ID); !isClaimConflict(err) {
		t.Errorf("heartbeat by non-holder err = %v, want *domain.ClaimConflictError", err)
	}

	// (c) After expiry even the holder's heartbeat conflicts (zombie lease).
	tc.now = base.Add(200 * time.Second) // past the extended base+180s expiry
	if _, err := tasks.Heartbeat(ctx, agentA, task.ID); !isClaimConflict(err) {
		t.Errorf("heartbeat after expiry err = %v, want *domain.ClaimConflictError", err)
	}
}

// TestHeartbeatRealSQL_UnclaimedTask_ErrNotClaimed pins the hardening: a
// heartbeat on a task with NO lease at all (over real SQL) is NOT a
// ClaimConflictError — the old zero-holder form rendered as
// "task claimed by \"\" until 0001-01-01T00:00:00Z" — but a specific
// domain.ErrNotClaimed naming the task, with nothing mutated.
func TestHeartbeatRealSQL_UnclaimedTask_ErrNotClaimed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tc := &testClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Never claimed", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = tasks.Heartbeat(ctx, agentA, task.ID)
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("heartbeat on unclaimed task err = %v (%T), want wrapped domain.ErrNotClaimed", err, err)
	}
	var cc *domain.ClaimConflictError
	if errors.As(err, &cc) {
		t.Fatalf("unclaimed heartbeat must not be a ClaimConflictError (even a zero-holder one): %+v", cc)
	}
	if !strings.Contains(err.Error(), string(task.ID)) || !strings.Contains(err.Error(), "claim first") {
		t.Errorf("unclaimed heartbeat message = %q, want it to name the task and say claim first", err.Error())
	}

	// A failed heartbeat must not touch the row (no version bump, no lease).
	stored, gerr := tasks.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if stored.Version != 1 || stored.Claim.Present() || stored.Status != domain.StatusTodo {
		t.Errorf("failed heartbeat mutated the row: v=%d holder=%v status=%s, want v1 unclaimed todo",
			stored.Version, stored.Claim.By(), stored.Status)
	}
}

// --- release over real SQL ---------------------------------------------------------

func TestReleaseRealSQL_NonHolderConflictAndUnclaimedNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)

	agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
	agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

	task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Release me", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	held, err := tasks.Claim(ctx, agentA, task.ID, 600, false) // v2 doing
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// (a) A non-holder's release conflicts with the live holder + expiry and
	// mutates nothing.
	_, eventsBefore := eventCounts(t, db, ctx, task.ID)
	if _, err := tasks.Release(ctx, agentB, task.ID, "not mine"); !isClaimConflict(err) {
		t.Fatalf("release by non-holder err = %v, want *domain.ClaimConflictError", err)
	}
	stale, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	byStale := stale.Claim.By()
	if stale.Version != held.Version || stale.Status != domain.StatusDoing || byStale == nil || byStale.Name != agentA.Name {
		t.Errorf("failed release mutated the row: (v%d %q holder %+v), want unchanged (v2 doing agent-a)",
			stale.Version, stale.Status, byStale)
	}
	if _, n := eventCounts(t, db, ctx, task.ID); n != eventsBefore {
		t.Errorf("failed release appended an event (%d → %d)", eventsBefore, n)
	}

	// (b) The holder's release clears the lease, moves todo, bumps version, and
	// records the note in the `released` event payload.
	tc.now = base.Add(time.Minute) // still inside the 600s window
	released, err := tasks.Release(ctx, agentA, task.ID, "context saved")
	if err != nil {
		t.Fatalf("release by holder: %v", err)
	}
	if released.Status != domain.StatusTodo || released.Version != 3 {
		t.Errorf("released = (status %q v%d), want (todo 3)", released.Status, released.Version)
	}
	if released.Claim.Present() {
		t.Errorf("lease not cleared: holder=%+v expiry=%v ttl=%d",
			released.Claim.By(), released.Claim.Expiry(), released.Claim.TTLSeconds())
	}
	var kind, after string
	err = db.QueryRowContext(ctx,
		`SELECT kind, after_json FROM event WHERE task_id = ? AND kind = 'released' ORDER BY rowid DESC LIMIT 1`,
		string(task.ID)).Scan(&kind, &after)
	if err != nil {
		t.Fatalf("released event: %v", err)
	}
	if !strings.Contains(after, `"note":"context saved"`) || !strings.Contains(after, `"holder":null`) {
		t.Errorf("released after_json = %q, want note + null holder", after)
	}

	// (c) Releasing an unclaimed task is an idempotent no-op over real SQL too.
	fresh, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Never claimed", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create second task: %v", err)
	}
	got, err := tasks.Release(ctx, agentB, fresh.ID, "")
	if err != nil {
		t.Fatalf("release of unclaimed task must be a no-op, got: %v", err)
	}
	if got.Version != 1 || got.Claim.Present() {
		t.Errorf("no-op release mutated the row: (v%d holder %+v expiry %v), want (v1 unclaimed)",
			got.Version, got.Claim.By(), got.Claim.Expiry())
	}
	if _, n := eventCounts(t, db, ctx, fresh.ID); n != 1 { // only `created`
		t.Errorf("no-op release appended an event (%d total, want 1)", n)
	}
}
