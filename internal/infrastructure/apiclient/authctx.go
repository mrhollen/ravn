package apiclient

import (
	"context"
	"fmt"
	"strings"
)

// authCtxKey is the context key for a per-request bearer-token override. A
// private type makes it uncollideable with other packages' value keys.
type authCtxKey struct{}

// WithAuthToken returns ctx carrying token as the bearer token to forward on
// every request made under that context, in place of the Client's
// construction-time static token (see New). An empty or whitespace-only token
// is a no-op, so callers can stamp a possibly-absent credential without
// clearing the static fallback.
//
// Per-request overrides exist so ONE long-lived client — e.g. the ravn-mcp
// adapter serving many distinct agents through one process — can forward each
// incoming request's own per-agent API token, while calls on plain contexts
// (context.Background) keep forwarding the shared deployment token exactly as
// before: nothing that does not stamp an override changes behavior.
func WithAuthToken(ctx context.Context, token string) context.Context {
	if strings.TrimSpace(token) == "" {
		return ctx
	}
	return context.WithValue(ctx, authCtxKey{}, token)
}

// AuthTokenFromContext reports the per-request bearer-token override stamped by
// WithAuthToken on ctx, if any.
func AuthTokenFromContext(ctx context.Context) (string, bool) {
	tok, ok := ctx.Value(authCtxKey{}).(string)
	return tok, ok
}

// ParseIdentityOpen resolves RAVN_IDENTITY_OPEN into the identity-open flag:
// absent/auto/true-family → open (true); strict/false-family → closed (false).
// ravn-server and the ravn-mcp adapter share this one parser so their notion
// of "strict" cannot drift; httpapi.ParseIdentityOpen delegates here and its
// behavior + error text are unchanged since T3.
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
