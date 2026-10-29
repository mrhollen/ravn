package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// TestTasksList_SearchEscapesLikeWildcards proves that the task search term is
// matched VERBATIM: % and _ are not wildcards, and a literal backslash in the
// query matches only a literal backslash (the LIKE escape char must not leak
// into matching). Each subtest seeds rows with distinctive IDs and asserts
// exactly which ones a given TaskFilter.Q returns.
func TestTasksList_SearchEscapesLikeWildcards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		for _, s := range []struct {
			id          string
			title       string
			description string
			notes       string
		}{
			// "100%" scenario: literal % must be required.
			{"pct-yes", "Progress at 100% complete", "", ""},
			{"pct-no", "Progress at 100 of 200 steps done", "", ""},
			// "step_1" scenario: underscore must be literal.
			{"und-yes", "Use step_1 first", "", ""},
			{"und-a1", "Use stepA1 first", "", ""},
			{"und-dash", "Use step-1 first", "", ""},
			{"und-space", "Use step 1 first", "", ""},
			// bare "%" scenario: only rows with a literal percent match.
			{"bare-pct-yes", "Contains a % symbol", "", ""},
			{"bare-pct-no", "Ordinary title without percent", "", ""},
			// backslash scenario: query "path\to" must match the literal, and a
			// row containing the escape sequence spelled out must not.
			{"bs-yes", "save to path\\to file", "", ""},
			{"bs-no", "save to pathXto file", "", ""},
			// backslash as lone query: only rows with a real backslash match.
			{"bs-lone-yes", "a b\\c reference", "", ""},
			{"bs-lone-no", "plain reference", "", ""},
			// % and _ must also be literal when they live in description/notes,
			// i.e. the ESCAPE clause is attached to every one of the 3 predicates.
			{"desc-pct-yes", "Title without special chars", "100% battery remaining", ""},
			{"note-pct-yes", "Another plain title", "", "notes say 99.5% and step_2 next"},
			{"note-no", "Yet another plain title", "", "nothing interesting in these notes"},
		} {
			row := domain.Task{
				ID: domain.ID(s.id), ProjectID: p.ID, Title: s.title,
				Description: s.description, Notes: s.notes,
				Status: domain.StatusTodo, Version: 1, CreatedAt: base, UpdatedAt: base,
			}
			if err := r.Tasks.Create(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	type scenario struct {
		name  string
		q     string
		match map[domain.ID]bool // ids expected in the result (all other seeded rows must be absent)
	}
	scenarios := []scenario{
		{"percent-literal", "100%",
			map[domain.ID]bool{"pct-yes": true, "desc-pct-yes": true}},
		{"underscore-literal", "step_1",
			map[domain.ID]bool{"und-yes": true}},
		{"bare-percent-matches-all", "%",
			map[domain.ID]bool{
				"pct-yes": true, "bare-pct-yes": true,
				"desc-pct-yes": true, "note-pct-yes": true, // note contains "99.5%"
			}},
		{"backslash-in-query", `path\to`,
			map[domain.ID]bool{"bs-yes": true}},
		{"bare-backslash-literal", `\`,
			map[domain.ID]bool{"bs-yes": true, "bs-lone-yes": true}},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			rows, total, err := tasks.List(ctx, application.TaskFilter{ProjectID: p.ID, Q: sc.q})
			if err != nil {
				t.Fatalf("List Q=%q: %v", sc.q, err)
			}
			got := make(map[domain.ID]bool, len(rows))
			for _, row := range rows {
				got[row.ID] = true
			}
			if int(total) != len(sc.match) {
				t.Fatalf("List Q=%q: total = %d, want %d (matched: %v)", sc.q, total, len(sc.match), got)
			}
			for id, want := range sc.match {
				if got[id] != want {
					state := "absent"
					if got[id] {
						state = "present"
					}
					t.Errorf("Q=%q: row %q is %s, want %t", sc.q, string(id), state, want)
				}
			}
		})
	}
}

// TestEscapeLikePattern_Unit pins the escaping helper itself, including that
// backslashes are escaped before the other metacharacters.
func TestEscapeLikePattern_Unit(t *testing.T) {
	cases := map[string]string{
		"100%":   "100\\%",
		"step_1": "step\\_1",
		"plain":  "plain",
		`a\b`:    "a\\\\b", // one backslash in → two out
		`C:\dir`: `C:\\dir`,
		"%_%\\":  "\\%\\_\\%\\\\", // %, _, %, \ each escaped, backslash last
		"100\\%": "100\\\\\\%",    // backslash escaped first, then the percent after it
	}
	for in, want := range cases {
		if got := escapeLikePattern(in); got != want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
