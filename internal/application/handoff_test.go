package application

// handoff_test.go: unit tests for the atomic HandOff use case over fakeUoW +
// fakes + FakeClock (no SQLite, no HTTP). Mirrors claim_test.go's focus on
// event payloads, version-bump policy, error shapes, and — critically — the
// no-partial-state contract when the repository write fails mid-transaction.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// Distinct actors so handoff tests never collide with the shared testAgent or
// claimAgent used by neighbouring files.
var (
	hoHolder   = domain.Actor{Type: domain.ActorAgent, Name: "handoff-holder-a"}
	hoReceiver = domain.Actor{Type: domain.ActorAgent, Name: "handoff-worker-b"}
	hoHuman    = domain.Actor{Type: domain.ActorHuman, Name: "human-receiver"}
	hoRival    = domain.Actor{Type: domain.ActorAgent, Name: "rival-c"}
)

// mustHandOffClaim claims a fresh task for holder with a generous 900s lease so
// it is still ACTIVE at the (unchanged) clock instant the handoff runs.
func mustHandOffClaim(t *testing.T, svc *Tasks, id domain.ID, holder domain.Actor) {
	t.Helper()
	if _, err := svc.Claim(context.Background(), holder, id, 900, false); err != nil {
		t.Fatalf("Claim %q: %v", holder.Name, err)
	}
}

func TestHandOff_AgentToAgent_AtomicEffectsAndEvents(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff happy", 2, nil) // v1 todo, unassigned

	mustHandOffClaim(t, svc, task.ID, hoHolder) // v2 doing, lease held by holder-a
	before := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{
		Kind:     domain.CommentHandoff,
		Body:     "Passing to worker-b; context below.",
		Notes:    "Fresh resume blob for the next run",
		Assignee: &hoReceiver,
	})
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}

	// Task row effects (the returned fresh row).
	if got.Task.Status != domain.StatusTodo {
		t.Errorf("status = %q, want todo (doing→todo)", got.Task.Status)
	}
	if got.Task.Version != 3 {
		t.Errorf("version = %d, want 3 (claim left v2; handoff bumps exactly once)", got.Task.Version)
	}
	if got.Task.Claim.Present() {
		t.Errorf("lease not cleared: %+v", got.Task.Claim)
	}
	if got.Task.Assignee == nil || got.Task.Assignee.Type != domain.ActorAgent || got.Task.Assignee.Name != hoReceiver.Name {
		t.Errorf("assignee = %+v, want agent handoff-worker-b", got.Task.Assignee)
	}
	if got.Task.Notes != "Fresh resume blob for the next run" {
		t.Errorf("notes = %q, want the replaced blob", got.Task.Notes)
	}

	// The persisted row must agree with the returned one.
	stored := uow.tasks.byID[task.ID]
	if stored.Version != 3 || stored.Status != domain.StatusTodo {
		t.Errorf("stored row = v%d/%s, want v3/todo", stored.Version, stored.Status)
	}

	// Exactly ONE comment: kind handoff, body forwarded verbatim, actor = holder.
	cs := uow.comments.byTask[task.ID]
	if len(cs) != 1 {
		t.Fatalf("comments = %d, want exactly 1", len(cs))
	}
	if cs[0].Kind != domain.CommentHandoff ||
		cs[0].Body != "Passing to worker-b; context below." ||
		cs[0].Actor.Name != hoHolder.Name {
		t.Errorf("comment = %+v, want handoff/forwarded-body/holder-a", cs[0])
	}

	// Event batch appended by the handoff, in order: noted, assigned, commented, released.
	evs := uow.events.events[before:]
	want := []string{
		string(domain.EventNoted), string(domain.EventAssigned),
		string(domain.EventCommented), string(domain.EventReleased),
	}
	if gotK := kindsOf(evs); !reflect.DeepEqual(gotK, want) {
		t.Errorf("event kinds = %v, want %v", gotK, want)
	}
}

func TestHandOff_ToAssigneeHuman_EmptyBodySynthesized(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff human", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder)
	before := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{
		Kind:     "", // default → handoff
		Body:     "", // synthesize a default
		Assignee: &hoHuman,
	})
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}

	wantBody := fmt.Sprintf("Handed off to %s by %s", hoHuman.Name, hoHolder.Name)
	if got.Comment.Kind != domain.CommentHandoff {
		t.Errorf("comment kind = %q, want handoff (default)", got.Comment.Kind)
	}
	if got.Comment.Body != wantBody {
		t.Errorf("synthesized body = %q, want %q", got.Comment.Body, wantBody)
	}
	if got.Task.Assignee == nil || got.Task.Assignee.Type != domain.ActorHuman || got.Task.Assignee.Name != hoHuman.Name {
		t.Errorf("assignee = %+v, want human human-receiver", got.Task.Assignee)
	}

	evs := uow.events.events[before:]
	want := []string{
		string(domain.EventAssigned), string(domain.EventCommented), string(domain.EventReleased),
	} // no noted (notes not supplied)
	if gotK := kindsOf(evs); !reflect.DeepEqual(gotK, want) {
		t.Errorf("event kinds = %v, want %v", gotK, want)
	}
}

