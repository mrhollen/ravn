package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
)

// stack is the full production wiring against a real temp-file SQLite DB:
// sqlite → UnitOfWork → every use case → App → mux with all /api/v1 routes.
// Slice 3a's tests build on it; later slices reuse the SAME helper for their
// handler tests (no new fixtures).
type stack struct {
	Handler http.Handler
	App     *App
}

func newStack(t testing.TB) stack {
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

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func newStackWithClock(t testing.TB, c *fakeClock) stack {
	t.Helper()
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "crowban-http-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { closeDB() })

	uow := sqlite.NewUnitOfWork(db)
	mint := idgen.New()

	app := NewApp(Deps{
		Projects:   application.NewProjects(uow, c, mint),
		Tasks:      application.NewTasks(uow, c, mint),
		Comments:   application.NewComments(uow, c, mint),
		Artifacts:  application.NewArtifacts(uow, c, mint),
		Acceptance: application.NewAcceptance(uow, c, mint),
		Relations:  application.NewRelations(uow, c, mint),
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, app)
	return stack{Handler: mux, App: app}
}

// --- tiny request/response helpers -------------------------------------------------

func actor(name string) map[string]any {
	return map[string]any{"type": "human", "name": name}
}

func doReq(t testing.TB, h http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://ravn.test"+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

// doRaw sends a body verbatim (no JSON marshaling) — for malformed-JSON cases.
func doRaw(t *testing.T, h http.Handler, method, path, raw string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://ravn.test"+path, bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

func mustDecode[T any](t testing.TB, body []byte) T {
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

func createProject(t testing.TB, stk stack, name string) projectDTO {
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
func makeTask(t testing.TB, stk stack, projectID, title string, extra map[string]any) taskSummaryDTO {
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

func newTask(t testing.TB, stk stack, projectID, title string) taskSummaryDTO {
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
	t.Parallel()
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

func TestProjectArchive_HTTP(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p1 := createProject(t, stk, "Keep Open")
	p2 := createProject(t, stk, "To Archive")

	// Archive p2 via PUT /api/v1/projects/{id}
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p2.ID, map[string]any{
		"actor":    actor("ada"),
		"archived": true,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /projects/%s archive = %d, want 200 (body %s)", p2.ID, code, string(body))
	}
	updated := mustDecode[projectDTO](t, body)
	if !updated.Archived {
		t.Fatalf("expected updated project to have archived=true, got %+v", updated)
	}

	// GET /projects (default): only open projects
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects = %d, want 200", code)
	}
	var openList struct {
		Projects []projectRowDTO `json:"projects"`
		Total    int             `json:"total"`
	}
	if err := json.Unmarshal(body, &openList); err != nil {
		t.Fatalf("decode open list: %v", err)
	}
	if openList.Total != 1 || len(openList.Projects) != 1 || openList.Projects[0].ID != p1.ID {
		t.Fatalf("expected only p1 in open list, got %+v", openList)
	}

	// GET /projects?archived=true: only archived projects
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects?archived=true", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects?archived=true = %d, want 200", code)
	}
	var archList struct {
		Projects []projectRowDTO `json:"projects"`
		Total    int             `json:"total"`
	}
	if err := json.Unmarshal(body, &archList); err != nil {
		t.Fatalf("decode arch list: %v", err)
	}
	if archList.Total != 1 || len(archList.Projects) != 1 || archList.Projects[0].ID != p2.ID {
		t.Fatalf("expected only p2 in arch list, got %+v", archList)
	}

	// Unarchive p2
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/projects/"+p2.ID, map[string]any{
		"actor":    actor("ada"),
		"archived": false,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /projects/%s unarchive = %d, want 200", p2.ID, code)
	}
	unarchived := mustDecode[projectDTO](t, body)
	if unarchived.Archived {
		t.Fatalf("expected unarchived project to have archived=false, got %+v", unarchived)
	}

	// GET /projects should now list both
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/projects", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /projects = %d, want 200", code)
	}
	if err := json.Unmarshal(body, &openList); err != nil {
		t.Fatalf("decode open list: %v", err)
	}
	if openList.Total != 2 {
		t.Fatalf("expected 2 projects after unarchive, got %d", openList.Total)
	}
}

func TestTaskCreateAcceptanceCriteriaAndDuplicates(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if _, _, err := stk.App.Relations.Relate(context.Background(),
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
	t.Parallel()
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
	t.Parallel()
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

// getDetail fetches the resume payload (GET /tasks/{id}) — the canonical way
// these tests verify child-row state that summary rows do not carry.
func getDetail(t *testing.T, stk stack, id string) taskDetailDTO {
	t.Helper()
	code, b := doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d, want 200 (body %s)", id, code, string(b))
	}
	return mustDecode[taskDetailDTO](t, b)
}

func TestClaimLifecycle(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Claims")
	task := newTask(t, stk, p.ID, "claimable work") // version 1

	// A claims → 200 fresh task: doing, version bumped, lease fields set.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusOK {
		t.Fatalf("A claim = %d, want 200 (body %s)", code, string(body))
	}
	got := mustDecode[taskSummaryDTO](t, body)
	if got.Status != "doing" || got.Version != 2 {
		t.Fatalf("A claim → %+v, want doing at version 2", got)
	}
	if got.ClaimedBy == nil || got.ClaimedBy.Name != "agent-a" || got.ClaimExpiresAt == nil {
		t.Fatalf("claim summary lease = holder %v expires %v, want agent-a + expiry", got.ClaimedBy, got.ClaimExpiresAt)
	}

	// get_task confirms the lease and that the DURABLE assignee is untouched.
	d := getDetail(t, stk, task.ID)
	if d.Status != "doing" || d.ClaimedBy == nil || d.ClaimedBy.Name != "agent-a" {
		t.Fatalf("detail after claim = status %s holder %+v, want doing / agent-a", d.Status, d.ClaimedBy)
	}
	if d.Assignee != nil {
		t.Fatalf("claim touched the durable assignee: %+v", d.Assignee)
	}
	if d.ClaimTTLSeconds != 900 { // ttl_sec omitted → server default
		t.Fatalf("default claim ttl = %d, want 900", d.ClaimTTLSeconds)
	}

	// B claims → 409 claim conflict with holder=A and the expiry.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-b"),
	})
	if code != http.StatusConflict {
		t.Fatalf("B claim (held by A) = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "claim conflict" || e.Holder == nil || e.Holder.Name != "agent-a" || e.ExpiresAt == "" {
		t.Fatalf("409 claim body = %+v, want error \"claim conflict\" holder agent-a with expiry", e)
	}
	// The message must carry the no-wait clarification (never sleep/poll for a
	// lease expiry; close-out mutations are version-guarded, not claim-gated) —
	// pinning the wording so it cannot silently regress.
	for _, frag := range []string{"do not wait for the lease to expire", "close-out mutations (comments/notes/status/ACs) are open to non-holders", "explicit permission"} {
		if !strings.Contains(e.Message, frag) {
			t.Errorf("409 claim message %q missing no-wait clarification fragment %q", e.Message, frag)
		}
	}

	// B heartbeats → 409 (not the active holder).
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-b"),
	})
	if code != http.StatusConflict {
		t.Fatalf("B heartbeat (not holder) = %d, want 409", code)
	}

	// A heartbeats → 200, still doing, NO version bump.
	before := getDetail(t, stk, task.ID).Version
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusOK {
		t.Fatalf("A heartbeat = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Status != "doing" || got.Version != before {
		t.Fatalf("heartbeat → %+v (was v%d), want still doing at version %d", got, before, before)
	}

	// B releases → 409 (holder-only).
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release", map[string]any{
		"actor": actor("agent-b"), "note": "not mine",
	})
	if code != http.StatusConflict {
		t.Fatalf("B release (not holder) = %d, want 409", code)
	}

	// A releases → 200: todo, lease cleared, version bumped.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release", map[string]any{
		"actor": actor("agent-a"), "note": "back to the pool",
	})
	if code != http.StatusOK {
		t.Fatalf("A release = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Status != "todo" || got.ClaimedBy != nil || got.Version != 3 {
		t.Fatalf("release → %+v, want todo unclaimed at version 3", got)
	}
	d = getDetail(t, stk, task.ID)
	if d.Status != "todo" || d.ClaimedBy != nil {
		t.Fatalf("detail after release = status %s holder %+v, want todo unclaimed", d.Status, d.ClaimedBy)
	}

	// B claims after the release → 200 with an explicit ttl_sec override.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-b"), "ttl_sec": 60,
	})
	if code != http.StatusOK {
		t.Fatalf("B claim after release = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Status != "doing" || got.ClaimedBy == nil || got.ClaimedBy.Name != "agent-b" {
		t.Fatalf("B claim → %+v, want doing held by agent-b", got)
	}
	d = getDetail(t, stk, task.ID)
	if d.ClaimTTLSeconds != 60 {
		t.Fatalf("ttl_sec override not honored: stored ttl %d, want 60", d.ClaimTTLSeconds)
	}

	// Negative ttl → 400; unknown task → 404.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "ttl_sec": -5,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("claim with negative ttl = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/0734567890abcdefghij00000000/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusNotFound {
		t.Fatalf("claim on unknown task = %d, want 404", code)
	}
}

