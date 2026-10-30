# Rävn MCP — agent onboarding reference

This is the reference an **MCP client (or the agent behind one)** needs to work
on a Rävn board. The full wire contract lives in [API.md](API.md); this doc
covers how to connect (HTTP or stdio transports), the actor convention, all 22
tools with their exact arguments and numeric bounds, the workflow prompts, the
read-only resource templates, plus error-body reading guidance.

## Transports

`ravn-mcp` serves the Rävn REST API as MCP tools over **two wire transports**,
selected at startup (flag `-stdio` / `-transport=stdio`, or env
`RAVN_MCP_TRANSPORT=stdio`; the default is `http` — unchanged legacy behavior):

### HTTP (default) — Streamable HTTP + legacy SSE

- `POST /mcp` — Streamable HTTP JSON-RPC calls (requests and notifications),
  served **stateless**: every request is self-contained, there are no sessions
  to resume, so the client can be restarted or load-balanced freely.
  `GET /mcp` also opens a Streamable-HTTP SSE notification stream.
- `GET /sse` + `POST /message` — the legacy **SSE transport** for clients that
  only speak pre-streamable MCP. `/sse` opens the event stream (30-second
  keep-alive pings); its `endpoint` event advertises `/message?sessionId=…` for
  the JSON-RPC POSTs, with session-ID fallback if sent via the
  `Mcp-Session-Id` header or a `session_id` query param.
- `GET /healthz` — proxies upstream ravn-server liveness (used by compose
  healthchecks). **Never** gated by auth; returns 503 (never 200) when the
  upstream is down.

**Auth:** when `RAVN_TOKEN` is set, it gates `/mcp`, `/sse`, and `/message`:
send `Authorization: Bearer <token>` (or `?token=<token>` on any of the three).
Unset = no auth (the single-tenant LAN default), and the token is forwarded as
a Bearer token on every upstream request.

**CORS:** enabled on `/mcp` and `/sse`. Allowed origins come from
`RAVN_MCP_CORS_ORIGINS` — comma-separated, entries trimmed; absent or blank
falls back to `*` (any origin). Browser **OPTIONS preflights** are answered
`204 No Content` with `Access-Control-Allow-Origin`, `Allow-Methods:
GET, POST, DELETE, OPTIONS`, and `Allow-Headers: Content-Type, Authorization,
Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID` — **before** auth runs, so
the handshake completes even when `RAVN_TOKEN` is set. Credentialed CORS
responses are deliberately **never** allowed (no `Access-Control-Allow-Credentials`)
because ravn-mcp authenticates with a Bearer token, never cookies.

**Ports:** the container/binary listens on `:9080` by default (`MCP_ADDR` /
`-addr`). In the compose setup that is published to host port **9311**
(`${RAVN_MCP_PORT:-9311}:9080`), so a typical client URL is
`http://<host>:9311/mcp` — 9311 is the *published* port; inside the container
it is 9080.

### stdio — for desktop / IDE clients

For clients that launch MCP servers as **subprocesses** (Claude Desktop,
Cursor, IDE extensions), run ravn-mcp with `-stdio` (shorthand),
`-transport=stdio`, or `RAVN_MCP_TRANSPORT=stdio`:

- Plain JSON-RPC over stdin/stdout; **no port is bound at all**.
- **All logging goes to stderr** (JSON slog lines, plus the SDK's stdio error
  logger) — stdout carries ONLY the protocol stream; any stray byte would
  corrupt it.
- EOF on stdin is a clean stop.

An unrecognized `-transport` value exits with status 2 naming the valid values
(`http`, `stdio`). Both transports register the identical tool/prompt/resource
set in exactly one place — nothing exists on only one transport.

## Pointing an MCP client at it

Configuration of the ravn-mcp binary (flags win over env; each flag reads its
env var as the default):

