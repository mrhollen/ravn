package application

// claim_test.go: unit tests for the atomic TTL-lease lifecycle (Claim, Release,
// Heartbeat) over fakeUoW + fakes + FakeClock — no SQLite, no HTTP. The racing-
// goroutines proof that only ONE winner survives concurrent claims lives in the
// SQLite integration tests (internal/infrastructure/sqlite/uow_claim_test.go);
// here the focus is event payloads, version-bump policy, and error shapes.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// claimAgent is a dedicated worker distinct from testAgent so tests can also
// exercise the "different actor" paths without confusion.
var claimAgent = domain.Actor{Type: domain.ActorAgent, Name: "worker-7"}

func asClaimConflict(t *testing.T, err error) *domain.ClaimConflictError {
	t.Helper()
	var cc *domain.ClaimConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("err = %v (%T), want *domain.ClaimConflictError", err, err)
	}
	return cc
}

// leaseEvents filters the fake event store down to claim/release events.
func leaseEvents(uow *fakeUoW) []domain.Event {
	out := make([]domain.Event, 0, len(uow.events.events))
	for _, e := range uow.events.events {
		if e.Kind == domain.EventClaimed || e.Kind == domain.EventReleased {
			out = append(out, e)
		}
	}
	return out
}

func TestTasksClaim_FreshClaimSetsLeaseDoingVersionAndEmitsClaimedEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Fresh Claim", 1, nil) // v1 todo; unassigned on purpose

	fc.SetNow(fc.now.Add(5 * time.Minute))
	claimedAt := fc.now
	got, err := svc.Claim(ctx, claimAgent, task.ID, 600, false)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if got.Status != domain.StatusDoing {
		t.Errorf("status = %q, want doing (claim implies doing)", got.Status)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2 (lease is a task-row mutation)", got.Version)
	}
	if by := got.Claim.By(); by == nil || by.Type != domain.ActorAgent || by.Name != claimAgent.Name {
		t.Fatalf("holder = %+v, want agent worker-7", by)
	}
	if !got.Claim.Present() || !got.Claim.At().Equal(claimedAt) {
		t.Errorf("claimed_at = %v (present=%v), want the claim instant %s", got.Claim.At(), got.Claim.Present(), claimedAt)
	}
	wantExpiry := claimedAt.Add(600 * time.Second) // expiry = now + ttl
	if !got.Claim.Present() || !got.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("claim_expires_at = %v, want %s", got.Claim.Expiry(), wantExpiry)
	}
	if got.Claim.TTLSeconds() != 600 {
		t.Errorf("stored ttl = %ds, want 600s", got.Claim.TTLSeconds())
	}
	if got.Assignee != nil {
		t.Errorf("claim must NOT touch the durable assignee (got %+v)", got.Assignee)
	}

	evs := leaseEvents(uow)
	if len(evs) != 1 {
		t.Fatalf("lease events = %d, want exactly 1 claimed", len(evs))
	}
	ev := evs[0]
	if ev.Kind != domain.EventClaimed || ev.Actor != claimAgent || !ev.At.Equal(claimedAt) {
		t.Errorf("event = (kind %q actor %+v at %s), want claimed by worker-7 at %s", ev.Kind, ev.Actor, ev.At, claimedAt)
	}
	for _, frag := range []string{`"status":"todo"`, `"holder":null`} {
		if !strings.Contains(ev.Before, frag) {
			t.Errorf("Before %q missing %s", ev.Before, frag)
		}
	}
	for _, frag := range []string{`"status":"doing"`, `"name":"worker-7"`, `"claim_expires_at"`, `"ttl_sec":600`, `"claim":"fresh"`} {
		if !strings.Contains(ev.After, frag) {
			t.Errorf("After %q missing %s", ev.After, frag)
		}
	}
	for _, e := range uow.events.events {
		if e.Kind == domain.EventStatusChanged {
			t.Error("claim must not also emit a generic status-changed event (the claimed event implies doing)")
		}
	}
}

