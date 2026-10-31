package sqlite

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// TestTimestamps_SQLiteComparisonFuzzMatchesGoSemantics fuzzes formatted timestamp
// pairs through a real SQLite connection and asserts the string comparison always
// matches Go's Before/Equal semantics (including equal-instant normalization).
func TestTimestamps_SQLiteComparisonFuzzMatchesGoSemantics(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	rng := rand.New(rand.NewSource(42))
	baseSec := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	// Generate 200 random timestamps over a 2-year window with random nanoseconds and timezones
	samples := make([]time.Time, 200)
	for i := range samples {
		secOffset := rng.Int63n(730 * 86400) // within 2 years
		ns := rng.Int63n(1_000_000_000)
		tzOffset := rng.Intn(28)*1800 - 43200 // -12h to +14h in 30min steps
		loc := time.FixedZone("FuzzTZ", tzOffset)
		samples[i] = time.Unix(baseSec+secOffset, ns).In(loc)
	}

	// Compare 1000 random pairs against SQLite
	for trial := 0; trial < 1000; trial++ {
		t1 := samples[rng.Intn(len(samples))]
		t2 := samples[rng.Intn(len(samples))]

		s1 := formatTS(t1)
		s2 := formatTS(t2)

		var sqlLess, sqlEqual, sqlGreater int
		err := db.QueryRowContext(ctx, `SELECT (? < ?), (? = ?), (? > ?)`, s1, s2, s1, s2, s1, s2).
			Scan(&sqlLess, &sqlEqual, &sqlGreater)
		if err != nil {
			t.Fatalf("trial %d SQLite query error: %v", trial, err)
		}

		goEqual := t1.Equal(t2)
		goBefore := t1.Before(t2)
		goAfter := t1.After(t2)

		if goBefore && sqlLess != 1 {
			t.Errorf("Mismatch for t1 < t2: t1=%v (%q) t2=%v (%q), sqlLess=%d", t1, s1, t2, s2, sqlLess)
		}
		if goEqual && sqlEqual != 1 {
			t.Errorf("Mismatch for t1 == t2: t1=%v (%q) t2=%v (%q), sqlEqual=%d", t1, s1, t2, s2, sqlEqual)
		}
		if goAfter && sqlGreater != 1 {
			t.Errorf("Mismatch for t1 > t2: t1=%v (%q) t2=%v (%q), sqlGreater=%d", t1, s1, t2, s2, sqlGreater)
		}
	}
}

