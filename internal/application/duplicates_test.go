package application

// duplicates_test.go: slice 2D2b unit tests for FindDuplicateCandidates —
// project-scoped, open-tasks-only title similarity with deterministic ranking
// and the default/capped limit. Pure scoring-table coverage lives in
// TestTitleSimilarity_ScoringTable; the real-SQL round trip (OpenTitles fetch +
// top-N hydration + scoring) is covered by the SQLite integration test.

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	// Normalized-equal ranks first (score 3); the token-overlap sibling
	// follows (score 1 — "auth" is NOT a whole token of "authentication", so
	// the old byte-containment score 2 no longer applies).
	if got[0].ID != auth.ID || got[1].ID != authExt.ID {
		t.Errorf("candidate order = [%s, %s], want exact match then token overlap",
			got[0].Title, got[1].Title)
	}
}

func TestFindDuplicateCandidates_ScopesToProjectAndCapsAtLimit(t *testing.T) {
	ctx := context.Background()
	svc, fc, uow := newTaskTestEnv(t) // testProjectID pre-seeded in the fake project repo

	otherProj := domain.ID("proj-2")
	uow.projects.byID[otherProj] = domain.Project{ID: otherProj, Name: "Other", CreatedAt: fc.now, UpdatedAt: fc.now}

	auth := createTaskIn(t, svc, testProjectID, "Refactor auth")                       // exact (3)
	authExt := createTaskIn(t, svc, testProjectID, "Refactor authentication")          // shared "refactor" (1 — "auth" is not a whole token of "authentication")
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
	// exact (3) first; the two score-1 rows tie-break by ID (creation order).
	wantOrder := []domain.ID{auth.ID, authExt.ID, authFlow.ID}
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

// TestTitleSimilarity_ScoringTable mirrors the domain scoring table (see
// TestTitleSimilarity in internal/domain/order_test.go): word-boundary
// whole-token containment replaces byte-substring containment, and the
// expanded stopword list keeps junk tokens out of the comparison.
func TestTitleSimilarity_ScoringTable(t *testing.T) {
	cases := []struct {
		name string
		q    string // raw query title (normalized inside)
		c    string // raw candidate title
		want int
	}{
		// --- 3: normalized-equal --------------------------------------------------
		{"exact after normalization", " Refactor auth ", "refactor AUTH", 3},

		// --- 2: whole-token containment on token SETS, either direction -----------
		{"query tokens all whole tokens of candidate", "auth fix", "fix auth bug", 2},
		{"candidate tokens all whole tokens of query", "debug the login module", "login", 2},
		{"reordered containment still contains", "ui polish", "polish ui for web", 2},

		// --- AC1: byte-substring false positives now score 0 -----------------------
		{"substring inside a word is not a match (ui/build suite)", "ui", "build suite", 0},
		{"substring inside a word is not a match (api/rapid response)", "api", "rapid response", 0},
		{"prefix of a longer word is not a match (auth/authentication)", "auth", "authentication", 0},
		{"inner substring is not a match (fix/hotfix deploy)", "fix", "hotfix deploy", 0},

		// --- 1: ≥ half of the query's significant tokens ----------------------------
		{"reordered equal token sets are not containment, token rule rescues", "refactor AUTH!", "AUTH refactor", 1},
		{"longer word shares one token — half threshold met", "refactor auth", "Refactor authentication", 1},
		{"half the significant tokens shared", "update user profile page", "Update user settings", 1},
		{"stopwords dropped before comparison, remaining sets equal → full overlap", "Fix of the auth bug", "fix auth bug", 1},

		// --- 0: no signal -------------------------------------------------------------
		{"no signal at all", "fix the login timeout", "update password hashing", 0},
		{"empty query", "", "anything at all", 0},
		{"stopword-only titles share no significant tokens", "of to the a and or but do we can if not all is are was were", "the and or a is are was with on in at for", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.TitleSimilarity(domain.NormalizeTitle(tc.q), domain.NormalizeTitle(tc.c),
				domain.SignificantTokens(domain.NormalizeTitle(tc.q)), domain.SignificantTokens(domain.NormalizeTitle(tc.c)))
			if got != tc.want {
				t.Errorf("titleSimilarity(%q, %q) = %d, want %d", tc.q, tc.c, got, tc.want)
			}
		})
	}
}

