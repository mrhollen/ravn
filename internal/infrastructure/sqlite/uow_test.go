package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// TestUnitOfWork_Panic_RollsBackTransactionAndReRaises verifies Requirement R3:
// When the work callback panics:
//  1. The transaction is cleanly rolled back (no rows committed to SQLite).
//  2. The panic is re-raised with the original value preserved.
//  3. The single-connection pool (SetMaxOpenConns(1)) is not deadlocked, and subsequent
//     transactions and queries succeed immediately.
func TestUnitOfWork_Panic_RollsBackTransactionAndReRaises(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	const panicVal = "deliberate test panic inside transaction"

	var recoveredVal any
	func() {
		defer func() {
			recoveredVal = recover()
		}()

		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			p := domain.Project{
				ID:        "panic-test-project",
				Name:      "Panic Test Project",
				CreatedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
			}
			if err := r.Projects.Create(ctx, p); err != nil {
				return err
			}
			ev := domain.Event{
				ID:        "panic-test-event",
				ProjectID: &p.ID,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
				Kind:      domain.EventCreated,
				At:        p.CreatedAt,
			}
			if err := r.Events.Append(ctx, ev); err != nil {
				return err
			}
			panic(panicVal)
		})
	}()

	// 1. Verify panic was re-raised with identical payload.
	if recoveredVal == nil {
		t.Fatal("expected panic to be re-raised by uow.Run, but recovered nil")
	}
	if recoveredVal != panicVal {
		t.Fatalf("recovered panic = %v (%T), want %v (%T)", recoveredVal, recoveredVal, panicVal, panicVal)
	}

	// 2. Verify all writes inside the panicked transaction were rolled back.
	var projCount, eventCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = 'panic-test-project'`).Scan(&projCount); err != nil {
		t.Fatalf("query project count: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE id = 'panic-test-event'`).Scan(&eventCount); err != nil {
		t.Fatalf("query event count: %v", err)
	}
	if projCount != 0 || eventCount != 0 {
		t.Fatalf("panicked transaction leaked uncommitted rows: project=%d event=%d, want 0/0", projCount, eventCount)
	}

	// 3. Verify single-connection pool is NOT deadlocked and remains fully usable.
	// Use a 2-second timeout to fail quickly if connection was leaked and deadlock occurs.
	subsequentCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var r2 application.Repos
	err := uow.Run(subsequentCtx, &r2, func() error {
		p := domain.Project{
			ID:        "post-panic-project",
			Name:      "Post Panic Project",
			CreatedAt: time.Date(2026, 9, 29, 0, 5, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 9, 29, 0, 5, 0, 0, time.UTC),
		}
		return r2.Projects.Create(subsequentCtx, p)
	})
	if err != nil {
		t.Fatalf("subsequent uow.Run failed (possible deadlock/busy lock): %v", err)
	}

	var postCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project WHERE id = 'post-panic-project'`).Scan(&postCount); err != nil || postCount != 1 {
		t.Fatalf("post-panic project row not found (count=%d, err=%v)", postCount, err)
	}
}

// TestUnitOfWork_Panic_TypedErrorPanic verifies that typed errors passed to panic
// are preserved and re-thrown without loss of type information.
func TestUnitOfWork_Panic_TypedErrorPanic(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	expectedErr := errors.New("custom panic error")

	var recoveredVal any
	func() {
		defer func() {
			recoveredVal = recover()
		}()

		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			panic(expectedErr)
		})
	}()

	if recoveredVal == nil {
		t.Fatal("expected panic, got nil")
	}
	err, ok := recoveredVal.(error)
	if !ok || !errors.Is(err, expectedErr) {
		t.Fatalf("recovered value %v (%T) is not the expected error %v", recoveredVal, recoveredVal, expectedErr)
	}
}

// TestUnitOfWork_Panic_WithPublisher_SuppressesEvents verifies that when a panic occurs,
// any domain events queued for publication are discarded and never published.
func TestUnitOfWork_Panic_WithPublisher_SuppressesEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	pub := &mockPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	pID := domain.ID("panic-pub-proj")
	func() {
		defer func() {
			_ = recover()
		}()

		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			_ = r.Events.Append(ctx, domain.Event{
				ID:        domain.ID("panic-pub-event"),
				ProjectID: &pID,
				Actor:     domain.Actor{Type: domain.ActorAgent, Name: "tester"},
				Kind:      domain.EventCreated,
				At:        time.Now().UTC(),
			})
			panic("panic before commit")
		})
	}()

	if len(pub.events) != 0 {
		t.Fatalf("publisher received %d events on panic, want 0", len(pub.events))
	}
}

// TestUnitOfWork_SubsequentTransactionsConcurrent verifies that after a panic,
// multiple concurrent transactions can queue and execute sequentially without deadlock.
func TestUnitOfWork_SubsequentTransactionsConcurrent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	uow := NewUnitOfWork(db)

	// Induce panic
	func() {
		defer func() { _ = recover() }()
		var r application.Repos
		_ = uow.Run(ctx, &r, func() error {
			panic("abort")
		})
	}()

	// Run 5 goroutines trying to execute transactions sequentially on the single-conn pool
	const workers = 5
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			var r application.Repos
			err := uow.Run(timeoutCtx, &r, func() error {
				p := domain.Project{
					ID:        domain.ID(fmt.Sprintf("proj-%d-%d", time.Now().UnixNano(), idx)),
					Name:      fmt.Sprintf("Concurrent Project %d", idx),
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				}
				return r.Projects.Create(timeoutCtx, p)
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent transaction after panic failed: %v", err)
	}
}