func TestTasksClaim_IdempotentRenewalBySameActiveHolder(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Renewal Task", 1, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	first, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // v2, expires at t+1min+600s
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}

	// Still inside the first lease window: re-claim by the SAME actor succeeds
	// idempotently with a DIFFERENT (override) TTL. Decision (ADR 2026-09-26):
	// the renewal IS logged — one `claimed` event tagged "renewal" — because
	// every task-row mutation emits an event, and the tag lets readers filter
	// the noise out; heartbeats remain the silent keep-alive channel.
	fc.SetNow(fc.now.Add(5 * time.Minute)) // t+6min: first lease (t+11min) still active
	renewed, err := svc.Claim(ctx, claimAgent, task.ID, 900, false)
	if err != nil {
		t.Fatalf("idempotent re-claim by same active holder must succeed, got: %v", err)
	}
	if renewed.Version != 3 {
		t.Errorf("renewal version = %d, want 3 (each successful claim is a task-row mutation)", renewed.Version)
	}
	if by := renewed.Claim.By(); by == nil || by.Name != claimAgent.Name {
		t.Fatalf("holder after renewal = %+v, want still worker-7", by)
	}
	wantExpiry := fc.now.Add(900 * time.Second) // re-stamped from the FRESH now with the new ttl
	if !renewed.Claim.Present() || !renewed.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("expiry after renewal = %v, want fresh now + override ttl = %s", renewed.Claim.Expiry(), wantExpiry)
	}
	if renewed.Claim.TTLSeconds() != 900 {
		t.Errorf("stored ttl after renewal = %ds, want the overridden 900s", renewed.Claim.TTLSeconds())
	}

	evs := leaseEvents(uow)
	if len(evs) != 2 {
		t.Fatalf("lease events = %d, want 2 (fresh + renewal)", len(evs))
	}
	if !strings.Contains(evs[0].After, `"claim":"fresh"`) || !strings.Contains(evs[1].After, `"claim":"renewal"`) {
		t.Errorf("outcome tags = (%q, %q), want (fresh, renewal)", evs[0].After, evs[1].After)
	}
	if !strings.Contains(evs[1].Before, `"name":"worker-7"`) {
		t.Errorf("renewal Before %q must name the prior (same) holder", evs[1].Before)
	}
	if first.Version != 2 || !first.Claim.Present() { // sanity: initial state as expected
		t.Errorf("initial claim = (v%d expiry %v holder %+v), want (v2, set)", first.Version, first.Claim.Expiry(), first.Claim.By())
	}

	// The lease key is the actor NAME, not the type: a human-typed "worker-7"
	// re-claims idempotently while worker-7's agent-typed lease is still active.
	fc.SetNow(fc.now.Add(time.Minute))
	sameName := domain.Actor{Type: domain.ActorHuman, Name: "worker-7"}
	got, err := svc.Claim(ctx, sameName, task.ID, 600, false)
	if err != nil {
		t.Fatalf("re-claim by same NAME (different type) must succeed idempotently, got: %v", err)
	}
	if by := got.Claim.By(); by == nil || by.Type != domain.ActorHuman || by.Name != "worker-7" {
		t.Errorf("holder after same-name re-claim = %+v, want the human-typed worker-7", by)
	}
	evs = leaseEvents(uow)
	if !strings.Contains(evs[2].After, `"claim":"renewal"`) {
		t.Errorf("same-name re-claim tagged %q, want renewal (name is the lease key)", evs[2].After)
	}
}

func TestTasksClaim_DifferentLiveHolderGetsConflictNothingMutated(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Contested Task", 1, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	held, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // live lease by worker-7
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	eventsBefore := len(uow.events.events)

	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	_, err = svc.Claim(ctx, rival, task.ID, 600, false)
	cc := asClaimConflict(t, err)
	if cc.Holder.Type != claimAgent.Type || cc.Holder.Name != claimAgent.Name {
		t.Errorf("conflict holder = %+v, want the live lease holder worker-7", cc.Holder)
	}
	if !cc.ExpiresAt.Equal(held.Claim.Expiry()) {
		t.Errorf("conflict expiry = %s, want the stored lease expiry %s (the body is the recovery instruction)", cc.ExpiresAt, held.Claim.Expiry())
	}

	stored, err := svc.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get after lost claim: %v", err)
	}
	byStored := stored.Claim.By()
	if stored.Version != held.Version || byStored == nil || byStored.Name != claimAgent.Name {
		t.Errorf("lost claim mutated the row: (v%d holder %+v), want unchanged (v%d worker-7)",
			stored.Version, byStored, held.Version)
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("lost claim appended an event (%d → %d); conflicts must not emit", eventsBefore, len(uow.events.events))
	}
}

