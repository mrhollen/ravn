// Board core module — Phase 2 batch B of the frontend modularization epic
// (batch B slice 1). Owns the BEHAVIOR of the board's STATE concerns from app.ts:
//   1. the blocked-state cache — derived from board snapshots + learned from
//      detail payloads, project-scoped and fail-closed;
//   2. client-side sorting / hoisting / card density (persisted prefs);
//   3. the client-side filters incl. the debounced server full-text search
//      (desc/notes) with its sequence guard.
// The render/data pipeline (columns, counts, board fetch, URL query sync)
// lives in modules/board-view.ts; it reaches back here through the
// isBlocked / fullTextMatchIds hooks, and this module reaches forward to it
// through the syncUrlState hook (filter/sort controls sync the URL).
// Every body moved here verbatim from app.ts; see docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html reads/
// writes still lives on the flat component root as a plain field, touched here
// exclusively through the typed accessors on {@link BoardDeps} (see modules/auth.ts).
// The blocked cache, sequence guards and debouncers are INTERNAL state — never
// rendered, never part of the flat namespace — and are owned here.

import { api } from "../api";
import { safeStorage } from "../util";
import { createDebounce, createSequenceGuard } from "../refresh";
import {
  DEFAULT_SORT_DIR,
  SORT_OPTIONS,
  isSortKey,
  persistHoistParents,
  persistSort,
} from "../sort";
import type { SortDir, SortKey } from "../sort";
import { hasAnyFilter, isDescriptionNotesMatch as descNotesMatchFor } from "../filters";
import { STATUSES } from "../types";
import type { BlockerRef, Board, TaskSummary } from "../types";

// ---------------------------------------------------------------------------
// Moved constants (Phase 2 batch B) — the composition root re-exports these so
// the existing import surface (tests, main.ts) stays intact.

/** Debounce window for the server-side description/notes board search. Local
 *  title/id matching stays instant; this only gates the extra round-trip, so
 *  ~250ms keeps typing latency unperceived (same order as SSE_RELOAD_DEBOUNCE_MS). */
export const SEARCH_DEBOUNCE_MS = 250;

/** Page size requested for the full-text query. The server clamps to its max
 *  (100), so this is "as many matches as one page can carry" — matches beyond
 *  a full page simply never surface in the union (documented caveat). */
export const FULLTEXT_SEARCH_LIMIT = 200;

// ------------------------------------------------------------- card density
/** Persisted card-density preference key (moved from app.ts; value unchanged). */
export const LS_CARD_DENSITY = "ravn.card_density";

export type CardDensity = "comfortable" | "compact";

export function isCardDensity(v: unknown): v is CardDensity {
  return v === "comfortable" || v === "compact";
}

/** Read persisted card density preference; anything malformed falls back to comfortable. */
export function readStoredCardDensity(): CardDensity {
  try {
    const raw = safeStorage.getItem(LS_CARD_DENSITY);
    return isCardDensity(raw) ? raw : "comfortable";
  } catch {
    return "comfortable";
  }
}

// ---------------------------------------------------------------------------

/** Typed dependency surface for the board core module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface BoardDeps {
  // -- state-in: live accessors into root-owned board fields -----------------
  selectedProjectId(): string;
  board(): Board | null;
  sortKey(): SortKey;
  sortDir(): SortDir;
  hoistParents(): boolean;
  cardDensity(): CardDensity;
  blockerPopoverTaskId(): string | null;
  filterPriority(): string;
  filterAssignee(): string;
  filterUnblocked(): boolean;
  filterStalled(): boolean;
  filterNeedsAttention(): boolean;
  filterDue(): string;
  filterSearch(): string;

  // -- state-out: root writes, invoked so mutations flow through the root ----
  setSortKey(k: SortKey): void;
  setSortDir(d: SortDir): void;
  setHoistParents(b: boolean): void;
  setCardDensity(v: CardDensity): void;
  setBlockerPopoverTaskId(v: string | null): void;
  setFilterPriority(v: string): void;
  setFilterAssignee(v: string): void;
  setFilterUnblocked(b: boolean): void;
  setFilterStalled(b: boolean): void;
  setFilterNeedsAttention(b: boolean): void;
  setFilterDue(v: string): void;
  setFilterSearch(v: string): void;

  // -- methods-out -------------------------------------------------------------
  /** Sync the URL query after a filter/sort/density change (owned by
   *  modules/board-view.ts — immediate when `true`). */
  syncUrlState(immediate?: boolean): void;
}

