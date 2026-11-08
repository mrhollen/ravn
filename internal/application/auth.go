package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// PasswordHasher is the stdlib-free port for credential hashing. The concrete Argon2id
// implementation lives in infrastructure (pwhash); tests supply a fast fake so no real
// KDF runs in the application-layer suite.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Check(storedHash, password string) bool
}

// AuthStore is the persistence seam for credentials, sessions and API tokens — the write
// side of the identity model that epic T2 left read-only. It is bound to the transaction
// in SQLiteUnitOfWork.Run exactly like every other repo. Token columns hold opaque
// SHA-256 hex digests only (AuthTokenHash); plaintext secrets are shown to their owner
// exactly once and never persisted.
type AuthStore interface {
	// EnsureIdentity upserts a NEUTRAL identity (no password credential) by name. A new
	// row is inserted with auth_method='none' and NULL credential_hash; an existing row is
	// returned UNCHANGED (a password set via UpsertPasswordCredential is never clobbered).
	// Returns the resolved identity and created=true only when a new row was inserted.
	EnsureIdentity(ctx context.Context, id domain.Identity) (domain.Identity, bool, error)

	// UpsertPasswordCredential sets or replaces the password credential for an identity by
	// name: auth_method='password' with the given PHC hash. A new identity is created if
	// none exists; an existing one has its credential overwritten (bootstrap / reset).
	UpsertPasswordCredential(ctx context.Context, id domain.Identity, credentialHash string) (domain.Identity, bool, error)

	// LookupCredentials resolves a trimmed name to its identity AND stored credential
	// hash — the ONE port method allowed to surface the hash; it stays in the application
	// layer and is never sent over HTTP. ok=false when no enabled row exists (missing or
	// disabled rows both report false, so disabled principals cannot log in). The returned
	// hash may be "" for an identity that has no password set yet.
	LookupCredentials(ctx context.Context, name string) (domain.Identity, string, bool, error)

	// IdentityByName resolves a trimmed name to its identity row (enabled or disabled); a
	// missing name is domain.ErrNotFound. Used by the operator token list/revoke endpoints.
	IdentityByName(ctx context.Context, name string) (domain.Identity, error)

	// CreateSession stores a fresh session token hash for identityID.
	CreateSession(ctx context.Context, sessionID, identityID domain.ID, tokenHash string, createdAt, expiresAt time.Time) error

	// ResolveSession resolves an active (not revoked, not expired) session hash to its
	// enabled identity and expiry. A missing/expired/revoked hash — or one whose identity
	// is disabled — is ErrAuthNoSession; a real I/O failure is wrapped and returned.
	ResolveSession(ctx context.Context, tokenHash string, now time.Time) (domain.Identity, time.Time, error)

	// RevokeSession stamps revoked_at on the matching session. Idempotent: an unknown or
	// already-revoked hash is a no-op, not an error.
	RevokeSession(ctx context.Context, tokenHash string, now time.Time) error

	// CreateAPIToken stores a fresh API token hash for identityID with its label.
	CreateAPIToken(ctx context.Context, tokenID, identityID domain.ID, tokenHash, label string, createdAt time.Time) error

	// ResolveAPIToken resolves an active (enabled) api_token hash to its identity and
	// lazily refreshes last_used_at. A missing/revoked hash — or one whose identity is
	// disabled — is ErrAuthNoSession; a real I/O failure is wrapped and returned.
	ResolveAPIToken(ctx context.Context, tokenHash string, now time.Time) (domain.Identity, error)

	// ListTokensByIdentity returns the non-secret metadata of every api_token for the
	// identity, newest first (disabled rows included — an operator can inspect them).
	ListTokensByIdentity(ctx context.Context, identityID domain.ID) ([]APITokenMeta, error)

	// RevokeAPITokenByID disables an api_token row by its durable id. An unknown id is
	// domain.ErrNotFound; a known-but-already-disabled token is a no-op success (204).
	RevokeAPITokenByID(ctx context.Context, id domain.ID, now time.Time) error
}

// APITokenMeta is the non-secret, listable view of an API token. The opaque secret and
// its hash never appear here.
type APITokenMeta struct {
	ID         domain.ID
	IdentityID domain.ID
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Disabled   bool
}

