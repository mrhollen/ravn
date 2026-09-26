package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// acceptanceCriterionRepo implements application.AcceptanceCriterionRepository
// over SQLite: Create (initial criteria for a fresh task), ListByTask (the
// resume payload's AC checklist), and SetDone (the complete_ac toggle).
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

// SetDone toggles the single criterion addressed by (taskID, seq) and records
// who did it and when: checking stamps note/completed_by_type/
// completed_by_name/completed_at to the given values; unchecking clears all
// three completion fields again. An unknown pair → domain.ErrNotFound. Returns
// the fresh row read back after the guarded write (house pattern — the UPDATE
// carries no RETURNING in SQLite's portable usage here). The write is a child-
// row mutation (ARCHITECTURE §4 version-bump policy): it never touches the
// parent task row's version or updated_at.
func (r *acceptanceCriterionRepo) SetDone(ctx context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error) {
	var (
		noteArg any = "" // cleared again on uncheck
		bType   any
		bName   any
		bAt     any
	)
	if done {
		noteArg = note
		bType = string(actor.Type)
		bName = actor.Name
		bAt = formatTS(now)
	}
	res, err := r.q.ExecContext(ctx, `
		UPDATE acceptance_criterion SET done = ?, note = ?,
			completed_by_type = ?, completed_by_name = ?, completed_at = ?
		WHERE task_id = ? AND seq = ?`,
		boolToInt(done), noteArg, bType, bName, bAt, string(taskID), seq)
	if err != nil {
		return domain.AcceptanceCriterion{}, fmt.Errorf("toggle acceptance criterion for task %s seq %d: %w", taskID, seq, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.AcceptanceCriterion{}, fmt.Errorf("rows affected after toggling acceptance criterion for task %s seq %d: %w", taskID, seq, err)
	}
	if n == 0 { // the pair is keyed UNIQUE(task_id, seq) — 0 rows means it does not exist
		return domain.AcceptanceCriterion{}, fmt.Errorf("%w: no acceptance criterion with seq %d on task %s", domain.ErrNotFound, seq, taskID)
	}
	row := r.q.QueryRowContext(ctx,
		`SELECT `+acceptanceColumns+` FROM acceptance_criterion WHERE task_id = ? AND seq = ?`, string(taskID), seq)
	c, err := scanAcceptanceCriterion(row) // re-read the stored result of the guarded write
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AcceptanceCriterion{}, domain.ErrNotFound // defensive: impossible inside one tx
	}
	return c, err
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
