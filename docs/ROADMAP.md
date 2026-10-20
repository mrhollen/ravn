# Rävn

## Overview

Rävn is an MCP-first KanBan tool for AI agents and humans. Agents are the primary users, leveraging the tool as external memory, a coordination bus for multi-agent work, and an evidence store. The system is single-tenant and Docker-first, with a pure-Go backend using onion architecture and a minimal Vite+TS+Tailwind frontend.

## MVP — Tier 1 ✅ COMPLETE

All Tier 1 items are built, tested, and verified end-to-end (live E2E smoke: both binaries running together — server REST + UI + MCP tools round-trip). Shipped in commits `0b6858c` → `74b9dc2`.

### Projects
- ✅ **Projects**: Create projects with name and markdown description; description doubles as the agent onboarding doc. (`1f67933`, `730c99b`) — full API contract in [API.md](API.md).

### Tasks & workflow
- ✅ **Core task fields**: Title and description, priority (0-5), optional due date for canonical ordering. (`7941c79`)
- ✅ **Fixed workflow**: Three statuses (todo, doing, done) with transitions recorded as events. (`730c99b`)
- ✅ **Atomic claim with TTL lease**: Claim a task atomically (CAS), move it to doing, with TTL expiry auto-healing zombie claims; release and heartbeat operations included. (`c02b27c`)
- ✅ **Typed assignee**: Durable ownership as (type: human|agent, name) pair; distinct from the ephemeral claim/lease. (`7941c79`)
- ✅ **Durable task notes**: Replace-on-write resume context for fresh session survival. (`2bd33c9`)
- ✅ **Typed comments**: Progress, question, decision, handoff-note, or note kinds with actor and timestamp. (`2bd33c9`)
- ✅ **Acceptance criteria checklist**: Ordered list of checkable criteria (seq 1..n) with optional completion notes; set at create, then toggle-only afterwards. (`2bd33c9`)
- ✅ **Task relations**: Parent/child hierarchy, blocked-by, duplicate-of, and related edges; computed "blocked" flag and duplicate warnings on create. (`801930a`, `f303821`)
- ✅ **Optimistic versioning**: Version-guarded writes with full activity log on every mutation. (`7941c79`)
- ✅ **Agent-first reads**: Filtered list_tasks (project, status, assignee, unblocked, text search, date ranges) with canonical sort and pagination; get_task returns one-call resume payload. (`730c99b`, `f303821`)
- ✅ **Minimal web UI**: Board columns by status plus task detail (notes, ACs, comments, artifacts). (`2655cd8`, `f5deeeb`)

### Claims/ownership
- ✅ **Claim/release/heartbeat**: Atomic claim sets status→doing; release moves back to todo; heartbeat refreshes lease during long work stretches. (`c02b27c`)

### Collaboration & evidence
- ✅ **Typed artifacts**: Evidence links (log, screenshot, command-output, trace, url, other) attached to tasks with kind, ref, and one-line summary. (`2bd33c9`)

### Data integrity & reads
- ✅ **Activity log**: Append-only event table capturing who, what, before→after, and timestamp for every mutation. (`1f67933`)
- ✅ **Duplicate detection**: Title match warning on create_task for open tasks in the same project. (`f303821`)

### Deployment & agent surface
- ✅ **REST API + MCP server**: Full HTTP contract ([API.md](API.md)) and all 14 MCP tools over stateless Streamable-HTTP ([MCP.md](MCP.md)). (`bd9ba62`, `d19799b`, `e0aca8b`)
- ✅ **Docker-first deployment**: Multi-stage build, compose with DB volume + healthchecks, smoke script. (`74b9dc2`)

**Explicitly ALLOWED in MVP**: Marking a task `done` without any artifact attached (artifact attachment is not enforced). NOT allowed: completing a task while any acceptance criterion is unchecked — the done transition is rejected with `400 invalid input` naming the unmet seqs, and there is **no override** (see [API.md](API.md)); likewise every AC mutation on a completed or archived task is refused until it is reopened / unarchived. Token auth (`RAVN_TOKEN`) is OPTIONAL for local dev: off by default, and when set the server enforces it on all `/api/v1/*` routes with `401`.

### Post-MVP waves — shipped after the initial MVP

