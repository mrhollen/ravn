package httpapi

// claim_blocked_test.go: HTTP-level proof of Claim's open-blocker gate — the
// 409 JSON body shape for the structured error (machine-readable "error" code
// + blocker IDs), the override_blockers bypass, and the resolved-blockers
// happy path. Application-level semantics (renewal exemption, takeover gating,
// conflict ordering) live in internal/application/claim_blocked_test.go.

import (
	"net/http"
	"strings"
	"testing"
)

// blockedErr is the 409 body for *domain.TaskBlockedError: the error code plus
// the machine-readable open-blocker ID list and an agent-recovery message.
type blockedErr struct {
	Error    string   `json:"error"`
	Resource string   `json:"resource"`
	ID       string   `json:"id"`
	Blockers []string `json:"blockers"`
	Message  string   `json:"message"`
}

// addBlockedBy adds "<blocked> is blocked-by <other>" and fatals on any error.
func addBlockedBy(t *testing.T, stk stack, blockedID, otherID string) {
	t.Helper()
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+blockedID+"/relations", map[string]any{
		"actor": actor("agent-a"), "other_id": otherID, "kind": "blocked-by",
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("add blocked-by edge = %d (%s)", code, string(body))
	}
}

func TestClaimBlocked_FreshClaimOnBlockedTask_409BodyShape(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Blocker Gate")
	blocker := newTask(t, stk, p.ID, "build the foundation")
	blocked := newTask(t, stk, p.ID, "needs the foundation")
	addBlockedBy(t, stk, blocked.ID, blocker.ID)

	// (a) FRESH claim on a blocked task → 409 with the structured body: a
	// machine-readable error code + the open blocker IDs + a recovery message.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+blocked.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusConflict {
		t.Fatalf("claim on blocked task = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[blockedErr](t, body)
	if e.Error != "task blocked" {
		t.Errorf("error code = %q, want \"task blocked\"", e.Error)
	}
	if e.Resource != "task" || e.ID != blocked.ID {
		t.Errorf("envelope = (resource %q id %q), want (task %s)", e.Resource, e.ID, blocked.ID)
	}
	if len(e.Blockers) != 1 || e.Blockers[0] != blocker.ID {
		t.Fatalf("blockers = %v, want exactly [%s]", e.Blockers, blocker.ID)
	}
	if !strings.Contains(e.Message, "blocked") || !strings.Contains(e.Message, blocker.ID) {
		t.Errorf("message %q must name the blocked state and the open blocker", e.Message)
	}

	// The 409 left nothing behind: still todo, unclaimed, still reported as
	// blocked by the resume payload.
	d := getDetail(t, stk, blocked.ID)
	if d.Status != "todo" || d.ClaimedBy != nil {
		t.Errorf("after rejected claim = (status %s holder %+v), want todo unclaimed", d.Status, d.ClaimedBy)
	}
	if !d.Blocked || len(d.Blockers) != 1 || d.Blockers[0].ID != blocker.ID {
		t.Errorf("detail blocked = %v with %d blockers, want true/[%s]", d.Blocked, len(d.Blockers), blocker.ID)
	}

	// (c) override_blockers=true bypasses the gate → 200 doing (the field is
	// OPTIONAL — its absence in the request above is what kept this gated).
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+blocked.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "override_blockers": true,
	})
	if code != http.StatusOK {
		t.Fatalf("override claim = %d, want 200 (body %s)", code, string(body))
	}
	got := mustDecode[taskSummaryDTO](t, body)
	if got.Status != "doing" || got.ClaimedBy == nil || got.ClaimedBy.Name != "agent-a" {
		t.Fatalf("override claim → %+v, want doing held by agent-a", got)
	}
}

func TestClaimBlocked_AllBlockersResolved_ClaimsNormally(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Resolved Blockers")
	blocker := newTask(t, stk, p.ID, "foundation")
	blocked := newTask(t, stk, p.ID, "needs foundation")
	addBlockedBy(t, stk, blocked.ID, blocker.ID)

	// Sanity: gated while the blocker is open.
	code, _ := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+blocked.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusConflict {
		t.Fatalf("claim before resolution = %d, want 409", code)
	}

	// Complete the blocker (todo → doing → done, the validated path).
	patchTask(t, stk, blocker.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, blocker.ID, 2, map[string]any{"status": "done"})

	// (b) A completed blocker no longer blocks: a PLAIN claim (no override)
	// succeeds — backward compatible with every pre-gate caller.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+blocked.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusOK {
		t.Fatalf("claim after blocker done = %d, want 200 (body %s)", code, string(body))
	}
	got := mustDecode[taskSummaryDTO](t, body)
	if got.Status != "doing" || got.ClaimedBy == nil || got.ClaimedBy.Name != "agent-a" {
		t.Errorf("claim after resolution → %+v, want doing held by agent-a", got)
	}

	d := getDetail(t, stk, blocked.ID)
	if d.Blocked || len(d.Blockers) != 0 {
		t.Errorf("detail after blocker done = blocked %v with %d blockers, want false/0", d.Blocked, len(d.Blockers))
	}
}
