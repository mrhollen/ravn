# crowban — Technical Architecture (MVP)

Status: final blueprint, 2026-09-25. Supersedes: nothing; complements `docs/research/agent-workflow-feature-analysis.md` (feature decisions are frozen there and this doc implements them). Primary users: AI agents via MCP. Secondary users: humans via a minimal web UI.

---

## 1. Final stack decision

Go for **both** services, one language, one single Go module producing two small static binaries (`cmd/crowban-server`, `cmd/crowban-mcp`). Target **Go ≥ 1.22 features only** (we rely solely on the 1.22 `net/http` ServeMux method+wildcard routing); pin the Docker build to `golang:1.26-alpine` (any current 1.x stable works). SQLite via **`modernc.org/sqlite`** (pure Go, no CGO) → fully static binaries (`CGO_ENABLED=0`), trivial multi-arch builds, no libsqlite3 in the image. The MCP binary uses **`github.com/mark3labs/mcp-go` v1.x** over the **Streamable HTTP transport in stateless mode**, mounted on stdlib `net/http`. Routing uses **stdlib `net/http.ServeMux` only — no chi**: the surface is ~20 fixed routes on two binaries; 1.22 mux patterns (`GET /api/v1/tasks/{id}`) give method+wildcard routing, automatic 405s with `Allow`, and zero middleware machinery we actually need (logging, recover, and token auth are ≤ 60 lines of stdlib code). Frontend: **Vite + TypeScript + Tailwind CSS v4 + Alpine.js**, built at image-build time and served as static files by the Go server.

Accepted tradeoffs (deliberate):
- Pure-Go SQLite runs ~1.3–2× slower than a C build on CPU-bound queries (vendor-measured). Irrelevant at crowban's scale (low QPS, single writer), provided we index every `ORDER BY` column and cap the DB pool (`SetMaxOpenConns(1)` — see §4).
- No router library means slightly more boilerplate in one `routes.go`. Accepted.
- stdlib HTTP has no built-in request logging middleware; we write three small ones with `log/slog`. Accepted (slog is stdlib since 1.21).
- One module for both binaries instead of two (see §2) — accepted because the dependency *enforcement* that matters to us lives in package layout, not module boundaries.

---

## 2. Onion layout

**Single Go module.** Repo root = module root; module path = your repo URL (shown below as `github.com/hollen/crowban` — rename if different). The MCP binary never touches SQLite or use-cases: it is a pure protocol adapter that translates MCP tool calls into REST calls to the main server. A single module lets both binaries share the domain vocabulary (status names, relation kinds, actor types) and one `go.sum`, with no release-cadence divergence; two modules would only add path ceremony for zero isolation benefit at this scale.

**Dependency rule (strict, enforceable by review/`depguard` later):**
- `domain/` imports **Go stdlib only**. No third-party packages, no other project packages.
- `application/` imports `domain/` + **ports it defines itself** (`application/ports.go`). No persistence, no HTTP, no MCP, no stdlib I/O beyond `context/time/errors/fmt`.
- `infrastructure/**` imports `domain/` + `application/` (to implement ports). **All third-party imports in the repo live here** (SQLite driver, ULID) or in `cmd/` composition roots.
- `api/` (REST adapter) and `mcp/` (MCP adapter) import `application/` + `domain/` — **never** `infrastructure/`.
- `cmd/*` (composition roots) is the only place that imports everything and wires concrete implementations into ports.

```
crowban/                              # go.mod: module github.com/hollen/crowban
├── Makefile
├── Dockerfile                        # one multi-stage file, 3 build stages + 2 runtime targets (see §8)
├── docker-compose.yml
├── .dockerignore
├── .env.example                      # CROWBAN_TOKEN=...
├── docs/ARCHITECTURE.md
│
├── domain/                           # CORE. stdlib only (time, errors, strings, sort). No I/O, no DB, no HTTP.
│   ├── entity.go                     # Project, Task, AcceptanceCriterion, Comment, Artifact, Relation — plain structs, value semantics
│   ├── status.go                     # workflow statuses as NAMED DATA: names ("todo","doing","done") + allowed-transition map; ValidateStatus/CanTransition helpers (data-driven, no hard enum)
│   ├── actor.go                      # Actor{Type "human"|"agent", Name string}; Agent/Human type constants
│   ├── relation.go                   # relation kinds as named constants: parent, child, blocked-by, duplicate-of, related
│   ├── errors.go                     # sentinel + structured errors: ErrNotFound, ConflictError{Current}, ClaimConflictError{Holder, ExpiresAt}, TransitionError…
│   └── order.go                      # canonical ordering comparator (priority desc → due asc nulls-last → created asc) + normalization for duplicate detection
│   └── *_test.go                     # pure unit tests: ordering incl. nulls-last, transition rules, normalization
│
├── application/                      # USE-CASES. imports domain only (+ stdlib). Ports are defined HERE (dependency inversion).
│   ├── ports.go                      # interfaces: ProjectRepo, TaskRepo, AcceptanceRepo, CommentRepo, ArtifactRepo, RelationRepo, EventStore, Clock, IDGen  ← every use case depends only on these
│   ├── projects.go                   # CreateProject, ListProjects (with per-status counts), GetProject, UpdateProject
│   ├── tasks.go                      # ListTasks (filters+canonical order+pagination), GetTask (one-call resume payload: task+ACs+comment tail+artifacts+relations+computed blocked+event tail), CreateTask (dup warning + optional parent/relations in one tx), UpdateTask (version-guarded, transition-validated)
│   ├── claim.go                      # ClaimTask (atomic CAS w/ TTL lease; sets status→doing; idempotent re-claim by same actor = lease refresh), ReleaseTask (holder-only; →todo; optional note comment), HeartbeatTask (extends stored TTL; no version bump)
│   ├── notes_comments.go             # UpdateNotes (replace-on-write, version-guarded), AddComment (typed kind, actor+ts)
│   ├── acceptance.go                 # ToggleAcceptanceCriterion (by 1-based seq, optional note), AddAC (append at next seq), EditACText (reword; identical = no-op), RemoveAC (delete; seqs keep their numbers)
│   ├── artifacts.go                  # AttachArtifact (typed kind + ref + one-line summary)
│   ├── relations.go                  # RelateTasks (upsert; same-project rule; one-parent rule), UnrelateTasks, computed blocked flag = ∃ outgoing blocked-by edge whose target is not done
│   └── *_test.go + fakes_test.go     # in-memory fake repos implementing ports + a controllable FakeClock; every use case tested without SQLite or HTTP (this is the highest-value test layer)
│
├── infrastructure/                   # ADAPTERS. All external deps live here (or in cmd).
│   ├── clock/realclock.go            # Clock impl: time.Now (UTC)
│   ├── idgen/ulid_gen.go             # IDGen impl: github.com/oklog/ulid/v2, monotonic per-process (JSON-safe string ids; avoids JS int64 precision bugs)
│   └── sqlite/                       # modernc.org/sqlite driver lives HERE and only here
│       ├── db.go                     # Open(path): sql.Open("sqlite", "file:<path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), PRAGMA foreign_keys=ON per connection (via RegisterConnectionHook), SetMaxOpenConns(1), runs migrations, Ping
│       ├── migrate.go                //go:embed migrations/*.sql — versioned plain-SQL migrations, executed in filename order, tracked in schema_migrations table (no migration tool; ~50 lines)
│       ├── migrations/0001_init.sql  # full DDL + seed statuses (see §4)
│       ├── projects_repo.go          # implements application.ProjectRepo — raw database/sql, explicit column lists, hand-rolled rows.Scan mapping to domain (no ORM, no sqlx)
│       ├── tasks_repo.go             # implements application.TaskRepo incl. the claim CAS: UPDATE … SET lease,status,assignee-if-null,version+1 WHERE id=? AND (claimed_by_name IS NULL OR claimed_by_name=$actor OR claim_expires_at <= $now) RETURNING *; 0 rows → read holder → ClaimConflictError
│       ├── acceptance_repo.go
│       ├── comments_repo.go
│       ├── artifacts_repo.go
│       ├── relations_repo.go
│       ├── events_store.go           # implements application.EventStore: append-only INSERT + windowed reads (task/project, limit/offset)
│       └── *_test.go                 # integration tests vs t.TempDir() file DB: migrations idempotency, claim-CAS race (2 goroutines → exactly 1 winner), canonical SQL order == domain.Less, FK cascades, RETURNING behavior
│
├── api/                              # REST ADAPTER. imports application + domain only. No sqlite, no mcp-go.
│   ├── server.go                     # NewRouter(deps *api.Deps) http.Handler: stdlib ServeMux with "METHOD /path/{id}" patterns; middleware chain (recover → slog logging → bearer-token auth); mounts /healthz and the static UI (fs.Sub of built dist + SPA fallback)
│   ├── routes.go                     # every route in one table-like list — the single place to read the whole API surface
│   ├── handlers_projects.go
│   ├── handlers_tasks.go             # claim/release/heartbeat/comments/notes/AC/artifacts/relations/events sub-resources
│   ├── dto.go                        # request/response DTOs + domain↔DTO mappers (intentional struct duplication with domain; boring, explicit, type-safe)
│   └── errors.go                     # domain error → HTTP: ErrNotFound→404, ConflictError→409 {error:"version_conflict",current:{version,updated_at}}, ClaimConflictError→409 {error:"already_claimed",holder,claim_expires_at}, validation→422; every body has an agent-readable "message" recovery hint
│   └── handlers_test.go              # httptest end-to-end over real temp-file SQLite: happy path for each of the 14 mapped routes + 409/404/401 shapes
│
├── mcp/                              # MCP ADAPTER (crowban-mcp binary only). imports application? NO — it calls REST. Imports: apiclient + stdlib + mcp-go.
│   ├── client_iface.go               # tiny interface over the REST surface the tools need (implemented by apiclient.Client; lets mcp tests run against a stub)
│   ├── tools.go                      # RegisterTools(s *mcpgo-server.MCPServer, c Client): all 14 tools. One file, mechanical. Descriptions are written for small LLMs: enumerate every enum value, state defaults, state the error contract ("on conflict you get holder + expiry; re-read or wait")
│   └── tools_test.go                 # stub client → assert exact REST calls per tool; 409 body → isError result with verbatim JSON text (error-as-recovery-instruction channel)
│
├── infrastructure/apiclient/         # HTTP client to the main server (used by mcp binary only). stdlib net/http only: BaseURL, Bearer token, 10s timeout, typed Get/Post/Put/Patch/Delete returning (body []byte, status int, error)
│   └── client.go
│
├── cmd/
│   ├── crowban-server/main.go        # COMPOSITION ROOT 1: env config → idgen/clock → sqlite open+migrate → repos+event store → use cases → api.Deps → http.Server (:8080, graceful shutdown). Also serves /app/public (built web UI) and /healthz
│   └── crowban-mcp/main.go           # COMPOSITION ROOT 2: env config → apiclient.Client → server.NewMCPServer("crowban", version, WithRecovery(), WithInputSchemaValidation()) → mcp.RegisterTools(...) → server.NewStreamableHTTPServer(mcs, WithStateLess(true), WithEndpointPath("/mcp")) → own ServeMux {"/mcp"→streamable handler, "GET /healthz"} → http.Server (:9080)
│
└── web/                              # FRONTEND (not part of the Go module; own package.json — see §7)
    ├── package.json  vite.config.ts  tsconfig.json  index.html
    └── src/main.ts, app.ts, api.ts, ui.css
```

