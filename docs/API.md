# crowban REST API — stable contract

This document is the **durable contract** for the crowban HTTP API. Both the
MCP container (`crowban-mcp`) and the web frontend implement against it: every
endpoint below is real, tested code in `internal/interface/http`. If this doc
and the code ever disagree, the tests win — but changes to either must update
this file in the same commit.

- **Base path:** `/api/v1` (all routes below are relative to it unless noted).
- **Transport:** JSON over HTTP only (`Content-Type: application/json`) except
  the SSE event streams, which are `text/event-stream`. Timestamps are RFC3339
  UTC (`2026-09-26T15:48:00Z`). IDs are ULID strings.
- **Auth:** off by default — crowban is a single-tenant, LAN-local service. When
  the server starts with `CROWBAN_TOKEN` set (env or `-token` flag), every route
  under `/api/v1/*` (the SSE event streams included) requires the token, sent as
  `Authorization: Bearer <token>` **or** `?token=<token>` on the query string
  (the query form exists for EventSource/SSE clients that cannot set headers).
  Absent or mismatched → `401` (below); unset token → nothing required.
  `/healthz`, root-level `/metrics`, and the static UI are never gated.

## Actor convention

Every mutation body carries an `actor` object — the self-declared identity of
whoever is acting (single-tenant MVP; there is no session state, so the server
trusts it):

```json
"actor": { "name": "ada", "type": "human" }
```

| Field | Rules |
|---|---|
| `name`  | **Required.** Non-empty after trimming; at most 100 runes; no control characters (newlines, tabs, …); must be well-formed UTF-8. This name is the lease/ownership key for claims — see "Lease-holder identity" below. |
| `type`  | Optional. `"human"` \| `"agent"`. **Defaults to `"human"`** when omitted (a bare name from a browser/curl is usually human; agents should send `"agent"` explicitly). Anything else → `400`. |

An invalid actor — empty/oversized/control-char name, unknown type — fails
with `400 invalid input` on every mutation endpoint before anything is written.

### Lease-holder identity

Claim-lease holder equality (claim renewal, heartbeat, release) keys on the
actor **NAME alone** — the unified NAME identity model: a human-typed and an
agent-typed actor that share a name are the SAME lease holder, so either may
re-claim / heartbeat / release the other's lease. `claimed_by_type` is stored
and returned for display/audit only; it never participates in holder
comparison. This matches the self-declared-identity MVP (names carry no trust
signal, and the holder's full `(type, name)` pair is always visible in task
rows and claim-conflict bodies), so making the comparison type-sensitive would
buy no protection while breaking clients that omit `type` on some calls.

## Error contract

Every error body on `/api/v1/*` is JSON with the exact shapes below. The
`message` field doubles as the **agent-readable recovery hint** — the MCP layer
propagates these bodies verbatim, so treat `message` as instructions, not as a
log line. (Key order in real responses is sorted by key; examples are shown in
logical order.)

### 400 invalid input

```json
{ "error": "invalid input", "message": "comment body must not be empty" }
```

Emitted for: malformed JSON (`"malformed JSON body: …"`), missing path id,
bad actor, unknown kind/status/type values, out-of-range priority or limit,
non-RFC3339 timestamps, negative claim ttl, self-relations, cross-project
relation pairs, and any other use-case validation failure. No `resource`/`id`.

### 401 unauthorized

Only when the server has `CROWBAN_TOKEN` configured (see Auth above); returned
by any `/api/v1/*` route on an absent or mismatched token:

```json
{ "error": "unauthorized", "message": "missing or invalid auth token" }
```

### 404 not found

```json
{
  "error": "not found",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "task \"01J3ZK8WQ9X7M2P4R6S8T0U2V4\" does not exist — check the id (use the list endpoint)"
}
```

`resource` is `"task"`, `"project"` (or `"comment"`-family where applicable).
A `PATCH …/acceptance-criteria/{seq}` with an unknown seq also returns 404 in
this shape (`resource: "task"`, the task id echoed).

### 409 version conflict

Returned by `PATCH /tasks/{id}` when the caller's `version` is stale — checked
**inside the write transaction**, so it holds even under concurrency — and by
other write paths when a concurrent writer wins an internal guard or (on the
optionally-guarded notes path) the base version is stale. The body carries
everything needed to rebase **without a separate read**:

```json
{
  "error": "version_conflict",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "current_version": 3,
  "updated_at": "2026-09-26T15:40:12Z",
  "message": "task \"01J3ZK8WQ9X7M2P4R6S8T0U2V4\" is at version 3 — re-read it and retry with that version",
  "current": { /* fresh task summary row, see below */ }
}
```

### 409 claim conflict

Returned by `claim` / `release` / `heartbeat` on a coordination collision. The
holder + expiry name who holds the lease and when it lapses — **not** an
instruction to wait for them:

```json
{
  "error": "claim conflict",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "holder": { "type": "agent", "name": "runner-2" },
  "expires_at": "2026-09-26T15:53:00Z",
  "message": "\"01J3ZK8WQ9X7M2P4R6S8T0U2V4\" is held by agent (runner-2) until 2026-09-26T15:53:00Z — do not wait for the lease to expire: close-out mutations (comments/notes/status/ACs) are open to non-holders, but only with explicit permission (from the holder or a human) to work that task; otherwise stop and report"
}
```

Cases that produce it: `claim` on a foreign active lease; `release` by anyone
but the active holder (an unclaimed task is an idempotent no-op, not an error);
`heartbeat` by a non-holder or after expiry. The body ALWAYS names a live
foreign holder — a heartbeat on a task with **no lease at all** is NOT a claim
conflict; see `409 not claimed` below.

> [!NOTE]
> **For agents — holder-gated vs version-guarded.** Only the *lease* operations
> (`claim`, and the holder-only `release`/`heartbeat`) are gated on holding the
> active lease. All other task mutations — comments, notes, status, priority,
> due date, assignee, archived, acceptance criteria — are **version-guarded only**
> (optimistic concurrency via `version`) and are open to non-holders. A `409
> claim conflict` therefore never requires sleeping, polling, or waiting for the
> lease to expire: if you have explicit permission (from the holder or a human)
> to work that task, proceed with those version-guarded mutations directly;
> otherwise stop and report. Idling until `expires_at` is never correct.

### 409 not claimed

Returned by `heartbeat` when the task has no lease at all (there is nobody to
wait for — the recovery instruction is “claim it first”). Same 409 family as
the claim conflicts so clients branch on status and handle both together; the
body tag names the real state.

```json
{
  "error": "not claimed",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "not claimed: task 01J3ZK8WQ9X7M2P4R6S8T0U2V4 has no claim lease — call claim first"
}
```

### 409 parent conflict

One task may have at most one parent. Adding a *second, different* parent:

```json
{
  "error": "parent conflict",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "task already has a parent: task 01J3ZK8WQ9X7M2P4R6S8T0U2V4 already has a parent (01J3YV2NMB4D9C6K8F0H3L5P7R)"
}
```

Re-adding the *same* parent is not an error — it is the idempotent no-op
described under relations.

### 415 unsupported media type

Returned when a request body is supplied with a missing or mismatched `Content-Type` header (must be `application/json`):

```json
{ "error": "unsupported media type", "message": "Content-Type must be application/json" }
```

### 500 internal

```json
{ "error": "internal", "message": "unexpected server error" }
```

Anything the mapping above does not cover. Logged server-side with detail; do
not retry blindly (retries are safe only where documented below).

## Pagination contract

Paged list endpoints use **offset/limit** query params and return one envelope:

```json
{ "tasks": [ /* rows */ ], "total": 42, "limit": 20, "offset": 0 }
```

- `total` = matching row count **before** paging (lets clients compute pages).
- `limit` — default **20**, max **100**; values above the cap are **clamped, not rejected**. `limit ≤ 0` → `400`.
- `offset` — default **0**; negative or non-numeric → `400`.
- The envelope echoes the *effective* limit/offset after clamping.

The same policy applies to `GET /tasks/{id}/comments`. (The `get_task` resume
payload has separate smaller “tail” limits: `comment_limit` default 10 max 50,
`event_limit` default 20 max 100.)

## Row shapes