var (
	// ErrAuthCredentialsInvalid is returned by Login when the username/password pair does
	// not match an enabled credential with a set password. It maps to 401 at the HTTP edge
	// — deliberately indistinguishable from "unknown user" so usernames cannot be
	// enumerated, and from "disabled account".
	ErrAuthCredentialsInvalid = errors.New("auth: invalid credentials")

	// ErrAuthNoSession is returned when a presented session/API token hash does not
	// resolve to an active, enabled principal. It maps to 401 at the edge.
	ErrAuthNoSession = errors.New("auth: no active session or api token")
)

// minPasswordLen is the shortest credential password accepted by CreateAccount (OWASP
// floor). Login accepts any length that a previously-stored hash verifies.
const minPasswordLen = 8

// AuthTokenHash returns the SHA-256 hex digest of an opaque secret — the value stored in
// session.token_hash / api_token.token_hash. The empty string hashes too (callers guard
// against presenting empty tokens).
func AuthTokenHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// NewOpaqueToken mints a 32-byte random secret, base64url-encoded without padding. It is
// the bearer value shown to its owner exactly once (sessions on login, api tokens on
// mint); only its AuthTokenHash is ever persisted.
func NewOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: mint opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// LoginResult is the outcome of a successful password login. Token is the opaque session
// secret, shown exactly once; it is not stored (only its hash is).
type LoginResult struct {
	Identity  domain.Identity
	Token     string
	ExpiresAt time.Time
}

// AccountResult is the outcome of an operator creating/resetting a human account.
type AccountResult struct {
	Identity domain.Identity
	Created  bool // true when a new identity row was inserted (→ HTTP 201); false on reset
}

// TokenResult is the outcome of minting an API token. Token is the opaque secret, shown
// exactly once; only its hash is persisted.
type TokenResult struct {
	Token string
	Meta  APITokenMeta
}

// Auth is the credential / session / api-token use case (multi-user epic T3). It owns the
// password→session exchange and the per-agent API-token lifecycle. It performs NO
// authorization: it only establishes WHO authenticated via WHAT, exposing that to callers
// as domain values. The HTTP gate consumes SessionPrincipal/APIPrincipal for request auth;
// T4's policies consume Principal for decisions.
type Auth struct {
	uow        UnitOfWork
	clock      Clock
	idgen      IDGenerator
	hasher     PasswordHasher
	sessionTTL time.Duration
}

// NewAuth wires the auth use case. A nil/zero sessionTTL falls back to 24h so a missing
// RAVN_SESSION_TTL still yields sane session lifetimes. hasher must be non-nil; uow may
// be nil only in compositions where no /auth/* route is ever reachable (it then errors on
// first use rather than at construction, mirroring the other nil-tolerated ports).
func NewAuth(uow UnitOfWork, clock Clock, idgen IDGenerator, hasher PasswordHasher, sessionTTL time.Duration) *Auth {
	if sessionTTL <= 0 {
		sessionTTL = 24 * time.Hour
	}
	return &Auth{uow: uow, clock: clock, idgen: idgen, hasher: hasher, sessionTTL: sessionTTL}
}

// Login exchanges a username/password pair for a fresh session token. The (slow) Argon2
// verification runs OUTSIDE any transaction so the single-writer DB lock is never held
// during the KDF: read the stored hash (one unit of work), verify in memory, then persist
// the new session (a second unit of work). Any mismatch — unknown user, disabled user, no
// password set, or wrong password — returns ErrAuthCredentialsInvalid. An empty username
// is malformed input and returns domain.ErrInvalid (→ 400) instead.
func (a *Auth) Login(ctx context.Context, username, password string) (LoginResult, error) {
	name := strings.TrimSpace(username)
	if name == "" {
		return LoginResult{}, fmt.Errorf("%w: username is required", domain.ErrInvalid)
	}

	id, hash, ok, err := a.lookupCredentials(ctx, name)
	if err != nil {
		return LoginResult{}, err
	}
	if !ok || hash == "" || !a.hasher.Check(hash, password) {
		return LoginResult{}, ErrAuthCredentialsInvalid
	}

	now := a.clock.Now()
	expiresAt := now.Add(a.sessionTTL)
	sessionID := a.idgen.NewID()
	opaque, err := NewOpaqueToken()
	if err != nil {
		return LoginResult{}, err
	}
	th := AuthTokenHash(opaque)

	var write Repos
	if err := a.uow.Run(ctx, &write, func() error {
		return write.Auth.CreateSession(ctx, sessionID, id.ID, th, now, expiresAt)
	}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Identity: id, Token: opaque, ExpiresAt: expiresAt}, nil
}

