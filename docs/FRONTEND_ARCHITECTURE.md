# Frontend Architecture (Phase 0 of the modularization epic)

Audited 2026-11-11 against `web/` at main (`app.ts` = 6,448 lines,
`index.html` = 3,654 lines, 816 `x-*` attribute occurrences, 229
`data-testid` anchors). This document is the planning contract for extracting
the `web/src/app.ts` monolith into modules. **No code moves in Phase 0** —
this file plus the coverage baselines in `docs/TESTING.md` and the file-size
guardrail (§6) are the only deliverables.

## 1. Current state & target layout

`web/src/` today (measured 2026-11-11):

| File | Lines | Role |
|---|---:|---|
| `app.ts` | 6,448 | **the monolith** — one Alpine root factory `ravnApp()` (~205 state+method entries) owning all UI state and behavior; also hosts ~30 top-level pure exports (query-param parse/build, route helpers, sort/theme/density readers) that live outside the factory but still in this file |
| `api.ts` | 567 | fetch client + wire types pass-through |
| `types.ts` | 486 | wire contracts (DO NOT touch — see §3 hard rules) |
| `util.ts` | 359 | pure display/format helpers |
| `focus.ts` | 195 | focus-trap + dialog focus core (already a module) |
| `realtime.ts` | 160 | SSE event dispatch core (pure, hook-injected) |
| `refresh.ts` | 159 | debounce / sequence-guard / in-flight coalescing cores |
| `md.ts` | 153 | markdown rendering wrapper |
| `members.ts` | 120 | roster normalization + permission rules (pure) |
| `moves.ts` | 117 | move legality + per-task lock + request timeout (pure) |
| `identity.ts` | 84 | session client + display-name rules |
| `drafts.ts` | 73 | draft serialize/deserialize keys (pure) |
| `board-scope.ts` | 27 | scoped task-list → Board reshape (pure) |
| `main.ts` / `alpinejs.d.ts` / `vite-env.d.ts` | ≤11 | bootstrap + ambient types |

**Target layout:**

```
web/src/
  app.ts              # composition root ONLY: instantiates modules, exposes the
                      # flat Alpine namespace, boot/init wiring. Shrinks over time.
  main.ts             # unchanged (Alpine.data("app", ravnApp))
  types.ts            # unchanged (wire contracts)
  api.ts util.ts md.ts focus.ts …   # existing pure cores — stay flat, not moved
  theme.ts sort.ts routes.ts filters.ts      # NEW in Phase 1: pure cores (flat, like the rest)
  modules/                    # NEW directory — Phase 2 feature modules live here
    board.ts sse.ts detail.ts task-edit.ts acs.ts comments.ts artifacts.ts …
```

Decisions, and why:

- **Pure cores stay flat in `web/src/`** (no new `lib/` directory). The 12
  existing modules are flat; moving them would be cosmetic churn with zero
  benefit (every import in `app.ts`, `main.ts` and `web/test/*.ts` already
  uses `../src/<name>` / `./<name>`). New pure cores land beside them.
- **Feature-scoped state+handler modules go in `web/src/modules/`.** One
  directory = one rule: "files here own UI state for one concern and are
  instantiated by the composition root." Phase 2 starts empty; the first
  `git mv` into it is deliberate and visible.
- **Dependency direction is strictly outward:**
  `index.html → main.ts → app.ts → modules/* / flat src/*`. No module ever
  imports `app.ts`; nothing outside `app.ts` imports from `modules/`.

**What a "module" is.** A TypeScript file that (a) owns exactly one UI
concern's logic and/or scoped state; (b) talks to the rest of the app only
through explicit typed interfaces (§4) — no `any` at module boundaries;
(c) knows nothing about Alpine: it contains no `x-*` directive knowledge, no
`data-testid` strings, no reference to `index.html`; (d) is instantiated by
the composition root (`ravnApp()` in `app.ts`) or imported as pure functions.
Phase 1 modules are pure-function files (like `moves.ts`). Phase 2 modules
add a stateful factory (`createXxx(deps)`), but the same file may host both —
a concern's pure core and its later stateful adapter share one module file so
the ownership map (§2) stays one row per concern.

