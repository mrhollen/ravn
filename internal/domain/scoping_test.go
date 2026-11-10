package domain

import "testing"

// scoping_test.go exercises the config-driven visibility/authorization policies and
// the Role/Operation vocabulary they depend on. It is table-driven across BOTH
// scoping modes (default/global and membership-scoped) plus the edge subjects —
// unknown, disabled, unresolved, owner, member — so the regression-safe default and
// the isolated mode are both pinned down in one place.

func TestScopingConfigConstructors(t *testing.T) {
	defaults := DefaultScoping()
	if defaults != (ScopingConfig{Global: true, MembershipScoped: false}) {
		t.Errorf("DefaultScoping() = %+v; want {Global:true MembershipScoped:false}", defaults)
	}
	mem := MembershipScoping()
	if mem != (ScopingConfig{Global: false, MembershipScoped: true}) {
		t.Errorf("MembershipScoping() = %+v; want {Global:false MembershipScoped:true}", mem)
	}
	// The zero value must NOT silently equal the safe default — it is the restrictive
	// posture, so a deployment that forgets to pick a constructor stays locked down.
	var zero ScopingConfig
	if zero == defaults {
		t.Errorf("zero ScopingConfig unexpectedly equals DefaultScoping(); it must be the restrictive posture")
	}
}

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		r, min Role
		want   bool
	}{
		{RoleNone, RoleNone, true},
		{RoleNone, RoleViewer, false},
		{RoleViewer, RoleNone, true},
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleEditor, false},
		{RoleEditor, RoleAdmin, false},
		{RoleAdmin, RoleAdmin, true},
		{RoleOwner, RoleAdmin, true},
		{RoleOwner, RoleViewer, true},
		{RoleNone, RoleOwner, false},
	}
	for i, c := range cases {
		if got := c.r.AtLeast(c.min); got != c.want {
			t.Errorf("case %d: (%s).AtLeast(%s) = %v; want %v", i, c.r, c.min, got, c.want)
		}
	}
}

func TestParseRole(t *testing.T) {
	cases := []struct {
		in      string
		want    Role
		wantErr bool
	}{
		{"", RoleNone, false},
		{"none", RoleNone, false},
		{" viewer ", RoleViewer, false}, // trimmed + case-insensitive
		{"VIEWER", RoleViewer, false},
		{"editor", RoleEditor, false},
		{"admin", RoleAdmin, false},
		{"owner", 0, true},     // owner is derived, never stored — must be loud
		{"superuser", 0, true}, // unknown value
	}
	for i, c := range cases {
		got, err := ParseRole(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("case %d (%q): err = %v; wantErr %v", i, c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("case %d (%q): ParseRole = %s; want %s", i, c.in, got, c.want)
		}
	}
}

// roleStringRoundTrip pins the storage-boundary pair (ParseRole is the inverse of
// Role.String for the three stored roles).
func TestRoleStringRoundTrip(t *testing.T) {
	for _, r := range []Role{RoleNone, RoleViewer, RoleEditor, RoleAdmin} {
		back, err := ParseRole(r.String())
		if err != nil {
			t.Errorf("ParseRole(%s) errored: %v", r, err)
			continue
		}
		if back != r {
			t.Errorf("ParseRole(%s.String()=%q) = %s; want %s", r, r.String(), back, r)
		}
	}
}

func TestEffectiveRole(t *testing.T) {
	const ownerIDStr = "OWNR0ID00000000000000000" // arbitrary non-zero; EffectiveRole only compares equality
	ownerID := ID(ownerIDStr)

	cases := []struct {
		name    string
		ownerID *ID
		member  Role
		subj    Subject
		want    Role
	}{
		{
			name:    "enabled owner supersedes stored membership",
			ownerID: &ownerID, member: RoleEditor,
			subj: Subject{Name: "o", Status: SubjectEnabled, ID: ownerID},
			want: RoleOwner,
		},
		{
			name:    "enabled non-owner keeps stored membership role",
			ownerID: &ownerID, member: RoleAdmin,
			subj: Subject{Name: "m", Status: SubjectEnabled, ID: ID("OTHER")},
			want: RoleAdmin,
		},
		{
			name:    "enabled non-member with no row is none",
			ownerID: &ownerID, member: RoleNone,
			subj: Subject{Name: "n", Status: SubjectEnabled, ID: ID("OTHER")},
			want: RoleNone,
		},
		{
			name:    "legacy nil created_by ⇒ no one is owner; membership applies as-is",
			ownerID: nil, member: RoleViewer,
			subj: Subject{Name: "v", Status: SubjectEnabled, ID: ID("OTHER")},
			want: RoleViewer,
		},
		{
			name:    "legacy nil created_by and no membership ⇒ none (goes dark under membership mode)",
			ownerID: nil, member: RoleNone,
			subj: Subject{Name: "x", Status: SubjectEnabled, ID: ID("OTHER")},
			want: RoleNone,
		},
		{
			name:    "disabled former owner loses access to what they own",
			ownerID: &ownerID, member: RoleEditor,
			subj: Subject{Name: "o", Status: SubjectDisabled, ID: ownerID},
			want: RoleNone,
		},
		{
			name:    "unknown subject has no role even if it matches created_by (defensive)",
			ownerID: &ownerID, member: RoleAdmin,
			subj: UnknownSubject(ownerIDStr),
			want: RoleNone,
		},
		{
			name:    "zero-valued enabled id cannot be owner (IsZero guard)",
			ownerID: nil, member: RoleViewer,
			subj: Subject{Name: "z", Status: SubjectEnabled, ID: ID("")},
			want: RoleNone,
		},
		{
			name:    "stored row above admin is clamped (defensive; membership never stores owner)",
			ownerID: &ownerID, member: RoleOwner,
			subj: Subject{Name: "h", Status: SubjectEnabled, ID: ID("OTHER")},
			want: RoleAdmin,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveRole(c.ownerID, c.member, c.subj); got != c.want {
				t.Errorf("EffectiveRole = %s; want %s", got, c.want)
			}
		})
	}
}