Notes:
- `application/ports.go` is the seam. A developer can never import sqlite from a use case — there is no exported type to import.
- No `internal/` wrapper needed; the layout itself encodes the rule. If you want machine-enforced rules later, add `depguard` in golangci-lint with exactly these package prefixes.
- The MCP adapter deliberately does NOT reuse `api/dto.go`: it passes through compact JSON strings (see §6). Duplicating a handful of wire structs in `apiclient` is cheaper than cross-layer import gymnastics.

---

## 3. Minimal dependency list

### (a) Main server module (`go.mod`)
| Module | Why |
|---|---|
| `modernc.org/sqlite` (v1.59.x) | Pure-Go SQLite driver. **Chosen over `mattn/go-sqlite3`**: mattn requires CGO → non-static binaries, per-arch toolchains in Docker buildx, libsqlite3 headaches; modernc builds fully static with `CGO_ENABLED=0`. Cost accepted: ~1.3–2× CPU overhead vs C SQLite (vendor-measured) — irrelevant at our QPS with indexes + single-writer pool. **Gotcha (in the driver's own docs):** your `go.mod` must end up with exactly the `modernc.org/libc` version that modernc.org/sqlite requires — never hand-pin libc; let `go mod tidy` resolve it. |
| `github.com/oklog/ulid/v2` | Sortable 26-char string IDs (JSON-safe across JS/agent clients, avoids int64→float precision loss). Tiny, stable, boring. |

That is the entire direct-dependency set for the server binary. Stdlib covers: `net/http` (mux, server, fs), `database/sql`, `log/slog`, `flag`, `embed`.

**Explicitly NOT needed for MVP** (do not add): chi/gin/echo (stdlib mux suffices, §1); sqlx or any ORM (raw `database/sql` + hand-rolled scanning — the query surface is ~25 statements); a migration tool like golang-migrate (embedded `.sql` + 50-line runner, below); viper/koanf (env parsing by hand); cobra (stdlib `flag`); FTS5/indexing libraries (`LIKE` search is enough for MVP; Tier-2 candidate); any logging lib (slog).

Migrations decision: **plain versioned `.sql` files under `infrastructure/sqlite/migrations/`, embedded with `//go:embed`, applied at boot in filename order, tracked in a `schema_migrations(filename TEXT PRIMARY KEY, applied_at)` table. Idempotent; no tool.**

### (b) MCP binary (same module — two extra direct deps total for the whole repo)
| Module | Why |
|---|---|
| `github.com/mark3labs/mcp-go` (v1.x, pin v1.1.1) | The de-facto standard Go MCP SDK. Confirmed current API: `server.NewMCPServer(name, version, opts...)`, typed tool builders `mcp.NewTool(name, mcp.WithDescription(…), mcp.WithString("x", mcp.Required(), mcp.Enum(...)))`, `s.AddTool(tool, handler)`, and — what we run it with — `server.NewStreamableHTTPServer(mcs, server.WithStateLess(true), server.WithEndpointPath("/mcp"))` (implements `http.Handler`; mount it in our own mux). Extras we use: `WithRecovery()`, `WithInputSchemaValidation()` (server-side arg validation — valuable since clients are small local models that will occasionally send malformed args). Implements MCP spec 2025-11-25 with backward compatibility to 2024-11-05. |

The official `github.com/modelcontextprotocol/go-sdk` exists and is a reasonable alternative; we pick mark3labs/mcp-go because (a) it is the most widely-used Go SDK (best model-training coverage, best examples online), (b) its API is a thin net/http wrapper that matches our boring style, and (c) if we ever hit friction, `mcp/tools.go` + `cmd/crowban-mcp/main.go` are the *only* files touched by an SDK swap.

---

## 4. Data model → schema

SQLite. All timestamps **RFC3339 UTC strings** (`2026-09-25T18:00:00Z`) — not unix ints (trivial for agents to reason about, no float issues). IDs = ULID strings. Actor stored as a `(type, name)` pair; type ∈ `human|agent`. `0001_init.sql`:

```sql
PRAGMA foreign_keys=ON;  -- also enforced per-connection by driver hook

CREATE TABLE status (                 -- workflow statuses are NAMED DATA, not an enum
  name         TEXT PRIMARY KEY,      -- seeded: 'todo','doing','done' (Tier-2 inserts more, no migration needed)
  display_name TEXT NOT NULL,
  sort_order   INTEGER NOT NULL UNIQUE,-- board column order
  is_terminal  INTEGER NOT NULL DEFAULT 0
);
INSERT INTO status(name,display_name,sort_order,is_terminal) VALUES
 ('todo','Todo',0,0),('doing','Doing',1,0),('done','Done',2,1);

CREATE TABLE project (
  id          TEXT PRIMARY KEY,        -- ULID
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',-- markdown; doubles as the agent onboarding doc
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE task (
  id                TEXT PRIMARY KEY,
  project_id        TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  status            TEXT NOT NULL DEFAULT 'todo' REFERENCES status(name),
  title             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',   -- markdown
  priority          INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 5),
  due_at            TEXT,                        -- nullable RFC3339
  notes             TEXT NOT NULL DEFAULT '',    -- durable resume context; replace-on-write
  assignee_type     TEXT CHECK (assignee_type IN ('human','agent')),   -- durable ownership, nullable
  assignee_name     TEXT,
  claimed_by_type   TEXT CHECK (claimed_by_type IN ('human','agent')), -- ephemeral lease holder, nullable
  claimed_by_name   TEXT,
  claimed_at        TEXT,
  claim_expires_at  TEXT,
  claim_ttl_sec     INTEGER,                   -- TTL stored at claim; heartbeat reuses it
  version           INTEGER NOT NULL DEFAULT 0,-- bumped ONLY by task-row mutations (see below)
  archived          INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  completed_at      TEXT                        -- set when status→done
);
-- Canonical ordering (the "what's next" index). SQLite has no NULLS LAST keyword:
-- (due_at IS NULL) sorts 0 before 1 → nulls last. EXACT expression used in every ORDER BY:
CREATE INDEX idx_task_order ON task(project_id, priority DESC, due_at ASC, created_at ASC) WHERE archived = 0;
CREATE INDEX idx_task_lookup ON task(project_id, status);

CREATE TABLE acceptance_criterion (
  id                TEXT PRIMARY KEY,
  task_id           TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq               INTEGER NOT NULL,          -- 1-based display order; complete_ac targets seq
  text              TEXT NOT NULL,
  done              INTEGER NOT NULL DEFAULT 0,
  note              TEXT NOT NULL DEFAULT '',  -- optional note recorded on check
  completed_by_type TEXT, completed_by_name TEXT, completed_at TEXT,
  UNIQUE (task_id, seq)
);

CREATE TABLE comment (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('progress','question','decision','handoff','note')),
  body       TEXT NOT NULL,                    -- markdown
  actor_type TEXT NOT NULL CHECK (actor_type IN ('human','agent')),
  actor_name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_comment_task ON comment(task_id, created_at);

CREATE TABLE artifact (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('log','screenshot','command-output','trace','url')),
  ref        TEXT NOT NULL,                    -- typed reference: URL or filesystem path (no uploads in MVP)
  summary    TEXT NOT NULL DEFAULT '',         -- one line
  actor_type TEXT NOT NULL, actor_name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_artifact_task ON artifact(task_id, created_at);

CREATE TABLE relation (                        -- directed edges; inverses ("blocks","parent-of") are computed
  id            TEXT PRIMARY KEY,
  task_id       TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,   -- source
  other_task_id TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,   -- target
  kind          TEXT NOT NULL CHECK (kind IN ('parent','child','blocked-by','duplicate-of','related')),
  created_at    TEXT NOT NULL,
  UNIQUE (task_id, other_task_id, kind),
  CHECK (task_id <> other_task_id)
);
CREATE UNIQUE INDEX idx_one_parent ON relation(task_id) WHERE kind = 'parent';  -- ≤1 parent per task
CREATE INDEX idx_relation_target   ON relation(other_task_id);

CREATE TABLE event (                            -- append-only activity log; every mutation appends here
  id           TEXT PRIMARY KEY,
  project_id   TEXT REFERENCES project(id) ON DELETE CASCADE,          -- null for task-level events? no — always set when derivable
  task_id      TEXT REFERENCES task(id)     ON DELETE CASCADE,         -- null only for pure project-level events
  actor_type   TEXT NOT NULL, actor_name TEXT NOT NULL,
  kind         TEXT NOT NULL,              -- created|updated|status-changed|claimed|released|heartbeat|noted|commented|ac-toggled|artifact-added|relation-added|relation-removed|project-created|project-updated
  before_json  TEXT,                        -- compact JSON of changed values before (nullable)
  after_json   TEXT,                        -- compact JSON after (nullable)
  ts           TEXT NOT NULL
);
CREATE INDEX idx_event_task    ON event(task_id, ts);
CREATE INDEX idx_event_project ON event(project_id, ts);

CREATE TABLE schema_migrations (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
```

**Version-bump policy (decisive):** `task.version` increments on mutations *to the task row*: `update_task`, claim, release, `update_notes`. It does **not** increment for child-row appends (comments, artifacts, AC toggles, relations) or heartbeat — those still emit events but must not cause 409s against a working agent's stale read. Claim response always carries the new version so the claiming agent proceeds with it.

**Computed flags:** `blocked` = ∃ outgoing `blocked-by` edge whose target task has `status != 'done'`. Computed at read time in the application layer (never stored). **Duplicate warning on create:** if an open (not done/archived) task in the same project has a normalized-equal title, `create_task`/`POST /tasks` response gains `warnings: ["possible_duplicate:<id>"]` — advisory, never an error.

**Indexes vs. perf note:** modernc.org/sqlite docs explicitly warn missing indexes cost more there than in C builds; the canonical-order index above covers the hottest query (`list_tasks`). Keep `EXPLAIN QUERY PLAN` free of `USE TEMP B-TREE` on that path (it is an integration-test assertion).

---

## 5. REST API map

Base `/api/v1`. Auth: off by default (single-tenant); when the server starts with `-token`/`CROWBAN_TOKEN` set, every `/api/v1/*` route requires `Authorization: Bearer <token>` or `?token=<token>` (SSE fallback) and answers `401 {"error":"unauthorized","message":"missing or invalid auth token"}` otherwise — `/healthz`, root `/metrics`, and the static UI stay open (implemented post-blueprint; see the 2026-09-27 ADR below). The full wire contract (row shapes, error bodies, pagination) lives in [API.md](API.md). Error envelope everywhere: `{"error":"<code>","message":"<recovery hint, agent-readable>", ...fields}`.

| REST endpoint | MCP tool | Notes |
|---|---|---|
| `GET /api/v1/projects` | `list_projects` | rows + per-status task counts |
| `POST /api/v1/projects` `{actor, name, description?}` | `create_project` | 201, fresh project; empty name → 400 |
| `GET /api/v1/projects/{id}` | — (UI) | the project row; 404 for unknown id |
| `PUT /api/v1/projects/{id}` `{name?, description?}` | — (UI) | rename/description edit |
| `GET /api/v1/tasks` | `list_tasks` | query params (all optional; unknown ignored): `project_id`, `status`, `assignee_name` (alias `assignee`), `assignee_type`, `archived` (def false), `unblocked`, `search` (alias `q`; case-insensitive substring over title+description+notes), `priority` (0-5), `due_before`/`due_after`, `created_after`/`created_before`, `completed_since`/`completed_after` + `completed_before`, `limit` (def 20, max 100, clamped), `offset`. Response: dense summary rows in **canonical order** + pagination envelope (`total`) |
| `POST /api/v1/tasks` | `create_task` | body: `actor, title, project_id (must exist), description?, priority? (0-5), due_at? (RFC3339), assignee?{type,name}, acceptance_criteria?[strings]`. One transaction: task + initial ACs (seq 1..n). Response: `{task: summary row v1, possible_duplicates: []}` — advisory only, never an error |
| `GET /api/v1/tasks/{id}` | `get_task` | one-call resume payload: all fields + notes + ACs + last-N comments (`comment_limit` def 10, max 50) + artifacts + relations + computed `blocked` + blockers + last-N events (`event_limit` def 20, max 100). Carries `version` |
| `PATCH /api/v1/tasks/{id}` | `update_task` | body: **`version` (required)**, `actor`, any of `title,description,status,priority,due_at,clear_due,assignee{type,name},clear_assignee,archived`. Status changes validated against the transition map. **409** `{error:"version_conflict",current_version,updated_at,message,current:{fresh row}}` on mismatch — the fresh row is the recovery instruction |
| `POST /api/v1/tasks/{id}/claim` | `claim_task` | body: `actor`, `ttl_sec?` (def 900, clamped to max 3600). Atomic CAS (§4 note). Sets lease + `status→doing`; **never touches the durable assignee**. **Idempotent for current holder** = lease refresh (no spurious conflict); expired foreign lease = takeover. **409** `{error:"claim conflict",holder:{type,name},expires_at,message}` |
| `POST /api/v1/tasks/{id}/release` | `release_task` | body: `actor`, `note?`. Holder-only against an active lease (else 409 naming holder + expiry); unclaimed task = idempotent no-op. Clears lease, `status→todo`; the optional note rides on the released **event** (NOT posted as a comment). Version bumps |
| `POST /api/v1/tasks/{id}/heartbeat` | `heartbeat_task` | body: `actor`. Extends `claim_expires_at` by the stored ttl. Holder-only (409 if foreign/expired/no lease — no-lease ⇒ empty `holder.type` = "claim first"). **No version bump, no `updated_at` change.** Returns the fresh summary row — one call doubles as a health check |
| `POST /api/v1/tasks/{id}/comments` | `add_comment` | body: `actor, kind∈{progress,question,decision,handoff,note}, body`. 201 fresh comment. (UI extra: `GET …/comments?limit&offset`) |
| `PUT /api/v1/tasks/{id}/notes` | `update_notes` | body: `actor`, `notes` (full replace), `version?` — OPTIONAL optimistic guard. Omitted/0 = historical contract: identical content is a no-op, a real change bumps; rare concurrent write → 409 like any other. Provided + stale → `409 version_conflict` with the fresh row, checked inside the write transaction |
| `PATCH /api/v1/tasks/{id}/acceptance-criteria/{seq}` | `complete_ac` (+text edit) | body: `actor, done(bool), note?`, or `actor, text` — when `text` is present it takes precedence (reword; identical text is a no-op with no event). 1-based seq. No version bump (child-row rule); unknown task/seq → 404 |
| `POST /api/v1/tasks/{id}/acceptance-criteria` | — (UI) | body: `actor, text` (non-empty after trim, else 400). Appends at seq max(seq)+1 (monotonic — no renumbering); one `ac-added` event in-tx. 201 fresh row; unknown task → 404 |
| `DELETE /api/v1/tasks/{id}/acceptance-criteria/{seq}` | — (UI) | body: `actor` (required — the removal appends an `ac-removed` event recording who). 204; surviving seqs keep their numbers (gaps stay); unknown task/seq → 404 |
| `POST /api/v1/tasks/{id}/artifacts` | `attach_artifact` | body: `actor, kind∈{log,screenshot,command-output,trace,url,other}, ref, summary?`. 201 fresh artifact |
| `POST /api/v1/tasks/{id}/relations` | `relate_tasks` (add) | body: `actor, other_id, kind∈{parent,child,blocked-by,duplicate-of,related}`. Same-project required (400), no self-relations, one-parent rule (409 parent conflict). `parent` is an input alias — stored/returned as the inverse `child` edge. **Upsert**: re-adding an existing edge returns 200 with the row and no event (idempotent for retrying agents); new edge → 201 |
| `DELETE /api/v1/tasks/{id}/relations/{other_id}/{kind}` | `relate_tasks` (remove) | 204. Keyed by composite `(task_id, other_id, kind)` — agents never need to remember relation ids; body optional but must carry a valid `actor` when present |
| `GET /api/v1/projects/{id}/board` | — (UI) | `{project_id, counts:{todo,doing,done}, tasks:{todo:[…],doing:[…],done:[…]}}` summary rows in canonical order per column — one round-trip for the whole board |
| `GET /api/v1/projects/{id}/events` | — (UI) | realtime SSE event stream scoped to the project (`text/event-stream`; 404 unknown project; `: connected` then 15s `: ping` comments, one event per mutation) |
| `GET /api/v1/events?project_id=…` | — (UI) | same feed, query-scoped; missing/empty `project_id` → 400 |
| `POST /api/v1/tasks/{id}/opencode/start` | — (UI/ops) | dispatch an autonomous OpenCode session for the task: creates the session, dispatches the prompt async, attaches a `url` artifact, claims the task under actor `opencode` (TTL 3600) when unclaimed → 200 `{session_id, session_url}`; upstream failures → 502 `opencode_error` |
| `GET /api/v1/metrics` · `GET /metrics` | — (ops) | in-memory request/task metrics, Prometheus text or JSON (`?format=` or `Accept`); root `/metrics` is never auth-gated, the API one takes the same 401 gate as the rest of `/api/v1` |
| `GET /healthz` | (healthcheck) | see §8 |

Claim-CAS over HTTP: no locks held across the round-trip; the race resolves in a single `UPDATE … WHERE lease-free-or-expired-or-same-holder` with `RETURNING`. Winner → 200/201 + fresh state + new version. Loser → 409 with holder identity and expiry — which *is* the recovery instruction ("wait until X, or work a different task").

---

## 6. MCP container design

- **SDK:** `github.com/mark3labs/mcp-go` v1.x (verified current API, §3).
- **Transports: stateless Streamable HTTP + legacy SSE.** Primary is the MCP spec's streamable-HTTP transport (replaced the older SSE transport): clients POST JSON-RPC to one endpoint; responses are `application/json` or an SSE stream. We run **stateless** (`server.WithStateLess(true)`): the SDK keeps no session map, every request is self-contained — which is exactly what makes the container stateless and trivially restartable/scaleable. The streamable server natively handles `POST /mcp` (calls and notifications) and `GET /mcp` (streaming notifications). Alongside it, a legacy **SSE server** (`server.NewSSEServer`) covers clients that only speak the pre-streamable protocol via `GET /sse` and `POST /message`. One caveat for wrappers: any middleware that wraps `http.ResponseWriter` must re-expose `Flush()` or the SDK's flusher assertion fails with 405/500 "Streaming unsupported".
- **Wiring (`cmd/crowban-mcp/main.go`):**

```go
mcpsrv := mcpserver.NewMCPServer("crowban", version,
    mcpserver.WithRecovery(),               // panics → JSON-RPC error, not container crash
    mcpserver.WithInputSchemaValidation(),  // malformed agent args → clean protocol error
)
mcp.RegisterTools(mcpsrv, client)           // mcp/tools.go — all 14 tools, one mechanical file
streamable := mcpserver.NewStreamableHTTPServer(mcpsrv,
    mcpserver.WithStateLess(true),
    mcpserver.WithEndpointPath("/mcp"),
)
sse := mcpserver.NewSSEServer(mcpsrv,        // legacy SSE transport, same tool registry
    mcpserver.WithKeepAlive(true),           // periodic pings keep idle streams through proxies
    mcpserver.WithSSEDisableLocalhostProtection(true),
)
mux := http.NewServeMux()
mux.Handle("/mcp", streamable)              // Streamable HTTP (POST calls/notifs, GET stream)
mux.Handle("/sse", sse.SSEHandler())         // canonical legacy SSE endpoint
mux.Handle("/message", messageHandlerWithSessionFallback(sse.MessageHandler())) // JSON-RPC into legacy session
mux.HandleFunc("GET /healthz", healthz)     // pings main server's /healthz through the client; 503 if upstream down
http.ListenAndServe(addr, withLogging(logger, mux))
```

- **Tool registration pattern (`mcp/tools.go`):** each tool = `mcp.NewTool(name, mcp.WithDescription(<written for small LLMs>), <typed property builders: WithString+Required, WithNumber, WithEnum(...), WithObject/WithArray where needed>)` + handler `func(ctx, mcp.CallToolRequest) (*mcp.CallToolResult, error)`. Handlers are one-liners around the client: parse args (`request.RequireString(...)`) → call REST → on success `mcp.NewToolResultText(compactJSON)`; on REST error return `mcp.NewToolResultError(text)` where text = **the REST error body verbatim** (its `message` already contains the recovery hint). Never fabricate success.
- **Config (env, compose-injected):** `-addr`/`MCP_ADDR` (def `:9080`), `CROWBAN_API_URL` (def `http://crowban-server:8080`; the compose service DNS name), `CROWBAN_TOKEN` (optional; forwarded as `Authorization: Bearer <token>` on every upstream call when set), `CROWBAN_AGENT_NAME` (def `agent`; the fallback actor NAME when a call omits `actor`). No config file, no state, no other env.
- **Docker exposure:** host 9311 → container :9080 (`CROWBAN_MCP_PORT`); clients point at `http://<host>:9311/mcp` (Streamable HTTP, or SSE on GET), and legacy-SSE-only clients at `http://<host>:9311/sse`. Note the SDK's DNS-rebinding guard: loopback-sourced requests with a non-localhost `Host` header get 403 — via Docker port forwarding this does not trigger (source isn't loopback in-container); escape hatch `WithDisableLocalhostProtection(true)` if host-side clients misbehave.
- **Idempotency / error conventions:** no client-supplied idempotency keys in MVP (accepted risk, §10). Instead: semantics are retry-safe where it matters — re-claim by same actor refreshes (200), `relate_tasks` upserts (200), version-guarded updates fail loudly with 409 rather than double-applying. Appends (comments/artifacts) are NOT idempotent; a post-timeout retry may duplicate — documented in tool descriptions ("check the comment tail before re-posting after an error").
- **Error propagation:** there is no special-case mapping — every non-2xx upstream answer (including `401` when a token is configured) arrives as `isError` with the REST body **verbatim** (`message` carries the recovery hint, e.g. `missing or invalid auth token` → set/forward `CROWBAN_TOKEN` on the MCP container); connection failure → "crowban-server unreachable at <url> — is it running?".

