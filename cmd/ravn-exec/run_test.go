package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// leaseStub is an in-memory, goroutine-safe fake of apiLease. It records the count
// and ordering of every lease/comment call so the tests can PROVE AC2 (multiple
// heartbeats during a long run + exactly one release) and AC3 (non-zero exit →
// progress comment with cmd+code before release; clean exit → no failure comment).
// No network, no ports.
type leaseStub struct {
	mu         sync.Mutex
	heartbeats int
	claims     int
	releases   int
	comments   []apiclient.CommentRequest
	order      []string // "hb" | "claim" | "release" | "comment", in call order

	fresh      bool  // when true the FIRST heartbeat misses (we don't hold the lease yet)
	failClaim  error // non-nil → ClaimTask returns this error (foreign lease / 409)
	hbErr      error // optional error returned by HeartbeatTask after a successful claim
	releaseErr error // optional error returned by ReleaseTask
}

func (s *leaseStub) HeartbeatTask(ctx context.Context, id string, actor apiclient.Actor) (*apiclient.TaskSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, "hb")
	s.heartbeats++
	if s.fresh && s.claims == 0 {
		return nil, errors.New("not claimed: initial probe misses so a fresh claim is taken")
	}
	return &apiclient.TaskSummary{ID: id}, s.hbErr
}

func (s *leaseStub) ClaimTask(ctx context.Context, id string, actor apiclient.Actor, ttlSec int) (*apiclient.TaskSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, "claim")
	if s.failClaim != nil {
		return nil, s.failClaim // claims stays 0 — the lease was NOT acquired
	}
	s.claims++
	return &apiclient.TaskSummary{ID: id, Status: "doing"}, nil
}

func (s *leaseStub) ReleaseTask(ctx context.Context, id string, actor apiclient.Actor, note string) (*apiclient.TaskSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, "release")
	s.releases++
	return &apiclient.TaskSummary{ID: id, Status: "todo"}, s.releaseErr
}

func (s *leaseStub) AddComment(ctx context.Context, id string, req apiclient.CommentRequest) (*apiclient.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, "comment")
	s.comments = append(s.comments, req)
	return nil, nil // Run only inspects the error; the comment body is asserted from `comments`
}

// snapshot copies the recorded state under lock so a test can read it without
// racing the (now-finished) background loop.
func (s *leaseStub) snapshot() (heartbeats, claims, releases int, comments []apiclient.CommentRequest, order []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]apiclient.CommentRequest, len(s.comments))
	copy(cp, s.comments)
	oc := make([]string, len(s.order))
	copy(oc, s.order)
	return s.heartbeats, s.claims, s.releases, cp, oc
}

func indexOf(seq []string, want string) int {
	for i, v := range seq {
		if v == want {
			return i
		}
	}
	return -1
}

// TestExecAC2MultipleHeartbeatsAndRelease proves AC2 end-to-end with a "long"
// silent foreground run: the background loop must fire heartbeats MULTIPLE times
// while the command runs, exactly one claim and exactly one release happen, a
// clean exit posts NO failure comment, and the release is the LAST lease op (the
// loop is stopped before teardown so nothing races it).
func TestExecAC2MultipleHeartbeatsAndRelease(t *testing.T) {
	stub := &leaseStub{fresh: true}
	opts := Options{
		TaskID:            "T-EXEC",
		Actor:             apiclient.Actor{Type: "agent", Name: "runner-e"},
		TTLSec:            900,
		HeartbeatInterval: 2 * time.Millisecond, // fast ticks so several fire in a short sleep
		Command:           []string{"true"},
	}

	ran := false
	code := Run(context.Background(), stub, opts, func() int {
		time.Sleep(50 * time.Millisecond) // simulate long, silent foreground work
		ran = true
		return 0
	})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0 (clean exit)", code)
	}
	if !ran {
		t.Fatal("runWrapped was not invoked")
	}

	hb, claims, rel, comments, order := stub.snapshot()
	if hb < 3 {
		t.Errorf("heartbeats during run = %d, want >=3 (background keep-alive fired multiple times)", hb)
	}
	if claims != 1 {
		t.Errorf("claims = %d, want exactly 1", claims)
	}
	if rel != 1 {
		t.Errorf("releases = %d, want exactly 1 (the lease is always released)", rel)
	}
	if len(comments) != 0 {
		t.Errorf("clean exit posted %d failure comment(s), want 0: %+v", len(comments), comments)
	}
	if last := order[len(order)-1]; last != "release" {
		t.Errorf("last lease op = %q, want \"release\" (heartbeat loop must stop before teardown)", last)
	}
}

// TestExecAC3SuccessNoComment is a focused check that a zero exit code posts NO
// progress comment (AC3's success half), while still releasing exactly once.
func TestExecAC3SuccessNoComment(t *testing.T) {
	stub := &leaseStub{fresh: true}
	opts := Options{TaskID: "T-OK", Actor: apiclient.Actor{Type: "agent", Name: "a"}, TTLSec: 900, Command: []string{"true"}}

	code := Run(context.Background(), stub, opts, func() int { return 0 })
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	hb, claims, rel, comments, _ := stub.snapshot()
	if hb < 1 || claims != 1 || rel != 1 {
		t.Errorf("lease counts: heartbeats=%d claims=%d releases=%d, want hb>=1 claims=1 releases=1", hb, claims, rel)
	}
	if len(comments) != 0 {
		t.Errorf("successful run posted %d comment(s), want none", len(comments))
	}
}

