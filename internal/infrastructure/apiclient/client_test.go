package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer builds an httptest upstream around handler and a client for it.
func newTestServer(t *testing.T, token string, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.URL, token), srv
}

// capture records one request's method/path/query/headers/body.
type capture struct {
	method string
	path   string
	query  map[string]string
	header http.Header
	body   []byte
}

func capturing(t *testing.T, next func(cap *capture, w http.ResponseWriter)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &capture{
			method: r.Method,
			path:   r.URL.Path,
			header: r.Header.Clone(),
			query:  map[string]string{},
		}
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				c.query[k] = v[0]
			}
		}
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			c.body = b
		}
		next(c, w)
	})
}

func writeJSONTest(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func wantAPIError(t *testing.T, err error, status int) *APIError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	e, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if e.StatusCode != status {
		t.Errorf("status = %d, want %d (err: %v)", e.StatusCode, status, err)
	}
	return e
}

// --- ListProjects ---------------------------------------------------------------------

const projectsBody = `{
  "projects": [
    {
      "id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",
      "name": "Website Redesign",
      "description": "MVP landing page work",
      "created_at": "2026-09-20T09:00:00Z",
      "updated_at": "2026-09-20T09:00:00Z",
      "task_counts": { "todo": 3, "doing": 1, "done": 5 }
    },
    {
      "id": "01J3YV2NMB4D9C6K8F0H3L5P7R",
      "name": "Agent Ops",
      "created_at": "2026-09-19T09:00:00Z",
      "updated_at": "2026-09-19T09:00:00Z",
      "task_counts": { "todo": 0, "doing": 0, "done": 0 }
    }
  ],
  "total": 2
}`

func TestListProjects_DecodesEnvelope(t *testing.T) {
	c, _ := newTestServer(t, "t0k3n", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodGet || cap.path != "/api/v1/projects" {
			t.Errorf("got %s %s, want GET /api/v1/projects", cap.method, cap.path)
		}
		if got := cap.header.Get("Authorization"); got != "Bearer t0k3n" {
			t.Errorf("Authorization = %q, want Bearer t0k3n", got)
		}
		writeJSONTest(w, http.StatusOK, projectsBody)
	}))

	list, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if list.Total != 2 || len(list.Projects) != 2 {
		t.Fatalf("total/len = %d/%d, want 2/2", list.Total, len(list.Projects))
	}
	p := list.Projects[0]
	if p.ID != "01J3YV2NMB4D9C6K8F0H3L5P7Q" || p.Name != "Website Redesign" {
		t.Errorf("project[0] = %+v", p)
	}
	if p.TaskCounts == nil || p.TaskCounts.Todo != 3 || p.TaskCounts.Doing != 1 || p.TaskCounts.Done != 5 {
		t.Errorf("task_counts = %+v, want todo=3 doing=1 done=5", p.TaskCounts)
	}
	if list.Projects[1].TaskCounts == nil || list.Projects[1].TaskCounts.Todo != 0 {
		t.Errorf("project[1] zero counts lost: %+v", list.Projects[1].TaskCounts)
	}
	if list.Projects[1].Description != "" {
		t.Errorf("omitted description should decode to empty, got %q", list.Projects[1].Description)
	}
}

func TestNoTokenSendsNoAuthorizationHeader(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if got := cap.header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		writeJSONTest(w, http.StatusOK, projectsBody)
	}))
	if _, err := c.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
}

// --- CreateProject ----------------------------------------------------------------------

