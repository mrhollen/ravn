package application

// collab_test.go: unit tests for the slice-2D1 collaboration/evidence write
// use cases — typed comments, replace-on-write notes, AC toggle, artifact
// attach — over fakeUoW + fakes + FakeClock. The focus is event payloads and
// the version-bump policy: child-row appends (comments/AC/artifacts) must NOT
// touch the parent task row's version or updated_at; UpdateNotes IS a task-row
// mutation and must bump version +1. The round-trip proof over real SQLite
// lives in internal/infrastructure/sqlite/uow_collab_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// seedTaskWithACs creates a task through the Tasks use case carrying two
// acceptance criteria, so collab tests can address seq 1 and 2.
func seedTaskWithACs(t *testing.T, svc *Tasks) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), testAgent, TaskInput{
		Title: "Collaboration target", ProjectID: testProjectID,
		ACs: []string{"First check", "Second check"},
	})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	return task
}

// eventsOfKind filters the fake event store down to one kind.
func eventsOfKind(uow *fakeUoW, kind domain.EventKind) []domain.Event {
	out := make([]domain.Event, 0)
	for _, e := range uow.events.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// --- comments -----------------------------------------------------------------

func TestCommentsAdd_AllKindsStoreCommentAndAppendEventWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Comment me", 1, nil)
	comments := NewComments(uow, fc, &fakeIDGen{})

	allKinds := []domain.CommentKind{
		domain.CommentProgress, domain.CommentQuestion, domain.CommentDecision,
		domain.CommentHandoff, domain.CommentNote,
	}
	for _, kind := range allKinds {
		fc.SetNow(fc.now.Add(time.Minute)) // distinct created_at per comment
		got, err := comments.Add(ctx, testAgent, task.ID, kind, "body "+string(kind))
		if err != nil {
			t.Fatalf("Add kind %q: %v", kind, err)
		}
		if got.TaskID != task.ID || got.Kind != kind || got.Actor != testAgent || got.ID == "" {
			t.Errorf("stored comment = %+v, want kind %q on task %s by agent-a", got, kind, task.ID)
		}
		if !got.CreatedAt.Equal(fc.now) {
			t.Errorf("created_at = %s, want the clock instant %s", got.CreatedAt, fc.now)
		}

		// Child-row rule: the parent task row must be byte-for-byte untouched.
		fresh, gerr := svc.Get(ctx, task.ID)
		if gerr != nil {
			t.Fatalf("Get after Add kind %q: %v", kind, gerr)
		}
		if fresh.Version != task.Version || !fresh.UpdatedAt.Equal(task.UpdatedAt) {
			t.Errorf("after comment %q: task version = %d (want %d), updated_at = %s (want %s) — child-row appends must not bump the parent",
				kind, fresh.Version, task.Version, fresh.UpdatedAt, task.UpdatedAt)
		}
	}

	evs := eventsOfKind(uow, domain.EventCommented)
	if len(evs) != len(allKinds) {
		t.Fatalf("commented events = %d, want exactly one per comment (%d)", len(evs), len(allKinds))
	}
	for i, ev := range evs {
		if ev.Actor != testAgent || ev.TaskID == nil || *ev.TaskID != task.ID {
			t.Errorf("event[%d] = (actor %+v task %v), want agent-a on %s", i, ev.Actor, ev.TaskID, task.ID)
		}
		if ev.ProjectID == nil || *ev.ProjectID != testProjectID {
			t.Errorf("event[%d].ProjectID = %v, want %s", i, ev.ProjectID, testProjectID)
		}
		if ev.Before != "" {
			t.Errorf("event[%d].Before = %q, want empty (append has no before-state)", i, ev.Before)
		}
		for _, frag := range []string{`"kind":"` + string(allKinds[i]) + `"`, `"body":"` + "body " + string(allKinds[i]) + `"`} {
			if !strings.Contains(ev.After, frag) {
				t.Errorf("event[%d].After %q missing %s", i, ev.After, frag)
			}
		}
	}
}

