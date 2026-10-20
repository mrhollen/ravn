package httpapi

import (
	"net/http"

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
	// and comment counts for EVERY board row in ONE grouped query per child
	// table — no N+1. Missing map key = zero rows; the DTO fields stay omitted.
	ids := make([]domain.ID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	acProgress, err := a.Acceptance.Progress(ctx, ids)
	if err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	commentCounts, err := a.Comments.Counts(ctx, ids)
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
		if n, ok := commentCounts[t.ID]; ok {
			s.CommentCount = int(n)
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
