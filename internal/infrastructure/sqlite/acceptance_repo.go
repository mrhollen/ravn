package sqlite

import (
	"context"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// acceptanceCriterionRepo will implement application.AcceptanceCriterionRepository
// over SQLite (slice 2C). Slice 2A wires the type into Repos only.
type acceptanceCriterionRepo struct {
	q querier
}

func newAcceptanceCriterionRepo(q querier) *acceptanceCriterionRepo {
	return &acceptanceCriterionRepo{q: q}
}

var _ application.AcceptanceCriterionRepository = (*acceptanceCriterionRepo)(nil)

func (r *acceptanceCriterionRepo) Create(ctx context.Context, criteria []domain.AcceptanceCriterion) error {
	return notYet("Create", "2C")
}

func (r *acceptanceCriterionRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.AcceptanceCriterion, error) {
	return nil, notYet("ListByTask", "2C")
}

func (r *acceptanceCriterionRepo) SetDone(ctx context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error) {
	return domain.AcceptanceCriterion{}, notYet("SetDone", "2C")
}
