package application

// memberships_test.go drives the T9 Memberships use case against the package's existing
// fakeIdents + idUoW (no new fixture): every guard outcome — hierarchy refusal, last-
// admin refusal and its exemptions, unknown/disabled/not-a-member distinctions, created
// vs re-grant, and disabled-row removal. Role inputs arrive Storable (the adapter
// validates free-form strings first); actorRole is the gate-computed effective role.

import (
	"context"
	"errors"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

func memberProject(createdBy string) domain.Project {
	p := domain.Project{ID: "PROJMEM000000000001"}
	if createdBy != "" {
		id := domain.ID(createdBy)
		p.CreatedBy = &id
	}
	return p
}

func TestMembershipsGrant(t *testing.T) {
	proj := memberProject("") // legacy: no owner

	cases := []struct {
		name        string
		idents      *fakeIdents
		targetName  string
		targetRole  domain.Role
		actorRole   domain.Role
		wantMember  bool // true when a row should end up stored (err == nil)
		wantCreated bool
		wantErrIs   error
	}{
		{
			name:       "fresh grant of viewer by an admin creates the row",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}},
			targetName: "alice", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantMember: true, wantCreated: true,
		},
		{
			name:       "re-grant is idempotent and reports created=false",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}, member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleViewer}},
			targetName: "alice", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantMember: true, wantCreated: false,
		},
		{
			name:       "admin may assign admin to a peer (at own level)",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)}},
			targetName: "bob", targetRole: domain.RoleAdmin, actorRole: domain.RoleAdmin,
			wantMember: true, wantCreated: true,
		},
		{
			name:       "owner (and folded operator) may assign any storable role",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"carol": ident("id-c", "carol", false)}},
			targetName: "carol", targetRole: domain.RoleAdmin, actorRole: domain.RoleOwner,
			wantMember: true, wantCreated: true,
		},
		{
			name:       "whitespace around the name is trimmed before resolution",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}},
			targetName: "  alice  ", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantMember: true, wantCreated: true,
		},
		{
			name:       "editor cannot elevate a member to admin",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)}},
			targetName: "bob", targetRole: domain.RoleAdmin, actorRole: domain.RoleEditor,
			wantErrIs: domain.ErrCannotAssign,
		},
		{
			name:       "viewer cannot assign editor",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)}},
			targetName: "bob", targetRole: domain.RoleEditor, actorRole: domain.RoleViewer,
			wantErrIs: domain.ErrCannotAssign,
		},
		{
			name:       "unknown username is refused without provisioning (distinct sentinel)",
			idents:     &fakeIdents{},
			targetName: "ghost", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrIdentityUnknown,
		},
		{
			name:       "disabled identity cannot be granted membership",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", true)}},
			targetName: "bob", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrIdentityDisabled,
		},
		{
			name:       "granting a non-admin role loses no admin status even when the sole admin acts",
			idents:     &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false), "bob": ident("id-b", "bob", false)}, member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin}},
			targetName: "bob", targetRole: domain.RoleEditor, actorRole: domain.RoleAdmin, // alice (admin) grants bob editor; no admin status lost
			wantMember: true, wantCreated: true,
		},
		{
			name:       "empty username is invalid input, not a lookup",
			idents:     &fakeIdents{byName: map[string]domain.Identity{}},
			targetName: "   ", targetRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrInvalid,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewMemberships(&idUoW{idents: c.idents})
			member, created, err := m.Grant(context.Background(), proj, c.targetName, c.targetRole, c.actorRole)
			if c.wantErrIs != nil {
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("Grant error = %v; want %v", err, c.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Grant error = %v; want nil", err)
			}
			if created != c.wantCreated {
				t.Errorf("created = %v; want %v", created, c.wantCreated)
			}
			got, rerr := c.idents.MemberRole(context.Background(), proj.ID, member.IdentityID)
			if rerr != nil || got != c.targetRole {
				t.Errorf("stored role = %s (%v); want %s", got, rerr, c.targetRole.String())
			}
		})
	}
}

