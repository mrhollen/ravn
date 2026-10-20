package sqlite

import (
	"context"
	"fmt"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// artifactRepo implements application.ArtifactRepository over SQLite.
// Artifacts are append-only: Add stores the row exactly as given (IDs and
// timestamps are minted/stamped by the use case — ADR) and ListByTask serves
// the resume payload's evidence list. The `other` kind escape hatch is already
// in the schema (0002).
type artifactRepo struct {
	q querier
}

func newArtifactRepo(q querier) *artifactRepo { return &artifactRepo{q: q} }

var _ application.ArtifactRepository = (*artifactRepo)(nil)

// Add stores the artifact row exactly as given. The write is part of a child-
// row mutation (ARCHITECTURE §4 version-bump policy): it must never touch the
// parent task row's version or updated_at. Constraint failures — unknown task
// (FK) or an out-of-set kind (CHECK) — surface as wrapped domain.ErrInvalid;
// the use case validates both beforehand, so a violation here means concurrent
// deletion of the parent between validation and insert.
func (r *artifactRepo) Add(ctx context.Context, a domain.Artifact) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO artifact (`+artifactColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(a.ID), string(a.TaskID), string(a.Kind), a.Ref, a.Summary,
		string(a.Actor.Type), a.Actor.Name, formatTS(a.CreatedAt))
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: artifact %s violates a constraint (unknown task or bad kind)", domain.ErrInvalid, a.ID)
		}
		return fmt.Errorf("insert artifact %s: %w", a.ID, err)
	}
	return nil
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