func TestCommentsAdd_Validation(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Validate me", 0, nil)
	comments := NewComments(uow, fc, &fakeIDGen{})

	if _, err := comments.Add(ctx, testAgent, task.ID, domain.CommentKind("chit-chat"), "x"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown kind → err = %v, want ErrInvalid", err)
	}
	if _, err := comments.Add(ctx, testAgent, task.ID, domain.CommentProgress, "   "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("whitespace body → err = %v, want ErrInvalid", err)
	}
	if _, err := comments.Add(ctx, testAgent, domain.ID("no-such-task"), domain.CommentProgress, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}

	if len(uow.comments.byTask[task.ID]) != 0 || len(uow.events.events) != 1 { // only the create event
		t.Fatalf("failed adds wrote state: comments=%d events=%d", len(uow.comments.byTask[task.ID]), len(uow.events.events))
	}
}

func TestCommentsAdd_EventBodyIsTruncated(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Long comment", 0, nil)
	comments := NewComments(uow, fc, &fakeIDGen{})

	body := strings.Repeat("x", eventBodyMax+50)
	got, err := comments.Add(ctx, testAgent, task.ID, domain.CommentNote, body)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got.Body != body { // the ROW keeps the full markdown…
		t.Errorf("stored body len = %d, want the full %d runes", len([]rune(got.Body)), len([]rune(body)))
	}
	evs := eventsOfKind(uow, domain.EventCommented)
	if len(evs) != 1 {
		t.Fatalf("commented events = %d, want 1", len(evs))
	}
	if strings.Contains(evs[0].After, body) { // …while the event payload is truncated
		t.Errorf("event After carries the full body — it must be capped at %d runes", eventBodyMax)
	}
	wantTrunc := string([]rune(body)[:eventBodyMax]) + "…"
	if !strings.Contains(evs[0].After, `"body":"`+wantTrunc+`"`) {
		t.Errorf("event After = %q, want body truncated to %q…", evs[0].After, wantTrunc)
	}
}

// --- notes (replace-on-write) ---------------------------------------------------

