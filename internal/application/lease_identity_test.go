package application

// lease_identity_test.go: documents and pins the UNIFIED NAME IDENTITY model
// for lease-holder equality (see sameLeaseHolder in tasks.go, ADR 2026-09-26):
// the actor NAME is the entire lease principal; claimed_by_type is
// display/audit-only. Positive: a human-typed and an agent-typed actor sharing
// a name are the SAME holder across claim / heartbeat / release. Negative: any
// DIFFERENT name is a foreign holder — conflict with nothing mutated, whatever
// the type.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// TestLeaseIdentity_SameNameDifferentType_ShareTheLease is the positive case:
// a human-typed "worker-7" and an agent-typed "worker-7" are one lease
// principal. This behavior is INTENTIONAL (unified NAME identity) — if it ever
// changes to type-sensitive principals, this test must be deleted and the SQL
// CAS in internal/infrastructure/sqlite/tasks_repo.go changed with it.
func TestLeaseIdentity_SameNameDifferentType_ShareTheLease(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Cross-Type Task", 1, nil)

	human7 := domain.Actor{Type: domain.ActorHuman, Name: "worker-7"}
	agent7 := domain.Actor{Type: domain.ActorAgent, Name: "worker-7"} // same NAME, different type

	// 1. Fresh claim by the human-typed worker-7.
	fc.SetNow(fc.now.Add(time.Minute))
	claimed, err := svc.Claim(ctx, human7, task.ID, 600, false)
	if err != nil {
		t.Fatalf("claim by human worker-7: %v", err)
	}
	if by := claimed.Claim.By(); by == nil || by.Type != domain.ActorHuman || by.Name != "worker-7" {
		t.Fatalf("holder after fresh claim = %+v, want human worker-7", by)
	}

	// 2. Heartbeat by the AGENT-typed same name succeeds — no version bump, no
	// event (keep-alive is silent), expiry extended with the stored TTL.
	eventsAfterClaim := len(uow.events.events)
	fc.SetNow(fc.now.Add(4 * time.Minute)) // inside the 600s lease window
	beaten, err := svc.Heartbeat(ctx, agent7, task.ID)
	if err != nil {
		t.Fatalf("heartbeat by same NAME (different type) must succeed, got: %v", err)
	}
	wantExpiry := fc.now.Add(600 * time.Second)
	if !beaten.Claim.Present() || !beaten.Claim.Expiry().Equal(wantExpiry) {
		t.Errorf("expiry after cross-type heartbeat = %v, want now + stored ttl = %s", beaten.Claim.Expiry(), wantExpiry)
	}
	if beaten.Version != claimed.Version {
		t.Errorf("version after heartbeat = %d, want UNCHANGED (%d)", beaten.Version, claimed.Version)
	}
	if len(uow.events.events) != eventsAfterClaim {
		t.Errorf("cross-type heartbeat appended an event; keep-alives must be silent")
	}

	// 3. Idempotent re-claim by the agent-typed same name is a RENEWAL (not a
	// takeover), and the lease's recorded type follows the latest claimant.
	renewed, err := svc.Claim(ctx, agent7, task.ID, 600, false)
	if err != nil {
		t.Fatalf("re-claim by same NAME (different type) must succeed idempotently, got: %v", err)
	}
	if by := renewed.Claim.By(); by == nil || by.Type != domain.ActorAgent || by.Name != "worker-7" {
		t.Errorf("holder after cross-type re-claim = %+v, want agent worker-7", by)
	}
	evs := leaseEvents(uow)
	if len(evs) != 2 || !strings.Contains(evs[1].After, `"claim":"renewal"`) {
		t.Errorf("cross-type re-claim events = %d (%v), want the second tagged renewal", len(evs), kindsOf(uow.events.events))
	}

	// 4. Release by the ORIGINAL human-typed name succeeds: release is keyed on
	// the name alone, so either type may end the shared lease.
	released, err := svc.Release(ctx, human7, task.ID, "cross-type release")
	if err != nil {
		t.Fatalf("release by same NAME (original type) must succeed, got: %v", err)
	}
	if released.Claim.Present() || released.Status != domain.StatusTodo {
		t.Errorf("after cross-type release = (lease present=%v status %q), want (false todo)",
			released.Claim.Present(), released.Status)
	}
}

// TestLeaseIdentity_DifferentName_NeverSharesTheLease is the negative case: a
// different name is ALWAYS a foreign holder — claim, heartbeat, and release
// each lose with *domain.ClaimConflictError{Holder, ExpiresAt}, mutate nothing,
// and append no event. Type is irrelevant to this rule.
func TestLeaseIdentity_DifferentName_NeverSharesTheLease(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Contested Identity", 1, nil)

	holder := domain.Actor{Type: domain.ActorHuman, Name: "worker-7"}
	fc.SetNow(fc.now.Add(time.Minute))
	held, err := svc.Claim(ctx, holder, task.ID, 600, false)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	eventsAfterClaim := len(uow.events.events)

	// Two rivals with DIFFERENT names. Their types deliberately differ from the
	// holder's and from each other — none of that matters, the name does.
	rivals := []domain.Actor{
		{Type: domain.ActorAgent, Name: "worker-8"},
		{Type: domain.ActorHuman, Name: "operator-admin"},
	}
	for _, rival := range rivals {
		t.Run(string(rival.Type)+"_"+rival.Name, func(t *testing.T) {
			if _, err := svc.Claim(ctx, rival, task.ID, 600, false); err != nil {
				cc := asClaimConflict(t, err)
				assertConflictBody(t, "claim", cc, holder, held.Claim.Expiry())
			} else {
				t.Fatalf("claim by different name must lose with ClaimConflictError, got success")
			}
			if _, err := svc.Heartbeat(ctx, rival, task.ID); err != nil {
				cc := asClaimConflict(t, err)
				assertConflictBody(t, "heartbeat", cc, holder, held.Claim.Expiry())
			} else {
				t.Fatalf("heartbeat by different name must lose with ClaimConflictError, got success")
			}
			if _, err := svc.Release(ctx, rival, task.ID, "not mine"); err != nil {
				cc := asClaimConflict(t, err)
				assertConflictBody(t, "release", cc, holder, held.Claim.Expiry())
			} else {
				t.Fatalf("release by different name must lose with ClaimConflictError, got success")
			}

			stored, gerr := svc.Get(ctx, task.ID)
			if gerr != nil {
				t.Fatalf("Get: %v", gerr)
			}
			by := stored.Claim.By()
			if stored.Version != held.Version || by == nil || by.Name != holder.Name || by.Type != holder.Type {
				t.Errorf("foreign attempts mutated the lease: (v%d holder %+v), want unchanged (v%d %s worker-7)",
					stored.Version, by, held.Version, holder.Type)
			}
			if len(uow.events.events) != eventsAfterClaim {
				t.Errorf("foreign attempts appended %d events; conflicts must not emit",
					len(uow.events.events)-eventsAfterClaim)
			}
		})
	}
}

// assertConflictBody checks that a claim/heartbeat/release conflict carries the
// live holder (name AND type — the body is the recovery instruction) and the
// stored lease expiry.
func assertConflictBody(t *testing.T, op string, cc *domain.ClaimConflictError, wantHolder domain.Actor, wantExpiry time.Time) {
	t.Helper()
	if cc.Holder != wantHolder {
		t.Errorf("%s conflict holder = %+v, want the live holder %+v", op, cc.Holder, wantHolder)
	}
	if !cc.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("%s conflict expiry = %s, want stored lease expiry %s", op, cc.ExpiresAt, wantExpiry)
	}
}
