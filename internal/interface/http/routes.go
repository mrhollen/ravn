package httpapi

import "net/http"

const apiPrefix = "/api/v1"

// RegisterRoutes mounts the REST surface on mux. Patterns use Go 1.22's
// method+wildcard syntax, so the stdlib mux answers unknown methods with a
// proper 405 (Allow header) for free. This is the single place to read the
// whole API:
//
//	GET   /api/v1/projects              list projects + per-status open task counts
//	POST  /api/v1/projects              create project → 201
//	GET   /api/v1/projects/{id}         get project (404 when unknown)
//	PUT   /api/v1/projects/{id}         update name/description
//	GET   /api/v1/projects/{id}/opencode/options  list reachable OpenCode agents and models for project
//	GET   /api/v1/projects/{id}/board   whole-project board snapshot (UI extra)
//	GET   /api/v1/projects/{id}/report  single-call project health digest: status/priority counts, live+expired claims, stalled doing-tasks, bottleneck blockers both directions, recently completed, per-assignee workload; ?stalled_minutes= & ?since= tune the windows (defaults 60 / now−24h)
//	GET   /api/v1/projects/{id}/events  realtime SSE stream for the project; replays events missed since Last-Event-ID (header or ?last_event_id) on reconnect
//	GET   /api/v1/events                same stream scoped by ?project_id= (+ the same replay cursor)
//	GET   /api/v1/tasks                 list task summaries: filters + offset/limit pagination + total
//	POST  /api/v1/tasks                 create task (+ACs) → 201 with possible_duplicates advisory
//	GET   /api/v1/tasks/{id}            one-call resume payload (task+ACs+comments+artifacts+relations+events+blocked)
//	PATCH /api/v1/tasks/{id}            version-guarded partial update → 200 fresh task; 409 with current_version + current task
//	POST  /api/v1/tasks/{id}/claim      claim lease (CAS) → 200 fresh task doing; 409 claim conflict (holder+expiry); 409 task blocked (open blockers, override_blockers=true to bypass)
//	POST  /api/v1/tasks/{id}/release    release lease (holder-only) → 200 fresh task todo; 409 not the holder
//	POST  /api/v1/tasks/{id}/heartbeat  extend lease (keep-alive, no version bump) → 200 fresh task; 409 conflict
//	POST  /api/v1/tasks/{id}/handoff    atomic lease-conditional handoff (notes/assignee + one comment + release) → 200 {task,comment}; 409 claim conflict / not claimed; 400 bad kind
//	POST  /api/v1/tasks/{id}/comments   append typed comment → 201 comment
//	GET   /api/v1/tasks/{id}/comments   paged thread newest first + total (UI extra)
//	PUT   /api/v1/tasks/{id}/notes      replace notes blob (no base-version check) → 200 fresh task
//	POST  /api/v1/tasks/{id}/acceptance-criteria           append a new AC at next seq → 201 row; 400 empty text
//	PATCH /api/v1/tasks/{id}/acceptance-criteria/{seq}   toggle one AC, or reword it via "text" → 200 updated row; 404 unknown seq
//	DELETE /api/v1/tasks/{id}/acceptance-criteria/{seq}  remove one AC (actor required) → 204; 404 unknown task/seq
//	POST  /api/v1/tasks/{id}/artifacts  attach evidence → 201 artifact
//	POST  /api/v1/tasks/{id}/opencode/start  dispatch OpenCode autonomous session → 200 session_id + session_url
//	POST  /api/v1/tasks/{id}/relations  add edge (upsert) → 201 new / 200 re-add; 409 parent conflict
//	DELETE /api/v1/tasks/{id}/relations/{other_id}/{kind}   remove edge → 204; 404 missing edge
func RegisterRoutes(mux *http.ServeMux, app *App) {
	mux.HandleFunc("GET "+apiPrefix+"/projects", app.handleListProjects)
	mux.HandleFunc("POST "+apiPrefix+"/projects", app.handleCreateProject)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}", app.handleGetProject)
	mux.HandleFunc("PUT "+apiPrefix+"/projects/{id}", app.handleUpdateProject)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}/opencode/options", app.handleOpenCodeOptions)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}/board", app.handleBoard)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}/report", app.handleProjectReport)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}/events", app.handleProjectEvents)
	mux.HandleFunc("GET "+apiPrefix+"/events", app.handleEvents)

	mux.HandleFunc("GET "+apiPrefix+"/tasks", app.handleListTasks)
	mux.HandleFunc("POST "+apiPrefix+"/tasks", app.handleCreateTask)
	mux.HandleFunc("GET "+apiPrefix+"/tasks/{id}", app.handleGetTask)
	mux.HandleFunc("PATCH "+apiPrefix+"/tasks/{id}", app.handleUpdateTask)

	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/claim", app.handleClaimTask)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/release", app.handleReleaseTask)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/heartbeat", app.handleHeartbeat)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/handoff", app.handleHandoffTask)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/comments", app.handleAddComment)
	mux.HandleFunc("GET "+apiPrefix+"/tasks/{id}/comments", app.handleListComments)
	mux.HandleFunc("PUT "+apiPrefix+"/tasks/{id}/notes", app.handleUpdateNotes)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/acceptance-criteria", app.handleAddAC)
	mux.HandleFunc("PATCH "+apiPrefix+"/tasks/{id}/acceptance-criteria/{seq}", app.handleToggleAC)
	mux.HandleFunc("DELETE "+apiPrefix+"/tasks/{id}/acceptance-criteria/{seq}", app.handleRemoveAC)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/artifacts", app.handleAttachArtifact)
	mux.HandleFunc("POST /tasks/{id}/artifacts", app.handleAttachArtifact)
	mux.HandleFunc("GET /artifacts/{id}", app.handleGetArtifact)
	mux.HandleFunc("GET "+apiPrefix+"/artifacts/{id}", app.handleGetArtifact)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/opencode/start", app.handleOpenCodeStart)
	mux.HandleFunc("POST "+apiPrefix+"/tasks/{id}/relations", app.handleAddRelation)
	mux.HandleFunc("DELETE "+apiPrefix+"/tasks/{id}/relations/{other_id}/{kind}", app.handleRemoveRelation)
}
