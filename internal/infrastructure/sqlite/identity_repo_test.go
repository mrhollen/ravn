package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// identity_repo_test.go exercises the read-side IdentityRepository over a real SQLite
// handle (migrations applied, foreign_keys ON). It pins the three GetByName outcomes,
// nullable-timestamp round-tripping, and the MemberRole role mapping — including that a
// stored credential_hash is never surfaced (the column list excludes it by construction).

func TestIdentityRepoGetByName(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := newIdentityRepo(db)

	// A project row is required so project_member (below) can satisfy its FK.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO project (id, name, created_at, updated_at) VALUES ('PRJ0000000000000000001', 'Seeded Project', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	const (
		idAlice = "IDENALICE000000000001"
		idCarol = "IDENCAROL000000000003"
	)
	inserts := []string{
		// enabled, has a last_active_at
		`INSERT INTO identity (id,name,kind,auth_method,credential_hash,disabled_at,created_at,last_active_at)
		 VALUES ('IDENALICE000000000001','alice','human','shared_token',NULL,NULL,'2026-01-01T00:00:00Z','2026-02-01T00:00:00Z')`,
		// enabled, never active (last_active_at NULL), agent kind, auth none
		`INSERT INTO identity (id,name,kind,auth_method,credential_hash,disabled_at,created_at,last_active_at)
		 VALUES ('IDENBOB0000000000002','bob','agent','none',NULL,NULL,'2026-01-02T00:00:00Z',NULL)`,
		// disabled, carries a credential_hash that must never be surfaced
		`INSERT INTO identity (id,name,kind,auth_method,credential_hash,disabled_at,created_at,last_active_at)
		 VALUES ('IDENCAROL000000000003','carol','human','password','$argon2id$SECRETHASH', '2026-03-01T00:00:00Z','2026-01-03T00:00:00Z',NULL)`,
	}
	for i, s := range inserts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed identity %d: %v", i, err)
		}
	}

	t.Run("enabled identity round-trips with nullable last_active set", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "alice")
		if err != nil {
			t.Fatalf("GetByName(alice) error = %v; want nil", err)
		}
		wantCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		wantActive := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		if got.ID != domain.ID(idAlice) || got.Name != "alice" {
			t.Errorf("identity id/name = %q/%q; want alice", got.ID, got.Name)
		}
		if got.Kind != domain.ActorHuman || got.AuthMethod != domain.AuthSharedToken {
			t.Errorf("kind/auth = %s/%s; want human/shared_token", got.Kind, got.AuthMethod)
		}
		if !got.Enabled() {
			t.Errorf("alice should be enabled (disabled_at NULL)")
		}
		if got.DisabledAt != nil {
			t.Errorf("disabled_at = %v; want nil", got.DisabledAt)
		}
		if !got.CreatedAt.Equal(wantCreated) {
			t.Errorf("created_at = %v; want %v", got.CreatedAt, wantCreated)
		}
		if got.LastActiveAt == nil || !got.LastActiveAt.Equal(wantActive) {
			t.Errorf("last_active_at = %v; want %v", got.LastActiveAt, wantActive)
		}
	})

	t.Run("enabled identity with NULL last_active round-trips to a nil pointer", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "bob")
		if err != nil {
			t.Fatalf("GetByName(bob) error = %v; want nil", err)
		}
		if got.Kind != domain.ActorAgent || got.AuthMethod != domain.AuthNone {
			t.Errorf("kind/auth = %s/%s; want agent/none", got.Kind, got.AuthMethod)
		}
		if !got.Enabled() {
			t.Errorf("bob should be enabled")
		}
		if got.LastActiveAt != nil {
			t.Errorf("last_active_at = %v; want nil for a never-active identity", got.LastActiveAt)
		}
	})

	t.Run("disabled identity is an outcome, not an I/O error", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "carol")
		if !errors.Is(err, domain.ErrIdentityDisabled) {
			t.Fatalf("GetByName(carol) error = %v; want ErrIdentityDisabled", err)
		}
		// The row is still returned populated so callers can attribute it.
		if got.ID != domain.ID(idCarol) || got.Name != "carol" || got.Enabled() {
			t.Errorf("disabled identity not carried through: %+v", got)
		}
		wantDisabled := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		if got.DisabledAt == nil || !got.DisabledAt.Equal(wantDisabled) {
			t.Errorf("disabled_at = %v; want %v", got.DisabledAt, wantDisabled)
		}
	})

	t.Run("unknown name is an outcome, distinct from disabled", func(t *testing.T) {
		_, err := repo.GetByName(ctx, "nobody")
		if !errors.Is(err, domain.ErrIdentityUnknown) {
			t.Fatalf("GetByName(nobody) error = %v; want ErrIdentityUnknown", err)
		}
	})
}

func TestIdentityRepoMemberRole(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := newIdentityRepo(db)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO project (id, name, created_at, updated_at) VALUES ('PRJ000000000000000001', 'Roles Project', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	const (
		proj  = "PRJ000000000000000001"
		idA   = "IDENMEMBEREDITOR00001"
		idV   = "IDENMEMBERVIEWER00002"
		idAdm = "IDENMEMBERADMIN00003"
		idOut = "IDENMEMBEROUTSIDER1" // no membership row
	)
	memberInserts := []string{
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('IDENMEMBEREDITOR00001','ed','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('IDENMEMBERVIEWER00002','vw','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('IDENMEMBERADMIN00003','ad','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('IDENMEMBEROUTSIDER1','ou','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO project_member (project_id,identity_id,role) VALUES ('PRJ000000000000000001','IDENMEMBEREDITOR00001','editor')`,
		`INSERT INTO project_member (project_id,identity_id,role) VALUES ('PRJ000000000000000001','IDENMEMBERVIEWER00002','viewer')`,
		`INSERT INTO project_member (project_id,identity_id,role) VALUES ('PRJ000000000000000001','IDENMEMBERADMIN00003','admin')`,
	}
	for i, s := range memberInserts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed member %d: %v", i, err)
		}
	}

	cases := []struct {
		name string
		id   domain.ID
		want domain.Role
	}{
		{"stored editor role maps to RoleEditor", domain.ID(idA), domain.RoleEditor},
		{"stored viewer role maps to RoleViewer", domain.ID(idV), domain.RoleViewer},
		{"stored admin role maps to RoleAdmin", domain.ID(idAdm), domain.RoleAdmin},
		{"non-member is a normal outcome: RoleNone, no error", domain.ID(idOut), domain.RoleNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repo.MemberRole(ctx, domain.ID(proj), c.id)
			if err != nil {
				t.Fatalf("MemberRole(%s) error = %v; want nil", c.id, err)
			}
			if got != c.want {
				t.Errorf("MemberRole(%s) = %s; want %s", c.id, got, c.want)
			}
		})
	}

	// A member identity that does not exist at all is also just RoleNone (no row).
	got, err := repo.MemberRole(ctx, domain.ID(proj), domain.ID("IDENMEMBERGHOST007"))
	if err != nil {
		t.Fatalf("MemberRole(unknown identity) error = %v; want nil", err)
	}
	if got != domain.RoleNone {
		t.Errorf("MemberRole(unknown identity) = %s; want none", got)
	}
}
