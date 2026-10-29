package sqlite

// Integration tests over a real temp-file SQLite database for the board's
// batch comment-stats read (application.Comments.Stats → commentRepo.
// CommentStatsByTasks): ONE window pass returns each task's comment count plus
// the kind/actor of its most-recent comment — max created_at, with same-
// timestamp ties broken by insertion order (rowid DESC), exactly ListByTask's
// newest-first ordering. Comment rows are seeded directly with exact
// timestamps so ties and out-of-order inserts are deterministic — no sleeps.

import (
	"context"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

func TestCommentStatsByTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)
	base := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	tc := &testClock{now: base}
	tasks, projects := newClaimUseCase(t, db, tc)
	p := mustCreateProject(t, ctx, projects)
	seed := domain.Actor{Type: domain.ActorAgent, Name: "stats-seeder"}

	mkTask := func(title string) domain.ID {
		tk, err := tasks.Create(ctx, seed, application.TaskInput{Title: title, ProjectID: p.ID})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return tk.ID
	}

	seedComment := func(id string, taskID domain.ID, kind domain.CommentKind, actorName string, at time.Time) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, string(taskID), string(kind), "fixture "+kind, string(domain.ActorAgent), actorName, formatTS(at)); err != nil {
			t.Fatalf("seed comment %s: %v", id, err)
		}
	}

	// A: plain chronology — note < question < progress; the newest row wins.
	a := mkTask("stats chronology")
	seedComment("st-a-1", a, domain.CommentNote, "worker-a", base)
	seedComment("st-a-2", a, domain.CommentQuestion, "worker-a", base.Add(2*time.Second))
	seedComment("st-a-3", a, domain.CommentProgress, "worker-b", base.Add(5*time.Second))

	// B: created_at TIE — handoff inserted first, note second; the later rowid
	// must win (insertion order, same rule ListByTask uses).
	b := mkTask("stats tie")
	tieAt := base.Add(30 * time.Second)
	seedComment("st-b-1", b, domain.CommentHandoff, "worker-c", tieAt)
	seedComment("st-b-2", b, domain.CommentNote, "worker-d", tieAt)

	// C: out-of-order inserts — a NEWER-timestamp row inserted FIRST must still
	// win over an older-timestamp row inserted second (max created_at rules).
	c := mkTask("stats out of order")
	newer := base.Add(60 * time.Second)
	seedComment("st-c-1", c, domain.CommentProgress, "worker-e", newer)
	seedComment("st-c-2", c, domain.CommentQuestion, "worker-f", newer.Add(-3*time.Second))

	// D: no comments at all → must be absent from the result map.
	d := mkTask("stats no comments")

	uow := NewUnitOfWork(db)
	var got map[domain.ID]application.TaskCommentStats
	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		var serr error
		got, serr = r.Comments.CommentStatsByTasks(ctx, []domain.ID{a, b, c, d})
		return serr
	})
	if err != nil {
		t.Fatalf("CommentStatsByTasks: %v", err)
	}

	want := map[domain.ID]application.TaskCommentStats{
		a: {Count: 3, LastKind: domain.CommentProgress, LastActor: domain.Actor{Type: domain.ActorAgent, Name: "worker-b"}},
		b: {Count: 2, LastKind: domain.CommentNote, LastActor: domain.Actor{Type: domain.ActorAgent, Name: "worker-d"}},
		c: {Count: 2, LastKind: domain.CommentProgress, LastActor: domain.Actor{Type: domain.ActorAgent, Name: "worker-e"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stats (%+v), want %d", len(got), got, len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("task %s missing from stats", id)
			continue
		}
		if g != w {
			t.Errorf("task %s stats = %+v, want %+v", id, g, w)
		}
	}
	if _, present := got[d]; present {
		t.Errorf("task %s has no comments — must be absent from the map", d)
	}

	// Empty input short-circuits: empty map, no error.
	var emptyOut map[domain.ID]application.TaskCommentStats
	if serr := uow.Run(ctx, &r, func() error {
		var e error
		emptyOut, e = r.Comments.CommentStatsByTasks(ctx, nil)
		return e
	}); serr != nil {
		t.Fatalf("empty input: %v", serr)
	}
	if len(emptyOut) != 0 {
		t.Errorf("empty input = %+v, want empty map", emptyOut)
	}
}
