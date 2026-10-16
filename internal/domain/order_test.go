package domain

import (
	"sort"
	"testing"
	"time"
)

// TestCanonicalLess covers the canonical "what's next" ordering
// (ARCHITECTURE §2): priority DESC -> due_at ASC nulls-last -> created_at ASC.
func TestCanonicalLess(t *testing.T) {
	base := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	plus := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }

	mk := func(p Priority, due *time.Time, created time.Time) Task {
		return Task{Priority: p, DueAt: due, CreatedAt: created}
	}

	cases := []struct {
		name string
		a    Task
		b    Task
		want bool // want CanonicalLess(a, b)
	}{
		// --- priority DESC dominates everything else -------------------------
		{"higher priority first", mk(5, nil, plus(2)), mk(3, timePtr(base), base), true},
		{"lower priority never before higher", mk(3, timePtr(base), base), mk(5, nil, plus(2)), false},
		{"priority dominates later due AND later created", mk(4, nil, plus(9)), mk(0, timePtr(plus(-1)), plus(-8)), true},

		// --- due_at ASC within equal priority --------------------------------
		{"earlier due first", mk(3, timePtr(plus(1)), base), mk(3, timePtr(plus(2)), base), true},
		{"later due never before earlier", mk(3, timePtr(plus(2)), base), mk(3, timePtr(plus(1)), base), false},

		// --- nulls-last: nil/zero due dates sort AFTER non-nil ----------------
		{"nil due after non-nil", mk(3, nil, base), mk(3, timePtr(plus(1)), base), false},
		{"non-nil due before nil", mk(3, timePtr(plus(1)), base), mk(3, nil, base), true},
		{"zero-value task (nil due) sorts last", Task{Priority: 3}, mk(3, timePtr(base), base), false},
		{"non-nil due before zero-value task", mk(3, timePtr(base), base), Task{Priority: 3}, true},

		// --- created_at ASC as final tiebreak --------------------------------
		{"earlier created first on full key tie", mk(3, timePtr(plus(1)), base), mk(3, timePtr(plus(1)), plus(1)), true},
		{"later created never before earlier on full key tie", mk(3, timePtr(plus(1)), plus(1)), mk(3, timePtr(plus(1)), base), false},
		{"equal due instants in different locations count as tied",
			mk(3, timePtr(base), base),
			mk(3, timePtr(time.Date(2026, 10, 16, 9, 0, 0, 0, time.FixedZone("UTC+1", 3600))), plus(1)),
			true},

		// --- strict weak ordering: equal tasks are neither before each other --
		{"identical tasks are not less (a<b)", mk(2, nil, base), mk(2, nil, base), false},
		{"identical tasks are not less (b<a)", mk(2, nil, base), mk(2, nil, base), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalLess(tc.a, tc.b); got != tc.want {
				t.Errorf("CanonicalLess(a, b) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCanonicalLess_SortsMixedBag sorts a mixed bag the same way fakeTasks.List
// does (sort.Slice over CanonicalLess) and asserts the full canonical
// sequence: priority DESC, due_at ASC nulls-last, created_at ASC.
func TestCanonicalLess_SortsMixedBag(t *testing.T) {
	base := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	plus := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }

	tasks := []Task{
		{ID: "p5-nodue", Priority: 5, CreatedAt: plus(3)},
		{ID: "p0-due-soonest", Priority: 0, DueAt: timePtr(plus(-5)), CreatedAt: plus(-6)},
		{ID: "p3-due-tie-newer", Priority: 3, DueAt: timePtr(plus(1)), CreatedAt: plus(1)},
		{ID: "p2-due-mid", Priority: 2, DueAt: timePtr(plus(2)), CreatedAt: plus(5)},
		{ID: "p3-nodue", Priority: 3, CreatedAt: plus(4)},
		{ID: "p3-due-early", Priority: 3, DueAt: timePtr(plus(-1)), CreatedAt: plus(2)},
		{ID: "p3-due-tie-oldest", Priority: 3, DueAt: timePtr(plus(1)), CreatedAt: base},
	}

	sort.Slice(tasks, func(i, j int) bool { return CanonicalLess(tasks[i], tasks[j]) })

	var got []string
	for _, tk := range tasks {
		got = append(got, string(tk.ID))
	}
	want := []string{
		"p5-nodue",                                                          // p5 first: priority DESC dominates null due + late creation
		"p3-due-early", "p3-due-tie-oldest", "p3-due-tie-newer", "p3-nodue", // p3: due ASC, created tiebreak, nulls last
		"p2-due-mid",
		"p0-due-soonest", // p0 last despite the soonest due date
	}
	if len(got) != len(want) {
		t.Fatalf("sorted %d tasks, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full order %v)", i, got[i], want[i], got)
			break
		}
	}
}

// TestNormalizeTitle mirrors the normalization semantics moved from the
// application layer: lowercased + trimmed.
func TestNormalizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Refactor Auth", "refactor auth"},
		{"  padded title   ", "padded title"},
		{"MiXeD CaSe!", "mixed case!"},
		{"", ""},
		{"\t\n   \n\t", ""},
	}
	for _, tc := range cases {
		if got := NormalizeTitle(tc.in); got != tc.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTitleSimilarity covers the 0–3 scoring table of the moved scorer under
// WORD-BOUNDARY semantics: normalized-equal, whole-token containment on token
// sets (never byte substrings), ≥half shared significant tokens, and the
// no-signal / stopword-only zero cases. The old byte-containment false
// positives ("ui" in "build suite", "auth" in "authentication") now score 0.
func TestTitleSimilarity(t *testing.T) {
	score := func(q, c string) int {
		nq, nc := NormalizeTitle(q), NormalizeTitle(c)
		return TitleSimilarity(nq, nc, SignificantTokens(nq), SignificantTokens(nc))
	}

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
			if got := score(tc.q, tc.c); got != tc.want {
				t.Errorf("TitleSimilarity(%q, %q) = %d, want %d", tc.q, tc.c, got, tc.want)
			}
		})
	}
}

// TestSignificantTokens_DropsRequiredStopwords pins AC2 of the duplicate-
// detection fix: every word in the required minimum stopword list is dropped
// by the tokenizer (a single-word title of it tokenizes to NOTHING), while
// content words survive.
func TestSignificantTokens_DropsRequiredStopwords(t *testing.T) {
	required := []string{
		"of", "to", "the", "a", "an", "and", "or", "but", "for", "with",
		"on", "in", "at", "do", "does", "done", "we", "can", "could", "if",
		"not", "no", "all", "is", "are", "was", "were",
	}
	for _, w := range required {
		if toks := SignificantTokens(NormalizeTitle(w)); len(toks) != 0 {
			t.Errorf("SignificantTokens(%q) keeps %d token(s), want 0 (%q must be a stopword)", w, len(toks), w)
		}
	}
	for _, w := range []string{"auth", "fix", "login", "bug", "ui"} {
		if toks := SignificantTokens(NormalizeTitle(w)); len(toks) != 1 {
			t.Errorf("SignificantTokens(%q) keeps %d token(s), want exactly the content token", w, len(toks))
		}
	}
}
