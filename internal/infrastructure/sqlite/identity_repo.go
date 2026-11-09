package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// identityRepo implements application.IdentityRepository over SQLite: the multi-user
// identity model. The read side (epic T2) resolves an identity by name and reports a
// stored membership role; the write side (T9, UpsertMember/RemoveMember/ListMembers)
// maintains the project_member rows for the Memberships use case. Credentials still
// live in the Auth store. It uses raw database/sql with an explicit column list and
// hand-rolled scanning (ARCHITECTURE §4), the same shape as every other repo here.
type identityRepo struct {
	q querier
}

func newIdentityRepo(q querier) *identityRepo { return &identityRepo{q: q} }

var _ application.IdentityRepository = (*identityRepo)(nil)

// identityColumns is the single source of truth for SELECT column order — it must
// stay aligned with scanIdentity's Scan targets. credential_hash is deliberately
// EXCLUDED (see domain.Identity): no secret ever crosses a domain boundary, so this
// query never reads it regardless of which repo method calls it.
const identityColumns = "id, name, kind, auth_method, disabled_at, created_at, last_active_at"

// GetByName resolves name to its identity row per the port contract: (identity, nil)
// for an enabled row, (_, ErrIdentityUnknown) when no row exists, and (identity,
// ErrIdentityDisabled) when the row is disabled. The two outcome sentinels are folded
// in here at the storage boundary so callers never compare nullable columns; a real
// I/O error is wrapped and returned.
func (r *identityRepo) GetByName(ctx context.Context, name string) (domain.Identity, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT `+identityColumns+` FROM identity WHERE name = ?`, name)
	id, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Identity{}, domain.ErrIdentityUnknown
	}
	if err != nil {
		return domain.Identity{}, fmt.Errorf("get identity %q: %w", name, err)
	}
	if !id.Enabled() {
		return id, domain.ErrIdentityDisabled
	}
	return id, nil
}

// GetByIds resolves identity ids to their rows in ONE unit of work for display
// purposes (created_by/owner rendering, multi-user epic T4) via a single batched
// IN query. It is deliberately OUTCOME-INSENSITIVE: unknown ids are simply absent
// from the map and DISABLED ids are included — this is attribution metadata, not
// authentication, so a later-disabled creator still renders on their rows (and an
// empty input list short-circuits without touching the DB). Only I/O errors are
// wrapped and returned.
func (r *identityRepo) GetByIds(ctx context.Context, ids []domain.ID) (map[domain.ID]domain.Identity, error) {
	out := make(map[domain.ID]domain.Identity, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") // one placeholder per id; ids are ULIDs (no quoting concerns)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = string(id)
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+identityColumns+` FROM identity WHERE id IN (`+ph+")", args...)
	if err != nil {
		return nil, fmt.Errorf("get identities by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() { // no row => unknown id, omitted by construction
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out[id.ID] = id
	}
	return out, rows.Err()
}

// MemberRole returns the stored membership role for (projectID, identityID). A missing
// row is the common case and maps to RoleNone with a nil error — "not a member" is a
// normal outcome, not a failure. The stored text is mapped through domain.ParseRole so
// a corrupted value surfaces as an error rather than silently becoming none.
func (r *identityRepo) MemberRole(ctx context.Context, projectID, identityID domain.ID) (domain.Role, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT role FROM project_member WHERE project_id = ? AND identity_id = ?`,
		string(projectID), string(identityID))
	var roleStr sql.NullString
	if err := row.Scan(&roleStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RoleNone, nil // not a member — normal outcome
		}
		return domain.RoleNone, fmt.Errorf("get membership %s/%s: %w", projectID, identityID, err)
	}
	return domain.ParseRole(roleStr.String)
}

