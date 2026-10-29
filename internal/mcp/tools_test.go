package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// --- deterministic mock upstream (simulates ravn-server; fixed data, no sleeps) ---

const mockProjectsBody = `{"projects":[{"id":"P1","name":"Website Redesign","description":"MVP landing page work","created_at":"2026-09-20T09:00:00Z","updated_at":"2026-09-20T09:00:00Z","task_counts":{"todo":3,"doing":1,"done":5}},{"id":"P2","name":"Agent Ops","created_at":"2026-09-19T09:00:00Z","updated_at":"2026-09-19T09:00:00Z","task_counts":{"todo":0,"doing":0,"done":0}}],"total":2}`

// The single GET /api/v1/projects/P1 row: like every bare get/create response,
// it carries NO task_counts (those ride only on list rows).
const mockGetProjectBody = `{"id":"P1","name":"Website Redesign","description":"MVP landing page work","created_at":"2026-09-20T09:00:00Z","updated_at":"2026-09-20T09:00:00Z"}`

const mockDetailBody = `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"due_at":"2026-10-01T00:00:00Z","assignee":{"type":"human","name":"ada"},"claimed_by":{"type":"agent","name":"runner-2"},"claim_expires_at":"2026-09-26T15:53:00Z","version":4,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z","project_id":"P1","description":"Users cannot log in after the password reset flow.","notes":"Last run: migration half-applied, rolled back.","archived":false,"blocked":true,"blockers":[{"id":"T2","title":"Fix DB migration","status":"doing"}],"claimed_at":"2026-09-26T15:48:03Z","claim_ttl_seconds":900,"acceptance_criteria":[{"seq":1,"text":"login works","done":true,"note":"manual test passed","completed_by":{"type":"human","name":"ada"},"completed_at":"2026-09-26T15:40:12Z"}],"comments":[{"id":"C1","kind":"progress","body":"ran the build — green","actor":{"type":"human","name":"ada"},"created_at":"2026-09-26T15:47:55Z"}],"artifacts":[],"relations":[{"task_id":"T1","other_task_id":"T2","kind":"blocked-by","created_at":"2026-09-26T15:49:30Z"}],"events":[{"id":"E1","kind":"claimed","actor":{"type":"agent","name":"runner-2"},"after":"{\"claim\":\"fresh\"}","at":"2026-09-26T15:48:03Z"}]}`

// The exact 409 claim-conflict body a foreign active lease produces (API.md).
// Compared byte-for-byte against the isError text to prove VERBATIM propagation.
const mockConflictBody = `{"error":"claim conflict","resource":"task","id":"HOLDING","holder":{"type":"agent","name":"runner-2"},"expires_at":"2026-09-26T15:53:00Z","message":"\"HOLDING\" is held by agent (runner-2) until 2026-09-26T15:53:00Z — wait for the lease to expire or work another task"}`

// The summary row of T1 at its stored version 4 (matches mockDetailBody's row).
const mockTaskRowV4 = `{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"due_at":"2026-10-01T00:00:00Z","version":4,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}`

// The exact 409 version-conflict body (API.md) a stale PATCH produces:
// current_version + fresh "current" row so the agent can rebase without re-reading.
const mockVersionConflictBody = `{"error":"version_conflict","resource":"task","id":"T1","current_version":4,"updated_at":"2026-09-26T15:48:03Z","message":"task \"T1\" is at version 4 — re-read it and retry with that version","current":{"id":"T1","title":"Fix login bug","status":"doing","priority":3,"due_at":"2026-10-01T00:00:00Z","version":4,"created_at":"2026-09-25T18:02:11Z","updated_at":"2026-09-26T15:48:03Z"}}`

// The exact 409 parent-conflict body (API.md): a second, different parent.
const mockParentConflictBody = `{"error":"parent conflict","resource":"task","id":"TPARENT","message":"task already has a parent: task TPARENT already has a parent (TOTHER)"}`

type claimCall struct {
	path      string
	body      string
	authorize string
}

// capturedCall records EVERY /api/v1 request the MCP stack sent upstream:
// method+path prove routing, query proves filter pass-through, body proves arg
// decoding. (The apiclient unit tests already prove per-method wire behavior;
// these assertions are tool-level wiring only.)
type capturedCall struct {
	method string
	path   string
	query  url.Values
	body   string
	auth   string
}

