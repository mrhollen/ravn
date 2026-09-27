// Package httpapi is crowban's REST adapter (ARCHITECTURE §5). It sits at the
// onion edge: it imports application + domain + stdlib only and never touches
// persistence — the concrete wiring lives in cmd/crowban-server.
//
// Slice 3a registers the Projects, Task-core, and board-snapshot routes under
// /api/v1. The App struct already holds every use case, so slice 3b (comments,
// artifacts, acceptance criteria, relations) only adds routes and handlers —
// no rework of the composition surface.
package httpapi

import (
	"context"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// Deps bundles the application use cases the HTTP adapter needs. The concrete
// use-case structs are passed (never the UnitOfWork): the adapter is a thin
// presentation layer and must not manage transactions itself.
type Deps struct {
	Projects   *application.Projects
	Tasks      *application.Tasks
	Comments   *application.Comments   // routed in slice 3b
	Artifacts  *application.Artifacts  // routed in slice 3b
	Acceptance *application.Acceptance // routed in slice 3b
	Relations  *application.Relations  // routed in slice 3b
	Broker     application.EventBroker // realtime pub-sub for SSE events
}

type noopBroker struct{}

func (noopBroker) Publish(domain.Event) {}
func (noopBroker) Subscribe(domain.ID) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event)
	close(ch)
	return ch, func() {}
}

// App is the stateless HTTP adapter: handlers only read its fields, all
// per-request state lives on the request. Safe for concurrent use.
type App struct {
	Deps
}

// NewApp builds the adapter from the wired use cases. All six are required —
// a nil dependency is a composition-root bug, so NewApp panics (wiring time,
// not runtime). Later slices add routes without touching this constructor.
func NewApp(d Deps) *App {
	if d.Projects == nil || d.Tasks == nil || d.Comments == nil ||
		d.Artifacts == nil || d.Acceptance == nil || d.Relations == nil {
		panic("httpapi.NewApp: all use cases must be provided")
	}
	if d.Broker == nil {
		d.Broker = noopBroker{}
	}
	return &App{Deps: d}
}

// listPageSize is the largest page application.Tasks.List returns (its max
// list limit). The board snapshot pages through it until every open task of a
// project has been collected.
const listPageSize = 100

// allOpenTasks returns EVERY open (non-archived) task of a project in
// canonical order, paging through the capped Tasks.List. The board endpoint
// needs the whole project in one round-trip; list_tasks stays page-capped.
func (a *App) allOpenTasks(ctx context.Context, projectID domain.ID) ([]domain.Task, error) {
	var out []domain.Task
	for {
		page, total, err := a.Tasks.List(ctx, application.TaskFilter{
			ProjectID: projectID,
			Limit:     listPageSize,
			Offset:    len(out),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if int64(len(out)) >= total || len(page) == 0 {
			return out, nil
		}
	}
}
