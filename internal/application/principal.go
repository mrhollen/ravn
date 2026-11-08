package application

import (
	"context"

	"github.com/mrhollen/ravn/internal/domain"
)

// Principal is the authenticated principal for a request, stamped into the request
// context by the HTTP auth gate (multi-user epic T3). It records WHO is acting and HOW
// they were authenticated so the authorization policies added in T4 can reason about
// the request. This is presentation/adapter plumbing only: NO visibility or role verdicts
// are made here — T3 establishes facts, T4 turns them into allow/deny.
type Principal struct {
	// Subject is the resolved durable identity for credential-based principals
	// (session / api_token). For operator (shared-token) principals it may be the zero
	// (unknown) subject; T4 resolves ownership/membership from Actor.Name when needed.
	Subject domain.Subject
	// Identity is the full durable identity row for credential-based principals; zero
	// for operators (an operator acts AS a name, not from their own stored row).
	Identity domain.Identity
	// Actor is the authoritative acting actor this request should be attributed to. For
	// a logged-in identity it is that identity's own kind/name (spoof prevention — a
	// credential principal cannot be re-attributed to another name). For token-mode
	// operators it is ZERO so legacy declared-actor attribution is preserved.
	Actor domain.Actor

	AuthMethod    domain.AuthMethod // how the request was authenticated
	IsOperator    bool              // true for the shared RAVN_TOKEN (privileged)
	Authenticated bool              // always true when a Principal is present
}

type principalKey struct{}

// WithPrincipal returns a context carrying p. The gate installs exactly one principal
// per request; setting more than one is a bug (the last wins).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext recovers the request's principal. ok is false when none was set —
// i.e. auth mode is off, or the request took a non-gated path — so callers fall back to
// today's declared-actor behavior.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// AuthoritativeActor returns the credential-derived Actor when one was established for
// this request (a logged-in identity's own name, or an operator's declared act-as
// target), and otherwise falls back to the caller-supplied declared actor. In token mode
// the principal carries a zero Actor, so this always yields fallback — today's exact
// attribution behavior. T4 handlers call this with their extractActor result as fallback.
func (p Principal) AuthoritativeActor(fallback domain.Actor) domain.Actor {
	if p.Actor.Name != "" {
		return p.Actor
	}
	return fallback
}
