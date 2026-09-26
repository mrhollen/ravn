package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// errNotImplemented marks the repository methods that are wired into
// application.Repos but whose implementations land in a later slice. Every
// stub method wraps it with the slice that ships the real behavior.
var errNotImplemented = errors.New("sqlite adapter: not implemented yet")

func notYet(repo, method, slice string) error {
	return fmt.Errorf("sqlite %s %s: %w (lands in slice %s)", repo, method, errNotImplemented, slice)
}

// taskRepo implements application.TaskRepository over SQLite. Slice 2B lands
// the plain CRUD half (Create/Get/List/Update); the claim CAS trio
// (Claim/Release/Heartbeat) stays stubbed until its own slice.
type taskRepo struct {
	q querier
}

func newTaskRepo(q querier) *taskRepo { return &taskRepo{q: q} }

var _ application.TaskRepository = (*taskRepo)(nil)

// taskColumns is the single source of truth for SELECT column order — it must
// stay aligned with scanTask's Scan targets (ARCHITECTURE §4 DDL, in table
// order). Nullable columns are scanned into pointer targets.
const taskColumns = `id, project_id, status, title, description, priority, due_at, notes,
	assignee_type, assignee_name, claimed_by_type, claimed_by_name, claimed_at,
	claim_expires_at, claim_ttl_sec, version, archived, created_at, updated_at, completed_at`

// Create stores a new task row exactly as given (IDs and timestamps are
// minted/stamped by the use case; fresh rows carry Version = 1 per the ADR).
// The write is idempotent by unique ID: re-inserting an existing ID leaves
// the stored row untouched and returns nil, so agent retries cannot corrupt
// state. Constraint failures (unknown project/status, out-of-range priority)
// surface as wrapped domain.ErrInvalid — the input was invalid, not the store.
func (r *taskRepo) Create(ctx context.Context, t domain.Task) error {
	res, err := r.q.ExecContext(ctx,
		`INSERT INTO task (`+taskColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) `+
			`ON CONFLICT(id) DO NOTHING`,
		string(t.ID), string(t.ProjectID), string(t.Status), t.Title, t.Description, int(t.Priority),
		tsOrNil(t.DueAt), t.Notes,
		actorTypeOrNil(t.Assignee), actorNameOrNil(t.Assignee),
		actorTypeOrNil(t.ClaimedBy), actorNameOrNil(t.ClaimedBy), tsOrNil(t.ClaimedAt),
		tsOrNil(t.ClaimExpiry), claimTTLOrNil(&t),
		t.Version, boolToInt(t.Archived),
		formatTS(t.CreatedAt), formatTS(t.UpdatedAt), tsOrNil(t.CompletedAt))
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: task %s violates a schema constraint", domain.ErrInvalid, t.ID)
		}
		return fmt.Errorf("insert task %s: %w", t.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after insert task %s: %w", t.ID, err)
	}
	if n == 0 {
		return nil // idempotent: the unique ID already exists; stored row untouched
	}
	return nil
}

// Get returns the task with id or domain.ErrNotFound.
func (r *taskRepo) Get(ctx context.Context, id domain.ID) (domain.Task, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM task WHERE id = ?`, string(id))
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, err
}

