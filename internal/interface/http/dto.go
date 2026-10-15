package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// --- actors ------------------------------------------------------------------

// actorDTO is the wire form of domain.Actor: {"type":"human|agent","name":...}.
type actorDTO struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func toActor(a *domain.Actor) *actorDTO {
	if a == nil {
		return nil
	}
	return &actorDTO{Type: string(a.Type), Name: a.Name}
}

// --- task rows -----------------------------------------------------------------

// taskSummaryDTO is the compact task row every list endpoint, the board
// snapshot, and create/update responses return (context-economy: summary rows
// keep an agent's working set small; GET /tasks/{id} carries the full resume
// payload instead). Timestamps are RFC3339 UTC.
type taskSummaryDTO struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Priority       int       `json:"priority"`
	DueAt          *string   `json:"due_at,omitempty"`
	Assignee       *actorDTO `json:"assignee,omitempty"`
	ClaimedBy      *actorDTO `json:"claimed_by,omitempty"`
	ClaimExpiresAt *string   `json:"claim_expires_at,omitempty"`
	Version        int       `json:"version"`
	CreatedAt      string    `json:"created_at"`
	UpdatedAt      string    `json:"updated_at"`
}

func toSummary(t domain.Task) taskSummaryDTO {
	s := taskSummaryDTO{
		ID:        t.ID.String(),
		Title:     t.Title,
		Status:    string(t.Status),
		Priority:  int(t.Priority),
		Version:   t.Version,
		CreatedAt: rfc3339(t.CreatedAt),
		UpdatedAt: rfc3339(t.UpdatedAt),
	}
	if t.DueAt != nil {
		v := rfc3339(*t.DueAt)
		s.DueAt = &v
	}
	s.Assignee = toActor(t.Assignee)
	if t.ClaimedBy != nil {
		s.ClaimedBy = toActor(t.ClaimedBy)
	}
	if t.ClaimExpiry != nil {
		v := rfc3339(*t.ClaimExpiry)
		s.ClaimExpiresAt = &v
	}
	return s
}

// taskDetailDTO is the one-call resume payload (GET /tasks/{id}, ARCHITECTURE
// §5): the summary row's fields (flattened via embedding) plus notes, claim-
// lease detail, and every child collection an agent needs to pick work back up.
// All list fields are always present ([] when empty — never null).
type taskDetailDTO struct {
	taskSummaryDTO

	ProjectID   string `json:"project_id"`
	Description string `json:"description"`
	Notes       string `json:"notes"`
	Archived    bool   `json:"archived"`

	Blocked  bool         `json:"blocked"`
	Blockers []blockerDTO `json:"blockers"`

	ClaimedAt       *string `json:"claimed_at,omitempty"`
	ClaimTTLSeconds int     `json:"claim_ttl_seconds,omitempty"` // 0 = not claimed
	CompletedAt     *string `json:"completed_at,omitempty"`

	AcceptanceCriteria []acDTO       `json:"acceptance_criteria"`
	Comments           []commentDTO  `json:"comments"`
	Artifacts          []artifactDTO `json:"artifacts"`
	Relations          []relationDTO `json:"relations"`
	Events             []eventDTO    `json:"events"`
}

type blockerDTO struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type acDTO struct {
	Seq         int       `json:"seq"`
	Text        string    `json:"text"`
	Done        bool      `json:"done"`
	Note        string    `json:"note,omitempty"`
	CompletedBy *actorDTO `json:"completed_by,omitempty"`
	CompletedAt *string   `json:"completed_at,omitempty"`
}

func toAC(c domain.AcceptanceCriterion) acDTO {
	dto := acDTO{Seq: c.Seq, Text: c.Text, Done: c.Done, Note: c.Note, CompletedBy: toActor(c.CompletedBy)}
	if c.CompletedAt != nil {
		v := rfc3339(*c.CompletedAt)
		dto.CompletedAt = &v
	}
	return dto
}

type commentDTO struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Body      string   `json:"body"`
	Actor     actorDTO `json:"actor"`
	CreatedAt string   `json:"created_at"`
}

