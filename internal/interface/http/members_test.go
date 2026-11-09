package httpapi

// T9 — Membership management API (backend). Covers AC1 (CRUD under the membership gate),
// AC2 (membership controls visibility — proven end-to-end with two independent session
// tokens), and the feature-off / not-enabled refusal shapes. The authorization-matrix,
// last-admin-conflict and malformed-input batteries live here; hostile role values and
// bodies are in members_adversarial_test.go per repo convention.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mrhollen/ravn/internal/application"
)

const membersPath = "/api/v1/projects/" // + id + "/members"

// membersListOut mirrors the GET envelope; Owner is pointer because it is null for
// ownerless (legacy / operator-created) projects.
type membersListOut struct {
	Owner   *actorDTO        `json:"owner"`
	Members []memberEntryDTO `json:"members"`
}

func memberAdd(t *testing.T, stk *scopingStack, pid string, body any, bearer string) (int, []byte) {
	t.Helper()
	return authReq(t, stk.Handler, "POST", membersPath+pid+"/members", body, bearer)
}

// TestMembers_TwoTokenMembershipProof is the AC2 proof: a member row created through
// this API — with NO raw SQL anywhere — flips an independent session from 403 to 200 on
// project reads, and removing the row flips it back.
func TestMembers_TwoTokenMembershipProof(t *testing.T) {
	stk := newMembersStack(t, AuthModeMultiuser)
	op := scopingSharedToken

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("alice", map[string]any{"name": "Proof"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	tokB := mustLogin(t, stk, "bob", "password123")

	// No membership yet: bob cannot even see the project (visibility line).
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pid, nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("non-member bob GET project = %d, want 403", code)
	}

	// alice (owner) grants bob editor through the API.
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "editor"}, tokA)
	if code != http.StatusCreated {
		t.Fatalf("alice grant bob = %d: %s", code, body)
	}
	var addResp memberRespDTO
	mustJSON(t, body, &addResp)
	if addResp.Username != "bob" || addResp.Role != "editor" || !addResp.Created {
		t.Fatalf("grant response = %+v; want bob/editor/created", addResp)
	}

	// The same session that was 403 a moment ago now reads the project and roster.
	code, body = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pid, nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("member bob GET project after grant = %d, want 200", code)
	}
	var list membersListOut
	code, body = authReq(t, stk.Handler, "GET", membersPath+pid+"/members", nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("member bob GET roster = %d: %s", code, body)
	}
	mustJSON(t, body, &list)
	if list.Owner == nil || list.Owner.Name != "alice" {
		t.Fatalf("roster owner = %+v; want alice (derived from created_by)", list.Owner)
	}
	found := false
	for _, m := range list.Members {
		if m.Username == "bob" && m.Role == "editor" {
			found = true
		}
	}
	if !found {
		t.Fatalf("roster members = %+v; want bob as editor", list.Members)
	}

	// Re-grant of the same (user, role) is idempotent: 200 + created:false.
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "editor"}, tokA)
	if code != http.StatusOK {
		t.Fatalf("re-grant bob = %d, want 200 (idempotent): %s", code, body)
	}
	mustJSON(t, body, &addResp)
	if addResp.Created {
		t.Fatalf("re-grant reported created=true: %+v", addResp)
	}

	// Removal through the API flips bob back to invisible.
	code, _ = authReq(t, stk.Handler, "DELETE", membersPath+pid+"/members/bob", nil, tokA)
	if code != http.StatusNoContent {
		t.Fatalf("alice remove bob = %d: %s", code, body)
	}
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pid, nil, tokB)
	if code != http.StatusForbidden {
		t.Fatalf("removed bob GET project = %d, want 403 again", code)
	}

	// Operator can still read the roster (operators are exempt from membership).
	code, _ = authReq(t, stk.Handler, "GET", membersPath+pid+"/members", nil, op)
	if code != http.StatusOK {
		t.Fatalf("operator GET roster = %d, want 200", code)
	}
}