- ✅ **Auth token enforcement**: Shared `RAVN_TOKEN` gates every `/api/v1/*` route when set (`Authorization: Bearer …`, or `?token=…` for SSE clients that cannot set headers); the web UI sends it; off by default. (`05a8bed`, `0149cb4`)
- ✅ **Drag-and-drop board**: Column drag-and-drop status moves with optimistic move + 409 re-read/re-apply and rollback. (`05a8bed`)
- ✅ **Realtime SSE events**: Per-project event stream (15 canonical kinds); the UI updates board, detail, counts and comment threads live, with a 15s poll fallback. (`028a0d0`, `bce6dd5`, `7b4c792`)
- ✅ **Task edit + project settings in UI**: Edit title/description/priority/due date/assignee; project settings modal (name, description, OpenCode server/directory/model); board filters. (`028a0d0`, `e3d2079`)
- ✅ **AC management in UI**: Append, inline reword, and delete criteria — matching the API endpoints exactly. (`e439c09`)
- ✅ **Relations & claims visibility in UI**: Relations editor (create/remove edges with 409-surface); claims rendered read-only — agents manage leases via MCP by design. (`01a1518`)
- ✅ **Client-side column sorting**: By last modified, created, due date, priority, or title with persisted direction. (`3a4757e`)
- ✅ **Observability**: `/metrics` and `/api/v1/metrics` (Prometheus text or JSON) and a configurable server log level. (`ed7489d`)
- ✅ **OpenCode dispatch**: `POST /tasks/{id}/opencode/start` creates an autonomous session, claims the task, attaches the session URL as an artifact; project-level server/directory/model settings plus a board launcher button. (`8dd1801`, `5dbd18a`, `cbef29e`, `c0a97e3`)
- ✅ **MCP prompts**: Onboarding prompts (`triage_open_tasks`, `next_task`). (`05a8bed`)
- ✅ **MCP resources**: Read-only `ravn://task/{id}` and `ravn://project/{id}` resources for resume payloads and project summaries. (`028a0d0`)

### Shipped boundary — still deferred

- **Definition-of-Done enforcement** — marking a task `done` stays allowed with unchecked ACs or no artifacts (Tier 2).
- **Claim operations in the UI** — claims are displayed read-only; claim/release/heartbeat remain API + MCP only, deliberately.
- **Per-agent identity & rate limits** — auth is one shared token for the whole stack; a registered agent table with per-agent tokens/audit lands in Tier 2.

Everything else deferred is listed under Tier 2 / Tier 3 below.

## Tier 2 — Strong second wave

- **handoff_task**: Atomic reassign + mandatory context note as one event; clean agent→agent and human→agent handoffs with audit trail.
- **project_report rollups**: One call answers "what's blocked?", "what's stalled?", "what finished since X?", per-project digests, cycle-time basics over the event log.
- **Explicit policies (Kanban practice)**: Custom statuses/workflow per project, WIP limits per column, Definition-of-Done *enforcement* (block status=done until all ACs checked / ≥1 artifact attached).
- **Labels/tags**: Filtering and full-text search (SQLite FTS5 over titles/descriptions/notes/comments) for richer discovery.
- **Structured decision records (ADR-lite)**: Options considered, decision, rationale — searchable across tasks/projects; links from task to decision.
- **Agent identity & advanced auth**: Registered agent table + per-agent tokens with attribution integrity and audit/rate limits on top of the shipped shared-token gating; optional outbound webhooks (e.g., question comment → Gotify/push so humans are pinged).
- **Artifact file uploads**: Server-side storage, size caps, UI preview beyond URL references.
- **Stall/health surfacing**: Stale filter on list_tasks (doing beyond N hours without activity), per-agent workload summaries.

## Tier 3 — Later / nice-to-haves

Scheduling/reminders/recurrence · timeline & Gantt views · flow analytics dashboards (cycle time, lead time, WIP compliance) · task templates & project cloning · import/export (GitHub Issues, Jira, CSV/JSON) · custom fields/plugin schema · artifact diffing and screenshot annotation · AI-assisted decomposition and AC drafting · agent runner integration (auto-launch an agent session per claimed task — the full self-bootstrap endgame) · fine-grained permissions / multi-user authN (OAuth/mTLS for agents) · portfolio rollups & OKR alignment · mobile UI · plugin webhooks (Matrix/Discord).

## Out of scope (MVP)

- Multi-tenancy
- Project deletion