func TestCreateProject_MethodPathBodyDecode(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/projects" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req CreateProjectRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Actor.Name != "ada" || req.Actor.Type != "" || req.Name != "Website Redesign" || req.Description != "MVP landing page work" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"id":"P1","name":"Website Redesign","description":"MVP landing page work","created_at":"2026-09-20T09:00:00Z","updated_at":"2026-09-20T09:00:00Z"}`)
	}))

	p, err := c.CreateProject(context.Background(), CreateProjectRequest{
		Actor: Actor{Name: "ada"}, Name: "Website Redesign", Description: "MVP landing page work",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID != "P1" || p.TaskCounts != nil {
		t.Errorf("create response row = %+v (counts must be absent)", p)
	}
}

// --- ListTasks --------------------------------------------------------------------------

func TestListTasks_QueryBuildingAndEnvelope(t *testing.T) {
	prio := 3
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodGet || cap.path != "/api/v1/tasks" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		want := map[string]string{
			"project_id": "P1", "status": "todo", "assignee_name": "ada", "assignee_type": "human",
			"archived": "true", "unblocked": "true", "search": "login", "priority": "3",
			"due_before": "2026-09-15T00:00:00Z", "due_after": "2026-10-01T00:00:00Z",
			"created_after": "2026-08-01T00:00:00Z", "created_before": "2026-09-25T00:00:00Z",
			"completed_after": "2026-09-15T00:00:00Z", "completed_before": "2026-10-05T00:00:00Z",
			"limit": "5", "offset": "10",
		}
		for k, v := range want {
			if cap.query[k] != v {
				t.Errorf("query %s = %q, want %q (all: %v)", k, cap.query[k], v, cap.query)
			}
		}
		writeJSONTest(w, http.StatusOK, `{"tasks":[{"id":"T1","title":"Fix login bug","status":"todo","priority":3,"version":1,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-25T18:02:11Z"}],"total":42,"limit":5,"offset":10}`)
	}))

	list, err := c.ListTasks(context.Background(), TaskFilter{
		ProjectID: "P1", Status: "todo", AssigneeName: "ada", AssigneeType: "human",
		Archived: true, Unblocked: true, Search: "login", Priority: &prio,
		DueBefore: "2026-09-15T00:00:00Z", DueAfter: "2026-10-01T00:00:00Z",
		CreatedAfter: "2026-08-01T00:00:00Z", CreatedBefore: "2026-09-25T00:00:00Z",
		CompletedAfter: "2026-09-15T00:00:00Z", CompletedBefore: "2026-10-05T00:00:00Z",
		Limit: 5, Offset: 10,
	})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if list.Total != 42 || list.Limit != 5 || list.Offset != 10 || len(list.Tasks) != 1 || list.Tasks[0].ID != "T1" {
		t.Errorf("envelope = %+v", list)
	}
}

func TestListTasks_ZeroFilterSendsNoQuery(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if len(cap.query) != 0 {
			t.Errorf("query = %v, want empty", cap.query)
		}
		writeJSONTest(w, http.StatusOK, `{"tasks":[],"total":0,"limit":20,"offset":0}`)
	}))
	if _, err := c.ListTasks(context.Background(), TaskFilter{}); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
}

// --- GetTask ----------------------------------------------------------------------------

const detailBody = `{
  "id": "T1",
  "title": "Fix login bug",
  "status": "doing",
  "priority": 3,
  "due_at": "2026-10-01T00:00:00Z",
  "assignee": { "type": "human", "name": "ada" },
  "claimed_by": { "type": "agent", "name": "runner-2" },
  "claim_expires_at": "2026-09-26T15:53:00Z",
  "version": 4,
  "created_at": "2026-09-25T18:02:11Z",
  "updated_at": "2026-09-26T15:48:03Z",
  "project_id": "P1",
  "description": "Users cannot log in after the password reset flow.",
  "notes": "Last run: migration half-applied, rolled back.",
  "archived": false,
  "blocked": true,
  "blockers": [ { "id": "T2", "title": "Fix DB migration", "status": "doing" } ],
  "claimed_at": "2026-09-26T15:48:03Z",
  "claim_ttl_seconds": 900,
  "acceptance_criteria": [ { "seq": 1, "text": "login works", "done": true, "note": "manual test passed", "completed_by": {"type":"human","name":"ada"}, "completed_at": "2026-09-26T15:40:12Z" } ],
  "comments": [ { "id": "C1", "kind": "progress", "body": "ran the build — green", "actor": {"type":"human","name":"ada"}, "created_at": "2026-09-26T15:47:55Z" } ],
  "artifacts": [ { "id": "A1", "kind": "url", "ref": "https://example.com/r.json", "summary": "deploy report", "actor": {"type":"agent","name":"runner-2"}, "created_at": "2026-09-26T15:48:00Z" } ],
  "relations": [ { "task_id": "T1", "other_task_id": "T2", "kind": "blocked-by", "created_at": "2026-09-26T15:49:30Z" } ],
  "events": [ { "id": "E1", "kind": "claimed", "actor": {"type":"agent","name":"runner-2"}, "after": "{\"claim\":\"fresh\"}", "at": "2026-09-26T15:48:03Z" } ]
}`