func TestRelease_ExpiredLeaseAndForce_HTTP(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fc := &fakeClock{now: base}
	stk := newStackWithClock(t, fc)
	p := createProject(t, stk, "Release Expired and Force")
	task := newTask(t, stk, p.ID, "lease test task")

	// 1. Agent A claims task with TTL 30s
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "ttl_sec": 30,
	})
	if code != http.StatusOK {
		t.Fatalf("claim A = %d, want 200 (body %s)", code, string(body))
	}

	// 2. Advance time past the 30s TTL -> lease expires
	fc.now = base.Add(45 * time.Second)

	// 3. Agent B attempts to release expired lease without force -> 409 Conflict
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release", map[string]any{
		"actor": actor("agent-b"), "note": "rival release",
	})
	if code != http.StatusConflict {
		t.Fatalf("B release of expired lease = %d, want 409 (body %s)", code, string(body))
	}
	errDTO := mustDecode[errResp](t, body)
	if errDTO.Error != "claim conflict" || errDTO.Holder == nil || errDTO.Holder.Name != "agent-a" {
		t.Fatalf("expected conflict holding agent-a, got %+v", errDTO)
	}

	// 4. Agent A releases own expired lease without force -> 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release", map[string]any{
		"actor": actor("agent-a"), "note": "finished late",
	})
	if code != http.StatusOK {
		t.Fatalf("A release of own expired lease = %d, want 200 (body %s)", code, string(body))
	}
	sum := mustDecode[taskSummaryDTO](t, body)
	if sum.Status != "todo" || sum.ClaimedBy != nil {
		t.Fatalf("task after expired release: %+v, want todo unclaimed", sum)
	}

	// 5. Agent A claims task again
	fc.now = base.Add(60 * time.Second)
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "ttl_sec": 30,
	})
	if code != http.StatusOK {
		t.Fatalf("re-claim A = %d, want 200", code)
	}

	// 6. Admin force-releases via query parameter ?force=true -> 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release?force=true", map[string]any{
		"actor": actor("admin-user"), "note": "admin break via query param",
	})
	if code != http.StatusOK {
		t.Fatalf("admin force release with ?force=true = %d, want 200 (body %s)", code, string(body))
	}
	sum = mustDecode[taskSummaryDTO](t, body)
	if sum.Status != "todo" || sum.ClaimedBy != nil {
		t.Fatalf("task after admin force release: %+v, want todo unclaimed", sum)
	}

	// 7. Agent A claims task again, and lease expires
	fc.now = base.Add(100 * time.Second)
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"), "ttl_sec": 30,
	})
	if code != http.StatusOK {
		t.Fatalf("claim A = %d, want 200", code)
	}
	fc.now = base.Add(200 * time.Second) // expired zombie lease

	// 8. Admin force-releases via body {"force": true} -> 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release", map[string]any{
		"actor": actor("admin-user"), "note": "admin break via body", "force": true,
	})
	if code != http.StatusOK {
		t.Fatalf("admin force release with body force=true = %d, want 200 (body %s)", code, string(body))
	}
	sum = mustDecode[taskSummaryDTO](t, body)
	if sum.Status != "todo" || sum.ClaimedBy != nil {
		t.Fatalf("task after admin force release: %+v, want todo unclaimed", sum)
	}

	// 9. Admin force-releases on already unclaimed task -> idempotent 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/release?force=true", map[string]any{
		"actor": actor("admin-user"),
	})
	if code != http.StatusOK {
		t.Fatalf("admin force release on unclaimed task = %d, want 200", code)
	}
}