func TestTasksUpdateNotes_ReplacesBumpAndEmitsNoted(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Note me", 0, nil)
	baseVersion := task.Version

	fc.SetNow(fc.now.Add(10 * time.Minute)) // distinct from the create instant
	notedAt := fc.now
	got, err := svc.UpdateNotes(ctx, testAgent, task.ID, "# handoff notes\nreplace the WHOLE blob", 0)
	if err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	if got.Notes != "# handoff notes\nreplace the WHOLE blob" {
		t.Errorf("notes = %q, want the full replacement", got.Notes)
	}
	if got.Version != baseVersion+1 {
		t.Errorf("version = %d, want %d (notes IS a task-row mutation)", got.Version, baseVersion+1)
	}
	if !got.UpdatedAt.Equal(notedAt) {
		t.Errorf("updated_at = %s, want the write instant %s", got.UpdatedAt, notedAt)
	}

	evs := eventsOfKind(uow, domain.EventNoted)
	if len(evs) != 1 {
		t.Fatalf("noted events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.Kind != domain.EventNoted || ev.Actor != testAgent || !ev.At.Equal(notedAt) {
		t.Errorf("event = (kind %q actor %+v at %s), want noted by agent-a at %s", ev.Kind, ev.Actor, ev.At, notedAt)
	}
	if ev.TaskID == nil || *ev.TaskID != task.ID || ev.ProjectID == nil || *ev.ProjectID != testProjectID {
		t.Errorf("event ids = (project %v task %v), want %s on %s", ev.ProjectID, ev.TaskID, testProjectID, task.ID)
	}
	if !strings.Contains(ev.Before, `"notes":""`) {
		t.Errorf("Before %q must carry the old blob (empty)", ev.Before)
	}
	// Raw literal: json.Marshal escapes the in-string newline as backslash-n.
	wantAfter := `{"notes":"# handoff notes\nreplace the WHOLE blob"}`
	if ev.After != wantAfter {
		t.Errorf("After = %q, want exactly %s (the full replacement blob)", ev.After, wantAfter)
	}
}

func TestTasksUpdateNotes_NoOpDoesNotBumpOrEmit(t *testing.T) {
	ctx := context.Background()
	svc, _, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Same notes", 0, nil)

	got, err := svc.UpdateNotes(ctx, testAgent, task.ID, "", 0) // the zero-value blob again (unguarded)
	if err != nil {
		t.Fatalf("UpdateNotes: %v", err)
	}
	if got.Version != task.Version || !got.UpdatedAt.Equal(task.UpdatedAt) {
		t.Errorf("no-op replace bumped state: version %d (want %d), updated_at %s (want %s)",
			got.Version, task.Version, got.UpdatedAt, task.UpdatedAt)
	}
	if evs := eventsOfKind(uow, domain.EventNoted); len(evs) != 0 {
		t.Errorf("no-op replace emitted %d noted events, want 0", len(evs))
	}
}

func TestTasksUpdateNotes_UnknownTask(t *testing.T) {
	svc, _, _ := newTaskTestEnv(t)
	if _, err := svc.UpdateNotes(context.Background(), testAgent, domain.ID("no-such-task"), "x", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}
}

// --- acceptance criteria -------------------------------------------------------

func TestAcceptanceToggleAC_CheckStampsWhoWhenAndEmitsEventWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	baseVersion, baseUpdated := task.Version, task.UpdatedAt
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	fc.SetNow(fc.now.Add(2 * time.Minute))
	doneAt := fc.now
	got, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, boolPtr(true), "verified in CI")
	if err != nil {
		t.Fatalf("ToggleAC check: %v", err)
	}
	if !got.Done || got.Seq != 1 || got.Note != "verified in CI" || got.ID == "" {
		t.Errorf("fresh AC = %+v, want seq 1 done with note", got)
	}
	if got.CompletedBy == nil || *got.CompletedBy != testAgent {
		t.Errorf("completed_by = %v, want agent-a", got.CompletedBy)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(doneAt) {
		t.Errorf("completed_at = %v, want %s", got.CompletedAt, doneAt)
	}

	// Child-row rule: the parent task row is untouched.
	fresh, gerr := svc.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get after toggle: %v", gerr)
	}
	if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC toggle: version = %d (want %d), updated_at = %s (want %s)",
			fresh.Version, baseVersion, fresh.UpdatedAt, baseUpdated)
	}

	evs := eventsOfKind(uow, domain.EventACToggled)
	if len(evs) != 1 {
		t.Fatalf("ac-toggled events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.Actor != testAgent || ev.TaskID == nil || *ev.TaskID != task.ID {
		t.Errorf("event = (actor %+v task %v), want agent-a on %s", ev.Actor, ev.TaskID, task.ID)
	}
	if !strings.Contains(ev.Before, `"seq":1`) || !strings.Contains(ev.Before, `"done":false`) {
		t.Errorf("Before %q must carry seq 1 and the old done=false", ev.Before)
	}
	if !strings.Contains(ev.After, `"seq":1`) || !strings.Contains(ev.After, `"done":true`) {
		t.Errorf("After %q must carry seq 1 and the new done=true", ev.After)
	}
}

func TestAcceptanceToggleAC_UncheckClearsCompletionFields(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	if _, err := acs.ToggleAC(ctx, testAgent, task.ID, 2, boolPtr(true), "first pass"); err != nil {
		t.Fatalf("check seq 2: %v", err)
	}
	fc.SetNow(fc.now.Add(5 * time.Minute))
	got, err := acs.ToggleAC(ctx, testAgent, task.ID, 2, boolPtr(false), "should be ignored")
	if err != nil {
		t.Fatalf("uncheck seq 2: %v", err)
	}
	if got.Done {
		t.Errorf("done = true after uncheck, want false")
	}
	if got.Note != "" || got.CompletedBy != nil || got.CompletedAt != nil {
		t.Errorf("completion fields not cleared on uncheck: note %q by %v at %v",
			got.Note, got.CompletedBy, got.CompletedAt)
	}

	evs := eventsOfKind(uow, domain.EventACToggled)
	if len(evs) != 2 {
		t.Fatalf("ac-toggled events = %d, want 2 (check + uncheck)", len(evs))
	}
	last := evs[len(evs)-1]
	if !strings.Contains(last.Before, `"done":true`) || !strings.Contains(last.After, `"done":false`) {
		t.Errorf("uncheck event = (%q → %q), want done true→false", last.Before, last.After)
	}
}

