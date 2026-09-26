package application

import (
	"context"
	"errors"
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

// List returns all projects ordered by created_at ASC (id ASC tie-break),
// mirroring the port contract.
func (f *fakeProjects) List(_ context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(f.byID))
	for _, p := range f.byID {
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

// List honors every implemented TaskFilter field, sorts canonically, and
// applies Limit/Offset AFTER counting the total. UnblockedOnly is ignored —
// same as the SQLite adapter until slice 2D (relations).
func (f *fakeTasks) List(_ context.Context, flt TaskFilter) ([]domain.Task, int64, error) {
	f.lastFilter = flt
	matched := make([]domain.Task, 0, len(f.byID))
	for _, t := range f.byID {
		if taskMatches(t, flt) {
			matched = append(matched, t)
		}
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
// and moves status→todo (bumping version) ONLY when actor holds an ACTIVE
// lease; a foreign or expired holder gets *domain.ClaimConflictError{Holder,
// ExpiresAt}. A task with no lease at all is an idempotent no-op returning the
// current row (mirrors the SQL's 0-rows → empty-holder branch).
func (f *fakeTasks) Release(_ context.Context, id domain.ID, actor domain.Actor, now time.Time) (domain.Task, error) {
	cur, ok := f.byID[id]
	if !ok {
		return domain.Task{}, domain.ErrNotFound
	}
	if cur.ClaimedBy == nil || cur.ClaimExpiry == nil {
		return cur, nil // nothing to release: idempotent no-op, row untouched
	}
	active := !cur.ClaimExpiry.Before(now)
	if !active || !sameLeaseHolder(&cur, actor) {
		return domain.Task{}, &domain.ClaimConflictError{Holder: *cur.ClaimedBy, ExpiresAt: *cur.ClaimExpiry}
	}
	start := now
	cur.Status = domain.StatusTodo
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
	// TODO(slice 2D): f.UnblockedOnly — relation-dependent; ignored like in sqlite.
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

// fakeACs is an in-memory AcceptanceCriterionRepository (create + read).
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

func (f *fakeACs) SetDone(context.Context, domain.ID, int, bool, string, domain.Actor, time.Time) (domain.AcceptanceCriterion, error) {
	return domain.AcceptanceCriterion{}, errors.New("fakeACs: SetDone lands in slice 2C")
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
	return &fakeUoW{
		projects:  projects,
		tasks:     newFakeTasks(),
		acs:       newFakeACs(),
		comments:  newFakeComments(),
		artifacts: newFakeArtifacts(),
		relations: newFakeRelations(),
		events:    events,
	}
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
