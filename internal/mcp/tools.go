// Package mcp adapts crowban's REST API to MCP tools (docs/ARCHITECTURE.md
// §6). It is imported only by cmd/crowban-mcp (and its tests) — never by
// cmd/crowban-server, which must stay free of the MCP SDK.
//
// Every tool handler follows ONE pattern:
//
//	decode flat args → resolve actor fallback → call the REST client →
//	  success: compact JSON text of the resource(s)
//	  failure: isError result whose text is the upstream error body VERBATIM —
//	  its "message" field IS the recovery instruction for the agent.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// Client is the REST surface the crowban MCP tools need. *apiclient.Client
// implements it; unit tests substitute fakes.
type Client interface {
	BaseURL() string
	ListProjects(ctx context.Context) (*apiclient.ProjectList, error)
	CreateProject(ctx context.Context, req apiclient.CreateProjectRequest) (*apiclient.Project, error)
	ListTasks(ctx context.Context, f apiclient.TaskFilter) (*apiclient.TaskList, error)
	GetTask(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error)
	CreateTask(ctx context.Context, req apiclient.CreateTaskRequest) (*apiclient.CreateTaskResponse, error)
	UpdateTask(ctx context.Context, id string, req apiclient.UpdateTaskRequest) (*apiclient.TaskSummary, error)
	ClaimTask(ctx context.Context, id string, actor apiclient.Actor, ttlSec int) (*apiclient.TaskSummary, error)
	ReleaseTask(ctx context.Context, id string, actor apiclient.Actor, note string) (*apiclient.TaskSummary, error)
	HeartbeatTask(ctx context.Context, id string, actor apiclient.Actor) (*apiclient.TaskSummary, error)
	AddComment(ctx context.Context, id string, req apiclient.CommentRequest) (*apiclient.Comment, error)
	UpdateNotes(ctx context.Context, id string, actor apiclient.Actor, notes string) (*apiclient.TaskSummary, error)
	CompleteAC(ctx context.Context, id string, seq int, req apiclient.ACToggleRequest) (*apiclient.AcceptanceCriterion, error)
	AttachArtifact(ctx context.Context, id string, req apiclient.ArtifactRequest) (*apiclient.Artifact, error)
	RelateTasks(ctx context.Context, id string, req apiclient.RelationRequest) (*apiclient.Relation, error)
	UnrelateTasks(ctx context.Context, id string, actor apiclient.Actor, otherID, kind string) error
}

// ServerOptions are the options every crowban MCPServer runs with:
//   - WithRecovery: a panic in a handler becomes a JSON-RPC error, not a
//     container crash.
//   - WithInputSchemaValidation: arguments that violate a tool's input schema
//     (small local models do send malformed args) come back as an isError tool
//     result with the validation details — so the model can self-correct.
func ServerOptions() []mcpsrv.ServerOption {
	return []mcpsrv.ServerOption{
		mcpsrv.WithRecovery(),
		mcpsrv.WithInputSchemaValidation(),
	}
}

// RegisterTools wires every crowban MCP tool onto s (the full 14-tool surface,
// docs/API.md route table). defaultActor is the container-default identity
// (from CROWBAN_AGENT_NAME) used whenever a mutating call arrives without a
// usable `actor` object.
func RegisterTools(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	registerListProjects(s, c)
	registerCreateProject(s, c, defaultActor)
	registerListTasks(s, c)
	registerGetTask(s, c)
	registerCreateTask(s, c, defaultActor)
	registerUpdateTask(s, c, defaultActor)
	registerClaimTask(s, c, defaultActor)
	registerReleaseTask(s, c, defaultActor)
	registerHeartbeatTask(s, c, defaultActor)
	registerAddComment(s, c, defaultActor)
	registerUpdateNotes(s, c, defaultActor)
	registerCompleteAC(s, c, defaultActor)
	registerAttachArtifact(s, c, defaultActor)
	registerRelateTasks(s, c, defaultActor)
}

