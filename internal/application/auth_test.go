package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// auth_test.go covers the Auth use case (multi-user epic T3) against a small in-memory
// fake store + deterministic clock/idgen + a fast fake hasher, so no real Argon2 runs here
// (pwhash has its own package tests). It pins: login exchange + KDF-outside-tx ordering,
// the indistinguishable credentials-invalid outcome, session resolve/revoke semantics,
// account create-vs-reset, token mint/list/revoke, and every validation error mapping.

var nowT0 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// ---- tiny stand-ins ------------------------------------------------------------

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var _ Clock = fixedClock{}

type seqIDGen struct{ n int }

func (g *seqIDGen) NewID() domain.ID { g.n++; return domain.ID("GEN" + strconv.Itoa(g.n)) }

var _ IDGenerator = (*seqIDGen)(nil)

// fakeHasher encodes "$h$"+password and verifies by re-derivation — enough to exercise the
// Check(stored, password) path deterministically with zero KDF cost.
type fakeHasher struct{}

func (fakeHasher) Hash(pw string) (string, error) { return "$h$" + pw, nil }
func (fakeHasher) Check(stored, pw string) bool   { return stored == "$h$"+pw }

var _ PasswordHasher = fakeHasher{}

// authUoW binds only the Auth port — the use case under test reads nothing else. It mirrors
// the real UnitOfWork.Run contract of binding fakes into r before fn.
type authUoW struct{ store AuthStore }

func (u *authUoW) Run(ctx context.Context, r *Repos, fn func() error) error {
	r.Auth = u.store
	return fn()
}

var _ UnitOfWork = (*authUoW)(nil)

// ---- fake store -----------------------------------------------------------------

type fakeSession struct {
	identityID domain.ID
	expiresAt  time.Time
	revoked    bool
}

type fakeToken struct {
	id         domain.ID
	identityID domain.ID
	label      string
	createdAt  time.Time
	lastUsed   time.Time
	disabled   bool
}

// fakeAuthStore is an in-memory AuthStore. It reproduces the enabled/disabled and
// expired/revoked semantics the SQLite store must honor so the use case can be tested
// without a database. Pointer values are shared across the per-key and per-id indexes so
// mutations (revoke/disable) stay consistent.
type fakeAuthStore struct {
	byName    map[string]domain.Identity
	byID      map[domain.ID]domain.Identity
	cred      map[domain.ID]string
	sessions  map[string]*fakeSession
	tokens    map[string]*fakeToken
	tokenByID map[domain.ID]*fakeToken
	order     []string

	revokeSessionErr error // inject a non-sentinel I/O error into RevokeSession
	listErr          error // inject a non-sentinel I/O error into ListTokensByIdentity
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{
		byName:    map[string]domain.Identity{},
		byID:      map[domain.ID]domain.Identity{},
		cred:      map[domain.ID]string{},
		sessions:  map[string]*fakeSession{},
		tokens:    map[string]*fakeToken{},
		tokenByID: map[domain.ID]*fakeToken{},
	}
}

func (s *fakeAuthStore) seedEnabled(id, name string, kind domain.ActorType, credHash string) {
	i := domain.Identity{ID: domain.ID(id), Name: name, Kind: kind, AuthMethod: domain.AuthPassword, CreatedAt: nowT0}
	s.byName[name] = i
	s.byID[domain.ID(id)] = i
	if credHash != "" {
		s.cred[domain.ID(id)] = credHash
	}
}

func (s *fakeAuthStore) setDisabled(name string) {
	i := s.byName[name]
	d := nowT0
	i.DisabledAt = &d
	s.byName[name] = i
	s.byID[i.ID] = i
}

var _ AuthStore = (*fakeAuthStore)(nil)

func (s *fakeAuthStore) EnsureIdentity(_ context.Context, id domain.Identity) (domain.Identity, bool, error) {
	if _, ok := s.byName[id.Name]; ok {
		return s.byName[id.Name], false, nil // existing row returned UNCHANGED
	}
	i := domain.Identity{ID: id.ID, Name: id.Name, Kind: id.Kind, AuthMethod: domain.AuthNone, CreatedAt: id.CreatedAt}
	s.byName[i.Name] = i
	s.byID[i.ID] = i
	return i, true, nil
}

