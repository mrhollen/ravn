package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// SQLiteUnitOfWork implements application.UnitOfWork over a single SQLite
// handle. Run begins one transaction, binds every repository in *Repos to it,
// invokes fn, then commits on success or rolls back on failure. Reads and
// writes both go through it (MVP uniformity).
//
// The pool is pinned to one connection (SetMaxOpenConns(1), db.go), so a live
// transaction serializes everything behind it — at crowban's QPS that is the
// intended single-writer behavior, not a bug.
type SQLiteUnitOfWork struct {
	db        *sql.DB
	publisher application.EventPublisher
}

// UOWOption configures a SQLiteUnitOfWork instance.
type UOWOption func(*SQLiteUnitOfWork)

// WithPublisher attaches an application.EventPublisher to the unit of work.
// Any domain events appended during a transaction are broadcast to the publisher
// immediately after the transaction commits successfully.
func WithPublisher(pub application.EventPublisher) UOWOption {
	return func(u *SQLiteUnitOfWork) {
		u.publisher = pub
	}
}

// NewUnitOfWork returns an application.UnitOfWork bound to db (already open and
// migrated; see Open).
func NewUnitOfWork(db *sql.DB, opts ...UOWOption) *SQLiteUnitOfWork {
	u := &SQLiteUnitOfWork{db: db}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

var _ application.UnitOfWork = (*SQLiteUnitOfWork)(nil)

type eventStoreRecordingWrapper struct {
	application.EventStore
	onAppend func(domain.Event)
}

func (w *eventStoreRecordingWrapper) Append(ctx context.Context, e domain.Event) error {
	if err := w.EventStore.Append(ctx, e); err != nil {
		return err
	}
	w.onAppend(e)
	return nil
}

// Run implements application.UnitOfWork. The same pattern applies to every
// field of r: implementations must replace it with a transaction-bound
// instance before calling fn, which the use case's closure captures through
// its *Repos parameter.
func (u *SQLiteUnitOfWork) Run(ctx context.Context, r *application.Repos, fn func() error) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	committed := false
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if !committed {
			_ = tx.Rollback()
		}
	}()

	r.Projects = newProjectRepo(tx)
	r.Tasks = newTaskRepo(tx)
	r.ACs = newAcceptanceCriterionRepo(tx)
	r.Comments = newCommentRepo(tx)
	r.Artifacts = newArtifactRepo(tx)
	r.Relations = newRelationRepo(tx)

	var committedEvents []domain.Event
	eventStore := newEventStore(tx)
	if u.publisher != nil {
		r.Events = &eventStoreRecordingWrapper{
			EventStore: eventStore,
			onAppend: func(e domain.Event) {
				committedEvents = append(committedEvents, e)
			},
		}
	} else {
		r.Events = eventStore
	}

	err = fn()
	if err == nil {
		if cerr := tx.Commit(); cerr != nil {
			return fmt.Errorf("commit: %w", cerr)
		}
		committed = true
		if u.publisher != nil && len(committedEvents) > 0 {
			for _, e := range committedEvents {
				u.publisher.Publish(e)
			}
		}
		return nil
	}
	return err
}
