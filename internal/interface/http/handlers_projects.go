package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// handleListProjects → GET /api/v1/projects. Rows carry per-status open task
// counts (ARCHITECTURE §5: "rows + per-status task counts"), computed by the
// Projects use case from ONE grouped query — no N+1.
func (a *App) handleListProjects(w http.ResponseWriter, r *http.Request) {
	archived := parseBoolFlag(r.URL.Query(), "archived")
	rows, err := a.Projects.ListWithCounts(r.Context(), application.ProjectFilter{Archived: archived})
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	out := make([]projectRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectRowDTO{
			projectDTO: toProject(row.Project),
			TaskCounts: toCounts(row.OpenTasks),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "total": len(out)})
}

// handleCreateProject → POST /api/v1/projects. 201 with the fresh project.
func (a *App) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	p, err := a.Projects.Create(r.Context(), actor, req.Name, req.Description)
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	writeJSON(w, http.StatusCreated, toProject(p))
}

// handleGetProject → GET /api/v1/projects/{id}. 404 for unknown ids.
func (a *App) handleGetProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := a.Projects.Get(r.Context(), id)
	if err != nil {
		writeAppError(w, err, "project", id)
		return
	}
	writeJSON(w, http.StatusOK, toProject(p))
}

// handleUpdateProject → PUT /api/v1/projects/{id}. Partial rename and/or
// description replace; an update with no fields set is a 400.
func (a *App) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateProjectReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "project", id)
		return
	}
	p, err := a.Projects.Update(r.Context(), actor, id, application.ProjectUpdate{
		Name:              req.Name,
		Description:       req.Description,
		OpenCodeServerURL: req.OpenCodeServerURL,
		OpenCodeDirectory: req.OpenCodeDirectory,
		OpenCodeModel:     req.OpenCodeModel,
		OpenCodeAgent:     req.OpenCodeAgent,
		OpenCodeVariant:   req.OpenCodeVariant,
		Archived:          req.Archived,
	})
	if err != nil {
		writeAppError(w, err, "project", id)
		return
	}
	writeJSON(w, http.StatusOK, toProject(p))
}

// handleBoard → GET /api/v1/projects/{id}/board (UI extra): the whole project
// board in ONE round-trip — open tasks grouped by status as summary rows, each
// column in canonical order, plus per-status counts. Unknown project → 404.
func (a *App) handleBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.Projects.Get(ctx, pid); err != nil { // 404 before touching the task table
		writeAppError(w, err, "project", pid)
		return
	}
	tasks, err := a.allOpenTasks(ctx, pid)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}

	// Per-card progress badges: (total, done) acceptance-criterion aggregates
	// and per-task comment stats (count + most-recent kind/actor) for EVERY
	// board row in ONE grouped query per child table — no N+1. Missing map key
	// = zero rows; the DTO fields stay omitted.
	ids := make([]domain.ID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	acProgress, err := a.Acceptance.Progress(ctx, ids)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	commentStats, err := a.Comments.Stats(ctx, ids)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	subtaskProgress, err := a.Relations.SubtaskProgress(ctx, ids)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	blockersByTask, err := a.Tasks.OpenBlockers(ctx, ids)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}

	resp := boardResponse{
		ProjectID: string(pid),
		Tasks: boardColumns{
			Todo:  make([]taskSummaryDTO, 0),
			Doing: make([]taskSummaryDTO, 0),
			Done:  make([]taskSummaryDTO, 0),
		},
	}
	for _, t := range tasks { // canonical-ordered input keeps each column ordered
		s := toSummary(t)
		if p, ok := acProgress[t.ID]; ok {
			s.AcTotal = int(p.Total)
			s.AcDone = int(p.Done)
		}
		if st, ok := commentStats[t.ID]; ok {
			s.CommentCount = int(st.Count)
			s.LastCommentKind = string(st.LastKind)
			s.LastCommentActor = toActor(&st.LastActor)
		}
		if sp, ok := subtaskProgress[t.ID]; ok && sp.Total > 0 {
			s.SubtaskTotal = int(sp.Total)
			s.SubtaskDone = int(sp.Done)
		}
		if blks, ok := blockersByTask[t.ID]; ok && len(blks) > 0 {
			dtos := make([]blockerDTO, 0, len(blks))
			for _, b := range blks {
				dtos = append(dtos, blockerDTO{
					ID: b.ID.String(), Title: b.Title, Status: string(b.Status),
				})
			}
			s.Blockers = dtos
		}
		switch t.Status {
		case domain.StatusTodo:
			resp.Tasks.Todo = append(resp.Tasks.Todo, s)
		case domain.StatusDoing:
			resp.Tasks.Doing = append(resp.Tasks.Doing, s)
		case domain.StatusDone:
			resp.Tasks.Done = append(resp.Tasks.Done, s)
		default: // non-canonical statuses (Tier-2 inserts) have no MVP board column
			continue
		}
	}
	resp.Counts = statusCountDTO{
		Todo:  len(resp.Tasks.Todo),
		Doing: len(resp.Tasks.Doing),
		Done:  len(resp.Tasks.Done),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleProjectReport → GET /api/v1/projects/{id}/report (UI + supervisor
// extra): the single-call project health digest — status/priority counts, live
// and expired claim leases, stalled doing-tasks, bottleneck blockers (both
// directions), recently completed tasks, and per-assignee workload. Optional
// query parameters tune the windows; each is omitted by default:
//
//	stalled_minutes  non-negative integer; a doing task with NO activity for
//	                 MORE than this many minutes counts as stalled
//	                 (default 60 when omitted or zero)
//	since            RFC3339 instant; recently_completed shows tasks finished at
//	                 or after it (default now−24h)
//
// Everything is computed by the use case in ONE unit of work, so the digest is
// a consistent snapshot. Unknown project → 404; malformed parameter → 400.
func (a *App) handleProjectReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	stalledMinutes, err := parseTailLimit(q.Get("stalled_minutes"), "stalled_minutes")
	if err != nil {
		writeAppError(w, err, "project", pid)
		return
	}
	var since *time.Time
	if v := q.Get("since"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			writeAppError(w, fmt.Errorf("%w: since must be an RFC3339 timestamp, got %q", domain.ErrInvalid, v), "project", pid)
			return
		}
		since = &t
	}
	rep, err := a.Projects.Report(ctx, pid, application.ReportOptions{StalledMinutes: stalledMinutes, Since: since})
	if err != nil {
		writeAppError(w, err, "project", pid)
		return
	}
	writeJSON(w, http.StatusOK, toProjectReport(rep))
}