| Variable | Flag | Default | Meaning |
|---|---|---|---|
| `RAVN_API_URL` | `-api-url` | `http://ravn-server:8080` | Base URL of the ravn-server instance to proxy. |
| `RAVN_TOKEN` | `-token` | *(unset)* | When set, required from MCP clients (`Authorization: Bearer <token>` or `?token=<token>`) on `/mcp`, `/sse`, `/message`, and forwarded on every upstream request. Unset = no auth (the single-tenant LAN default). |
| `RAVN_AGENT_NAME` | `-agent-name` | `agent` | The container-default actor NAME used when a call arrives without an `actor` argument (see below). Set it to this agent's own name. |
| `RAVN_MCP_TRANSPORT` | `-transport` | `http` | Transport to serve: `http` (Streamable HTTP + SSE) or `stdio`. |
| `RAVN_MCP_CORS_ORIGINS` | *(env only)* | `*` when unset/blank | Comma-separated list of allowed CORS origins for `/mcp` and `/sse`; pinned entries are echoed back verbatim. |
| `MCP_ADDR` | `-addr` | `:9080` | Listen address of the MCP HTTP server (ignored in stdio mode — no port is bound). |
| `MCP_MAX_PAYLOAD_BYTES` | *(env only)* | 4 MiB | Hard cap on incoming `/mcp` POST payload bytes; larger requests get `413 payload_too_large`. |

Example client configs:

Streamable-HTTP transport (compose-published port shown):

```json
{
  "mcpServers": {
    "ravn": {
      "type": "http",
      "url": "http://192.168.1.50:9311/mcp"
    }
  }
}
```

(Include the `/mcp` path in the URL. Healthcheck: `curl http://192.168.1.50:9311/healthz`.
Replace 9311 with your published `RAVN_MCP_PORT` (host mapping) or `MCP_ADDR`
(internal) if you overrode it. When `RAVN_TOKEN` is set, add an
`Authorization: Bearer <token>` header to the client config — legacy SSE clients
can instead pass `?token=<token>` on `/sse`.)

Stdio transport (desktop/IDE client launches the binary):

```json
{
  "mcpServers": {
    "ravn": {
      "command": "/opt/ravn/bin/ravn-mcp",
      "args": ["-stdio"],
      "env": {
        "RAVN_API_URL": "http://192.168.1.50:3311",
        "RAVN_TOKEN": "…",
        "RAVN_AGENT_NAME": "runner-2"
      }
    }
  }
}
```

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
- When `actor` is omitted (or empty, or present-but-malformed — wrong shape /
  empty name), the tool falls back to the container default from
  `RAVN_AGENT_NAME` rather than erroring: small models recover more cheaply
  from a working call. A call that works with the fallback still attributes to
  that shared name — pass your own name whenever you can.

## Tools (22)

