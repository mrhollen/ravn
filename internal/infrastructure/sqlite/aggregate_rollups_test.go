package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// aggregate_rollups_test.go covers the batch rollup / aggregation queries that
// back project reports and board counts: taskRepo.OpenTaskCounts,
// taskRepo.OpenBlockersByTasks, relationRepo.SubtaskProgressByParents,
// commentRepo.CountByTask and eventStore.MaxAtByTasks. These are single grouped
// scans (no per-project N+1) — exactly the shape of query where an off-by-one,
// a wrong join direction, or a missing archived/done filter hides. Every test
// drives the REAL repo against a REAL migrated DB (openTestDB), seeded through
// the transactional ports so foreign keys and CHECK constraints are honored
// identically to production.

var rollupBase = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func mustRollupTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UTC()
}

// withRollupSeed runs fn inside a real UnitOfWork transaction over db and
// fatals if any port call errors. After it returns the rows are committed and
// visible to direct repo calls on the same handle (mirrors
// offset_without_limit_test.go).
func withRollupSeed(t *testing.T, db *sql.DB, fn func(r *application.Repos)) {
	t.Helper()
	var r application.Repos
	// fn fatals (via must*) on any port error, so by the time it returns there
	// is nothing to surface here; return nil to commit the seeded rows.
	if err := NewUnitOfWork(db).Run(context.Background(), &r, func() error {
		fn(&r)
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func mustCreate(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
}
func mustAddRel(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("add relation: %v", err)
	}
}
func mustAddComment(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
}
func mustAppendEvent(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
}

// rollupTask builds a minimal valid task row (Version 1, stamped times).
func rollupTask(id, projectID domain.ID, title string, status domain.Status, archived bool) domain.Task {
	return domain.Task{
		ID: id, ProjectID: projectID, Title: title, Status: status,
		Version: 1, Archived: archived, CreatedAt: rollupBase, UpdatedAt: rollupBase,
	}
}

func rollupProject(id, name string) domain.Project {
	return domain.Project{ID: domain.ID(id), Name: name, CreatedAt: rollupBase, UpdatedAt: rollupBase}
}

// rollupRel builds a relation edge. id must be unique per (task,other,kind).
func rollupRel(id, taskID, otherID string, kind domain.RelationKind, at time.Time) domain.Relation {
	return domain.Relation{ID: domain.ID(id), TaskID: domain.ID(taskID), OtherTaskID: domain.ID(otherID), Kind: kind, CreatedAt: at}
}

func rollupEvent(id, taskID string, at time.Time) domain.Event {
	tid := domain.ID(taskID)
	return domain.Event{ID: domain.ID(id), TaskID: &tid, Actor: domain.Actor{Type: domain.ActorAgent, Name: "rollup"}, Kind: domain.EventCreated, At: at}
}

// --- OpenTaskCounts (taskRepo) ---------------------------------------------------------

type rollupCountKey struct {
	p domain.ID
	s domain.Status
}

func wantCountMap(pairs ...struct {
	p domain.ID
	s domain.Status
	n int64
}) map[rollupCountKey]int64 {
	m := make(map[rollupCountKey]int64, len(pairs))
	for _, pr := range pairs {
		m[rollupCountKey{pr.p, pr.s}] = pr.n
	}
	return m
}

func assertCountRows(t *testing.T, rows []application.TaskCountRow, want map[rollupCountKey]int64) {
	t.Helper()
	got := make(map[rollupCountKey]int64, len(rows))
	for _, r := range rows {
		got[rollupCountKey{r.ProjectID, r.Status}] = r.Count
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OpenTaskCounts = %v, want %v", got, want)
	}
}

func TestTaskRepoOpenTaskCounts(t *testing.T) {
	t.Parallel()

	t.Run("EmptyDatabase_ReturnsNoRows", func(t *testing.T) {
		db := openTestDB(t)
		rows, err := newTaskRepo(db).OpenTaskCounts(context.Background())
		if err != nil {
			t.Fatalf("OpenTaskCounts on empty db: %v", err)
		}
		assertCountRows(t, rows, wantCountMap())
	})

	t.Run("SingleProject_MixedStatuses_ExcludesArchived_CountsDone", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("t-1", "p1", "todo a", domain.StatusTodo, false),
				rollupTask("t-2", "p1", "todo b", domain.StatusTodo, false),
				rollupTask("t-3", "p1", "doing", domain.StatusDoing, false),
				rollupTask("t-4", "p1", "done", domain.StatusDone, false),
				rollupTask("t-5", "p1", "archived todo", domain.StatusTodo, true), // must not count
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
		})
		rows, err := newTaskRepo(db).OpenTaskCounts(context.Background())
		if err != nil {
			t.Fatalf("OpenTaskCounts: %v", err)
		}
		assertCountRows(t, rows, wantCountMap(
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p1", domain.StatusTodo, 2}, // archived todo excluded → 2 not 3
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p1", domain.StatusDoing, 1},
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p1", domain.StatusDone, 1}, // completed tasks count under "done"
		))
	})

	t.Run("MultiProject_FanOut_NoTasksPairAbsent", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			for _, p := range []domain.Project{rollupProject("p1", "P1"), rollupProject("p2", "P2"), rollupProject("p3", "P3")} {
				mustCreate(t, r.Projects.Create(ctx, p))
			}
			tasks := []domain.Task{
				rollupTask("a-1", "p1", "todo", domain.StatusTodo, false),
				rollupTask("a-2", "p1", "done", domain.StatusDone, false),
				rollupTask("b-1", "p2", "doing 1", domain.StatusDoing, false),
				rollupTask("b-2", "p2", "doing 2", domain.StatusDoing, false),
				// p3 intentionally has NO tasks → must not produce a pair.
			}
			for _, tk := range tasks {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
		})
		rows, err := newTaskRepo(db).OpenTaskCounts(context.Background())
		if err != nil {
			t.Fatalf("OpenTaskCounts: %v", err)
		}
		assertCountRows(t, rows, wantCountMap(
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p1", domain.StatusTodo, 1},
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p1", domain.StatusDone, 1},
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"p2", domain.StatusDoing, 2},
		))
	})

	t.Run("DoneOnlyProject_OnlyDonePairPresent", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("pd", "DoneOnly")))
			for _, id := range []string{"d-1", "d-2", "d-3"} {
				mustCreate(t, r.Tasks.Create(ctx, rollupTask(domain.ID(id), "pd", "done "+id, domain.StatusDone, false)))
			}
		})
		rows, err := newTaskRepo(db).OpenTaskCounts(context.Background())
		if err != nil {
			t.Fatalf("OpenTaskCounts: %v", err)
		}
		assertCountRows(t, rows, wantCountMap(
			struct {
				p domain.ID
				s domain.Status
				n int64
			}{"pd", domain.StatusDone, 3},
		))
	})
}