func TestTasksClaim_ExpiredLeaseTakeoverByAnotherActorAndByPreviousHolder(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Zombie Task", 1, nil)

	// worker-7 claims a short (300s) lease.
	fc.SetNow(fc.now.Add(time.Minute))
	_, err := svc.Claim(ctx, claimAgent, task.ID, 300, false)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}

	// Advance PAST the expiry: the lease is a zombie. Another actor takes over.
	fc.SetNow(fc.now.Add(6 * time.Minute)) // past t+1min+300s
	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	takeover, err := svc.Claim(ctx, rival, task.ID, 600, false)
	if err != nil {
		t.Fatalf("takeover of expired lease must win, got: %v", err)
	}
	if by := takeover.Claim.By(); by == nil || by.Name != rival.Name {
		t.Fatalf("holder after takeover = %+v, want worker-8", by)
	}
	if takeover.Status != domain.StatusDoing || takeover.Version != 3 {
		t.Errorf("takeover row = (status %q v%d), want (doing 3)", takeover.Status, takeover.Version)
	}

	evs := leaseEvents(uow)
	if len(evs) != 2 {
		t.Fatalf("lease events = %d, want 2", len(evs))
	}
	if !strings.Contains(evs[1].After, `"claim":"takeover"`) || !strings.Contains(evs[1].Before, `"name":"worker-7"`) {
		t.Errorf("takeover event = (before %q, after %q), want prior holder worker-7 tagged takeover", evs[1].Before, evs[1].After)
	}

	// The previous holder reclaiming their OWN expired zombie lease is ALSO a
	// takeover (their lease was not active), not a renewal.
	fc.SetNow(fc.now.Add(20 * time.Minute)) // past worker-8's expiry too
	back, err := svc.Claim(ctx, claimAgent, task.ID, 600, false)
	if err != nil {
		t.Fatalf("self-takeover of own expired lease must win, got: %v", err)
	}
	if by := back.Claim.By(); by == nil || by.Name != claimAgent.Name {
		t.Errorf("holder = %+v, want worker-7 again", by)
	}
	evs = leaseEvents(uow)
	if len(evs) != 3 || !strings.Contains(evs[2].After, `"claim":"takeover"`) {
		t.Errorf("self-zombie re-claim must be tagged takeover; got %d events (%v)", len(evs), kindsOf(uow.events.events))
	}
}

func TestTasksRelease_HolderClearsLeaseAndEmitsReleasedEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Release Task", 1, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	held, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // v2 doing
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A NON-holder releasing while the lease is live gets ClaimConflictError
	// (documented choice over domain.ErrInvalid: it's a coordination collision
	// carrying holder + expiry, not malformed input) and nothing mutates.
	eventsBefore := len(uow.events.events)
	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	_, err = svc.Release(ctx, rival, task.ID, "not mine")
	cc := asClaimConflict(t, err)
	if cc.Holder.Name != claimAgent.Name || !cc.ExpiresAt.Equal(held.Claim.Expiry()) {
		t.Errorf("release conflict = (holder %+v expiry %s), want live holder worker-7 with stored expiry", cc.Holder, cc.ExpiresAt)
	}
	stored, err := svc.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Version != held.Version || stored.Status != domain.StatusDoing || len(uow.events.events) != eventsBefore {
		t.Errorf("failed release mutated state: (v%d %q, %d events), want unchanged", stored.Version, stored.Status, len(uow.events.events))
	}

	fc.SetNow(fc.now.Add(10 * time.Minute)) // still inside the lease window
	released, err := svc.Release(ctx, claimAgent, task.ID, "stale enough")
	if err != nil {
		t.Fatalf("Release by holder: %v", err)
	}
	if released.Status != domain.StatusTodo {
		t.Errorf("status after release = %q, want todo", released.Status)
	}
	if released.Version != 3 {
		t.Errorf("version after release = %d, want 3 (release is a task-row mutation)", released.Version)
	}
	if released.Claim.Present() {
		t.Errorf("lease not fully cleared: present=%v holder=%+v expiry=%v ttl=%d",
			released.Claim.Present(), released.Claim.By(), released.Claim.Expiry(), released.Claim.TTLSeconds())
	}
	if released.Assignee != nil {
		t.Errorf("release must NOT touch the durable assignee (got %+v)", released.Assignee)
	}

	evs := leaseEvents(uow)
	if len(evs) != 2 { // claimed + released
		t.Fatalf("lease events = %d, want 2", len(evs))
	}
	rv := evs[1]
	if rv.Kind != domain.EventReleased || rv.Actor != claimAgent {
		t.Fatalf("event = (kind %q actor %+v), want released by worker-7", rv.Kind, rv.Actor)
	}
	for _, frag := range []string{`"status":"doing"`, `"name":"worker-7"`, `"claim_expires_at"`} {
		if !strings.Contains(rv.Before, frag) {
			t.Errorf("Before %q missing %s", rv.Before, frag)
		}
	}
	for _, frag := range []string{`"status":"todo"`, `"holder":null`, `"note":"stale enough"`} {
		if !strings.Contains(rv.After, frag) {
			t.Errorf("After %q missing %s", rv.After, frag)
		}
	}
}

