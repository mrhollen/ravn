# Rävn — Brand & Design Guide

> **MCP-first, multi-project Kanban for humans and AI agents.**
>
> *State survives session death. Zero duplicate work. Proof of done.*

## 1. Brand Foundation & Mythos

### 1.1 Why "Rävn"?

In Norse mythology, Odin relies on two ravens—**Huginn** (*Thought*) and **Muninn** (*Memory*)—who fly across the world and return with verifiable intelligence.

**Rävn** embodies this exact architecture for software engineering with AI coding agents:

1. **Muninn — External Memory:** Agents are ephemeral; context windows die, sessions crash, and containers restart. Rävn acts as durable external state so any fresh session can resume in a single MCP call (`get_task`) using replace-on-write durable notes and historical decision logs.

2. **Huginn — Coordination Bus:** When multiple agents and humans work concurrently across projects, chaos ensues without locks. Rävn provides atomic compare-and-swap (CAS) claims, TTL leases with heartbeats that auto-heal zombie claims, typed handoff comments, and dependency graphs.

3. **Verifiable Sight — Evidence Store:** "Done" is not a claim; it is a proof. Tasks require ordered acceptance criteria and typed artifacts (`log`, `screenshot`, `command-output`, `trace`, `url`).

### 1.2 Naming & Orthography

* **Prose & Branding:** Always write **Rävn** (capitalized `R`, lowercase `ävn` with the diaeresis/umlaut).

* **Pronunciation:** `/ˈrɑːvən/` (*RAH-vuhn*) or `/ˈrævən/` (*RAV-uhn*).

* **Code, CLI, Docker & Env Vars:** Strip the umlaut in technical identifiers for universal ASCII safety:

  * Binary / Container / Package: `ravn`, `ravn-mcp`

  * Environment Variables: `RAVN_TOKEN`, `RAVN_DB_PATH`, `RAVN_PORT`

### 1.3 Brand Pillars

| Pillar | Meaning in Practice | 
| ----- | ----- | 
| **Agent-First, Human-Legible** | AI coding agents are the primary users via MCP; the human web board is a high-signal observability and dispatch console. | 
| **Durable & Self-Healing** | Optimistic versioning guards every mutation. Ephemeral agent crashes never permanently lock work—TTL expiry automatically heals zombie claims. | 
| **Dependency-Minimal Craft** | Pure Go, onion architecture, pure-Go SQLite (zero CGO), Docker-first. No bloated frameworks, no fragile runtime dependencies. | 
| **Evidence Over Assertions** | Work transitions to `Done` backed by structured acceptance criteria and immutable artifact links. | 

## 2. Logo & Visual Mark

### 2.1 Anatomy of the Mark

The Rävn logo is a **Nordic-geometric low-poly raven crest** housed inside a dark obsidian squircle. Every element maps directly to a system concept:

1. **The Twin Amber Diamonds (`◆◆`):** Floating above the crown, the two rhombuses serve a dual purpose—visually echoing the **umlaut (`ä`)** in *Rävn* while representing **Huginn & Muninn** (Coordination & Memory / AI Agent & Human Observer).

2. **The Faceted Indigo Plumage:** Sharp, interconnected polygonal facets outlined in dark obsidian represent structured task graphs (`parent/child`, `blocked-by`), atomic state boundaries, and clean onion architecture.

3. **The Glacier Cyan Beak:** A precision-honed, two-tone steel-and-ice mandible pointing strictly right—symbolizing deterministic forward workflow progression (`To Do` $\rightarrow$ `Doing` $\rightarrow$ `Done`).

4. **The Forge-Gold Eye & Throat Facet:** The piercing amber eye represents live observability (`/healthz`, `/metrics`, SSE streams), while the glowing gold triangular facet at the base of the neck signifies an active **Atomic Claim / TTL Lease Beacon**.

5. **The Orbital Heartbeat Ring:** The subtle dashed circular track (`stroke-dasharray`) framing the raven represents the continuous TTL heartbeat loop and per-project realtime SSE event bus.

### 2.2 Logo Variants & Usage Contexts

* **Primary App Icon (Full Squircle):** Use for repository headers, macOS/Linux app icons, Docker Hub avatars, and OAuth/MCP integration tiles.

* **Standalone Crest (No Squircle Container):** The faceted raven + twin diamonds may be placed directly onto dark UI surfaces (`#080B16` to `#12172E`) such as a login/token gate or empty-state board view.

* **Simplified Favicon (**$16\text{px}$ **–** $24\text{px}$**):** At micro sizes below $24\text{px}$, omit the dashed orbital ring and increase the scale of the raven head and twin diamonds by $15\%$ so the amber eye and cyan beak remain crisp.