func (s *fakeAuthStore) UpsertPasswordCredential(_ context.Context, id domain.Identity, credentialHash string) (domain.Identity, bool, error) {
	if existing, ok := s.byName[id.Name]; ok {
		existing.AuthMethod = domain.AuthPassword
		s.byName[existing.Name] = existing
		s.byID[existing.ID] = existing
		s.cred[existing.ID] = credentialHash
		return existing, false, nil
	}
	i := domain.Identity{ID: id.ID, Name: id.Name, Kind: id.Kind, AuthMethod: domain.AuthPassword, CreatedAt: id.CreatedAt}
	s.byName[i.Name] = i
	s.byID[i.ID] = i
	s.cred[i.ID] = credentialHash
	return i, true, nil
}

func (s *fakeAuthStore) LookupCredentials(_ context.Context, name string) (domain.Identity, string, bool, error) {
	name = strings.TrimSpace(name)
	id, ok := s.byName[name]
	if !ok || !id.Enabled() {
		return domain.Identity{}, "", false, nil // missing OR disabled → uniform "no valid credential"
	}
	return id, s.cred[id.ID], true, nil
}

func (s *fakeAuthStore) IdentityByName(_ context.Context, name string) (domain.Identity, error) {
	id, ok := s.byName[strings.TrimSpace(name)]
	if !ok {
		return domain.Identity{}, domain.ErrNotFound
	}
	return id, nil
}

func (s *fakeAuthStore) CreateSession(_ context.Context, _, identityID domain.ID, tokenHash string, _, expiresAt time.Time) error {
	s.sessions[tokenHash] = &fakeSession{identityID: identityID, expiresAt: expiresAt}
	return nil
}

func (s *fakeAuthStore) ResolveSession(_ context.Context, tokenHash string, now time.Time) (domain.Identity, time.Time, error) {
	ses, ok := s.sessions[tokenHash]
	if !ok || ses.revoked || !ses.expiresAt.After(now) {
		return domain.Identity{}, time.Time{}, ErrAuthNoSession
	}
	id, ok := s.byID[ses.identityID]
	if !ok || !id.Enabled() {
		return domain.Identity{}, time.Time{}, ErrAuthNoSession
	}
	return id, ses.expiresAt, nil
}

func (s *fakeAuthStore) RevokeSession(_ context.Context, tokenHash string, _ time.Time) error {
	if s.revokeSessionErr != nil {
		return s.revokeSessionErr
	}
	if ses, ok := s.sessions[tokenHash]; ok {
		ses.revoked = true // idempotent: unknown hash is a no-op
	}
	return nil
}

func (s *fakeAuthStore) CreateAPIToken(_ context.Context, tokenID, identityID domain.ID, tokenHash, label string, createdAt time.Time) error {
	tk := &fakeToken{id: tokenID, identityID: identityID, label: label, createdAt: createdAt}
	s.tokens[tokenHash] = tk
	s.tokenByID[tokenID] = tk
	s.order = append(s.order, tokenHash)
	return nil
}

func (s *fakeAuthStore) ResolveAPIToken(_ context.Context, tokenHash string, now time.Time) (domain.Identity, error) {
	tk, ok := s.tokens[tokenHash]
	if !ok || tk.disabled {
		return domain.Identity{}, ErrAuthNoSession
	}
	id, ok := s.byID[tk.identityID]
	if !ok || !id.Enabled() {
		return domain.Identity{}, ErrAuthNoSession
	}
	tk.lastUsed = now // lazy refresh
	return id, nil
}

