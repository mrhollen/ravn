package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// identityRepo implements application.IdentityRepository over SQLite: the read side
// of the multi-user identity model (epic T2). It is deliberately READ-ONLY — it
// resolves an identity by name and reports a stored membership role; writing rows
// (credentials, sessions, memberships) belongs to later epic tickets. It uses raw
// database/sql with an explicit column list and hand-rolled scanning (ARCHITECTURE §4),
// the same shape as every other repo here.
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
