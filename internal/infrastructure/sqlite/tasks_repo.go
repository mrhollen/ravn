package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// taskRepo implements application.TaskRepository over SQLite: the plain CRUD
// half (Create/Get/List/Update, slice 2B) plus the atomic lease CAS trio
// (Claim/Release/Heartbeat, slice 2C — ARCHITECTURE §4–5, ADR 2026-09-26).
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

// lastActivityExpr derives domain.Task.LastActivity on read: the MAX of the
// task row's own updated_at and the latest child-row timestamp for this task
// (comment, artifact, acceptance-criterion completion). Each child-table lookup
// is COALESCEd to the empty string so SQLite's multi-argument MAX never sees a
// NULL (it would return NULL if any argument were NULL); task.updated_at is
// NOT NULL, so the result is never NULL. formatTS's fixed-width zero-padded
// UTC form makes lexicographic text max == chronological max. Child-row
// mutations never touch the parent row (no updated_at or version bump — ADR),
// so this read-time derivation is the sole source of LastActivity.
const lastActivityExpr = `MAX(task.updated_at,
	COALESCE((SELECT MAX(c.created_at) FROM comment c WHERE c.task_id = task.id), ''),
	COALESCE((SELECT MAX(a.created_at) FROM artifact a WHERE a.task_id = task.id), ''),
	COALESCE((SELECT MAX(ac.completed_at) FROM acceptance_criterion ac WHERE ac.task_id = task.id), ''))`

// taskSelect is the full-row SELECT list feeding scanTask: taskColumns plus the
// derived last_activity column appended as the trailing target. Used by every
// statement returning complete task rows — Get, List, and Claim's RETURNING.
// Summary/aggregation queries (OpenTitles, BlockersOf, OpenTaskCounts, …) never
// use it; they select their own light column lists and must not gain the extra
// column.
const taskSelect = taskColumns + ", " + lastActivityExpr + ` AS last_activity`

// Create stores a new task row exactly as given (IDs and timestamps are
// minted/stamped by the use case; fresh rows carry Version = 1 per the ADR).
// The write is idempotent by unique ID: re-inserting an existing ID leaves
// the stored row untouched and returns nil, so agent retries cannot corrupt
// state. Constraint failures (unknown project/status, out-of-range priority)
// surface as wrapped domain.ErrInvalid — the input was invalid, not the store.
func (r *taskRepo) Create(ctx context.Context, t domain.Task) error {
	la := claimLeaseArgs(t.Claim) // the five lease columns, rendered all-or-nothing
	res, err := r.q.ExecContext(ctx,
		`INSERT INTO task (`+taskColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) `+
			`ON CONFLICT(id) DO NOTHING`,
		string(t.ID), string(t.ProjectID), string(t.Status), t.Title, t.Description, int(t.Priority),
		tsOrNil(t.DueAt), t.Notes,
		actorTypeOrNil(t.Assignee), actorNameOrNil(t.Assignee),
		la[0], la[1], la[2], la[3], la[4],
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
	row := r.q.QueryRowContext(ctx, `SELECT `+taskSelect+` FROM task WHERE id = ?`, string(id))
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

	query := `SELECT ` + taskSelect + ` FROM task WHERE ` + where +
		` ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC`
	if f.Limit > 0 { // <= 0 = unbounded when called directly; the use case always clamps
		query += ` LIMIT ?`
		args = append(args, f.Limit)
		if f.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, f.Offset)
		}
	} else if f.Offset > 0 {
		query += ` LIMIT -1 OFFSET ?`
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

// OpenTitles returns every open (non-archived, non-done) task of projectID as
// a titles-only (id, title) pair, in canonical order — UNBOUNDED. The WHERE is
// the exact system-wide "open" predicate (ARCHITECTURE §4: not done and not
// archived), and the ORDER BY is List's canonical expression verbatim so both
// adapters can never disagree on scan order. This is the cheap pre-selection
// behind FindDuplicateCandidates: it scores every open row in memory, then
// hydrates only its top-N survivors via Get — no full rows are materialized for
// a big project. The lookup rides idx_task_order's (project_id, …) prefix plus
// the partial archived = 0 index; the status filter is residual (Tier 2 can
// add FTS-backed matching, ARCHITECTURE §3).
func (r *taskRepo) OpenTitles(ctx context.Context, projectID domain.ID) ([]application.TaskTitle, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT id, title FROM task WHERE project_id = ? AND archived = 0 AND status <> 'done' `+
			`ORDER BY priority DESC, (due_at IS NULL), due_at ASC, created_at ASC`, string(projectID))
	if err != nil {
		return nil, fmt.Errorf("open titles: %w", err)
	}
	defer rows.Close()

	out := make([]application.TaskTitle, 0)
	for rows.Next() {
		var tt application.TaskTitle
		if err := rows.Scan(&tt.ID, &tt.Title); err != nil {
			return nil, fmt.Errorf("scan open-title row: %w", err)
		}
		out = append(out, tt)
	}
	return out, rows.Err()
}