func TestTasksRelease_UnclaimedTaskIsIdempotentNoOp(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Never Claimed", 1, nil) // v1 todo, no lease

	fc.SetNow(fc.now.Add(time.Minute))
	got, err := svc.Release(ctx, claimAgent, task.ID, "huh")
	if err != nil {
		t.Fatalf("releasing an unclaimed task must be an idempotent no-op, got: %v", err)
	}
	if got.Version != 1 || got.Status != domain.StatusTodo {
		t.Errorf("no-op release mutated the row: (v%d %q), want (v1 todo)", got.Version, got.Status)
	}
	if len(uow.events.events) != 1 { // only the original `created`
		t.Errorf("no-op release must append no event (got %d total)", len(uow.events.events))
	}
}

func TestTasksRelease_ExpiredLease_OriginalHolderSucceedsAndRivalFails(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Expired Lease Release", 1, nil)

	held, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // v2 doing
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Advance time past the 600s TTL
	fc.SetNow(fc.now.Add(700 * time.Second))

	// Rival tries to release expired lease -> ClaimConflictError
	eventsBefore := len(uow.events.events)
	rival := domain.Actor{Type: domain.ActorAgent, Name: "rival-agent"}
	_, err = svc.Release(ctx, rival, task.ID, "rival trying to release expired")
	if err == nil {
		t.Fatalf("expected ClaimConflictError for rival release of expired lease, got nil")
	}
	cc := asClaimConflict(t, err)
	if cc.Holder.Name != claimAgent.Name || !cc.ExpiresAt.Equal(held.Claim.Expiry()) {
		t.Errorf("rival conflict = %+v, want holder %s expiry %s", cc, claimAgent.Name, held.Claim.Expiry())
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("failed rival release appended event")
	}

	// Original holder releases expired lease -> succeeds
	released, err := svc.Release(ctx, claimAgent, task.ID, "finishing late")
	if err != nil {
		t.Fatalf("release by holder of expired lease: %v", err)
	}
	if released.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", released.Status)
	}
	if released.Version != 3 {
		t.Errorf("version = %d, want 3", released.Version)
	}
	if released.Claim.Present() {
		t.Errorf("lease not cleared: holder=%+v expiry=%v", released.Claim.By(), released.Claim.Expiry())
	}

	evs := leaseEvents(uow)
	if len(evs) != 2 { // claimed + released
		t.Fatalf("lease events = %d, want 2", len(evs))
	}
	rv := evs[1]
	if rv.Kind != domain.EventReleased || rv.Actor != claimAgent {
		t.Fatalf("event = (kind %q actor %+v), want released by claimAgent", rv.Kind, rv.Actor)
	}
}

