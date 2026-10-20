package sqlite

// Integration tests over a real temp-file SQLite database for the slice-2D1
// collaboration/evidence WRITE use cases: typed comments, replace-on-write
// notes, AC toggle, artifact attach. Each test writes through its use case and
// then re-reads via Tasks.GetDetail / Get to prove the child-row read paths
// (comment tail, artifacts, AC checklist) reflect the new data, that the
// version-bump policy held on real SQL (child rows leave task.version and
// updated_at untouched; notes bumps version +1), and that the matching event
// row committed alongside the data in the same transaction.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
)

// boolPtr returns a pointer to b (test helper for ToggleAC's optional done —
// nil means "preserve").
func boolPtr(b bool) *bool { return &b }

func newCollabEnv(t *testing.T, db *sql.DB) (*application.Tasks, *application.Projects,
	*application.Comments, *application.Acceptance, *application.Artifacts) {
	t.Helper()
	uow := NewUnitOfWork(db) // one tx-bound Repos per Run; sharing uow/clock/idgen is safe
	return application.NewTasks(uow, clock.New(), idgen.New()),
		application.NewProjects(uow, clock.New(), idgen.New()),
		application.NewComments(uow, clock.New(), idgen.New()),
		application.NewAcceptance(uow, clock.New(), idgen.New()),
		application.NewArtifacts(uow, clock.New(), idgen.New())
}

// detailEventKinds returns the set of event kinds present in a TaskDetail's
// tail (newest first), for "the mutation committed its event" assertions.
func detailEventKinds(t *testing.T, d application.TaskDetail) []domain.EventKind {
	t.Helper()
	out := make([]domain.EventKind, 0, len(d.Events))
	for _, e := range d.Events {
		out = append(out, e.Kind)
	}
	return out
}

func TestCollab_CommentAppearsInGetDetailTailWithEventAndNoVersionBump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects, comments, _, _ := newCollabEnv(t, db)
	p := mustCreateProject(t, ctx, projects)
	task, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "Comment me", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	baseVersion, baseUpdated := task.Version, task.UpdatedAt

	got, err := comments.Add(ctx, testActor, task.ID, domain.CommentHandoff, "hand off: left off at the SQL step")
	if err != nil {
		t.Fatalf("Add comment: %v", err)
	}
	if got.Kind != domain.CommentHandoff || got.TaskID != task.ID {
		t.Fatalf("returned comment = %+v, want handoff on %s", got, task.ID)
	}

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comment tail = %d rows, want the fresh comment (1)", len(detail.Comments))
	}
	c := detail.Comments[0]
	if c.ID != got.ID || c.Kind != domain.CommentHandoff || c.Body != "hand off: left off at the SQL step" ||
		c.Actor != testActor {
		t.Errorf("stored comment = %+v, want id %s handoff by agent-a", c, got.ID)
	}

	// Child-row rule on real SQL: the task row's version + updated_at untouched.
	if detail.Task.Version != baseVersion || !detail.Task.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after comment: version = %d (want %d), updated_at = %s (want %s)",
			detail.Task.Version, baseVersion, detail.Task.UpdatedAt, baseUpdated)
	}

	kinds := detailEventKinds(t, detail)
	found := false
	for i, k := range kinds {
		if k != domain.EventCommented {
			continue
		}
		found = true
		ev := detail.Events[i]
		if ev.Actor != testActor || ev.TaskID == nil || *ev.TaskID != task.ID {
			t.Errorf("commented event actor/ids = (%+v %v), want agent-a on %s", ev.Actor, ev.TaskID, task.ID)
		}
		if !strings.Contains(ev.After, `"kind":"handoff"`) || !strings.Contains(ev.After, "left off at the SQL step") {
			t.Errorf("commented event After = %q, want kind + body", ev.After)
		}
	}
	if !found {
		t.Errorf("no commented event in the detail tail (kinds: %v)", kinds)
	}
}

