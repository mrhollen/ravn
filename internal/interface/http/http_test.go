package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

// stack is the full production wiring against a real temp-file SQLite DB:
// sqlite → UnitOfWork → every use case → App → mux with all /api/v1 routes.
// Slice 3a's tests build on it; later slices reuse the SAME helper for their
// handler tests (no new fixtures).
type stack struct {
	Handler http.Handler
	App     *App
}

func newStack(t *testing.T) stack {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-http-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()

	app := NewApp(Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	return stack{Handler: mux, App: app}
}

// --- tiny request/response helpers -------------------------------------------------

func actor(name string) map[string]any {
	return map[string]any{"type": "human", "name": name}
}

func doReq(t *testing.T, h http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://crowban.test"+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

// doRaw sends a body verbatim (no JSON marshaling) — for malformed-JSON cases.
func doRaw(t *testing.T, h http.Handler, method, path, raw string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://crowban.test"+path, bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

func mustDecode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode response %q: %v", string(body), err)
	}
	return v
}

// errResp covers every error envelope the slice-3a contract can emit.
type errResp struct {
	Error          string          `json:"error"`
	Message        string          `json:"message"`
	Resource       string          `json:"resource"`
	ID             string          `json:"id"`
	CurrentVersion int             `json:"current_version"`
	Holder         *actorDTO       `json:"holder"`
	ExpiresAt      string          `json:"expires_at"`
	Current        *taskSummaryDTO `json:"current"`
}

// --- seed helpers --------------------------------------------------------------------

func createProject(t *testing.T, stk stack, name string) projectDTO {
	t.Helper()
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/projects", map[string]any{
		"actor": actor("ada"), "name": name, "description": "seeded in test",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /projects (%s) = %d, want 201 (body %s)", name, code, string(body))
	}
	return mustDecode[projectDTO](t, body)
}

// makeTask creates a task; extra overrides any create-task request field
// (e.g. "priority": 5, "due_at": "...", "assignee": {...}).
func makeTask(t *testing.T, stk stack, projectID, title string, extra map[string]any) taskSummaryDTO {
	t.Helper()
	body := map[string]any{"actor": actor("eve"), "title": title, "project_id": projectID}
	for k, v := range extra {
		body[k] = v
	}
	code, b := doReq(t, stk.Handler, "POST", "/api/v1/tasks", body)
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks (%s) = %d, want 201 (body %s)", title, code, string(b))
	}
	return mustDecode[createTaskResponse](t, b).Task
}

func newTask(t *testing.T, stk stack, projectID, title string) taskSummaryDTO {
	t.Helper()
	return makeTask(t, stk, projectID, title, nil)
}

func patchTask(t *testing.T, stk stack, id string, version int, extra map[string]any) taskSummaryDTO {
	t.Helper()
	body := map[string]any{"actor": actor("eve"), "version": version}
	for k, v := range extra {
		body[k] = v
	}
	code, b := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+id, body)
	if code != http.StatusOK {
		t.Fatalf("PATCH /tasks/%s (v%d %v) = %d, want 200 (body %s)", id, version, extra, code, string(b))
	}
	return mustDecode[taskSummaryDTO](t, b)
}