func TestHandOff_ClearAssignee(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	// Seed a durable assignee at creation so the clear is observable.
	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title: "handoff clear", ProjectID: testProjectID, Priority: 1,
		Assignee: &hoHuman, // pre-existing owner to be cleared
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mustHandOffClaim(t, svc, task.ID, hoHolder)
	before := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{
		ClearAssignee: true, // precedence over any receiver (none given)
	})
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if got.Task.Assignee != nil {
		t.Errorf("assignee not cleared: %+v", got.Task.Assignee)
	}
	wantBody := fmt.Sprintf("Lease released by %s (task unassigned)", hoHolder.Name)
	if got.Comment.Body != wantBody {
		t.Errorf("synthesized body = %q, want %q", got.Comment.Body, wantBody)
	}

	evs := uow.events.events[before:]
	want := []string{
		string(domain.EventAssigned), string(domain.EventCommented), string(domain.EventReleased),
	} // owner → null is an assigned change; no notes supplied
	if gotK := kindsOf(evs); !reflect.DeepEqual(gotK, want) {
		t.Errorf("event kinds = %v, want %v", gotK, want)
	}
}

func TestHandOff_ForeignHolder_ConflictNoStateChange(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff foreign", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder) // v2 doing, held by holder-a (lease live)

	preVersion := uow.tasks.byID[task.ID].Version
	beforeEvents := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoRival, task.ID, HandOffInput{Assignee: &hoReceiver})
	_ = got
	cc := asClaimConflict(t, err) // *domain.ClaimConflictError or fatal
	if cc.Holder.Name != hoHolder.Name {
		t.Errorf("conflict holder = %+v, want %s", cc.Holder, hoHolder.Name)
	}

	stored := uow.tasks.byID[task.ID]
	if stored.Version != preVersion {
		t.Errorf("version changed on conflict: %d → %d", preVersion, stored.Version)
	}
	if stored.Status != domain.StatusDoing {
		t.Errorf("status changed on conflict: %s, want doing", stored.Status)
	}
	if !stored.Claim.Present() || stored.Claim.By().Name != hoHolder.Name {
		t.Errorf("lease mutated on conflict: %+v", stored.Claim)
	}
	if len(uow.comments.byTask[task.ID]) != 0 {
		t.Errorf("comment written on conflict")
	}
	if len(uow.events.events) != beforeEvents {
		t.Errorf("events appended on conflict")
	}
}

func TestHandOff_NotClaimed_NoLease(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff unclaimed", 1, nil) // never claimed → todo

	preVersion := uow.tasks.byID[task.ID].Version
	beforeEvents := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{})
	_ = got
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("err = %v (%T), want wrapped domain.ErrNotClaimed", err, err)
	}
	var cc *domain.ClaimConflictError
	if errors.As(err, &cc) {
		t.Errorf("unclaimed handoff must NOT be a ClaimConflictError: %+v", cc)
	}

	stored := uow.tasks.byID[task.ID]
	if stored.Version != preVersion || stored.Status != domain.StatusTodo {
		t.Errorf("state changed on not-claimed: v%d/%s", stored.Version, stored.Status)
	}
	if len(uow.comments.byTask[task.ID]) != 0 {
		t.Errorf("comment written on not-claimed")
	}
	if len(uow.events.events) != beforeEvents {
		t.Errorf("events appended on not-claimed")
	}
}

func TestHandOff_ExpiredLease_NotClaimed(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff expired", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder) // ttl 900s from now
	fc.SetNow(fc.now.Add(2 * time.Hour))        // well past the expiry — no sleep

	preVersion := uow.tasks.byID[task.ID].Version
	beforeEvents := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{})
	_ = got
	if !errors.Is(err, domain.ErrNotClaimed) {
		t.Fatalf("err = %v (%T), want wrapped domain.ErrNotClaimed for an expired lease", err, err)
	}

	stored := uow.tasks.byID[task.ID]
	if stored.Version != preVersion {
		t.Errorf("version changed on expired lease: %d → %d", preVersion, stored.Version)
	}
	if !stored.Claim.Present() || stored.Claim.By().Name != hoHolder.Name {
		t.Errorf("expired lease was released/changed: %+v", stored.Claim)
	}
	if len(uow.comments.byTask[task.ID]) != 0 || len(uow.events.events) != beforeEvents {
		t.Errorf("partial state written on expired lease")
	}
}

