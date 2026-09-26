package sqlite

import (
	"context"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// commentRepo will implement application.CommentRepository over SQLite
// (slice 2C). Slice 2A wires the type into Repos only.
type commentRepo struct {
	q querier
}

func newCommentRepo(q querier) *commentRepo { return &commentRepo{q: q} }

var _ application.CommentRepository = (*commentRepo)(nil)

func (r *commentRepo) Add(ctx context.Context, c domain.Comment) error {
	return notYet("Add", "2C")
}

func (r *commentRepo) ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, error) {
	return nil, notYet("ListByTask", "2C")
}
