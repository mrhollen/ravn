// Package apiclient is a typed, stdlib-only HTTP client for the crowban REST
// API documented in docs/API.md (base path /api/v1). It is consumed only by
// the crowban-mcp binary and is never imported by cmd/crowban-server.
//
// The DTO types below mirror the API.md wire contract field-for-field, so a
// value decoded here and re-encoded is byte-compatible with what the server
// emits: same names, and the same omission rules (optional fields are absent
// when unset — never null).
package apiclient

import "time"

// DefaultTimeout bounds every request made through Client. crowban is a
// LAN-local service; 10s is generous for any of its round-trips.
const DefaultTimeout = 10 * time.Second

// Actor is the self-declared identity carried in every mutation body
// (API.md "Actor convention"). Name is required and non-empty — it is the
// lease/ownership key for claims and the attribution key for events. Type is
// "human" or "agent"; when empty the server defaults it to "human".
type Actor struct {
	Type string `json:"type,omitempty"` // "human" | "agent" (server default: human)
	Name string `json:"name"`           // required; claim/lease + attribution key
}

// TaskSummary is the compact task row returned by every list/board/create/
// update/claim/release/heartbeat response. Optional fields are omitted
// entirely when unset (never null). ClaimedBy is the EPHEMERAL lease holder,
// distinct from the durable Assignee.
type TaskSummary struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Status         string  `json:"status"` // todo | doing | done
	Priority       int     `json:"priority"`
	DueAt          *string `json:"due_at,omitempty"`
	Assignee       *Actor  `json:"assignee,omitempty"`
	ClaimedBy      *Actor  `json:"claimed_by,omitempty"`
	ClaimExpiresAt *string `json:"claim_expires_at,omitempty"`
	Version        int     `json:"version"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// TaskDetail is the one-call resume payload of GET /tasks/{id}: the summary
// fields flattened (embedded) plus everything an agent needs to pick work
// back up. All list fields are always present ([] when empty — never null).
type TaskDetail struct {
	TaskSummary

	ProjectID   string `json:"project_id"`
	Description string `json:"description"`
	Notes       string `json:"notes"`
	Archived    bool   `json:"archived"`

	// Blocked is the computed flag: an outgoing blocked-by edge to a task that
	// is not done. Blockers are the summary rows of those targets.
	Blocked  bool      `json:"blocked"`
	Blockers []Blocker `json:"blockers"`

	ClaimedAt       *string `json:"claimed_at,omitempty"`
	ClaimTTLSeconds int     `json:"claim_ttl_seconds,omitempty"` // 0 = not claimed
	CompletedAt     *string `json:"completed_at,omitempty"`

	AcceptanceCriteria []AcceptanceCriterion `json:"acceptance_criteria"`
	Comments           []Comment             `json:"comments"` // tail, newest first
	Artifacts          []Artifact            `json:"artifacts"`
	Relations          []Relation            `json:"relations"`
	Events             []Event               `json:"events"` // tail, newest first
}

// Blocker is the reduced row of a task that blocks this one (id/title/status).
type Blocker struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// AcceptanceCriterion mirrors the AC row. Seq is 1-based; Note and the
// completed_* fields are omitted when unset.
type AcceptanceCriterion struct {
	Seq         int     `json:"seq"`
	Text        string  `json:"text"`
	Done        bool    `json:"done"`
	Note        string  `json:"note,omitempty"`
	CompletedBy *Actor  `json:"completed_by,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
}

// Comment is a typed comment row. Kind is progress|question|decision|handoff|note.
type Comment struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"` // markdown
	Actor     Actor  `json:"actor"`
	CreatedAt string `json:"created_at"`
}

// Artifact is a typed evidence row. Kind is log|screenshot|command-output|trace|url|other;
// Ref is a URL or filesystem path; Summary is omitted when empty.
type Artifact struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Summary   string `json:"summary,omitempty"`
	Actor     Actor  `json:"actor"`
	CreatedAt string `json:"created_at"`
}