// newMockUpstream stands up a fake ravn-server. Every route answers with
// fixed data:
//   - GET /api/v1/projects/{id} answers with the fixed P1 row for id "P1" and
//     a 404 body verbatim for anything else; PUT /api/v1/projects/{id} applies
//     the partial update (pointer fields) onto the base row.
//   - POST /tasks/{id}/claim returns the conflict body verbatim for id "HOLDING"
//     and echoes the requesting actor as claimed_by otherwise (and records every
//     claim call).
//   - PATCH /tasks/{id} treats version 4 as current: any other version → 409
//     version_conflict verbatim; version 4 → fresh row at version 5.
//   - POST /tasks/{id}/relations normalizes kind "parent" to the inverse child
//     edge (server rule) and 409s on a second parent for id "TPARENT".
func newMockUpstream(t *testing.T) (*httptest.Server, *[]claimCall, *[]capturedCall) {
	t.Helper()

	var claims []claimCall
	var calls []capturedCall
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	}

	// record drains the request body, captures it for assertions, and enforces
	// Bearer auth on every /api/v1 route.
	record := func(r *http.Request) string {
		var b []byte
		if r.Body != nil {
			b, _ = io.ReadAll(r.Body)
		}
		calls = append(calls, capturedCall{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			body:   string(b),
			auth:   r.Header.Get("Authorization"),
		})
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("upstream: %s %s Authorization = %q, want Bearer secret-token", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		return string(b)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok","db":"ok"}`)
	})
	mux.HandleFunc("GET /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, mockProjectsBody)
	})
	// GET /api/v1/projects/{id}/report returns a fixed digest; the forwarded
	// query params are asserted from the captured call (the apiclient only
	// sends non-defaults, so their ABSENCE is what a defaults call must show).
	mux.HandleFunc("GET /api/v1/projects/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		row := map[string]any{
			"project_id":      r.PathValue("id"),
			"counts":          map[string]any{"by_status": map[string]int{"todo": 1, "doing": 1, "done": 1}, "by_priority": map[string]int{"0": 3}},
			"active_claims":   []map[string]any{{"id": "T1", "title": "Live", "claimed_by": map[string]string{"type": "agent", "name": "a"}, "claim_expires_at": "2026-09-26T17:48:03Z", "remaining_sec": 600}},
			"expired_claims":  []map[string]any{{"id": "T2", "title": "Zombie"}},
			"stalled_tasks":   []map[string]any{{"id": "T3", "title": "Stuck", "last_activity_at": "2026-09-26T14:48:03Z", "idle_minutes": 180}},
			"bottlenecks":     []map[string]any{{"id": "T2", "title": "Zombie", "blocks": 1}},
			"open_blocked_by": []map[string]any{{"blocker": map[string]string{"id": "T2", "title": "Zombie"}, "blocked": []map[string]any{{"id": "T3", "title": "Stuck"}}}},
			"recently_completed": []map[string]any{{"id": "T4", "title": "Shipped", "completed_at": "2026-09-26T15:48:03Z"}},
			"workload":          map[string]int{"a": 1, "": 2},
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.CreateProjectRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /projects body not JSON: %v (%s)", err, b)
		}
		row := map[string]any{
			"id": "PNEW", "name": req.Name,
			"created_at": "2026-09-26T15:48:03Z", "updated_at": "2026-09-26T15:48:03Z",
		}
		if req.Description != "" {
			row["description"] = req.Description
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("GET /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") != "P1" {
			writeJSON(w, http.StatusNotFound, `{"error":"not found","resource":"project","id":`+jsonString(r.PathValue("id"))+`,"message":"unknown project"}`)
			return
		}
		writeJSON(w, http.StatusOK, mockGetProjectBody)
	})
	mux.HandleFunc("PUT /api/v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.UpdateProjectRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: PUT /projects body not JSON: %v (%s)", err, b)
			return
		}
		row := map[string]any{ // the partial update applied onto the stored P1 row
			"id": r.PathValue("id"), "name": "Website Redesign",
			"created_at": "2026-09-20T09:00:00Z", "updated_at": "2026-09-26T16:03:00Z",
		}
		if req.Name != nil {
			row["name"] = *req.Name
		}
		if req.Description != nil {
			row["description"] = *req.Description
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		q := r.URL.Query()
		limit, offset := q.Get("limit"), q.Get("offset")
		if limit == "" {
			limit = "20"
		}
		if offset == "" {
			offset = "0"
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(
			`{"tasks":[%s],"total":1,"limit":%s,"offset":%s}`, mockTaskRowV4, limit, offset))
	})
	mux.HandleFunc("POST /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.CreateTaskRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /tasks body not JSON: %v (%s)", err, b)
		}
		task := map[string]any{
			"id": "TNEW", "title": req.Title, "status": "todo", "priority": req.Priority,
			"version": 1, "created_at": "2026-09-26T15:48:03Z", "updated_at": "2026-09-26T15:48:03Z",
		}
		if req.DueAt != nil {
			task["due_at"] = *req.DueAt
		}
		if req.Assignee != nil {
			task["assignee"] = req.Assignee
		}
		resp := map[string]any{ // advisory duplicates: never an error (API.md)
			"task": task,
			"possible_duplicates": []map[string]any{{
				"id": "TDUP", "title": strings.ToLower(req.Title) + " (dup)", "status": "todo",
				"priority": req.Priority, "version": 2,
				"created_at": "2026-09-20T09:00:00Z", "updated_at": "2026-09-20T09:00:00Z",
			}},
		}
		data, _ := json.Marshal(resp)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.PathValue("id") != "T1" {
			writeJSON(w, http.StatusNotFound, `{"error":"not found","resource":"task","id":`+jsonString(r.PathValue("id"))+`,"message":"unknown task"}`)
			return
		}
		if q := r.URL.Query().Get("comment_limit"); q != "" && q != "5" {
			t.Errorf("upstream: comment_limit = %q, want 5", q)
		}
		writeJSON(w, http.StatusOK, mockDetailBody)
	})
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: PATCH /tasks body not JSON: %v (%s)", err, b)
		}
		if req.Version != 4 { // the mock's stored version is 4 (mockDetailBody)
			writeJSON(w, http.StatusConflict, mockVersionConflictBody)
			return
		}
		row := map[string]any{
			"id": r.PathValue("id"), "title": "Fix login bug", "status": "doing", "priority": 3,
			"version": 5, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T16:00:00Z",
		}
		if req.Title != nil {
			row["title"] = *req.Title
		}
		if req.Status != nil {
			row["status"] = *req.Status
		}
		if req.Priority != nil {
			row["priority"] = *req.Priority
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		id := r.PathValue("id")
		claims = append(claims, claimCall{path: r.URL.Path, body: string(b), authorize: r.Header.Get("Authorization")})
		if id == "HOLDING" {
			writeJSON(w, http.StatusConflict, mockConflictBody)
			return
		}
		var req struct {
			Actor apiclient.Actor `json:"actor"`
		}
		_ = json.Unmarshal([]byte(b), &req)
		summary := map[string]any{
			"id": id, "title": "Fix login bug", "status": "doing", "priority": 3,
			"version": 5, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T15:48:03Z",
			"claimed_by":       map[string]string{"type": req.Actor.Type, "name": req.Actor.Name},
			"claim_expires_at": "2026-09-26T16:18:03Z",
		}
		data, _ := json.Marshal(summary)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.ReleaseTaskRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /release body not JSON: %v (%s)", err, b)
		}
		row := map[string]any{ // lease cleared, status → todo (API.md)
			"id": r.PathValue("id"), "title": "Fix login bug", "status": "todo", "priority": 3,
			"version": 6, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T16:01:00Z",
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.HeartbeatRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /heartbeat body not JSON: %v (%s)", err, b)
		}
		row := map[string]any{ // lease extended; version and updated_at untouched (API.md)
			"id": r.PathValue("id"), "title": "Fix login bug", "status": "doing", "priority": 3,
			"version": 4, "claimed_by": req.Actor, "claim_expires_at": "2026-09-26T17:00:00Z",
			"created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T15:48:03Z",
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/handoff", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		id := r.PathValue("id")
		if id == "HOLDING" {
			writeJSON(w, http.StatusConflict, mockConflictBody) // foreign active lease → verbatim 409
			return
		}
		var req apiclient.HandoffRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /handoff body not JSON: %v (%s)", err, b)
			return
		}
		out := map[string]any{
			"task":    map[string]any{"id": id, "title": "Fix login bug", "status": "todo", "priority": 3, "version": 6, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T16:20:00Z"},
			"comment": map[string]any{"id": "CH", "kind": req.Kind, "body": req.Body, "actor": req.Actor, "created_at": "2026-09-26T15:50:00Z"},
		}
		data, _ := json.Marshal(out)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.CommentRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /comments body not JSON: %v (%s)", err, b)
		}
		row := map[string]any{
			"id": "CNEW", "kind": req.Kind, "body": req.Body,
			"actor": req.Actor, "created_at": "2026-09-26T15:50:00Z",
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("PUT /api/v1/tasks/{id}/notes", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.NotesRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: PUT /notes body not JSON: %v (%s)", err, b)
		}
		row := map[string]any{ // a real notes change bumps the version (API.md)
			"id": r.PathValue("id"), "title": "Fix login bug", "status": "doing", "priority": 3,
			"version": 6, "created_at": "2026-09-25T18:02:11Z", "updated_at": "2026-09-26T16:02:00Z",
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/acceptance-criteria", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.ACAddRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /acceptance-criteria body not JSON: %v (%s)", err, b)
		}
		row := apiclient.AcceptanceCriterion{Seq: 2, Text: req.Text, Done: false}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/acceptance-criteria/{seq}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH /api/v1/tasks/{id}/acceptance-criteria/{seq}", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.ACToggleRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: PATCH /acceptance-criteria body not JSON: %v (%s)", err, b)
		}
		seq, err := strconv.Atoi(r.PathValue("seq"))
		if err != nil {
			t.Errorf("upstream: seq path value = %q, want number", r.PathValue("seq"))
		}
		text := "login works"
		if req.Text != nil {
			text = *req.Text
		}
		row := apiclient.AcceptanceCriterion{Seq: seq, Text: text, Done: req.Done, Note: req.Note}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusOK, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		b := record(r)
		if strings.HasPrefix(ct, "multipart/form-data") {
			r.Body = io.NopCloser(strings.NewReader(b))
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				t.Errorf("upstream: ParseMultipartForm error: %v", err)
			}
			kind := r.FormValue("kind")
			summary := r.FormValue("summary")
			actorType := r.FormValue("actor_type")
			actorName := r.FormValue("actor_name")
			row := apiclient.Artifact{
				ID:        "AUPLOAD",
				Kind:      kind,
				Ref:       "/artifacts/AUPLOAD",
				Summary:   summary,
				Actor:     apiclient.Actor{Type: actorType, Name: actorName},
				CreatedAt: "2026-09-26T15:52:00Z",
			}
			data, _ := json.Marshal(row)
			writeJSON(w, http.StatusCreated, string(data))
			return
		}
		var req apiclient.ArtifactRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /artifacts body not JSON: %v (%s)", err, b)
		}
		row := apiclient.Artifact{ID: "ANEW", Kind: req.Kind, Ref: req.Ref, Summary: req.Summary, Actor: req.Actor, CreatedAt: "2026-09-26T15:52:00Z"}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/relations", func(w http.ResponseWriter, r *http.Request) {
		b := record(r)
		var req apiclient.RelationRequest
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Errorf("upstream: POST /relations body not JSON: %v (%s)", err, b)
		}
		id := r.PathValue("id")
		row := map[string]any{
			"task_id": id, "other_task_id": req.OtherID, "kind": req.Kind,
			"created_at": "2026-09-26T15:49:30Z",
		}
		switch {
		case req.Kind == "parent" && id == "TPARENT": // second different parent → 409 (API.md)
			writeJSON(w, http.StatusConflict, mockParentConflictBody)
			return
		case req.Kind == "parent": // input alias: stored as the inverse child edge
			row["task_id"], row["other_task_id"], row["kind"] = req.OtherID, id, "child"
		}
		data, _ := json.Marshal(row)
		writeJSON(w, http.StatusCreated, string(data))
	})
	mux.HandleFunc("DELETE /api/v1/tasks/{id}/relations/{other_id}/{kind}", func(w http.ResponseWriter, r *http.Request) {
		record(r) // body carries the actor (removal event attribution)
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &claims, &calls
}

func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// --- helpers ---------------------------------------------------------------------------

var wantToolNames = []string{
	"list_projects", "create_project", "get_project", "update_project", "list_tasks", "get_task", "project_report", "create_task",
	"update_task", "claim_task", "release_task", "heartbeat_task", "handoff_task", "add_comment",
	"update_notes", "complete_ac", "add_ac", "remove_ac", "edit_ac", "attach_artifact", "relate_tasks",
}

func startMCPStack(t *testing.T, upstreamURL string) *httptest.Server {
	return startMCPStackWithToken(t, upstreamURL, "secret-token")
}

func startMCPStackWithToken(t *testing.T, upstreamURL, token string) *httptest.Server {
	t.Helper()
	agentName := os.Getenv("RAVN_AGENT_NAME")
	if agentName == "" {
		agentName = "default-agent"
	}
	api := apiclient.New(upstreamURL, token)
	mcs := mcpsrv.NewMCPServer("ravn", "test-version", ServerOptions()...)
	RegisterTools(mcs, api, apiclient.Actor{Type: "agent", Name: agentName})
	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true),
		mcpsrv.WithEndpointPath("/mcp"),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.HandleFunc("GET /healthz", HealthzHandler(api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, mcpURL string, ctx context.Context) *mcpclient.Client {
	t.Helper()
	tr, err := mcptransport.NewStreamableHTTP(mcpURL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHTTP: %v", err)
	}
	c := mcpclient.NewClient(tr)
	initRes, err := c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: "ravn-mcp-test", Version: "0"},
	}})
	if err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	if initRes.ServerInfo.Name != "ravn" {
		t.Fatalf("MCP server name = %q, want %q", initRes.ServerInfo.Name, "ravn")
	}
	return c
}

func callTool(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	res, err := c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	return res
}

func firstText(t *testing.T, res *mcpgo.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want TextContent", res.Content[0])
	}
	return tc.Text
}

func asMap(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("result text is not a JSON object: %v\n%s", err, text)
	}
	return m
}