// --- shared helpers -------------------------------------------------------------------

// textResult marshals v as compact JSON (no indentation — context economy for
// small models) and wraps it in a success result.
func textResult(v any) (*mcpgo.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcpgo.NewToolResultError("internal: cannot encode crowban response: " + err.Error()), nil
	}
	return mcpgo.NewToolResultText(string(data)), nil
}

// fail maps a REST error to an isError result. Upstream API errors surface the
// upstream body VERBATIM (its "message" field is written as the recovery hint);
// transport failures get the documented unreachable message instead.
func fail(c Client, err error) *mcpgo.CallToolResult {
	if e, ok := apiclient.AsAPIError(err); ok {
		return mcpgo.NewToolResultError(string(e.Body))
	}
	return mcpgo.NewToolResultError(fmt.Sprintf(
		"crowban-server unreachable at %s — is it running? (%v)", c.BaseURL(), err))
}

// invalidInput builds the same local isError body shape a server-side 400
// would use, for required arguments we can reject before any round-trip.
func invalidInput(tool, arg string) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultError(fmt.Sprintf(
		`{"error":"invalid input","message":"%s: missing or empty required argument %q"}`, tool, arg))
}

// actorShapedProp builds the shared {name, type} object property used by the
// optional `actor` and `assignee` arguments.
func actorShapedProp(propName, nameDesc string) mcpgo.ToolOption {
	return mcpgo.WithObject(propName, mcpgo.Properties(map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": nameDesc,
		},
		"type": map[string]any{
			"type":        "string",
			"enum":        []string{"human", "agent"},
			"description": "\"human\" or \"agent\"; the server defaults to \"human\" when omitted. Agents should send \"agent\" explicitly.",
		},
	}))
}

// actorProp is the shared optional `actor` object property of every mutating
// tool: {name (string, required when present), type ("human"|"agent")}.
func actorProp() mcpgo.ToolOption {
	return actorShapedProp("actor", "Who is acting. Claims and attribution key on this NAME — pass a distinct name per agent so claims are correct.")
}

// assigneeProp is the optional durable-ownership object of create_task and
// update_task (distinct from the ephemeral claim lease).
func assigneeProp() mcpgo.ToolOption {
	return actorShapedProp("assignee", "DURABLE ownership of the task (not the ephemeral claim lease). Omit to leave unassigned.")
}

// actorHint is appended to every mutating tool's description: the actor
// convention and its fallback rule, written for small models.
const actorHint = " Actor: pass an optional object {name, type} — agents SHOULD pass a distinct name (type \"agent\") so claims and attribution are correct, because claims key on the actor NAME; when omitted (or empty), the container default agent name from CROWBAN_AGENT_NAME is used."

// resolveActor applies the actor convention to decoded arguments: use the
// caller's `actor` object when it carries a non-empty name (type passes
// through as-is — the server validates and defaults it); otherwise fall back
// to the container default. A present-but-malformed actor (wrong shape, empty
// name) also falls back rather than 400-ing: small models recover more cheaply
// from a working call than from another error round-trip.
func resolveActor(args map[string]any, def apiclient.Actor) apiclient.Actor {
	raw, ok := args["actor"]
	if !ok {
		return def
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return def
	}
	name, _ := obj["name"].(string)
	if strings.TrimSpace(name) == "" {
		return def
	}
	typ, _ := obj["type"].(string)
	return apiclient.Actor{Name: name, Type: typ}
}

// optionalActor decodes an OPTIONAL actor-shaped object argument (e.g.
// create_task's `assignee`): nil when absent or nameless. Unlike resolveActor
// there is NO fallback — an omitted field means "leave it unset", and the
// container default must never silently become a task's owner.
func optionalActor(args map[string]any, key string) *apiclient.Actor {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	name, _ := obj["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil
	}
	typ, _ := obj["type"].(string)
	a := apiclient.Actor{Name: name, Type: typ}
	return &a
}

