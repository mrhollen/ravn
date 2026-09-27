// Package mcp adapts crowban's REST API to MCP tools (docs/ARCHITECTURE.md
// §6). It is imported only by cmd/crowban-mcp (and its tests) — never by
// cmd/crowban-server, which must stay free of the MCP SDK.
//
// Every tool handler follows ONE pattern (slice 4a establishes it with
// list_projects / get_task / claim_task; slice 4b adds the remaining eleven of
// the 14-tool surface):
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
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
)

// Client is the REST surface the crowban MCP tools need. *apiclient.Client
// implements it; unit tests substitute fakes. It grows as slice 4b adds tools.
type Client interface {
	BaseURL() string
	ListProjects(ctx context.Context) (*apiclient.ProjectList, error)
	GetTask(ctx context.Context, id string, commentLimit, eventLimit int) (*apiclient.TaskDetail, error)
	ClaimTask(ctx context.Context, id string, actor apiclient.Actor, ttlSec int) (*apiclient.TaskSummary, error)
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

// RegisterTools wires every crowban MCP tool onto s.
// defaultActor is the container-default identity (from CROWBAN_AGENT_NAME) used
// whenever a mutating call arrives without an usable `actor` object.
func RegisterTools(s *mcpsrv.MCPServer, c Client, defaultActor apiclient.Actor) {
	registerListProjects(s, c)
	registerGetTask(s, c)
	registerClaimTask(s, c, defaultActor)
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

// actorProp is the shared optional `actor` object property of every mutating
// tool: {name (string, required when present), type ("human"|"agent")}.
func actorProp() mcpgo.ToolOption {
	return mcpgo.WithObject("actor", mcpgo.Properties(map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "Who is acting. Claims and attribution key on this NAME — pass a distinct name per agent so claims are correct.",
		},
		"type": map[string]any{
			"type":        "string",
			"enum":        []string{"human", "agent"},
			"description": "\"human\" or \"agent\"; the server defaults to \"human\" when omitted. Agents should send \"agent\" explicitly.",
		},
	}))
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

// --- the three pattern tools -----------------------------------------------------------

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
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"get_task: missing required argument \"id\""}`), nil
		}
		detail, err := c.GetTask(ctx, id, req.GetInt("comment_limit", 0), req.GetInt("event_limit", 0))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(detail)
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
			return mcpgo.NewToolResultError(`{"error":"invalid input","message":"claim_task: missing required argument \"id\""}`), nil
		}
		actor := resolveActor(req.GetArguments(), defaultActor)
		row, err := c.ClaimTask(ctx, id, actor, req.GetInt("ttl_sec", 0))
		if err != nil {
			return fail(c, err), nil
		}
		return textResult(row)
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
