package application

// claim_blocked_test.go: unit tests for Claim's open-blocker gate — a FRESH
// claim (or takeover of an expired lease) on a task with unresolved blockers is
// rejected with *domain.TaskBlockedError unless overrideBlockers bypasses the
// gate; renewals by the current active holder are exempt (a working agent's
// refresh must never break), and an ACTIVE foreign lease keeps its existing
// ClaimConflictError. Open blocker = outgoing blocked-by edge whose target is
// not done and not archived — the predicate BlockersOf encodes (fake twin of
// SQLite's UnblockedOnly guard). The HTTP-level 409 body shape is proven in
// internal/interface/http/claim_blocked_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

func asTaskBlocked(t *testing.T, err error) *domain.TaskBlockedError {
	t.Helper()
	var tb *domain.TaskBlockedError
	if !errors.As(err, &tb) {
		t.Fatalf("err = %v (%T), want *domain.TaskBlockedError", err, err)
	}
	return tb
}

// newClaimBlockedEnv is newTaskTestEnv plus the Relations use case on the SAME
// fakeUoW (so BlockersOf sees the edge set) and a test clock.
func newClaimBlockedEnv(t *testing.T) (*Tasks, *Relations, *fakeClock, *fakeUoW) {
	t.Helper()
	svc, fc, uow := newTaskTestEnv(t)
	rels := NewRelations(uow, fc, &fakeIDGen{})
	return svc, rels, fc, uow
}

// mustDone transitions t to done through the validated todo→doing→done path.
func mustDone(t *testing.T, svc *Tasks, id domain.ID) {
	t.Helper()
	doing := domain.StatusDoing
	if _, err := svc.Update(context.Background(), testAgent, id, TaskUpdateInput{Status: &doing}); err != nil {
		t.Fatalf("move %s to doing: %v", id, err)
	}
	done := domain.StatusDone
	if _, err := svc.Update(context.Background(), testAgent, id, TaskUpdateInput{Status: &done}); err != nil {
		t.Fatalf("complete %s: %v", id, err)
	}
}

// (a) FRESH claim on a blocked task ⇒ rejected with the structured error; no
// lease, no status change, no version bump, no event.
func TestClaim_FreshClaimOnBlockedTask_RejectedStructuredNoMutation(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Needs foundation", 2, nil) // the blocked task
	y := mustCreateTask(t, svc, "Build foundation", 3, nil) // its open blocker
	mustBlock(t, rels, x, y)

	fc.SetNow(fc.now.Add(time.Minute))
	eventsBefore := len(uow.events.events)
	_, err := svc.Claim(ctx, claimAgent, x.ID, 600, false)
	tb := asTaskBlocked(t, err)
	if tb.TaskID != x.ID {
		t.Errorf("error TaskID = %s, want the claimed task %s", tb.TaskID, x.ID)
	}
	if len(tb.BlockerIDs) != 1 || tb.BlockerIDs[0] != y.ID {
		t.Fatalf("error BlockerIDs = %v, want exactly [%s]", tb.BlockerIDs, y.ID)
	}

	stored, err := svc.Get(ctx, x.ID)
	if err != nil {
		t.Fatalf("Get after rejected claim: %v", err)
	}
	if stored.Version != 1 || stored.Status != domain.StatusTodo || stored.ClaimedBy != nil {
		t.Errorf("rejected claim mutated the row: (v%d status %q holder %+v), want unchanged (v1 todo unclaimed)",
			stored.Version, stored.Status, stored.ClaimedBy)
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("rejected claim appended an event (%d → %d); rejections must not emit",
			eventsBefore, len(uow.events.events))
	}
}