func TestClaimAndHeartbeat_DoneAndArchived_HTTP(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Claims Guards")

	// 1. Task completed
	taskDone := newTask(t, stk, p.ID, "completed task")
	// Claim as agent-a
	code, _ := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskDone.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	// Mark done
	code, _ = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+taskDone.ID, map[string]any{
		"actor":   actor("agent-a"),
		"status":  "done",
		"version": 2,
	})
	if code != http.StatusOK {
		t.Fatalf("patch done: %d", code)
	}

	// Claiming done task returns 400 invalid input and does not mutate status
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskDone.ID+"/claim", map[string]any{
		"actor": actor("agent-b"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("claim done task = %d, want 400 (body %s)", code, string(body))
	}
	d := getDetail(t, stk, taskDone.ID)
	if d.Status != "done" {
		t.Errorf("status = %q, want done", d.Status)
	}

	// Heartbeat on done task returns 400 invalid input
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskDone.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("heartbeat done task = %d, want 400 (body %s)", code, string(body))
	}

	// 2. Task archived
	taskArchived := newTask(t, stk, p.ID, "archived task")
	code, _ = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+taskArchived.ID, map[string]any{
		"actor":    actor("agent-a"),
		"archived": true,
		"version":  1,
	})
	if code != http.StatusOK {
		t.Fatalf("patch archived: %d", code)
	}

	// Claiming archived task returns 400 invalid input
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskArchived.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("claim archived task = %d, want 400 (body %s)", code, string(body))
	}

	// Heartbeat on archived task returns 400 invalid input
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+taskArchived.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("heartbeat archived task = %d, want 400 (body %s)", code, string(body))
	}
}

func TestTransitionRejectionAndUnclaimedHeartbeat_Bodies(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Error Bodies")
	task := newTask(t, stk, p.ID, "body shapes")

	// 1. Illegal status move (todo → done, skipping doing) — the SAME 400
	//    "invalid input" shape as before the structured TransitionError, with
	//    the message naming both endpoints (web/src/moves.ts pattern-matches it).
	code, body := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID, map[string]any{
		"actor":   actor("eve"),
		"version": 1,
		"status":  "done",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PATCH todo→done = %d, want 400 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	wantMsg := `cannot transition status from "todo" to "done"`
	if e.Error != "invalid input" || e.Message != wantMsg {
		t.Fatalf("400 body = %+v, want error \"invalid input\" message %q", e, wantMsg)
	}

	// 2. Heartbeat on an UNCLAIMED task — NOT a claim conflict with an empty
	//    holder ("claimed by \"\" until 0001-…"), but 409 "not claimed" telling
	//    the agent to claim first (MCP propagates this body verbatim).
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-b"),
	})
	if code != http.StatusConflict {
		t.Fatalf("unclaimed heartbeat = %d, want 409 (body %s)", code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "not claimed" || e.Resource != "task" || e.ID != task.ID {
		t.Fatalf("409 body = %+v, want error \"not claimed\" resource task id %s", e, task.ID)
	}
	if e.Holder != nil {
		t.Errorf("not-claimed body carries a holder (%+v); the tag IS the signal now", e.Holder)
	}
	if !strings.Contains(e.Message, "claim first") {
		t.Errorf("not-claimed message = %q, want it to say claim first", e.Message)
	}

	// 3. A FOREIGN holder still gets the classic 409 claim conflict (unchanged).
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/claim", map[string]any{
		"actor": actor("agent-a"),
	})
	if code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{
		"actor": actor("agent-b"),
	})
	if code != http.StatusConflict {
		t.Fatalf("foreign heartbeat = %d, want 409 (body %s)", code, string(body))
	}
	e = mustDecode[errResp](t, body)
	if e.Error != "claim conflict" || e.Holder == nil || e.Holder.Name != "agent-a" {
		t.Fatalf("foreign heartbeat 409 = %+v, want claim conflict naming agent-a", e)
	}
}