// TestAcceptanceToggleAC_NoteOnlyPreservesDoneAndCompletionFields pins the
// nil-done contract: a note-only toggle (done == nil) keeps the current done
// state AND its completion fields, rewrites only the note, and emits one
// `ac-toggled` event with identical before/after done. Repeating it with the
// SAME note is a no-op (no second write, no second event).
func TestAcceptanceToggleAC_NoteOnlyPreservesDoneAndCompletionFields(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	first, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, boolPtr(true), "first pass")
	if err != nil {
		t.Fatalf("check seq 1: %v", err)
	}
	if first.CompletedBy == nil || !first.CompletedAt.Equal(fc.now) {
		t.Fatalf("checked AC = %+v, want completer stamped at the clock instant", first)
	}
	completedAt := fc.now

	fc.SetNow(fc.now.Add(7 * time.Minute)) // a re-stamp would move completed_at to this instant

	got, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, nil, "re-verified later")
	if err != nil {
		t.Fatalf("note-only ToggleAC: %v", err)
	}
	if !got.Done || got.Note != "re-verified later" {
		t.Errorf("note-only AC = %+v, want done PRESERVED with the updated note", got)
	}
	if got.CompletedBy == nil || *got.CompletedBy != testAgent {
		t.Errorf("completed_by = %v, want agent-a (unchanged)", got.CompletedBy)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completedAt) {
		t.Errorf("completed_at = %v, want %s (PRESERVED — not re-stamped to the later instant)", got.CompletedAt, completedAt)
	}

	evs := eventsOfKind(uow, domain.EventACToggled)
	if len(evs) != 2 {
		t.Fatalf("ac-toggled events = %d, want 2 (check + note-only)", len(evs))
	}
	last := evs[len(evs)-1]
	if !strings.Contains(last.Before, `"done":true`) || !strings.Contains(last.After, `"done":true`) {
		t.Errorf("note-only event = (%q → %q), want identical done before/after", last.Before, last.After)
	}

	// Identical note + nil done: no-op — row unchanged, NO additional event.
	same, err := acs.ToggleAC(ctx, testAgent, task.ID, 1, nil, "re-verified later")
	if err != nil {
		t.Fatalf("repeat note-only ToggleAC: %v", err)
	}
	if same.Note != "re-verified later" || !same.Done {
		t.Errorf("no-op AC = %+v, want the stored row returned unchanged", same)
	}
	if evs := eventsOfKind(uow, domain.EventACToggled); len(evs) != 2 {
		t.Errorf("events grew to %d after an identical-note no-op, want still 2", len(evs))
	}

	// Note-only on an UN-DONE criterion keeps it open (nil preserves either way).
	open, err := acs.ToggleAC(ctx, testAgent, task.ID, 2, nil, "still pending")
	if err != nil {
		t.Fatalf("note-only ToggleAC seq 2: %v", err)
	}
	if open.Done || open.Note != "still pending" {
		t.Errorf("seq 2 = %+v, want still not done with the updated note", open)
	}
}

func TestAcceptanceToggleAC_UnknownSeqOrTask(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	if _, err := acs.ToggleAC(ctx, testAgent, task.ID, 99, boolPtr(true), ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown seq → err = %v, want ErrNotFound", err)
	}
	if _, err := acs.ToggleAC(ctx, testAgent, domain.ID("no-such-task"), 1, boolPtr(true), ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}
	if evs := eventsOfKind(uow, domain.EventACToggled); len(evs) != 0 {
		t.Errorf("failed toggles emitted %d ac-toggled events, want 0", len(evs))
	}
}