// TestFindDuplicateCandidates_WholeTokenBoundaryOnly is the app-level guard for
// AC1: raw byte substrings inside longer words must NOT surface as candidates,
// while whole-token containment still ranks first.
func TestFindDuplicateCandidates_WholeTokenBoundaryOnly(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)

	// Every candidate title below CONTAINS one of the query strings as a raw
	// byte substring of a longer word ("ui" in "build", "api" in "rapid",
	// "auth" in "author", "fix" in "hotfix") — none carries it as a whole
	// token, so none may be returned.
	createTaskIn(t, svc, testProjectID, "Build suite")       // 'ui' inside "build"
	createTaskIn(t, svc, testProjectID, "Rapid response")    // 'api' inside "rapid"
	createTaskIn(t, svc, testProjectID, "Author guidelines") // 'auth' inside "author"
	createTaskIn(t, svc, testProjectID, "Hotfix deploy")     // 'fix' inside "hotfix"

	for _, q := range []string{"ui", "api", "auth", "fix"} {
		got, err := svc.FindDuplicateCandidates(ctx, testProjectID, q, 0)
		if err != nil {
			t.Fatalf("FindDuplicateCandidates(%q): %v", q, err)
		}
		if len(got) != 0 {
			t.Errorf("query %q surfaced %v, want none (byte substrings are not matches)", q, titleList(t, got))
		}
	}

	// Whole-token containment still scores: "auth fix"'s tokens are all whole
	// tokens of "Fix auth bug", so it surfaces and ranks first.
	containing := createTaskIn(t, svc, testProjectID, "Fix auth bug")
	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "auth fix", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != containing.ID {
		t.Errorf("candidates = %v, want exactly the whole-token containment %q", titleList(t, got), containing.Title)
	}
}

// TestFindDuplicateCandidates_ScansWholeProject is the app-level guard for AC3:
// a duplicate sitting far beyond the old one-page (100-row) scan bound must be
// found — the scan covers ALL open project tasks.
func TestFindDuplicateCandidates_ScansWholeProject(t *testing.T) {
	ctx := context.Background()
	svc, fc, _ := newTaskTestEnv(t)

	const fillers = 210 // canonical order = created_at ASC: the duplicate lands at index 210
	for i := 0; i < fillers; i++ {
		createTaskIn(t, svc, testProjectID, fmt.Sprintf("Filler task %d", i)) // tokens {filler,task,N}: no query overlap
		fc.SetNow(fc.Now().Add(time.Second))                                  // distinct ascending CreatedAt → deterministic canonical order
	}
	dup := createTaskIn(t, svc, testProjectID, "Refactor the auth module")

	// Prove the duplicate sits beyond the first page: the old
	// one-page-bounded scan could never have seen it.
	page, total, err := svc.List(ctx, TaskFilter{ProjectID: testProjectID, Limit: 100})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if total != int64(fillers+1) || len(page) != 100 {
		t.Fatalf("total = %d, page = %d rows, want %d tasks with a full first page", total, len(page), fillers+1)
	}
	for _, row := range page {
		if row.ID == dup.ID {
			t.Fatalf("duplicate is inside the first 100 rows — the test fixture is wrong")
		}
	}

	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "Refactor the auth module", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != dup.ID {
		t.Errorf("candidates = %v, want the duplicate at canonical index %d (beyond the old scan bound)", titleList(t, got), fillers)
	}
}

// TestFindDuplicateCandidates_StopwordOnlyDifferencesDontRank guards AC2 at app
// level: a stopword-only title has no significant tokens and can never surface;
// titles that differ only in stopwords are compared on their real content
// tokens alone (they rank via the overlap rule, not via the junk words).
func TestFindDuplicateCandidates_StopwordOnlyDifferencesDontRank(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTaskTestEnv(t)

	createTaskIn(t, svc, testProjectID, "The of to we can but") // stopword-only: empty token set
	related := createTaskIn(t, svc, testProjectID, "Fix of the auth bug")

	// A stopword-only query has nothing to match on: no candidate, ever.
	got, err := svc.FindDuplicateCandidates(ctx, testProjectID, "the and or but", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates(stopwords): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("stopword-only query surfaced %v, want none", titleList(t, got))
	}

	// Stopwords-only difference between two real titles: "Fix of the auth bug"
	// and "fix auth bug" share the identical content-token set and rank.
	got, err = svc.FindDuplicateCandidates(ctx, testProjectID, "fix auth bug", 0)
	if err != nil {
		t.Fatalf("FindDuplicateCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != related.ID {
		t.Errorf("candidates = %v, want the stopword-differing title %q", titleList(t, got), related.Title)
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