* **ASCII / Terminal Mark:** Use in CLI `--help` output, container startup logs, and MCP server initialization banners:

```
          ◆   ◆
        ◢███████◣
       ◢████◈████◣═══▸   R Ä V N
      ◢██████████◤       MCP-First Kanban & Agent Coordination Bus
      ◥█████◣██◤         v1.0 • Pure-Go SQLite • SSE Live

```

### 2.3 Clear Space & Minimum Size

* **Clear Space:** Maintain a minimum padding of $0.25\times$ the icon's width around the squircle. When using the standalone crest, measure clear space from the top tip of the right-hand amber diamond.

* **Minimum Sizes:**

  * Full Squircle with Orbital Ring: $32\text{px} \times 32\text{px}$

  * Standalone Crest (UI Header): $24\text{px} \times 24\text{px}$

### 2.4 Logo Misuse (Don'ts)

* **Do not** crop out or omit the twin amber diamonds above the raven's head—they are integral to the silhouette and brand identity.

* **Do not** flip the logo horizontally. The raven must always face right (forward along the Kanban flow).

* **Do not** remove the dark polygonal facet lines (`#080B14`) to make a flat silhouette; the geometric mesh is signature to Rävn.

* **Do not** place the standalone dark-indigo crest on a pure white (`#FFFFFF`) background without its dark squircle container, as the glowing twin diamonds and frost upper beak lose contrast against light backgrounds.

## 3. Color Palette & Design Tokens

Rävn is a **dark-mode-native** developer tool. Its color palette is extracted directly from the SVG logo's pigments and extended into semantic UI tokens for the web board.

### 3.1 Core Brand Palette (Extracted from Mark)

#### Void & Obsidian Surfaces (Canvas & Architecture)

| Token Name | Swatch / Role | Hex | RGB | 
| ----- | ----- | ----- | ----- | 
| **Void Abyss** | Root app background / outer squircle | `#080B16` | `rgb(8, 11, 22)` | 
| **Nordic Midnight** | Kanban column surface / sidebar | `#0E1326` | `rgb(14, 19, 38)` | 
| **Obsidian Card** | Task card surface / modal background | `#151C36` | `rgb(21, 28, 54)` | 
| **Card Hover** | Elevated interactive card surface | `#1C2547` | `rgb(28, 37, 71)` | 
| **Facet Hairline** | Borders, dividers, orbital dashed ring | `#263159` | `rgb(38, 49, 89)` | 

#### Raven Indigo & Iris (Primary Brand & Agent Identity)

| Token Name | Swatch / Role | Hex | RGB | 
| ----- | ----- | ----- | ----- | 
| **Deep Plumage** | Lower neck facet shadow / subtle badge fill | `#232070` | `rgb(35, 32, 112)` | 
| **Royal Raven** | Mid-plumage facet / primary button fill | `#3B37B8` | `rgb(59, 55, 184)` | 
| **Electric Iris** | Cheek facet / primary interactive & focus | `#5C59E8` | `rgb(92, 89, 232)` | 
| **Crown Periwinkle** | Top head facet highlight / agent link accent | `#8280FF` | `rgb(130, 128, 255)` | 

#### Forge Amber & Gold (Claims, Leases & The Twin Diamonds)

| Token Name | Swatch / Role | Hex | RGB | 
| ----- | ----- | ----- | ----- | 
| **Beacon Gold** | Throat facet / active claim badge highlight | `#FFC425` | `rgb(255, 196, 37)` | 
| **Heartbeat Amber** | Twin diamonds & eye / `Doing` & active lease | `#FF8C1A` | `rgb(255, 140, 26)` | 
| **Ember Orange** | Diamond gradient base / expiring lease warning | `#F25C19` | `rgb(242, 92, 25)` | 

#### Glacier Cyan & Steel (Beak, Telemetry & Evidence)

| Token Name | Swatch / Role | Hex | RGB | 
| ----- | ----- | ----- | ----- | 
| **Frost Crest** | Upper beak base / primary crisp typography | `#E2ECF7` | `rgb(226, 236, 247)` | 
| **Glacier Cyan** | Upper beak tip / SSE live pulse & Artifacts | `#34B3E4` | `rgb(52, 179, 228)` | 
| **Deep Fjord** | Upper beak mid-tone / secondary info badge | `#1D78A3` | `rgb(29, 120, 163)` | 
| **Steel Mandible** | Lower beak slate / muted secondary text | `#7E92B0` | `rgb(126, 146, 176)` | 

