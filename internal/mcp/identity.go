package mcp

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// identity.go (T6): per-request credential tracking for the HTTP transports.
// One ravn-mcp process can serve MANY distinct agents: each incoming MCP
// request may carry its own per-agent API token in Authorization (or the
// ?token= query fallback the auth gate accepts). These helpers stamp that
// credential into the request context so that (a) the tool's upstream REST
// calls forward EXACTLY this token — which is what lets a per-agent token
// authenticate to a named identity on a RAVN_AUTH=multiuser ravn-server, and
// (b) identity-aware actor resolution can tell "this request has a real named
// credential" from "this is the legacy shared-token/anonymous flow".

// Credential identifies the HTTP credential one incoming MCP request carried.
type Credential struct {
	// Token is the bearer token as received (never logged; kept for callers
	// that need to re-forward or audit it).
	Token string
	// Shared is true when Token equals the adapter's gate token (RAVN_TOKEN):
	// the legacy deployment credential, under which identity stays name-keyed
	// and free-form actors flow through as before.
	Shared bool
}

type credKey struct{}

// WithCredential stamps c onto ctx for the MCP handlers of this request.
func WithCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, credKey{}, c)
}

// CredentialFromContext reports the credential stamped by WithCredential on ctx.
func CredentialFromContext(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(credKey{}).(Credential)
	return c, ok
}

// bearerToken extracts the request's bearer credential the same way ravn-mcp's
// auth gate does: the Authorization header first, then the ?token= query
// fallback (streaming clients — GET /sse and its /message session — cannot
// always set headers). Empty when neither is present.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if parts := strings.SplitN(strings.TrimSpace(auth), " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		if tok := strings.TrimSpace(parts[1]); tok != "" {
			return tok
		}
	}
	return r.URL.Query().Get("token")
}

// credentialContextFunc stamps one incoming request's credential into the MCP
// handler context: as the per-request upstream authorization override (apiclient.
// WithAuthToken — tools forward exactly this token to ravn-server) and as an
// mcp.Credential marker for identity-aware actor resolution. sharedToken is
// the adapter's gate token; a request credential equal to it is marked Shared.
// A request without any credential changes the context untouched, so legacy
// shared-token/anonymous traffic keeps today's byte-for-byte behavior: upstream
// keeps using the client's static construction-time token.
func credentialContextFunc(sharedToken string) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		tok := bearerToken(r)
		if tok == "" {
			return ctx // nothing to stamp — static-token flow unchanged
		}
		shared := sharedToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(sharedToken)) == 1
		ctx = apiclient.WithAuthToken(ctx, tok)
		return WithCredential(ctx, Credential{Token: tok, Shared: shared})
	}
}

// StreamableCredentialOption is the context hook for the streamable-HTTP
// transport (/mcp): it makes every request's own bearer credential available to
// tools via context (see credentialContextFunc). Wire it alongside the other
// NewStreamableHTTPServer options.
func StreamableCredentialOption(sharedToken string) mcpsrv.StreamableHTTPOption {
	return mcpsrv.WithHTTPContextFunc(credentialContextFunc(sharedToken))
}

// SSECredentialOption is the SSE-transport twin (/sse + /message): every
// session message's request credential is stamped the same way. Wire it into
// NewSSEServer. Note WithAppendQueryToMessageEndpoint (used by ravn-mcp)
// carries a ?token= opened on /sse onto each /message POST, so the query
// fallback in bearerToken keeps per-agent tokens working over legacy SSE too.
func SSECredentialOption(sharedToken string) mcpsrv.SSEOption {
	return mcpsrv.WithSSEContextFunc(credentialContextFunc(sharedToken))
}
