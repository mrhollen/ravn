package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiPrefix is the base path of every versioned ravn route (API.md).
const apiPrefix = "/api/v1"

// maxResponseBytes caps how much of a response body we will ever buffer.
// ravn payloads are small (a full resume payload is tens of KB at most);
// this guards against pathological responses, not against limits in general.
const maxResponseBytes = 1 << 20

// Client is a typed REST client for one ravn-server instance. It is safe
// for concurrent use (http.Client is; the fields are immutable after New).
type Client struct {
	base  string // base URL without trailing slash, e.g. http://ravn-server:8080
	token string // optional; sent as Authorization: Bearer <token> when non-empty
	http  *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient overrides the underlying http.Client (tests, custom TLS).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithTimeout overrides the per-request timeout (default DefaultTimeout).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// New builds a Client for baseURL. token may be empty: ravn is
// single-tenant/LAN-local and unauthenticated by default, but when a token IS
// configured it is forwarded on every request (API.md "Auth").
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: DefaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL returns the configured base URL (used in unreachable messages).
func (c *Client) BaseURL() string { return c.base }

// ListProjects → GET /projects. Not paged; rows newest-first, each carrying
// per-status open task counts.
func (c *Client) ListProjects(ctx context.Context) (*ProjectList, error) {
	var out ProjectList
	if err := c.do(ctx, "ListProjects", http.MethodGet, "/projects", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateProject → POST /projects. 201 with the fresh project row (no counts).
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	var out Project
	if err := c.do(ctx, "CreateProject", http.MethodPost, "/projects", nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProject → GET /projects/{id}. 200 with the project row.
func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	var out Project
	path := "/projects/" + url.PathEscape(id)
	if err := c.do(ctx, "GetProject", http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateProject → PUT /projects/{id}. 200 with the updated project row.
func (c *Client) UpdateProject(ctx context.Context, id string, req UpdateProjectRequest) (*Project, error) {
	var out Project
	path := "/projects/" + url.PathEscape(id)
	if err := c.do(ctx, "UpdateProject", http.MethodPut, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTasks → GET /tasks. Canonical order (priority DESC → due ASC with undated
// last → created ASC) inside the offset/limit envelope. Zero filter fields add
// no query params; the server applies its own defaults (limit 20).
func (c *Client) ListTasks(ctx context.Context, f TaskFilter) (*TaskList, error) {
	var out TaskList
	if err := c.do(ctx, "ListTasks", http.MethodGet, "/tasks", taskFilterQuery(f), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTask → GET /tasks/{id}: the one-call resume payload. commentLimit and
// eventLimit are 0 = "omit" (server defaults 10/20; maxima 50/100 clamp).
func (c *Client) GetTask(ctx context.Context, id string, commentLimit, eventLimit int) (*TaskDetail, error) {
	q := url.Values{}
	if commentLimit > 0 {
		q.Set("comment_limit", fmt.Sprintf("%d", commentLimit))
	}
	if eventLimit > 0 {
		q.Set("event_limit", fmt.Sprintf("%d", eventLimit))
	}
	var out TaskDetail
	path := "/tasks/" + url.PathEscape(id)
	if err := c.do(ctx, "GetTask", http.MethodGet, path, orNil(q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateTask → POST /tasks. 201 with the fresh row plus advisory duplicates.
func (c *Client) CreateTask(ctx context.Context, req CreateTaskRequest) (*CreateTaskResponse, error) {
	var out CreateTaskResponse
	if err := c.do(ctx, "CreateTask", http.MethodPost, "/tasks", nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateTask → PATCH /tasks/{id}. Version-guarded partial update; a stale
// version returns *APIError with status 409 and the version_conflict body
// (which carries the fresh row for rebasing).
func (c *Client) UpdateTask(ctx context.Context, id string, req UpdateTaskRequest) (*TaskSummary, error) {
	var out TaskSummary
	path := "/tasks/" + url.PathEscape(id)
	if err := c.do(ctx, "UpdateTask", http.MethodPatch, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimTask → POST /tasks/{id}/claim. Atomic CAS: wins when the lease is free,
// expired (takeover), or held by the same named actor (renewal); a foreign
// active lease loses with *APIError status 409 and the claim-conflict body
// (holder + expires_at verbatim). ttlSec 0 = omit (server default 900s).
func (c *Client) ClaimTask(ctx context.Context, id string, actor Actor, ttlSec int) (*TaskSummary, error) {
	req := ClaimTaskRequest{Actor: actor}
	if ttlSec != 0 {
		v := ttlSec
		req.TTLSec = &v
	}
	var out TaskSummary
	path := "/tasks/" + url.PathEscape(id) + "/claim"
	if err := c.do(ctx, "ClaimTask", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseTask → POST /tasks/{id}/release. Holder-only against an active lease;
// a task with no lease at all is an idempotent no-op returning the fresh row.
func (c *Client) ReleaseTask(ctx context.Context, id string, actor Actor, note string) (*TaskSummary, error) {
	req := ReleaseTaskRequest{Actor: actor, Note: note}
	var out TaskSummary
	path := "/tasks/" + url.PathEscape(id) + "/release"
	if err := c.do(ctx, "ReleaseTask", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HeartbeatTask → POST /tasks/{id}/heartbeat. Extends the active self-lease by
// the stored TTL; bumps no version, emits no event. The fresh row doubles as
// the liveness answer.
func (c *Client) HeartbeatTask(ctx context.Context, id string, actor Actor) (*TaskSummary, error) {
	req := HeartbeatRequest{Actor: actor}
	var out TaskSummary
	path := "/tasks/" + url.PathEscape(id) + "/heartbeat"
	if err := c.do(ctx, "HeartbeatTask", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddComment → POST /tasks/{id}/comments. 201 with the fresh comment row.
// Appends are NOT idempotent — a retry after a timeout may duplicate.
func (c *Client) AddComment(ctx context.Context, id string, req CommentRequest) (*Comment, error) {
	var out Comment
	path := "/tasks/" + url.PathEscape(id) + "/comments"
	if err := c.do(ctx, "AddComment", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateNotes → PUT /tasks/{id}/notes. Replace-on-write of the whole blob; no
// version field in the request (single-writer scratchpad). Identical content is
// a no-op; a concurrent race may surface as 409 version_conflict.
func (c *Client) UpdateNotes(ctx context.Context, id string, actor Actor, notes string) (*TaskSummary, error) {
	req := NotesRequest{Actor: actor, Notes: notes}
	var out TaskSummary
	path := "/tasks/" + url.PathEscape(id) + "/notes"
	if err := c.do(ctx, "UpdateNotes", http.MethodPut, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CompleteAC → PATCH /tasks/{id}/acceptance-criteria/{seq}. seq is the 1-based
// criterion sequence; unknown task OR unknown seq → 404. No version bump
// (child-row rule). Returns the updated AC row.
func (c *Client) CompleteAC(ctx context.Context, id string, seq int, req ACToggleRequest) (*AcceptanceCriterion, error) {
	var out AcceptanceCriterion
	path := fmt.Sprintf("/tasks/%s/acceptance-criteria/%d", url.PathEscape(id), seq)
	if err := c.do(ctx, "CompleteAC", http.MethodPatch, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AddAC → POST /tasks/{id}/acceptance-criteria. Appends a new criterion at
// next seq (max(seq)+1). 201 with the fresh AC row. Child-row rule: no task
// version bump.
func (c *Client) AddAC(ctx context.Context, id string, req ACAddRequest) (*AcceptanceCriterion, error) {
	var out AcceptanceCriterion
	path := fmt.Sprintf("/tasks/%s/acceptance-criteria", url.PathEscape(id))
	if err := c.do(ctx, "AddAC", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveAC → DELETE /tasks/{id}/acceptance-criteria/{seq}. seq is the 1-based
// criterion sequence. 204 on success (no body). Unknown task OR unknown seq → 404.
// Child-row rule: no task version bump. Surviving criteria keep their seqs.
func (c *Client) RemoveAC(ctx context.Context, id string, seq int, actor Actor) error {
	req := ACRemoveRequest{Actor: actor}
	path := fmt.Sprintf("/tasks/%s/acceptance-criteria/%d", url.PathEscape(id), seq)
	headers := map[string]string{
		"X-Actor-Name": actor.Name,
		"X-Actor-Type": string(actor.Type),
	}
	return c.doWithHeaders(ctx, "RemoveAC", http.MethodDelete, path, nil, headers, &req, nil)
}

// EditACText → PATCH /tasks/{id}/acceptance-criteria/{seq}. seq is the 1-based
// criterion sequence. Rewords the criterion text. 200 with the updated AC row.
// Child-row rule: no task version bump.
func (c *Client) EditACText(ctx context.Context, id string, seq int, req ACEditTextRequest) (*AcceptanceCriterion, error) {
	var out AcceptanceCriterion
	path := fmt.Sprintf("/tasks/%s/acceptance-criteria/%d", url.PathEscape(id), seq)
	if err := c.do(ctx, "EditACText", http.MethodPatch, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AttachArtifact → POST /tasks/{id}/artifacts. 201 with the fresh artifact row.
// Appends are NOT idempotent.
func (c *Client) AttachArtifact(ctx context.Context, id string, req ArtifactRequest) (*Artifact, error) {
	var out Artifact
	path := "/tasks/" + url.PathEscape(id) + "/artifacts"
	if err := c.do(ctx, "AttachArtifact", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadArtifact uploads a file via POST /tasks/{id}/artifacts with multipart/form-data.
// Returns the created Artifact row (201).
func (c *Client) UploadArtifact(ctx context.Context, id string, filename string, r io.Reader, kind, summary string, actor Actor) (*Artifact, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("UploadArtifact: create form file: %w", err)
	}
	if _, err := io.Copy(part, r); err != nil {
		return nil, fmt.Errorf("UploadArtifact: copy file body: %w", err)
	}

	if err := w.WriteField("kind", kind); err != nil {
		return nil, fmt.Errorf("UploadArtifact: write kind: %w", err)
	}
	if summary != "" {
		if err := w.WriteField("summary", summary); err != nil {
			return nil, fmt.Errorf("UploadArtifact: write summary: %w", err)
		}
	}
	if actor.Name != "" {
		if err := w.WriteField("actor_name", actor.Name); err != nil {
			return nil, fmt.Errorf("UploadArtifact: write actor_name: %w", err)
		}
		if err := w.WriteField("actor_type", string(actor.Type)); err != nil {
			return nil, fmt.Errorf("UploadArtifact: write actor_type: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("UploadArtifact: close multipart writer: %w", err)
	}

	u := c.base + apiPrefix + "/tasks/" + url.PathEscape(id) + "/artifacts"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return nil, fmt.Errorf("UploadArtifact: building request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("UploadArtifact: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("UploadArtifact: reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Op: "UploadArtifact", StatusCode: resp.StatusCode, URL: u, Body: data}
	}

	var out Artifact
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("UploadArtifact: decoding %d response: %w", resp.StatusCode, err)
	}
	return &out, nil
}

// RelateTasks → POST /tasks/{id}/relations (add). Upsert: a brand-new edge →
// 201; re-adding an existing identical edge → 200 with the same stored row and
// no event. A second, different parent → 409 parent conflict.
func (c *Client) RelateTasks(ctx context.Context, id string, req RelationRequest) (*Relation, error) {
	var out Relation
	path := "/tasks/" + url.PathEscape(id) + "/relations"
	if err := c.do(ctx, "RelateTasks", http.MethodPost, path, nil, &req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UnrelateTasks → DELETE /tasks/{id}/relations/{other_id}/{kind}. 204 on
// success (no body); the optional body carries the actor so the removal event
// records who removed it. Missing edge → 404.
func (c *Client) UnrelateTasks(ctx context.Context, id string, actor Actor, otherID, kind string) error {
	req := UnrelateTaskRequest{Actor: actor}
	path := fmt.Sprintf("/tasks/%s/relations/%s/%s", url.PathEscape(id), url.PathEscape(otherID), url.PathEscape(kind))
	headers := map[string]string{
		"X-Actor-Name": actor.Name,
		"X-Actor-Type": string(actor.Type),
	}
	return c.doWithHeaders(ctx, "UnrelateTasks", http.MethodDelete, path, nil, headers, &req, nil)
}

// Health → GET /healthz — OUTSIDE the /api/v1 prefix (API.md) and never
// authenticated. A degraded upstream (DB ping failed) answers 503 and surfaces
// as *APIError with the degraded body — call sites that proxy health can
// re-emit it verbatim.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var out Health
	if err := c.doPath(ctx, "Health", http.MethodGet, "/healthz", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do performs one REST call against the versioned API: it prepends /api/v1 to
// path and delegates to doPath.
func (c *Client) do(ctx context.Context, op, method, path string, query url.Values, body any, out any) error {
	return c.doPathWithHeaders(ctx, op, method, apiPrefix+path, query, nil, body, out)
}

func (c *Client) doWithHeaders(ctx context.Context, op, method, path string, query url.Values, headers map[string]string, body any, out any) error {
	return c.doPathWithHeaders(ctx, op, method, apiPrefix+path, query, headers, body, out)
}

// doPath performs one REST call: marshal the request (if any), apply auth and
// content-type headers, execute, read the full response body, and either
// decode it into out (2xx) or wrap it in *APIError (non-2xx). The upstream
// body is ALWAYS preserved verbatim on error — that channel carries the
// agent-readable recovery instructions to the MCP layer.
func (c *Client) doPath(ctx context.Context, op, method, path string, query url.Values, body any, out any) error {
	return c.doPathWithHeaders(ctx, op, method, path, query, nil, body, out)
}

func (c *Client) doPathWithHeaders(ctx context.Context, op, method, path string, query url.Values, headers map[string]string, body any, out any) error {
	u := c.base + path
	if query != nil && len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: encoding request: %w", op, err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("%s: building request: %w", op, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failure (server down, DNS, timeout). NOT an APIError — the
		// MCP layer maps it to the documented "unreachable" message.
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s: reading response: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Op: op, StatusCode: resp.StatusCode, URL: u, Body: data}
	}
	if out == nil || len(data) == 0 { // 204 No Content etc.
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decoding %d response: %w", op, resp.StatusCode, err)
	}
	return nil
}

// taskFilterQuery renders TaskFilter as GET /tasks query params. Zero values
// are omitted entirely (the server applies its own defaults).
func taskFilterQuery(f TaskFilter) url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("project_id", f.ProjectID)
	set("status", f.Status)
	set("assignee_name", f.AssigneeName)
	set("assignee_type", f.AssigneeType)
	if f.Archived {
		q.Set("archived", "true")
	}
	if f.Unblocked {
		q.Set("unblocked", "true")
	}
	set("search", f.Search)
	if f.Priority != nil {
		q.Set("priority", fmt.Sprintf("%d", *f.Priority))
	}
	set("due_before", f.DueBefore)
	set("due_after", f.DueAfter)
	set("created_after", f.CreatedAfter)
	set("created_before", f.CreatedBefore)
	set("completed_after", f.CompletedAfter)
	set("completed_before", f.CompletedBefore)
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", f.Offset))
	}
	return q
}

func orNil(q url.Values) url.Values {
	if len(q) == 0 {
		return nil
	}
	return q
}