// TestClaim_LeaseExactNanosecondBoundaries adversarially pins the exact lease-expiry
// boundary semantics: 1ns before (lease valid), exactly at expiry (still valid for the
// holder, not stealable), and 1ns after (expired: holder loses, rival may take over).
func TestClaim_LeaseExactNanosecondBoundaries(t *testing.T) {
	// Adversarially test lease expiration exact boundary:
	// Exact whole second vs exact fraction vs 1ns offsets.
	ctx := context.Background()

	testCases := []struct {
		name        string
		expiry      time.Time
		claimTTLSec int
	}{
		{
			name:        "Lease expiring at exact whole second",
			expiry:      time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC),
			claimTTLSec: 30,
		},
		{
			name:        "Lease expiring at 1 nanosecond",
			expiry:      time.Date(2026, 10, 1, 12, 0, 30, 1, time.UTC),
			claimTTLSec: 30,
		},
		{
			name:        "Lease expiring at 500 milliseconds",
			expiry:      time.Date(2026, 10, 1, 12, 0, 30, 500000000, time.UTC),
			claimTTLSec: 30,
		},
		{
			name:        "Lease expiring at 999999999 nanoseconds",
			expiry:      time.Date(2026, 10, 1, 12, 0, 30, 999999999, time.UTC),
			claimTTLSec: 30,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			claimStart := tc.expiry.Add(-time.Duration(tc.claimTTLSec) * time.Second)
			clock := &testClock{now: claimStart}
			tasks, projects := newClaimUseCase(t, db, clock)
			p := mustCreateProject(t, ctx, projects)

			agentA := domain.Actor{Type: domain.ActorAgent, Name: "agent-a"}
			agentB := domain.Actor{Type: domain.ActorAgent, Name: "agent-b"}

			task, err := tasks.Create(ctx, agentA, application.TaskInput{Title: "Boundary Task", ProjectID: p.ID})
			if err != nil {
				t.Fatalf("Create task: %v", err)
			}

			// Claim at claimStart with ttl
			_, err = tasks.Claim(ctx, agentA, task.ID, tc.claimTTLSec, false)
			if err != nil {
				t.Fatalf("Initial claim: %v", err)
			}

			// Check 1: 1 nanosecond BEFORE expiry
			// Active lease invariant: Holder can heartbeat/release, Competitor CANNOT claim.
			clock.now = tc.expiry.Add(-1 * time.Nanosecond)

			// Competitor attempt should fail
			_, err = tasks.Claim(ctx, agentB, task.ID, 30, false)
			if err == nil {
				t.Fatalf("Competitor claimed lease 1ns BEFORE expiry!")
			}
			if !isClaimConflict(err) {
				t.Fatalf("Expected ClaimConflictError, got %v", err)
			}

			// Holder heartbeat should succeed
			_, err = tasks.Heartbeat(ctx, agentA, task.ID)
			if err != nil {
				t.Fatalf("Holder heartbeat 1ns before expiry failed: %v", err)
			}

			// Reset expiry back for exact boundary test
			// Heartbeat extended it to clock.now + ttl. Let's reset the row's expiry directly.
			_, err = db.ExecContext(ctx, `UPDATE task SET claim_expires_at = ? WHERE id = ?`,
				formatTS(tc.expiry), string(task.ID))
			if err != nil {
				t.Fatalf("reset expiry: %v", err)
			}

			// Check 2: EXACTLY AT expiry (now == expiry)
			// According to SQL:
			// Claim WHERE includes: `OR claim_expires_at < ?`
			// Release & Heartbeat WHERE includes: `AND claim_expires_at >= ?`
			// Since expiry == now:
			// `expiry < now` is FALSE (competitor cannot steal).
			// `expiry >= now` is TRUE (holder is still valid at the exact boundary).
			clock.now = tc.expiry

			// Competitor attempt at exact boundary: MUST FAIL
			_, err = tasks.Claim(ctx, agentB, task.ID, 30, false)
			if err == nil {
				t.Fatalf("Competitor claimed lease at EXACT expiry moment (premature before < now)!")
			}
			if !isClaimConflict(err) {
				t.Fatalf("Expected ClaimConflictError at exact boundary, got %v", err)
			}

			// Holder heartbeat at exact boundary: MUST SUCCEED
			_, err = tasks.Heartbeat(ctx, agentA, task.ID)
			if err != nil {
				t.Fatalf("Holder heartbeat at exact expiry boundary failed: %v", err)
			}

			// Reset expiry back to tc.expiry
			_, err = db.ExecContext(ctx, `UPDATE task SET claim_expires_at = ? WHERE id = ?`,
				formatTS(tc.expiry), string(task.ID))
			if err != nil {
				t.Fatalf("reset expiry: %v", err)
			}

			// Check 3: 1 nanosecond AFTER expiry (now == expiry + 1ns)
			// Expired invariant: Holder CANNOT heartbeat/release, Competitor CAN claim.
			clock.now = tc.expiry.Add(1 * time.Nanosecond)

			// Holder heartbeat: MUST FAIL
			_, err = tasks.Heartbeat(ctx, agentA, task.ID)
			if err == nil {
				t.Fatalf("Holder heartbeat succeeded 1ns AFTER expiry!")
			}
			if !isClaimConflict(err) {
				t.Fatalf("Expected ClaimConflictError for expired holder heartbeat, got %v", err)
			}

			// Competitor claim: MUST SUCCEED
			claimed, err := tasks.Claim(ctx, agentB, task.ID, 30, false)
			if err != nil {
				t.Fatalf("Competitor takeover 1ns after expiry failed: %v", err)
			}
			byClaimed := claimed.Claim.By()
			if byClaimed == nil || byClaimed.Name != agentB.Name {
				t.Fatalf("Competitor takeover holder = %v, want agent-b", byClaimed)
			}
		})
	}
}