---

## 7. Frontend stack

| Piece | Choice | Why |
|---|---|---|
| Bundler | **Vite** (latest stable) | Boring, fast, zero-config for this shape; the standard answer a small LLM knows well |
| Language | **TypeScript — yes** | Small local LLMs are reliably competent in TS; a typed `api.ts` client keeps the wire contract honest and catches drift at build time. Vite consumes `.ts` with zero config |
| Styling | **Tailwind CSS v4** (not v3) | v4 is the current major: one-line setup — `@import "tailwindcss";` in CSS + the `@tailwindcss/vite` plugin; no `tailwind.config.js`, no PostCSS pipeline. Less config surface = less for small models to break |
| Interactivity | **Alpine.js** (single dep, `alpinejs`) | Not React/Vue/Svelte — one `<script>` library (~20KB) that turns the MVP board into declarative `x-data`/`x-for`/`@click` DOM. Small models write Alpine far more reliably than hand-rolled imperative DOM diffing; no component framework to learn, no state library. Buttons for status transitions (todo→doing→done), claim button, AC checkboxes, comment box, notes editor, artifact link form. **No drag-and-drop in MVP** |
| Markdown | `marked` only (no sanitizer — all content is our own single-tenant data; noted as a risk) | Titles/descriptions/notes/comments are markdown per spec |