// findCall locates one captured upstream call; fatal if the tool never routed
// there — the routing assertion that matters at the tool level.
func findCall(t *testing.T, calls []capturedCall, method, path string) capturedCall {
	t.Helper()
	for _, cc := range calls {
		if cc.method == method && cc.path == path {
			return cc
		}
	}
	t.Fatalf("no captured %s %s (upstream saw: %+v)", method, path, calls)
	return capturedCall{}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

// --- the round trip ----------------------------------------------------------------------

func TestStreamableHTTPRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, claims, _ := newMockUpstream(t)
	mcpSrv := startMCPStack(t, upstream.URL)

	c := dial(t, mcpSrv.URL, ctx)
	defer func() { _ = c.Close() }()

	// tools/list shows exactly the ravn tools in wantToolNames (no extras).
	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(list.Tools) != len(wantToolNames) {
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
		}
		t.Fatalf("tools/list returned %d tools (%v), want %d (%v)", len(list.Tools), names, len(wantToolNames), wantToolNames)
	}
	seen := make(map[string]bool, len(list.Tools))
	for _, tool := range list.Tools {
		seen[tool.Name] = true
	}
	for _, name := range wantToolNames {
		if !seen[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}

	// attach_artifact encourages (but never requires) testing-artifact uploads
	// so humans can verify agent work — the wording is part of the contract.
	for _, tool := range list.Tools {
		if tool.Name == "attach_artifact" {
			for _, sub := range []string{"RECOMMENDED for verification", "encouraged, never required"} {
				if !strings.Contains(tool.Description, sub) {
					t.Errorf("attach_artifact description missing %q: %s", sub, tool.Description)
				}
			}
		}
	}

	// list_tasks exposes the full GET /tasks filter surface as declared input-
	// schema properties (functional parity with the REST query params).
	for _, tool := range list.Tools {
		if tool.Name != "list_tasks" {
			continue
		}
		props := tool.InputSchema.Properties
		for _, key := range []string{"archived", "assignee_type", "created_after", "created_before", "completed_before",
			"parent_id", "claimed", "claimed_by", "expired_claim"} {
			if _, ok := props[key]; !ok {
				t.Errorf("list_tasks input schema missing property %q", key)
			}
		}
		archived, _ := props["archived"].(map[string]any)
		if archived == nil || archived["type"] != "boolean" {
			t.Errorf("list_tasks \"archived\" property = %v, want type boolean", archived)
		}
		for _, key := range []string{"claimed", "expired_claim"} {
			prop, _ := props[key].(map[string]any)
			if prop == nil || prop["type"] != "boolean" {
				t.Errorf("list_tasks %q property = %v, want type boolean", key, prop)
			}
		}
		atType, _ := props["assignee_type"].(map[string]any)
		enums, _ := atType["enum"].([]any)
		if atType == nil || len(enums) != 2 {
			t.Errorf("list_tasks \"assignee_type\" property = %v, want enum [human agent]", atType)
		}
	}

	// get_task returns the mock's resume payload (JSON-equivalent).
	res, err := c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": "T1", "comment_limit": 5},
	}})
	if err != nil {
		t.Fatalf("tools/call get_task: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_task returned isError: %s", firstText(t, res))
	}
	gotDetail := asMap(t, firstText(t, res))
	wantDetail := asMap(t, mockDetailBody)
	if !reflect.DeepEqual(gotDetail, wantDetail) {
		t.Errorf("get_task payload mismatch:\n got  %s\n want %s", mustJSON(t, gotDetail), mockDetailBody)
	}

	// claim_task success: actor passes through; Bearer token is forwarded upstream.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name: "claim_task",
		Arguments: map[string]any{
			"id":      "T1",
			"actor":   map[string]any{"name": "runner-2", "type": "agent"},
			"ttl_sec": 1800,
		},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task: %v", err)
	}
	if res.IsError {
		t.Fatalf("claim_task returned isError: %s", firstText(t, res))
	}
	claimed := asMap(t, firstText(t, res))
	holder, _ := claimed["claimed_by"].(map[string]any)
	if holder == nil || holder["name"] != "runner-2" || holder["type"] != "agent" {
		t.Errorf("claimed_by = %v, want agent runner-2", claimed["claimed_by"])
	}

	// claim_task WITHOUT actor → container default used; id HOLDING → 409 whose
	// body must arrive VERBATIM as the isError text (the recovery instruction).
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "claim_task",
		Arguments: map[string]any{"id": "HOLDING"},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task (conflict): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected isError for foreign lease, got success: %s", firstText(t, res))
	}
	if text := firstText(t, res); text != mockConflictBody {
		t.Errorf("conflict body not verbatim:\n got  %s\n want %s", text, mockConflictBody)
	}

	// Verify what the MCP server actually sent upstream for each claim.
	if len(*claims) != 2 {
		t.Fatalf("upstream saw %d claims, want 2: %+v", len(*claims), *claims)
	}
	explicit := (*claims)[0]
	if explicit.authorize != "Bearer secret-token" {
		t.Errorf("claim request Authorization = %q, want Bearer secret-token", explicit.authorize)
	}
	var exp struct {
		Actor  apiclient.Actor `json:"actor"`
		TTLSec *int            `json:"ttl_sec"`
	}
	if err := json.Unmarshal([]byte(explicit.body), &exp); err != nil {
		t.Fatalf("claim body not JSON: %v (%s)", err, explicit.body)
	}
	if exp.Actor.Name != "runner-2" || exp.Actor.Type != "agent" || exp.TTLSec == nil || *exp.TTLSec != 1800 {
		t.Errorf("explicit claim body = %s", explicit.body)
	}

	fallback := (*claims)[1]
	var fb struct {
		Actor apiclient.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte(fallback.body), &fb); err != nil {
		t.Fatalf("fallback claim body not JSON: %v (%s)", err, fallback.body)
	}
	if fb.Actor.Name != "default-agent" || fb.Actor.Type != "agent" {
		t.Errorf("fallback actor = %+v, want agent default-agent (RAVN_AGENT_NAME semantics)", fb.Actor)
	}

	// list_projects returns the projects+counts envelope.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: "list_projects"}})
	if err != nil {
		t.Fatalf("tools/call list_projects: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_projects returned isError: %s", firstText(t, res))
	}
	projects := asMap(t, firstText(t, res))
	rows, _ := projects["projects"].([]any)
	total, _ := projects["total"].(float64)
	if len(rows) != 2 || int(total) != 2 {
		t.Errorf("list_projects = %s", firstText(t, res))
	}

	// Malformed args (id as a number) → input-schema validation surfaces the
	// failure as an isError TOOL result (SEP-1303), not a protocol error.
	res, err = c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "claim_task",
		Arguments: map[string]any{"id": 42},
	}})
	if err != nil {
		t.Fatalf("tools/call claim_task (bad arg type): %v", err)
	}
	if !res.IsError {
		t.Errorf("expected input-schema validation failure as isError, got success: %s", firstText(t, res))
	}
}

// --- slice 4b tool round-trips -------------------------------------------------------------

func TestCreateProjectRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "create_project", map[string]any{
		"actor":       map[string]any{"name": "ada", "type": "human"},
		"name":        "Website Redesign",
		"description": "MVP landing page work",
	})
	if res.IsError {
		t.Fatalf("create_project returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["id"] != "PNEW" || row["name"] != "Website Redesign" || row["description"] != "MVP landing page work" {
		t.Errorf("create_project result = %s", firstText(t, res))
	}

	cc := findCall(t, *calls, http.MethodPost, "/api/v1/projects")
	var sent apiclient.CreateProjectRequest
	if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
		t.Fatalf("create_project upstream body not JSON: %v (%s)", err, cc.body)
	}
	if sent.Actor.Name != "ada" || sent.Actor.Type != "human" || sent.Name != "Website Redesign" || sent.Description != "MVP landing page work" {
		t.Errorf("create_project upstream body = %s", cc.body)
	}
}

func TestGetProjectRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "get_project", map[string]any{"id": "P1"})
	if res.IsError {
		t.Fatalf("get_project returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["id"] != "P1" || row["name"] != "Website Redesign" || row["description"] != "MVP landing page work" {
		t.Errorf("get_project result = %s", firstText(t, res))
	}
	findCall(t, *calls, http.MethodGet, "/api/v1/projects/P1") // routing; record() enforces Bearer auth

	// Unknown id → the 404 body arrives verbatim (its message IS the recovery hint).
	res = callTool(t, c, ctx, "get_project", map[string]any{"id": "NOPE"})
	if !res.IsError {
		t.Fatalf("get_project (unknown id) expected isError, got: %s", firstText(t, res))
	}
	want404 := `{"error":"not found","resource":"project","id":"NOPE","message":"unknown project"}`
	if text := firstText(t, res); text != want404 {
		t.Errorf("404 body not verbatim:\n got  %s\n want %s", text, want404)
	}

	// Malformed args (id as a number) → input-schema validation surfaces the
	// failure as an isError TOOL result (SEP-1303), not a protocol error.
	res2, err := c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "get_project",
		Arguments: map[string]any{"id": 42},
	}})
	if err != nil {
		t.Fatalf("tools/call get_project (bad arg type): %v", err)
	}
	if !res2.IsError {
		t.Errorf("expected input-schema validation failure as isError, got success: %s", firstText(t, res2))
	}
}

func TestUpdateProjectRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// Full: rename + description replace in one call.
	res := callTool(t, c, ctx, "update_project", map[string]any{
		"actor":       map[string]any{"name": "ada", "type": "human"},
		"id":          "P1",
		"name":        "Website Redesign v2",
		"description": "MVP landing page, second pass",
	})
	if res.IsError {
		t.Fatalf("update_project returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["id"] != "P1" || row["name"] != "Website Redesign v2" || row["description"] != "MVP landing page, second pass" {
		t.Errorf("update_project result = %s", firstText(t, res))
	}

	cc := findCall(t, *calls, http.MethodPut, "/api/v1/projects/P1")
	var sent apiclient.UpdateProjectRequest
	if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
		t.Fatalf("update_project upstream body not JSON: %v (%s)", err, cc.body)
	}
	if sent.Actor.Name != "ada" || sent.Actor.Type != "human" {
		t.Errorf("update_project upstream actor = %+v", sent.Actor)
	}
	if sent.Name == nil || *sent.Name != "Website Redesign v2" {
		t.Errorf("update_project upstream name = %v, want Website Redesign v2 (body %s)", sent.Name, cc.body)
	}
	if sent.Description == nil || *sent.Description != "MVP landing page, second pass" {
		t.Errorf("update_project upstream description = %v (body %s)", sent.Description, cc.body)
	}

	// Partial: an EXPLICIT empty description is a valid clear — presence on the
	// wire is the signal. The omitted name must stay nil (absent from the body).
	res = callTool(t, c, ctx, "update_project", map[string]any{
		"id":          "P1",
		"description": "",
	})
	if res.IsError {
		t.Fatalf("update_project (clear description) returned isError: %s", firstText(t, res))
	}
	cc = (*calls)[len(*calls)-1] // the LAST upstream call (same route as the full update)
	if cc.method != http.MethodPut || cc.path != "/api/v1/projects/P1" {
		t.Fatalf("last upstream call = %s %s, want PUT /api/v1/projects/P1", cc.method, cc.path)
	}
	var partial apiclient.UpdateProjectRequest
	if err := json.Unmarshal([]byte(cc.body), &partial); err != nil {
		t.Fatalf("update_project upstream body not JSON: %v (%s)", err, cc.body)
	}
	if partial.Name != nil {
		t.Errorf("partial update sent name=%q, want omitted (body %s)", *partial.Name, cc.body)
	}
	if partial.Description == nil || *partial.Description != "" {
		t.Errorf("partial update description = %v, want \"\" (body %s)", partial.Description, cc.body)
	}

	// A no-field update → rejected locally, no round-trip (the API 400s it).
	res = callTool(t, c, ctx, "update_project", map[string]any{"id": "P1"})
	if !res.IsError {
		t.Fatalf("update_project (no fields) expected isError, got: %s", firstText(t, res))
	}
	if text := firstText(t, res); !strings.Contains(text, `set at least one of`) {
		t.Errorf("no-field rejection = %s, want the invalid-input body", text)
	}

	// A whitespace name → same local rejection the API's 400 would be.
	res = callTool(t, c, ctx, "update_project", map[string]any{"id": "P1", "name": "   "})
	if !res.IsError {
		t.Fatalf("update_project (empty name) expected isError, got: %s", firstText(t, res))
	}
	if text := firstText(t, res); !strings.Contains(text, `required argument "name"`) {
		t.Errorf("empty-name rejection = %s, want the invalid-input body", text)
	}

	// Malformed args (id as a number) → input-schema validation failure.
	res2, err := c.CallTool(ctx, mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{
		Name:      "update_project",
		Arguments: map[string]any{"id": 42},
	}})
	if err != nil {
		t.Fatalf("tools/call update_project (bad arg type): %v", err)
	}
	if !res2.IsError {
		t.Errorf("expected input-schema validation failure as isError, got success: %s", firstText(t, res2))
	}
}

func TestListTasksFilterPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// Every optional filter at once: the mock must see exactly these params.
	res := callTool(t, c, ctx, "list_tasks", map[string]any{
		"project_id":       "P1",
		"status":           "todo",
		"assignee_name":    "ada",
		"assignee_type":    "agent",
		"archived":         true,
		"unblocked":        true,
		"search":           "login",
		"priority":         3,
		"due_before":       "2026-10-02T00:00:00Z",
		"due_after":        "2026-09-01T00:00:00Z",
		"created_after":    "2026-08-01T00:00:00Z",
		"created_before":   "2026-09-25T00:00:00Z",
		"completed_since":  "2026-09-15T00:00:00Z",
		"completed_before": "2026-10-05T00:00:00Z",
		"limit":            5,
		"offset":           10,
	})
	if res.IsError {
		t.Fatalf("list_tasks returned isError: %s", firstText(t, res))
	}
	env := asMap(t, firstText(t, res))
	tasks, _ := env["tasks"].([]any)
	total, _ := env["total"].(float64)
	limit, _ := env["limit"].(float64)
	offset, _ := env["offset"].(float64)
	if len(tasks) != 1 || int(total) != 1 || int(limit) != 5 || int(offset) != 10 {
		t.Errorf("list_tasks envelope = %s", firstText(t, res))
	}

	cc := findCall(t, *calls, http.MethodGet, "/api/v1/tasks")
	wantQ := url.Values{
		"project_id":     {"P1"},
		"status":         {"todo"},
		"assignee_name":  {"ada"},
		"assignee_type":  {"agent"},
		"archived":       {"true"},
		"unblocked":      {"true"},
		"search":         {"login"},
		"priority":       {"3"},
		"due_before":     {"2026-10-02T00:00:00Z"},
		"due_after":      {"2026-09-01T00:00:00Z"},
		"created_after":  {"2026-08-01T00:00:00Z"},
		"created_before": {"2026-09-25T00:00:00Z"},
		// The tool arg completed_since is transmitted as the documented alias
		// param completed_after (API.md: "aliases of each other").
		"completed_after":  {"2026-09-15T00:00:00Z"},
		"completed_before": {"2026-10-05T00:00:00Z"},
		"limit":            {"5"},
		"offset":           {"10"},
	}
	if !reflect.DeepEqual(cc.query, wantQ) {
		t.Errorf("filter query mismatch:\n got  %s\n want %s", cc.query.Encode(), wantQ.Encode())
	}
}

func TestListTasks_ClaimAndParentFilterPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// The epic pair: parent_id + claimed/claimed_by + the expired_claim tri-state.
	// A "false" must still be transmitted — presence is the tri-state signal.
	res := callTool(t, c, ctx, "list_tasks", map[string]any{
		"project_id":    "P1",
		"parent_id":     "PEP",
		"claimed":       true,
		"claimed_by":    "agent-a",
		"expired_claim": false,
	})
	if res.IsError {
		t.Fatalf("list_tasks returned isError: %s", firstText(t, res))
	}
	cc := findCall(t, *calls, http.MethodGet, "/api/v1/tasks")
	wantQ := url.Values{
		"project_id":    {"P1"},
		"parent_id":     {"PEP"},
		"claimed":       {"true"},
		"claimed_by":    {"agent-a"},
		"expired_claim": {"false"},
	}
	if !reflect.DeepEqual(cc.query, wantQ) {
		t.Errorf("filter query mismatch:\n got  %s\n want %s", cc.query.Encode(), wantQ.Encode())
	}
}

func TestListTasks_OmittedTriStateSendsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// Omitted tri-state params send nothing — the REST layer must see them as
	// unset (nil), not as "false".
	res := callTool(t, c, ctx, "list_tasks", map[string]any{"project_id": "P1"})
	if res.IsError {
		t.Fatalf("list_tasks returned isError: %s", firstText(t, res))
	}
	cc := findCall(t, *calls, http.MethodGet, "/api/v1/tasks")
	for _, k := range []string{"parent_id", "claimed", "claimed_by", "expired_claim"} {
		if v, ok := cc.query[k]; ok {
			t.Errorf("query %s = %q after plain list_tasks, want key absent", k, v)
		}
	}
	if cc.query.Get("project_id") != "P1" {
		t.Errorf("query project_id = %q, want P1 (all: %s)", cc.query.Get("project_id"), cc.query.Encode())
	}
}

// TestProjectReportRoundTrip verifies the project_report tool: all tuning args
// reach the upstream as query params, and OMITTING them sends NONE (the server
// applies its own defaults — a 0/explicit value must not masquerade as one).
func TestProjectReportRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "project_report", map[string]any{
		"project_id":      "P1",
		"stalled_minutes": 90,
		"since":           "2026-10-01T00:00:00Z",
	})
	if res.IsError {
		t.Fatalf("project_report returned isError: %s", firstText(t, res))
	}
	env := asMap(t, firstText(t, res))
	if env["project_id"] != "P1" {
		t.Errorf("report project_id = %v, want P1 (%s)", env["project_id"], firstText(t, res))
	}
	cc := findCall(t, *calls, http.MethodGet, "/api/v1/projects/P1/report")
	if got := cc.query.Get("stalled_minutes"); got != "90" {
		t.Errorf("forwarded stalled_minutes = %q, want 90", got)
	}
	if got := cc.query.Get("since"); got != "2026-10-01T00:00:00Z" {
		t.Errorf("forwarded since = %q, want 2026-10-01T00:00:00Z", got)
	}

	// Omitted tuning args (and stalled_minutes=0) send NO query params at all.
	res = callTool(t, c, ctx, "project_report", map[string]any{
		"project_id":      "P1",
		"stalled_minutes": 0,
	})
	if res.IsError {
		t.Fatalf("project_report (defaults) returned isError: %s", firstText(t, res))
	}
	last := (*calls)[len(*calls)-1] // the defaults call is the most recent upstream hit
	if last.method != http.MethodGet || last.path != "/api/v1/projects/P1/report" {
		t.Fatalf("defaults call upstream = %s %s, want GET /api/v1/projects/P1/report", last.method, last.path)
	}
	if len(last.query) != 0 {
		t.Errorf("omitted tuning args still forwarded query %s, want none (server applies its own defaults)", last.query.Encode())
	}

	// Empty project_id is rejected locally — no round-trip.
	before := len(*calls)
	res = callTool(t, c, ctx, "project_report", map[string]any{"project_id": "  "})
	if !res.IsError || !strings.Contains(firstText(t, res), "invalid input") {
		t.Errorf("empty project_id: want invalid-input isError, got %s", firstText(t, res))
	}
	if len(*calls) != before {
		t.Errorf("empty project_id still hit the upstream (%d calls, want none new)", len(*calls)-before)
	}
}

func TestCreateTask_ParentIDPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "create_task", map[string]any{
		"actor":      map[string]any{"name": "eve", "type": "agent"},
		"title":      "child of epic",
		"project_id": "P1",
		"parent_id":  "PEP",
	})
	if res.IsError {
		t.Fatalf("create_task returned isError: %s", firstText(t, res))
	}
	cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks")
	var sent apiclient.CreateTaskRequest
	if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
		t.Fatalf("create_task upstream body not JSON: %v (%s)", err, cc.body)
	}
	if sent.ParentID != "PEP" {
		t.Errorf("parent_id = %q, want PEP (body %s)", sent.ParentID, cc.body)
	}
}

func TestCreateTask_BlockedByPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "create_task", map[string]any{
		"actor":      map[string]any{"name": "eve", "type": "agent"},
		"title":      "behind two blockers",
		"project_id": "P1",
		"blocked_by": []any{"B1", "B2"},
	})
	if res.IsError {
		t.Fatalf("create_task returned isError: %s", firstText(t, res))
	}
	cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cc.body), &raw); err != nil {
		t.Fatalf("create_task upstream body not JSON: %v (%s)", err, cc.body)
	}
	bb, present := raw["blocked_by"]
	if !present {
		t.Fatalf("blocked_by missing from upstream body: %s", cc.body)
	}
	var items []string
	if err := json.Unmarshal(bb, &items); err != nil {
		t.Fatalf("blocked_by not a string array: %v (%s)", err, bb)
	}
	if len(items) != 2 || items[0] != "B1" || items[1] != "B2" {
		t.Errorf("blocked_by = %v, want [B1 B2] forwarded verbatim in order (body %s)", items, cc.body)
	}

	// Omitted → the key must not appear upstream at all. A fresh stack keeps
	// findCall's first-match semantics unambiguous.
	upstream2, _, calls2 := newMockUpstream(t)
	c2 := dial(t, startMCPStack(t, upstream2.URL).URL, ctx)
	defer func() { _ = c2.Close() }()
	res = callTool(t, c2, ctx, "create_task", map[string]any{
		"actor":      map[string]any{"name": "eve", "type": "agent"},
		"title":      "plain task",
		"project_id": "P1",
	})
	if res.IsError {
		t.Fatalf("create_task (no blocked_by) returned isError: %s", firstText(t, res))
	}
	cc2 := findCall(t, *calls2, http.MethodPost, "/api/v1/tasks")
	var raw2 map[string]any
	if err := json.Unmarshal([]byte(cc2.body), &raw2); err != nil {
		t.Fatalf("upstream body not JSON: %v (%s)", err, cc2.body)
	}
	if _, present := raw2["blocked_by"]; present {
		t.Errorf("blocked_by must be omitted when unset (body %s)", cc2.body)
	}
}

func TestCreateTaskRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	res := callTool(t, c, ctx, "create_task", map[string]any{
		"actor":               map[string]any{"name": "eve", "type": "agent"},
		"title":               "Fix login bug",
		"description":         "Users cannot log in after the password reset flow.",
		"project_id":          "P1",
		"priority":            3,
		"due_at":              "2026-10-01T00:00:00Z",
		"assignee":            map[string]any{"name": "ada", "type": "human"},
		"acceptance_criteria": []any{"login works", "logout works"},
	})
	if res.IsError {
		t.Fatalf("create_task returned isError: %s", firstText(t, res))
	}
	resp := asMap(t, firstText(t, res))
	task, _ := resp["task"].(map[string]any)
	dups, _ := resp["possible_duplicates"].([]any)
	if task == nil || task["id"] != "TNEW" || task["version"] != float64(1) {
		t.Errorf("create_task.task = %v", resp["task"])
	}
	if len(dups) != 1 {
		t.Fatalf("possible_duplicates = %v, want the advisory duplicate row", dups)
	}
	dup, _ := dups[0].(map[string]any)
	if dup["id"] != "TDUP" {
		t.Errorf("possible_duplicates[0] = %v", dup)
	}

	cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks")
	var sent apiclient.CreateTaskRequest
	if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
		t.Fatalf("create_task upstream body not JSON: %v (%s)", err, cc.body)
	}
	if sent.Actor.Name != "eve" || sent.Title != "Fix login bug" || sent.ProjectID != "P1" ||
		sent.Priority != 3 || sent.DueAt == nil || *sent.DueAt != "2026-10-01T00:00:00Z" {
		t.Errorf("create_task upstream body = %s", cc.body)
	}
	if sent.Assignee == nil || sent.Assignee.Name != "ada" || sent.Assignee.Type != "human" {
		t.Errorf("assignee not passed through: %s", cc.body)
	}
	if len(sent.AcceptanceCriteria) != 2 || sent.AcceptanceCriteria[0] != "login works" || sent.AcceptanceCriteria[1] != "logout works" {
		t.Errorf("acceptance_criteria = %v", sent.AcceptanceCriteria)
	}
}

func TestUpdateTaskRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// STALE version (3 vs stored 4) → isError with the 409 body VERBATIM — it
	// carries current_version + a fresh "current" row for rebasing without re-reading.
	res := callTool(t, c, ctx, "update_task", map[string]any{
		"id":      "T1",
		"actor":   map[string]any{"name": "runner-2", "type": "agent"},
		"version": 3,
		"status":  "done",
	})
	if !res.IsError {
		t.Fatalf("stale update_task: expected isError, got success: %s", firstText(t, res))
	}
	if text := firstText(t, res); text != mockVersionConflictBody {
		t.Errorf("409 body not verbatim:\n got  %s\n want %s", text, mockVersionConflictBody)
	}

	// FRESH version (4) → success with the bumped row.
	res = callTool(t, c, ctx, "update_task", map[string]any{
		"id":       "T1",
		"version":  4,
		"title":    "Fix login bug (again)",
		"priority": 2,
	})
	if res.IsError {
		t.Fatalf("fresh update_task returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["version"] != float64(5) || row["title"] != "Fix login bug (again)" || row["priority"] != float64(2) {
		t.Errorf("update_task result = %s", firstText(t, res))
	}

	var patches []capturedCall
	for _, cc := range *calls {
		if cc.method == http.MethodPatch && cc.path == "/api/v1/tasks/T1" {
			patches = append(patches, cc)
		}
	}
	if len(patches) != 2 {
		t.Fatalf("upstream saw %d PATCH /tasks/T1 calls, want 2: %+v", len(patches), patches)
	}
	var stale apiclient.UpdateTaskRequest
	if err := json.Unmarshal([]byte(patches[0].body), &stale); err != nil {
		t.Fatalf("stale update_task upstream body not JSON: %v (%s)", err, patches[0].body)
	}
	if stale.Version != 3 || stale.Status == nil || *stale.Status != "done" {
		t.Errorf("stale update_task upstream body = %s", patches[0].body)
	}
	var sent apiclient.UpdateTaskRequest
	if err := json.Unmarshal([]byte(patches[1].body), &sent); err != nil {
		t.Fatalf("fresh update_task upstream body not JSON: %v (%s)", err, patches[1].body)
	}
	if sent.Version != 4 || sent.Title == nil || *sent.Title != "Fix login bug (again)" || sent.Priority == nil || *sent.Priority != 2 {
		t.Errorf("fresh update_task upstream body = %s", patches[1].body)
	}
}

func TestUpdateTaskClearDueAndAssigneeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// Call update_task with clear_due: true, clear_assignee: true
	res := callTool(t, c, ctx, "update_task", map[string]any{
		"id":             "T1",
		"version":        4,
		"clear_due":      true,
		"clear_assignee": true,
	})
	if res.IsError {
		t.Fatalf("update_task with clear_due and clear_assignee returned error: %s", firstText(t, res))
	}

	var patches []capturedCall
	for _, cc := range *calls {
		if cc.method == http.MethodPatch && cc.path == "/api/v1/tasks/T1" {
			patches = append(patches, cc)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 PATCH /tasks/T1 call, got %d", len(patches))
	}
	var sent apiclient.UpdateTaskRequest
	if err := json.Unmarshal([]byte(patches[0].body), &sent); err != nil {
		t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, patches[0].body)
	}
	if !sent.ClearDue {
		t.Errorf("expected ClearDue == true, got false")
	}
	if !sent.ClearAssignee {
		t.Errorf("expected ClearAssignee == true, got false")
	}
	if sent.Version != 4 {
		t.Errorf("expected Version == 4, got %d", sent.Version)
	}

	// Verify that when clear_due and clear_assignee are omitted, they default to false
	resDefault := callTool(t, c, ctx, "update_task", map[string]any{
		"id":      "T1",
		"version": 4,
		"title":   "New Title",
	})
	if resDefault.IsError {
		t.Fatalf("update_task without clear flags returned error: %s", firstText(t, resDefault))
	}
	patches = nil
	for _, cc := range *calls {
		if cc.method == http.MethodPatch && cc.path == "/api/v1/tasks/T1" {
			patches = append(patches, cc)
		}
	}
	if len(patches) != 2 {
		t.Fatalf("expected 2 PATCH /tasks/T1 calls, got %d", len(patches))
	}
	var sentDefault apiclient.UpdateTaskRequest
	if err := json.Unmarshal([]byte(patches[1].body), &sentDefault); err != nil {
		t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, patches[1].body)
	}
	if sentDefault.ClearDue {
		t.Errorf("expected ClearDue == false when omitted, got true")
	}
	if sentDefault.ClearAssignee {
		t.Errorf("expected ClearAssignee == false when omitted, got true")
	}

	// Schema validation: non-boolean passed to clear_due should return isError
	resInvalid := callTool(t, c, ctx, "update_task", map[string]any{
		"id":        "T1",
		"version":   4,
		"clear_due": "not-a-bool",
	})
	if !resInvalid.IsError {
		t.Fatalf("expected isError for non-boolean clear_due, got success: %s", firstText(t, resInvalid))
	}
}

func TestUpdateTaskArchivedCoercion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	t.Run("passing archived \"true\" sends Archived: &true in upstream request", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": "true",
		})
		if res.IsError {
			t.Fatalf("update_task with archived: \"true\" returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Archived == nil || !*sent.Archived {
			t.Errorf("Archived = %v, want &true", sent.Archived)
		}
	})

	t.Run("passing archived true sends Archived: &true in upstream request", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": true,
		})
		if res.IsError {
			t.Fatalf("update_task with archived: true returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Archived == nil || !*sent.Archived {
			t.Errorf("Archived = %v, want &true", sent.Archived)
		}
	})

	t.Run("passing archived \"false\" sends Archived: &false in upstream request", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": "false",
		})
		if res.IsError {
			t.Fatalf("update_task with archived: \"false\" returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Archived == nil || *sent.Archived {
			t.Errorf("Archived = %v, want &false", sent.Archived)
		}
	})

	t.Run("passing archived false sends Archived: &false in upstream request", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": false,
		})
		if res.IsError {
			t.Fatalf("update_task with archived: false returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Archived == nil || *sent.Archived {
			t.Errorf("Archived = %v, want &false", sent.Archived)
		}
	})

	t.Run("passing archived \"not-a-bool\" returns isError with validation error message", func(t *testing.T) {
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": "not-a-bool",
		})
		if !res.IsError {
			t.Fatalf("expected isError for archived: \"not-a-bool\", got success: %s", firstText(t, res))
		}
		wantMsg := `{"error":"invalid input","message":"update_task: \"archived\" must be a boolean"}`
		if got := firstText(t, res); got != wantMsg {
			t.Errorf("validation error message mismatch:\n got  %s\n want %s", got, wantMsg)
		}
	})

	t.Run("passing invalid non-boolean type returns isError with validation error message", func(t *testing.T) {
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":       "T1",
			"version":  4,
			"archived": 12345,
		})
		if !res.IsError {
			t.Fatalf("expected isError for non-boolean type, got success: %s", firstText(t, res))
		}
		wantMsg := `{"error":"invalid input","message":"update_task: \"archived\" must be a boolean"}`
		if got := firstText(t, res); got != wantMsg {
			t.Errorf("validation error message mismatch:\n got  %s\n want %s", got, wantMsg)
		}
	})
}

func TestUpdateTaskDescriptionHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	t.Run("passing description: \"\" sets Description: &\"\" in upstream PATCH request (clearing description)", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":          "T1",
			"version":     4,
			"description": "",
		})
		if res.IsError {
			t.Fatalf("update_task with description: \"\" returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Description == nil {
			t.Fatalf("expected Description != nil, got nil")
		}
		if *sent.Description != "" {
			t.Errorf("Description = %q, want \"\"", *sent.Description)
		}
	})

	t.Run("passing description with whitespace only sets Description: &\"\" in upstream PATCH request", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":          "T1",
			"version":     4,
			"description": "   ",
		})
		if res.IsError {
			t.Fatalf("update_task with description: \"   \" returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Description == nil {
			t.Fatalf("expected Description != nil, got nil")
		}
		if *sent.Description != "" {
			t.Errorf("Description = %q, want \"\"", *sent.Description)
		}
	})

	t.Run("omitting description leaves Description: nil in upstream PATCH request (preserving description)", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":      "T1",
			"version": 4,
			"title":   "New Title",
		})
		if res.IsError {
			t.Fatalf("update_task without description returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Description != nil {
			t.Errorf("expected Description == nil when omitted, got %q", *sent.Description)
		}
	})

	t.Run("passing description: \"new text\" sets Description: &\"new text\"", func(t *testing.T) {
		*calls = nil
		res := callTool(t, c, ctx, "update_task", map[string]any{
			"id":          "T1",
			"version":     4,
			"description": "new text",
		})
		if res.IsError {
			t.Fatalf("update_task with description: \"new text\" returned isError: %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1")
		var sent apiclient.UpdateTaskRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("failed to unmarshal PATCH body: %v (%s)", err, cc.body)
		}
		if sent.Description == nil {
			t.Fatalf("expected Description != nil, got nil")
		}
		if *sent.Description != "new text" {
			t.Errorf("Description = %q, want \"new text\"", *sent.Description)
		}
	})
}

func TestToBool(t *testing.T) {
	tests := []struct {
		name      string
		input     any
		wantVal   bool
		wantValid bool
	}{
		{"bool true", true, true, true},
		{"bool false", false, false, true},
		{"string true", "true", true, true},
		{"string false", "false", false, true},
		{"string TRUE", "TRUE", true, true},
		{"string FALSE", "FALSE", false, true},
		{"string True", "True", true, true},
		{"string False", "False", false, true},
		{"string with whitespace", "  true  ", true, true},
		{"string 1", "1", true, true},
		{"string 0", "0", false, true},
		{"string t", "t", true, true},
		{"string f", "f", false, true},
		{"invalid string not-a-bool", "not-a-bool", false, false},
		{"invalid empty string", "", false, false},
		{"invalid whitespace string", "   ", false, false},
		{"invalid int", 1, false, false},
		{"invalid float", 0.0, false, false},
		{"invalid nil", nil, false, false},
		{"invalid slice", []string{"true"}, false, false},
		{"invalid map", map[string]any{"archived": true}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVal, gotValid := toBool(tt.input)
			if gotValid != tt.wantValid {
				t.Errorf("toBool(%v) valid = %v, want %v", tt.input, gotValid, tt.wantValid)
			}
			if gotVal != tt.wantVal {
				t.Errorf("toBool(%v) val = %v, want %v", tt.input, gotVal, tt.wantVal)
			}
		})
	}
}

func TestReleaseAndHeartbeatRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// release: routes to POST /tasks/{id}/release with actor+note; returns the
	// fresh row (status → todo, lease cleared).
	res := callTool(t, c, ctx, "release_task", map[string]any{
		"id":    "T1",
		"actor": map[string]any{"name": "runner-2", "type": "agent"},
		"note":  "stalled on the DB migration",
	})
	if res.IsError {
		t.Fatalf("release_task returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["status"] != "todo" || row["version"] != float64(6) {
		t.Errorf("release_task result = %s", firstText(t, res))
	}
	if _, hasLease := row["claimed_by"]; hasLease {
		t.Errorf("released row still carries claimed_by: %s", firstText(t, res))
	}
	ccRel := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/release")
	var rel apiclient.ReleaseTaskRequest
	if err := json.Unmarshal([]byte(ccRel.body), &rel); err != nil {
		t.Fatalf("release upstream body not JSON: %v (%s)", err, ccRel.body)
	}
	if rel.Actor.Name != "runner-2" || rel.Note != "stalled on the DB migration" {
		t.Errorf("release upstream body = %s", ccRel.body)
	}

	// heartbeat: routes to POST /tasks/{id}/heartbeat with actor only; returns
	// the fresh row that doubles as the liveness answer (version NOT bumped).
	res = callTool(t, c, ctx, "heartbeat_task", map[string]any{
		"id":    "T1",
		"actor": map[string]any{"name": "runner-2", "type": "agent"},
	})
	if res.IsError {
		t.Fatalf("heartbeat_task returned isError: %s", firstText(t, res))
	}
	row = asMap(t, firstText(t, res))
	holder, _ := row["claimed_by"].(map[string]any)
	if row["version"] != float64(4) || holder == nil || holder["name"] != "runner-2" {
		t.Errorf("heartbeat_task result = %s", firstText(t, res))
	}
	if row["claim_expires_at"] == nil {
		t.Errorf("heartbeat row missing claim_expires_at: %s", firstText(t, res))
	}
	ccHb := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/heartbeat")
	var hb apiclient.HeartbeatRequest
	if err := json.Unmarshal([]byte(ccHb.body), &hb); err != nil {
		t.Fatalf("heartbeat upstream body not JSON: %v (%s)", err, ccHb.body)
	}
	if hb.Actor.Name != "runner-2" || strings.Contains(ccHb.body, "note") {
		t.Errorf("heartbeat upstream body = %s (want actor only)", ccHb.body)
	}
}

// TestHandoffTaskRoundTrip proves handoff_task wiring at the tool level:
// args decode into the correct apiclient.HandoffRequest and are forwarded verbatim
// to POST /tasks/{id}/handoff; a success renders {task,comment}; a foreign active
// lease returns the upstream 409 body VERBATIM (the isError text == mockConflictBody).
func TestHandoffTaskRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// Success: full handoff with kind + body + to_assignee + notes. The tool must
	// route these into the request body and render the {task, comment} result.
	res := callTool(t, c, ctx, "handoff_task", map[string]any{
		"id":           "T1",
		"kind":         "handoff",
		"body":         "Handing off to ada; migration finished, see notes.",
		"notes":        "Next: verify the rollback script.",
		"to_assignee":  map[string]any{"name": "ada", "type": "human"},
		"actor":        map[string]any{"name": "runner-2", "type": "agent"},
	})
	if res.IsError {
		t.Fatalf("handoff_task returned isError: %s", firstText(t, res))
	}
	out := asMap(t, firstText(t, res))
	task, _ := out["task"].(map[string]any)
	comment, _ := out["comment"].(map[string]any)
	if task == nil || comment == nil {
		t.Fatalf("handoff_task result missing task/comment: %s", firstText(t, res))
	}
	if task["status"] != "todo" || task["version"] != float64(6) {
		t.Errorf("handoff task row = %v", task)
	}
	if comment["kind"] != "handoff" || comment["body"] != "Handing off to ada; migration finished, see notes." {
		t.Errorf("handoff comment row = %v", comment)
	}
	cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/handoff")
	var ho apiclient.HandoffRequest
	if err := json.Unmarshal([]byte(cc.body), &ho); err != nil {
		t.Fatalf("handoff upstream body not JSON: %v (%s)", err, cc.body)
	}
	if ho.Actor.Name != "runner-2" || ho.Kind != "handoff" || ho.Body == "" || ho.Notes == "" {
		t.Errorf("handoff upstream body = %s", cc.body)
	}
	if ho.ToAssignee == nil || ho.ToAssignee.Name != "ada" || ho.ToAssignee.Type != "human" {
		t.Errorf("handoff to_assignee not forwarded: %v", ho.ToAssignee)
	}

	// Foreign active lease: the tool must surface the 409 body byte-for-byte.
	res = callTool(t, c, ctx, "handoff_task", map[string]any{
		"id":    "HOLDING",
		"actor": map[string]any{"name": "other-agent", "type": "agent"},
	})
	if !res.IsError {
		t.Fatalf("handoff_task on foreign-held task did not error: %s", firstText(t, res))
	}
	if got := firstText(t, res); got != mockConflictBody {
		t.Errorf("handoff_task conflict body not verbatim:\n got %s\nwant %s", got, mockConflictBody)
	}
}

func TestAddCommentCompleteACAttachArtifactRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	t.Run("add_comment", func(t *testing.T) {
		res := callTool(t, c, ctx, "add_comment", map[string]any{
			"id":    "T1",
			"actor": map[string]any{"name": "ada", "type": "human"},
			"kind":  "question",
			"body":  "Which migration order is correct?",
		})
		if res.IsError {
			t.Fatalf("add_comment returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "CNEW" || row["kind"] != "question" || row["body"] != "Which migration order is correct?" {
			t.Errorf("add_comment result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/comments")
		var sent apiclient.CommentRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("add_comment upstream body not JSON: %v (%s)", err, cc.body)
		}
		if sent.Kind != "question" || sent.Body != "Which migration order is correct?" || sent.Actor.Name != "ada" {
			t.Errorf("add_comment upstream body = %s", cc.body)
		}
	})

	t.Run("complete_ac", func(t *testing.T) {
		res := callTool(t, c, ctx, "complete_ac", map[string]any{
			"id":   "T1",
			"seq":  2,
			"done": true,
			"note": "manual test passed",
		})
		if res.IsError {
			t.Fatalf("complete_ac returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		seq, _ := row["seq"].(float64)
		if int(seq) != 2 || row["done"] != true || row["note"] != "manual test passed" {
			t.Errorf("complete_ac result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1/acceptance-criteria/2")
		var sent apiclient.ACToggleRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("complete_ac upstream body not JSON: %v (%s)", err, cc.body)
		}
		if !sent.Done || sent.Note != "manual test passed" {
			t.Errorf("complete_ac upstream body = %s", cc.body)
		}
	})

	t.Run("complete_ac_text", func(t *testing.T) {
		res := callTool(t, c, ctx, "complete_ac", map[string]any{
			"id":   "T1",
			"seq":  2,
			"done": false,
			"text": "reworded via complete_ac",
		})
		if res.IsError {
			t.Fatalf("complete_ac (text) returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["text"] != "reworded via complete_ac" {
			t.Errorf("complete_ac (text) result = %s", firstText(t, res))
		}
	})

	t.Run("add_ac", func(t *testing.T) {
		res := callTool(t, c, ctx, "add_ac", map[string]any{
			"id":    "T1",
			"actor": map[string]any{"name": "runner-2", "type": "agent"},
			"text":  "verify logout works",
		})
		if res.IsError {
			t.Fatalf("add_ac returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		seq, _ := row["seq"].(float64)
		if int(seq) != 2 || row["text"] != "verify logout works" || row["done"] != false {
			t.Errorf("add_ac result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/acceptance-criteria")
		var sent apiclient.ACAddRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("add_ac upstream body not JSON: %v (%s)", err, cc.body)
		}
		if sent.Text != "verify logout works" || sent.Actor.Name != "runner-2" || sent.Actor.Type != "agent" {
			t.Errorf("add_ac upstream body = %s", cc.body)
		}
	})

	t.Run("remove_ac", func(t *testing.T) {
		res := callTool(t, c, ctx, "remove_ac", map[string]any{
			"id":    "T1",
			"seq":   2,
			"actor": map[string]any{"name": "runner-2", "type": "agent"},
		})
		if res.IsError {
			t.Fatalf("remove_ac returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["removed"] != true || row["task_id"] != "T1" || int(row["seq"].(float64)) != 2 {
			t.Errorf("remove_ac result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodDelete, "/api/v1/tasks/T1/acceptance-criteria/2")
		var sent apiclient.ACRemoveRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("remove_ac upstream body not JSON: %v (%s)", err, cc.body)
		}
		if sent.Actor.Name != "runner-2" || sent.Actor.Type != "agent" {
			t.Errorf("remove_ac upstream body = %s", cc.body)
		}
	})

	t.Run("edit_ac", func(t *testing.T) {
		res := callTool(t, c, ctx, "edit_ac", map[string]any{
			"id":    "T1",
			"seq":   1,
			"actor": map[string]any{"name": "runner-2", "type": "agent"},
			"text":  "login works flawlessly",
		})
		if res.IsError {
			t.Fatalf("edit_ac returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["text"] != "login works flawlessly" || int(row["seq"].(float64)) != 1 {
			t.Errorf("edit_ac result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPatch, "/api/v1/tasks/T1/acceptance-criteria/1")
		var sent apiclient.ACEditTextRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("edit_ac upstream body not JSON: %v (%s)", err, cc.body)
		}
		if sent.Text != "login works flawlessly" || sent.Actor.Name != "runner-2" || sent.Actor.Type != "agent" {
			t.Errorf("edit_ac upstream body = %s", cc.body)
		}
	})

	t.Run("attach_artifact", func(t *testing.T) {
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":      "T1",
			"actor":   map[string]any{"name": "runner-2", "type": "agent"},
			"kind":    "url",
			"ref":     "https://example.com/reports/deploy.json",
			"summary": "deploy report for the fix",
		})
		if res.IsError {
			t.Fatalf("attach_artifact returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "ANEW" || row["kind"] != "url" ||
			row["ref"] != "https://example.com/reports/deploy.json" || row["summary"] != "deploy report for the fix" {
			t.Errorf("attach_artifact result = %s", firstText(t, res))
		}
		cc := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/artifacts")
		var sent apiclient.ArtifactRequest
		if err := json.Unmarshal([]byte(cc.body), &sent); err != nil {
			t.Fatalf("attach_artifact upstream body not JSON: %v (%s)", err, cc.body)
		}
		if sent.Kind != "url" || sent.Ref != "https://example.com/reports/deploy.json" || sent.Actor.Name != "runner-2" {
			t.Errorf("attach_artifact upstream body = %s", cc.body)
		}
	})

	t.Run("attach_artifact_base64", func(t *testing.T) {
		payload := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":          "T1",
			"actor":       map[string]any{"name": "runner-2", "type": "agent"},
			"kind":        "screenshot",
			"ref":         "evidence.png",
			"summary":     "evidence screenshot",
			"data_base64": payload,
		})
		if res.IsError {
			t.Fatalf("attach_artifact returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "AUPLOAD" || row["kind"] != "screenshot" ||
			row["ref"] != "/artifacts/AUPLOAD" || row["summary"] != "evidence screenshot" {
			t.Errorf("attach_artifact result = %s", firstText(t, res))
		}
	})

	t.Run("attach_artifact_local_file", func(t *testing.T) {
		tmpFile, err := os.CreateTemp(t.TempDir(), "screenshot-*.png")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		_, _ = tmpFile.Write([]byte("fake-file-bytes"))
		_ = tmpFile.Close()

		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":      "T1",
			"actor":   map[string]any{"name": "runner-2", "type": "agent"},
			"kind":    "screenshot",
			"ref":     tmpFile.Name(),
			"summary": "local file screenshot",
		})
		if res.IsError {
			t.Fatalf("attach_artifact returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "AUPLOAD" || row["kind"] != "screenshot" ||
			row["ref"] != "/artifacts/AUPLOAD" || row["summary"] != "local file screenshot" {
			t.Errorf("attach_artifact result = %s", firstText(t, res))
		}
	})

	t.Run("attach_artifact_base64_filename_only", func(t *testing.T) {
		payload := base64.StdEncoding.EncodeToString([]byte("fake-video-bytes"))
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":          "T1",
			"actor":       map[string]any{"name": "runner-2", "type": "agent"},
			"kind":        "other",
			"filename":    "recording.mp4",
			"summary":     "test recording",
			"data_base64": payload,
		})
		if res.IsError {
			t.Fatalf("attach_artifact returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "AUPLOAD" || row["kind"] != "other" || row["ref"] != "/artifacts/AUPLOAD" {
			t.Errorf("attach_artifact result = %s", firstText(t, res))
		}
	})

	t.Run("attach_artifact_data_uri", func(t *testing.T) {
		payload := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":          "T1",
			"kind":        "screenshot",
			"data_base64": payload,
			"summary":     "data uri screenshot",
		})
		if res.IsError {
			t.Fatalf("attach_artifact data uri returned isError: %s", firstText(t, res))
		}
		row := asMap(t, firstText(t, res))
		if row["id"] != "AUPLOAD" {
			t.Errorf("attach_artifact result = %s", firstText(t, res))
		}
	})

	t.Run("attach_artifact_missing_inputs", func(t *testing.T) {
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":   "T1",
			"kind": "screenshot",
		})
		if !res.IsError {
			t.Fatalf("expected error when neither data_base64 nor ref provided")
		}
		if !strings.Contains(firstText(t, res), "data_base64") || !strings.Contains(firstText(t, res), "must be provided") {
			t.Errorf("unexpected error text: %s", firstText(t, res))
		}
	})

	t.Run("attach_artifact_unreachable_local_path", func(t *testing.T) {
		res := callTool(t, c, ctx, "attach_artifact", map[string]any{
			"id":   "T1",
			"kind": "screenshot",
			"ref":  "/path/does/not/exist/screenshot.png",
		})
		if !res.IsError {
			t.Fatalf("expected error for non-existent local file path")
		}
		if !strings.Contains(firstText(t, res), "not found on the MCP server filesystem") {
			t.Errorf("unexpected error text: %s", firstText(t, res))
		}
	})
}

func TestRelateTasksAddRemoveRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	// ADD (action defaults to "add"): routes POST /tasks/{id}/relations with
	// other_id+kind; returns the stored edge row.
	res := callTool(t, c, ctx, "relate_tasks", map[string]any{
		"id":       "T1",
		"other_id": "T2",
		"kind":     "blocked-by",
	})
	if res.IsError {
		t.Fatalf("relate_tasks(add) returned isError: %s", firstText(t, res))
	}
	row := asMap(t, firstText(t, res))
	if row["task_id"] != "T1" || row["other_task_id"] != "T2" || row["kind"] != "blocked-by" {
		t.Errorf("relate_tasks(add) result = %s", firstText(t, res))
	}
	ccAdd := findCall(t, *calls, http.MethodPost, "/api/v1/tasks/T1/relations")
	var add apiclient.RelationRequest
	if err := json.Unmarshal([]byte(ccAdd.body), &add); err != nil {
		t.Fatalf("relate_tasks(add) upstream body not JSON: %v (%s)", err, ccAdd.body)
	}
	if add.OtherID != "T2" || add.Kind != "blocked-by" {
		t.Errorf("relate_tasks(add) upstream body = %s", ccAdd.body)
	}

	// REMOVE: routes DELETE /tasks/{id}/relations/{other_id}/{kind}; the 204
	// No-Content answer becomes a compact removed:true confirmation.
	res = callTool(t, c, ctx, "relate_tasks", map[string]any{
		"id":       "T1",
		"other_id": "T2",
		"kind":     "blocked-by",
		"action":   "remove",
	})
	if res.IsError {
		t.Fatalf("relate_tasks(remove) returned isError: %s", firstText(t, res))
	}
	rem := asMap(t, firstText(t, res))
	if rem["removed"] != true || rem["task_id"] != "T1" || rem["other_task_id"] != "T2" || rem["kind"] != "blocked-by" {
		t.Errorf("relate_tasks(remove) result = %s", firstText(t, res))
	}
	ccDel := findCall(t, *calls, http.MethodDelete, "/api/v1/tasks/T1/relations/T2/blocked-by")
	var del apiclient.UnrelateTaskRequest
	if err := json.Unmarshal([]byte(ccDel.body), &del); err != nil {
		t.Fatalf("relate_tasks(remove) upstream body not JSON: %v (%s)", err, ccDel.body)
	}
	if del.Actor.Name != "default-agent" { // no actor passed → container default fallback
		t.Errorf("relate_tasks(remove) upstream body = %s", ccDel.body)
	}

	// A second, different parent → isError with the 409 body VERBATIM.
	res = callTool(t, c, ctx, "relate_tasks", map[string]any{
		"id":       "TPARENT",
		"other_id": "TOTHER2",
		"kind":     "parent",
	})
	if !res.IsError {
		t.Fatalf("relate_tasks(second parent): expected isError, got success: %s", firstText(t, res))
	}
	if text := firstText(t, res); text != mockParentConflictBody {
		t.Errorf("parent conflict body not verbatim:\n got  %s\n want %s", text, mockParentConflictBody)
	}
}

// --- /healthz proxy -------------------------------------------------------------------------

func TestHealthzProxiesUpstream(t *testing.T) {
	upstream, _, _ := newMockUpstream(t)
	mcpSrv := startMCPStack(t, upstream.URL)

	getStatus := func() int {
		resp, err := http.Get(mcpSrv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := getStatus(); code != http.StatusOK {
		t.Errorf("healthy upstream → /healthz = %d, want 200", code)
	}

	// Upstream down → the MCP container must report 503 so compose's
	// depends_on: service_healthy does not chain through a dead backend.
	upstream.Close()
	code := func() int {
		resp, err := http.Get(mcpSrv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var h struct {
			Status string `json:"status"`
			DB     string `json:"db"`
		}
		if err := json.Unmarshal(body, &h); err != nil {
			t.Fatalf("healthz body not JSON: %v (%s)", err, body)
		}
		if h.Status != "degraded" || !strings.Contains(h.DB, "unreachable") {
			t.Errorf("unreachable upstream body = %+v, want degraded/unreachable", h)
		}
		return resp.StatusCode
	}()
	if code != http.StatusServiceUnavailable {
		t.Errorf("unreachable upstream → /healthz = %d, want 503", code)
	}
}

func TestMCPTokenForwardingEndToEnd(t *testing.T) {
	ctx := context.Background()
	const expectedToken = "prod-auth-token-999"

	// Mock upstream requiring expectedToken with the API.md error shape
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"missing or invalid auth token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockProjectsBody))
	}))
	t.Cleanup(upstream.Close)

	// 1. MCP stack with matching token succeeds end-to-end
	mcpGood := startMCPStackWithToken(t, upstream.URL, expectedToken)
	clientGood := dial(t, mcpGood.URL, ctx)
	resGood := callTool(t, clientGood, ctx, "list_projects", nil)
	if resGood.IsError {
		t.Fatalf("expected success with correct token, got error: %s", firstText(t, resGood))
	}
	goodText := firstText(t, resGood)
	if !strings.Contains(goodText, "Website Redesign") {
		t.Errorf("list_projects result = %q, want it to contain 'Website Redesign'", goodText)
	}

	// 2. MCP stack with wrong token fails with 401 unauthorized
	mcpBad := startMCPStackWithToken(t, upstream.URL, "wrong-token")
	clientBad := dial(t, mcpBad.URL, ctx)
	resBad := callTool(t, clientBad, ctx, "list_projects", nil)
	if !resBad.IsError {
		t.Fatalf("expected error with wrong token, got success: %+v", resBad)
	}
	badText := firstText(t, resBad)
	if !strings.Contains(badText, "unauthorized") {
		t.Errorf("expected unauthorized in error response, got %q", badText)
	}

	// 3. MCP stack with empty token fails with 401 unauthorized
	mcpEmpty := startMCPStackWithToken(t, upstream.URL, "")
	clientEmpty := dial(t, mcpEmpty.URL, ctx)
	resEmpty := callTool(t, clientEmpty, ctx, "list_projects", nil)
	if !resEmpty.IsError {
		t.Fatalf("expected error with empty token, got success: %+v", resEmpty)
	}
	emptyText := firstText(t, resEmpty)
	if !strings.Contains(emptyText, "unauthorized") {
		t.Errorf("expected unauthorized in error response, got %q", emptyText)
	}
}

func TestMCPServerNameAndActorFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const customAgent = "custom-ravn-agent"
	t.Setenv("RAVN_AGENT_NAME", customAgent)

	upstream, claims, _ := newMockUpstream(t)
	mcpSrv := startMCPStack(t, upstream.URL)

	tr, err := mcptransport.NewStreamableHTTP(mcpSrv.URL + "/mcp")
	if err != nil {
		t.Fatalf("NewStreamableHTTP: %v", err)
	}
	c := mcpclient.NewClient(tr)
	initRes, err := c.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: "ravn-mcp-test", Version: "0"},
	}})
	if err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Assert MCP server name is ravn
	if initRes.ServerInfo.Name != "ravn" {
		t.Errorf("ServerInfo.Name = %q, want %q", initRes.ServerInfo.Name, "ravn")
	}

	// Assert actor fallback uses RAVN_AGENT_NAME
	res := callTool(t, c, ctx, "claim_task", map[string]any{
		"id": "T1",
	})
	if res.IsError {
		t.Fatalf("claim_task returned error: %s", firstText(t, res))
	}
	if len(*claims) != 1 {
		t.Fatalf("upstream saw %d claims, want 1: %+v", len(*claims), *claims)
	}
	var fb struct {
		Actor apiclient.Actor `json:"actor"`
	}
	if err := json.Unmarshal([]byte((*claims)[0].body), &fb); err != nil {
		t.Fatalf("claim body not JSON: %v (%s)", err, (*claims)[0].body)
	}
	if fb.Actor.Name != customAgent || fb.Actor.Type != "agent" {
		t.Errorf("fallback actor = %+v, want agent %q from RAVN_AGENT_NAME", fb.Actor, customAgent)
	}
}

