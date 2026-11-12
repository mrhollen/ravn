// Board view module — Phase 2 batch B of the frontend modularization epic
// (batch B slice 1). Owns the BEHAVIOR of the board's RENDER/DATA PIPELINE from
// app.ts: the columns assembly, per-status/visible/hidden counts, the stalled
// alert + attention badge delegation, the URL query sync (build/sync/apply/
// restore), listTasksParams mapping and the loadBoard fetch lifecycle with its
// sequence guard. The board STATE concerns (blocked cache, sort/density prefs,
// filters + full-text search) live in modules/board.ts; this module reaches back
// there through the isBlocked / fullTextMatchIds / syncBlocked hooks.
// Every body moved here verbatim from app.ts; see docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html reads/
// writes still lives on the flat component root as a plain field, touched here
// exclusively through the typed accessors on {@link BoardViewDeps} (see
// modules/auth.ts). The URL-sync timer and board-load sequence guard are
// INTERNAL state — never rendered, never part of the flat namespace.

import { ApiError, api, errMsg } from "../api";
import { boardFromTaskList } from "../board-scope";
import { createSequenceGuard, shouldApplyBoard } from "../refresh";
import { DEFAULT_SORT_DIR, readStoredSort } from "../sort";
import type { SortDir, SortKey } from "../sort";
import { attentionBadgeKind as badgeKindFor, countStalledExpired } from "../filters";
import {
  buildBoardQueryString as buildQueryFor,
  parseBoardQueryParams,
  taskRouteIdFromPath,
  type BoardQueryParams,
} from "../routes";
import { visibleColumns, type ColumnView } from "../columns";
import { STATUSES } from "../types";
import type { Board, ListTasksParams, MineScope, Status, TaskSummary } from "../types";

/** Typed dependency surface for the board view module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface BoardViewDeps {
  // -- state-in: live accessors into root-owned board fields -----------------
  selectedProjectId(): string;
  /** Per-actor scoping (epic T7): "all" or a mine-scope for the fetch. */
  taskScope(): "all" | MineScope;
  board(): Board | null;
  showArchived(): boolean;
  doneLimit(): number;
  /** Live clock tick — drives stalled/attention re-evaluation every second. */
  nowTick(): number;
  hoistParents(): boolean;
  sortKey(): SortKey;
  sortDir(): SortDir;
  filterPriority(): string;
  filterAssignee(): string;
  filterUnblocked(): boolean;
  filterStalled(): boolean;
  filterNeedsAttention(): boolean;
  filterDue(): string;
  filterSearch(): string;

  // -- state-out: root writes, invoked so mutations flow through the root ----
  setBoard(b: Board | null): void;
  setProjectForbidden(b: boolean): void;
  setLoadError(msg: string): void;
  setSelectedProjectId(v: string): void;
  setSortKey(k: SortKey): void;
  setSortDir(d: SortDir): void;
  setFilterPriority(v: string): void;
  setFilterAssignee(v: string): void;
  setFilterUnblocked(b: boolean): void;
  setFilterStalled(b: boolean): void;
  setFilterNeedsAttention(b: boolean): void;
  setFilterDue(v: string): void;
  setFilterSearch(v: string): void;

  // -- methods-out -------------------------------------------------------------
  /** Open the auth modal (401 board reads). */
  openAuthModal(): void;
  /** Archived rows for the current project, or null when not in archived view
   *  (owned by the root archive region until its own slice). */
  archivedColumns(): Record<Status, TaskSummary[]> | null;
  /** Blocked-state hook → modules/board.ts. */
  isBlocked(id: string): boolean;
  /** Full-text match ids for the last settled query → modules/board.ts. */
  fullTextMatchIds(): ReadonlySet<string>;
  /** Derive blocked state from a fresh board snapshot → modules/board.ts. */
  syncBlocked(b: Board): void;
  /** True when any client-side filter is active → modules/board.ts. */
  hasActiveFilters(): boolean;
}

export interface BoardViewModule {
  // stalled alert + attention badge (pure-module delegation)
  stalledExpiredCount(): number;
  stalledAlertLabel(): string;
  toggleStalledFilter(): void;
  attentionBadgeKind(task: TaskSummary): "question" | "handoff" | null;

  // URL query sync
  buildBoardQueryString(): string;
  boardUrl(): string;
  syncUrlState(immediate?: boolean): void;
  applyQueryParams(params: BoardQueryParams, isPopState?: boolean): void;
  initFromUrl(): void;
  /** True while a URL-restore write batch is in flight (boot $watch guard). */
  isRestoringUrlState(): boolean;

  // query param → listTasks mapping
  listTasksParams(): ListTasksParams;