// TestMembers_ListEnvelope: owner rendering (owner vs legacy null), username sorting,
// and the operator-created-ownerless shape.
func TestMembers_ListEnvelope(t *testing.T) {
	stk := newMembersStack(t, AuthModeMultiuser)
	op := scopingSharedToken

	// Ownerless project: created by the operator under a name with no identity row —
	// attribution resolves to nothing, so "owner" is null.
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("ghostop", map[string]any{"name": "Legacy"}), op)
	if code != http.StatusCreated {
		t.Fatalf("operator create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	// Provision the target identities first (membership never auto-provisions), then
	// add in deliberately unsorted order.
	mustCreateAccount(t, stk, "zed", "password123")
	mustCreateAccount(t, stk, "amy", "password123")
	for _, p := range []struct{ name, role string }{{"zed", "viewer"}, {"amy", "admin"}} {
		code, body = memberAdd(t, stk, pid, map[string]any{"username": p.name, "role": p.role}, op)
		if code != http.StatusCreated {
			t.Fatalf("add %s = %d: %s", p.name, code, body)
		}
	}

	var list membersListOut
	code, body = authReq(t, stk.Handler, "GET", membersPath+pid+"/members", nil, op)
	if code != http.StatusOK {
		t.Fatalf("GET roster = %d: %s", code, body)
	}
	mustJSON(t, body, &list)
	if list.Owner != nil {
		t.Fatalf("ownerless project rendered owner %+v; want null", list.Owner)
	}
	if len(list.Members) != 2 || list.Members[0].Username != "amy" || list.Members[1].Username != "zed" {
		t.Fatalf("members = %+v; want amy(admin) then zed(viewer), sorted by username", list.Members)
	}
	if list.Members[0].Role != "admin" || list.Members[1].Role != "viewer" {
		t.Fatalf("roles = %q/%q; want admin/viewer", list.Members[0].Role, list.Members[1].Role)
	}
}

// TestMembers_AuthorizationMatrix pins the role gate on every verb: viewers and editors
// can read the roster but not mutate it (403 required=admin resource=members), admins
// manage freely, and unknown projects 404 like any project-scoped route.
func TestMembers_AuthorizationMatrix(t *testing.T) {
	stk := newMembersStack(t, AuthModeMultiuser)

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("alice", map[string]any{"name": "Matrix"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	for _, name := range []string{"bob", "dave", "eve"} {
		mustCreateAccount(t, stk, name, "password123") // membership never auto-provisions: account first, grant second
	}
	for _, p := range []struct{ name, role string }{{"bob", "viewer"}, {"dave", "editor"}, {"eve", "admin"}} {
		code, body = memberAdd(t, stk, pid, map[string]any{"username": p.name, "role": p.role}, tokA)
		if code != http.StatusCreated {
			t.Fatalf("alice add %s(%s) = %d: %s", p.name, p.role, code, body)
		}
	}
	tokB := mustLogin(t, stk, "bob", "password123")
	tokD := mustLogin(t, stk, "dave", "password123")
	tokE := mustLogin(t, stk, "eve", "password123")

	// Viewer: read yes, every mutation 403 at the admin line.
	code, _ = authReq(t, stk.Handler, "GET", membersPath+pid+"/members", nil, tokB)
	if code != http.StatusOK {
		t.Fatalf("viewer GET roster = %d, want 200", code)
	}
	viewerDenials := []struct {
		method, path string
		body         any
	}{
		{"POST", membersPath + pid + "/members", map[string]any{"username": "bob", "role": "editor"}},
		{"PATCH", membersPath + pid + "/members/bob", map[string]any{"role": "admin"}},
		{"DELETE", membersPath + pid + "/members/dave", nil},
	}
	for _, c := range viewerDenials {
		code, body = authReq(t, stk.Handler, c.method, c.path, c.body, tokB)
		if code != http.StatusForbidden {
			t.Fatalf("viewer %s %s = %d, want 403: %s", c.method, c.path, code, body)
		}
		forbiddenBody(t, body, "members", "admin")
	}

	// Editor: same wall (member management sits ABOVE the editor line).
	code, body = authReq(t, stk.Handler, "POST", membersPath+pid+"/members",
		map[string]any{"username": "bob", "role": "editor"}, tokD)
	if code != http.StatusForbidden {
		t.Fatalf("editor POST member = %d, want 403: %s", code, body)
	}
	forbiddenBody(t, body, "members", "admin")

	// Admin: full management through the API.
	mustCreateAccount(t, stk, "carol", "password123") // provisioned but never granted yet — eve grants her
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "carol", "role": "viewer"}, tokE)
	if code != http.StatusCreated {
		t.Fatalf("eve add carol = %d: %s", code, body)
	}
	code, body = authReq(t, stk.Handler, "PATCH", membersPath+pid+"/members/bob", map[string]any{"role": "editor"}, tokE)
	if code != http.StatusOK {
		t.Fatalf("eve PATCH bob→editor = %d: %s", code, body)
	}
	code, _ = authReq(t, stk.Handler, "DELETE", membersPath+pid+"/members/dave", nil, tokE)
	if code != http.StatusNoContent {
		t.Fatalf("eve remove dave = %d, want 204: %s", code, body)
	}

	// Removal through the API also revokes visibility (AC2, mutation side).
	code, _ = authReq(t, stk.Handler, "GET", "/api/v1/projects/"+pid, nil, tokD)
	if code != http.StatusForbidden {
		t.Fatalf("removed dave GET project = %d, want 403", code)
	}

	// Unknown (but syntactically valid) project id 404s before the gate — both verbs.
	const missingULID = "01J3ZK8WQ9X7M2P4R6S8T2V0W4"
	code, _ = authReq(t, stk.Handler, "GET", membersPath+missingULID+"/members", nil, tokE)
	if code != http.StatusNotFound {
		t.Fatalf("GET roster unknown project = %d, want 404", code)
	}
	code, body = memberAdd(t, stk, missingULID, map[string]any{"username": "bob", "role": "viewer"}, tokE)
	if code != http.StatusNotFound {
		t.Fatalf("POST member unknown project = %d, want 404: %s", code, body)
	}
}

// TestMembers_LastAdminRefusal pins the AC3 conflict guard end-to-end on an OWNERLESS
// legacy project (owner-attributed projects are exempt — see the use-case tests): the
// sole stored admin cannot demote or remove self, but the operator can.
func TestMembers_LastAdminRefusal(t *testing.T) {
	stk := newMembersStack(t, AuthModeMultiuser)
	op := scopingSharedToken

	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("ghostop", map[string]any{"name": "Legacy"}), op)
	if code != http.StatusCreated {
		t.Fatalf("operator create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123") // membership never auto-provisions: account first, grant second
	mustCreateAccount(t, stk, "carol", "password123")
	for _, p := range []struct{ name, role string }{{"bob", "admin"}, {"carol", "viewer"}} {
		code, body = memberAdd(t, stk, pid, map[string]any{"username": p.name, "role": p.role}, op)
		if code != http.StatusCreated {
			t.Fatalf("add %s(%s) = %d: %s", p.name, p.role, code, body)
		}
	}
	tokB := mustLogin(t, stk, "bob", "password123")

	selfMutations := []struct {
		method, path string
		body         any
	}{
		{"PATCH", membersPath + pid + "/members/bob", map[string]any{"role": "viewer"}},
		{"DELETE", membersPath + pid + "/members/bob", nil},
	}
	for _, c := range selfMutations {
		code, body = authReq(t, stk.Handler, c.method, c.path, c.body, tokB)
		if code != http.StatusConflict {
			t.Fatalf("sole-admin %s self = %d, want 409: %s", c.method, code, body)
		}
		m := jmap(t, jraw(body))
		if got := jstr(m, "error"); got != ErrCodeConflict {
			t.Fatalf("409 error = %q, want %s: %s", got, ErrCodeConflict, body)
		}
		if got := jstr(m, "resource"); got != "members" {
			t.Fatalf("409 resource = %q, want members: %s", got, body)
		}
	}

	// The operator (owner-equivalent) is exempt and breaks the deadlock.
	code, _ = authReq(t, stk.Handler, "DELETE", membersPath+pid+"/members/bob", nil, op)
	if code != http.StatusNoContent {
		t.Fatalf("operator remove sole admin = %d: %s", code, body)
	}

	// carol is still a visible viewer; the project now has zero stored admins but an
	// operator can keep managing it — grant someone back in.
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "carol", "role": "admin"}, op)
	if code != http.StatusOK {
		t.Fatalf("operator promote carol (re-grant upsert) = %d: %s", code, body)
	}
}

