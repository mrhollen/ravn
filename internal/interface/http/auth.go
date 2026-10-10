package httpapi

import (
	"crypto/subtle"
	"net/http"
	"path"
	"strings"
)

// AuthMiddleware returns an HTTP middleware that gates requests when expectedToken
// is non-empty. When expectedToken is empty (unset), auth is disabled for zero-config
// local dev mode, and all requests pass through untouched.
//
// When enabled, all requests under the API prefix (/api and /api/*) require
// an Authorization header with a Bearer token matching expectedToken:
//
//	Authorization: Bearer <token>
//
// Missing or incorrect tokens are rejected with HTTP 401 Unauthorized and JSON body:
//
//	{"error":"unauthorized","message":"missing or invalid auth token"}
//
// Non-API routes (such as /healthz and static UI assets) are never gated, regardless
// of whether an auth token is set or whether an Authorization header is provided.
func AuthMiddleware(expectedToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if expectedToken == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAPIRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if !validBearerToken(auth, expectedToken) {
				// Query parameter authentication (?token=) is permitted ONLY on GET SSE event stream routes
				// (/api/v1/events and /api/v1/projects/{id}/events) due to browser EventSource limitations.
				// All mutating routes and standard GET endpoints mandate Authorization: Bearer <token>.
				if isSSEEventRoute(r) {
					qToken := r.URL.Query().Get("token")
					if qToken != "" && subtle.ConstantTimeCompare([]byte(qToken), []byte(expectedToken)) == 1 {
						next.ServeHTTP(w, r)
						return
					}
				}
				writeJSONError(w, http.StatusUnauthorized, map[string]any{
					"error":   "unauthorized",
					"message": "missing or invalid auth token",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// WithAuth is a convenience wrapper that applies AuthMiddleware directly to handler.
func WithAuth(expectedToken string, handler http.Handler) http.Handler {
	return AuthMiddleware(expectedToken)(handler)
}

func isAPIRoute(p string) bool {
	cleaned := path.Clean(p)
	return cleaned == "/api" || strings.HasPrefix(cleaned, "/api/")
}

func isSSEEventRoute(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := path.Clean(r.URL.Path)
	if p == "/api/v1/events" || p == "/api/events" {
		return true
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if (len(parts) == 4 || len(parts) == 5) && parts[len(parts)-1] == "events" && parts[len(parts)-3] == "projects" {
		return true
	}
	return false
}

func validBearerToken(header, expected string) bool {
	if header == "" || expected == "" {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
