package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// rfc3339 renders a timestamp in the wire format ravn uses everywhere
// (ARCHITECTURE §4): RFC3339 UTC, e.g. "2026-09-25T18:00:00Z".
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// marshalFailureBody is the byte-stable fallback envelope for a JSON marshal
// failure. It MUST stay valid JSON with both "error" and "message" fields —
// every /api/v1 response body is JSON, including this one (01M3MMXQH5M82C7BETZY9QMG6P).
var marshalFailureBody = []byte(`{"error":"internal","message":"failed to encode response"}`)

// writeJSON serializes v as a JSON response with the given status. A marshal
// failure is a bug (our DTOs cannot fail to marshal); it degrades to a 500 via
// writeMarshalFailure — raw JSON bytes, because a failed encode must not
// re-enter the encoder, and http.Error would break the contract by answering
// text/plain with a trailing newline.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeMarshalFailure(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(body) //nolint:errcheck // ResponseWriter write after header is best-effort by design
}

// writeMarshalFailure writes the 500 fallback for a marshal failure. Raw bytes
// (no encoder round-trip), Content-Type application/json, no trailing newline:
// the contract that every /api/v1 response body is JSON holds even here.
func writeMarshalFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write(marshalFailureBody) //nolint:errcheck // ResponseWriter write after header is best-effort by design
}

// writeJSONError writes one of the JSON error envelopes (errors.go documents
// the domain-error → status mapping). Map payloads keep the exact field names
// explicit at each call site; encoding/json marshals map keys in sorted order,
// so bodies are byte-stable for identical conditions.
func writeJSONError(w http.ResponseWriter, code int, payload map[string]any) {
	writeJSON(w, code, payload)
}