// TestMembers_MembershipOffRefusesAllVerbs pins the feature gate: with RAVN_SCOPING off
// (global/default mode) every verb — even on a project the caller can manage in every
// other way — returns 403 with the machine-readable reason token.
func TestMembers_MembershipOffRefusesAllVerbs(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, nil, true, "") // global mode
	if stk.App.Memberships == nil {                             // wire it to prove refusal happens at the GATE, not for lack of wiring
		stk.App.Memberships = application.NewMemberships(stk.uow)
	}

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("alice", map[string]any{"name": "Off"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", membersPath + pid + "/members", nil},
		{"POST", membersPath + pid + "/members", map[string]any{"username": "alice", "role": "editor"}},
		{"PATCH", membersPath + pid + "/members/alice", map[string]any{"role": "viewer"}},
		{"DELETE", membersPath + pid + "/members/alice", nil},
	}
	for _, c := range cases {
		code, body = authReq(t, stk.Handler, c.method, c.path, c.body, tokA)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s (feature off) = %d, want 403: %s", c.method, c.path, code, body)
		}
		m := jmap(t, jraw(body))
		if got := jstr(m, "error"); got != ErrCodeForbidden {
			t.Fatalf("%s error = %q, want forbidden: %s", c.method, got, body)
		}
		if got := jstr(m, "reason"); got != "membership_scoping_disabled" {
			t.Fatalf("%s reason = %q, want membership_scoping_disabled: %s", c.method, got, body)
		}
	}

	// The shared operator token is refused too — the feature does not exist in this mode.
	code, _ = authReq(t, stk.Handler, "GET", membersPath+pid+"/members", nil, scopingSharedToken)
	if code != http.StatusForbidden {
		t.Fatalf("operator GET roster (feature off) = %d, want 403", code)
	}
}

