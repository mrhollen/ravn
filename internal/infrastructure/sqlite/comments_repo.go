package sqlite

import (
	"context"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// commentRepo implements application.CommentRepository over SQLite. Slice 2B
// lands the ListByTask read (the resume payload's comment tail); Add lands in
// slice 2C.
type commentRepo struct {
	q querier
}

func newCommentRepo(q querier) *commentRepo { return &commentRepo{q: q} }

var _ application.CommentRepository = (*commentRepo)(nil)

func (r *commentRepo) Add(ctx context.Context, c domain.Comment) error {
	return notYet("comment repository", "Add", "2C")
}

// commentColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanComment's Scan targets.
const commentColumns = "id, task_id, kind, body, actor_type, actor_name, created_at"

// ListByTask returns up to limit comments newest first (created_at DESC; rowid
// DESC breaks same-timestamp ties in insertion order), skipping offset.
// limit <= 0 means unbounded — clamping is the use case's job.
func (r *commentRepo) ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, error) {
	query := `SELECT ` + commentColumns + ` FROM comment WHERE task_id = ? ORDER BY created_at DESC, rowid DESC`
	args := []any{string(taskID)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	if offset > 0 {
		query += ` OFFSET ?`
		args = append(args, offset)
	}

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list comments for task %s: %w", taskID, err)
	}
	defer rows.Close()

	out := make([]domain.Comment, 0)
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan comment row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanComment maps one row (in commentColumns order) to a domain.Comment.
func scanComment(s scanner) (domain.Comment, error) {
	var (
		c         domain.Comment
		taskID    string
		kind      string
		body      string
		actorType string
		actorName string
		createdAt string
	)
	if err := s.Scan(&c.ID, &taskID, &kind, &body, &actorType, &actorName, &createdAt); err != nil {
		return domain.Comment{}, err
	}
	c.TaskID = domain.ID(taskID)
	c.Kind = domain.CommentKind(kind)
	c.Body = body
	c.Actor = domain.Actor{Type: domain.ActorType(actorType), Name: actorName}
	at, err := parseTS(createdAt)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("comment %s created_at %q: %w", c.ID, createdAt, err)
	}
	c.CreatedAt = at
	return c, nil
}
