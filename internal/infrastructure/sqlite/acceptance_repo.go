package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// acceptanceCriterionRepo implements application.AcceptanceCriterionRepository
// over SQLite. Slice 2B lands Create (needed by the Tasks.Create use case) and
// ListByTask (the resume payload's AC checklist); SetDone lands in slice 2C.
type acceptanceCriterionRepo struct {
	q querier
}

func newAcceptanceCriterionRepo(q querier) *acceptanceCriterionRepo {
	return &acceptanceCriterionRepo{q: q}
}

var _ application.AcceptanceCriterionRepository = (*acceptanceCriterionRepo)(nil)

// acceptanceColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanAcceptanceCriterion's Scan targets.
const acceptanceColumns = "id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at"

// Create stores the initial criteria for a freshly created task exactly as
// given (IDs and dense 1-based Seq values are minted by the use case). The
// caller is expected to run this inside the same unit of work as the task
// insert so both commit or roll back together. Constraint failures — duplicate
// (task_id, seq) or an unknown task (FK) — surface as wrapped domain.ErrInvalid.
func (r *acceptanceCriterionRepo) Create(ctx context.Context, criteria []domain.AcceptanceCriterion) error {
	for _, c := range criteria {
		_, err := r.q.ExecContext(ctx,
			`INSERT INTO acceptance_criterion (`+acceptanceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(c.ID), string(c.TaskID), c.Seq, c.Text, boolToInt(c.Done), c.Note,
			actorTypeOrNil(c.CompletedBy), actorNameOrNil(c.CompletedBy), tsOrNil(c.CompletedAt))
		if err != nil {
			if isConstraintViolation(err) {
				return fmt.Errorf("%w: acceptance criterion for task %s seq %d violates a constraint", domain.ErrInvalid, c.TaskID, c.Seq)
			}
			return fmt.Errorf("insert acceptance criterion %s: %w", c.ID, err)
		}
	}
	return nil
}

// ListByTask returns the task's criteria ordered by Seq ASC (empty slice if it
// has none) — the resume payload's AC checklist.
func (r *acceptanceCriterionRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.AcceptanceCriterion, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+acceptanceColumns+` FROM acceptance_criterion WHERE task_id = ? ORDER BY seq ASC`,
		string(taskID))
	if err != nil {
		return nil, fmt.Errorf("list acceptance criteria for task %s: %w", taskID, err)
	}
	defer rows.Close()

	out := make([]domain.AcceptanceCriterion, 0)
	for rows.Next() {
		c, err := scanAcceptanceCriterion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan acceptance criterion row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *acceptanceCriterionRepo) SetDone(ctx context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error) {
	return domain.AcceptanceCriterion{}, notYet("acceptance criterion repository", "SetDone", "2C")
}

// scanAcceptanceCriterion maps one row (in acceptanceColumns order) to a
// domain.AcceptanceCriterion; the completion trio round-trips NULL → nil.
func scanAcceptanceCriterion(s scanner) (domain.AcceptanceCriterion, error) {
	var (
		c             domain.AcceptanceCriterion
		taskID        string
		seq           int
		text          string
		done          int
		note          string
		completedType *string
		completedName *string
		completedAt   *string
	)
	if err := s.Scan(&c.ID, &taskID, &seq, &text, &done, &note, &completedType, &completedName, &completedAt); err != nil {
		return domain.AcceptanceCriterion{}, err
	}
	c.TaskID = domain.ID(taskID)
	c.Seq = seq
	c.Text = text
	c.Done = done != 0
	c.Note = note
	c.CompletedBy = actorOrNil(completedType, completedName)
	if completedAt != nil {
		ts, err := parseTS(*completedAt)
		if err != nil {
			return domain.AcceptanceCriterion{}, fmt.Errorf("acceptance criterion %s completed_at %q: %w", c.ID, *completedAt, err)
		}
		c.CompletedAt = &ts
	}
	return c, nil
}