## 2. Module ownership map

Every concern in `app.ts` (section line numbers verified against main on
2026-11-11). **Phase**: P1 = pure core extractable now (behavior-preserving
move, no state); P2 = feature-scoped state+handlers that move as a unit in a
later phase; root = deliberately stays in the composition root.

| Concern | `app.ts` location | Target module file | Phase |
|---|---:|---|---|
| Theme types, stored-reader, dark resolution, apply/persist | top :239–256; state :650–735 | `src/theme.ts` | P1 (pure core + LS helpers); thin root adapters keep binding names until P2 |
| Client-side sort: keys/dirs/options/reader (:257–310), sort state+handlers (:1891–1946), hoist-parents comparator logic (:1897–1909, applied inside `columns()`) | :257–310, :1891–1946, inline in `columns()` | `src/sort.ts` | P1 (comparator + LS round-trip pure); state fields stay at root until the board module (P2) |
| Card density (reader :311–327; state/toggle :1948–1963) | :311–327, :1948–1963 | `modules/board.ts` | P2 (trivial persisted toggle; rides with the board view prefs) |
| Task-route deep-link helpers `taskRouteIdFromPath`/`taskRoutePath` (:328–349); mobile detail viewport check (`isMobileDetailViewport`, :467–483) | :328–349, :467–483 | `src/routes.ts` | P1 (already pure top-level exports — move + re-export) |
| Board query param parse/build (:350–466); `readStoredTaskScope`/`TaskScope` (:81–109) | :350–466, :81–109 | `src/routes.ts` (query params), `src/filters.ts` (task scope is a board filter) | P1 |
| Clipboard helper `copyTextToClipboard` (:484+) | :484+ | `src/util.ts` (append) | P1 (DOM glue, no state) |
| SSE event-detail formatting `formatEventDetail` (:110–219) | :110–219 | `src/realtime.ts` (append) | P1 (pure string building) |
| Actor name + `actor()` (:634–649) | :634–649 | **root** | stays: every mutating handler builds the `ActorInput` from it — shared infra in the same class as toasts; revisit only if a module needs it independently |
| Document title composition (`TITLE_SEPARATOR` :224; `updateDocumentTitle` :675–730) | :675–730 | **root** | stays: 5-line join over `projects`/`detail`; not worth an interface boundary |
| Toasts (interface :600–619; methods :715–779) | :600–619, :715–779 | **root** | stays: cross-cutting service every module emits into — extracting it creates a bus that all modules depend on. Revisit in P2 if two or more modules need richer toast behavior |
| Auth token + session modal + boot-401 continuation (:780–996) | :780–996 | `modules/auth.ts` | P2 (pure session client already in `src/identity.ts`) |
| Focus session bookkeeping (:997–1016) | :997–1016 | `src/focus.ts` (existing — fold residual state into its factory) | P2 |
| Projects list, selection, counts, create-project dialog (:1017–1099, :1336–1380) | :1017–1099, :1336–1380 | `modules/projects.ts` | P2 (sequence guard/coalescing already pure in `src/refresh.ts`) |
| Projects dropdown — custom menu (:1100–1232) ("project menu" ~:1103 in the audit) | :1100–1232 | `modules/menus.ts` | P2 — shared custom-dropdown controller (open/close, arrow-key nav, outside-click) reused by both header menus; per-menu state stays at root until then |
| Header settings menu (:1233–1335) ("header menu" ~:1240 in the audit) | :1233–1335 | `modules/menus.ts` (controller); its *contents* belong to their own owners (theme → `src/theme.ts`, auth entry → `modules/auth.ts`, auto-refresh → board/live) | P2 |
| OpenCode options list (:1381–1439) + drawer run panel (:3924–4027) | :1381–1439, :3924–4027 | `modules/opencode.ts` | P2 |
| Project settings modal (:1440–1468) + members panel T9 (:1469–1790) | :1440–1468, :1469–1790 | `modules/project-settings.ts` | P2 (roster pure core already in `src/members.ts`: `normalizeRoster`, `canManageRoster`, error copy) |
| Board state: snapshot, blocked-state cache + derive/sync/reset (:1791–1890), `loadBoard` sequencing | :1791–1890 | `modules/board.ts` (P2 state) — pure sliver P1 in the same file: `blockedIdsFromBoard(board)` set derivation | P1+P2 |
| **Board/columns derivation** `columns()` (:2941–3070): filter → sort → done-pagination pipeline; visible/hidden counts + filtered-empty banner (:2906–2921) ("board/columns" ~:2946 in the audit) | :2890–3070 (inside URL-sync section) | `src/filters.ts` + `src/sort.ts` — P1 pure pipeline over plain args; `modules/board.ts` owns the state it composes | P1+P2 |
| Archive management: single-task archive/restore w/ version guard + retry-once (:1964–2195); bulk "Archive all Done" (:2196–2256) | :1964–2256 | `modules/archive.ts` | P2 (the locate-row-in-live-archived-detail helper becomes a pure fn with injected state) |
| Done-column pagination (`DEFAULT_DONE_LIMIT` :221; :2257–2271) | :2257–2271 | `modules/board.ts` (state) / P1 slice inside the `columns()` pipeline | P1+P2 |
| Multi-select + batch bar (:2272–2406), robust per-task batch execution engine AC3 (:2407–2489), batch actions AC2/AC3 (:2489–2568) | :2272–2568 | `modules/batch.ts` — P1-pure: the per-task execute/retry-once/skip-already-applied engine (`runBatch(tasks, exec)` with an injected executor); P2: selection state + shift-range anchor | P1+P2 |
| Filters state + predicate application (:2569–2746): priority/assignee/unblocked/stalled/needs-attention/due-window/search predicates | :2569–2746 (+ inline in `columns()`) | `src/filters.ts` — P1: every predicate as a pure fn `(task, ctx) => boolean` + composition helper; `modules/board.ts` owns the `filter*` state fields + `$watch` wiring | P1+P2 |
| Full-text (desc/notes) remote search (:2587–2746; constants :228/:232) | :2587–2746 | `src/filters.ts` — P1: result merge/dedupe against live board rows; debounce+in-flight state rides with the board module (debounce core already in `src/refresh.ts`) | P1+P2 |
| URL query sync handlers (`syncUrlState`, popstate/deep-link adoption) (:2747–3165) | :2747–3165 | parse/build pure core → `src/routes.ts` (P1); the handlers themselves → `modules/board.ts` (board/filter URL parts, P2); boot-time task-route adoption stays composition-root wiring (§5 step 4) | P1+P2 |
| Realtime SSE: connect/backoff/replay cursor/disconnect (:3166–3418) + polling fallback (:3419–3462); `sseTelemetry` test-support counters (:585–598) | :585–598, :3166–3462 | `modules/sse.ts` (P2 state; pure dispatch already in `src/realtime.ts`, coalescing in `src/refresh.ts`; the thin adapter pattern at app.ts:3310–3360 is the model) | P2 |
| New-task form + create-and-select (:3463–3621) | :3463–3621 | `modules/new-task.ts` | P2 (discard-guard draft helpers already in `src/drafts.ts`) |
| Task detail drawer state/lifecycle (:3622–3646) + full-page `/tasks/<id>` view (:3647–3923) ("routes" ~:3650 / "detail+AC" in the audit) | :3622–3923 | `modules/detail.ts` (P2 state; `canApplyDetail` ownership guard already pure in `src/refresh.ts`; route helpers P1 in `src/routes.ts`) | P1+P2 |
| Copy-ID toolbar action (:4028–4181) | :4028–4181 | `modules/detail.ts` (drawer toolbar behavior; clipboard core P1 → `src/util.ts`) | P1+P2 |
| Quick metadata (priority & due popovers, :4182–4321) + assignee popover (:4322–4398) + title/description edit with dirty drafts (:4399–4584) + notes scratchpad (:4994–5097) | :4182–4584, :4994–5097 | `modules/task-edit.ts` (P2; draft serialize keys already in `src/drafts.ts`) — all "edit a field on the open task" behavior, one module | P2 |
| Acceptance criteria (:5098–5359) ("detail+AC" ~:5115 in the audit) | :5098–5359 | `modules/acs.ts` | P2 (`parseChecklistText` already pure in `src/util.ts`) |
| Comments (:5360–5481) | :5360–5481 | `modules/comments.ts` | P2 (draft keys already in `src/drafts.ts`) |
| Artifacts (:5482–5728) | :5482–5728 | `modules/artifacts.ts` | P2 (`isTextArtifact` already pure in `src/util.ts`) |
| Claims: display helpers + claim/release handlers (:5729–5825). **Exception:** the 1s `nowTick` ticker lives here but is consumed by BOTH claims display and the board stalled filter → `nowTick` stays **root** (shared clock); pure countdown/expiry logic already in `src/util.ts` | :5729–5825 | `modules/claims.ts` (P2) + **root** (`nowTick`) | P2 |
| Relations (:5826–6208; `RelationView` interface :621–630) | :5826–6208 | `modules/relations.ts` — P1-pure: `RelationRow → RelationView` shaping | P1+P2 |
| Drag & drop (:4585–4696) + touch swipe-to-status + per-card move menu (:4697–4960) | :4585–4960 | `modules/drag.ts` (P2 gesture state; pure move core already in `src/moves.ts`: `canMove`, `beginMove/endMove`, `withTimeout`) | P2 |
| Operational bar: drawer popovers + version-guarded `moveStatus` from the drawer (:4961–4993) | :4961–4993 | `modules/detail.ts` (popover state) delegating to the moves core | P2 |
| Template glue (:6209–6248) — binds module methods into the flat namespace | :6209–6248 | **root** (this is what a composition root IS; never extracted) | stays |
| `init()`/boot (:6249–6448): module instantiation, deep-link adoption, boot fetches, global listeners | :6249–6448 | **root** — if Phase 2 leaves the root still above the §6 cap, extract boot wiring into `modules/bootstrap.ts` as the designated escape hatch (team-lead decision with evidence) | stays / bootstrap |