export interface BoardModule {
  // blocked-state cache (project-scoped, fail-closed — see factory docs)
  isBlocked(id: string): boolean;
  syncBlockedFromBoard(b: Board): void;
  resetBlockedCache(projectId: string): void;
  taskBlockers(task: TaskSummary): BlockerRef[];
  blockerTooltipText(task: TaskSummary): string;
  toggleBlockerPopover(taskId: string): void;
  learnBlockedFromDetail(
    projectId: string,
    taskId: string,
    blocked: boolean | null | undefined,
    blockers: BlockerRef[] | null | undefined,
  ): void; // detail-payload seam (drawer region)
  refreshBlockedSet(b: Board, isStillCurrent?: () => boolean): Promise<void>;

  // subtask meta / sorting / hoisting / card density
  subtaskTotal(task: TaskSummary): number;
  subtaskDone(task: TaskSummary): number;
  subtaskProgressPercent(task: TaskSummary): number;
  toggleHoistParents(): void;
  setSort(): void;
  toggleSortDir(): void;
  clearSort(): void;
  saveSort(): void;
  activeSortLabel(): string;
  setCardDensity(mode: CardDensity): void;
  toggleCardDensity(): void;

  // filters + full-text (desc/notes) search
  scheduleFullTextSearch(): void;
  cancelFullTextSearch(): void; // project-switch seam (selectProject)
  runFullTextSearch(): Promise<void>;
  isDescriptionNotesMatch(task: TaskSummary): boolean;
  /** Ids the server matched for the last settled query (hook for board-view). */
  fullTextMatchIds(): ReadonlySet<string>;
  availableAssignees(): string[];
  hasActiveFilters(): boolean;
  clearFilters(): void;
}

/** Create the board core module. One instance per component root (the root
 *  builds it lazily — see modules/auth.ts for why). */
