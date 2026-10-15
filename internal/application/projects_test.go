package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/domain"
)

// projects_test.go: unit tests for the Projects use case over fakeUoW + fakes
// + FakeClock — no SQLite, no HTTP (the highest-value test layer per
// ARCHITECTURE §9).

const testStart = "2026-09-25T18:00:00Z"

func newProjectTestEnv(t *testing.T) (*Projects, *fakeClock, *fakeProjects, *fakeEvents) {
	t.Helper()
	at := mustTime(t, testStart)
	fc := newFakeClock(at)
	fp := newFakeProjects()
	fe := &fakeEvents{}
	svc := NewProjects(newFakeUoW(fp, fe), fc, &fakeIDGen{})
	return svc, fc, fp, fe
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UTC()
}

var testAgent = domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}

func TestProjectsCreate_MintsIDStampsTimestampsAppendsCreatedEvent(t *testing.T) {
	ctx := context.Background()
	svc, fc, fp, fe := newProjectTestEnv(t)
	at := fc.now

	p, err := svc.Create(ctx, testAgent, "  Demo Project  ", "onboarding doc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if p.ID == "" {
		t.Fatal("expected a minted ID, got zero value")
	}
	if p.CreatedAt != at || p.UpdatedAt != at {
		t.Fatalf("timestamps = (%s, %s), want both %s", p.CreatedAt, p.UpdatedAt, at)
	}
	if p.Name != "Demo Project" {
		t.Fatalf("name not trimmed: %q", p.Name)
	}
	stored, ok := fp.byID[p.ID]
	if !ok || stored != p {
		t.Fatalf("stored project mismatch: got %+v, want %+v (present=%v)", stored, p, ok)
	}

	if len(fe.events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(fe.events))
	}
	ev := fe.events[0]
	if ev.Kind != domain.EventCreated {
		t.Errorf("event kind = %q, want %q", ev.Kind, domain.EventCreated)
	}
	if ev.Actor != testAgent {
		t.Errorf("event actor = %+v, want %+v", ev.Actor, testAgent)
	}
	if ev.ProjectID == nil || *ev.ProjectID != p.ID {
		t.Errorf("event project id = %v, want %s", ev.ProjectID, p.ID)
	}
	if ev.TaskID != nil {
		t.Errorf("event task id = %v, want nil (project-level event)", ev.TaskID)
	}
	if !ev.At.Equal(at) {
		t.Errorf("event ts = %s, want %s", ev.At, at)
	}
	if ev.Before != "" || ev.After == "" {
		t.Errorf("expected empty Before and non-empty After, got (%q, %q)", ev.Before, ev.After)
	} else if !strings.Contains(ev.After, `"name":"Demo Project"`) {
		t.Errorf("After payload %q does not carry the new name", ev.After)
	}
}

func TestProjectsUpdate_AppendsUpdatedEventWithChangedFieldsOnly(t *testing.T) {
	ctx := context.Background()
	svc, fc, _, fe := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "Before Name", "old description")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fc.SetNow(fc.now.Add(5 * time.Minute)) // advance the clock so UpdatedAt visibly moves

	newName := "After Name"
	newDesc := "new description"
	updated, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{Name: &newName, Description: &newDesc})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if updated.Name != newName || updated.Description != newDesc {
		t.Fatalf("updated = %+v, want name %q desc %q", updated, newName, newDesc)
	}
	if !updated.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("CreatedAt changed: %s → %s", p.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt != fc.now {
		t.Errorf("UpdatedAt = %s, want freshly stamped %s", updated.UpdatedAt, fc.now)
	}

	if len(fe.events) != 2 {
		t.Fatalf("expected 2 events (created + updated), got %d", len(fe.events))
	}
	ev := fe.events[1]
	if ev.Kind != domain.EventUpdated {
		t.Errorf("event kind = %q, want %q", ev.Kind, domain.EventUpdated)
	}
	if ev.Actor != testAgent || !ev.At.Equal(fc.now) {
		t.Errorf("event (actor=%+v, at=%s), want (%+v, %s)", ev.Actor, ev.At, testAgent, fc.now)
	}
	if !strings.Contains(ev.Before, "Before Name") || strings.Contains(ev.Before, "After Name") {
		t.Errorf("Before payload %q should carry only the old values", ev.Before)
	}
	if !strings.Contains(ev.After, "After Name") || strings.Contains(ev.After, "old description") {
		t.Errorf("After payload %q should carry only the new values", ev.After)
	}
}

func TestProjectsUpdate_PartialChangeLeavesOtherFieldsAlone(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "Name", "keep me")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	newDesc := "replaced"
	updated, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{Description: &newDesc})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Name" || updated.Description != newDesc {
		t.Fatalf("partial update broke untouched fields: %+v", updated)
	}
}

func TestProjectsGet_ReturnsStoredProject(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "Get Me", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := svc.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != p {
		t.Fatalf("Get returned %+v, want %+v", got, p)
	}

	if _, err := svc.Get(ctx, domain.ID("does-not-exist")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown) err = %v, want domain.ErrNotFound", err)
	}
}