func TestTasksRelease_ForceRelease_ByOperator(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Force Release Test", 1, nil)

	_, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // v2 doing
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Advance time past the 600s TTL (zombie lease)
	fc.SetNow(fc.now.Add(700 * time.Second))

	operator := domain.Actor{Type: domain.ActorHuman, Name: "operator-admin"}
	released, err := svc.Release(ctx, operator, task.ID, "breaking zombie lease", true)
	if err != nil {
		t.Fatalf("force release: %v", err)
	}
	if released.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo", released.Status)
	}
	if released.Version != 3 {
		t.Errorf("version = %d, want 3", released.Version)
	}
	if released.Claim.Present() {
		t.Errorf("lease not cleared: holder=%+v expiry=%v", released.Claim.By(), released.Claim.Expiry())
	}

	evs := leaseEvents(uow)
	if len(evs) != 2 { // claimed + released
		t.Fatalf("lease events = %d, want 2", len(evs))
	}
	rv := evs[1]
	if rv.Kind != domain.EventReleased || rv.Actor != operator {
		t.Fatalf("event = (kind %q actor %+v), want released by operator", rv.Kind, rv.Actor)
	}
	if !strings.Contains(rv.After, `"force":true`) {
		t.Errorf("After %q missing \"force\":true", rv.After)
	}

	// Repeated force release on unclaimed task is idempotent no-op
	eventsBefore := len(uow.events.events)
	noop, err := svc.Release(ctx, operator, task.ID, "repeat", true)
	if err != nil {
		t.Fatalf("noop force release: %v", err)
	}
	if noop.Version != 3 || noop.Claim.Present() {
		t.Errorf("noop force release mutated row: v=%d holder=%+v expiry=%v", noop.Version, noop.Claim.By(), noop.Claim.Expiry())
	}
	if len(uow.events.events) != eventsBefore {
		t.Errorf("noop force release appended an event")
	}
}

func TestTasksHeartbeat_ExtendsActiveLeaseWithoutVersionOrEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Beat Me", 1, nil)

	fc.SetNow(fc.now.Add(time.Minute))
	_, err := svc.Claim(ctx, claimAgent, task.ID, 600, false) // v2, expires t+1min+600s
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	eventsAfterClaim := len(uow.events.events) // created + claimed

	fc.SetNow(fc.now.Add(4 * time.Minute)) // inside the lease window
	beaten, err := svc.Heartbeat(ctx, claimAgent, task.ID)
	if err != nil {
		t.Fatalf("heartbeat by active holder: %v", err)
	}
	wantExpiry := fc.now.Add(600 * time.Second) // now + STORED ttl (600s), not a new override
	if !beaten.Claim.Present() || !beaten.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("expiry after heartbeat = %v, want now + stored ttl = %s", beaten.Claim.Expiry(), wantExpiry)
	}
	if beaten.Version != 2 {
		t.Errorf("version after heartbeat = %d, want UNCHANGED (keep-alive bumps nothing)", beaten.Version)
	}
	if len(uow.events.events) != eventsAfterClaim {
		t.Errorf("heartbeat appended an event (%d → %d); keep-alives must be silent",
			eventsAfterClaim, len(uow.events.events))
	}

	// A non-holder heartbeating gets a conflict (the lease is worker-7's).
	rival := domain.Actor{Type: domain.ActorAgent, Name: "worker-8"}
	var ccRival *domain.ClaimConflictError
	if _, err := svc.Heartbeat(ctx, rival, task.ID); !errors.As(err, &ccRival) {
		t.Errorf("heartbeat by non-holder err = %v, want *domain.ClaimConflictError", err)
	}

	// After expiry even the holder's heartbeat is a conflict (zombie lease).
	fc.SetNow(fc.now.Add(30 * time.Minute)) // well past the extended expiry
	var ccExpired *domain.ClaimConflictError
	if _, err := svc.Heartbeat(ctx, claimAgent, task.ID); !errors.As(err, &ccExpired) {
		t.Errorf("heartbeat after expiry err = %v, want *domain.ClaimConflictError", err)
	}

	// Heartbeating an unclaimed task is NOT a claim conflict — there is nobody
	// to wait for — but a specific domain.ErrNotClaimed ("claim it first"),
	// with the task id in the message.
	fresh := mustCreateTask(t, svc, "Beat Me Too", 1, nil)
	_, err = svc.Heartbeat(ctx, claimAgent, fresh.ID)
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("heartbeat on unclaimed task err = %v (%T), want wrapped domain.ErrNotClaimed", err, err)
	}
	var ccZero *domain.ClaimConflictError
	if errors.As(err, &ccZero) {
		t.Errorf("unclaimed heartbeat must not be a ClaimConflictError (even a zero-holder one): %+v", ccZero)
	}
	if !strings.Contains(err.Error(), string(fresh.ID)) {
		t.Errorf("unclaimed heartbeat message %q should name the task id", err.Error())
	}
}