func TestCommentsAddListAndErrors(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Threads")
	task := newTask(t, stk, p.ID, "discussed work")

	kinds := []struct{ kind, body string }{
		{"progress", "ran the build — green"},
		{"question", "should we also update the docs?"},
		{"decision", "using offset pagination for comments"},
		{"handoff", "@bob picking this up next"},
		{"note", "context: this is the MVP thread"},
	}
	for _, k := range kinds { // every valid kind → 201 fresh comment
		code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
			"actor": actor("ada"), "kind": k.kind, "body": k.body,
		})
		if code != http.StatusCreated {
			t.Fatalf("POST /comments (%s) = %d, want 201 (body %s)", k.kind, code, string(body))
		}
		c := mustDecode[commentDTO](t, body)
		if c.ID == "" || c.Kind != k.kind || c.Body != k.body || c.Actor.Name != "ada" || c.CreatedAt == "" {
			t.Fatalf("201 comment = %+v, want kind %s body %q actor ada", c, k.kind, k.body)
		}
	}

	type page struct {
		Comments []commentDTO `json:"comments"`
		Total    int64        `json:"total"`
		Limit    int          `json:"limit"`
		Offset   int          `json:"offset"`
	}
	decodePage := func(b []byte) page {
		var pg page
		if err := json.Unmarshal(b, &pg); err != nil {
			t.Fatalf("decode comment list %q: %v", string(b), err)
		}
		return pg
	}

	// GET list: newest first + total (before paging) + effective limit/offset.
	code, body := doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+task.ID+"/comments?limit=3", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /comments = %d, want 200 (body %s)", code, string(body))
	}
	pg := decodePage(body)
	if pg.Total != 5 || len(pg.Comments) != 3 || pg.Limit != 3 || pg.Offset != 0 {
		t.Fatalf("comment page = total %d rows %d limit %d offset %d, want 5/3/3/0", pg.Total, len(pg.Comments), pg.Limit, pg.Offset)
	}
	for i, want := range []string{kinds[4].body, kinds[3].body, kinds[2].body} { // newest first
		if pg.Comments[i].Body != want {
			t.Fatalf("page[?limit=3] row %d = %q, want %q (newest first)", i, pg.Comments[i].Body, want)
		}
	}

	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/"+task.ID+"/comments?limit=2&offset=3", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /comments offset = %d, want 200", code)
	}
	pg = decodePage(body)
	if pg.Total != 5 || len(pg.Comments) != 2 || pg.Offset != 3 {
		t.Fatalf("comment page offset=3 = total %d rows %d, want 5/2", pg.Total, len(pg.Comments))
	}
	if pg.Comments[0].Body != kinds[1].body || pg.Comments[1].Body != kinds[0].body {
		t.Fatalf("page[limit=2&offset=3] = %+v, want the two oldest newest-first", pg.Comments)
	}

	// The same thread is reflected in get_task's comment tail (default limit 10).
	d := getDetail(t, stk, task.ID)
	if len(d.Comments) != 5 {
		t.Fatalf("get_task comments = %d rows, want 5", len(d.Comments))
	}
	if d.Comments[0].Body != kinds[4].body || d.Comments[4].Body != kinds[0].body {
		t.Fatalf("get_task comment tail order wrong: first %q last %q", d.Comments[0].Body, d.Comments[4].Body)
	}

	// Empty body → 400; bad kind → 400; unknown task → 404 (POST and GET).
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
		"actor": actor("ada"), "kind": "note", "body": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("empty comment body = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
		"actor": actor("ada"), "kind": "rant", "body": "x",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad comment kind = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/0734567890abcdefghij00000000/comments", map[string]any{
		"actor": actor("ada"), "kind": "note", "body": "x",
	})
	if code != http.StatusNotFound {
		t.Fatalf("comment on unknown task = %d, want 404", code)
	}
	code, _ = doReq(t, stk.Handler, "GET", "/api/v1/tasks/0734567890abcdefghij00000000/comments", nil)
	if code != http.StatusNotFound {
		t.Fatalf("list comments of unknown task = %d, want 404", code)
	}
}

func TestNotesReplaceOnWrite(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Scratchpad")
	task := newTask(t, stk, p.ID, "noted work") // version 1, empty notes

	// PUT replaces the whole blob; NO base-version field in the body.
	code, body := doReq(t, stk.Handler, "PUT", "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("ada"), "notes": "state of the world: migration half-done",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /notes = %d, want 200 (body %s)", code, string(body))
	}
	got := mustDecode[taskSummaryDTO](t, body)
	if got.Version != 2 {
		t.Fatalf("PUT /notes → version %d, want 2", got.Version)
	}
	d := getDetail(t, stk, task.ID)
	if d.Notes != "state of the world: migration half-done" || d.Version != 2 {
		t.Fatalf("detail notes = %q at v%d, want the new blob at v2", d.Notes, d.Version)
	}

	// A second change bumps again.
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("agent-b"), "notes": "takeover: rolling back now",
	})
	if code != http.StatusOK {
		t.Fatalf("2nd PUT /notes = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Version != 3 {
		t.Fatalf("2nd PUT /notes → version %d, want 3", got.Version)
	}

	// Identical content is a no-op: fresh row returned WITHOUT another bump.
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/tasks/"+task.ID+"/notes", map[string]any{
		"actor": actor("agent-b"), "notes": "takeover: rolling back now",
	})
	if code != http.StatusOK {
		t.Fatalf("no-op PUT /notes = %d, want 200 (body %s)", code, string(body))
	}
	got = mustDecode[taskSummaryDTO](t, body)
	if got.Version != 3 {
		t.Fatalf("identical-content PUT bumped to v%d, want it to stay at 3", got.Version)
	}

	// Unknown task → 404.
	code, body = doReq(t, stk.Handler, "PUT", "/api/v1/tasks/0734567890abcdefghij00000000/notes", map[string]any{
		"actor": actor("ada"), "notes": "x",
	})
	if code != http.StatusNotFound {
		t.Fatalf("PUT /notes unknown task = %d, want 404", code)
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "not found" || e.Resource != "task" {
		t.Fatalf("notes 404 body = %+v, want not found / task", e)
	}
}

