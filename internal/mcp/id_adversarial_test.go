package mcp

// id_adversarial_test.go: hostile ID input at the MCP boundary. Every malformed
// identifier argument must be rejected locally by domain.ParseID — isError with
// "<tool>: ... malformed id ..." — WITHOUT a single round-trip to upstream, so
// a confused or hostile LLM cannot use the tool surface as an oracle against
// the ravn-server, and oversized values cannot balloon the response. A
// well-formed-but-unknown ULID must NOT be over-rejected: it round-trips and
// the upstream 404 is forwarded verbatim.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// adversarialUnknownULID is well-formed but unknown to the mock upstream — the
// control that proves validation rejects format, not existence.
const adversarialUnknownULID = "01A2B3C4D5E6F7G8H9J0K1M2N3"

func TestToolIDArgumentsRejectedLocallyWithoutUpstreamRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	upstream, _, calls := newMockUpstream(t)
	c := dial(t, startMCPStack(t, upstream.URL).URL, ctx)
	defer func() { _ = c.Close() }()

	before := len(*calls)
	cases := []struct {
		tool string
		args map[string]any
	}{
		// Required path/body ids in every handler family.
		{"get_task", map[string]any{"id": "T1"}},
		{"update_task", map[string]any{"id": "nope", "version": 4, "title": "x"}},
		{"claim_task", map[string]any{"id": "short"}},
		{"release_task", map[string]any{"id": "0734567890abcdefghij00000000"}}, // 26 chars, 'i' is not Crockford
		// Oversized id: the error detail must stay bounded.
		{"add_comment", map[string]any{"id": strings.Repeat("B", 1<<20), "kind": "note", "body": "x"}},
		// Filter / create ids.
		{"list_tasks", map[string]any{"project_id": "not-a-ulid"}},
		{"create_task", map[string]any{"title": "x", "project_id": "p"}},
		// The second id in a relation: malformed other_id is rejected too.
		{"relate_tasks", map[string]any{"id": adversarialUnknownULID, "other_id": "zzz", "kind": "related"}},
	}
	for _, tc := range cases {
		res := callTool(t, c, ctx, tc.tool, tc.args)
		if !res.IsError {
			t.Errorf("%s: not rejected locally: %+v", tc.tool, res)
			continue
		}
		text := firstText(t, res)
		if !strings.Contains(text, `"error":"invalid_input"`) ||
			!strings.Contains(text, tc.tool+": invalid input") || !strings.Contains(text, "malformed id") {
			t.Errorf("%s: text = %q, want local invalid_input rejection naming the tool and the malformed id", tc.tool, text)
		}
		if len(text) > 512 {
			t.Errorf("%s: error body %d bytes, want < 512 (hostile value must not be echoed back)", tc.tool, len(text))
		}
	}
	if got := len(*calls); got != before {
		t.Errorf("upstream saw %d new calls after malformed args, want 0 (local rejection only)", got-before)
	}

	// Control: a well-formed but unknown ULID is forwarded and the upstream 404
	// comes back verbatim — validation must not reject format-valid ids.
	res := callTool(t, c, ctx, "get_task", map[string]any{"id": adversarialUnknownULID})
	text := firstText(t, res)
	if !strings.Contains(text, "unknown task") {
		t.Errorf("get_task(unknown ULID) = %q, want the upstream 404 forwarded verbatim", text)
	}
	if got := len(*calls); got != before+1 {
		t.Errorf("upstream saw %d calls for the well-formed unknown id, want exactly 1 round-trip", got-before)
	}
}