// (b) All blockers done or archived ⇒ claim succeeds normally. Done and
// archived targets do NOT count as open blockers — same predicate as List's
// UnblockedOnly guard.
func TestClaim_AllBlockersDoneOrArchived_ClaimsNormally(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Two resolved blockers", 2, nil)
	doneB := mustCreateTask(t, svc, "Already finished", 1, nil)
	archB := mustCreateTask(t, svc, "Archived mid-flight", 1, nil)

	mustDone(t, svc, doneB.ID)
	archive := true
	if _, err := svc.Update(ctx, testAgent, archB.ID, TaskUpdateInput{Archived: &archive}); err != nil {
		t.Fatalf("archive blocker: %v", err)
	}
	mustBlock(t, rels, x, doneB)
	mustBlock(t, rels, x, archB)

	fc.SetNow(fc.now.Add(time.Minute))
	got, err := svc.Claim(ctx, claimAgent, x.ID, 600, false) // no override needed
	if err != nil {
		t.Fatalf("claim with all blockers resolved: %v", err)
	}
	if got.Status != domain.StatusDoing || got.Version != 2 || got.ClaimedBy == nil || got.ClaimedBy.Name != claimAgent.Name {
		t.Errorf("claim = (status %q v%d holder %+v), want doing v2 held by worker-7",
			got.Status, got.Version, got.ClaimedBy)
	}

	evs := leaseEvents(uow)
	if len(evs) != 1 || !strings.Contains(evs[0].After, `"claim":"fresh"`) {
		t.Errorf("expected exactly one fresh claimed event, got %d", len(evs))
	}
}

// (c) Override=true on a blocked task ⇒ claim succeeds (emergency unblock).
// The gate also guards takeovers of EXPIRED leases: without the override they
// are rejected just like fresh claims; with it, the takeover proceeds.
func TestClaim_OverrideBypassesOpenBlockerGate(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Emergency", 4, nil)
	y := mustCreateTask(t, svc, "Still blocking", 3, nil)
	mustBlock(t, rels, x, y)

	fc.SetNow(fc.now.Add(time.Minute))
	// Sanity: the default (no override) is rejected with the structured error.
	var noOverrideErr *domain.TaskBlockedError
	if _, cerr := svc.Claim(ctx, claimAgent, x.ID, 600, false); !errors.As(cerr, &noOverrideErr) {
		t.Fatalf("claim without override err = %v (%T), want *domain.TaskBlockedError", cerr, cerr)
	}

	// Override → the FRESH claim succeeds despite the open blocker.
	got, err := svc.Claim(ctx, claimAgent, x.ID, 300, true)
	if err != nil {
		t.Fatalf("override claim: %v", err)
	}
	if got.Status != domain.StatusDoing || got.ClaimedBy == nil || got.ClaimedBy.Name != claimAgent.Name {
		t.Errorf("override claim = (status %q holder %+v), want doing held by worker-7", got.Status, got.ClaimedBy)
	}

	// Expiry passes; a RIVAL takeover of the expired lease is still gated...
	fc.SetNow(fc.now.Add(10 * time.Minute)) // past t+1min+300s
	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	_, err = svc.Claim(ctx, rival, x.ID, 600, false)
	tb := asTaskBlocked(t, err)
	if len(tb.BlockerIDs) != 1 || tb.BlockerIDs[0] != y.ID {
		t.Fatalf("gated takeover BlockerIDs = %v, want [%s]", tb.BlockerIDs, y.ID)
	}
	// ...until it carries the override: then the takeover wins.
	took, err := svc.Claim(ctx, rival, x.ID, 600, true)
	if err != nil {
		t.Fatalf("override takeover: %v", err)
	}
	if took.ClaimedBy == nil || took.ClaimedBy.Name != rival.Name || took.Status != domain.StatusDoing {
		t.Errorf("takeover = (holder %+v status %q), want worker-8 doing", took.ClaimedBy, took.Status)
	}
	evs := leaseEvents(uow)
	if len(evs) != 2 { // fresh (override) + takeover (override)
		t.Fatalf("lease events = %d, want 2", len(evs))
	}
	if !strings.Contains(evs[1].After, `"claim":"takeover"`) {
		t.Errorf("second claim tagged %q, want takeover", evs[1].After)
	}
}