// TestExecAC3FailureCommentThenRelease proves AC3's failure half: a non-zero exit
// code posts EXACTLY ONE progress comment that names both the command and the exit
// code, and that comment is posted BEFORE the (always) release.
func TestExecAC3FailureCommentThenRelease(t *testing.T) {
	stub := &leaseStub{fresh: true}
	command := []string{"sh", "-c", "exit 3"} // displayCommand renders this verbatim
	opts := Options{TaskID: "T-FAIL", Actor: apiclient.Actor{Type: "agent", Name: "runner-x"}, TTLSec: 900, Command: command}

	code := Run(context.Background(), stub, opts, func() int { return 3 }) // the child's non-zero exit code
	if code != 3 {
		t.Fatalf("Run returned %d, want the propagated child code 3", code)
	}

	_, _, rel, comments, order := stub.snapshot()
	if rel != 1 {
		t.Errorf("releases = %d, want exactly 1 even on failure", rel)
	}
	if len(comments) != 1 {
		t.Fatalf("failure posted %d comment(s), want exactly 1: %+v", len(comments), comments)
	}
	c := comments[0]
	if c.Kind != "progress" {
		t.Errorf("failure comment kind = %q, want \"progress\"", c.Kind)
	}
	if !strings.Contains(c.Body, "sh -c exit 3") {
		t.Errorf("failure comment body does not name the command: %q", c.Body)
	}
	if !strings.Contains(c.Body, "code 3") {
		t.Errorf("failure comment body does not record the exit code: %q", c.Body)
	}
	if ic, ir := indexOf(order, "comment"), indexOf(order, "release"); ic == -1 || ir == -1 || ic > ir {
		t.Errorf("comment must be posted BEFORE release; order = %v (comment@%d, release@%d)", order, ic, ir)
	}
}

// TestExecCannotClaimWithoutRunning proves the safety gate: when neither our own
// heartbeat holds the lease NOR a fresh claim succeeds (e.g. a foreign active
// lease → 409), Run returns non-zero WITHOUT invoking the command, without
// releasing a lease we never held, and without starting any background heartbeats.
func TestExecCannotClaimWithoutRunning(t *testing.T) {
	stub := &leaseStub{
		fresh:     true,
		failClaim: errors.New("409 claim conflict: foreign active lease (claimed_by=other-agent)"),
	}
	opts := Options{TaskID: "T-LOCKED", Actor: apiclient.Actor{Type: "agent", Name: "runner-y"}, TTLSec: 900, Command: []string{"true"}}

	ran := false
	code := Run(context.Background(), stub, opts, func() int { ran = true; return 0 })

	if code == 0 {
		t.Fatalf("Run returned 0 when the lease could not be claimed; want non-zero")
	}
	if ran {
		t.Error("the command ran even though the task's lease could not be claimed — must never happen")
	}

	hb, claims, rel, comments, _ := stub.snapshot()
	if claims != 0 {
		t.Errorf("claims = %d, want 0 (claim failed)", claims)
	}
	if rel != 0 {
		t.Errorf("releases = %d, want 0 (we never held a lease to release)", rel)
	}
	if len(comments) != 0 {
		t.Errorf("posted %d comment(s), want none on a claim failure", len(comments))
	}
	if hb != 1 {
		t.Errorf("heartbeats = %d, want exactly 1 (only the initial probe; the loop must not start without a lease)", hb)
	}
}

// TestResolveInterval pins the interval policy: an explicit interval wins;
// otherwise it is ttlSec/3 (well under the TTL); degenerate sub-3s TTLs yield a
// non-positive duration so Run skips background ticks.
func TestResolveInterval(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want time.Duration
	}{
		{"explicit wins", Options{HeartbeatInterval: 7 * time.Second, TTLSec: 900}, 7 * time.Second},
		{"default ttl/3", Options{TTLSec: 900}, 300 * time.Second},
		{"default uses DefaultTTLSec when ttl zero", Options{}, DefaultTTLSec / 3 * time.Second},
		{"degenerate sub-3s ttl no ticks", Options{TTLSec: 2}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveInterval(tc.opts); got != tc.want {
				t.Errorf("resolveInterval(%v) = %v, want %v", tc.opts, got, tc.want)
			}
		})
	}
}

// TestDisplayCommand covers the argv renderer used in failure comments.
func TestDisplayCommand(t *testing.T) {
	if got := displayCommand([]string{"go", "test", "./..."}); got != "go test ./..." {
		t.Errorf("displayCommand = %q, want \"go test ./...\"", got)
	}
	if got := displayCommand(nil); got != "<empty>" {
		t.Errorf("displayCommand(nil) = %q, want \"<empty>\"", got)
	}
}