**Task summary row** — returned by every list/board/create/update/claim/
release/heartbeat/notes response (compact on purpose; context economy):

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "title": "Fix login bug",
  "status": "doing",
  "priority": 3,
  "due_at": "2026-10-01T00:00:00Z",
  "assignee": { "type": "human", "name": "ada" },
  "claimed_by": { "type": "agent", "name": "runner-2" },
  "claim_expires_at": "2026-09-26T15:53:00Z",
  "version": 4,
  "created_at": "2026-09-25T18:02:11Z",
  "updated_at": "2026-09-26T15:48:03Z"
}
```

Optional fields (`due_at`, `assignee`, `claimed_by`, `claim_expires_at`) are
**omitted entirely** when unset — never `null`. `claimed_by` is the *ephemeral*
lease holder and is distinct from the durable `assignee`; claiming/releasing/
heartbeating never touches the assignee.

Three more optional fields — `ac_total`, `ac_done`, and `comment_count` — are
populated **only on board-snapshot summary rows** (see
`GET /projects/{id}/board` below): the acceptance-criteria total, how many of
them are done, and the comment-thread length. They back the web UI's per-card
badges and are **omitted entirely** when zero — list/create/update/claim and
the detail rows never carry them, keeping those payloads compact. They are
computed with one grouped query per child table (no N+1).

**Task detail row** — `GET /tasks/{id}` only: the summary fields (flattened),
plus:

```json
{
  "project_id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",
  "description": "Users cannot log in after the password reset flow.",
  "notes": "Last run: migration half-applied, rolled back.\nNext: verify `go test ./...`",
  "archived": false,
  "blocked": true,
  "blockers": [ { "id": "…", "title": "Fix DB migration", "status": "doing" } ],   // [] when none — never null
  "claimed_at": "2026-09-26T15:48:03Z",          // omitted while unclaimed (also claim_ttl_seconds)
  "claim_ttl_seconds": 900,
  "completed_at": "2026-09-26T15:55:00Z",        // set when the task moves to done; cleared again if it leaves done
  "acceptance_criteria": [ /* ac rows, [] when none — never null */ ],
  "comments":          [ /* comment rows, newest first (tail) */ ],
  "artifacts":         [ /* artifact rows, oldest first */ ],
  "relations":         [ /* relation rows touching this task, either direction */ ],
  "events":            [ /* event rows, newest first (tail) */ ]
}
```

| Row | Shape |
|---|---|
| AC          | `{ "seq": 1, "text": "login works", "done": true, "note": "manual test passed", "completed_by": {"type":"human","name":"ada"}, "completed_at": "2026-09-26T15:40:12Z" }` — `seq` is 1-based; note/completed_* omitted when unset |
| Comment     | `{ "id": "…", "kind": "progress", "body": "ran the build — green", "actor": {"type":"human","name":"ada"}, "created_at": "…" }` |
| Artifact    | `{ "id": "…", "kind": "url", "ref": "https://example.com/reports/deploy.json", "summary": "deploy report", "actor": {…}, "created_at": "…" }` — `summary` omitted when empty |
| Relation    | `{ "task_id": "…", "other_task_id": "…", "kind": "child", "created_at": "…" }` — the stored edge's own id is never exposed; edges are addressed by the composite `(task_id, other_task_id, kind)` |
| Event       | `{ "id": "…", "kind": "claimed", "actor": {…}, "before": "{…}", "after": "{…}", "at": "…" }` — `before`/`after` are compact JSON strings from the event store; each is omitted when empty |

## Route table

17 MCP tools map onto the endpoints marked with a tool name; the rest are UI
extras (the web frontend uses them directly).

| Method & path | Purpose | MCP tool |
|---|---|---|
| `GET /healthz`                        | liveness + DB ping (unauthenticated, outside `/api/v1`) | — (container healthcheck) |
| `GET  /metrics`                       | in-memory metrics, Prometheus text or JSON (outside `/api/v1`, never gated) | — (ops) |
| `GET  /api/v1/metrics`                | same metrics under the API prefix (auth-gated when a token is set)          | — (ops) |
| `GET  /projects`                      | projects + per-status open task counts                  | `list_projects` |
| `POST /projects`                      | create project → 201                                     | `create_project` |
| `GET  /projects/{id}`                 | project detail (UI)                                      | — |
| `PUT  /projects/{id}`                 | rename/description/opencode-config edit (UI)             | — |
| `GET  /projects/{id}/board`           | whole-project board snapshot (UI)                        | — |
| `GET  /projects/{id}/events`          | realtime SSE event stream scoped to the project (UI)     | — |
| `GET  /events?project_id=…`           | same stream, query-scoped; `project_id` required (else `400`) | — |
| `GET  /tasks`                         | filtered, ordered, paged task summaries                  | `list_tasks` |
| `POST /tasks`                         | create task (+ACs) → 201 with duplicate advisory         | `create_task` |
| `GET  /tasks/{id}`                    | one-call resume payload                                  | `get_task` |
| `PATCH /tasks/{id}`                   | version-guarded partial update                           | `update_task` |
| `POST /tasks/{id}/claim`              | acquire/renew the claim lease                            | `claim_task` |
| `POST /tasks/{id}/release`            | holder releases the lease                                | `release_task` |
| `POST /tasks/{id}/heartbeat`          | keep the lease alive (no version bump)                   | `heartbeat_task` |
| `POST /tasks/{id}/comments`           | append a typed comment → 201                             | `add_comment` |
| `GET  /tasks/{id}/comments`           | paged comment thread + total (UI)                        | — |
| `PUT  /tasks/{id}/notes`              | replace the notes blob                                   | `update_notes` |
| `POST /tasks/{id}/acceptance-criteria`            | append a new AC at next seq → 201          | `add_ac` |
| `PATCH /tasks/{id}/acceptance-criteria/{seq}`     | toggle one AC, or reword it via `text`    | `complete_ac`, `edit_ac` |
| `DELETE /tasks/{id}/acceptance-criteria/{seq}`    | remove one AC → 204 (actor required)      | `remove_ac` |
| `POST /tasks/{id}/artifacts`          | attach evidence → 201                                    | `attach_artifact` |
| `POST /tasks/{id}/opencode/start`     | dispatch an autonomous OpenCode session → 200 session id + url (UI/ops) | — |
| `POST   /tasks/{id}/relations`        | add a relation edge (upsert)                             | `relate_tasks` (add) |
| `DELETE /tasks/{id}/relations/{other_id}/{kind}` | remove a relation edge                | `relate_tasks` (remove) |

---

## Endpoints

### GET /healthz

Liveness probe; the MCP container pings it to report upstream health.

```json
{ "status": "ok", "db": "ok" }
```

`503 {"status":"degraded","db":"error: …"}` when the DB ping fails. Outside the
`/api/v1` prefix; never requires auth.

### GET /metrics · GET /api/v1/metrics — observability

In-memory request and task counters, no external dependencies. The format is
chosen by `?format=json|prometheus|text` or the `Accept` header; with neither,
`/api/v1/metrics` defaults to JSON and root `/metrics` to Prometheus text
exposition (`text/plain; version=0.0.4`).

- Root `/metrics` lives outside the API prefix — never auth-gated (ops-only,
  not part of the durable agent contract).
- `GET /api/v1/metrics` is under the prefix and takes the same `401` gate as
  everything else when a token is configured.

Series: `http_requests_total`, `http_requests_in_flight`,
`http_requests_by_status{code}`, `http_requests_by_method{method}`,
`http_request_duration_seconds_total`, `process_uptime_seconds` (Prometheus) /
`uptime_seconds` (JSON), plus live DB counts `tasks_by_status{status}` and
`active_claims` (omitted from JSON when the DB callback is unwired). The JSON
body is a flat snapshot object with one key per series.

### GET /projects → `list_projects`

Lists projects, newest-first, with per-status **open** task counts (all three
statuses always present, zero when empty). Not paged in the MVP.

Query param:
- `archived`: `true` lists archived projects; default `false` = open only.

```json
{
  "projects": [
    {
      "id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",
      "name": "Website Redesign",
      "description": "MVP landing page work",
      "opencode_server_url": "http://localhost:4444",
      "opencode_directory": "/home/hollen/Code/crowban",
      "opencode_model": "llama.cpp/Qwen Medium Long Context",
      "archived": false,
      "created_at": "2026-09-20T09:00:00Z",
      "updated_at": "2026-09-20T09:00:00Z",
      "task_counts": { "todo": 3, "doing": 1, "done": 5 }
    }
  ],
  "total": 1
}
```

`description` and the three `opencode_*` fields are **omitted** when unset. The
`opencode_*` fields configure autonomous session dispatch for this project
(see `POST /tasks/{id}/opencode/start`).

### POST /projects → `create_project`

Request: `{ "actor": {"name":"ada"}, "name": "Website Redesign", "description": "MVP landing page work" }`
(`description` optional). `201` with the fresh project row. Empty name → `400`.

### GET /projects/{id} (UI) · PUT /projects/{id} (UI)

Get returns the project row; `404 not found` (`resource: "project"`) when
unknown. Put takes `{ "actor": …, "name"?: …, "description"?: …,
"opencode_server_url"?: …, "opencode_directory"?: …, "opencode_model"?: …, "archived"?: … }`
with pointer semantics — omitted = unchanged, at least one field required (else
`400`) — and returns `200` with the updated row. The `opencode_*` fields
configure where/how `POST /tasks/{id}/opencode/start` dispatches sessions for
this project (empty string clears the value).

### GET /projects/{id}/board (UI)

Whole project in one round-trip: open tasks grouped by status, canonical order
inside each column.

```json
{
  "project_id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",
  "counts": { "todo": 2, "doing": 1, "done": 1 },
  "tasks": {
    "todo":  [ /* summary rows */ ],
    "doing": [ /* summary rows, incl. ac_total / ac_done / comment_count */ ],
    "done":  [ /* summary rows */ ]
  }
}
```

All three columns always present (`[]` when empty). `404` for unknown project.
Each summary row here additionally carries the card-badge fields — e.g. a task
with 3 criteria (1 done) and 2 comments renders:

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "title": "Fix login bug",
  "status": "doing",
  "priority": 3,
  "ac_total": 3,
  "ac_done": 1,
  "comment_count": 2,
  "version": 4,
  "created_at": "2026-09-25T18:02:11Z",
  "updated_at": "2026-09-26T15:48:03Z"
}
```

