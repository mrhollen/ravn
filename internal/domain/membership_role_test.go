package domain

// membership_role_test.go pins T9's pure management-guard primitives: Role.Storable
// (the storage vocabulary) and CanAssignRole (the hierarchy rule). The guards' I/O-
// dependent composition lives in the Memberships use case tests.

import "testing"

func TestRoleStorable(t *testing.T) {
	cases := []struct {
		role Role
		want bool
	}{
		{RoleNone, false}, // "no row" — never written
		{RoleViewer, true},
		{RoleEditor, true},
		{RoleAdmin, true},
		{RoleOwner, false}, // derived from created_by — never stored
	}
	for _, c := range cases {
		if got := c.role.Storable(); got != c.want {
			t.Errorf("Role(%s).Storable() = %v; want %v", c.role.String(), got, c.want)
		}
	}
}

func TestCanAssignRole(t *testing.T) {
	cases := []struct {
		name   string
		actor  Role // the acting manager's EFFECTIVE role (operator folded in as RoleOwner by the gate)
		target Role
		want   bool
	}{
		{"owner may assign any storable role", RoleOwner, RoleAdmin, true},
		{"owner may demote to viewer", RoleOwner, RoleViewer, true},
		{"admin may assign admin (self/peers at own level)", RoleAdmin, RoleAdmin, true},
		{"admin may demote to editor", RoleAdmin, RoleEditor, true},
		{"admin may demote to viewer", RoleAdmin, RoleViewer, true},
		{"editor may keep editor", RoleEditor, RoleEditor, true},
		{"editor may assign viewer below self", RoleEditor, RoleViewer, true},
		{"editor cannot elevate to admin", RoleEditor, RoleAdmin, false},
		{"viewer may keep viewer", RoleViewer, RoleViewer, true},
		{"viewer cannot assign editor", RoleViewer, RoleEditor, false},
		{"none-role caller assigns nothing (gate blocks before the guard; pure check agrees)", RoleNone, RoleViewer, false},

		// Defensive: this function is a total-order check over any two roles — it says
		// nothing about storable-ness (the use case validates that separately). Pin the
		// raw comparison for out-of-vocabulary inputs so a refactor cannot silently
		// change semantics.
		{"owner above owner (unreachable in practice)", RoleOwner, RoleOwner, true},
		{"none target below everyone", RoleViewer, RoleNone, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CanAssignRole(c.actor, c.target); got != c.want {
				t.Errorf("CanAssignRole(%s, %s) = %v; want %v", c.actor.String(), c.target.String(), got, c.want)
			}
		})
	}
}