func (s *fakeAuthStore) ListTokensByIdentity(_ context.Context, identityID domain.ID) ([]APITokenMeta, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := []APITokenMeta{}
	for i := len(s.order) - 1; i >= 0; i-- { // newest first
		tk := s.tokens[s.order[i]]
		if tk.identityID != identityID {
			continue
		}
		m := APITokenMeta{ID: tk.id, IdentityID: tk.identityID, Label: tk.label, CreatedAt: tk.createdAt, Disabled: tk.disabled}
		if !tk.lastUsed.IsZero() {
			lu := tk.lastUsed
			m.LastUsedAt = &lu
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *fakeAuthStore) RevokeAPITokenByID(_ context.Context, id domain.ID, _ time.Time) error {
	tk, ok := s.tokenByID[id]
	if !ok {
		return fmt.Errorf("%w: api token %q", domain.ErrNotFound, id)
	}
	tk.disabled = true // idempotent
	return nil
}

// ---- shared builder -------------------------------------------------------------

func newAuthFor(store *fakeAuthStore, ttl time.Duration) *Auth {
	return NewAuth(&authUoW{store: store}, fixedClock{nowT0}, &seqIDGen{}, fakeHasher{}, ttl)
}

// ---- login ---------------------------------------------------------------------

func TestAuthLoginSuccess(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	a := newAuthFor(st, time.Hour)

	res, err := a.Login(context.Background(), "alice", "secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Identity.Name != "alice" || res.Identity.ID != "A1" {
		t.Fatalf("identity = %+v, want alice/A1", res.Identity)
	}
	if res.Token == "" {
		t.Fatal("empty session token")
	}
	wantExp := nowT0.Add(time.Hour)
	if !res.ExpiresAt.Equal(wantExp) {
		t.Fatalf("ExpiresAt = %v, want %v", res.ExpiresAt, wantExp)
	}
	ses, ok := st.sessions[AuthTokenHash(res.Token)]
	if !ok {
		t.Fatal("no session stored for the returned token hash")
	}
	if ses.identityID != "A1" || !ses.expiresAt.Equal(wantExp) {
		t.Fatalf("stored session = %+v, want identity A1 expiring %v", ses, wantExp)
	}
}

func TestAuthLoginWrongPassword(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	a := newAuthFor(st, time.Hour)

	if _, err := a.Login(context.Background(), "alice", "wrong"); !errors.Is(err, ErrAuthCredentialsInvalid) {
		t.Fatalf("err = %v, want ErrAuthCredentialsInvalid", err)
	}
	if len(st.sessions) != 0 {
		t.Fatalf("a session was created on failed login: %d", len(st.sessions))
	}
}

func TestAuthLoginUnknownUser(t *testing.T) {
	a := newAuthFor(newFakeAuthStore(), time.Hour)
	if _, err := a.Login(context.Background(), "bob", "secret"); !errors.Is(err, ErrAuthCredentialsInvalid) {
		t.Fatalf("err = %v, want ErrAuthCredentialsInvalid (indistinguishable from wrong password)", err)
	}
}

func TestAuthLoginDisabledUser(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	st.setDisabled("alice")
	a := newAuthFor(st, time.Hour)
	if _, err := a.Login(context.Background(), "alice", "secret"); !errors.Is(err, ErrAuthCredentialsInvalid) {
		t.Fatalf("disabled user must not log in; err = %v", err)
	}
}

func TestAuthLoginEmptyUsername(t *testing.T) {
	a := newAuthFor(newFakeAuthStore(), time.Hour)
	if _, err := a.Login(context.Background(), "   ", "secret"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want domain.ErrInvalid (400, not 401)", err)
	}
}

func TestAuthLoginStoreErrorPropagates(t *testing.T) {
	// A real store I/O error in Login's write unit of work (not the credentials sentinel)
	// must surface verbatim to the caller — the HTTP edge maps it to 500, not 401.
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	boom := errors.New("disk full")
	failing := &failingSessionStore{inner: st, createErr: boom}
	a := NewAuth(&authUoW{store: failing}, fixedClock{nowT0}, &seqIDGen{}, fakeHasher{}, time.Hour)
	if _, err := a.Login(context.Background(), "alice", "secret"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's I/O error to propagate", err)
	}
}

// failingSessionStore wraps a real fake and fails only CreateSession, to exercise Login's
// second (write) unit of work error path.
type failingSessionStore struct {
	inner     *fakeAuthStore
	createErr error
}

func (f *failingSessionStore) EnsureIdentity(ctx context.Context, id domain.Identity) (domain.Identity, bool, error) {
	return f.inner.EnsureIdentity(ctx, id)
}
func (f *failingSessionStore) UpsertPasswordCredential(ctx context.Context, id domain.Identity, h string) (domain.Identity, bool, error) {
	return f.inner.UpsertPasswordCredential(ctx, id, h)
}
func (f *failingSessionStore) LookupCredentials(ctx context.Context, name string) (domain.Identity, string, bool, error) {
	return f.inner.LookupCredentials(ctx, name)
}
func (f *failingSessionStore) IdentityByName(ctx context.Context, name string) (domain.Identity, error) {
	return f.inner.IdentityByName(ctx, name)
}
func (f *failingSessionStore) CreateSession(_ context.Context, _, _ domain.ID, _ string, _, _ time.Time) error {
	return f.createErr
}
func (f *failingSessionStore) ResolveSession(ctx context.Context, h string, now time.Time) (domain.Identity, time.Time, error) {
	return f.inner.ResolveSession(ctx, h, now)
}
func (f *failingSessionStore) RevokeSession(ctx context.Context, h string, now time.Time) error {
	return f.inner.RevokeSession(ctx, h, now)
}
func (f *failingSessionStore) CreateAPIToken(ctx context.Context, a, b domain.ID, h, l string, c time.Time) error {
	return f.inner.CreateAPIToken(ctx, a, b, h, l, c)
}
func (f *failingSessionStore) ResolveAPIToken(ctx context.Context, h string, now time.Time) (domain.Identity, error) {
	return f.inner.ResolveAPIToken(ctx, h, now)
}
func (f *failingSessionStore) ListTokensByIdentity(ctx context.Context, id domain.ID) ([]APITokenMeta, error) {
	return f.inner.ListTokensByIdentity(ctx, id)
}
func (f *failingSessionStore) RevokeAPITokenByID(ctx context.Context, id domain.ID, now time.Time) error {
	return f.inner.RevokeAPITokenByID(ctx, id, now)
}

// ---- session -------------------------------------------------------------------

func TestAuthInspectAndRevokeSession(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	a := newAuthFor(st, time.Hour)

	res, _ := a.Login(context.Background(), "alice", "secret")

	id, exp, err := a.InspectSession(context.Background(), res.Token)
	if err != nil || id.Name != "alice" || !exp.Equal(nowT0.Add(time.Hour)) {
		t.Fatalf("InspectSession = (%+v, %v, %v), want alice + expiry", id, exp, err)
	}

	// Revocation is idempotent and blocks further inspection.
	if err := a.RevokeSession(context.Background(), res.Token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if err := a.RevokeSession(context.Background(), res.Token); err != nil {
		t.Fatalf("second RevokeSession must be a no-op, got %v", err)
	}
	if _, _, err := a.InspectSession(context.Background(), res.Token); !errors.Is(err, ErrAuthNoSession) {
		t.Fatalf("after revoke InspectSession = %v, want ErrAuthNoSession", err)
	}
}

func TestAuthInspectExpiredSession(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	a := newAuthFor(st, time.Hour)
	tok := "expired-token"
	st.sessions[AuthTokenHash(tok)] = &fakeSession{identityID: "A1", expiresAt: nowT0.Add(-time.Hour)}
	if _, _, err := a.InspectSession(context.Background(), tok); !errors.Is(err, ErrAuthNoSession) {
		t.Fatalf("expired session must not resolve; err = %v", err)
	}
}

func TestAuthRevokeSessionStoreErrorPropagates(t *testing.T) {
	st := newFakeAuthStore()
	st.seedEnabled("A1", "alice", domain.ActorHuman, "$h$secret")
	st.revokeSessionErr = errors.New("db down")
	a := newAuthFor(st, time.Hour)
	if err := a.RevokeSession(context.Background(), "x"); !errors.Is(err, st.revokeSessionErr) {
		t.Fatalf("store I/O error must propagate; got %v", err)
	}
}

// ---- accounts ------------------------------------------------------------------

func TestAuthCreateAccountNewThenReset(t *testing.T) {
	st := newFakeAuthStore()
	a := newAuthFor(st, time.Hour)

	n, err := a.CreateAccount(context.Background(), "carol", "", "password123")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !n.Created || n.Identity.Kind != domain.ActorHuman || n.Identity.AuthMethod != domain.AuthPassword {
		t.Fatalf("created account = %+v, want created human/password", n)
	}
	if got := st.cred[n.Identity.ID]; got != "$h$password123" {
		t.Fatalf("stored credential = %q", got)
	}

	r, err := a.CreateAccount(context.Background(), "carol", domain.ActorHuman, "newpass456")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if r.Created {
		t.Fatal("second create must report Created=false (reset)")
	}
	if got := st.cred[r.Identity.ID]; got != "$h$newpass456" {
		t.Fatalf("credential not replaced on reset: %q", got)
	}
}

func TestAuthCreateAccountValidation(t *testing.T) {
	a := newAuthFor(newFakeAuthStore(), time.Hour)
	cases := []struct {
		name string
		fn   func() error
	}{
		{"short password", func() error { _, e := a.CreateAccount(context.Background(), "d", "", "short"); return e }},
		{"empty name", func() error { _, e := a.CreateAccount(context.Background(), "  ", "", "password123"); return e }},
		{"invalid kind", func() error {
			_, e := a.CreateAccount(context.Background(), "e", domain.ActorType("robot"), "password123")
			return e
		}},
	}
	for _, tc := range cases {
		if err := tc.fn(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: err = %v, want domain.ErrInvalid", tc.name, err)
		}
	}
}

// ---- api tokens ----------------------------------------------------------------

func TestAuthMintAPIToken(t *testing.T) {
	st := newFakeAuthStore()
	a := newAuthFor(st, time.Hour)

	res, err := a.MintAPIToken(context.Background(), "agent-x", domain.ActorAgent, "ci key")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if res.Token == "" || res.Meta.ID.IsZero() {
		t.Fatalf("mint result = %+v, want token + meta id", res)
	}
	// Minting for an absent name creates a NEUTRAL (no-password) identity.
	id, ok := st.byName["agent-x"]
	if !ok || id.Kind != domain.ActorAgent || id.AuthMethod != domain.AuthNone {
		t.Fatalf("provisioned identity = %+v, want agent with no password", id)
	}
	if res.Meta.IdentityID != id.ID {
		t.Fatalf("meta identity %q does not match provisioned %q", res.Meta.IdentityID, id.ID)
	}

	// The minted secret resolves to its identity.
	if got, err := a.APIPrincipal(context.Background(), res.Token); err != nil || got.Name != "agent-x" {
		t.Fatalf("APIPrincipal = (%+v, %v), want agent-x", got, err)
	}
}

func TestAuthMintAPITokenValidation(t *testing.T) {
	a := newAuthFor(newFakeAuthStore(), time.Hour)
	cases := []struct {
		name string
		fn   func() error
	}{
		{"empty name", func() error { _, e := a.MintAPIToken(context.Background(), "", domain.ActorAgent, ""); return e }},
		{"invalid kind", func() error {
			_, e := a.MintAPIToken(context.Background(), "x", domain.ActorType("nope"), "")
			return e
		}},
	}
	for _, tc := range cases {
		if err := tc.fn(); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: err = %v, want domain.ErrInvalid", tc.name, err)
		}
	}
}

func TestAuthListAPITokens(t *testing.T) {
	st := newFakeAuthStore()
	a := newAuthFor(st, time.Hour)

	// Unknown identity → ErrNotFound (404 at the edge).
	if _, err := a.ListAPITokens(context.Background(), "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown identity = %v, want domain.ErrNotFound", err)
	}

	if _, err := a.CreateAccount(context.Background(), "alice", "", "password123"); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	none, err := a.ListAPITokens(context.Background(), "alice")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no-token list = (%v, %v), want non-nil empty slice", none, err)
	}

	m1, _ := a.MintAPIToken(context.Background(), "alice", domain.ActorHuman, "first")
	m2, _ := a.MintAPIToken(context.Background(), "alice", domain.ActorHuman, "second")
	rows, err := a.ListAPITokens(context.Background(), "alice")
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = (%v, %v), want 2 rows", rows, err)
	}
	// Newest first: the second mint is row[0].
	if rows[0].ID != m2.Meta.ID || rows[1].ID != m1.Meta.ID {
		t.Fatalf("ordering wrong: got [%s, %s], want [%s, %s]", rows[0].ID, rows[1].ID, m2.Meta.ID, m1.Meta.ID)
	}
	if rows[0].Label != "second" {
		t.Fatalf("row[0].label = %q", rows[0].Label)
	}

	// A store I/O error (not a sentinel) must propagate.
	st.listErr = errors.New("list boom")
	if _, err := a.ListAPITokens(context.Background(), "alice"); !errors.Is(err, st.listErr) {
		t.Fatalf("store I/O error must propagate; got %v", err)
	}
}

func TestAuthRevokeAPIToken(t *testing.T) {
	st := newFakeAuthStore()
	a := newAuthFor(st, time.Hour)
	res, _ := a.MintAPIToken(context.Background(), "agent-x", domain.ActorAgent, "ci")

	if err := a.RevokeAPIToken(context.Background(), res.Meta.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Already-disabled → idempotent no-op.
	if err := a.RevokeAPIToken(context.Background(), res.Meta.ID); err != nil {
		t.Fatalf("second revoke must be a no-op; got %v", err)
	}
	// The token can no longer resolve.
	if _, err := a.APIPrincipal(context.Background(), res.Token); !errors.Is(err, ErrAuthNoSession) {
		t.Fatalf("revoked token must not resolve; got %v", err)
	}

	// Unknown / empty id mapping.
	if err := a.RevokeAPIToken(context.Background(), ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty id = %v, want domain.ErrInvalid", err)
	}
	if err := a.RevokeAPIToken(context.Background(), "MISSING"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown id = %v, want domain.ErrNotFound", err)
	}
}
