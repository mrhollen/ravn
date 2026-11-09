package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// duplicateAdvisoryLimit is how many similar open tasks POST /tasks advertises
// as possible duplicates (slice 3a contract; the use case clamps higher asks).
const duplicateAdvisoryLimit = 5

// handleListTasks → GET /api/v1/tasks?filters. Returns summary rows in
// canonical order plus `total` (before paging) and the effective limit/offset.
//
// Multi-user epic T4: ?mine=assigned|created|activity restricts the list to the
// ACTING identity's assignments / creations / event activity (additive — omitting
// it leaves the query byte-identical); under membership scoping the list is
// additionally visibility-scoped server-side. Owner rows are batch-resolved after
// the build (one lookup, no N+1) and omitted everywhere attribution is absent.
func (a *App) handleListTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f, err := parseTaskFilter(q)
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	dn := declaredName(r) // header/query-declared actor (no body on list GETs)
	_, empty, merr := a.applyMine(ctx, q, dn, &f) // fills Creator/AssigneeName/ActivityActor; "" = untouched filter
	if merr != nil {
		writeAppError(w, merr, "task", "")
		return
	}
	if empty { // structurally empty (anonymous under membership scoping): skip the query entirely
		writeJSON(w, http.StatusOK, map[string]any{
			"tasks":  []taskSummaryDTO{},
			"total":  0,
			"limit":  f.Limit,
			"offset": f.Offset,
		})
		return
	}
	f.Visibility = a.listScope(ctx, dn) // nil in default mode → unchanged query

	rows, total, err := a.Tasks.List(ctx, f)
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	out := make([]taskSummaryDTO, 0, len(rows))
	for _, t := range rows {
		out = append(out, toSummary(t))
	}
	if err := a.fillTaskOwners(ctx, out); err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":  out,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
}

// applyMine parses and validates ?mine= for the TASK list endpoint (projects
// support only "created"; tasks support all three) and fills the matching filter
// field IN PLACE. The returned token is "" when the parameter is absent (filter
// untouched); `empty` is true when the request can ONLY return zero rows (an
// anonymous principal under membership scoping asking for its assignments or
// activity — nothing is ever stored under an empty name), letting the caller skip
// the query entirely. Precedence: an explicit conflicting ?assignee_name/?assignee
// alongside ?mine=assigned is a 400 (ambiguous), never a silent override.
func (a *App) applyMine(ctx context.Context, q url.Values, declaredName string, f *application.TaskFilter) (string, bool, error) {
	mine, err := parseMine(q)
	if err != nil {
		return "", false, err
	}
	switch mine {
	case "":
		return "", false, nil
	case "assigned":
		if q.Get("assignee_name") != "" || q.Get("assignee") != "" {
			return "", false, fmt.Errorf("%w: ?mine=assigned conflicts with an explicit assignee filter", domain.ErrInvalid)
		}
		name, nerr := a.actorFilterName(ctx, declaredName)
		if nerr != nil {
			return "", false, nerr
		}
		if name == "" { // anonymous under membership scoping: structurally empty
			return mine, true, nil
		}
		f.AssigneeName = name
	case "created":
		creator, cerr := a.creatorFilterID(ctx, declaredName)
		if cerr != nil {
			return "", false, cerr
		}
		f.Creator = creator // zero-id (unresolvable actor) matches nothing in SQL — honest empty list
	case "activity":
		name, nerr := a.actorFilterName(ctx, declaredName)
		if nerr != nil {
			return "", false, nerr
		}
		if name == "" { // anonymous under membership scoping: structurally empty
			return mine, true, nil
		}
		f.ActivityActor = name
	default:
		return "", false, fmt.Errorf("%w: ?mine for tasks supports \"assigned\", \"created\" or \"activity\"", domain.ErrInvalid)
	}
	return mine, false, nil
}