// --- acceptance-criteria editing (post-create) --------------------------------

func TestAcceptanceAddAC_AppendsAtNextSeqEmitsEventWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc) // seq 1 and 2 exist
	baseVersion, baseUpdated := task.Version, task.UpdatedAt
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	fc.SetNow(fc.now.Add(3 * time.Minute))
	got, err := acs.AddAC(ctx, testAgent, task.ID, "Third check")
	if err != nil {
		t.Fatalf("AddAC: %v", err)
	}
	if got.Seq != 3 || got.Text != "Third check" || got.TaskID != task.ID || got.Done || got.ID == "" {
		t.Errorf("fresh AC = %+v, want seq 3 not-done on %s with a minted ID", got, task.ID)
	}

	// Child-row rule: the parent task row is untouched.
	fresh, gerr := svc.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get after AddAC: %v", gerr)
	}
	if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC add: version = %d (want %d), updated_at = %s (want %s)",
			fresh.Version, baseVersion, fresh.UpdatedAt, baseUpdated)
	}

	evs := eventsOfKind(uow, domain.EventACAdded)
	if len(evs) != 1 {
		t.Fatalf("ac-added events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.Actor != testAgent || ev.TaskID == nil || *ev.TaskID != task.ID {
		t.Errorf("event = (actor %+v task %v), want agent-a on %s", ev.Actor, ev.TaskID, task.ID)
	}
	if ev.Before != "" {
		t.Errorf("Before %q must be empty (an append has no before-state)", ev.Before)
	}
	if !strings.Contains(ev.After, `"seq":3`) || !strings.Contains(ev.After, `"text":"Third check"`) {
		t.Errorf("After %q must carry seq 3 and the new text", ev.After)
	}
}

func TestAcceptanceAddAC_Validation(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	if _, err := acs.AddAC(ctx, testAgent, task.ID, "   "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("whitespace text → err = %v, want ErrInvalid", err)
	}
	if _, err := acs.AddAC(ctx, testAgent, domain.ID("no-such-task"), "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}

	rows, _ := uow.acs.ListByTask(ctx, task.ID)
	if len(rows) != 2 {
		t.Fatalf("failed add wrote state: AC rows = %d, want the original 2", len(rows))
	}
	if evs := eventsOfKind(uow, domain.EventACAdded); len(evs) != 0 {
		t.Errorf("failed adds emitted %d ac-added events, want 0", len(evs))
	}
}

func TestAcceptanceEditACText_UpdatesTextWithBeforeAfterWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	baseVersion, baseUpdated := task.Version, task.UpdatedAt
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	fc.SetNow(fc.now.Add(4 * time.Minute))
	got, err := acs.EditACText(ctx, testAgent, task.ID, 1, "First check (hardened)")
	if err != nil {
		t.Fatalf("EditACText: %v", err)
	}
	if got.Seq != 1 || got.Text != "First check (hardened)" {
		t.Errorf("fresh AC = %+v, want seq 1 reworded", got)
	}

	fresh, gerr := svc.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get after EditACText: %v", gerr)
	}
	if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC edit: version = %d (want %d), updated_at = %s (want %s)",
			fresh.Version, baseVersion, fresh.UpdatedAt, baseUpdated)
	}

	evs := eventsOfKind(uow, domain.EventACUpdated)
	if len(evs) != 1 {
		t.Fatalf("ac-updated events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if !strings.Contains(ev.Before, `"seq":1`) || !strings.Contains(ev.Before, `"text":"First check"`) {
		t.Errorf("Before %q must carry the OLD text", ev.Before)
	}
	if !strings.Contains(ev.After, `"seq":1`) || !strings.Contains(ev.After, `"text":"First check (hardened)"`) {
		t.Errorf("After %q must carry the NEW text", ev.After)
	}
}

