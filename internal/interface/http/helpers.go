package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// decodeJSON decodes the request body into v. Non-empty bodies must have
// Content-Type: application/json (or media type application/json with parameters),
// returning 415 ({"error":"unsupported media type"}) if mismatched.
// Malformed JSON or a missing body is a 400 ({"error":"invalid input"})
// and the handler returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	hasBody := false
	if r.Body != nil && r.Body != http.NoBody {
		if r.ContentLength > 0 {
			hasBody = true
		} else {
			var buf [1]byte
			n, _ := r.Body.Read(buf[:])
			if n > 0 {
				hasBody = true
				r.Body = struct {
					io.Reader
					io.Closer
				}{
					Reader: io.MultiReader(bytes.NewReader(buf[:n]), r.Body),
					Closer: r.Body,
				}
			}
		}
	}

	if !hasBody {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "malformed JSON body: unexpected EOF",
		})
		return false
	}

	ct := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, map[string]any{
			"error":   "unsupported media type",
			"message": "Content-Type must be application/json",
		})
		return false
	}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "malformed JSON body: " + err.Error(),
		})
		return false
	}
	return true
}

// pathID extracts and validates the {id} path parameter. ULIDs are plain
// strings on the wire; a missing id is malformed input (400), while any other
// value is passed through to the use case, which answers 404 when unknown.
func pathID(w http.ResponseWriter, r *http.Request) (domain.ID, bool) {
	id := domain.ID(r.PathValue("id"))
	if id.IsZero() {
		writeJSONError(w, http.StatusBadRequest, map[string]any{
			"error":   "invalid input",
			"message": "missing path id",
		})
		return "", false
	}
	return id, true
}

// --- list_tasks query parameters --------------------------------------------------
//
// Pagination: OFFSET + LIMIT (the shape TaskFilter already carries; no
// page/page-per-page indirection). Defaults and caps match the use-case
// constants — limit defaults to 20 and is clamped to 100, offset defaults to 0.
// The response envelope echoes the effective limit/offset plus `total` (row
// count before paging), so clients can compute how many pages remain.
//
// Filters (all optional; unknown params are ignored):
//
//	project_id    ULID — restrict to one project
//	status        todo|doing|done (other values → 400)
//	assignee      durable assignee NAME (alias of assignee_name)
//	assignee_name durable assignee name
//	assignee_type human|agent (other values → 400)
//	archived      true lists archived tasks; default false = open only
//	unblocked     true keeps only tasks with no open blocker
//	search        case-insensitive substring over title+description+notes (alias: q)
//	priority      exact match, 0–5 (out of range → 400)
//	due_before    RFC3339 — due_at strictly before this instant (undated never matches)
//	due_after     RFC3339 — due_at at or after this instant (undated never matches)
//	created_after / created_before  RFC3339 window over creation time
//	completed_since (alias of completed_after) / completed_before  RFC3339 window
//	limit         default 20, max 100; offset defaults to 0. limit <= 0 → 400.

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func parseBoolFlag(q interface{ Get(key string) string }, name string) bool {
	switch q.Get(name) {
	case "true", "1":
		return true
	default:
		return false
	}
}

