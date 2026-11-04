package application

import (
	"context"
	"strings"

	"github.com/mrhollen/ravn/internal/domain"
)

// Artifacts implements the task evidence-attachment use case (ARCHITECTURE §5
// POST /tasks/{id}/artifacts, MCP attach_artifact): validate the typed kind
// and the non-empty reference, confirm the parent task exists, then append the
// artifact and exactly one `artifact-added` event in a single unit of work.
// MVP evidence is a typed reference (URL or filesystem path) plus an optional
// one-line summary — no uploads (domain.Artifact, ADR 2026-09-25 for the
// `other` escape-hatch kind).
//
// Version-bump policy (ARCHITECTURE §4): attaching an artifact is a CHILD-ROW
// mutation — it touches only the artifact table plus the event log. The
// parent task row's version and updated_at are never touched, so a working
// agent holding a stale read cannot be 409'd by another agent's evidence drop.
type Artifacts struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator
}

// NewArtifacts returns the Artifacts use case bound to uow, clock, and idgen.
// The use case mints the artifact ID and stamps created_at before calling the
// repository (ADR); adapters store values exactly as given.
func NewArtifacts(uow UnitOfWork, clock Clock, idgen IDGenerator) *Artifacts {
	return &Artifacts{uow: uow, clock: clock, idgen: idgen}
}

// NewID mints a fresh artifact identifier using the configured IDGenerator.
func (s *Artifacts) NewID() domain.ID {
	return s.idgen.NewID()
}

// Attach validates kind (must be one of log|screenshot|command-output|trace|
// url|other), confirms the reference is non-empty (domain.ErrInvalid
// otherwise — a reference-less artifact carries no evidence), and confirms the
// parent task exists (domain.ErrNotFound). summary may be empty. It appends
// the artifact plus ONE `artifact-added` event whose After JSON carries kind,
// ref, and summary. The parent task's version is NOT bumped (child-row rule);
// nothing is written on validation failure.
func (s *Artifacts) Attach(ctx context.Context, actor domain.Actor, taskID domain.ID, kind domain.ArtifactKind, ref string, summary string) (domain.Artifact, error) {
	return s.AttachWithID(ctx, s.idgen.NewID(), actor, taskID, kind, ref, summary)
}

// AttachWithID is Attach with a caller-supplied artifact ID (e.g. For uploads where
// the stored file on disk is named after the minted ULID before database insertion).
// A zero-value id causes NewID to be minted.
func (s *Artifacts) AttachWithID(ctx context.Context, id domain.ID, actor domain.Actor, taskID domain.ID, kind domain.ArtifactKind, ref string, summary string) (domain.Artifact, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Artifact{}, err
	}
	ref = strings.TrimSpace(ref)
	if id.IsZero() {
		id = s.idgen.NewID()
	}

	now := s.clock.Now()
	art := domain.Artifact{
		ID:        id,
		TaskID:    taskID,
		Kind:      kind,
		Ref:       ref,
		Summary:   summary, // optional one-liner; empty allowed
		Actor:     a,
		CreatedAt: now,
	}
	if err := art.Validate(); err != nil {
		return domain.Artifact{}, err
	}

	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr // ErrNotFound for unknown tasks
		}
		if err := r.Artifacts.Add(ctx, art); err != nil {
			return err
		}
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &art.TaskID,
			Actor:     a,
			Kind:      domain.EventArtifactAdded,
			After: compactJSON(map[string]string{
				"kind":    string(kind),
				"ref":     ref,
				"summary": summary,
			}),
			At: now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	return art, nil // adapters store values as-given; artifacts are immutable after insert
}

// Remove removes the artifact with artifactID from taskID and appends an
// `artifact-removed` event recording what was deleted. Returns the removed artifact.
func (s *Artifacts) Remove(ctx context.Context, actor domain.Actor, taskID, artifactID domain.ID) (domain.Artifact, error) {
	a, err := normalizeActor(actor)
	if err != nil {
		return domain.Artifact{}, err
	}
	now := s.clock.Now()
	var removed domain.Artifact
	var r Repos
	err = s.uow.Run(ctx, &r, func() error {
		t, gerr := r.Tasks.Get(ctx, taskID)
		if gerr != nil {
			return gerr
		}
		del, derr := r.Artifacts.Delete(ctx, taskID, artifactID)
		if derr != nil {
			return derr
		}
		removed = del
		ev := domain.Event{
			ID:        s.idgen.NewID(),
			ProjectID: &t.ProjectID,
			TaskID:    &taskID,
			Actor:     a,
			Kind:      domain.EventArtifactRemoved,
			Before: compactJSON(map[string]any{
				"id":      removed.ID.String(),
				"kind":    string(removed.Kind),
				"ref":     removed.Ref,
				"summary": removed.Summary,
			}),
			At: now,
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	return removed, nil
}
