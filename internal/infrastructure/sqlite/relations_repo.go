package sqlite

import (
	"context"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// relationRepo implements application.RelationRepository over SQLite. Slice 2B
// landed the ListByTask read (the resume payload's raw edge list — both
// directions); slice 2D2a lands Add (idempotent upsert keyed by
// (task_id, other_task_id, kind), with the ≤1-parent partial-index violation
// translated to domain.ErrParentConflict) and Remove (composite-key delete).
type relationRepo struct {
	q querier
}

func newRelationRepo(q querier) *relationRepo { return &relationRepo{q: q} }

var _ application.RelationRepository = (*relationRepo)(nil)

// Add upserts the edge keyed by (task_id, other_task_id, kind): an identical
// existing row is left untouched (original ID and created_at preserved) and
// Add returns nil — the retry-safe no-op the port contract promises. The use
// case validates existence, same-project, self-edges, and the one-parent rule
// BEFORE calling; what this method still guards against:
//
//   - the ≤1-parent partial unique index (migration 0003): a kind='child' row
//     whose task_id already has a DIFFERENT parent trips UNIQUE(task_id) WHERE
//     kind='child' — mapped onto domain.ErrParentConflict. This is the
//     backstop for a concurrent writer winning the race between the use case's
//     pre-check and this upsert; under normal serialized operation the use case
//     rejects such adds earlier with the same sentinel.
//   - any other constraint failure (FK to a vanished task, CHECK) surfaces as
//     wrapped domain.ErrInvalid — invalid input, not store trouble. The
//     composite triple itself can never fail here: ON CONFLICT absorbs it.
func (r *relationRepo) Add(ctx context.Context, rel domain.Relation) error {
	res, err := r.q.ExecContext(ctx,
		`INSERT INTO relation (`+relationColumns+`) VALUES (?, ?, ?, ?, ?) `+
			`ON CONFLICT(task_id, other_task_id, kind) DO NOTHING`,
		string(rel.ID), string(rel.TaskID), string(rel.OtherTaskID), string(rel.Kind), formatTS(rel.CreatedAt))
	if err != nil {
		if isConstraintViolation(err) {
			if rel.Kind == domain.RelationChild { // only the partial index can fire on a child row
				return fmt.Errorf("%w: task %s already has a parent", domain.ErrParentConflict, rel.TaskID)
			}
			return fmt.Errorf("%w: relation %s violates a schema constraint", domain.ErrInvalid, rel.ID)
		}
		return fmt.Errorf("insert relation %s: %w", rel.ID, err)
	}
	if n, cerr := res.RowsAffected(); cerr != nil {
		return fmt.Errorf("rows affected after insert relation %s: %w", rel.ID, cerr)
	} else if n == 0 {
		return nil // idempotent: the identical edge is already stored; row untouched
	}
	return nil
}

// Remove deletes exactly the edge keyed by (taskID, otherTaskID, kind) — the
// same composite REST's DELETE uses; relation IDs are never part of the key.
// A missing edge is domain.ErrNotFound (the caller-facing "nothing to remove"),
// never a silent success: silently succeeding would hide a double-remove retry
// that had already converged on the first pass.
func (r *relationRepo) Remove(ctx context.Context, taskID, otherTaskID domain.ID, kind domain.RelationKind) error {
	res, err := r.q.ExecContext(ctx,
		`DELETE FROM relation WHERE task_id = ? AND other_task_id = ? AND kind = ?`,
		string(taskID), string(otherTaskID), string(kind))
	if err != nil {
		return fmt.Errorf("delete relation (%s -> %s, %s): %w", taskID, otherTaskID, kind, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after delete relation: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// relationColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanRelation's Scan targets.
const relationColumns = "id, task_id, other_task_id, kind, created_at"

// ListByTask returns every edge touching taskID in either direction — outgoing
// (task_id = ?) and incoming (other_task_id = ?) — ordered by created_at ASC
// (rowid ASC tie-break). The resume payload renders these raw edges; the
// computed "blocked" flag derives from the outgoing blocked-by subset at read
// time (slice 2D), so both directions must come back.
func (r *relationRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Relation, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+relationColumns+` FROM relation WHERE task_id = ? OR other_task_id = ? ORDER BY created_at ASC, rowid ASC`,
		string(taskID), string(taskID))
	if err != nil {
		return nil, fmt.Errorf("list relations for task %s: %w", taskID, err)
	}
	defer rows.Close()

	out := make([]domain.Relation, 0)
	for rows.Next() {
		rel, err := scanRelation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan relation row: %w", err)
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// scanRelation maps one row (in relationColumns order) to a domain.Relation.
func scanRelation(s scanner) (domain.Relation, error) {
	var (
		rel       domain.Relation
		taskID    string
		otherID   string
		kind      string
		createdAt string
	)
	if err := s.Scan(&rel.ID, &taskID, &otherID, &kind, &createdAt); err != nil {
		return domain.Relation{}, err
	}
	rel.TaskID = domain.ID(taskID)
	rel.OtherTaskID = domain.ID(otherID)
	rel.Kind = domain.RelationKind(kind)
	at, err := parseTS(createdAt)
	if err != nil {
		return domain.Relation{}, fmt.Errorf("relation %s created_at %q: %w", rel.ID, createdAt, err)
	}
	rel.CreatedAt = at
	return rel, nil
}
