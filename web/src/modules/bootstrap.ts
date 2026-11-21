// Boot wiring for the Alpine root component (Phase 2 bootstrap hatch).
//
// Owns the one-shot boot sequence that previously sat at the bottom of app.ts:
// the init() memo, creation of the 1s `nowTick` ticker and the SSE-reload
// debounce, registration of the window/matchMedia/$watch listeners, first-load
// deep-link resolution, and the boot-error Retry path. Every template-bound
// name (appBooted, bootError, bootRetrying, nowTick, ...) stays flat root
// state; this module reaches it only through typed deps — the same pattern as
// modules/detail.ts and modules/sse.ts.

import { createDebounce, SSE_RELOAD_DEBOUNCE_MS } from "../refresh";
import {
  dashboardRouteProjectIdFromPath,
  isProjectsOverviewPath,
  taskRouteIdFromPath,
} from "../routes";
import type { ThemeMode } from "../theme";
import type { ProjectWithCounts } from "../types";
import { safeStorage } from "../util";
import { LS_PROJECT } from "./projects";

export interface BootstrapDeps {
  // state-in — flat root fields the boot sequence reads
  selectedProjectId: () => string;
  projects: () => ProjectWithCounts[];
  loadError: () => string;
  bootError: () => boolean;
  bootRetrying: () => boolean;
  polling: () => boolean;
  themeMq: () => MediaQueryList | null;
  theme: () => ThemeMode;

  // state-out — flat root fields the boot sequence writes
  setAppBooted(v: boolean): void;
  setBootError(v: boolean): void;
  setBootRetrying(v: boolean): void;
  setShowNewProject(v: boolean): void;
  setSelectedProjectId(v: string): void;
  setIsMobile(v: boolean): void;
  setNowTick(ts: number): void;
  setTickerId(id: ReturnType<typeof setInterval> | null): void;
  setSseReloadDebounce(d: ReturnType<typeof createDebounce> | null): void;

  // hooks — root-owned orchestration kept outside the module
  loadProjects: (force?: boolean) => Promise<void>;
  loadBoard: (force?: boolean) => Promise<void>;
  refreshSessionIdentity: () => Promise<void>;
  openDeepLinkedTask: (id: string) => Promise<void>;
  openDeepLinkedDashboard: (projectId: string, fromBootError?: boolean) => Promise<void>;
  openDeepLinkedProjectsOverview: () => Promise<void>;
  connectSse: () => void;
  startPolling: () => void;
  disconnectSse: (resetBackoff?: boolean) => void;
  onPopState: () => void;
  initFromUrl: () => void;
  applyTheme: () => void;
  setDocumentTitle: () => void;
  scheduleFullTextSearch: () => void;
  syncUrlState: (immediate?: boolean) => void;
  isRestoringUrlState: () => boolean;
  focusWhenVisible: (el: HTMLElement | null) => void;
  /** Alpine $watch seam — the root no-ops this under node:test, where the
   *  component proxy (and its $watch) do not exist. */
  watch: (prop: string, cb: () => void) => void;
}

