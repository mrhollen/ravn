package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestProjectsOverview_EmptyDatabase(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, testStart)
	svc, _ := newLWCEnv(t, at)

	rows, err := svc.Overview(ctx, OverviewOptions{})
	if err != nil {
		t.Fatalf("Overview on empty db: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("Overview on empty db = %#v, want empty non-nil slice", rows)
	}
}

func TestProjectsOverview_AggregatesHealthAndFilters(t *testing.T) {
	ctx := context.Background()
	now := mustTime(t, testStart)
	svc, uow := newLWCEnv(t, now)

	ownerA := domain.ID("owner-a")
	actorA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

	// p1: active, project updated_at older than latest non-archived task; has active/expired/stalled/attention
	uow.projects.byID["p1"] = domain.Project{
		ID: "p1", Name: "P1", CreatedBy: &ownerA,
		CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-4 * time.Hour),
	}
	// p2: active, project updated_at NEWER than tasks; no doing claims; empty attention
	uow.projects.byID["p2"] = domain.Project{
		ID: "p2", Name: "P2",
		CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Minute),
	}
	// p3: active, empty project (falls back to project UpdatedAt)
	uow.projects.byID["p3"] = domain.Project{
		ID: "p3", Name: "P3",
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-90 * time.Minute),
	}
	// p4: archived project
	uow.projects.byID["p4"] = domain.Project{
		ID: "p4", Name: "P4 Archived", Archived: true,
		CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-10 * time.Minute),
	}

	// p1 tasks
	uow.tasks.byID["p1-todo"] = domain.Task{
		ID: "p1-todo", ProjectID: "p1", Title: "todo", Status: domain.StatusTodo,
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-20 * time.Minute),
	}
	uow.tasks.byID["p1-active"] = domain.Task{
		ID: "p1-active", ProjectID: "p1", Title: "active", Status: domain.StatusDoing,
		Claim:   domain.NewClaim(actorA, now.Add(-10*time.Minute), 1200), // expires now+10m
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-10 * time.Minute),
	}
	uow.tasks.byID["p1-exact-exp"] = domain.Task{
		ID: "p1-exact-exp", ProjectID: "p1", Title: "exact exp", Status: domain.StatusDoing,
		Claim:   domain.NewClaim(actorA, now.Add(-60*time.Second), 60), // expires == now
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-5 * time.Minute),
	}
	uow.tasks.byID["p1-beat-stale"] = domain.Task{
		ID: "p1-beat-stale", ProjectID: "p1", Title: "heartbeat stale", Status: domain.StatusDoing,
		Claim:   domain.NewClaim(actorA, now.Add(-2*time.Hour), 10800), // expires now+1h
		Version: 1, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-70 * time.Minute),
	}
	uow.tasks.byID["p1-done"] = domain.Task{
		ID: "p1-done", ProjectID: "p1", Title: "done", Status: domain.StatusDone,
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-3 * time.Minute),
	}
	uow.tasks.byID["p1-arch"] = domain.Task{
		ID: "p1-arch", ProjectID: "p1", Title: "arch", Status: domain.StatusTodo, Archived: true,
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, // newer, but archived
	}

	// p2 tasks (updated earlier than p2.UpdatedAt)
	uow.tasks.byID["p2-todo"] = domain.Task{
		ID: "p2-todo", ProjectID: "p2", Title: "p2 todo", Status: domain.StatusTodo,
		Version: 1, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-30 * time.Minute),
	}

	// p4 task (inside archived project)
	uow.tasks.byID["p4-todo"] = domain.Task{
		ID: "p4-todo", ProjectID: "p4", Title: "p4 todo", Status: domain.StatusTodo,
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-1 * time.Minute),
	}

	// Comments for needs_attention
	uow.comments.byTask["p1-todo"] = []domain.Comment{
		{ID: "c1", TaskID: "p1-todo", Kind: domain.CommentProgress, Actor: actorA, CreatedAt: now.Add(-15 * time.Minute)},
		{ID: "c2", TaskID: "p1-todo", Kind: domain.CommentQuestion, Actor: actorA, CreatedAt: now.Add(-10 * time.Minute)},
	}
	uow.comments.byTask["p1-active"] = []domain.Comment{
		{ID: "c3", TaskID: "p1-active", Kind: domain.CommentHandoff, Actor: actorA, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "c4", TaskID: "p1-active", Kind: domain.CommentNote, Actor: actorA, CreatedAt: now.Add(-5 * time.Minute)},
	}
	uow.comments.byTask["p1-done"] = []domain.Comment{
		{ID: "c5", TaskID: "p1-done", Kind: domain.CommentQuestion, Actor: actorA, CreatedAt: now.Add(-5 * time.Minute)},
	}
	uow.comments.byTask["p1-arch"] = []domain.Comment{
		{ID: "c6", TaskID: "p1-arch", Kind: domain.CommentHandoff, Actor: actorA, CreatedAt: now.Add(-5 * time.Minute)},
	}

	// Default options (active projects, 60m stalled window)
	rows, err := svc.Overview(ctx, OverviewOptions{})
	if err != nil {
		t.Fatalf("Overview default: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3 active projects", len(rows))
	}

	// p1
	r1 := rows[0]
	if r1.Project.ID != "p1" || r1.OpenTasks[domain.StatusTodo] != 1 || r1.OpenTasks[domain.StatusDoing] != 3 || r1.OpenTasks[domain.StatusDone] != 1 {
		t.Errorf("p1 row = %+v", r1)
	}
	if r1.Health.ActiveClaims != 2 || r1.Health.ExpiredClaims != 1 || r1.Health.Stalled != 2 || r1.Health.NeedsAttention != 1 || !r1.Health.LastActivityAt.Equal(now.Add(-3*time.Minute)) {
		t.Errorf("p1 health = %+v", r1.Health)
	}

	// p2: project.UpdatedAt (now-2m) > task.UpdatedAt (now-30m)
	r2 := rows[1]
	if r2.Project.ID != "p2" || !r2.Health.LastActivityAt.Equal(now.Add(-2*time.Minute)) {
		t.Errorf("p2 row = %+v, want last_activity_at=now-2m", r2)
	}

	// p3: empty project falls back to project.UpdatedAt
	r3 := rows[2]
	if r3.Project.ID != "p3" || r3.OpenTasks[domain.StatusTodo] != 0 || r3.OpenTasks[domain.StatusDoing] != 0 || r3.OpenTasks[domain.StatusDone] != 0 {
		t.Errorf("p3 counts = %+v, want zeros", r3.OpenTasks)
	}
	if !r3.Health.LastActivityAt.Equal(now.Add(-90 * time.Minute)) {
		t.Errorf("p3 last_activity_at = %v, want now-90m", r3.Health.LastActivityAt)
	}

	// Custom StalledMinutes=15: p1-active (updated now-10m) still active, p1-beat-stale (now-70m) stalled, p1-exact-exp (expired) stalled
	// And with StalledMinutes=5: p1-active (now-10m) ALSO becomes stalled!
	rows5, err := svc.Overview(ctx, OverviewOptions{StalledMinutes: 5})
	if err != nil {
		t.Fatalf("Overview stalled=5: %v", err)
	}
	if rows5[0].Health.Stalled != 3 {
		t.Errorf("p1 stalled (window 5m) = %d, want 3", rows5[0].Health.Stalled)
	}

	// Huge StalledMinutes clamps cleanly without duration overflow
	rowsHuge, err := svc.Overview(ctx, OverviewOptions{StalledMinutes: maxOverviewStalledMinutes * 10})
	if err != nil {
		t.Fatalf("Overview huge stalled: %v", err)
	}
	if rowsHuge[0].Health.Stalled != 1 { // only claim-expired p1-exact-exp
		t.Errorf("p1 stalled (huge window) = %d, want 1", rowsHuge[0].Health.Stalled)
	}

	// Archived filter
	archRows, err := svc.Overview(ctx, OverviewOptions{Filter: ProjectFilter{Archived: true}})
	if err != nil {
		t.Fatalf("Overview archived: %v", err)
	}
	if len(archRows) != 1 || archRows[0].Project.ID != "p4" || archRows[0].OpenTasks[domain.StatusTodo] != 1 {
		t.Errorf("archived rows = %+v, want [p4 with todo=1]", archRows)
	}

	// Creator filter
	mineRows, err := svc.Overview(ctx, OverviewOptions{Filter: ProjectFilter{Creator: &ownerA}})
	if err != nil {
		t.Fatalf("Overview creator: %v", err)
	}
	if len(mineRows) != 1 || mineRows[0].Project.ID != "p1" {
		t.Errorf("creator rows = %+v, want [p1]", mineRows)
	}
}

