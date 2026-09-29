# crowban MCP — agent onboarding reference

This is the reference an **MCP client (or the agent behind one)** needs to work
on a crowban board. The full wire contract lives in [API.md](API.md); this doc
covers how to connect, the actor convention, all 14 tools with their exact
arguments, plus the workflow prompts and read-only resources.

## What it is

`crowban-mcp` is a **stateless Streamable-HTTP MCP server** that adapts the
crowban REST API into MCP tools. Primary endpoint:

```
http://<host>:9311/mcp
```

Stateless means every request is self-contained: there are no sessions to
resume, so the client can be restarted or load-balanced freely. The same host
also serves `GET /healthz` (upstream liveness; used by compose healthchecks).

For clients that only speak the legacy **SSE transport** (they open the session
with an HTTP GET to an SSE endpoint):

```
http://<host>:9311/sse
```

`/sse` opens the legacy SSE event stream (its `endpoint` event advertises `/message?sessionId=…`
for the JSON-RPC POSTs, with session ID fallback if sent in headers).
Streamable HTTP clients (including Antigravity and modern MCP clients) connect directly to:

```
http://<host>:9311/mcp
```

`/mcp` natively handles both Streamable HTTP POST calls and GET SSE notification streams.

## Pointing an MCP client at it

The server container's configuration (env vars):

| Variable | Default | Meaning |
|---|---|---|
| `CROWBAN_API_URL` | `http://crowban-server:8080` | Base URL of the crowban-server instance to proxy. |
| `CROWBAN_TOKEN` | *(unset)* | When set, the MCP server requires it from its own clients (`Authorization: Bearer <token>` or `?token=<token>`) on `/mcp`, `/sse`, and `/message`, and forwards it on every upstream request. Unset = no auth (the single-tenant LAN default). |
| `CROWBAN_AGENT_NAME` | `agent` | The container-default actor NAME used when a call arrives without an `actor` argument (see below). Set it to this agent's own name. |
| `MCP_ADDR` | `:9080` | Listen address of the MCP HTTP server. |

Example MCP client config (Streamable-HTTP transport):

```json
{
  "mcpServers": {
    "crowban": {
      "type": "http",
      "url": "http://192.168.1.50:9311/mcp"
    }
  }
}
```

(Include the `/mcp` path in the URL. Healthcheck: `curl http://192.168.1.50:9311/healthz`. Replace 9311 with your `CROWBAN_MCP_PORT` if you overrode it. When `CROWBAN_TOKEN` is set, add an `Authorization: Bearer <token>` header to the client config — legacy SSE clients can instead pass `?token=<token>` on `/sse`.)

## The actor convention

Every mutating tool accepts an optional `actor` object:

```json
"actor": { "name": "runner-2", "type": "agent" }
```

- **`name` is the identity.** Claims, leases, and event attribution all key on
  this name — any non-empty string works. **Pass a distinct name per agent**
  (e.g. `runner-2`, `planner`, `ada`), otherwise two agents will collide on
  claim ownership ("who holds this task?").
- Agents should send `"type": "agent"` explicitly; the server defaults omitted
  types to `"human"`.
- When `actor` is omitted (or empty/malformed), the tool falls back to the
  container default from `CROWBAN_AGENT_NAME`. A call that works with the
  fallback still attributes to that shared name — pass your own name whenever
  you can.

## Tools (14)

