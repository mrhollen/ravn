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

// repos_adversarial_test.go feeds hostile rows and constraints to the repo
// layer: schema constraint violations (FK/CHECK/unique) must surface as
// domain.ErrInvalid — never raw driver text leaking into callers — and corrupt
// timestamp columns stored out-of-band must make READS fail with a named-field
// error instead of silently yielding zero times or panicking. Seeds go through
// UnitOfWork; the corruption is deliberately applied via raw SQL on the pool,
// which is exactly how a row would get mangled by an out-of-band writer.

func adSeedTaskWithAC(t *testing.T, db *sql.DB) domain.ID {
	t.Helper()
	const taskID = domain.ID("ad-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(context.Background(), rollupProject("ad-proj", "Adversarial Project")))
		mustCreate(t, r.Tasks.Create(context.Background(), rollupTask(taskID, "ad-proj", "Adversarial Task", domain.StatusTodo, false)))
	})
	return taskID
}

func TestAcceptance_Create_Add_RejectConstraintViolations(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := adSeedTaskWithAC(t, db)

	mkCrits := func(ids ...string) []domain.AcceptanceCriterion {
		crits := make([]domain.AcceptanceCriterion, 0, len(ids))
		for i, id := range ids {
			crits = append(crits, domain.AcceptanceCriterion{ID: domain.ID(id), TaskID: taskID, Seq: i + 1, Text: "c"})
		}
		return crits
	}

	var r application.Repos
	// Foreign-key violation: criteria for a task that does not exist.
	ghost := mkCrits("ad-ac-ghost")
	ghost[0].TaskID = domain.ID("ad-no-such-task")
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.ACs.Create(ctx, ghost)
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("AC Create(unknown task FK) = %v, want ErrInvalid", runErr)
	}

	// Unique violation: the same (task, seq) pair twice in one batch. The
	// second criterion targets the SAME task with a duplicated seq 1.
	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		dup := mkCrits("ad-ac-1", "ad-ac-2")
		dup[1].Seq = dup[0].Seq // both claim seq 1
		return r.ACs.Create(ctx, dup)
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("AC Create(duplicate seq) = %v, want ErrInvalid", runErr)
	}

	// Add has the same two shapes.
	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.ACs.Add(ctx, domain.AcceptanceCriterion{ID: "ad-ac-g2", TaskID: domain.ID("ad-no-such-task"), Seq: 9, Text: "g"})
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("AC Add(unknown task FK) = %v, want ErrInvalid", runErr)
	}
	// Seed one criterion (seq 7), then Add a second claiming the same seq.
	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.ACs.Create(ctx, []domain.AcceptanceCriterion{{ID: "ad-ac-7", TaskID: taskID, Seq: 7, Text: "seed"}})
	})
	if runErr != nil {
		t.Fatalf("seed seq-7 criterion: %v", runErr)
	}
	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.ACs.Add(ctx, domain.AcceptanceCriterion{ID: "ad-ac-7b", TaskID: taskID, Seq: 7, Text: "dup"})
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("AC Add(duplicate seq) = %v, want ErrInvalid", runErr)
	}
}

func TestTask_Create_RejectsBogusStatusAndPriority(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("ad-proj", "Adversarial Project")))
	})

	var r application.Repos
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.Tasks.Create(ctx, rollupTask(domain.ID("ad-bogus-1"), "ad-proj", "Bogus Status", domain.Status("weird"), false))
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("Task Create(bogus status FK) = %v, want ErrInvalid", runErr)
	}

	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		tooHot := rollupTask(domain.ID("ad-bogus-2"), "ad-proj", "Too High Priority", domain.StatusTodo, false)
		tooHot.Priority = 9 // CHECK (priority BETWEEN 0 AND 5)
		return r.Tasks.Create(ctx, tooHot)
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("Task Create(priority 9) = %v, want ErrInvalid", runErr)
	}
}

func TestArtifact_Add_RejectsBogusKindAndForeignTask(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := adSeedTaskWithAC(t, db)

	var r application.Repos
	runErr := NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.Artifacts.Add(ctx, domain.Artifact{
			ID: "ad-art-1", TaskID: taskID, Kind: domain.ArtifactKind("bogus"), Ref: "/tmp/x", CreatedAt: rollupBase,
		})
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("Artifact Add(bogus kind CHECK) = %v, want ErrInvalid", runErr)
	}

	runErr = NewUnitOfWork(db).Run(ctx, &r, func() error {
		return r.Artifacts.Add(ctx, domain.Artifact{
			ID: "ad-art-2", TaskID: domain.ID("ad-no-such-task"), Kind: domain.ArtifactKind("log"), Ref: "/tmp/x", CreatedAt: rollupBase,
		})
	})
	if !errors.Is(runErr, domain.ErrInvalid) {
		t.Errorf("Artifact Add(unknown task FK) = %v, want ErrInvalid", runErr)
	}
}