func toComment(c domain.Comment) commentDTO {
	return commentDTO{
		ID:        c.ID.String(),
		Kind:      string(c.Kind),
		Body:      c.Body,
		Actor:     actorDTO{Type: string(c.Actor.Type), Name: c.Actor.Name},
		CreatedAt: rfc3339(c.CreatedAt),
	}
}

type artifactDTO struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Ref       string   `json:"ref"`
	Summary   string   `json:"summary,omitempty"`
	Actor     actorDTO `json:"actor"`
	CreatedAt string   `json:"created_at"`
}

func toArtifact(a domain.Artifact) artifactDTO {
	return artifactDTO{
		ID:        a.ID.String(),
		Kind:      string(a.Kind),
		Ref:       a.Ref,
		Summary:   a.Summary,
		Actor:     actorDTO{Type: string(a.Actor.Type), Name: a.Actor.Name},
		CreatedAt: rfc3339(a.CreatedAt),
	}
}

// relationDTO is a raw edge touching the task in either direction. The edge's
// own id is intentionally omitted: relations are addressed on the wire by the
// composite (task, other_task, kind), so agents never need to remember
// relation ids (ARCHITECTURE §5 DELETE route).
type relationDTO struct {
	TaskID      string `json:"task_id"`
	OtherTaskID string `json:"other_task_id"`
	Kind        string `json:"kind"`
	CreatedAt   string `json:"created_at"`
}

// toRelation renders one stored edge. The kind is ALWAYS the stored (canonical)
// kind — the 'parent' tool-boundary alias never appears in a persisted row, so
// it never appears here either.
func toRelation(r domain.Relation) relationDTO {
	return relationDTO{
		TaskID:      r.TaskID.String(),
		OtherTaskID: r.OtherTaskID.String(),
		Kind:        string(r.Kind),
		CreatedAt:   rfc3339(r.CreatedAt),
	}
}

// eventDTO is one activity-log entry; Before/After are the compact JSON
// payloads verbatim from the event store.
type eventDTO struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Actor  actorDTO `json:"actor"`
	Before string   `json:"before,omitempty"`
	After  string   `json:"after,omitempty"`
	At     string   `json:"at"`
}

func toDetail(d application.TaskDetail) taskDetailDTO {
	out := taskDetailDTO{
		taskSummaryDTO: toSummary(d.Task),
		ProjectID:      d.Task.ProjectID.String(),
		Description:    d.Task.Description,
		Notes:          d.Task.Notes,
		Archived:       d.Task.Archived,
		Blocked:        d.Blocked,
	}
	if d.Task.ClaimedAt != nil {
		v := rfc3339(*d.Task.ClaimedAt)
		out.ClaimedAt = &v
	}
	if d.Task.ClaimTTLSeconds > 0 {
		out.ClaimTTLSeconds = d.Task.ClaimTTLSeconds
	}
	if d.Task.CompletedAt != nil {
		v := rfc3339(*d.Task.CompletedAt)
		out.CompletedAt = &v
	}

	out.Blockers = make([]blockerDTO, 0, len(d.Blockers))
	for _, b := range d.Blockers {
		out.Blockers = append(out.Blockers, blockerDTO{
			ID: b.ID.String(), Title: b.Title, Status: string(b.Status),
		})
	}

	out.AcceptanceCriteria = make([]acDTO, 0, len(d.AcceptanceCriteria))
	for _, c := range d.AcceptanceCriteria {
		out.AcceptanceCriteria = append(out.AcceptanceCriteria, toAC(c))
	}

	out.Comments = make([]commentDTO, 0, len(d.Comments))
	for _, c := range d.Comments {
		out.Comments = append(out.Comments, toComment(c))
	}

	out.Artifacts = make([]artifactDTO, 0, len(d.Artifacts))
	for _, a := range d.Artifacts {
		out.Artifacts = append(out.Artifacts, toArtifact(a))
	}

	out.Relations = make([]relationDTO, 0, len(d.Relations))
	for _, r := range d.Relations {
		out.Relations = append(out.Relations, toRelation(r))
	}

	out.Events = make([]eventDTO, 0, len(d.Events))
	for _, e := range d.Events {
		out.Events = append(out.Events, eventDTO{
			ID:     e.ID.String(),
			Kind:   string(e.Kind),
			Actor:  actorDTO{Type: string(e.Actor.Type), Name: e.Actor.Name},
			Before: e.Before,
			After:  e.After,
			At:     rfc3339(e.At),
		})
	}

	return out
}