func TestTasksClaim_TTLDefaultOverrideAndCap(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)

	// ttlSec = 0 → DefaultClaimTTL (15m).
	fc.SetNow(fc.now.Add(time.Minute))
	task := mustCreateTask(t, svc, "Default TTL", 1, nil)
	got, err := svc.Claim(ctx, claimAgent, task.ID, 0, false)
	if err != nil {
		t.Fatalf("claim with ttl 0: %v", err)
	}
	wantExpiry := fc.now.Add(DefaultClaimTTL)
	if !got.Claim.Present() || !got.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("expiry with ttlSec=0 = %v, want now + DefaultClaimTTL (%s)", got.Claim.Expiry(), wantExpiry)
	}
	if got.Claim.TTLSeconds() != int(DefaultClaimTTL.Seconds()) {
		t.Errorf("stored ttl = %ds, want %d", got.Claim.TTLSeconds(), int(DefaultClaimTTL.Seconds()))
	}

	// Oversized override clamps to maxClaimTTL (3600s) rather than erroring.
	fc.SetNow(fc.now.Add(time.Minute))
	got, err = svc.Claim(ctx, claimAgent, mustCreateTask(t, svc, "Capped TTL", 1, nil).ID, 7200, false)
	if err != nil {
		t.Fatalf("claim with ttl 7200: %v", err)
	}
	if !got.Claim.Present() || !got.Claim.Expiry().Equal(fc.now.Add(maxClaimTTL)) {
		t.Errorf("expiry for ttlSec=7200 = %v, want now + max (%s)", got.Claim.Expiry(), maxClaimTTL)
	}

	// Negative TTL is invalid input: rejected, nothing written.
	fc.SetNow(fc.now.Add(time.Minute))
	victim := mustCreateTask(t, svc, "Negative TTL", 1, nil)
	if _, err := svc.Claim(ctx, claimAgent, victim.ID, -5, false); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("claim with ttlSec=-5 err = %v, want wrapped domain.ErrInvalid", err)
	}
	if stored, _ := svc.Get(ctx, victim.ID); stored.Version != 1 || stored.Claim.Present() {
		t.Errorf("invalid TTL must not mutate the row (got v%d holder %+v expiry %v)",
			stored.Version, stored.Claim.By(), stored.Claim.Expiry())
	}

	// WithDefaultClaimTTL injects a custom default for ttlSec = 0 calls.
	fast := svc.WithDefaultClaimTTL(30 * time.Second)
	fc.SetNow(fc.now.Add(time.Minute))
	got, err = fast.Claim(ctx, claimAgent, mustCreateTask(t, svc, "Injected TTL", 1, nil).ID, 0, false)
	if err != nil {
		t.Fatalf("claim with injected default: %v", err)
	}
	if !got.Claim.Present() || !got.Claim.Expiry().Equal(fc.now.Add(30*time.Second)) {
		t.Errorf("expiry with injected 30s default = %v, want now + 30s", got.Claim.Expiry())
	}
}

