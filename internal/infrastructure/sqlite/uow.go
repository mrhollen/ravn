package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
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
	db *sql.DB
}

// NewUnitOfWork returns an application.UnitOfWork bound to db (already open and
// migrated; see Open).
func NewUnitOfWork(db *sql.DB) *SQLiteUnitOfWork {
	return &SQLiteUnitOfWork{db: db}
}

var _ application.UnitOfWork = (*SQLiteUnitOfWork)(nil)

// Run implements application.UnitOfWork. The same pattern applies to every
// field of r: implementations must replace it with a transaction-bound
// instance before calling fn, which the use case's closure captures through
// its *Repos parameter.
func (u *SQLiteUnitOfWork) Run(ctx context.Context, r *application.Repos, fn func() error) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	r.Projects = newProjectRepo(tx)
	r.Tasks = newTaskRepo(tx)
	r.ACs = newAcceptanceCriterionRepo(tx)
	r.Comments = newCommentRepo(tx)
	r.Artifacts = newArtifactRepo(tx)
	r.Relations = newRelationRepo(tx)
	r.Events = newEventStore(tx)

	err = fn()
	if err == nil {
		if cerr := tx.Commit(); cerr != nil {
			_ = tx.Rollback() // best effort; a failed commit has already killed the tx state we care about
			return fmt.Errorf("commit: %w", cerr)
		}
		return nil
	}
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
		return fmt.Errorf("%w (rollback also failed: %v)", err, rerr)
	}
	return err
}