// TestMembers_NotEnabled404 pins the composition guard: a membership-scoped deployment
// that booted without the Memberships use case answers 404 "not enabled" on every verb
// (same pattern as /auth/* when Deps.Auth is nil), distinct from the feature-off 403.
func TestMembers_NotEnabled404(t *testing.T) {
	stk := newScopingStack(t, AuthModeMultiuser, membershipCfg(), true, "") // scoping on, Memberships unwired

	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("alice", map[string]any{"name": "Unwired"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", membersPath + pid + "/members", nil},
		{"POST", membersPath + pid + "/members", map[string]any{"username": "alice", "role": "editor"}},
		{"PATCH", membersPath + pid + "/members/alice", map[string]any{"role": "viewer"}},
		{"DELETE", membersPath + pid + "/members/alice", nil},
	}
	for _, c := range cases {
		code, body = authReq(t, stk.Handler, c.method, c.path, c.body, tokA)
		if code != http.StatusNotFound {
			t.Fatalf("%s %s (unwired) = %d, want 404: %s", c.method, c.path, code, body)
		}
		m := jmap(t, jraw(body))
		if got := jstr(m, "error"); got != ErrCodeNotFound {
			t.Fatalf("%s error = %q, want not_found: %s", c.method, got, body)
		}
		if got := jstr(m, "resource"); got != "members" {
			t.Fatalf("%s resource = %q, want members: %s", c.method, got, body)
		}
	}
}

// TestMembers_ListUnknownProject404 — read verb against an id that was never created.
func TestMembers_ListUnknownProject404(t *testing.T) {
	stk := newMembersStack(t, AuthModeMultiuser)
	code, body := authReq(t, stk.Handler, "GET", membersPath+"01J3ZK8WQ9X7M2P4R6S8T2V0W4/members", nil, scopingSharedToken)
	if code != http.StatusNotFound {
		t.Fatalf("GET roster unknown project = %d: %s", code, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
}
