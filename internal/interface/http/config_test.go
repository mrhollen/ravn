package httpapi

import (
	"testing"
	"time"
)

// auth_config_test.go covers the T3 env→config pure functions in isolation: the three
// knobs (auth mode, session TTL, identity-open) must parse predictably and fail fast on a
// typo so a misconfigured deployment never boots into an inconsistent gate.

func TestResolveAuthMode(t *testing.T) {
	cases := []struct {
		name           string
		env            string
		sharedTokenSet bool
		want           AuthMode
		wantErr        bool
	}{
		{"empty, no token → off", "", false, AuthModeOff, false},
		{"empty, token set → token (current default)", "", true, AuthModeToken, false},
		{"off", "off", true, AuthModeOff, false},
		{"none alias", "none", true, AuthModeOff, false},
		{"disabled alias", "disabled", true, AuthModeOff, false},
		{"token explicit", "token", false, AuthModeToken, false},
		{"shared_token alias", "shared_token", true, AuthModeToken, false},
		{"single alias", "SINGLE", true, AuthModeToken, false},
		{"multiuser", "multiuser", true, AuthModeMultiuser, false},
		{"multi-user alias", "multi-user", true, AuthModeMultiuser, false},
		{"multi_user alias", "MULTI_USER", true, AuthModeMultiuser, false},
		{"whitespace trimmed", "  multiuser  ", true, AuthModeMultiuser, false},
		{"bogus value → error", "passwords-only", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAuthMode(tc.env, tc.sharedTokenSet)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ResolveAuthMode(%q, %v) error = %v, wantErr=%v", tc.env, tc.sharedTokenSet, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ResolveAuthMode(%q, %v) = %q, want %q", tc.env, tc.sharedTokenSet, got, tc.want)
			}
		})
	}
}

func TestParseSessionTTL(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"empty → 24h default", "", 24 * time.Hour, false},
		{"explicit hours", "12h", 12 * time.Hour, false},
		{"minutes", "90m", 90 * time.Minute, false},
		{"whitespace trimmed", " 5s ", 5 * time.Second, false},
		{"invalid duration → error", "banana", 0, true},
		{"zero → error (must be positive)", "0s", 0, true},
		{"negative → error", "-3h", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionTTL(tc.env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseSessionTTL(%q) error = %v, wantErr=%v", tc.env, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ParseSessionTTL(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestParseIdentityOpen(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    bool
		wantErr bool
	}{
		{"empty → auto (open)", "", true, false},
		{"auto", "auto", true, false},
		{"true", "true", true, false},
		{"1 alias", "1", true, false},
		{"on alias", "ON", true, false},
		{"strict → closed", "strict", false, false},
		{"false", "false", false, false},
		{"0 alias", "0", false, false},
		{"off alias", "closed", false, false},
		{"bogus value → error", "maybe", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseIdentityOpen(tc.env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseIdentityOpen(%q) error = %v, wantErr=%v", tc.env, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ParseIdentityOpen(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}