// --- OpenBlockersByTasks (taskRepo) ----------------------------------------------------

func assertBlockerIDs(t *testing.T, got map[domain.ID][]domain.Task, want map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(out) = %d (%v), want %d (%v)", len(got), fmt.Sprintf("%v", got), len(want), want)
	}
	for k, v := range want {
		tasks, ok := got[domain.ID(k)]
		gotIDs := make([]string, 0, len(tasks))
		if !ok || len(tasks) != len(v) {
			t.Errorf("out[%q] = %v (present=%v), want %v", k, fmt.Sprintf("%v", got), ok, v)
			continue
		}
		for _, tk := range tasks {
			gotIDs = append(gotIDs, string(tk.ID))
		}
		if !reflect.DeepEqual(gotIDs, v) {
			t.Errorf("out[%q] (ordered) = %v, want %v", k, gotIDs, v)
		}
	}
}

func TestTaskRepoOpenBlockersByTasks(t *testing.T) {
	t.Parallel()

	t.Run("EmptyInput_ShortCircuitsNoSQL", func(t *testing.T) {
		db := openTestDB(t)
		got, err := newTaskRepo(db).OpenBlockersByTasks(context.Background(), nil)
		if err != nil {
			t.Fatalf("OpenBlockersByTasks(nil): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("out = %v, want empty", got)
		}
	})

	t.Run("NoEdges_ReturnsEmptyMap", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-1", "p1", "a", domain.StatusTodo, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-2", "p1", "b", domain.StatusDoing, false)))
		})
		got, err := newTaskRepo(db).OpenBlockersByTasks(context.Background(), []domain.ID{"t-1", "t-2"})
		if err != nil {
			t.Fatalf("OpenBlockersByTasks: %v", err)
		}
		assertBlockerIDs(t, got, map[string][]string{}) // no blockers → no keys at all
	})

	t.Run("ExcludesDoneAndArchivedBlockers", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("dep", "p1", "dependent", domain.StatusDoing, false),
				rollupTask("blk-open", "p1", "open blocker", domain.StatusTodo, false),    // counts
				rollupTask("blk-done", "p1", "done blocker", domain.StatusDone, false),    // excluded (done)
				rollupTask("blk-arch", "p1", "archived blocker", domain.StatusTodo, true), // excluded (archived)
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r1", "dep", "blk-open", domain.RelationBlockedBy, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r2", "dep", "blk-done", domain.RelationBlockedBy, rollupBase.Add(time.Minute))))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r3", "dep", "blk-arch", domain.RelationBlockedBy, rollupBase.Add(2*time.Minute))))
		})
		got, err := newTaskRepo(db).OpenBlockersByTasks(context.Background(), []domain.ID{"dep"})
		if err != nil {
			t.Fatalf("OpenBlockersByTasks: %v", err)
		}
		assertBlockerIDs(t, got, map[string][]string{"dep": {"blk-open"}})
	})

	t.Run("OrdersByRelationCreatedAt_NotTargetID", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("dep", "p1", "dependent", domain.StatusDoing, false),
				// Target IDs deliberately out of the expected (created_at) order so a
				// buggy ORDER BY b.id ASC would fail.
				rollupTask("blk-c", "p1", "c", domain.StatusTodo, false), // created first  → 1st
				rollupTask("blk-a", "p1", "a", domain.StatusTodo, false), // created second → 2nd
				rollupTask("blk-b", "p1", "b", domain.StatusTodo, false), // created third  → 3rd
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r1", "dep", "blk-c", domain.RelationBlockedBy, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r2", "dep", "blk-a", domain.RelationBlockedBy, rollupBase.Add(time.Minute))))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r3", "dep", "blk-b", domain.RelationBlockedBy, rollupBase.Add(2*time.Minute))))
		})
		got, err := newTaskRepo(db).OpenBlockersByTasks(context.Background(), []domain.ID{"dep"})
		if err != nil {
			t.Fatalf("OpenBlockersByTasks: %v", err)
		}
		assertBlockerIDs(t, got, map[string][]string{"dep": {"blk-c", "blk-a", "blk-b"}})
	})

	t.Run("MultiDependent_FanOut", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("dep-1", "p1", "d1", domain.StatusDoing, false),
				rollupTask("dep-2", "p1", "d2", domain.StatusTodo, false),
				rollupTask("blk-a", "p1", "a", domain.StatusTodo, false), // blocks dep-1 (open)
				rollupTask("blk-b", "p1", "b", domain.StatusTodo, false), // blocks dep-2 (open)
				rollupTask("blk-c", "p1", "c", domain.StatusDone, false), // blocks dep-2 (done → excluded)
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r1", "dep-1", "blk-a", domain.RelationBlockedBy, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r2", "dep-2", "blk-b", domain.RelationBlockedBy, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r3", "dep-2", "blk-c", domain.RelationBlockedBy, rollupBase)))
		})
		got, err := newTaskRepo(db).OpenBlockersByTasks(context.Background(), []domain.ID{"dep-1", "dep-2"})
		if err != nil {
			t.Fatalf("OpenBlockersByTasks: %v", err)
		}
		assertBlockerIDs(t, got, map[string][]string{
			"dep-1": {"blk-a"},
			"dep-2": {"blk-b"}, // blk-c (done) excluded
		})
	})
}

