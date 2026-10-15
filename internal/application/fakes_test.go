package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// fakes_test.go holds the in-memory test doubles for every port in this
// package's use cases. Nothing in production code depends on them; SQLite
// integration tests prove the real adapters agree with these semantics.

// fakeClock is a controllable Clock: tests set "now" and advance it by hand.
type fakeClock struct{ now time.Time }

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time     { return c.now }
func (c *fakeClock) SetNow(t time.Time) { c.now = t }

// fakeIDGen mints deterministic, sortable IDs ("id-0001", "id-0002", …).
type fakeIDGen struct{ n int }

func (g *fakeIDGen) NewID() domain.ID {
	g.n++
	return domain.ID(fmt.Sprintf("id-%04d", g.n))
}

// fakeProjects is an in-memory ProjectRepository for use-case unit tests.
type fakeProjects struct {
	byID map[domain.ID]domain.Project
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{byID: make(map[domain.ID]domain.Project)}
}

func (f *fakeProjects) Create(_ context.Context, p domain.Project) error {
	if _, ok := f.byID[p.ID]; ok {
		return fmt.Errorf("duplicate project id %s", p.ID)
	}
	f.byID[p.ID] = p
	return nil
}

func (f *fakeProjects) Get(_ context.Context, id domain.ID) (domain.Project, error) {
	p, ok := f.byID[id]
	if !ok {
		return domain.Project{}, domain.ErrNotFound
	}
	return p, nil
}

