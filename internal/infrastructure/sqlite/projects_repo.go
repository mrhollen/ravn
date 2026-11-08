package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// projectRepo implements application.ProjectRepository over SQLite: raw
// database/sql, explicit column lists, hand-rolled scanning (ARCHITECTURE §4).
type projectRepo struct {
	q querier
}

func newProjectRepo(q querier) *projectRepo { return &projectRepo{q: q} }

var _ application.ProjectRepository = (*projectRepo)(nil)

// projectColumns is the single source of truth for SELECT column order — it
// must stay aligned with scanProject's Scan targets. created_by (migration 0011)
// is trailing so legacy rows scan as NULL → domain.CreatedBy stays nil.
const projectColumns = "id, name, description, opencode_server_url, opencode_directory, opencode_model, opencode_agent, opencode_variant, archived, created_at, updated_at, created_by"

// Create stores a new project row exactly as given (IDs and timestamps are
// minted/stamped by the use case). A duplicate name trips the UNIQUE
// constraint and surfaces as a wrapped domain.ErrInvalid per the port contract.
func (r *projectRepo) Create(ctx context.Context, p domain.Project) error {
	archivedInt := 0
	if p.Archived {
		archivedInt = 1
	}
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO project (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(p.ID), p.Name, p.Description, p.OpenCodeServerURL, p.OpenCodeDirectory, p.OpenCodeModel, p.OpenCodeAgent, p.OpenCodeVariant, archivedInt, formatTS(p.CreatedAt), formatTS(p.UpdatedAt), idOrNil(p.CreatedBy))
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

// List returns all projects matching filter ordered by created_at ASC (id ASC tie-break).
func (r *projectRepo) List(ctx context.Context, f ...application.ProjectFilter) ([]domain.Project, error) {
	archivedInt := 0
	if len(f) > 0 && f[0].Archived {
		archivedInt = 1
	}
	where := "archived = ?"
	args := []any{archivedInt}
	if len(f) > 0 { // optional narrowing — the zero-value filter adds NOTHING, so default-mode queries are unchanged
		if f[0].Creator != nil { // ?mine=created: exact created_by match (a zero ID matches nothing — legacy rows are NULL)
			where += " AND created_by = ?"
			args = append(args, string(*f[0].Creator))
		}
		if f[0].Visibility != nil { // membership-scoped visibility: owner OR stored member (epic T4)
			where += " AND (" + projectVisibleExpr + ")"
			args = append(args, string(f[0].Visibility.IdentityID), string(f[0].Visibility.IdentityID))
		}
	}
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+projectColumns+` FROM project WHERE `+where+` ORDER BY created_at ASC, id ASC`, args...)
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
	archivedInt := 0
	if p.Archived {
		archivedInt = 1
	}
	res, err := r.q.ExecContext(ctx,
		`UPDATE project SET name = ?, description = ?, opencode_server_url = ?, opencode_directory = ?, opencode_model = ?, opencode_agent = ?, opencode_variant = ?, archived = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.OpenCodeServerURL, p.OpenCodeDirectory, p.OpenCodeModel, p.OpenCodeAgent, p.OpenCodeVariant, archivedInt, formatTS(p.UpdatedAt), string(p.ID))
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
		p                 domain.Project
		description       string
		opencodeServerURL string
		opencodeDirectory string
		opencodeModel     string
		opencodeAgent     string
		opencodeVariant   string
		archived          int
		createdAt         string
		updatedAt         string
		createdBy         sql.NullString // NULL for every legacy row → unattributed, never an error
	)
	if err := s.Scan(&p.ID, &p.Name, &description, &opencodeServerURL, &opencodeDirectory, &opencodeModel, &opencodeAgent, &opencodeVariant, &archived, &createdAt, &updatedAt, &createdBy); err != nil {
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
	if createdBy.Valid && createdBy.String != "" { // malformed non-NULL is impossible (FK), but a stray '' stays unattributed
		cb := domain.ID(createdBy.String)
		p.CreatedBy = &cb
	}
	p.Description = description
	p.OpenCodeServerURL = opencodeServerURL
	p.OpenCodeDirectory = opencodeDirectory
	p.OpenCodeModel = opencodeModel
	p.OpenCodeAgent = opencodeAgent
	p.OpenCodeVariant = opencodeVariant
	p.Archived = archived != 0
	p.CreatedAt = created
	p.UpdatedAt = updated
	return p, nil
}