func TestGetTask_LimitsAndDecode(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodGet || cap.path != "/api/v1/tasks/T1" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		if cap.query["comment_limit"] != "5" || cap.query["event_limit"] != "7" {
			t.Errorf("query = %v, want comment_limit=5 event_limit=7", cap.query)
		}
		writeJSONTest(w, http.StatusOK, detailBody)
	}))

	d, err := c.GetTask(context.Background(), "T1", 5, 7)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"id", d.ID == "T1"},
		{"optional due_at set", d.DueAt != nil && *d.DueAt == "2026-10-01T00:00:00Z"},
		{"assignee", d.Assignee != nil && d.Assignee.Name == "ada" && d.Assignee.Type == "human"},
		{"claimed_by distinct from assignee", d.ClaimedBy != nil && d.ClaimedBy.Name == "runner-2" && d.ClaimedBy.Type == "agent"},
		{"version", d.Version == 4},
		{"blocked + blockers", d.Blocked && len(d.Blockers) == 1 && d.Blockers[0].ID == "T2"},
		{"claim lease detail", d.ClaimedAt != nil && d.ClaimTTLSeconds == 900},
		{"ac row", len(d.AcceptanceCriteria) == 1 && d.AcceptanceCriteria[0].Seq == 1 && d.AcceptanceCriteria[0].Done && d.AcceptanceCriteria[0].CompletedBy != nil},
		{"comment tail", len(d.Comments) == 1 && d.Comments[0].Kind == "progress"},
		{"artifact", len(d.Artifacts) == 1 && d.Artifacts[0].Ref == "https://example.com/r.json"},
		{"relation stored kind", len(d.Relations) == 1 && d.Relations[0].Kind == "blocked-by"},
		{"event tail", len(d.Events) == 1 && d.Events[0].After == `{"claim":"fresh"}`},
	}
	for _, c2 := range checks {
		if !c2.ok {
			t.Errorf("check failed: %s", c2.name)
		}
	}
}

func TestGetTask_ZeroLimitsSendNoQuery(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if len(cap.query) != 0 {
			t.Errorf("query = %v, want empty", cap.query)
		}
		writeJSONTest(w, http.StatusOK, detailBody)
	}))
	if _, err := c.GetTask(context.Background(), "T1", 0, 0); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
}

func TestGetTask_404PreservesBody(t *testing.T) {
	notFound := `{"error":"not found","resource":"task","id":"NOPE","message":"task \"NOPE\" does not exist — check the id (use the list endpoint)"}`
	c, _ := newTestServer(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONTest(w, http.StatusNotFound, notFound)
	}))

	_, err := c.GetTask(context.Background(), "NOPE", 0, 0)
	e := wantAPIError(t, err, http.StatusNotFound)
	if string(e.Body) != notFound {
		t.Errorf("body not verbatim:\n got %s\nwant %s", e.Body, notFound)
	}
}

// --- CreateTask ---------------------------------------------------------------------------

