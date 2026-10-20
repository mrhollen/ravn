package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

// string_bounds_test.go: unit tests pinning the string-length caps at every
// WRITE path — task create/update/notes, project create/update, comment add,
// and AC add/edit/toggle. Each over-cap input must be rejected through the
// established domain.ErrInvalid path (HTTP 400 invalid-input) with nothing
// written; at-cap inputs are accepted; and legacy rows stored over the caps
// stay readable and mutable for fields an update does not touch
// (write-path-only enforcement).

func assertErrInvalid(t *testing.T, err error, op string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("%s: err = %v, want wrapped domain.ErrInvalid", op, err)
	}
}

// newBoundsCollabEnv wires Tasks + Comments + Acceptance over ONE fakeUoW so a
// seeded task is visible to all three use cases.
func newBoundsCollabEnv(t *testing.T) (*Tasks, *Comments, *Acceptance, *fakeClock, *fakeUoW) {
	t.Helper()
	fc := newFakeClock(mustTime(t, testStart))
	fp := newFakeProjects()
	fp.byID[testProjectID] = domain.Project{ID: testProjectID, Name: "Demo", CreatedAt: fc.now, UpdatedAt: fc.now}
	uow := newFakeUoW(fp, &fakeEvents{})
	tasks := NewTasks(uow, fc, &fakeIDGen{})
	comments := NewComments(uow, fc, &fakeIDGen{})
	acs := NewAcceptance(uow, fc, &fakeIDGen{})
	return tasks, comments, acs, fc, uow
}

func seedBoundTask(t *testing.T, svc *Tasks) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), testAgent, TaskInput{
		Title: "Seed task for bound tests", ProjectID: testProjectID,
	})
	if err != nil {
		t.Fatalf("Create seed task: %v", err)
	}
	return task
}

// --- Tasks.Create -------------------------------------------------------------

func TestTasksCreate_TitleOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)

	_, err := svc.Create(ctx, testAgent, TaskInput{
		Title: strings.Repeat("t", domain.MaxTaskTitleLen+1), ProjectID: testProjectID,
	})
	assertErrInvalid(t, err, "Create")
	if len(uow.tasks.byID) != 0 {
		t.Fatalf("rejected create still stored %d task row(s)", len(uow.tasks.byID))
	}
	if len(uow.events.events) != 0 {
		t.Fatalf("rejected create still appended %d event(s)", len(uow.events.events))
	}
}

func TestTasksCreate_DescriptionOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)

	_, err := svc.Create(ctx, testAgent, TaskInput{
		Title: "fine title", ProjectID: testProjectID,
		Description: strings.Repeat("d", domain.MaxTaskDescriptionLen+1),
	})
	assertErrInvalid(t, err, "Create")
	if len(uow.tasks.byID) != 0 {
		t.Fatalf("rejected create still stored %d task row(s)", len(uow.tasks.byID))
	}
}

func TestTasksCreate_ACTextOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)

	_, err := svc.Create(ctx, testAgent, TaskInput{
		Title: "fine title", ProjectID: testProjectID,
		ACs: []string{"short criterion", strings.Repeat("c", domain.MaxACTextLen+1)},
	})
	assertErrInvalid(t, err, "Create")
	if len(uow.tasks.byID) != 0 {
		t.Fatalf("rejected create still stored %d task row(s)", len(uow.tasks.byID))
	}
}

func TestTasksCreate_AtCapAccepted(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)

	task, err := svc.Create(ctx, testAgent, TaskInput{
		Title:       strings.Repeat("t", domain.MaxTaskTitleLen),
		Description: strings.Repeat("d", domain.MaxTaskDescriptionLen),
		ProjectID:   testProjectID,
		ACs:         []string{strings.Repeat("c", domain.MaxACTextLen)},
	})
	if err != nil {
		t.Fatalf("Create at caps: %v (at-cap values must be accepted)", err)
	}
	stored, ok := uow.tasks.byID[task.ID]
	if !ok || stored.Title != task.Title || stored.Description != task.Description {
		t.Fatalf("stored row mismatch: present=%v", ok)
	}
	gotACs, err := uow.acs.ListByTask(ctx, task.ID)
	if err != nil || len(gotACs) != 1 || gotACs[0].Text != strings.Repeat("c", domain.MaxACTextLen) {
		t.Fatalf("stored AC mismatch: %+v (err %v)", gotACs, err)
	}
}

