package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// projectRepo implements application.ProjectRepository over SQLite: raw
// database/sql, explicit column lists, hand-rolled scanning (ARCHITECTURE §4).
type projectRepo struct {
	q querier
}

func newProjectRepo(q querier) *projectRepo { return &projectRepo{q: q} }

var _ application.ProjectRepository = (*projectRepo)(nil)

// projectColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanProject's Scan targets.
const projectColumns = "id, name, description, created_at, updated_at"

// Create stores a new project row exactly as given (IDs and timestamps are
// minted/stamped by the use case). A duplicate name trips the UNIQUE
// constraint and surfaces as a wrapped domain.ErrInvalid per the port contract.
func (r *projectRepo) Create(ctx context.Context, p domain.Project) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO project (`+projectColumns+`) VALUES (?, ?, ?, ?, ?)`,
		string(p.ID), p.Name, p.Description, formatTS(p.CreatedAt), formatTS(p.UpdatedAt))
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: project name %q already exists", domain.ErrInvalid, p.Name)
		}
		return fmt.Errorf("insert project %s: %w", p.ID, err)
	}
	return nil
}

// Get returns the project with id or domain.ErrNotFound.
func (r *projectRepo) Get(ctx context.Context, id domain.ID) (domain.Project, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM project WHERE id = ?`, string(id))
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, domain.ErrNotFound
	}
	return p, err
}

// List returns all projects ordered by created_at ASC (id ASC tie-break).
func (r *projectRepo) List(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+projectColumns+` FROM project ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project row: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update renames and/or rewrites the description. Not version-guarded — the
// project row has no version column (§4 DDL); zero affected rows means the id
// does not exist (domain.ErrNotFound).
func (r *projectRepo) Update(ctx context.Context, p domain.Project) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE project SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, formatTS(p.UpdatedAt), string(p.ID))
	if err != nil {
		if isConstraintViolation(err) {
			return fmt.Errorf("%w: project name %q already exists", domain.ErrInvalid, p.Name)
		}
		return fmt.Errorf("update project %s: %w", p.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected after update project %s: %w", p.ID, err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// scanner is the one-method view of *sql.Row and *sql.Rows that scanProject
// needs; both types implement it.
type scanner interface{ Scan(dest ...any) error }

func scanProject(s scanner) (domain.Project, error) {
	var (
		p           domain.Project
		description string
		createdAt   string
		updatedAt   string
	)
	if err := s.Scan(&p.ID, &p.Name, &description, &createdAt, &updatedAt); err != nil {
		return domain.Project{}, err
	}
	created, err := parseTS(createdAt)
	if err != nil {
		return domain.Project{}, fmt.Errorf("project %s created_at %q: %w", p.ID, createdAt, err)
	}
	updated, err := parseTS(updatedAt)
	if err != nil {
		return domain.Project{}, fmt.Errorf("project %s updated_at %q: %w", p.ID, updatedAt, err)
	}
	p.Description = description
	p.CreatedAt = created
	p.UpdatedAt = updated
	return p, nil
}
