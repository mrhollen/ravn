package httpapi

import (
	"fmt"
	"strings"
	"time"
)

// AuthMode selects the request-auth strategy applied by AuthGate (multi-user epic T3).
type AuthMode string

const (
	// AuthModeOff is passthrough: no bearer check, no principal. Declared free-form
	// actors still flow through handlers exactly as they do today.
	AuthModeOff AuthMode = "off"
	// AuthModeToken is the single shared RAVN_TOKEN bearer (the current deployment model).
	// After the legacy middleware passes, an operator act-as principal is injected.
	AuthModeToken AuthMode = "token"
	// AuthModeMultiuser resolves each request's primary bearer as shared-operator → session
	// token → api_token, injecting a credential or operator principal. The only mode where
	// /auth/login is reachable without the shared token.
	AuthModeMultiuser AuthMode = "multiuser"
)

// defaultSessionTTL is the RAVN_SESSION_TTL fallback (also enforced by application.NewAuth).
const defaultSessionTTL = 24 * time.Hour

// ResolveAuthMode maps the RAVN_AUTH value to an AuthMode. An empty value preserves current
// deployments: token when a shared token is configured, else off. Spellings are
// case-insensitive and tolerate common aliases; anything unrecognized is an error so startup
// fails fast on a typo.
func ResolveAuthMode(env string, sharedTokenSet bool) (AuthMode, error) {
	switch v := strings.ToLower(strings.TrimSpace(env)); v {
	case "":
		if sharedTokenSet {
			return AuthModeToken, nil
		}
		return AuthModeOff, nil
	case "off", "none", "disabled":
		return AuthModeOff, nil
	case "token", "shared", "shared_token", "single":
		return AuthModeToken, nil
	case "multiuser", "multi-user", "multi_user", "users":
		return AuthModeMultiuser, nil
	default:
		return "", fmt.Errorf("value %q is not one of off, token, multiuser", env)
	}
}

// ParseSessionTTL parses RAVN_SESSION_TTL as a Go duration (e.g. "24h", "30m"). An empty
// value returns the 24h default; a non-empty invalid or non-positive value is an error so
// startup fails fast rather than silently issuing zero-length sessions.
func ParseSessionTTL(env string) (time.Duration, error) {
	v := strings.TrimSpace(env)
	if v == "" {
		return defaultSessionTTL, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("value %q is not a valid duration (e.g. \"24h\"): %w", env, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("value %q must be a positive duration", env)
	}
	return d, nil
}

// ParseIdentityOpen parses RAVN_IDENTITY_OPEN (multiuser only): auto/true (default), or
// strict/false. T3 validates it at startup; T4's visibility policies consume the value to
// decide whether any authenticated principal may create projects. Kept separate from
// ResolveAuthMode so each knob has one clear purpose.
func ParseIdentityOpen(env string) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(env)); v {
	case "", "auto", "true", "1", "yes", "on":
		return true, nil
	case "strict", "closed", "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("value %q is not one of auto, strict, true, false", env)
	}
}
