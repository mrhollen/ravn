package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

// nowTS is a fixed instant used to stamp disabled identities in these fixtures.
var nowTS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Shared I/O-failure sentinels: the same value must appear on BOTH the fake's error
// field and the test's expectation so errors.Is (identity comparison) matches.
var (
	testGetDown    = errors.New("db closed")
	testReadDown   = errors.New("down")
	testMemberDown = errors.New("member down")
)

func ptrID(s string) *domain.ID { id := domain.ID(s); return &id }

// identity_resolver_test.go covers IdentityResolver / ResolveInProject against a small,
// self-contained fake. The resolver only reads repos.Identities, so the fixture UoW here
// binds just that port (mirroring the real UnitOfWork.Run contract of binding fakes into
// r before fn). It pins the three resolution outcomes, whitespace trimming, I/O-error
// propagation, and the owner/membership role fold.

type memberKey struct{ project, identity domain.ID }

type fakeIdents struct {
	byName map[string]domain.Identity
	member map[memberKey]domain.Role
	failGet    error
	failMember error
	failByIds error // injects a failure into GetByIds (display lookups)
	getCalls   int // to assert MemberRole is short-circuited for non-enabled subjects
}

// GetByIds mirrors the sqlite adapter's display semantics: outcome-INSENSITIVE —
// unknown ids are omitted from the map, disabled ids are INCLUDED (attribution, not
// auth), and it derives from byName so existing fixtures need no changes.
func (f *fakeIdents) GetByIds(_ context.Context, ids []domain.ID) (map[domain.ID]domain.Identity, error) {
	if f.failByIds != nil {
		return nil, f.failByIds
	}
	out := make(map[domain.ID]domain.Identity, len(ids))
	for _, id := range ids {
		for _, i := range f.byName {
			if i.ID == id {
				out[id] = i
			}
		}
	}
	return out, nil
}

func (f *fakeIdents) GetByName(_ context.Context, name string) (domain.Identity, error) {
	f.getCalls++
	if f.failGet != nil {
		return domain.Identity{}, f.failGet
	}
	id, ok := f.byName[name]
	if !ok {
		return domain.Identity{}, domain.ErrIdentityUnknown
	}
	if !id.Enabled() {
		return id, domain.ErrIdentityDisabled
	}
	return id, nil
}

func (f *fakeIdents) MemberRole(_ context.Context, projectID, identityID domain.ID) (domain.Role, error) {
	if f.failMember != nil {
		return domain.RoleNone, f.failMember
	}
	return f.member[memberKey{projectID, identityID}], nil // missing key → RoleNone (zero value)
}

var _ IdentityRepository = (*fakeIdents)(nil)

// idUoW binds only the identities port — the resolver reads nothing else.
type idUoW struct{ idents IdentityRepository }

func (u *idUoW) Run(ctx context.Context, r *Repos, fn func() error) error {
	r.Identities = u.idents
	return fn()
}

var _ UnitOfWork = (*idUoW)(nil)

func ident(id string, name string, disabled bool) domain.Identity {
	i := domain.Identity{ID: domain.ID(id), Name: name, Kind: domain.ActorHuman, AuthMethod: domain.AuthSharedToken}
	if disabled {
		d := nowTS
		i.DisabledAt = &d
	}
	return i
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name      string
		idents    *fakeIdents
		reqName   string
		wantSubj  domain.Subject
		wantErrIs error // nil means "no error expected"
	}{
		{
			name:     "enabled identity resolves to enabled subject with id",
			idents:   &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}},
			reqName:  "alice",
			wantSubj: domain.Subject{Name: "alice", Status: domain.SubjectEnabled, ID: domain.ID("id-a")},
		},
		{
			name:     "disabled identity resolves to disabled subject (not an error)",
			idents:   &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", true)}},
			reqName:  "bob",
			wantSubj: domain.Subject{Name: "bob", Status: domain.SubjectDisabled, ID: domain.ID("id-b")},
		},
		{
			name:     "unknown name resolves to unknown subject (not an error)",
			idents:   &fakeIdents{},
			reqName:  "ghost",
			wantSubj: domain.UnknownSubject("ghost"),
		},
		{
			name:     "whitespace is trimmed before lookup",
			idents:   &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}},
			reqName:  "  alice  ",
			wantSubj: domain.Subject{Name: "alice", Status: domain.SubjectEnabled, ID: domain.ID("id-a")},
		},
		{
			name:      "unexpected I/O failure propagates",
			idents:    &fakeIdents{failGet: testGetDown},
			reqName:   "alice",
			wantErrIs: testGetDown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewIdentityResolver(&idUoW{idents: c.idents})
			got, err := r.Resolve(context.Background(), c.reqName)
			if c.wantErrIs != nil {
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("Resolve error = %v; want %v", err, c.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve error = %v; want nil", err)
			}
			if got != c.wantSubj {
				t.Errorf("Resolve subject = %+v; want %+v", got, c.wantSubj)
			}
		})
	}
}

