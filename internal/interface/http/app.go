// Package httpapi is ravn's REST adapter (ARCHITECTURE §5). It sits at the
// onion edge: it imports application + domain + stdlib only and never touches
// persistence — the concrete wiring lives in cmd/ravn-server.
//
// Slice 3a registers the Projects, Task-core, and board-snapshot routes under
// /api/v1. The App struct already holds every use case, so slice 3b (comments,
// artifacts, acceptance criteria, relations) only adds routes and handlers —
// no rework of the composition surface.
package httpapi

import (
	"context"

	"net/http"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// Deps bundles the application use cases the HTTP adapter needs. The concrete
// use-case structs are passed (never the UnitOfWork): the adapter is a thin
// presentation layer and must not manage transactions itself.
type Deps struct {
	Projects    *application.Projects
	Tasks       *application.Tasks
	Comments    *application.Comments    // routed in slice 3b
	Artifacts   *application.Artifacts   // routed in slice 3b
	Acceptance  *application.Acceptance  // routed in slice 3b
	Relations   *application.Relations   // routed in slice 3b
	Memberships *application.Memberships // OPTIONAL: multi-user membership management (T9); nil → /projects/{id}/members* routes return 404 ("not enabled"), other behavior unchanged
	Auth        *application.Auth        // OPTIONAL: multi-user auth (T3); nil → /auth/* routes return 404, other behavior unchanged
	Broker      application.EventBroker  // realtime pub-sub for SSE events
	Events      application.EventStore   // OPTIONAL: activity-log read path behind SSE Last-Event-ID replay; nil = live-only streams (no replay)
	// Scoping drives the multi-user epic T4 visibility + authorization gates.
	// NIL MEANS DefaultScoping() (see-all, no gating) — never interpret the zero
	// ScopingConfig directly (its zero value is restrictive); all gate helpers in
	// scoping.go fold nil to the default, so existing wiring and test stacks stay
	// byte-identical. Membership-scoped deployments also set Identity (and, for
	// project creation, IdentityOpen).
	Scoping          *domain.ScopingConfig         // RAVN_SCOPING parsed via ParseScopingMode; nil → global/default
	Identity         *application.IdentityResolver // T4: name→Subject + project role resolution; required when Scoping.MembershipScoped
	IdentityOpen     bool                          // RAVN_IDENTITY_OPEN: credential principals may create projects (membership scoping)
	HTTPClient       *http.Client                  // optional HTTP client for external integrations (e.g. OpenCode)
	ArtifactsDir     string                        // directory holding stored artifact files
	SSEWriteDeadline time.Duration                 // per-write deadline for SSE streams (stalled-client zombie guard); zero/negative → DefaultSSEWriteDeadline
}

// DefaultSSEWriteDeadline is the PRODUCTION default for Deps.SSEWriteDeadline.
// A stalled/slow client whose TCP buffers fill can block an SSE handler INSIDE
// a write, outside the stream's select loop; bounding every write/flush to
// this deadline guarantees the blocked call returns an error so the handler
// exits and runs deferred cleanup instead of zombie-ing. Keep it generous in
// production — tests override Deps.SSEWriteDeadline with short values rather
// than touching this constant (see TestEvents_SSE_DefaultWriteDeadline, which
// pins it at exactly 10s).
const DefaultSSEWriteDeadline = 10 * time.Second

type noopBroker struct{}

func (noopBroker) Publish(domain.Event) {}
func (noopBroker) Subscribe(domain.ID) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event)
	close(ch)
	return ch, func() {}
}
func (noopBroker) Close() error { return nil }

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
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if d.ArtifactsDir == "" {
		d.ArtifactsDir = "./artifacts"
	}
	if d.SSEWriteDeadline <= 0 {
		d.SSEWriteDeadline = DefaultSSEWriteDeadline // production default; tests may override
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