func TestAcceptanceToggleAndErrors(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Checklist")
	task := makeTask(t, stk, p.ID, "checklisted work", map[string]any{
		"acceptance_criteria": []string{"login works", "logout works"},
	})

	// Toggle seq 1 on → 200 with the updated criterion row.
	code, body := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/1", map[string]any{
		"actor": actor("ada"), "done": true, "note": "manual test passed",
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH ac 1 = %d, want 200 (body %s)", code, string(body))
	}
	ac := mustDecode[acDTO](t, body)
	if ac.Seq != 1 || !ac.Done || ac.Note != "manual test passed" ||
		ac.CompletedBy == nil || ac.CompletedBy.Name != "ada" || ac.CompletedAt == nil {
		t.Fatalf("updated AC = %+v, want seq 1 done with note + completer", ac)
	}

	// Reflected in get_task; the sibling criterion is untouched.
	d := getDetail(t, stk, task.ID)
	if len(d.AcceptanceCriteria) != 2 || !d.AcceptanceCriteria[0].Done || d.AcceptanceCriteria[1].Done {
		t.Fatalf("detail ACs = %+v, want [0] done and [1] not", d.AcceptanceCriteria)
	}

	// Uncheck clears the completion fields again.
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/1", map[string]any{
		"actor": actor("ada"), "done": false,
	})
	if code != http.StatusOK {
		t.Fatalf("uncheck ac 1 = %d, want 200 (body %s)", code, string(body))
	}
	ac = mustDecode[acDTO](t, body)
	if ac.Done || ac.CompletedBy != nil || ac.CompletedAt != nil {
		t.Fatalf("unchecked AC = %+v, want done=false with cleared completion fields", ac)
	}
	d = getDetail(t, stk, task.ID)
	if d.AcceptanceCriteria[0].Done {
		t.Fatalf("get_task still shows seq 1 done after uncheck")
	}

	// Bad seq → 404 (unknown criterion); non-numeric/zero seq → 400; unknown task → 404.
	code, _ = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/99", map[string]any{
		"actor": actor("ada"), "done": true,
	})
	if code != http.StatusNotFound {
		t.Fatalf("PATCH ac seq 99 = %d, want 404", code)
	}
	for _, bad := range []string{"abc", "0"} {
		code, _ = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/"+bad, map[string]any{
			"actor": actor("ada"), "done": true,
		})
		if code != http.StatusBadRequest {
			t.Fatalf("PATCH ac seq %q = %d, want 400", bad, code)
		}
	}
	code, _ = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/0734567890abcdefghij00000000/acceptance-criteria/1", map[string]any{
		"actor": actor("ada"), "done": true,
	})
	if code != http.StatusNotFound {
		t.Fatalf("PATCH ac on unknown task = %d, want 404", code)
	}
}

func TestAcceptanceCriteriaEditingEndpoints(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "AC editing")
	task := makeTask(t, stk, p.ID, "edited checklist", map[string]any{
		"acceptance_criteria": []string{"login works", "logout works"},
	})

	// POST appends at the next seq → 201 with the fresh row.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
		"actor": actor("ada"), "text": "reset works",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST ac = %d, want 201 (body %s)", code, string(body))
	}
	ac := mustDecode[acDTO](t, body)
	if ac.Seq != 3 || ac.Text != "reset works" || ac.Done {
		t.Fatalf("appended AC = %+v, want seq 3 'reset works' not done", ac)
	}

	// Whitespace-only text → 400; unknown task → 404.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
		"actor": actor("ada"), "text": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST ac empty text = %d, want 400 (body %s)", code, string(body))
	}
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/0734567890abcdefghij00000000/acceptance-criteria", map[string]any{
		"actor": actor("ada"), "text": "x",
	})
	if code != http.StatusNotFound {
		t.Fatalf("POST ac unknown task = %d, want 404 (body %s)", code, string(body))
	}

	// PATCH with "text" rewords the criterion → 200 updated row; toggle state untouched.
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/2", map[string]any{
		"actor": actor("ada"), "text": "logout works (hardened)",
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH ac text = %d, want 200 (body %s)", code, string(body))
	}
	ac = mustDecode[acDTO](t, body)
	if ac.Seq != 2 || ac.Text != "logout works (hardened)" || ac.Done {
		t.Fatalf("reworded AC = %+v, want seq 2 reworded, still not done", ac)
	}

	// A toggle on the same seq still works after a reword (no state corruption).
	code, body = doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/2", map[string]any{
		"actor": actor("ada"), "done": true,
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH ac toggle after reword = %d, want 200 (body %s)", code, string(body))
	}

	// DELETE with an actor body → 204; the row is gone and surviving seqs keep theirs.
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/3", map[string]any{
		"actor": actor("ada"),
	})
	if code != http.StatusNoContent {
		t.Fatalf("DELETE ac 3 = %d, want 204", code)
	}
	d := getDetail(t, stk, task.ID)
	if len(d.AcceptanceCriteria) != 2 || d.AcceptanceCriteria[1].Seq != 2 {
		t.Fatalf("detail ACs after delete = %+v, want 2 rows, seq 2 KEEPS its number", d.AcceptanceCriteria)
	}

	// DELETE without a body → 400 (the event needs an actor); unknown seq → 404.
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/2", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("DELETE ac no body = %d, want 400 (actor required)", code)
	}
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/99", map[string]any{
		"actor": actor("ada"),
	})
	if code != http.StatusNotFound {
		t.Fatalf("DELETE ac unknown seq = %d, want 404", code)
	}

	// get_task still exposes the reworded row and its done state.
	d = getDetail(t, stk, task.ID)
	if d.AcceptanceCriteria[1].Text != "logout works (hardened)" || !d.AcceptanceCriteria[1].Done {
		t.Fatalf("final AC seq 2 = %+v, want reworded + done", d.AcceptanceCriteria[1])
	}
	evKinds := make([]string, 0, len(d.Events))
	for _, e := range d.Events {
		evKinds = append(evKinds, e.Kind)
	}
	for _, wantKind := range []string{"ac-added", "ac-updated", "ac-toggled", "ac-removed"} {
		found := false
		for _, k := range evKinds {
			if k == wantKind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("detail events missing %q (saw %v)", wantKind, evKinds)
		}
	}
}