// BlockersOf returns the open blockers of taskID — the targets of its outgoing
// blocked-by edges whose target is NOT done and NOT archived, in edge-creation
// order (created_at ASC, id ASC tie-break). Only DIRECT blockers count; there
// is no transitive closure in MVP. The predicate is the exact twin of
// taskListWhere's UnblockedOnly guard, so GetDetail's Blocked flag flips at
// exactly the moments List(UnblockedOnly) includes/excludes the task. Rows are
// light summaries: id/title/status only — the rest of domain.Task stays zero.
func (r *taskRepo) BlockersOf(ctx context.Context, taskID domain.ID) ([]domain.Task, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT b.id, b.title, b.status FROM relation r `+
			`JOIN task b ON b.id = r.other_task_id `+
			`WHERE r.task_id = ? AND r.kind = 'blocked-by' AND b.status <> 'done' AND b.archived = 0 `+
			`ORDER BY r.created_at ASC, b.id ASC`, string(taskID))
	if err != nil {
		return nil, fmt.Errorf("list blockers for task %s: %w", taskID, err)
	}
	defer rows.Close()

	out := make([]domain.Task, 0)
	for rows.Next() {
		var (
			t      domain.Task
			status string
		)
		if err := rows.Scan(&t.ID, &t.Title, &status); err != nil {
			return nil, fmt.Errorf("scan blocker row for task %s: %w", taskID, err)
		}
		t.Status = domain.Status(status)
		out = append(out, t)
	}
	return out, rows.Err()
}

// OpenBlockersByTasks returns open blockers for the given taskIDs in edge creation order.
func (r *taskRepo) OpenBlockersByTasks(ctx context.Context, taskIDs []domain.ID) (map[domain.ID][]domain.Task, error) {
	out := make(map[domain.ID][]domain.Task, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(taskIDs))
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		placeholders[i] = "?"
		args[i] = string(id)
	}
	rows, err := r.q.QueryContext(ctx,
		`SELECT r.task_id, b.id, b.title, b.status FROM relation r `+
			`JOIN task b ON b.id = r.other_task_id `+
			`WHERE r.task_id IN (`+strings.Join(placeholders, ", ")+`) AND r.kind = 'blocked-by' AND b.status <> 'done' AND b.archived = 0 `+
			`ORDER BY r.created_at ASC, b.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list open blockers for %d tasks: %w", len(taskIDs), err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			taskID string
			t      domain.Task
			status string
		)
		if err := rows.Scan(&taskID, &t.ID, &t.Title, &status); err != nil {
			return nil, fmt.Errorf("scan blocker row: %w", err)
		}
		t.Status = domain.Status(status)
		tid := domain.ID(taskID)
		out[tid] = append(out[tid], t)
	}
	return out, rows.Err()
}

