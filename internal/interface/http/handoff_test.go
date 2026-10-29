package httpapi

// E2E test for POST /tasks/{id}/handoff over a real SQLite-backed stack (same as
// the claim→lapse tests): create → A claims (agent) → hand off to B → assert 200
// with the task now todo + assigned to B + exactly one forwarded handoff comment;
// then a NON-holder's attempt while A re-holds the lease → 409 naming the holder.

import (
	"net/http"
	"testing"
)

func handoffAgent(name string) map[string]any { return map[string]any{"type": "agent", "name": name} }

func TestHandoffREST_ClaimThenHandOffThenForeignConflict(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Handoff Project")
	task := newTask(t, stk, p.ID, "handoff e2e target")

	// A (agent worker-a) claims → doing, lease held by a.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": handoffAgent("worker-a"), "ttl_sec": 3600,
	})
	if code != http.StatusOK {
		t.Fatalf("claim by worker-a = %d, want 200 (body %s)", code, string(body))
	}

	// Hand off from a to human receiver b: kind + body + notes + to_assignee.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/handoff", map[string]any{
		"actor":       handoffAgent("worker-a"),
		"kind":        "handoff",
		"body":        "Passing to b — see notes.",
		"notes":       "Resume blob for the next run",
		"to_assignee": map[string]any{"type": "human", "name": "worker-b"},
	})
	if code != http.StatusOK {
		t.Fatalf("handoff = %d, want 200 (body %s)", code, string(body))
	}
	res := mustDecode[handoffResp](t, body)

	// Task row effects: doing→todo, lease cleared, durable assignee set to human b.
	if res.Task.Status != "todo" {
		t.Errorf("task status = %q, want todo (doing→todo)", res.Task.Status)
	}
	if res.Task.ClaimedBy != nil {
		t.Errorf("claimed_by not cleared: %+v", res.Task.ClaimedBy)
	}
	if res.Task.Assignee == nil || res.Task.Assignee.Type != "human" || res.Task.Assignee.Name != "worker-b" {
		t.Errorf("assignee = %+v, want human worker-b", res.Task.Assignee)
	}

	// Exactly one handoff comment in the body, forwarded verbatim, actor = a.
	if res.Comment.Kind != "handoff" || res.Comment.Body != "Passing to b — see notes." {
		t.Errorf("comment = (kind %q body %q), want the forwarded handoff note", res.Comment.Kind, res.Comment.Body)
	}
	if res.Comment.Actor.Type != "agent" || res.Comment.Actor.Name != "worker-a" {
		t.Errorf("comment actor = %+v, want agent worker-a", res.Comment.Actor)
	}

	// The handoff cleared the lease. Re-claim by A so a NON-holder (b) now faces a
	// live foreign lease → genuine 409 claim conflict naming the holder.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": handoffAgent("worker-a"), "ttl_sec": 3600,
	})
	if code != http.StatusOK {
		t.Fatalf("re-claim by worker-a = %d, want 200 (body %s)", code, string(body))
	}

	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/handoff", map[string]any{
		"actor": handoffAgent("worker-b"), "kind": "handoff", "body": "I do not hold this lease",
	})
	if code != http.StatusConflict {
		t.Fatalf("non-holder handoff = %d, want 409 (body %s)", code, string(body))
	}
	errBody := mustDecode[errResp](t, body)
	if errBody.Error != "claim conflict" {
		t.Errorf("409 error = %q, want \"claim conflict\"", errBody.Error)
	}
	if errBody.Holder == nil || errBody.Holder.Name != "worker-a" {
		t.Errorf("conflict holder = %+v, want worker-a (the live lease holder)", errBody.Holder)
	}
}