func TestTasksClaim_CompletedOrArchived_ReturnsErrInvalidAndNoMutation(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)

	// 1. Claiming a completed task returns ErrInvalid and does not mutate status
	doneTask := mustCreateTask(t, svc, "Done Task", 1, nil)
	// Transition to doing, then done
	_, err := svc.Claim(ctx, claimAgent, doneTask.ID, 600, false)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}
	doneStatus := domain.StatusDone
	_, err = svc.Update(ctx, claimAgent, doneTask.ID, TaskUpdateInput{Status: &doneStatus})
	if err != nil {
		t.Fatalf("setup mark done: %v", err)
	}

	beforeEvCount := len(uow.events.events)
	fc.SetNow(fc.now.Add(time.Minute))
	otherAgent := domain.Actor{Type: domain.ActorAgent, Name: "worker-other"}
	_, err = svc.Claim(ctx, otherAgent, doneTask.ID, 600, false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("claim done task err = %v, want domain.ErrInvalid", err)
	}

	stored, err := svc.Get(ctx, doneTask.ID)
	if err != nil {
		t.Fatalf("get done task: %v", err)
	}
	if stored.Status != domain.StatusDone {
		t.Errorf("stored status = %q, want done (must not be mutated to doing)", stored.Status)
	}
	if stored.Claim.Present() {
		t.Errorf("stored lease present (holder %+v expiry %v), want none", stored.Claim.By(), stored.Claim.Expiry())
	}
	if len(uow.events.events) != beforeEvCount {
		t.Errorf("no events should be emitted on rejected claim (got %d events, want %d)", len(uow.events.events), beforeEvCount)
	}

	// 2. Claiming an archived task returns ErrInvalid and does not mutate status
	archivedTask := mustCreateTask(t, svc, "Archived Task", 1, nil)
	arch := true
	_, err = svc.Update(ctx, claimAgent, archivedTask.ID, TaskUpdateInput{Archived: &arch})
	if err != nil {
		t.Fatalf("setup archive: %v", err)
	}

	beforeEvCount = len(uow.events.events)
	fc.SetNow(fc.now.Add(time.Minute))
	_, err = svc.Claim(ctx, claimAgent, archivedTask.ID, 600, false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("claim archived task err = %v, want domain.ErrInvalid", err)
	}

	stored, err = svc.Get(ctx, archivedTask.ID)
	if err != nil {
		t.Fatalf("get archived task: %v", err)
	}
	if !stored.Archived {
		t.Error("archived task must remain archived")
	}
	if stored.Status != domain.StatusTodo {
		t.Errorf("stored status = %q, want todo", stored.Status)
	}
	if stored.Claim.Present() {
		t.Errorf("stored lease present (holder %+v expiry %v), want none", stored.Claim.By(), stored.Claim.Expiry())
	}
	if len(uow.events.events) != beforeEvCount {
		t.Errorf("no events should be emitted on rejected claim (got %d events, want %d)", len(uow.events.events), beforeEvCount)
	}
}

func TestTasksHeartbeat_CompletedOrArchived_ReturnsErrInvalidAndNoMutation(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)

	// 1. Heartbeat on completed task returns ErrInvalid
	task := mustCreateTask(t, svc, "Heartbeat Done Task", 1, nil)
	_, err := svc.Claim(ctx, claimAgent, task.ID, 600, false)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}
	doneStatus := domain.StatusDone
	_, err = svc.Update(ctx, claimAgent, task.ID, TaskUpdateInput{Status: &doneStatus})
	if err != nil {
		t.Fatalf("setup mark done: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Minute))
	_, err = svc.Heartbeat(ctx, claimAgent, task.ID)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("heartbeat done task err = %v, want domain.ErrInvalid", err)
	}

	// 2. Heartbeat on archived task returns ErrInvalid
	archTask := mustCreateTask(t, svc, "Heartbeat Archived Task", 1, nil)
	arch := true
	_, err = svc.Update(ctx, claimAgent, archTask.ID, TaskUpdateInput{Archived: &arch})
	if err != nil {
		t.Fatalf("setup archive: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Minute))
	_, err = svc.Heartbeat(ctx, claimAgent, archTask.ID)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("heartbeat archived task err = %v, want domain.ErrInvalid", err)
	}
}

func TestTasksRelease_DoneStatusPreserved(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)

	task := mustCreateTask(t, svc, "Done Release Task", 1, nil)
	_, err := svc.Claim(ctx, claimAgent, task.ID, 600, false)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}

	// Manually set task to done while keeping lease (e.g. legacy/direct)
	cur, _ := svc.Get(ctx, task.ID)
	cur.Status = domain.StatusDone
	uow := svc.uow.(*fakeUoW)
	uow.tasks.byID[task.ID] = cur

	fc.SetNow(fc.now.Add(time.Minute))
	released, err := svc.Release(ctx, claimAgent, task.ID, "completed work")
	if err != nil {
		t.Fatalf("release done task: %v", err)
	}
	if released.Status != domain.StatusDone {
		t.Errorf("released status = %q, want done (must NOT revert to todo)", released.Status)
	}
	if released.Claim.Present() {
		t.Errorf("released lease present (holder %+v expiry %v), want none", released.Claim.By(), released.Claim.Expiry())
	}
}