func TestArtifactAttachAndErrors(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Evidence")
	task := newTask(t, stk, p.ID, "evidenced work")

	// Attach → 201 fresh artifact.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
		"actor":   actor("agent-a"),
		"kind":    "url",
		"ref":     "https://example.com/reports/2026-09-26/deploy.json",
		"summary": "deploy report for the fix",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /artifacts = %d, want 201 (body %s)", code, string(body))
	}
	art := mustDecode[artifactDTO](t, body)
	if art.Kind != "url" || art.Ref == "" || art.Summary == "" || art.Actor.Name != "agent-a" || art.CreatedAt == "" {
		t.Fatalf("201 artifact = %+v, want url kind with ref+summary", art)
	}

	// Reflected in get_task's artifacts.
	d := getDetail(t, stk, task.ID)
	if len(d.Artifacts) != 1 || d.Artifacts[0].Ref != art.Ref || d.Artifacts[0].Summary != art.Summary {
		t.Fatalf("get_task artifacts = %+v, want the attached row", d.Artifacts)
	}

	// summary is optional.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
		"actor": actor("human-x"), "kind": "log", "ref": "/var/log/crowban/build-42.log",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /artifacts (no summary) = %d, want 201 (body %s)", code, string(body))
	}

	// Bad kind → 400; empty ref → 400; unknown task → 404.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
		"actor": actor("agent-a"), "kind": "photo", "ref": "x",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad artifact kind = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
		"actor": actor("agent-a"), "kind": "url", "ref": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("empty artifact ref = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/0734567890abcdefghij00000000/artifacts", map[string]any{
		"actor": actor("agent-a"), "kind": "url", "ref": "x",
	})
	if code != http.StatusNotFound {
		t.Fatalf("artifact on unknown task = %d, want 404", code)
	}
}

func TestRelationsLifecycle(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Graph")
	parent := newTask(t, stk, p.ID, "parent task")
	child := newTask(t, stk, p.ID, "child task")
	secondParent := newTask(t, stk, p.ID, "second parent")

	// Add child → 201 with the STORED edge (task_id=child, other_task_id=parent).
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": parent.ID, "kind": "child",
	})
	if code != http.StatusCreated {
		t.Fatalf("add child relation = %d, want 201 (body %s)", code, string(body))
	}
	rel := mustDecode[relationDTO](t, body)
	if rel.TaskID != child.ID || rel.OtherTaskID != parent.ID || rel.Kind != "child" {
		t.Fatalf("stored relation = %+v, want child→parent edge", rel)
	}

	// Reflected in get_task's relations.
	d := getDetail(t, stk, child.ID)
	if len(d.Relations) != 1 || d.Relations[0].TaskID != child.ID || d.Relations[0].OtherTaskID != parent.ID {
		t.Fatalf("get_task relations = %+v, want the child edge", d.Relations)
	}

	// Re-adding the SAME edge → 200 (idempotent upsert), same stored row.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": parent.ID, "kind": "child",
	})
	if code != http.StatusOK {
		t.Fatalf("re-add existing edge = %d, want 200 (body %s)", code, string(body))
	}
	if got := mustDecode[relationDTO](t, body); got.TaskID != child.ID || got.OtherTaskID != parent.ID {
		t.Fatalf("upsert response = %+v, want the existing stored row", got)
	}

	// A SECOND different parent → 409 parent conflict.
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": secondParent.ID, "kind": "child",
	})
	if code != http.StatusConflict {
		t.Fatalf("second parent = %d, want 409 (body %s)", code, string(body))
	}
	e := mustDecode[errResp](t, body)
	if e.Error != "parent conflict" || !strings.Contains(e.Message, "already has a parent") {
		t.Fatalf("409 parent-conflict body = %+v, want error \"parent conflict\" naming the existing parent", e)
	}

	// Self-relation → 400; invalid kind → 400; unknown task (either end) → 404.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": child.ID, "kind": "related",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("self-relation = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": parent.ID, "kind": "depends-on",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid relation kind = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": "0734567890abcdefghij00000000", "kind": "related",
	})
	if code != http.StatusNotFound {
		t.Fatalf("relation to unknown task = %d, want 404", code)
	}
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/0734567890abcdefghij00000000/relations", map[string]any{
		"actor": actor("eve"), "other_id": child.ID, "kind": "related",
	})
	if code != http.StatusNotFound {
		t.Fatalf("relation from unknown task = %d, want 404", code)
	}

	// Cross-project pair → 400.
	p2 := createProject(t, stk, "Other Project")
	outside := newTask(t, stk, p2.ID, "outside task")
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": outside.ID, "kind": "related",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("cross-project relation = %d, want 400", code)
	}

	// Remove → 204; the edge is gone from get_task.
	code, body = doReq(t, stk.Handler, "DELETE",
		"/api/v1/tasks/"+child.ID+"/relations/"+parent.ID+"/child",
		map[string]any{"actor": actor("eve")})
	if code != http.StatusNoContent {
		t.Fatalf("remove relation = %d, want 204 (body %s)", code, string(body))
	}
	d = getDetail(t, stk, child.ID)
	if len(d.Relations) != 0 {
		t.Fatalf("relations after remove = %+v, want empty", d.Relations)
	}

	// With the first parent gone, the second parent may now be added.
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+child.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": secondParent.ID, "kind": "child",
	})
	if code != http.StatusCreated {
		t.Fatalf("second parent after removal = %d, want 201", code)
	}

	// Removing a MISSING edge → 404 (the first-parent edge is gone).
	code, _ = doReq(t, stk.Handler, "DELETE",
		"/api/v1/tasks/"+child.ID+"/relations/"+parent.ID+"/child",
		map[string]any{"actor": actor("eve")})
	if code != http.StatusNotFound {
		t.Fatalf("remove missing edge = %d, want 404", code)
	}

	// Removal requires the actor in the body (the removal event records who).
	code, _ = doReq(t, stk.Handler, "DELETE",
		"/api/v1/tasks/"+child.ID+"/relations/"+secondParent.ID+"/child", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("remove without actor body = %d, want 400", code)
	}
	code, _ = doReq(t, stk.Handler, "DELETE",
		"/api/v1/tasks/"+child.ID+"/relations/"+secondParent.ID+"/child",
		map[string]any{"actor": actor("eve")})
	if code != http.StatusNoContent {
		t.Fatalf("remove with actor = %d, want 204", code)
	}
	d = getDetail(t, stk, child.ID)
	if len(d.Relations) != 0 {
		t.Fatalf("relations after final remove = %+v, want empty", d.Relations)
	}
}

