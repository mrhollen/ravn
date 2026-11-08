package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// repos_edge_test.go covers the classification branches of the child-row and
// lease methods that had no direct tests: UpdateNote / Delete / Reorder /
// ProgressByTasks on acceptance criteria, artifact Delete, task Claim/Heartbeat
// rejections for archived/done tasks, and task Create's idempotent path. Seeds
// go through UnitOfWork (withRollupSeed) so FK/CHECK constraints behave like
// production; the read-backs happen in a second transaction or via direct repo
// calls on the pool.

var edgeActor = domain.Actor{Type: domain.ActorAgent, Name: "edge-tester"}

func edgeSeedTask(t *testing.T, db *sql.DB) domain.ID {
	t.Helper()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(context.Background(), rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(context.Background(), rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
	})
	return taskID
}

func edgeSeedACs(t *testing.T, db *sql.DB, texts ...string) {
	t.Helper()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		crits := make([]domain.AcceptanceCriterion, 0, len(texts))
		for i, text := range texts {
			crits = append(crits, domain.AcceptanceCriterion{
				ID: domain.ID("e-ac-" + string(rune('0'+i))), TaskID: taskID, Seq: i + 1, Text: text,
			})
		}
		if err := r.ACs.Create(context.Background(), crits); err != nil {
			t.Fatalf("seed acceptance criteria: %v", err)
		}
	})
}

func TestAcceptance_UpdateNote_RewritesAndReadsBack(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{{ID: "e-ac-1", TaskID: taskID, Seq: 1, Text: "original text"}}))
	})

	var got domain.AcceptanceCriterion
	var runErr error
	var r application.Repos
	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		var err error
		got, err = r.ACs.UpdateNote(ctx, taskID, 1, "verified by QA")
		return err
	})
	if runErr != nil {
		t.Fatalf("UpdateNote: %v", runErr)
	}
	if got.Note != "verified by QA" || got.Text != "original text" || got.Done {
		t.Errorf("UpdateNote read-back = %+v, want note rewritten with text/done untouched", got)
	}
}

func TestAcceptance_UpdateNote_UnknownSeq_ErrorsNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
	})

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.UpdateNote(ctx, taskID, 99, "nope")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdateNote(unknown seq) = %v, want ErrNotFound", err)
	}
}

func TestAcceptance_SetDone_UnknownSeq_ErrorsNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
	})

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.SetDone(ctx, taskID, 99, true, "note", edgeActor, rollupBase)
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetDone(unknown seq) = %v, want ErrNotFound", err)
	}
}

func TestAcceptance_UpdateText_UnknownSeq_ErrorsNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
	})

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.UpdateText(ctx, taskID, 99, "nope")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdateText(unknown seq) = %v, want ErrNotFound", err)
	}
}

func TestAcceptance_Delete_ReturnsDeletedRowAndRejectsUnknownSeq(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{
			{ID: "e-ac-1", TaskID: taskID, Seq: 1, Text: "a"},
			{ID: "e-ac-2", TaskID: taskID, Seq: 2, Text: "b"},
		}))
	})

	var deleted domain.AcceptanceCriterion
	var r application.Repos
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		var err error
		deleted, err = r.ACs.Delete(ctx, taskID, 2)
		return err
	})
	if runErr != nil {
		t.Fatalf("Delete: %v", runErr)
	}
	if deleted.Text != "b" || deleted.Seq != 2 {
		t.Errorf("deleted row = %+v, want the BEFORE-state of seq 2 (text \"b\")", deleted)
	}

	listed, lerr := newAcceptanceCriterionRepo(db).ListByTask(ctx, taskID)
	if lerr != nil {
		t.Fatalf("ListByTask after delete: %v", lerr)
	}
	if len(listed) != 1 || listed[0].Seq != 1 || listed[0].Text != "a" {
		t.Errorf("criteria after delete = %+v, want only seq 1 (\"a\") surviving with its original seq", listed)
	}

	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.Delete(ctx, taskID, 99)
		return err
	})
	if !errors.Is(runErr, domain.ErrNotFound) {
		t.Errorf("Delete(unknown seq) = %v, want ErrNotFound", runErr)
	}
}

func TestAcceptance_Reorder_MovesCriteriaToRequestedPositions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{
			{ID: "e-ac-1", TaskID: taskID, Seq: 1, Text: "a"},
			{ID: "e-ac-2", TaskID: taskID, Seq: 2, Text: "b"},
			{ID: "e-ac-3", TaskID: taskID, Seq: 3, Text: "c"},
		}))
	})

	var reordered []domain.AcceptanceCriterion
	var r application.Repos
	// Current seq 3 ("c") → position 1, seq 1 ("a") → position 2, seq 2 ("b") → position 3.
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		var err error
		reordered, err = r.ACs.Reorder(ctx, taskID, []int{3, 1, 2})
		return err
	})
	if runErr != nil {
		t.Fatalf("Reorder: %v", runErr)
	}
	wantTexts := []string{"c", "a", "b"}
	for i, c := range reordered {
		if c.Seq != i+1 || c.Text != wantTexts[i] {
			t.Errorf("reordered[%d] = (seq %d, %q), want (seq %d, %q)", i, c.Seq, c.Text, i+1, wantTexts[i])
		}
	}
}