func TestCollab_ArtifactAppearsInGetDetailWithEventAndNoVersionBump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects, _, _, artifacts := newCollabEnv(t, db)
	p := mustCreateProject(t, ctx, projects)
	task, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "Attach me", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	baseVersion, baseUpdated := task.Version, task.UpdatedAt

	got, err := artifacts.Attach(ctx, testActor, task.ID, domain.ArtifactCommandOutput, "https://evidence.local/build.txt", "build log, exit 0")
	if err != nil {
		t.Fatalf("Attach artifact: %v", err)
	}
	if got.Kind != domain.ArtifactCommandOutput || got.Ref != "https://evidence.local/build.txt" {
		t.Fatalf("returned artifact = %+v, want command-output evidence ref", got)
	}

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if len(detail.Artifacts) != 1 {
		t.Fatalf("artifacts = %d rows, want the fresh artifact (1)", len(detail.Artifacts))
	}
	a := detail.Artifacts[0]
	if a.ID != got.ID || a.Kind != domain.ArtifactCommandOutput || a.Ref != "https://evidence.local/build.txt" ||
		a.Summary != "build log, exit 0" || a.Actor != testActor {
		t.Errorf("stored artifact = %+v, want id %s command-output by agent-a", a, got.ID)
	}

	if detail.Task.Version != baseVersion || !detail.Task.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after artifact: version = %d (want %d), updated_at = %s (want %s)",
			detail.Task.Version, baseVersion, detail.Task.UpdatedAt, baseUpdated)
	}

	found := false
	for i, k := range detailEventKinds(t, detail) {
		if k != domain.EventArtifactAdded {
			continue
		}
		found = true
		ev := detail.Events[i]
		if !strings.Contains(ev.After, `"kind":"command-output"`) || !strings.Contains(ev.After, `"ref":"https://evidence.local/build.txt"`) {
			t.Errorf("artifact-added event After = %q, want kind + ref", ev.After)
		}
	}
	if !found {
		t.Errorf("no artifact-added event in the detail tail (kinds: %v)", detailEventKinds(t, detail))
	}
}

func TestCollab_AcToggleShownDoneInGetDetailWithEventAndNoVersionBump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects, _, acs, _ := newCollabEnv(t, db)
	p := mustCreateProject(t, ctx, projects)
	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		Title: "Check me", ProjectID: p.ID, ACs: []string{"Wire the toggle"},
	})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	baseVersion, baseUpdated := task.Version, task.UpdatedAt

	got, err := acs.ToggleAC(ctx, testActor, task.ID, 1, boolPtr(true), "wired and tested")
	if err != nil {
		t.Fatalf("ToggleAC: %v", err)
	}
	if !got.Done || got.CompletedBy == nil || got.CompletedBy.Name != testActor.Name {
		t.Fatalf("returned AC = %+v, want seq 1 done by agent-a", got)
	}

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if len(detail.AcceptanceCriteria) != 1 {
		t.Fatalf("ACs = %d rows, want 1", len(detail.AcceptanceCriteria))
	}
	ac := detail.AcceptanceCriteria[0]
	if !ac.Done || ac.Note != "wired and tested" || ac.CompletedBy == nil || *ac.CompletedBy != testActor || ac.CompletedAt == nil {
		t.Errorf("stored AC = %+v, want done with note + who/when", ac)
	}

	if detail.Task.Version != baseVersion || !detail.Task.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC toggle: version = %d (want %d), updated_at = %s (want %s)",
			detail.Task.Version, baseVersion, detail.Task.UpdatedAt, baseUpdated)
	}

	found := false
	for i, k := range detailEventKinds(t, detail) {
		if k != domain.EventACToggled {
			continue
		}
		found = true
		ev := detail.Events[i]
		if !strings.Contains(ev.Before, `"done":false`) || !strings.Contains(ev.After, `"done":true`) {
			t.Errorf("ac-toggled event = (%q → %q), want done false→true", ev.Before, ev.After)
		}
	}
	if !found {
		t.Errorf("no ac-toggled event in the detail tail (kinds: %v)", detailEventKinds(t, detail))
	}
}