func listTasks(t *testing.T, stk stack, query string) ([]taskSummaryDTO, int64) {
	t.Helper()
	code, b := doReq(t, stk.Handler, "GET", "/api/v1/tasks"+query, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks%s = %d, want 200 (body %s)", query, code, string(b))
	}
	var page struct {
		Tasks []taskSummaryDTO `json:"tasks"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatalf("decode list response %q: %v", string(b), err)
	}
	return page.Tasks, page.Total
}

func idsOf(tasks []taskSummaryDTO) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}

func wantIDs(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: ids = %v (len %d), want %v (len %d)", what, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: ids[%d] = %q, want %q (all got=%v)", what, i, got[i], want[i], got)
		}
	}
}

// --- tests ---------------------------------------------------------------------------

func TestProjectsLifecycleAndCounts(t *testing.T) {
	stk := newStack(t)
	p1 := createProject(t, stk, "Alpha")

	a := newTask(t, stk, p1.ID, "task A")
	b := newTask(t, stk, p1.ID, "task B")
	_ = newTask(t, stk, p1.ID, "task C")
	patchTask(t, stk, a.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, a.ID, 2, map[string]any{"status": "done"})
	patchTask(t, stk, b.ID, 1, map[string]any{"status": "doing"})

	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects = %d, want 200 (body %s)", code, string(body))
	}
	var lst struct {
		Projects []projectRowDTO `json:"projects"`
		Total    int             `json:"total"`
	}
	if err := json.Unmarshal(body, &lst); err != nil {
		t.Fatalf("decode projects list %q: %v", string(body), err)
	}
	if lst.Total != 1 || len(lst.Projects) != 1 {
		t.Fatalf("projects total = %d (%d rows), want 1", lst.Total, len(lst.Projects))
	}
	got := lst.Projects[0]
	if got.ID != p1.ID || got.Name != "Alpha" {
		t.Fatalf("first project row = %+v, want id %s name Alpha", got, p1.ID)
	}
	want := statusCountDTO{Todo: 1, Doing: 1, Done: 1}
	if got.TaskCounts != want {
		t.Fatalf("task_counts for Alpha = %+v, want %+v", got.TaskCounts, want)
	}

	// A project with no tasks shows all-zero counts.
	p2 := createProject(t, stk, "Beta")
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects (2nd) = %d, want 200", code)
	}
	if err := json.Unmarshal(body, &lst); err != nil {
		t.Fatalf("decode projects list %q: %v", string(body), err)
	}
	if lst.Total != 2 {
		t.Fatalf("projects total = %d, want 2", lst.Total)
	}
	var beta projectRowDTO
	for _, row := range lst.Projects {
		if row.ID == p2.ID {
			beta = row
		}
	}
	if (beta.TaskCounts) != (statusCountDTO{}) {
		t.Fatalf("task_counts for Beta = %+v, want all zero", beta.TaskCounts)
	}

	// Get: 200 with the row, 404 JSON for unknown ids.
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p1.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects/%s = %d, want 200 (body %s)", p1.ID, code, string(body))
	}
	if got := mustDecode[projectDTO](t, body); got.Name != "Alpha" || got.Description != "seeded in test" {
		t.Fatalf("GET /projects/%s = %+v, want Alpha with seeded description", p1.ID, got)
	}

	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/0734567890abcdefghij00000000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET unknown project = %d, want 404 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "not found" || e.Resource != "project" {
		t.Fatalf("404 body = %+v, want error \"not found\" resource \"project\"", e)
	}

	// Empty name → 400.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/projects", map[string]any{
		"actor": actor("ada"), "name": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /projects with blank name = %d, want 400", code)
	}
}

func TestTaskCreateAcceptanceCriteriaAndDuplicates(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Dup Watch")

	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor":               actor("eve"),
		"title":               "Fix login bug",
		"description":         "Users cannot log in after the password reset.",
		"project_id":          p.ID,
		"priority":            3,
		"acceptance_criteria": []string{"login works", "logout works"},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d, want 201 (body %s)", code, string(body))
	}
	cr := mustDecode[createTaskResponse](t, body)
	if cr.Task.Version != 1 || cr.Task.Status != "todo" || cr.Task.Priority != 3 {
		t.Fatalf("created task = %+v, want version 1 / todo / priority 3", cr.Task)
	}
	if len(cr.PossibleDuplicates) != 0 {
		t.Fatalf("possible_duplicates for the first task = %d rows, want 0", len(cr.PossibleDuplicates))
	}

	// Resume payload: full detail incl. the two ACs and a created event tail.
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+cr.Task.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d, want 200 (body %s)", cr.Task.ID, code, string(body))
	}
	d := mustDecode[taskDetailDTO](t, body)
	if d.Title != "Fix login bug" || d.ProjectID != p.ID || d.Version != 1 {
		t.Fatalf("detail header = %+v, want the created task", d.taskSummaryDTO)
	}
	if len(d.AcceptanceCriteria) != 2 {
		t.Fatalf("acceptance_criteria = %d rows, want 2", len(d.AcceptanceCriteria))
	}
	if d.AcceptanceCriteria[0].Seq != 1 || d.AcceptanceCriteria[0].Text != "login works" ||
		d.AcceptanceCriteria[1].Seq != 2 || d.AcceptanceCriteria[1].Text != "logout works" {
		t.Fatalf("acceptance_criteria = %+v, want seqs 1,2 with seeded texts", d.AcceptanceCriteria)
	}
	if len(d.Comments) != 0 || len(d.Artifacts) != 0 || len(d.Relations) != 0 {
		t.Fatalf("empty child collections not empty: comments=%d artifacts=%d relations=%d",
			len(d.Comments), len(d.Artifacts), len(d.Relations))
	}
	if d.Blocked || len(d.Blockers) != 0 {
		t.Fatalf("fresh task reported blocked (blocked=%v blockers=%d)", d.Blocked, len(d.Blockers))
	}
	if len(d.Events) == 0 || d.Events[0].Kind != "created" {
		t.Fatalf("events tail = %+v, want at least the created event", d.Events)
	}

	// A similar title advertises the existing task as a possible duplicate —
	// and never the newly created task's own id.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "fix login bug", "project_id": p.ID,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /tasks (similar) = %d, want 201 (body %s)", code, string(body))
	}
	cr2 := mustDecode[createTaskResponse](t, body)
	foundFirst, foundSelf := false, false
	for _, dup := range cr2.PossibleDuplicates {
		if dup.ID == cr.Task.ID {
			foundFirst = true
		}
		if dup.ID == cr2.Task.ID {
			foundSelf = true
		}
	}
	if !foundFirst {
		t.Fatalf("possible_duplicates = %+v, want it to contain %s", cr2.PossibleDuplicates, cr.Task.ID)
	}
	if foundSelf {
		t.Fatalf("possible_duplicates contains the new task's own id %s", cr2.Task.ID)
	}
}

func TestTaskListFiltersPaginationAndOrder(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Orders")

	gamma := makeTask(t, stk, p.ID, "gamma", map[string]any{"priority": 5}) // highest priority, undated
	beta := makeTask(t, stk, p.ID, "beta", map[string]any{
		"priority": 3, "due_at": "2026-10-01T00:00:00Z",
	})
	alpha := makeTask(t, stk, p.ID, "alpha", map[string]any{
		"priority": 3, "due_at": "2026-10-05T00:00:00Z",
	})
	delta := makeTask(t, stk, p.ID, "delta", map[string]any{
		"priority": 1, "assignee": map[string]any{"type": "human", "name": "bob"},
	})

	// Canonical order: priority DESC, due ASC nulls-last, created ASC.
	rows, total := listTasks(t, stk, "?project_id="+p.ID)
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	wantIDs(t, idsOf(rows), []string{gamma.ID, beta.ID, alpha.ID, delta.ID}, "canonical order")

	// Priority filter (exact match).
	rows, total = listTasks(t, stk, "?project_id="+p.ID+"&priority=3")
	if total != 2 {
		t.Fatalf("priority=3 total = %d, want 2", total)
	}
	wantIDs(t, idsOf(rows), []string{beta.ID, alpha.ID}, "priority=3 order (due asc)")

	// Status filter.
	patchTask(t, stk, gamma.ID, 1, map[string]any{"status": "doing"})
	rows, total = listTasks(t, stk, "?project_id="+p.ID+"&status=doing")
	if total != 1 {
		t.Fatalf("status=doing total = %d, want 1", total)
	}
	wantIDs(t, idsOf(rows), []string{gamma.ID}, "status=doing")

	// Search (case-insensitive substring over title+description+notes).
	rows, _ = listTasks(t, stk, "?project_id="+p.ID+"&search=ALPH")
	wantIDs(t, idsOf(rows), []string{alpha.ID}, "search=ALPH")

	// Assignee name filter (+ the documented `assignee` alias).
	for _, extra := range []string{"assignee_name=bob", "assignee=bob"} {
		rows, total = listTasks(t, stk, pQuery(p.ID)+"&"+extra)
		if total != 1 || len(rows) != 1 || rows[0].ID != delta.ID {
			t.Fatalf("%s: got %d rows (%v), want only %s", extra, total, idsOf(rows), delta.ID)
		}
	}

	// Due-date window. due_after is inclusive (>=), due_before exclusive (<);
	// undated tasks never match either bound.
	rows, total = listTasks(t, stk, "?project_id="+p.ID+"&due_after=2026-10-03T00:00:00Z")
	if total != 1 {
		t.Fatalf("due_after total = %d, want 1", total)
	}
	wantIDs(t, idsOf(rows), []string{alpha.ID}, "due_after window")
	rows, total = listTasks(t, stk, "?project_id="+p.ID+"&due_before=2026-10-02T00:00:00Z")
	if total != 1 {
		t.Fatalf("due_before total = %d, want 1", total)
	}
	wantIDs(t, idsOf(rows), []string{beta.ID}, "due_before window")

	// Pagination: offset/limit with the effective values echoed + total kept.
	code, b := doReq(t, stk.Handler, "GET", "/api/v1/tasks?project_id="+p.ID+"&limit=2&offset=0", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks limit/offset = %d, want 200", code)
	}
	var page struct {
		Tasks  []taskSummaryDTO `json:"tasks"`
		Total  int64            `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatalf("decode paged response %q: %v", string(b), err)
	}
	wantIDs(t, idsOf(page.Tasks), []string{gamma.ID, beta.ID}, "page 1 (limit=2)")
	if page.Total != 4 || page.Limit != 2 || page.Offset != 0 {
		t.Fatalf("envelope total/limit/offset = %d/%d/%d, want 4/2/0", page.Total, page.Limit, page.Offset)
	}

	code, b = doReq(t, stk.Handler, "GET", "/api/v1/tasks?project_id="+p.ID+"&limit=2&offset=2", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks offset=2 = %d, want 200", code)
	}
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatalf("decode paged response %q: %v", string(b), err)
	}
	wantIDs(t, idsOf(page.Tasks), []string{alpha.ID, delta.ID}, "page 2 (offset=2)")

	// Unblocked: work is blocked-by blocker (seeded via the app layer — the
	// relations routes land in slice 3b); unblocked=true excludes it, and the
	// total honors the same predicate.
	blocker := makeTask(t, stk, p.ID, "blocker", map[string]any{"priority": 4})
	work := makeTask(t, stk, p.ID, "work item", map[string]any{"priority": 4})
	if _, err := stk.App.Relations.Relate(context.Background(),
		domain.Actor{Type: domain.ActorAgent, Name: "eve"}, domain.ID(work.ID), domain.ID(blocker.ID),
		domain.RelationBlockedBy, application.RelateAdd); err != nil {
		t.Fatalf("seed blocked-by edge: %v", err)
	}
	rows, total = listTasks(t, stk, pQuery(p.ID)+"&unblocked=true")
	if total != 5 { // six open tasks minus the blocked one
		t.Fatalf("unblocked=true total = %d, want 5", total)
	}
	for _, row := range rows {
		if row.ID == work.ID {
			t.Fatalf("unblocked=true still lists blocked task %s", work.ID)
		}
	}
	rows, total = listTasks(t, stk, pQuery(p.ID))
	if total != 6 || len(rows) != 6 {
		t.Fatalf("no-filter total/rows = %d/%d, want 6/6", total, len(rows))
	}

	// Malformed filter values → 400.
	for _, q := range []string{
		"?status=bogus",
		"?priority=9",
		"?due_before=not-a-date",
		"?limit=-3",
		"?offset=x",
	} {
		code, b = doReq(t, stk.Handler, "GET", "/api/v1/tasks"+q, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("GET /tasks%s = %d, want 400 (body %s)", q, code, string(b))
		}
		e := mustDecode[errResp](t, b)
		if e.Error != "invalid input" {
			t.Fatalf("GET /tasks%s error code = %q, want \"invalid input\"", q, e.Error)
		}
	}
}

