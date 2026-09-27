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
- **Artifacts**: Typed evidence links (log / screenshot / command-output / trace / url / other) attached to tasks.
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

### Docker (recommended)

Requires Docker (with compose). Copy the env template only if you want to change defaults:

```bash
cp .env.example .env       # optional — every variable has a default
make up                    # builds both images, starts crowban-server (:8080) + crowban-mcp (:9080) detached
make smoke                 # hits both /healthz + GET /api/v1/projects, prints PASS/FAIL
make logs                  # stream both containers' logs
make down                  # stop and remove containers (keeps the SQLite volume)
```

- **UI**: http://localhost:8080
- **API**: http://localhost:8080/api/v1 — full contract in [docs/API.md](docs/API.md)
- **MCP endpoint**: http://localhost:9080/mcp (onboarding for agents: [docs/MCP.md](docs/MCP.md))

**Port overrides.** Host-side published ports are env-overridable (`CROWBAN_SERVER_PORT`, default `8080`; `CROWBAN_MCP_PORT`, default `9080`). The containers always listen on 8080/9080 *internally* — only the host mapping changes, so the MCP→server upstream URL stays stable. If another process already owns :8080, set `CROWBAN_SERVER_PORT=18080` in `.env` (do **not** kill what's there) and run `make up`; point UI/clients at http://localhost:18080.

**Persistence.** The SQLite database lives in the named Docker volume `crowban-data` (mounted at `/data`) and survives rebuilds and `make down`. Backups = a consistent copy of that volume (stop the container first, or use SQLite's `.backup`).

### Direct binaries (development)

No Docker needed — build both statically and run them yourself:

```bash
make build                 # → bin/crowban-server + bin/crowban-mcp (pure-Go, static)
make ui                    # → web/dist (the built Vite UI)

./bin/crowban-server -addr=:8080 -data=./crowban.db -static=./web/dist &
CROWBAN_API_URL=http://localhost:8080 ./bin/crowban-mcp -addr=:9080 &
```

`-addr`, `-data`, `-token`, `-static` are the server flags; the MCP binary takes `-addr` plus env `CROWBAN_API_URL` (where the server lives), `CROWBAN_AGENT_NAME` (fallback actor name for calls that omit `actor`), and optional `CROWBAN_TOKEN` (forwarded upstream as a Bearer token). Verify with `curl http://localhost:8080/healthz` and `curl http://localhost:9080/healthz`.

**Optional auth.** Set `CROWBAN_TOKEN` in your environment or `.env`; the MCP container forwards it on every upstream call. Auth enforcement on the server itself is a planned addition (see [docs/ROADMAP.md](docs/ROADMAP.md)) — for now the token is accepted but not yet required.

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
          "Authorization": "Bearer <your CROWBAN_TOKEN, if auth is enabled>"
        }
      }
    }
  }
}
```

*Illustrative example — omit `authentication` entirely while auth enforcement is not yet in place; adjust to your client's format.*

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — stack, onion layout, schema, API map, MCP design, Docker blueprint
- [docs/API.md](docs/API.md) — the durable REST wire contract (row shapes, error bodies, pagination, idempotency)
- [docs/MCP.md](docs/MCP.md) — agent onboarding: connection, actor convention, all 14 tools
- [docs/ROADMAP.md](docs/ROADMAP.md) — MVP scope (Tier 1) and the Tier 2/3 boundary