func TestProjectsList_ReturnsAllInCreationOrder(t *testing.T) {
	ctx := context.Background()
	svc, fc, _, _ := newProjectTestEnv(t)

	for _, name := range []string{"First", "Second", "Third"} {
		if _, err := svc.Create(ctx, testAgent, name, ""); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		fc.SetNow(fc.now.Add(time.Second)) // distinct timestamps → deterministic order
	}

	got, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"First", "Second", "Third"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d projects, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("List[%d].Name = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestProjectsCreate_EmptyNameReturnsErrInvalid(t *testing.T) {
	ctx := context.Background()
	svc, _, fp, fe := newProjectTestEnv(t)

	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := svc.Create(ctx, testAgent, name, ""); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Create(%q) err = %v, want wrapped domain.ErrInvalid", name, err)
		}
	}
	if len(fp.byID) != 0 || len(fe.events) != 0 {
		t.Fatalf("invalid input must write nothing: projects=%d events=%d", len(fp.byID), len(fe.events))
	}
}

func TestProjectsUpdate_EmptyNameOrNoFieldsReturnsErrInvalid(t *testing.T) {
	ctx := context.Background()
	svc, _, _, fe := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "Keep", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	badName := "  "
	if _, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{Name: &badName}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("Update(empty name) err = %v, want wrapped domain.ErrInvalid", err)
	}
	if _, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("Update(no fields) err = %v, want wrapped domain.ErrInvalid", err)
	}
	validName := "Renamed" // input must be valid for the lookup to run; then the unknown id surfaces
	if _, err := svc.Update(ctx, testAgent, domain.ID("unknown"), ProjectUpdate{Name: &validName}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update(unknown id) err = %v, want domain.ErrNotFound", err)
	}
	if len(fe.events) != 1 {
		t.Fatalf("failed updates must not emit events: got %d", len(fe.events))
	}
}

func TestProjectsUpdate_OpenCodeSettings(t *testing.T) {
	t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", "true")
	ctx := context.Background()
	svc, fc, fp, fe := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "OpenCode Project", "initial desc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	serverURL := "http://localhost:7777"
	dir := "/home/test/app"
	model := "llama.cpp/Qwen Nano"
	fc.SetNow(fc.now.Add(time.Minute))

	updated, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{
		OpenCodeServerURL: &serverURL,
		OpenCodeDirectory: &dir,
		OpenCodeModel:     &model,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.OpenCodeServerURL != serverURL || updated.OpenCodeDirectory != dir || updated.OpenCodeModel != model {
		t.Fatalf("updated OpenCode fields mismatch: got (%q, %q, %q), want (%q, %q, %q)", updated.OpenCodeServerURL, updated.OpenCodeDirectory, updated.OpenCodeModel, serverURL, dir, model)
	}
	if stored := fp.byID[p.ID]; stored.OpenCodeServerURL != serverURL || stored.OpenCodeDirectory != dir || stored.OpenCodeModel != model {
		t.Fatalf("stored OpenCode fields mismatch: got (%q, %q, %q)", stored.OpenCodeServerURL, stored.OpenCodeDirectory, stored.OpenCodeModel)
	}

	// Verify update event
	if len(fe.events) != 2 {
		t.Fatalf("events count = %d, want 2 (create + update)", len(fe.events))
	}
	ev := fe.events[1]
	if ev.Kind != domain.EventUpdated {
		t.Fatalf("event kind = %v, want %v", ev.Kind, domain.EventUpdated)
	}
	if !strings.Contains(ev.After, `"opencode_server_url":"http://localhost:7777"`) || !strings.Contains(ev.After, `"opencode_directory":"/home/test/app"`) || !strings.Contains(ev.After, `"opencode_model":"llama.cpp/Qwen Nano"`) {
		t.Fatalf("event after payload missing opencode fields: %s", ev.After)
	}
}

func TestProjectsUpdate_Archive(t *testing.T) {
	ctx := context.Background()
	svc, fc, fp, fe := newProjectTestEnv(t)

	p, err := svc.Create(ctx, testAgent, "To Archive", "some desc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	fc.SetNow(fc.now.Add(time.Minute))
	arch := true
	updated, err := svc.Update(ctx, testAgent, p.ID, ProjectUpdate{
		Archived: &arch,
	})
	if err != nil {
		t.Fatalf("Update archive: %v", err)
	}
	if !updated.Archived {
		t.Fatal("expected updated.Archived to be true")
	}
	if !fp.byID[p.ID].Archived {
		t.Fatal("expected stored.Archived to be true")
	}

	// Verify update event
	if len(fe.events) != 2 {
		t.Fatalf("events count = %d, want 2 (create + update)", len(fe.events))
	}
	ev := fe.events[1]
	if ev.Kind != domain.EventUpdated {
		t.Fatalf("event kind = %v, want %v", ev.Kind, domain.EventUpdated)
	}
	if !strings.Contains(ev.After, `"archived":"true"`) {
		t.Fatalf("event after payload missing archived field: %s", ev.After)
	}
}

func TestProjectsList_ArchivedFilter(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newProjectTestEnv(t)

	p1, err := svc.Create(ctx, testAgent, "Active Project", "")
	if err != nil {
		t.Fatalf("Create active: %v", err)
	}
	p2, err := svc.Create(ctx, testAgent, "Archived Project", "")
	if err != nil {
		t.Fatalf("Create to-be-archived: %v", err)
	}

	arch := true
	if _, err := svc.Update(ctx, testAgent, p2.ID, ProjectUpdate{Archived: &arch}); err != nil {
		t.Fatalf("Update archive: %v", err)
	}

	// Default list: open projects only
	openProjects, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if len(openProjects) != 1 || openProjects[0].ID != p1.ID {
		t.Fatalf("List default = %+v, want only p1", openProjects)
	}

	// Filter archived: true
	archivedProjects, err := svc.List(ctx, ProjectFilter{Archived: true})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archivedProjects) != 1 || archivedProjects[0].ID != p2.ID {
		t.Fatalf("List archived = %+v, want only p2", archivedProjects)
	}
}