// pQuery returns the standard project-scoped list query prefix.
func pQuery(projectID string) string {
	return "?project_id=" + projectID
}

func TestTaskPatchVersionSemantics(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Versions")
	task := newTask(t, stk, p.ID, "versioned work")

	// Correct version → 200, incremented version, applied fields.
	got := patchTask(t, stk, task.ID, 1, map[string]any{"status": "doing"})
	if got.Version != 2 || got.Status != "doing" {
		t.Fatalf("PATCH v1 → %+v, want version 2 status doing", got)
	}
	got = patchTask(t, stk, task.ID, 2, map[string]any{"title": "Renamed"})
	if got.Version != 3 || got.Title != "Renamed" {
		t.Fatalf("PATCH v2 → %+v, want version 3 title Renamed", got)
	}

	// STALE version → 409 with current_version AND the fresh current task.
	code, body := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID, map[string]any{
		"actor": actor("eve"), "version": 1, "status": "done",
	})
	if code != http.StatusConflict {
		t.Fatalf("PATCH with stale version = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "version_conflict" || e.CurrentVersion != 3 {
		t.Fatalf("409 body = %+v, want error version_conflict current_version 3", e)
	}
	if e.Current == nil || e.Current.ID != task.ID || e.Current.Title != "Renamed" || e.Current.Status != "doing" {
		t.Fatalf("409 current task = %+v, want the fresh row (Renamed/doing)", e.Current)
	}

	// Missing version → 400.
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID, map[string]any{
		"actor": actor("eve"), "status": "todo",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PATCH without version = %d, want 400 (body %s)", code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "invalid input" {
		t.Fatalf("missing-version error code = %q, want \"invalid input\"", e.Error)
	}

	// Unknown task id → 404 with the resource named.
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/0734567890abcdefghij00000000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET unknown task = %d, want 404 (body %s)", code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "not found" || e.Resource != "task" {
		t.Fatalf("404 body = %+v, want error \"not found\" resource \"task\"", e)
	}
}

