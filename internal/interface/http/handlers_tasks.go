package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// duplicateAdvisoryLimit is how many similar open tasks POST /tasks advertises
// as possible duplicates (slice 3a contract; the use case clamps higher asks).
const duplicateAdvisoryLimit = 5

// handleListTasks → GET /api/v1/tasks?filters. Returns summary rows in
// canonical order plus `total` (before paging) and the effective limit/offset.
func (a *App) handleListTasks(w http.ResponseWriter, r *http.Request) {
	f, err := parseTaskFilter(r.URL.Query())
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	rows, total, err := a.Tasks.List(r.Context(), f)
	if err != nil {
		writeAppError(w, err, "task", "")
		return
	}
	out := make([]taskSummaryDTO, 0, len(rows))
	for _, t := range rows {
		out = append(out, toSummary(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":  out,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
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

	in := application.TaskInput{
		Title:       req.Title,
		Description: req.Description,
		ProjectID:   domain.ID(req.ProjectID),
		Priority:    domain.Priority(req.Priority),
		ACs:         req.AcceptanceCriteria,
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
	writeJSON(w, http.StatusCreated, createTaskResponse{Task: toSummary(task), PossibleDuplicates: dups})
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
	d, err := a.Tasks.GetDetail(r.Context(), id, commentLimit, eventLimit)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	writeJSON(w, http.StatusOK, toDetail(d))
}

// handleUpdateTask → PATCH /api/v1/tasks/{id}. Version-guarded partial update:
// the body must carry `version` (the stored version the caller last read).
// Success → 200 with the fresh task (summary row, incremented version); a stale
// version → 409 whose body carries current_version AND the fresh current task.
//
// Where the guard lives: the Tasks use case deliberately rebases onto the LATEST
// stored row (its internal CAS then covers races inside its own write — ADR
// "next.Version = cur.Version"), so the caller's expected version is enforced
// at this adapter edge, per §5's PATCH contract: read the current row first,
// reject on mismatch before anything is written.
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
			"error":   "invalid input",
			"message": "version is required: the stored version of the task you last read (fresh tasks are version 1)",
		})
		return
	}

	cur, err := a.Tasks.Get(ctx, id)
	if err != nil {
		writeAppError(w, err, "task", id)
		return
	}
	if cur.Version != req.Version { // stale read → the fresh row IS the recovery instruction
		writeVersionConflict(w, id, cur)
		return
	}

	in := application.TaskUpdateInput{
		ClearDue:      req.ClearDue,
		ClearAssignee: req.ClearAssignee,
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

	fresh, err := a.Tasks.Update(ctx, actor, id, in)
	if err != nil {
		writeAppError(w, err, "task", id) // *ConflictError → 409 with current_version + current task
		return
	}
	writeJSON(w, http.StatusOK, toSummary(fresh))
}
