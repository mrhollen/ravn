package sqlite

import (
	"context"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// relationRepo will implement application.RelationRepository over SQLite
// (slice 2D), including the upsert keyed by (task_id, other_task_id, kind).
// Slice 2A wires the type into Repos only.
type relationRepo struct {
	q querier
}

func newRelationRepo(q querier) *relationRepo { return &relationRepo{q: q} }

var _ application.RelationRepository = (*relationRepo)(nil)

func (r *relationRepo) Add(ctx context.Context, rel domain.Relation) error {
	return notYet("Add", "2D")
}

func (r *relationRepo) Remove(ctx context.Context, taskID, otherTaskID domain.ID, kind domain.RelationKind) error {
	return notYet("Remove", "2D")
}

func (r *relationRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Relation, error) {
	return nil, notYet("ListByTask", "2D")
}
