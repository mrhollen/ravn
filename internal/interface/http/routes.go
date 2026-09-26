package httpapi

import "net/http"

const apiPrefix = "/api/v1"

// RegisterRoutes mounts the REST surface on mux. Patterns use Go 1.22's
// method+wildcard syntax, so the stdlib mux answers unknown methods with a
// proper 405 (Allow header) for free. This is the single place to read the
// whole slice-3a API:
//
//	GET   /api/v1/projects              list projects + per-status open task counts
//	POST  /api/v1/projects              create project → 201
//	GET   /api/v1/projects/{id}         get project (404 when unknown)
//	PUT   /api/v1/projects/{id}         update name/description
//	GET   /api/v1/projects/{id}/board   whole-project board snapshot (UI extra)
//	GET   /api/v1/tasks                 list task summaries: filters + offset/limit pagination + total
//	POST  /api/v1/tasks                 create task (+ACs) → 201 with possible_duplicates advisory
//	GET   /api/v1/tasks/{id}            one-call resume payload (task+ACs+comments+artifacts+relations+events+blocked)
//	PATCH /api/v1/tasks/{id}            version-guarded partial update → 200 fresh task; 409 with current_version + current task
func RegisterRoutes(mux *http.ServeMux, app *App) {
	mux.HandleFunc("GET "+apiPrefix+"/projects", app.handleListProjects)
	mux.HandleFunc("POST "+apiPrefix+"/projects", app.handleCreateProject)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}", app.handleGetProject)
	mux.HandleFunc("PUT "+apiPrefix+"/projects/{id}", app.handleUpdateProject)
	mux.HandleFunc("GET "+apiPrefix+"/projects/{id}/board", app.handleBoard)

	mux.HandleFunc("GET "+apiPrefix+"/tasks", app.handleListTasks)
	mux.HandleFunc("POST "+apiPrefix+"/tasks", app.handleCreateTask)
	mux.HandleFunc("GET "+apiPrefix+"/tasks/{id}", app.handleGetTask)
	mux.HandleFunc("PATCH "+apiPrefix+"/tasks/{id}", app.handleUpdateTask)
}