Suggested P2 ordering (dependency-driven): `board` → `sse` → `projects` →
`auth` → `detail` → `task-edit` → `acs`/`comments`/`artifacts`/`claims`/
`relations` (drawer surfaces) → `archive`/`batch`/`drag` (board actions) →
`menus`/`project-settings`/`opencode`/`new-task` (remaining chrome).

## 3. The driving constraint (why extraction looks the way it does)

1. **The UI surface stays declarative Alpine template in `index.html`, and it
   STAYS there.** No migration to components/framework. Every one of the ~570+
   `x-*` bindings resolves against ONE flat namespace: the object returned by
   `ravnApp()` at `x-data="app()"`. Nested Alpine subcomponents are explicitly
   REJECTED — they would sever those bindings, the 229 `data-testid` anchors
   that the E2E suite locates with, and `web/test/app.test.ts`, which drives a
   real bare-object component.
2. **Extraction keeps ONE flat Alpine root namespace.** Moving code out of
   `app.ts` changes *where logic lives*, never *what name the template binds*.
   Every bound name remains a top-level entry of `ravnApp()`'s returned object
   (a plain field, or an explicit getter/setter pair delegating to a module —
   no hidden spreads into the reactive proxy). No `data-testid` attribute and
   no wire contract in `types.ts` may ever change as a side effect.