All tools return **compact JSON text** on success. All failures come back as an
`isError` tool result whose text is the upstream error body **verbatim** — its
`message` field is written to be your recovery instruction. See
[Reading errors](#reading-error-bodies). Arguments violating a tool's *input
schema* (wrong types, missing required args, out-of-range numbers) also come
back as `isError` with validation details — fix the argument shape and retry.

| Tool | Purpose | Key arguments | Returns |
|---|---|---|---|
| `list_projects` | All projects newest-first, each with id, name, description and per-status open task counts (todo/doing/done; zero when empty). Use it to find a `project_id`. | *(none)* | `{"projects":[…], "total":N}` |
| `create_project` | Create a project. | `name` (required), `description?` (markdown), `actor?` | Fresh project row (`id`, `name`, …) |
| `get_project` | One project by ULID: the single row — **no** per-status task counts (those come from `list_projects`). Read-only, so no `actor` argument. Unknown id → 404 body verbatim. | `id` (required; ULID from `list_projects`) | Project row (`id`, `name`, `description`, `archived` flag, timestamps) |
| `update_project` | **Partial** update of one project mirroring REST pointer semantics: set any of `name` (rename; must be non-empty after trimming) or `description` (full markdown replace; an empty string clears it). Omitted fields are unchanged; at least one field is required — a call with neither fails as invalid input, exactly like the API's 400 for an empty update. | `id` (required), `name?`, `description?`, `actor?` | Updated project row |
| `list_tasks` | **The "what should I work on?" call.** Task summary rows in canonical order (priority ↓ → due ↑ with undated last → created ↑) inside the pagination envelope. Every argument is an OPTIONAL filter — combine them to narrow (e.g. `project_id` + `unblocked`, or `search`). Rows are summaries only: pick one and call `get_task` for its full resume context. | All 20 optional — see [list_tasks filter arguments](#list_tasks-filter-arguments) below | `{"tasks":[summary rows], "total":N, "limit":N, "offset":N}` |
| `get_task` | One-call resume payload: every field (including the current `version` — you'll need it for `update_task`), notes, acceptance criteria, comments tail, blockers/relations, artifacts, recent events. Unknown id → not-found naming the id; re-check ids with `list_projects`/`list_tasks`. | `id` (required), `comment_limit?` (int 0–50; default 10), `event_limit?` (int 0–100; default 20); larger values are clamped server-side | Task detail row (see API.md) |
| `get_task_brief` | A **compact, token-optimized** resume/handoff brief for one task — the lightweight alternative to `get_task` when you only need to pick up where a previous agent stopped. Returns exactly: `title`; `description` (trimmed); the durable `notes` blob **VERBATIM** (this is the resume context); and the LAST ≤2 comments whose kind is `"handoff"` as `handoff_comments[]` (`body`, `by`, `created_at`). Everything else — events, artifacts, acceptance criteria, blockers, relations, the full comment list — is omitted to stay small. Read-only: `actor` is accepted for interface parity but not attributed. Unknown id → not-found naming the id. | `id` (required), `actor?` | `{"title":…, "description"?, "notes":…, "handoff_comments":[…]}` |
| `project_report` | The **single-call project health digest** — use this instead of combining `list_tasks` + `get_task` per task. Returns counts by status (todo/doing/done) and priority (0–5), active claim leases with remaining seconds, expired claims (zombies), stalled doing-tasks (no activity for MORE than the window; heartbeats never count as activity), bottleneck blockers in BOTH directions (who blocks what / who is blocked by whom), recently completed tasks inside the `since` window, and per-assignee workload (unassigned under `"\\"`). Both windows default when omitted. Unknown project → not-found verbatim. | `project_id` (required; ULID from `list_projects`, must exist), `stalled_minutes?` (number ≥ 0; default 60 when omitted or zero), `since?` (RFC3339; default now-24h) | The report object described above, as compact JSON |
| `create_task` | Create a task in a project, with optional initial ACs — all in ONE transaction. Also optional `parent_id` (file it as a child of an existing task in the same call) and a `blocked_by` array (the task is created ALREADY blocked by each named task; repeats dedupe). Response includes advisory `possible_duplicates` — **non-blocking**: never an error, but if it lists a near-duplicate do NOT create another task; work on or relate to the existing one instead. Unknown `project_id`, `parent_id` or `blocked_by` ids → invalid-input error verbatim (nothing is created). | `title` (required), `project_id` (required), `description?`, `priority?` (int 0–5; default 0), `due_at?` (RFC3339), `assignee?` {name,type}, `acceptance_criteria?` (array of strings → seq 1..n), `parent_id?` (ULID; same project), `blocked_by?` (array of task ULIDs; same project), `actor?` | `{"task": summary row at version 1, "possible_duplicates":[…]}` |
| `update_task` | **Version-guarded** partial update of one task; omitted fields unchanged. A STALE `version` returns isError with the 409 body VERBATIM — it carries `current_version` plus a fresh `"current"` row, so retry your update with that version WITHOUT re-reading. **Clear semantics:** `clear_due` (boolean) removes the deadline and takes precedence over `due_at` when both are given in one call (an empty-string `due_at` is a no-op that keeps the current deadline); `clear_assignee` (boolean) removes the durable assignee and takes precedence over `assignee`. | `id`, `version` (int ≥ 1; both required — optimistic-concurrency guard, 1 on a fresh task), any of: `title?`, `description?` (present = full replace, even to empty), `status?` (todo\|doing\|done; transition-validated), `priority?` (int 0–5), `due_at?` (RFC3339), `clear_due?` (bool), `assignee?` {name,type}, `clear_assignee?` (bool), `archived?` (bool), `override_blockers?` (bool — bypass the unresolved-blocker check when transitioning to done), `actor?` | Fresh summary row (version bumped) |
| `claim_task` | Atomically take a TTL lease and move the task to doing. Retry-safe: if YOU already hold the active lease, re-claiming just refreshes it. A foreign active lease → 409 naming holder + expiry — don't wait/sleep/poll for expiry; comments/notes/status/AC mutations are open to non-holders only with explicit permission. | `id` (required), `actor?`, `ttl_sec?` (int 0–3600; default 900 when omitted or zero, >3600 clamped server-side) | Fresh summary row with `claimed_by` + `claim_expires_at` |
| `release_task` | Give up YOUR claim lease (status → todo). Holder-only: another holder's active lease → 409 naming the real holder and expiry. Releasing a task with no lease at all is an idempotent no-op. The note rides on the released event — it is NOT posted as a comment. | `id` (required), `note?`, `actor?` | Fresh summary row (lease cleared) |
| `heartbeat_task` | Keep-alive for YOUR active claim lease during long silent work — call it regularly so the lease doesn't expire mid-task. Deliberately bumps NO version and touches NO `updated_at` (a heartbeat never invalidates a working agent's read base). The fresh row doubles as a health/state check. Non-holder or expired → 409 naming the real holder + expiry; NO lease at all → 409 "not claimed" — call `claim_task` first. | `id` (required), `actor?` | Fresh summary row (new `claim_expires_at`) |
| `handoff_task` | **Atomically hand off a task you hold** to its next actor in ONE call, replacing the fragile update_notes + add_comment + assign + release sequence. You must hold the ACTIVE claim lease (a foreign active lease → 409; none at all → 409 "not claimed" — `claim_task` first). On success, in one transaction: optionally replaces notes, sets/clears the durable assignee, appends exactly ONE comment (kind `"handoff"` default or `"question"` — anything else → 400), clears the lease and moves status back to todo; version bumped once. | `id` (required), `kind?` ("handoff" \| "question"), `body?` (markdown; omitted/empty → a concise synthesized default like "Handed off to \<receiver\> by \<actor\>"), `notes?` (REPLACE-ON-WRITE; omitted/empty leaves the stored notes untouched — it is never a clear), `to_assignee?` {name,type} (sets the receiver as durable assignee; omit = unchanged), `clear_assignee?` (bool; takes precedence over `to_assignee`), `actor?` | `{"task": fresh row, "comment": created comment}` |
| `add_comment` | Append a typed markdown comment to the task thread (a child-row mutation: **NO** task version bump). `kind: "question"` signals you need HUMAN INPUT; `"handoff"` = stepping away from the task. Not idempotent — after a timeout error, check the comment tail via `get_task` before re-posting or you may duplicate. | `id`, `kind` (required; progress\|question\|decision\|handoff\|note), `body` (required markdown, non-empty), `actor?` | The created comment row |
| `update_notes` | Replace the task's DURABLE resume-context blob in WHOLE — the "write down what you found / what's next" call so a fresh session can pick up where you stopped. **Replace-on-write**: send the complete new notes, not an append; presence counts (an intentional empty string clears the scratchpad). Deliberately NO version field (single-writer scratchpad): identical content is a no-op; a genuinely concurrent write can surface as 409 `version_conflict` — handle it like any 409. | `id`, `notes` (both required), `actor?` | Fresh summary row (read notes back via `get_task`) |
| `complete_ac` | Check or uncheck ONE acceptance criterion by its 1-based `seq` (from `get_task`'s `acceptance_criteria`), **or reword it**: optional `text` sets the new wording and takes precedence over `done`/`note`. `done=true` records `completed_by`/`completed_at`; `done=false` un-checks, clearing both (free). Child-row mutation: NO version bump. Unknown task OR unknown seq → not-found verbatim. **Adding / editing / removing criteria are SEPARATE tools** — `add_ac`, `edit_ac`, `remove_ac`. | `id`, `seq` (int ≥ 1), `done` (bool) — all three required — plus `note?`, `text?` (reword; wins over done/note), `actor?` | The updated AC row |
| `add_ac` | Append a new acceptance criterion to an existing task at `max(seq)+1` — callers never choose the seq. Child-row mutation: NO version bump. Unknown task → not-found verbatim. | `id` (required), `text` (required markdown, non-empty), `actor?` | The created AC row |
| `remove_ac` | Remove an acceptance criterion by its 1-based `seq`. Surviving criteria keep their seqs (no renumbering). NO version bump. Unknown task OR unknown seq → not-found verbatim. | `id`, `seq` (int ≥ 1; both required), `actor?` | `{"removed":true, "task_id", "seq"}` |
| `edit_ac` | Reword or edit the TEXT of an existing acceptance criterion by its 1-based seq. NO version bump. Unknown task OR unknown seq → not-found verbatim. | `id`, `seq` (int ≥ 1), `text` (all required; markdown, non-empty), `actor?` | The updated AC row |
| `attach_artifact` | Attach a typed piece of evidence. **Recommended for verification work:** when your work includes running tests or verification commands, attach that output (`command-output`/`log`/`screenshot`) so a human can verify it — encouraged, never required. Upload binary files (images, video, audio, PDF, logs) via `data_base64` (+ `filename` for the extension); a `data:` URI passed in `ref` is treated as base64 too. Either `data_base64` or `ref` is REQUIRED. A `ref` that is a web URL (http/https or mailto) is stored by reference; a path readable **on the MCP server's own filesystem** is uploaded — never pass local client paths in `ref`. Not idempotent — after an error, check artifacts via `get_task` before re-attaching or you may duplicate. | `id`, `kind` (required; log\|screenshot\|command-output\|trace\|url\|other), `data_base64?` (base64 file bytes or data URI — REQUIRED for screenshots and workspace evidence files), `filename?` (e.g. "test.png"), `ref?` (URL, mailto, or server-side path), `summary?`, `actor?` | The created artifact row |
| `relate_tasks` | Add or REMOVE a relation edge between two tasks in the SAME project. Both ends must exist and may not be the same task. For add: "parent" is normalized server-side to the inverse "child" edge (the returned kind is always the STORED kind); re-adding an existing identical edge is an idempotent no-op; a second DIFFERENT parent → 409 parent-conflict verbatim. For remove: upstream answers 204 No Content. | `id`, `other_id`, `kind` (all required; child\|parent\|blocked-by\|duplicate-of\|related), `action?` ("add" default \| "remove"), `actor?` | add: stored relation row; remove: `{"removed":true, "task_id", "other_task_id", "kind"}` |

Notes on relations: a task has **at most one parent** — re-adding the same
parent is an idempotent no-op, a second *different* parent → 409 parent
conflict. Edges are child rows (no version bump). The `blocked` flag on
`get_task` is derived from your outgoing `blocked-by` edges at read time.

### list_tasks filter arguments

All twenty arguments are optional and combine as AND filters:

| Argument | Type | Semantics |
|---|---|---|
| `project_id` | string (ULID) | Restrict to one project (from `list_projects`). |
| `status` | enum | Exact status match: `todo` \| `doing` \| `done`. |
| `assignee_name` | string | Durable assignee NAME (not the ephemeral claim holder). |
| `assignee_type` | enum | Filter by durable assignee type: `human` \| `agent`. |
| `archived` | boolean | `true` = list archived tasks; default/omitted (`false`) lists open (non-archived) tasks only. |
| `unblocked` | boolean | `true` = keep only tasks with no open blocker — the ready-to-work set. |
| `parent_id` | string (ULID) | Children of one task: only tasks filed under this parent match. |
| `claimed` | boolean, **tri-state** | On the EPHEMERAL claim lease: `true` = a lease row is present (live or stale), `false` = unclaimed, omitted = any. A non-boolean value → invalid input. |
| `claimed_by` | string | Exact match on the EPHEMERAL claim-holder name (not the durable assignee). |
| `expired_claim` | boolean, **tri-state** | Lease lapse: `true` = stale lease (expiry in the past — anyone may claim), `false` = unclaimed or still active, omitted = any. A non-boolean value → invalid input. |
| `search` | string | Case-insensitive substring over title + description + notes. |
| `priority` | integer | Exact priority match, 0–5 (schema-enforced). |
| `due_before` | RFC3339 string | due_at **strictly before** this (undated tasks never match). |
| `due_after` | RFC3339 string | due_at **at or after** this (inclusive). |
| `created_after` | RFC3339 string | Only tasks created at/after this time (inclusive). |
| `created_before` | RFC3339 string | Only tasks created strictly before this time. |
| `completed_since` | RFC3339 string | Only tasks completed at/after this time. |
| `completed_before` | RFC3339 string | Only tasks completed strictly before this time (complements `completed_since`). |
| `limit` | integer | Page size: 1–100; server default 20 when omitted (values above the max are clamped server-side, not rejected). |
| `offset` | integer | Pagination offset: ≥ 0, default 0. |

**Canonical ordering** (applied by the API before pagination): priority DESC →
due_at ASC with undated tasks last → created_at ASC. **Pagination envelope:**
`{"tasks":[…], "total":N, "limit":N, "offset":N}` — `total` is the count across
the whole filtered set, not just this page.

### Integer / bound semantics

Numeric arguments are schema-validated: out-of-range values come back as
`isError` validation details rather than reaching the API.

| Argument | Type | Bounds & defaults | Used by |
|---|---|---|---|
| `priority` | integer | 0–5 (0 = lowest, 5 = highest); default 0 when omitted on create | `list_tasks`, `create_task`, `update_task` |
| `limit` | integer | 1–100; server default 20 when omitted (>max clamped server-side) | `list_tasks` |
| `offset` | integer | ≥ 0; default 0 | `list_tasks` |
| `comment_limit` | integer | 0–50; default 10; larger values clamped server-side | `get_task` |
| `event_limit` | integer | 0–100; default 20; larger values clamped server-side | `get_task` |
| `ttl_sec` | integer | 0–3600; omitted or zero → 900; >3600 clamped server-side (negative rejected) | `claim_task` |
| `version` | integer | ≥ 1, **required** — optimistic-concurrency guard; fresh tasks are version 1 and every mutating result echoes the new version | `update_task` |
| `seq` | integer | ≥ 1, **required** — the 1-based AC index from `get_task`.acceptance_criteria | `complete_ac`, `remove_ac`, `edit_ac` |
| `stalled_minutes` | number | ≥ 0; omitted or zero → server default 60 | `project_report` |

## Prompts & resources

Beyond the 22 tools, the server registers two workflow **prompts** and
read-only **resources** (both show up in your client's prompt/resource
listings).

### Prompts

| Prompt | Purpose | Arguments |
|---|---|---|
| `triage_open_tasks` | Formats a triage / next-actions summary: in-flight (`doing`) work with claim-lease and assignee detail, unblocked `todo` candidates ("ready for pickup"), and blocked `todo` tasks (todo minus unblocked), plus numbered recommended next actions. An unknown `project_id` yields an explicit error message telling you to verify projects with `list_projects`. | `project_id?` (ULID; omitted = across all projects), `limit?` (max tasks inspected per status; default 50, capped at 100) |
| `next_task` | Finds the highest-priority unblocked task for an assignee or project and generates a ready-to-send agent onboarding prompt: full task details (incl. version), description, acceptance criteria, resume notes, recent discussion, and the step-by-step workflow playbook — claim → update notes → heartbeat during long work → complete every AC → attach test evidence (encouraged, never required) → mark done directly WITHOUT releasing first; it also recommends `handoff_task` for handing off in-progress work. If no unblocked task exists, it falls back to an in-progress task claimed/assigned to that actor, or returns a "No Available Tasks" message with next actions. | `project_id?`, `assignee?` (durable assignee name), `actor_name?` (defaults to the `RAVN_AGENT_NAME` container default) |

### Resources — URI *templates*, not static resources

The server registers **four RFC 6570 URI templates** — these are what a client
sees in `resources/templates/list`:

| Template URI | Name | Fills in `{id}` with… | Read via `resources/read` gives you |
|---|---|---|---|
| `ravn://task/{id}` | Task Resume Payload | a task ULID | The full `get_task` resume payload — version, notes, acceptance criteria, comments tail, blockers/relations, artifacts, events — as `application/json`. |
| `ravn://project/{id}` | Project Summary | a project ULID | The single project row from `get_project` (`id`, `name`, `description`, `archived`, timestamps) as `application/json` — **no** per-status task counts (those come from the `list_projects` tool). |
| `crowban://task/{id}` | Task Resume Payload (Legacy) | a task ULID | Same as `ravn://task/{id}`. Kept for backwards compatibility with older clients; prefer `ravn://`. |
| `crowban://project/{id}` | Project Summary (Legacy) | a project ULID | Same as `ravn://project/{id}`. Prefer `ravn://`. |

Read that last table this way: the `{id}` slot is a **template parameter**, not
a literal URI. Clients should read from *concrete* URIs with the slot filled in
— e.g. `resources/read` on `ravn://task/01J3ZK8WQ9X7M2P4R6S8T0U2V4` — or pass
the id as a template argument (`arguments: {"id": "01J3…"}`). Reading the bare
template (with `{id}` still in it) returns an error naming what to fix —
`` `"task ID required: specify a task ULID in URI (e.g. ravn://task/<id>)"` ``.
There are no
static, always-listable concrete resource URIs — every read materializes one on
demand from the board. Resources are read-only and never mutate state; an
unknown task/project id surfaces the upstream 404 naming the id.

## Example tool calls

**Pick up work — `get_task`:**

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "comment_limit": 5 }
```

Note the returned `version` (e.g. `4`) — you'll need it for `update_task`.

When you only need the resume context (notes + last handoffs) and want to save
context tokens, use `get_task_brief`:

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4" }
```

**Project look-up and partial update — `get_project` / `update_project`:**

```json
{ "id": "01J2Q8XK5N3P6R4T7W9YB1D3F5" }
```

```json
{
  "id": "01J2Q8XK5N3P6R4T7W9YB1D3F5",
  "actor": { "name": "runner-2", "type": "agent" },
  "description": "Scope narrowed: landing page only; auth moves to Q2."
}
```

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

**Version-guarded update — `update_task`** (including the clear flags; a clear
flag takes precedence over its value counterpart when both are sent in one
call, and an empty-string `due_at` is a no-op):

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "version": 4,
  "status": "done"
}
```

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "version": 5,
  "clear_due": true,
  "clear_assignee": true
}
```

**Acceptance criteria — `complete_ac` / `add_ac`:**

Check one off, un-check another (`done=false` is free), or reword one — the
optional `text` takes precedence over `done`/`note`:

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "actor": { "name": "runner-2", "type": "agent" }, "seq": 1, "done": true, "note": "verified by go test ./..." }
```

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "actor": { "name": "runner-2", "type": "agent" }, "seq": 2, "done": false }
```

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "actor": { "name": "runner-2", "type": "agent" }, "seq": 3, "done": true, "text": "Also cover the regression case from ticket #123" }
```

Adding a new criterion, rewording an existing one, or removing one are separate
tools — `add_ac` (appends at max(seq)+1), `edit_ac`, and `remove_ac`.

**Attach test evidence — `attach_artifact`** (encouraged for testing work, never
required — this is what a human reviews to verify your work):

Upload binary files (screenshot, video, audio, PDF) using base64:

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "kind": "screenshot",
  "filename": "test-finished.png",
  "data_base64": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB...",
  "summary": "E2E theme toggle AC1 verified"
}
```

