package sqlite

// Integration tests over a real temp-file SQLite database for the TASK core of
// slice 2B: the Tasks use case round-trips through SQLiteUnitOfWork, Create's
// task row + ACs + event commit atomically in one tx, Update is
// optimistic-versioned against the real guard (fresh row on success,
// ConflictError{Current} on a stale writer), and List applies the canonical
// SQL order + pagination.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
)

func newTaskUseCase(t *testing.T, db *sql.DB) (*application.Tasks, *application.Projects) {
	t.Helper()
	uow := NewUnitOfWork(db)
	return application.NewTasks(uow, clock.New(), idgen.New()),
		application.NewProjects(uow, clock.New(), idgen.New())
}

func mustCreateProject(t *testing.T, ctx context.Context, projects *application.Projects) domain.Project {
	t.Helper()
	p, err := projects.Create(ctx, testActor, "Task Project", "")
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	return p
}

// taskRoundTripEqual compares every storage-round-tripped field; pointer fields
// are compared by content (== on structs containing pointers would always fail).
func taskRoundTripEqual(t *testing.T, got, want domain.Task) {
	t.Helper()
	if got.ID != want.ID || got.ProjectID != want.ProjectID ||
		got.Title != want.Title || got.Description != want.Description ||
		got.Status != want.Status || got.Priority != want.Priority ||
		got.Notes != want.Notes || got.Version != want.Version || got.Archived != want.Archived {
		t.Fatalf("task mismatch:\n got %+v\nwant %+v", got, want)
	}
	if !optTimeEqual(got.DueAt, want.DueAt) || !optTimeEqual(got.CompletedAt, want.CompletedAt) {
		t.Errorf("optional times differ: got (due %v done %v) want (due %v done %v)",
			got.DueAt, got.CompletedAt, want.DueAt, want.CompletedAt)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps differ: got (%s, %s) want (%s, %s)",
			got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	if (got.Assignee == nil) != (want.Assignee == nil) {
		t.Fatalf("assignee presence differs: got %+v want %+v", got.Assignee, want.Assignee)
	} else if got.Assignee != nil && *got.Assignee != *want.Assignee {
		t.Errorf("assignee = %+v, want %+v", *got.Assignee, *want.Assignee)
	}
	if !got.LastActivity.Equal(got.UpdatedAt) { // derived on read (no column — ADR)
		t.Errorf("LastActivity %s != UpdatedAt %s after round trip", got.LastActivity, got.UpdatedAt)
	}
}

func optTimeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// --- tests --------------------------------------------------------------------

func TestTasksUseCase_CreateRoundTripsWithACsAndEventsInOneTx(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	assignee := domain.Actor{Type: domain.ActorAgent, Name: "worker-1"}
	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		Title: "Round trip me", Description: "**md**", ProjectID: p.ID, Priority: 3,
		Assignee: &assignee,
		ACs:      []string{"Build it", "Test it"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.Version != 1 || task.Status != domain.StatusTodo || task.ID == "" {
		t.Fatalf("created task = (id %q v%d status %q), want minted id, v1, todo", task.ID, task.Version, task.Status)
	}

	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	taskRoundTripEqual(t, got, task)

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Task.ID != task.ID || detail.Task.Version != 1 {
		t.Errorf("detail task = %+v, want the created row (v1)", detail.Task)
	}
	if len(detail.AcceptanceCriteria) != 2 {
		t.Fatalf("GetDetail ACs = %d, want 2", len(detail.AcceptanceCriteria))
	}
	wantTexts := []string{"Build it", "Test it"}
	for i, want := range wantTexts {
		ac := detail.AcceptanceCriteria[i]
		if ac.Seq != i+1 || ac.Text != want || ac.Done || ac.ID == "" || ac.TaskID != task.ID {
			t.Errorf("AC[%d] = seq %d text %q done %v id %q, want seq %d text %q", i, ac.Seq, ac.Text, ac.Done, ac.ID, i+1, want)
		}
	}
	if len(detail.Comments) != 0 || len(detail.Artifacts) != 0 || len(detail.Relations) != 0 {
		t.Errorf("fresh task detail has child rows: comments=%d artifacts=%d relations=%d",
			len(detail.Comments), len(detail.Artifacts), len(detail.Relations))
	}
	if len(detail.Events) != 1 || detail.Events[0].Kind != domain.EventCreated {
		t.Errorf("detail events = %+v, want exactly the created event", detail.Events)
	}

	// The event row landed in the SAME transaction as the data (direct table check).
	var (
		kind       string
		actorName  string
		beforeJSON *string
		projectID  *string
	)
	err = db.QueryRowContext(ctx,
		`SELECT kind, actor_name, before_json, project_id FROM event WHERE task_id = ?`,
		string(task.ID)).Scan(&kind, &actorName, &beforeJSON, &projectID)
	if err != nil {
		t.Fatalf("event lookup: %v", err)
	}
	if kind != string(domain.EventCreated) || actorName != testActor.Name {
		t.Errorf("in-tx event = (%q by %q), want created by %s", kind, actorName, testActor.Name)
	}
	if beforeJSON != nil {
		t.Errorf("created event before_json = %q, want NULL", *beforeJSON)
	}
	if projectID == nil || string(domain.ID(*projectID)) != string(p.ID) {
		t.Errorf("event project_id = %v, want %s (always set when derivable)", projectID, p.ID)
	}

	// Update via the use case: version bumps 1 → 2 on the real guard.
	doing := domain.StatusDoing
	updated, err := tasks.Update(ctx, testActor, task.ID, application.TaskUpdateInput{Status: &doing})
	if err != nil {
		t.Fatalf("Update status: %v", err)
	}
	if updated.Version != 2 || updated.Status != domain.StatusDoing {
		t.Fatalf("updated = (status %q v%d), want (doing 2)", updated.Status, updated.Version)
	}
	var dbVersion int
	if err := db.QueryRowContext(ctx, `SELECT version FROM task WHERE id = ?`, string(task.ID)).Scan(&dbVersion); err != nil {
		t.Fatalf("version lookup: %v", err)
	}
	if dbVersion != 2 {
		t.Errorf("stored version = %d after Update, want 2", dbVersion)
	}
	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE task_id = ?`, string(task.ID)).Scan(&eventCount); err != nil {
		t.Fatalf("event count: %v", err)
	}
	if eventCount != 2 { // created + status-changed — both committed in-tx with the bump
		t.Errorf("event rows = %d, want 2 (created + status-changed)", eventCount)
	}
	var changedKind string
	err = db.QueryRowContext(ctx,
		`SELECT kind FROM event WHERE task_id = ? AND kind != 'created' ORDER BY ts DESC LIMIT 1`,
		string(task.ID)).Scan(&changedKind)
	if err != nil {
		t.Fatalf("status event lookup: %v", err)
	}
	if changedKind != string(domain.EventStatusChanged) {
		t.Errorf("second event kind = %q, want status-changed", changedKind)
	}
}

func TestTasksUseCase_ListCanonicalOrderAndPaginationOverRealSQL(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	// Insert directly with explicit timestamps (the use case would stamp them
	// with the real clock — too coarse to order six rows deterministically).
	base := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	due1 := base.Add(24 * time.Hour)
	due2 := base.Add(48 * time.Hour)

	var r application.Repos
	seeds := []struct {
		id    string
		title string
		prio  int
		due   *time.Time
		creat time.Duration
	}{
		{"f-1", "P3b", 3, &due1, 0},
		{"f-2", "P5d", 5, nil, time.Minute},
		{"f-3", "P3a", 3, nil, 2 * time.Minute},
		{"f-4", "P5a", 5, &due1, 3 * time.Minute},
		{"f-5", "P5c", 5, &due2, 4 * time.Minute},
		{"f-6", "P5b", 5, &due1, 5 * time.Minute},
	}
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		for _, s := range seeds {
			ts := base.Add(s.creat)
			row := domain.Task{
				ID: domain.ID(s.id), ProjectID: p.ID, Title: s.title,
				Status: domain.StatusTodo, Priority: domain.Priority(s.prio), DueAt: s.due,
				Version: 1, CreatedAt: ts, UpdatedAt: ts,
			}
			if err := r.Tasks.Create(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	rows, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 6 {
		t.Fatalf("total = %d, want 6", total)
	}
	want := []string{"P5a", "P5b", "P5c", "P5d", "P3b", "P3a"} // prio desc → due asc nulls-last → created asc
	got := make([]string, len(rows))
	for i, row := range rows {
		got[i] = row.Title
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SQL order[%d] = %q, want %q — full: got %v want %v", i, got[i], want[i], got, want)
		}
	}

	// Pagination through the real SQL LIMIT/OFFSET.
	page1, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, Limit: 2})
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Title != "P5a" || page1[1].Title != "P5b" || total != 6 {
		t.Errorf("page (0,2) = (%d rows [%s %s], total %d), want 2 rows [P5a P5b] total 6",
			len(page1), firstTitle(page1), secondTitle(page1), total)
	}
	page3, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("List page 3: %v", err)
	}
	if len(page3) != 2 || page3[0].Title != "P3b" || page3[1].Title != "P3a" || total != 6 {
		t.Errorf("page (4,2) = (%d rows [%s %s], total %d), want 2 rows [P3b P3a] total 6",
			len(page3), firstTitle(page3), secondTitle(page3), total)
	}

	// An archived row must vanish from the open list (and vice versa).
	err = NewUnitOfWork(db).Run(ctx, &r, func() error {
		row, gerr := r.Tasks.Get(ctx, domain.ID("f-6")) // P5b, v1
		if gerr != nil {
			return gerr
		}
		row.Archived = true
		row.UpdatedAt = row.UpdatedAt.Add(time.Minute)
		_, uerr := r.Tasks.Update(ctx, row)
		return uerr
	})
	if err != nil {
		t.Fatalf("archive f-6: %v", err)
	}
	rows, total, err = tasks.List(ctx, application.TaskFilter{ProjectID: p.ID})
	if err != nil {
		t.Fatalf("List after archive: %v", err)
	}
	if total != 5 || len(rows) != 5 {
		t.Errorf("after archiving one row: total=%d rows=%d, want 5/5", total, len(rows))
	}
	rows, total, err = tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, Archived: true})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "f-6" {
		t.Errorf("archived list = (total %d, %+v), want exactly f-6", total, rows)
	}
}

func TestTasksRepo_UpdateStaleVersionReturnsConflictWithFreshRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	task, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "Conflict probe", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A pool-bound repo (autocommit path — same implementation as the tx one).
	repo := newTaskRepo(db)

	// Simulate a concurrent writer that already committed a bump to version 2.
	current, err := repo.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	winner, err := repo.Update(ctx, current) // v1 → v2
	if err != nil {
		t.Fatalf("concurrent writer Update: %v", err)
	}
	if winner.Version != 2 {
		t.Fatalf("concurrent writer stored version = %d, want 2", winner.Version)
	}

	// A writer still holding the OLD snapshot (version 1) loses the race.
	stale := task // fields as at v1
	fresh, err := repo.Update(ctx, stale)
	var conflict *domain.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale Update err = %v, want *domain.ConflictError", err)
	}
	if fresh != (domain.Task{}) {
		t.Errorf("stale Update returned row %+v, want zero value", fresh)
	}
	if conflict.Current.Version != 2 || conflict.Current.Title != "Conflict probe" {
		t.Errorf("ConflictError.Current = (v%d %q), want the freshly read v2 row", conflict.Current.Version, conflict.Current.Title)
	}

	// And an unknown ID is a plain ErrNotFound, not a conflict.
	if _, err := repo.Update(ctx, domain.Task{ID: "ghost", ProjectID: p.ID, Version: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update(unknown id) err = %v, want domain.ErrNotFound", err)
	}

	// The winning writer's row is untouched by the lost race.
	if got, _ := repo.Get(ctx, task.ID); got.Version != 2 || got.Title != "Conflict probe" {
		t.Errorf("row after stale write = (v%d %q), want (v2 Conflict probe)", got.Version, got.Title)
	}
}

func TestTasksUseCase_CreateRollsBackTaskACsAndEventTogether(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)
	p := domain.Project{ID: "rb-proj", Name: "RB Proj", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}

	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, p); err != nil {
			return err
		}
		tid := domain.ID("rb-task")
		row := domain.Task{ID: tid, ProjectID: p.ID, Title: "Rollback probe", Status: domain.StatusTodo, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := r.Tasks.Create(ctx, row); err != nil {
			return err
		}
		ac := domain.AcceptanceCriterion{ID: "rb-ac", TaskID: tid, Seq: 1, Text: "will roll back"}
		if err := r.ACs.Create(ctx, []domain.AcceptanceCriterion{ac}); err != nil {
			return err
		}
		ev := domain.Event{ID: "rb-event", ProjectID: &p.ID, TaskID: &tid, Actor: testActor, Kind: domain.EventCreated, At: time.Now().UTC()}
		if err := r.Events.Append(ctx, ev); err != nil {
			return err
		}
		return errors.New("forced failure after task+AC+event writes")
	})
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("Run = %v, want the forced error", err)
	}

	var taskRows, acRows, eventRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE id = 'rb-task'`).Scan(&taskRows); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM acceptance_criterion WHERE id = 'rb-ac'`).Scan(&acRows); err != nil {
		t.Fatalf("count ACs: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE id = 'rb-event'`).Scan(&eventRows); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if taskRows != 0 || acRows != 0 || eventRows != 0 {
		t.Fatalf("rollback leaked rows: task=%d ac=%d event=%d, want 0/0/0 (data and event commit or roll back together)",
			taskRows, acRows, eventRows)
	}
}

func TestTasksRepo_GetReturnsNotFoundForMissingID(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := newTaskRepo(db)
	if _, err := repo.Get(ctx, domain.ID("nope")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want domain.ErrNotFound", err)
	}
}

func firstTitle(rows []domain.Task) string {
	if len(rows) == 0 {
		return "<none>"
	}
	return rows[0].Title
}

func secondTitle(rows []domain.Task) string {
	if len(rows) < 2 {
		return "<none>"
	}
	return rows[1].Title
}