func TestMembershipsSetRole(t *testing.T) {
	cases := []struct {
		name      string
		createdBy string // "" = legacy project (no owner); else the owner's identity id
		idents    *fakeIdents
		target    string
		newRole   domain.Role
		actorRole domain.Role
		wantErrIs error
	}{
		{
			name: "sole admin on an ownerless project cannot demote self — last-admin refusal",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", newRole: domain.RoleEditor, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrLastAdmin,
		},
		{
			name: "with a second stored admin the same self-demotion is allowed",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false), "bob": ident("id-b", "bob", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin, {"PROJMEM000000000001", "id-b"}: domain.RoleAdmin},
			},
			target: "alice", newRole: domain.RoleEditor, actorRole: domain.RoleAdmin,
			wantErrIs: nil,
		},
		{
			name:      "a project WITH an owner stays manageable: sole stored admin demotes self",
			createdBy: "id-o",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false), "owner": ident("id-o", "owner", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", newRole: domain.RoleViewer, actorRole: domain.RoleAdmin, // a plain admin acts; the owner row alone keeps the project manageable
			wantErrIs: nil,
		},
		{
			name: "operator (folded RoleOwner) may demote the last stored admin on a legacy project",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", newRole: domain.RoleViewer, actorRole: domain.RoleOwner,
			wantErrIs: nil,
		},
		{
			name: "editor cannot elevate an existing member to admin (hierarchy)",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-b"}: domain.RoleViewer},
			},
			target: "bob", newRole: domain.RoleAdmin, actorRole: domain.RoleEditor,
			wantErrIs: domain.ErrCannotAssign,
		},
		{
			name:   "non-member target is ErrNotFound, distinct from unknown identity",
			idents: &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)}},
			target: "bob", newRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrNotFound,
		},
		{
			name:   "unknown target stays ErrIdentityUnknown (not the not-a-member sentinel)",
			idents: &fakeIdents{},
			target: "ghost", newRole: domain.RoleViewer, actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrIdentityUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewMemberships(&idUoW{idents: c.idents})
			_, err := m.SetRole(context.Background(), memberProject(c.createdBy), c.target, c.newRole, c.actorRole)
			if c.wantErrIs != nil {
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("SetRole error = %v; want %v", err, c.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetRole error = %v; want nil", err)
			}
			var id domain.ID
			for _, i := range c.idents.byName {
				if i.Name == c.target {
					id = i.ID
				}
			}
			got, _ := c.idents.MemberRole(context.Background(), domain.ID("PROJMEM000000000001"), id)
			if got != c.newRole {
				t.Errorf("stored role after SetRole = %s; want %s", got.String(), c.newRole.String())
			}
		})
	}
}

func TestMembershipsRemove(t *testing.T) {
	cases := []struct {
		name      string
		createdBy string // "" = legacy project (no owner); else the owner's identity id
		idents    *fakeIdents
		target    string
		actorRole domain.Role
		wantErrIs error
	}{
		{
			name: "removing a sole stored admin on an ownerless project is refused (self-removal)",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrLastAdmin,
		},
		{
			name: "operator may remove the last stored admin on an ownerless project",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", actorRole: domain.RoleOwner,
			wantErrIs: nil,
		},
		{
			name:      "owner's project: removing the sole stored admin leaves the owner in charge — allowed",
			createdBy: "id-o",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false), "owner": ident("id-o", "owner", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin},
			},
			target: "alice", actorRole: domain.RoleOwner,
			wantErrIs: nil,
		},
		{
			name: "removing a non-admin member never trips the last-admin guard",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-b"}: domain.RoleViewer},
			},
			target: "bob", actorRole: domain.RoleAdmin,
			wantErrIs: nil,
		},
		{
			name: "removing a disabled member's row is allowed (tidying up)",
			idents: &fakeIdents{
				byName: map[string]domain.Identity{"bob": ident("id-b", "bob", true)},
				member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-b"}: domain.RoleViewer},
			},
			target: "bob", actorRole: domain.RoleAdmin,
			wantErrIs: nil,
		},
		{
			name:   "non-member target is the not-a-member outcome",
			idents: &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", false)}},
			target: "bob", actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrNotFound,
		},
		{
			name:   "unknown target is ErrIdentityUnknown, distinct from not-a-member",
			idents: &fakeIdents{},
			target: "ghost", actorRole: domain.RoleAdmin,
			wantErrIs: domain.ErrIdentityUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewMemberships(&idUoW{idents: c.idents})
			p := memberProject(c.createdBy)
			err := m.Remove(context.Background(), p, c.target, c.actorRole)
			if c.wantErrIs != nil {
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("Remove error = %v; want %v", err, c.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Remove error = %v; want nil", err)
			}
			var id domain.ID
			for _, i := range c.idents.byName {
				if i.Name == c.target {
					id = i.ID
				}
			}
			got, _ := c.idents.MemberRole(context.Background(), p.ID, id)
			if got != domain.RoleNone {
				t.Errorf("row after Remove still has role %s; want none (deleted)", got.String())
			}
		})
	}
}

func TestMembershipsList(t *testing.T) {
	m := NewMemberships(&idUoW{idents: &fakeIdents{
		byName: map[string]domain.Identity{
			"bob":   ident("id-b", "bob", false),
			"alice": ident("id-a", "alice", false),
		},
		member: map[memberKey]domain.Role{
			{"PROJMEM000000000001", "id-b"}: domain.RoleViewer,
			{"PROJMEM000000000001", "id-a"}: domain.RoleAdmin,
		},
	}})
	got, err := m.List(context.Background(), memberProject(""))
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(got) != 2 || got[0].IdentityID != "id-a" || got[1].IdentityID != "id-b" {
		t.Fatalf("List order/content = %+v; want id-a then id-b (stable identity_id order)", got)
	}
	// disabled identities' rows are part of the raw view (policy filtering is upstream)
	m2 := NewMemberships(&idUoW{idents: &fakeIdents{
		byName: map[string]domain.Identity{"zed": ident("id-z", "zed", true)},
		member: map[memberKey]domain.Role{{"PROJMEM000000000001", "id-z"}: domain.RoleEditor},
	}})
	got2, err := m2.List(context.Background(), memberProject(""))
	if err != nil || len(got2) != 1 {
		t.Fatalf("List with disabled identity = %+v, %v; want the row included", got2, err)
	}
}