func TestAcceptance_Reorder_CountMismatch_ErrorsInvalid(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{
			{ID: "e-ac-1", TaskID: taskID, Seq: 1, Text: "a"},
			{ID: "e-ac-2", TaskID: taskID, Seq: 2, Text: "b"},
			{ID: "e-ac-3", TaskID: taskID, Seq: 3, Text: "c"},
		}))
	})

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.Reorder(ctx, taskID, []int{1, 2}) // only 2 of the 3 seqs listed
		return err
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("Reorder with mismatched count = %v, want ErrInvalid", err)
	}
}

func TestAcceptance_Reorder_UnknownSeq_ErrorsInvalidWithoutPartialWrites(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Edge Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{
			{ID: "e-ac-1", TaskID: taskID, Seq: 1, Text: "a"},
			{ID: "e-ac-2", TaskID: taskID, Seq: 2, Text: "b"},
			{ID: "e-ac-3", TaskID: taskID, Seq: 3, Text: "c"},
		}))
	})

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.ACs.Reorder(ctx, taskID, []int{1, 2, 9}) // seq 9 does not exist
		return err
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Reorder with unknown seq = %v, want ErrInvalid", err)
	}

	// The validation happens BEFORE any UPDATE, so nothing may have moved.
	listed, lerr := newAcceptanceCriterionRepo(db).ListByTask(ctx, taskID)
	if lerr != nil {
		t.Fatalf("ListByTask after failed reorder: %v", lerr)
	}
	wantTexts := []string{"a", "b", "c"}
	for i, c := range listed {
		if c.Seq != i+1 || c.Text != wantTexts[i] {
			t.Errorf("criteria after failed reorder = (seq %d, %q), want untouched (seq %d, %q)", c.Seq, c.Text, i+1, wantTexts[i])
		}
	}
}

func TestAcceptance_ProgressByTasks_AggregatesPerTaskAndShortCircuitsEmpty(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const (
		t1 = domain.ID("e-prog-1")
		t2 = domain.ID("e-prog-2")
		tx = domain.ID("e-prog-none") // no criteria at all → omitted
	)
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		for _, tk := range []domain.Task{
			rollupTask(t1, "e-proj", "Prog Task 1", domain.StatusTodo, false),
			rollupTask(t2, "e-proj", "Prog Task 2", domain.StatusDoing, false),
		} {
			mustCreate(t, r.Tasks.Create(ctx, tk))
		}
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{
			{ID: "e-pac-1", TaskID: t1, Seq: 1, Text: "a1"},
			{ID: "e-pac-2", TaskID: t1, Seq: 2, Text: "a2", Done: true},
			{ID: "e-pac-3", TaskID: t1, Seq: 3, Text: "a3"},
			{ID: "e-pac-4", TaskID: t2, Seq: 1, Text: "b1"},
			{ID: "e-pac-5", TaskID: t2, Seq: 2, Text: "b2"},
		}))
	})

	repo := newAcceptanceCriterionRepo(db)
	got, err := repo.ProgressByTasks(ctx, []domain.ID{t1, t2, tx})
	if err != nil {
		t.Fatalf("ProgressByTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ProgressByTasks = %+v (len %d), want exactly the two tasks with criteria", got, len(got))
	}
	if p := got[t1]; p.Total != 3 || p.Done != 1 {
		t.Errorf("progress[%s] = %+v, want {Total: 3, Done: 1}", t1, p)
	}
	if p := got[t2]; p.Total != 2 || p.Done != 0 {
		t.Errorf("progress[%s] = %+v, want {Total: 2, Done: 0}", t2, p)
	}
	if _, ok := got[tx]; ok {
		t.Errorf("task without criteria must be omitted from the map, got %+v", got[tx])
	}

	empty, err := repo.ProgressByTasks(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("ProgressByTasks(nil) = (%+v, %v), want empty map and nil error", empty, err)
	}
}

func TestArtifact_Delete_ReturnsDeletedRowAndRejectsUnknownID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := edgeSeedTask(t, db)

	const artID = domain.ID("e-art-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Artifacts.Add(ctx, domain.Artifact{
			ID: artID, TaskID: taskID, Kind: domain.ArtifactKind("log"), Ref: "/tmp/edge.log",
			Summary: "seed log", Actor: edgeActor, CreatedAt: rollupBase,
		}))
	})

	var deleted domain.Artifact
	var r application.Repos
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		var err error
		deleted, err = r.Artifacts.Delete(ctx, taskID, artID)
		return err
	})
	if runErr != nil {
		t.Fatalf("artifact Delete: %v", runErr)
	}
	if deleted.ID != artID || deleted.Kind != domain.ArtifactKind("log") || deleted.Summary != "seed log" {
		t.Errorf("deleted artifact = %+v, want the before-state of %q", deleted, artID)
	}

	listed, lerr := newArtifactRepo(db).ListByTask(ctx, taskID)
	if lerr != nil {
		t.Fatalf("ListByTask after delete: %v", lerr)
	}
	if len(listed) != 0 {
		t.Errorf("artifacts after delete = %+v, want none", listed)
	}

	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.Artifacts.Delete(ctx, taskID, domain.ID("e-art-missing"))
		return err
	})
	if !errors.Is(runErr, domain.ErrNotFound) {
		t.Errorf("artifact Delete(unknown id) = %v, want ErrNotFound", runErr)
	}
}