// toInt converts a decoded JSON argument value (float64/int from number args,
// or numeric string) to an int. Schema validation already guarantees the
// property is present and numeric; this is decode step only.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

// --- the 14 tools -----------------------------------------------------------------------

func registerListProjects(s *mcpsrv.MCPServer, c Client) {
	tool := mcpgo.NewTool("list_projects",
		mcpgo.WithDescription("List all projects, newest first, each with id, name, description and per-status open task counts (todo/doing/done; zero when empty). Returns compact JSON: {\"projects\":[...],\"total\":N}. Use this to find a project_id before listing or creating tasks."),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		list, err := c.ListProjects(ctx)
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(list)
	})
}

func registerCreateProject(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("create_project",
		mcpgo.WithDescription(
			"Create a new project. Returns the fresh project row as compact JSON (id, name, description, timestamps). Name is required; an empty name or other bad input surfaces as an invalid-input error verbatim."+actorHint),
		mcpgo.WithString("name", mcpgo.Required(),
			mcpgo.Description("Project name (required, non-empty).")),
		mcpgo.WithString("description",
			mcpgo.Description("Optional markdown description of the project's scope.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		name := strings.TrimSpace(req.GetString("name", ""))
		if name == "" {
			return invalidInput("create_project", "name"), nil
		}
		row, err := c.CreateProject(ctx, apiclient.CreateProjectRequest{
			Actor:       resolveActor(req.GetArguments(), defaultActor),
			Name:        name,
			Description: strings.TrimSpace(req.GetString("description", "")),
		})
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerListTasks(s *mcpsrv.MCPServer, c Client) {
	tool := mcpgo.NewTool("list_tasks",
		mcpgo.WithDescription("The \"what should I work on?\" call: task summary rows in canonical order (priority DESC → due ASC with undated last → created ASC) inside the pagination envelope {\"tasks\":[...],\"total\":N,\"limit\":N,\"offset\":N}. Every argument is an OPTIONAL filter — combine them to narrow (e.g. project_id + unblocked, or search). Rows are summaries only: pick one and call get_task for its full resume context."),
		mcpgo.WithString("project_id",
			mcpgo.Description("Restrict to one project (ULID from list_projects).")),
		mcpgo.WithString("status", mcpgo.Enum("todo", "doing", "done"),
			mcpgo.Description("Exact status match.")),
		mcpgo.WithString("assignee_name",
			mcpgo.Description("Durable assignee NAME to filter by (not the ephemeral claim holder).")),
		mcpgo.WithBoolean("unblocked",
			mcpgo.Description("true = keep only tasks with no open blocker — the ready-to-work set.")),
		mcpgo.WithString("search",
			mcpgo.Description("Case-insensitive substring over title+description+notes.")),
		mcpgo.WithNumber("priority", mcpgo.Min(0), mcpgo.Max(5),
			mcpgo.Description("Exact priority match (0–5).")),
		mcpgo.WithString("due_before",
			mcpgo.Description("RFC3339; due_at strictly before this (undated tasks never match).")),
		mcpgo.WithString("due_after",
			mcpgo.Description("RFC3339; due_at at or after this (inclusive).")),
		mcpgo.WithString("completed_since",
			mcpgo.Description("RFC3339; only tasks completed at/after this time.")),
		mcpgo.WithNumber("limit",
			mcpgo.Description("Page size: server default 20, max 100 (clamped).")),
		mcpgo.WithNumber("offset",
			mcpgo.Description("Pagination offset (default 0).")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		args := req.GetArguments()
		var f apiclient.TaskFilter
		f.ProjectID = strings.TrimSpace(req.GetString("project_id", ""))
		f.Status = strings.TrimSpace(req.GetString("status", ""))
		f.AssigneeName = strings.TrimSpace(req.GetString("assignee_name", ""))
		f.Unblocked = req.GetBool("unblocked", false)
		f.Search = strings.TrimSpace(req.GetString("search", ""))
		if v, ok := args["priority"]; ok {
			p := toInt(v)
			f.Priority = &p
		}
		f.DueBefore = strings.TrimSpace(req.GetString("due_before", ""))
		f.DueAfter = strings.TrimSpace(req.GetString("due_after", ""))
		f.CompletedAfter = strings.TrimSpace(req.GetString("completed_since", ""))
		f.Limit = req.GetInt("limit", 0)
		f.Offset = req.GetInt("offset", 0)
		list, err := c.ListTasks(ctx, f)
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(list)
	})
}

func registerGetTask(s *mcpsrv.MCPServer, c Client) {
	tool := mcpgo.NewTool("get_task",
		mcpgo.WithDescription("Get one task's full resume payload in a single call: every field (including the current version — use it for update_task), notes, acceptance criteria, tail of recent comments, blockers and relations, artifacts, and recent events. Optional tails: comment_limit (default 10, max 50) and event_limit (default 20, max 100); larger values are clamped server-side. Unknown id → isError not-found naming the id; re-check ids with list_projects/list_tasks."),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID, e.g. 01J3ZK8WQ9X7M2P4R6S8T0U2V4.")),
		mcpgo.WithNumber("comment_limit",
			mcpgo.Description("How many recent comments to include (default 10, max 50; omitted = default).")),
		mcpgo.WithNumber("event_limit",
			mcpgo.Description("How many recent events to include (default 20, max 100; omitted = default).")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("get_task", "id"), nil
		}
		detail, err := c.GetTask(ctx, id, req.GetInt("comment_limit", 0), req.GetInt("event_limit", 0))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(detail)
	})
}

func registerCreateTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("create_task",
		mcpgo.WithDescription(
			"Create a task in a project, optionally with priority (0–5), RFC3339 due date, durable assignee, and initial acceptance criteria (an array of strings that become seq 1..n in one transaction with the task). Returns {\"task\": fresh summary row at version 1, \"possible_duplicates\": [...]}. possible_duplicates is ADVISORY only — never an error; if it lists a near-duplicate, do NOT create another task; work on or relate to the existing one instead. Unknown project_id → invalid-input error verbatim."+actorHint),
		mcpgo.WithString("title", mcpgo.Required(),
			mcpgo.Description("Task title (required, non-empty).")),
		mcpgo.WithString("description",
			mcpgo.Description("Optional markdown description.")),
		mcpgo.WithString("project_id", mcpgo.Required(),
			mcpgo.Description("Project ULID the task belongs to (from list_projects; must exist).")),
		mcpgo.WithNumber("priority", mcpgo.Min(0), mcpgo.Max(5),
			mcpgo.Description("Priority 0–5 (default 0 when omitted).")),
		mcpgo.WithString("due_at",
			mcpgo.Description("RFC3339 due date, e.g. 2026-10-01T00:00:00Z.")),
		assigneeProp(),
		mcpgo.WithArray("acceptance_criteria", mcpgo.WithStringItems(),
			mcpgo.Description("Initial acceptance criteria as an array of strings; they become seq 1..n.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		title := strings.TrimSpace(req.GetString("title", ""))
		projectID := strings.TrimSpace(req.GetString("project_id", ""))
		if title == "" {
			return invalidInput("create_task", "title"), nil
		}
		if projectID == "" {
			return invalidInput("create_task", "project_id"), nil
		}
		args := req.GetArguments()
		body := apiclient.CreateTaskRequest{
			Actor:       resolveActor(args, defaultActor),
			Title:       title,
			Description: strings.TrimSpace(req.GetString("description", "")),
			ProjectID:   projectID,
		}
		if v, ok := args["priority"]; ok {
			p := toInt(v)
			body.Priority = p
		}
		if d := strings.TrimSpace(req.GetString("due_at", "")); d != "" {
			body.DueAt = &d
		}
		body.Assignee = optionalActor(args, "assignee")
		if v, ok := args["acceptance_criteria"]; ok {
			if items, ok := v.([]any); ok {
				for _, it := range items {
					if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
						body.AcceptanceCriteria = append(body.AcceptanceCriteria, s)
					}
				}
			}
		}
		resp, err := c.CreateTask(ctx, body)
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(resp)
	})
}

func registerUpdateTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("update_task",
		mcpgo.WithDescription(
			"Version-guarded partial update of one task: any of title, description, status (todo|doing|done, validated against allowed transitions), priority (0–5), due_at (RFC3339), clear_due (boolean), assignee, clear_assignee (boolean), archived — omitted fields are unchanged. version is REQUIRED and must be the stored version you last read (fresh tasks are version 1; every mutating result echoes the new version). A STALE version returns isError with the 409 body VERBATIM — it carries current_version plus a fresh \"current\" row, so retry your update with that version WITHOUT re-reading. Returns the fresh summary row (version bumped)."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID to update.")),
		mcpgo.WithInteger("version", mcpgo.Required(),
			mcpgo.Description("The stored version you last read (optimistic-concurrency guard). Stale → 409 body verbatim with the fresh row to rebase on.")),
		mcpgo.WithString("title",
			mcpgo.Description("New title.")),
		mcpgo.WithString("description",
			mcpgo.Description("New markdown description.")),
		mcpgo.WithString("status", mcpgo.Enum("todo", "doing", "done"),
			mcpgo.Description("New status (validated against the transition map; an illegal move → invalid-input error).")),
		mcpgo.WithNumber("priority", mcpgo.Min(0), mcpgo.Max(5),
			mcpgo.Description("New priority 0–5.")),
		mcpgo.WithString("due_at",
			mcpgo.Description("RFC3339 due date to set. An empty string is a no-op that keeps the current deadline — use clear_due to remove it.")),
		mcpgo.WithBoolean("clear_due",
			mcpgo.Description("true = clear the task's deadline (due_at becomes null). Takes precedence over due_at if both are given in the same call.")),
		assigneeProp(),
		mcpgo.WithBoolean("clear_assignee",
			mcpgo.Description("true = remove durable assignee (task becomes unassigned). Takes precedence over assignee if both are given in the same call.")),
		mcpgo.WithBoolean("archived",
			mcpgo.Description("Archive (true) or unarchive (false) the task.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("update_task", "id"), nil
		}
		version := req.GetInt("version", 0)
		if version <= 0 {
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"update_task: \"version\" is required — read the current version with get_task first"}`), nil
		}
		args := req.GetArguments()
		body := apiclient.UpdateTaskRequest{
			Actor:   resolveActor(args, defaultActor),
			Version: version,
		}
		if v := strings.TrimSpace(req.GetString("title", "")); v != "" {
			s := v
			body.Title = &s
		}
		if v := strings.TrimSpace(req.GetString("description", "")); v != "" {
			s := v
			body.Description = &s
		}
		if v := strings.TrimSpace(req.GetString("status", "")); v != "" {
			s := v
			body.Status = &s
		}
		if v, ok := args["priority"]; ok {
			p := toInt(v)
			body.Priority = &p
		}
		if v := strings.TrimSpace(req.GetString("due_at", "")); v != "" {
			s := v
			body.DueAt = &s
		}
		body.ClearDue = req.GetBool("clear_due", false)
		body.Assignee = optionalActor(args, "assignee")
		body.ClearAssignee = req.GetBool("clear_assignee", false)
		if v, ok := args["archived"]; ok {
			b, _ := v.(bool)
			body.Archived = &b
		}
		row, err := c.UpdateTask(ctx, id, body)
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerClaimTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("claim_task",
		mcpgo.WithDescription(
			"Atomically take a TTL lease on a task and move it to doing. Retry-safe: if YOU already hold the active lease, re-claiming just refreshes it. If ANOTHER actor holds the active lease the result is isError with the conflict body verbatim — it names the holder and claim_expires_at; wait for the expiry or work another task. ttl_sec is optional (default 900, max 3600, clamped server-side; negative → error)."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID to claim.")),
		actorProp(),
		mcpgo.WithNumber("ttl_sec",
			mcpgo.Description("Lease length in seconds (default 900 when omitted; max 3600, clamped).")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("claim_task", "id"), nil
		}
		actor := resolveActor(req.GetArguments(), defaultActor)
		row, err := c.ClaimTask(ctx, id, actor, req.GetInt("ttl_sec", 0))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerReleaseTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("release_task",
		mcpgo.WithDescription(
			"Give up YOUR claim lease on a task; its status returns to todo and the fresh summary row is returned. Optional note explains why (it rides on the released event — it is NOT posted as a comment). Holder-only: if another actor holds the active lease you get isError with the 409 claim-conflict body verbatim naming the real holder and expiry; releasing a task with no lease at all is an idempotent no-op (retry-safe)."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID whose lease you hold.")),
		mcpgo.WithString("note",
			mcpgo.Description("Optional one-liner on why you are releasing (e.g. \"stalled on the DB migration\").")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("release_task", "id"), nil
		}
		row, err := c.ReleaseTask(ctx, id, resolveActor(req.GetArguments(), defaultActor), strings.TrimSpace(req.GetString("note", "")))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerHeartbeatTask(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("heartbeat_task",
		mcpgo.WithDescription(
			"Keep-alive for YOUR active claim lease during long silent work — call it regularly while working so the lease does not expire mid-task. Deliberately bumps NO version and touches NO updated_at (a heartbeat never invalidates a working agent's read base). The returned fresh summary row doubles as a health/state check: it shows status, claimed_by, and the new claim_expires_at. If you are not the holder, or the task has no lease at all → isError with the 409 claim-conflict body verbatim (an empty holder.type means \"claim_task first\")."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID whose lease you hold.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("heartbeat_task", "id"), nil
		}
		row, err := c.HeartbeatTask(ctx, id, resolveActor(req.GetArguments(), defaultActor))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerAddComment(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("add_comment",
		mcpgo.WithDescription(
			"Append a typed markdown comment to the task thread (a child-row mutation: NO task version bump). kind: progress | question | decision | handoff | note — use \"question\" to signal that you need HUMAN INPUT, and \"handoff\" when stepping away from the task. body is required markdown (non-empty after trimming). Returns the created comment row (201). Appends are NOT idempotent: after a timeout error, check the comment tail via get_task before re-posting or you may duplicate."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID to comment on.")),
		mcpgo.WithString("kind", mcpgo.Required(), mcpgo.Enum("progress", "question", "decision", "handoff", "note"),
			mcpgo.Description("Comment kind. \"question\" = needs human input; \"handoff\" = stepping away with context for the next agent.")),
		mcpgo.WithString("body", mcpgo.Required(),
			mcpgo.Description("Markdown body (required, non-empty).")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		body := strings.TrimSpace(req.GetString("body", ""))
		if id == "" {
			return invalidInput("add_comment", "id"), nil
		}
		if body == "" {
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"add_comment: \"body\" must not be empty (markdown)"}`), nil
		}
		comment, err := c.AddComment(ctx, id, apiclient.CommentRequest{
			Actor: resolveActor(req.GetArguments(), defaultActor),
			Kind:  strings.TrimSpace(req.GetString("kind", "")),
			Body:  body,
		})
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(comment)
	})
}

