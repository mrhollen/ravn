package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// authStore implements application.AuthStore: the write + credential side of the multi-user
// identity model (epic T3). It owns rows epic T2 left read-only — credentials, sessions,
// api_tokens — and is bound to the transaction in SQLiteUnitOfWork.Run exactly like every
// other repo. Token columns hold SHA-256 hex digests only (the opaque secret is shown once
// and never persisted); timestamps round-trip through formatTS/parseTS. It reuses
// identity_repo's scanIdentity / identityColumns for the read shape so column order can't
// drift between the two repos.
type authStore struct {
	q querier
}

func newAuthStore(q querier) *authStore { return &authStore{q: q} }

var _ application.AuthStore = (*authStore)(nil)

// getByName is the store's internal name→identity lookup: (identity, nil) for any row,
// domain.ErrNotFound when absent. It does NOT filter on disabled_at — callers decide how
// a disabled row means (login rejects it; token listing still shows it).
func (s *authStore) getByName(ctx context.Context, name string) (domain.Identity, error) {
	row := s.q.QueryRowContext(ctx, `SELECT `+identityColumns+` FROM identity WHERE name = ?`, name)
	id, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Identity{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Identity{}, fmt.Errorf("get identity %q: %w", name, err)
	}
	return id, nil
}

// EnsureIdentity upserts a NEUTRAL identity (no password credential). An existing row is
// returned unchanged (created=false); otherwise the passed identity is inserted with
// auth_method='none' and NULL credential_hash. A UNIQUE race between the existence check
// and the insert resolves by returning whichever row now exists.
func (s *authStore) EnsureIdentity(ctx context.Context, id domain.Identity) (domain.Identity, bool, error) {
	if existing, err := s.getByName(ctx, id.Name); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Identity{}, false, err
	}
	if _, err := s.q.ExecContext(ctx, `
		INSERT INTO identity(id, name, kind, auth_method, credential_hash, disabled_at, created_at, last_active_at)
		VALUES (?, ?, ?, 'none', NULL, NULL, ?, NULL)`,
		string(id.ID), id.Name, string(id.Kind), formatTS(id.CreatedAt)); err != nil {
		if isUniqueConstraintViolation(err) {
			existing, e := s.getByName(ctx, id.Name) // raced a concurrent insert — return the winner
			return existing, false, e
		}
		return domain.Identity{}, false, fmt.Errorf("insert identity %q: %w", id.Name, err)
	}
	return domain.Identity{ID: id.ID, Name: id.Name, Kind: id.Kind, AuthMethod: domain.AuthNone, CreatedAt: id.CreatedAt}, true, nil
}

// UpsertPasswordCredential sets or replaces the password credential for an identity by
// name (auth_method='password' + the given PHC hash). An existing row has its credential
// overwritten (bootstrap / reset); a missing one is created. A UNIQUE race on create
// retries as an update.
func (s *authStore) UpsertPasswordCredential(ctx context.Context, id domain.Identity, credentialHash string) (domain.Identity, bool, error) {
	if existing, err := s.getByName(ctx, id.Name); err == nil {
		if _, uerr := s.q.ExecContext(ctx, `UPDATE identity SET auth_method='password', credential_hash=? WHERE name=?`,
			credentialHash, id.Name); uerr != nil {
			return domain.Identity{}, false, fmt.Errorf("update password %q: %w", id.Name, uerr)
		}
		existing.AuthMethod = domain.AuthPassword
		return existing, false, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Identity{}, false, err
	}
	if _, ierr := s.q.ExecContext(ctx, `
		INSERT INTO identity(id, name, kind, auth_method, credential_hash, disabled_at, created_at, last_active_at)
		VALUES (?, ?, ?, 'password', ?, NULL, ?, NULL)`,
		string(id.ID), id.Name, string(id.Kind), credentialHash, formatTS(id.CreatedAt)); ierr != nil {
		if isUniqueConstraintViolation(ierr) {
			return s.UpsertPasswordCredential(ctx, id, credentialHash) // raced — now an update
		}
		return domain.Identity{}, false, fmt.Errorf("insert identity %q: %w", id.Name, ierr)
	}
	return domain.Identity{ID: id.ID, Name: id.Name, Kind: id.Kind, AuthMethod: domain.AuthPassword, CreatedAt: id.CreatedAt}, true, nil
}

