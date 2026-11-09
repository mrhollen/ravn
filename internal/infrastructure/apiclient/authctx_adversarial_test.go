package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// authctx_adversarial_test.go: hostile token values must never panic, misroute
// or enable header injection. The safety boundary is the net/http transport,
// which REFUSES to send a request whose Authorization value carries CR/LF/NUL
// ("invalid header field value"); such values surface as a transport error —
// NOT an *APIError — so no forged/broken request ever leaves the process.
// Every other hostile value is forwarded byte-for-byte, letting the upstream
// ravn-server (the only authority on credential validity) reject it with its
// standard 401 body. The ONLY transformation this layer applies is the no-op
// rule: empty/whitespace-only values stamp nothing.

func TestAuthToken_HostileValuesForwardedVerbatim(t *testing.T) {
	hosts := []struct {
		name string
		tok  string
	}{
		{"internal tab (legal header byte)", "a\tb"},
		{"unicode identity", "üñïçødé-агент"},
		{"very long token (64 KiB)", strings.Repeat("x", 64*1024)},
		{"json-looking token", `{"Authorization":"Bearer forged"}`},
		{"quote and backslash soup", `"a\\b\"c"`},
	}
	for _, tt := range hosts {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			c, _ := newTestServer(t, "static", capturing(t, func(cap *capture, w http.ResponseWriter) {
				got = cap.header.Get("Authorization")
				writeJSONTest(w, http.StatusOK, projectsBody)
			}))
			if _, err := c.ListProjects(WithAuthToken(context.Background(), tt.tok)); err != nil {
				t.Fatalf("ListProjects: %v", err)
			}
			want := "Bearer " + tt.tok
			if got != want {
				t.Errorf("Authorization = %q, want verbatim %q", got, want)
			}
		})
	}
}

// TestAuthToken_ControlBytesRefusedByTransport pins the header-injection
// boundary: CR, LF and NUL in a token make net/http refuse the request itself.
// The caller sees a transport error (not an *APIError); nothing is sent.
func TestAuthToken_ControlBytesRefusedByTransport(t *testing.T) {
	for i, v := range []string{"a\nb", "a\rb", "\x00", "forged\r\nX-Injected: yes"} {
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) {
			var got string
			c, _ := newTestServer(t, "static", capturing(t, func(cap *capture, w http.ResponseWriter) {
				got = cap.header.Get("Authorization")
				writeJSONTest(w, http.StatusOK, projectsBody)
			}))
			ctx := WithAuthToken(context.Background(), v)
			_, err := c.ListProjects(ctx)
			if err == nil {
				t.Fatalf("expected the transport to refuse a %q-bearing header, but the call succeeded", v)
			}
			if e, ok := AsAPIError(err); ok {
				t.Fatalf("control-byte refusal must be a transport error, got *APIError: %+v", e)
			}
			if !strings.Contains(err.Error(), "invalid header field value") {
				t.Errorf("error %q does not name the stdlib header validation", err)
			}
			if got != "" {
				t.Errorf("request must never leave the process; upstream saw Authorization %q", got)
			}
		})
	}
}

// TestAuthToken_NoOpRuleBlankVariants: hostile-looking-but-blank values must hit
// the no-op path (not stamped), so a client that "clears" its credential with a
// blank string falls back to the static token instead of sending Bearer <blank>.
func TestAuthToken_NoOpRuleBlankVariants(t *testing.T) {
	// A bare NUL byte is NOT Unicode whitespace, so TrimSpace leaves it and it
	// takes the (refused-by-transport) hostile path above — only true blanks no-op.
	for i, v := range []string{"", " ", "\t", "\n", "  \t\n  "} {
		ctx := WithAuthToken(context.Background(), v)
		if _, ok := AuthTokenFromContext(ctx); ok {
			t.Errorf("case %d (%q): blank variant must NOT stamp a token", i, v)
		}
	}
}

func TestParseIdentityOpen_Unit(t *testing.T) {
	tests := []struct {
		in       string
		wantOpen bool
		wantErr  bool
	}{
		{in: "", wantOpen: true},
		{in: "auto", wantOpen: true},
		{in: "AUTO", wantOpen: true},
		{in: "true", wantOpen: true},
		{in: "1", wantOpen: true},
		{in: "yes", wantOpen: true},
		{in: "on", wantOpen: true},
		{in: "  strict  ", wantOpen: false},
		{in: "strict", wantOpen: false},
		{in: "closed", wantOpen: false},
		{in: "false", wantOpen: false},
		{in: "0", wantOpen: false},
		{in: "no", wantOpen: false},
		{in: "off", wantOpen: false},
		{in: "bogus", wantErr: true},
		{in: "STRICT2", wantErr: true},
	}
	for _, tt := range tests {
		t.Run("["+tt.in+"]", func(t *testing.T) {
			got, err := ParseIdentityOpen(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseIdentityOpen(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantOpen {
				t.Errorf("ParseIdentityOpen(%q) = %v, want %v", tt.in, got, tt.wantOpen)
			}
			if tt.wantErr && !strings.Contains(err.Error(), fmt.Sprintf("%q", tt.in)) {
				t.Errorf("error %q does not name the offending value", err)
			}
		})
	}
}