// TestVisibilityPolicy pins both modes. In the global (default) config every subject —
// including unknown and unresolved ones — sees everything (today's behavior). In the
// membership-scoped config only enabled subjects with a real relationship see a project,
// and disabled subjects never do regardless of config.
func TestVisibilityPolicy(t *testing.T) {
	const ownerIDStr = "OWNR0ID00000000000000000"
	ownerID := ID(ownerIDStr)

	cases := []struct {
		name   string
		cfg    ScopingConfig
		subj   Subject
		role   Role
		want   bool
		method func(*VisibilityPolicy, Subject, Role) bool
	}{
		// --- global / default: see-all for every subject ---
		{"global enabled member", DefaultScoping(), Subject{Name: "m", Status: SubjectEnabled, ID: ID("m")}, RoleViewer, true, (*VisibilityPolicy).CanSeeProject},
		{"global enabled non-member", DefaultScoping(), Subject{Name: "x", Status: SubjectEnabled, ID: ID("x")}, RoleNone, true, (*VisibilityPolicy).CanSeeProject},
		{"global unknown subject", DefaultScoping(), UnknownSubject("ghost"), RoleNone, true, (*VisibilityPolicy).CanSeeProject},
		{"global unresolved zero subject", DefaultScoping(), Subject{}, RoleNone, true, (*VisibilityPolicy).CanSeeProject},
		// disabled is locked out even in global mode (disabled principals cannot authenticate)
		{"global disabled subject denied", DefaultScoping(), Subject{Name: "d", Status: SubjectDisabled, ID: ID("d")}, RoleNone, false, (*VisibilityPolicy).CanSeeProject},

		// --- membership-scoped: only enabled subjects with a real relationship ---
		{"member viewer sees", MembershipScoping(), Subject{Name: "m", Status: SubjectEnabled, ID: ID("m")}, RoleViewer, true, (*VisibilityPolicy).CanSeeProject},
		{"member none denied", MembershipScoping(), Subject{Name: "x", Status: SubjectEnabled, ID: ID("x")}, RoleNone, false, (*VisibilityPolicy).CanSeeProject},
		{"unknown subject denied in membership", MembershipScoping(), UnknownSubject("ghost"), RoleNone, false, (*VisibilityPolicy).CanSeeProject},
		{"disabled subject denied in membership", MembershipScoping(), Subject{Name: "d", Status: SubjectDisabled, ID: ownerID}, RoleOwner, false, (*VisibilityPolicy).CanSeeProject},

		// --- inheritance: tasks/comments/artifacts/events follow the project rule ---
		{"task inherits global see-all", DefaultScoping(), UnknownSubject("ghost"), RoleNone, true, (*VisibilityPolicy).CanSeeTask},
		{"comment inherits membership denial", MembershipScoping(), Subject{Name: "x", Status: SubjectEnabled, ID: ID("x")}, RoleNone, false, (*VisibilityPolicy).CanSeeComment},
		{"artifact inherits member visibility", MembershipScoping(), Subject{Name: "m", Status: SubjectEnabled, ID: ID("m")}, RoleEditor, true, (*VisibilityPolicy).CanSeeArtifact},
		{"event inherits membership denial for unknown", MembershipScoping(), UnknownSubject("ghost"), RoleNone, false, (*VisibilityPolicy).CanSeeEvent},
	}
	for _, c := range cases {
		p := NewVisibilityPolicy(c.cfg)
		t.Run(c.name, func(t *testing.T) {
			if got := c.method(p, c.subj, c.role); got != c.want {
				t.Errorf("CanSee = %v; want %v", got, c.want)
			}
		})
	}
}