type failingOverviewUoW struct {
	projectsErr error
	taskErr     error
	commentErr  error
	base        *fakeUoW
}

func (f *failingOverviewUoW) Run(ctx context.Context, r *Repos, fn func() error) error {
	if err := f.base.Run(ctx, r, func() error { return nil }); err != nil {
		return err
	}
	r.Projects = &errProjectsRepo{ProjectRepository: f.base.projects, listErr: f.projectsErr}
	r.Tasks = &errTasksRepo{TaskRepository: f.base.tasks, rollupErr: f.taskErr}
	r.Comments = &errCommentsRepo{CommentRepository: f.base.comments, attnErr: f.commentErr}
	return fn()
}

type errProjectsRepo struct {
	ProjectRepository
	listErr error
}

func (e *errProjectsRepo) List(ctx context.Context, filter ...ProjectFilter) ([]domain.Project, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.ProjectRepository.List(ctx, filter...)
}

type errTasksRepo struct {
	TaskRepository
	rollupErr error
}

func (e *errTasksRepo) ProjectTaskRollups(ctx context.Context, now, stalledCutoff time.Time) (map[domain.ID]ProjectTaskRollup, error) {
	if e.rollupErr != nil {
		return nil, e.rollupErr
	}
	return e.TaskRepository.ProjectTaskRollups(ctx, now, stalledCutoff)
}

