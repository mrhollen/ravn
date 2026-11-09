package sqlite

// identity_member_write_test.go exercises T9's IdentityRepository write side over a real
// SQLite handle (migrations applied, foreign_keys ON): upsert idempotence + role
// vocabulary enforcement, remove-as-outcome (ErrNotFound when no row), and the stable
// ListMembers order. It mirrors identity_repo_test.go's seeding style (raw INSERTs with
// fixed ids so FKs are satisfiable).

import (
	"context"
	"errors"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestIdentityRepoMemberWrites(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := newIdentityRepo(db)

	const proj = "PRJMEMWRITE000000001"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO project (id, name, created_at, updated_at) VALUES (?, 'Write Project', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, proj); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	const (
		idA = "IDENWRITA00000000001" // alice
		idB = "IDENWRITEB0000000002" // bob
		idC = "IDENWRITCC0000000003" // carol
	)
	for _, s := range []string{
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('` + idA + `','alice','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('` + idB + `','bob','human','none',NULL,'2026-01-01T00:00:00Z')`,
		`INSERT INTO identity (id,name,kind,auth_method,disabled_at,created_at) VALUES ('` + idC + `','carol','agent','none',NULL,'2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed identity: %v", err)
		}
	}

	t.Run("upsert inserts a new row readable by MemberRole and ListMembers", func(t *testing.T) {
		if err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idA), domain.RoleEditor); err != nil {
			t.Fatalf("UpsertMember alice=editor: %v", err)
		}
		got, err := repo.MemberRole(ctx, domain.ID(proj), domain.ID(idA))
		if err != nil || got != domain.RoleEditor {
			t.Errorf("MemberRole(alice) = %s, %v; want editor, nil", got, err)
		}
	})

	t.Run("upsert is idempotent: same role re-grant and a different-role update both succeed", func(t *testing.T) {
		if err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idA), domain.RoleEditor); err != nil {
			t.Fatalf("re-grant same role: %v", err)
		}
		if err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idA), domain.RoleAdmin); err != nil {
			t.Fatalf("update to admin: %v", err)
		}
		got, err := repo.MemberRole(ctx, domain.ID(proj), domain.ID(idA))
		if err != nil || got != domain.RoleAdmin {
			t.Errorf("MemberRole(alice) = %s, %v; want admin, nil after update", got, err)
		}
		// exactly one row: the PK upsert must not duplicate
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM project_member WHERE project_id = ? AND identity_id = ?`, proj, idA).Scan(&n); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if n != 1 {
			t.Errorf("row count for alice = %d; want 1 (upsert must not duplicate)", n)
		}
	})

	t.Run("upsert rejects non-storable roles without touching the table", func(t *testing.T) {
		for _, role := range []domain.Role{domain.RoleNone, domain.RoleOwner} {
			err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idB), role)
			if err == nil {
				t.Errorf("UpsertMember(role=%s) error = nil; want wrapped ErrInvalid", role.String())
			} else if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("UpsertMember(role=%s) error = %v; want wrapped ErrInvalid", role.String(), err)
			}
		}
		if got, _ := repo.MemberRole(ctx, domain.ID(proj), domain.ID(idB)); got != domain.RoleNone {
			t.Errorf("bob's role after rejected upserts = %s; want none (table untouched)", got)
		}
	})

	t.Run("ListMembers returns stable identity_id order with project scoping", func(t *testing.T) {
		if err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idC), domain.RoleViewer); err != nil {
			t.Fatalf("upsert carol: %v", err)
		}
		if err := repo.UpsertMember(ctx, domain.ID(proj), domain.ID(idB), domain.RoleEditor); err != nil {
			t.Fatalf("upsert bob: %v", err)
		}
		got, err := repo.ListMembers(ctx, domain.ID(proj))
		if err != nil {
			t.Fatalf("ListMembers: %v", err)
		}
		wantRoles := map[string]domain.Role{idA: domain.RoleAdmin, idB: domain.RoleEditor, idC: domain.RoleViewer}
		if len(got) != 3 {
			t.Fatalf("len(members) = %d; want 3", len(got))
		}
		for i, m := range got {
			if m.ProjectID != domain.ID(proj) {
				t.Errorf("member[%d] = %+v; want project-scoped row", i, m)
				continue
			}
			wantRole, okKey := wantRoles[string(m.IdentityID)]
			if !okKey {
				t.Errorf("member[%d] = %+v; not one of the seeded identities", i, m)
				continue
			}
			if m.Role != wantRole {
				t.Errorf("member[%d] %s role = %s; want %s", i, m.IdentityID, m.Role.String(), wantRole.String())
			}
			if i > 0 && string(got[i-1].IdentityID) >= string(got[i].IdentityID) {
				t.Errorf("order not ascending at index %d: %s !< %s", i, got[i-1].IdentityID, got[i].IdentityID)
			}
		}
	})

	t.Run("ListMembers of a project with no rows returns an empty non-nil slice", func(t *testing.T) {
		got, err := repo.ListMembers(ctx, domain.ID("PRJMEMWRITE000000002")) // no such project: still zero rows, no FK checked on read
		if err != nil {
			t.Fatalf("ListMembers(empty) error = %v; want nil", err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("ListMembers(empty) = %#v; want empty non-nil slice", got)
		}
	})

	t.Run("remove deletes the row; second remove is a wrapped ErrNotFound outcome", func(t *testing.T) {
		if err := repo.RemoveMember(ctx, domain.ID(proj), domain.ID(idB)); err != nil {
			t.Fatalf("RemoveMember(bob): %v", err)
		}
		if got, _ := repo.MemberRole(ctx, domain.ID(proj), domain.ID(idB)); got != domain.RoleNone {
			t.Errorf("bob's role after remove = %s; want none", got)
		}
		err := repo.RemoveMember(ctx, domain.ID(proj), domain.ID(idB))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second RemoveMember(bob) error = %v; want wrapped ErrNotFound", err)
		}
	})

	t.Run("remove of a pair that never had a row is the ErrNotFound outcome", func(t *testing.T) {
		err := repo.RemoveMember(ctx, domain.ID(proj), domain.ID(idB)) // bob already removed above — no row exists
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("RemoveMember(rowless pair) error = %v; want wrapped ErrNotFound", err)
		}
	})

	t.Run("rows survive across operations in the exact expected set", func(t *testing.T) {
		got, err := repo.ListMembers(ctx, domain.ID(proj))
		if err != nil {
			t.Fatalf("ListMembers: %v", err)
		}
		var names []string
		for _, m := range got {
			names = append(names, string(m.IdentityID))
		}
		if len(got) != 2 { // bob removed; alice + carol remain
			t.Fatalf("surviving rows = %v; want the 2 un-removed identities", names)
		}
	})
}