// List returns the tasks matching f in canonical order (priority DESC, due_at
// ASC nulls-last, created_at ASC) plus the total number of matching tasks
// before Limit/Offset are applied. The nulls-last expression — (due_at IS
// NULL) sorts 0 before 1, i.e. non-null due dates first — is the EXACT form
// mandated by ARCHITECTURE §4 because SQLite has no NULLS LAST keyword.
func (r *taskRepo) List(ctx context.Context, f application.TaskFilter) ([]domain.Task, int64, error) {
	where, args := taskListWhere(f)

	var total int64
	if err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	query := `SELECT ` + taskColumns + ` FROM task WHERE ` + where +
		` ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC`
	if f.Limit > 0 { // <= 0 = unbounded when called directly; the use case always clamps
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, f.Offset)
	}

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan task row: %w", err)
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// Update performs the optimistic version-guarded write (ADR): it applies only
// when the stored row's Version equals t.Version, stores t's fields with
// Version = t.Version + 1, and returns the fresh row. Zero affected rows means
// either an unknown ID (domain.ErrNotFound) or a stale writer
// (*domain.ConflictError carrying the freshly read current row).
func (r *taskRepo) Update(ctx context.Context, t domain.Task) (domain.Task, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE task SET project_id = ?, status = ?, title = ?, description = ?, priority = ?, due_at = ?,
		 notes = ?, assignee_type = ?, assignee_name = ?, claimed_by_type = ?, claimed_by_name = ?,
		 claimed_at = ?, claim_expires_at = ?, claim_ttl_sec = ?, version = ?, archived = ?,
		 updated_at = ?, completed_at = ?
		 WHERE id = ? AND version = ?`,
		string(t.ProjectID), string(t.Status), t.Title, t.Description, int(t.Priority), tsOrNil(t.DueAt),
		t.Notes,
		actorTypeOrNil(t.Assignee), actorNameOrNil(t.Assignee),
		actorTypeOrNil(t.ClaimedBy), actorNameOrNil(t.ClaimedBy), tsOrNil(t.ClaimedAt),
		tsOrNil(t.ClaimExpiry), claimTTLOrNil(&t),
		t.Version+1, boolToInt(t.Archived), formatTS(t.UpdatedAt), tsOrNil(t.CompletedAt),
		string(t.ID), t.Version)
	if err != nil {
		return domain.Task{}, fmt.Errorf("update task %s: %w", t.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Task{}, fmt.Errorf("rows affected after update task %s: %w", t.ID, err)
	}
	if n == 0 {
		current, gerr := r.Get(ctx, t.ID)
		if gerr != nil {
			return domain.Task{}, gerr // ErrNotFound when the row is gone entirely
		}
		return domain.Task{}, &domain.ConflictError{Current: current}
	}
	fresh, err := r.Get(ctx, t.ID) // read back the stored result of the guarded write
	if err != nil {
		return domain.Task{}, fmt.Errorf("re-read task %s after update: %w", t.ID, err)
	}
	return fresh, nil
}

// Claim/Release/Heartbeat — the atomic lease CAS trio (ARCHITECTURE §4).
func (r *taskRepo) Claim(ctx context.Context, taskID domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("task repository", "Claim", "the claim slice")
}

func (r *taskRepo) Release(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("task repository", "Release", "the claim slice")
}

func (r *taskRepo) Heartbeat(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	return domain.Task{}, notYet("task repository", "Heartbeat", "the claim slice")
}

// --- query building + scanning ----------------------------------------------------

// taskListWhere renders TaskFilter's conditions as a WHERE clause fragment and
// its positional args. Archived is always applied: false (zero value) = open
// tasks only, the REST default; true = archived tasks only.
func taskListWhere(f application.TaskFilter) (string, []any) {
	conds := make([]string, 0, 8)
	args := make([]any, 0, 10)

	if f.ProjectID != "" {
		conds = append(conds, "project_id = ?")
		args = append(args, string(f.ProjectID))
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, string(f.Status))
	}
	if f.AssigneeType != "" {
		conds = append(conds, "assignee_type = ?")
		args = append(args, string(f.AssigneeType))
	}
	if f.AssigneeName != "" {
		conds = append(conds, "assignee_name = ?")
		args = append(args, f.AssigneeName)
	}

	switch f.Archived {
	case true:
		conds = append(conds, "archived = 1")
	default: // false = open tasks only (REST default)
		conds = append(conds, "archived = 0")
	}

	if f.Q != "" {
		// Case-insensitive LIKE over title+description+notes. LIKE is already
		// ASCII-case-insensitive; lower() makes it explicit and consistent.
		pattern := "%" + strings.ToLower(f.Q) + "%"
		conds = append(conds, "(lower(title) LIKE ? OR lower(description) LIKE ? OR lower(notes) LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}

	if f.CreatedAfter != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, formatTS(*f.CreatedAfter))
	}
	if f.CreatedBefore != nil {
		conds = append(conds, "created_at < ?")
		args = append(args, formatTS(*f.CreatedBefore))
	}
	if f.CompletedAfter != nil {
		conds = append(conds, "completed_at >= ?")
		args = append(args, formatTS(*f.CompletedAfter))
	}
	if f.CompletedBefore != nil {
		conds = append(conds, "completed_at < ?")
		args = append(args, formatTS(*f.CompletedBefore))
	}

	// TODO(slice 2D): f.UnblockedOnly — relation-dependent filtering needs the
	// relation table ("no outgoing blocked-by edge whose target is not done").
	// Intentionally ignored until then: the port contract says adapters ignore
	// it rather than fail.

	return strings.Join(conds, " AND "), args
}

// scanTask maps one row (in taskColumns order) to a domain.Task. NULL in the
// nullable columns round-trips to the domain's zero/nil representation;
// LastActivity has no column and is derived as UpdatedAt on every read (ADR).
func scanTask(s scanner) (domain.Task, error) {
	var (
		t              domain.Task
		projectID      string
		status         string
		priority       int
		dueAt          *string
		notes          string
		assigneeType   *string
		assigneeName   *string
		claimedByType  *string
		claimedByName  *string
		claimedAt      *string
		claimExpiresAt *string
		claimTTL       *int
		version        int
		archived       int
		createdAt      string
		updatedAt      string
		completedAt    *string
	)
	err := s.Scan(
		&t.ID, &projectID, &status, &t.Title, &t.Description, &priority, &dueAt, &notes,
		&assigneeType, &assigneeName, &claimedByType, &claimedByName, &claimedAt,
		&claimExpiresAt, &claimTTL, &version, &archived, &createdAt, &updatedAt, &completedAt)
	if err != nil {
		return domain.Task{}, err
	}

	t.ProjectID = domain.ID(projectID)
	t.Status = domain.Status(status)
	t.Priority = domain.Priority(priority)
	t.Notes = notes
	t.Version = version
	t.Archived = archived != 0

	if dueAt != nil {
		ts, err := parseTS(*dueAt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("task %s due_at %q: %w", t.ID, *dueAt, err)
		}
		t.DueAt = &ts
	}
	t.Assignee = actorOrNil(assigneeType, assigneeName)

	if claimedByType != nil && claimedByName != nil {
		holder := domain.Actor{Type: domain.ActorType(*claimedByType), Name: *claimedByName}
		t.ClaimedBy = &holder
		if claimedAt != nil {
			ts, err := parseTS(*claimedAt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("task %s claimed_at %q: %w", t.ID, *claimedAt, err)
			}
			t.ClaimedAt = &ts
		}
		if claimExpiresAt != nil {
			ts, err := parseTS(*claimExpiresAt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("task %s claim_expires_at %q: %w", t.ID, *claimExpiresAt, err)
			}
			t.ClaimExpiry = &ts
		}
		if claimTTL != nil {
			t.ClaimTTLSeconds = *claimTTL
		}
	}

	created, err := parseTS(createdAt)
	if err != nil {
		return domain.Task{}, fmt.Errorf("task %s created_at %q: %w", t.ID, createdAt, err)
	}
	updated, err := parseTS(updatedAt)
	if err != nil {
		return domain.Task{}, fmt.Errorf("task %s updated_at %q: %w", t.ID, updatedAt, err)
	}
	t.CreatedAt = created
	t.UpdatedAt = updated
	if completedAt != nil {
		ts, err := parseTS(*completedAt)
		if err != nil {
			return domain.Task{}, fmt.Errorf("task %s completed_at %q: %w", t.ID, *completedAt, err)
		}
		t.CompletedAt = &ts
	}
	t.LastActivity = updated // derived (no column in the MVP schema — ADR)
	return t, nil
}