// handleCreateTask → POST /api/v1/tasks. 201 with the fresh task (summary row)
// plus advisory possible_duplicates from the same project — the new task's own
// id is always excluded (its title trivially matches itself). The duplicate
// scan is best-effort: a failure after a successful create logs and returns an
// empty advisory set rather than hiding the created task.
func (a *App) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req createTaskReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}

	// Boundary format checks on every ID field BEFORE the use case — malformed
	// ids (including oversized ones) 400 here and never reach storage.
	projectID, err := domain.ParseID(req.ProjectID)
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	var parentID domain.ID // "" = no parent edge
	if p := strings.TrimSpace(req.ParentID); p != "" {
		parentID, err = domain.ParseID(p)
		if err != nil {
			writeAppError(w, err, "task", "")
			return
		}
	}
	in := application.TaskInput{
		Title:       req.Title,
		Description: req.Description,
		ProjectID:   projectID,
		Priority:    domain.Priority(req.Priority),
		ACs:         req.AcceptanceCriteria,
		ParentID:    parentID,
	}
	for _, b := range req.BlockedBy {
		bid, err := domain.ParseID(strings.TrimSpace(b)) // blank/malformed entry → 400 here (was rejected upstream)
		if err != nil {
			writeAppError(w, err, "task", "")
			return
		}
		in.BlockedBy = append(in.BlockedBy, bid)
	}
	if req.DueAt != nil {
		t, err := parseTimeParam(*req.DueAt, "due_at")
		if err != nil {
			writeAppError(w, err, "task", "")
			return
		}
		in.DueAt = t
	}
	if req.Assignee != nil {
		as, err := req.Assignee.toDomain()
		if err != nil {
			writeAppError(w, err, "task", "")
			return
		}
		in.Assignee = &as
	}

	// T4: OpWrite gate on the OWNING project (zero queries in default mode — the
	// use case keeps its pre-T4 404 for unknown projects), spoof-proofed acting
	// actor, and created_by attribution for credential principals (any scoping
	// mode; operators/off-mode stays unattributed exactly as before).
	if _, ok := a.gateProject(w, r, actor.Name, projectID, domain.OpWrite); !ok {
		return
	}
	actor = a.authoritativeActor(ctx, actor)
	reqPr := a.requester(ctx, actor.Name)
	if !reqPr.operator && reqPr.subject.Enabled() && reqPr.subject.ID != "" {
		id := reqPr.subject.ID
		in.CreatedBy = &id
	}

	task, err := a.Tasks.Create(ctx, actor, in)
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}

	dups := make([]taskSummaryDTO, 0)
	cands, cErr := a.Tasks.FindDuplicateCandidates(ctx, task.ProjectID, task.Title, duplicateAdvisoryLimit)
	if cErr != nil {
		slog.Warn("duplicate scan failed after task create; returning empty advisory set",
			"err", cErr, "task", string(task.ID))
	} else {
		for _, c := range cands {
			if c.ID == task.ID { // never warn about the task that was just created
				continue
			}
			dups = append(dups, toSummary(c))
		}
	}
	resp := createTaskResponse{Task: toSummary(task), PossibleDuplicates: dups}
	// T4 owner display (no-ops unless attribution is present): the fresh task row
	// and its advisory duplicates, each in one batched lookup.
	single := []taskSummaryDTO{resp.Task}
	if oerr := a.fillTaskOwners(ctx, single); oerr != nil {
		writeAppError(w, oerr, "task", task.ID)
		return
	}
	resp.Task = single[0]
	if len(resp.PossibleDuplicates) > 0 {
		if oerr := a.fillTaskOwners(ctx, resp.PossibleDuplicates); oerr != nil {
			writeAppError(w, oerr, "task", task.ID)
			return
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleGetTask → GET /api/v1/tasks/{id}. One-call resume payload. Optional
// query params: comment_limit (default 10, max 50) and event_limit (default
// 20, max 100) — 0/negative select the default; values above the cap are
// clamped by the use case.
func (a *App) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	commentLimit, err := parseTailLimit(q.Get("comment_limit"), "comment_limit")
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	eventLimit, err := parseTailLimit(q.Get("event_limit"), "event_limit")
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	ctx := r.Context()
	// T4: visibility gate on a light summary fetch BEFORE the heavy resume payload
	// (zero extra queries in default mode; membership mode rejects invisible tasks
	// without pulling comments/artifacts/events).
	t, terr := a.Tasks.Get(ctx, id)
	if terr != nil {
		writeAppError(w, terr, "task", id)
		return
	}
	if !a.checkTask(w, r, declaredName(r), t, domain.OpRead) {
		return
	}
	d, err := a.Tasks.GetDetail(ctx, id, commentLimit, eventLimit)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	out := toDetail(d) // embeds taskSummaryDTO — CreatedBy/Owner are promoted fields
	single := []taskSummaryDTO{out.taskSummaryDTO} // T4 owner display (no-op unless attributed)
	if oerr := a.fillTaskOwners(ctx, single); oerr != nil {
		writeAppError(w, oerr, "task", id)
		return
	}
	out.taskSummaryDTO = single[0]
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateTask → PATCH /api/v1/tasks/{id}. Version-guarded partial update:
// the body must carry `version` (the stored version the caller last read).
// Success → 200 with the fresh task (summary row, incremented version); a stale
// version → 409 whose body carries current_version AND the fresh current task.
//
// Where the guard lives: req.Version is passed to the use case as
// TaskUpdateInput.ExpectedVersion, and Tasks.Update checks it INSIDE its write
// transaction — that in-transaction check is the AUTHORITATIVE one, so a
// concurrent writer that commits between this handler's read and the use case's
// write can never slip past it (no lost updates; see the race test in
// occ_test.go). The pre-read below is only a fast-fail for obviously stale
// requests: it renders the identical 409 body (the contract the web client's
// moveTaskStatus retry parses) without opening the write transaction. It is NOT
// what makes the guard correct under concurrency.
func (a *App) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateTaskReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	if req.Version < 1 { // tasks start at version 1, so 0 always means "missing"
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   ErrCodeInvalidInput,
			"message": "version is required: the stored version of the task you last read (fresh tasks are version 1)",
		})
		return
	}

	cur, err := a.Tasks.Get(ctx, id)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	if !a.checkTask(w, r, actor.Name, cur, domain.OpWrite) { // T4: gate on the pre-read already fetched (zero queries in default mode)
		return
	}
	if cur.Version != req.Version { // stale read → the fresh row IS the recovery instruction
		writeVersionConflict(w, id, cur)
		return
	}

	in := application.TaskUpdateInput{
		// The caller's last-read version — validated authoritatively INSIDE the
		// use case's write transaction (the pre-check above is a fast-fail only).
		ExpectedVersion:  req.Version,
		ClearDue:         req.ClearDue,
		ClearAssignee:    req.ClearAssignee,
		OverrideBlockers: req.OverrideBlockers,
	}
	if req.Title != nil {
		in.Title = req.Title
	}
	if req.Description != nil {
		in.Description = req.Description
	}
	if req.Status != nil {
		s := domain.Status(*req.Status) // validated by the use case (400 / transition rules)
		in.Status = &s
	}
	if req.Priority != nil {
		p := domain.Priority(*req.Priority) // range-validated by the use case
		in.Priority = &p
	}
	if req.DueAt != nil {
		t, err := parseTimeParam(*req.DueAt, "due_at")
		if err != nil {
			writeAppError(w, err, "task", id)
			return
		}
		in.DueAt = t
	}
	if req.Assignee != nil {
		as, err := req.Assignee.toDomain()
		if err != nil {
			writeAppError(w, err, "task", id)
			return
		}
		in.Assignee = &as
	}
	if req.Archived != nil {
		in.Archived = req.Archived
	}

	fresh, err := a.Tasks.Update(ctx, a.authoritativeActor(ctx, actor), id, in) // T4 spoof-proofing
	if err != nil {
		writeAppError(w, err, "task", id) // *ConflictError → 409 with current_version + current task
		return
	}
	out := toSummary(fresh)
	single := []taskSummaryDTO{out} // T4 owner display (no-op unless attributed)
	if oerr := a.fillTaskOwners(ctx, single); oerr != nil {
		writeAppError(w, oerr, "task", id)
		return
	}
	writeJSON(w, http.StatusOK, single[0])
}