// --- Tasks.Update -------------------------------------------------------------

func TestTasksUpdate_TitleAndDescriptionOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, svc)
	baseEvents := len(uow.events.events)

	longTitle := strings.Repeat("t", domain.MaxTaskTitleLen+1)
	updated, err := svc.Update(ctx, testAgent, seed.ID, TaskUpdateInput{Title: &longTitle})
	assertErrInvalid(t, err, "Update(title)")
	if updated.Title == longTitle {
		t.Fatal("update returned the over-cap title as if stored")
	}

	longDesc := strings.Repeat("d", domain.MaxTaskDescriptionLen+1)
	_, err = svc.Update(ctx, testAgent, seed.ID, TaskUpdateInput{Description: &longDesc})
	assertErrInvalid(t, err, "Update(description)")

	stored, ok := uow.tasks.byID[seed.ID]
	if !ok || stored.Title != seed.Title || stored.Description != seed.Description || stored.Version != seed.Version {
		t.Fatalf("row changed despite rejected updates: %+v", stored)
	}
	if len(uow.events.events) != baseEvents {
		t.Fatalf("rejected updates still appended events: %d → %d", baseEvents, len(uow.events.events))
	}
}

// TestTasksUpdate_LegacyOversizedRowStaysMutable pins the write-path-only
// contract end-to-end: a row stored over the caps (simulated here by poking
// the fake directly) is still readable and updatable when the update leaves
// those fields untouched — AND it can even be SHRUNK to an in-bound value.
func TestTasksUpdate_LegacyOversizedRowStaysMutable(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, svc)

	stored := uow.tasks.byID[seed.ID] // simulate a pre-cap legacy row
	stored.Title = strings.Repeat("t", domain.MaxTaskTitleLen*2)
	stored.Description = strings.Repeat("d", domain.MaxTaskDescriptionLen+1)
	uow.tasks.byID[seed.ID] = stored

	// Unrelated field: must succeed even though the row carries legacy
	// over-cap values.
	prio := domain.Priority(4)
	got, err := svc.Update(ctx, testAgent, seed.ID, TaskUpdateInput{Priority: &prio})
	if err != nil {
		t.Fatalf("Update(priority) on legacy oversized row = %v, want success", err)
	}
	if got.Title != stored.Title || got.Description != stored.Description {
		t.Fatal("untouched legacy values were rewritten by an unrelated update")
	}

	// Shrinking the legacy over-cap description to an in-bound value is a real
	// write and must succeed.
	shrunk := "now short"
	got, err = svc.Update(ctx, testAgent, got.ID, TaskUpdateInput{Description: &shrunk})
	if err != nil {
		t.Fatalf("Update(description→short) on legacy row = %v, want success", err)
	}
	if got.Description != shrunk {
		t.Fatalf("description = %q, want %q", got.Description, shrunk)
	}

	// But an INBOUND over-cap value is still rejected, even against a legacy row.
	longTitle := strings.Repeat("t", domain.MaxTaskTitleLen+1)
	_, err = svc.Update(ctx, testAgent, got.ID, TaskUpdateInput{Title: &longTitle})
	assertErrInvalid(t, err, "Update(over-cap title on legacy row)")
}

// --- Tasks.UpdateNotes ---------------------------------------------------------