// lookupCredentials runs the read unit of work and hands back the stored identity/hash.
func (a *Auth) lookupCredentials(ctx context.Context, name string) (domain.Identity, string, bool, error) {
	var repos Repos
	var id domain.Identity
	var hash string
	var ok bool
	var re error
	if err := a.uow.Run(ctx, &repos, func() error {
		id, hash, ok, re = repos.Auth.LookupCredentials(ctx, name)
		return re
	}); err != nil {
		return domain.Identity{}, "", false, err
	}
	return id, hash, ok, re // re may be nil or a wrapped I/O error
}

// InspectSession resolves the opaque session token currently in use (the presenting
// bearer) to its enabled identity and expiry — backing GET /auth/session. A missing or
// expired/revoked token is ErrAuthNoSession (→ 401).
func (a *Auth) InspectSession(ctx context.Context, opaqueToken string) (domain.Identity, time.Time, error) {
	now := a.clock.Now()
	var repos Repos
	var id domain.Identity
	var exp time.Time
	var re error
	if err := a.uow.Run(ctx, &repos, func() error {
		id, exp, re = repos.Auth.ResolveSession(ctx, AuthTokenHash(opaqueToken), now)
		return re
	}); err != nil {
		return domain.Identity{}, time.Time{}, err
	}
	return id, exp, re // re may be ErrAuthNoSession
}

// SessionPrincipal resolves an opaque session bearer to its enabled identity for the auth
// gate. ErrAuthNoSession means "not a valid live session" (the gate then tries api_token).
func (a *Auth) SessionPrincipal(ctx context.Context, opaqueToken string) (domain.Identity, error) {
	id, _, err := a.InspectSession(ctx, opaqueToken)
	return id, err
}

// RevokeSession invalidates the presenting session token (DELETE /auth/session). It is
// idempotent: revoking an unknown or already-revoked token succeeds with no error, so a
// stale client replaying its logout still gets 204.
func (a *Auth) RevokeSession(ctx context.Context, opaqueToken string) error {
	now := a.clock.Now()
	th := AuthTokenHash(opaqueToken)
	var repos Repos
	return a.uow.Run(ctx, &repos, func() error { return repos.Auth.RevokeSession(ctx, th, now) })
}

// CreateAccount creates (or resets) a human identity with a password credential so it can
// log in — the operator's bootstrap path. The password is hashed OUTSIDE the transaction
// (slow KDF not held across the DB lock); name and kind are validated, then the PHC hash
// is upserted. A short or missing password is domain.ErrInvalid (→ 400).
func (a *Auth) CreateAccount(ctx context.Context, name string, kind domain.ActorType, password string) (AccountResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AccountResult{}, fmt.Errorf("%w: account name is required", domain.ErrInvalid)
	}
	if len(password) < minPasswordLen {
		return AccountResult{}, fmt.Errorf("%w: password must be at least %d characters", domain.ErrInvalid, minPasswordLen)
	}
	if kind == "" {
		kind = domain.ActorHuman
	}
	if !kind.Valid() {
		return AccountResult{}, fmt.Errorf("%w: kind must be %q or %q", domain.ErrInvalid, domain.ActorHuman, domain.ActorAgent)
	}

	hash, err := a.hasher.Hash(password)
	if err != nil {
		return AccountResult{}, err
	}
	id := domain.Identity{ID: a.idgen.NewID(), Name: name, Kind: kind, AuthMethod: domain.AuthPassword, CreatedAt: a.clock.Now()}

	var out AccountResult
	var re error
	var repos Repos
	if err := a.uow.Run(ctx, &repos, func() error {
		out.Identity, out.Created, re = repos.Auth.UpsertPasswordCredential(ctx, id, hash)
		return re
	}); err != nil {
		return AccountResult{}, err
	}
	return out, re // re may be a wrapped I/O error
}

