# Rävn — MCP-first Kanban: feature & architecture analysis (research notes)

Researcher: opencode. Status: final v1 (2026-09-25). Repo was greenfield at time of writing (README/LICENSE only). Pre-implementation snapshot — where it diverges from what shipped, the ADR section of `docs/ARCHITECTURE.md` and `docs/ROADMAP.md` are current.

## Problem framing

Rävn's primary users are AI agents with **small context windows** that get reset often. The tool is therefore, in effect:
1. An **external memory** (state must survive session death),
2. A **coordination bus / mutex service** (multi-agent claims, handoffs, no duplicate work),
3. An **evidence store** (proof of done), and only secondarily a human kanban board.

Every design decision below follows from those three jobs. Grounding sources: Wikipedia "Model Context Protocol" (MCP = JSON-RPC standard for tool integration, N×M connector problem), "Kanban (development)" (explicit policies / done rules, WIP limits, pull system), "Optimistic concurrency control" (version-guarded writes as the standard low-contention conflict pattern). Lease-with-TTL claim pattern is standard distributed-systems practice (e.g. etcd leases); no offline source needed.

## Cross-cutting design principles (agent-first API)

- **Dense, small responses.** List endpoints return summary rows (id/title/status/owner/priority/due/blocked) with pagination; detail only on `get_task`. Local models burn context fast — every byte counts.
- **One-call resume payload.** A fresh session must be able to rebuild its working state in ~2 calls: `list_tasks` (what exists) + `get_task(id)` (everything: fields, notes, ACs, tail of comments, blockers, artifacts).
- **Atomic claiming.** Claim is a single compare-and-swap transaction. Two agents racing → exactly one wins; the loser gets an error that says *who owns it and until when* (errors as recovery instructions — small models self-correct well when told what to do next).
- **Idempotent retries.** Flaky local inference / network → agents retry. `claim_task` by current owner = success no-op returning state; repeated identical updates are harmless.
- **Optimistic concurrency.** Every task carries a version number; updates guard on it. On conflict, return what changed and who did it (the agent re-reads instead of blind-writing). No long-held DB locks.
- **Stable canonical ordering everywhere** (priority desc → due asc nulls-last → created asc), so "what's next" has exactly one deterministic answer.
- **Every mutation is an audited event** with actor + timestamp. This single log powers rollups ("done this week"), stall detection, handoff history, and "what happened while I was gone?" — no separate analytics machinery needed.
- **Distinguish assignee vs claim.** Assignee = durable ownership (human *or* agent identity). Claim/lease = ephemeral working session with TTL. A task can be assigned to someone but unclaimed until a session actually works on it.

## A) Prioritized feature list

### TIER 1 — MVP vertical slice (tight)

