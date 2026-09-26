package sqlite

import (
	"context"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// artifactRepo implements application.ArtifactRepository over SQLite. Slice 2B
// lands the ListByTask read (the resume payload's evidence list); Add lands in
// slice 2C. The `other` kind escape hatch is already in the schema (0002).
type artifactRepo struct {
	q querier
}

func newArtifactRepo(q querier) *artifactRepo { return &artifactRepo{q: q} }

var _ application.ArtifactRepository = (*artifactRepo)(nil)

func (r *artifactRepo) Add(ctx context.Context, a domain.Artifact) error {
	return notYet("artifact repository", "Add", "2C")
}

// artifactColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanArtifact's Scan targets.
const artifactColumns = "id, task_id, kind, ref, summary, actor_type, actor_name, created_at"

// ListByTask returns all artifacts ordered by created_at ASC (rowid ASC breaks
// same-timestamp ties in insertion order) — the resume payload's evidence list.
func (r *artifactRepo) ListByTask(ctx context.Context, taskID domain.ID) ([]domain.Artifact, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+artifactColumns+` FROM artifact WHERE task_id = ? ORDER BY created_at ASC, rowid ASC`,
		string(taskID))
	if err != nil {
		return nil, fmt.Errorf("list artifacts for task %s: %w", taskID, err)
	}
	defer rows.Close()

	out := make([]domain.Artifact, 0)
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact row: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scanArtifact maps one row (in artifactColumns order) to a domain.Artifact.
func scanArtifact(s scanner) (domain.Artifact, error) {
	var (
		a         domain.Artifact
		taskID    string
		kind      string
		ref       string
		summary   string
		actorType string
		actorName string
		createdAt string
	)
	if err := s.Scan(&a.ID, &taskID, &kind, &ref, &summary, &actorType, &actorName, &createdAt); err != nil {
		return domain.Artifact{}, err
	}
	a.TaskID = domain.ID(taskID)
	a.Kind = domain.ArtifactKind(kind)
	a.Ref = ref
	a.Summary = summary
	a.Actor = domain.Actor{Type: domain.ActorType(actorType), Name: actorName}
	at, err := parseTS(createdAt)
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("artifact %s created_at %q: %w", a.ID, createdAt, err)
	}
	a.CreatedAt = at
	return a, nil
}
