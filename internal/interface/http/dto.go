package httpapi

import (
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

type commentDTO struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Body      string   `json:"body"`
	Actor     actorDTO `json:"actor"`
	CreatedAt string   `json:"created_at"`
}

type artifactDTO struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Ref       string   `json:"ref"`
	Summary   string   `json:"summary,omitempty"`
	Actor     actorDTO `json:"actor"`
	CreatedAt string   `json:"created_at"`
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
		dto := acDTO{Seq: c.Seq, Text: c.Text, Done: c.Done, Note: c.Note, CompletedBy: toActor(c.CompletedBy)}
		if c.CompletedAt != nil {
			v := rfc3339(*c.CompletedAt)
			dto.CompletedAt = &v
		}
		out.AcceptanceCriteria = append(out.AcceptanceCriteria, dto)
	}

	out.Comments = make([]commentDTO, 0, len(d.Comments))
	for _, c := range d.Comments {
		out.Comments = append(out.Comments, commentDTO{
			ID:        c.ID.String(),
			Kind:      string(c.Kind),
			Body:      c.Body,
			Actor:     actorDTO{Type: string(c.Actor.Type), Name: c.Actor.Name},
			CreatedAt: rfc3339(c.CreatedAt),
		})
	}

	out.Artifacts = make([]artifactDTO, 0, len(d.Artifacts))
	for _, a := range d.Artifacts {
		out.Artifacts = append(out.Artifacts, artifactDTO{
			ID:        a.ID.String(),
			Kind:      string(a.Kind),
			Ref:       a.Ref,
			Summary:   a.Summary,
			Actor:     actorDTO{Type: string(a.Actor.Type), Name: a.Actor.Name},
			CreatedAt: rfc3339(a.CreatedAt),
		})
	}

	out.Relations = make([]relationDTO, 0, len(d.Relations))
	for _, r := range d.Relations {
		out.Relations = append(out.Relations, relationDTO{
			TaskID:      r.TaskID.String(),
			OtherTaskID: r.OtherTaskID.String(),
			Kind:        string(r.Kind),
			CreatedAt:   rfc3339(r.CreatedAt),
		})
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toProject(p domain.Project) projectDTO {
	return projectDTO{
		ID:          p.ID.String(),
		Name:        p.Name,
		Description: p.Description,
		CreatedAt:   rfc3339(p.CreatedAt),
		UpdatedAt:   rfc3339(p.UpdatedAt),
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
	Actor       actorInput `json:"actor"`
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
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

	Title         *string     `json:"title"`
	Description   *string     `json:"description"`
	Status        *string     `json:"status"`
	Priority      *int        `json:"priority"`
	DueAt         *string     `json:"due_at"`
	ClearDue      bool        `json:"clear_due"`
	Assignee      *actorInput `json:"assignee"`
	ClearAssignee bool        `json:"clear_assignee"`
	Archived      *bool       `json:"archived"`
}