### 3.2 Semantic Domain Tokens (Kanban & MCP Entities)

#### Workflow Status Columns

* **`To Do` (Queued / Unclaimed):** `#7E92B0` (*Steel Mandible*) — Calm, waiting for dispatch.

* **`Doing` (In Progress / Claimed):** `#FF8C1A` (*Heartbeat Amber*) — Warm, active execution energy matching the raven's eye and diamonds.

* **`Done` (Verified / Complete):** `#10B981` (*Nordic Emerald*) paired with `#34B3E4` (*Glacier Cyan*) for attached evidence artifacts.

* **`Blocked` (Computed Relation Flag):** `#F43F5E` (*Crimson Rune*) — High-visibility alert when any `blocked-by` dependency is incomplete.

#### Priority Scale (`0` – `5`)

Tasks support integer priorities `0` through `5`. Use a diamond glyph (`◆`) or monospace badge (`P0`–`P5`):

* **`P0` (Trivial / Backlog):** `#4A5678` (Muted Slate)

* **`P1` (Low):** `#7E92B0` (Steel Mandible)

* **`P2` (Normal / Default):** `#34B3E4` (Glacier Cyan)

* **`P3` (High):** `#8280FF` (Crown Periwinkle)

* **`P4` (Urgent):** `#FF8C1A` (Heartbeat Amber)

* **`P5` (Critical / Immediate):** `#F43F5E` (Crimson Rune with subtle `#F43F5E33` glow)

#### Typed Comments & Artifacts

| Comment Kind | Accent Color | Icon / Prefix Concept | 
| ----- | ----- | ----- | 
| `progress` | `#34B3E4` (Glacier Cyan) | Forward chevron / step pulse | 
| `question` | `#FFC425` (Beacon Gold) | Amber query beacon | 
| `decision` | `#8280FF` (Crown Periwinkle) | Architectural lock / gavel | 
| `handoff-note` | `#FF8C1A` (Heartbeat Amber) | Relay baton between actors | 
| `note` | `#7E92B0` (Steel Mandible) | Standard context annotation | 

### 3.3 Ready-to-Use CSS Custom Properties

Drop this directly into the minimal web UI stylesheet:

```
:root {
  /* Surfaces */
  --ravn-bg-void: #080B16;
  --ravn-bg-column: #0E1326;
  --ravn-bg-card: #151C36;
  --ravn-bg-card-hover: #1C2547;
  --ravn-border-facet: #263159;
  --ravn-border-focus: #5C59E8;

  /* Typography */
  --ravn-text-primary: #E2ECF7;
  --ravn-text-secondary: #94A6C4;
  --ravn-text-muted: #596A8A;

  /* Brand Indigo (Raven Plumage) */
  --ravn-indigo-deep: #232070;
  --ravn-indigo-primary: #3B37B8;
  --ravn-indigo-electric: #5C59E8;
  --ravn-indigo-crown: #8280FF;

  /* Brand Amber (Twin Diamonds & Atomic Claims) */
  --ravn-amber-gold: #FFC425;
  --ravn-amber-heartbeat: #FF8C1A;
  --ravn-amber-ember: #F25C19;

  /* Brand Cyan (Beak, SSE & Evidence Artifacts) */
  --ravn-cyan-glacier: #34B3E4;
  --ravn-cyan-fjord: #1D78A3;

  /* Workflow & Semantic Status */
  --ravn-status-todo: #7E92B0;
  --ravn-status-doing: #FF8C1A;
  --ravn-status-done: #10B981;
  --ravn-status-blocked: #F43F5E;
}

```

## 4. Typography & Hierarchy

Rävn's interface bridges human project management and machine-level telemetry. Typography must render Markdown prose effortlessly while keeping IDs, versions, and TTL timers scannable at a glance.

### 4.1 Font Stack

* **Primary UI & Markdown Prose (`--font-sans`):**

  `'Inter', 'Plus Jakarta Sans', -apple-system, BlinkMacSystemFont, sans-serif`

  *Requirement:* Must feature a clean, unmistakable diaeresis on **`ä`** and support tabular numerals (`font-variant-numeric: tabular-nums`).

* **Telemetry, Identifiers & Code (`--font-mono`):**

  `'JetBrains Mono', 'Geist Mono', 'Fira Code', monospace`

  *Usage:* Task IDs, optimistic version counters (`v12`), TTL lease countdowns (`04m 32s`), actor handles (`agent:opencode-1`, `human:soren`), artifact types (`command-output`), and MCP tool names.

### 4.2 Typographic Scale (Web UI)