All tools return **compact JSON text** on success. All failures come back as an
`isError` tool result whose text is the upstream error body **verbatim** — its
`message` field is written to be your recovery instruction. See
[Reading errors](#reading-error-bodies).

| Tool | Purpose | Key arguments | Returns |
|---|---|---|---|
| `list_projects` | All projects newest-first with per-status open task counts. | *(none)* | `{"projects":[{id, name, description, …, task_counts}], "total":N}` |
| `create_project` | Create a project. | `name` (required), `description?` (markdown), `actor?` | Fresh project row (`id`, `name`, …) |
| `list_tasks` | **The "what should I work on?" call.** Filtered, canonically ordered (priority ↓ → due ↑, undated last → created ↑), paged summaries. | All optional: `project_id`, `status` (todo\|doing\|done), `assignee_name`, `unblocked` (bool), `search`, `priority` (0–5), `due_before`, `due_after`, `completed_since`, `limit` (def 20, max 100), `offset` | `{"tasks":[summary rows], "total", "limit", "offset"}` |
| `get_task` | One-call resume payload: every field incl. the current `version`, notes, acceptance criteria, comments tail, blockers/relations, artifacts, events. | `id` (required), `comment_limit?` (def 10, max 50), `event_limit?` (def 20, max 100) | Task detail row (see API.md) |
| `create_task` | Create a task (+ initial ACs, one transaction). Response includes advisory `possible_duplicates` — **non-blocking**; don't create near-duplicates. | `title` (required), `project_id` (required), `description?`, `priority?` (0–5), `due_at?` (RFC3339), `assignee?` {name,type}, `acceptance_criteria?` (array of strings → seq 1..n), `actor?` | `{"task": summary row at version 1, "possible_duplicates":[…]}` |
| `update_task` | Version-guarded partial update; omitted fields unchanged. Stale `version` → 409 body verbatim with `current_version` + fresh `current` row — retry with that version, no re-read needed. | `id`, `version` (both required), any of: `title`, `description`, `status`, `priority`, `due_at`, `assignee` {name,type}, `archived` (bool), `actor?` | Fresh summary row (version bumped) |
| `claim_task` | Atomically take a TTL lease and move the task to doing. Retry-safe for the current holder; a foreign active lease → 409 naming holder + expiry. | `id` (required), `actor?`, `ttl_sec?` (def 900, max 3600) | Fresh summary row with `claimed_by` + `claim_expires_at` |
| `release_task` | Give up your lease (status → todo). Holder-only; unclaimed task = idempotent no-op. | `id` (required), `note?`, `actor?` | Fresh summary row (lease cleared) |
| `heartbeat_task` | Keep the lease alive during long silent work; NO version bump, NO `updated_at` change. The fresh row doubles as a health/state check. Non-holder or no lease → 409 (empty `holder.type` = "claim first"). | `id` (required), `actor?` | Fresh summary row (new `claim_expires_at`) |
| `add_comment` | Append a typed markdown comment (no version bump). `kind: "question"` signals **needs human input**. Not idempotent — after an error, check the tail before re-posting. | `id`, `kind` (required; progress\|question\|decision\|handoff\|note), `body` (required markdown), `actor?` | The created comment row |
| `update_notes` | Replace the DURABLE resume-context blob — "write down what you found / what's next so a fresh session can pick up". **Replace-on-write**: send the complete new notes, not an append. No version field; identical content is a no-op. | `id`, `notes` (both required), `actor?` | Fresh summary row (read notes back via `get_task`) |
| `complete_ac` | Check/uncheck one acceptance criterion by its 1-based seq. Uncheck clears `completed_by`/`completed_at` (free). No version bump. | `id`, `seq` (number ≥ 1, required), `done` (bool, required), `note?`, `actor?` | The updated AC row |
| `attach_artifact` | Attach typed evidence. **Recommended for testing work:** when you run tests or verification commands, attach the output (`command-output`/`log`/`screenshot`) so a human can verify your work — encouraged, never required. Not idempotent — check artifacts via `get_task` before re-attaching after an error. | `id`, `kind` (required; log\|screenshot\|command-output\|trace\|url\|other), `ref` (required URL/path), `summary?`, `actor?` | The created artifact row |
| `relate_tasks` | Add or remove a relation edge between two tasks in the same project. | `id`, `other_id`, `kind` (all required; child\|parent\|blocked-by\|duplicate-of\|related), `action?` ("add" default \| "remove"), `actor?` | add: stored relation row (a `parent` input is stored/returned as the inverse `child` edge); remove: `{"removed":true, task_id, other_task_id, kind}` |

Notes on relations: a task has **at most one parent** — re-adding the same
parent is an idempotent no-op, a second *different* parent → 409 parent
conflict. Edges are child rows (no version bump). The `blocked` flag on
`get_task` is derived from your outgoing `blocked-by` edges at read time.

## Prompts & resources

Beyond the 14 tools, the server registers two workflow **prompts** and read-only
**resources** (both show up in your client's prompt/resource listings).

### Prompts

| Prompt | Purpose | Arguments |
|---|---|---|
| `triage_open_tasks` | Formats a triage / next-actions summary: in-flight (`doing`) work, unblocked `todo` candidates, and blocked `todo` tasks. | `project_id?` (ULID; omitted = across projects), `limit?` (max tasks inspected per status; default 50, capped at 100) |
| `next_task` | Finds the highest-priority unblocked task for an assignee or project and generates a ready-to-send agent onboarding prompt — task detail plus the step-by-step workflow playbook, including the encouraged (not required) test-evidence step via `attach_artifact`. | `project_id?`, `assignee?` (durable assignee name), `actor_name?` (defaults to the `CROWBAN_AGENT_NAME` container default) |

### Resources

| Resource URI | Contents |
|---|---|
| `crowban://task/{id}` | The full `get_task` resume payload — version, notes, ACs, comments tail, blockers/relations, artifacts, events — as a resource. |
| `crowban://project/{id}` | Project summary row with per-status open task counts. |

Resources are addressed by URI (`crowban://task/<ULID>`); an unknown or missing
task/project id returns an error naming what to fix. They are read-only and
never mutate state.

## Example tool calls

**Pick up work — `get_task`:**

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "comment_limit": 5 }
```

Note the returned `version` (e.g. `4`) — you'll need it for `update_task`.

**Claim it:**

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "ttl_sec": 1800
}
```

**Write durable resume context — `update_notes`** (replace the whole blob; this
is what a fresh session reads first):

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "notes": "Last run: migration half-applied, rolled back.\nFindings: `migrations/0007` fails on the existing index name.\nNext: verify `go test ./...`, then re-run migrations in order."
}
```

**Version-guarded update — `update_task`:**

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "version": 4,
  "status": "done"
}
```

