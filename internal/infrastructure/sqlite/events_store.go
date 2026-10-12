package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// eventStore implements application.EventStore over the append-only `event`
// table. Events are insert-only: there is deliberately no update or delete
// method.
type eventStore struct {
	q querier
}

func newEventStore(q querier) *eventStore { return &eventStore{q: q} }

var _ application.EventStore = (*eventStore)(nil)

// NewEventStore returns an application.EventStore bound directly to the
// connection pool (autocommit reads, no transaction). It serves read paths
// that need the activity log OUTSIDE a UnitOfWork.Run — today the SSE
// Last-Event-ID replay in the HTTP adapter. Mutation work always goes through
// the transaction-bound store built inside Run.
func NewEventStore(db *sql.DB) application.EventStore { return newEventStore(db) }

// eventColumns is the single source of truth for SELECT column order — it must
// stay aligned with scanEvent's Scan targets. Nullable columns (project_id,
// task_id, before_json, after_json) are stored as NULL when absent so reads
// round-trip to the domain's nil/"" representation.
const eventColumns = "id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts"

// Append inserts one event exactly as given. In transactional use it is called
// inside the same UnitOfWork.Run as the mutation it records (unit-of-work
// rule); direct appends against the pool run in autocommit.
func (s *eventStore) Append(ctx context.Context, e domain.Event) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO event (`+eventColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(e.ID),
		idOrNil(e.ProjectID),
		idOrNil(e.TaskID),
		string(e.Actor.Type),
		e.Actor.Name,
		string(e.Kind),
		strOrNil(e.Before),
		strOrNil(e.After),
		formatTS(e.At))
	if err != nil {
		return fmt.Errorf("append event %s: %w", e.ID, err)
	}
	return nil
}

// ListByTask returns up to limit events for the task, newest first (ts DESC;
// rowid DESC breaks same-timestamp ties in insertion order), skipping offset.
// limit <= 0 means unbounded.
func (s *eventStore) ListByTask(ctx context.Context, taskID domain.ID, limit, offset int) ([]domain.Event, error) {
	return s.list(ctx, "task_id", string(taskID), limit, offset)
}

// ListByProject returns up to limit events project-wide, newest first (ts
// DESC; rowid DESC breaks same-timestamp ties in insertion order), skipping
// offset. limit <= 0 means unbounded.
func (s *eventStore) ListByProject(ctx context.Context, projectID domain.ID, limit, offset int) ([]domain.Event, error) {
	return s.list(ctx, "project_id", string(projectID), limit, offset)
}

// ListAfterProject returns the project's events strictly after after, oldest
// first (id ASC — event ids are ULIDs, so lexicographic order IS
// chronological order), up to limit. It backs SSE Last-Event-ID replay: a
// client reconnecting after a drop asks for exactly what it missed since its
// last delivered event id. Events with no project never match; the cursor is
// exclusive (id > after). limit <= 0 means unbounded.
func (s *eventStore) ListAfterProject(ctx context.Context, projectID domain.ID, after domain.ID, limit int) ([]domain.Event, error) {
	query := `SELECT ` + eventColumns + ` FROM event WHERE project_id = ? AND id > ? ORDER BY id ASC`
	args := []any{string(projectID), string(after)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events after %s for project %s: %w", after, projectID, err)
	}
	defer rows.Close()

	out := make([]domain.Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *eventStore) list(ctx context.Context, column, value string, limit, offset int) ([]domain.Event, error) {
	query := `SELECT ` + eventColumns + ` FROM event WHERE ` + column + ` = ? ORDER BY ts DESC, rowid DESC`
	args := []any{value}
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

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events for %s %q: %w", column, value, err)
	}
	defer rows.Close()

	out := make([]domain.Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func idOrNil(id *domain.ID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// scanEvent maps one row to a domain.Event. Nullable columns are scanned into
// pointer-to-pointer targets so NULL round-trips to the domain's nil/"" form.
func scanEvent(s scanner) (domain.Event, error) {
	var (
		ev         domain.Event
		projectID  *domain.ID
		taskID     *domain.ID
		actorType  string
		actorName  string
		kind       string
		beforeJSON *string
		afterJSON  *string
		ts         string
	)
	if err := s.Scan(&ev.ID, &projectID, &taskID, &actorType, &actorName, &kind, &beforeJSON, &afterJSON, &ts); err != nil {
		return domain.Event{}, err
	}
	at, err := parseTS(ts)
	if err != nil {
		return domain.Event{}, fmt.Errorf("event %s ts %q: %w", ev.ID, ts, err)
	}
	ev.ProjectID = projectID
	ev.TaskID = taskID
	ev.Actor = domain.Actor{Type: domain.ActorType(actorType), Name: actorName}
	ev.Kind = domain.EventKind(kind)
	if beforeJSON != nil {
		ev.Before = *beforeJSON
	}
	if afterJSON != nil {
		ev.After = *afterJSON
	}
	ev.At = at
	return ev, nil
}