3. **Modules are plain TS.** Pure cores = stateless functions/factories.
   Feature modules = `createXxx(deps)` factories returning `{ state, methods }`
   that the root instantiates in `init()` and delegates to through thin
   adapters — exactly the "pure core + thin adapter" pattern already running
   at `app.ts:3310–3312` (`handleSseEvent` → `dispatchRealtimeEvent`).

## 4. Typed contract conventions for modules

`alpinejs.d.ts` gives us nothing (its `AlpineApi.data` takes
`definition: unknown`), so the interfaces below are our own and are the ONLY
type safety at module boundaries. Rules:

1. **One factory per file.** P2 files export exactly one `createXxx(deps)`
   plus its interface(s). P1 files export named pure functions/constants.
   Module-level mutable singletons are forbidden except test-support counters
   (precedent: `sseTelemetry`).
2. **State-in = typed accessor functions, never the root object.** The module
   receives getters that read live state (`selectedProjectId: () => string`),
   closing over `this` at the adapter — the hook pattern from
   `app.ts:3315–3349`. Never pass a reference to the component; never type a
   dependency as `any` or an index signature.
3. **Methods-out = an explicit hooks interface.** Everything the module needs
   to call back (reload scheduling, detail refresh, toasts) is a named method
   on the deps interface. A module that wants something its interface doesn't
   have must grow the interface — not reach into the root.