func TestTask_Claim_RejectsArchivedAndDoneTasks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := edgeSeedTask(t, db)

	if _, err := db.ExecContext(ctx, `UPDATE task SET archived = 1 WHERE id = ?`, string(taskID)); err != nil {
		t.Fatalf("archive seed task: %v", err)
	}
	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		_, err := r.Tasks.Claim(ctx, taskID, edgeActor, 900, rollupBase)
		return err
	})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "archived") {
		t.Errorf("Claim(archived task) = %v, want ErrInvalid naming the archived state", err)
	}

	db2 := openTestDB(t) // fresh DB: the second case needs an unarchived done task
	taskID2 := edgeSeedTask(t, db2)
	if _, err := db2.ExecContext(ctx, `UPDATE task SET status = 'done' WHERE id = ?`, string(taskID2)); err != nil {
		t.Fatalf("complete seed task: %v", err)
	}
	var r2 application.Repos
	err = NewUnitOfWork(db2).Run(ctx, &r2, func() error {
		_, err := r2.Tasks.Claim(ctx, taskID2, edgeActor, 900, rollupBase)
		return err
	})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "completed") {
		t.Errorf("Claim(done task) = %v, want ErrInvalid naming the completed state", err)
	}
}

func TestTask_Heartbeat_ClassifiesUnknownArchivedAndDone(t *testing.T) {
	ctx := context.Background()

	db0 := openTestDB(t)
	var r application.Repos
	err := NewUnitOfWork(db0).Run(ctx, &r, func() error {
		_, err := r.Tasks.Heartbeat(ctx, domain.ID("e-no-such-task"), edgeActor, rollupBase)
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Heartbeat(unknown task) = %v, want ErrNotFound", err)
	}

	dbA := openTestDB(t)
	taskA := edgeSeedTask(t, dbA)
	var rA application.Repos
	runErr := NewUnitOfWork(dbA).Run(ctx, &rA, func() error {
		if _, err := rA.Tasks.Claim(ctx, taskA, edgeActor, 900, rollupBase); err != nil {
			return err
		}
		return nil
	})
	if runErr != nil {
		t.Fatalf("claim for archived case: %v", runErr)
	}
	if _, err := dbA.ExecContext(ctx, `UPDATE task SET archived = 1 WHERE id = ?`, string(taskA)); err != nil {
		t.Fatalf("archive: %v", err)
	}
	runErr = NewUnitOfWork(dbA).Run(ctx, &rA, func() error {
		_, err := rA.Tasks.Heartbeat(ctx, taskA, edgeActor, rollupBase)
		return err
	})
	if !errors.Is(runErr, domain.ErrInvalid) || !strings.Contains(runErr.Error(), "archived") {
		t.Errorf("Heartbeat(archived task) = %v, want ErrInvalid naming the archived state", runErr)
	}

	dbD := openTestDB(t)
	taskD := edgeSeedTask(t, dbD)
	var rD application.Repos
	runErr = NewUnitOfWork(dbD).Run(ctx, &rD, func() error {
		if _, err := rD.Tasks.Claim(ctx, taskD, edgeActor, 900, rollupBase); err != nil {
			return err
		}
		return nil
	})
	if runErr != nil {
		t.Fatalf("claim for done case: %v", runErr)
	}
	if _, err := dbD.ExecContext(ctx, `UPDATE task SET status = 'done' WHERE id = ?`, string(taskD)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	runErr = NewUnitOfWork(dbD).Run(ctx, &rD, func() error {
		_, err := rD.Tasks.Heartbeat(ctx, taskD, edgeActor, rollupBase)
		return err
	})
	if !errors.Is(runErr, domain.ErrInvalid) || !strings.Contains(runErr.Error(), "completed") {
		t.Errorf("Heartbeat(done task) = %v, want ErrInvalid naming the completed state", runErr)
	}
}

func TestTask_Create_IdempotentOnExistingID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("e-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("e-proj", "Edge Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "e-proj", "Original Title", domain.StatusTodo, false)))
	})

	// Second insert with the SAME id but different fields: must be a silent no-op.
	var r application.Repos
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		dup := rollupTask(taskID, "e-proj", "Duplicate Title", domain.StatusDoing, true)
		return r.Tasks.Create(ctx, dup)
	})
	if runErr != nil {
		t.Fatalf("re-Create with existing id = %v, want nil (idempotent no-op)", runErr)
	}

	got, err := newTaskRepo(db).Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get after re-create: %v", err)
	}
	if got.Title != "Original Title" || got.Status != domain.StatusTodo || got.Archived || got.Version != 1 {
		t.Errorf("stored row after idempotent re-create = %+v, want the ORIGINAL row untouched (title/status/archived/version)", got)
	}
}
