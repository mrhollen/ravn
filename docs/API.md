# Rävn REST API — stable contract

This document is the **durable contract** for the Rävn HTTP API. Both the
MCP container (`ravn-mcp`) and the web frontend implement against it: every
endpoint below is real, tested code in `internal/interface/http`. If this doc
and the code ever disagree, the tests win — but changes to either must update
this file in the same commit.

- **Base path:** `/api/v1` (all routes below are relative to it unless noted).
- **Transport:** JSON over HTTP only (`Content-Type: application/json`) except
  the SSE event streams, which are `text/event-stream`. Timestamps are RFC3339
  UTC (`2026-09-26T15:48:00Z`). IDs are ULID strings.
- **Auth:** optional and tiered, selected by the server's `RAVN_AUTH` setting
  (`off` | `token` | `multiuser`; defaults: token when `RAVN_TOKEN` is set, else
  off). The authoritative variable reference lives in
  [ARCHITECTURE.md §9 Multi-user & authentication](ARCHITECTURE.md#9-multi-user--authentication);
  the wire contract per tier follows.

  - **off** — nothing required; declared free-form actors flow through as
    self-declared identity (the historical zero-config LAN default).
  - **token** — one shared bearer (`RAVN_TOKEN`, env or `-token` flag) on every
    `/api/v1/*` route, sent as `Authorization: Bearer <token>`. Every admitted
    request acts as the privileged *operator* (optionally acting-as a declared
    name). Absent or mismatched token → `401`.
  - **multiuser** — each request's bearer is resolved in strict order: shared
    operator token → password-login **session token** → per-agent **api_token**
    (see [Multi-user authentication endpoints](#multi-user-authentication-endpoints)).
    Missing or unresolvable bearer → `401`
    (`"present a valid session token or api_token secret as the Bearer token"`).
    The only public route is `POST /auth/login`; every other `/auth/*` and all
    data routes require one of the three.

  Query string token authentication (`?token=<secret>`) is accepted **only** on
  `GET` requests for SSE streams (`/api/v1/events`, `/api/v1/projects/{id}/events`,
  or when `Accept: text/event-stream` is requested) due to browser EventSource
  limitations — in multiuser mode the query secret may be ANY of the three
  bearer kinds. All mutating endpoints (`POST`, `PUT`, `PATCH`, `DELETE`) and
  standard `GET` routes strictly require `Authorization: Bearer <secret>` and reject
  query string tokens with `401` to prevent token leakage in server access logs
  and HTTP referer headers. `/healthz` and the static UI are never gated in any
  tier. Root `GET /metrics` is auth-gated with the same `401` contract as
  `/api/v1/*` whenever a token exists (any tier) — **unless**
  `RAVN_PUBLIC_METRICS=true` explicitly opts it back in for public scraping
  (Prometheus clients that cannot send an `Authorization` header); with no
  `RAVN_TOKEN` at all it is open. See "Observability" below.

## Actor convention

Every mutation body carries an `actor` object — the declared identity of whoever
is acting. **In `off` and `token` tiers** this is self-declared (the server
trusts it, as in the single-tenant MVP). **In `multiuser`**, a request that
authenticated via a session or api_token credential acts authoritatively as its
OWN stored identity: the body's `actor` must still be present and well-formed,
but its NAME is overridden by the authenticated identity wherever it differs —
a declared actor naming someone else cannot attribute work or pass gates to
that name (anti-spoofing). The operator bearer keeps declared-actor behavior
(an operator may act-as a name):

```json
"actor": { "name": "ada", "type": "human" }
```

| Field | Rules |
|---|---|
| `name`  | **Required.** Non-empty after trimming; at most 100 runes; no control characters (newlines, tabs, …); must be well-formed UTF-8. This name is the lease/ownership key for claims — see "Lease-holder identity" below. |
| `type`  | Optional. `"human"` \| `"agent"`. **Defaults to `"human"`** when omitted (a bare name from a browser/curl is usually human; agents should send `"agent"` explicitly). Anything else → `400`. |

An invalid actor — empty/oversized/control-char name, unknown type — fails
with `400 invalid_input` on every mutation endpoint before anything is written.

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
`error` code is ONE **snake_case** token per condition (`claim_conflict`, not
`claim conflict`; `internal`, not `internal error`) — clients may match on it
exactly. The
`message` field doubles as the **agent-readable recovery hint** — the MCP layer
propagates these bodies verbatim, so treat `message` as instructions, not as a
log line. (Key order in real responses is sorted by key; examples are shown in
logical order.)

### 400 invalid_input

```json
{ "error": "invalid_input", "message": "comment body must not be empty" }
```

Emitted for: malformed JSON (`"malformed JSON body: …"`), missing path id,
bad actor, unknown kind/status/type values, out-of-range priority or limit,
non-RFC3339 timestamps, negative claim ttl, self-relations, cross-project
relation pairs, and any other use-case validation failure. No `resource`/`id`.

### 401 unauthorized

Only when auth is enabled (see Auth above). Two message variants by tier —
both carry `error: "unauthorized"`; match on the status, treat `message` as a
hint:

- **token / root `/metrics`** — absent or mismatched shared token, or query
  string token authentication attempted on a non-SSE or mutating endpoint:

  ```json
  { "error": "unauthorized", "message": "missing or invalid auth token" }
  ```

- **multiuser** — missing bearer, or a bearer that resolves to none of the
  three kinds (shared operator / session / api_token), on any gated route:

  ```json
  { "error": "unauthorized", "message": "present a valid session token or api_token secret as the Bearer token" }
  ```

  Credential-specific `401`s inside the auth endpoints themselves use their own
  unenumerable wording (e.g. login always answers `"invalid username or
  password"` for unknown AND wrong-password alike, so usernames cannot be
   enumerated) — see the endpoint sections below.

### 403 forbidden

Only meaningful when auth is enabled; four variants by cause:

- **Role denial** (multiuser + `RAVN_SCOPING=membership`) — authenticated but
  under-privileged for the operation on that project. `resource` names the type
  and `required` the minimum role, both machine-readable so clients can render
  upgrade hints without parsing the message:

  ```json
  { "error": "forbidden", "resource": "project", "required": "editor",
    "message": "this write operation requires at least the \"editor\" role in this project (you hold less or no relationship to it)" }
  ```

  Minimum roles: read → `viewer`, write → `editor`, destructive → `admin`.
- **Provisioning denied** — a credential principal (session/api_token) hit an
  operator-only route (`/auth/accounts`, `/auth/tokens`):

  ```json
  { "error": "forbidden", "message": "operator access required" }
  ```

- **Feature off** — any verb on the membership endpoints in a non-memember-scoped
  deployment (including `GET`; there is nothing to list). Deliberately 403, not
  404: the caller authenticated fine, the capability simply does not exist here:

  ```json
  { "error": "forbidden", "resource": "members", "reason": "membership_scoping_disabled",
    "message": "membership management requires RAVN_SCOPING=membership — …" }
  ```

- **Identity closed** — a credential principal tries to `POST /projects` while
  `RAVN_IDENTITY_OPEN=strict`:

  ```json
  { "error": "forbidden", "resource": "project",
    "message": "creating projects requires the shared operator token or a resolved enabled identity (identity-open deployments)" }
  ```

### 404 not_found

```json
{
  "error": "not_found",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "task \"01J3ZK8WQ9X7M2P4R6S8T0U2V4\" does not exist — check the id (use the list endpoint)"
}
```

`resource` is `"task"`, `"project"` (or `"comment"`-family where applicable).
A `PATCH …/acceptance-criteria/{seq}` with an unknown seq also returns 404 in
this shape (`resource: "task"`, the task id echoed).

### 409 version_conflict

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

### 409 claim_conflict

Returned by `claim` / `release` / `heartbeat` on a coordination collision. The
holder + expiry name who holds the lease and when it lapses — **not** an
instruction to wait for them:

```json
{
  "error": "claim_conflict",
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
conflict; see `409 not_claimed` below.

> [!NOTE]
> **For agents — holder-gated vs version-guarded.** Only the *lease* operations
> (`claim`, and the holder-only `release`/`heartbeat`) are gated on holding the
> active lease. All other task mutations — comments, notes, status, priority,
> due date, assignee, archived, acceptance criteria — are **version-guarded only**
> (optimistic concurrency via `version`) and are open to non-holders. A `409
> claim_conflict` therefore never requires sleeping, polling, or waiting for the
> lease to expire: if you have explicit permission (from the holder or a human)
> to work that task, proceed with those version-guarded mutations directly;
> otherwise stop and report. Idling until `expires_at` is never correct.

### 409 not_claimed

Returned by `heartbeat` when the task has no lease at all (there is nobody to
wait for — the recovery instruction is “claim it first”). Same 409 family as
the claim conflicts so clients branch on status and handle both together; the
body tag names the real state.

```json
{
  "error": "not_claimed",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "not claimed: task 01J3ZK8WQ9X7M2P4R6S8T0U2V4 has no claim lease — call claim first"
}
```

### 409 parent_conflict

One task may have at most one parent. Adding a *second, different* parent:

```json
{
  "error": "parent_conflict",
  "resource": "task",
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "message": "task already has a parent: task 01J3ZK8WQ9X7M2P4R6S8T0U2V4 already has a parent (01J3YV2NMB4D9C6K8F0H3L5P7R)"
}
```

Re-adding the *same* parent is not an error — it is the idempotent no-op
described under relations.

### 415 unsupported_media_type

Returned when a request body is supplied with a missing or mismatched `Content-Type` header (must be `application/json`):

```json
{ "error": "unsupported_media_type", "message": "Content-Type must be application/json" }
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

Two more board-only fields — `last_comment_kind` (string, e.g. `"question"` or
`"handoff"`) and `last_comment_actor` (`{"type","name"}`) — identify the kind
and poster of the task's **most recent comment** (max `created_at`; same-
timestamp ties break by insertion order). They let board cards render
question/handoff badges without fetching the thread, and are omitted entirely
when the task has no comments. Like the other badge fields they appear only on
board-snapshot rows and come from the same single grouped comment-table query
that fills `comment_count`.

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
| `GET  /metrics`                       | in-memory metrics, Prometheus text or JSON (outside `/api/v1`; bearer-gated like the API when a token is set unless `RAVN_PUBLIC_METRICS=true`) | — (ops) |
| `GET  /api/v1/metrics`                | same metrics under the API prefix (auth-gated when a token is set; always gated, unaffected by `RAVN_PUBLIC_METRICS`)          | — (ops) |
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
| `POST /tasks/{id}/handoff`            | atomic lease-conditional handoff (notes/assignee + one comment + release) → 200 {task,comment} | `handoff_task` |
| `POST /tasks/{id}/comments`           | append a typed comment → 201                             | `add_comment` |
| `GET  /tasks/{id}/comments`           | paged comment thread + total (UI)                        | — |
| `PUT  /tasks/{id}/notes`              | replace the notes blob                                   | `update_notes` |
| `POST /tasks/{id}/acceptance-criteria`            | append a new AC at next seq → 201          | `add_ac` |
| `PATCH /tasks/{id}/acceptance-criteria/{seq}`     | toggle one AC, or reword it via `text`    | `complete_ac`, `edit_ac` |
| `DELETE /tasks/{id}/acceptance-criteria/{seq}`    | remove one AC → 204 (actor required)      | `remove_ac` |
| `POST /tasks/{id}/artifacts`          | attach evidence → 201                                    | `attach_artifact` |
| `POST /tasks/{id}/opencode/start`     | dispatch an autonomous OpenCode session → 200 session id + url (UI/ops) | — |
| `POST /admin/backup`                  | zero-downtime hot backup (`VACUUM 'main' INTO`) into a base-dir-restricted path; never overwrites (ops) | — (ops) |
| `POST   /tasks/{id}/relations`        | add a relation edge (upsert)                             | `relate_tasks` (add) |
| `DELETE /tasks/{id}/relations/{other_id}/{kind}` | remove a relation edge                | `relate_tasks` (remove) |
| `POST /auth/login`                    | exchange username+password for a session token (public in multiuser) → 200 {token, expires_at, identity} | — (auth, multiuser) |
| `GET  /auth/session`                  | inspect the presenting session bearer → 200 {identity, expires_at}; 401 when it isn't a live session | — (auth) |
| `DELETE /auth/session`                | revoke the presenting session → 204 (idempotent)         | — (auth) |
| `POST /auth/accounts`                 | create or reset a human account (operator-only) → 201/200 | — (auth, provisioning) |
| `POST /auth/tokens`                   | mint an api_token for a named identity (operator-only) → 201, secret shown once | — (auth, provisioning) |
| `GET  /auth/tokens?name=…`            | list token metadata for one identity (operator-only)     | — (auth, provisioning) |
| `DELETE /auth/tokens/{id}`            | disable a token by durable id (operator-only) → 204 (idempotent) | — (auth, provisioning) |
| `GET    /projects/{id}/members`       | stored members + derived owner; `RAVN_SCOPING=membership` only (any visible member may read) | — (membership) |
| `POST   /projects/{id}/members`       | add or re-grant a member {username, role} → 201/200; admin-or-owner gated | — (membership) |
| `PATCH  /projects/{id}/members/{username}` | set an existing member's role → 200                 | — (membership) |
| `DELETE /projects/{id}/members/{username}` | remove the stored membership row → 204; hierarchy + last-admin guards apply | — (membership) |

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

- Root `/metrics` lives outside the API prefix (ops-only, not part of the
  durable agent contract) but carries the **same `401` bearer gate as the API**
  whenever `RAVN_TOKEN` is set — unauthenticated scrapes receive
  `401 {"error":"unauthorized","message":"missing or invalid auth token"}`, so
  task statistics and internal request counts are not leaked on public
  deployments. Authenticated scrape:
  `curl -H "Authorization: Bearer $RAVN_TOKEN" http://<host>:<port>/metrics`.
- Set `RAVN_PUBLIC_METRICS=true` (env) to keep root `/metrics` publicly
  scrapable even with a token set — for Prometheus clients that cannot send an
  `Authorization` header. Default (unset/false): gated whenever a token is set.
  When `RAVN_TOKEN` is empty, neither route requires auth (zero-config local
  default, unchanged).
- `GET /api/v1/metrics` is under the prefix and takes the same `401` gate as
  everything else when a token is configured — it stays gated even with
  `RAVN_PUBLIC_METRICS=true` (the opt-in only affects root `/metrics`).

Series: `http_requests_total`, `http_requests_in_flight`,
`http_requests_by_status{code}`, `http_requests_by_method{method}`,
`http_request_duration_seconds_total`, `process_uptime_seconds` (Prometheus) /
`uptime_seconds` (JSON), plus live DB counts `tasks_by_status{status}` and
`active_claims` (omitted from JSON when the DB callback is unwired). The JSON
body is a flat snapshot object with one key per series.

### Multi-user authentication (`/auth/*`)

Available whenever the server boots with a database (auth is always wired then);
the provisioning routes additionally require a **shared-operator** bearer — a
credential principal (session/api_token) gets `403 {"error":"forbidden","message":
"operator access required"}` on all four of them, so per-user tokens can never
provision accounts or mint other tokens. When the auth use case is unwired
(no-database deployments) every `/auth/*` route answers `404 {"resource":"…",
"message":"authentication is not enabled"}`.

The `identityDTO` shared by these responses:

```json
{ "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4", "name": "ada", "kind": "human",
  "auth_method": "password", "created_at": "2026-10-01T09:00:00Z" }
```

`kind` is `"human"` | `"agent"`; `auth_method` is `"password"` (login-able) or
`"api_token"` (neutral, token-only).

### POST /auth/login → session token

The only **public** route in multiuser mode. Exchanges a username + password for
a fresh opaque session token (only its hash is stored — the secret is shown
exactly once, on this response):

Request: `{ "username": "ada", "password": "s3cret!" }`

```json
{ "token": "Kx7vQ…(32-byte base64url, no prefix)", "expires_at": "2026-10-02T09:15:04Z",
  "identity": { /* identityDTO */ } }
```

All three bearer kinds (operator / session / api_token) are the same shape —
32 random bytes, base64url without padding — and carry no type prefix; the server
resolves a presented secret by lookup order, never by inspection of the value.

`200`. `400 invalid_input` on empty username. **Unknown AND wrong-password
credentials are indistinguishable**: both →
`401 {"error":"unauthorized","message":"invalid username or password"}` so
usernames cannot be enumerated (disabled identities likewise). The session TTL
is set by `RAVN_SESSION_TTL` (default 24h); the returned token is usable as the
Bearer secret on every gated route, including SSE via `?token=`, and as the
principal for all scoping/attribution decisions.

### GET /auth/session → inspect presenting session

`200 {"identity": {…}, "expires_at": "…"}` when the bearer **is** a live session
token; otherwise `401`. An operator or api_token bearer has no session — that is
the expected `401 {"message":"no active session for this token"}`, not an error.

### DELETE /auth/session → revoke presenting session

`204`, idempotent: a valid, unknown, or already-revoked token all succeed
(a replayed logout never errors).

### POST /auth/accounts → create or reset an account (operator-only)

Request: `{ "name": "ada", "kind": "human|agent"?, "password": "s3cret!" }`
(`kind` optional, default `"human"`; `password` min 8 chars; empty name / bad
kind / short password → `400`).

```json
{ "identity": { /* identityDTO */ }, "created": true }
```

`201` on create (`created: true`), `200` when the account already exists and its
password is **reset** (`created: false`) — the idempotent form of provisioning a
known name. There is no rename/disable/delete route in this release; reset the
password to rotate, and manage per-project access through membership instead.

### POST /auth/tokens → mint an api_token (operator-only)

Request: `{ "name": "builder-agent", "kind": "human|agent"?, "label": "ci-deploy" }`
— mints a long-lived API token for the named identity, **creating it as a
neutral (no-password) row in the same transaction if absent** (`kind` then
defaults to `"human"`, so pass `"agent"` explicitly for agent tokens). Empty
name or bad kind → `400`. The secret is shown exactly once:

```json
{ "token": "Kx7vQ…(same 32-byte base64url shape as session secrets)",
  "meta": { "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V5", "identity_id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
            "label": "ci-deploy", "created_at": "…", "last_used_at": null, "disabled": false } }
```

`201`. Present the `token` value as the Bearer secret; it authenticates as that
identity on every request (subject to the same scoping gates as any session).

### GET /auth/tokens?name=… → list token metadata (operator-only)

`200` with an array of `meta` rows for the named identity (disabled included),
empty array when it has none; unknown identity → `404`. Secrets never appear —
only `id`, `identity_id`, `label`, timestamps, `disabled`.

### DELETE /auth/tokens/{id} → disable a token (operator-only)

`204`, idempotent (already-disabled is a no-op). Unknown or empty id → `404`/`400`.
Disabling is the only deletion: the durable row stays for audit.

### Project membership endpoints (`/projects/{id}/members`)

Gated to deployments booted with `RAVN_SCOPING=membership`; every other
deployment refuses **all four verbs, including GET**, with
`403 {"resource":"members","reason":"membership_scoping_disabled",…}` (machine-
readable `reason` so clients can detect "feature off"). Gate order per verb:
bad path id → `400`; scoping off → that `403`; feature unwired → `404 not
enabled`; unknown project → `404 not_found`; visibility/role denial → `403`
(`required` field). `GET` sits at the **read line** (any visible member may read
the roster); `POST`/`PATCH`/`DELETE` require **admin-or-owner** — or the shared
operator, which passes every gate and is exempt from both use-case guards.

The request bodies accept an OPTIONAL `actor` field for wire compatibility with
shared client code: it is validated (malformed → `400`) and then **ignored** —
these verbs always act as the authenticated principal, and member rows store no
attribution. Roles are case-insensitive; `"owner"` is refused by name (`400`:
ownership derives from `created_by` and is not assignable).

#### GET /projects/{id}/members

```json
{ "owner": { "type": "human", "name": "ada" },
  "members": [ { "username": "builder-agent", "role": "editor" } ] }
```

`owner` is **derived** from the project's `created_by`, not a stored row — it is
`null` for legacy unattributed projects. `members` are the stored rows sorted by
username; an owner who also holds a stored row appears in both places (their role
column shows the STORED role).

#### POST /projects/{id}/members → add or re-grant

Request: `{ "username": "ada", "role": "viewer|editor|admin" }` (both required;
the name must be an **existing enabled** identity — no auto-provisioning: unknown
→ `404 {"resource":"identity",…}` pointing at `POST /auth/accounts`).

```json
{ "username": "ada", "role": "editor", "created": true }
```

`201 created:true` for a fresh row; `200 created:false` on re-grant (idempotent,
same or different role). Guards: granting/demoting **at or below your own role**
→ `403 {"resource":"members"}` hierarchy refusal (`ErrCannotAssign`); disabling an
identity → `403` on Grant/PATCH (DELETE of its row still works).

#### PATCH /projects/{id}/members/{username} → set role

Request: `{ "role": "viewer|editor|admin" }`. Existing member only — a target
with no stored row → `404 {"resource":"member","message":"…not a member of this
project…"}` (use POST to add). `200` echoing the normalized username + canonical
role string. Same guards as POST, including `409 {"resource":"members"}` when the
move would leave an ownerless project with **no admins left** (`ErrLastAdmin`).

#### DELETE /projects/{id}/members/{username} → remove row

`204`, no body. Removes the stored membership (a disabled identity's row is
explicitly removable); non-member or unknown name → distinct `404`s. The last-
admin guard applies here too. Removing the **owner** never fails — ownership is
derived and survives; they simply lose their member-row privileges.

### GET /projects → `list_projects`

Lists projects, newest-first, with per-status **open** task counts (all three
statuses always present, zero when empty). Not paged in the MVP.

Query params:
- `archived`: `true` lists archived projects; default `false` = open only.
- `mine=created` (multi-user epic T4): restrict to projects the acting identity
  **created**. A credential principal filters by its own stored identity id; a
  free-form actor in token/off modes that has no identity row resolves to an
  honest empty list (legacy rows are unattributed). Any other `mine` value →
  `400`.

Visibility: in the default configuration every caller sees every project. Under
`RAVN_SCOPING=membership`, a credential principal sees only projects it created
or has been added to as a member; requests with no resolvable identity are
fail-closed (structurally empty list). The shared operator always sees all.

```json
{
  "projects": [
    {
      "id": "01J3YV2NMB4D9C6K8F0H3L5P7Q",
      "name": "Website Redesign",
      "description": "MVP landing page work",
      "opencode_server_url": "http://localhost:4444",
      "opencode_directory": "/workspace",
      "opencode_model": "",
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

Multi-user (T4): a credential principal (session/api_token) stamps durable
`created_by` ownership of what it creates in **every** scoping mode — attribution
is a property of who authenticated, not of the visibility policy. Under
`RAVN_SCOPING=membership`, whether such a principal may create projects at all is
governed by `RAVN_IDENTITY_OPEN`: open (default) allows; `strict` → `403`
(identity closed). Operator and off-mode creates stay unattributed, exactly as
before.

### GET /projects/{id} (UI) · PUT /projects/{id} (UI)

Get returns the project row; `404 not_found` (`resource: "project"`) when
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
    "doing": [ /* summary rows, incl. ac_total / ac_done / comment_count / last_comment_kind / last_comment_actor */ ],
    "done":  [ /* summary rows */ ]
  }
}
```

All three columns always present (`[]` when empty). `404` for unknown project.
Each summary row here additionally carries the card-badge fields — e.g. a task
with 3 criteria (1 done) and 2 comments whose most recent is a question renders:

```json
{
  "id": "01J3ZK8WQ9X7M2P4R6S8T0U2V4",
  "title": "Fix login bug",
  "status": "doing",
  "priority": 3,
  "ac_total": 3,
  "ac_done": 1,
  "comment_count": 2,
  "last_comment_kind": "question",
  "last_comment_actor": { "type": "agent", "name": "runner-2" },
  "version": 4,
  "created_at": "2026-09-25T18:02:11Z",
  "updated_at": "2026-09-26T15:48:03Z"
}
```

A row with no criteria and no comments omits all five badge keys.

### GET /projects/{id}/events · GET /events — realtime SSE streams

Both endpoints stream the same project-scoped mutation feed over Server-Sent
Events (`Content-Type: text/event-stream`; headers `Cache-Control: no-cache`,
`X-Accel-Buffering: no`). Use whichever scoping is convenient; the web UI uses
the path form.

- `GET /api/v1/projects/{id}/events` — project from the path. Unknown project →
  `404 not_found` (`resource: "project"`) before the stream opens.
- `GET /api/v1/events?project_id=…` — project from the query string; a missing
  or empty `project_id` → `400 invalid_input`
  (`missing required query parameter "project_id"`).

Stream framing (both endpoints): an initial comment line `: connected`, then a
`: ping` comment every 15 seconds to keep idle streams alive through proxies.
Each mutation is one **default (unnamed) SSE message frame** — an `id:` line
plus ONE `data:` JSON payload, with no `event:` line. The routing field is the
payload's `kind` member:

```
id: 01J3ZKQ5M2T8V4X6Y0Z2A4B6C8
data: {"event_id":"…","project_id":"01J3YV2NMB4D9C6K8F0H3L5P7Q","task_id":"01J3ZK8WQ9X7M2P4R6S8T0U2V4","kind":"status-changed","status":"doing","actor":{"type":"agent","name":"runner-2"},"timestamp":"2026-09-26T15:48:03Z"}
```

Why unnamed frames: per the WHATWG EventSource spec a frame carrying
`event: <name>` is delivered only to listeners registered for that exact name —
`onmessage` never fires for it. Named framing therefore forced every client to
whitelist each kind, and any server-side addition of a new kind was silently
dropped by clients written before it (desync until the next poll). With
unnamed frames one `onmessage` handler receives every event and dispatches by
payload.kind, so a new kind keeps the UI in sync without a client code change.
(Pre-fix server builds emitted `event: <kind>` lines; the web client still
registers named listeners for the canonical kinds below as defense-in-depth
for mixed-version deploys.)

The `data` payload omits `project_id`, `task_id`, `status`, and `actor` when
empty. The canonical event kinds are: `created`, `updated`, `status-changed`,
`claimed`, `released`, `heartbeat`, `assigned`, `noted`, `commented`,
`ac-toggled`, `ac-added`, `ac-updated`, `ac-removed`, `artifact-added`,
`relation-changed` (one event per mutation — the same feed as the task detail's
`events` tail, live). The stream is not constrained to this set: whatever kind
a mutation emits is delivered verbatim in the payload.

SSE clients cannot set headers, so when auth is enabled the secret rides on the
query string: `…/events?project_id=…&token=<secret>` — in multiuser mode that
may be ANY of the three bearer kinds (operator / session / api_token), resolved
in the same order as the `Authorization` header. Note the feed covers task- and
AC-shaped mutations only: membership grants/removals and auth provisioning emit
no events.

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
| `mine`           | `assigned` \| `created` \| `activity` — restrict to tasks assigned to / created by / touched by the acting identity (T4). `assigned` alongside an explicit `assignee*` filter → `400` (ambiguous) |
| `limit`          | default 20, max 100 (clamped); ≤ 0 → `400` |
| `offset`         | default 0; negative → `400` |

`mine` resolves against the **acting** identity: a credential principal always
its own stored name/id (a free-form actor cannot fake someone else's work); a
token/off-mode request its declared actor. In every mode a name with no history
yields an honest empty list — never an error. Visibility is otherwise governed
by the same membership-scoping rules as `GET /projects` (tasks have no
independent roles; everything derives from the project relationship).

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
  "status": "doing",           // validated against the transition map → 400 invalid_input naming from→to when illegal
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
1. The task has no open blockers (`blocked-by` targets that are not done and not archived). If open blockers exist and `override_blockers` is false, returns `409` (`{"error": "task_blocked", "blockers": [...]}`).
2. All acceptance criteria on the task are satisfied (`done: true`). If any criterion is incomplete, returns `400` (`{"error": "invalid_input"}`) with a message naming the count and seq of every unmet criterion — e.g. `"…has 2 incomplete acceptance criteria (seq 1, 2) — reopen or complete acceptance criteria first"`. **There is no override flag**: complete the criteria (or reopen a task that was completed with them left open). A task with zero criteria passes vacuously. The blocker gate is checked first when both would fire.


### POST /tasks/{id}/claim → `claim_task`

Acquires or renews the **lease** (the ephemeral `claimed_by`, never the durable
assignee). One atomic CAS: wins when the lease is free, expired (takeover —
including taking over your own zombie lease), or held by you (renewal); a
foreign active lease loses with `409 claim_conflict` and nothing is mutated.

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
gets `409 claim_conflict` naming the real holder. A task with **no lease at
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
conflict`; **no lease at all** → `409 not_claimed` (recovery: claim first).
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
`400` (`{"error": "invalid_input"}`) naming the recovery —
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
talks back to Rävn over MCP. No request body. `200` with:

```json
{ "session_id": "ses_…", "session_url": "http://localhost:4444/session/ses_…" }
```

Server-side the handler (1) creates an OpenCode session — agent `build`, title
`Rävn: <task title>`, model per the table below; (2) asynchronously dispatch
a prompt carrying the task's title, description and acceptance-criteria
checklist plus explicit instructions to use the Rävn MCP tools and the
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
| Session working directory | `opencode_directory` | `OPENCODE_DIRECTORY` | `/workspace` |
| Model ref (`providerID/modelID`) | `opencode_model` | `OPENCODE_MODEL` | (empty — no model sent; server default) |
| Allowed host allowlist | - | `OPENCODE_ALLOWED_HOSTS` | (empty; default denies private/loopback/metadata) |
| Allow private IP networks | - | `OPENCODE_ALLOW_PRIVATE_IPS` | `false` |
| Allowed directories | - | `OPENCODE_ALLOWED_DIRECTORIES` | (empty; any clean absolute path allowed) |

Security & Validation:
- Server URL: requires `http` or `https` scheme. Targets to cloud metadata services (e.g. `169.254.169.254`, `metadata.google.internal`) and private/loopback IP ranges are blocked by default to prevent Server-Side Request Forgery (SSRF). Legitimate internal endpoints (such as `10.0.0.5:4444` or `localhost`) can be explicitly allowed via `OPENCODE_ALLOWED_HOSTS` or `OPENCODE_ALLOW_PRIVATE_IPS=true`.
- Working Directory: must be an absolute path without path traversal (`..`). If `OPENCODE_ALLOWED_DIRECTORIES` is set, the path must reside within an allowed directory.
- Response Leakage: non-2xx responses from upstream OpenCode servers sanitize errors and do not echo raw upstream response body snippets.

Errors: unknown task or project → `404`; malformed model ref (must look like
`"providerID/modelID"`), prohibited/malformed server URL, or path traversal directory → `400 invalid_input`; unreachable
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
→ `400 invalid_input` rather than a fabricated identity in the audit log:

```json
{ "actor": { "name": "eve" } }
```

`204 No Content` on success (no body). Missing edge → `404 not_found`. Invalid
kind / self-relation / unknown task → `400`/`404` per the usual mapping.

### POST /admin/backup (ops) — zero-downtime hot backup

Writes a consistent point-in-time copy of the whole database via SQLite's
`VACUUM 'main' INTO '<file>'` while the server keeps serving. The result is ONE
self-contained rollback-journal file (no `-wal`/`-shm` sidecars) — restore by
copying it and reopening with any SQLite tool. Never use this to overwrite an
existing file, and never naively copy the live `ravn.db` in WAL mode instead.

Body:

```json
{ "dest": "/data/backups/ravn-20261108.db" }
```

- `dest` is required and **must be absolute**. It must resolve inside the
  allowed base dir: env `RAVN_BACKUP_DIR`, default `<data dir>/backups`
  (`/data/backups` on the volume in Docker). Escape attempts — paths outside
  the base, `..` traversal, relative paths, NUL bytes, and symlinks that lead
  outside the base — all → `400 invalid_input`. Parent directories are created.

| Outcome | Status & body |
|---|---|
| success | `200 { "path": "<dest>", "bytes": <size> }` |
| dest missing, relative, or outside/escaping the base | `400 { "error": "invalid_input", "message": … }` |
| file/dir already exists at `dest` | `409 { "error": "conflict", "message": "backup target already exists: <dest>" }` (file preserved untouched) |
| VACUUM INTO itself fails | `500 { "error": "internal", … }` — detail in the server log |
| token set, absent/mismatched | standard `401 unauthorized` gate (like every `/api/v1` route) |

```bash
curl -sS -X POST -H "Authorization: Bearer $RAVN_TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"dest":"/data/backups/ravn-20261108.db"}' \
     http://<host>:<port>/api/v1/admin/backup
```

Ops wrappers: `scripts/backup.sh` (`make backup`) drives this endpoint when
`RAVN_API_URL` is set (falling back to the one-shot CLI flag otherwise) and
verifies the result with `PRAGMA integrity_check`; bare CLI is
`ravn-server -data <db> -backup <dest>` / `-integrity-check <file>` — see
README "Backups".

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
  failure → “ravn-server unreachable at \<url\> — is it running?”.
