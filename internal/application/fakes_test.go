package application

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// fakes_test.go holds the in-memory test doubles for the ports this slice's
// use cases touch. Later slices add their own fakes here (task, acceptance,
// comment, artifact, relation). Nothing in production code depends on them.

// fakeClock is a controllable Clock: tests set "now" and advance it by hand.
type fakeClock struct{ now time.Time }

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time     { return c.now }
func (c *fakeClock) SetNow(t time.Time) { c.now = t }

// fakeIDGen mints deterministic, sortable IDs ("id-0001", "id-0002", …).
type fakeIDGen struct{ n int }

func (g *fakeIDGen) NewID() domain.ID {
	g.n++
	return domain.ID(fmt.Sprintf("id-%04d", g.n))
}

// fakeProjects is an in-memory ProjectRepository for use-case unit tests.
type fakeProjects struct {
	byID map[domain.ID]domain.Project
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{byID: make(map[domain.ID]domain.Project)}
}

func (f *fakeProjects) Create(_ context.Context, p domain.Project) error {
	if _, ok := f.byID[p.ID]; ok {
		return fmt.Errorf("duplicate project id %s", p.ID)
	}
	f.byID[p.ID] = p
	return nil
}

func (f *fakeProjects) Get(_ context.Context, id domain.ID) (domain.Project, error) {
	p, ok := f.byID[id]
	if !ok {
		return domain.Project{}, domain.ErrNotFound
	}
	return p, nil
}

// List returns all projects ordered by created_at ASC (id ASC tie-break),
// mirroring the port contract.
func (f *fakeProjects) List(_ context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(f.byID))
	for _, p := range f.byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeProjects) Update(_ context.Context, p domain.Project) error {
	if _, ok := f.byID[p.ID]; !ok {
		return domain.ErrNotFound
	}
	f.byID[p.ID] = p
	return nil
}

// fakeEvents is an in-memory EventStore that records append order.
type fakeEvents struct{ events []domain.Event }

func (f *fakeEvents) Append(_ context.Context, e domain.Event) error {
	f.events = append(f.events, e)
	return nil
}

func (f *fakeEvents) ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Event, error) {
	out := make([]domain.Event, 0)
	for _, e := range f.events {
		if e.TaskID != nil && *e.TaskID == taskID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeEvents) ListByProject(ctx context.Context, projectID domain.ID, limit, offset int) ([]domain.Event, error) {
	out := make([]domain.Event, 0)
	for _, e := range f.events {
		if e.ProjectID != nil && *e.ProjectID == projectID {
			out = append(out, e)
		}
	}
	return out, nil
}

// fakeUoW implements UnitOfWork for use-case unit tests: it binds the given
// fakes into r before fn (the same contract the real implementations honor)
// and skips transaction mechanics — rollback behavior is covered by the
// SQLite integration tests.
type fakeUoW struct {
	projects *fakeProjects
	events   *fakeEvents
}

func newFakeUoW(projects *fakeProjects, events *fakeEvents) *fakeUoW {
	return &fakeUoW{projects: projects, events: events}
}

func (u *fakeUoW) Run(_ context.Context, r *Repos, fn func() error) error {
	r.Projects = u.projects
	r.Events = u.events
	// The remaining ports stay nil; the projects use case never touches them.
	return fn()
}

var _ UnitOfWork = (*fakeUoW)(nil)