func TestTasksUpdateNotes_OverCapRejectedAndAtCapAccepted(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, svc)
	baseEvents := len(uow.events.events)

	_, err := svc.UpdateNotes(ctx, testAgent, seed.ID, strings.Repeat("n", domain.MaxTaskNotesLen+1), 0)
	assertErrInvalid(t, err, "UpdateNotes(over cap)")
	stored, ok := uow.tasks.byID[seed.ID]
	if !ok || stored.Notes != "" || stored.Version != seed.Version {
		t.Fatalf("row changed despite rejected notes update: %+v", stored)
	}

	got, err := svc.UpdateNotes(ctx, testAgent, seed.ID, strings.Repeat("n", domain.MaxTaskNotesLen), 0)
	if err != nil {
		t.Fatalf("UpdateNotes(at cap) = %v, want success", err)
	}
	if got.Notes == "" || len(got.Notes) != domain.MaxTaskNotesLen {
		t.Fatalf("stored notes length = %d, want exactly the cap", len(got.Notes))
	}
	if len(uow.events.events) != baseEvents+1 {
		t.Fatalf("events = %d, want exactly one noted event after the accepted write", len(uow.events.events))
	}

	// A rejected over-cap blob leaves no trace: a later in-bound write still
	// sees the pre-rejection state as its no-op/no-baseline comparison.
	if _, err := svc.UpdateNotes(ctx, testAgent, seed.ID, "fresh short notes", 0); err != nil {
		t.Fatalf("UpdateNotes(short) = %v", err)
	}
}

// --- Projects ------------------------------------------------------------------

func TestProjectsCreate_DescriptionOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, fp, fe := newProjectTestEnv(t)

	_, err := svc.Create(ctx, testAgent, "Bound project", strings.Repeat("d", domain.MaxProjectDescriptionLen+1))
	assertErrInvalid(t, err, "Projects.Create")
	if len(fp.byID) != 0 {
		t.Fatalf("rejected create still stored %d project(s)", len(fp.byID))
	}
	if len(fe.events) != 0 {
		t.Fatalf("rejected create still appended %d event(s)", len(fe.events))
	}

	p, err := svc.Create(ctx, testAgent, "Bound project", strings.Repeat("d", domain.MaxProjectDescriptionLen))
	if err != nil {
		t.Fatalf("Projects.Create at cap = %v, want success", err)
	}
	if len(p.Description) != domain.MaxProjectDescriptionLen {
		t.Fatalf("stored description length = %d, want exactly the cap", len(p.Description))
	}
}

func TestProjectsUpdate_DescriptionOverCapRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, fp, fe := newProjectTestEnv(t)
	p, err := svc.Create(ctx, testAgent, "Bound project", "short doc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	baseEvents := len(fe.events)

	longDesc := strings.Repeat("d", domain.MaxProjectDescriptionLen+1)
	_, err = svc.Update(ctx, testAgent, p.ID, ProjectUpdate{Description: &longDesc})
	assertErrInvalid(t, err, "Projects.Update")
	stored, ok := fp.byID[p.ID]
	if !ok || stored.Description != "short doc" {
		t.Fatalf("project changed despite rejected update: %+v", stored)
	}
	if len(fe.events) != baseEvents {
		t.Fatal("rejected update still appended an event")
	}

	// Legacy tolerance: a pre-cap row stays mutable on unrelated updates.
	stored.Description = strings.Repeat("d", domain.MaxProjectDescriptionLen+1)
	fp.byID[p.ID] = stored
	name := "Renamed"
	if _, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{Name: &name}); err != nil {
		t.Fatalf("Projects.Update(name) on legacy oversized row = %v, want success", err)
	}
}

// --- Comments ------------------------------------------------------------------