// MintAPIToken mints an API token for the named identity (operator-only). If the identity
// does not exist yet it is created as a neutral (no-password) row in the SAME transaction
// as the token, so a never-acted agent can still be provisioned. The opaque secret is
// generated outside the transaction and returned once; only its hash is stored. An empty
// name or invalid kind is domain.ErrInvalid.
func (a *Auth) MintAPIToken(ctx context.Context, name string, kind domain.ActorType, label string) (TokenResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TokenResult{}, fmt.Errorf("%w: token target name is required", domain.ErrInvalid)
	}
	if kind == "" {
		kind = domain.ActorHuman // consistent with actorInput defaults; operators pass "agent" explicitly
	}
	if !kind.Valid() {
		return TokenResult{}, fmt.Errorf("%w: kind must be %q or %q", domain.ErrInvalid, domain.ActorHuman, domain.ActorAgent)
	}

	opaque, err := NewOpaqueToken()
	if err != nil {
		return TokenResult{}, err
	}
	th := AuthTokenHash(opaque)
	tokenID := a.idgen.NewID()
	now := a.clock.Now()

	var out TokenResult
	var repos Repos
	err = a.uow.Run(ctx, &repos, func() error {
		id, _, e := repos.Auth.EnsureIdentity(ctx, domain.Identity{ID: a.idgen.NewID(), Name: name, Kind: kind, CreatedAt: now})
		if e != nil {
			return e
		}
		if e = repos.Auth.CreateAPIToken(ctx, tokenID, id.ID, th, label, now); e != nil {
			return e
		}
		out.Meta = APITokenMeta{ID: tokenID, IdentityID: id.ID, Label: label, CreatedAt: now}
		return nil
	})
	if err != nil {
		return TokenResult{}, err
	}
	out.Token = opaque
	return out, nil
}

// ListAPITokens returns the non-secret metadata of every API token for the named identity.
// An unknown identity is domain.ErrNotFound (→ 404); an identity with zero tokens returns
// an empty slice (200). A disabled identity can still be listed (an operator inspecting it).
func (a *Auth) ListAPITokens(ctx context.Context, name string) ([]APITokenMeta, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: token list name is required", domain.ErrInvalid)
	}
	var out []APITokenMeta
	var re error
	var repos Repos
	if err := a.uow.Run(ctx, &repos, func() error {
		id, e := repos.Auth.IdentityByName(ctx, name)
		if e != nil {
			return e
		}
		out, re = repos.Auth.ListTokensByIdentity(ctx, id.ID)
		return re
	}); err != nil {
		return nil, err
	}
	if out == nil {
		out = []APITokenMeta{} // never a null body on success
	}
	return out, re
}

// RevokeAPIToken disables an API token by its durable id (operator-only). An unknown or
// empty id is domain.ErrNotFound / domain.ErrInvalid (→ 404/400); revoking an
// already-disabled token is a no-op success (204).
func (a *Auth) RevokeAPIToken(ctx context.Context, id domain.ID) error {
	if id.IsZero() {
		return fmt.Errorf("%w: api token id is required", domain.ErrInvalid)
	}
	now := a.clock.Now()
	var repos Repos
	return a.uow.Run(ctx, &repos, func() error { return repos.Auth.RevokeAPITokenByID(ctx, id, now) })
}

// APIPrincipal resolves an opaque api_token bearer to its enabled identity for the auth
// gate. ErrAuthNoSession means "not a valid live api token" (the gate reports 401).
func (a *Auth) APIPrincipal(ctx context.Context, opaqueToken string) (domain.Identity, error) {
	now := a.clock.Now()
	var repos Repos
	var id domain.Identity
	var re error
	if err := a.uow.Run(ctx, &repos, func() error {
		id, re = repos.Auth.ResolveAPIToken(ctx, AuthTokenHash(opaqueToken), now)
		return re
	}); err != nil {
		return domain.Identity{}, err
	}
	return id, re // re may be ErrAuthNoSession
}