A row with no criteria and no comments omits all three badge keys.

### GET /projects/{id}/events · GET /events — realtime SSE streams

Both endpoints stream the same project-scoped mutation feed over Server-Sent
Events (`Content-Type: text/event-stream`; headers `Cache-Control: no-cache`,
`X-Accel-Buffering: no`). Use whichever scoping is convenient; the web UI uses
the path form.

- `GET /api/v1/projects/{id}/events` — project from the path. Unknown project →
  `404 not found` (`resource: "project"`) before the stream opens.
- `GET /api/v1/events?project_id=…` — project from the query string; a missing
  or empty `project_id` → `400 invalid input`
  (`missing required query parameter "project_id"`).

Stream framing (both endpoints): an initial comment line `: connected`, then a
`: ping` comment every 15 seconds to keep idle streams alive through proxies.
Each mutation is one SSE event:

```
id: 01J3ZKQ5M2T8V4X6Y0Z2A4B6C8
event: status-changed
data: {"event_id":"…","project_id":"01J3YV2NMB4D9C6K8F0H3L5P7Q","task_id":"01J3ZK8WQ9X7M2P4R6S8T0U2V4","kind":"status-changed","status":"doing","actor":{"type":"agent","name":"runner-2"},"timestamp":"2026-09-26T15:48:03Z"}
```