Total runtime deps: `alpinejs`, `marked`. Dev deps: `vite`, `@vitejs/plugin-*` none needed, `typescript`, `tailwindcss`, `@tailwindcss/vite`. Build output: hashed `assets/*.{js,css}` + `index.html` → `web/dist/`, expected < 200KB total.

**UI scope (functional-but-simple):** project picker in header; three columns from `GET /projects/{id}/board`; cards show title, priority chip, due date, blocked badge, assignee/claim chip with countdown-ish expiry; click card → detail pane (all fields, AC checklist wired to `PATCH …/ac/{seq}`, notes textarea + save, comments with kind selector, artifacts list + add form, relations list, recent events). Human actor name asked once, stored in `localStorage`, sent as `actor:{type:"human",name}`. Manual refresh button (+ optional 15s auto-poll toggle). That's the whole app.

**Serving:** the Vite build runs inside the Docker image build (stage `web-build`); the Go server serves it with stdlib only:

```go
public := os.DirFS("/app/public")            // built dist copied in
sub, _ := fs.Sub(public, ".")
mux.Handle("GET /", spaFallback(http.FileServerFS(sub)))   // unknown non-/api GETs → index.html
```

No separate web container, no nginx.

---

## 8. Docker blueprint

**One `Dockerfile`, four stages (3 build + 2 runtime targets selected via `build.target`):**