func TestBoardSnapshot(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Board")

	low := makeTask(t, stk, p.ID, "low priority todo", map[string]any{"priority": 1})
	high := makeTask(t, stk, p.ID, "high priority todo", map[string]any{"priority": 5})
	doingT := newTask(t, stk, p.ID, "doing task")
	doneT := newTask(t, stk, p.ID, "done task")
	patchTask(t, stk, doingT.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, doneT.ID, 1, map[string]any{"status": "doing"})
	patchTask(t, stk, doneT.ID, 2, map[string]any{"status": "done"})

	// Badge fields: give "low priority todo" 3 criteria (1 done) and 2 comments;
	// every other board task keeps zero badge data (fields omitted on the wire).
	for _, text := range []string{"ac one", "ac two", "ac three"} {
		doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+low.ID+"/acceptance-criteria", map[string]any{
			"actor": actor("eve"), "text": text,
		})
	}
	if code, _ := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+low.ID+"/acceptance-criteria/1", map[string]any{
		"actor": actor("eve"), "done": true,
	}); code != http.StatusOK {
		t.Fatalf("toggle ac 1 = %d, want 200", code)
	}
	for i := 0; i < 2; i++ {
		doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+low.ID+"/comments", map[string]any{
			"actor": actor("eve"), "kind": "note", "body": "board badge comment",
		})
	}

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

	// AC1/AC2/AC3: per-card badge fields ride on the summary rows.
	findRow := func(id string) *taskSummaryDTO {
		for i := range b.Tasks.Todo {
			if b.Tasks.Todo[i].ID == id {
				return &b.Tasks.Todo[i]
			}
		}
		return nil
	}
	lowRow := findRow(low.ID)
	if lowRow == nil {
		t.Fatalf("low row missing from board todo column")
	}
	if lowRow.AcTotal != 3 || lowRow.AcDone != 1 {
		t.Fatalf("low ac badge = %d/%d, want 1/3", lowRow.AcDone, lowRow.AcTotal)
	}
	if lowRow.CommentCount != 2 {
		t.Fatalf("low comment badge = %d, want 2", lowRow.CommentCount)
	}
	for _, col := range [][]taskSummaryDTO{b.Tasks.Todo, b.Tasks.Doing, b.Tasks.Done} {
		for i := range col {
			row := &col[i]
			if row.ID == low.ID {
				continue
			}
			if row.AcTotal != 0 || row.AcDone != 0 || row.CommentCount != 0 {
				t.Fatalf("task %s badge fields = %+v, want all zero (omitted)", row.ID, *row)
			}
		}
	}

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

