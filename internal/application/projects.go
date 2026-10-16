package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mrhollen/crowban/internal/domain"
)

// ProjectUpdate carries the fields a caller wants to change on an existing
// project. Pointer semantics: nil = leave unchanged; a non-nil Name must be
// non-empty after trimming (else domain.ErrInvalid). An update with no fields
// set is domain.ErrInvalid — there is nothing to write and no event to emit.
type ProjectUpdate struct {
	Name              *string // rename (unique constraint enforced by the repository)
	Description       *string // full markdown replace; empty string allowed
	OpenCodeServerURL *string // optional OpenCode server URL; empty string allowed to clear
	OpenCodeDirectory *string // optional OpenCode worktree directory; empty string allowed to clear
	OpenCodeModel     *string // optional OpenCode model "providerID/modelID"; empty string allowed to clear
	Archived          *bool   // optional archive state
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
// unit of work. The acting actor is validated and normalized BEFORE anything
// else (domain.Actor.Validate via normalizeActor): an invalid principal never
// reaches the event log. name must be non-empty and at most
// domain.MaxProjectNameLen runes (else domain.ErrInvalid); the whole entity is
// gated by domain.Project.Validate before any write. Duplicate names surface
// as a wrapped domain.ErrInvalid from the repository.
func (s *Projects) Create(ctx context.Context, actor domain.Actor, name, description string) (domain.Project, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return domain.Project{}, err
	}
	name = strings.TrimSpace(name)

	now := s.clock.Now()
	p := domain.Project{
		ID:          s.idgen.NewID(),
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := p.Validate(); err != nil {
		return domain.Project{}, err
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
	err = s.uow.Run(ctx, &r, func() error {
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

// List returns every project matching filter in created_at ASC order (repository-defined).
func (s *Projects) List(ctx context.Context, f ...ProjectFilter) ([]domain.Project, error) {
	var out []domain.Project
	var repos Repos
	err := s.uow.Run(ctx, &repos, func() error {
		var err error
		out, err = repos.Projects.List(ctx, f...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectRow pairs a project with its open task counts per canonical status
// (the GET /projects row shape, ARCHITECTURE §5). Open = non-archived;
// completed tasks count under "done". Every canonical status is always present
// in OpenTasks, zero when empty.
type ProjectRow struct {
	Project   domain.Project
	OpenTasks map[domain.Status]int64
}

// ListWithCounts returns every project (created_at ASC) matching filter with its per-status
// open task counts. The snapshot is gathered in ONE unit of work so it is
// internally consistent: projects come from ProjectRepository and the dense
// (project, status) aggregates from TaskRepository.OpenTaskCounts — a single
// grouped query over the task table, no N+1 fan-out per project.
func (s *Projects) ListWithCounts(ctx context.Context, f ...ProjectFilter) ([]ProjectRow, error) {
	var projects []domain.Project
	var rows []TaskCountRow
	var r Repos
	err := s.uow.Run(ctx, &r, func() error {
		var err error
		if projects, err = r.Projects.List(ctx, f...); err != nil {
			return err
		}
		rows, err = r.Tasks.OpenTaskCounts(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}

	byProject := make(map[domain.ID]map[domain.Status]int64, len(rows))
	for _, row := range rows {
		m := byProject[row.ProjectID]
		if m == nil {
			m = make(map[domain.Status]int64)
			byProject[row.ProjectID] = m
		}
		m[row.Status] += row.Count
	}

	out := make([]ProjectRow, 0, len(projects))
	for _, p := range projects {
		counts := make(map[domain.Status]int64, len(domain.CanonicalStatuses()))
		for _, st := range domain.CanonicalStatuses() {
			counts[st] = 0 // always present, zero when empty (wire shape is fixed)
		}
		for st, n := range byProject[p.ID] {
			counts[st] = n
		}
		out = append(out, ProjectRow{Project: p, OpenTasks: counts})
	}
	return out, nil
}

// Update applies a partial field change to an existing project and appends an
// `updated` event in the same unit of work. Before/After JSON carry only the
// changed fields. The acting actor is validated and normalized BEFORE anything
// else (domain.Actor.Validate via normalizeActor): an invalid principal never
// reaches the event log or triggers a write. Every incoming field value is
// shape-validated BEFORE any write (name via domain.ValidateProjectName;
// OpenCodeServerURL/OpenCodeDirectory via their security-aware application
// validators; OpenCodeModel via domain.ValidateOpenCodeModel, which mirrors
// parseOpenCodeModel exactly) — only the CHANGED values are checked, so legacy
// rows with pre-invariant stored values stay mutable without a rewrite.
// Unknown id → domain.ErrNotFound; no fields set, empty or over-long new name,
// invalid OpenCode value, or invalid actor → domain.ErrInvalid.
func (s *Projects) Update(ctx context.Context, actor domain.Actor, id domain.ID, u ProjectUpdate) (domain.Project, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return domain.Project{}, err
	}
	newName, hasName := "", false
	if u.Name != nil {
		newName = strings.TrimSpace(*u.Name)
		if err := domain.ValidateProjectName(newName); err != nil {
			return domain.Project{}, err
		}
		hasName = true
	}
	newDesc, hasDesc := "", false
	if u.Description != nil {
		newDesc, hasDesc = *u.Description, true
	}
	newServerURL, hasServerURL := "", false
	if u.OpenCodeServerURL != nil {
		newServerURL, hasServerURL = strings.TrimSpace(*u.OpenCodeServerURL), true
		if newServerURL != "" {
			if err := ValidateOpenCodeServerURL(newServerURL); err != nil {
				return domain.Project{}, err
			}
		}
	}
	newDir, hasDir := "", false
	if u.OpenCodeDirectory != nil {
		newDir, hasDir = strings.TrimSpace(*u.OpenCodeDirectory), true
		if newDir != "" {
			if err := ValidateOpenCodeDirectory(newDir); err != nil {
				return domain.Project{}, err
			}
		}
	}
	newModel, hasModel := "", false
	if u.OpenCodeModel != nil {
		newModel, hasModel = strings.TrimSpace(*u.OpenCodeModel), true
		// Mirrors parseOpenCodeModel (internal/interface/http): "providerID/modelID"
		// with a first slash and both halves non-empty; empty clears to server default.
		if err := domain.ValidateOpenCodeModel(newModel); err != nil {
			return domain.Project{}, err
		}
	}
	newArchived, hasArchived := false, false
	if u.Archived != nil {
		newArchived, hasArchived = *u.Archived, true
	}
	if !hasName && !hasDesc && !hasServerURL && !hasDir && !hasModel && !hasArchived {
		return domain.Project{}, fmt.Errorf("%w: no project fields to update", domain.ErrInvalid)
	}

	now := s.clock.Now()
	evID := s.idgen.NewID()
	var stored domain.Project

	var before, after = map[string]string{}, map[string]string{}
	var repos Repos
	err = s.uow.Run(ctx, &repos, func() error {
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
		if hasServerURL {
			before["opencode_server_url"] = p.OpenCodeServerURL
			after["opencode_server_url"] = newServerURL
			p.OpenCodeServerURL = newServerURL
		}
		if hasDir {
			before["opencode_directory"] = p.OpenCodeDirectory
			after["opencode_directory"] = newDir
			p.OpenCodeDirectory = newDir
		}
		if hasModel {
			before["opencode_model"] = p.OpenCodeModel
			after["opencode_model"] = newModel
			p.OpenCodeModel = newModel
		}
		if hasArchived {
			before["archived"] = strconv.FormatBool(p.Archived)
			after["archived"] = strconv.FormatBool(newArchived)
			p.Archived = newArchived
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