4. **Allowed imports:** P1 cores: `./types`, `./util` + other flat cores
   (never `app.ts`). P2 modules additionally: `./api` and DOM access only
   through narrow injected seams where testability demands it (precedent:
   `focus.ts` exports its selector; `refresh.ts` injects `TimerApi`).
5. **Timers are cancellable.** Any module owning a timer exposes
   `cancel()`/`destroy()`, is idempotent, and must clear on teardown — the
   boot-idempotency test in `web/test/app.test.ts` asserts zero leaked
   intervals and exactly one SSE stream; new modules keep it green.
6. **Re-export compatibility shim.** Whenever a symbol previously exported by
   `app.ts` moves to a module, `app.ts` re-exports it (`export { x } from
   "./module"`). `web/test/app.test.ts` imports ~18 names directly from
   `../src/app.ts` (see its import list) and that import surface must not
   break — bare-object fakes depend on the `ravnApp()` factory shape staying
   intact.

Concrete examples, in the style of `realtime.ts`/`refresh.ts`:

```ts
// web/src/filters.ts  (P1 pure core — no DOM, no fetch, timer-free)
import type { Status, TaskSummary } from "./types";

/** State-in snapshot the column pipeline needs. Plain data in: the module
 *  must not import app state or take component references. */
export interface BoardFilterState {
  priority: string;            // raw select value; "" = off
  assignee: string;            // "unassigned" | exact name (lc) | ""
  unblockedOnly: boolean;
  stalled: boolean;
  needsAttention: boolean;
  search: string;              // local title/id substring, already lowercased
  nowTick: number;             // ms epoch — the shared root ticker value
  isBlocked: (id: string) => boolean; // state-in accessor (root-owned cache)
}

export interface ColumnView {
  status: Status;
  tasks: TaskSummary[];        // filtered + sorted, done-cap applied
  totalCount: number;          // pre-filter count for the header pill
}

/** Pure pipeline over PLAIN args: filter → sort → done pagination. The root's
 *  `columns()` becomes a one-line delegation to this. */
export function visibleColumns(
  rows: Record<Status, TaskSummary[]>,
  state: BoardFilterState,
  opts: { hoistParents: boolean; sortKey: SortKey; sortDir: SortDir; doneLimit: number },
): ColumnView[] { /* …moved logic, verbatim… */ }
```

