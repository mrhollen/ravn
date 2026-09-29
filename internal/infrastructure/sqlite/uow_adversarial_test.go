package sqlite

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

type customPanicStruct struct {
	Code int
	Msg  string
}

type safeEventPublisher struct {
	mu     sync.Mutex
	events []domain.Event
}

func (s *safeEventPublisher) Publish(e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *safeEventPublisher) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *safeEventPublisher) Events() []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]domain.Event, len(s.events))
	copy(copied, s.events)
	return copied
}

// TestAdversarial_PanicTypesAndPoolRelease empirically verifies that uow.Run
// releases the SQLite single-connection pool (SetMaxOpenConns(1)) across all panic types:
// nil, string, typed error, struct by value, struct by pointer, and primitive int.
func TestAdversarial_PanicTypesAndPoolRelease(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	testCases := []struct {
		name         string
		panicPayload any
		checkRecover func(t *testing.T, rec any)
	}{
		{
			name:         "string panic",
			panicPayload: "fatal string error",
			checkRecover: func(t *testing.T, rec any) {
				if rec != "fatal string error" {
					t.Fatalf("recovered %v (%T), want 'fatal string error'", rec, rec)
				}
			},
		},
		{
			name:         "typed error panic",
			panicPayload: errors.New("sentinel boom"),
			checkRecover: func(t *testing.T, rec any) {
				err, ok := rec.(error)
				if !ok || err.Error() != "sentinel boom" {
					t.Fatalf("recovered %v (%T), want error 'sentinel boom'", rec, rec)
				}
			},
		},
		{
			name: "struct by value panic",
			panicPayload: customPanicStruct{
				Code: 42,
				Msg:  "struct-value-panic",
			},
			checkRecover: func(t *testing.T, rec any) {
				cps, ok := rec.(customPanicStruct)
				if !ok || cps.Code != 42 || cps.Msg != "struct-value-panic" {
					t.Fatalf("recovered %v (%T), want customPanicStruct{42, struct-value-panic}", rec, rec)
				}
			},
		},
		{
			name: "struct pointer panic",
			panicPayload: &customPanicStruct{
				Code: 99,
				Msg:  "struct-pointer-panic",
			},
			checkRecover: func(t *testing.T, rec any) {
				cps, ok := rec.(*customPanicStruct)
				if !ok || cps.Code != 99 || cps.Msg != "struct-pointer-panic" {
					t.Fatalf("recovered %v (%T), want *customPanicStruct{99, struct-pointer-panic}", rec, rec)
				}
			},
		},
		{
			name:         "primitive int panic",
			panicPayload: 12345,
			checkRecover: func(t *testing.T, rec any) {
				val, ok := rec.(int)
				if !ok || val != 12345 {
					t.Fatalf("recovered %v (%T), want 12345", rec, rec)
				}
			},
		},
		{
			name:         "nil panic",
			panicPayload: nil,
			checkRecover: func(t *testing.T, rec any) {
				// In Go 1.21+, panic(nil) turns into *runtime.PanicNilError
				if rec == nil {
					t.Fatal("expected panic(nil) to be re-raised (got nil recovery)")
				}
				if _, ok := rec.(*runtime.PanicNilError); !ok {
					t.Logf("recovered non-nil value for panic(nil): %v (%T)", rec, rec)
				}
			},
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			projID := domain.ID(fmt.Sprintf("adv-panic-proj-%d", i))
			eventID := domain.ID(fmt.Sprintf("adv-panic-event-%d", i))

			var recoveredVal any
			didRecover := false

			func() {
				defer func() {
					if r := recover(); r != nil {
						recoveredVal = r
						didRecover = true
					}
				}()

				var r application.Repos
				_ = uow.Run(ctx, &r, func() error {
					p := domain.Project{
						ID:        projID,
						Name:      fmt.Sprintf("Project %s", tc.name),
						CreatedAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					}
					if err := r.Projects.Create(ctx, p); err != nil {
						return err
					}
					ev := domain.Event{
						ID:        eventID,
						ProjectID: &projID,
						Actor:     domain.Actor{Type: domain.ActorAgent, Name: "challenger"},
						Kind:      domain.EventCreated,
						At:        p.CreatedAt,
					}
					if err := r.Events.Append(ctx, ev); err != nil {
						return err
					}

					// Trigger panic
					panic(tc.panicPayload)
				})
			}()

			if !didRecover {
				t.Fatalf("[%s] expected panic to be re-raised, but execution did not panic", tc.name)
			}
			tc.checkRecover(t, recoveredVal)

			// Verify row rollback in DB
			var pCount, eCount int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = ?`, string(projID)).Scan(&pCount); err != nil {
				t.Fatalf("[%s] query project count: %v", tc.name, err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE id = ?`, string(eventID)).Scan(&eCount); err != nil {
				t.Fatalf("[%s] query event count: %v", tc.name, err)
			}
			if pCount != 0 || eCount != 0 {
				t.Fatalf("[%s] leaked uncommitted row: project=%d event=%d", tc.name, pCount, eCount)
			}

			// Verify connection pool is fully usable immediately without deadlock or busy timeout
			subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			healthyProjID := domain.ID(fmt.Sprintf("adv-healthy-proj-%d", i))
			var rPost application.Repos
			err := uow.Run(subCtx, &rPost, func() error {
				return rPost.Projects.Create(subCtx, domain.Project{
					ID:        healthyProjID,
					Name:      fmt.Sprintf("Healthy After %s", tc.name),
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				})
			})
			if err != nil {
				t.Fatalf("[%s] subsequent transaction failed (connection pool deadlock/leak?): %v", tc.name, err)
			}

			var healthyCount int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = ?`, string(healthyProjID)).Scan(&healthyCount); err != nil || healthyCount != 1 {
				t.Fatalf("[%s] subsequent project row count = %d, want 1 (err=%v)", tc.name, healthyCount, err)
			}
		})
	}
}

// TestAdversarial_EventSuppressionOnPanic verifies that events appended to r.Events
// are NEVER broadcast to EventPublisher if the callback panics.
func TestAdversarial_EventSuppressionOnPanic(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pub := &safeEventPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	pID := domain.ID("suppress-test-proj")

	// 1. Transaction panics after appending events
	func() {
		defer func() { _ = recover() }()
		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			_ = r.Projects.Create(ctx, domain.Project{
				ID:        pID,
				Name:      "Suppressed Project",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			})
			_ = r.Events.Append(ctx, domain.Event{
				ID:        domain.ID("ev-panicked-1"),
				ProjectID: &pID,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
				Kind:      domain.EventCreated,
				At:        time.Now().UTC(),
			})
			_ = r.Events.Append(ctx, domain.Event{
				ID:        domain.ID("ev-panicked-2"),
				ProjectID: &pID,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
				Kind:      domain.EventCreated,
				At:        time.Now().UTC(),
			})
			panic("abort before commit")
		})
	}()

	if pub.Count() != 0 {
		t.Fatalf("publisher received %d events from panicked transaction, want 0", pub.Count())
	}

	// 2. Transaction returns error after appending events
	func() {
		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			_ = r.Events.Append(ctx, domain.Event{
				ID:        domain.ID("ev-error-1"),
				ProjectID: &pID,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
				Kind:      domain.EventCreated,
				At:        time.Now().UTC(),
			})
			return errors.New("callback returned error")
		})
	}()

	if pub.Count() != 0 {
		t.Fatalf("publisher received %d events from errored transaction, want 0", pub.Count())
	}

	// 3. Successful transaction properly emits its events
	commitPID := domain.ID("committed-proj")
	commitEID := domain.ID("ev-committed-1")
	var r application.Repos
	err := uow.Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, domain.Project{
			ID:        commitPID,
			Name:      "Committed",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		return r.Events.Append(ctx, domain.Event{
			ID:        commitEID,
			ProjectID: &commitPID,
			Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
			Kind:      domain.EventCreated,
			At:        time.Now().UTC(),
		})
	})
	if err != nil {
		t.Fatalf("commit tx failed: %v", err)
	}

	if pub.Count() != 1 {
		t.Fatalf("publisher received %d events, want exactly 1", pub.Count())
	}
	if pub.Events()[0].ID != commitEID {
		t.Fatalf("published event ID = %s, want %s", pub.Events()[0].ID, commitEID)
	}
}

// TestAdversarial_AllTablesRollbackOnPanic tests that writes across ALL domain tables
// (project, task, acceptance_criterion, comment, artifact, relation, event) are
// atomically rolled back when a panic occurs, leaving zero trace in any table.
func TestAdversarial_AllTablesRollbackOnPanic(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	now := time.Now().UTC()
	pID := domain.ID("all-tables-proj")
	tID1 := domain.ID("all-tables-task-1")
	tID2 := domain.ID("all-tables-task-2")
	acID := domain.ID("all-tables-ac")
	commID := domain.ID("all-tables-comm")
	artID := domain.ID("all-tables-art")
	relID := domain.ID("all-tables-rel")
	evID := domain.ID("all-tables-ev")

	func() {
		defer func() { _ = recover() }()

		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			if err := r.Projects.Create(ctx, domain.Project{
				ID:        pID,
				Name:      "All Tables Project",
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return err
			}

			if err := r.Tasks.Create(ctx, domain.Task{
				ID:        tID1,
				ProjectID: pID,
				Title:     "Task 1",
				Status:    domain.StatusTodo,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return err
			}

			if err := r.Tasks.Create(ctx, domain.Task{
				ID:        tID2,
				ProjectID: pID,
				Title:     "Task 2",
				Status:    domain.StatusTodo,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return err
			}

			if err := r.ACs.Create(ctx, []domain.AcceptanceCriterion{
				{
					ID:     acID,
					TaskID: tID1,
					Seq:    1,
					Text:   "Must rollback",
				},
			}); err != nil {
				return err
			}

			if err := r.Comments.Add(ctx, domain.Comment{
				ID:        commID,
				TaskID:    tID1,
				Kind:      domain.CommentProgress,
				Body:      "Comment before panic",
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "challenger"},
				CreatedAt: now,
			}); err != nil {
				return err
			}

			if err := r.Artifacts.Add(ctx, domain.Artifact{
				ID:        artID,
				TaskID:    tID1,
				Kind:      domain.ArtifactLog,
				Ref:       "/tmp/test.log",
				Summary:   "Test log",
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "challenger"},
				CreatedAt: now,
			}); err != nil {
				return err
			}

			if err := r.Relations.Add(ctx, domain.Relation{
				ID:          relID,
				TaskID:      tID1,
				OtherTaskID: tID2,
				Kind:        domain.RelationBlockedBy,
				CreatedAt:   now,
			}); err != nil {
				return err
			}

			if err := r.Events.Append(ctx, domain.Event{
				ID:        evID,
				ProjectID: &pID,
				TaskID:    &tID1,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "challenger"},
				Kind:      domain.EventCreated,
				At:        now,
			}); err != nil {
				return err
			}

			panic("panic after writing to all 7 tables")
		})
	}()

	// Verify all 7 tables have 0 rows from this aborted transaction
	tables := []struct {
		table string
		query string
		id    domain.ID
	}{
		{"project", "SELECT COUNT(*) FROM project WHERE id = ?", pID},
		{"task", "SELECT COUNT(*) FROM task WHERE id = ?", tID1},
		{"acceptance_criterion", "SELECT COUNT(*) FROM acceptance_criterion WHERE id = ?", acID},
		{"comment", "SELECT COUNT(*) FROM comment WHERE id = ?", commID},
		{"artifact", "SELECT COUNT(*) FROM artifact WHERE id = ?", artID},
		{"relation", "SELECT COUNT(*) FROM relation WHERE task_id = ?", tID1},
		{"event", "SELECT COUNT(*) FROM event WHERE id = ?", evID},
	}

	for _, tc := range tables {
		var count int
		if err := db.QueryRowContext(ctx, tc.query, string(tc.id)).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", tc.table, err)
		}
		if count != 0 {
			t.Fatalf("table %s leaked %d rows after panic rollback", tc.table, count)
		}
	}
}

// TestAdversarial_Stress50_SequentialAndConcurrentTransactions launches 60 rapid
// concurrent transactions under the single-connection pool (SetMaxOpenConns(1))
// with a mix of panics (string, struct, nil, error), normal error rollbacks, and commits.
//
// It verifies:
// 1. Zero deadlocks (all complete within test timeout).
// 2. Zero database busy timeouts.
// 3. Zero leaked uncommitted transactions.
// 4. Exact consistency of SQLite storage and EventPublisher.
func TestAdversarial_Stress50_SequentialAndConcurrentTransactions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pub := &safeEventPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	const totalWorkers = 60
	// 20 commit, 10 string panic, 10 struct panic, 10 nil panic, 10 error returns
	const wantCommitted = 20
	const wantPanicked = 30
	const wantErrored = 10

	var (
		panicsRecovered  int64
		errorsReturned   int64
		commitsSucceeded int64
		wg               sync.WaitGroup
		startBarrier     = make(chan struct{})
	)

	type workerType int
	const (
		typeCommit workerType = iota
		typePanicString
		typePanicStruct
		typePanicNil
		typeReturnError
	)

	workers := make([]workerType, 0, totalWorkers)
	for i := 0; i < wantCommitted; i++ {
		workers = append(workers, typeCommit)
	}
	for i := 0; i < 10; i++ {
		workers = append(workers, typePanicString)
		workers = append(workers, typePanicStruct)
		workers = append(workers, typePanicNil)
		workers = append(workers, typeReturnError)
	}

	for idx, wType := range workers {
		wg.Add(1)
		go func(workerID int, wt workerType) {
			defer wg.Done()

			// Wait for synchronized start to maximize contention on SetMaxOpenConns(1)
			<-startBarrier

			workerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			pID := domain.ID(fmt.Sprintf("stress-proj-%03d", workerID))
			eID := domain.ID(fmt.Sprintf("stress-ev-%03d", workerID))

			switch wt {
			case typeCommit:
				var r application.Repos
				err := uow.Run(workerCtx, &r, func() error {
					if err := r.Projects.Create(workerCtx, domain.Project{
						ID:        pID,
						Name:      fmt.Sprintf("Stress Project %d", workerID),
						CreatedAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					}); err != nil {
						return err
					}
					return r.Events.Append(workerCtx, domain.Event{
						ID:        eID,
						ProjectID: &pID,
						Actor:     domain.Actor{Type: domain.ActorAgent, Name: "stress"},
						Kind:      domain.EventCreated,
						At:        time.Now().UTC(),
					})
				})
				if err != nil {
					t.Errorf("worker %d (commit) failed unexpectedly: %v", workerID, err)
					return
				}
				atomic.AddInt64(&commitsSucceeded, 1)

			case typePanicString:
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      fmt.Sprintf("Panicked %d", workerID),
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						_ = r.Events.Append(workerCtx, domain.Event{
							ID:        eID,
							ProjectID: &pID,
							Actor:     domain.Actor{Type: domain.ActorAgent, Name: "stress"},
							Kind:      domain.EventCreated,
							At:        time.Now().UTC(),
						})
						panic(fmt.Sprintf("stress string panic %d", workerID))
					})
				}()

			case typePanicStruct:
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      fmt.Sprintf("Panicked %d", workerID),
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						_ = r.Events.Append(workerCtx, domain.Event{
							ID:        eID,
							ProjectID: &pID,
							Actor:     domain.Actor{Type: domain.ActorAgent, Name: "stress"},
							Kind:      domain.EventCreated,
							At:        time.Now().UTC(),
						})
						panic(customPanicStruct{Code: workerID, Msg: "struct-panic"})
					})
				}()

			case typePanicNil:
				func() {
					defer func() {
						if rec := recover(); rec != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      fmt.Sprintf("Panicked %d", workerID),
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						_ = r.Events.Append(workerCtx, domain.Event{
							ID:        eID,
							ProjectID: &pID,
							Actor:     domain.Actor{Type: domain.ActorAgent, Name: "stress"},
							Kind:      domain.EventCreated,
							At:        time.Now().UTC(),
						})
						panic(nil)
					})
				}()

			case typeReturnError:
				var r application.Repos
				err := uow.Run(workerCtx, &r, func() error {
					_ = r.Projects.Create(workerCtx, domain.Project{
						ID:        pID,
						Name:      fmt.Sprintf("Errored %d", workerID),
						CreatedAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					})
					_ = r.Events.Append(workerCtx, domain.Event{
						ID:        eID,
						ProjectID: &pID,
						Actor:     domain.Actor{Type: domain.ActorAgent, Name: "stress"},
						Kind:      domain.EventCreated,
						At:        time.Now().UTC(),
					})
					return errors.New("simulated error")
				})
				if err != nil {
					atomic.AddInt64(&errorsReturned, 1)
				}
			}
		}(idx, wType)
	}

	// Release all workers simultaneously
	close(startBarrier)

	// Wait with a timeout to detect any deadlock
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Succeeded in completing all 60 concurrent workers
	case <-time.After(20 * time.Second):
		t.Fatal("DEADLOCK DETECTED: 60 concurrent workers timed out after 20 seconds on SetMaxOpenConns(1)")
	}

	// Verify counts
	if commitsSucceeded != wantCommitted {
		t.Errorf("committed transactions = %d, want %d", commitsSucceeded, wantCommitted)
	}
	if panicsRecovered != wantPanicked {
		t.Errorf("panics recovered = %d, want %d", panicsRecovered, wantPanicked)
	}
	if errorsReturned != wantErrored {
		t.Errorf("errors returned = %d, want %d", errorsReturned, wantErrored)
	}

	// Verify database rows: ONLY the 20 committed projects and events must exist
	var dbProjectCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id LIKE 'stress-proj-%'`).Scan(&dbProjectCount); err != nil {
		t.Fatalf("query db projects: %v", err)
	}
	if dbProjectCount != wantCommitted {
		t.Fatalf("DB project count = %d, want exactly %d committed projects", dbProjectCount, wantCommitted)
	}

	var dbEventCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE id LIKE 'stress-ev-%'`).Scan(&dbEventCount); err != nil {
		t.Fatalf("query db events: %v", err)
	}
	if dbEventCount != wantCommitted {
		t.Fatalf("DB event count = %d, want exactly %d committed events", dbEventCount, wantCommitted)
	}

	// Verify EventPublisher received ONLY the 20 committed events
	if pub.Count() != wantCommitted {
		t.Fatalf("Publisher received %d events, want exactly %d", pub.Count(), wantCommitted)
	}

	// Post-stress verification: run 10 rapid sequential transactions to verify pool is completely pristine
	for i := 0; i < 10; i++ {
		seqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		seqPID := domain.ID(fmt.Sprintf("post-stress-%d", i))
		var r application.Repos
		err := uow.Run(seqCtx, &r, func() error {
			return r.Projects.Create(seqCtx, domain.Project{
				ID:        seqPID,
				Name:      fmt.Sprintf("Post Stress %d", i),
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			})
		})
		cancel()
		if err != nil {
			t.Fatalf("post-stress sequential tx %d failed: %v", i, err)
		}
	}
}

// TestAdversarial_HighScale_200ConcurrentWorkers stresses the single-connection pool
// under high concurrency (200 goroutines) with a chaotic mix of panics, errors, and commits.
func TestAdversarial_HighScale_200ConcurrentWorkers(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pub := &safeEventPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	const totalWorkers = 200
	const wantCommitted = 80
	const wantStringPanic = 40
	const wantStructPanic = 40
	const wantNilPanic = 20
	const wantErrorReturn = 20

	var (
		panicsRecovered  int64
		errorsReturned   int64
		commitsSucceeded int64
		wg               sync.WaitGroup
		startBarrier     = make(chan struct{})
	)

	type workerType int
	const (
		wCommit workerType = iota
		wStringPanic
		wStructPanic
		wNilPanic
		wErrorReturn
	)

	var workerPlan []workerType
	for i := 0; i < wantCommitted; i++ {
		workerPlan = append(workerPlan, wCommit)
	}
	for i := 0; i < wantStringPanic; i++ {
		workerPlan = append(workerPlan, wStringPanic)
	}
	for i := 0; i < wantStructPanic; i++ {
		workerPlan = append(workerPlan, wStructPanic)
	}
	for i := 0; i < wantNilPanic; i++ {
		workerPlan = append(workerPlan, wNilPanic)
	}
	for i := 0; i < wantErrorReturn; i++ {
		workerPlan = append(workerPlan, wErrorReturn)
	}

	if len(workerPlan) != totalWorkers {
		t.Fatalf("worker plan count %d != %d", len(workerPlan), totalWorkers)
	}

	for idx, wType := range workerPlan {
		wg.Add(1)
		go func(workerID int, wt workerType) {
			defer wg.Done()
			<-startBarrier

			workerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()

			pID := domain.ID(fmt.Sprintf("scale-proj-%03d", workerID))
			eID := domain.ID(fmt.Sprintf("scale-ev-%03d", workerID))

			switch wt {
			case wCommit:
				var r application.Repos
				err := uow.Run(workerCtx, &r, func() error {
					if err := r.Projects.Create(workerCtx, domain.Project{
						ID:        pID,
						Name:      fmt.Sprintf("Scale Project %d", workerID),
						CreatedAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					}); err != nil {
						return err
					}
					return r.Events.Append(workerCtx, domain.Event{
						ID:        eID,
						ProjectID: &pID,
						Actor:     domain.Actor{Type: domain.ActorAgent, Name: "scale"},
						Kind:      domain.EventCreated,
						At:        time.Now().UTC(),
					})
				})
				if err != nil {
					t.Errorf("worker %d commit failed: %v", workerID, err)
					return
				}
				atomic.AddInt64(&commitsSucceeded, 1)

			case wStringPanic:
				func() {
					defer func() {
						if r := recover(); r != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      "Panic",
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						panic("string panic")
					})
				}()

			case wStructPanic:
				func() {
					defer func() {
						if r := recover(); r != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      "Panic",
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						panic(customPanicStruct{Code: workerID, Msg: "struct panic"})
					})
				}()

			case wNilPanic:
				func() {
					defer func() {
						if r := recover(); r != nil {
							atomic.AddInt64(&panicsRecovered, 1)
						}
					}()
					var r application.Repos
					_ = uow.Run(workerCtx, &r, func() error {
						_ = r.Projects.Create(workerCtx, domain.Project{
							ID:        pID,
							Name:      "Panic",
							CreatedAt: time.Now().UTC(),
							UpdatedAt: time.Now().UTC(),
						})
						panic(nil)
					})
				}()

			case wErrorReturn:
				var r application.Repos
				err := uow.Run(workerCtx, &r, func() error {
					_ = r.Projects.Create(workerCtx, domain.Project{
						ID:        pID,
						Name:      "Error",
						CreatedAt: time.Now().UTC(),
						UpdatedAt: time.Now().UTC(),
					})
					return errors.New("aborted")
				})
				if err != nil {
					atomic.AddInt64(&errorsReturned, 1)
				}
			}
		}(idx, wType)
	}

	close(startBarrier)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Succeeded
	case <-time.After(30 * time.Second):
		t.Fatal("DEADLOCK DETECTED: 200 concurrent transactions timed out")
	}

	if commitsSucceeded != wantCommitted {
		t.Fatalf("commits = %d, want %d", commitsSucceeded, wantCommitted)
	}
	if panicsRecovered != (wantStringPanic + wantStructPanic + wantNilPanic) {
		t.Fatalf("panics recovered = %d, want %d", panicsRecovered, wantStringPanic+wantStructPanic+wantNilPanic)
	}
	if errorsReturned != wantErrorReturn {
		t.Fatalf("errors returned = %d, want %d", errorsReturned, wantErrorReturn)
	}

	// Verify exact count of projects in DB
	var dbCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id LIKE 'scale-proj-%'`).Scan(&dbCount); err != nil {
		t.Fatalf("query db count: %v", err)
	}
	if dbCount != wantCommitted {
		t.Fatalf("db projects count = %d, want %d", dbCount, wantCommitted)
	}

	// Verify publisher events
	if pub.Count() != wantCommitted {
		t.Fatalf("publisher count = %d, want %d", pub.Count(), wantCommitted)
	}
}

// TestAdversarial_ContextCancellation verifies that canceling a context while inside
// or before uow.Run cleanly releases the connection and leaves no zombie state.
func TestAdversarial_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	// Pre-canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	var r application.Repos
	err := uow.Run(canceledCtx, &r, func() error {
		return r.Projects.Create(canceledCtx, domain.Project{
			ID:        domain.ID("canceled-proj"),
			Name:      "Canceled",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
	})
	if err == nil {
		t.Fatal("expected error with pre-canceled context, got nil")
	}

	// Verify subsequent transaction succeeds immediately on same DB handle
	subCtx, cancelSub := context.WithTimeout(ctx, 2*time.Second)
	defer cancelSub()

	var rSub application.Repos
	err = uow.Run(subCtx, &rSub, func() error {
		return rSub.Projects.Create(subCtx, domain.Project{
			ID:        domain.ID("post-cancel-proj"),
			Name:      "Post Cancel Success",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
	})
	if err != nil {
		t.Fatalf("subsequent transaction after context cancel failed: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = 'post-cancel-proj'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}
