package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// ProjectUpdate carries the fields a caller wants to change on an existing
// project. Pointer semantics: nil = leave unchanged; a non-nil Name must be
// non-empty after trimming (else domain.ErrInvalid). An update with no fields
// set is domain.ErrInvalid — there is nothing to write and no event to emit.
type ProjectUpdate struct {
	Name        *string // rename (unique constraint enforced by the repository)
	Description *string // full markdown replace; empty string allowed
}

// Projects implements the project use cases: create, get, list, update. It
// depends only on the ports in this package (dependency inversion); wiring a
// concrete UnitOfWork happens in the cmd/* composition roots.
type Projects struct {
	uow   UnitOfWork
	clock Clock
	idgen IDGenerator
}

// NewProjects returns the Projects use case bound to uow, clock, and idgen.
// Use cases mint IDs and stamp timestamps before calling ports (ADR); adapters
// store values exactly as given.
func NewProjects(uow UnitOfWork, clock Clock, idgen IDGenerator) *Projects {
	return &Projects{uow: uow, clock: clock, idgen: idgen}
}

// Create stores a new project and appends its `created` event in the same
// unit of work. The caller's actor is recorded on the event; name must be
// non-empty (else domain.ErrInvalid); duplicate names surface as a wrapped
// domain.ErrInvalid from the repository.
func (s *Projects) Create(ctx context.Context, actor domain.Actor, name, description string) (domain.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Project{}, fmt.Errorf("%w: project name must not be empty", domain.ErrInvalid)
	}

	now := s.clock.Now()
	p := domain.Project{
		ID:          s.idgen.NewID(),
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	ev := domain.Event{
		ID:        s.idgen.NewID(),
		ProjectID: &p.ID, // TaskID nil: pure project-level event
		Actor:     actor,
		Kind:      domain.EventCreated,
		After:     compactJSON(map[string]string{"name": p.Name, "description": p.Description}),
		At:        now,
	}

	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		if err := r.Projects.Create(ctx, p); err != nil {
			return err
		}
		return r.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Project{}, err
	}
	return p, nil // adapters store values as-given; p is the stored row
}

// Get returns the project with id or domain.ErrNotFound. Reads run through the
// unit of work too (MVP uniformity).
func (s *Projects) Get(ctx context.Context, id domain.ID) (domain.Project, error) {
	var stored domain.Project
	var repos Repos
	err := s.uow.Run(ctx, &repos, func() error {
		var err error
		stored, err = repos.Projects.Get(ctx, id)
		return err
	})
	if err != nil {
		return domain.Project{}, err
	}
	return stored, nil
}

// List returns every project in created_at ASC order (repository-defined).
func (s *Projects) List(ctx context.Context) ([]domain.Project, error) {
	var out []domain.Project
	var repos Repos
	err := s.uow.Run(ctx, &repos, func() error {
		var err error
		out, err = repos.Projects.List(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update applies a partial field change to an existing project and appends an
// `updated` event in the same unit of work. Before/After JSON carry only the
// changed fields. Unknown id → domain.ErrNotFound; no fields set or empty new
// name → domain.ErrInvalid.
func (s *Projects) Update(ctx context.Context, actor domain.Actor, id domain.ID, u ProjectUpdate) (domain.Project, error) {
	newName, hasName := "", false
	if u.Name != nil {
		newName = strings.TrimSpace(*u.Name)
		if newName == "" {
			return domain.Project{}, fmt.Errorf("%w: project name must not be empty", domain.ErrInvalid)
		}
		hasName = true
	}
	newDesc, hasDesc := "", false
	if u.Description != nil {
		newDesc, hasDesc = *u.Description, true
	}
	if !hasName && !hasDesc {
		return domain.Project{}, fmt.Errorf("%w: no project fields to update", domain.ErrInvalid)
	}

	now := s.clock.Now()
	evID := s.idgen.NewID()
	var stored domain.Project

	var before, after = map[string]string{}, map[string]string{}
	var repos Repos
	err := s.uow.Run(ctx, &repos, func() error {
		p, err := repos.Projects.Get(ctx, id)
		if err != nil {
			return err
		}
		if hasName {
			before["name"] = p.Name
			after["name"] = newName
			p.Name = newName
		}
		if hasDesc {
			before["description"] = p.Description
			after["description"] = newDesc
			p.Description = newDesc
		}
		p.UpdatedAt = now
		ev := domain.Event{
			ID:        evID,
			ProjectID: &p.ID,
			Actor:     actor,
			Kind:      domain.EventUpdated,
			Before:    compactJSON(before),
			After:     compactJSON(after),
			At:        now,
		}
		if err := repos.Projects.Update(ctx, p); err != nil {
			return err
		}
		stored = p
		return repos.Events.Append(ctx, ev)
	})
	if err != nil {
		return domain.Project{}, err
	}
	return stored, nil
}

// compactJSON renders a changed-values map as the single-line JSON strings
// stored in event.before_json / event.after_json. json here is pure data
// shaping, not I/O; Marshal of these map[string]string inputs cannot fail.
func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