// LookupCredentials resolves name to (identity, credential hash). It is the ONLY store
// method that surfaces a hash — and only to the application layer. ok=false for a missing
// OR disabled identity (a disabled principal cannot log in), so callers get a uniform
// "no valid credential" outcome; the hash may be "" for an enabled identity with no
// password set yet.
func (s *authStore) LookupCredentials(ctx context.Context, name string) (domain.Identity, string, bool, error) {
	name = strings.TrimSpace(name)
	row := s.q.QueryRowContext(ctx, `SELECT `+identityColumns+`, credential_hash FROM identity WHERE name = ?`, name)
	var id domain.Identity
	var kind, auth string
	var disabled sql.NullString
	var created string
	var lastAct sql.NullString
	var hash sql.NullString
	if err := row.Scan(&id.ID, &id.Name, &kind, &auth, &disabled, &created, &lastAct, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Identity{}, "", false, nil // no such identity → not found (not an error)
		}
		return domain.Identity{}, "", false, fmt.Errorf("lookup credentials %q: %w", name, err)
	}
	id.Kind = domain.ActorType(kind)
	id.AuthMethod = domain.AuthMethod(auth)
	if t, e := parseTS(created); e == nil {
		id.CreatedAt = t
	}
	if disabled.Valid {
		if t, e := parseTS(disabled.String); e == nil {
			d := t
			id.DisabledAt = &d
		}
	}
	if lastAct.Valid {
		if t, e := parseTS(lastAct.String); e == nil {
			la := t
			id.LastActiveAt = &la
		}
	}
	if !id.Enabled() {
		return domain.Identity{}, "", false, nil // disabled → cannot authenticate (reported as not found)
	}
	return id, hash.String, true, nil
}

// IdentityByName resolves a trimmed name to its identity row regardless of enabled state;
// a missing name is domain.ErrNotFound. Backs the operator token list/revoke endpoints,
// which must be able to inspect even a disabled account's tokens.
func (s *authStore) IdentityByName(ctx context.Context, name string) (domain.Identity, error) {
	return s.getByName(ctx, strings.TrimSpace(name))
}

// CreateSession stores a fresh session token hash for identityID with its validity window.
func (s *authStore) CreateSession(ctx context.Context, sessionID, identityID domain.ID, tokenHash string, createdAt, expiresAt time.Time) error {
	if _, err := s.q.ExecContext(ctx, `
		INSERT INTO session(id, identity_id, token_hash, created_at, expires_at, revoked_at, meta)
		VALUES (?, ?, ?, ?, ?, NULL, '')`,
		string(sessionID), string(identityID), tokenHash, formatTS(createdAt), formatTS(expiresAt)); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// ResolveSession resolves an active session (not revoked, not expired at `now`) to its
// enabled identity and expiry. Both sides of the expiry compare are formatTS strings, so
// lexicographic == chronological (fixed-width RFC3339 UTC). A missing/expired/revoked hash
// — or one whose identity is disabled — is application.ErrAuthNoSession; real I/O errors
// are wrapped and returned.
func (s *authStore) ResolveSession(ctx context.Context, tokenHash string, now time.Time) (domain.Identity, time.Time, error) {
	row := s.q.QueryRowContext(ctx, `
		SELECT i.id, i.name, i.kind, i.auth_method, i.disabled_at, i.created_at, i.last_active_at, s.expires_at
		  FROM session s JOIN identity i ON i.id = s.identity_id
		 WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?`, tokenHash, formatTS(now))
	var id domain.Identity
	var kind, auth string
	var disabled sql.NullString
	var created, expires string
	var lastAct sql.NullString
	if err := row.Scan(&id.ID, &id.Name, &kind, &auth, &disabled, &created, &lastAct, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Identity{}, time.Time{}, application.ErrAuthNoSession
		}
		return domain.Identity{}, time.Time{}, fmt.Errorf("resolve session: %w", err)
	}
	id.Kind = domain.ActorType(kind)
	id.AuthMethod = domain.AuthMethod(auth)
	if t, e := parseTS(created); e == nil {
		id.CreatedAt = t
	}
	if disabled.Valid {
		if t, e := parseTS(disabled.String); e == nil {
			d := t
			id.DisabledAt = &d
		}
	}
	if lastAct.Valid {
		if t, e := parseTS(lastAct.String); e == nil {
			la := t
			id.LastActiveAt = &la
		}
	}
	if !id.Enabled() {
		return domain.Identity{}, time.Time{}, application.ErrAuthNoSession // live session for a disabled identity is rejected
	}
	exp, e := parseTS(expires)
	if e != nil {
		return domain.Identity{}, time.Time{}, fmt.Errorf("resolve session: bad expires_at %q: %w", expires, e)
	}
	return id, exp, nil
}