func TestAcceptanceEditACText_IdenticalTextIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	got, err := acs.EditACText(ctx, testAgent, task.ID, 2, "Second check") // the stored text again
	if err != nil {
		t.Fatalf("EditACText no-op: %v", err)
	}
	if got.Seq != 2 || got.Text != "Second check" {
		t.Errorf("no-op edit returned %+v, want the unchanged row", got)
	}
	if evs := eventsOfKind(uow, domain.EventACUpdated); len(evs) != 0 {
		t.Errorf("identical reword emitted %d ac-updated events, want 0", len(evs))
	}
}

func TestAcceptanceEditACText_UnknownSeqOrTaskAndValidation(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	if _, err := acs.EditACText(ctx, testAgent, task.ID, 99, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown seq → err = %v, want ErrNotFound", err)
	}
	if _, err := acs.EditACText(ctx, testAgent, domain.ID("no-such-task"), 1, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}
	if _, err := acs.EditACText(ctx, testAgent, task.ID, 1, "   "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("whitespace text → err = %v, want ErrInvalid", err)
	}

	rows, _ := uow.acs.ListByTask(ctx, task.ID)
	if len(rows) != 2 || rows[0].Text != "First check" {
		t.Errorf("failed edits mutated state: %+v", rows)
	}
}

func TestAcceptanceRemoveAC_DeletesLeavesGapAndEmitsEventWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc) // seq 1 and 2 exist
	baseVersion, baseUpdated := task.Version, task.UpdatedAt
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	fc.SetNow(fc.now.Add(6 * time.Minute))
	if err := acs.RemoveAC(ctx, testAgent, task.ID, 1); err != nil {
		t.Fatalf("RemoveAC: %v", err)
	}

	rows, lerr := uow.acs.ListByTask(ctx, task.ID)
	if lerr != nil {
		t.Fatalf("ListByTask after remove: %v", lerr)
	}
	if len(rows) != 1 || rows[0].Seq != 2 || rows[0].Text != "Second check" {
		t.Errorf("remaining ACs = %+v, want [seq 2] KEEPS ITS SEQ (gaps stay)", rows)
	}

	fresh, gerr := svc.Get(ctx, task.ID)
	if gerr != nil {
		t.Fatalf("Get after RemoveAC: %v", gerr)
	}
	if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
		t.Errorf("after AC remove: version = %d (want %d), updated_at = %s (want %s)",
			fresh.Version, baseVersion, fresh.UpdatedAt, baseUpdated)
	}

	evs := eventsOfKind(uow, domain.EventACRemoved)
	if len(evs) != 1 {
		t.Fatalf("ac-removed events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.Actor != testAgent || ev.TaskID == nil || *ev.TaskID != task.ID {
		t.Errorf("event = (actor %+v task %v), want agent-a on %s", ev.Actor, ev.TaskID, task.ID)
	}
	if !strings.Contains(ev.Before, `"seq":1`) || !strings.Contains(ev.Before, `"text":"First check"`) {
		t.Errorf("Before %q must carry the DELETED seq + text", ev.Before)
	}
	if ev.After != "" {
		t.Errorf("After %q must be empty (the row is gone)", ev.After)
	}
}

func TestAcceptanceRemoveAC_UnknownSeqOrTask(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := seedTaskWithACs(t, svc)
	acs := NewAcceptance(uow, fc, &fakeIDGen{})

	if err := acs.RemoveAC(ctx, testAgent, task.ID, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown seq → err = %v, want ErrNotFound", err)
	}
	if err := acs.RemoveAC(ctx, testAgent, domain.ID("no-such-task"), 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}

	rows, _ := uow.acs.ListByTask(ctx, task.ID)
	if len(rows) != 2 {
		t.Errorf("failed removes wrote state: AC rows = %d, want the original 2", len(rows))
	}
	if evs := eventsOfKind(uow, domain.EventACRemoved); len(evs) != 0 {
		t.Errorf("failed removes emitted %d ac-removed events, want 0", len(evs))
	}
}

// --- artifacts -------------------------------------------------------------------