**Attach test evidence — `attach_artifact`** (encouraged for testing work, never
required — this is what a human reviews to verify your work):

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "kind": "command-output",
  "ref": "/tmp/crowban/test-run.log",
  "summary": "go test ./... — all pass after the fix"
}
```

## Reading error bodies

Every upstream failure arrives **verbatim** as the `isError` text — a JSON body
whose `message` field is your next instruction. Treat it that way; don't retry
blindly.

**409 version conflict** (`update_task`, and rarely `update_notes`) — you read
an old version; someone else wrote in between. The body already carries the
fresh row, so retry immediately with the new version — no re-read needed:

```json
{
  "error": "version_conflict",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "current_version": 3,
  "updated_at": "2026-09-26T15:40:12Z",
  "message": "task \"01J3ZK8WQ9X7M2P4R6S8T0U2V4\" is at version 3 — re-read it and retry with that version",
  "current": { "id": "…", "title": "Fix login bug", "status": "doing", "version": 3, "…": "…" }
}
```

→ Re-send your `update_task` with `"version": 3`. If you were changing a field
that also changed in `current`, fold that in and retry once more.

**409 claim conflict** (`claim_task` / `release_task` / `heartbeat_task`) — the
holder and expiry **are** the instruction: wait for expiry, or work another task.

```json
{
  "error": "claim conflict",
  "resource": "task",
  "id": "…",
  "holder": { "type": "agent", "name": "runner-2" },
  "expires_at": "2026-09-26T15:53:00Z",
  "message": "\"…\" is held by agent (runner-2) until 2026-09-26T15:53:00Z — wait for the lease to expire or work another task"
}
```

A `heartbeat_task` with **no lease at all** also gets this shape with an empty
holder name (`"holder": { "type": "" }`) — it means *claim the task first*.

**409 parent conflict** (`relate_tasks` add) — a task already has a different
parent; one task may have at most one. Pick a different relation kind (e.g.
`related`) or fix the hierarchy.

**404 not found** — unknown id (or seq for `complete_ac`). Re-check with
`list_projects` / `list_tasks`.

**400 invalid input** — bad actor, unknown enum value, out-of-range priority,
non-RFC3339 timestamp, self-relation, empty comment body, … The message names
the fix. Note that arguments violating a tool's *input schema* (wrong types,
missing required args) also come back as `isError` with validation details —
fix the argument shape and retry.

**Retry-safety summary:** retry-safe = same-actor `claim_task`, `release_task`
on an unclaimed task, `update_notes` with identical content, re-adding an
existing relation edge. **Not idempotent** = `add_comment` / `attach_artifact`
appends — after any timeout, read the tail via `get_task` before re-posting or
you may duplicate.

If crowban-server itself is down, every tool fails with:
`crowban-server unreachable at <url> — is it running? (…)` — that's a transport
error, not a board error; check `GET /healthz`.