// (d) RE-CLAIM by the CURRENT ACTIVE holder (renewal) is exempt from the gate:
// a blocked-by edge landing mid-task must never break a working agent's
// refresh. The rival in the same window still sees the usual conflict.
func TestClaim_RenewalByActiveHolderNotGatedByNewlyLandedBlockers(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "In flight", 2, nil)
	y := mustCreateTask(t, svc, "First blocker", 3, nil)
	z := mustCreateTask(t, svc, "Lands mid-task", 1, nil)
	mustBlock(t, rels, x, y)

	fc.SetNow(fc.now.Add(time.Minute))
	claimed, err := svc.Claim(ctx, claimAgent, x.ID, 600, true) // starts via override
	if err != nil {
		t.Fatalf("initial override claim: %v", err)
	}

	// A NEW open blocker lands while the lease is active.
	mustBlock(t, rels, x, z)

	// The holder's re-claim (lease refresh) must succeed WITHOUT any override —
	// breaking it here would strand a working agent inside its own lease window.
	fc.SetNow(fc.now.Add(5 * time.Minute)) // t+6min: first lease (t+11min) still active
	renewed, err := svc.Claim(ctx, claimAgent, x.ID, 600, false)
	if err != nil {
		t.Fatalf("renewal by active holder must not be gated by blockers, got: %v", err)
	}
	if renewed.Version != claimed.Version+1 || renewed.ClaimedBy == nil || renewed.ClaimedBy.Name != claimAgent.Name {
		t.Errorf("renewal = (v%d holder %+v), want v%d held by worker-7",
			renewed.Version, renewed.ClaimedBy, claimed.Version+1)
	}
	evs := leaseEvents(uow)
	if len(evs) != 2 || !strings.Contains(evs[1].After, `"claim":"renewal"`) {
		t.Errorf("expected fresh + renewal events, got %d (%v)", len(evs), kindsOf(uow.events.events))
	}

	// A rival inside the window still gets the plain conflict — blockers are not
	// in play for an ACTIVE foreign lease (see case e below).
	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	var ccRival *domain.ClaimConflictError
	if _, rerr := svc.Claim(ctx, rival, x.ID, 600, false); !errors.As(rerr, &ccRival) {
		t.Errorf("rival claim on active lease err = %v (%T), want *domain.ClaimConflictError", rerr, rerr)
	}
}

// (e) A task in 'doing' with an ACTIVE foreign lease keeps its EXACT existing
// conflict behavior — the gate must not shadow ClaimConflictError by firing
// first. The rival gets the holder+expiry body, nothing mutates.
func TestClaim_ActiveForeignLeaseOnBlockedTask_KeepsConflictError(t *testing.T) {
	ctx := context.Background()
	svc, rels, fc, uow := newClaimBlockedEnv(t)
	x := mustCreateTask(t, svc, "Contested + blocked", 2, nil)
	y := mustCreateTask(t, svc, "Open blocker", 3, nil)
	mustBlock(t, rels, x, y)

	fc.SetNow(fc.now.Add(time.Minute))
	held, err := svc.Claim(ctx, claimAgent, x.ID, 600, true) // worker-7 holds an active lease
	if err != nil {
		t.Fatalf("initial override claim: %v", err)
	}
	eventsBefore := len(uow.events.events)

	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	_, err = svc.Claim(ctx, rival, x.ID, 600, false) // no override — and still a conflict, not "task blocked"
	cc := asClaimConflict(t, err)
	if cc.Holder.Type != claimAgent.Type || cc.Holder.Name != claimAgent.Name {
		t.Errorf("conflict holder = %+v, want the live lease holder worker-7", cc.Holder)
	}
	if !cc.ExpiresAt.Equal(*held.ClaimExpiry) {
		t.Errorf("conflict expiry = %s, want the stored lease expiry %s", cc.ExpiresAt, *held.ClaimExpiry)
	}

	stored, err := svc.Get(ctx, x.ID)
	if err != nil {
		t.Fatalf("Get after lost claim: %v", err)
	}
	if stored.Version != held.Version || stored.ClaimedBy == nil || stored.ClaimedBy.Name != claimAgent.Name {
		t.Errorf("lost claim mutated the row: (v%d holder %+v), want unchanged (v%d worker-7)",
			stored.Version, stored.ClaimedBy, held.Version)
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("lost claim appended an event (%d → %d); conflicts must not emit", eventsBefore, len(uow.events.events))
	}
}