func TestErrorMappingBasics(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Errors")

	// Empty actor name → 400.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": map[string]string{"type": "human", "name": ""}, "title": "t", "project_id": p.ID,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with empty actor name = %d, want 400 (body %s)", code, string(body))
	}

	// Unknown actor type → 400.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": map[string]string{"type": "robot", "name": "eve"}, "title": "t", "project_id": p.ID,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with bad actor type = %d, want 400 (body %s)", code, string(body))
	}

	// Empty title → 400; unknown project → 400.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "   ", "project_id": p.ID,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with blank title = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "orphan", "project_id": "0734567890abcdefghij00000000",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks for unknown project = %d, want 400", code)
	}

	// Malformed JSON → 400 (never a panic/500).
	code, body = doRaw(t, stk.Handler, "POST", "/api/v1/tasks", `{"actor": oops`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with broken JSON = %d, want 400 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "invalid input" || e.Message == "" {
		t.Fatalf("broken-JSON error = %+v, want invalid input with a message", e)
	}
}

func TestBoardSnapshot(t *testing.T) {
	stk := newStack(t)
	p := createProject(t, stk, "Board")

	low := makeTask(t, stk, p.ID, "low priority todo", map[string]any{"priority": 1})
	high := makeTask(t, stk, p.ID, "high priority todo", map[string]any{"priority": 5})
	doingT := newTask(t, stk, p.ID, "doing task")
	doneT := newTask(t, stk, p.ID, "done task")
	patchTask(t, stk, doingT.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, doneT.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, doneT.ID, 2, map[string]any{"status": "done"})

	code, body := doReq(t, stk.Handler, "GET", "/api/v1/projects/"+p.ID+"/board", nil)
	if code != http.StatusOK {
		t.Fatalf("GET board = %d, want 200 (body %s)", code, string(body))
	}
	b := mustDecode[boardResponse](t, body)
	if b.ProjectID != p.ID {
		t.Fatalf("board project_id = %s, want %s", b.ProjectID, p.ID)
	}
	wantCounts := statusCountDTO{Todo: 2, Doing: 1, Done: 1}
	if b.Counts != wantCounts {
		t.Fatalf("board counts = %+v, want %+v", b.Counts, wantCounts)
	}
	wantIDs(t, idsOf(b.Tasks.Todo), []string{high.ID, low.ID}, "todo column (canonical order)")
	wantIDs(t, idsOf(b.Tasks.Doing), []string{doingT.ID}, "doing column")
	wantIDs(t, idsOf(b.Tasks.Done), []string{doneT.ID}, "done column")

	// Empty board shape stays well-formed ([] not null).
	empty := createProject(t, stk, "Empty Board")
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/"+empty.ID+"/board", nil)
	if code != http.StatusOK {
		t.Fatalf("GET empty board = %d, want 200 (body %s)", code, string(body))
	}
	b = mustDecode[boardResponse](t, body)
	if b.Counts != (statusCountDTO{}) || len(b.Tasks.Todo) != 0 || len(b.Tasks.Doing) != 0 || len(b.Tasks.Done) != 0 {
		t.Fatalf("empty board = %+v, want zero counts and empty columns", b)
	}

	// Unknown project → 404 (project checked before the task table).
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects/0734567890abcdefghij00000000/board", nil)
	if code != http.StatusNotFound {
		t.Fatalf("GET board for unknown project = %d, want 404 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "not found" || e.Resource != "project" {
		t.Fatalf("board 404 = %+v, want not found / project", e)
	}
}
