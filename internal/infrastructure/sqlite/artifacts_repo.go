package sqlite

import (
	"context"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// artifactRepo will implement application.ArtifactRepository over SQLite
// (slice 2C). Slice 2A wires the type into Repos only; note the `other` kind
// escape hatch is already available in the schema (migration 0002).
type artifactRepo struct {
	q querier
}

func newArtifactRepo(q querier) *artifactRepo { return &artifactRepo{q: q} }

var _ application.ArtifactRepository = (*artifactRepo)(nil)

func (r *artifactRepo) Add(ctx context.Context, a domain.Artifact) error {
	return notYet("Add", "2C")
}

func (r *artifactRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Artifact, error) {
	return nil, notYet("ListByTask", "2C")
}
