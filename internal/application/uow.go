package application

import "context"

// Repos bundles every repository port a use case can touch. It is the
// parameter of UnitOfWork.Run: implementations build one instance bound to the
// transaction (or connection) that this unit of work runs on, assign ALL its
// fields from it before calling fn, and hand the same pointer back to the use
// case through fn's closure capture.
type Repos struct {
	Projects  ProjectRepository
	Tasks     TaskRepository
	ACs       AcceptanceCriterionRepository
	Comments  CommentRepository
	Artifacts ArtifactRepository
	Relations RelationRepository
	Events    EventStore
}

// UnitOfWork is the transactional seam between use cases and persistence. Run
// executes fn inside ONE transaction: it commits when fn returns nil and rolls
// back when fn returns an error (the implementation's own commit/rollback
// failures are wrapped and returned). In MVP it may be used for both reads and
// writes alike — uniformity beats micro-optimizing read paths.
//
// Contract for implementations: before invoking fn, replace every field of r
// with the instance bound to this unit of work's transaction, so the closure
// inside fn sees only transactional repositories. Callers pass a zero-value
// &Repos{} (or an existing one they intend to discard afterwards).
//
// RULE: every mutation's EventStore.Append happens inside the SAME Run as the
// write it records — the data row and its event row commit or roll back
// together, so the activity log can never diverge from the data.
type UnitOfWork interface {
	Run(ctx context.Context, r *Repos, fn func() error) error
}