Or attach a web URL:

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "actor": { "name": "runner-2", "type": "agent" },
  "kind": "command-output",
  "ref": "https://ci.example.lan/builds/123/test.log",
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

**409 claim conflict** (`claim_task` / `release_task` / `heartbeat_task` /
`handoff_task`) — the holder and expiry **are** the instruction: wait for
expiry, or work another task.

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

**404 not found** — unknown id: a task ULID, a project ULID
(`get_project` / `update_project` / `project_report`), or an AC `seq` for
`complete_ac` / `remove_ac` / `edit_ac`. Re-check with `list_projects` /
`list_tasks`.

**400 invalid input** — bad actor, unknown enum value, out-of-range priority,
non-RFC3339 timestamp, self-relation, empty comment body, an `update_project`
with no fields to set, … The message names the fix. Note that arguments
violating a tool's *input schema* (wrong types, missing required args,
out-of-bounds numbers) also come back as `isError` with validation details —
fix the argument shape and retry.

**Retry-safety summary:** retry-safe = same-actor `claim_task`, `release_task`
on an unclaimed task, `update_notes` with identical content, re-adding an
existing relation edge. **Not idempotent** = appends and single-transaction
side effects: `add_comment` / `attach_artifact` (append — after any timeout,
read the tail via `get_task` before re-posting or you may duplicate) and
`handoff_task` (posts exactly one comment inside its transaction — re-check the
task state via `get_task` before re-running).

If ravn-server itself is down, every tool fails with:
`ravn-server unreachable at <url> — is it running? (…)` — that's a transport
error, not a board error; check `GET /healthz`.

## Related tooling: `ravn-exec`

Companion CLI for wrapping long-running commands in an automatic claim lease:
`ravn-exec --task-id <id> [--ttl-sec N] [--interval DUR] -- <command> [args…]`.
It probes for an existing lease on the task (via a heartbeat) and claims one if
needed (TTL default 900 seconds; server max 3600), then runs your command as a
direct child process (no shell; stdin/stdout/stderr pass straight through)
while heartbeating every `--interval` (default: `ttl-sec/3`, Go duration format
e.g. `2m`). On any exit it **always releases the lease**, and on a non-zero
exit code it first posts one `progress` comment recording the command and its
exit code (`ravn exec: <command> exited with code <N>`). It reads the same
environment as ravn-mcp's upstream config: `RAVN_API_URL`, `RAVN_TOKEN`, and
`RAVN_AGENT_NAME` (the actor that claims/releases).
