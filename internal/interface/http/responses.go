package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// rfc3339 renders a timestamp in the wire format crowban uses everywhere
// (ARCHITECTURE §4): RFC3339 UTC, e.g. "2026-09-25T18:00:00Z".
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// writeJSON serializes v as a JSON response with the given status. A marshal
// failure is a bug (our DTOs cannot fail to marshal); it degrades to a 500.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"internal","message":"failed to encode response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(body) //nolint:errcheck // ResponseWriter write after header is best-effort by design
}

// writeJSONError writes one of the JSON error envelopes (errors.go documents
// the domain-error → status mapping). Map payloads keep the exact field names
// explicit at each call site; encoding/json marshals map keys in sorted order,
// so bodies are byte-stable for identical conditions.
func writeJSONError(w http.ResponseWriter, code int, payload map[string]any) {
	writeJSON(w, code, payload)
}