// Relation is a stored edge touching a task. Kind is always the STORED canonical
// kind — "parent" never appears on the wire (it is an input alias normalized to
// the inverse child edge). Edges are addressed by the composite
// (TaskID, OtherTaskID, Kind); relation ids are never exposed.
type Relation struct {
	TaskID      string `json:"task_id"`
	OtherTaskID string `json:"other_task_id"`
	Kind        string `json:"kind"` // child | blocked-by | duplicate-of | related (stored)
	CreatedAt   string `json:"created_at"`
}

// Event is an activity-log row. Before/After are compact JSON strings from the
// event store; each is omitted when empty. At is RFC3339 UTC.
type Event struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Actor  Actor  `json:"actor"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	At     string `json:"at"`
}

// StatusCounts holds per-status task counts; all three canonical statuses are
// always present, zero when empty.
type StatusCounts struct {
	Todo  int `json:"todo"`
	Doing int `json:"doing"`
	Done  int `json:"done"`
}

// ProjectList is the GET /projects response (not paged in the MVP): rows
// newest-first with per-status open task counts.
type ProjectList struct {
	Projects []Project `json:"projects"`
	Total    int       `json:"total"`
}

// TaskList is the standard pagination envelope of GET /tasks: summary rows in
// canonical order plus total (before paging) and the effective limit/offset
// after clamping.
type TaskList struct {
	Tasks  []TaskSummary `json:"tasks"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// Project is a project row. TaskCounts is present on list rows (GET /projects)
// and omitted on bare create/get rows — hence the pointer + omitempty.
type Project struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	TaskCounts  *StatusCounts `json:"task_counts,omitempty"`
}

// Health is the body of GET /healthz (outside /api/v1): {"status":"ok","db":"ok"}
// or a degraded variant with status 503 when the DB ping fails.
type Health struct {
	Status string `json:"status"` // ok | degraded
	DB     string `json:"db"`     // "ok" or "error: …"
}

// --- request bodies ------------------------------------------------------------------

// CreateProjectRequest is POST /projects. Name must be non-empty (else 400).
type CreateProjectRequest struct {
	Actor       Actor  `json:"actor"`
	Name        string `json:"name"`
	Description string `json:"description"` // optional markdown
}

// TaskFilter selects GET /tasks query params; zero values mean "use the server
// default" and produce no query param at all. Priority uses a pointer because 0
// is a valid priority and must be distinguishable from unset.
type TaskFilter struct {
	ProjectID       string // ULID, restrict to one project
	Status          string // todo | doing | done
	AssigneeName    string // durable assignee name (param alias: assignee)
	AssigneeType    string // human | agent
	Archived        bool   // true lists archived tasks; default false = open only
	Unblocked       bool   // keep only tasks with no open blocker
	Search          string // substring over title+description+notes (param alias: q)
	Priority        *int   // exact match 0-5
	DueBefore       string // RFC3339; due_at strictly before (undated never matches)
	DueAfter        string // RFC3339; inclusive
	CreatedAfter    string // RFC3339 window over creation time
	CreatedBefore   string
	CompletedAfter  string // aliases of completed_since
	CompletedBefore string

	Limit  int // server default 20, max 100 (clamped)
	Offset int // server default 0
}

