package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// errNotImplemented marks the repository methods that are wired into
// application.Repos but whose implementations land in a later slice. Every
// stub method wraps it with the slice that ships the real behavior.
var errNotImplemented = errors.New("sqlite adapter: not implemented yet")

func notYet(method, slice string) error {
	return fmt.Errorf("sqlite task repository %s: %w (lands in slice %s)", method, errNotImplemented, slice)
}

// taskRepo will implement application.TaskRepository over SQLite, including the
// atomic claim CAS (ARCHITECTURE §4). Slice 2A wires the type into Repos so the
// composition is complete; the methods stub until slice 2B.
type taskRepo struct {
	q querier
}

func newTaskRepo(q querier) *taskRepo { return &taskRepo{q: q} }

var _ application.TaskRepository = (*taskRepo)(nil)

func (r *taskRepo) Create(ctx context.Context, t domain.Task) error {
	return notYet("Create", "2B")
}

func (r *taskRepo) Get(ctx context.Context, id domain.ID) (domain.Task, error) {
	return domain.Task{}, notYet("Get", "2B")
}

func (r *taskRepo) List(ctx context.Context, f application.TaskFilter) ([]domain.Task, int64, error) {
	return nil, 0, notYet("List", "2B")
}

func (r *taskRepo) Update(ctx context.Context, t domain.Task) (domain.Task, error) {
	return domain.Task{}, notYet("Update", "2B")
}

func (r *taskRepo) Claim(ctx context.Context, taskID domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("Claim", "2B")
}

func (r *taskRepo) Release(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("Release", "2B")
}

func (r *taskRepo) Heartbeat(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("Heartbeat", "2B")
}