// --- projects ------------------------------------------------------------------

// projectDTO is the wire form of domain.Project.
type projectDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	OpenCodeServerURL string `json:"opencode_server_url,omitempty"`
	OpenCodeDirectory string `json:"opencode_directory,omitempty"`
	OpenCodeModel     string `json:"opencode_model,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func toProject(p domain.Project) projectDTO {
	return projectDTO{
		ID:                p.ID.String(),
		Name:              p.Name,
		Description:       p.Description,
		OpenCodeServerURL: p.OpenCodeServerURL,
		OpenCodeDirectory: p.OpenCodeDirectory,
		OpenCodeModel:     p.OpenCodeModel,
		CreatedAt:         rfc3339(p.CreatedAt),
		UpdatedAt:         rfc3339(p.UpdatedAt),
	}
}

// statusCountDTO carries per-status open task counts (GET /projects rows and
// the board snapshot). All three canonical statuses are always present, zero
// when empty.
type statusCountDTO struct {
	Todo  int `json:"todo"`
	Doing int `json:"doing"`
	Done  int `json:"done"`
}

func toCounts(m map[domain.Status]int64) statusCountDTO {
	return statusCountDTO{
		Todo:  int(m[domain.StatusTodo]),
		Doing: int(m[domain.StatusDoing]),
		Done:  int(m[domain.StatusDone]),
	}
}

// projectRowDTO is one GET /projects row: the project plus its per-status open
// task counts (ARCHITECTURE §5: "rows + per-status task counts").
type projectRowDTO struct {
	projectDTO
	TaskCounts statusCountDTO `json:"task_counts"`
}

// --- responses -------------------------------------------------------------------

// createTaskResponse is the 201 body of POST /tasks: the fresh task (summary
// row) plus advisory duplicate candidates from the same project, EXCLUDING the
// new task's own id. Advisory only — never an error condition; empty when
// nothing in the project resembles the title.
type createTaskResponse struct {
	Task               taskSummaryDTO   `json:"task"`
	PossibleDuplicates []taskSummaryDTO `json:"possible_duplicates"`
}

// boardColumns groups the project's open tasks by status for the board
// snapshot (GET /projects/{id}/board). Each column holds summary rows in
// canonical order and is always present ([] when empty — never null).
type boardColumns struct {
	Todo  []taskSummaryDTO `json:"todo"`
	Doing []taskSummaryDTO `json:"doing"`
	Done  []taskSummaryDTO `json:"done"`
}

// boardResponse is the whole-project board in one round-trip: grouped summary
// rows plus per-status counts (the counts are the column lengths).
type boardResponse struct {
	ProjectID string         `json:"project_id"`
	Counts    statusCountDTO `json:"counts"`
	Tasks     boardColumns   `json:"tasks"`
}

// --- request bodies ----------------------------------------------------------------

// actorInput is the self-declared acting identity every mutation carries
// (single-tenant MVP; the name is the lease/ownership key). Name is required;
// Type is optional and defaults to "human" when omitted — a bare name from a
// browser or curl is usually human, while agents should send type="agent"
// explicitly. An empty name or an unknown type fails with domain.ErrInvalid
// (→ 400).
type actorInput struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func (a actorInput) toDomain() (domain.Actor, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return domain.Actor{}, fmt.Errorf("%w: actor.name is required", domain.ErrInvalid)
	}
	t := a.Type
	if t == "" {
		t = string(domain.ActorHuman) // default when the type is omitted (see doc above)
	}
	at := domain.ActorType(t)
	if !at.Valid() {
		return domain.Actor{}, fmt.Errorf(
			"%w: actor.type must be %q or %q, got %q", domain.ErrInvalid, domain.ActorHuman, domain.ActorAgent, t)
	}
	return domain.Actor{Type: at, Name: name}, nil
}

// createProjectReq is POST /projects. Name is required (empty → 400 from the
// use case).
type createProjectReq struct {
	Actor       actorInput `json:"actor"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
}

