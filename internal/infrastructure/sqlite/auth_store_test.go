package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// auth_store_test.go exercises application.AuthStore over a REAL SQLite handle (migrations
// applied, foreign_keys ON). It pins the semantics the gate + use case rely on: neutral vs
// credential upsert, enabled/disabled filtering in LookupCredentials, session expiry and
// revocation, api_token disable/last-used refresh, list ordering, and the not-found/error
// boundaries. Timestamps use second-precision RFC3339, so assertions keep ≥1s separation.

var tsBase = time.Date(2026, 2, 10, 8, 0, 0, 0, time.UTC)

func mkIdent(id, name string, kind domain.ActorType) domain.Identity {
	return domain.Identity{ID: domain.ID(id), Name: name, Kind: kind, CreatedAt: tsBase}
}

func mustEnsure(t *testing.T, s *authStore, id domain.Identity) domain.Identity {
	t.Helper()
	got, created, err := s.EnsureIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("EnsureIdentity(%s): %v", id.Name, err)
	}
	t.Logf("ensure %s created=%v", id.Name, created)
	return got
}

func disableIdentity(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE identity SET disabled_at = ? WHERE name = ?`, tsBase.Format(time.RFC3339), name); err != nil {
		t.Fatalf("disable %s: %v", name, err)
	}
}

func TestAuthStoreEnsureIdentity(t *testing.T) {
	s := newAuthStore(openTestDB(t))
	ctx := context.Background()

	first, created, err := s.EnsureIdentity(ctx, mkIdent("I1", "bob", domain.ActorHuman))
	if err != nil || !created {
		t.Fatalf("first ensure = (created=%v, err=%v), want created", created, err)
	}
	if first.AuthMethod != domain.AuthNone || first.ID != "I1" {
		t.Fatalf("neutral identity = %+v, want auth_method=none id=I1", first)
	}

	// Same name under a different proposed id → returns the EXISTING row unchanged.
	again, created2, err := s.EnsureIdentity(ctx, mkIdent("I2", "bob", domain.ActorAgent))
	if err != nil || created2 {
		t.Fatalf("second ensure = (created=%v, err=%v), want existing/unchanged", created2, err)
	}
	if again.ID != "I1" || again.Kind != domain.ActorHuman {
		t.Fatalf("existing row must be returned unchanged, got %+v", again)
	}
}

func TestAuthStoreUpsertPasswordCredential(t *testing.T) {
	s := newAuthStore(openTestDB(t))
	ctx := context.Background()

	id, created, err := s.UpsertPasswordCredential(ctx, mkIdent("I1", "carol", domain.ActorHuman), "HASH-A")
	if err != nil || !created || id.AuthMethod != domain.AuthPassword {
		t.Fatalf("create = (created=%v, auth=%q, err=%v)", created, id.AuthMethod, err)
	}
	if got := mustLookup(t, s, "carol"); got != "HASH-A" {
		t.Fatalf("credential after create = %q", got)
	}

	// Reset: overwrite the credential on an existing identity (created=false).
	_, created2, err := s.UpsertPasswordCredential(ctx, mkIdent("I1", "carol", domain.ActorHuman), "HASH-B")
	if err != nil || created2 {
		t.Fatalf("reset = (created=%v, err=%v), want existing/unchanged-id", created2, err)
	}
	if got := mustLookup(t, s, "carol"); got != "HASH-B" {
		t.Fatalf("credential after reset = %q, want HASH-B", got)
	}
}

func mustLookup(t *testing.T, s *authStore, name string) string {
	t.Helper()
	_, hash, ok, err := s.LookupCredentials(context.Background(), name)
	if err != nil || !ok {
		t.Fatalf("LookupCredentials(%s) = (ok=%v, %v), want enabled w/ hash", name, ok, err)
	}
	return hash
}

func TestAuthStoreLookupCredentials(t *testing.T) {
	db := openTestDB(t)
	s := newAuthStore(db)
	ctx := context.Background()

	// Missing → ok=false, not an error.
	if _, _, ok, err := s.LookupCredentials(ctx, "nobody"); err != nil || ok {
		t.Fatalf("missing = (ok=%v, %v), want ok=false no error", ok, err)
	}

	mustEnsure(t, s, mkIdent("I1", "alice", domain.ActorHuman))
	if _, _, err := s.UpsertPasswordCredential(ctx, mkIdent("I1", "alice", domain.ActorHuman), "H"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if h := mustLookup(t, s, "alice"); h != "H" {
		t.Fatalf("enabled credential = %q", h)
	}

	// Disabled → uniform ok=false (a disabled principal cannot log in).
	disableIdentity(t, db, "alice")
	if _, _, ok, err := s.LookupCredentials(ctx, "alice"); err != nil || ok {
		t.Fatalf("disabled lookup = (ok=%v, %v), want ok=false", ok, err)
	}
}

func TestAuthStoreIdentityByName(t *testing.T) {
	db := openTestDB(t)
	s := newAuthStore(db)
	ctx := context.Background()

	mustEnsure(t, s, mkIdent("I1", "dave", domain.ActorAgent))
	if got, err := s.IdentityByName(ctx, "dave"); err != nil || got.ID != "I1" {
		t.Fatalf("IdentityByName(dave) = (%+v, %v)", got, err)
	}
	// IdentityByName reports disabled identities too (token listing must see them).
	disableIdentity(t, db, "dave")
	if _, err := s.IdentityByName(ctx, "dave"); err != nil {
		t.Fatalf("disabled identity must still be resolvable: %v", err)
	}
	if _, err := s.IdentityByName(ctx, "ghost"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing = %v, want domain.ErrNotFound", err)
	}
}

func TestAuthStoreSessionLifecycle(t *testing.T) {
	db := openTestDB(t)
	s := newAuthStore(db)
	ctx := context.Background()

	mustEnsure(t, s, mkIdent("I1", "alice", domain.ActorHuman))
	if _, _, err := s.EnsureIdentity(ctx, mkIdent("ID2", "bob", domain.ActorAgent)); err != nil {
		t.Fatalf("ensure bob: %v", err)
	}
	disableIdentity(t, db, "bob") // a live session for a disabled identity must be rejected

	hLive := "sess-live"
	if err := s.CreateSession(ctx, "S1", "I1", hLive, tsBase, tsBase.Add(time.Hour)); err != nil {
		t.Fatalf("create live session: %v", err)
	}
	id, exp, err := s.ResolveSession(ctx, hLive, tsBase)
	if err != nil || id.Name != "alice" || !exp.Equal(tsBase.Add(time.Hour)) {
		t.Fatalf("resolve live = (%+v, %v, %v), want alice +1h", id, exp, err)
	}

	// Expired (expires_at in the past at `now`) → ErrAuthNoSession.
	if err := s.CreateSession(ctx, "S2", "I1", "sess-expired", tsBase.Add(-2*time.Hour), tsBase.Add(-time.Hour)); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, _, err := s.ResolveSession(ctx, "sess-expired", tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("expired = %v, want ErrAuthNoSession", err)
	}

	// Revocation blocks resolution and is idempotent.
	if err := s.RevokeSession(ctx, hLive, tsBase); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := s.ResolveSession(ctx, hLive, tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("revoked = %v, want ErrAuthNoSession", err)
	}
	if err := s.RevokeSession(ctx, hLive, tsBase); err != nil {
		t.Fatalf("second revoke must be a no-op: %v", err)
	}
	if err := s.RevokeSession(ctx, "never-existed", tsBase); err != nil {
		t.Fatalf("revoking an unknown hash must be a no-op: %v", err)
	}

	// A live session for a disabled identity is rejected.
	if err := s.CreateSession(ctx, "S3", "ID2", "sess-disabled-id", tsBase, tsBase.Add(time.Hour)); err != nil {
		t.Fatalf("create disabled-id session: %v", err)
	}
	if _, _, err := s.ResolveSession(ctx, "sess-disabled-id", tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("disabled-identity session = %v, want ErrAuthNoSession", err)
	}

	// Unknown token → ErrAuthNoSession.
	if _, _, err := s.ResolveSession(ctx, "no-such-token", tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("unknown = %v, want ErrAuthNoSession", err)
	}
}

func TestAuthStoreAPITokenLifecycle(t *testing.T) {
	db := openTestDB(t)
	s := newAuthStore(db)
	ctx := context.Background()

	mustEnsure(t, s, mkIdent("I1", "agent-x", domain.ActorAgent))

	// Two tokens with distinct creation instants for a deterministic newest-first list.
	if err := s.CreateAPIToken(ctx, "T1", "I1", "tok-old", "old", tsBase); err != nil {
		t.Fatalf("create old token: %v", err)
	}
	if err := s.CreateAPIToken(ctx, "T2", "I1", "tok-new", "new", tsBase.Add(time.Minute)); err != nil {
		t.Fatalf("create new token: %v", err)
	}

	// Resolve refreshes last_used_at lazily.
	if id, err := s.ResolveAPIToken(ctx, "tok-old", tsBase); err != nil || id.Name != "agent-x" {
		t.Fatalf("resolve = (%+v, %v), want agent-x", id, err)
	}
	rows, err := s.ListTokensByIdentity(ctx, "I1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = (%d rows, %v), want 2", len(rows), err)
	}
	// Newest first: T2 (created +1m) before T1.
	if rows[0].ID != "T2" || rows[1].ID != "T1" {
		t.Fatalf("ordering = [%s, %s], want [T2, T1]", rows[0].ID, rows[1].ID)
	}
	old := rows[1]
	if old.Label != "old" || old.LastUsedAt == nil || !old.LastUsedAt.Equal(tsBase) {
		t.Fatalf("row for T1 = %+v, want last_used stamped", old)
	}

	// Revoke disables the token; resolution then fails; re-revoke is a no-op.
	if err := s.RevokeAPITokenByID(ctx, "T1", tsBase); err != nil {
		t.Fatalf("revoke T1: %v", err)
	}
	if _, err := s.ResolveAPIToken(ctx, "tok-old", tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("revoked token = %v, want ErrAuthNoSession", err)
	}
	if err := s.RevokeAPITokenByID(ctx, "T1", tsBase); err != nil {
		t.Fatalf("second revoke must be a no-op: %v", err)
	}

	rows, _ = s.ListTokensByIdentity(ctx, "I1")
	for _, r := range rows {
		if r.ID == "T1" && !r.Disabled {
			t.Fatal("revoked token not flagged disabled in list")
		}
	}

	// Unknown id → domain.ErrNotFound; empty list for an identity with none.
	if err := s.RevokeAPITokenByID(ctx, "NOPE", tsBase); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown id = %v, want domain.ErrNotFound", err)
	}
	mustEnsure(t, s, mkIdent("I3", "loner", domain.ActorHuman))
	if none, err := s.ListTokensByIdentity(ctx, "I3"); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no-token list = (%v, %v), want non-nil empty", none, err)
	}

	// A token for a disabled identity does not resolve.
	disableIdentity(t, db, "agent-x")
	if _, err := s.ResolveAPIToken(ctx, "tok-new", tsBase); !errors.Is(err, application.ErrAuthNoSession) {
		t.Fatalf("disabled-identity token = %v, want ErrAuthNoSession", err)
	}
}