```ts
// web/src/modules/sse.ts  (P2 feature module — state + handlers)
import type { SseConnectionStatus } from "../types";

/** State-in / methods-out contract. The composition root passes lambdas that
 *  close over `this` (same shape as the hooks at app.ts:3315–3349). */
export interface SseDeps {
  // state-in — live accessors, not copies:
  selectedProjectId(): string;
  isDetailOpenFor(taskId: string): boolean;
  // methods-out — everything the module may call back into the root:
  scheduleReload(): void;                       // coalesced silent reload
  refreshDetail(taskId?: string): Promise<boolean>;
  toast(msg: string, kind?: "error" | "info"): void;
}

export interface SseModule {
  state: { sseStatus: SseConnectionStatus; reconnecting: boolean };
  connect(projectId: string): void;             // exponential backoff 2s..16s inside
  disconnect(reason?: "switch" | "logout"): void;
  destroy(): void;                              // idempotent teardown, clears all timers
}

export function createSseModule(deps: SseDeps): SseModule { /* … */ }
```

Root delegation (what `app.ts` grows per module — this keeps the flat
namespace auditable and the bare-object fakes working):

```ts
// app.ts — thin adapter, names unchanged for index.html:
_sse: null as ReturnType<typeof createSseModule> | null,

// Bound names that live in a module are exposed as explicit getter/setter
// pairs (Alpine's reactive proxy tracks accessors; tests can still do
// `app.sseStatus = "closed"` and hit the setter):
get sseStatus() { return this._sse?.state.sseStatus ?? "closed"; },
set sseStatus(v: SseConnectionStatus) { if (this._sse) this._sse.state.sseStatus = v; },

connectSse(): void {
  const m = createSseModule({
    selectedProjectId: () => this.selectedProjectId,
    isDetailOpenFor: (id) => Boolean(this.detailOpen && this.detail?.id === id),
    scheduleReload: () => this.sseReloadDebounce?.schedule(),
    refreshDetail: (id) => this.refreshDetail(true, id),
    toast: (msg, kind) => this.toast(msg, kind),
  });
  this._sse = m;
  m.connect(this.selectedProjectId);
},
```

## 5. Extraction checklist (do it exactly like this, every time)

Two developers extracting the same concern in isolation must land on the same
result. Steps for moving concern **X**:

1. **Inventory before touching.** Read X's section(s) in `app.ts` and list:
   every state field it owns, every method (bound or not), every top-level
   export it defines, every cross-concern call it makes (`this.otherThing()`).
   Grep `web/test/app.test.ts` for each exported symbol (its import list at
   the top names everything tests depend on) and grep `index.html` for each
   bound method name. Write the inventory in the PR description.
2. **Contract first, move second.** In the target file (`src/<x>.ts` for P1,
   `src/modules/<x>.ts` for P2), write the interfaces from §4 first — state-in
   accessors and methods-out hooks derived from the inventory. Only then
   cut-and-paste the logic. **Rename-only refactor rule:** extraction commits
   move code verbatim; if a line's behavior changes, that is a separate commit
   (and a separate review) so `git diff` of the move is provably behavior-
   preserving.
3. **Wire the thin adapter in `app.ts`.** Import from the module; replace the
   moved bodies with delegating methods whose names are byte-identical to the
   old ones (index.html binds by name). Instantiate stateful modules in
   `init()`/`connect` paths exactly once; teardown on every existing close
   path. Re-export any symbol that previously came from `app.ts` (§4 rule 6).
4. **Flat-namespace verification.** Diff the key list of `ravnApp()`'s
   returned object before vs after — a throwaway node script (jiti, fake
   window like `bootIdempotencyHarness`) that prints `Object.keys(ravnApp())`
   sorted; commit nothing from it. Every name must still be present and
   writable (field or setter-backed). `git diff -- index.html` must be EMPTY;
   if the template needed a change to make behavior work, the extraction is
   wrong — stop and fix the adapter instead.