// corruptTaskField writes a non-timestamp string into one task timestamp column
// and then re-reads the row; every read path must fail naming that field.
func TestTask_Get_FailsOnCorruptTimestampColumns(t *testing.T) {
	cases := []struct {
		name    string
		corrupt string // column to mangle (NULL-safe: set directly)
		want    string // field name the error must carry
	}{
		{"due_at", `UPDATE task SET due_at = 'not-a-ts' WHERE id = ?`, "due_at"},
		{"completed_at", `UPDATE task SET completed_at = 'not-a-ts' WHERE id = ?`, "completed_at"},
		{"claim_expires_at", `UPDATE task SET claim_expires_at = 'not-a-ts', claimed_by_type = 'agent', claimed_by_name = 'ghost' WHERE id = ?`, "claim_expires_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			taskID := adSeedTaskWithAC(t, db)
			if _, err := db.ExecContext(ctx, tc.corrupt, string(taskID)); err != nil {
				t.Fatalf("corrupt %s: %v", tc.name, err)
			}
			got, err := newTaskRepo(db).Get(ctx, taskID)
			if err == nil {
				t.Fatalf("Get with corrupt %s = %+v, want an error naming the field", tc.want, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q so operators can locate the mangled column", err, tc.want)
			}
		})
	}
}

func TestTask_Get_FailsOnCorruptLastActivityDerivedFromComment(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := adSeedTaskWithAC(t, db)
	withRollupSeed(t, db, func(r *application.Repos) {
		mustAddComment(t, r.Comments.Add(ctx, domain.Comment{
			ID: "ad-com-1", TaskID: taskID, Kind: domain.CommentNote, Body: "seed",
			Actor: edgeActor, CreatedAt: rollupBase,
		}))
	})
	// 'zzz' sorts above every RFC3339 stamp, so MAX(updated_at, comment…) picks
	// the corrupt value and the derived last_activity becomes unparseable.
	if _, err := db.ExecContext(ctx, `UPDATE comment SET created_at = 'zzz-bad' WHERE id = 'ad-com-1'`); err != nil {
		t.Fatalf("corrupt comment: %v", err)
	}
	got, err := newTaskRepo(db).Get(ctx, taskID)
	if err == nil {
		t.Fatalf("Get with corrupt derived last_activity = %+v, want an error", got)
	}
	if !strings.Contains(err.Error(), "last_activity") {
		t.Errorf("error = %q, want it to name last_activity", err)
	}
}

func TestAcceptance_ListByTask_FailsOnCorruptCompletedAt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const taskID = domain.ID("ad-task-1")
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Projects.Create(ctx, rollupProject("ad-proj", "Adversarial Project")))
		mustCreate(t, r.Tasks.Create(ctx, rollupTask(taskID, "ad-proj", "Adversarial Task", domain.StatusTodo, false)))
		mustCreate(t, r.ACs.Create(ctx, []domain.AcceptanceCriterion{{ID: "ad-ac-1", TaskID: taskID, Seq: 1, Text: "c"}}))
	})
	if _, err := db.ExecContext(ctx, `UPDATE acceptance_criterion SET completed_at = 'not-a-ts' WHERE id = 'ad-ac-1'`); err != nil {
		t.Fatalf("corrupt ac completed_at: %v", err)
	}
	got, err := newAcceptanceCriterionRepo(db).ListByTask(ctx, taskID)
	if err == nil {
		t.Fatalf("ListByTask with corrupt completed_at = %+v, want an error", got)
	}
	if !strings.Contains(err.Error(), "completed_at") {
		t.Errorf("error = %q, want it to name completed_at", err)
	}
}

func TestArtifact_ListByTask_FailsOnCorruptCreatedAt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	taskID := adSeedTaskWithAC(t, db)
	withRollupSeed(t, db, func(r *application.Repos) {
		mustCreate(t, r.Artifacts.Add(ctx, domain.Artifact{
			ID: "ad-art-1", TaskID: taskID, Kind: domain.ArtifactKind("log"), Ref: "/tmp/x", CreatedAt: rollupBase,
		}))
	})
	if _, err := db.ExecContext(ctx, `UPDATE artifact SET created_at = 'not-a-ts' WHERE id = 'ad-art-1'`); err != nil {
		t.Fatalf("corrupt artifact created_at: %v", err)
	}
	got, err := newArtifactRepo(db).ListByTask(ctx, taskID)
	if err == nil {
		t.Fatalf("ListByTask with corrupt created_at = %+v, want an error", got)
	}
	if !strings.Contains(err.Error(), "created_at") {
		t.Errorf("error = %q, want it to name created_at", err)
	}
}