func TestResolveInProject(t *testing.T) {
	const proj = "proj-1"

	cases := []struct {
		name        string
		idents      *fakeIdents
		reqName     string
		createdBy   *domain.ID
		wantSubj    domain.Subject
		wantRole    domain.Role
		wantErrIs   error
	}{
		{
			name:      "owner (matches created_by) supersedes membership",
			idents:    &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}, member: map[memberKey]domain.Role{{proj, "id-a"}: domain.RoleViewer}},
			reqName:   "alice",
			createdBy: ptrID("id-a"),
			wantSubj:  domain.Subject{Name: "alice", Status: domain.SubjectEnabled, ID: domain.ID("id-a")},
			wantRole:  domain.RoleOwner,
		},
		{
			name:      "member keeps stored membership role (not the owner)",
			idents:    &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}, member: map[memberKey]domain.Role{{proj, "id-a"}: domain.RoleEditor}},
			reqName:   "alice",
			createdBy: ptrID("someone-else"),
			wantSubj:  domain.Subject{Name: "alice", Status: domain.SubjectEnabled, ID: domain.ID("id-a")},
			wantRole:  domain.RoleEditor,
		},
		{
			name:      "enabled non-member on a legacy project (nil created_by) → none",
			idents:    &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}},
			reqName:   "alice",
			createdBy: nil,
			wantSubj:  domain.Subject{Name: "alice", Status: domain.SubjectEnabled, ID: domain.ID("id-a")},
			wantRole:  domain.RoleNone,
		},
		{
			name:     "disabled subject gets no role and skips the membership read",
			idents:   &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", true)}},
			reqName:  "bob",
			wantSubj: domain.Subject{Name: "bob", Status: domain.SubjectDisabled, ID: domain.ID("id-b")},
			wantRole: domain.RoleNone,
		},
		{
			name:     "unknown subject gets no role",
			idents:   &fakeIdents{},
			reqName:  "ghost",
			wantSubj: domain.UnknownSubject("ghost"),
			wantRole: domain.RoleNone,
		},
		{
			name:      "I/O failure on the identity read propagates",
			idents:    &fakeIdents{failGet: testReadDown},
			reqName:   "alice",
			wantErrIs: testReadDown,
		},
		{
			name:      "I/O failure on the membership read propagates",
			idents:    &fakeIdents{byName: map[string]domain.Identity{"alice": ident("id-a", "alice", false)}, failMember: testMemberDown},
			reqName:   "alice",
			createdBy: ptrID("someone-else"),
			wantErrIs: testMemberDown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewIdentityResolver(&idUoW{idents: c.idents})
			subj, role, err := r.ResolveInProject(context.Background(), c.reqName, domain.ID(proj), c.createdBy)
			if c.wantErrIs != nil {
				if !errors.Is(err, c.wantErrIs) {
					t.Fatalf("ResolveInProject error = %v; want %v", err, c.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveInProject error = %v; want nil", err)
			}
			if subj != c.wantSubj {
				t.Errorf("subject = %+v; want %+v", subj, c.wantSubj)
			}
			if role != c.wantRole {
				t.Errorf("role = %s; want %s", role, c.wantRole)
			}
		})
	}
}

// TestResolveInProjectSkipsMembershipForNonEnabled pins the short-circuit: a disabled or
// unknown subject must NOT trigger a MemberRole read (it has no usable id / no access).
func TestResolveInProjectSkipsMembershipForNonEnabled(t *testing.T) {
	proj := domain.ID("proj-1")

	t.Run("disabled", func(t *testing.T) {
		f := &fakeIdents{byName: map[string]domain.Identity{"bob": ident("id-b", "bob", true)}, failMember: errors.New("should not be called")}
		r := NewIdentityResolver(&idUoW{idents: f})
		if _, _, err := r.ResolveInProject(context.Background(), "bob", proj, nil); err != nil {
			t.Fatalf("unexpected error (membership read must be skipped): %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		f := &fakeIdents{failMember: errors.New("should not be called")}
		r := NewIdentityResolver(&idUoW{idents: f})
		if _, _, err := r.ResolveInProject(context.Background(), "ghost", proj, nil); err != nil {
			t.Fatalf("unexpected error (membership read must be skipped): %v", err)
		}
	})
}
