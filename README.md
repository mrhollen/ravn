# crowban

MCP-first, multi-project KanBan for humans and AI agents.

## What it is

crowban is an MCP-first KanBan tool where AI coding agents are the primary users. It serves as external memory (state survives session death), a coordination bus for multi-agent work (claims, handoffs, no duplicate work), and an evidence store (proof of done). The human web board is secondary. The system is single-tenant with a dependency-minimal Go backend in onion architecture, Docker-first deployment (main server + stateless MCP server containers), and pure-Go SQLite (no CGO).

## MVP features

- **Projects**: Create and manage projects with name and markdown description (doubles as agent onboarding doc).
- **Tasks**: Markdown title/description, priority (0-5), due date.
- **Workflow**: To Do / Doing / Done status transitions recorded as events.
- **Atomic claims**: Claim/release/heartbeat with TTL lease; CAS prevents duplicate grabbing; TTL expiry auto-heals zombie claims.
- **Typed assignee**: Durable ownership (human or agent identity) distinct from ephemeral claim.
- **Durable notes**: Replace-on-write resume context for fresh session survival.
- **Typed comments**: Progress / question / decision / handoff-note / note kinds with actor and timestamp.
- **Acceptance criteria**: Ordered checklist with optional completion notes.
- **Artifacts**: Typed evidence links (log / screenshot / command-output / trace / url) attached to tasks.
- **Relations**: Parent/child, blocked-by, duplicate-of, related edges; computed "blocked" flag; duplicate warnings on create.
- **Optimistic versioning**: Version-guarded writes with full activity log on every mutation.
- **Agent-first reads**: Filtered list_tasks (project/status/assignee/unblocked/search/date-range) with canonical sort and pagination; get_task returns one-call resume payload.
- **Minimal web UI**: Board columns by status plus task detail (notes, ACs, comments, artifacts).
- **MCP tool surface**: 14 tools for agents to interact with the system.

## Architecture

- **Onion layering**: domain (stdlib only) → application (ports defined here) → infrastructure (SQLite driver, ULID) → api (REST adapter) → mcp (MCP adapter).
- **Pure-Go SQLite**: No CGO via modernc.org/sqlite; fully static binaries with CGO_ENABLED=0.
- **Streamable-HTTP MCP**: Via mark3labs/mcp-go v1.x over stdlib net/http in stateless mode.
- **Frontend**: Vite + TypeScript + Tailwind CSS v4 + Alpine.js, built at image-build time and served by the Go binary.
- **See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** for full technical details.

## Quick start

Assume a Makefile with targets `up`, `down`, `logs`, `build`, `test`, `smoke`:

```bash
make up                    # starts crowban-server (:8080) + crowban-mcp (:9080) in Docker
make smoke                 # end-to-end check
```

- **UI**: http://localhost:8080
- **MCP endpoint**: http://localhost:9080/mcp

**Optional auth**: Set `CROWBAN_TOKEN` in your environment. Auth is off by default for single-tenant local use.

## MCP client config

An MCP client would point at the server over HTTP like this:

```json
{
  "mcpServers": {
    "crowban": {
      "url": "http://localhost:9080/mcp",
      "authentication": {
        "type": "httpHeader",
        "headers": {
          "Authorization": "Bearer <CROWBAN_API_TOKEN>"
        }
      }
    }
  }
}
```

*Illustrative example; adjust authentication to your client's format.*