// UpsertMember writes the stored membership row per the port contract: only Storable
// roles may persist (RoleNone/RoleOwner rejected with wrapped ErrInvalid — matching
// the CHECK constraint, so an in-memory guard catches it before the driver ever sees
// it), and ON CONFLICT(project_id, identity_id) makes re-granting idempotent. The
// project_member row carries no timestamps of its own (T9 keeps the table unchanged).
func (r *identityRepo) UpsertMember(ctx context.Context, projectID, identityID domain.ID, role domain.Role) error {
	if !role.Storable() {
		return fmt.Errorf("%w: storable membership roles are viewer/editor/admin, got %q", domain.ErrInvalid, role.String())
	}
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO project_member (project_id, identity_id, role) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, identity_id) DO UPDATE SET role = excluded.role`,
		string(projectID), string(identityID), role.String())
	if err != nil {
		return fmt.Errorf("upsert membership %s/%s: %w", projectID, identityID, err)
	}
	return nil
}

// RemoveMember deletes the stored membership row per the port contract. A DELETE that
// affects zero rows is the "not a member" OUTCOME and maps to wrapped ErrNotFound —
// RowsAffected is authoritative here (no read-then-delete race window).
func (r *identityRepo) RemoveMember(ctx context.Context, projectID, identityID domain.ID) error {
	res, err := r.q.ExecContext(ctx,
		`DELETE FROM project_member WHERE project_id = ? AND identity_id = ?`,
		string(projectID), string(identityID))
	if err != nil {
		return fmt.Errorf("remove membership %s/%s: %w", projectID, identityID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove membership %s/%s: %w", projectID, identityID, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: identity %s is not a member of project %s", domain.ErrNotFound, identityID, projectID)
	}
	return nil
}

// ListMembers returns the project's stored membership rows in stable identity_id order.
// Disabled identities' rows are included (raw table view — policy filtering is the use
// case's job). The stored text round-trips through domain.ParseRole so a corrupted role
// value surfaces as an error, same as MemberRole.
func (r *identityRepo) ListMembers(ctx context.Context, projectID domain.ID) ([]domain.ProjectMember, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT identity_id, role FROM project_member WHERE project_id = ? ORDER BY identity_id ASC`,
		string(projectID))
	if err != nil {
		return nil, fmt.Errorf("list memberships %s: %w", projectID, err)
	}
	defer rows.Close()
	out := make([]domain.ProjectMember, 0)
	for rows.Next() {
		var (
			identityID domain.ID
			roleStr    string
		)
		if err := rows.Scan(&identityID, &roleStr); err != nil {
			return nil, fmt.Errorf("list memberships %s: %w", projectID, err)
		}
		role, err := domain.ParseRole(roleStr)
		if err != nil {
			return nil, err // wrapped ErrInvalid naming the corrupted value
		}
		out = append(out, domain.ProjectMember{ProjectID: projectID, IdentityID: identityID, Role: role})
	}
	return out, rows.Err()
}

// scanIdentity mirrors the identityColumns order. disabled_at and last_active_at are
// nullable (NULL = enabled / never-active) and round-trip to nil pointers via
// sql.NullString; created_at is NOT NULL in the schema. kind/auth_method map onto the
// domain's string-backed enums directly.
func scanIdentity(s scanner) (domain.Identity, error) {
	var (
		id       domain.Identity
		kind     string
		auth     string
		disabled sql.NullString
		created  string
		lastAct  sql.NullString
	)
	if err := s.Scan(&id.ID, &id.Name, &kind, &auth, &disabled, &created, &lastAct); err != nil {
		return domain.Identity{}, err
	}
	id.Kind = domain.ActorType(kind)
	id.AuthMethod = domain.AuthMethod(auth)
	createdAt, err := parseTS(created)
	if err != nil {
		return domain.Identity{}, fmt.Errorf("identity %s created_at %q: %w", id.ID, created, err)
	}
	id.CreatedAt = createdAt
	if disabled.Valid {
		t, err := parseTS(disabled.String)
		if err != nil {
			return domain.Identity{}, fmt.Errorf("identity %s disabled_at %q: %w", id.ID, disabled.String, err)
		}
		id.DisabledAt = &t
	}
	if lastAct.Valid {
		t, err := parseTS(lastAct.String)
		if err != nil {
			return domain.Identity{}, fmt.Errorf("identity %s last_active_at %q: %w", id.ID, lastAct.String, err)
		}
		id.LastActiveAt = &t
	}
	return id, nil
}
