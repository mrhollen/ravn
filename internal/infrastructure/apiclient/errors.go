package apiclient

import (
	"errors"
	"fmt"
	"strings"
)

// APIError is a non-2xx response from ravn-server. It PRESERVES the status
// code and the full upstream response body verbatim, because the body's
// "message" field doubles as the agent-readable recovery instruction (API.md
// "Error contract") — the MCP layer surfaces these bodies untouched so agents
// learn how to recover (who holds a conflicting lease and until when, which id
// to re-check, etc.).
type APIError struct {
	Op         string // client method that failed, e.g. "ClaimTask"
	StatusCode int    // upstream HTTP status (400/404/409/500/…)
	URL        string // the request URL that produced the error
	Body       []byte // full upstream response body, byte-for-byte
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(string(e.Body))
	if len(body) > 256 {
		body = body[:256] + "…"
	}
	return fmt.Sprintf("%s: ravn-server answered HTTP %d for %s: %s", e.Op, e.StatusCode, e.URL, body)
}

// AsAPIError extracts the *APIError from err if one is present in its chain.
// Transport failures (server down, timeout) are NOT APIErrors and come back
// false — callers render those as "unreachable" messages instead.
func AsAPIError(err error) (*APIError, bool) {
	var e *APIError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