func TestCollab_UpdateNotesReplacesBumpsVersionAndEmitsNoted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects, _, _, _ := newCollabEnv(t, db)
	p := mustCreateProject(t, ctx, projects)
	task, err := tasks.Create(ctx, testActor, application.TaskInput{Title: "Note me", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	baseVersion := task.Version

	got, err := tasks.UpdateNotes(ctx, testActor, task.ID, "## resume context\nleft off at the migration step", 0)
	if err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	if got.Notes != "## resume context\nleft off at the migration step" {
		t.Errorf("notes = %q, want the full replacement blob", got.Notes)
	}
	if got.Version != baseVersion+1 {
		t.Errorf("version = %d, want %d (notes IS a task-row mutation)", got.Version, baseVersion+1)
	}

	// Get re-reads the stored row: new notes + bumped version.
	stored, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Notes != got.Notes || stored.Version != baseVersion+1 {
		t.Errorf("stored = (notes %q v%d), want the replaced blob at v%d", stored.Notes, stored.Version, baseVersion+1)
	}

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Task.Notes != got.Notes || detail.Task.Version != baseVersion+1 {
		t.Errorf("detail task = (notes %q v%d), want the replaced blob at v%d", detail.Task.Notes, detail.Task.Version, baseVersion+1)
	}
	found := false
	for i, k := range detailEventKinds(t, detail) {
		if k != domain.EventNoted {
			continue
		}
		found = true
		ev := detail.Events[i]
		if !strings.Contains(ev.After, "left off at the migration step") {
			t.Errorf("noted event After = %q, want the new blob", ev.After)
		}
	}
	if !found {
		t.Errorf("no noted event in the detail tail (kinds: %v)", detailEventKinds(t, detail))
	}
}

// TestCollab_AcEditingRoundTripProvesSeqStabilityOnRealSQL covers the
// post-create editing trio end to end: append at seq 2, reword it, delete it —
// then prove via GetDetail that only the original criterion remains WITH ITS
// ORIGINAL SEQ (no renumbering), that all three event rows committed alongside
// their data in their transactions, and that the task row's version and
// updated_at were never touched.
func TestCollab_AcEditingRoundTripProvesSeqStabilityOnRealSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects, _, acs, _ := newCollabEnv(t, db)
	p := mustCreateProject(t, ctx, projects)
	task, err := tasks.Create(ctx, testActor, application.TaskInput{
		Title: "Edit me", ProjectID: p.ID, ACs: []string{"Original check"},
	})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	baseVersion, baseUpdated := task.Version, task.UpdatedAt

	added, err := acs.AddAC(ctx, testActor, task.ID, "Added later")
	if err != nil {
		t.Fatalf("AddAC: %v", err)
	}
	if added.Seq != 2 || added.Text != "Added later" {
		t.Fatalf("added AC = %+v, want seq 2 'Added later'", added)
	}

	edited, err := acs.EditACText(ctx, testActor, task.ID, 2, "Added later (hardened)")
	if err != nil {
		t.Fatalf("EditACText: %v", err)
	}
	if edited.Seq != 2 || edited.Text != "Added later (hardened)" {
		t.Fatalf("edited AC = %+v, want seq 2 reworded", edited)
	}

	// Identical reword is a no-op: fresh row, no new event.
	if _, err := acs.EditACText(ctx, testActor, task.ID, 2, "Added later (hardened)"); err != nil {
		t.Fatalf("EditACText no-op: %v", err)
	}

	if err := acs.RemoveAC(ctx, testActor, task.ID, 2); err != nil {
		t.Fatalf("RemoveAC: %v", err)
	}

	detail, err := tasks.GetDetail(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if len(detail.AcceptanceCriteria) != 1 {
		t.Fatalf("ACs = %d rows, want only the original (1)", len(detail.AcceptanceCriteria))
	}
	ac := detail.AcceptanceCriteria[0]
	if ac.Seq != 1 || ac.Text != "Original check" {
		t.Errorf("surviving AC = %+v, want seq 1 'Original check' (its ORIGINAL seq — gaps stay)", ac)
	}

	if detail.Task.Version != baseVersion || !detail.Task.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC editing: version = %d (want %d), updated_at = %s (want %s)",
			detail.Task.Version, baseVersion, detail.Task.UpdatedAt, baseUpdated)
	}

	want := map[domain.EventKind]string{
		domain.EventACAdded:   `"seq":2`,
		domain.EventACUpdated: `"text":"Added later"`, // Before carries the old text
		domain.EventACRemoved: `"seq":2`,
	}
	for i, k := range detailEventKinds(t, detail) {
		wantFrag, ok := want[k]
		if !ok {
			continue
		}
		ev := detail.Events[i]
		if ev.Actor != testActor || ev.TaskID == nil || *ev.TaskID != task.ID {
			t.Errorf("%s event actor/ids = (%+v %v), want agent-a on %s", k, ev.Actor, ev.TaskID, task.ID)
		}
		if !strings.Contains(ev.Before+ev.After, wantFrag) {
			t.Errorf("%s event payload = (%q → %q), want to carry %s", k, ev.Before, ev.After, wantFrag)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("missing AC-editing events in the detail tail: %v (kinds seen: %v)", want, detailEventKinds(t, detail))
	}

	// The no-op reword emitted NO extra ac-updated row.
	var updated int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE kind = 'ac-updated'`).Scan(&updated); err != nil {
		t.Fatalf("count ac-updated: %v", err)
	}
	if updated != 1 {
		t.Errorf("ac-updated events = %d, want exactly 1 (identical reword is a no-op)", updated)
	}
}

// TestCollab_FailedChildRowWriteCommitsNothing proves the transactional seam on
// real SQL: a child-row write whose parent task does not exists errors out and
// leaves NO orphan event row behind (data + event commit or roll back together).
func TestCollab_FailedChildRowWriteCommitsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	_, _, comments, _, artifacts := newCollabEnv(t, db)

	missing := domain.ID("01H0000000000000000000MISS") // never created (valid ULID shape)
	if _, err := comments.Add(ctx, testActor, missing, domain.CommentNote, "orphan?"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("comment on unknown task → err = %v, want ErrNotFound", err)
	}
	if _, err := artifacts.Attach(ctx, testActor, missing, domain.ArtifactLog, "x", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("artifact on unknown task → err = %v, want ErrNotFound", err)
	}

	// The project exists but has no tasks → zero event rows anywhere.
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 0 {
		t.Errorf("events after failed child-row writes = %d, want 0 (no orphans)", n)
	}
	var c int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM comment`).Scan(&c); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if c != 0 {
		t.Errorf("comments after failed adds = %d, want 0", c)
	}
}