// TestTasksList_DueOrderingSubsecondAndDeadlineFilters verifies canonical due-date
// ordering at subsecond precision over real SQL, plus DueBefore/DueAfter filtering.
func TestTasksList_DueOrderingSubsecondAndDeadlineFilters(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	// Create 20 tasks with non-trivial due dates:
	// - some whole second
	// - some with .000000001
	// - some with .500000000
	// - some with .999999999
	// - some across minute / hour boundaries
	// - some with non-UTC timezones
	base := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	deltas := []time.Duration{
		0,
		1 * time.Nanosecond,
		500 * time.Millisecond,
		999*time.Millisecond + 999999*time.Nanosecond,
		1 * time.Second,
		1*time.Second + 1*time.Nanosecond,
		1*time.Second + 500*time.Millisecond,
		2 * time.Second,
		10 * time.Minute,
		24 * time.Hour,
	}

	type item struct {
		id  string
		due time.Time
	}
	var items []item
	for i, d := range deltas {
		// UTC version
		items = append(items, item{
			id:  fmt.Sprintf("task-utc-%02d", i),
			due: base.Add(d),
		})
		// JST (+9h) version shifted so instant is interleaved
		items = append(items, item{
			id:  fmt.Sprintf("task-jst-%02d", i),
			due: base.Add(d + 100*time.Millisecond).In(time.FixedZone("JST", 9*3600)),
		})
	}

	// Sort items in expected chronological order
	sort.Slice(items, func(i, j int) bool {
		return items[i].due.Before(items[j].due)
	})

	// Insert into DB in randomized/reverse order
	var r application.Repos
	err := NewUnitOfWork(db).Run(ctx, &r, func() error {
		for i := len(items) - 1; i >= 0; i-- {
			due := items[i].due
			taskRow := domain.Task{
				ID:        domain.ID(items[i].id),
				ProjectID: p.ID,
				Title:     items[i].id,
				Status:    domain.StatusTodo,
				Priority:  3, // all equal priority to test due_at ASC sorting
				DueAt:     &due,
				Version:   1,
				CreatedAt: base,
				UpdatedAt: base,
			}
			if err := r.Tasks.Create(ctx, taskRow); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	// 1. Verify List with due_at ASC canonical sorting
	listed, total, err := tasks.List(ctx, application.TaskFilter{
		ProjectID: p.ID,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if int(total) != len(items) {
		t.Fatalf("total = %d, want %d", total, len(items))
	}

	for i, expected := range items {
		if listed[i].ID != domain.ID(expected.id) {
			t.Errorf("Sort mismatch at position %d: got %s (due %v), want %s (due %v)",
				i, listed[i].ID, listed[i].DueAt, expected.id, expected.due)
		}
	}

	// 2. Test DueBefore and DueAfter filtering at subsecond cutoffs
	cutoff := base.Add(1 * time.Second) // exact whole second cutoff
	beforeList, _, err := tasks.List(ctx, application.TaskFilter{
		ProjectID: p.ID,
		DueBefore: &cutoff,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("List DueBefore: %v", err)
	}
	for _, tRow := range beforeList {
		if !tRow.DueAt.Before(cutoff) {
			t.Errorf("DueBefore violation: task %s has due %v, cutoff %v", tRow.ID, tRow.DueAt, cutoff)
		}
	}

	afterList, _, err := tasks.List(ctx, application.TaskFilter{
		ProjectID: p.ID,
		DueAfter:  &cutoff,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("List DueAfter: %v", err)
	}
	for _, tRow := range afterList {
		if tRow.DueAt.Before(cutoff) {
			t.Errorf("DueAfter violation: task %s has due %v, cutoff %v", tRow.ID, tRow.DueAt, cutoff)
		}
	}

	if len(beforeList)+len(afterList) != len(items) {
		t.Errorf("Partition mismatch: len(before)=%d + len(after)=%d != total %d",
			len(beforeList), len(afterList), len(items))
	}
}

// TestEventStore_SubsecondDescendingOrder verifies event-store ordering holds at
// subsecond precision: events appended with < 1s gaps must come back strictly
// descending from ListByTask over real SQL.
func TestEventStore_SubsecondDescendingOrder(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	events := newEventStore(db)
	tasks, projects := newTaskUseCase(t, db)
	p := mustCreateProject(t, ctx, projects)

	agent := domain.Actor{Type: domain.ActorAgent, Name: "tester"}
	task, err := tasks.Create(ctx, agent, application.TaskInput{Title: "Event Task", ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}

	taskID := task.ID
	projectID := p.ID
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	// Create events with fine subsecond timestamps
	deltas := []time.Duration{
		0,
		1 * time.Nanosecond,
		100 * time.Nanosecond,
		500 * time.Millisecond,
		999*time.Millisecond + 999999*time.Nanosecond,
		1 * time.Second,
	}

	for i, d := range deltas {
		ev := domain.Event{
			ID:        domain.ID(fmt.Sprintf("ev-%02d", i)),
			TaskID:    &taskID,
			ProjectID: &projectID,
			Actor:     agent,
			Kind:      domain.EventCreated,
			At:        base.Add(d),
		}
		if err := events.Append(ctx, ev); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// ListByTask returns newest first (ts DESC)
	list, err := events.ListByTask(ctx, taskID, 100, 0)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}

	if len(list) < len(deltas) {
		t.Fatalf("got %d events, want at least %d", len(list), len(deltas))
	}

	// Verify events are in strict descending chronological order (ts DESC)
	for i := 0; i < len(list)-1; i++ {
		cur := list[i].At
		next := list[i+1].At
		if cur.Before(next) {
			t.Errorf("Event order inversion at index %d: cur (%v) is before next (%v)", i, cur, next)
		}
	}
}
