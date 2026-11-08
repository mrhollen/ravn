package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// AuthGate is the mode-aware request-auth middleware that replaces the bare legacy
// AuthMiddleware at the router root (multi-user epic T3). It preserves every current
// deployment's behavior exactly:
//
//   - off       → passthrough, no principal (declared free-form actors flow through as today).
//   - token     → legacy shared RAVN_TOKEN bearer check, then an operator act-as principal is
//     injected on API routes once it lets the request through. No DB reads.
//   - multiuser → non-API/OPTIONS bypass, public /auth/login bypass, then the request's primary
//     bearer resolves as shared-operator → session token → api_token, injecting a credential or
//     operator principal. Unresolvable bearer → 401.
func AuthGate(mode AuthMode, sharedToken string, auth *application.Auth) func(http.Handler) http.Handler {
	switch mode {
	case AuthModeOff:
		return func(next http.Handler) http.Handler { return next }
	case AuthModeToken:
		return operatorGate(sharedToken)
	default: // AuthModeMultiuser and any unexpected value fail safe to the strictest mode.
		return multiuserGate(sharedToken, auth)
	}
}

// operatorGate is token mode: the legacy shared-token middleware unchanged, with an operator
// act-as principal injected on API routes once it lets a request through. Non-API requests
// (static assets etc.) pass through with no principal. The inner handler wraps the real next,
// so principal injection only happens for requests the legacy gate actually admitted.
func operatorGate(sharedToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return AuthMiddleware(sharedToken)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAPIRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			p := operatorPrincipalFromRequest(r)
			next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), p)))
		}))
	}
}

// multiuserGate resolves the request's primary bearer in strict order and injects the
// matching principal. A missing/unknown bearer → 401; a genuine store I/O error → 500 via
// writeAppError (distinguished from the expected ErrAuthNoSession fall-through).
func multiuserGate(sharedToken string, auth *application.Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAPIRoute(r.URL.Path) {
				next.ServeHTTP(w, r) // static / non-API passthrough
				return
			}
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r) // preflight bypasses auth
				return
			}
			if isPublicAuthRoute(r) {
				next.ServeHTTP(w, r) // POST /auth/login is public in multiuser mode
				return
			}

			tok := primaryBearer(r)
			unauthorized := func() {
				writeJSONError(w, http.StatusUnauthorized, map[string]any{
					"error":   ErrCodeUnauthorized,
					"message": "present a valid session token or api_token secret as the Bearer token",
				})
			}
			if tok == "" {
				unauthorized()
				return
			}

			// 1) shared RAVN_TOKEN — operator override, constant-time compare.
			if sharedToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(sharedToken)) == 1 {
				p := operatorPrincipalFromRequest(r)
				next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), p)))
				return
			}

			if auth == nil {
				writeJSONError(w, http.StatusUnauthorized, map[string]any{
					"error":   ErrCodeUnauthorized,
					"message": "authentication is not enabled",
				})
				return
			}

			// 2) session token (password login).
			if id, err := auth.SessionPrincipal(r.Context(), tok); err == nil {
				p := credentialPrincipal(id, domain.AuthPassword)
				next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), p)))
				return
			} else if !errors.Is(err, application.ErrAuthNoSession) {
				writeAppError(w, err, "auth", "") // genuine I/O error → 500
				return
			}

			// 3) api_token (per-agent credential).
			if id, err := auth.APIPrincipal(r.Context(), tok); err == nil {
				p := credentialPrincipal(id, domain.AuthAPI)
				next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), p)))
				return
			} else if !errors.Is(err, application.ErrAuthNoSession) {
				writeAppError(w, err, "auth", "") // genuine I/O error → 500
				return
			}

			unauthorized()
		})
	}
}

// isPublicAuthRoute reports the ONLY multiuser route reachable without a credential: the
// password-login exchange. Every other /auth/* route still requires an operator or
// credential principal (enforced by the gate and, for provisioning, the handlers).
func isPublicAuthRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch path.Clean(r.URL.Path) {
	case "/api/v1/auth/login", "/api/auth/login":
		return true
	default:
		return false
	}
}

// primaryBearer extracts the single bearer secret a multiuser request presents: the
// Authorization header's Bearer value, else (SSE EventSource routes only) the ?token= query
// param — mirroring the legacy SSE allowance. It does NOT validate; callers resolve it.
func primaryBearer(r *http.Request) string {
	if t := extractBearer(r.Header.Get("Authorization")); t != "" {
		return t
	}
	if isSSEEventRoute(r) {
		return r.URL.Query().Get("token")
	}
	return ""
}

// extractBearer returns the token part of an "Authorization: Bearer <tok>" header ("" when
// absent or malformed). Deliberately separate from validBearerToken, which compares to a
// fixed expected secret — here the caller resolves whatever was presented.
func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// declaredActor mirrors helpers.extractActor's header/query precedence but returns plain
// strings so the principal builder does not couple to the actorInput struct type.
func declaredActor(r *http.Request) (name, typ string) {
	if n := strings.TrimSpace(r.Header.Get("X-Actor-Name")); n != "" {
		return n, strings.TrimSpace(r.Header.Get("X-Actor-Type"))
	}
	if n := strings.TrimSpace(r.URL.Query().Get("actor_name")); n != "" {
		return n, strings.TrimSpace(r.URL.Query().Get("actor_type"))
	}
	if n := strings.TrimSpace(r.URL.Query().Get("actor")); n != "" {
		return n, strings.TrimSpace(r.URL.Query().Get("actor_type"))
	}
	return "", ""
}

// operatorPrincipalFromRequest builds the token-mode / shared-operator principal: privileged
// (IsOperator), authenticated via the shared token, with an authoritative Actor drawn from
// the declared X-Actor-* header (or default "operator"/human) and a zero Subject — T4 may
// resolve the subject later for authorization.
func operatorPrincipalFromRequest(r *http.Request) application.Principal {
	p := application.Principal{IsOperator: true, Authenticated: true, AuthMethod: domain.AuthSharedToken}
	if name, typ := declaredActor(r); name != "" {
		k := domain.ActorType(typ)
		if !k.Valid() {
			k = domain.ActorHuman
		}
		p.Actor = domain.Actor{Type: k, Name: name}
	} else {
		p.Actor = domain.Actor{Type: domain.ActorHuman, Name: "operator"}
	}
	return p
}

// credentialPrincipal builds the multiuser principal for a resolved identity: authoritative
// Actor and Subject from the durable identity, AuthMethod set to what was presented (password
// session vs api_token), never operator.
func credentialPrincipal(id domain.Identity, method domain.AuthMethod) application.Principal {
	k := id.Kind
	if !k.Valid() {
		k = domain.ActorHuman
	}
	return application.Principal{
		Subject:       domain.SubjectFromIdentity(id),
		Identity:      id,
		Actor:         domain.Actor{Type: k, Name: id.Name},
		AuthMethod:    method,
		Authenticated: true,
	}
}