func TestArtifactsAttach_AllKindsStoreArtifactAndAppendEventWithoutBumpingTaskVersion(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Evidence me", 2, nil)
	baseVersion, baseUpdated := task.Version, task.UpdatedAt
	arts := NewArtifacts(uow, fc, &fakeIDGen{})

	allKinds := []domain.ArtifactKind{
		domain.ArtifactLog, domain.ArtifactScreenshot, domain.ArtifactCommandOutput,
		domain.ArtifactTrace, domain.ArtifactURL, domain.ArtifactOther,
	}
	for _, kind := range allKinds {
		fc.SetNow(fc.now.Add(time.Minute)) // distinct created_at per artifact
		got, err := arts.Attach(ctx, testAgent, task.ID, kind, "https://evidence.local/x", "run "+string(kind))
		if err != nil {
			t.Fatalf("Attach kind %q: %v", kind, err)
		}
		if got.TaskID != task.ID || got.Kind != kind || got.Ref != "https://evidence.local/x" || got.Actor != testAgent || got.ID == "" {
			t.Errorf("stored artifact = %+v, want kind %q on %s", got, kind, task.ID)
		}

		fresh, gerr := svc.Get(ctx, task.ID)
		if gerr != nil {
			t.Fatalf("Get after Attach kind %q: %v", kind, gerr)
		}
		if fresh.Version != baseVersion || !fresh.UpdatedAt.Equal(baseUpdated) {
			t.Errorf("after artifact %q: version = %d (want %d), updated_at = %s (want %s) — child-row appends must not bump the parent",
				kind, fresh.Version, baseVersion, fresh.UpdatedAt, baseUpdated)
		}
	}

	evs := eventsOfKind(uow, domain.EventArtifactAdded)
	if len(evs) != len(allKinds) {
		t.Fatalf("artifact-added events = %d, want exactly one per artifact (%d)", len(evs), len(allKinds))
	}
	for i, ev := range evs {
		if ev.Actor != testAgent || ev.TaskID == nil || *ev.TaskID != task.ID || ev.Before != "" {
			t.Errorf("event[%d] = (actor %+v task %v before %q), want agent-a on %s with empty before", i, ev.Actor, ev.TaskID, ev.Before, task.ID)
		}
		for _, frag := range []string{`"kind":"` + string(allKinds[i]) + `"`, `"ref":"https://evidence.local/x"`, `"summary":"run ` + string(allKinds[i]) + `"`} {
			if !strings.Contains(ev.After, frag) {
				t.Errorf("event[%d].After %q missing %s", i, ev.After, frag)
			}
		}
	}
}

func TestArtifactsAttach_Validation(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Validate artifact", 0, nil)
	arts := NewArtifacts(uow, fc, &fakeIDGen{})

	if _, err := arts.Attach(ctx, testAgent, task.ID, domain.ArtifactKind("gif"), "x", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown kind → err = %v, want ErrInvalid", err)
	}
	if _, err := arts.Attach(ctx, testAgent, task.ID, domain.ArtifactLog, "  ", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty ref → err = %v, want ErrInvalid", err)
	}
	if _, err := arts.Attach(ctx, testAgent, domain.ID("no-such-task"), domain.ArtifactLog, "x", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task → err = %v, want ErrNotFound", err)
	}

	if len(uow.artifacts.byTask[task.ID]) != 0 || len(uow.events.events) != 1 { // only the create event
		t.Fatalf("failed attaches wrote state: artifacts=%d events=%d", len(uow.artifacts.byTask[task.ID]), len(uow.events.events))
	}
}

func TestArtifactsAttach_OptionalSummary(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t)
	task := mustCreateTask(t, svc, "Bare artifact", 0, nil)
	arts := NewArtifacts(uow, fc, &fakeIDGen{})

	got, err := arts.Attach(ctx, testAgent, task.ID, domain.ArtifactTrace, "/tmp/trace.txt", "")
	if err != nil {
		t.Fatalf("Attach with empty summary: %v (summary is optional)", err)
	}
	if got.Summary != "" || got.Ref != "/tmp/trace.txt" {
		t.Errorf("artifact = %+v, want ref /tmp/trace.txt with empty summary", got)
	}
}