```dockerfile
# syntax=docker/dockerfile:1
FROM node:22-alpine AS web-build
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build                    # vite → /web/dist

FROM golang:1.26-alpine AS go-build  # CGO not installed → pure-Go builds only (as intended)
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/crowban-server ./cmd/crowban-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/crowban-mcp     ./cmd/crowban-mcp

FROM alpine:3.21 AS server-runtime   # alpine (not scratch): busybox wget for the healthcheck
RUN adduser -D -u 10001 app
COPY --from=go-build  /out/crowban-server /usr/local/bin/
COPY --from=web-build /web/dist /app/public
USER app
ENV CROWBAN_ADDR=:8080 CROWBAN_DATA=/data/crowban.db
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["crowban-server"]

FROM alpine:3.21 AS mcp-runtime
RUN adduser -D -u 10001 app
COPY --from=go-build /out/crowban-mcp /usr/local/bin/
USER app
ENV MCP_ADDR=:9080
EXPOSE 9080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:9080/healthz || exit 1
ENTRYPOINT ["crowban-mcp"]
```

**`docker-compose.yml`:**

```yaml
services:
  crowban-server:
    build: { context: ., target: server-runtime }
    ports: ["${CROWBAN_BIND_IP:-127.0.0.1}:${CROWBAN_SERVER_PORT:-3311}:8080"]
    environment:
      CROWBAN_ADDR: ":8080"
      CROWBAN_DATA: "/data/crowban.db"
      CROWBAN_STATIC_DIR: "/app/public"   # UI baked in at image build (web-build stage)
      CROWBAN_TOKEN: "${CROWBAN_TOKEN:-}"  # optional; empty = auth off (single-tenant local default)
    volumes: [ "crowban-data:/data" ]     # named volume holds crowban.db + -wal/-shm

  crowban-mcp:
    build: { context: ., target: mcp-runtime }
    ports: ["${CROWBAN_BIND_IP:-127.0.0.1}:${CROWBAN_MCP_PORT:-9311}:9080"]
    environment:
      MCP_ADDR: ":9080"
      CROWBAN_API_URL: "http://crowban-server:8080"   # compose service DNS — no IPs, no host assumptions
      CROWBAN_TOKEN: "${CROWBAN_TOKEN:-}"   # optional; empty = auth off (supersedes the :? required-interpolation sketch, see ADR 2026-09-27)
    depends_on:
      crowban-server: { condition: service_healthy }

volumes:
  crowban-data:
```

