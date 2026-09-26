# crowban

## Overview

crowban is an MCP-first KanBan tool for AI agents and humans. Agents are the primary users, leveraging the tool as external memory, a coordination bus for multi-agent work, and an evidence store. The system is single-tenant and Docker-first, with a pure-Go backend using onion architecture and a minimal Vite+TS+Tailwind frontend.

## MVP — Tier 1 (IN SCOPE / building now)

### Projects
- **Projects**: Create and manage projects with name and markdown description; description doubles as the agent onboarding doc.

### Tasks & workflow
- **Core task fields**: Markdown title and description, priority (0-5), and optional due date for canonical ordering.
- **Fixed workflow**: Three statuses (todo, doing, done) with transitions recorded as events.
- **Atomic claim with TTL lease**: Claim a task atomically (CAS), move it to doing, with TTL expiry auto-healing zombie claims; release and heartbeat operations included.
- **Typed assignee**: Durable ownership as (type: human|agent, name) pair; distinguishes from ephemeral claim/lease.
- **Durable task notes**: Replace-on-write resume context for fresh session survival.
- **Typed comments**: Progress, question, decision, handoff-note, or note kinds with actor and timestamp.
- **Acceptance criteria checklist**: Ordered list of checkable criteria with optional completion notes.
- **Task relations**: Parent/child hierarchy, blocked-by, duplicate-of, and related edges; computed "blocked" flag and duplicate warnings on create.
- **Optimistic versioning**: Version-guarded writes with full activity log on every mutation.
- **Agent-first reads**: Filtered list_tasks (project, status, assignee, unblocked, text search, date ranges) with canonical sort and pagination; get_task returns one-call resume payload.
- **Minimal web UI**: Board columns by status plus task detail (notes, ACs, comments, artifacts).

### Claims/ownership
- **Claim/release/heartbeat**: Atomic claim sets status→doing; release moves back to todo; heartbeat refreshes lease during long work stretches.

### Collaboration & evidence
- **Typed artifacts**: Evidence links (log, screenshot, command-output, trace, url) attached to tasks with kind, ref, and one-line summary.

### Data integrity & reads
- **Activity log**: Append-only event table capturing who, what, before→after, and timestamp for every mutation.
- **Duplicate detection**: Fuzzy title match warning on create_task for open tasks in the same project.

**Explicitly ALLOWED in MVP**: Marking a task `done` is allowed even if acceptance criteria are unchecked or no artifact attached (Definition-of-Done *enforcement* is Tier 2). Auth/token is OPTIONAL for local dev.

## Tier 2 — Strong second wave

- **handoff_task**: Atomic reassign + mandatory context note as one event; clean agent→agent and human→agent handoffs with audit trail.
- **project_report rollups**: One call answers "what's blocked?", "what's stalled?", "what finished since X?", per-project digests, cycle-time basics over the event log.
- **Explicit policies (Kanban practice)**: Custom statuses/workflow per project, WIP limits per column, Definition-of-Done *enforcement* (block status=done until all ACs checked / ≥1 artifact attached).
- **Labels/tags**: Filtering and full-text search (SQLite FTS5 over titles/descriptions/notes/comments) for richer discovery.
- **Structured decision records (ADR-lite)**: Options considered, decision, rationale — searchable across tasks/projects; links from task to decision.
- **Agent identity & auth**: Registered agent table + API tokens (attribution integrity, per-agent audit/rate limits); optional outbound webhooks (e.g., question comment → Gotify/push so humans are pinged).
- **Artifact file uploads**: Server-side storage, size caps, UI preview beyond URL references.
- **MCP prompts/resources**: Onboarding prompt ("how agents should use crowban": claim → work → heartbeat → notes often → complete with evidence) and read-only task resources for richer context delivery.
- **Stall/health surfacing**: Stale filter on list_tasks (doing beyond N hours without activity), per-agent workload summaries.

## Tier 3 — Later / nice-to-haves

Scheduling/reminders/recurrence · timeline & Gantt views · flow analytics dashboards (cycle time, lead time, WIP compliance) · task templates & project cloning · import/export (GitHub Issues, Jira, CSV/JSON) · custom fields/plugin schema · artifact diffing and screenshot annotation · AI-assisted decomposition and AC drafting · agent runner integration (auto-launch an agent session per claimed task — the full self-bootstrap endgame) · fine-grained permissions / multi-user authN (OAuth/mTLS for agents) · portfolio rollups & OKR alignment · mobile UI · plugin webhooks (Matrix/Discord).

## Out of scope (MVP)

- Multi-tenancy
- Project deletion