The `data` payload omits `project_id`, `task_id`, `status`, and `actor` when
empty. The canonical event kinds are: `created`, `updated`, `status-changed`,
`claimed`, `released`, `heartbeat`, `assigned`, `noted`, `commented`,
`ac-toggled`, `ac-added`, `ac-updated`, `ac-removed`, `artifact-added`,
`relation-changed` (one event per mutation — the same feed as the task detail's
`events` tail, live).

SSE clients cannot set headers, so when auth is enabled the token rides on the
query string: `…/events?project_id=…&token=<CROWBAN_TOKEN>`.

### GET /tasks → `list_tasks`

Summary rows in **canonical order** (priority DESC → due ASC with undated last →
created ASC) plus the pagination envelope.

Query params (all optional; unknown params ignored; any malformed value → `400`):

| Param | Meaning |
|---|---|
| `project_id`     | restrict to one project (ULID string) |
| `status`         | exact match: `todo` \| `doing` \| `done` |
| `assignee_name`  | durable assignee NAME; alias: `assignee` |
| `assignee_type`  | `human` \| `agent` (combined with the name) |
| `archived`       | `true` lists archived tasks; default false = open only |
| `unblocked`      | `true` keeps only tasks with no open blocker |
| `search`         | case-insensitive substring over title+description+notes; alias: `q` |
| `priority`       | exact match, integer 0–5 |
| `due_before`     | RFC3339; due_at strictly **before** (undated never matches) |
| `due_after`      | RFC3339; due_at at or after (inclusive) |
| `created_after` / `created_before` | RFC3339 window over creation time |
| `completed_since` / `completed_after` (aliases of each other) and `completed_before` | RFC3339 completion window |
| `limit`          | default 20, max 100 (clamped); ≤ 0 → `400` |
| `offset`         | default 0; negative → `400` |

```json
{
  "tasks": [ { /* summary rows, canonical order */ } ],
  "total": 42,
  "limit": 20,
  "offset": 0
}
```

### POST /tasks → `create_task`

Request:

```json
{
  "actor": { "name": "eve", "type": "human" },
  "title": "Fix login bug",            // required; empty → 400
  "description": "Users cannot log in…",
  "project_id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",   // required; must exist (else 400)
  "priority": 3,                        // 0–5, default 0
  "due_at": "2026-10-01T00:00:00Z",     // RFC3339, optional
  "assignee": { "type": "human", "name": "ada" },
  "acceptance_criteria": ["login works", "logout works"]   // become seq 1..n
}
```

One transaction: task + initial ACs. `201` with the fresh summary row plus an
**advisory** (never an error) duplicate candidate list from the same project,
excluding the new task itself:

```json
{
  "task": { /* summary row, version 1 */ },
  "possible_duplicates": [ { "id": "…", "title": "fix login bug", "…": "…" } ]
}
```

### GET /tasks/{id} → `get_task`

The one-call resume payload (detail row above). Optional query params:
`comment_limit` (default 10, max 50), `event_limit` (default 20, max 100) —
larger values are clamped; negative/non-numeric → `400`. Unknown id → `404`.

### PATCH /tasks/{id} → `update_task`

Version-guarded partial update. **`version` is required** — the stored version
you last read (fresh tasks are version 1); missing/0 → `400`, stale → `409
version_conflict` carrying `current_version` + the fresh `current` row. The
check runs **inside the write transaction**, so a concurrent writer that lands
between your read and this request can never produce a lost update — you will
always see either `200` (your write won) or `409` with the winner's fresh row.
All other fields optional (omitted = unchanged):

```json
{
  "actor": { "name": "eve" },
  "version": 3,
  "status": "doing",           // validated against the transition map → 400 invalid input naming from→to when illegal
  "title": null,               // any of: title, description, status, priority (0–5),
  "priority": 2,
  "due_at": "2026-10-02T00:00:00Z",   // RFC3339 to set…
  "clear_due": true,           // …or clear the deadline
  "assignee": { "type": "human", "name": "ada" },  // durable ownership; or "clear_assignee": true
  "archived": false,
  "override_blockers": false   // true bypasses unresolved blocker gate when moving to done
}
```

`200` with the fresh summary row (version incremented). Status changes emit
events; this is the only field that touches `completed_at`.

