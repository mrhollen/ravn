package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// commentRepo implements application.CommentRepository over SQLite. Comments
// are append-only: Add stores the row exactly as given (IDs and timestamps are
// minted/stamped by the use case — ADR) and ListByTask serves the resume
// payload's newest-first comment tail.
type commentRepo struct {
	q querier
}

func newCommentRepo(q querier) *commentRepo { return &commentRepo{q: q} }

var _ application.CommentRepository = (*commentRepo)(nil)

// Add stores the comment row exactly as given. The write is part of a child-
// row mutation (ARCHITECTURE §4 version-bump policy): it must never touch the
// parent task row's version or updated_at. Constraint failures — unknown task
// (FK) or an out-of-set kind (CHECK) — surface as wrapped domain.ErrInvalid;
// the use case validates both beforehand, so a violation here means concurrent
// deletion of the parent between validation and insert.
func (r *commentRepo) Add(ctx context.Context, c domain.Comment) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO comment (`+commentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(c.ID), string(c.TaskID), string(c.Kind), c.Body,
		string(c.Actor.Type), c.Actor.Name, formatTS(c.CreatedAt))
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: comment %s violates a constraint (unknown task or bad kind)", domain.ErrInvalid, c.ID)
		}
		return fmt.Errorf("insert comment %s: %w", c.ID, err)
	}
	return nil
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
		if offset > 0 {
			query += ` OFFSET ?`
			args = append(args, offset)
		}
	} else if offset > 0 {
		query += ` LIMIT -1 OFFSET ?`
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

// CountByTask returns the total comment count for a task (before paging) — the
// `total` of the paged GET /tasks/{id}/comments envelope. The idx_comment_task
// index covers the predicate, so this is an index-only scan.
func (r *commentRepo) CountByTask(ctx context.Context, taskID domain.ID) (int64, error) {
	var n int64
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM comment WHERE task_id = ?`, string(taskID)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count comments for task %s: %w", taskID, err)
	}
	return n, nil
}

// CountsByTasks returns the total comment count for each given task in ONE
// grouped query (the batch form of CountByTask, no N+1). Tasks with zero
// comments are omitted from the map; empty input short-circuits without SQL.
func (r *commentRepo) CountsByTasks(ctx context.Context, taskIDs []domain.ID) (map[domain.ID]int64, error) {
	out := make(map[domain.ID]int64, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	query, args := inByTaskID("comment", taskIDs)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count comments for %d tasks: %w", len(taskIDs), err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			taskID string
			n      int64
		)
		if err := rows.Scan(&taskID, &n); err != nil {
			return nil, fmt.Errorf("scan comment count row: %w", err)
		}
		out[domain.ID(taskID)] = n
	}
	return out, rows.Err()
}

// inByTaskID builds a "SELECT task_id, COUNT(*) FROM <table> WHERE task_id IN
// (…)" grouped-count query for the given ids. Shared by the batch count reads
// on child tables keyed by task_id.
func inByTaskID(table string, taskIDs []domain.ID) (string, []any) {
	placeholders := make([]string, len(taskIDs))
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		placeholders[i] = "?"
		args[i] = string(id)
	}
	return `SELECT task_id, COUNT(*) FROM ` + table + ` WHERE task_id IN (` + strings.Join(placeholders, ", ") + `) GROUP BY task_id`, args
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