// CreateTaskRequest is POST /tasks. Title and ProjectID are required; the rest
// optional. Priority is 0-5 (default 0); DueAt is RFC3339. AcceptanceCriteria
// become seq 1..n in one transaction with the task.
type CreateTaskRequest struct {
	Actor              Actor    `json:"actor"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	ProjectID          string   `json:"project_id"`
	Priority           int      `json:"priority"`
	DueAt              *string  `json:"due_at,omitempty"`
	Assignee           *Actor   `json:"assignee,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}

// CreateTaskResponse is the 201 body of POST /tasks: the fresh summary row plus
// an ADVISORY duplicate candidate list (never an error; empty when nothing in
// the project resembles the title).
type CreateTaskResponse struct {
	Task               TaskSummary   `json:"task"`
	PossibleDuplicates []TaskSummary `json:"possible_duplicates"`
}

// UpdateTaskRequest is PATCH /tasks/{id}. Version is REQUIRED — the stored
// version last read (fresh tasks start at 1); missing/0 → 400, stale → 409
// version_conflict carrying a fresh row. Every other field is optional with
// pointer semantics: nil = unchanged.
type UpdateTaskRequest struct {
	Actor   Actor `json:"actor"`
	Version int   `json:"version"` // required

	Title         *string `json:"title,omitempty"`
	Description   *string `json:"description,omitempty"`
	Status        *string `json:"status,omitempty"`    // validated against the transition map
	Priority      *int    `json:"priority,omitempty"`  // 0-5
	DueAt         *string `json:"due_at,omitempty"`    // RFC3339 to set…
	ClearDue      bool    `json:"clear_due,omitempty"` // …or clear the deadline
	Assignee      *Actor  `json:"assignee,omitempty"`  // durable ownership; or
	ClearAssignee bool    `json:"clear_assignee,omitempty"`
	Archived      *bool   `json:"archived,omitempty"`
}

// ClaimTaskRequest is POST /tasks/{id}/claim. TTLSec is optional: 0 means
// "omit" (server default 900s); values above the cap are clamped to 3600s by
// the server; negative → 400.
type ClaimTaskRequest struct {
	Actor  Actor `json:"actor"`
	TTLSec *int  `json:"ttl_sec,omitempty"`
}

// ReleaseTaskRequest is POST /tasks/{id}/release. Note is optional and rides on
// the released event (it is NOT posted as a comment).
type ReleaseTaskRequest struct {
	Actor Actor  `json:"actor"`
	Note  string `json:"note,omitempty"`
}

// HeartbeatRequest is POST /tasks/{id}/heartbeat — actor only; the stored TTL
// is reused. Heartbeats bump no version and emit no event.
type HeartbeatRequest struct {
	Actor Actor `json:"actor"`
}

// CommentRequest is POST /tasks/{id}/comments. Kind must be one of
// progress|question|decision|handoff|note (else 400); Body is markdown and must
// be non-empty after trimming (else 400). Appends are NOT idempotent.
type CommentRequest struct {
	Actor Actor  `json:"actor"`
	Kind  string `json:"kind"`
	Body  string `json:"body"`
}

// NotesRequest is PUT /tasks/{id}/notes: REPLACE-ON-WRITE of the whole blob.
// Deliberately carries no version field (single-writer scratchpad); identical
// content is a no-op, a concurrent race can surface as 409 version_conflict.
type NotesRequest struct {
	Actor Actor  `json:"actor"`
	Notes string `json:"notes"`
}

// ACToggleRequest is PATCH /tasks/{id}/acceptance-criteria/{seq}. Done=true
// records completed_by/completed_at; false clears both (uncheck is free).
type ACToggleRequest struct {
	Actor Actor  `json:"actor"`
	Done  bool   `json:"done"`
	Note  string `json:"note,omitempty"`
}

// ArtifactRequest is POST /tasks/{id}/artifacts. Kind must be one of
// log|screenshot|command-output|trace|url|other (else 400); Ref is a URL or
// filesystem path, non-empty after trimming; Summary is an optional one-liner.
type ArtifactRequest struct {
	Actor   Actor  `json:"actor"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Summary string `json:"summary,omitempty"`
}

// RelationRequest is POST /tasks/{id}/relations. Kind accepts the tool-boundary
// values child|parent|blocked-by|duplicate-of|related ("parent" is normalized to
// the inverse child edge server-side). OtherID must name an existing task in the
// SAME project (else 400/404) and may not equal {id}. Upsert: re-adding an
// existing identical edge → 200, no event (idempotent).
type RelationRequest struct {
	Actor   Actor  `json:"actor"`
	OtherID string `json:"other_id"`
	Kind    string `json:"kind"`
}

// UnrelateTaskRequest is the optional body of DELETE /tasks/{id}/relations/
// {other_id}/{kind}. It must carry a valid actor when present (the removal
// appends an event recording who removed it).
type UnrelateTaskRequest struct {
	Actor Actor `json:"actor"`
}
