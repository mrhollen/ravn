package application

// duplicates_test.go: slice 2D2b unit tests for FindDuplicateCandidates —
// project-scoped, open-tasks-only title similarity with deterministic ranking
// and the default/capped limit. Pure scoring-table coverage lives in
// TestTitleSimilarity_ScoringTable; the real-SQL round trip (List fetch +
// scoring) is covered by the SQLite integration test.

import (
	"context"
	"testing"

	"github.com/mrhollen/crowban/internal/domain"
)

func createTaskIn(t *testing.T, svc *Tasks, projectID domain.ID, title string) domain.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), testAgent, TaskInput{Title: title, ProjectID: projectID})
	if err != nil {
		t.Fatalf("Create %q in %s: %v", title, projectID, err)
	}
	return task
}

func TestFindDuplicateCandidates_SurfacesSimilarOpenTitles(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)

	auth := createTaskIn(t, svc, testProjectID, "Refactor auth")
	authExt := createTaskIn(t, svc, testProjectID, "Refactor authentication")

	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "Refactor auth", 0) // 0 = default limit (5)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates (%v), want the two refactor titles", len(got), titleList(t, got))
	}
	// Normalized-equal ranks first (score 3); the substring sibling follows (2).
	if got[0].ID != auth.ID || got[1].ID != authExt.ID {
		t.Errorf("candidate order = [%s, %s], want exact match then substring match",
			got[0].Title, got[1].Title)
	}
}

func TestFindDuplicateCandidates_ScopesToProjectAndCapsAtLimit(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t) // testProjectID pre-seeded in the fake project repo

	otherProj := domain.ID("proj-2")
	uow.projects.byID[otherProj] = domain.Project{ID: otherProj, Name: "Other", CreatedAt: fc.now, UpdatedAt: fc.now}

	auth := createTaskIn(t, svc, testProjectID, "Refactor auth")                       // exact (3)
	authExt := createTaskIn(t, svc, testProjectID, "Refactor authentication")          // substring (2)
	authFlow := createTaskIn(t, svc, testProjectID, "Refactor the authorization flow") // token overlap only (1)
	foreign := createTaskIn(t, svc, otherProj, "Refactor auth system")                 // different project: never returned

	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "Refactor auth", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates (%v), want the three in-project looks", len(got), titleList(t, got))
	}
	for i, c := range got {
		if c.ProjectID != testProjectID {
			t.Errorf("candidate[%d] %q is from project %s, want only %s", i, c.Title, c.ProjectID, testProjectID)
		}
		if c.ID == foreign.ID {
			t.Errorf("cross-project task %q leaked into the candidates", foreign.Title)
		}
	}
	wantOrder := []domain.ID{auth.ID, authExt.ID, authFlow.ID} // 3 > 2 > 1
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("candidate[%d] = %q, want %q (score-desc, id-asc order)", i, got[i].Title, titleOf(t, svc, want))
		}
	}

	capped, err := svc.FindDuplicateCandidates(ctx, testProjectID, "Refactor auth", 1)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates limit=1: %v", err)
	}
	if len(capped) != 1 || capped[0].ID != auth.ID {
		t.Errorf("capped = %v, want exactly the top candidate %q", titleList(t, capped), auth.Title)
	}
}

func TestFindDuplicateCandidates_IgnoresDoneAndArchived(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)

	finished := createTaskIn(t, svc, testProjectID, "Refactor auth")
	stillOpen := createTaskIn(t, svc, testProjectID, "Refactor authentication")
	shelved := createTaskIn(t, svc, testProjectID, "Refactor authorization")

	doingStatus := domain.StatusDoing
	if _, err := svc.Update(ctx, testAgent, finished.ID, TaskUpdateInput{Status: &doingStatus}); err != nil {
		t.Fatalf("move task to doing: %v", err)
	}
	doneStatus := domain.StatusDone
	if _, err := svc.Update(ctx, testAgent, finished.ID, TaskUpdateInput{Status: &doneStatus}); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	archive := true
	if _, err := svc.Update(ctx, testAgent, shelved.ID, TaskUpdateInput{Archived: &archive}); err != nil {
		t.Fatalf("archive task: %v", err)
	}

	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "refactor auth!", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	// "open" = not done AND not archived (ARCHITECTURE §4): only stillOpen may surface.
	if len(got) != 1 || got[0].ID != stillOpen.ID {
		t.Errorf("candidates = %v, want exactly the open %q", titleList(t, got), stillOpen.Title)
	}
}

func TestTitleSimilarity_ScoringTable(t *testing.T) {
	cases := []struct {
		name string
		q    string // raw query title (normalized inside)
		c    string // raw candidate title
		want int
	}{
		{"exact after normalization", " Refactor auth ", "refactor AUTH", 3},
		{"query contained in candidate", "refactor auth", "Refactor authentication", 2},
		{"candidate contains short query", "auth", "authentication", 2},
		{"reordered tokens defeat containment, token rule rescues", "refactor AUTH!", "AUTH refactor", 1},
		{"half the significant tokens shared", "update user profile page", "Update user settings", 1},
		{"no signal at all", "fix the login timeout", "update password hashing", 0},
		{"empty query", "", "anything at all", 0},
		{"stopword-only titles share no significant tokens", "and or the a", "the and or a", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := titleSimilarity(normalizeTitle(tc.q), normalizeTitle(tc.c),
				significantTokens(normalizeTitle(tc.q)), significantTokens(normalizeTitle(tc.c)))
			if got != tc.want {
				t.Errorf("titleSimilarity(%q, %q) = %d, want %d", tc.q, tc.c, got, tc.want)
			}
		})
	}
}

// --- tiny assertion helpers (local to this file) ---------------------------------

func titleList(t *testing.T, tasks []domain.Task) []string {
	t.Helper()
	out := make([]string, 0, len(tasks))
	for _, r := range tasks {
		out = append(out, r.Title)
	}
	return out
}

func titleOf(t *testing.T, svc *Tasks, id domain.ID) string {
	t.Helper()
	task, err := svc.Get(context.Background(), id)
	if err != nil {
		return "(missing " + string(id) + ")"
	}
	return task.Title
}