// updateProjectReq is PUT /projects/{id}. Pointer semantics: nil = unchanged;
// at least one field must be present (the use case rejects an empty update).
type updateProjectReq struct {
	Actor             actorInput `json:"actor"`
	Name              *string    `json:"name"`
	Description       *string    `json:"description"`
	OpenCodeServerURL *string    `json:"opencode_server_url"`
	OpenCodeDirectory *string    `json:"opencode_directory"`
	OpenCodeModel     *string    `json:"opencode_model"`
}

// createTaskReq is POST /tasks. Zero/omitted optional fields take documented
// defaults: priority 0, no deadline, unassigned. DueAt is RFC3339.
type createTaskReq struct {
	Actor              actorInput  `json:"actor"`
	Title              string      `json:"title"` // required; empty → 400
	Description        string      `json:"description"`
	ProjectID          string      `json:"project_id"` // required; must exist (else 400)
	Priority           int         `json:"priority"`   // 0–5; out of range → 400
	DueAt              *string     `json:"due_at"`
	Assignee           *actorInput `json:"assignee"`
	AcceptanceCriteria []string    `json:"acceptance_criteria"`
}

// updateTaskReq is PATCH /tasks/{id}. Version is the stored version the caller
// last read — required; the write applies only while it still matches (else
// 409 carrying the fresh row). All other fields are optional: omitted =
// unchanged. DueAt present → set (RFC3339); clear_due → remove any deadline;
// assignee present → set; clear_assignee → remove the durable owner.
type updateTaskReq struct {
	Actor   actorInput `json:"actor"`
	Version int        `json:"version"` // required; 0 = missing → 400 (tasks start at v1)

	Title            *string     `json:"title"`
	Description      *string     `json:"description"`
	Status           *string     `json:"status"`
	Priority         *int        `json:"priority"`
	DueAt            *string     `json:"due_at"`
	ClearDue         bool        `json:"clear_due"`
	Assignee         *actorInput `json:"assignee"`
	ClearAssignee    bool        `json:"clear_assignee"`
	Archived         *bool       `json:"archived"`
	OverrideBlockers bool        `json:"override_blockers"`
	OverrideACs      bool        `json:"override_acs"`
}

// --- slice 3b request bodies ------------------------------------------------------

// claimReq is POST /tasks/{id}/claim. Actor required; TTLSec optional —
// omitted/0 → server default (900s), negative → 400, values above the cap are
// clamped to 3600 by the use case. OverrideBlockers optionally bypasses the
// open-blocker gate (emergency unblock); omitted = false (backward compatible).
type claimReq struct {
	Actor            actorInput `json:"actor"`
	TTLSec           *int       `json:"ttl_sec"`
	OverrideBlockers bool       `json:"override_blockers"`
}

// releaseReq is POST /tasks/{id}/release. The optional note rides on the
// released event payload (it does NOT become a comment).
type releaseReq struct {
	Actor actorInput `json:"actor"`
	Note  string     `json:"note"`
}

// heartbeatReq is POST /tasks/{id}/heartbeat — keep-alive; the actor alone.
type heartbeatReq struct {
	Actor actorInput `json:"actor"`
}

// commentReq is POST /tasks/{id}/comments. Kind must be one of
// progress|question|decision|handoff|note (else 400); Body is markdown and
// must not be empty after trimming (else 400).
type commentReq struct {
	Actor actorInput `json:"actor"`
	Kind  string     `json:"kind"`
	Body  string     `json:"body"`
}

// notesReq is PUT /tasks/{id}/notes — whole-blob replace. Version is an
// OPTIONAL optimistic guard: omitted/0 keeps the historical contract (single-
// writer resume scratchpad; only a genuinely interlocking concurrent write can
// lose the use case's internal CAS and surface as 409), while a provided value
// makes a stale base version conflict with 409 carrying the fresh row — even
// when the blob is identical (see handleUpdateNotes). Existing clients that
// send no field are unaffected (absent decodes to 0 = unguarded).
type notesReq struct {
	Actor   actorInput `json:"actor"`
	Notes   string     `json:"notes"`
	Version int        `json:"version"` // optional; 0/absent = unguarded (legacy)
}