// List returns all projects matching filter ordered by created_at ASC (id ASC tie-break),
// mirroring the port contract.
func (f *fakeProjects) List(_ context.Context, filter ...ProjectFilter) ([]domain.Project, error) {
	archived := false
	if len(filter) > 0 {
		archived = filter[0].Archived
	}
	out := make([]domain.Project, 0, len(f.byID))
	for _, p := range f.byID {
		if p.Archived != archived {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeProjects) Update(_ context.Context, p domain.Project) error {
	if _, ok := f.byID[p.ID]; !ok {
		return domain.ErrNotFound
	}
	f.byID[p.ID] = p
	return nil
}

// fakeTasks is an in-memory TaskRepository mirroring the SQLite adapter's
// semantics: Create is idempotent by unique ID, Update is version-guarded with
// ConflictError{Current} on a stale writer, List applies filters + canonical
// order + Limit/Offset and reports the pre-pagination total.
type fakeTasks struct {
	byID map[domain.ID]domain.Task
	// failUpdate, when non-nil, is invoked by Update before the version check;
	// a non-nil result replaces the normal outcome (simulates a concurrent
	// writer racing between the use case's Get and Update).
	failUpdate func(t domain.Task) error
	// lastFilter records the most recent List filter so tests can assert the
	// use case's clamping of Limit/Offset.
	lastFilter TaskFilter
	// rel gives relation-aware methods (List with UnblockedOnly, BlockersOf)
	// access to the edge set; newFakeUoW wires it. Nil = no edges at all, so a
	// standalone fake behaves exactly like an empty relation table.
	rel *fakeRelations
}

func newFakeTasks() *fakeTasks { return &fakeTasks{byID: make(map[domain.ID]domain.Task)} }

var _ TaskRepository = (*fakeTasks)(nil)

// Create stores the row as given; an existing ID is a no-op (idempotent, like
// the adapter's ON CONFLICT(id) DO NOTHING).
func (f *fakeTasks) Create(_ context.Context, t domain.Task) error {
	if _, ok := f.byID[t.ID]; !ok {
		f.byID[t.ID] = t
	}
	return nil
}

func (f *fakeTasks) Get(_ context.Context, id domain.ID) (domain.Task, error) {
	t, ok := f.byID[id]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, nil
}

// BlockersOf returns taskID's open blockers — the not-done, non-archived
// targets of its outgoing blocked-by edges (direct only, like the adapter), in
// edge-creation order. A standalone fake (rel == nil) reports no edges, like an
// empty relation table. The full stored task is returned; use cases and tests
// read it as a light summary (id/title/status).
func (f *fakeTasks) BlockersOf(ctx context.Context, taskID domain.ID) ([]domain.Task, error) {
	out := make([]domain.Task, 0)
	if f.rel == nil {
		return out, nil
	}
	edges, err := f.rel.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if e.TaskID != taskID || e.Kind != domain.RelationBlockedBy { // outgoing blocked-by only
			continue
		}
		target, ok := f.byID[e.OtherTaskID]
		if !ok {
			continue // dangling edge (the FK would prevent this in SQLite)
		}
		if target.Status == domain.StatusDone || target.Archived {
			continue
		}
		out = append(out, target)
	}
	return out, nil
}

// hasOpenBlocker reports whether t carries an outgoing blocked-by edge whose
// target is not done and not archived — the in-memory twin of SQLite's
// UnblockedOnly NOT EXISTS guard (slice 2D2b). Note a task that IS somebody's
// blocker but has no blockers of its own does NOT count as blocked.
func (f *fakeTasks) hasOpenBlocker(t domain.Task) bool {
	if f.rel == nil {
		return false
	}
	for _, e := range f.rel.edges {
		if e.TaskID != t.ID || e.Kind != domain.RelationBlockedBy {
			continue
		}
		target, ok := f.byID[e.OtherTaskID]
		if !ok {
			continue
		}
		if target.Status != domain.StatusDone && !target.Archived {
			return true
		}
	}
	return false
}

// List honors every TaskFilter field (UnblockedOnly included — slice 2D2b, via
// the edge set), sorts canonically, and applies Limit/Offset AFTER counting the
// total. The UnblockedOnly check must happen here (not in taskMatches) because
// it looks at relation edges, which only this struct can see through rel.
func (f *fakeTasks) List(_ context.Context, flt TaskFilter) ([]domain.Task, int64, error) {
	f.lastFilter = flt
	matched := make([]domain.Task, 0, len(f.byID))
	for _, t := range f.byID {
		if !taskMatches(t, flt) {
			continue
		}
		if flt.UnblockedOnly && f.hasOpenBlocker(t) { // an unresolved blocker excludes it
			continue
		}
		matched = append(matched, t)
	}
	sort.Slice(matched, func(i, j int) bool { return taskLess(matched[i], matched[j]) })

	total := int64(len(matched))
	if flt.Offset > 0 {
		if flt.Offset >= len(matched) {
			return []domain.Task{}, total, nil
		}
		matched = matched[flt.Offset:]
	}
	if flt.Limit > 0 && flt.Limit < len(matched) {
		matched = matched[:flt.Limit]
	}
	return matched, total, nil
}

// OpenTaskCounts aggregates open tasks per (project, status), mirroring the
// single grouped query in the sqlite adapter.
func (f *fakeTasks) OpenTaskCounts(_ context.Context) ([]TaskCountRow, error) {
	agg := make(map[domain.ID]map[domain.Status]int64)
	for _, t := range f.byID {
		if t.Archived { // open = non-archived only
			continue
		}
		m := agg[t.ProjectID]
		if m == nil {
			m = make(map[domain.Status]int64)
			agg[t.ProjectID] = m
		}
		m[t.Status]++
	}
	out := make([]TaskCountRow, 0, len(agg))
	for pid, m := range agg {
		for st, n := range m {
			out = append(out, TaskCountRow{ProjectID: pid, Status: st, Count: n})
		}
	}
	return out, nil
}

// Update is the optimistic guard in memory: stored Version must equal t.Version,
// then the row is stored with Version+1; a stale writer gets ConflictError with
// the current row (the use case's conflict-path unit tests rely on this).
func (f *fakeTasks) Update(_ context.Context, t domain.Task) (domain.Task, error) {
	if f.failUpdate != nil { // fault injection: a concurrent writer won the race
		if err := f.failUpdate(t); err != nil {
			return domain.Task{}, err
		}
	}
	cur, ok := f.byID[t.ID]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	if cur.Version != t.Version {
		return domain.Task{}, &domain.ConflictError{Current: cur}
	}
	t.Version = cur.Version + 1
	f.byID[t.ID] = t
	return f.byID[t.ID], nil
}

// Claim mirrors the SQLite adapter's CAS in memory: it wins when the lease is
// free, EXPIRED (claim_expires_at < now — takeover by anyone), or already held
// by actor with an ACTIVE lease (idempotent re-claim). A different live holder
// loses with *domain.ClaimConflictError{Holder, ExpiresAt} and nothing changes.
// Each win re-stamps claimed_at/claim_expires_at to now/now+ttl, forces
// status=doing, and bumps version — the same invariants the SQL encodes. The
// in-memory map is single-goroutine by construction (fakeUoW runs fn
// synchronously), so this read-check-write is as atomic as the CAS for unit
// tests; the racing-goroutines proof lives in the SQLite integration tests.
func (f *fakeTasks) Claim(_ context.Context, id domain.ID, actor domain.Actor, ttlSec int, now time.Time) (domain.Task, error) {
	cur, ok := f.byID[id]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	if cur.Archived {
		return domain.Task{}, fmt.Errorf("%w: cannot claim archived task %s", domain.ErrInvalid, id)
	}
	if cur.Status == domain.StatusDone {
		return domain.Task{}, fmt.Errorf("%w: cannot claim completed task %s", domain.ErrInvalid, id)
	}
	switch {
	case cur.ClaimedBy == nil || cur.ClaimExpiry == nil: // free
	case cur.ClaimExpiry.Before(now): // expired → takeover (any claimant)
	case sameLeaseHolder(&cur, actor): // active self-lease (NAME is the key) → idempotent re-claim
	default:
		return domain.Task{}, &domain.ClaimConflictError{Holder: *cur.ClaimedBy, ExpiresAt: *cur.ClaimExpiry}
	}
	holder := actor
	start := now
	expiry := now.Add(time.Duration(ttlSec) * time.Second)
	cur.Status = domain.StatusDoing
	cur.ClaimedBy = &holder
	cur.ClaimedAt = &start
	cur.ClaimExpiry = &expiry
	cur.ClaimTTLSeconds = ttlSec
	cur.Version++
	cur.UpdatedAt = start
	f.byID[id] = cur
	return cur, nil
}

// Release mirrors the adapter's holder-only CAS in memory: it clears the lease
// Release mirrors sqlite's guarded release: it clears lease fields
// and moves status→todo (bumping version) for the lease holder (active or
// expired). A foreign holder gets *domain.ClaimConflictError{Holder, ExpiresAt}.
// When force is true, any lease is cleared regardless of holder.
// A task with no lease at all is an idempotent no-op returning the
// current row (mirrors the SQL's 0-rows → empty-holder branch).
func (f *fakeTasks) Release(_ context.Context, id domain.ID, actor domain.Actor, now time.Time, force bool) (domain.Task, error) {
	cur, ok := f.byID[id]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	if cur.ClaimedBy == nil || cur.ClaimExpiry == nil {
		return cur, nil // nothing to release: idempotent no-op, row untouched
	}
	if !force && !sameLeaseHolder(&cur, actor) {
		return domain.Task{}, &domain.ClaimConflictError{Holder: *cur.ClaimedBy, ExpiresAt: *cur.ClaimExpiry}
	}
	start := now
	if cur.Status != domain.StatusDone {
		cur.Status = domain.StatusTodo
	}
	cur.ClaimedBy = nil
	cur.ClaimedAt = nil
	cur.ClaimExpiry = nil
	cur.ClaimTTLSeconds = 0
	cur.Version++
	cur.UpdatedAt = start
	f.byID[id] = cur
	return cur, nil
}

// Heartbeat mirrors the adapter's guarded extension in memory: it extends
// claim_expires_at to now + the STORED TTL only for the current holder of an
// ACTIVE lease; a foreign or expired holder — or no lease at all (zero-value
// Holder = "claim it first") — gets *domain.ClaimConflictError. Deliberately no
// version bump and no UpdatedAt change: keep-alive is not a state change.
func (f *fakeTasks) Heartbeat(_ context.Context, id domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	cur, ok := f.byID[id]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	if cur.Archived {
		return domain.Task{}, fmt.Errorf("%w: cannot heartbeat archived task %s", domain.ErrInvalid, id)
	}
	if cur.Status == domain.StatusDone {
		return domain.Task{}, fmt.Errorf("%w: cannot heartbeat completed task %s", domain.ErrInvalid, id)
	}
	if cur.ClaimedBy == nil || cur.ClaimExpiry == nil {
		return domain.Task{}, &domain.ClaimConflictError{Holder: domain.Actor{}, ExpiresAt: time.Time{}}
	}
	active := !cur.ClaimExpiry.Before(now)
	if !active || !sameLeaseHolder(&cur, actor) {
		return domain.Task{}, &domain.ClaimConflictError{Holder: *cur.ClaimedBy, ExpiresAt: *cur.ClaimExpiry}
	}
	expiry := now.Add(time.Duration(cur.ClaimTTLSeconds) * time.Second)
	cur.ClaimExpiry = &expiry
	f.byID[id] = cur // Version and UpdatedAt intentionally untouched
	return cur, nil
}

// taskMatches mirrors sqlite's taskListWhere condition-by-condition.
func taskMatches(t domain.Task, f TaskFilter) bool {
	if f.ProjectID != "" && t.ProjectID != f.ProjectID {
		return false
	}
	if f.Status != "" && t.Status != f.Status {
		return false
	}
	if f.AssigneeType != "" && (t.Assignee == nil || t.Assignee.Type != f.AssigneeType) {
		return false
	}
	if f.AssigneeName != "" && (t.Assignee == nil || t.Assignee.Name != f.AssigneeName) {
		return false
	}
	if f.Priority != nil && t.Priority != *f.Priority {
		return false
	}
	if f.DueBefore != nil && (t.DueAt == nil || !t.DueAt.Before(*f.DueBefore)) { // due_at < before; undated never matches
		return false
	}
	if f.DueAfter != nil && (t.DueAt == nil || t.DueAt.Before(*f.DueAfter)) { // due_at >= after; undated never matches
		return false
	}
	if f.Archived != t.Archived { // false = open only (REST default); true = archived only
		return false
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.Contains(strings.ToLower(t.Title), q) &&
			!strings.Contains(strings.ToLower(t.Description), q) &&
			!strings.Contains(strings.ToLower(t.Notes), q) {
			return false
		}
	}
	if f.CreatedAfter != nil && t.CreatedAt.Before(*f.CreatedAfter) { // created_at >= after
		return false
	}
	if f.CreatedBefore != nil && !t.CreatedAt.Before(*f.CreatedBefore) { // created_at < before
		return false
	}
	if f.CompletedAfter != nil && (t.CompletedAt == nil || t.CompletedAt.Before(*f.CompletedAfter)) {
		return false
	}
	if f.CompletedBefore != nil && (t.CompletedAt == nil || !t.CompletedAt.Before(*f.CompletedBefore)) {
		return false
	}
	// Note: f.UnblockedOnly is NOT checked here — it depends on relation edges,
	// which only fakeTasks.List can see through its rel reference (slice 2D2b).
	return true
}

// taskLess is the canonical "what's next" ordering: priority DESC, due_at ASC
// nulls-last (SQLite's `(due_at IS NULL)` trick), created_at ASC.
func taskLess(a, b domain.Task) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	aNil, bNil := a.DueAt == nil, b.DueAt == nil
	if aNil || bNil {
		return !aNil // non-null due dates sort before nulls
	}
	if !a.DueAt.Equal(*b.DueAt) {
		return a.DueAt.Before(*b.DueAt)
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

// fakeACs is an in-memory AcceptanceCriterionRepository: the creation/initial-
// checklist path plus the post-create editing trio (Add / UpdateText / Delete).
type fakeACs struct {
	byTask map[domain.ID][]domain.AcceptanceCriterion
}

func newFakeACs() *fakeACs { return &fakeACs{byTask: make(map[domain.ID][]domain.AcceptanceCriterion)} }

var _ AcceptanceCriterionRepository = (*fakeACs)(nil)

// Create appends the criteria as given (IDs/Seq are minted by the use case).
func (f *fakeACs) Create(_ context.Context, criteria []domain.AcceptanceCriterion) error {
	for _, c := range criteria {
		f.byTask[c.TaskID] = append(f.byTask[c.TaskID], c)
	}
	return nil
}

// ListByTask returns the task's criteria ordered by Seq ASC (empty slice if
// none), mirroring the port contract.
func (f *fakeACs) ListByTask(_ context.Context, taskID domain.ID) ([]domain.AcceptanceCriterion, error) {
	out := make([]domain.AcceptanceCriterion, len(f.byTask[taskID]))
	copy(out, f.byTask[taskID])
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// SetDone mirrors the SQLite adapter's guarded toggle in memory: the pair is
// addressed by (taskID, seq); checking stamps note/completion fields to the
// given values, unchecking clears all three again. Unknown pair →
// domain.ErrNotFound; the fresh row is returned.
func (f *fakeACs) SetDone(_ context.Context, taskID domain.ID, seq int, done bool, note string, actor domain.Actor, now time.Time) (domain.AcceptanceCriterion, error) {
	rows := f.byTask[taskID]
	for i := range rows {
		if rows[i].Seq != seq {
			continue
		}
		c := &rows[i]
		c.Done = done
		if done {
			c.Note = note
			a := actor
			t := now
			c.CompletedBy = &a
			c.CompletedAt = &t
		} else {
			c.Note = ""
			c.CompletedBy = nil
			c.CompletedAt = nil
		}
		return *c, nil
	}
	return domain.AcceptanceCriterion{}, domain.ErrNotFound
}

// Add mirrors the SQLite adapter's post-create append: stores the criterion as
// given (ID/Seq minted by the use case). No constraint checks in memory — the
// real duplicate/FK semantics live in the sqlite package.
func (f *fakeACs) Add(_ context.Context, c domain.AcceptanceCriterion) error {
	f.byTask[c.TaskID] = append(f.byTask[c.TaskID], c)
	return nil
}

// UpdateText mirrors the SQLite adapter's guarded reword: only the text field
// changes; done/note/completion fields are untouched. Unknown pair →
// domain.ErrNotFound; the fresh row is returned.
func (f *fakeACs) UpdateText(_ context.Context, taskID domain.ID, seq int, text string) (domain.AcceptanceCriterion, error) {
	rows := f.byTask[taskID]
	for i := range rows {
		if rows[i].Seq != seq {
			continue
		}
		c := &rows[i]
		c.Text = text
		return *c, nil
	}
	return domain.AcceptanceCriterion{}, domain.ErrNotFound
}

// Delete mirrors the SQLite adapter: removes exactly the (taskID, seq) row and
// returns the DELETED row (its before-state rides on the caller's event).
// Surviving rows keep their seqs (gaps stay). Unknown pair → domain.ErrNotFound.
func (f *fakeACs) Delete(_ context.Context, taskID domain.ID, seq int) (domain.AcceptanceCriterion, error) {
	rows := f.byTask[taskID]
	for i := range rows {
		if rows[i].Seq != seq {
			continue
		}
		removed := rows[i]
		f.byTask[taskID] = append(rows[:i], rows[i+1:]...)
		return removed, nil
	}
	return domain.AcceptanceCriterion{}, domain.ErrNotFound
}

// fakeComments is an in-memory CommentRepository (append + newest-first tail).
type fakeComments struct {
	byTask map[domain.ID][]domain.Comment
}

func newFakeComments() *fakeComments {
	return &fakeComments{byTask: make(map[domain.ID][]domain.Comment)}
}

var _ CommentRepository = (*fakeComments)(nil)

// Add appends the comment as given.
func (f *fakeComments) Add(_ context.Context, c domain.Comment) error {
	f.byTask[c.TaskID] = append(f.byTask[c.TaskID], c)
	return nil
}

// CountByTask returns the stored comment count for a task (before paging).
func (f *fakeComments) CountByTask(_ context.Context, taskID domain.ID) (int64, error) {
	return int64(len(f.byTask[taskID])), nil
}

// ListByTask returns up to limit comments newest first (created_at DESC,
// insertion order as tie-break), skipping offset; limit <= 0 is unbounded.
func (f *fakeComments) ListByTask(_ context.Context, taskID domain.ID, limit, offset int) ([]domain.Comment, error) {
	all := make([]domain.Comment, len(f.byTask[taskID]))
	copy(all, f.byTask[taskID])
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })

	if offset > 0 {
		if offset >= len(all) {
			return []domain.Comment{}, nil
		}
		all = all[offset:]
	}
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	out := make([]domain.Comment, len(all))
	copy(out, all)
	return out, nil
}

// fakeArtifacts is an in-memory ArtifactRepository (append + oldest-first).
type fakeArtifacts struct {
	byTask map[domain.ID][]domain.Artifact
}

func newFakeArtifacts() *fakeArtifacts {
	return &fakeArtifacts{byTask: make(map[domain.ID][]domain.Artifact)}
}

var _ ArtifactRepository = (*fakeArtifacts)(nil)

// Add appends the artifact as given.
func (f *fakeArtifacts) Add(_ context.Context, a domain.Artifact) error {
	f.byTask[a.TaskID] = append(f.byTask[a.TaskID], a)
	return nil
}

// ListByTask returns all artifacts ordered by created_at ASC (empty slice if
// none), mirroring the port contract.
func (f *fakeArtifacts) ListByTask(_ context.Context, taskID domain.ID) ([]domain.Artifact, error) {
	out := make([]domain.Artifact, len(f.byTask[taskID]))
	copy(out, f.byTask[taskID])
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// fakeRelations is an in-memory RelationRepository: edges touching either end
// are returned by ListByTask; Add/Remove keep minimal upsert/delete semantics.
type fakeRelations struct {
	edges []domain.Relation
}

func newFakeRelations() *fakeRelations { return &fakeRelations{} }

var _ RelationRepository = (*fakeRelations)(nil)

// Add upserts the edge keyed by (TaskID, OtherTaskID, Kind).
func (f *fakeRelations) Add(_ context.Context, rel domain.Relation) error {
	for _, e := range f.edges {
		if e.TaskID == rel.TaskID && e.OtherTaskID == rel.OtherTaskID && e.Kind == rel.Kind {
			return nil
		}
	}
	f.edges = append(f.edges, rel)
	return nil
}

// Remove deletes exactly that edge; missing → domain.ErrNotFound.
func (f *fakeRelations) Remove(_ context.Context, taskID, otherTaskID domain.ID, kind domain.RelationKind) error {
	for i, e := range f.edges {
		if e.TaskID == taskID && e.OtherTaskID == otherTaskID && e.Kind == kind {
			f.edges = append(f.edges[:i], f.edges[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// ListByTask returns every edge touching taskID in either direction, ordered
// by created_at ASC (empty slice if none).
func (f *fakeRelations) ListByTask(_ context.Context, taskID domain.ID) ([]domain.Relation, error) {
	out := make([]domain.Relation, 0)
	for _, e := range f.edges {
		if e.TaskID == taskID || e.OtherTaskID == taskID {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// fakeEvents is an in-memory EventStore that records append order and serves
// newest-first tails with limit/offset, mirroring the SQLite event store.
type fakeEvents struct{ events []domain.Event }

var _ EventStore = (*fakeEvents)(nil)

// Append records the event; the recorded slice doubles as the assertion target
// in use-case tests (exactly which events a mutation emitted).
func (f *fakeEvents) Append(_ context.Context, e domain.Event) error {
	f.events = append(f.events, e)
	return nil
}

// ListByTask returns up to limit events for the task, newest first (insertion
// order reversed — same-timestamp ties resolve like the adapter's rowid DESC),
// skipping offset; limit <= 0 is unbounded.
func (f *fakeEvents) ListByTask(_ context.Context, taskID domain.ID, limit, offset int) ([]domain.Event, error) {
	return tailBy(func(e domain.Event) bool { return e.TaskID != nil && *e.TaskID == taskID }, f.events, limit, offset)
}

// ListByProject returns up to limit events project-wide, newest first, skipping
// offset; limit <= 0 is unbounded.
func (f *fakeEvents) ListByProject(_ context.Context, projectID domain.ID, limit, offset int) ([]domain.Event, error) {
	return tailBy(func(e domain.Event) bool { return e.ProjectID != nil && *e.ProjectID == projectID }, f.events, limit, offset)
}

// ListAfterProject returns the project's events strictly after (by id), oldest
// first, up to limit — the SSE replay path over the in-memory log. The recorded
// slice is append-ordered, which mirrors chronological order.
func (f *fakeEvents) ListAfterProject(_ context.Context, projectID domain.ID, after domain.ID, limit int) ([]domain.Event, error) {
	out := make([]domain.Event, 0)
	for _, e := range f.events {
		if e.ProjectID != nil && *e.ProjectID == projectID && e.ID > after {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func tailBy(match func(domain.Event) bool, events []domain.Event, limit, offset int) ([]domain.Event, error) {
	matched := make([]domain.Event, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- { // newest first
		if match(events[i]) {
			matched = append(matched, events[i])
		}
	}
	if offset > 0 {
		if offset >= len(matched) {
			return []domain.Event{}, nil
		}
		matched = matched[offset:]
	}
	if limit > 0 && limit < len(matched) {
		matched = matched[:limit]
	}
	out := make([]domain.Event, len(matched))
	copy(out, matched)
	return out, nil
}

// fakeUoW implements UnitOfWork for use-case unit tests: it binds ALL the
// given fakes into r before fn (the same contract the real implementation
// honors) and skips transaction mechanics — rollback behavior is covered by
// the SQLite integration tests.
type fakeUoW struct {
	projects  *fakeProjects
	tasks     *fakeTasks
	acs       *fakeACs
	comments  *fakeComments
	artifacts *fakeArtifacts
	relations *fakeRelations
	events    *fakeEvents
}

// newFakeUoW keeps the original (projects, events) parameters; the remaining
// ports get fresh in-memory fakes that tests can reach through the returned
// struct's fields.
func newFakeUoW(projects *fakeProjects, events *fakeEvents) *fakeUoW {
	u := &fakeUoW{
		projects:  projects,
		tasks:     newFakeTasks(),
		acs:       newFakeACs(),
		comments:  newFakeComments(),
		artifacts: newFakeArtifacts(),
		relations: newFakeRelations(),
		events:    events,
	}
	u.tasks.rel = u.relations // relation-aware List (UnblockedOnly) + BlockersOf (slice 2D2b)
	return u
}

func (u *fakeUoW) Run(_ context.Context, r *Repos, fn func() error) error {
	r.Projects = u.projects
	r.Tasks = u.tasks
	r.ACs = u.acs
	r.Comments = u.comments
	r.Artifacts = u.artifacts
	r.Relations = u.relations
	r.Events = u.events
	return fn()
}

var _ UnitOfWork = (*fakeUoW)(nil)
