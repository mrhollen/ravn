package sqlite

import (
	"context"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// relationRepo implements application.RelationRepository over SQLite. Slice 2B
// lands the ListByTask read (the resume payload's raw edge list — both
// directions); Add/Remove land in slice 2D with the upsert keyed by
// (task_id, other_task_id, kind).
type relationRepo struct {
	q querier
}

func newRelationRepo(q querier) *relationRepo { return &relationRepo{q: q} }

var _ application.RelationRepository = (*relationRepo)(nil)

func (r *relationRepo) Add(ctx context.Context, rel domain.Relation) error {
	return notYet("relation repository", "Add", "2D")
}

func (r *relationRepo) Remove(ctx context.Context, taskID, otherTaskID domain.ID, kind domain.RelationKind) error {
	return notYet("relation repository", "Remove", "2D")
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