export function createBoardModule(deps: BoardDeps): BoardModule {
  // -- internal state (never rendered, never part of the flat namespace) -----
  /** IDs of tasks known to be blocked right now — derived from the board
   *  snapshot's `blockers` arrays (syncBlockedFromBoard) plus learned from
   *  detail fetches (knownBlocked). BOTH structures are scoped to
   *  `blockedCacheProjectId`: readers see an empty cache whenever the scope
   *  differs from the selected project, so a switch can never render a
   *  "blocked" indicator based on ANOTHER project's tasks — including entry
   *  points that change selectedProjectId without going through selectProject()
   *  (deep-link / popstate adoption, archive fallback, boot). */
  let blockedIds = new Set<string>();
  let knownBlocked: Record<string, boolean> = {};
  let knownBlockers: Record<string, BlockerRef[]> = {};
  /** Project the blocked-state cache belongs to; "" = nothing derived yet. */
  let blockedCacheProjectId = "";

  // -------------------------------------------- full-text (desc/notes) search
  // Task 01M5XTAW3RZ4QJCSW9C3CS255R: on top of the instant client-side title/id
  // filter, a debounced GET /tasks?search= contributes its task ids to the
  // filtered set. fullTextMatchSet = ids the SERVER matched for the last settled
  // query; searchSeq retires out-of-order responses (clearing the input bumps it,
  // so a late response can never repopulate a cleared board filter).
  let fullTextMatchSet = new Set<string>();
  const searchSeq = createSequenceGuard();
  let searchDebounce: { schedule(): void; cancel(): void; isPending(): boolean } | null = null;

  // -- blocked-state cache -------------------------------------------------------
  function isBlocked(id: string): boolean {
    // Fail closed: a cache owned by another project counts as empty.
    if (blockedCacheProjectId !== deps.selectedProjectId()) return false;
    if (blockedIds.has(id) || !!knownBlocked[id]) return true;
    const b = deps.board();
    if (b && b.project_id === deps.selectedProjectId()) {
      for (const s of STATUSES) {
        const t = b.tasks[s]?.find((task) => task.id === id);
        if (t?.blockers && t.blockers.length > 0) return true;
      }
    }
    return false;
  }

  /** Sync blockedIds directly from a board snapshot's blockers arrays (ticket 01M5XSNHZJ22E3RS7Z4PY4N274).
   *  Synchronous, zero extra HTTP requests, and works for any task volume (>100 tasks). */
  function syncBlockedFromBoard(b: Board): void {
    if (blockedCacheProjectId !== b.project_id) {
      resetBlockedCache(b.project_id);
    }
    const blocked = new Set<string>();
    for (const s of STATUSES) {
      for (const t of b.tasks[s] ?? []) {
        if (t.blockers && t.blockers.length > 0) {
          blocked.add(t.id);
        }
      }
    }
    blockedIds = blocked;
    blockedCacheProjectId = b.project_id;
  }

  /** Drop every derived/learned entry and re-scope the cache to
   *  `projectId` ("" = fully empty). Called on project switch and whenever
   *  a board payload arrives for a project the cache does not belong to. */
  function resetBlockedCache(projectId: string): void {
    blockedIds = new Set();
    knownBlocked = {};
    knownBlockers = {};
    deps.setBlockerPopoverTaskId(null);
    blockedCacheProjectId = projectId;
  }

  function taskBlockers(task: TaskSummary): BlockerRef[] {
    if (blockedCacheProjectId !== deps.selectedProjectId()) return [];
    if (task.blockers && task.blockers.length > 0) return task.blockers;
    if (knownBlockers[task.id] && knownBlockers[task.id].length > 0) {
      return knownBlockers[task.id];
    }
    return [];
  }

  function blockerTooltipText(task: TaskSummary): string {
    const blks = taskBlockers(task);
    if (blks.length > 0) {
      return "Blocked by: " + blks.map((b) => b.title + " (" + b.status + ")").join(", ");
    }
    return "Has an open blocker";
  }

  function toggleBlockerPopover(taskId: string): void {
    deps.setBlockerPopoverTaskId(deps.blockerPopoverTaskId() === taskId ? null : taskId);
  }

  /** Learn blocked state from a detail payload for card badges (the board diff
   *  is authoritative on load; details fill in between loads and cover >100-task
   *  projects). Only while the task's project IS the selected one: a detail
   *  refresh that outlives a project switch must neither add entries to nor
   *  delete from the cache of the project now on screen — board loads own the
   *  scope (see resetBlockedCache / selectProject at the root). */
  function learnBlockedFromDetail(
    projectId: string,
    taskId: string,
    blocked: boolean | null | undefined,
    blockers: BlockerRef[] | null | undefined,
  ): void {
    if (projectId !== deps.selectedProjectId()) return;
    const kb = { ...knownBlocked };
    const kblks = { ...knownBlockers };
    if (blocked) {
      kb[taskId] = true;
      kblks[taskId] = blockers || [];
    } else {
      delete kb[taskId];
      delete kblks[taskId];
    }
    knownBlocked = kb;
    knownBlockers = kblks;
  }

  /** Derive which open tasks are blocked directly from the board snapshot's
   *  blockers arrays (ticket 01M5XSNHZJ22E3RS7Z4PY4N274). Kept as an async
   *  method for backward compatibility with callers/tests, delegating to
   *  synchronous syncBlockedFromBoard without any extra HTTP requests. */
  async function refreshBlockedSet(b: Board, isStillCurrent?: () => boolean): Promise<void> {
    if (isStillCurrent && !isStillCurrent()) return;
    syncBlockedFromBoard(b);
  }

  // -- subtask meta -----------------------------------------------------------------
  function subtaskTotal(task: TaskSummary): number {
    return task.subtask_total ?? 0;
  }

  function subtaskDone(task: TaskSummary): number {
    return task.subtask_done ?? 0;
  }

  function subtaskProgressPercent(task: TaskSummary): number {
    const total = task.subtask_total ?? 0;
    if (total <= 0) return 0;
    const done = task.subtask_done ?? 0;
    return Math.min(100, Math.max(0, Math.round((done / total) * 100)));
  }

  // -- sorting / hoisting / density ----------------------------------------------------
  function toggleHoistParents(): void {
    deps.setHoistParents(!deps.hoistParents());
    persistHoistParents(deps.hoistParents());
  }

  /** Change handler: x-model has already written the raw value into sortKey;
   *  normalize it and apply the criterion's default direction. */
  function setSort(): void {
    const k: SortKey = isSortKey(deps.sortKey()) ? deps.sortKey() : "";
    deps.setSortKey(k);
    if (k !== "") deps.setSortDir(DEFAULT_SORT_DIR[k]);
    saveSort();
    deps.syncUrlState(true);
  }

  function toggleSortDir(): void {
    if (deps.sortKey() === "") return;
    deps.setSortDir(deps.sortDir() === "asc" ? "desc" : "asc");
    saveSort();
    deps.syncUrlState(true);
  }

  function clearSort(): void {
    if (deps.sortKey() === "") return;
    deps.setSortKey("");
    saveSort();
    deps.syncUrlState(true);
  }

  function saveSort(): void {
    persistSort(deps.sortKey(), deps.sortDir());
  }

  function activeSortLabel(): string {
    const o = SORT_OPTIONS.find((x) => x.value === deps.sortKey());
    return o && o.value !== "" ? o.label : "";
  }

  // -- card density ---------------------------------------------------------------------
  function setCardDensity(mode: CardDensity): void {
    deps.setCardDensity(mode);
    try {
      safeStorage.setItem(LS_CARD_DENSITY, mode);
    } catch {
      /* private mode — non-fatal */
    }
  }

  function toggleCardDensity(): void {
    setCardDensity(deps.cardDensity() === "comfortable" ? "compact" : "comfortable");
  }

  // -- filters + full-text search ---------------------------------------------------------
  /** Lazy trailing debounce (250ms) over runFullTextSearch. Created on first
   *  need so plain module instances (unit tests, SSR-ish envs) work without
   *  any init step. */
  function ensureSearchDebounce(): { schedule(): void; cancel(): void; isPending(): boolean } {
    if (!searchDebounce) {
      searchDebounce = createDebounce(() => {
        void runFullTextSearch();
      }, SEARCH_DEBOUNCE_MS);
    }
    return searchDebounce;
  }

  /** Cancel any pending debounced query, retire an in-flight response and drop
   *  the match id set — shared by scheduleFullTextSearch's empty branch and the
   *  project-switch cascade (selectProject). */
  function cancelFullTextSearch(): void {
    searchDebounce?.cancel();
    searchSeq.next(); // invalidate any in-flight response
    fullTextMatchSet = new Set<string>();
  }

  /** (Re)arm the description/notes search from ANY filterSearch or project
   *  change. Non-empty query → trailing-debounced server query. Empty query
   *  (cleared input / reset filters) → cancel the pending timer, retire any
   *  in-flight response via the sequence guard, and drop the match id set —
   *  so the instant local filter alone restores the unfiltered board. */
  function scheduleFullTextSearch(): void {
    const q = (deps.filterSearch() ?? "").trim();
    if (!q || !deps.selectedProjectId()) {
      cancelFullTextSearch();
      return;
    }
    ensureSearchDebounce().schedule();
  }

  /** The debounced server query itself. Runs ~SEARCH_DEBOUNCE_MS after the
   *  LAST keystroke (trailing), so a whole typing burst costs one round-trip.
   *  Stale-response protection: capture the sequence token at START; on resolve,
   *  apply only if no newer search was initiated AND the query and selected
   *  project are unchanged (a late response for an older query or a different
   *  project must never overwrite current state). */
  async function runFullTextSearch(): Promise<void> {
    const q = (deps.filterSearch() ?? "").trim();
    const projectId = deps.selectedProjectId();
    if (!q || !projectId) return;
    const token = searchSeq.next();
    try {
      const res = await api.listTasks({ project_id: projectId, search: q, limit: FULLTEXT_SEARCH_LIMIT });
      if (!searchSeq.isFresh(token)) return; // newer search or clear superseded us
      if (projectId !== deps.selectedProjectId()) return; // project switched mid-flight
      if ((deps.filterSearch() ?? "").trim() !== q) return; // query changed without a new run
      const ids = new Set<string>();
      for (const t of res.tasks ?? []) ids.add(t.id);
      fullTextMatchSet = ids;
    } catch {
      // Best-effort enrichment: on failure the board simply degrades to the
      // instant local title/id filter (no toast, no broken state). A FAILED
      // search that is still the newest clears stale ids so old matches of a
      // superseded query can't linger.
      if (searchSeq.isFresh(token)) fullTextMatchSet = new Set<string>();
    }
  }

  /** AC2's match-source indicator: true when the active search matched this
   *  card ONLY via description/notes — its title and id do NOT contain the
   *  substring (cards whose title/id already match show no chip). */
  function isDescriptionNotesMatch(task: TaskSummary): boolean {
    return descNotesMatchFor(task, deps.filterSearch() ?? "", fullTextMatchSet);
  }

  /** Ids the server matched for the last settled query — hook for
   *  modules/board-view.ts (columns() passes them to the filter core). */
  function fullTextMatchIds(): ReadonlySet<string> {
    return fullTextMatchSet;
  }

  function availableAssignees(): string[] {
    const b = deps.board();
    if (!b) return [];
    const set = new Set<string>();
    for (const s of STATUSES) {
      for (const t of b.tasks[s] ?? []) {
        if (t.assignee?.name) {
          set.add(t.assignee.name);
        }
      }
    }
    return Array.from(set).sort();
  }

  function hasActiveFilters(): boolean {
    return hasAnyFilter({
      priority: deps.filterPriority(),
      assignee: deps.filterAssignee(),
      unblockedOnly: deps.filterUnblocked(),
      stalled: deps.filterStalled(),
      needsAttention: deps.filterNeedsAttention(),
      due: deps.filterDue(),
      search: deps.filterSearch(),
    });
  }

  function clearFilters(): void {
    deps.setFilterPriority("");
    deps.setFilterAssignee("");
    deps.setFilterUnblocked(false);
    deps.setFilterStalled(false);
    deps.setFilterNeedsAttention(false);
    deps.setFilterDue("");
    deps.setFilterSearch("");
    // Explicit clear (in addition to the filterSearch $watch that does the
    // same in-browser): cancel any pending debounced search, retire the
    // in-flight response, drop the desc/notes match set.
    scheduleFullTextSearch();
    deps.syncUrlState(true);
  }

  return {
    isBlocked, syncBlockedFromBoard, resetBlockedCache, taskBlockers, blockerTooltipText,
    toggleBlockerPopover, learnBlockedFromDetail, refreshBlockedSet, subtaskTotal,
    subtaskDone, subtaskProgressPercent, toggleHoistParents, setSort, toggleSortDir,
    clearSort, saveSort, activeSortLabel, setCardDensity, toggleCardDensity, scheduleFullTextSearch,
    cancelFullTextSearch, runFullTextSearch, isDescriptionNotesMatch, fullTextMatchIds,
    availableAssignees, hasActiveFilters, clearFilters,
  };
}