- **Healthcheck endpoint (implement exactly this):** `GET /healthz` on the main server → `200 {"status":"ok","db":"ok"}` after a successful `SELECT 1` ping; `503 {"status":"degraded","db":"error: …"}` if the DB is unreachable. On the MCP server, `/healthz` proxies the upstream check: 200 only when main server's `/healthz` is healthy (so `depends_on: service_healthy` chains correctly).
- The node build is a *Docker stage*, not a compose service — no build-only containers run. `.dockerignore`:

```
.git
.env
bin/
*.db  *.db-wal  *.db-shm
web/node_modules
web/dist
docs
```

(Keep `web/package-lock.json` in the context — it is needed for `npm ci`; it does not match any ignore rule.)

- Backups = consistent copy of the volume (stop container, or use SQLite's `.backup`); see §10.

---

## 9. Build & test plan

**Build:**
- UI: `cd web && npm ci && npm run build` → `web/dist` (used by Docker stage; also committed nothing — gitignored).
- Go: `CGO_ENABLED=0 go build ./cmd/...` → two static binaries. `make build` does both locally into `bin/`.

**Tests per onion layer (all under `go test ./...`, no external services):**

| Layer | Tests | Why high value |
|---|---|---|
| `domain` | canonical ordering incl. nulls-last + ties; transition map validity; title normalization for dup detection | pure, instant, protects the invariant that "what's next" has one answer |
| `application` (fakes + FakeClock) | **claim:** fresh win / double-claim 409-with-holder / expired-lease takeover / idempotent re-claim / heartbeat holder-check / release by non-holder rejected; **update:** version ok / version conflict / illegal transition; **create:** dup warning, parent+relations in one tx, cross-project relation rejected; **blocked:** computed flag flips when blocker completes | the concurrency + coordination logic lives here — this is where crowban's correctness is. Fakes make it fast and dependency-free |
| `infrastructure/sqlite` (temp-file DB per test) | migrations apply twice safely; claim CAS with 2 racing goroutines → exactly one winner; canonical SQL order matches `domain.Less` on randomized fixtures; FK cascades (delete project wipes task tree + events); `EXPLAIN QUERY PLAN` for the list query has no TEMP B-TREE | proves the SQL actually encodes what the fakes assume — the classic fake/real divergence, caught cheaply here |
| `api` (httptest over real temp SQLite) | happy path per each of the 14 mapped routes + UI extras; 401 without token; 404 shape; **409 version_conflict body carries `current.version`; claim 409 carries holder+expiry**; healthz with/without DB | one end-to-end slice covering REST contract + status-code semantics (the MCP layer's input contract) |
| `mcp` (stub client) | each of the 14 tools → assert exact method/path/body emitted; REST 409 body arrives verbatim as `isError` text; success bodies are compact JSON | proves the 14-tool surface and error propagation without Docker |

**Makefile targets:** `help`, `fmt` (`gofmt -w . && go run golang.org/x/tools/cmd/goimports@latest -w .` optional — keep plain gofmt), `vet` (`go vet ./...` + `gofmt -l .` must be empty), `build` (both binaries → `bin/`), `ui` (npm ci + vite build), `test` (`go test ./... -count=1`), `up` (`docker compose up --build -d`), `logs` (`docker compose logs -f`), `down` (`docker compose down`), `smoke` (curl script: healthz → create project → create task → claim as agent-a → duplicate claim expect 409 → comment → update with stale version expect 409; plus one MCP `initialize`+`tools/list` POST to the MCP host port /mcp, ${CROWBAN_MCP_PORT:-9311}).

Ordering advice for devs: domain → application(+fakes) → sqlite → api → mcp, testing each layer before moving out — the onion makes this sequence natural.

---

## 10. Risks & open questions

**Top risks (mitigations in parens):**
1. **mcp-go API churn.** The SDK is explicitly "under active development" and the MCP spec itself moves (we're pinned to behavior of v1.1.1 / spec 2025-11-25). Mitigation: pin `v1.1.x` in go.sum; confine all SDK usage to `mcp/tools.go` + `cmd/crowban-mcp/main.go` so an upgrade (or swap to the official `modelcontextprotocol/go-sdk`) touches exactly two files.
2. **Pure-Go SQLite quirks:** (a) the `modernc.org/libc` pin must match what modernc.org/sqlite requires — never hand-edit that version in go.mod; (b) CPU-bound queries run ~1.3–2× slower than C builds, so under-indexed ORDER BYs hurt more — mitigated by the canonical-order index + the EXPLAIN QUERY PLAN test assertion; `SetMaxOpenConns(1)` removes BUSY handling entirely at our scale.
3. **No idempotency keys on append operations.** Flaky local networks + agent retries can duplicate comments/artifacts (relations are upsert-safe; claim is CAS-safe). Mitigation: documented tool-description guidance ("after an error, read the tail before re-posting"); request-id idempotency keys are a cheap Tier-2 add.
4. **SQLite on odd filesystems.** WAL misbehaves on some network/shared filesystems — keep `crowban-data` on a local named volume (compose default), never a bind mount from NFS/CIFS. Backup story: MVP = stop-and-copy the volume; an in-place `.backup` admin endpoint is Tier-2 (open question below).
5. **Single-writer pool bottleneck resolved with dual pools:** Pinned globally to `SetMaxOpenConns(1)`, all queries (including `/healthz` pings and `/metrics` table scans) queued behind active write transactions, risking false degraded health reports (503s) and container restart loops during agent operations. This tradeoff was resolved by introducing a dual-pool architecture (`sqlite.OpenPool` / `sqlite.DBPool`): `Writer` retains `SetMaxOpenConns(1)` to eliminate writer `SQLITE_BUSY` contention, while `Reader` maintains a concurrent pool (`DefaultReaderMaxOpenConns=5`) taking full advantage of SQLite WAL mode's concurrent non-blocking reader throughput (see ADR 2026-10-15).

**Decisions deliberately left to you:**
- `CROWBAN_TOKEN` policy: blueprint assumes always-on bearer auth (required in compose). Loosening it to optional-for-dev is a 5-line middleware change if you want dev convenience without env.
- **`update_task` may move a task to `done` with unchecked ACs and no artifacts** (Definition-of-Done *enforcement* is Tier-2 per the feature doc; MVP records but doesn't block). Confirm that's acceptable for MVP.
- Project deletion: **out of scope for MVP** (no DELETE on projects); cascade machinery exists in the schema anyway.
- Exact image tags beyond what's written (`golang:1.26-alpine`, `node:22-alpine`, `alpine:3.21`) — bump to current equivalents at build time; nothing here depends on exact patch versions, only Go ≥ 1.22 features.
- MCP host port (9080 chosen per your note) and whether agent clients will ever need the SSE GET stream (stateless mode says no).

---

## Implementation decisions (ADR)

Decisions locked during implementation so later slices do not re-litigate them. Dated; where a bullet supersedes earlier text in this doc, the bullet wins.

- **2026-09-25 — `internal/` wrapper approved.** The repo layout uses an idiomatic Go `internal/` wrapper around the onion (`internal/domain`, `internal/application`, `internal/infrastructure/**`, and later `internal/api`, `internal/mcp`). Supersedes §2's "no `internal/` wrapper needed"; the dependency rule itself is unchanged.
- **2026-09-25 — `/healthz` contract.** `/healthz` returns exactly the §8 contract: `200 {"status":"ok","db":"ok"}` on a successful `SELECT 1`; on DB failure it reports the degraded status body per §8 (`503 {"status":"degraded","db":"error: …"}`). It never returns an "error" body.
- **2026-09-25 — Artifact kinds include `other`.** The canonical artifact-kind set is `log | screenshot | command-output | trace | url | other` — `other` is the escape hatch for evidence types the fixed list misses. It lands via migration `0002_artifact_other.sql` (SQLite cannot ALTER a CHECK, so the table is rebuilt preserving data), keeping `domain.ArtifactKind.Valid()` and the DDL CHECK in agreement. ROADMAP.md's Tier-1 artifact list predates this decision.
- **2026-09-25 — Go version pin.** The `go` directive is 1.25 (forced by modernc.org/sqlite v1.59.0); all code targets ≤ Go 1.22 language features only; the Docker build pin `golang:1.26-alpine` remains valid. Do NOT hand-pin `modernc.org/libc` — let `go mod tidy` resolve whatever modernc.org/sqlite requires (§3 gotcha, reaffirmed).
- **2026-09-25 — Port conventions.** Use cases mint IDs (`IDGenerator`) and stamp timestamps (`Clock`) before calling ports; adapters store values exactly as given and never invent them. `TaskRepository.Update(t)`: `t.Version` is the expected stored version; on success it stores version+1 and returns the fresh row; a stale writer gets `*domain.ConflictError{Current}` carrying the current row. `Claim/Release/Heartbeat(ctx, id, actor, ttlSec?, now)` take an injected `now`; claim wins (CAS) when the lease is free OR expired OR already held by the same actor. Release moves status→todo and bumps version; heartbeat does NOT bump version (child-row rule). `TaskRepository.List(f TaskFilter)` returns canonical-ordered rows plus the total count before Limit/Offset.
- **2026-09-25 — Transactional seam = UnitOfWork.** A lightweight port in `internal/application/uow.go`: `Repos` bundles the seven repository ports; `UnitOfWork.Run(ctx, r *Repos, fn func() error) error` executes `fn` inside ONE transaction (commit on nil, rollback on error). Run may be used for both reads and writes in MVP (uniformity). **RULE: every mutation's `EventStore.Append` happens inside the SAME transaction as the write it records** — data row and event row commit or roll back together.
- **2026-09-25 — Naming.** `domain.Actor{Type,Name}` is the assignee/actor value; `Task.Assignee *Actor` is nullable (nil = unassigned). `Event.Kind` is a typed constant set — `domain.EventKind`, defined in `internal/domain/event.go` — whose canonical values are: `created, updated, status-changed, claimed, released, heartbeat, assigned, noted, commented, ac-toggled, artifact-added, relation-changed, ac-added, ac-updated, ac-removed` (the three AC-editing kinds were appended last so earlier kinds keep their stable indices; they cover post-create checklist management). Project-level events use the generic `created`/`updated` kinds with `TaskID` nil; a single `relation-changed` kind covers both edge additions and removals (the Before/After JSON carries the edge), superseding the fine-grained `relation-added`/`relation-removed` vocabulary in the §4 comment. `Task.LastActivity` has no schema column in MVP — adapters set it = UpdatedAt on task-row mutations.
- **2026-09-26 — Claim lease lifecycle (slice 2C): atomic CAS + lazy expiry, no sweeper.** `Claim(id, actor, ttlSec?, now)` is ONE atomic CAS — `UPDATE task SET lease,status='doing',version+1 WHERE id=? AND (claimed_by_name IS NULL OR claim_expires_at < now OR claimed_by_name = actor) RETURNING *`: the winner takes a fresh lease period (claimed_at/claim_expires_at re-stamped to now, now+ttl even on renewal — full history lives in the event log), and the loser gets `*domain.ClaimConflictError{Holder, ExpiresAt}` with nothing mutated (the body IS the recovery instruction). Lease-holder key is the actor **NAME** (MVP single-tenant, self-declared; `claimed_by_type` is display-only and may differ across re-claims by the same named actor). One `claimed` event per successful claim, its After JSON tagged `"claim":"fresh" | "takeover" | "renewal"` — renewal = same named actor re-claiming an ACTIVE lease (logged on purpose: every task-row mutation emits an event; the tag makes the noise filterable, heartbeats stay the silent channel), takeover = a prior lease had EXPIRED (including a self-takeover of one's own zombie lease). **Lazy expiry, NO background sweeper in MVP:** nothing flips expired doing→todo in a goroutine; an expired lease stays on disk (status=doing, holder set) but is re-claimable by ANY actor via takeover and surfaces as "lease expired" to readers; auto-flipping deferred to Tier 2. `Release` is holder-only against an ACTIVE lease — non-holder OR expired self-release → `*domain.ClaimConflictError` (chosen over `ErrInvalid`: a coordination collision carrying holder+expiry, not malformed input); releasing a task with no lease at all is an idempotent no-op returning the fresh row (retry-safe). Success clears the lease, moves status→todo, bumps version, appends `released` (note in payload). `Heartbeat` extends claim_expires_at to now + the STORED ttl for the active self-holder only; it does NOT bump version, does NOT touch updated_at, and emits NO event (keep-alive ≠ state change — must never 409 a working agent's stale read); foreign/expired holder or no lease at all → `ClaimConflictError` (zero-value Holder = "claim it first"). **Claim/release/heartbeat NEVER touch the durable Assignee** (claimed_by is a separate ephemeral field; assignment stays in `Tasks.Update`, which emits `assigned`) — supersedes §5's "sets assignee if unassigned". TTL: `DefaultClaimTTL = 900s` (`application.DefaultClaimTTL`), per-call override clamped to max 3600s, negative rejected as `ErrInvalid`; the default is overridable via `Tasks.WithDefaultClaimTTL` for tests.
- **2026-09-26 — Serving the built web UI (slice 5a): stdlib static + SPA fallback, graceful degradation.** `cmd/crowban-server` serves the Vite build with stdlib only (§7, no web container): directory from env `CROWBAN_STATIC_DIR` (flag `-static`, default `./web/dist`), mounted as `http.FileServerFS(os.DirFS(dir))` under the catch-all pattern `"GET /"`, wrapped in an SPA fallback — a GET/HEAD whose path maps to no existing file and is not under `/api` or `/healthz` is rewritten to `/` (dist root → `index.html`) so client-side deep links work. **Rewrite target is `/`, NOT `/index.html`:** FileServer's `serveFile` (net/http/fs.go) redirects any request whose path ends in `/index.html` with a relative 301 to `"./"`, which loops on deep links; serving the dist root delivers index.html with no redirect. **Mux precedence:** every API pattern (`… /api/v1/…`) and `GET /healthz` is an exact path — more specific than the `"GET /"` catch-all under Go 1.22 ServeMux resolution, independent of registration order — so static serving can never shadow them (proved by `TestStaticServingDoesNotShadowAPIAndHealthz`; unknown paths under `/api` that do reach the catch-all 404 rather than falling back to index.html). **Graceful degradation:** when the static dir is missing or has no `index.html` at startup, the server logs a warning and keeps the placeholder root — backend-only runs still boot, `/` still answers, and the `/healthz` contract is unchanged.
- **2026-09-27 — Shipped layout supersedes §2's tree.** Module path is `github.com/mrhollen/crowban` (`go 1.25.5`). The REST adapter lives in `internal/interface/http/`, not `internal/api/`: `app.go` (App + Deps), `routes.go` (the single route table), `handlers_projects.go`, `handlers_tasks.go` (list/create/get/update), `handlers_collab.go` (claim/release/heartbeat/comments/notes/AC/artifacts/relations), `handlers_events.go` (SSE streams), `handlers_opencode.go` (session dispatch), `dto.go`, `errors.go`, `auth.go`, `helpers.go`, `responses.go`, `metrics.go`. The router is assembled in `cmd/crowban-server/main.go` (`newRouter`), not an api-package `server.go`; the http adapter itself imports only application+domain (+ stdlib) — sqlite-specific wiring (task-stats callback) happens in the cmd composition root. SQLite package: `db.go`, `runner.go` (embedded migration runner), `querier.go`, `convert.go` (RFC3339Nano timestamp boundary), `health.go`, `metrics.go`, one repo per aggregate, migrations `0001_init` … `0005_project_opencode_model`. MCP adapter files: `internal/mcp/{tools,prompts,resources}.go` + `cmd/crowban-mcp/main.go`. Web sources: `web/src/{main,app,api,types,util,md}.ts`, `style.css`, plus `alpinejs.d.ts`/`vite-env.d.ts`. Shipped Dockerfiles pin `golang:1.25-alpine`. The dependency rule itself is unchanged.
- **2026-09-27 — Token auth shipped (supersedes §5's "unused until the token middleware lands").** `AuthMiddleware` in `internal/interface/http/auth.go`: when a non-empty token is configured, every path under `/api` requires `Authorization: Bearer <token>` or a `?token=` query param (the SSE/EventSource fallback), compared in constant time; absent/mismatched → `401 {"error":"unauthorized","message":"missing or invalid auth token"}`. Empty token = middleware is a pass-through. `/healthz`, root `/metrics`, and static UI are never gated. Wiring: `-token` flag / `CROWBAN_TOKEN` env in `cmd/crowban-server/main.go`; the web UI stores and sends it; the MCP container forwards it via apiclient; compose passes `${CROWBAN_TOKEN:-}` — empty means auth off (single-tenant LAN default), supersed §8's required-interpolation sketch.
- **2026-09-27 — Realtime SSE events shipped (post-MVP wave).** In-process pub/sub broker `internal/infrastructure/pubsub/broker.go`: per-project fan-out, per-subscriber buffered channels (default 64); a stalled subscriber is dropped rather than blocking mutating handlers; the http adapter carries a noop fallback when unwired. Endpoints: `GET /api/v1/projects/{id}/events` and `GET /api/v1/events?project_id=` (400 when missing) — `text/event-stream`, no-cache, `X-Accel-Buffering: no`, initial `: connected` comment, 15s `: ping` comments, `id:`/`event:`/`data:` framing with a compact payload (`event_id`, `project_id`?, `task_id`?, `kind`, `status`?, `actor`?, `timestamp`). Kinds are the 15 canonical `domain.EventKind` values (ADR "Naming" above). The UI consumes it via EventSource with a 15s poll fallback — supersedes the polling-only UI note in §7 and the ROADMAP's "no push channel" boundary line.
- **2026-09-27 — Metrics + log level shipped.** `internal/interface/http/metrics.go`: lock-free atomic counters (total requests, in-flight, by-status 100–599, by-method, total duration) plus an optional DB callback (`sqlite.CollectTaskMetrics` → `tasks_by_status{status}` excluding archived and `active_claims`). Served at root `GET /metrics` (never auth-gated; defaults to Prometheus text exposition 0.0.4) and `GET /api/v1/metrics` (same 401 gate as the rest of `/api/v1`; defaults to JSON); `?format=json|prometheus|text` or `Accept` overrides either. The server also takes `-log-level` (`LOG_LEVEL`/`CROWBAN_LOG_LEVEL`). §5/§8 had no metrics surface — this bullet adds it.
- **2026-09-27 — OpenCode dispatch shipped.** `POST /api/v1/tasks/{id}/opencode/start` (no body): each setting resolves project row → env var → default — server URL (`OPENCODE_SERVER_URL`, def `http://localhost:4444`), session directory (`OPENCODE_DIRECTORY`, def `/home/hollen/Code/crowban`), model ref `providerID/modelID` (`OPENCODE_MODEL`, def `llama.cpp/Qwen Medium Long Context`). The handler creates an OpenCode session (agent `build`, title `CrowBan: <task title>`, model set explicitly so key-gated fallbacks cannot 401 the session), dispatches a task prompt asynchronously, attaches a `url` artifact ("OpenCode autonomous session"), and claims the task under `{ "type": "agent", "name": "opencode" }` with TTL 3600 when unclaimed — then returns 200 `{session_id, session_url}`. Unreachable/invalid upstream → 502 `{"error":"opencode_error","message":…}`. Schema: migrations `0004_project_opencode` / `0005_project_opencode_model` add `project.opencode_server_url`, `opencode_directory`, `opencode_model` (supersedes §4's project table). UI: "Start Task in OpenCode" launcher + project-settings modal editing the three fields.
- **2026-09-27 — MCP surface beyond tools.** `internal/mcp/prompts.go` registers prompts `triage_open_tasks(project_id?, limit? def 50 max 100)` and `next_task(project_id?, assignee?, actor_name?)`; `internal/mcp/resources.go` registers read-only resources `crowban://task/{id}` (the full `get_task` resume payload) and `crowban://project/{id}` (project row + counts). `mcp.HealthzHandler` proxies the upstream `/healthz` (503 while down), so compose `service_healthy` chains through it. `cmd/crowban-mcp/main.go` additionally installs `withModernMCPCompat` (backfills `_meta.protocolVersion` from the `Mcp-Protocol-Version` header for strict SDK notification validation) and the legacy-SSE session fallback (`/sse`, `/message`). Supersedes §2/§6's tools-only framing of the MCP adapter.
- **2026-09-27 — Web UI scope expanded post-MVP.** Supersedes §7's "UI scope (functional-but-simple)" paragraph, which describes the original MVP board: the shipped UI adds drag-and-drop status moves (optimistic move + 409 re-read/re-apply with rollback), SSE live refresh of board/detail/counts/comments, a project settings modal (name/description/opencode_* via `PUT /projects/{id}`), a relations editor (add/remove edges, 409 surfaced verbatim), AC add/reword/delete controls, client-side column sorting (persisted direction), an assignee selector and board filters, paginated comment threads with live updates, and the OpenCode launcher. Claims stay READ-ONLY in the UI by design — agents manage leases via MCP.
- **2026-10-15 — Dual DB pool: single-writer serialization vs. concurrent reader throughput in WAL mode.** Resolves the risk of health checks (`/healthz`) and metrics scrapes (`/metrics`) blocking behind active write transactions. SQLite in WAL mode permits any number of concurrent readers alongside an active writer without lock contention, provided transactions do not share connection handles. In the original MVP architecture, a single global `SetMaxOpenConns(1)` pool serialized all operations, causing read-only endpoints (`SELECT 1` in `sqlite.Health`, table scans in `CollectTaskMetrics`, and SSE `Last-Event-ID` activity log replays) to queue behind long-running write transactions or bulk queries. When external orchestrators (Kubernetes/Docker) probed `/healthz` during an active transaction, the probe could time out or return 503 degraded, triggering restart loops. `sqlite.OpenPool` provides a dual connection pool abstraction (`sqlite.DBPool`):
  1. **Writer pool (`SetMaxOpenConns(1)`):** strictly serializes mutations (`UnitOfWork.Run`) and executes embedded DDL migrations, preventing `SQLITE_BUSY` write conflicts in-process without application-level mutexes.
  2. **Reader pool (`SetMaxOpenConns(5)`, `SetMaxIdleConns(2)`):** dedicated pool for non-blocking concurrent reads (`sqlite.HealthPool`, `sqlite.CollectTaskMetricsPool`, and SSE replay `EventStore`). Readers execute under SQLite WAL snapshot isolation, guaranteeing immediate responsiveness and predictable probe latencies even under heavy write load.