5. **Tests: move assertions, don't duplicate them.** Create/extend
   `web/test/<x>.test.ts` (node:test + jiti, module-prefixed titles per
   `docs/TESTING.md` §3) covering the extracted logic by calling the module
   directly. In `app.test.ts`, convert component-mediated tests of moved
   logic to call the new function; keep only true adapter/integration
   assertions on the root. Never weaken an existing assertion during a move.
6. **Run, in this order:**
   ```bash
   cd web && npm run test:unit      # ≤15s budget — includes the §6 guardrail
   cd web && npm run build          # tsc catches contract/type drift
   cd web && npm run test:e2e       # ~47s; run on EVERY extraction (the data-
                                    # testid locators are the contract proof)
   ```
7. **Coverage & baselines.** `npm run test:unit:coverage`: the new file must
   be ≥90% lines at birth for P1 pure cores (precedent: `board-scope.ts`
   100%, `moves.ts` 97.44%) and ≥80% for P2 modules; total web line coverage
   may not drop. Re-measure the per-file table in `docs/TESTING.md` §2 when a
   phase lands and update it with the date. If `app.ts` crosses below the §6
   cap, remove its allowlist entry in the same commit that made it happen.
8. **Commit shape.** One concern = one PR: `[move] <concern> → <target>` (the
   move + adapter + re-exports), plus optionally a follow-up `[test]` commit
   for new direct tests of the pure core. The guardrail allowlist entry, if it
   changed, lands in the same commit as the line-count change that justified
   it.

## 6. File-size guardrail (design; implementation: `web/test/file-size.test.ts`)

**Mechanism.** A dependency-free `node:test` unit test — NOT a linter rule —
asserts that no TypeScript source under `web/src/` exceeds **800 lines**.
Chosen over adding a lint tool because the repo's web stack is deliberately
dependency-light (`docs/TESTING.md` §3) and `npm run test:unit`
(`node --import jiti/register --test test/*.test.ts`) already runs every file
matching `web/test/*.test.ts`, so the guardrail lands in **both** local runs
and both CI workflows (GitHub + Forgejo invoke the same npm script — see
`docs/TESTING.md` §1) with zero workflow edits.

**Scope.** `web/src/**/*.ts` only. `index.html` (template — exempt by design,
it is not a monolith risk in the same sense), `style.css`, `web/e2e/` and
`web/test/` are out of scope. Line count = newline-separated segments with a
trailing final newline not counted as an extra blank line (matches `wc -l`).

**Cap: 800 lines.** Rationale: the largest *healthy* file today is `api.ts`
at 567; 800 leaves ~40% headroom above every existing non-monolith file, so
no legitimate module hits it while a concern that does should be split along
surface lines (§2 shows the splits are already known) rather than the cap
being raised. **The cap is never raised.**

**Allowlist (the shrinking part).** An explicit in-test set of relative paths
allowed to exceed the cap. Today it contains exactly one entry:

```
app.ts  — 6,448 lines (measured 2026-11-11)
```

Rules that keep the allowlist honest and monotonically shrinking:

- An entry is removed the moment its file drops to ≤800 lines. The test also
  **fails on stale entries**: an allowlisted file that now measures ≤800 is a
  violation — so the list can only shrink, never rot.
- Files are never added silently. Adding an entry requires (a) justification
  in the commit message and (b) it must name the extraction path that will
  remove it. The expectation is that the allowlist becomes **empty** as the
  Phase 2 extractions complete (§2's module split makes every concern <800 by
  construction).
- If, after all planned extractions land, the composition root residue
  (boot/init + template glue + adapters) legitimately still exceeds 800 lines,
  that is a **signal to split further** — the designated escape hatch is
  extracting boot wiring into `src/modules/bootstrap.ts` (§2) — or, failing
  that evidence-backed decision made by the team lead. Not a quiet cap bump.

**Failure output.** On violation the test prints every offending file with its
measured line count vs the cap, plus each allowlisted-but-now-under-cap file
with an instruction to delete its entry — so the fix is always one-line-able.