// TestNumericParamSchemasAreIntegerWithBounds asserts the REGISTERED JSON schema
// of every integer argument matches what the backend actually accepts, so LLM
// clients are told up front — not after a 400 round-trip — that these take
// integers within these bounds. The bounds mirror the real enforcement:
//
//	priority        0–5, out of range → 400
//	    list_tasks: parseTaskFilter (interface/http/helpers.go);
//	    create/update: application/tasks.go Create & Update ("out of range 0–5")
//	limit           positive integer, default 20, > 100 clamped   — parseTaskFilter / List
//	offset          non-negative                                  — parseTaskFilter / List
//	comment_limit   >= 0 (0 = default 10), > 50 clamped           — parseTailLimit + GetDetail
//	event_limit     >= 0 (0 = default 20), > 100 clamped          — parseTailLimit + GetDetail
//	ttl_sec         >= 0 (negative → 400; 0 = default 900), > 3600 clamped — Claim
//	version         >= 1 (fresh tasks are version 1)              — UpdateTaskRequest / update_task
func TestNumericParamSchemasAreIntegerWithBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, _ := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	list, err := c.ListTools(ctx, mcpgo.ListToolsRequest{})
	if err != nil {
		t.Fatalf("c.ListTools failed: %v", err)
	}
	byName := make(map[string]map[string]any, len(list.Tools)) // tool name → properties map
	for i := range list.Tools {
		byName[list.Tools[i].Name] = list.Tools[i].InputSchema.Properties
	}

	cases := []struct {
		tool, prop string
		min, max   int // -1 = "no such key expected", >= 0 = expected JSON number
	}{
		{"list_tasks", "priority", 0, 5},
		{"create_task", "priority", 0, 5},
		{"update_task", "priority", 0, 5},
		{"list_tasks", "limit", 1, 100},
		{"list_tasks", "offset", 0, -1},
		{"get_task", "comment_limit", 0, 50},
		{"get_task", "event_limit", 0, 100},
		{"claim_task", "ttl_sec", 0, 3600},
		{"update_task", "version", 1, -1},
	}

	for _, tc := range cases {
		props := byName[tc.tool]
		if props == nil {
			t.Fatalf("%s: tool not found or InputSchema.Properties is empty", tc.tool)
		}
		raw, ok := props[tc.prop]
		if !ok {
			t.Errorf("%s: input schema missing property %q", tc.tool, tc.prop)
			continue
		}
		prop, _ := raw.(map[string]any)
		if prop == nil {
			t.Errorf("%s.%s: property is %T, want object", tc.tool, tc.prop, raw)
			continue
		}
		if prop["type"] != "integer" {
			t.Errorf("%s.%s schema type = %v, want \"integer\"", tc.tool, tc.prop, prop["type"])
		}
		assertBound := func(key string, want int) {
			if want < 0 {
				if _, present := prop[key]; present {
					t.Errorf("%s.%s: schema has unexpected %q = %v", tc.tool, tc.prop, key, prop[key])
				}
				return
			}
			got, ok := prop[key].(float64) // JSON numbers decode as float64
			if !ok {
				t.Errorf("%s.%s: schema missing %q (or not numeric: %v)", tc.tool, tc.prop, key, prop[key])
				return
			}
			if got != float64(want) {
				t.Errorf("%s.%s: schema %s = %v, want %d", tc.tool, tc.prop, key, got, want)
			}
		}
		assertBound("minimum", tc.min)
		assertBound("maximum", tc.max)
	}

	// End-to-end proof the fix works: values that used to decode as floats and
	// only fail later server-side are now rejected at MCP input-schema
	// validation with a self-correcting message (WithInputSchemaValidation is
	// on via ServerOptions()).
	if res := callTool(t, c, ctx, "list_tasks", map[string]any{"priority": 2.5}); !res.IsError {
		t.Errorf("list_tasks priority=2.5: want schema-validation error, got success: %s", firstText(t, res))
	}
	if res := callTool(t, c, ctx, "claim_task", map[string]any{"id": "T1", "ttl_sec": -5}); !res.IsError {
		t.Errorf("claim_task ttl_sec=-5: want schema-validation error, got success: %s", firstText(t, res))
	}
	if res := callTool(t, c, ctx, "update_task", map[string]any{"id": "T1", "version": 0.5}); !res.IsError {
		t.Errorf("update_task version=0.5: want schema-validation error, got success: %s", firstText(t, res))
	}
}