* **Board / Project Title:** `1.25rem` ($20\text{px}$), `font-weight: 700`, tracking `-0.02em`, color `--ravn-text-primary`.

* **Column Header (`TO DO` / `DOING` / `DONE`):** `0.75rem` ($12\text{px}$), `font-weight: 700`, uppercase, tracking `0.08em`, paired with a monospace count pill.

* **Task Card Title:** `0.9375rem` ($15\text{px}$), `font-weight: 600`, line-height `1.4`.

* **Markdown Body (Descriptions & Durable Notes):** `0.875rem` ($14\text{px}$), line-height `1.6`, max line width `68ch` in task detail drawers.

* **Telemetry Micro-Copy (Claims, Versions, Timestamps):** `0.75rem` ($12\text{px}$), `--font-mono`, `font-variant-numeric: tabular-nums`.

## 5. UI & Component Visual Language

Even with a minimal web UI, subtle structural choices make the board unmistakably **Rävn**:

1. **Faceted Geometry over Soft Bubbles:**

   * Avoid overly rounded pill-shaped cards (`border-radius: 24px`). Use crisp, structural radii (`6px` for task cards, `4px` for badges and inputs) with `1px solid var(--ravn-border-facet)` borders that echo the polygonal facets of the raven logo.

2. **Distinguishing Assignee vs. Atomic Claim:**

   * **Durable Assignee** (ownership): Rendered as a quiet indigo/slate pill (`👤 human:alex` or `🤖 agent:architect`).

   * **Active Atomic Claim** (ephemeral lock): Rendered as an illuminated **Heartbeat Amber** border/badge (`◆ CLAIMED • agent:worker-02 • TTL 03:45`) so humans immediately see which tasks have an active agent process attached.

3. **The Twin-Diamond Live Indicator:**

   * In the top navigation bar, use the twin diamonds (`◆◆`) as the realtime SSE status indicator:

     * **SSE Connected (Live):** Both diamonds glow **Glacier Cyan** (`#34B3E4`) or **Heartbeat Amber** (`#FF8C1A`).

     * **15s Poll Fallback:** Diamonds dim to **Steel Mandible** (`#7E92B0`) with a dashed outline.

     * **Disconnected / Auth Gate:** Diamonds turn **Crimson Rune** (`#F43F5E`).

4. **Durable Notes ("Resume Context") Highlighting:**

   * Inside the Task Detail view, style the **Durable Notes** section as a dedicated *"Session Resume Payload"* callout box with a left border of `3px solid var(--ravn-indigo-crown)`. This visually reinforces that notes are replace-on-write state meant for fresh agent survival.

## 6. Voice, Tone & Copywriting Guidelines

Rävn speaks to two distinct audiences simultaneously: **Systems-minded developers** (via documentation, READMEs, and the web UI) and **AI Coding Agents** (via MCP tool schemas, onboarding prompts, and resource templates).

### 6.1 Brand Personality

* **Architectural & Precise:** We use accurate distributed-systems terminology (*CAS*, *TTL lease*, *optimistic versioning*, *idempotent*, *replace-on-write*).

* **Zero AI Hype:** We never use vague buzzwords like *"supercharge your 10x AI workforce"* or *"magical autonomous synergy."* We solve concrete failure modes: context death, race conditions on tasks, and unverified completions.

* **Dry Nordic Clarity:** Direct, understated, and high-signal. Every sentence earns its token budget.

### 6.2 Canonical Glossary (Use Exact Terms)

Always maintain strict distinction between these domain concepts in UI labels, docs, and MCP prompts:

| Term | Definition & Copy Rule | Never Confuse With | 
| ----- | ----- | ----- | 
| **Claim** | Ephemeral, TTL-leased lock (`claim_task`, `heartbeat_claim`, `release_claim`) preventing concurrent duplicate work. | **Assignee** (which is durable ownership that persists across claims). | 
| **Durable Note** | Replace-on-write markdown state attached to a task for fresh session resumption. | **Typed Comment** (which is an append-only chronological log entry). | 
| **Artifact** | Typed evidence link (`log`, `screenshot`, `command-output`, `trace`, `url`, `other`) proving work was completed. | Generic file attachment. Always frame artifacts as **Evidence / Proof of Done**. | 
| **Project Description** | Markdown document that doubles as the **Agent Onboarding Doc** for that project. | A decorative summary. Remind users that agents read this first. | 
| **One-Call Resume** | The `get_task` payload containing task metadata, durable notes, ACs, relations, recent comments, and artifacts in one round-trip. | Paginated/fragmented task fetching. | 

### 6.3 Copywriting Do's & Don'ts

