package sqlite

import (
	"reflect"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// tasklist_where_test.go table-pins the pure WHERE-clause renderer behind
// Tasks.List: every filter field, its exact clause text and arg order, the
// tri-state Claimed/ExpiredClaim handling, the LIKE escaping contract, and the
// ExpiredClaim-without-clock invariant. The rendered fragment is shared by the
// COUNT(*) and row queries, so a wrong column or arg order here silently mis-
// lists or mis-counts tasks — exactly what this table guards.

var whereNow = time.Date(2026, 10, 9, 15, 30, 0, 0, time.UTC)

func p[T any](v T) *T { return &v }

func TestTaskListWhere(t *testing.T) {
	cases := []struct {
		name string
		f    application.TaskFilter
		want string
		args []any
	}{
		{
			name: "zero value lists open tasks only",
			f:    application.TaskFilter{},
			want: "archived = 0", args: []any{},
		},
		{
			name: "archived flag flips the archive clause",
			f:    application.TaskFilter{Archived: true},
			want: "archived = 1", args: []any{},
		},
		{
			name: "identity filters in canonical order",
			f: application.TaskFilter{
				ProjectID: "p1", Status: domain.StatusDoing,
				AssigneeType: domain.ActorAgent, AssigneeName: "amy",
				Priority: p(domain.Priority(3)),
			},
			want: "project_id = ? AND status = ? AND assignee_type = ? AND assignee_name = ? AND archived = 0 AND priority = ?",
			args: []any{"p1", string(domain.StatusDoing), string(domain.ActorAgent), "amy", 3},
		},
		{
			name: "due and created/completed windows render formatted stamps",
			f: application.TaskFilter{
				DueBefore: &whereNow, DueAfter: &whereNow,
				CreatedAfter: &whereNow, CreatedBefore: &whereNow,
				CompletedAfter: &whereNow, CompletedBefore: &whereNow,
			},
			want: "archived = 0 AND due_at < ? AND due_at >= ? AND created_at >= ? AND created_at < ? AND completed_at >= ? AND completed_at < ?",
			args: []any{formatTS(whereNow), formatTS(whereNow), formatTS(whereNow), formatTS(whereNow), formatTS(whereNow), formatTS(whereNow)},
		},
		{
			name: "search term is escaped verbatim and repeated three times",
			f:    application.TaskFilter{Q: `100% _x\y`},
			want: "archived = 0 AND (lower(title) LIKE ? ESCAPE '\\' OR lower(description) LIKE ? ESCAPE '\\' OR lower(notes) LIKE ? ESCAPE '\\')",
			args: []any{"%100\\% \\_x\\\\y%", "%100\\% \\_x\\\\y%", "%100\\% \\_x\\\\y%"},
		},
		{
			name: "unblocked and parent subqueries carry their exact shapes",
			f:    application.TaskFilter{UnblockedOnly: true, ParentID: "epic-1"},
			want: "archived = 0 AND NOT EXISTS (SELECT 1 FROM relation r JOIN task b ON b.id = r.other_task_id WHERE r.task_id = task.id AND r.kind = 'blocked-by' AND b.status <> 'done' AND b.archived = 0) AND EXISTS (SELECT 1 FROM relation r WHERE r.task_id = task.id AND r.kind = 'child' AND r.other_task_id = ?)",
			args: []any{"epic-1"},
		},
		{
			name: "claimed tri-state and holder name",
			f:    application.TaskFilter{Claimed: p(true), ClaimedBy: "bob"},
			want: "archived = 0 AND claimed_by_name IS NOT NULL AND claimed_by_name = ?",
			args: []any{"bob"},
		},
		{
			name: "claimed false matches only unleased tasks",
			f:    application.TaskFilter{Claimed: p(false)},
			want: "archived = 0 AND claimed_by_name IS NULL", args: []any{},
		},
		{
			name: "expired claim true lapses at the threaded clock",
			f: application.TaskFilter{
				ExpiredClaim:   p(true),
				ClaimExpiryNow: &whereNow,
			},
			want: "archived = 0 AND claim_expires_at IS NOT NULL AND claim_expires_at < ?",
			args: []any{formatTS(whereNow)},
		},
		{
			name: "expired claim false keeps unclaimed or active leases",
			f: application.TaskFilter{
				ExpiredClaim:   p(false),
				ClaimExpiryNow: &whereNow,
			},
			want: "archived = 0 AND (claim_expires_at IS NULL OR claim_expires_at >= ?)",
			args: []any{formatTS(whereNow)},
		},
		{
			name: "expired claim without clock fails loudly",
			f:    application.TaskFilter{ExpiredClaim: p(true)},
			want: "", args: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotArgs := taskListWhere(tc.f)
			if got != tc.want {
				t.Errorf("WHERE = %q,\nwant   %q", got, tc.want)
			}
			if !reflect.DeepEqual(gotArgs, tc.args) {
				t.Errorf("args = %#v,\nwant   %#v", gotArgs, tc.args)
			}
		})
	}
}

func TestEscapeLikePattern_EscapesAllThreeMetaCharacters(t *testing.T) {
	cases := map[string]string{
		`a\b`:   `a\\b`,
		`100%`:  `100\%`,
		`s_1`:   `s\_1`,
		``:      ``,
		"plain": "plain",
	}
	for in, want := range cases {
		if got := escapeLikePattern(in); got != want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
