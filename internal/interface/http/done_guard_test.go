package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

type unmetACsErr struct {
	Error    string `json:"error"`
	Resource string `json:"resource"`
	ID       string `json:"id"`
	UnmetACs []int  `json:"unmet_acs"`
	Message  string `json:"message"`
}

func TestUpdateDone_BlockedTask_409BodyShape(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Blocked Done Gate")
	blocker := newTask(t, stk, p.ID, "Blocker")
	blocked := newTask(t, stk, p.ID, "Blocked task")
	addBlockedBy(t, stk, blocked.ID, blocker.ID)

	// Move to doing first (todo -> doing -> done)
	code, body := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+blocked.ID, map[string]any{
		"actor":   actor("alice"),
		"version": blocked.Version,
		"status":  "doing",
	})
	if code != http.StatusOK {
		t.Fatalf("move to doing = %d (%s)", code, string(body))
	}
	doingSummary := mustDecode[taskSummaryDTO](t, body)

	// Attempt move doing -> done while blocked
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+blocked.ID, map[string]any{
		"actor":   actor("alice"),
		"version": doingSummary.Version,
		"status":  "done",
	})
	if code != http.StatusConflict {
		t.Fatalf("update to done on blocked task = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[blockedErr](t, body)
	if e.Error != "task blocked" {
		t.Errorf("error code = %q, want \"task blocked\"", e.Error)
	}
	if e.Resource != "task" || e.ID != blocked.ID {
		t.Errorf("envelope = (resource %q id %q), want (task %s)", e.Resource, e.ID, blocked.ID)
	}
	if len(e.Blockers) != 1 || e.Blockers[0] != blocker.ID {
		t.Fatalf("blockers = %v, want [%s]", e.Blockers, blocker.ID)
	}
	if !strings.Contains(e.Message, "blocked") {
		t.Errorf("message %q must mention blocked state", e.Message)
	}

	// Override blockers -> 200 OK
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+blocked.ID, map[string]any{
		"actor":             actor("alice"),
		"version":           doingSummary.Version,
		"status":            "done",
		"override_blockers": true,
	})
	if code != http.StatusOK {
		t.Fatalf("override update to done = %d, want 200 (body %s)", code, string(body))
	}
	res := mustDecode[taskSummaryDTO](t, body)
	if res.Status != "done" {
		t.Errorf("status = %q, want done", res.Status)
	}
}

func TestUpdateDone_UnsatisfiedACs_409BodyShape(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "AC Done Gate")

	// Create task with 2 acceptance criteria
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor":               actor("alice"),
		"title":               "Task with ACs",
		"project_id":          p.ID,
		"acceptance_criteria": []string{"Criteria 1", "Criteria 2"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create task = %d (%s)", code, string(body))
	}
	created := mustDecode[createTaskResponse](t, body)

	// Move to doing
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":   actor("alice"),
		"version": created.Task.Version,
		"status":  "doing",
	})
	if code != http.StatusOK {
		t.Fatalf("move to doing = %d (%s)", code, string(body))
	}
	doingSummary := mustDecode[taskSummaryDTO](t, body)

	// (a) Attempt move to done with incomplete ACs -> 409
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":   actor("alice"),
		"version": doingSummary.Version,
		"status":  "done",
	})
	if code != http.StatusConflict {
		t.Fatalf("update to done with unmet ACs = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[unmetACsErr](t, body)
	if e.Error != "unsatisfied_acceptance_criteria" {
		t.Errorf("error = %q, want \"unsatisfied_acceptance_criteria\"", e.Error)
	}
	if e.Resource != "task" || e.ID != created.Task.ID {
		t.Errorf("envelope = (%s %s), want (task %s)", e.Resource, e.ID, created.Task.ID)
	}
	if len(e.UnmetACs) != 2 || e.UnmetACs[0] != 1 || e.UnmetACs[1] != 2 {
		t.Fatalf("unmet_acs = %v, want [1, 2]", e.UnmetACs)
	}
	if !strings.Contains(e.Message, "unsatisfied acceptance criteria") {
		t.Errorf("message %q must explain unsatisfied ACs", e.Message)
	}

	// (b) Toggle AC 1 to done
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID+"/acceptance-criteria/1", map[string]any{
		"actor": actor("alice"),
		"done":  true,
		"note":  "verified",
	})
	if code != http.StatusOK {
		t.Fatalf("toggle AC 1 = %d (%s)", code, string(body))
	}

	// Attempt move to done again -> 409 with unmet_acs = [2]
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":   actor("alice"),
		"version": doingSummary.Version,
		"status":  "done",
	})
	if code != http.StatusConflict {
		t.Fatalf("update to done with unmet AC 2 = %d, want 409 (body %s)", code, string(body))
	}
	e = mustDecode[unmetACsErr](t, body)
	if len(e.UnmetACs) != 1 || e.UnmetACs[0] != 2 {
		t.Fatalf("unmet_acs = %v, want [2]", e.UnmetACs)
	}

	// (c) Override ACs -> 200 OK
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":        actor("alice"),
		"version":      doingSummary.Version,
		"status":       "done",
		"override_acs": true,
	})
	if code != http.StatusOK {
		t.Fatalf("override update to done = %d, want 200 (body %s)", code, string(body))
	}
	res := mustDecode[taskSummaryDTO](t, body)
	if res.Status != "done" {
		t.Errorf("status = %q, want done", res.Status)
	}
}

func TestUpdateDone_AllACsSatisfied_SucceedsNormally(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "AC Completed Project")

	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor":               actor("alice"),
		"title":               "Task with AC",
		"project_id":          p.ID,
		"acceptance_criteria": []string{"Single criterion"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create task = %d (%s)", code, string(body))
	}
	created := mustDecode[createTaskResponse](t, body)

	// Move to doing
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":   actor("alice"),
		"version": created.Task.Version,
		"status":  "doing",
	})
	if code != http.StatusOK {
		t.Fatalf("move to doing = %d (%s)", code, string(body))
	}
	doingSummary := mustDecode[taskSummaryDTO](t, body)

	// Toggle criterion to done
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID+"/acceptance-criteria/1", map[string]any{
		"actor": actor("alice"),
		"done":  true,
	})
	if code != http.StatusOK {
		t.Fatalf("toggle AC 1 = %d (%s)", code, string(body))
	}

	// Move to done without override -> 200 OK
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+created.Task.ID, map[string]any{
		"actor":   actor("alice"),
		"version": doingSummary.Version,
		"status":  "done",
	})
	if code != http.StatusOK {
		t.Fatalf("update to done when AC satisfied = %d, want 200 (body %s)", code, string(body))
	}
	res := mustDecode[taskSummaryDTO](t, body)
	if res.Status != "done" {
		t.Errorf("status = %q, want done", res.Status)
	}
}