func TestCreateTask_BodyAndDecode(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req CreateTaskRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Title != "Fix login bug" || req.ProjectID != "P1" || req.Priority != 3 || len(req.AcceptanceCriteria) != 2 || req.Assignee == nil || req.DueAt != nil {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"task":{"id":"T9","title":"Fix login bug","status":"todo","priority":3,"version":1,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-25T18:02:11Z"},"possible_duplicates":[{"id":"T2","title":"fix login bug","status":"todo","priority":3,"version":2,"created_at":"2026-09-24T18:02:11Z","updated_at":"2026-09-25T18:02:11Z"}]}`)
	}))

	resp, err := c.CreateTask(context.Background(), CreateTaskRequest{
		Actor: Actor{Name: "eve"}, Title: "Fix login bug", ProjectID: "P1", Priority: 3,
		Assignee: &Actor{Type: "human", Name: "ada"}, AcceptanceCriteria: []string{"login works", "logout works"},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if resp.Task.ID != "T9" || len(resp.PossibleDuplicates) != 1 || resp.PossibleDuplicates[0].ID != "T2" {
		t.Errorf("response = %+v", resp)
	}
}

// --- UpdateTask -----------------------------------------------------------------------------

func TestUpdateTask_PointerSemantics(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPatch || cap.path != "/api/v1/tasks/T1" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var raw map[string]any
		if err := json.Unmarshal(cap.body, &raw); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		// Only the set fields may appear on the wire.
		for _, k := range []string{"title", "description", "priority", "due_at", "clear_due", "assignee", "clear_assignee", "archived"} {
			if _, present := raw[k]; present {
				t.Errorf("field %q must be omitted from the body (got %s)", k, cap.body)
			}
		}
		if v, _ := raw["status"].(string); v != "doing" {
			t.Errorf("status = %v, want doing", raw["status"])
		}
		if v, _ := raw["version"].(float64); int(v) != 3 {
			t.Errorf("version = %v, want 3", raw["version"])
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"version":5,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}`)
	}))

	status := "doing"
	row, err := c.UpdateTask(context.Background(), "T1", UpdateTaskRequest{Actor: Actor{Name: "eve"}, Version: 3, Status: &status})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if row.Version != 5 || row.Status != "doing" {
		t.Errorf("row = %+v", row)
	}
}

// --- ClaimTask -----------------------------------------------------------------------------

const claimConflictBody = `{
  "error": "claim conflict",
  "resource": "task",
  "id": "T1",
  "holder": { "type": "agent", "name": "runner-2" },
  "expires_at": "2026-09-26T15:53:00Z",
  "message": "\"T1\" is held by agent (runner-2) until 2026-09-26T15:53:00Z — wait for the lease to expire or work another task"
}`

func TestClaimTask_SuccessSendsActorAndTTL(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/claim" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		wantBody := `{"actor":{"type":"agent","name":"runner-2"},"ttl_sec":1800}`
		if string(cap.body) != wantBody {
			t.Errorf("body = %s, want %s", cap.body, wantBody)
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"claimed_by":{"type":"agent","name":"runner-2"},"claim_expires_at":"2026-09-26T16:18:03Z","version":5,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}`)
	}))

	row, err := c.ClaimTask(context.Background(), "T1", Actor{Type: "agent", Name: "runner-2"}, 1800)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if row.ClaimedBy == nil || row.ClaimedBy.Name != "runner-2" || row.ClaimExpiresAt == nil || row.Version != 5 {
		t.Errorf("row = %+v", row)
	}
}

