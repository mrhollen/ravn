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

**After Phase 1 (main `ab1644d`, measured 2026-11-11):** `app.ts` shrank to
**6,154** lines and seven pure cores now sit beside the existing flat modules:
`theme.ts` (48), `sort.ts` (170), `routes.ts` (162), `filters.ts` (159),
`columns.ts` (86), `keyboard.ts` (133) and `ac-math.ts` (53); `moves.ts` grew
 117 → 180 to host the drag/swipe gesture decisions. Nothing has moved into
`modules/` yet — Phase 2 starts from this state.

**After Phase 2 batches A+B (main `44b0228`, measured 2026-11-12):** `app.ts`
is **5,807** lines and `web/src/modules/` holds its first five feature
modules — `auth.ts` (265), `projects.ts` (196), `menus.ts` (137),
`board.ts` (474) and `board-view.ts` (458) — plus their unit tests under
`web/test/`.

**Target layout:**

```
web/src/
  app.ts              # composition root ONLY: instantiates modules, exposes the
                      # flat Alpine namespace, boot/init wiring. Shrinks over time.
  main.ts             # unchanged (Alpine.data("app", ravnApp))
  types.ts            # unchanged (wire contracts)
  api.ts util.ts md.ts focus.ts …   # existing pure cores — stay flat, not moved
   theme.ts sort.ts routes.ts filters.ts columns.ts   # NEW in Phase 1 (landed): pure cores (flat, like the rest)
   keyboard.ts ac-math.ts               # NEW in Phase 1 (landed): pure cores; moves.ts extended with gesture decisions
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
 | Card density (reader :311–327; state/toggle :1948–1963) | :311–327, :1948–1963 | `modules/board.ts` | **P2 landed (batch B)** — toggle + LS round-trip in the module; the bound `cardDensity` field stays at root |
| Task-route deep-link helpers `taskRouteIdFromPath`/`taskRoutePath` (:328–349); mobile detail viewport check (`isMobileDetailViewport`, :467–483) | :328–349, :467–483 | `src/routes.ts` | P1 (already pure top-level exports — move + re-export) |
| Board query param parse/build (:350–466); `readStoredTaskScope`/`TaskScope` (:81–109) | :350–466, :81–109 | `src/routes.ts` (query params), `src/filters.ts` (task scope is a board filter) | P1 |
| Clipboard helper `copyTextToClipboard` (:484+) | :484+ | `src/util.ts` (append) | P1 (DOM glue, no state) |
| SSE event-detail formatting `formatEventDetail` (:110–219) | :110–219 | `src/realtime.ts` (append) | P1 (pure string building) |
| Actor name + `actor()` (:634–649) | :634–649 | **root** | stays: every mutating handler builds the `ActorInput` from it — shared infra in the same class as toasts; revisit only if a module needs it independently |
| Document title composition (`TITLE_SEPARATOR` :224; `updateDocumentTitle` :675–730) | :675–730 | **root** | stays: 5-line join over `projects`/`detail`; not worth an interface boundary |
| Toasts (interface :600–619; methods :715–779) | :600–619, :715–779 | **root** | stays: cross-cutting service every module emits into — extracting it creates a bus that all modules depend on. Revisit in P2 if two or more modules need richer toast behavior |
| Auth token + session modal + boot-401 continuation (:780–996) | :780–996 | `modules/auth.ts` | **P2 landed (batch A)** — behavior + typed access interface; the 10 bound fields remain root fields (see Phase 2 status note). Pure session client already in `src/identity.ts` |
| Focus session bookkeeping (:997–1016) | :997–1016 | `src/focus.ts` (existing — fold residual state into its factory) | P2 |
| Projects list, selection, counts, create-project dialog (:1017–1099, :1336–1380) | :1017–1099, :1336–1380 | `modules/projects.ts` | **P2 landed (batch A)** — list lifecycle (sequence guard + in-flight coalescing now module-owned internals), option/select labels and the create-project dialog moved behind the typed `ProjectsDeps`; `LS_PROJECT` moved here too. The bound fields remain root fields (see Phase 2 status note). `selectProject`'s selection cascade stayed at root until the board slice and now lands there (batch B): it calls both board modules through their root seams (`resetBlockedCache`, `invalidatePendingLoads`, `cancelFullTextSearch`) plus the SSE/URL wiring. The dropdown's menu machinery is in `modules/menus.ts` (batch A) |
| Projects dropdown — custom menu (:1100–1232) ("project menu" ~:1103 in the audit) | :1100–1232 | `modules/menus.ts` | **P2 landed (batch A)** — open/close, roving arrow/Home/End focus and the capture-phase outside-click listener live in the shared controller; per-menu state (`projectMenuOpen`) stays a root field, and `pickProject`'s close-then-select cascade landed with the board slice (batch B): close via the controller, then the root's selection cascade. Non-DOM coverage: `web/test/menus-module.test.ts` |
| Header settings menu (:1233–1335) ("header menu" ~:1240 in the audit) | :1233–1335 | `modules/menus.ts` (controller); its *contents* belong to their own owners (theme → `src/theme.ts`, auth entry → `modules/auth.ts`, auto-refresh → board/live) | **P2 landed (batch A)** — same shared controller; the "at most one header menu" mutual exclusion (opening the settings menu collapses the project dropdown) stays at root because both flags are root fields. Non-DOM coverage: `web/test/menus-module.test.ts` |
| OpenCode options list (:1381–1439) + drawer run panel (:3924–4027) | :1381–1439, :3924–4027 | `modules/opencode.ts` | **P2 landed (batch B)** — the options cache read surface (agents/models/variant derivation) and `fetchOpenCodeOptions` (id gate, no-server-URL gate with the open-modal unsaved-URL exception, per-project cache + force, silent catch) live in the module behind typed `OpenCodeDeps`; all three cache fields remain plain root state. The drawer run panel (`taskOpenCodeConfig` + its defaults) stays at root until the detail slice |
| Project settings modal (:1440–1468) + members panel T9 (:1469–1790) | :1440–1468, :1469–1790 | `modules/project-settings.ts` | **P2 landed (batch B)** — form lifecycle with the pristine baseline re-based on the authoritative fetch, the discard guard, the archive-confirm flow + save/restore, and the T9 members panel live in the module closure behind typed `ProjectSettingsDeps`; the roster pure core was already in `src/members.ts` (`normalizeRoster`, `canManageRoster`, error copy). The ten bound fields stay root fields; the baseline copy and the focus session are module-internal. Non-DOM coverage: `web/test/project-settings-module.test.ts` |
 | Board state: snapshot, blocked-state cache + derive/sync/reset (:1791–1890), `loadBoard` sequencing | :1791–1890 | `modules/board.ts` (cache + prefs, P2) / `modules/board-view.ts` (`loadBoard` lifecycle) | **P2 landed (batch B)** — scope-guarded blocked cache (board-snapshot sync is in-module, no standalone pure sliver), detail learning seam and reset; the sequence-guarded fetch lifecycle lives with the view pipeline (see status note for the core/view split rationale) |
 | **Board/columns derivation** `columns()` (:2941–3070): filter → sort → done-pagination pipeline; visible/hidden counts + filtered-empty banner (:2906–2921) ("board/columns" ~:2946 in the audit) | :2890–3070 (inside URL-sync section) | `src/filters.ts` + `src/sort.ts` — P1 pure pipeline over plain args; `modules/board-view.ts` owns the state it composes | **P2 landed (batch B)** — composition (filter predicates, sort+hoist, FTS id union, done-cap bypass) in the view module; the flat pure cores are untouched and still imported by it |
| Archive management: single-task archive/restore w/ version guard + retry-once (:1964–2195); bulk "Archive all Done" (:2196–2256) | :1964–2256 | `modules/archive.ts` | P2 (the locate-row-in-live-archived-detail helper becomes a pure fn with injected state) |
 | Done-column pagination (`DEFAULT_DONE_LIMIT` :221; :2257–2271) | :2257–2271 | `modules/board-view.ts` (cap + bypass logic) / P1 slice inside the `columns()` pipeline | **P2 landed (batch B)** — the 20-card cut, its count pills and the full-text-search bypass live in the view module; the bound `doneLimit` field stays at root |
| Multi-select + batch bar (:2272–2406), robust per-task batch execution engine AC3 (:2407–2489), batch actions AC2/AC3 (:2489–2568) | :2272–2568 | `modules/batch.ts` — P1-pure: the per-task execute/retry-once/skip-already-applied engine (`runBatch(tasks, exec)` with an injected executor); P2: selection state + shift-range anchor | P1+P2 |
 | Filters state + predicate application (:2569–2746): priority/assignee/unblocked/stalled/needs-attention/due-window/search predicates | :2569–2746 (+ inline in `columns()`) | `src/filters.ts` — P1: every predicate as a pure fn `(task, ctx) => boolean` + composition helper; the `filter*` state lives behind typed accessors owned by both board modules | **P2 landed (batch B)** — `hasActiveFilters`, `clearFilters` and `availableAssignees` live in `modules/board.ts`; the local title/search predicate, server-side mapping (`listTasksParams`) and visible/hidden counts compose in `modules/board-view.ts`. The bound `filter*` fields stay at root |
 | Full-text (desc/notes) remote search (:2587–2746; constants :228/:232) | :2587–2746 | `src/filters.ts` — P1: result merge/dedupe against live board rows; debounce+in-flight state in `modules/board.ts` (debounce core already in `src/refresh.ts`) | **P2 landed (batch B)** — the 250ms debounce, sequence guard and match-id set are module-internal state; `SEARCH_DEBOUNCE_MS` / `FULLTEXT_SEARCH_LIMIT` moved to `modules/board.ts` and are re-exported from the root unchanged |
 | URL query sync handlers (`syncUrlState`, popstate/deep-link adoption) (:2747–3165) | :2747–3165 | parse/build pure core → `src/routes.ts` (P1); the handlers themselves → `modules/board-view.ts` (board/filter URL parts, P2); boot-time task-route adoption stays composition-root wiring (§5 step 4) | **P2 landed (batch B)** — `syncUrlState` (immediate + 150ms debounced, task-route guard), `applyQueryParams` (popstate full-restore vs partial initial restore) and `initFromUrl` live in the view module; land-target deviates from the originally planned single `board.ts` (see status note) |
| Realtime SSE: connect/backoff/replay cursor/disconnect (:3166–3418) + polling fallback (:3419–3462); `sseTelemetry` test-support counters (:585–598) | :585–598, :3166–3462 | `modules/sse.ts` (P2 state; pure dispatch already in `src/realtime.ts`, coalescing in `src/refresh.ts`; the thin adapter pattern at app.ts:3310–3360 is the model) | **P2 landed (batch B)** — stream lifecycle, backoff, replay cursor, disconnect toast + polling fallback live in the module closure behind typed `SseDeps` (state-in/out via lambdas; toast/dismiss/reload/refresh hooks). `POLL_MS`, `LEGACY_NAMED_SSE_EVENT_KINDS` (doc + const) and the `sseTelemetry` counters moved here and are re-exported from the root unchanged. The bound `sseStatus` / `polling` fields stay root fields; the EventSource handle, timers, backoff and cursor are module-internal. The root's `dismissToast` clears the disconnect-toast guard through the module seam (`clearSseDisconnectNotificationIf`). Test support: `backoffMs()` / `pollTimerId()` read-only accessors on the returned surface (same pattern as `sseTelemetry`) plus pure helpers `nextBackoffMs` / `shouldSendReplayCursor` / `buildSseUrl`. Non-DOM coverage: `web/test/sse-module.test.ts` |
| New-task form + create flow (:3463–3621) | :3463–3621 | `modules/new-task.ts` | **P2 landed (batch B)** — dialog lifecycle, dirty-draft guard and the whole create flow behind typed `NewTaskDeps`; every bound field (incl. `newTaskTarget`, which the modal header renders) stays plain root state; only the focus session is module-internal. Keyboard dispatch (`n` / Escape cascade) keeps calling the flat surface at root |
| Task detail drawer state/lifecycle (:3622–3646) + full-page `/tasks/<id>` view (:3647–3923) ("routes" ~:3650 / "detail+AC" in the audit) | :3622–3923 | `modules/detail.ts` (P2 state; `canApplyDetail` ownership guard already pure in `src/refresh.ts`; route helpers P1 in `src/routes.ts`) | P1+P2 |
| Copy-ID toolbar action (:4028–4181) | :4028–4181 | `modules/detail.ts` (drawer toolbar behavior; clipboard core P1 → `src/util.ts`) | P1+P2 |
| Quick metadata (priority & due popovers, :4182–4321) + assignee popover (:4322–4398) + title/description edit with dirty drafts (:4399–4584) + notes scratchpad (:4994–5097) | :4182–4584, :4994–5097 | `modules/task-edit.ts` (P2; draft serialize keys already in `src/drafts.ts`) — all "edit a field on the open task" behavior, one module | **PARTIAL (batch B)** — core edit session AND quick-metadata popovers + assignee update flows landed (the four-popover mutex writes the status/owner flags through typed setters; `toggleStatusMenu`/`toggleOwnerPopover` stay with the operational bar → detail); only the notes scratchpad still rides at root |
| Acceptance criteria (:5098–5359) ("detail+AC" ~:5115 in the audit) | :5098–5359 | `modules/acs.ts` | **P2 landed (batch C)** — toggle/add/paste-add/move/drop reorder, the inline edit session and delete, plus the four display helpers over `src/ac-math.ts`. All nine state fields are template-bound → stay root; detail patches are whole-object reassignments through `setDetail`. Non-DOM coverage: `web/test/acs-module.test.ts` (34 tests) |
| Comments (:5360–5481) | :5360–5481 | `modules/comments.ts` | **P2 landed (batch C)** — draft reset-then-restore + debounced persistence, older-comment pagination and postComment. All five state fields template-bound → root; `detail.comments` / `commentForm.body` are property writes through the state-IN getter, whole-field writes via setters. Non-DOM coverage: `web/test/comments-module.test.ts` (21 tests) |
| Artifacts (:5482–5728) | :5482–5728 | `modules/artifacts.ts` | P2 (`isTextArtifact` already pure in `src/util.ts`) |
| Claims: display helpers + claim/release handlers (:5729–5825). **Exception:** the 1s `nowTick` ticker lives here but is consumed by BOTH claims display and the board stalled filter → `nowTick` stays **root** (shared clock); pure countdown/expiry logic already in `src/util.ts` | :5729–5825 | `modules/claims.ts` (P2) + **root** (`nowTick`) | P2 |
| Global keyboard: bare-key dispatch `handleGlobalKeydown` + the Escape cascade `dismissTop` (:6129–6204; absent from the Phase 0 audit — the cascade's priority order is the part that breaks silently when a surface is inserted) | :6129–6204 | `src/keyboard.ts` (P1 decisions, LANDED) + **root** (the imperative close steps ride with their P2 surface owners: menus/detail/new-task) | P1 done / rest P2 |
| Relations (:5826–6208; `RelationView` interface :621–630) | :5826–6208 | `modules/relations.ts` — P1-pure: `RelationRow → RelationView` shaping | P1+P2 |
| Drag & drop (:4585–4696) + touch swipe-to-status + per-card move menu (:4697–4960) | :4585–4960 | `modules/drag.ts` (P2 gesture state; pure move core already in `src/moves.ts`: `canMove`, `beginMove/endMove`, `withTimeout` — **extended in Phase 1** with the drag/swipe *decision* helpers: drop legality copy, swipe constants, direction/target/activation/commit) | P2 |
| Operational bar: drawer popovers + version-guarded `moveStatus` from the drawer (:4961–4993) | :4961–4993 | `modules/detail.ts` (popover state) delegating to the moves core | P2 |
| Template glue (:6209–6248) — binds module methods into the flat namespace | :6209–6248 | **root** (this is what a composition root IS; never extracted) | stays |
| `init()`/boot (:6249–6448): module instantiation, deep-link adoption, boot fetches, global listeners | :6249–6448 | **root** — if Phase 2 leaves the root still above the §6 cap, extract boot wiring into `modules/bootstrap.ts` as the designated escape hatch (team-lead decision with evidence) | stays / bootstrap |

**Phase 1 status (completed on main `ab1644d`, 2026-11-11).** Landed, in slice
order — every commit is behavior-parity-only with its own unit tests (post-
Phase 1 coverage table: `docs/TESTING.md` §2):

- `src/theme.ts` (b171a3d) — theme row.
- `src/sort.ts` (faecaad) — sort row, incl. the hoist-parents comparator and LS round-trip.
- `src/routes.ts` (ac5b621) — deep-link helpers + board query-param parse/build rows.
- `src/filters.ts` + `src/columns.ts` (82ff441) — filter predicate family, full-text merge and the column assembly / done-cap pipeline; the `filter*` state fields and their `$watch` wiring stay at the root until the P2 board module owns them, as planned.
- `src/moves.ts` extended + `src/ac-math.ts` (5b00a40) — drag/swipe decision helpers and AC display math (done counts, progress label, ring geometry).
- `src/keyboard.ts` (ab1644d) — the keyboard row above.

Remaining P1 slivers **not** landed this phase (they ride with their P2
modules or a follow-up pass): clipboard helper → `util.ts`,
`formatEventDetail` → `realtime.ts`, the `blockedIdsFromBoard` set derivation,
and the `RelationRow → RelationView` shaping.

**Phase 2 status (batches A+B landed on `main`).** Landed so far:

- `modules/auth.ts` — auth row, a **behavior-only extraction**: all eight
  handlers moved verbatim behind the typed `AuthDeps` access interface,
  instantiated lazily by the root's `_authModule()` so every accessor closes
  over Alpine's reactive proxy (a method call's `this`).

  **State-ownership deviation from §3.3's `{ state, methods }` example —
  deliberate:** the ten auth fields bound by `index.html` remain plain root
  fields with verbatim initializers; the module never owns them. Rationale:
  (a) constraint #2 requires every bound name to stay a top-level entry of
  `ravnApp()`'s object — nesting state would force ten new forwarding
  getter/setter pairs on the root for zero gain; (b) Alpine 3.x wraps the
  x-data object in Vue's reactive proxy, which observes mutations made
  THROUGH the proxy only — a nested state object captured raw from its own
  closures is unsafe to mutate. The typed accessor lambdas (root implements
  each as `this.x = v`) give the module §4's "no reference to the root"
  guarantee while keeping every binding name, initial value and template
  attribute byte-identical. Non-DOM coverage: `web/test/auth-module.test.ts`.

- `modules/projects.ts` — projects row, same behavior-only pattern (lazy
  `_projectsModule()`, typed `ProjectsDeps`): the list lifecycle
  (`loadProjects` + core), the option/select label derivations, and the
  create-project dialog. The sequence guard and in-flight coalescer are now
  module-internal state — they were never rendered, so moving them out of the
  flat namespace is safe. `LS_PROJECT` moved with its main consumer and is
  re-exported from the root unchanged. Deliberately left at root (for now):
  `selectProject`'s cross-cutting selection cascade (board/search/SSE/URL —
  lands with the board slice) and `onMyProjectsToggle` (shares `LS_MY_PROJECTS`
  with its reader + field initializer). Non-DOM coverage:
  `web/test/projects-module.test.ts`.

- `modules/menus.ts` — the shared custom-dropdown controller batch A
  finished: both header menus (project dropdown + settings menu) now run on
  one `createMenuController(deps, config)` factory — open/close idempotency,
  the capture-phase outside-click listener (module-owned state), roving
  arrow/Home/End focus and the "first/last/selected" initial-focus strategy.
  The bound `projectMenuOpen` / `headerMenuOpen` flags stay root fields (same
  Alpine state-ownership rationale as above), each menu's testid lookups ride
  on injected seam lambdas, and the "at most one header menu" mutual
   exclusion stays at root because it touches both flags. The legacy
   `crowbanApp` alias export (zero references) was removed in the same slice.
   Non-DOM coverage: `web/test/menus-module.test.ts`.

**Batch B (main `44b0228`, 2026-11-12)** — the board concern, split into TWO
stateful modules rather than one:

- `modules/board.ts` (474) — board CORE: the scope-guarded blocked-state
  cache (board-snapshot sync, detail learning seam, reset), subtask meta,
  sort/hoist/density prefs, and the filter surface (`hasActiveFilters`,
  `clearFilters`, `availableAssignees`) plus the debounced full-text search
  (250ms debounce + sequence guard + match-id set are module-internal; the
  `SEARCH_DEBOUNCE_MS` / `FULLTEXT_SEARCH_LIMIT` / `LS_CARD_DENSITY` constants
  and `CardDensity` type moved here, re-exported from the root unchanged).

- `modules/board-view.ts` (458) — board VIEW pipeline: stalled/attention
  helpers, URL query build/parse/sync (`syncUrlState` incl. the task-route
  guard and popstate full-restore), `listTasksParams` mapping, counts +
  `columns()` composition over the flat P1 pure cores (FTS id union, done-cap
  bypass) and the `loadBoard` lifecycle (sequence guard, mine-scope reshape,
  401/403 handling).

  **Why two files (deviation from §1's one-module-per-concern):** the board
  concern's state has two lifecycles — long-lived caches/prefs that survive
  every render (core) and render-driven derivation plus the fetch lifecycle
  (view) — and a single file carrying both was trending past the §6 line cap.
  Both are still "the board module" to the root, which builds them lazily via
  `_boardModule()` / `_boardViewModule()` and wires their cross-seams
  (`syncUrlState`, `isBlocked`, `fullTextMatchIds`, `syncBlocked`,
  `hasActiveFilters`) with setter/getter lambdas in the same style as batch A.
  Every bound field (snapshot, filters, sort, counts) remains a plain root
  field; `selectProject`/`pickProject`/`applyDetail` keep their cascades at
  root and now call both modules through those seams. Non-DOM coverage:
  `web/test/board-module.test.ts` + `web/test/board-view-module.test.ts` over
  the shared fake-deps harness (`web/test/board-harness.ts`); app.test.ts shed
  its 13 internal-poking BlockedCache/FullTextSearch tests for 2 root-wiring
  regression tests (applyDetail seam; pickProject retire/re-arm). `app.ts`
  shrank 6,083 → **5,807** (still over the §6 cap — the remaining batches are
  the plan's reduction path).

- `modules/sse.ts` (433) — realtime SSE + polling fallback: EventSource
  lifecycle with the single-stream guarantee, exponential reconnect backoff
  (2s→16s), the Last-Event-ID replay cursor, the one-time disconnect toast
  (30s arm, root-to-module guard clearing via `clearSseDisconnectNotificationIf`)
  and the ~15s polling fallback. Pure decision helpers extracted for non-DOM
  tests: `nextBackoffMs`, `shouldSendReplayCursor` (cursor project-match gate),
  `buildSseUrl`. The root keeps the bound `sseStatus` / `polling` fields, a
  `_sse` cache + lazy `_sseModule()` builder, and delegation shims for the
  public surface (`connectSse`, `disconnectSse`, `startPolling`,
  `pollingChanged`, `sseStatusWord`); boot wiring (beforeunload teardown,
  post-boot `startPolling`, `continueBoot` connect) is unchanged. The one
  root→module coupling outside the seams: `dismissToast` clears the
  disconnect-toast guard on a manual × of that toast. E2E specs that drove
  internals now route through `app._sseModule()`; app.test.ts shed its
  internal-poking disconnect-notification test (covered in
   `web/test/sse-module.test.ts`). `app.ts` shrank 5,807 → **5,554**.

- `modules/project-settings.ts` (436) — the project settings modal AND its
  in-dialog T9 members panel: the form lifecycle (seeded from the list row on
  open, re-based on the authoritative `getProject` fetch), baseline-relative
  dirty tracking + the confirm-first guarded close, the type-the-name
  archive-confirm flow with its reselection fallback (next project / new-project
  prompt when the list empties), `saveProjectSettings`'s baseline-relative
  empty-string-or-undefined clearing semantics for the opencode fields,
  `restoreProject`, and the members panel (roster load that short-circuits in
  zero-config mode, manage decision, add / role-change / remove with their
  never-throw refetch loops). All ten bound names stay plain root fields; the
  pristine baseline copy and the dialog focus session are INTERNAL to the
  module closure (never rendered — safe to leave the flat namespace). DOM focus
  steps now go through the `focusByTestid` hook so the guard + query stay on the
  root. The lazy `_projectSettingsModule()` builder wires state-in getters,
  state-out setters and the 11 orchestration hooks (toast/actor/dialogs/
  loadProjects/loadBoard/SSE/fetchOpenCodeOptions); sixteen delegation shims
  keep the public surface byte-identical for index.html. app.test.ts's
  baseline-poking discard-guard test moved to
  `web/test/project-settings-module.test.ts` (22 tests, stub deps + patched
  api), and its agent/variant save test now re-bases by re-opening against a
   stubbed `getProject` instead of poking the internal baseline. `app.ts` shrank
   5,554 → **5,353**.

- `modules/opencode.ts` (113) — the OpenCode options cache + fetch: the
  `availableOpenCodeAgents` / `availableOpenCodeModels` read surface and the
  variant derivation (exact ref or model_id match, resolved_model fallback,
  deduplicated union across all models otherwise — exported as the pure helper
  `variantsForModels`) plus `fetchOpenCodeOptions` (id gate, no-server-URL gate
  where an unsaved URL in the open settings modal still counts, per-project
  cache + force, silent error catch). Unlike its batch B siblings this module
  owns NO closure-internal state — all three cache fields stay plain root
  state; the lazy `_opencodeModule()` builder wires six state-in getters and
  three state-out setters, no hooks. Four delegation shims keep the bound names
  byte-identical; the project-settings module's `fetchOpenCodeOptions` hook now
  repoints at this module instead of the root shim. The drawer run panel
  (`taskOpenCodeConfig` + defaults) stays at root — it rides with the detail
  slice. Non-DOM coverage: `web/test/opencode-module.test.ts` (20 tests, stub
  state + patched api); app.test.ts's two OpenCode surface tests are unchanged.
  `app.ts` shrank 5,353 → **5,341** — a small net because nothing moved into a
  closure and the cache fields stayed at root.

- `modules/new-task.ts` (224) — the new-task dialog: lifecycle
  (`openNewTask(status)` / `openNewSubtask(parent)` / single close path
  `closeNewTask()`), the draft-dirty derivation + confirm-first guarded close,
  and the whole create flow — payload assembly, the version-guarded non-todo
  follow-up move (a 409 conflict is advisory; any other failure falls to the
  outer catch), the optional subtask `child` relation link with its failure
  toast, the `possible_duplicates` advisory and the post-POST refresh fan-out.
  Typed `NewTaskDeps`: 6 state-in getters, 7 state-out setters, 8 hooks. Every
  template-bound name stays a plain root field — including `newTaskTarget`,
  which the modal header renders via `statusLabel()` and which therefore could
  NOT be module-internal (learned the hard way: an E2E run caught the missing
  binding before commit); only the dialog focus session is closure-internal.
  Eight delegation shims keep the surface byte-identical; the global keyboard
  dispatch (bare-key open + the Escape cascade) keeps calling those shims at
  root. Non-DOM coverage: `web/test/new-task-module.test.ts` (18 tests, stub
  state + patched api); app.test.ts's CreateTask guard/subtask tests are
  unchanged (they only drive the flat surface and poke the still-root
  `newTaskParentId`). `app.ts` shrank 5,341 → **5,273**.

- `modules/task-edit.ts` (238) — **partial landing of the task-edit row**: the
  core drawer edit session moved verbatim — startEditing (draft seed + pristine
  snapshot + saved-draft restore), cancelEditing, the discard guard pair
  (editDraftDirty / discardEditDraftIfConfirmed), the description draft
  persistence pair and saveTaskEdit with its due/assignee set-or-clear payload
  assembly and version-conflict retry (applyDetail hook). All five fields —
  incl. the `editDraftOriginal` snapshot, never rendered but poked by
  `drafts.test.ts`, so it stays root per the bound-name precedent — remain
  plain root state; the module owns NO closure-internal state (no dialog focus
  session to own). Seven delegation shims keep the surface byte-identical;
  index.html untouched. `descriptionDraftKey` import dropped from app.ts
  (module-only now). Non-DOM coverage: `web/test/task-edit-module.test.ts`.
  `app.ts` shrank 5,273 → **5,176** (−97).

  **Continued in `80b2feb`:** the row's quick-metadata popovers
  (togglePriorityPopover/toggleDuePopover + updatePriority/saveDueDate/
  clearDueDate/updateDueDate) and assignee update flows (saveAssignee/
  clearAssignee/updateAssignee) moved into the same module — 9 more shims,
  suite now at 55 tests (+25; documents the retry's explicit
  `clear_assignee: false` parity quirk). All eight state fields are
  template-bound so they stay root; TaskEditDeps grows to 13 state-in /
  15 state-out / 6 hooks. Only the notes scratchpad remains at root in this
  row (it rides with the detail cascade — `toggleStatusMenu`/`toggleOwnerPopover`
  likewise stay for modules/detail.ts). `app.ts` shrank 5,176 → **5,032**
  (−144; cumulative −241 vs the ≈−298 full-row price — the remainder is the
  notes scratchpad region).

  **`acs` row landed in `60ee932`:** the whole acceptance-criteria section
  moved to `modules/acs.ts` (Phase 2 batch C) — toggleAc (server-row merge +
  failed-toggle checkbox revert), addAc / handleAcPaste multi-line fan-out,
  moveAc / dropAc reorders, the inline edit session, delete with
  editing-session cleanup, and the four display helpers over `src/ac-math.ts`.
  All nine state fields are template-bound → stay root; AcsDeps = 10 state-in
  / 10 state-out / 2 hooks (detail patches via `setDetail` as whole-object
  reassignments). 13 shims; ac-math alias + parseChecklistText imports dropped
  from app.ts. Gates: unit **579/579** (+34), build+tsc clean, E2E 189/189
  (47.8s), index.html identical. `app.ts` shrank 5,032 → **4,901** (−131;
  first sub-5,000 crossing of the burn-down).

  **`comments` row landed in `4efc7ad`:** the comments section moved to
  `modules/comments.ts` (Phase 2 batch C) — draft reset-then-restore +
  debounced persistence, older-comment pagination math/load and postComment
  with its draft-key removal. All five state fields are template-bound → stay
  root; CommentsDeps = 6 state-in / 5 state-out / 2 hooks. `detail.comments`
  reassignment (mergeComments) and `commentForm.body` clear happen as property
  writes through the state-IN getter — only whole-field writes use setters.
  8 shims; commentDraftKey/serialize/deserializeCommentDraft imports dropped
  from app.ts (notes still own notesDraftKey). Gates: unit **600/600** (+21),
  build+tsc clean, E2E 189/189 (46.8s), index.html identical. `app.ts` shrank
  4,901 → **4,856** (−45). Test suite documents two parity quirks:
  `olderCommentsRemaining` is unclamped without a detail, and the postComment
  total double-ratchets (merge + explicit +1).

Suggested P2 ordering (dependency-driven): ~~board~~ → ~~sse~~ → ~~projects~~ →
~~auth~~ → `task-edit` (landed; only the notes scratchpad rides with the
detail cascade) → ~~acs~~ → ~~comments~~ → `artifacts`/`claims`/`relations`
(drawer surfaces) → `detail` **last** —
REORDERED from its original second position: `openTask`/`closeDetail` are
cross-cutting cascades writing ~20 flat fields across the not-yet-modularized
sub-concerns above, so extracted last each cascade write converges to ONE hook
into an already-landed module (same pattern as `selectProject` before the
board slice) → `archive`/`batch`/`drag` (board actions) →
~~menus~~/~~project-settings~~/~~opencode~~/~~new-task~~ (remaining chrome).

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
    100%, `moves.ts` 100% post-Phase 1) and ≥80% for P2 modules; total web line coverage
    may not drop. Caveat: on type-heavy files the Node-26 + jiti line-mapping
    artifact can print a line % below 90 even though raw V8 hit data shows full
    execution — `columns.ts`/`keyboard.ts` are the Phase 1 instances; judge such
    files on V8-truth and document it (`docs/TESTING.md` §2 footnote).
    Re-measure the per-file table in `docs/TESTING.md` §2 when a
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