// TestAuthorizationPolicy pins the gating axis. In the default (non-membership-scoped)
// config EVERY operation is allowed for every subject — including unknown/unresolved —
// which is the zero-change guarantee for existing deployments. Once membership scoping
// is on, an enabled subject needs a role meeting the operation minimum; disabled subjects
// are always denied in both modes.
func TestAuthorizationPolicy(t *testing.T) {
	cases := []struct {
		name string
		cfg  ScopingConfig
		subj Subject
		role Role
		op   Operation
		want bool
	}{
		// --- default / non-membership-scoped: no gating at all ---
		{"default read allowed for unknown", DefaultScoping(), UnknownSubject("ghost"), RoleNone, OpRead, true},
		{"default write allowed for unresolved", DefaultScoping(), Subject{}, RoleNone, OpWrite, true},
		{"default destructive allowed for anyone", DefaultScoping(), UnknownSubject("ghost"), RoleNone, OpDestructive, true},
		// disabled is denied even in default mode (cannot authenticate)
		{"default disabled denied", DefaultScoping(), Subject{Name: "d", Status: SubjectDisabled, ID: ID("d")}, RoleNone, OpRead, false},

		// --- membership-scoped: role-gated reads/writes/destructive ---
		{"member viewer can read", MembershipScoping(), Subject{Name: "v", Status: SubjectEnabled, ID: ID("v")}, RoleViewer, OpRead, true},
		{"member viewer cannot write", MembershipScoping(), Subject{Name: "v", Status: SubjectEnabled, ID: ID("v")}, RoleViewer, OpWrite, false},
		{"member editor can read+write not delete", MembershipScoping(), Subject{Name: "e", Status: SubjectEnabled, ID: ID("e")}, RoleEditor, OpWrite, true},
		{"member editor cannot destructive", MembershipScoping(), Subject{Name: "e", Status: SubjectEnabled, ID: ID("e")}, RoleEditor, OpDestructive, false},
		{"admin can read+write+destructive", MembershipScoping(), Subject{Name: "a", Status: SubjectEnabled, ID: ID("a")}, RoleAdmin, OpDestructive, true},
		{"owner (superset of admin) destructive ok", MembershipScoping(), Subject{Name: "o", Status: SubjectEnabled, ID: ID("o")}, RoleOwner, OpDestructive, true},
		{"none role denied everything in membership", MembershipScoping(), Subject{Name: "x", Status: SubjectEnabled, ID: ID("x")}, RoleNone, OpRead, false},
		{"unknown subject denied in membership", MembershipScoping(), UnknownSubject("ghost"), RoleViewer, OpRead, false},
		{"disabled denied even with owner role", MembershipScoping(), Subject{Name: "d", Status: SubjectDisabled, ID: ID("o")}, RoleOwner, OpDestructive, false},
	}
	for _, c := range cases {
		p := NewAuthorizationPolicy(c.cfg)
		t.Run(c.name, func(t *testing.T) {
			if got := p.Can(c.subj, c.role, c.op); got != c.want {
				t.Errorf("Can(%s) = %v; want %v", c.op, got, c.want)
			}
		})
	}
}

// opMinimumRole pins the operation→role mapping the authorization policy uses when
// membership scoping is on.
func TestOperationMinimumRole(t *testing.T) {
	if OpRead.minRole() != RoleViewer {
		t.Errorf("OpRead minimum = %s; want viewer", OpRead.minRole())
	}
	if OpWrite.minRole() != RoleEditor {
		t.Errorf("OpWrite minimum = %s; want editor", OpWrite.minRole())
	}
	if OpDestructive.minRole() != RoleAdmin {
		t.Errorf("OpDestructive minimum = %s; want admin", OpDestructive.minRole())
	}
}