// --- SubtaskProgressByParents (relationRepo) -------------------------------------------

func assertSubtaskProgress(t *testing.T, got map[domain.ID]application.TaskSubtaskProgress, want map[string]application.TaskSubtaskProgress) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(out) = %d (%v), want %d (%v)", len(got), fmt.Sprintf("%v", got), len(want), want)
	}
	for k, v := range want {
		gv, ok := got[domain.ID(k)]
		if !ok || gv != v {
			t.Errorf("out[%q] = %+v (present=%v), want %+v", k, gv, ok, v)
		}
	}
}

func TestRelationRepoSubtaskProgressByParents(t *testing.T) {
	t.Parallel()

	t.Run("EmptyInput_ShortCircuitsNoSQL", func(t *testing.T) {
		db := openTestDB(t)
		got, err := newRelationRepo(db).SubtaskProgressByParents(context.Background(), nil)
		if err != nil {
			t.Fatalf("SubtaskProgressByParents(nil): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("out = %v, want empty", got)
		}
	})

	t.Run("ParentsNoChildren_ReturnsEmptyMap", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("par-1", "p1", "p1", domain.StatusTodo, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("par-2", "p1", "p2", domain.StatusDoing, false)))
		})
		got, err := newRelationRepo(db).SubtaskProgressByParents(context.Background(), []domain.ID{"par-1", "par-2"})
		if err != nil {
			t.Fatalf("SubtaskProgressByParents: %v", err)
		}
		assertSubtaskProgress(t, got, map[string]application.TaskSubtaskProgress{}) // no children → absent
	})

	t.Run("CountsDoneAndOpen_ExcludesArchivedChildren", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("par", "p1", "parent", domain.StatusTodo, false),
				rollupTask("ch-done", "p1", "done child", domain.StatusDone, false),     // counts (total + done)
				rollupTask("ch-open", "p1", "open child", domain.StatusTodo, false),     // counts (total only)
				rollupTask("ch-arch", "p1", "archived child", domain.StatusDoing, true), // excluded entirely
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
			// 'child' edge: task_id = CHILD, other_task_id = PARENT.
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r1", "ch-done", "par", domain.RelationChild, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r2", "ch-open", "par", domain.RelationChild, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r3", "ch-arch", "par", domain.RelationChild, rollupBase)))
		})
		got, err := newRelationRepo(db).SubtaskProgressByParents(context.Background(), []domain.ID{"par"})
		if err != nil {
			t.Fatalf("SubtaskProgressByParents: %v", err)
		}
		// Archived child is excluded from BOTH total and done.
		assertSubtaskProgress(t, got, map[string]application.TaskSubtaskProgress{
			"par": {Total: 2, Done: 1},
		})
	})

	t.Run("MultiParent_FanOut", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			for _, tk := range []domain.Task{
				rollupTask("par-1", "p1", "p1", domain.StatusTodo, false),
				rollupTask("par-2", "p1", "p2", domain.StatusDoing, false),
				// par-1: one done + one open → {2,1}
				rollupTask("a-done", "p1", "a", domain.StatusDone, false),
				rollupTask("a-open", "p1", "b", domain.StatusTodo, false),
				// par-2: one open → {1,0}
				rollupTask("b-open", "p1", "c", domain.StatusDoing, false),
			} {
				mustCreate(t, r.Tasks.Create(ctx, tk))
			}
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r1", "a-done", "par-1", domain.RelationChild, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r2", "a-open", "par-1", domain.RelationChild, rollupBase)))
			mustAddRel(t, r.Relations.Add(ctx, rollupRel("r3", "b-open", "par-2", domain.RelationChild, rollupBase)))
		})
		got, err := newRelationRepo(db).SubtaskProgressByParents(context.Background(), []domain.ID{"par-1", "par-2"})
		if err != nil {
			t.Fatalf("SubtaskProgressByParents: %v", err)
		}
		assertSubtaskProgress(t, got, map[string]application.TaskSubtaskProgress{
			"par-1": {Total: 2, Done: 1},
			"par-2": {Total: 1, Done: 0},
		})
	})
}

