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

// TestParseScopingMode covers the T4 RAVN_SCOPING parser: empty/global/auto must be a zero
// observable change from today (DefaultScoping), "membership" enables per-project visibility
// + role gating, and a typo fails fast like ResolveAuthMode.
func TestParseScopingMode(t *testing.T) {
	cases := []struct {
		name           string
		env            string
		wantGlobal     bool
		wantMembership bool
		wantErr        bool
	}{
		{"empty → default (global)", "", true, false, false},
		{"global explicit", "global", true, false, false},
		{"auto alias", "AUTO", true, false, false},
		{"whitespace trimmed", " membership ", false, true, false},
		{"membership → per-project gating", "membership", false, true, false},
		{"bogus value → error", "per-project", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScopingMode(tc.env)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseScopingMode(%q) error = %v, wantErr=%v", tc.env, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got.Global != tc.wantGlobal || got.MembershipScoped != tc.wantMembership {
				t.Fatalf("ParseScopingMode(%q) = %+v, want Global=%v MembershipScoped=%v",
					tc.env, got, tc.wantGlobal, tc.wantMembership)
			}
		})
	}
}

// TestScopingFromEnv covers the composite T4 resolution: a non-nil *ScopingConfig is returned
// ONLY for an explicit "membership" (the gate helpers then engage), while unset/global fall
// back to nil so existing deployments short-circuit with zero behavior change. Parse errors on
// EITHER knob fall back to the SAFE defaults (global / open) rather than booting restricted by
// accident — run() validates both up front, so this is defense in depth for embedded wiring.
func TestScopingFromEnv(t *testing.T) {
	cases := []struct {
		name          string
		scopingEnv    string
		identityEnv   string
		wantNilConfig bool
		wantOpen      bool
	}{
		{"unset → nil config, open", "", "", true, true},
		{"global explicit → nil config", "global", "", true, true},
		{"membership → non-nil config", "membership", "", false, true},
		{"membership + strict → closed", "membership", "strict", false, false},
		{"bogus scoping falls back to global (nil)", "per-project", "", true, true},
		{"bogus identity falls back to open", "membership", "maybe", false, true},
		{"both bogus → safe defaults", "wat", "wat", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, open := ScopingFromEnv(tc.scopingEnv, tc.identityEnv)
			if (cfg == nil) != tc.wantNilConfig {
				t.Fatalf("ScopingFromEnv(%q,%q) config = %v, wantNil=%v", tc.scopingEnv, tc.identityEnv, cfg, tc.wantNilConfig)
			}
			if !tc.wantNilConfig && !cfg.MembershipScoped {
				t.Fatalf("ScopingFromEnv(%q,%q) returned a non-membership config %+v", tc.scopingEnv, tc.identityEnv, *cfg)
			}
			if open != tc.wantOpen {
				t.Fatalf("ScopingFromEnv(%q,%q) identityOpen = %v, want %v", tc.scopingEnv, tc.identityEnv, open, tc.wantOpen)
			}
		})
	}
}
