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
//
// Multi-user epic T4: ?mine=created restricts to projects the acting identity
// CREATED (the only mine token for projects); under membership scoping the list
// is additionally visibility-scoped server-side (owner OR stored member). In
// default/global mode neither clause fires — the query and response are
// byte-identical to pre-T4. Owner rows are batch-resolved AFTER the list build
// (one lookup, no N+1) and omitted everywhere attribution is absent.
func (a *App) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	archived := parseBoolFlag(q, "archived")

	flt := application.ProjectFilter{Archived: archived}
	mine, err := parseMine(q)
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	if mine != "" { // projects support only ?mine=created (tasks support assigned/activity too)
		if mine != "created" {
			writeAppError(w, fmt.Errorf("%w: ?mine for projects supports only \"created\"", domain.ErrInvalid), "project", "")
			return
		}
		actorIn, _ := extractActor(r, actorInput{})
		creator, cerr := a.creatorFilterID(ctx, actorIn.Name)
		if cerr != nil {
			writeAppError(w, cerr, "project", "")
			return
		}
		flt.Creator = creator
	}
	flt.Visibility = a.listScope(ctx, declaredName(r)) // nil in default mode → unchanged query

	rows, err := a.Projects.ListWithCounts(ctx, flt)
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
	if err := a.fillOwners(ctx, func(i int) string { return out[i].CreatedBy }, func(i int, o *actorDTO) { out[i].Owner = o }, len(out)); err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "total": len(out)})
}

// declaredName extracts the acting-actor NAME from a request's standard channels
// (body-declared actors are passed explicitly by handlers that decode one first;
// this is the header/query fallback for list/GET endpoints with no body).
func declaredName(r *http.Request) string {
	in, _ := extractActor(r, actorInput{})
	return in.Name
}

// handleCreateProject → POST /api/v1/projects. 201 with the fresh project.
//
// Multi-user epic T4: under membership scoping a project may be created only by
// the shared-token OPERATOR (always allowed) or by a CREDENTIAL principal whose
// identity is enabled AND for which RAVN_IDENTITY_OPEN admits new creators —
// anyone else gets 403 forbidden. When the acting principal is a credential
// identity, the new project's created_by is stamped with that identity (making
// it the Owner); operator/unauthenticated creates stay unattributed exactly as
// before (legacy byte-identical rows).
func (a *App) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req createProjectReq
	if !decodeJSON(w, r, &req) {
		return
	}
	actor, err := req.Actor.toDomain()
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}

	// T4 attribution + creation gate. A CREDENTIAL principal (session/api_token —
	// only present in multiuser mode) always has created_by stamped, in EVERY
	// scoping mode: attribution is a property of WHO authenticated, not of the
	// visibility policy. Under membership scoping that credential must ADDITIONALLY
	// pass the identity-open gate (anonymous/operator creates stay unattributed
	// exactly as pre-T4; legacy rows byte-identical).
	owner := (*domain.ID)(nil)
	reqPr := a.requester(ctx, actor.Name)
	if !reqPr.operator && reqPr.subject.Enabled() && reqPr.subject.ID != "" {
		if a.scopingActive() && !a.IdentityOpen {
			writeJSONError(w, http.StatusForbidden, map[string]any{
				"error":    ErrCodeForbidden,
				"resource": "project",
				"message":  "creating projects requires the shared operator token or a resolved enabled identity (identity-open deployments)",
			})
			return
		}
		id := reqPr.subject.ID
		owner = &id // credential principal: durable ownership of what it creates
	}
	p, err := a.Projects.CreateAs(ctx, actor, req.Name, req.Description, owner)
	if err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	out := toProject(p)
	if err := a.fillOwners(ctx, func(i int) string { return out.CreatedBy }, func(i int, o *actorDTO) { out.Owner = o }, 1); err != nil {
		writeAppError(w, err, "project", "")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetProject → GET /api/v1/projects/{id}. 404 for unknown ids.
// T4: visibility gate on the already-fetched row (checkProject short-circuits with
// zero lookups in default mode) + owner display fill.
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
	if _, _, ok := a.checkProject(w, r, declaredName(r), p, domain.OpRead); !ok {
		return
	}
	out := toProject(p)
	if err := a.fillProjectOwners(r.Context(), []projectDTO{out}); err != nil {
		writeAppError(w, err, "project", id)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	// T4: OpWrite gate (zero queries in default mode — the use case keeps its
	// pre-T4 404 for unknown ids), then spoof-proofed acting actor.
	if _, ok := a.gateProject(w, r, actor.Name, id, domain.OpWrite); !ok {
		return
	}
	actor = a.authoritativeActor(r.Context(), actor)
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
	out := toProject(p)
	if err := a.fillProjectOwners(r.Context(), []projectDTO{out}); err != nil {
		writeAppError(w, err, "project", id)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	p, err := a.Projects.Get(ctx, pid) // 404 before touching the task table
	if err != nil {
		writeAppError(w, err, "project", pid)
		return
	}
	if _, _, ok := a.checkProject(w, r, declaredName(r), p, domain.OpRead); !ok { // T4: visibility gate (zero lookups in default mode)
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
	// T4 owner display: per-column batched fill (each call no-ops unless the column
	// carries created_by rows, so unattributed boards stay byte-identical).
	if err := a.fillTaskOwners(ctx, resp.Tasks.Todo); err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	if err := a.fillTaskOwners(ctx, resp.Tasks.Doing); err != nil {
		writeAppError(w, err, "task", pid)
		return
	}
	if err := a.fillTaskOwners(ctx, resp.Tasks.Done); err != nil {
		writeAppError(w, err, "task", pid)
		return
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
		t, perr := parseTimeParam(v, "since")
		if perr != nil {
			writeAppError(w, perr, "project", pid)
			return
		}
		since = t
	}
	// T4: OpRead visibility gate BEFORE the heavy report snapshot (zero queries in
	// default mode; membership mode rejects invisible projects without running it).
	if _, ok := a.gateProject(w, r, declaredName(r), pid, domain.OpRead); !ok {
		return
	}
	rep, err := a.Projects.Report(ctx, pid, application.ReportOptions{StalledMinutes: stalledMinutes, Since: since})
	if err != nil {
		writeAppError(w, err, "project", pid)
		return
	}
	writeJSON(w, http.StatusOK, toProjectReport(rep))
}
