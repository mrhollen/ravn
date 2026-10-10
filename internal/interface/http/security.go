package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// SecurityHeadersMiddleware sets standard security headers on all HTTP responses:
// - X-Content-Type-Options: nosniff
// - X-Frame-Options: DENY
// - Referrer-Policy: strict-origin-when-cross-origin
func SecurityHeadersMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			next.ServeHTTP(w, r)
		})
	}
}

// CORSOptions configures Cross-Origin Resource Sharing (CORS) handling.
type CORSOptions struct {
	AllowedOrigins []string // e.g. ["*"] or ["http://localhost:5173", "http://localhost:3000"]
	AllowedMethods []string // e.g. ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]
	AllowedHeaders []string // e.g. ["Authorization", "Content-Type", "X-Actor-Name", "X-Actor-Type"]
	MaxAge         int      // in seconds, e.g. 86400
}

// DefaultCORSOptions returns sensible defaults for CORS handling:
// - AllowedOrigins: ["*"]
// - AllowedMethods: ["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]
// - AllowedHeaders: ["Authorization", "Content-Type", "X-Actor-Name", "X-Actor-Type"]
// - MaxAge: 86400
func DefaultCORSOptions() CORSOptions {
	return CORSOptions{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Actor-Name", "X-Actor-Type"},
		MaxAge:         86400,
	}
}

// ParseCORSOrigins splits a comma-separated string of origins. If empty or "*",
// returns []string{"*"}.
func ParseCORSOrigins(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return []string{"*"}
	}
	parts := strings.Split(s, ",")
	var origins []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			origins = append(origins, p)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

// CORSMiddleware wraps next with CORS handling according to opts.
// OPTIONS preflight requests bypass token authentication and return 204 No Content.
func CORSMiddleware(opts CORSOptions) func(http.Handler) http.Handler {
	if len(opts.AllowedMethods) == 0 {
		opts.AllowedMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	}
	if len(opts.AllowedHeaders) == 0 {
		opts.AllowedHeaders = []string{"Authorization", "Content-Type", "X-Actor-Name", "X-Actor-Type"}
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 86400
	}

	methodsStr := strings.Join(opts.AllowedMethods, ", ")
	headersStr := strings.Join(opts.AllowedHeaders, ", ")
	maxAgeStr := strconv.Itoa(opts.MaxAge)

	wildcard := false
	originsMap := make(map[string]bool, len(opts.AllowedOrigins))
	for _, o := range opts.AllowedOrigins {
		if o == "*" {
			wildcard = true
			break
		}
		originsMap[strings.ToLower(strings.TrimSpace(o))] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			originAllowed := false
			allowOriginHeader := ""

			if origin != "" {
				if wildcard {
					originAllowed = true
					allowOriginHeader = "*"
				} else if originsMap[strings.ToLower(strings.TrimSpace(origin))] {
					originAllowed = true
					allowOriginHeader = origin
					w.Header().Add("Vary", "Origin")
				}
			} else if wildcard {
				// No Origin header sent (e.g. same-origin or non-browser client)
				allowOriginHeader = "*"
			}

			if allowOriginHeader != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowOriginHeader)
			}

			if r.Method == http.MethodOptions {
				if origin == "" || originAllowed {
					w.Header().Set("Access-Control-Allow-Methods", methodsStr)
					w.Header().Set("Access-Control-Allow-Headers", headersStr)
					w.Header().Set("Access-Control-Max-Age", maxAgeStr)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
