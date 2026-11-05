package httpapi

// id_validation_adversarial_test.go: hostile ID input at the REST boundary.
// Malformed identifiers (wrong length, non-Crockford characters, embedded path
// separators) must die at the edge — parseBoundaryID, parseTaskFilter or
// handleCreateTask — with a 400 invalid_input envelope: never forwarded to
// storage, never echoed back in full (oversized values stay bounded), and a
// well-formed-but-unknown ULID still falls through to the honest 404.

import (
	"net/http"
	"strings"
	"testing"
)

// hostileULID is a multi-megabyte "id": every character is Crockford-legal, so
// only its length can reject it — and the error must not echo it back.
var hostileULID = strings.Repeat("A", 1<<20) // 1 MiB

func TestIDBoundary_MalformedIDsRejectedBeforeStorage(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	shortID := "T1"                             // too short
	badCharID := "0734567890abcdefghij00000000" // 26 chars, but 'i' is not Crockford
	wellKnown := makeTask(t, stk, createProject(t, stk, "known").ID, "known", nil)

	cases := []struct {
		method, path string
	}{
		// Path-position ids: every handler family that reads a task/project id.
		{"GET", "/api/v1/projects/" + shortID},
		{"PUT", "/api/v1/projects/" + badCharID},
		{"GET", "/api/v1/projects/a%2Fb/board"}, // encoded slash must not route across resources
		{"GET", "/api/v1/tasks/" + shortID},
		{"PATCH", "/api/v1/tasks/" + badCharID},
		{"POST", "/api/v1/tasks/" + shortID + "/claim"},
		{"PUT", "/api/v1/tasks/" + shortID + "/notes"},
		{"POST", "/api/v1/tasks/" + shortID + "/comments"},
	}
	for _, tc := range cases {
		code, body := doReq(t, stk.Handler, tc.method, tc.path, nil)
		env := mustDecode[errResp](t, body)
		if code != http.StatusBadRequest || env.Error != "invalid_input" {
			t.Errorf("%s %s = %d %q, want 400 invalid_input", tc.method, tc.path[:min(len(tc.path), 40)], code, env.Message)
		} else if !strings.Contains(env.Message, "malformed id") {
			t.Errorf("%s %s: message %q, want it to name the malformed id", tc.method, tc.path[:min(len(tc.path), 40)], env.Message)
		}
	}
	if code, body := doReq(t, stk.Handler, "PATCH", "/api/v1/tasks/"+wellKnown.ID+"/acceptance-criteria/99", map[string]any{"actor": actor("eve"), "done": true}); code != http.StatusNotFound {
		t.Errorf("PATCH unknown seq = %d (body %s), want 404", code, string(body))
	}
}

func TestIDBoundary_UnknownRelationEndpointsRejectMalformedOtherID(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "relproj")
	t1 := makeTask(t, stk, p.ID, "t1", nil)

	// Body-position other_id (POST relations).
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks/"+t1.ID+"/relations", map[string]any{
		"actor": actor("eve"), "other_id": "zzz", "kind": "related",
	})
	env := mustDecode[errResp](t, body)
	if code != http.StatusBadRequest || env.Error != "invalid_input" {
		t.Errorf("POST relations other_id=zzz = %d %q, want 400 invalid_input", code, env.Message)
	}

	// Path-position other_id (DELETE relations).
	code, body = doReq(t, stk.Handler, "DELETE", "/api/v1/tasks/"+t1.ID+"/relations/short-id/related", nil)
	env = mustDecode[errResp](t, body)
	if code != http.StatusBadRequest || env.Error != "invalid_input" {
		t.Errorf("DELETE relations other_id=short-id = %d %q, want 400 invalid_input", code, env.Message)
	}
}

func TestIDBoundary_OversizedIDResponseStaysBounded(t *testing.T) {
	t.Parallel()
	stk := newStack(t)

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/tasks/" + hostileULID},
		{"GET", "/api/v1/projects/" + hostileULID},
	} {
		code, body := doReq(t, stk.Handler, tc.method, tc.path, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("%s oversized id = %d (body %d bytes), want 400", tc.method, code, len(body))
		}
		if len(body) > 512 {
			t.Errorf("%s oversized-id response = %d bytes, want < 512 (hostile value must not be echoed back)", tc.method, len(body))
		}
		env := mustDecode[errResp](t, body)
		if env.Error != "invalid_input" || !strings.Contains(env.Message, "malformed id") {
			t.Errorf("%s: envelope %+v, want invalid_input naming the malformed id", tc.method, env)
		}
	}
}

func TestIDBoundary_BodyAndFilterIDsRejectedBeforeStorage(t *testing.T) {
	t.Parallel()
	stk := newStack(t)
	p := createProject(t, stk, "filterproj")

	// POST /tasks body project_id.
	code, body := doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "orphan", "project_id": "bad",
	})
	env := mustDecode[errResp](t, body)
	if code != http.StatusBadRequest || env.Error != "invalid_input" {
		t.Errorf("POST /tasks project_id=bad = %d %q, want 400 invalid_input", code, env.Message)
	}

	// POST /tasks blocked_by with a blank entry (boundary rejects before the use case).
	code, body = doReq(t, stk.Handler, "POST", "/api/v1/tasks", map[string]any{
		"actor": actor("eve"), "title": "blocked", "project_id": p.ID, "blocked_by": []string{""},
	})
	env = mustDecode[errResp](t, body)
	if code != http.StatusBadRequest || env.Error != "invalid_input" {
		t.Errorf("POST /tasks blocked_by=[\"\"] = %d %q, want 400 invalid_input", code, env.Message)
	}

	// GET /tasks filter ids.
	for _, q := range []string{"?project_id=not-a-ulid", "?parent_id=T1"} {
		code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks"+q, nil)
		env = mustDecode[errResp](t, body)
		if code != http.StatusBadRequest || env.Error != "invalid_input" {
			t.Errorf("GET /tasks%s = %d %q, want 400 invalid_input", q, code, env.Message)
		}
	}

	// Control: a well-formed but unknown ULID is NOT over-rejected — it 404s.
	code, body = doReq(t, stk.Handler, "GET", "/api/v1/tasks/01A2B3C4D5E6F7G8H9J0K1M2N3", nil)
	if code != http.StatusNotFound {
		t.Errorf("GET unknown well-formed task = %d (body %s), want 404", code, string(body))
	}
}