// --- CountByTask (commentRepo) ---------------------------------------------------------

func TestCommentRepoCountByTask(t *testing.T) {
	t.Parallel()

	t.Run("CountsAllCommentsForTask", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-1", "p1", "a", domain.StatusTodo, false)))
			for i := 0; i < 3; i++ {
				id := domain.ID(fmt.Sprintf("c%d", i))
				mustAddComment(t, r.Comments.Add(ctx, domain.Comment{ID: id, TaskID: "t-1", Kind: domain.CommentNote, Body: string(id), Actor: domain.Actor{Type: domain.ActorAgent, Name: "x"}, CreatedAt: rollupBase}))
			}
		})
		n, err := newCommentRepo(db).CountByTask(context.Background(), "t-1")
		if err != nil {
			t.Fatalf("CountByTask: %v", err)
		}
		if n != 3 {
			t.Errorf("count = %d, want 3", n)
		}
	})

	t.Run("ZeroComments_ReturnsZero", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-empty", "p1", "no comments", domain.StatusTodo, false)))
		})
		n, err := newCommentRepo(db).CountByTask(context.Background(), "t-empty")
		if err != nil {
			t.Fatalf("CountByTask: %v", err)
		}
		if n != 0 {
			t.Errorf("count = %d, want 0 for task with no comments", n)
		}
	})

	t.Run("IndependentPerTask_FanOut", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-1", "p1", "a", domain.StatusTodo, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-2", "p1", "b", domain.StatusTodo, false)))
			for i := 0; i < 2; i++ {
				id := domain.ID(fmt.Sprintf("a%d", i))
				mustAddComment(t, r.Comments.Add(ctx, domain.Comment{ID: id, TaskID: "t-1", Kind: domain.CommentNote, Body: string(id), Actor: domain.Actor{Type: domain.ActorAgent, Name: "x"}, CreatedAt: rollupBase}))
			}
			for i := 0; i < 5; i++ {
				id := domain.ID(fmt.Sprintf("b%d", i))
				mustAddComment(t, r.Comments.Add(ctx, domain.Comment{ID: id, TaskID: "t-2", Kind: domain.CommentNote, Body: string(id), Actor: domain.Actor{Type: domain.ActorAgent, Name: "x"}, CreatedAt: rollupBase}))
			}
		})
		repo := newCommentRepo(db)
		ctx := context.Background()
		n1, err := repo.CountByTask(ctx, "t-1")
		if err != nil {
			t.Fatalf("CountByTask(t-1): %v", err)
		}
		n2, err := repo.CountByTask(ctx, "t-2")
		if err != nil {
			t.Fatalf("CountByTask(t-2): %v", err)
		}
		if n1 != 2 || n2 != 5 {
			t.Errorf("counts = (%d, %d), want (2, 5)", n1, n2)
		}
	})

	t.Run("QueryError_SurfacesWrapped", func(t *testing.T) {
		db := openTestDB(t)
		// Close the pool so Scan on the subsequent row returns sql.ErrConnDone;
		// this is the only way to drive CountByTask's error branch with a real
		// querier (QueryRowContext returns a concrete *sql.Row, not an interface).
		if err := db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("closing db to force a query error: %v", err)
		}
		repo := newCommentRepo(db)
		if _, err := repo.CountByTask(context.Background(), "t-1"); err == nil {
			t.Fatal("CountByTask on closed db: want an error, got nil")
		} else if !strings.Contains(err.Error(), "count comments for task t-1") {
			t.Errorf("error not wrapped with task id as expected: %v", err)
		}
	})
}