Transitioning to `status: "done"` validates that:
1. The task has no open blockers (`blocked-by` targets that are not done and not archived). If open blockers exist and `override_blockers` is false, returns `409` (`{"error": "task blocked", "blockers": [...]}`).
2. All acceptance criteria on the task are satisfied (`done: true`). If any criterion is incomplete, returns `400` (`{"error": "invalid input"}`) with a message naming the count and seq of every unmet criterion — e.g. `"…has 2 incomplete acceptance criteria (seq 1, 2) — reopen or complete acceptance criteria first"`. **There is no override flag**: complete the criteria (or reopen a task that was completed with them left open). A task with zero criteria passes vacuously. The blocker gate is checked first when both would fire.


### POST /tasks/{id}/claim → `claim_task`

Acquires or renews the **lease** (the ephemeral `claimed_by`, never the durable
assignee). One atomic CAS: wins when the lease is free, expired (takeover —
including taking over your own zombie lease), or held by you (renewal); a
foreign active lease loses with `409 claim conflict` and nothing is mutated.

```json
{ "actor": { "name": "runner-2", "type": "agent" }, "ttl_sec": 1800 }
```

The request's `ttl_sec` field is optional: omitted or 0 → server default **900s**; negative → `400`; values above the cap are clamped to 3600s. On success:
`status→doing`, lease re-stamped to now / now+ttl, version bumped.

`200` with the fresh summary row (`claimed_by` and `claim_expires_at` set):

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "title": "Fix login bug",
  "status": "doing",
  "priority": 3,
  "claimed_by": { "type": "agent", "name": "runner-2" },
  "claim_expires_at": "2026-09-26T16:18:03Z",
  "version": 5,
  "created_at": "2026-09-25T18:02:11Z",
  "updated_at": "2026-09-26T15:48:03Z"
}
```

Idempotent for the current holder (a re-claim refreshes — retry-safe). Unknown
task → `404`.

### POST /tasks/{id}/release → `release_task`

```json
{ "actor": { "name": "runner-2", "type": "agent" }, "note": "stalled on the DB migration" }
```

**Holder-only against an active lease** — anyone else (or you, after expiry)
gets `409 claim conflict` naming the real holder. A task with **no lease at
all** is an idempotent no-op returning the fresh row (retry-safe). Success:
lease cleared, `status→todo`, version bumped; the optional `note` rides on the
released event (it is NOT posted as a comment). `200` fresh summary row.

### POST /tasks/{id}/heartbeat → `heartbeat_task`

```json
{ "actor": { "name": "runner-2", "type": "agent" } }
```

Keep-alive for the active self-holder: extends `claim_expires_at` by the
**stored** ttl. Deliberately **does not bump the version and does not touch
`updated_at`** — a heartbeat is not a state change, so it never invalidates a
working agent's read base. Foreign holder / expired lease → `409 claim
conflict`; **no lease at all** → `409 not claimed` (recovery: claim first).
The fresh row doubles as the liveness answer: `200` summary row.

### POST /tasks/{id}/comments → `add_comment`

```json
{ "actor": { "name": "ada" }, "kind": "progress", "body": "ran the build — green" }
```

`kind` ∈ **`progress` \| `question` | `decision` \| `handoff` \| `note`** (else
`400`); `body` is markdown and must be non-empty after trimming (else `400`).
Unknown task → `404`. Appending is a child-row mutation: **no task version
bump**, one `commented` event. `201` with the fresh comment row:

```json
{ "id": "01J3ZKQ5M2T8V4X6Y0Z2A4B6C8", "kind": "progress", "body": "ran the build — green", "actor": { "type": "human", "name": "ada" }, "created_at": "2026-09-26T15:47:55Z" }
```

Appends are **not** idempotent — a retry after a timeout may duplicate; check
the comment tail before re-posting after an error.

### GET /tasks/{id}/comments (UI)

Paged thread, **newest first**, standard envelope (`limit` default 20 max 100,
`offset` default 0; same policy as `list_tasks`). `404` for unknown task.

```json
{
  "comments": [ { "id": "…", "kind": "handoff", "body": "@bob picking this up next", "actor": { "type": "human", "name": "ada" }, "created_at": "…" } ],
  "total": 12,
  "limit": 20,
  "offset": 0
}
```

### PUT /tasks/{id}/notes → `update_notes`

```json
{ "actor": { "name": "eve" }, "notes": "Last run: migration half-applied.\nNext: verify tests.", "version": 7 }
```

**Replace-on-write of the whole blob.** The `version` field is **optional** —
notes are a single-writer resume scratchpad, and demanding a re-read before
every edit would only add round-trips:

- **Omitted (or 0):** the historical contract. A real change bumps the task
  version; **identical content is a no-op** that returns the fresh row without
  another bump (retry-safe). The use case still read-then-writes internally,
  so a genuinely *concurrent* write can lose its race and surface as
  `409 version_conflict` — rare, but handle it like any 409.
- **Provided:** optimistic guard, checked inside the write transaction. If the
  stored version is no longer that value, `409 version_conflict` with
  `current_version` + the fresh `current` row — a stale base conflicts even
  when the blob is identical. Retry with the current version (no re-read
  needed; it is in the body).

`200` with the fresh summary row (notes are visible via `get_task`). Unknown
task → `404`.

### PATCH /tasks/{id}/acceptance-criteria/{seq} → `complete_ac` · `edit_ac`

**Settled-parent rule (applies to EVERY criterion mutation on this and the
following AC endpoints — toggle, reword, add, remove):** while the parent task
is completed (`status: "done"`) or archived, all of them are refused with
`400` (`{"error": "invalid input"}`) naming the recovery —
`"…completed task <id> — reopen the task first"` / `"…archived task <id> —
unarchive it first"`. Nothing is written and no event is emitted. Reopening
(`done→todo`) or unarchiving restores full mutability. Note: re-checking an
ALREADY-DONE criterion on a done task is refused by design under this rule.