// RevokeSession stamps revoked_at on the matching session. Idempotent: an unknown or
// already-revoked hash affects zero rows and is not an error (a replayed logout → 204).
func (s *authStore) RevokeSession(ctx context.Context, tokenHash string, now time.Time) error {
	if _, err := s.q.ExecContext(ctx, `UPDATE session SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, formatTS(now), tokenHash); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// CreateAPIToken stores a fresh API token hash for identityID with its label.
func (s *authStore) CreateAPIToken(ctx context.Context, tokenID, identityID domain.ID, tokenHash, label string, createdAt time.Time) error {
	if _, err := s.q.ExecContext(ctx, `
		INSERT INTO api_token(id, identity_id, token_hash, label, created_at, last_used_at, disabled_at)
		VALUES (?, ?, ?, ?, ?, NULL, NULL)`,
		string(tokenID), string(identityID), tokenHash, label, formatTS(createdAt)); err != nil {
		return fmt.Errorf("create api token: %w", err)
	}
	return nil
}

// ResolveAPIToken resolves an active (enabled) api_token to its enabled identity and
// lazily refreshes last_used_at (best-effort — auth must not fail on the stamp). A
// missing/revoked hash, or one whose identity is disabled, is application.ErrAuthNoSession.
func (s *authStore) ResolveAPIToken(ctx context.Context, tokenHash string, now time.Time) (domain.Identity, error) {
	row := s.q.QueryRowContext(ctx, `
		SELECT i.id, i.name, i.kind, i.auth_method, i.disabled_at, i.created_at, i.last_active_at
		  FROM api_token t JOIN identity i ON i.id = t.identity_id
		 WHERE t.token_hash = ? AND t.disabled_at IS NULL`, tokenHash)
	var id domain.Identity
	var kind, auth string
	var disabled sql.NullString
	var created string
	var lastAct sql.NullString
	if err := row.Scan(&id.ID, &id.Name, &kind, &auth, &disabled, &created, &lastAct); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Identity{}, application.ErrAuthNoSession
		}
		return domain.Identity{}, fmt.Errorf("resolve api token: %w", err)
	}
	id.Kind = domain.ActorType(kind)
	id.AuthMethod = domain.AuthMethod(auth)
	if t, e := parseTS(created); e == nil {
		id.CreatedAt = t
	}
	if disabled.Valid {
		if t, e := parseTS(disabled.String); e == nil {
			d := t
			id.DisabledAt = &d
		}
	}
	if lastAct.Valid {
		if t, e := parseTS(lastAct.String); e == nil {
			la := t
			id.LastActiveAt = &la
		}
	}
	if !id.Enabled() {
		return domain.Identity{}, application.ErrAuthNoSession // token for a disabled identity is rejected
	}
	_, _ = s.q.ExecContext(ctx, `UPDATE api_token SET last_used_at=? WHERE token_hash=? AND disabled_at IS NULL`, formatTS(now), tokenHash)
	return id, nil
}

// ListTokensByIdentity returns the non-secret metadata of every api_token for the identity,
// newest first (disabled rows included — an operator can inspect a disabled account).
func (s *authStore) ListTokensByIdentity(ctx context.Context, identityID domain.ID) ([]application.APITokenMeta, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT id, identity_id, label, created_at, last_used_at, disabled_at FROM api_token WHERE identity_id = ? ORDER BY created_at DESC`,
		string(identityID))
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	out := []application.APITokenMeta{}
	for rows.Next() {
		var m application.APITokenMeta
		var created string
		var lastUsed sql.NullString
		var disabled sql.NullString
		if err := rows.Scan(&m.ID, &m.IdentityID, &m.Label, &created, &lastUsed, &disabled); err != nil {
			return nil, fmt.Errorf("scan api token: %w", err)
		}
		if t, e := parseTS(created); e == nil {
			m.CreatedAt = t
		}
		if lastUsed.Valid {
			if t, e := parseTS(lastUsed.String); e == nil {
				lu := t
				m.LastUsedAt = &lu
			}
		}
		m.Disabled = disabled.Valid // disabled_at NULL = enabled
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api tokens: %w", err)
	}
	return out, nil
}

// RevokeAPITokenByID disables an api_token by its durable id. An unknown id is
// domain.ErrNotFound (→ 404); a known-but-already-disabled token is a no-op success (204).
func (s *authStore) RevokeAPITokenByID(ctx context.Context, id domain.ID, now time.Time) error {
	var got domain.ID
	if err := s.q.QueryRowContext(ctx, `SELECT id FROM api_token WHERE id = ?`, string(id)).Scan(&got); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: api token %q", domain.ErrNotFound, id)
		}
		return fmt.Errorf("revoke api token: %w", err)
	}
	if _, err := s.q.ExecContext(ctx, `UPDATE api_token SET disabled_at=? WHERE id=? AND disabled_at IS NULL`, formatTS(now), string(id)); err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	return nil
}