// acToggleReq is PATCH /tasks/{id}/acceptance-criteria/{seq}. Two shapes share
// the endpoint: Done (bool) sets/clears the checkbox, with Note optionally
// recording why — the original complete_ac form; or Text (*string) rewords the
// criterion, in which case it takes precedence and Done/Note are ignored.
// "text" is a *string so its ABSENCE (pure toggle) is distinguishable from an
// explicit empty string (400 — a criterion needs words).
type acToggleReq struct {
	Actor actorInput `json:"actor"`
	Done  bool       `json:"done"`
	Note  string     `json:"note"`
	Text  *string    `json:"text,omitempty"`
}

// acCreateReq is POST /tasks/{id}/acceptance-criteria. Text is the criterion's
// markdown wording and must be non-empty after trimming (else 400). The seq is
// minted server-side as max(seq)+1 — callers never choose it.
type acCreateReq struct {
	Actor actorInput `json:"actor"`
	Text  string     `json:"text"`
}

// acRemoveReq is the body of DELETE /tasks/{id}/acceptance-criteria/{seq}. The
// seq rides on the path; the body carries only the actor, which is REQUIRED —
// a removal appends an event that must record who removed it (400 when absent
// or invalid).
type acRemoveReq struct {
	Actor actorInput `json:"actor"`
}

// artifactReq is POST /tasks/{id}/artifacts. Kind must be one of
// log|screenshot|command-output|trace|url|other (else 400); Ref is the URL or
// filesystem path and must not be empty after trimming (else 400); Summary is
// an optional one-liner.
type artifactReq struct {
	Actor   actorInput `json:"actor"`
	Kind    string     `json:"kind"`
	Ref     string     `json:"ref"`
	Summary string     `json:"summary"`
}

// relateReq is POST /tasks/{id}/relations. Kind is any tool-boundary value —
// child|parent|blocked-by|duplicate-of|related (else 400); the 'parent' alias
// is normalized server-side to the inverse child edge. OtherID must name a
// task of the SAME project (cross-project → 400) and may not equal {id}
// (self-relation → 400).
type relateReq struct {
	Actor   actorInput `json:"actor"`
	OtherID string     `json:"other_id"`
	Kind    string     `json:"kind"`
}

// removeRelationReq is the body of DELETE /tasks/{id}/relations/{other_id}/
// {kind}. The edge itself is keyed by the path; the body carries only the
// actor, which is REQUIRED — a relation removal appends an event that must
// record who removed it (400 when absent or invalid).
type removeRelationReq struct {
	Actor actorInput `json:"actor"`
}

// --- realtime events ---------------------------------------------------------

// realtimeEventDTO is the compact payload streamed over SSE to subscribers.
// It carries task_id, kind, status, timestamp, and metadata so UI clients
// can immediately update their views or patch locally without full polling.
type realtimeEventDTO struct {
	EventID   string    `json:"event_id,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status,omitempty"`
	Actor     *actorDTO `json:"actor,omitempty"`
	Timestamp string    `json:"timestamp"`
}

func toRealtimeEventDTO(e domain.Event) realtimeEventDTO {
	dto := realtimeEventDTO{
		EventID:   string(e.ID),
		Kind:      string(e.Kind),
		Timestamp: rfc3339(e.At),
	}
	if e.ProjectID != nil {
		dto.ProjectID = string(*e.ProjectID)
	}
	if e.TaskID != nil {
		dto.TaskID = string(*e.TaskID)
	}
	if e.Actor.Name != "" || e.Actor.Type != "" {
		dto.Actor = toActor(&e.Actor)
	}
	dto.Status = extractEventStatus(e)
	return dto
}

func extractEventStatus(e domain.Event) string {
	if e.After != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(e.After), &m); err == nil {
			if s, ok := m["status"].(string); ok && s != "" {
				return s
			}
		}
	}
	switch e.Kind {
	case domain.EventClaimed:
		return "doing"
	case domain.EventReleased:
		return "todo"
	case domain.EventCreated:
		if e.TaskID != nil {
			return "todo"
		}
	}
	return ""
}

// openCodeStartResponse is POST /tasks/{id}/opencode/start response body.
type openCodeStartResponse struct {
	SessionID  string `json:"session_id"`
	SessionURL string `json:"session_url"`
}