func TestClaimTask_ZeroTTLIsOmitted(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if strings.Contains(string(cap.body), "ttl_sec") {
			t.Errorf("ttl_sec must be omitted for 0: %s", cap.body)
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"version":5,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}`)
	}))
	if _, err := c.ClaimTask(context.Background(), "T1", Actor{Name: "agent-x"}, 0); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
}

func TestClaimTask_ConflictBodyPreservedVerbatim(t *testing.T) {
	c, _ := newTestServer(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONTest(w, http.StatusConflict, claimConflictBody)
	}))

	_, err := c.ClaimTask(context.Background(), "T1", Actor{Type: "agent", Name: "runner-3"}, 0)
	e := wantAPIError(t, err, http.StatusConflict)
	if string(e.Body) != claimConflictBody {
		t.Errorf("conflict body not preserved verbatim:\n got %q\nwant %q", e.Body, claimConflictBody)
	}
	if !strings.Contains(e.Error(), "409") || !strings.Contains(e.Error(), "claim conflict") {
		t.Errorf("Error() should carry status + body: %v", err)
	}
}

// --- ReleaseTask / HeartbeatTask ---------------------------------------------------------------

func TestReleaseTask_NoteOptional(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/release" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ReleaseTaskRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Actor.Name != "runner-2" || req.Note != "stalled on the DB migration" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"todo","priority":3,"version":6,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:59:00Z"}`)
	}))

	row, err := c.ReleaseTask(context.Background(), "T1", Actor{Type: "agent", Name: "runner-2"}, "stalled on the DB migration")
	if err != nil {
		t.Fatalf("ReleaseTask: %v", err)
	}
	if row.Status != "todo" || row.Version != 6 {
		t.Errorf("row = %+v", row)
	}
}

func TestHeartbeatTask_ActorOnly(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/heartbeat" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req HeartbeatRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Actor.Name != "runner-2" || strings.Contains(string(cap.body), "ttl") {
			t.Errorf("request = %+v body=%s", req, cap.body)
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"claimed_by":{"type":"agent","name":"runner-2"},"claim_expires_at":"2026-09-26T16:58:00Z","version":5,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}`)
	}))

	if _, err := c.HeartbeatTask(context.Background(), "T1", Actor{Type: "agent", Name: "runner-2"}); err != nil {
		t.Fatalf("HeartbeatTask: %v", err)
	}
}

// --- AddComment / UpdateNotes / CompleteAC / AttachArtifact -----------------------------------

func TestAddComment_201AndBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/comments" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req CommentRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Kind != "progress" || req.Body == "" || req.Actor.Name != "ada" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"id":"C2","kind":"progress","body":"ran the build — green","actor":{"type":"human","name":"ada"},"created_at":"2026-09-26T15:47:55Z"}`)
	}))

	cm, err := c.AddComment(context.Background(), "T1", CommentRequest{Actor: Actor{Name: "ada"}, Kind: "progress", Body: "ran the build — green"})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if cm.ID != "C2" || cm.Actor.Type != "human" {
		t.Errorf("comment = %+v", cm)
	}
}

func TestUpdateNotes_ReplaceBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPut || cap.path != "/api/v1/tasks/T1/notes" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req NotesRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Notes != "Last run: half-applied.\nNext: verify tests." {
			t.Errorf("notes = %q", req.Notes)
		}
		if strings.Contains(string(cap.body), "version") {
			t.Errorf("notes body must NOT carry a version field: %s", cap.body)
		}
		writeJSONTest(w, http.StatusOK, `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"version":7,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T16:01:00Z"}`)
	}))

	row, err := c.UpdateNotes(context.Background(), "T1", Actor{Name: "eve"}, "Last run: half-applied.\nNext: verify tests.")
	if err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	if row.Version != 7 {
		t.Errorf("row = %+v", row)
	}
}

func TestCompleteAC_PathAndBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPatch || cap.path != "/api/v1/tasks/T1/acceptance-criteria/2" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ACToggleRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if !req.Done || req.Note != "manual test passed" || req.Actor.Name != "ada" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusOK, `{"seq":2,"text":"logout works","done":true,"note":"manual test passed","completed_by":{"type":"human","name":"ada"},"completed_at":"2026-09-26T15:40:12Z"}`)
	}))

	ac, err := c.CompleteAC(context.Background(), "T1", 2, ACToggleRequest{Actor: Actor{Name: "ada"}, Done: true, Note: "manual test passed"})
	if err != nil {
		t.Fatalf("CompleteAC: %v", err)
	}
	if ac.Seq != 2 || !ac.Done || ac.CompletedBy == nil {
		t.Errorf("ac = %+v", ac)
	}
}