| # | Feature | Rationale (agent workflow) | Data-model implications |
|---|---------|---------------------------|------------------------|
| 1 | **Projects** (create/list/get; name + markdown description) | Multi-project is core to the brief; description doubles as project-level "agent onboarding doc" (how agents should work here). Zero-cost convention that ships agent conventions without code. | `project` entity. |
| 2 | **Core task fields**: markdown title + description, priority (small int scale), due date | The unit of work. Priority + due give the canonical "next task" ordering; due dates feed stall detection later. | `task` fields: priority int, due ts, markdown text cols. |
| 3 | **Fixed workflow todo / doing / done**, transitions recorded as events | Minimum viable state machine that maps to claim/release semantics. Store statuses as named data (not hard-coded enum) so Tier-2 custom workflows don't need a migration. | `task.status` ref; status-change event rows. |
| 4 | **Atomic claim with TTL lease + release + heartbeat** (`claim_task`, `release_task`, `heartbeat_task`) | THE anti-duplicate-work mechanism for multi-agent operation. Atomic CAS prevents two agents grabbing one task; TTL expiry auto-heals zombie claims when a local model crashes mid-task (common); any owner mutation or comment refreshes the lease, and `heartbeat` exists for long silent work stretches. Claim sets status→doing atomically. | `task.owner`, `claimed_at`, `claim_expires_at`; background sweeper; claim/release event rows. |
| 5 | **Typed assignee (human \| agent)** + assignment events | Durable ownership that survives sessions and enables handoff semantics (reassign = handoff substrate). Agent identity in MVP is a self-declared name string (trusted single-tenant); tokens come in Tier 2. | `task.assignee {type, ref}`; assign event rows with actor. |
| 6 | **Durable task notes ("resume context")** via dedicated `update_notes` (replace-on-write) | Small-context survival: an agent writes "what I found out / what I'm about to do" into the tool so a fresh session resumes without re-deriving. Dedicated tool makes the intent loud and cheap; replace semantics keep one canonical blob instead of note fragments. | `task.notes` markdown column (full-replace, version-stamped). |
| 7 | **Typed comments** (progress / question / decision / handoff-note) with actor + timestamp | The progress channel *and* the human↔agent channel: `question`-typed comments mark "needs human input"; `handoff` marks ownership transfer context. Tail of thread is part of the resume payload, so a fresh agent inherits the conversation cheaply. | `comment` entity with `kind` enum. |
| 8 | **Acceptance criteria checklist** (ordered list, checkable with optional note) | Verification spine: "done" becomes falsifiable rather than asserted. Small models handle an integer-indexed checklist far better than free-form re-descriptions. | `acceptance_criterion` entity per task: seq, text, done, completed_by/at, note. |
| 9 | **Test artifacts / evidence links** (typed: log, screenshot, command output, trace, url) attached to tasks | Evidence of completion in the modalities agents actually produce (command output, logs, traces). MVP = typed references (URL/path + summary), optional small inline content; binary file storage is Tier 2. | `artifact` entity: kind enum, ref, summary, creator, ts. |
| 10 | **Task relations**: parent/child, blocked-by, duplicate-of, related — plus a computed **"blocked" flag** and **duplicate warning on create_task** (fuzzy title match) | Decomposition (subtasks) is required for complex multi-step work; "unblocked" is the killer discovery filter ("what can I actually start right now?"); duplicate warning stops agents re-creating tasks after context loss — a known failure mode. | `relation` table (directed edges; inverses computed); `parent_task_id` on task for hierarchy; blocked = ∃ open blocker; create-time similarity check. |
| 11 | **Optimistic versioning + full activity log** on every mutation (actor, kind, before→after summary, ts) | Concurrent agents editing the same board: conflicts surfaced as instructive errors instead of silent overwrites (standard OCC pattern). The event log is also the substrate for Tier-2 rollups/stall reports — capture it from day 1 or you can't build them later. | `task.version` int; `event` table (append-only). |
| 12 | **Agent-first read surface**: `list_tasks` with filters (project, status, assignee, unblocked, text search, date ranges) + canonical sort + pagination; `get_task` one-call resume payload | Discovery is the hottest agent operation and must be near-zero-cost. Date-range filters (e.g., completed after X) already answer "what finished this week?" without a report engine. `get_task` returns fields + notes + ACs + last-N comments + blockers + artifacts in one call. | No new entities — query design; indexes on status/priority/due/created/completed. |
| 13 | **Minimal web UI**: board columns by status + task detail (notes, ACs, comments, artifacts) | Humans must be able to *see* what agents are doing and intervene (observability of the fleet). Secondary but required for the slice to be usable at all. | None — read-mostly projection of Tier-1 data. |

### TIER 2 — strong second wave (one-liners)

- **`handoff_task`**: atomic reassign + mandatory context note as one event; clean agent→agent and human→agent handoffs with an audit trail.
- **`project_report` rollup tool**: one call answers "what's blocked?", "what's stalled? (doing > N h without activity)", "what finished since X?", per-project digests, cycle-time basics — pure sugar over the event log + timestamps already captured in T1.
- **Explicit policies (Kanban practice)**: custom statuses/workflow per project, WIP limits per column, Definition-of-Done *enforcement* (block `status=done` until all ACs checked / ≥1 artifact attached).
- **Labels/tags** + filtering and full-text search (SQLite FTS5 over titles/descriptions/notes/comments) for richer discovery.
- **Structured decision records** (ADR-lite): options considered, decision, rationale — searchable across tasks/projects; links from task to decision. Captures *why* as a first-class queryable object.
- **Agent identity & auth**: registered agent table + API tokens (attribution integrity, per-agent audit/rate limits); optional outbound webhooks (e.g., `question` comment → Gotify/push so humans are pinged).
- **Artifact file uploads** (server-side storage, size caps, UI preview) beyond URL references.
- **MCP prompts/resources**: an onboarding prompt ("how agents should use Rävn": claim → work → heartbeat → notes often → complete with evidence) and read-only task resources for richer context delivery.
- **Stall/health surfacing**: `stale` filter on list_tasks (doing beyond N h without activity), per-agent workload summaries.

### TIER 3 — later (for ROADMAP, brief)

