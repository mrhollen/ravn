package httpapi

// T9 — hostile input against the membership endpoints (repo convention: adversarial
// batteries live in *_adversarial_test.go). Covers role parsing edge cases, unknown
// JSON fields, malformed bodies, missing/blank usernames, unknown identities, disabled
// targets, and the accepted-but-ignored actor wire-compat field.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rawAuthReq is authReq for LITERAL body bytes: the shared helper json.Marshals its
// argument, which would base64-encode a []byte instead of sending it verbatim — this
// battery needs genuinely malformed JSON on the wire.
func rawAuthReq(t *testing.T, h http.Handler, method, path string, rawBody string, bearer string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if rawBody != "" {
		reader = strings.NewReader(rawBody)
	}
	req, err := http.NewRequest(method, "http://ravn.test"+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if rawBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res.Code, res.Body.Bytes()
}

// freshMembersFixture builds an owner-attributed project plus a provisioned, granted
// viewer ("bob") — enough state for every case below. The owner's token is the mutator.
func freshMembersFixture(t *testing.T) (*scopingStack, string, string) {
	t.Helper()
	stk := newMembersStack(t, AuthModeMultiuser)
	mustCreateAccount(t, stk, "alice", "password123")
	tokA := mustLogin(t, stk, "alice", "password123")
	code, body := authReq(t, stk.Handler, "POST", "/api/v1/projects",
		withActor("alice", map[string]any{"name": "Adv"}), tokA)
	if code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", code, body)
	}
	pid := extractID(t, body)

	mustCreateAccount(t, stk, "bob", "password123")
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "viewer"}, tokA)
	if code != http.StatusCreated {
		t.Fatalf("grant bob = %d: %s", code, body)
	}
	return stk, pid, tokA
}

func errEnvelope(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	m := jmap(t, jraw(body))
	if got := jstr(m, "error"); got == "" {
		t.Fatalf("response has no error code: %s", body)
	}
	return m
}

// TestMembers_AdversarialRoleValues pins parseMemberRole at the wire boundary.
func TestMembers_AdversarialRoleValues(t *testing.T) {
	stk, pid, tokA := freshMembersFixture(t)

	cases := []struct {
		raw      string // "" = field omitted
		wantCode int
		wantSub  string // substring asserted in the message ("" = no assertion)
	}{
		{"owner", http.StatusBadRequest, "cannot be assigned"},
		{"NONE", http.StatusBadRequest, "viewer, editor or admin"},
		{"none", http.StatusBadRequest, "viewer, editor or admin"},
		{"", http.StatusBadRequest, "role must be one of viewer, editor or admin"}, // omitted field
		{"  ", http.StatusBadRequest, ""},                                          // blank
		{"SUPERADMIN", http.StatusBadRequest, "not one of"},
		{"superadmin", http.StatusBadRequest, "not one of"},
		{"viewer2", http.StatusBadRequest, "not one of"},
		{"VIEWER", http.StatusOK, ""},  // case-insensitive; bob's row exists → idempotent re-grant (200)
		{"Editor ", http.StatusOK, ""}, // trimmed + cased: also a re-grant (bob is viewer, editor ≠ stored... still 200 upsert)
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			body := map[string]any{"username": "bob"}
			if c.raw != "" {
				body["role"] = c.raw
			}
			code, resp := memberAdd(t, stk, pid, body, tokA)
			if code != c.wantCode {
				t.Fatalf("role %q → %d, want %d: %s", c.raw, code, c.wantCode, resp)
			}
			if c.wantSub != "" {
				m := errEnvelope(t, resp)
				if got := jstr(m, "message"); !strings.Contains(got, c.wantSub) {
					t.Fatalf("role %q message = %q; want substring %q", c.raw, got, c.wantSub)
				}
			}
		})
	}

	// Distinguish the fresh-grant case cleanly: "VIEWER" for bob is a re-grant (row
	// exists) so it must be 200/created:false — verify the idempotent shape explicitly.
	var addResp memberRespDTO
	code, body := memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "VIEWER"}, tokA)
	if code != http.StatusOK {
		t.Fatalf("case-insensitive re-grant = %d: %s", code, body)
	}
	mustJSON(t, body, &addResp)
	if addResp.Created || addResp.Role != "viewer" {
		t.Fatalf("re-grant shape = %+v; want created=false role=viewer (normalized)", addResp)
	}
}

// TestMembers_AdversarialBodies pins decode-time refusals: unknown fields, malformed
// JSON, and blank/missing usernames are all 400 before any business logic runs.
func TestMembers_AdversarialBodies(t *testing.T) {
	stk, pid, tokA := freshMembersFixture(t)

	// Unknown field — DisallowUnknownFields must reject even with valid core fields.
	code, body := memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "viewer", "hax": true}, tokA)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400: %s", code, body)
	}

	// Blank and missing usernames.
	for _, c := range []map[string]any{
		{"username": "   ", "role": "viewer"},
		{"role": "viewer"}, // no username at all
	} {
		code, body = memberAdd(t, stk, pid, c, tokA)
		if code != http.StatusBadRequest {
			t.Fatalf("blank/missing username %v = %d, want 400: %s", c, code, body)
		}
		errEnvelope(t, body) // must be a structured error, not empty prose
	}

	// Malformed / non-JSON bodies on both mutating verbs (raw bytes — authReq would
	// base64-encode a []byte through json.Marshal).
	code, body = rawAuthReq(t, stk.Handler, "POST", membersPath+pid+"/members", "{not json", tokA)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed POST body = %d, want 400: %s", code, body)
	}
	code, body = rawAuthReq(t, stk.Handler, "PATCH", membersPath+pid+"/members/bob", "[]", tokA)
	if code != http.StatusBadRequest {
		t.Fatalf("non-object PATCH body = %d, want 400: %s", code, body)
	}

	// Wrong-typed core fields (role as number).
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": 42}, tokA)
	if code != http.StatusBadRequest {
		t.Fatalf("numeric role = %d, want 400: %s", code, body)
	}
}