func TestDeleteEndpoints_ActorHeadersAndQueryParams(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "RFC 9110 Tests")

	// Create a task with 4 ACs
	task := makeTask(t, stk, p.ID, "Delete Tests", map[string]any{
		"acceptance_criteria": []string{"AC 1", "AC 2", "AC 3", "AC 4"},
	})

	// 1. DELETE AC with headers (X-Actor-Name, X-Actor-Type), nil body → 204
	req, err := http.NewRequest("DELETE", "http://ravn.test/api/v1/tasks/"+task.ID+"/acceptance-criteria/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Actor-Name", "alice")
	req.Header.Set("X-Actor-Type", "agent")
	res := httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("DELETE AC 1 with headers = %d, want 204 (body %s)", res.Code, res.Body.String())
	}
	d := getDetail(t, stk, task.ID)
	if len(d.AcceptanceCriteria) != 3 {
		t.Fatalf("AC count after delete = %d, want 3", len(d.AcceptanceCriteria))
	}
	// Verify last event has alice agent attribution
	lastEv := d.Events[0]
	if lastEv.Kind != "ac-removed" || lastEv.Actor.Name != "alice" || lastEv.Actor.Type != "agent" {
		t.Fatalf("event = %+v, want ac-removed by alice/agent", lastEv)
	}

	// 2. DELETE AC with query parameters (?actor_name=bob&actor_type=human), nil body → 204
	req, err = http.NewRequest("DELETE", "http://ravn.test/api/v1/tasks/"+task.ID+"/acceptance-criteria/2?actor_name=bob&actor_type=human", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res = httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("DELETE AC 2 with query params = %d, want 204 (body %s)", res.Code, res.Body.String())
	}
	d = getDetail(t, stk, task.ID)
	if len(d.AcceptanceCriteria) != 2 {
		t.Fatalf("AC count after delete = %d, want 2", len(d.AcceptanceCriteria))
	}
	lastEv = d.Events[0]
	if lastEv.Kind != "ac-removed" || lastEv.Actor.Name != "bob" || lastEv.Actor.Type != "human" {
		t.Fatalf("event = %+v, want ac-removed by bob/human", lastEv)
	}

	// 3. DELETE AC with JSON body (backward compatibility) → 204
	code, _ := doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/3", map[string]any{
		"actor": actor("charlie"),
	})
	if code != http.StatusNoContent {
		t.Fatalf("DELETE AC 3 with body = %d, want 204", code)
	}

	// 4. DELETE AC with empty body and no headers/params → 400
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/acceptance-criteria/4", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("DELETE AC 4 without actor = %d, want 400", code)
	}

	// Test relations
	t2 := newTask(t, stk, p.ID, "Related Task")

	// Add edge
	code, _ = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("setup"), "other_id": t2.ID, "kind": "blocked-by",
	})
	if code != http.StatusCreated {
		t.Fatalf("add relation = %d, want 201", code)
	}

	// 5. DELETE relation with headers, nil body → 204
	req, err = http.NewRequest("DELETE", "http://ravn.test/api/v1/tasks/"+task.ID+"/relations/"+t2.ID+"/blocked-by", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Actor-Name", "alice")
	req.Header.Set("X-Actor-Type", "agent")
	res = httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("DELETE relation with headers = %d, want 204 (body %s)", res.Code, res.Body.String())
	}
	d = getDetail(t, stk, task.ID)
	if len(d.Relations) != 0 {
		t.Fatalf("relations = %+v, want empty", d.Relations)
	}

	// Re-add edge
	doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("setup"), "other_id": t2.ID, "kind": "blocked-by",
	})

	// 6. DELETE relation with query parameters, nil body → 204
	req, err = http.NewRequest("DELETE", "http://ravn.test/api/v1/tasks/"+task.ID+"/relations/"+t2.ID+"/blocked-by?actor_name=bob&actor_type=human", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res = httptest.NewRecorder()
	stk.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("DELETE relation with query params = %d, want 204 (body %s)", res.Code, res.Body.String())
	}
	d = getDetail(t, stk, task.ID)
	if len(d.Relations) != 0 {
		t.Fatalf("relations = %+v, want empty", d.Relations)
	}

	// Re-add edge
	doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("setup"), "other_id": t2.ID, "kind": "blocked-by",
	})

	// 7. DELETE relation with JSON body (backward compatibility) → 204
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/relations/"+t2.ID+"/blocked-by", map[string]any{
		"actor": actor("charlie"),
	})
	if code != http.StatusNoContent {
		t.Fatalf("DELETE relation with body = %d, want 204", code)
	}

	// Re-add edge
	doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("setup"), "other_id": t2.ID, "kind": "blocked-by",
	})

	// 8. DELETE relation without body, headers, or query params → 400
	code, _ = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+task.ID+"/relations/"+t2.ID+"/blocked-by", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("DELETE relation without actor = %d, want 400", code)
	}
}

func TestAddRelation_WithHeavyChildCollections(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "Heavy Project")
	task := newTask(t, stk, p.ID, "Heavy Task")
	other := newTask(t, stk, p.ID, "Other Task")

	// Add multiple comments, ACs, artifacts to make task detail heavy
	for i := 1; i <= 5; i++ {
		doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
			"actor": actor("eve"), "content": "Comment content",
		})
		doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/acceptance-criteria", map[string]any{
			"actor": actor("eve"), "text": "Acceptance criterion",
		})
		doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/artifacts", map[string]any{
			"actor": actor("eve"), "kind": "log", "ref": "log.txt", "summary": "test log",
		})
	}

	// 1. First add: brand new relation returns 201 Created
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": other.ID, "kind": "related",
	})
	if code != http.StatusCreated {
		t.Fatalf("first relation add = %d, want 201 (body %s)", code, string(body))
	}
	rel := mustDecode[relationDTO](t, body)
	if rel.TaskID != task.ID || rel.OtherTaskID != other.ID || rel.Kind != "related" {
		t.Fatalf("unexpected relation returned: %+v", rel)
	}

	// 2. Second add of identical relation: idempotent returns 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+task.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": other.ID, "kind": "related",
	})
	if code != http.StatusOK {
		t.Fatalf("second relation add = %d, want 200 (body %s)", code, string(body))
	}
	rel2 := mustDecode[relationDTO](t, body)
	if rel2.TaskID != rel.TaskID || rel2.OtherTaskID != rel.OtherTaskID || rel2.CreatedAt != rel.CreatedAt {
		t.Fatalf("expected same stored relation %+v, got %+v", rel, rel2)
	}

	// 3. Parent alias add: returns 201 Created, stored as child
	third := newTask(t, stk, p.ID, "Parent Task")
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+third.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": task.ID, "kind": "parent",
	})
	if code != http.StatusCreated {
		t.Fatalf("parent relation add = %d, want 201 (body %s)", code, string(body))
	}
	rel3 := mustDecode[relationDTO](t, body)
	if rel3.TaskID != task.ID || rel3.OtherTaskID != third.ID || rel3.Kind != "child" {
		t.Fatalf("expected stored inverse child relation, got: %+v", rel3)
	}

	// 4. Re-add parent alias: returns 200 OK
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+third.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": task.ID, "kind": "parent",
	})
	if code != http.StatusOK {
		t.Fatalf("parent re-add = %d, want 200 (body %s)", code, string(body))
	}
}