// parseTaskFilter translates GET /tasks query parameters into a TaskFilter.
// Any malformed value yields domain.ErrInvalid (→ 400 via writeAppError).
func parseTaskFilter(q urlValues) (application.TaskFilter, error) {
	f := application.TaskFilter{}

	if v := q.Get("project_id"); v != "" {
		f.ProjectID = domain.ID(v)
	}
	if v := q.Get("status"); v != "" {
		st := domain.Status(v)
		if !st.IsCanonical() {
			return f, fmt.Errorf("%w: unknown status %q (expected todo, doing or done)", domain.ErrInvalid, v)
		}
		f.Status = st
	}

	name := q.Get("assignee_name")
	if name == "" {
		name = q.Get("assignee") // documented alias
	}
	f.AssigneeName = name
	if v := q.Get("assignee_type"); v != "" {
		at := domain.ActorType(v)
		if !at.Valid() {
			return f, fmt.Errorf("%w: assignee_type must be \"human\" or \"agent\", got %q", domain.ErrInvalid, v)
		}
		f.AssigneeType = at
	}

	f.Archived = parseBoolFlag(q, "archived")
	f.UnblockedOnly = parseBoolFlag(q, "unblocked")

	f.Q = q.Get("search")
	if f.Q == "" {
		f.Q = q.Get("q") // documented alias (ARCHITECTURE §5 spelling)
	}

	if v := q.Get("priority"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 0 || p > 5 {
			return f, fmt.Errorf("%w: priority must be an integer between 0 and 5, got %q", domain.ErrInvalid, v)
		}
		pr := domain.Priority(p)
		f.Priority = &pr
	}

	var err error
	if f.DueBefore, err = parseTimeParam(q.Get("due_before"), "due_before"); err != nil {
		return f, err
	}
	if f.DueAfter, err = parseTimeParam(q.Get("due_after"), "due_after"); err != nil {
		return f, err
	}
	if f.CreatedAfter, err = parseTimeParam(q.Get("created_after"), "created_after"); err != nil {
		return f, err
	}
	if f.CreatedBefore, err = parseTimeParam(q.Get("created_before"), "created_before"); err != nil {
		return f, err
	}

	completedAfter := q.Get("completed_since")
	if completedAfter == "" {
		completedAfter = q.Get("completed_after") // §5 spelling; completed_since is an alias of it
	}
	if f.CompletedAfter, err = parseTimeParam(completedAfter, "completed_since"); err != nil {
		return f, err
	}
	if f.CompletedBefore, err = parseTimeParam(q.Get("completed_before"), "completed_before"); err != nil {
		return f, err
	}

	limit := defaultListLimit
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit <= 0 {
			return f, fmt.Errorf("%w: limit must be a positive integer, got %q", domain.ErrInvalid, v)
		}
		if limit > maxListLimit {
			limit = maxListLimit // clamped, not rejected (use-case contract)
		}
		f.Limit = limit
	} else {
		f.Limit = defaultListLimit
	}

	offset := 0
	if v := q.Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			return f, fmt.Errorf("%w: offset must be a non-negative integer, got %q", domain.ErrInvalid, v)
		}
	}
	f.Offset = offset

	return f, nil
}

// urlValues is the query-value surface parseTaskFilter needs (url.Values
// satisfies it; keeps the helper trivially testable).
type urlValues interface{ Get(key string) string }

func parseTimeParam(v, name string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be RFC3339 (e.g. 2026-09-25T18:00:00Z), got %q", domain.ErrInvalid, name, v)
	}
	t = t.UTC()
	return &t, nil
}

// parsePageParams translates the offset/limit query params of paged child-row
// reads (GET /tasks/{id}/comments). Same policy as list_tasks: limit defaults
// to 20 and is clamped to 100 (values above the cap are NOT an error); offset
// defaults to 0. The returned values are the EFFECTIVE ones — echo them in the
// response envelope like list_tasks does. Malformed input → domain.ErrInvalid
// (→ 400).
func parsePageParams(q urlValues) (limit, offset int, err error) {
	limit = defaultListLimit
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit <= 0 {
			return 0, 0, fmt.Errorf("%w: limit must be a positive integer, got %q", domain.ErrInvalid, v)
		}
		if limit > maxListLimit {
			limit = maxListLimit // clamped, not rejected (use-case contract)
		}
	}
	offset = 0
	if v := q.Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("%w: offset must be a non-negative integer, got %q", domain.ErrInvalid, v)
		}
	}
	return limit, offset, nil
}

// parseTailLimit parses the optional "tail" limits on detail reads
// (comment_limit / event_limit). Absent or non-positive selects the use-case
// default; values above the cap are clamped by the use case. Negative or
// non-numeric input is a 400.
func parseTailLimit(v, name string) (int, error) {
	if v == "" {
		return 0, nil // zero = "use the documented default" (handled by the use case)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer, got %q", domain.ErrInvalid, name, v)
	}
	return n, nil
}

// extractActor extracts actor metadata for requests where actor can be supplied
// via body, request headers (X-Actor-Name, X-Actor-Type), or query parameters
// (actor_name, actor_type, or actor). Returns actorInput and true if an actor was found.
func extractActor(r *http.Request, bodyActor actorInput) (actorInput, bool) {
	if strings.TrimSpace(bodyActor.Name) != "" {
		return bodyActor, true
	}
	if name := strings.TrimSpace(r.Header.Get("X-Actor-Name")); name != "" {
		t := strings.TrimSpace(r.Header.Get("X-Actor-Type"))
		return actorInput{Name: name, Type: t}, true
	}
	if name := strings.TrimSpace(r.URL.Query().Get("actor_name")); name != "" {
		t := strings.TrimSpace(r.URL.Query().Get("actor_type"))
		return actorInput{Name: name, Type: t}, true
	}
	if name := strings.TrimSpace(r.URL.Query().Get("actor")); name != "" {
		t := strings.TrimSpace(r.URL.Query().Get("actor_type"))
		return actorInput{Name: name, Type: t}, true
	}
	return actorInput{}, false
}