func TestCommentsAdd_BodyOverCapRejectedAndAtCapAccepted(t *testing.T) {
	ctx := context.Background()
	tasksSvc, comments, _, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, tasksSvc)

	_, err := comments.Add(ctx, testAgent, seed.ID, domain.CommentProgress, strings.Repeat("b", domain.MaxCommentBodyLen+1))
	assertErrInvalid(t, err, "Comments.Add(over cap)")
	if got := len(uow.comments.byTask[seed.ID]); got != 0 {
		t.Fatalf("rejected comment still stored %d comment(s)", got)
	}
	if len(uow.events.events) != 1 { // only the seed create event exists
		t.Fatalf("events = %d, want 1 (the seed create)", len(uow.events.events))
	}

	added, err := comments.Add(ctx, testAgent, seed.ID, domain.CommentProgress, strings.Repeat("b", domain.MaxCommentBodyLen))
	if err != nil {
		t.Fatalf("Comments.Add at cap = %v, want success", err)
	}
	if len(added.Body) != domain.MaxCommentBodyLen {
		t.Fatalf("stored body length = %d, want exactly the cap", len(added.Body))
	}
}

// --- Acceptance criteria ---------------------------------------------------------

func TestAcceptanceAddAC_TextOverCapRejected(t *testing.T) {
	ctx := context.Background()
	tasksSvc, _, acs, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, tasksSvc)

	_, err := acs.AddAC(ctx, testAgent, seed.ID, strings.Repeat("c", domain.MaxACTextLen+1))
	assertErrInvalid(t, err, "AddAC")
	if got := len(uow.acs.byTask[seed.ID]); got != 0 {
		t.Fatalf("rejected AC still stored %d row(s)", got)
	}

	added, err := acs.AddAC(ctx, testAgent, seed.ID, strings.Repeat("c", domain.MaxACTextLen))
	if err != nil {
		t.Fatalf("AddAC at cap = %v, want success", err)
	}
	if len(added.Text) != domain.MaxACTextLen {
		t.Fatalf("stored text length = %d, want exactly the cap", len(added.Text))
	}
}

func TestAcceptanceEditACText_OverCapRejected(t *testing.T) {
	ctx := context.Background()
	tasksSvc, _, acs, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, tasksSvc)
	if _, err := acs.AddAC(ctx, testAgent, seed.ID, "first criterion"); err != nil {
		t.Fatalf("AddAC: %v", err)
	}
	baseEvents := len(uow.events.events)

	_, err := acs.EditACText(ctx, testAgent, seed.ID, 1, strings.Repeat("c", domain.MaxACTextLen+1))
	assertErrInvalid(t, err, "EditACText")
	gotACs, _ := uow.acs.ListByTask(ctx, seed.ID)
	if len(gotACs) != 1 || gotACs[0].Text != "first criterion" {
		t.Fatalf("row changed despite rejected reword: %+v", gotACs)
	}
	if len(uow.events.events) != baseEvents {
		t.Fatal("rejected reword still appended an event")
	}
}

func TestAcceptanceToggleAC_NoteOverCapRejectedAndAtCapAccepted(t *testing.T) {
	ctx := context.Background()
	tasksSvc, _, acs, _, uow := newBoundsCollabEnv(t)
	seed := seedBoundTask(t, tasksSvc)
	if _, err := acs.AddAC(ctx, testAgent, seed.ID, "first criterion"); err != nil {
		t.Fatalf("AddAC: %v", err)
	}

	_, err := acs.ToggleAC(ctx, testAgent, seed.ID, 1, boolPtr(true), strings.Repeat("n", domain.MaxACNoteLen+1))
	assertErrInvalid(t, err, "ToggleAC(over-cap note)")
	gotACs, _ := uow.acs.ListByTask(ctx, seed.ID)
	if len(gotACs) != 1 || gotACs[0].Done || gotACs[0].Note != "" {
		t.Fatalf("row changed despite rejected toggle: %+v", gotACs)
	}

	got, err := acs.ToggleAC(ctx, testAgent, seed.ID, 1, boolPtr(true), strings.Repeat("n", domain.MaxACNoteLen))
	if err != nil {
		t.Fatalf("ToggleAC at note cap = %v, want success", err)
	}
	if len(got.Note) != domain.MaxACNoteLen || !got.Done || got.CompletedBy == nil {
		t.Fatalf("stored note/criterion mismatch: %+v", got)
	}
}
