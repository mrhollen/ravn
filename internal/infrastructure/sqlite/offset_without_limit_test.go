package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

func TestOffsetWithoutLimit_TasksCommentsEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	p := domain.Project{
		ID:        "proj-offset-test",
		Name:      "Offset Test Project",
		CreatedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}

	task1 := domain.Task{
		ID:        "task-offset-1",
		ProjectID: p.ID,
		Title:     "Task 1",
		Status:    domain.StatusTodo,
		Priority:  domain.Priority(3),
		Version:   1,
		CreatedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
	task2 := domain.Task{
		ID:        "task-offset-2",
		ProjectID: p.ID,
		Title:     "Task 2",
		Status:    domain.StatusTodo,
		Priority:  domain.Priority(2),
		Version:   1,
		CreatedAt: time.Date(2026, 10, 9, 12, 1, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 9, 12, 1, 0, 0, time.UTC),
	}
	task3 := domain.Task{
		ID:        "task-offset-3",
		ProjectID: p.ID,
		Title:     "Task 3",
		Status:    domain.StatusTodo,
		Priority:  domain.Priority(1),
		Version:   1,
		CreatedAt: time.Date(2026, 10, 9, 12, 2, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 9, 12, 2, 0, 0, time.UTC),
	}

	actor := domain.Actor{Type: domain.ActorAgent, Name: "tester"}

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, p); err != nil {
			return err
		}
		for _, tk := range []domain.Task{task1, task2, task3} {
			if err := r.Tasks.Create(ctx, tk); err != nil {
				return err
			}
		}
		for i, body := range []string{"Comment 1", "Comment 2", "Comment 3"} {
			c := domain.Comment{
				ID:        domain.ID(fmt.Sprintf("comment-%d", i+1)),
				TaskID:    task1.ID,
				Kind:      domain.CommentNote,
				Body:      body,
				Actor:     actor,
				CreatedAt: time.Date(2026, 10, 9, 12, i, 0, 0, time.UTC),
			}
			if err := r.Comments.Add(ctx, c); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			ev := domain.Event{
				ID:        domain.ID(fmt.Sprintf("event-%d", i+1)),
				ProjectID: &p.ID,
				TaskID:    &task1.ID,
				Actor:     actor,
				Kind:      domain.EventCreated,
				At:        time.Date(2026, 10, 9, 12, i, 0, 0, time.UTC),
			}
			if err := r.Events.Append(ctx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed data: %v", err)
	}

	tasksRepo := newTaskRepo(db)
	commentsRepo := newCommentRepo(db)
	eventsStore := newEventStore(db)

	// 1. Tasks: test with offset > 0 and limit <= 0 (unbounded)
	t.Run("Tasks_OffsetWithoutLimit", func(t *testing.T) {
		for _, limit := range []int{0, -1} {
			rows, total, err := tasksRepo.List(ctx, application.TaskFilter{
				ProjectID: p.ID,
				Offset:    1,
				Limit:     limit,
			})
			if err != nil {
				t.Fatalf("Tasks.List with offset=1, limit=%d failed: %v", limit, err)
			}
			if total != 3 {
				t.Errorf("total = %d, want 3", total)
			}
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want 2 rows when skipping 1 of 3", len(rows))
			}
			if rows[0].ID != "task-offset-2" || rows[1].ID != "task-offset-3" {
				t.Errorf("unexpected task IDs: got [%s, %s], want [task-offset-2, task-offset-3]", rows[0].ID, rows[1].ID)
			}
		}
	})

	// 2. Comments: test with offset > 0 and limit <= 0 (unbounded)
	t.Run("Comments_OffsetWithoutLimit", func(t *testing.T) {
		for _, limit := range []int{0, -1} {
			comments, err := commentsRepo.ListByTask(ctx, task1.ID, limit, 1)
			if err != nil {
				t.Fatalf("Comments.ListByTask with offset=1, limit=%d failed: %v", limit, err)
			}
			if len(comments) != 2 {
				t.Fatalf("got %d comments, want 2 comments when skipping 1 of 3", len(comments))
			}
			if comments[0].ID != "comment-2" || comments[1].ID != "comment-1" {
				t.Errorf("unexpected comment IDs: got [%s, %s], want [comment-2, comment-1]", comments[0].ID, comments[1].ID)
			}
		}
	})

	// 3. Events: test ListByProject and ListByTask with offset > 0 and limit <= 0 (unbounded)
	t.Run("Events_OffsetWithoutLimit", func(t *testing.T) {
		for _, limit := range []int{0, -1} {
			projEvents, err := eventsStore.ListByProject(ctx, p.ID, limit, 1)
			if err != nil {
				t.Fatalf("Events.ListByProject with offset=1, limit=%d failed: %v", limit, err)
			}
			if len(projEvents) != 2 {
				t.Fatalf("got %d project events, want 2 when skipping 1 of 3", len(projEvents))
			}
			if projEvents[0].ID != "event-2" || projEvents[1].ID != "event-1" {
				t.Errorf("unexpected project event IDs: got [%s, %s], want [event-2, event-1]", projEvents[0].ID, projEvents[1].ID)
			}

			taskEvents, err := eventsStore.ListByTask(ctx, task1.ID, limit, 1)
			if err != nil {
				t.Fatalf("Events.ListByTask with offset=1, limit=%d failed: %v", limit, err)
			}
			if len(taskEvents) != 2 {
				t.Fatalf("got %d task events, want 2 when skipping 1 of 3", len(taskEvents))
			}
			if taskEvents[0].ID != "event-2" || taskEvents[1].ID != "event-1" {
				t.Errorf("unexpected task event IDs: got [%s, %s], want [event-2, event-1]", taskEvents[0].ID, taskEvents[1].ID)
			}
		}
	})

	// 4. Verify within a transactional UnitOfWork as well
	t.Run("WithinUnitOfWork", func(t *testing.T) {
		err := NewUnitOfWork(db).Run(ctx, &r, func() error {
			rows, total, err := r.Tasks.List(ctx, application.TaskFilter{
				ProjectID: p.ID,
				Offset:    1,
				Limit:     0,
			})
			if err != nil {
				return fmt.Errorf("UoW Tasks.List: %w", err)
			}
			if total != 3 || len(rows) != 2 {
				return fmt.Errorf("UoW Tasks.List unexpected counts: total=%d, len=%d", total, len(rows))
			}

			comments, err := r.Comments.ListByTask(ctx, task1.ID, 0, 1)
			if err != nil {
				return fmt.Errorf("UoW Comments.ListByTask: %w", err)
			}
			if len(comments) != 2 {
				return fmt.Errorf("UoW Comments.ListByTask unexpected len=%d", len(comments))
			}

			events, err := r.Events.ListByProject(ctx, p.ID, 0, 1)
			if err != nil {
				return fmt.Errorf("UoW Events.ListByProject: %w", err)
			}
			if len(events) != 2 {
				return fmt.Errorf("UoW Events.ListByProject unexpected len=%d", len(events))
			}

			return nil
		})
		if err != nil {
			t.Fatalf("WithinUnitOfWork failed: %v", err)
		}
	})
}