`{seq}` is the **1-based** criterion sequence in the URL. Non-numeric or < 1 →
`400`; unknown task **or** unknown seq → `404`.

Two shapes share the endpoint:

```json
// toggle (complete_ac): done sets/clears the checkbox, note records why
{ "actor": { "name": "ada" }, "done": true, "note": "manual test passed" }

// reword (edit_ac): "text" takes precedence — done/note are ignored in that case
{ "actor": { "name": "ada" }, "text": "login works (hardened)" }
```

Setting `done: true` records `completed_by`/`completed_at`; setting it back to
`false` clears both (uncheck is free). Rewording updates only the text (the
checkbox and completion fields are untouched); a reword to the IDENTICAL text
is a no-op — fresh row returned, no event. Both are child-row mutations: no
task version bump. `200` with the updated AC row:

```json
{ "seq": 1, "text": "login works", "done": true, "note": "manual test passed", "completed_by": { "type": "human", "name": "ada" }, "completed_at": "2026-09-26T15:40:12Z" }
```

### POST /tasks/{id}/acceptance-criteria → `add_ac`

Appends a new criterion to an existing task. The seq is minted server-side as
**max(seq)+1** — callers never choose it, and surviving seqs are never
renumbered (deletes leave gaps). `text` must be non-empty after trimming
(`400` otherwise); unknown task → `404`. Child-row mutation: one `ac-added`
event in the same transaction, no version bump. `201` with the fresh AC row:

```json
{ "actor": { "name": "ada" }, "text": "reset works" }
// → 201
{ "seq": 3, "text": "reset works", "done": false }
```

### DELETE /tasks/{id}/acceptance-criteria/{seq} → `remove_ac`

Removes exactly the criterion at the **1-based** `{seq}`. The body is optional
JSON but MUST carry a valid `actor` — a removal appends an `ac-removed` event
that records WHO removed it, so no-body/empty-actor → `400`. The deleted
criterion's seq + text ride on the event's `before` payload. Surviving criteria
keep their seq values — **no renumbering** (gaps stay). Unknown task or seq →
`404`. No version bump. `204` with no body:

```json
{ "actor": { "name": "ada" } }
```

### POST /tasks/{id}/artifacts → `attach_artifact`

```json
{ "actor": { "name": "runner-2", "type": "agent" }, "kind": "url", "ref": "https://example.com/reports/deploy.json", "summary": "deploy report for the fix" }
```

`kind` ∈ **`log` \| `screenshot` \| `command-output` \| `trace` \| `url` \|
`other`** (else `400`); `ref` is a URL or filesystem path and must be non-empty
after trimming (else `400`); `summary` is an optional one-liner. Unknown task →
`404`. Append-only, one `artifact-added` event, no version bump. `201` fresh
artifact row (same shape as in the detail payload). Appends are not idempotent.

**Testing-evidence convention:** the MCP `attach_artifact` tool description and
the OpenCode dispatch prompt recommend that agents attach testing evidence
(`command-output`/`log`/`screenshot`) after running tests or verification
commands, so a human can verify the work — encouraged, never required; the
endpoint itself stays neutral (no enforcement).

### POST /tasks/{id}/opencode/start (UI/ops)

Dispatches an autonomous OpenCode session that works the task end-to-end and
talks back to crowban over MCP. No request body. `200` with:

```json
{ "session_id": "ses_…", "session_url": "http://localhost:4444/session/ses_…" }
```

Server-side the handler (1) creates an OpenCode session — agent `build`, title
`CrowBan: <task title>`, model per the table below; (2) asynchronously dispatch
a prompt carrying the task's title, description and acceptance-criteria
checklist plus explicit instructions to use the CrowBan MCP tools and the
encouraged (not required) recommendation to attach testing artifacts as
evidence for human verification; (3) attaches
a `url` artifact on the task pointing at the session; (4) if the task is
unclaimed, claims it under `{ "type": "agent", "name": "opencode" }` with a
3600s TTL. Fire-and-forget: after `200` the session progresses on its own —
watch it through the task's events/comments or the attached artifact URL.

Each setting resolves project row → env var → default:

| Setting | Project field | Env var | Default |
|---|---|---|---|
| OpenCode server base URL | `opencode_server_url` | `OPENCODE_SERVER_URL` | `http://localhost:4444` |
| Session working directory | `opencode_directory` | `OPENCODE_DIRECTORY` | `/home/hollen/Code/crowban` |
| Model ref (`providerID/modelID`) | `opencode_model` | `OPENCODE_MODEL` | `llama.cpp/Qwen Medium Long Context` |
| Allowed host allowlist | - | `OPENCODE_ALLOWED_HOSTS` | (empty; default denies private/loopback/metadata) |
| Allow private IP networks | - | `OPENCODE_ALLOW_PRIVATE_IPS` | `false` |
| Allowed directories | - | `OPENCODE_ALLOWED_DIRECTORIES` | (empty; any clean absolute path allowed) |

Security & Validation:
- Server URL: requires `http` or `https` scheme. Targets to cloud metadata services (e.g. `169.254.169.254`, `metadata.google.internal`) and private/loopback IP ranges are blocked by default to prevent Server-Side Request Forgery (SSRF). Legitimate internal endpoints (such as `10.0.0.5:4444` or `localhost`) can be explicitly allowed via `OPENCODE_ALLOWED_HOSTS` or `OPENCODE_ALLOW_PRIVATE_IPS=true`.
- Working Directory: must be an absolute path without path traversal (`..`). If `OPENCODE_ALLOWED_DIRECTORIES` is set, the path must reside within an allowed directory.
- Response Leakage: non-2xx responses from upstream OpenCode servers sanitize errors and do not echo raw upstream response body snippets.

Errors: unknown task or project → `404`; malformed model ref (must look like
`"providerID/modelID"`), prohibited/malformed server URL, or path traversal directory → `400 invalid input`; unreachable
OpenCode server, non-2xx upstream response, or an unparsable session reply →
`502 {"error": "opencode_error", "message": "…"}`.

### POST /tasks/{id}/relations → `relate_tasks` (add)

```json
{ "actor": { "name": "eve" }, "other_id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "kind": "child" }
```

- `kind` ∈ **`child` \| `parent` \| `blocked-by` \| `duplicate-of` \| `related`**
  (else `400`). **`parent` is an input alias**: “{id} is a parent of
  {other_id}” is stored as the inverse child edge (`task_id = other_id,
  other_task_id = id, kind = "child"`). Every response row therefore always
  carries the *stored* kind — `"parent"` never appears on the wire.
- `other_id` must name an existing task **in the same project** (cross-project
  → `400`; unknown either end → `404`). A task cannot relate to itself (`400`).
- **Upsert semantics:** a brand-new edge → `201` with the stored row; re-adding
  an existing identical edge → `200` with that same stored row and **no event**
  (idempotent for retrying agents). A second, *different* parent → `409 parent
  conflict`; re-adding the *same* parent is the no-op above.

```json
{ "task_id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "other_task_id": "01J3YV2NMB4D9C6K8F0H3L5P7R", "kind": "child", "created_at": "2026-09-26T15:49:30Z" }
```

Edges are child rows: no task version bump, one `relation-changed` event. The
computed `blocked` flag (∃ outgoing `blocked-by` edge whose target is not done)
is derived from these edges at read time — never stored.

### DELETE /tasks/{id}/relations/{other_id}/{kind} → `relate_tasks` (remove)

Removes exactly the edge named by the **composite** — relation ids are never
on the wire, so agents don't have to remember them. `{kind}` accepts the same
tool-boundary values; removing `"parent"` deletes the same inverse child row as
removing `"child"` from the other side.

The body is optional JSON but **must carry a valid `actor`** when present — a
removal appends an event that records *who* removed it, so no-body/empty-actor
→ `400 invalid input` rather than a fabricated identity in the audit log:

```json
{ "actor": { "name": "eve" } }
```

`204 No Content` on success (no body). Missing edge → `404 not found`. Invalid
kind / self-relation / unknown task → `400`/`404` per the usual mapping.

## Version-bump policy (summary)

The task row's `version` bumps ONLY on task-row mutations: create,
`PATCH /tasks/{id}`, status changes, notes change, and claim/release lease
changes. Child-row mutations — comments, artifacts, AC toggles, relations,
events — never bump it, so a working agent holding a read can keep issuing
version-guarded writes without spurious 409s from someone else's chatter.
Heartbeats bump nothing at all.

## Idempotency / retry notes (for the MCP layer)

- Retry-safe: `claim` by the same actor (renewal), `release` on an unclaimed
  task, `PUT …/notes` with identical content, re-adding an existing relation.
- Fails loudly (never double-applies): version-guarded `PATCH /tasks/{id}` →
  `409` with the fresh row.
- **Not** idempotent: comment and artifact appends — a post-timeout retry may
  duplicate; read the tail before re-posting after an error.
- Error propagation for MCP: REST errors are surfaced to the LLM **verbatim**
  (the `message` field is written to be the recovery instruction); connection
  failure → “crowban-server unreachable at \<url\> — is it running?”.