func TestHandOff_InvalidKind_BeforeWrites(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff badkind", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder) // held + doing

	preVersion := uow.tasks.byID[task.ID].Version
	beforeEvents := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{Kind: domain.CommentKind("blocker")})
	_ = got
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v (%T), want wrapped domain.ErrInvalid", err, err)
	}

	stored := uow.tasks.byID[task.ID]
	if stored.Version != preVersion || stored.Status != domain.StatusDoing {
		t.Errorf("state changed on invalid kind: v%d/%s", stored.Version, stored.Status)
	}
	if len(uow.comments.byTask[task.ID]) != 0 {
		t.Errorf("comment written on invalid kind")
	}
	if len(uow.events.events) != beforeEvents {
		t.Errorf("events appended on invalid kind")
	}
}

func TestHandOff_EmptyBody_NoReceiver_SynthesizedDefault(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff bare", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder)
	before := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{}) // kind default, no body/receiver/clear/notes
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	wantBody := fmt.Sprintf("Handed off by %s", hoHolder.Name)
	if got.Comment.Kind != domain.CommentHandoff || got.Comment.Body != wantBody {
		t.Errorf("comment = (%q, %q), want (handoff, %q)", got.Comment.Kind, got.Comment.Body, wantBody)
	}

	evs := uow.events.events[before:]
	want := []string{
		string(domain.EventCommented), string(domain.EventReleased),
	} // no assignee/notes change → only commented + released
	if gotK := kindsOf(evs); !reflect.DeepEqual(gotK, want) {
		t.Errorf("event kinds = %v, want %v", gotK, want)
	}
}

func TestHandOff_NotesOmitted_NeverCleared(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff keepnotes", 1, nil)
	const existing = "Existing resume blob that must survive the handoff"
	if _, err := svc.UpdateNotes(ctx, testAgent, task.ID, existing, task.Version); err != nil {
		t.Fatalf("UpdateNotes seed: %v", err)
	}

	mustHandOffClaim(t, svc, task.ID, hoHolder)
	before := len(uow.events.events)

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{Assignee: &hoReceiver}) // Notes empty → keep
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if got.Task.Notes != existing {
		t.Errorf("notes changed/cleared: %q, want %q (empty notes must never clear)", got.Task.Notes, existing)
	}

	evs := uow.events.events[before:]
	want := []string{
		string(domain.EventAssigned), string(domain.EventCommented), string(domain.EventReleased),
	} // no noted event because the blob was not replaced
	if gotK := kindsOf(evs); !reflect.DeepEqual(gotK, want) {
		t.Errorf("event kinds = %v (want NO noted): want %v", gotK, want)
	}
}

func TestHandOff_FailMidTxn_NoPartialState(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "handoff rollback", 1, nil)
	mustHandOffClaim(t, svc, task.ID, hoHolder) // v2 doing

	// Snapshot the full pre-state so we can prove nothing moved.
	pre := uow.tasks.byID[task.ID]
	preVersion := pre.Version
	beforeEvents := len(uow.events.events)

	uow.tasks.failHandoff = func(t domain.Task) error {
		return errors.New("simulated handoff write failure")
	}

	got, err := svc.HandOff(ctx, hoHolder, task.ID, HandOffInput{
		Body:     "should not persist",
		Notes:    "SHOULD NOT PERSIST",
		Assignee: &hoReceiver,
	})
	_ = got
	if err == nil || err.Error() != "simulated handoff write failure" {
		t.Fatalf("err = %v (%T), want the injected write failure", err, err)
	}

	stored := uow.tasks.byID[task.ID]
	if stored.Version != preVersion {
		t.Errorf("version changed: %d → %d (want unchanged)", preVersion, stored.Version)
	}
	if stored.Status != domain.StatusDoing {
		t.Errorf("status changed: %s (want doing)", stored.Status)
	}
	if !stored.Claim.Present() || stored.Claim.By().Name != hoHolder.Name {
		t.Errorf("lease mutated: %+v (want still held by holder-a)", stored.Claim)
	}
	if stored.Notes != pre.Notes {
		t.Errorf("notes mutated: %q", stored.Notes)
	}
	if pre.Assignee == nil && stored.Assignee != nil {
		t.Errorf("assignee materialized on failure: %+v", stored.Assignee)
	}
	if len(uow.comments.byTask[task.ID]) != 0 {
		t.Errorf("comment persisted on failure")
	}
	if len(uow.events.events) != beforeEvents {
		t.Errorf("events appended on failure")
	}
}