type errCommentsRepo struct {
	CommentRepository
	attnErr error
}

func (e *errCommentsRepo) NeedsAttentionByProject(ctx context.Context) (map[domain.ID]int64, error) {
	if e.attnErr != nil {
		return nil, e.attnErr
	}
	return e.CommentRepository.NeedsAttentionByProject(ctx)
}

func TestProjectsOverview_ErrorPropagation(t *testing.T) {
	now := mustTime(t, testStart)
	boom := errors.New("boom")

	for _, tc := range []struct {
		name string
		uow  func(base *fakeUoW) *failingOverviewUoW
	}{
		{"projects list error", func(b *fakeUoW) *failingOverviewUoW { return &failingOverviewUoW{base: b, projectsErr: boom} }},
		{"task rollups error", func(b *fakeUoW) *failingOverviewUoW { return &failingOverviewUoW{base: b, taskErr: boom} }},
		{"comment attention error", func(b *fakeUoW) *failingOverviewUoW { return &failingOverviewUoW{base: b, commentErr: boom} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakeProjects()
			fp.byID["p1"] = domain.Project{ID: "p1", Name: "P1", CreatedAt: now, UpdatedAt: now}
			base := newFakeUoW(fp, &fakeEvents{})
			svc := NewProjects(tc.uow(base), newFakeClock(now), &fakeIDGen{})
			if _, err := svc.Overview(context.Background(), OverviewOptions{}); !errors.Is(err, boom) {
				t.Fatalf("Overview error = %v, want boom", err)
			}
		})
	}
}