func TestAddAC_PathAndBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/acceptance-criteria" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ACAddRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Text != "verify password length" || req.Actor.Name != "runner-2" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"seq":3,"text":"verify password length","done":false}`)
	}))

	ac, err := c.AddAC(context.Background(), "T1", ACAddRequest{Actor: Actor{Type: "agent", Name: "runner-2"}, Text: "verify password length"})
	if err != nil {
		t.Fatalf("AddAC: %v", err)
	}
	if ac.Seq != 3 || ac.Text != "verify password length" || ac.Done {
		t.Errorf("ac = %+v", ac)
	}
}

func TestRemoveAC_PathAndBody_204(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodDelete || cap.path != "/api/v1/tasks/T1/acceptance-criteria/2" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ACRemoveRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Actor.Name != "runner-2" {
			t.Errorf("request = %+v", req)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	err := c.RemoveAC(context.Background(), "T1", 2, Actor{Type: "agent", Name: "runner-2"})
	if err != nil {
		t.Fatalf("RemoveAC: %v", err)
	}
}

func TestEditACText_PathAndBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPatch || cap.path != "/api/v1/tasks/T1/acceptance-criteria/2" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ACEditTextRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Text != "logout works safely" || req.Actor.Name != "ada" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusOK, `{"seq":2,"text":"logout works safely","done":false}`)
	}))

	ac, err := c.EditACText(context.Background(), "T1", 2, ACEditTextRequest{Actor: Actor{Name: "ada"}, Text: "logout works safely"})
	if err != nil {
		t.Fatalf("EditACText: %v", err)
	}
	if ac.Seq != 2 || ac.Text != "logout works safely" {
		t.Errorf("ac = %+v", ac)
	}
}

func TestAttachArtifact_201AndBody(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/artifacts" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req ArtifactRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.Kind != "url" || req.Ref != "https://example.com/reports/deploy.json" || req.Summary == "" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"id":"A9","kind":"url","ref":"https://example.com/reports/deploy.json","summary":"deploy report for the fix","actor":{"type":"agent","name":"runner-2"},"created_at":"2026-09-26T15:48:00Z"}`)
	}))

	a, err := c.AttachArtifact(context.Background(), "T1", ArtifactRequest{Actor: Actor{Type: "agent", Name: "runner-2"}, Kind: "url", Ref: "https://example.com/reports/deploy.json", Summary: "deploy report for the fix"})
	if err != nil {
		t.Fatalf("AttachArtifact: %v", err)
	}
	if a.ID != "A9" || a.Summary == "" {
		t.Errorf("artifact = %+v", a)
	}
}

func TestUploadArtifact_201AndMultipart(t *testing.T) {
	c, _ := newTestServer(t, "test-token", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/artifacts" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		ct := cap.header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("got Content-Type %s", ct)
		}
		auth := cap.header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("got Authorization %s", auth)
		}
		writeJSONTest(w, http.StatusCreated, `{"id":"AUPLOAD","kind":"screenshot","ref":"/artifacts/AUPLOAD","summary":"my screenshot","actor":{"type":"agent","name":"runner-2"},"created_at":"2026-09-26T15:48:00Z"}`)
	}))

	fileContent := strings.NewReader("fake-png-bytes")
	a, err := c.UploadArtifact(context.Background(), "T1", "screen.png", fileContent, "screenshot", "my screenshot", Actor{Type: "agent", Name: "runner-2"})
	if err != nil {
		t.Fatalf("UploadArtifact: %v", err)
	}
	if a.ID != "AUPLOAD" || a.Ref != "/artifacts/AUPLOAD" || a.Summary != "my screenshot" {
		t.Errorf("artifact = %+v", a)
	}
}