Scheduling/reminders/recurrence · timeline & Gantt views · flow analytics dashboards (cycle time, lead time, WIP compliance) · task templates & project cloning · import/export (GitHub Issues, Jira, CSV/JSON) · custom fields/plugin schema · artifact diffing and screenshot annotation · AI-assisted decomposition and AC drafting · agent runner integration (auto-launch an agent session per claimed task — the full self-bootstrap endgame) · fine-grained permissions / multi-user authN (OAuth/mTLS for agents) · portfolio rollups & OKR alignment · mobile UI · plugin webhooks (Matrix/Discord).

## B) Data model sketch (TIER 1, plain terms)

- **Project**: id, name, description (markdown), created/updated. (Tier 2 adds workflow definition and done-policy per project.)
- **Task**: id; project; optional parent task (one parent → subtask tree); title; description (markdown); status (todo|doing|done, stored as named data for future extension); priority (small int); due date (optional); assignee (type + reference, human or agent, nullable) — *durable* ownership; owner/lease (agent ref, claimed-at, lease expiry) — *ephemeral* working session; notes (markdown, the durable resume context); version number (for optimistic concurrency); archived flag; timestamps: created, updated, completed, last-activity (any event bumps it).
- **Acceptance Criterion**: belongs to a task, ordered sequence; text; done flag; who/when checked; optional note on check.
- **Comment**: belongs to a task; author (typed identity); markdown body; kind (progress|question|decision|handoff|note); created (+edited) timestamp.
- **Artifact (test evidence)**: belongs to a task; kind (log|screenshot|command-output|trace|url|other); reference (URL/path, or small inline content in MVP); one-line summary; creator; timestamp.
- **Relation**: directed edge between two tasks with kind (child, blocked-by, duplicate-of, related). Inverses ("blocks", "parent") are computed, not stored. A task is *blocked* when it has a blocked-by edge to a task that is not done. (Parent/child lives in the `parent` field; cross-links in relations — one mechanism each, no redundancy.)
- **Event (activity log, append-only)**: which task/project, which actor, what kind (created, status-changed, claimed, released, assigned, noted, commented, artifact-added, relation-changed, AC-toggled), a short before→after summary, timestamp. Powers audit, rollups, stall detection, and "what changed while I was away."
- **Agent identity**: in MVP just a self-declared name string used as the actor reference (single-tenant, trusted). Tier 2 upgrades this to registered agents with tokens.

Relationships in one line: project → many tasks; task → one parent (optional) + many ACs/comments/artifacts/relations/events; task ↔ task via relations; everything stamped with who and when.

## C) MCP tool surface (TIER 1 = 14 tools)

Conventions shared by all tools: compact JSON; list endpoints paginated (default 20, max 100) with summary rows only; mutations return the fresh task summary (agent never re-fetches); every error message includes a recovery hint; stable canonical ordering (priority desc → due asc nulls-last → created asc).

| Tool | One-line description |
|------|---------------------|
| `list_projects` | All projects with id, name, and per-status task counts. |
| `create_project` | Create a project (name + markdown description). |
| `list_tasks` | Filtered (project/status/assignee/unblocked/search/date-range), canonically sorted, paginated summary rows of tasks — the "what should I work on?" call. |
| `get_task` | One-call resume payload: all fields, notes, acceptance criteria, tail of comments, blockers/relations, artifacts, recent events. |
| `create_task` | Create a task (parent, relations, priority, due, initial ACs); returns new task plus duplicate warnings if similar titles exist. |
| `update_task` | Partial field update (title, description, status, priority, due, assignee, archive); version-guarded (optimistic concurrency), actor-stamped, returns fresh state. |
| `claim_task` | Atomically take a TTL lease on a task and move it to doing; idempotent if you already own it; otherwise a clear error naming the current owner and lease expiry. |
| `release_task` | Give up the lease (back to todo), optionally posting a note explaining why. |
| `heartbeat_task` | Refresh lease expiry during long silent work; returns a compact state summary so one call doubles as a health check. |
| `add_comment` | Post a markdown comment with kind progress/question/decision/handoff, attributed to the caller. |
| `update_notes` | Replace the task's durable resume-context notes (the "write it down in the tool" call). |
| `complete_ac` | Check or uncheck an acceptance criterion by index, optionally recording a note. |
| `attach_artifact` | Attach typed evidence (log/screenshot/command-output/trace/url) with a one-line summary to a task. |
| `relate_tasks` | Create or remove relations between tasks: parent/child, blocked-by, duplicate-of, related. |

Tier 2 adds ~3 more: `handoff_task` (atomic reassign + mandatory context note), `project_report` (rollups/digests), and onboarding prompt/resources exposed via MCP prompts rather than tools.
