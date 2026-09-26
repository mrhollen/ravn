package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// Comments implements the task comment-thread use case (ARCHITECTURE §5 POST
// /tasks/{id}/comments, MCP add_comment): validate the typed kind and body,
// confirm the parent task exists, then append the comment and exactly one
// `commented` event in a single unit of work.
//
// Version-bump policy (ARCHITECTURE §4): appending a comment is a CHILD-ROW
// mutation — it touches only the comment table plus the event log. The parent
// task row's version and updated_at are never touched, so a working agent
// holding a stale read of the task can still issue its next version-guarded
// write without a spurious 409.
type Comments struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator
}

// NewComments returns the Comments use case bound to uow, clock, and idgen.
// The use case mints the comment ID and stamps created_at before calling the
// repository (ADR); adapters store values exactly as given.
func NewComments(uow UnitOfWork, clock Clock, idgen IDGenerator) *Comments {
	return &Comments{uow: uow, clock: clock, idgen: idgen}
}

// eventBodyMax caps how many runes of a comment body the `commented` event's
// After payload carries — events stay compact in the activity feed while the
// full markdown remains in the comment row itself.
const eventBodyMax = 200

// Add validates kind (must be one of progress|question|decision|handoff|note)
// and body (non-empty after trimming), confirms the parent task exists
// (domain.ErrNotFound otherwise — the FK enforces this in storage too), and
// appends the comment plus ONE `commented` event whose After JSON carries the
// kind and a truncated body. The parent task's version is NOT bumped
// (child-row rule). Unknown task → domain.ErrNotFound; bad kind or empty
// body → domain.ErrInvalid; nothing is written on validation failure.
func (s *Comments) Add(ctx context.Context, actor domain.Actor, taskID domain.ID, kind domain.CommentKind, body string) (domain.Comment, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Comment{}, err
	}
	if !kind.Valid() {
		return domain.Comment{}, fmt.Errorf("%w: unknown comment kind %q", domain.ErrInvalid, kind)
	}
	if strings.TrimSpace(body) == "" {
		return domain.Comment{}, fmt.Errorf("%w: comment body must not be empty", domain.ErrInvalid)
	}

	now := s.clock.Now()
	c := domain.Comment{
		ID:        s.idgen.NewID(),
		TaskID:    taskID,
		Kind:      kind,
		Body:      body, // markdown stored as given; only emptiness is validated
		Actor:     a,
		CreatedAt: now,
	}

	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if err := r.Comments.Add(ctx, c); err != nil {
			return err
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &c.TaskID,
			Actor:     a,
			Kind:      domain.EventCommented,
			After: compactJSON(map[string]any{
				"kind": string(kind),
				"body": truncateEventBody(body, eventBodyMax),
			}),
			At: now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Comment{}, err
	}
	return c, nil // adapters store values as-given; the comment is immutable after insert
}

// List pages a task's comment thread newest first (the UI-extra GET
// /tasks/{id}/comments) and returns the total comment count BEFORE paging, so
// clients can compute how many pages remain. limit/offset follow the list_tasks
// pagination policy (default 20, clamped to 100; offset defaults to 0). The
// read runs through the unit of work for uniformity with every other read and
// is read-only: it appends no events and touches nothing. Unknown task →
// domain.ErrNotFound.
func (s *Comments) List(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, int64, error) {
	if limit <= 0 {
		limit = defaultListLimit
	} else if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}

	var rows []domain.Comment
	var total int64
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		if _, gerr := r.Tasks.Get(ctx, taskID); gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		cs, lerr := r.Comments.ListByTask(ctx, taskID, limit, offset)
		if lerr != nil {
			return lerr
		}
		n, nerr := r.Comments.CountByTask(ctx, taskID)
		if nerr != nil {
			return nerr
		}
		rows, total = cs, n
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// truncateEventBody shortens a comment body to at most n runes for event
// payloads, appending an ellipsis when something was cut.
func truncateEventBody(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