export function createBootstrapModule(deps: BootstrapDeps) {
  // Boot memo (carried over from app.ts — ticket 01M3VJ5PQEAJ60B0QGPNZT8NKP):
  // Alpine invokes the component's init() TWICE per page load — once because
  // x-data auto-runs a data object's init method, and again from the root
  // element's explicit x-init="init()" (directive order runs x-data first;
  // verified empirically against the shipped bundle: two invocation stacks
  // ~6ms apart, the second carrying Alpine's `[Alpine] init()` eval frame).
  // The memo makes any repeat call return the FIRST boot's promise instead of
  // re-running it, so no duplicate ticker intervals, debouncers, window
  // listeners, projects fetches or SSE EventSources are created. (loadProjects'
  // in-flight coalescer had already masked only the fetch; everything else
  // still duplicated — including one open-then-immediately-closed
  // EventSource.)
  //
  // The memo is never re-triggered on a failed boot by design: _runBoot()
  // catches every network error into loadError/state, and the boot-error
  // screen's Retry runs retryBoot(), which re-runs loadProjects + continueBoot
  // directly WITHOUT calling init(). A rejected promise would therefore only
  // mean a genuine programming bug — in that case clear the memo so a later
  // call can retry.
  let bootPromise: Promise<void> | null = null;
  /** The live ticker interval — kept in the closure AND mirrored onto the root
   *  `_tickerId` field (tests read it there); both carry the same value. */
  let tickerId: ReturnType<typeof setInterval> | null = null;

  /** The actual boot sequence — run at most ONCE per component instance
   *  (see the memo above). Kept as its own function so the memo stays
   *  trivially inspectable and unit-testable. */
  async function _runBoot(): Promise<void> {
    tickerId = setInterval(() => {
      deps.setNowTick(Date.now());
    }, 1000);
    if (typeof (tickerId as any)?.unref === "function") {
      (tickerId as any).unref();
    }
    deps.setTickerId(tickerId);
    // Coalescer for SSE-triggered board/projects reloads (handleSseEvent):
    // a burst of events → ONE round after the quiet window. Created before any
    // network work so even an early event burst can't fire uncoalesced loads.
    const debounce = createDebounce(() => {
      void deps.loadBoard(true);
      void deps.loadProjects(true);
    }, SSE_RELOAD_DEBOUNCE_MS);
    deps.setSseReloadDebounce(debounce);
    if (typeof window !== "undefined") {
      window.addEventListener("beforeunload", () => {
        if (tickerId) clearInterval(tickerId);
        deps.disconnectSse(true);
      });
      // Track live OS preference changes — only relevant while in system mode.
      deps.themeMq()?.addEventListener("change", () => {
        if (deps.theme() === "system") deps.applyTheme();
      });
      // Mobile breakpoint flag mirroring Tailwind's `md:` (min-width 768px):
      // drives the x-if on the slide-down panel so its controls never coexist
      // with their desktop/header instances in the DOM.
      const mobileMq = window.matchMedia("(min-width: 768px)");
      deps.setIsMobile(!mobileMq.matches);
      mobileMq.addEventListener("change", () => {
        deps.setIsMobile(!mobileMq.matches);
      });
      // Browser back/forward is a first-class way into and out of the full-
      // page task view (/tasks/<id>) — reconcile on every popstate.
      window.addEventListener("popstate", () => deps.onPopState());

      // Restore URL query parameters (filters, sort, project)
      deps.initFromUrl();
    }
    const watchProps = [
      "filterPriority",
      "filterAssignee",
      "filterDue",
      "filterUnblocked",
      "filterStalled",
      "filterNeedsAttention",
      "filterSearch",
      "sortKey",
      "sortDir",
      "selectedProjectId",
    ];
    for (const prop of watchProps) {
      deps.watch(prop, () => {
        if (!deps.isRestoringUrlState()) {
          deps.syncUrlState(prop !== "filterSearch");
        }
      });
    }
    // Full-text search (01M5XTAW3RZ4QJCSW9C3CS255R): EVERY path that mutates
    // the query or the project re-arms the debounced desc/notes search —
    // typed input (both desktop and mobile bind filterSearch), Reset
    // filters, boot restore from ?q=... (applyQueryParams runs with the
    // board-view module's URL-restore flag set, hence these are SEPARATE
    // watchers that deliberately do NOT gate on it), browser back/forward, and
    // project switches. Clearing the query lands in scheduleFullTextSearch's
    // empty branch: timer cancelled, in-flight retired, match set dropped.
    deps.watch("filterSearch", () => deps.scheduleFullTextSearch());
    deps.watch("selectedProjectId", () => deps.scheduleFullTextSearch());
    deps.applyTheme();
    if (typeof document !== "undefined") {
      deps.setDocumentTitle();
    }
    await deps.loadProjects();
    // Fire-and-forget identity probe: learns WHO the presenting bearer is
    // (multi-user session) so the header can show it reactively; a
    // non-session/off-mode token simply resolves to "no identity" without
    // disturbing boot or the token.
    void deps.refreshSessionIdentity();
    // Deep link: the initial URL may already be a full-page task, dashboard, or projects overview route.
    const deepLinkedTaskId =
      typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
    const deepLinkedDashboardProjectId =
      typeof window !== "undefined" ? dashboardRouteProjectIdFromPath(window.location.pathname) : null;
    const deepLinkedProjectsOverview =
      typeof window !== "undefined" ? isProjectsOverviewPath(window.location.pathname) : false;
    if (deps.loadError()) {
      // Boot projects fetch failed — network blip or backend still starting.
      // Do NOT validate the selection against an empty list that is the
      // PRODUCT of the failure: keep selectedProjectId as-is, write nothing
      // to storage, and open no New Project bar (ticket
      // 01M3MMYP938G972KY8NF7RGMQR). Surface a boot-error screen with a
      // Retry that re-runs exactly this continuation once the fetch recovers.
      if (deepLinkedTaskId) {
        // A full-page task open carries its own fetch + error/retry state, so
        // it proceeds even when the projects list failed to load.
        await deps.openDeepLinkedTask(deepLinkedTaskId);
      } else if (deepLinkedDashboardProjectId) {
        await deps.openDeepLinkedDashboard(deepLinkedDashboardProjectId, true);
      } else {
        deps.setBootError(true);
        // Move focus to Retry as soon as it is rendered — x-show flushes on
        // Alpine's own schedule, so poll frames instead of focusing blind
        // (same convention as the drawer's fetch-failure state).
        const retryBtn =
          typeof document === "undefined"
            ? null
            : document.querySelector<HTMLElement>('[data-testid="boot-retry-btn"]');
        deps.focusWhenVisible(retryBtn);
      }
    } else {
      await continueBoot(deepLinkedTaskId, deepLinkedDashboardProjectId, deepLinkedProjectsOverview);
    }
    if (deps.polling()) deps.startPolling();
    // Boot finished: the Projects dropdown trigger may now be clicked
    // (e2e helpers wait on it becoming enabled). Set last so a failed
    // early load can't unlock an inert control.
    deps.setAppBooted(true);
  }

  /** Success-path boot continuation, shared by init() and retryBoot():
   *  validate the stored selection against the freshly loaded list (a stale
   *  id falls back to the first project), persist it, load the board and
   *  connect SSE — or invite creating a project when the server is genuinely
   *  empty. Only ever called AFTER a successful projects fetch. */
  async function continueBoot(
    deepLinkedTaskId: string | null,
    deepLinkedDashboardProjectId: string | null = null,
    deepLinkedProjectsOverview = false,
  ): Promise<void> {
    const projects = deps.projects();
    const known = projects.some((p) => p.id === deps.selectedProjectId());
    if (!known || !deps.selectedProjectId()) {
      deps.setSelectedProjectId(projects[0]?.id ?? "");
    }
    if (deepLinkedTaskId) {
      await deps.openDeepLinkedTask(deepLinkedTaskId);
    } else if (deepLinkedDashboardProjectId) {
      await deps.openDeepLinkedDashboard(deepLinkedDashboardProjectId);
    } else if (deepLinkedProjectsOverview) {
      await deps.openDeepLinkedProjectsOverview();
    } else if (deps.selectedProjectId()) {
      try {
        safeStorage.setItem(LS_PROJECT, deps.selectedProjectId());
      } catch { /* ignore */ }
      await deps.loadBoard();
      deps.connectSse();
      // Boot with a restored search query (?q=…): initFromUrl() applied the
      // query BEFORE the $watch handlers were registered (init order), so those
      // initial mutations never fired scheduleFullTextSearch — kick the
      // debounced desc/notes search here so a shared board-search URL actually
      // surfaces its matches (empty query is a harmless no-op branch).
      deps.scheduleFullTextSearch();
    } else {
      // No projects yet — invite creating one.
      deps.setShowNewProject(true);
    }
    deps.syncUrlState(true);
  }

  /** Retry button on the boot-error screen (ticket 01M3MMYP938G972KY8NF7RGMQR):
   *  re-runs the projects load and — only when it succeeds — the exact same
   *  continuation as init()'s success path. A second failure keeps the stored
   *  selection intact and just refreshes the error message; recovery is
   *  user-driven (no automatic retry loop, same convention as the drawer). */
  async function retryBoot(): Promise<void> {
    if (!deps.bootError() || deps.bootRetrying()) return;
    deps.setBootRetrying(true);
    try {
      await deps.loadProjects();
      if (deps.loadError()) return; // still failing — boot-error screen stays up
      deps.setBootError(false);
      const deepLinkedTaskId =
        typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
      const deepLinkedDashboardProjectId =
        typeof window !== "undefined" ? dashboardRouteProjectIdFromPath(window.location.pathname) : null;
      const deepLinkedProjectsOverview =
        typeof window !== "undefined" ? isProjectsOverviewPath(window.location.pathname) : false;
      await continueBoot(deepLinkedTaskId, deepLinkedDashboardProjectId, deepLinkedProjectsOverview);
    } finally {
      deps.setBootRetrying(false);
    }
  }

  /** Boot guard entry point (see the memo above). Memoized: repeat calls —
   *  Alpine fires this twice per page load — return the FIRST boot's promise. */
  async function init(): Promise<void> {
    if (!bootPromise) {
      bootPromise = _runBoot();
      bootPromise.catch(() => {
        // Only reachable on a genuine (non-network) throw — see above.
        bootPromise = null;
      });
    }
    return bootPromise;
  }

  return {
    init, continueBoot, retryBoot,
    // TEST-SUPPORT read-only accessor (same pattern as sseTelemetry): exposes
    // the boot memo that no product code reads outside init(), for tests.
    bootPromise: () => bootPromise,
  };
}

export type BootstrapModule = ReturnType<typeof createBootstrapModule>;