func registerUpdateNotes(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("update_notes",
		mcpgo.WithDescription(
			"Replace the task's DURABLE resume-context blob in WHOLE — the \"write down what you found / what's next\" call, so a fresh session can pick up where you stopped. This is REPLACE-ON-WRITE: send the complete new notes, not an append. There is deliberately NO version field (single-writer scratchpad): identical content is a no-op; a genuinely concurrent write can surface as 409 version_conflict — handle it like any 409 (re-read with get_task, then rewrite). Returns the fresh summary row; read notes back via get_task."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID whose notes to replace.")),
		mcpgo.WithString("notes", mcpgo.Required(),
			mcpgo.Description("Complete new markdown notes blob (replace-on-write): findings, state, and the concrete next step.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("update_notes", "id"), nil
		}
		args := req.GetArguments()
		// Presence, not non-emptiness: an intentional empty blob clears the
		// scratchpad (the server imposes no empty check on notes).
		nstr, ok := args["notes"].(string)
		if !ok {
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"update_notes: \"notes\" is required (markdown)"}`), nil
		}
		row, err := c.UpdateNotes(ctx, id, resolveActor(args, defaultActor), nstr)
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerCompleteAC(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("complete_ac",
		mcpgo.WithDescription(
			"Check or uncheck ONE acceptance criterion by its 1-based seq (from get_task's acceptance_criteria). done=true records completed_by/completed_at; done=false clears both (uncheck is free). Optional note. A child-row mutation: NO task version bump. Returns the updated AC row. Unknown task OR unknown seq → isError not-found verbatim."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID owning the criterion.")),
		mcpgo.WithInteger("seq", mcpgo.Required(), mcpgo.Min(1),
			mcpgo.Description("1-based acceptance-criterion sequence (acceptance_criteria[].seq).")),
		mcpgo.WithBoolean("done", mcpgo.Required(),
			mcpgo.Description("true = check it off; false = uncheck (clears completed_by/completed_at).")),
		mcpgo.WithString("note",
			mcpgo.Description("Optional note, e.g. how the criterion was verified.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		if id == "" {
			return invalidInput("complete_ac", "id"), nil
		}
		seq := req.GetInt("seq", 0)
		if seq < 1 {
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"complete_ac: \"seq\" is required and must be >= 1 (see acceptance_criteria in get_task)"}`), nil
		}
		row, err := c.CompleteAC(ctx, id, seq, apiclient.ACToggleRequest{
			Actor: resolveActor(req.GetArguments(), defaultActor),
			Done:  req.GetBool("done", false),
			Note:  strings.TrimSpace(req.GetString("note", "")),
		})
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerAttachArtifact(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("attach_artifact",
		mcpgo.WithDescription(
			"Attach a typed piece of evidence to a task: kind log | screenshot | command-output | trace | url | other; ref is a URL or filesystem path (required); summary is an optional one-line description. Returns the created artifact row (201). Append-only and NOT idempotent — after an error, check artifacts via get_task before re-attaching or you may duplicate. RECOMMENDED for verification: when your work includes running tests or verification commands, attach that evidence (kind \"command-output\", \"log\" or \"screenshot\") so a human can verify it — encouraged, never required."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID to attach the artifact to.")),
		mcpgo.WithString("kind", mcpgo.Required(), mcpgo.Enum("log", "screenshot", "command-output", "trace", "url", "other"),
			mcpgo.Description("Artifact kind.")),
		mcpgo.WithString("ref", mcpgo.Required(),
			mcpgo.Description("URL or filesystem path of the evidence (required, non-empty).")),
		mcpgo.WithString("summary",
			mcpgo.Description("Optional one-line description of what the artifact shows.")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		ref := strings.TrimSpace(req.GetString("ref", ""))
		if id == "" {
			return invalidInput("attach_artifact", "id"), nil
		}
		if ref == "" {
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"attach_artifact: \"ref\" must be a non-empty URL or path"}`), nil
		}
		row, err := c.AttachArtifact(ctx, id, apiclient.ArtifactRequest{
			Actor:   resolveActor(req.GetArguments(), defaultActor),
			Kind:    strings.TrimSpace(req.GetString("kind", "")),
			Ref:     ref,
			Summary: strings.TrimSpace(req.GetString("summary", "")),
		})
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
	})
}

func registerRelateTasks(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	tool := mcpgo.NewTool("relate_tasks",
		mcpgo.WithDescription(
			"Add or REMOVE a relation edge between two tasks in the SAME project. kind: child | parent | blocked-by | duplicate-of | related; action \"add\" (default) or \"remove\". For add: returns the stored relation row — note that \"parent\" is normalized server-side to the inverse \"child\" edge, so the returned kind is always the STORED kind; re-adding an existing identical edge is an idempotent no-op (200); a second DIFFERENT parent → isError with the 409 parent-conflict body verbatim (a task has at most one parent). For remove: returns {\"removed\":true,...} (the server answers 204 No Content); a missing edge → isError not-found verbatim. Both ends must exist and may not be the same task."+actorHint),
		mcpgo.WithString("id", mcpgo.Required(),
			mcpgo.Description("Task ULID the edge is filed under.")),
		mcpgo.WithString("other_id", mcpgo.Required(),
			mcpgo.Description("The other task's ULID; must exist in the same project and differ from id.")),
		mcpgo.WithString("kind", mcpgo.Required(), mcpgo.Enum("child", "parent", "blocked-by", "duplicate-of", "related"),
			mcpgo.Description("Relation kind. \"parent\" = \"{id} is a parent of {other_id}\" (stored as the inverse child edge).")),
		mcpgo.WithString("action", mcpgo.Enum("add", "remove"),
			mcpgo.Description("\"add\" (default) or \"remove\".")),
		actorProp(),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := strings.TrimSpace(req.GetString("id", ""))
		otherID := strings.TrimSpace(req.GetString("other_id", ""))
		kind := strings.TrimSpace(req.GetString("kind", ""))
		if id == "" {
			return invalidInput("relate_tasks", "id"), nil
		}
		if otherID == "" {
			return invalidInput("relate_tasks", "other_id"), nil
		}
		if kind == "" {
			return invalidInput("relate_tasks", "kind"), nil
		}
		action := strings.ToLower(strings.TrimSpace(req.GetString("action", "add")))
		if action == "" {
			action = "add"
		}
		actor := resolveActor(req.GetArguments(), defaultActor)
		switch action {
		case "add":
			rel, err := c.RelateTasks(ctx, id, apiclient.RelationRequest{Actor: actor, OtherID: otherID, Kind: kind})
			if err != nil {
				return fail(c, err), nil
			}
			return textResult(rel)
		case "remove":
			if err := c.UnrelateTasks(ctx, id, actor, otherID, kind); err != nil {
				return fail(c, err), nil
			}
			return textResult(map[string]any{ // upstream answers 204 No Content; confirm the removed edge
				"removed":       true,
				"task_id":       id,
				"other_task_id": otherID,
				"kind":          kind,
			})
		default:
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"relate_tasks: \"action\" must be \"add\" or \"remove\""}`), nil
		}
	})
}

// --- /healthz proxy ---------------------------------------------------------------------

// UpstreamHealth is the subset of *apiclient.Client the MCP container's
// /healthz needs. It exists so the handler is testable without a full client.
type UpstreamHealth interface {
	BaseURL() string
	Health(ctx context.Context) (*apiclient.Health, error)
}

// HealthzHandler implements GET /healthz for the MCP container: it proxies the
// main server's /healthz so docker `depends_on: service_healthy` chains through
// crowban-mcp. 200 only when upstream is healthy; an upstream 503 (degraded DB)
// is re-emitted verbatim with its status; an unreachable upstream maps to a
// fresh 503 degraded body (never 200 — that would let compose start dependents
// against a dead backend).
func HealthzHandler(up UpstreamHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, err := up.Health(r.Context())
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, h)
		default:
			if e, ok := apiclient.AsAPIError(err); ok { // upstream answered (e.g. 503 degraded) → proxy verbatim
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(e.StatusCode)
				_, _ = w.Write(e.Body)
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, apiclient.Health{
				Status: "degraded",
				DB:     "error: crowban-server unreachable at " + up.BaseURL(),
			})
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