// --- RelateTasks / UnrelateTasks -----------------------------------------------------------------

func TestRelateTasks_BodyDecode(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodPost || cap.path != "/api/v1/tasks/T1/relations" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req RelationRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, cap.body)
		}
		if req.OtherID != "T2" || req.Kind != "blocked-by" || req.Actor.Name != "eve" {
			t.Errorf("request = %+v", req)
		}
		writeJSONTest(w, http.StatusCreated, `{"task_id":"T1","other_task_id":"T2","kind":"blocked-by","created_at":"2026-09-26T15:49:30Z"}`)
	}))

	r, err := c.RelateTasks(context.Background(), "T1", RelationRequest{Actor: Actor{Name: "eve"}, OtherID: "T2", Kind: "blocked-by"})
	if err != nil {
		t.Fatalf("RelateTasks: %v", err)
	}
	if r.Kind != "blocked-by" || r.OtherTaskID != "T2" {
		t.Errorf("relation = %+v", r)
	}
}

func TestUnrelateTasks_DeleteWithActorBody_204(t *testing.T) {
	c, _ := newTestServer(t, "", capturing(t, func(cap *capture, w http.ResponseWriter) {
		if cap.method != http.MethodDelete || cap.path != "/api/v1/tasks/T1/relations/T2/blocked-by" {
			t.Errorf("got %s %s", cap.method, cap.path)
		}
		var req UnrelateTaskRequest
		if err := json.Unmarshal(cap.body, &req); err != nil {
			t.Fatalf("delete body not JSON: %v (%q)", err, cap.body)
		}
		if req.Actor.Name != "eve" || req.Actor.Type != "human" {
			t.Errorf("actor = %+v", req.Actor)
		}
		w.WriteHeader(http.StatusNoContent) // 204, no body
	}))

	if err := c.UnrelateTasks(context.Background(), "T1", Actor{Type: "human", Name: "eve"}, "T2", "blocked-by"); err != nil {
		t.Fatalf("UnrelateTasks: %v", err)
	}
}

// --- Health -------------------------------------------------------------------------------------

func TestHealth_OK(t *testing.T) {
	c, _ := newTestServer(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" { // outside /api/v1
			t.Errorf("path = %s, want /healthz", r.URL.Path)
		}
		writeJSONTest(w, http.StatusOK, `{"status":"ok","db":"ok"}`)
	}))
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" || h.DB != "ok" {
		t.Errorf("health = %+v", h)
	}
}

func TestHealth_DegradedIsAPIErrorWithVerbatimBody(t *testing.T) {
	degraded := `{"status":"degraded","db":"error: no such table: task"}`
	c, _ := newTestServer(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONTest(w, http.StatusServiceUnavailable, degraded)
	}))
	_, err := c.Health(context.Background())
	e := wantAPIError(t, err, http.StatusServiceUnavailable)
	if string(e.Body) != degraded {
		t.Errorf("body not verbatim: %q", e.Body)
	}
}

// --- transport failures -------------------------------------------------------------------------

func TestTransportFailureIsNotAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := New(srv.URL, "")
	srv.Close() // deterministic: the port is now refusing connections

	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
	if _, ok := AsAPIError(err); ok {
		t.Errorf("transport failure must not be an *APIError: %v", err)
	}
}

func TestUpstream500Preserved(t *testing.T) {
	body := `{"error":"internal","message":"unexpected server error"}`
	c, _ := newTestServer(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONTest(w, http.StatusInternalServerError, body)
	}))
	_, err := c.ListProjects(context.Background())
	e := wantAPIError(t, err, http.StatusInternalServerError)
	if string(e.Body) != body {
		t.Errorf("body not verbatim: %q", e.Body)
	}
}