| Context | ❌ Don't (Generic SaaS / Hype) | ✅ Do (Authentic Rävn Voice) | 
| ----- | ----- | ----- | 
| **Hero Value Prop** | *The smart AI-powered project management platform where bots and humans collaborate seamlessly!* | *MCP-first Kanban for AI coding agents and humans. External memory, atomic claims, and verifiable proof of done.* | 
| **Empty Durable Note State** | *Write anything you want to remember about this task here!* | *No resume context recorded. Replace-on-write notes written here survive session death and are returned in `get_task`.* | 
| **Blocked Task Warning** | *Oops! You can't start this yet because another task isn't finished.* | *Blocked by unfinished dependency `#42`. Resolve blocking tasks before claiming.* | 
| **Optimistic Lock Conflict** | *Something went wrong saving your changes. Please try again.* | *Version conflict (`expected v4, actual v5`). Another actor mutated this task. Re-fetch via `get_task` and retry.* | 
| **OpenCode Dispatch Button** | *✨ Ask AI to Magic-Solve Task* | *Dispatch OpenCode Session* | 

### 6.4 Writing MCP Tool Descriptions (Copy for Agents)

In an MCP-first product, **tool descriptions and onboarding prompts are primary product copy.** When writing or updating the 14 MCP tools:

1. **Lead with the Verb + Mutation Scope:** State immediately whether the tool reads, mutates with a version guard, or acquires a lease.

2. **State Preconditions & Side Effects:** Explicitly tell the agent what happens on conflict or expiry (e.g., *"Requires current task `version`. Fails with conflict if stale. Emits an SSE event."*).Rävn

   MCP-first, multi-project KanBan for humans and AI agents.

   ## What it is

   Rävn is an MCP-first KanBan tool where AI coding agents are the primary users. It serves as external memory (state survives session death), a coordination bus for multi-agent work (claims, handoffs, no duplicate work), and an evidence store (proof of done). The human web board is secondary. The system is single-tenant with a dependency-minimal Go backend in onion architecture, Docker-first deployment (main server + stateless MCP server containers), and pure-Go SQLite (no CGO).

   ## MVP features

   * **Projects**: Create and manage projects with name and markdown description (doubles as agent onboarding doc).

   * **Tasks**: Markdown title/description, priority (0-5), due date.

   * **Workflow**: To Do / Doing / Done status transitions recorded as events.

   * **Atomic claims**: Claim/release/heartbeat with TTL lease; CAS prevents duplicate grabbing; TTL expiry auto-heals zombie claims.

   * **Typed assignee**: Durable ownership (human or agent identity) distinct from ephemeral claim.

   * **Durable notes**: Replace-on-write resume context for fresh session survival.

   * **Typed comments**: Progress / question / decision / handoff-note / note kinds with actor and timestamp.

   * **Acceptance criteria**: Ordered checklist with optional completion notes.

   * **Artifacts**: Typed evidence links (log / screenshot / command-output / trace / url / other) attached to tasks.

   * **Relations**: Parent/child, blocked-by, duplicate-of, related edges; computed "blocked" flag; duplicate warnings on create.

   * **Optimistic versioning**: Version-guarded writes with full activity log on every mutation.

   * **Realtime events**: Per-project SSE streams; every mutation emits a typed event (the UI updates live, with a 15s poll fallback).

   * **Optional token auth**: `RAVN_TOKEN` gates all `/api/v1/*` routes when set (`Authorization: Bearer …` or `?token=…` for SSE clients); off by default.

   * **Observability**: `/healthz` liveness + in-memory metrics (Prometheus text or JSON) at `/metrics`.

   * **Agent-first reads**: Filtered list_tasks (project/status/assignee/unblocked/search/date-range) with canonical sort and pagination; get_task returns one-call resume payload.

   * **Minimal web UI**: Board columns by status plus task detail (notes, ACs, comments, artifacts), project settings, relations editor, and drag-and-drop status moves.

   * **OpenCode dispatch**: Start an autonomous agent session per task via `POST /tasks/{id}/opencode/start` or the board launcher.

   * **MCP tool surface**: 14 tools plus onboarding prompts and read-only task/project resources for agents to interact with the system.

   ## 

3. **Guide Workflow Etiquette in Prompts:**

   * *Before starting work:* Call `claim_task` (verify `blocked == false`).

   * *During long execution:* Send `heartbeat_claim` before TTL expiry and log `progress` or `decision` comments.

   * *Before ending a session or handing off:* Overwrite **Durable Notes** with exact resume state, check off verified **Acceptance Criteria**, attach **Artifacts** (`command-output` / `log`), and `release_claim`.