// --- MaxAtByTasks (eventStore) ---------------------------------------------------------

func TestEventStoreMaxAtByTasks(t *testing.T) {
	t.Parallel()

	t.Run("EmptyInput_ShortCircuitsNoSQL", func(t *testing.T) {
		db := openTestDB(t)
		got, err := newEventStore(db).MaxAtByTasks(context.Background(), nil)
		if err != nil {
			t.Fatalf("MaxAtByTasks(nil): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("out = %v, want empty", got)
		}
	})

	t.Run("OmitsTasksWithNoEvents", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-a", "p1", "a", domain.StatusDoing, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-b", "p1", "b", domain.StatusTodo, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-c", "p1", "c", domain.StatusTodo, false))) // no events
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e1", "t-a", mustRollupTime(t, "2026-10-09T12:05:00Z"))))
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e2", "t-b", mustRollupTime(t, "2026-10-09T12:30:00Z"))))
		})
		got, err := newEventStore(db).MaxAtByTasks(context.Background(), []domain.ID{"t-a", "t-b", "t-c"})
		if err != nil {
			t.Fatalf("MaxAtByTasks: %v", err)
		}
		wantA := mustRollupTime(t, "2026-10-09T12:05:00Z")
		wantB := mustRollupTime(t, "2026-10-09T12:30:00Z")
		if a, ok := got["t-a"]; !ok || !a.Equal(wantA) {
			t.Errorf("out[t-a] = %v (present=%v), want %v", a, ok, wantA)
		}
		if b, ok := got["t-b"]; !ok || !b.Equal(wantB) {
			t.Errorf("out[t-b] = %v (present=%v), want %v", b, ok, wantB)
		}
		if _, ok := got["t-c"]; ok {
			t.Errorf("out[t-c] present, want omitted (no events)")
		}
	})

	t.Run("PicksMaxByChronology_NotFirstOrLastInserted", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-x", "p1", "x", domain.StatusDoing, false)))
			// Insert in a NON-monotonic order: 12:00, then 14:00, then 13:00. The
			// max must be 14:00 — neither the first (12:00) nor last (13:00) row.
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e-early", "t-x", mustRollupTime(t, "2026-10-09T12:00:00Z"))))
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e-latest", "t-x", mustRollupTime(t, "2026-10-09T14:00:00Z"))))
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e-mid", "t-x", mustRollupTime(t, "2026-10-09T13:00:00Z"))))
		})
		got, err := newEventStore(db).MaxAtByTasks(context.Background(), []domain.ID{"t-x"})
		if err != nil {
			t.Fatalf("MaxAtByTasks: %v", err)
		}
		want := mustRollupTime(t, "2026-10-09T14:00:00Z")
		at, ok := got["t-x"]
		if !ok || !at.Equal(want) {
			t.Errorf("out[t-x] = %v (present=%v), want %v", at, ok, want)
		}
	})

	t.Run("MultiTask_FanOut_DistinctMaxes", func(t *testing.T) {
		db := openTestDB(t)
		withRollupSeed(t, db, func(r *application.Repos) {
			ctx := context.Background()
			mustCreate(t, r.Projects.Create(ctx, rollupProject("p1", "P1")))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-1", "p1", "a", domain.StatusDoing, false)))
			mustCreate(t, r.Tasks.Create(ctx, rollupTask("t-2", "p1", "b", domain.StatusDoing, false)))
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e1", "t-1", mustRollupTime(t, "2026-10-09T10:00:00Z"))))
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e2", "t-1", mustRollupTime(t, "2026-10-09T11:00:00Z")))) // t-1 max = 11:00
			mustAppendEvent(t, r.Events.Append(ctx, rollupEvent("e3", "t-2", mustRollupTime(t, "2026-10-09T09:00:00Z")))) // t-2 max = 09:00
		})
		got, err := newEventStore(db).MaxAtByTasks(context.Background(), []domain.ID{"t-1", "t-2"})
		if err != nil {
			t.Fatalf("MaxAtByTasks: %v", err)
		}
		want1 := mustRollupTime(t, "2026-10-09T11:00:00Z")
		want2 := mustRollupTime(t, "2026-10-09T09:00:00Z")
		if a, ok := got["t-1"]; !ok || !a.Equal(want1) {
			t.Errorf("out[t-1] = %v (present=%v), want %v", a, ok, want1)
		}
		if b, ok := got["t-2"]; !ok || !b.Equal(want2) {
			t.Errorf("out[t-2] = %v (present=%v), want %v", b, ok, want2)
		}
	})
}