// Update performs the optimistic version-guarded write (ADR): it applies only
// when the stored row's Version equals t.Version, stores t's fields with
// Version = t.Version + 1, and returns the fresh row. Zero affected rows means
// either an unknown ID (domain.ErrNotFound) or a stale writer
// (*domain.ConflictError carrying the freshly read current row).
func (r *taskRepo) Update(ctx context.Context, t domain.Task) (domain.Task, error) {
	la := claimLeaseArgs(t.Claim) // the five lease columns, rendered all-or-nothing
	res, err := r.q.ExecContext(ctx,
		`UPDATE task SET project_id = ?, status = ?, title = ?, description = ?, priority = ?, due_at = ?,
		 notes = ?, assignee_type = ?, assignee_name = ?, claimed_by_type = ?, claimed_by_name = ?,
		 claimed_at = ?, claim_expires_at = ?, claim_ttl_sec = ?, version = ?, archived = ?,
		 updated_at = ?, completed_at = ?
		 WHERE id = ? AND version = ?`,
		string(t.ProjectID), string(t.Status), t.Title, t.Description, int(t.Priority), tsOrNil(t.DueAt),
		t.Notes,
		actorTypeOrNil(t.Assignee), actorNameOrNil(t.Assignee),
		la[0], la[1], la[2], la[3], la[4],
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

// Claim acquires or renews actor's lease atomically — the core of ravn's
// duplicate-work prevention (ARCHITECTURE §4–5, ADR 2026-09-26). One single
// UPDATE whose WHERE covers ALL winning conditions at once:
//
//	claimed_by_name IS NULL          → lease free (fresh claim)
//	OR claim_expires_at < now        → any EXPIRED lease (takeover / zombie-heal, by anyone)
//	OR claimed_by_name = actor       → actor's own ACTIVE lease (idempotent re-claim)
//
// …with RETURNING; exactly one racer per lease window can match the WHERE, and
// under the single-connection pool (SetMaxOpenConns(1)) UPDATEs are fully
// serialized, so concurrent claims resolve deterministically. A 0-row outcome
// means a DIFFERENT live holder holds the lease: read the row back and return
// *domain.ClaimConflictError{Holder, ExpiresAt} — the body is the loser's
// recovery instruction. Each successful claim starts a NEW lease period:
// claimed_at/claim_expires_at are re-stamped to now/now+ttl even on renewal
// (the event log carries the full history). Status is forced to doing and the
// version bumped; the durable assignee columns are deliberately untouched
// (claim ≠ assignment). Lease-holder identity is the NAME alone — the unified
// NAME identity model defined by sameLeaseHolder in internal/application/
// tasks.go (single-tenant MVP, self-declared names): claimed_by_type is stored
// for display/audit only and deliberately does NOT participate in this CAS, so
// a human-typed and an agent-typed actor sharing a name are the same holder.
// Change here and there together if that model ever changes.
func (r *taskRepo) Claim(ctx context.Context, taskID domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error) {
	expiry := now.Add(time.Duration(ttlSec) * time.Second)
	row := r.q.QueryRowContext(ctx, `
		UPDATE task SET
			claimed_by_type = ?, claimed_by_name = ?,
			claimed_at = ?, claim_expires_at = ?, claim_ttl_sec = ?,
			status = 'doing', version = version + 1, updated_at = ?
		WHERE id = ?
		  AND archived = 0
		  AND status != 'done'
		  AND (claimed_by_name IS NULL
		       OR claim_expires_at < ?
		       OR claimed_by_name = ?)
		RETURNING `+taskSelect, // RETURNING evaluates post-update rows; the derived subqueries see the fresh updated_at
		string(actor.Type), actor.Name,
		formatTS(now), formatTS(expiry), ttlSec,
		formatTS(now),
		string(taskID), formatTS(now), actor.Name)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) { // CAS lost: a different live holder won or task is done/archived
		current, gerr := r.Get(ctx, taskID)
		if gerr != nil {
			return domain.Task{}, gerr // ErrNotFound if the row vanished entirely
		}
		if current.Archived {
			return domain.Task{}, fmt.Errorf("%w: cannot claim archived task %s", domain.ErrInvalid, taskID)
		}
		if current.Status == domain.StatusDone {
			return domain.Task{}, fmt.Errorf("%w: cannot claim completed task %s", domain.ErrInvalid, taskID)
		}
		if !current.Claim.Present() { // ClaimFromStored on read already guarantees all-or-nothing
			// Unreachable: a 0-row claim requires a non-expired different holder.
			return domain.Task{}, fmt.Errorf("claim task %s: CAS matched no row but the stored lease is empty (internal invariant)", taskID)
		}
		holder, exp := current.Claim.By(), current.Claim.Expiry()
		return domain.Task{}, &domain.ClaimConflictError{Holder: *holder, ExpiresAt: exp}
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("claim task %s: %w", taskID, err)
	}
	return t, nil // the RETURNING row IS the stored result of the CAS
}

// Release clears the lease and moves status back to todo (or preserves done), bumping version.
// The WHERE enforces holder-only release against an active or expired lease (unified NAME
// identity — claimed_by_name is the whole principal; see sameLeaseHolder in the application
// layer); a foreign-name holder loses with *domain.ClaimConflictError{Holder, ExpiresAt} and nothing is mutated.
// When force is true, an administrative release clears any lease without requiring the holder key.
// A task with no lease at all is an idempotent no-op returning the fresh row (retry-
// safe: re-releasing after a success must not error). The durable assignee
// columns are never touched.
func (r *taskRepo) Release(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time, force bool) (domain.Task, error) {
	var (
		res sql.Result
		err error
	)
	if force {
		res, err = r.q.ExecContext(ctx, `
			UPDATE task SET
				claimed_by_type = NULL, claimed_by_name = NULL,
				claimed_at = NULL, claim_expires_at = NULL, claim_ttl_sec = NULL,
				status = CASE WHEN status = 'done' THEN 'done' ELSE 'todo' END,
				version = version + 1, updated_at = ?
			WHERE id = ? AND claimed_by_name IS NOT NULL`,
			formatTS(now), string(taskID))
	} else {
		res, err = r.q.ExecContext(ctx, `
			UPDATE task SET
				claimed_by_type = NULL, claimed_by_name = NULL,
				claimed_at = NULL, claim_expires_at = NULL, claim_ttl_sec = NULL,
				status = CASE WHEN status = 'done' THEN 'done' ELSE 'todo' END,
				version = version + 1, updated_at = ?
			WHERE id = ? AND claimed_by_name = ?`,
			formatTS(now), string(taskID), actor.Name)
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("release task %s: %w", taskID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Task{}, fmt.Errorf("rows affected after release task %s: %w", taskID, err)
	}
	if n == 0 {
		current, gerr := r.Get(ctx, taskID)
		if gerr != nil {
			return domain.Task{}, gerr // ErrNotFound if the row vanished entirely
		}
		if !current.Claim.Present() { // ClaimFromStored on read already guarantees all-or-nothing
			return current, nil // nothing to release: idempotent no-op, row untouched
		}
		holder, exp := current.Claim.By(), current.Claim.Expiry()
		return domain.Task{}, &domain.ClaimConflictError{Holder: *holder, ExpiresAt: exp}
	}
	fresh, err := r.Get(ctx, taskID) // read back the stored result of the guarded write
	if err != nil {
		return domain.Task{}, fmt.Errorf("re-read task %s after release: %w", taskID, err)
	}
	return fresh, nil
}

// Heartbeat extends actor's ACTIVE lease to now + the STORED TTL (claim_ttl_sec).
// Two statements: the read supplies the stored TTL and proves holder identity,
// the guarded UPDATE re-checks `claimed_by_name = ? AND claim_expires_at >= ?`
// so the extension can never apply to a lost or lapsed lease. Holder identity
// here is the NAME alone (unified NAME identity — see sameLeaseHolder in the
// application layer; claimed_by_type is display-only and not compared). Under the
// single-connection pool the pair is serialized against every other writer (and
// both run inside one tx when called through UnitOfWork), so no lease can move
// between the read and the write. Deliberately does NOT bump version and does
// NOT touch updated_at: a keep-alive is not a state change and must never cause
// 409s against a working agent's stale read (ADR 2026-09-26). A foreign or
// expired holder gets *domain.ClaimConflictError{Holder, ExpiresAt} naming the
// live lease; NO lease at all is NOT a claim conflict — there is nobody to wait
// for — and surfaces as domain.ErrNotClaimed ("claim it first").
// Returns the fresh row; one call doubles as a health check.
func (r *taskRepo) Heartbeat(ctx context.Context, taskID domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	var (
		ttl      *int
		expiry   *string
		holder   *string
		status   string
		archived int
	)
	err := r.q.QueryRowContext(ctx,
		`SELECT claimed_by_name, claim_ttl_sec, claim_expires_at, status, archived FROM task WHERE id = ?`,
		string(taskID)).Scan(&holder, &ttl, &expiry, &status, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("read lease for heartbeat on task %s: %w", taskID, err)
	}
	if archived != 0 {
		return domain.Task{}, fmt.Errorf("%w: cannot heartbeat archived task %s", domain.ErrInvalid, taskID)
	}
	if status == string(domain.StatusDone) {
		return domain.Task{}, fmt.Errorf("%w: cannot heartbeat completed task %s", domain.ErrInvalid, taskID)
	}
	if holder == nil || ttl == nil { // row exists but carries no lease at all
		// NOT a claim conflict — nobody holds the lease, there is nothing to wait
		// for. ErrNotClaimed names the recovery: claim it first (REST 409
		// "not claimed"; MCP propagates the body verbatim).
		return domain.Task{}, fmt.Errorf("%w: task %s has no claim lease — call claim first", domain.ErrNotClaimed, taskID)
	}
	newExpiry := formatTS(now.Add(time.Duration(*ttl) * time.Second))
	res, uerr := r.q.ExecContext(ctx, `
		UPDATE task SET claim_expires_at = ?
		WHERE id = ? AND claimed_by_name = ? AND claim_expires_at >= ? AND archived = 0 AND status != 'done'`,
		newExpiry, string(taskID), actor.Name, formatTS(now))
	if uerr != nil {
		return domain.Task{}, fmt.Errorf("heartbeat task %s: %w", taskID, uerr)
	}
	n, aerr := res.RowsAffected()
	if aerr != nil {
		return domain.Task{}, fmt.Errorf("rows affected after heartbeat on task %s: %w", taskID, aerr)
	}
	if n == 0 { // defensive: the read and write are serialized, so this cannot lose in practice
		current, gerr := r.Get(ctx, taskID)
		if gerr != nil {
			return domain.Task{}, gerr
		}
		if !current.Claim.Present() {
			return domain.Task{}, fmt.Errorf("%w: task %s has no claim lease — call claim first", domain.ErrNotClaimed, taskID)
		}
		holder, exp := current.Claim.By(), current.Claim.Expiry()
		return domain.Task{}, &domain.ClaimConflictError{Holder: *holder, ExpiresAt: exp}
	}
	fresh, err := r.Get(ctx, taskID) // return the stored row — it doubles as a health check
	if err != nil {
		return domain.Task{}, fmt.Errorf("re-read task %s after heartbeat: %w", taskID, err)
	}
	return fresh, nil
}

// OpenTaskCounts computes per-(project, status) open task counts in ONE grouped
// query (ARCHITECTURE §5: GET /projects rows carry per-status counts — keep it
// a single scan of the task table, no N+1 fan-out per project). Only
// non-archived tasks are counted; pairs with zero open tasks simply don't
// appear — callers fill in zero counts for the missing canonical statuses.
func (r *taskRepo) OpenTaskCounts(ctx context.Context) ([]application.TaskCountRow, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT project_id, status, COUNT(*)
		FROM task
		WHERE archived = 0
		GROUP BY project_id, status`)
	if err != nil {
		return nil, fmt.Errorf("count open tasks by status: %w", err)
	}
	defer rows.Close()

	out := make([]application.TaskCountRow, 0)
	for rows.Next() {
		var (
			row application.TaskCountRow
			pid string
			st  string
		)
		if err := rows.Scan(&pid, &st, &row.Count); err != nil {
			return nil, fmt.Errorf("scan open task counts: %w", err)
		}
		row.ProjectID = domain.ID(pid)
		row.Status = domain.Status(st)
		out = append(out, row)
	}
	return out, rows.Err()
}

// --- query building + scanning ----------------------------------------------------

// escapeLikePattern escapes the LIKE metacharacters so a user-supplied search
// term matches literally (verbatim). Backslash is escaped FIRST because it is
// the escape character; strings.NewReplacer does a single left-to-right pass
// over the input and never re-scans its own replacements, so one backslash in
// yields two out ("a\b" → "a\\b", "100%" → "100\%", "s_1" → "s\_1") — safe to
// pair with a LIKE ... ESCAPE '\' clause.
func escapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

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

	if f.Priority != nil {
		conds = append(conds, "priority = ?")
		args = append(args, int(*f.Priority))
	}
	if f.DueBefore != nil {
		// Undated tasks never match: in SQL, NULL < x is NULL (not true).
		conds = append(conds, "due_at < ?")
		args = append(args, formatTS(*f.DueBefore))
	}
	if f.DueAfter != nil {
		conds = append(conds, "due_at >= ?")
		args = append(args, formatTS(*f.DueAfter))
	}

	if f.Q != "" {
		// Case-insensitive LIKE over title+description+notes. LIKE is already
		// ASCII-case-insensitive; lower() makes it explicit and consistent.
		// escapeLikePattern turns % / _ / \ into literal matches, so a search
		// term is matched VERBATIM; ESCAPE '\' tells SQLite which character
		// performs that escaping.
		pattern := "%" + escapeLikePattern(strings.ToLower(f.Q)) + "%"
		conds = append(conds,
			"(lower(title) LIKE ? ESCAPE '\\' OR lower(description) LIKE ? ESCAPE '\\' OR lower(notes) LIKE ? ESCAPE '\\')")
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

	if f.UnblockedOnly { // slice 2D2b: X is listed only when no unresolved blocker exists —
		// an outgoing blocked-by edge whose target is not done and not archived.
		// COUNT(*) and the row query share taskListWhere, so both apply it.
		conds = append(conds,
			`NOT EXISTS (SELECT 1 FROM relation r JOIN task b ON b.id = r.other_task_id `+
				`WHERE r.task_id = task.id AND r.kind = 'blocked-by' AND b.status <> 'done' AND b.archived = 0)`)
	}

	return strings.Join(conds, " AND "), args
}

// scanTask maps one row (in taskSelect order — taskColumns plus the trailing
// derived last_activity column) to a domain.Task. NULL in the nullable columns
// round-trips to the domain's zero/nil representation. LastActivity has no
// stored column: every full-row SELECT (taskSelect, see lastActivityExpr)
// derives it at read time as MAX(updated_at, latest child-row timestamp across
// comment / artifact / acceptance_criterion), so a child-row mutation shows up
// on the next read without ever bumping the parent's updated_at or version.
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
		lastActivity   *string // derived (taskSelect), never NULL in practice — see lastActivityExpr
	)
	err := s.Scan(
		&t.ID, &projectID, &status, &t.Title, &t.Description, &priority, &dueAt, &notes,
		&assigneeType, &assigneeName, &claimedByType, &claimedByName, &claimedAt,
		&claimExpiresAt, &claimTTL, &version, &archived, &createdAt, &updatedAt, &completedAt,
		&lastActivity)
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

	// Lease columns → one all-or-nothing Claim. A well-formed row has either
	// all five pieces or none; a corrupt partial row (any piece NULL, or a
	// non-positive TTL) degrades to "unclaimed" via ClaimFromStored instead of
	// smuggling a half-lease into the domain — reads stay lenient, the domain
	// stays consistent.
	if claimedByType != nil && claimedByName != nil {
		holder := domain.Actor{Type: domain.ActorType(*claimedByType), Name: *claimedByName}
		var at, exp *time.Time
		if claimedAt != nil {
			ts, err := parseTS(*claimedAt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("task %s claimed_at %q: %w", t.ID, *claimedAt, err)
			}
			at = &ts
		}
		if claimExpiresAt != nil {
			ts, err := parseTS(*claimExpiresAt)
			if err != nil {
				return domain.Task{}, fmt.Errorf("task %s claim_expires_at %q: %w", t.ID, *claimExpiresAt, err)
			}
			exp = &ts
		}
		t.Claim = domain.ClaimFromStored(&holder, at, exp, claimTTL)
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
	// LastActivity is derived on read by taskSelect (see lastActivityExpr): the
	// SQL never yields NULL, but the pointer + fallback keeps a corrupt row from
	// zeroing the field — it degrades to the previous "just UpdatedAt" semantics.
	lastAct := updated
	if lastActivity != nil {
		ts, err := parseTS(*lastActivity)
		if err != nil {
			return domain.Task{}, fmt.Errorf("task %s last_activity %q: %w", t.ID, *lastActivity, err)
		}
		lastAct = ts
	}
	t.LastActivity = lastAct
	return t, nil
}