  // counts + columns + fetch lifecycle
  statusCounts(): Record<Status, number>;
  visibleTaskCount(): number;
  totalTaskCount(): number;
  hiddenTaskCount(): number;
  showFilteredEmptyState(): boolean;
  colDotClass(status: Status): string;
  colCountPillClass(status: Status): string;
  columns(): ColumnView[];
  loadBoard(silent?: boolean): Promise<void>;
  /** Retire in-flight board loads (project-switch seam — selectProject). */
  invalidatePendingLoads(): void;
}

/** Create the board view module. One instance per component root (the root
 *  builds it lazily — see modules/auth.ts for why). */
export function createBoardViewModule(deps: BoardViewDeps): BoardViewModule {
  // -- internal state (never rendered, never part of the flat namespace) -----
  let urlSyncTimer: ReturnType<typeof setTimeout> | null = null;
  let restoringUrlState = false;
  const boardSeq = createSequenceGuard();

  /** Number of `doing` tasks that are abandoned/stalled-expired — expired
   *  claim lease OR no update for > 60 minutes (isStalledExpired). Reads
   *  `nowTick` so the count re-evaluates live every second, exactly like
   *  claimExpired(). Counts PRE-FILTER board rows by design: the alert
   *  summarizes the whole project's doing column, not the filtered view. */
  function stalledExpiredCount(): number {
    const b = deps.board();
    if (!b) return 0;
    return countStalledExpired(b.tasks.doing ?? [], deps.nowTick());
  }

  /** Accessible title/label for the toolbar alert pill: "N abandoned/stalled
   *  task(s)" — singular/plural aware. */
  function stalledAlertLabel(): string {
    const n = stalledExpiredCount();
    return `${n} abandoned/stalled ${n === 1 ? "task" : "tasks"}`;
  }

  /** Invert the Expired/stalled filter and sync the URL immediately (same
   *  immediacy as every other filter control). The toolbar alert pill is a
   *  button on this state; the checkbox in the filter bar binds it directly. */
  function toggleStalledFilter(): void {
    deps.setFilterStalled(!deps.filterStalled());
    // URL sync is owned by THIS module — call locally, not through the root.
    syncUrlState(true);
  }

  /** Which high-signal comment badge a card's meta cluster should show:
   *  "question" or "handoff" when the task is OPEN (todo/doing) and its
   *  newest comment has that kind (needsAttention), null otherwise — done
   *  tasks, routine kinds (progress/note/decision) and no-comment threads
   *  never badge. Drives the card-question-badge / card-handoff-badge spans. */
  function attentionBadgeKind(task: TaskSummary): "question" | "handoff" | null {
    return badgeKindFor(task);
  }

  // -------------------------------------------------------- URL query sync
  function buildBoardQueryString(): string {
    return buildQueryFor({
      project: deps.selectedProjectId(),
      priority: deps.filterPriority(),
      owner: deps.filterAssignee(),
      due: deps.filterDue(),
      unblocked: deps.filterUnblocked(),
      stalled: deps.filterStalled(),
      attention: deps.filterNeedsAttention(),
      q: deps.filterSearch(),
      sort: deps.sortKey(),
      dir: deps.sortDir(),
    });
  }

  function boardUrl(): string {
    const q = buildBoardQueryString();
    return q ? `/${q}` : "/";
  }

  function syncUrlState(immediate = false): void {
    if (restoringUrlState) return;
    if (typeof window === "undefined" || !window.history?.replaceState) return;
    if (taskRouteIdFromPath(window.location.pathname) !== null) return;
    const doSync = () => {
      urlSyncTimer = null;
      const query = buildBoardQueryString();
      const newUrl = "/" + query + (window.location.hash || "");
      const currentUrl =
        (window.location.pathname || "/") +
        (window.location.search || "") +
        (window.location.hash || "");
      if (newUrl !== currentUrl) {
        window.history.replaceState(null, "", newUrl);
      }
    };
    if (immediate) {
      if (urlSyncTimer) {
        clearTimeout(urlSyncTimer);
        urlSyncTimer = null;
      }
      doSync();
    } else {
      if (urlSyncTimer) clearTimeout(urlSyncTimer);
      urlSyncTimer = setTimeout(doSync, 150);
    }
  }

  function applyQueryParams(params: BoardQueryParams, isPopState = false): void {
    restoringUrlState = true;
    try {
      if (params.project) {
        deps.setSelectedProjectId(params.project);
      }
      if (params.priority !== undefined || isPopState) {
        deps.setFilterPriority(params.priority ?? "");
      }
      if (params.owner !== undefined || isPopState) {
        deps.setFilterAssignee(params.owner ?? "");
      }
      if (params.due !== undefined || isPopState) {
        deps.setFilterDue(params.due ?? "");
      }
      if (params.unblocked !== undefined || isPopState) {
        deps.setFilterUnblocked(params.unblocked ?? false);
      }
      if (params.stalled !== undefined || isPopState) {
        deps.setFilterStalled(params.stalled ?? false);
      }
      if (params.attention !== undefined || isPopState) {
        deps.setFilterNeedsAttention(params.attention ?? false);
      }
      if (params.q !== undefined || isPopState) {
        deps.setFilterSearch(params.q ?? "");
      }
      if (params.sort !== undefined) {
        deps.setSortKey(params.sort);
        deps.setSortDir(
          params.dir ?? (DEFAULT_SORT_DIR[params.sort as Exclude<SortKey, "">] || "desc"),
        );
      } else if (isPopState) {
        const s = readStoredSort();
        deps.setSortKey(s.key);
        deps.setSortDir(s.dir);
      }
    } finally {
      restoringUrlState = false;
    }
  }

  function initFromUrl(): void {
    if (typeof window === "undefined" || !window.location) return;
    const search = window.location.search || "";
    if (!search) return;
    const params = parseBoardQueryParams(search);
    applyQueryParams(params, false);
  }

  function isRestoringUrlState(): boolean {
    return restoringUrlState;
  }

  // --------------------------------------------------------------------- query
  function listTasksParams(): ListTasksParams {
    const p: ListTasksParams = { project_id: deps.selectedProjectId() };
    if (deps.filterPriority() !== "") p.priority = Number(deps.filterPriority());
    if (deps.filterAssignee() && deps.filterAssignee() !== "unassigned") {
      p.assignee_name = deps.filterAssignee();
    }
    if (deps.filterUnblocked()) p.unblocked = true;
    if (deps.filterDue() === "overdue") {
      p.due_before = new Date().toISOString();
    } else if (deps.filterDue() === "today") {
      const end = new Date();
      end.setHours(23, 59, 59, 999);
      p.due_before = end.toISOString();
    } else if (deps.filterDue() === "week") {
      const end = new Date();
      end.setDate(end.getDate() + 7);
      end.setHours(23, 59, 59, 999);
      p.due_before = end.toISOString();
    }
    if (deps.filterSearch().trim()) {
      p.search = deps.filterSearch().trim();
    }
    return p;
  }

  // ------------------------------------------------------------------- counts
  /** Per-status OPEN counts for the header's project count pill
   *  (todo·doing·done). Derived from board.tasks — the same arrays the
   *  columns render — so the pill can never disagree with the board.
   *  Pre-filter counts by design: it summarizes the whole project, not
   *  the current filtered view. All three keys are always present in
   *  board.tasks ([] when empty), so no undefined guards needed beyond
   *  a missing board. */
  function statusCounts(): Record<Status, number> {
    const b = deps.board();
    if (!b) return { todo: 0, doing: 0, done: 0 };
    const counts = {} as Record<Status, number>;
    for (const s of STATUSES) counts[s] = b.tasks[s]?.length ?? 0;
    return counts;
  }

  /** Number of tasks currently VISIBLE across all three columns
   *  (i.e. after client-side filtering in columns()). The board itself is
   *  fetched unfiltered, so the delta against totalTaskCount() is exactly
   *  what the active filters hide — no pagination/archival lives here. */
  function visibleTaskCount(): number {
    let n = 0;
    for (const col of columns()) n += col.totalCount ?? col.tasks.length;
    return n;
  }

  /** Total tasks in the project (pre-filter), summed over all statuses. */
  function totalTaskCount(): number {
    const b = deps.board();
    if (!b) return 0;
    let n = 0;
    for (const s of STATUSES) n += b.tasks[s]?.length ?? 0;
    return n;
  }

  /** Tasks hidden by the active filters (never negative). */
  function hiddenTaskCount(): number {
    return Math.max(0, totalTaskCount() - visibleTaskCount());
  }

  /** True when the board should show the filtered-empty-state banner:
   *  some filter is active, the project actually HAS tasks, yet every
   *  column renders zero of them. A truly empty project keeps its plain
   *  empty look (no banner) so the two states stay distinguishable. */
  function showFilteredEmptyState(): boolean {
    // In the archived view the banner's premise (filters hid the LIVE board's
    // tasks) is false — a zero-count archived set is just an empty archive.
    if (deps.showArchived()) return false;
    if (!deps.hasActiveFilters() || !deps.board()) return false;
    return totalTaskCount() > 0 && visibleTaskCount() === 0;
  }

  /** Column-header status dot color (To Do zinc, Doing amber, Done emerald). */
  function colDotClass(status: Status): string {
    return status === "todo"
      ? "bg-zinc-400"
      : status === "doing"
        ? "bg-amber-500"
        : "bg-emerald-500";
  }

  /** Column-header count pill tint, matching the column's status. */
  function colCountPillClass(status: Status): string {
    if (status === "doing")
      return "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800";
    if (status === "done")
      return "bg-emerald-100 text-emerald-800 ring-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:ring-emerald-800";
    return "bg-zinc-100 text-zinc-600 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700";
  }

  // ------------------------------------------------------------------ columns
  function columns(): ColumnView[] {
    const b = deps.board();
    // "Show archived" swaps the DATA SOURCE only (same client filters, same
    // sort): live board rows vs the separately-fetched archived rows. The two
    // sets are disjoint server-side, so no per-row archival check is needed.
    const archived = deps.archivedColumns();
    return visibleColumns(
      (s) => (archived ? archived[s] : b ? b.tasks[s] ?? [] : []),
      {
        priority: deps.filterPriority(),
        assignee: deps.filterAssignee(),
        unblockedOnly: deps.filterUnblocked(),
        stalled: deps.filterStalled(),
        needsAttention: deps.filterNeedsAttention(),
        due: deps.filterDue(),
        search: deps.filterSearch(),
        nowTick: deps.nowTick(),
        isBlocked: (id) => deps.isBlocked(id),
        fullTextMatchIds: deps.fullTextMatchIds(),
      },
      {
        hoistParents: deps.hoistParents(),
        sortKey: deps.sortKey(),
        sortDir: deps.sortDir(),
        doneLimit: deps.doneLimit(),
        showArchived: deps.showArchived(),
      },
    );
  }

  // ----------------------------------------------------------------- fetching
  async function loadBoard(silent = false): Promise<void> {
    if (!deps.selectedProjectId()) return;
    const token = boardSeq.next(); // newest initiated board load wins
    // Per-actor scoping (epic T7): when a `mine` scope is active AND we're not in the
    // archived view, fetch a flat task list via listTasks({ mine }) and reshape it into
    // the same Board snapshot columns() reads. "All" / archived keep the getBoard path.
    // (Scoped rows omit the board-only card badges — ac_total/comment_count/etc.) by
    // design; that only affects which optional chips render in the scoped view.)
    const scoped = !deps.showArchived() && deps.taskScope() !== "all";
    try {
      let b: Board;
      if (scoped) {
        const res = await api.listTasks({
          project_id: deps.selectedProjectId(),
          mine: deps.taskScope() as MineScope,
          limit: 100,
        });
        b = boardFromTaskList(deps.selectedProjectId(), res.tasks);
      } else {
        b = await api.getBoard(deps.selectedProjectId());
      }
      // Out-of-order + project-switch protection: a stale response (a newer
      // load started after this one, or the user moved to another project in
      // flight) must never overwrite current state.
      const mayApply = (): boolean =>
        shouldApplyBoard({
          token,
          latestToken: boardSeq.latest(),
          payloadProjectId: b.project_id,
          selectedProjectId: deps.selectedProjectId(),
        });
      if (!mayApply()) return; // stale (newer load started) or foreign project — never overwrite
      deps.setBoard(b);
      deps.syncBlocked(b);
      deps.setProjectForbidden(false); // a successful read means access is fine again
      if (!silent) deps.setLoadError("");
    } catch (e) {
      if (!silent) deps.setLoadError(errMsg(e));
      if (ApiError.isUnauthorized(e)) {
        deps.openAuthModal();
      } else if (ApiError.isForbidden(e)) {
        // Membership scoping: a signed-in session that can't see THIS project. Not a
        // credential problem (so no auth modal) — show the one-shot "no access" state
        // and stop the board/SSE from looping on a resource we may not read.
        deps.setBoard(null);
        deps.setProjectForbidden(true);
      }
    }
  }

  /** Retire in-flight board loads: bump the sequence so any pending payload
   *  fails its token check (project-switch seam — selectProject). */
  function invalidatePendingLoads(): void {
    boardSeq.next();
  }

  return {
    stalledExpiredCount, stalledAlertLabel, toggleStalledFilter, attentionBadgeKind,
    buildBoardQueryString, boardUrl, syncUrlState, applyQueryParams, initFromUrl,
    isRestoringUrlState, listTasksParams, statusCounts, visibleTaskCount, totalTaskCount,
    hiddenTaskCount, showFilteredEmptyState, colDotClass, colCountPillClass, columns,
    loadBoard, invalidatePendingLoads,
  };
}