// TestMembers_AdversarialActorField pins the wire-compat contract: a well-formed actor
// is accepted and IGNORED (the authenticated principal still acts), while a malformed
// one is rejected like on every other mutation endpoint.
func TestMembers_AdversarialActorField(t *testing.T) {
	stk, pid, tokA := freshMembersFixture(t)

	mustCreateAccount(t, stk, "carol", "password123")

	// Alice's session declares carol as actor — accepted, ignored: the grant still
	// succeeds AS ALICE (no spoofing; gating uses the principal, not the declared name).
	code, body := memberAdd(t, stk, pid, map[string]any{
		"username": "carol", "role": "viewer", "actor": map[string]any{"type": "agent", "name": "carol"},
	}, tokA)
	if code != http.StatusCreated {
		t.Fatalf("POST with declared actor = %d, want 201: %s", code, body)
	}

	// Malformed declared actors → 400, both verbs.
	malformed := []any{
		map[string]any{"actor": map[string]any{"name": ""}},                   // empty name
		map[string]any{"actor": map[string]any{"type": "alien", "name": "x"}}, // bad type
		map[string]any{"actor": map[string]any{"type": "human"}},              // missing name
		map[string]any{"actor": "i am an actor not an object"},                // wrong shape
	}
	for _, actor := range malformed {
		code, body = memberAdd(t, stk, pid, mergeMaps(map[string]any{"username": "carol", "role": "viewer"}, map[string]any{"actor": actor}), tokA)
		if code != http.StatusBadRequest {
			t.Fatalf("malformed actor %v → %d, want 400: %s", actor, code, body)
		}
		errEnvelope(t, body)
		code, body = authReq(t, stk.Handler, "PATCH", membersPath+pid+"/members/bob", mergeMaps(map[string]any{"role": "viewer"}, map[string]any{"actor": actor}), tokA)
		if code != http.StatusBadRequest {
			t.Fatalf("malformed actor on PATCH %v → %d, want 400: %s", actor, code, body)
		}
	}
}

// TestMembers_AdversarialTargetIdentities pins the 404 family: unknown names and disabled
// targets get DISTINCT envelopes (identity vs member resources).
func TestMembers_AdversarialTargetIdentities(t *testing.T) {
	stk, pid, tokA := freshMembersFixture(t)

	// Unknown identity on POST/DELETE → 404 resource=identity with the provisioning hint.
	code, body := memberAdd(t, stk, pid, map[string]any{"username": "ghost", "role": "viewer"}, tokA)
	if code != http.StatusNotFound {
		t.Fatalf("unknown username = %d, want 404: %s", code, body)
	}
	m := errEnvelope(t, body)
	if got := jstr(m, "resource"); got != "identity" {
		t.Fatalf("unknown-username resource = %q, want identity: %s", got, body)
	}
	if got := jstr(m, "message"); !strings.Contains(got, "provision") {
		t.Fatalf("unknown-username message lacks provisioning hint: %q", got)
	}

	code, _ = authReq(t, stk.Handler, "DELETE", membersPath+pid+"/members/ghost", nil, tokA)
	if code != http.StatusNotFound {
		t.Fatalf("DELETE unknown identity = %d, want 404: %s", code, body)
	}

	// Existing identity but NO member row → PATCH is 404 resource=member (not identity).
	mustCreateAccount(t, stk, "dave", "password123") // provisioned, never granted
	code, body = authReq(t, stk.Handler, "PATCH", membersPath+pid+"/members/dave", map[string]any{"role": "editor"}, tokA)
	if code != http.StatusNotFound {
		t.Fatalf("PATCH non-member = %d, want 404: %s", code, body)
	}
	m = errEnvelope(t, body)
	if got := jstr(m, "resource"); got != "member" {
		t.Fatalf("non-member resource = %q, want member: %s", got, body)
	}

	// Disabled target: grant and demote refuse (403), removal is still allowed (204).
	if _, err := stk.db.Exec("UPDATE identity SET disabled_at = '2026-01-01T00:00:00Z' WHERE name = ?", "bob"); err != nil {
		t.Fatalf("disable bob: %v", err)
	}
	code, body = memberAdd(t, stk, pid, map[string]any{"username": "bob", "role": "editor"}, tokA)
	if code != http.StatusForbidden {
		t.Fatalf("grant disabled identity = %d, want 403: %s", code, body)
	}
	m = errEnvelope(t, body)
	if got := jstr(m, "error"); got != ErrCodeForbidden || jstr(m, "resource") != "members" {
		t.Fatalf("disabled-grant envelope = %s; want forbidden/members", body)
	}
	code, _ = authReq(t, stk.Handler, "PATCH", membersPath+pid+"/members/bob", map[string]any{"role": "editor"}, tokA)
	if code != http.StatusForbidden {
		t.Fatalf("demote disabled identity = %d, want 403: %s", code, body)
	}
	code, _ = authReq(t, stk.Handler, "DELETE", membersPath+pid+"/members/bob", nil, tokA)
	if code != http.StatusNoContent {
		t.Fatalf("remove disabled identity's row = %d, want 204: %s", code, body)
	}
}

// small test-local helper (kept here so the two members_* files stay self-contained)

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
