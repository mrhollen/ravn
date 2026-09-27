package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

type mockPublisher struct {
	events []domain.Event
}

func (m *mockPublisher) Publish(e domain.Event) {
	m.events = append(m.events, e)
}

func TestUnitOfWork_WithPublisher_PublishesCommittedEvents(t *testing.T) {
	ctx := context.Background()
	db, closeDB, err := Open(ctx, filepath.Join(t.TempDir(), "pub-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer closeDB()

	pub := &mockPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	pID := domain.ID("01J3YV2NMB4D9C6K8F0H3L5P7Q")
	var r application.Repos
	err = uow.Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, domain.Project{
			ID:   pID,
			Name: "Published Project",
		}); err != nil {
			return err
		}
		return r.Events.Append(ctx, domain.Event{
			ID:        domain.ID("01J3YV2NMB4D9C6K8F0H3L5P7R"),
			ProjectID: &pID,
			Actor:     domain.Actor{Type: domain.ActorHuman, Name: "tester"},
			Kind:      domain.EventCreated,
			At:        time.Now().UTC(),
		})
	})
	if err != nil {
		t.Fatalf("uow.Run: %v", err)
	}

	if len(pub.events) != 1 {
		t.Fatalf("publisher received %d events, want 1", len(pub.events))
	}
	if pub.events[0].Kind != domain.EventCreated || *pub.events[0].ProjectID != pID {
		t.Errorf("publisher event = %+v", pub.events[0])
	}
}

func TestUnitOfWork_WithPublisher_DoesNotPublishOnRollback(t *testing.T) {
	ctx := context.Background()
	db, closeDB, err := Open(ctx, filepath.Join(t.TempDir(), "pub-rollback-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer closeDB()

	pub := &mockPublisher{}
	uow := NewUnitOfWork(db, WithPublisher(pub))

	pID := domain.ID("01J3YV2NMB4D9C6K8F0H3L5P7Q")
	var r application.Repos
	expectedErr := errors.New("simulated error")
	err = uow.Run(ctx, &r, func() error {
		_ = r.Events.Append(ctx, domain.Event{
			ID:        domain.ID("01J3YV2NMB4D9C6K8F0H3L5P7R"),
			ProjectID: &pID,
			Actor:     domain.Actor{Type: domain.ActorHuman, Name: "tester"},
			Kind:      domain.EventCreated,
			At:        time.Now().UTC(),
		})
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("uow.Run err = %v, want %v", err, expectedErr)
	}

	if len(pub.events) != 0 {
		t.Errorf("publisher received %d events on rollback, want 0", len(pub.events))
	}
}
