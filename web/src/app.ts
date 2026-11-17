// The single Alpine.js root component for the Rävn board UI. One factory
// returning a state + methods object; registered as `Alpine.data("app", …)` in
// main.ts and used via x-data="app()" in index.html.
//
// Scope: VISIBILITY + light intervention. Humans can create projects/tasks,
// move status, save notes, toggle ACs, comment, attach artifacts. Claims are
// READ-ONLY here (agents manage leases via MCP).

import { ApiError, api, errMsg, LS_TOKEN, setApiTimeoutMsForTests } from "./api";
import { loadStoredSession } from "./identity";
import { createAuthModule, type AuthModule } from "./modules/auth";
import { createProjectsModule, LS_PROJECT, type ProjectsModule } from "./modules/projects";
export { LS_PROJECT } from "./modules/projects"; // moved constant — public export unchanged
import { FOCUSABLE_SELECTOR, focusElementOnceVisible, focusWhenReady, openDialogFocus, type DialogFocus } from "./focus";
import { createMenuController, menuFirstLastIndex, type MenuController } from "./modules/menus";
import { createBoardModule, readStoredCardDensity } from "./modules/board";
import type { BoardModule, CardDensity } from "./modules/board";
import { createBoardViewModule } from "./modules/board-view";
import type { BoardViewModule } from "./modules/board-view";
import {
  createDashboardModule,
  DEFAULT_HISTORY_BUCKET,
  DEFAULT_STALLED_MINUTES,
  formatIdleMinutes,
  type CumulativeSummary,
  type DashboardModule,
  type PriorityBreakdownEntry,
  type StatusSummaryEntry,
  type ThroughputSummary,
  type WorkloadEntry,
} from "./modules/dashboard";
import { createSseModule, sseTelemetry } from "./modules/sse";
import type { SseModule } from "./modules/sse";
import { createOpenCodeModule } from "./modules/opencode";
import type { OpenCodeModule } from "./modules/opencode";
import { createNewTaskModule } from "./modules/new-task";
import type { NewTaskModule } from "./modules/new-task";
import { createTaskEditModule } from "./modules/task-edit";
import type { TaskEditModule } from "./modules/task-edit";
import { createArchiveModule, type ArchiveModule } from "./modules/archive";
import { createBatchModule, type BatchModule } from "./modules/batch";
import { createDragModule, type DragModule, type SwipeState } from "./modules/drag";
import { createAcsModule } from "./modules/acs";
import type { AcsModule } from "./modules/acs";
import { createCommentsModule } from "./modules/comments";
import { createArtifactsModule, type ArtifactsModule } from "./modules/artifacts";
import { createClaimsModule, type ClaimsModule } from "./modules/claims";
import { createRelationsModule, type RelationView, type RelationsModule } from "./modules/relations";
import { createBootstrapModule, type BootstrapModule } from "./modules/bootstrap";
import { createDetailModule, type DetailModule } from "./modules/detail";
import type { CommentsModule } from "./modules/comments";
import { createProjectSettingsModule } from "./modules/project-settings";
import type { ProjectSettingsModule } from "./modules/project-settings";
import { escapeAction, globalKeyAction, isEditableTarget } from "./keyboard";
// moves.ts import reduced by Phase 2 batch D (drag slice): only `canMove` is
// still consumed at the root (exposed as template glue); everything else moved
// into modules/drag.ts.
import { canMove } from "./moves";
import { createDebounce } from "./refresh"; // SSE_RELOAD_DEBOUNCE_MS moved to modules/bootstrap.ts (Phase 2 bootstrap hatch)
// Phase 2 close-out F1: the pure event-diff string builder moved to realtime.ts;
// imported so the root's shorthand template bind below still resolves — and
// re-exported at its old spot so the legacy `app.formatEventDetail` import surface stays intact.
import { formatEventDetail } from "./realtime";
import { renderMarkdown } from "./md";
import { MEMBER_ROLES } from "./members";
// UI primitive layer (Phase 3) — web/src/ui: typed pure contracts, flat-root
// entries below. No nested Alpine components; state stays on the root.
import { btnClass } from "./ui/button";
import { boardCardClasses, cardDensityClass as uiCardDensityClass, type CardDensityPart } from "./ui/card";
import { EMPTY_STATE } from "./ui/empty";
import { MENU_PANEL_CLASS, menuItemClass } from "./ui/menu";
import { TOAST_BASE_CLASS, toastClassList, toastErrorPrefix } from "./ui/toast";
import { persistTheme, readStoredTheme, resolveDark, type ThemeMode } from "./theme";
import {
  SORT_OPTIONS,
  readStoredHoistParents,
  readStoredSort,
} from "./sort";
import {
  dashboardRoutePath,
  dashboardRouteProjectIdFromPath,
  parseBoardQueryParams,
  taskRouteIdFromPath,
  type BoardQueryParams,
} from "./routes";
import type { ColumnView } from "./columns";
import type {
  AcRow,
  ActiveClaim,
  ActorInput,
  ArtifactKind,
  ArtifactRow,
  BlockerRef,
  Board,
  CommentKind,
  CommentRow,
  ListTasksParams,
  MemberRole,
  MembersRosterDTO,
  OpenCodeAgentOption,
  OpenCodeModelOption,
  OpenCodeOptionsResponse,
  ProjectReport,
  ProjectReportHistory,
  ProjectWithCounts,
  RelationRow,
  ReportHistoryBucket,
  SessionIdentity,
  SseConnectionStatus,
  StartOpenCodeInput,
  StartOpenCodeResponse,
  Status,
  TaskDetail,
  TaskSummary,
} from "./types";
import { STATUSES, STATUS_LABEL } from "./types";
import {
  actorLabel,
  artifactKindClass,
  claimHint,
  commentKindClass,
  dueLabel,
  formatClaimCountdown,
  isClaimExpired,
  isOverdue,
  initials,
  isTextArtifact,
  priorityClass,
  priorityName,
  relTime,
  safeStorage,
  shortId,
  statusLabel,
  timeOfDay,
} from "./util";

// ---------------------------------------------------------------------------
// Phase 1 re-export shims — symbols that previously lived in app.ts moved to
// dedicated flat modules; these keep the existing import surface (tests,
// main.ts) intact until P2 switches consumers to direct module imports.
export { LS_THEME } from "./theme";
export type { ThemeMode } from "./theme";
export { LS_HOIST_PARENTS, LS_SORT, SORT_OPTIONS, isSortKey } from "./sort";
export type { SortDir, SortKey } from "./sort";
export {
  MOBILE_DETAIL_MAX_WIDTH,
  buildBoardQueryString,
  dashboardRoutePath,
  dashboardRouteProjectIdFromPath,
  isMobileDetailViewport,
  parseBoardQueryParams,
  taskRouteIdFromPath,
  taskRoutePath,
} from "./routes";
export type { BoardQueryParams } from "./routes";
// Phase 2 (board) re-exports — moved constants/types keep the existing import
// surface (tests, main.ts) intact.
export {
  LS_CARD_DENSITY,
  SEARCH_DEBOUNCE_MS,
  FULLTEXT_SEARCH_LIMIT,
  isCardDensity,
  readStoredCardDensity,
} from "./modules/board";
export type { CardDensity } from "./modules/board";

export const LS_ACTOR = "ravn.actor";

// P5 F3: board-scope persistence (LS keys + read/validate readers) moved to src/filters.ts;
// imported for internal use and re-exported so the existing import surface stays intact.
import { LS_TASK_SCOPE, LS_MY_PROJECTS, readStoredTaskScope, readStoredMyProjects } from "./filters";
export { LS_TASK_SCOPE, LS_MY_PROJECTS, readStoredTaskScope, readStoredMyProjects };
import type { TaskScope } from "./filters";
export type { TaskScope };

// Phase 2 close-out F1: `formatEventDetail` (the pure event-diff string builder) moved to
// src/realtime.ts — re-exported here so the legacy `app.formatEventDetail` import surface
// stays intact; the root's shorthand template bind uses the local import above.
export { formatEventDetail };

export { LS_TOKEN };
export const DEFAULT_DONE_LIMIT = 20;
/** Separator between the segments of a dynamic document.title (WCAG 2.4.2,
 *  task 01M3MMVV92639YJ5822824NTV1): "<task> – <project> – Rävn". */
export const TITLE_SEPARATOR = " – ";
const MD_CACHE_MAX = 500;

// Phase 2 (sse): the SSE preamble — LEGACY_NAMED_SSE_EVENT_KINDS doc + const and the
// sseTelemetry singleton counters — moved to modules/sse.ts; re-exported below so the
// existing import surface (tests, main.ts) stays intact.
export { sseTelemetry } from "./modules/sse";

// Toast item type — single-sourced in ui/toast.ts (Phase 3); re-exported so the
// existing import surface stays intact.
import type { Toast } from "./ui/toast";
export type { Toast };

export function ravnApp() {
  return {
    // ------------------------------------------------------------------ actor
    /** Display name stored in localStorage; sent as `actor.name` on every mutation. */
    actorName: safeStorage.getItem(LS_ACTOR) ?? "",

    saveActor(): void {
      try {
        safeStorage.setItem(LS_ACTOR, this.actorName.trim());
      } catch {
        /* private mode — non-fatal */
      }
    },

    actor(): ActorInput {
      return { name: this.actorName.trim() || "human", type: "human" };
    },

    // ------------------------------------------------------------------- theme
    /** Persisted UI theme; applied by toggling `.dark` on <html>. */
    theme: readStoredTheme(),
    _themeMq: (typeof window !== "undefined"
      ? window.matchMedia("(prefers-color-scheme: dark)")
      : null) as MediaQueryList | null,

    /** Whether the resolved theme is dark right now. */
    effectiveDark(): boolean {
      return resolveDark(this.theme, !!this._themeMq?.matches);
    },

    setTheme(mode: ThemeMode): void {
      this.theme = mode;
      persistTheme(mode);
      this.applyTheme();
    },

    // -------------------------------------------------------- document title
    /** Update document title with Rävn branding. */
    setDocumentTitle(title?: string): void {
      if (typeof document === "undefined") return;
      document.title = title ? `${title} · Rävn` : "Rävn";
    },

    /** WCAG 2.4.2 (task 01M3MMVV92639YJ5822824NTV1): reflect the active view in
     *  document.title so browser tabs and screen-reader announcements identify
     *  WHERE the user is, not just what app:
     *    task detail open → "<task title> – <project name> – Rävn"
     *    board view       → "<project name> – Rävn"
     *    no project yet   → "Rävn" (the static <title> fallback)
     *  `detail` deliberately outlives its drawer (closeDetail() keeps it for
     *  fast re-open), so the TASK segment is gated on detailOpen, not on
     *  detail being non-null. Deliberately NOT called from the boot sequence:
     *  first paint keeps the static "Rävn" title until the user explicitly
     *  switches project or opens a task (boot auto-selects silently). */
    updateDocumentTitle(): void {
      if (typeof document === "undefined") return;
      const project = this.projects.find((p) => p.id === this.selectedProjectId);
      const projectName = (project?.name ?? "").trim();
      const taskTitle = this.detailOpen ? ((this.detail?.title ?? "").trim()) : "";
      if (taskTitle && projectName) {
        document.title = `${taskTitle}${TITLE_SEPARATOR}${projectName}${TITLE_SEPARATOR}Rävn`;
      } else if (this.dashboardOpen && projectName) {
        document.title = `Dashboard${TITLE_SEPARATOR}${projectName}${TITLE_SEPARATOR}Rävn`;
      } else if (projectName) {
        document.title = `${projectName}${TITLE_SEPARATOR}Rävn`;
      } else {
        document.title = "Rävn";
      }
    },

    /** Re-sync the `.dark` class on <html> with the resolved theme. The OS-
     *  preference listener (wired in init) only re-applies while in system mode,
     *  so explicit light/dark stays pinned. */
    applyTheme(): void {
      if (typeof document === "undefined") return;
      document.documentElement.classList.toggle("dark", this.effectiveDark());
    },

    // ------------------------------------------------------------------ toasts
    toasts: [] as Toast[],
    toastSeq: 0,

    toast(
      msg: string,
      kind: "error" | "info" = "info",
      action?: { label: string; href?: string; onAction?: () => void },
      options?: {
        persistent?: boolean;
        borderClass?: string;
        hideErrorPrefix?: boolean;
        isSseDisconnect?: boolean;
      }
    ): number {
      const id = ++this.toastSeq;
      const item: Toast = { id, msg, kind };
      if (action) item.action = action;
      if (options?.persistent) item.persistent = true;
      if (options?.borderClass) item.borderClass = options.borderClass;
      if (options?.hideErrorPrefix) item.hideErrorPrefix = true;
      if (options?.isSseDisconnect) item.isSseDisconnect = true;
      this.toasts = [...this.toasts, item];
      // Global setTimeout (not window.setTimeout): identical in the browser,
      // but keeps async error paths unit-testable without a full window mock.
      if (!options?.persistent) {
        setTimeout(() => {
          this.dismissToast(id);
        }, 6500);
      }
      return id;
    },

    /** Manually drop one toast (its × button). Same removal path as auto-expiry.
     *  Two-phase: mark it `leaving` so the x-show leave transition (slide + fade)
     *  plays, then drop the row once that animation has finished — Alpine's
     *  x-for removes nodes instantly, so a direct filter would kill the exit. */
    dismissToast(id: number): void {
      // The SSE disconnect-toast guard lives in modules/sse.ts — clear it when its × is
      // clicked so a later disconnect may surface the notification again.
      this._sseModule().clearSseDisconnectNotificationIf(id);
      this.toasts = this.toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t));
      setTimeout(() => {
        this.toasts = this.toasts.filter((t) => t.id !== id);
      }, 280); // leave transition is duration-200; keep the margin generous
    },

    /** Runs a toast's local JS action (the Undo button — `action.onAction`) and
     *  dismisses the toast. Async callbacks are allowed: the toast leaves either
     *  way, and the action reports its own result with follow-up toasts. */
    runToastAction(t: Toast): void {
      if (!t.action?.onAction) return;
      try {
        // `() => void` accepts promise-returning callbacks too; detect at runtime
        // so async failures still surface as an error toast.
        const r = t.action.onAction() as unknown;
        if (r && typeof (r as Promise<void>).catch === "function") {
          (r as Promise<void>).catch((e) => this.toast(errMsg(e), "error"));
        }
      } catch (e) {
        this.toast(errMsg(e), "error");
      }
      this.dismissToast(t.id);
    },

    // ------------------------------------------------------------------ auth token
    authToken: api.getAuthToken(),
    showAuthModal: false,
    tokenInput: "",
    /** Focus session for the auth modal (trap + restore); null while closed. */
    authFocus: null as DialogFocus | null,

    // -- multi-user identity + login (epic T7) ---------------------------------
    /** The acting session identity resolved from the live /auth/session probe (or the
     *  pre-boot cache so the header renders it instantly). Null when there is no
     *  logged-in session — the UI then falls back to the free-form actor name. */
    identity: loadStoredSession()?.identity ?? null as SessionIdentity | null,
    /** Which panel of the combined auth modal is shown. Defaults to "login" only if we
     *  remember a prior session (so a returning signed-in user lands on Sign in); fresh
     *  browsers / token-mode servers have no stored identity → default "token", which
     *  keeps the pre-existing token-flow E2E assertions (single password input + Save) green. */
    authModalTab: loadStoredSession() ? ("login" as const) : ("token" as const),
    loginUsername: "",
    loginPassword: "",
    loginBusy: false,
    loginError: "",
    /** Per-actor board scope (persisted): "all" or one of the task `mine` values. */
    taskScope: readStoredTaskScope() as TaskScope,
    /** "My projects" filter (persisted): true → the open project list is fetched with
     *  ?mine=created. Only honored while a session identity exists — the server rejects
     *  mine=created with no acting actor — so logged-out views always show every project. */
    myProjectsOnly: readStoredMyProjects(),
    /** True once a signed-in session is denied access to the currently selected project
     *  (membership scoping 403). Drives a one-shot "no access" banner and stops the SSE
     *  reconnect loop; cleared on a successful board load / project change. */
    projectForbidden: false,

    // -- auth behavior (owned by modules/auth.ts — Phase 2 batch A) -------------
    // Lazily-built module instance + its dependency surface, closed over `this`.
    // Built on first use (not in the factory body) so the accessors below close
    // over Alpine's reactive proxy in the browser: every state-out write then flows
    // through the proxy and re-renders. The bound FIELDS above stay plain root
    // fields for the same reason — a nested module-owned state object mutated from
    // its own closures would not be observed by Vue's reactivity (see modules/auth.ts).
    _auth: null as AuthModule | null,

    /** Build (on first call) or return the auth module bound to THIS component instance. */
    _authModule(): AuthModule {
      if (this._auth) return this._auth;
      const m = createAuthModule({
        // state-in
        actorName: () => this.actorName,
        authToken: () => this.authToken,
        showAuthModal: () => this.showAuthModal,
        tokenInput: () => this.tokenInput,
        authFocus: () => this.authFocus,
        identity: () => this.identity,
        loginUsername: () => this.loginUsername,
        loginPassword: () => this.loginPassword,
        loadError: () => this.loadError,
        hasSelectedProject: () => Boolean(this.selectedProjectId),
        // state-out
        setAuthToken: (v) => {
          this.authToken = v;
        },
        setShowAuthModal: (v) => {
          this.showAuthModal = v;
        },
        setTokenInput: (v) => {
          this.tokenInput = v;
        },
        setAuthFocus: (f) => {
          this.authFocus = f;
        },
        setIdentity: (id) => {
          this.identity = id;
        },
        setAuthModalTab: (t) => {
          this.authModalTab = t;
        },
        setLoginBusy: (b) => {
          this.loginBusy = b;
        },
        setLoginError: (msg) => {
          this.loginError = msg;
        },
        setLoginPassword: (p) => {
          this.loginPassword = p;
        },
        // methods-out
        clearLoadError: () => {
          this.loadError = "";
        },
        clearBootError: () => {
          this.bootError = false;
        },
        clearProjectForbidden: () => {
          this.projectForbidden = false;
        },
        loadProjects: (silent) => this.loadProjects(silent),
        continueBoot: (id) => this.continueBoot(id),
        connectSse: () => this.connectSse(),
        disconnectSse: (resetBackoff) => this.disconnectSse(resetBackoff),
        loadBoard: () => this.loadBoard(),
        openAuthFocusSession: () => this.openDialog("cb-auth-modal", "input"),
        closeDialog: (f) => this.closeDialog(f),
        // DOM seams
        authModalRoot: () =>
          typeof document === "undefined" ? null : document.getElementById("cb-auth-modal"),
        pageActiveElement: () => (typeof document === "undefined" ? null : document.activeElement),
      });
      this._auth = m;
      return m;
    },

    /** The name to show as "acting as". A live session identity wins over the free-form
     *  actor override; otherwise the (trimmed) actor name; otherwise "You". */
    actingIdentityName(): string {
      return this._authModule().actingIdentityName();
    },

    openAuthModal(tab?: "login" | "token"): void {
      this._authModule().openAuthModal(tab);
    },

    /** Submit the Sign-in form: exchange credentials for a session token (stored as the
     *  Bearer credential on every call), adopt the returned identity, and resume boot. */
    doLogin(): Promise<void> {
      return this._authModule().doLogin();
    },

    /** Sign out: revoke the session server-side, drop the Bearer credential + identity
     *  cache, and reload so the (now unscoped) lists are authoritative again. Best-effort
     *  revoke — a network failure to DELETE still clears local state. */
    logout(): Promise<void> {
      return this._authModule().logout();
    },

    /** Inspect the presenting bearer via GET /auth/session to learn WHO is acting. Fire-
     *  and-forget at boot: a non-session bearer (shared/operator token, or an off-mode
     *  server with no auth route) legitimately returns no identity — that's NOT an error,
     *  so we just clear any stale cached identity in that case without touching the token. */
    refreshSessionIdentity(): Promise<void> {
      return this._authModule().refreshSessionIdentity();
    },

    /** Board-scope change: persist, then reload the board (scope and the archived view
     *  are separate data sources, so drop archived mode if it was on). */
    onScopeChange(): void {
      try {
        safeStorage.setItem(LS_TASK_SCOPE, this.taskScope);
      } catch {
        /* ignore */
      }
      if (this.showArchived) this.showArchived = false;
      void this.loadBoard();
    },

    /** "My projects" filter change (epic T7): persist the flag, then reload the project
     *  lists. The open list re-fetches with ?mine=created (gated on a session identity in
     *  loadProjectsCore); the archived list is deliberately never mine-scoped. */
    onMyProjectsToggle(): void {
      try {
        safeStorage.setItem(LS_MY_PROJECTS, this.myProjectsOnly ? "1" : "0");
      } catch {
        /* ignore */
      }
      void this.loadProjects(false);
    },

    /** Single close path for the auth modal (× / Cancel / backdrop / save / clear / Esc). */
    closeAuthModal(): void {
      this._authModule().closeAuthModal();
    },

    saveAuthToken(): void {
      this._authModule().saveAuthToken();
    },

    clearAuthToken(): void {
      this._authModule().clearAuthToken();
    },

    // -------------------------------------------------------------- focus session
    /** Start a focus session for a dialog surface by root element id (WCAG 2.4.3).
     *  Initial focus moves in as soon as the surface is actually rendered
     *  (focusWhenReady polls frames: x-show flushes on Alpine's own schedule,
     *  which can post-date even a setTimeout(0)). `autoFocus: false` for
     *  surfaces that focus explicitly once their content has loaded (drawer). */
    openDialog(id: string, initialSelector?: string, autoFocus = true): DialogFocus | null {
      if (typeof document === "undefined") return null;
      const root = document.getElementById(id);
      if (!root) return null;
      const f = openDialogFocus(root, { initialSelector });
      if (autoFocus) focusWhenReady(f, root);
      return f;
    },

    /** Close a focus session (restores trigger focus); idempotent on null. */
    closeDialog(f: DialogFocus | null): void {
      f?.close();
    },

    // ---------------------------------------------------------------- projects
    projects: [] as ProjectWithCounts[],
    /** Archived projects (GET /projects?archived=true returns ONLY archived
     *  rows, so the UI keeps a SEPARATE list from `projects`). Rendered under an
     *  "Archived" section in the project menu with a one-button restore action. */
    archivedProjects: [] as ProjectWithCounts[],
    loadError: "",
    /** Boot-error screen (ticket 01M3MMYP938G972KY8NF7RGMQR): the initial
     *  projects fetch failed. The stored selection is kept EXACTLY as it is —
     *  no fallback to ""/first project, no localStorage write, and the New
     *  Project bar stays closed. Retry re-runs the load + continuation
     *  (see init() / retryBoot()). */
    bootError: false,
    /** True while retryBoot()'s re-fetch is in flight (Retry shows "Retrying…"
     *  and stays disabled until the request settles). Same convention as
     *  detailRetrying. */
    bootRetrying: false,

    /** Request-sequence guards: a response may only apply if no NEWER load was
     *  initiated after it started (see web/src/refresh.ts). Internal-only — not
     *  rendered, so deliberately plain fields outside template concerns. The
     *  list-projects guard moved to modules/projects.ts with its behavior; the
     *  board and full-text-search guards live in modules/board-view.ts /
     *  modules/board.ts (Phase 2 batch B). */
    /** Trailing debounce coalescing SSE-triggered board/projects reloads into
     *  one round per quiet window (SSE_RELOAD_DEBOUNCE_MS). Created in init(). */
    sseReloadDebounce: null as ReturnType<typeof createDebounce> | null,

    // -- projects behavior (owned by modules/projects.ts — Phase 2 batch A) ---
    // Lazily-built module instance + its dependency surface, closed over `this`.
    // Built on first use (not in the factory body) so the accessors below close
    // over Alpine's reactive proxy in the browser: every state-out write then flows
    // through the proxy and re-renders. The bound FIELDS above stay plain root
    // fields for the same reason — a nested module-owned state object mutated from
    // its own closures would not be observed by Vue's reactivity (see modules/auth.ts).
    _projects: null as ProjectsModule | null,

    /** Build (on first call) or return the projects module bound to THIS component instance. */
    _projectsModule(): ProjectsModule {
      if (this._projects) return this._projects;
      const m = createProjectsModule({
        // state-in
        projects: () => this.projects,
        archivedProjects: () => this.archivedProjects,
        loadError: () => this.loadError,
        selectedProjectId: () => this.selectedProjectId,
        myProjectsOnly: () => this.myProjectsOnly,
        identity: () => this.identity,
        newProjectDraft: () => this.newProject,
        projectBusy: () => this.projectBusy,
        // state-out
        setProjects: (v) => {
          this.projects = v;
        },
        setArchivedProjects: (v) => {
          this.archivedProjects = v;
        },
        setLoadError: (v) => {
          this.loadError = v;
        },
        setNewProjectDraft: (d) => {
          this.newProject = d;
        },
        setProjectBusy: (b) => {
          this.projectBusy = b;
        },
        setShowNewProject: (b) => {
          this.showNewProject = b;
        },
        // methods-out
        actor: () => this.actor(),
        openAuthModal: () => this.openAuthModal(),
        connectSse: () => this.connectSse(),
        loadBoard: () => this.loadBoard(),
        toast: (msg, kind) => {
          this.toast(msg, kind);
        },
        closeProjectMenu: () => this.closeProjectMenu(),
        adoptSelectedProject: (id) => {
          this.selectedProjectId = id;
          try {
            safeStorage.setItem(LS_PROJECT, id);
          } catch { /* ignore */ }
        },
        focusCreateNameInput: () => {
          if (typeof document === "undefined") return;
          this._focusWhenVisible(document.getElementById("cb-new-project-name-input"));
        },
      });
      this._projects = m;
      return m;
    },

    /** Project-list lifecycle — owned by modules/projects.ts (Phase 2 batch A):
     *  parallel open+archived fetch with the sequence guard and in-flight
     *  coalescing cores (src/refresh.ts); ?mine=created scoping on the OPEN list. */
    loadProjects(silent = false): Promise<void> {
      return this._projectsModule().loadProjects(silent);
    },

    /** Menu option label with live task counts — owned by modules/projects.ts. */
    projectOptionLabel(p: ProjectWithCounts): string {
      return this._projectsModule().projectOptionLabel(p);
    },

    // -------------------------------------------------- projects dropdown (custom)
    // The header's Projects control is a custom dropdown — trigger button plus an
    // Alpine-managed menu — instead of a native <select>, which cannot host the
    // trailing "+ New project…" action item. Keyboard: Enter/Space or ArrowDown
    // on the trigger opens it; arrows (+Home/End) move between items; Enter
    // activates the focused item; Escape closes and returns focus to the
    // trigger (closeProjectMenu(true)). Clicks outside the whole control close
    // it WITHOUT stealing focus.
    projectMenuOpen: false,
    /** Flips on at the end of init(); gates the trigger so a pre-boot click
     *  can't open an empty, inert menu (and e2e can wait for it to enable). */
    appBooted: false,
    /** Lazily-built Projects-dropdown controller (modules/menus.ts). The
     *  capture-phase outside-click handler it manages is module-internal state. */
    _projectMenu: null as MenuController | null,

    /** Trigger label: the selected project's option label, or the placeholder —
     *  owned by modules/projects.ts (Phase 2 batch A). */
    projectSelectLabel(): string {
      return this._projectsModule().projectSelectLabel();
    },

    toggleProjectMenu(): void {
      if (this.projectMenuOpen) this.closeProjectMenu();
      else this.openProjectMenu("selected");
    },

    /** Build (on first call) or return the Projects-dropdown controller bound to
     *  THIS instance. Shared open/close/roving-nav/outside-click machinery —
     *  owned by modules/menus.ts (Phase 2 batch A); per-menu testids, the item
     *  selector and the "selected" initial-focus strategy are injected below. */
    _projectMenuController(): MenuController {
      if (this._projectMenu) return this._projectMenu;
      const m = createMenuController(
        {
          isOpen: () => this.projectMenuOpen,
          setOpen: (b) => {
            this.projectMenuOpen = b;
          },
          panel: () =>
            typeof document === "undefined" ? null : document.querySelector<HTMLElement>('[data-testid="project-menu"]'),
          trigger: () =>
            typeof document === "undefined" ? null : document.querySelector<HTMLElement>('[data-testid="project-select"]'),
          insideControl: (t) => {
            if (typeof document === "undefined" || !(t instanceof Node)) return false;
            const wrap = document.querySelector<HTMLElement>('[data-testid="project-dropdown"]');
            return !!wrap && wrap.contains(t);
          },
        },
        {
          itemSelector: "button",
          activeKind: "button",
          // "selected" = current project (or the first one when nothing is selected).
          pickInitial: (items, target) => {
            if (target === "last") return items.length - 1;
            if (target === "selected") {
              const i = items.findIndex((el) => el.getAttribute("data-project-id") === this.selectedProjectId);
              if (i !== -1) return i;
            }
            return 0;
          },
        },
      );
      this._projectMenu = m;
      return m;
    },

    /** Open the menu and focus an option: "selected" = current project (or the
     *  first one when nothing is selected), "first"/"last" for ArrowDown/Up on
     *  a closed trigger. Shared controller — modules/menus.ts (Phase 2 batch A). */
    openProjectMenu(focusTarget: "selected" | "first" | "last" = "selected"): void {
      this._projectMenuController().open(focusTarget);
    },

    /** Focus `el` once it is actually visible — thin root shim over the shared
     *  frame-polling helper in src/focus.ts (moved there for modules/menus.ts;
     *  remaining call sites are the new-project form focus + archive confirm +
     *  boot/SSE retry buttons). */
    _focusWhenVisible(el: HTMLElement | null): void {
      focusElementOnceVisible(el);
    },

    /** Close the menu. `restoreFocus` sends focus back to the trigger (Esc / a
     *  pick); outside clicks close WITHOUT restoring — focus is already where
     *  the user clicked. Shared controller — modules/menus.ts. */
    closeProjectMenu(restoreFocus = false): void {
      this._projectMenuController().close(restoreFocus);
    },

    /** Arrow/Home/End navigation between menu items (buttons in DOM order —
     *  project options, then the "+ New project…" action). Shared controller — modules/menus.ts. */
    moveProjectFocus(dir: number | "home" | "end"): void {
      this._projectMenuController().move(dir);
    },

    /** Pick a project from the menu: same semantics as the old native select's
     *  @change (x-model write + selectProject persist/reload), then close with
     *  focus back on the trigger. Re-picking the current one just collapses. */
    pickProject(id: string): void {
      if (id !== this.selectedProjectId) {
        this.selectedProjectId = id;
        this.selectProject();
      }
      this.closeProjectMenu(true);
    },

    // ------------------------------------------------- header settings menu
    // Theme, identity (actor name), auto-refresh and the auth-token entry are
    // consolidated in ONE compact dropdown at the right edge of the header.
    // The trigger is an avatar circle with the actor's initial ("You" when
    // unset) — deliberately not a gear glyph: the Projects dropdown has its own
    // attached project-settings gear, so the two controls stay distinct.
    // Keyboard mirrors the Projects dropdown: ArrowDown/ArrowUp on the closed
    // trigger opens it (first/last control); inside, arrows (+Home/End) move
    // between the panel's focusable controls; Escape closes and returns focus
    // to the trigger; outside clicks close WITHOUT stealing focus. The panel
    // sits at z-40 — the dropdown tier (below toasts z-50 and the detail modal
    // z-[60]). Theme/actor/polling persist exactly as before (setTheme →
    // localStorage, saveActor → localStorage, pollingChanged → poll timer);
    // only the token entry closes the menu on click (the auth modal's own
    // focus session takes over).
    headerMenuOpen: false,
    /** Lazily-built settings-menu controller (modules/menus.ts); the
     *  outside-click handler it manages is module-internal state. */
    _headerMenu: null as MenuController | null,

    /** Trigger glyph: the actor's initial, or "You" when no name is set. */
    actorGlyph(): string {
      const n = this.actorName.trim();
      return n ? n.slice(0, 1).toUpperCase() : "You";
    },

    toggleHeaderMenu(): void {
      if (this.headerMenuOpen) this.closeHeaderMenu();
      else this.openHeaderMenu("first");
    },

    /** Build (on first call) or return the settings-menu controller bound to
     *  THIS instance. Shared open/close/roving-nav/outside-click machinery —
     *  owned by modules/menus.ts (Phase 2 batch A). */
    _headerMenuController(): MenuController {
      if (this._headerMenu) return this._headerMenu;
      const m = createMenuController(
        {
          isOpen: () => this.headerMenuOpen,
          setOpen: (b) => {
            this.headerMenuOpen = b;
          },
          panel: () =>
            typeof document === "undefined" ? null : document.querySelector<HTMLElement>('[data-testid="header-menu"]'),
          trigger: () =>
            typeof document === "undefined"
              ? null
              : document.querySelector<HTMLElement>('[data-testid="header-menu-trigger"]'),
          insideControl: (t) => {
            if (typeof document === "undefined" || !(t instanceof Node)) return false;
            const wrap = document.querySelector<HTMLElement>('[data-testid="header-menu-anchor"]');
            return !!wrap && wrap.contains(t);
          },
        },
        {
          itemSelector: FOCUSABLE_SELECTOR,
          activeKind: "element",
          pickInitial: (items, target) => menuFirstLastIndex(items.length, target),
        },
      );
      this._headerMenu = m;
      return m;
    },

    /** Open the menu and focus its first/last focusable control. Opening it
     *  collapses the Projects dropdown so at most one header menu is ever live
     *  (that mutual exclusion stays at the root — both flags are owned here);
     *  the open/close machinery itself is the shared controller in modules/menus.ts. */
    openHeaderMenu(focusTarget: "first" | "last" = "first"): void {
      if (this.headerMenuOpen) return;
      if (this.projectMenuOpen) this.closeProjectMenu();
      this._headerMenuController().open(focusTarget);
    },

    /** Close the menu. `restoreFocus` sends focus back to the trigger (Esc /
     *  programmatic close); outside clicks close WITHOUT restoring — focus is
     *  already where the user clicked. Shared controller — modules/menus.ts. */
    closeHeaderMenu(restoreFocus = false): void {
      this._headerMenuController().close(restoreFocus);
    },

    /** Arrow/Home/End navigation between the panel's focusable controls in DOM
     *  order (theme buttons, actor input, auto-refresh checkbox, token item).
     *  The actor input is single-line: Up/Down have no caret role there
     *  (Left/Right do), so arrows uniformly hop controls — same contract as
     *  the Projects dropdown. Shared controller — modules/menus.ts. */
    moveHeaderFocus(dir: number | "home" | "end"): void {
      this._headerMenuController().move(dir);
    },

    /** "Token: set/none" menu item: close WITHOUT restoring focus — the auth
     *  modal opens and its own focus session takes over. */
    openTokenFromMenu(): void {
      this.closeHeaderMenu();
      this.openAuthModal();
    },

    // ------------------------------------------------------------ new project
    showNewProject: false,
    newProject: { name: "", description: "" },
    projectBusy: false,

    /** "+ New project…" menu item: closes the dropdown and opens the existing
     *  form bar below the header, focusing the name field. The `newProject`
     *  draft is deliberately NOT reset here — a dismissed/cancelled form must
     *  keep its typed input (no phantom creation on submit, no loss on return);
     *  the draft clears only on successful create. Behavior owned by modules/projects.ts. */
    openNewProject(): void {
      this._projectsModule().openNewProject();
    },

    /** Submit the new-project form — behavior owned by modules/projects.ts (Phase 2 batch A). */
    createProject(): Promise<void> {
      return this._projectsModule().createProject();
    },

    // ------------------------------------------------------------ opencode options
    openCodeOptions: null as OpenCodeOptionsResponse | null,
    openCodeOptionsProjectId: "" as string,
    loadingOpenCodeOptions: false,

    _opencode: null as OpenCodeModule | null,

    /** Lazily-built opencode module (modules/opencode.ts — Phase 2 batch B):
     *  the cached options read surface + variant derivation and the per-project
     *  fetch gate. The cache itself is plain root state; nothing internal is
     *  kept in this module's closure (see docs/FRONTEND_ARCHITECTURE.md §2). */
    _opencodeModule(): OpenCodeModule {
      if (this._opencode) return this._opencode;
      this._opencode = createOpenCodeModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        projects: () => this.projects,
        showProjectSettings: () => this.showProjectSettings,
        projectSettingsFormOpencodeUrl: () => this.projectSettingsForm.opencode_server_url,
        openCodeOptions: () => this.openCodeOptions,
        openCodeOptionsProjectId: () => this.openCodeOptionsProjectId,
        // state-out
        setOpenCodeOptions: (o) => { this.openCodeOptions = o; },
        setOpenCodeOptionsProjectId: (pid) => { this.openCodeOptionsProjectId = pid; },
        setLoadingOpenCodeOptions: (v) => { this.loadingOpenCodeOptions = v; },
      });
      return this._opencode;
    },

    availableOpenCodeAgents(): OpenCodeAgentOption[] {
      return this._opencodeModule().availableOpenCodeAgents();
    },

    availableOpenCodeModels(): OpenCodeModelOption[] {
      return this._opencodeModule().availableOpenCodeModels();
    },

    availableOpenCodeVariantsFor(modelRef?: string): string[] {
      return this._opencodeModule().availableOpenCodeVariantsFor(modelRef);
    },

    async fetchOpenCodeOptions(projectId?: string, force = false): Promise<OpenCodeOptionsResponse | null> {
      return await this._opencodeModule().fetchOpenCodeOptions(projectId, force);
    },

    // ------------------------------------------------------------ project settings
    showProjectSettings: false,
    projectSettingsForm: {
      name: "",
      description: "",
      opencode_server_url: "",
      opencode_directory: "",
      opencode_model: "",
      opencode_agent: "",
      opencode_variant: "",
    },
    projectSettingsBusy: false,
    showArchiveConfirm: false,
    archiveConfirmInput: "",

    // ---------------------------------------------------------- members panel (T9)
    /** Roster read state for the settings modal's Members section. "loading" until
     *  GET /members settles; "ready" renders roster + controls; "unavailable" =
     *  feature off (403 reason membership_scoping_disabled), not visible, or not
     *  enabled → the section must simply NOT render at all. */
    membersState: "" as "" | "loading" | "ready" | "unavailable",
    /** Normalized GET /projects/{id}/members body (owner + stored rows). Null until ready. */
    membersRoster: null as MembersRosterDTO | null,
    /** True while a member add / role change / remove is in flight. */
    membersBusy: false,
    memberAddName: "",
    memberAddRole: "viewer" as MemberRole,

    _projectSettings: null as ProjectSettingsModule | null,

    /** Lazily-built project-settings module (modules/project-settings.ts — Phase 2
     *  batch B): the settings form lifecycle + pristine baseline + discard guard,
     *  the archive-confirm flow, save/restore with the reselection fallback, and
     *  the in-dialog members panel (T9). The baseline copy and the dialog focus
     *  session are INTERNAL to that module; every bound name above stays a plain
     *  root field for the template. */
    _projectSettingsModule(): ProjectSettingsModule {
      if (this._projectSettings) return this._projectSettings;
      this._projectSettings = createProjectSettingsModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        projects: () => this.projects,
        identity: () => this.identity,
        showProjectSettings: () => this.showProjectSettings,
        projectSettingsForm: () => this.projectSettingsForm,
        projectSettingsBusy: () => this.projectSettingsBusy,
        archiveConfirmInput: () => this.archiveConfirmInput,
        membersRoster: () => this.membersRoster,
        membersBusy: () => this.membersBusy,
        memberAddName: () => this.memberAddName,
        memberAddRole: () => this.memberAddRole,
        // state-out
        setShowProjectSettings: (v) => { this.showProjectSettings = v; },
        setProjectSettingsForm: (f) => { this.projectSettingsForm = f; },
        setProjectSettingsBusy: (v) => { this.projectSettingsBusy = v; },
        setShowArchiveConfirm: (v) => { this.showArchiveConfirm = v; },
        setArchiveConfirmInput: (v) => { this.archiveConfirmInput = v; },
        setSelectedProjectId: (v) => { this.selectedProjectId = v; },
        clearBoard: () => { this.board = null; },
        setShowNewProject: (v) => { this.showNewProject = v; },
        setMembersState: (s) => { this.membersState = s; },
        setMembersRoster: (r) => { this.membersRoster = r; },
        setMembersBusy: (v) => { this.membersBusy = v; },
        setMemberAddName: (v) => { this.memberAddName = v; },
        setMemberAddRole: (r) => { this.memberAddRole = r; },
        // hooks — root-owned orchestration kept outside the module
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        confirmDiscard: (msg) => this.confirmDiscard(msg),
        openDialog: (id, sel) => this.openDialog(id, sel),
        closeDialog: (f) => this.closeDialog(f),
        focusByTestid: (tid) => {
          if (typeof window !== "undefined" && typeof document !== "undefined") {
            this._focusWhenVisible(document.querySelector<HTMLElement>(`[data-testid="${tid}"]`));
          }
        },
        loadProjects: () => this.loadProjects(),
        loadBoard: () => this.loadBoard(),
        connectSse: () => this.connectSse(),
        disconnectSse: (reset) => this.disconnectSse(reset),
        fetchOpenCodeOptions: (pid, force) => this._opencodeModule().fetchOpenCodeOptions(pid, force),
      });
      return this._projectSettings;
    },

    openProjectSettings(): Promise<void> {
      return this._projectSettingsModule().openProjectSettings();
    },

    closeProjectSettings(): void {
      this._projectSettingsModule().closeProjectSettings();
    },

    projectSettingsDirty(): boolean {
      return this._projectSettingsModule().projectSettingsDirty();
    },

    requestCloseProjectSettings(): void {
      this._projectSettingsModule().requestCloseProjectSettings();
    },

    openArchiveConfirm(): void {
      this._projectSettingsModule().openArchiveConfirm();
    },

    cancelArchiveConfirm(): void {
      this._projectSettingsModule().cancelArchiveConfirm();
    },

    isArchiveInputValid(): boolean {
      return this._projectSettingsModule().isArchiveInputValid();
    },

    async archiveProject(): Promise<void> {
      await this._projectSettingsModule().archiveProject();
    },

    async restoreProject(projectId: string): Promise<void> {
      await this._projectSettingsModule().restoreProject(projectId);
    },

    async saveProjectSettings(): Promise<void> {
      await this._projectSettingsModule().saveProjectSettings();
    },

    canManageMembers(): boolean {
      return this._projectSettingsModule().canManageMembers();
    },

    isOwnerMember(username: string): boolean {
      return this._projectSettingsModule().isOwnerMember(username);
    },

    loadMembers(): Promise<void> {
      return this._projectSettingsModule().loadMembers();
    },

    async addMember(): Promise<void> {
      await this._projectSettingsModule().addMember();
    },

    async changeMemberRole(username: string): Promise<void> {
      await this._projectSettingsModule().changeMemberRole(username);
    },

    async removeMember(username: string): Promise<void> {
      await this._projectSettingsModule().removeMember(username);
    },

    // ------------------------------------------------------------------- board
    selectedProjectId: safeStorage.getItem(LS_PROJECT) ?? "",
    board: null as Board | null,
    blockerPopoverTaskId: null as string | null,

    /** Lazily-built board CORE module (modules/board.ts — Phase 2 batch B):
     *  blocked-state cache, sort/hoist/density prefs, filters + full-text search.
     *  The blocked cache and the search debounce/guards are INTERNAL to that
     *  module; every bound name below stays a plain root field for the template. */
    _board: null as BoardModule | null,

    /** Build (on first call) or return the board core module bound to THIS component instance. */
    _boardModule(): BoardModule {
      if (this._board) return this._board;
      const m = createBoardModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        board: () => this.board,
        sortKey: () => this.sortKey,
        sortDir: () => this.sortDir,
        hoistParents: () => this.hoistParents,
        cardDensity: () => this.cardDensity,
        blockerPopoverTaskId: () => this.blockerPopoverTaskId,
        filterPriority: () => this.filterPriority,
        filterAssignee: () => this.filterAssignee,
        filterUnblocked: () => this.filterUnblocked,
        filterStalled: () => this.filterStalled,
        filterNeedsAttention: () => this.filterNeedsAttention,
        filterDue: () => this.filterDue,
        filterSearch: () => this.filterSearch,
        // state-out
        setSortKey: (v) => {
          this.sortKey = v;
        },
        setSortDir: (v) => {
          this.sortDir = v;
        },
        setHoistParents: (b) => {
          this.hoistParents = b;
        },
        setCardDensity: (v) => {
          this.cardDensity = v;
        },
        setBlockerPopoverTaskId: (v) => {
          this.blockerPopoverTaskId = v;
        },
        setFilterPriority: (v) => {
          this.filterPriority = v;
        },
        setFilterAssignee: (v) => {
          this.filterAssignee = v;
        },
        setFilterUnblocked: (b) => {
          this.filterUnblocked = b;
        },
        setFilterStalled: (b) => {
          this.filterStalled = b;
        },
        setFilterNeedsAttention: (b) => {
          this.filterNeedsAttention = b;
        },
        setFilterDue: (v) => {
          this.filterDue = v;
        },
        setFilterSearch: (v) => {
          this.filterSearch = v;
        },
        // methods-out
        syncUrlState: (immediate) => this.syncUrlState(immediate),
      });
      this._board = m;
      return m;
    },

    /** Lazily-built board VIEW module (modules/board-view.ts — Phase 2 batch B):
     *  columns/counts, URL query sync and the loadBoard lifecycle. The URL-sync
     *  timer and the board-load sequence guard are INTERNAL to that module. */
    _boardView: null as BoardViewModule | null,

    /** Build (on first call) or return the board view module bound to THIS component instance. */
    _boardViewModule(): BoardViewModule {
      if (this._boardView) return this._boardView;
      const m = createBoardViewModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        taskScope: () => this.taskScope,
        board: () => this.board,
        showArchived: () => this.showArchived,
        doneLimit: () => this.doneLimit,
        nowTick: () => this.nowTick,
        hoistParents: () => this.hoistParents,
        sortKey: () => this.sortKey,
        sortDir: () => this.sortDir,
        filterPriority: () => this.filterPriority,
        filterAssignee: () => this.filterAssignee,
        filterUnblocked: () => this.filterUnblocked,
        filterStalled: () => this.filterStalled,
        filterNeedsAttention: () => this.filterNeedsAttention,
        filterDue: () => this.filterDue,
        filterSearch: () => this.filterSearch,
        // state-out
        setBoard: (v) => {
          this.board = v;
        },
        setProjectForbidden: (b) => {
          this.projectForbidden = b;
        },
        setLoadError: (v) => {
          this.loadError = v;
        },
        setSelectedProjectId: (v) => {
          this.selectedProjectId = v;
        },
        setSortKey: (v) => {
          this.sortKey = v;
        },
        setSortDir: (v) => {
          this.sortDir = v;
        },
        setFilterPriority: (v) => {
          this.filterPriority = v;
        },
        setFilterAssignee: (v) => {
          this.filterAssignee = v;
        },
        setFilterUnblocked: (b) => {
          this.filterUnblocked = b;
        },
        setFilterStalled: (b) => {
          this.filterStalled = b;
        },
        setFilterNeedsAttention: (b) => {
          this.filterNeedsAttention = b;
        },
        setFilterDue: (v) => {
          this.filterDue = v;
        },
        setFilterSearch: (v) => {
          this.filterSearch = v;
        },
        // methods-out
        openAuthModal: () => this.openAuthModal(),
        archivedColumns: () => this.archivedColumns(),
        isBlocked: (id) => this._boardModule().isBlocked(id),
        fullTextMatchIds: () => this._boardModule().fullTextMatchIds(),
        syncBlocked: (b) => this._boardModule().syncBlockedFromBoard(b),
        hasActiveFilters: () => this._boardModule().hasActiveFilters(),
      });
      this._boardView = m;
      return m;
    },

    // -- board core behavior — owned by modules/board.ts (Phase 2 batch B) ----
    /** Blocked-state cache + subtask meta (project-scoped, fail-closed). */
    isBlocked(id: string): boolean {
      return this._boardModule().isBlocked(id);
    },

    syncBlockedFromBoard(b: Board): void {
      this._boardModule().syncBlockedFromBoard(b);
    },

    resetBlockedCache(projectId: string): void {
      this._boardModule().resetBlockedCache(projectId);
    },

    taskBlockers(task: TaskSummary): BlockerRef[] {
      return this._boardModule().taskBlockers(task);
    },

    blockerTooltipText(task: TaskSummary): string {
      return this._boardModule().blockerTooltipText(task);
    },

    toggleBlockerPopover(taskId: string): void {
      this._boardModule().toggleBlockerPopover(taskId);
    },

    subtaskTotal(task: TaskSummary): number {
      return this._boardModule().subtaskTotal(task);
    },

    subtaskDone(task: TaskSummary): number {
      return this._boardModule().subtaskDone(task);
    },

    subtaskProgressPercent(task: TaskSummary): number {
      return this._boardModule().subtaskProgressPercent(task);
    },

    // ------------------------------------------------------ client-side sorting
    /** Purely cosmetic ordering inside each column; the server order is never
     *  changed. Persisted so reloads keep the user's choice (like actor name). */
    sortKey: readStoredSort().key,
    sortDir: readStoredSort().dir,

    /** When ON (default), parent cards are hoisted to the top of their column;
     *  the active sort still orders cards within each group. Persisted like the
     *  sort choice (like actor name) so reloads keep the user's preference. */
    hoistParents: readStoredHoistParents(),

    toggleHoistParents(): void {
      this._boardModule().toggleHoistParents();
    },

    setSort(): void {
      this._boardModule().setSort();
    },

    toggleSortDir(): void {
      this._boardModule().toggleSortDir();
    },

    clearSort(): void {
      this._boardModule().clearSort();
    },

    saveSort(): void {
      this._boardModule().saveSort();
    },

    activeSortLabel(): string {
      return this._boardModule().activeSortLabel();
    },

    // ----------------------------------------------------------- card density
    cardDensity: readStoredCardDensity(),

    setCardDensity(mode: CardDensity): void {
      this._boardModule().setCardDensity(mode);
    },

    toggleCardDensity(): void {
      this._boardModule().toggleCardDensity();
    },

    // ---------------------------------------------------------- archive mgmt
    // Board-level archive support (task 01M5XSNN2KA876TR0QGE79YFPT): single-task
    // archive/unarchive with Undo, bulk "Archive all Done", and the "Show
    // archived" view mode. Archived rows live in a SEPARATE data source from
    // `board` — the board endpoint (allOpenTasks) only ever returns NON-archived
    // tasks, so the two can never mix; toggling OFF restores the previous board
    // EXACTLY because `board` was never mutated by this feature (bulk archive
    // removes rows, which is the correct post-archive state anyway).
    // BEHAVIOR owned by modules/archive.ts (Phase 2 batch D); the bound fields
    // below stay on the flat root (Alpine constraint), touched exclusively
    // through the module's typed accessors. The archived fetch sequence guard
    // is INTERNAL to that module now.
    showArchived: false,
    /** Per-status groups of ARCHIVED tasks, fetched with listTasks({archived:true}).
     *  null = not loaded yet. Kept after toggling OFF so re-enabling is instant;
     *  refreshed by any archive/unarchive that changes the archived set. */
    archivedBoardTasks: null as Record<Status, TaskSummary[]> | null,
    /** Done-column bulk archive confirmation step (two-click confirm inline in
     *  the column footer). Distinct from the PROJECT archive's showArchiveConfirm. */
    archiveAllDoneConfirmOpen: false,
    archiveAllDoneBusy: false,

    _archive: null as ArchiveModule | null,

    /** Build (on first call) or return the archive module bound to THIS component instance. */
    _archiveModule(): ArchiveModule {
      if (this._archive) return this._archive;
      const m = createArchiveModule({
        // state-in
        board: () => this.board,
        showArchived: () => this.showArchived,
        archivedBoardTasks: () => this.archivedBoardTasks,
        selectedProjectId: () => this.selectedProjectId,
        detail: () => this.detail,
        archiveAllDoneConfirmOpen: () => this.archiveAllDoneConfirmOpen,
        archiveAllDoneBusy: () => this.archiveAllDoneBusy,
        busyTaskIds: () => this.busyTaskIds,
        // state-out
        setShowArchived: (v) => {
          this.showArchived = v;
        },
        setArchivedBoardTasks: (v) => {
          this.archivedBoardTasks = v;
        },
        setArchiveAllDoneConfirmOpen: (v) => {
          this.archiveAllDoneConfirmOpen = v;
        },
        setArchiveAllDoneBusy: (v) => {
          this.archiveAllDoneBusy = v;
        },
        // methods-out
        toast: (msg, kind, action) => this.toast(msg, kind, action),
        actor: () => this.actor(),
        clearSelection: () => this.clearSelection(),
        refreshDetail: (force) => this.refreshDetail(force),
        applyDetail: (t) => { this.applyDetail(t); },
        loadBoard: (force) => this.loadBoard(force),
        // routed back through the root shim so flat-namespace stubs still
        // observe internal archived-view refreshes.
        refreshArchivedView: () => this.refreshArchivedView(),
      });
      this._archive = m;
      return m;
    },

    /** True when the columns should render the ARCHIVED data source — see module. */
    archivedColumns(): Record<Status, TaskSummary[]> | null {
      return this._archiveModule().archivedColumns();
    },

    /** Toggle the "Show archived" board view — see modules/archive.ts. */
    async toggleShowArchived(): Promise<void> {
      await this._archiveModule().toggleShowArchived();
    },

    /** Re-fetch the archived view after a mutation — see module. */
    async refreshArchivedView(): Promise<void> {
      await this._archiveModule().refreshArchivedView();
    },

    /** AC1: archive/unarchive ONE task (optimistic + Undo) — see module. */
    async setTaskArchived(taskId: string, archived: boolean): Promise<void> {
      await this._archiveModule().setTaskArchived(taskId, archived);
    },

    // ------------------------------------------------- "Archive all Done" (bulk)
    openArchiveAllDoneConfirm(): void {
      this._archiveModule().openArchiveAllDoneConfirm();
    },

    cancelArchiveAllDoneConfirm(): void {
      this._archiveModule().cancelArchiveAllDoneConfirm();
    },

    /** AC2: sequentially archive EVERY done task — see modules/archive.ts. */
    async confirmArchiveAllDone(): Promise<void> {
      await this._archiveModule().confirmArchiveAllDone();
    },

    // ---------------------------------------------------- done column pagination
    doneLimit: DEFAULT_DONE_LIMIT,

    showMoreDone(step = 20): void {
      this.doneLimit += step;
    },

    showAllDone(): void {
      this.doneLimit = Infinity;
    },

    collapseDone(): void {
      this.doneLimit = DEFAULT_DONE_LIMIT;
    },

    // ------------------------------------------------- multi-select + batch bar
    // Task 01M5XTB04A8ZX1TX8ARYFG5D9F. Board cards gain a hover/focus selection
    // checkbox plus modifier-click selection (Ctrl/Cmd toggle, Shift range
    // within the clicked column's VISIBLE order). Selecting ≥1 card raises a
    // floating bottom batch-action bar: Move to / Set priority / Assign owner /
    // Archive, run per-task with the archive flow's version-guard + retry-once
    // discipline and a consolidated summary toast. Selection is in-session ONLY
    // (never synced to the URL — it is ephemeral working state, like drag/sort).
    /** Currently selected card ids. Replaced (never mutated in place) so Alpine
     *  tracks the change — Set mutation methods are invisible to the proxy. */
    selectedTaskIds: new Set<string>(),
    /** Last selection click's anchor for Shift+Click range selection. Kept at
     *  its original position after a range select (Excel-style): repeated
     *  shift-clicks keep spanning FROM the anchor until a new plain/checkbox
     *  selection re-anchors it. */
    _selectionAnchorId: null as string | null,
    /** True while a batch operation is running (buttons disabled in the bar). */
    batchBusy: false,
    batchMoveMenuOpen: false,
    batchPriorityMenuOpen: false,
    batchAssignMenuOpen: false,
    /** Free-text owner used ONLY when the project has no existing assignee
     *  names to offer as dropdown options (AC2). */
    batchCustomOwner: "",

    // Behavior lives in modules/batch.ts (Phase 2 final sweep) — see module.
    // All six fields above stay flat root state; these shims keep every
    // index.html binding and root-level test byte-identical.
    _batch: null as BatchModule | null,

    _batchModule(): BatchModule {
      if (this._batch) return this._batch;
      const m = createBatchModule({
        // state-in
        selectedTaskIds: () => this.selectedTaskIds,
        selectionAnchorId: () => this._selectionAnchorId,
        batchBusy: () => this.batchBusy,
        batchMoveMenuOpen: () => this.batchMoveMenuOpen,
        batchPriorityMenuOpen: () => this.batchPriorityMenuOpen,
        batchAssignMenuOpen: () => this.batchAssignMenuOpen,
        board: () => this.board,
        // state-out
        setSelectedTaskIds: (v) => { this.selectedTaskIds = v; },
        setSelectionAnchorId: (v) => { this._selectionAnchorId = v; },
        setBatchBusy: (v) => { this.batchBusy = v; },
        setBatchMoveMenuOpen: (v) => { this.batchMoveMenuOpen = v; },
        setBatchPriorityMenuOpen: (v) => { this.batchPriorityMenuOpen = v; },
        setBatchAssignMenuOpen: (v) => { this.batchAssignMenuOpen = v; },
        // methods-out
        columns: () => this.columns(),
        availableAssignees: () => this.availableAssignees(),
        actor: () => this.actor(),
        toast: (msg, kind) => this.toast(msg, kind),
        loadBoard: (force) => this.loadBoard(force),
      });
      this._batch = m;
      return m;
    },

    isTaskSelected(id: string): boolean {
      return this._batchModule().isTaskSelected(id);
    },

    selectedCount(): number {
      return this._batchModule().selectedCount();
    },

    toggleSelectTask(id: string): void {
      this._batchModule().toggleSelectTask(id);
    },

    rangeSelectInColumn(id: string): void {
      this._batchModule().rangeSelectInColumn(id);
    },

    selectAllVisible(): void {
      this._batchModule().selectAllVisible();
    },

    clearSelection(): void {
      this._batchModule().clearSelection();
    },

    _closeBatchMenus(except?: "move" | "priority" | "assign"): void {
      this._batchModule().closeBatchMenus(except);
    },

    toggleBatchMoveMenu(): void {
      this._batchModule().toggleBatchMoveMenu();
    },

    toggleBatchPriorityMenu(): void {
      this._batchModule().toggleBatchPriorityMenu();
    },

    toggleBatchAssignMenu(): void {
      this._batchModule().toggleBatchAssignMenu();
    },

    batchAssigneeOptions(): string[] {
      return this._batchModule().batchAssigneeOptions();
    },

    async batchMoveTo(status: Status): Promise<void> {
      await this._batchModule().batchMoveTo(status);
    },

    async batchSetPriority(priority: number): Promise<void> {
      await this._batchModule().batchSetPriority(priority);
    },

    async batchAssignOwner(name: string | null): Promise<void> {
      await this._batchModule().batchAssignOwner(name);
    },

    async batchArchiveSelected(): Promise<void> {
      await this._batchModule().batchArchiveSelected();
    },

    // ------------------------------------------------------------------ filters
    filterPriority: "" as string,
    filterAssignee: "" as string,
    filterUnblocked: false,
    /** True while the "Expired / stalled" filter is active: restricts the board
     *  to doing tasks whose claim lease has lapsed or that have been idle for
     *  more than 60 minutes (isStalledExpired). Purely client-side — the server
     *  has no equivalent list parameter. */
    filterStalled: false,
    /** True while the "Needs attention" filter is active: restricts the board
     *  to OPEN tasks (todo/doing) whose newest comment's kind is `question` or
     *  `handoff` (needsAttention). Purely client-side — done tasks are never
     *  shown by this filter, and it AND-composes with every other active
     *  filter exactly like stalled/unblocked. */
    filterNeedsAttention: false,
    filterDue: "" as string,
    filterSearch: "",

    // -------------------------------------------- full-text (desc/notes) search
    // Task 01M5XTAW3RZ4QJCSW9C3CS255R — owned by modules/board.ts (Phase 2 batch B):
    // the debounced GET /tasks?search= contributes its task ids to the filtered
    // set; the debounce and sequence guards are module-internal.

    /** (Re)arm the description/notes search — see modules/board.ts. */
    scheduleFullTextSearch(): void {
      this._boardModule().scheduleFullTextSearch();
    },

    async runFullTextSearch(): Promise<void> {
      await this._boardModule().runFullTextSearch();
    },

    /** AC2's match-source indicator — see modules/board.ts. */
    isDescriptionNotesMatch(task: TaskSummary): boolean {
      return this._boardModule().isDescriptionNotesMatch(task);
    },

    availableAssignees(): string[] {
      return this._boardModule().availableAssignees();
    },

    hasActiveFilters(): boolean {
      return this._boardModule().hasActiveFilters();
    },

    clearFilters(): void {
      this._boardModule().clearFilters();
    },

    // -- stalled alert + attention badge — owned by modules/board-view.ts ----
    /** Number of `doing` tasks that are abandoned/stalled-expired (live, pre-
     *  filter) — see modules/board-view.ts. */
    stalledExpiredCount(): number {
      return this._boardViewModule().stalledExpiredCount();
    },

    /** Accessible label for the toolbar alert pill — see modules/board-view.ts. */
    stalledAlertLabel(): string {
      return this._boardViewModule().stalledAlertLabel();
    },

    /** Invert the Expired/stalled filter and sync the URL immediately. */
    toggleStalledFilter(): void {
      this._boardViewModule().toggleStalledFilter();
    },

    /** High-signal comment badge for a card — see modules/board-view.ts. */
    attentionBadgeKind(task: TaskSummary): "question" | "handoff" | null {
      return this._boardViewModule().attentionBadgeKind(task);
    },

    // -------------------------------------------------------- URL query sync
    // Owned by modules/board-view.ts (Phase 2 batch B): the URL-sync timer and
    // the restore-flag are module-internal; the root keeps the shims.

    /** Query string for the current board state — see modules/board-view.ts. */
    buildBoardQueryString(): string {
      return this._boardViewModule().buildBoardQueryString();
    },

    boardUrl(): string {
      return this._boardViewModule().boardUrl();
    },

    syncUrlState(immediate = false): void {
      if (
        this.dashboardOpen ||
        (typeof window !== "undefined" &&
          dashboardRouteProjectIdFromPath(window.location.pathname) !== null)
      ) {
        return;
      }
      this._boardViewModule().syncUrlState(immediate);
    },

    applyQueryParams(params: BoardQueryParams, isPopState = false): void {
      this._boardViewModule().applyQueryParams(params, isPopState);
    },

    initFromUrl(): void {
      this._boardViewModule().initFromUrl();
    },

    listTasksParams(): ListTasksParams {
      return this._boardViewModule().listTasksParams();
    },

    // -- counts + columns — owned by modules/board-view.ts (Phase 2 batch B) --
    /** Per-status OPEN counts for the header pill (pre-filter). */
    statusCounts(): Record<Status, number> {
      return this._boardViewModule().statusCounts();
    },

    visibleTaskCount(): number {
      return this._boardViewModule().visibleTaskCount();
    },

    totalTaskCount(): number {
      return this._boardViewModule().totalTaskCount();
    },

    hiddenTaskCount(): number {
      return this._boardViewModule().hiddenTaskCount();
    },

    showFilteredEmptyState(): boolean {
      return this._boardViewModule().showFilteredEmptyState();
    },

    colDotClass(status: Status): string {
      return this._boardViewModule().colDotClass(status);
    },

    colCountPillClass(status: Status): string {
      return this._boardViewModule().colCountPillClass(status);
    },

    /** Rendered board columns (filters → sort → Done cap) — see module. */
    columns(): ColumnView[] {
      return this._boardViewModule().columns();
    },

    selectProject(): void {
      try {
        safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
      } catch { /* ignore */ }
      this.board = null;
      this.doneLimit = DEFAULT_DONE_LIMIT;
      this.clearSelection(); // selection ids belong to the project being left
      // Archived view belongs to the project being LEFT: exit the mode, drop its
      // data, and retire any in-flight archived fetch (its rows describe the
      // previous project). Mirrors how selectProject invalidates board/search.
      this._archiveModule().resetForProjectSwitch();
      this.openCodeOptions = null;
      this.openCodeOptionsProjectId = "";
      // The blocked-state cache belongs to the project being LEFT: reset it,
      // scoped to the new selection, so the fresh board starts from no stale
      // badges (isBlocked()'s scope check also covers the entry points that
      // change selectedProjectId without going through here).
      this.resetBlockedCache(this.selectedProjectId);
       // Invalidate anything pending for the PREVIOUS project: drop a queued
       // debounced SSE reload and retire in-flight board loads. (The fresh
       // loadBoard() below also bumps the sequence, and late payloads fail the
       // project-ownership check too — this makes the switch explicit.)
       this.sseReloadDebounce?.cancel();
       this._boardViewModule().invalidatePendingLoads();
       // Full-text search results belong to the project being LEFT: cancel the
       // pending debounced query, retire any in-flight response, and drop the
       // match id set — then re-arm for the new selection if a term is active
       // (mirrors how selectProject invalidates pending board loads above).
       this._boardModule().cancelFullTextSearch();
       // Dashboard report belongs to the project being LEFT: clear and reload if open.
       this.dashboardReport = null;
       this.dashboardError = "";
       this._dashboardModule().invalidatePendingLoads();
      this.connectSse();
      void this.loadBoard();
      if (this.dashboardOpen) {
        void this._dashboardModule().loadReport(false);
        if (
          typeof window !== "undefined" &&
          window.history?.replaceState &&
          this.selectedProjectId
        ) {
          window.history.replaceState(null, "", dashboardRoutePath(this.selectedProjectId));
        }
      } else {
        // P4 project-in-URL: a user-initiated switch on the plain board route
        // is a NAVIGATION — push a history entry so browser Back/Forward step
        // across projects (popstate's board branch restores ?project= plus the
        // full filter set, app.ts onPopState). Dashboard mode keeps its
        // replaceState behavior above. The equality guard makes this a no-op
        // when the $watch-driven syncUrlState below computes the same URL and
        // prevents duplicate entries if that watch raced ahead of us.
        // Programmatic adoptions (boot stored selection, deep links) never run
        // selectProject, so they keep their replaceState/no-history behavior.
        if (
          typeof window !== "undefined" &&
          window.history?.pushState &&
          this.selectedProjectId
        ) {
          const target = this.boardUrl() + (window.location.hash || "");
          const current =
            (window.location.pathname || "/") +
            (window.location.search || "") +
            (window.location.hash || "");
          if (target !== current) window.history.pushState(null, "", target);
        }
      }
      this.scheduleFullTextSearch();
      this.syncUrlState(true);
      // User-driven project switch → the tab title follows (WCAG 2.4.2).
      this.updateDocumentTitle();
    },

    /** Board fetch lifecycle (sequence guard, per-actor scoping, 401/403
     *  handling) — owned by modules/board-view.ts (Phase 2 batch B). */
    async loadBoard(silent = false): Promise<void> {
      await this._boardViewModule().loadBoard(silent);
    },

    /** Derive blocked state from a board snapshot — see modules/board.ts. */
    async refreshBlockedSet(b: Board, isStillCurrent?: () => boolean): Promise<void> {
      await this._boardModule().refreshBlockedSet(b, isStillCurrent);
    },

    // ------------------------------------------------------------ realtime SSE
    sseStatus: "disconnected" as SseConnectionStatus,
    /** TEST-SUPPORT: component reference to the module-level SSE singleton counters so
     *  browser tests can read them via Alpine.$data(root)._sseTelemetry (see sseTelemetry in
     *  modules/sse.ts). Carried on the component instead of a window flag so shipped bundles
     *  stay clean of global test hooks. */
    _sseTelemetry: sseTelemetry,

    /** Lightweight ~15s poll of board (+ open detail + project counts). No websockets. */
    polling: true,

    _sse: null as SseModule | null,

    _sseModule(): SseModule {
      if (this._sse) return this._sse;
      this._sse = createSseModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        projectForbidden: () => this.projectForbidden,
        authToken: () => this.authToken,
        polling: () => this.polling,
        sseStatus: () => this.sseStatus,
        // state-out
        setSseStatus: (s) => { this.sseStatus = s; },
        setProjectForbidden: (v) => { this.projectForbidden = v; },
        // hooks — root-owned orchestration kept outside the module
        toast: (m, k, o) => this.toast(m, k, undefined, o),
        dismissToast: (id) => this.dismissToast(id),
        scheduleReload: () => { this.sseReloadDebounce?.schedule(); },
        isDetailOpenFor: (taskId) => Boolean(this.detailOpen && (this.detailId === taskId || this.detail?.id === taskId)),
        refreshDetail: (taskId) =>
          this.refreshDetail(true, taskId).then((applied) => {
            // refreshDetail returns false when the payload was discarded by the ownership guard
            // (drawer closed or switched mid-flight), and we re-check at settle time even for an
            // applied payload so a stale SSE event can never fire comment side effects after the
            // drawer has closed or moved on.
            if (!applied) return false;
            if (this.detailOpen && this.detail && this.detail.id === taskId) {
              void this.initComments(taskId);
            }
            return true;
          }).catch(() => false), // a failed re-fetch must never surface as an unhandled rejection on the stream path
        refreshOpenDetail: async () => {
          if (this.detailOpen && this.detail) {
            await this.refreshDetail(true);
            void this.initComments(this.detail.id);
          }
        },
        loadProjectsSilent: () => this.loadProjects(true),
        loadBoardSilent: () => this.loadBoard(true),
        refreshDashboard: () => { this._dashboardModule().refreshDashboard(); },
      });
      return this._sse;
    },

    // -------------------------------------------------------------- dashboard
    dashboardOpen: false,
    dashboardReport: null as ProjectReport | null,
    dashboardLoading: false,
    dashboardError: "",
    dashboardHistory: null as ProjectReportHistory | null,
    dashboardHistoryLoading: false,
    dashboardHistoryError: "",
    dashboardHistoryBucket: DEFAULT_HISTORY_BUCKET as ReportHistoryBucket,
    dashboardStalledMinutes: DEFAULT_STALLED_MINUTES,
    _dashboard: null as DashboardModule | null,

    _dashboardModule(): DashboardModule {
      if (this._dashboard) return this._dashboard;
      this._dashboard = createDashboardModule({
        selectedProjectId: () => this.selectedProjectId,
        dashboardOpen: () => this.dashboardOpen,
        dashboardReport: () => this.dashboardReport,
        dashboardHistory: () => this.dashboardHistory,
        historyBucket: () => this.dashboardHistoryBucket,
        stalledMinutes: () => this.dashboardStalledMinutes,
        nowTick: () => this.nowTick,
        setDashboardOpen: (open) => { this.dashboardOpen = open; },
        setDashboardReport: (rep) => { this.dashboardReport = rep; },
        setDashboardLoading: (loading) => { this.dashboardLoading = loading; },
        setDashboardError: (err) => { this.dashboardError = err; },
        setDashboardHistory: (hist) => { this.dashboardHistory = hist; },
        setDashboardHistoryLoading: (loading) => { this.dashboardHistoryLoading = loading; },
        setDashboardHistoryError: (err) => { this.dashboardHistoryError = err; },
        setHistoryBucket: (bucket) => { this.dashboardHistoryBucket = bucket; },
        setProjectForbidden: (v) => { this.projectForbidden = v; },
        openAuthModal: () => this.openAuthModal(),
        updateDocumentTitle: () => this.updateDocumentTitle(),
      });
      return this._dashboard;
    },

    openDashboard(): void {
      this._openDashboardNav();
    },

    closeDashboard(): void {
      this._closeDashboardNav();
    },

    toggleDashboard(): void {
      if (this.dashboardOpen) this.closeDashboard();
      else this.openDashboard();
    },

    async loadDashboardReport(silent = false): Promise<void> {
      await this._dashboardModule().loadReport(silent);
    },

    async loadDashboardHistory(silent = false): Promise<void> {
      await this._dashboardModule().loadHistory(silent);
    },

    async setDashboardHistoryBucket(bucket: ReportHistoryBucket): Promise<void> {
      await this._dashboardModule().setHistoryBucket(bucket);
    },

    isDashboardHistoryEmpty(): boolean {
      return this._dashboardModule().isHistoryEmpty();
    },

    dashboardThroughputSummary(): ThroughputSummary {
      return this._dashboardModule().throughputModel().summary;
    },

    dashboardCumulativeSummary(): CumulativeSummary {
      return this._dashboardModule().cumulativeModel().summary;
    },

    dashboardThroughputSvgInner(): string {
      return this._dashboardModule().throughputSvgInner();
    },

    dashboardCumulativeSvgInner(): string {
      return this._dashboardModule().cumulativeSvgInner();
    },

    dashboardThroughputAriaLabel(): string {
      return this._dashboardModule().throughputAriaLabel();
    },

    dashboardCumulativeAriaLabel(): string {
      return this._dashboardModule().cumulativeAriaLabel();
    },

    dashboardProjectName(): string {
      return this.projects.find((p) => p.id === this.selectedProjectId)?.name ?? "";
    },

    dashboardTotalTasks(): number {
      return this._dashboardModule().totalTasks();
    },

    isDashboardEmpty(): boolean {
      return this._dashboardModule().isEmpty();
    },

    dashboardStatusEntries(): StatusSummaryEntry[] {
      return this._dashboardModule().statusEntries();
    },

    dashboardPriorityEntries(): PriorityBreakdownEntry[] {
      return this._dashboardModule().priorityEntries();
    },

    dashboardWorkloadEntries(): WorkloadEntry[] {
      return this._dashboardModule().workloadEntries();
    },

    dashboardClaimRemaining(claim: ActiveClaim): string {
      return this._dashboardModule().activeClaimRemaining(claim);
    },

    formatIdleMinutes,
    timeOfDay,

    connectSse(): void {
      this._sseModule().connectSse();
    },

    disconnectSse(resetBackoff = true): void {
      this._sseModule().disconnectSse(resetBackoff);
    },

    pollingChanged(): void {
      this._sseModule().pollingChanged();
    },

    sseStatusWord(): string {
      return this._sseModule().sseStatusWord();
    },

    startPolling(): void {
      this._sseModule().startPolling();
    },

    // ---------------------------------------------------------------- new task
    showNewTask: false,
    /** Target column when creating from a non-To-Do column's quick-add (the modal header renders it). */
    newTaskTarget: "todo" as Status,
    confirmNewTaskDiscard: false,
    newTask: {
      title: "",
      description: "",
      priority: 0,
      due: "",
      acs: "",
      assigneeName: "",
      assigneeType: "human" as "human" | "agent",
    },
    taskBusy: false,
    /** possible_duplicates advisory from the last create — shown non-blockingly. */
    duplicates: [] as TaskSummary[],
    /** Parent task ID when creating a subtask via "+ Add subtask". */
    newTaskParentId: null as string | null,

    _newTask: null as NewTaskModule | null,

    /** Lazily-built new-task module (modules/new-task.ts — Phase 2 batch B):
     *  dialog lifecycle + guarded draft close and the create flow (POST, the
     *  version-guarded non-todo move, optional subtask link, duplicates
     *  advisory). Every bound field above stays plain root state; only the
     *  focus session lives in this module's closure. */
    _newTaskModule(): NewTaskModule {
      if (this._newTask) return this._newTask;
      this._newTask = createNewTaskModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        detail: () => this.detail,
        newTask: () => this.newTask,
        taskBusy: () => this.taskBusy,
        newTaskParentId: () => this.newTaskParentId,
        newTaskTarget: () => this.newTaskTarget,
        // state-out
        setShowNewTask: (v) => { this.showNewTask = v; },
        setNewTaskTarget: (s) => { this.newTaskTarget = s; },
        setConfirmNewTaskDiscard: (v) => { this.confirmNewTaskDiscard = v; },
        setDuplicates: (t) => { this.duplicates = t; },
        setTaskBusy: (v) => { this.taskBusy = v; },
        setNewTask: (f) => { this.newTask = f; },
        setNewTaskParentId: (v) => { this.newTaskParentId = v; },
        // hooks — root-owned orchestration kept outside the module
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        openDialog: (id, sel) => this.openDialog(id, sel),
        closeDialog: (f) => this.closeDialog(f),
        loadProjects: () => this.loadProjects(),
        loadBoard: (force) => this.loadBoard(force),
        refreshDetail: (force) => this.refreshDetail(force),
      });
      return this._newTask;
    },

    openNewSubtask(parentTask: { id: string } | null | undefined): void {
      this._newTaskModule().openNewSubtask(parentTask);
    },

    openNewTask(status: Status = "todo"): void {
      this._newTaskModule().openNewTask(status);
    },

    closeNewTask(): void {
      this._newTaskModule().closeNewTask();
    },

    newTaskDirty(): boolean {
      return this._newTaskModule().newTaskDirty();
    },

    requestCloseNewTask(): void {
      this._newTaskModule().requestCloseNewTask();
    },

    cancelNewTaskConfirm(): void {
      this._newTaskModule().cancelNewTaskConfirm();
    },

    clearDuplicates(): void {
      this._newTaskModule().clearDuplicates();
    },

    /** Create flow — behavior owned by modules/new-task.ts (Phase 2 batch B). */
    async createTask(): Promise<void> {
      return await this._newTaskModule().createTask();
    },

    // ------------------------------------------------------------ task detail
    detailOpen: false,
    /** The task id currently open (or being opened) — decoupled from the row so
     *  a refresh can start even while `detail` is momentarily null. */
    detailId: null as string | null,
    detail: null as TaskDetail | null,
    /** Task ids whose status-move PATCH is in flight — one entry PER TASK so
     *  moving card A never freezes card B. See web/src/moves.ts for the rules. */
    busyTaskIds: new Set<string>(),
    /** Reactive read for templates (card busy styling, drawer button disable). */
    isTaskBusy(id: string): boolean {
      return this.busyTaskIds.has(id);
    },
    /** Human-friendly message from a failed GET /tasks/{id} for the currently
     *  open task — the drawer's error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED).
     *  Null while loading or when `detail` is rendered. Set in refreshDetail(),
     *  cleared by applyDetail() and openTask(). */
    detailLoadError: null as string | null,
    /** True while retryDetailLoad()'s re-fetch is in flight (Retry button
     *  shows "Retrying…" and stays disabled until the request settles). */
    detailRetrying: false,

    // ------------------------------------------- full-page view (/tasks/<id>)
    /** Whether the open detail is rendered as a FULL PAGE instead of the wide
     *  centered modal (task 01M47MWHWW0M1Q3WZYFGFHXTYZ). On phones (<md,
     *  <768px) card clicks open directly in this mode — the full page is the
     *  mobile fallback for the detail view. The same markup and every edit op
     *  are shared; only the surrounding chrome differs. */
    fullPageOpen: false,
    /** True when WE created the current /tasks/<id> history entry via pushState
     *  (or replaced an entry of ours). Deep-linked opens arrive with a URL but
     *  no board entry behind them — closing those must replaceState('/') and
     *  never history.back(), which would navigate out of the app. */
    fullPagePushed: false,
    /** True when WE pushed /projects/<id>/dashboard via pushState. Deep links
     *  and popstate navigations set false so closing replaces with boardUrl()
     *  instead of popping out of the app. */
    dashboardPushed: false,

    _detail: null as DetailModule | null,

    /** Lazily-built task-detail module (modules/detail.ts — Phase 2 batch D):
     *  open/close lifecycle + the cascade resets of other modules' state that
     *  "ride with their owner" (relation form, AC edit session, comment
     *  counters, artifact copy flag, opencode flags), full-page view + deep-link
     *  adoption with the two-shape history sync (pushed → back(), deep link →
     *  replaceState — never back() from a deep link), applyDetail/refreshDetail
     *  patching with the ownership guard, the operational bar and the notes
     *  scratchpad. Every bound field above stays plain root state; only the two
     *  dialog focus sessions live in this module's closure (new-task precedent). */
    _detailModule(): DetailModule {
      if (this._detail) return this._detail;
      this._detail = createDetailModule({
        // state-in
        detail: () => this.detail,
        detailId: () => this.detailId,
        detailOpen: () => this.detailOpen,
        fullPageOpen: () => this.fullPageOpen,
        fullPagePushed: () => this.fullPagePushed,
        dashboardOpen: () => this.dashboardOpen,
        selectedProjectId: () => this.selectedProjectId,
        projects: () => this.projects,
        detailRetrying: () => this.detailRetrying,
        statusMenuOpen: () => this.statusMenuOpen,
        ownerPopoverOpen: () => this.ownerPopoverOpen,
        priorityPopoverOpen: () => this.priorityPopoverOpen,
        duePopoverOpen: () => this.duePopoverOpen,
        notesDirty: () => this.notesDirty,
        notesDraft: () => this.notesDraft,
        notesDraftOwner: () => this.notesDraftOwner,
        savingNotes: () => this.savingNotes,
        unsavedNotesAction: () => this.unsavedNotesAction,
        totalComments: () => this.totalComments,
        copiedArtifactTimer: () => this.copiedArtifactTimer,
        relationForm: () => this.relationForm,
        startingOpenCode: () => this.startingOpenCode,
        showTaskOpenCodeConfig: () => this.showTaskOpenCodeConfig,
        taskOpenCodeForm: () => this.taskOpenCodeForm,
        taskOpenCodeBaseline: () => this.taskOpenCodeBaseline,
        openCodeSessions: () => this.openCodeSessions,
        openCodeOptions: () => this.openCodeOptions,
        openCodeOptionsProjectId: () => this.openCodeOptionsProjectId,
        // state-out
        setDetail: (v) => { this.detail = v; },
        setDetailId: (v) => { this.detailId = v; },
        setDetailOpen: (v) => { this.detailOpen = v; },
        setFullPageOpen: (v) => { this.fullPageOpen = v; },
        setFullPagePushed: (v) => { this.fullPagePushed = v; },
        setDashboardPushed: (v) => { this.dashboardPushed = v; },
        setSelectedProjectId: (v) => { this.selectedProjectId = v; },
        setDetailLoadError: (v) => { this.detailLoadError = v; },
        setDetailRetrying: (v) => { this.detailRetrying = v; },
        setStatusMenuOpen: (v) => { this.statusMenuOpen = v; },
        setOwnerPopoverOpen: (v) => { this.ownerPopoverOpen = v; },
        setPriorityPopoverOpen: (v) => { this.priorityPopoverOpen = v; },
        setDuePopoverOpen: (v) => { this.duePopoverOpen = v; },
        setNotesDirty: (v) => { this.notesDirty = v; },
        setNotesDraft: (v) => { this.notesDraft = v; },
        setNotesDraftOwner: (v) => { this.notesDraftOwner = v; },
        setNotesShowPreview: (v) => { this.notesShowPreview = v; },
        setUnsavedNotesAction: (v) => { this.unsavedNotesAction = v; },
        setSavingNotes: (v) => { this.savingNotes = v; },
        setTotalComments: (v) => { this.totalComments = v; },
        setLoadingOlderComments: (v) => { this.loadingOlderComments = v; },
        setCopiedArtifactId: (v) => { this.copiedArtifactId = v; },
        setCopiedArtifactTimer: (v) => { this.copiedArtifactTimer = v; },
        setEditingTask: (v) => { this.editingTask = v; },
        setDescShowPreview: (v) => { this.descShowPreview = v; },
        setAssigneeInputName: (v) => { this.assigneeInputName = v; },
        setAssigneeInputType: (v) => { this.assigneeInputType = v; },
        setEditingAcSeq: (v) => { this.editingAcSeq = v; },
        setEditingAcText: (v) => { this.editingAcText = v; },
        setNewAcText: (v) => { this.newAcText = v; },
        setStartingOpenCode: (v) => { this.startingOpenCode = v; },
        setShowTaskOpenCodeConfig: (v) => { this.showTaskOpenCodeConfig = v; },
        setTaskOpenCodeForm: (f) => { this.taskOpenCodeForm = f; },
        setTaskOpenCodeBaseline: (f) => { this.taskOpenCodeBaseline = f; },
        setOpenCodeSessions: (s) => { this.openCodeSessions = s; },
        setShowNewProject: (v) => { this.showNewProject = v; },
        // hooks — root-owned orchestration kept outside the module
        toast: (m, k, a) => this.toast(m, k, a),
        actor: () => this.actor(),
        isTaskBusy: (id) => this.isTaskBusy(id),
        moveTaskStatus: (id, s, v) => this.moveTaskStatus(id, s, v),
        editDraftDirty: () => this.editDraftDirty(),
        discardEditDraftIfConfirmed: () => this.discardEditDraftIfConfirmed(),
        restoreCommentDraftForTask: (id) => this.restoreCommentDraftForTask(id),
        initComments: (id) => this.initComments(id),
        openDialog: (id, sel, af) => this.openDialog(id, sel, af),
        closeDialog: (f) => this.closeDialog(f),
        learnBlockedFromDetail: (pid, tid, blocked, blockers) =>
          this._boardModule().learnBlockedFromDetail(pid, tid, blocked, blockers),
        updateDocumentTitle: () => this.updateDocumentTitle(),
        boardUrl: () => this.boardUrl(),
        loadBoard: (force) => this.loadBoard(force),
        connectSse: () => this.connectSse(),
        openAuthModal: () => this.openAuthModal(),
        closeDashboard: () => this._dashboardModule().closeDashboard(),
        fetchOpenCodeOptions: (pid) => this.fetchOpenCodeOptions(pid),
      });
      return this._detail;
    },

    /** Open/close lifecycle — behavior owned by modules/detail.ts (Phase 2 batch D). */
    async openTask(id: string): Promise<void> {
      return await this._detailModule().openTask(id);
    },

    /** Detail-close entry point for the backdrop, × buttons and Escape —
     *  behavior owned by modules/detail.ts (Phase 2 batch D). */
    requestCloseDetail(): void {
      this._detailModule().requestCloseDetail();
    },

    closeDetail(syncHistory = true): void {
      this._detailModule().closeDetail(syncHistory);
    },

    /** Switch the open task's detail to the FULL-PAGE view — behavior owned by
     *  modules/detail.ts (Phase 2 batch D). */
    openFullPage(id?: string): void {
      this._detailModule().openFullPage(id);
    },

    /** Open project dashboard view and push `/projects/<id>/dashboard` onto
     *  browser history unless already on that route. Closes any open task detail
     *  (prompting on unsaved edits) without calling history.back(). */
    _openDashboardNav(): void {
      if (!this.selectedProjectId) return;
      if (this.detailOpen) {
        if (this.notesDirty) {
          this.promptUnsavedNotes({ kind: "close" });
          return;
        }
        if (!this.discardEditDraftIfConfirmed()) return;
        this.closeDetail(false);
      }
      if (!this.dashboardOpen) {
        this._dashboardModule().openDashboard();
      }
      if (typeof window !== "undefined" && window.history?.pushState) {
        const target = dashboardRoutePath(this.selectedProjectId);
        if (dashboardRouteProjectIdFromPath(window.location.pathname) !== this.selectedProjectId) {
          window.history.pushState(null, "", target);
          this.dashboardPushed = true;
        } else {
          this.dashboardPushed = false;
        }
      }
    },

    /** Close project dashboard view and reconcile the URL back to the board.
     *  When we pushed `/projects/<id>/dashboard`, history.back() pops it so
     *  Forward returns cleanly; on a deep-linked open (no prior entry),
     *  replaceState avoids navigating out of the app. */
    _closeDashboardNav(syncHistory = true): void {
      const wasOpen = this.dashboardOpen;
      const pushedEntry = this.dashboardPushed;
      this.dashboardPushed = false;
      if (wasOpen) {
        this._dashboardModule().closeDashboard();
      }
      if (
        syncHistory &&
        wasOpen &&
        typeof window !== "undefined" &&
        dashboardRouteProjectIdFromPath(window.location.pathname) !== null
      ) {
        if (pushedEntry && typeof window.history?.back === "function") {
          window.history.back();
        } else if (typeof window.history?.replaceState === "function") {
          window.history.replaceState(null, "", this.boardUrl());
        }
      }
    },

    /** Adopt `/projects/<id>/dashboard` from a popstate navigation without
     *  pushing history. */
    _adoptDashboardFromRoute(projectId: string): void {
      if (this.projects.length > 0 && !this.projects.some((p) => p.id === projectId)) {
        this.toast("Project not found", "error");
        if (typeof window !== "undefined") window.history.replaceState(null, "", this.boardUrl());
        return;
      }
      if (this.selectedProjectId !== projectId) {
        this.selectedProjectId = projectId;
        try {
          safeStorage.setItem(LS_PROJECT, projectId);
        } catch { /* private mode */ }
        this.resetBlockedCache(projectId);
        this.dashboardReport = null;
        this.dashboardError = "";
        this._dashboardModule().invalidatePendingLoads();
        this.connectSse();
        void this.loadBoard();
      }
      this.dashboardPushed = false;
      this._dashboardModule().openDashboard();
    },

    /** Reconcile app state with the URL after a browser navigation (back /
     *  forward). The pathname is the source of truth across `/tasks/<id>`,
     *  `/projects/<id>/dashboard`, and the board `/`. Never writes history for
     *  forward/back steps — the navigation already happened. */
    onPopState(): void {
      if (typeof window === "undefined") return;
      const id = taskRouteIdFromPath(window.location.pathname);
      const dashProjectId = dashboardRouteProjectIdFromPath(window.location.pathname);
      if (id) {
        if (this.dashboardOpen) {
          this._closeDashboardNav(false);
        }
        if (!this.detailOpen || this.detailId !== id) {
          void (async (): Promise<void> => {
            try {
              const t = await api.getTask(id, { commentLimit: 50 });
              if (this.selectedProjectId !== t.project_id) {
                this.selectedProjectId = t.project_id;
                try {
                  safeStorage.setItem(LS_PROJECT, t.project_id);
                } catch { /* private mode */ }
                this.resetBlockedCache(t.project_id);
                await this.loadBoard();
              }
              // The entry is already in history — a close must replaceState, not back().
              this.fullPagePushed = false;
              if (!this.detailOpen) this.connectSse();
              this.fullPageOpen = true;
              await this.openTask(id);
            } catch (e) {
              if (ApiError.isUnauthorized(e)) this.openAuthModal();
              else this.toast(errMsg(e), "error");
              window.history.replaceState(null, "", this.boardUrl());
            }
          })();
        } else {
          // Forward navigation onto the already-open task: just re-arm page mode.
          this.fullPageOpen = true;
          this.fullPagePushed = false;
          this.updateDocumentTitle();
        }
      } else if (dashProjectId) {
        if (this.detailOpen) {
          this.closeDetail(false);
        }
        this._adoptDashboardFromRoute(dashProjectId);
      } else {
        if (this.detailOpen) {
          this.closeDetail(false);
        }
        if (this.dashboardOpen) {
          this._closeDashboardNav(false);
        }
        const oldProject = this.selectedProjectId;
        const search = window.location.search || "";
        const params = parseBoardQueryParams(search);
        this.applyQueryParams(params, true);
        if (this.selectedProjectId && this.selectedProjectId !== oldProject) {
          this.resetBlockedCache(this.selectedProjectId);
          void this.loadBoard();
          this.connectSse();
        }
      }
    },

    /** Deep link on first load: `/projects/<id>/dashboard` in the initial URL.
     *  Adopts `<id>` as selected project (overriding any stored selection),
     *  connects SSE, loads the board, and opens the dashboard. */
    async openDeepLinkedDashboard(projectId: string, fromBootError = false): Promise<void> {
      const known = this.projects.some((p) => p.id === projectId);
      if (!known && !fromBootError) {
        this.toast("Project not found", "error");
        if (typeof window !== "undefined") window.history.replaceState(null, "", this.boardUrl());
        const fallbackKnown = this.projects.some((p) => p.id === this.selectedProjectId);
        if (!fallbackKnown || !this.selectedProjectId) {
          this.selectedProjectId = this.projects[0]?.id ?? "";
        }
        if (this.selectedProjectId) {
          try {
            safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
          } catch { /* ignore */ }
          await this.loadBoard();
          this.connectSse();
        } else {
          this.showNewProject = true;
        }
        return;
      }
      if (this.selectedProjectId !== projectId) {
        this.selectedProjectId = projectId;
        this.resetBlockedCache(projectId);
      }
      if (!fromBootError) {
        try {
          safeStorage.setItem(LS_PROJECT, projectId);
        } catch { /* private mode */ }
      }
      this.dashboardReport = null;
      this.dashboardError = "";
      this._dashboardModule().invalidatePendingLoads();
      this.dashboardPushed = false;
      this._dashboardModule().openDashboard();
      if (!fromBootError) {
        this.connectSse();
        await this.loadBoard();
      }
    },

    /** Deep link on first load: /tasks/<id> — behavior owned by modules/detail.ts (Phase 2 batch D). */
    async openDeepLinkedTask(id: string): Promise<void> {
      return await this._detailModule().openDeepLinkedTask(id);
    },

    // ---------------------------------------------------------------- opencode
    startingOpenCode: false,
    openCodeSessions: {} as Record<string, StartOpenCodeResponse>,
    showTaskOpenCodeConfig: false,
    taskOpenCodeForm: {
      agent: "",
      model: "",
      variant: "",
    },
    taskOpenCodeBaseline: {
      agent: "",
      model: "",
      variant: "",
    },

    /** OpenCode start panel — behavior owned by modules/detail.ts (Phase 2 batch D). */
    initTaskOpenCodeConfig(): void {
      this._detailModule().initTaskOpenCodeConfig();
    },

    toggleOpenCodeConfig(): void {
      this._detailModule().toggleOpenCodeConfig();
    },

    taskOpenCodeDirty(): boolean {
      return this._detailModule().taskOpenCodeDirty();
    },

    activeOpenCodeSession(d?: TaskDetail | null): StartOpenCodeResponse | null {
      return this._detailModule().activeOpenCodeSession(d);
    },

    async startTaskInOpenCode(customInput?: StartOpenCodeInput): Promise<void> {
      return await this._detailModule().startTaskInOpenCode(customInput);
    },

    // ------------------------------------------------------------------ copy id
    /** Copy the FULL task ID to the clipboard — behavior owned by modules/detail.ts (Phase 2 batch D). */
    async copyTaskId(id: string): Promise<void> {
      return await this._detailModule().copyTaskId(id);
    },

    /** Copy an artifact ref (file path or URL) to clipboard with brief visual confirmation.
     *  Behavior owned by modules/artifacts.ts. */
    async copyArtifactRef(ref: string, id?: string): Promise<void> {
      return await this._artifactsModule().copyArtifactRef(ref, id);
    },

    /** Fetch GET /tasks/{id} and apply it — behavior owned by modules/detail.ts (Phase 2 batch D). */
    async refreshDetail(silent = false, idToFetch?: string): Promise<boolean> {
      return await this._detailModule().refreshDetail(silent, idToFetch);
    },

    /** Re-fetch the currently open task (drawer Retry button) — behavior owned by
     *  modules/detail.ts (Phase 2 batch D). */
    async retryDetailLoad(): Promise<void> {
      return await this._detailModule().retryDetailLoad();
    },

    applyDetail(t: TaskDetail): void {
      this._detailModule().applyDetail(t);
    },

    // ------------------------------------------------------------ quick metadata (priority & due)
    priorityPopoverOpen: false,
    priorityBusy: false,
    duePopoverOpen: false,
    dueInputVal: "",
    dueBusy: false,

    togglePriorityPopover(): void {
      this._taskEditModule().togglePriorityPopover();
    },

    toggleDuePopover(): void {
      this._taskEditModule().toggleDuePopover();
    },

    /** Priority/due/assignee update flows — behavior owned by modules/task-edit.ts (Phase 2 batch B). */
    async updatePriority(priority: number): Promise<void> {
      return await this._taskEditModule().updatePriority(priority);
    },

    async saveDueDate(): Promise<void> {
      return await this._taskEditModule().saveDueDate();
    },

    async clearDueDate(): Promise<void> {
      return await this._taskEditModule().clearDueDate();
    },

    async updateDueDate(dueIso?: string | null): Promise<void> {
      return await this._taskEditModule().updateDueDate(dueIso);
    },

    // ------------------------------------------------------------ assignee
    assigneeInputName: "",
    assigneeInputType: "human" as "human" | "agent",
    assigneeBusy: false,

    /** Assignee flows — behavior owned by modules/task-edit.ts (Phase 2 batch B). */
    async saveAssignee(): Promise<void> {
      return await this._taskEditModule().saveAssignee();
    },

    async clearAssignee(): Promise<void> {
      return await this._taskEditModule().clearAssignee();
    },

    async updateAssignee(name: string, type: "human" | "agent" = "human"): Promise<void> {
      return await this._taskEditModule().updateAssignee(name, type);
    },

    // ------------------------------------------------------------ task edit
    editingTask: false,
    savingEdit: false,
    /** Write/Preview toggle for the DESCRIPTION markdown field (edit mode only).
     *  Default = Write. Reset to Write in startEditing() and on every openTask(). */
    descShowPreview: false,
    editDraft: {
      title: "",
      description: "",
      priority: 0,
      due: "",
      assigneeName: "",
      assigneeType: "human" as "human" | "agent",
    },
    /** Snapshot of the loaded values taken when edit mode starts — the
     *  "has anything actually changed?" reference for the discard guard. */
    editDraftOriginal: {
      title: "",
      description: "",
      priority: 0,
      due: "",
      assigneeName: "",
      assigneeType: "human" as "human" | "agent",
    },

    _taskEdit: null as TaskEditModule | null,

    /** Lazily-built task-edit module (modules/task-edit.ts — Phase 2 batch B):
     *  edit session lifecycle + the snapshot discard guard, the description
     *  draft persistence pair, saveTaskEdit with its version-conflict retry,
     *  and the quick-metadata popover toggles/update flows (priority, due) +
     *  assignee update flows. Every field above (incl. the editDraftOriginal
     *  snapshot — poked by drafts.test.ts) stays plain root state; this module
     *  owns nothing. toggleStatusMenu/toggleOwnerPopover stay here (operational
     *  bar → modules/detail.ts); they seed these popover/input fields through
     *  the flat root state. */
    _taskEditModule(): TaskEditModule {
      if (this._taskEdit) return this._taskEdit;
      this._taskEdit = createTaskEditModule({
        // state-in — core edit session
        detail: () => this.detail,
        editDraft: () => this.editDraft,
        editDraftOriginal: () => this.editDraftOriginal,
        editingTask: () => this.editingTask,
        savingEdit: () => this.savingEdit,
        // state-out — core edit session
        setEditingTask: (v) => { this.editingTask = v; },
        setSavingEdit: (v) => { this.savingEdit = v; },
        setDescShowPreview: (v) => { this.descShowPreview = v; },
        setEditDraft: (d) => { this.editDraft = d; },
        setEditDraftOriginal: (d) => { this.editDraftOriginal = d; },
        // state-in — quick-metadata popovers + assignee inputs
        priorityPopoverOpen: () => this.priorityPopoverOpen,
        duePopoverOpen: () => this.duePopoverOpen,
        dueInputVal: () => this.dueInputVal,
        priorityBusy: () => this.priorityBusy,
        dueBusy: () => this.dueBusy,
        assigneeInputName: () => this.assigneeInputName,
        assigneeInputType: () => this.assigneeInputType,
        assigneeBusy: () => this.assigneeBusy,
        // state-out — quick-metadata popovers + assignee inputs (status/owner
        // flags are owned by the operational-bar region; written through here)
        setPriorityPopoverOpen: (v) => { this.priorityPopoverOpen = v; },
        setDuePopoverOpen: (v) => { this.duePopoverOpen = v; },
        setDueInputVal: (v) => { this.dueInputVal = v; },
        setPriorityBusy: (v) => { this.priorityBusy = v; },
        setDueBusy: (v) => { this.dueBusy = v; },
        setOwnerPopoverOpen: (v) => { this.ownerPopoverOpen = v; },
        setStatusMenuOpen: (v) => { this.statusMenuOpen = v; },
        setAssigneeInputName: (v) => { this.assigneeInputName = v; },
        setAssigneeInputType: (v) => { this.assigneeInputType = v; },
        setAssigneeBusy: (v) => { this.assigneeBusy = v; },
        // hooks — root-owned orchestration kept outside the module
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        confirmDiscard: (msg) => this.confirmDiscard(msg),
        refreshDetail: (force) => this.refreshDetail(force),
        loadBoard: (force) => this.loadBoard(force),
        applyDetail: (t) => { this.applyDetail(t); },
      });
      return this._taskEdit;
    },

    _acs: null as AcsModule | null,

    /** Lazily-built acceptance-criteria module (modules/acs.ts — Phase 2
     *  batch C): toggle/add/paste-add/move/drop reorder + the inline edit
     *  session (start/cancel/save) and delete, plus the four display helpers
     *  over src/ac-math.ts. All nine AC state fields stay plain root state
     *  (every one is template-bound); detail patches go through setDetail as
     *  whole-object reassignments, exactly as the originals did. */
    _acsModule(): AcsModule {
      if (this._acs) return this._acs;
      this._acs = createAcsModule({
        // state-in
        detail: () => this.detail,
        acBusySeq: () => this.acBusySeq,
        newAcText: () => this.newAcText,
        addingAc: () => this.addingAc,
        editingAcSeq: () => this.editingAcSeq,
        editingAcText: () => this.editingAcText,
        savingAcSeq: () => this.savingAcSeq,
        deletingAcSeq: () => this.deletingAcSeq,
        reorderingAcs: () => this.reorderingAcs,
        draggedAcSeq: () => this.draggedAcSeq,
        // state-out
        setDetail: (t) => { this.detail = t; },
        setAcBusySeq: (v) => { this.acBusySeq = v; },
        setNewAcText: (v) => { this.newAcText = v; },
        setAddingAc: (v) => { this.addingAc = v; },
        setEditingAcSeq: (v) => { this.editingAcSeq = v; },
        setEditingAcText: (v) => { this.editingAcText = v; },
        setSavingAcSeq: (v) => { this.savingAcSeq = v; },
        setDeletingAcSeq: (v) => { this.deletingAcSeq = v; },
        setReorderingAcs: (v) => { this.reorderingAcs = v; },
        setDraggedAcSeq: (v) => { this.draggedAcSeq = v; },
        // hooks
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
      });
      return this._acs;
    },

    _comments: null as CommentsModule | null,

    /** Lazily-built comments module (modules/comments.ts — Phase 2 batch C):
     *  the comment draft restore/save pair, older-comment pagination and
     *  posting. All five bound state fields stay plain root state; detail is
     *  mutated in place (comments array reassignment) through the state-IN
     *  getter, while whole-field writes go through setters. */
    _commentsModule(): CommentsModule {
      if (this._comments) return this._comments;
      this._comments = createCommentsModule({
        // state-in
        detail: () => this.detail,
        commentForm: () => this.commentForm,
        commentShowPreview: () => this.commentShowPreview,
        postingComment: () => this.postingComment,
        loadingOlderComments: () => this.loadingOlderComments,
        totalComments: () => this.totalComments,
        // state-out
        setCommentForm: (f) => { this.commentForm = f; },
        setCommentShowPreview: (v) => { this.commentShowPreview = v; },
        setPostingComment: (v) => { this.postingComment = v; },
        setLoadingOlderComments: (v) => { this.loadingOlderComments = v; },
        setTotalComments: (v) => { this.totalComments = v; },
        // hooks
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
      });
      return this._comments;
    },

    _artifacts: null as ArtifactsModule | null,

    /** Lazily-built artifacts module (modules/artifacts.ts — Phase 2 batch C):
     *  file/drop/paste upload, attach (file or ref), image preview modal, the
     *  isStored* media predicates, text-preview toggle + fetch and delete.
     *  All bound state fields stay plain root state; detail artifacts are
     *  patched via whole-object setDetail reassignment under the same-task
     *  guard, in-place form/preview writes go through the state-IN getters. */
    _artifactsModule(): ArtifactsModule {
      if (this._artifacts) return this._artifacts;
      this._artifacts = createArtifactsModule({
        // state-in
        detail: () => this.detail,
        artifactForm: () => this.artifactForm,
        attachingArtifact: () => this.attachingArtifact,
        previewModal: () => this.previewModal,
        copiedArtifactId: () => this.copiedArtifactId,
        copiedArtifactTimer: () => this.copiedArtifactTimer,
        deletingArtifactId: () => this.deletingArtifactId,
        artifactTextExpanded: () => this.artifactTextExpanded,
        artifactTextContent: () => this.artifactTextContent,
        artifactTextLoading: () => this.artifactTextLoading,
        artifactTextError: () => this.artifactTextError,
        artifactDragOver: () => this.artifactDragOver,
        detailOpen: () => this.detailOpen,
        // state-out
        setDetail: (d) => { this.detail = d; },
        setArtifactForm: (f) => { this.artifactForm = f; },
        setAttachingArtifact: (v) => { this.attachingArtifact = v; },
        setPreviewModal: (p) => { this.previewModal = p; },
        setCopiedArtifactId: (id) => { this.copiedArtifactId = id; },
        setCopiedArtifactTimer: (t) => { this.copiedArtifactTimer = t; },
        setDeletingArtifactId: (v) => { this.deletingArtifactId = v; },
        setArtifactTextExpanded: (r) => { this.artifactTextExpanded = r; },
        setArtifactTextContent: (r) => { this.artifactTextContent = r; },
        setArtifactTextLoading: (r) => { this.artifactTextLoading = r; },
        setArtifactTextError: (r) => { this.artifactTextError = r; },
        setArtifactDragOver: (v) => { this.artifactDragOver = v; },
        // hooks
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        isWebHref: (s) => this.isWebHref(s),
        confirmDiscard: (msg) => this.confirmDiscard(msg),
      });
       return this._artifacts;
     },

    _claims: null as ClaimsModule | null,

    /** Lazily-built claims module (modules/claims.ts — Phase 2 batch C):
     *  claim/release/heartbeat flows + the pure display helpers. `claimBusy`
     *  is template-bound → root; `nowTick`/`_tickerId` are the shared 1s
     *  ticker also consumed by the board stalled filter → root (see _runBoot). */
    _claimsModule(): ClaimsModule {
      if (this._claims) return this._claims;
      this._claims = createClaimsModule({
        // state-in
        detail: () => this.detail,
        nowTick: () => this.nowTick,
        claimBusy: () => this.claimBusy,
        // state-out
        setClaimBusy: (v) => { this.claimBusy = v; },
        // hooks
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        refreshDetail: (f) => this.refreshDetail(f),
        loadBoard: (f) => this.loadBoard(f),
      });
       return this._claims;
    },

    _relations: null as RelationsModule | null,

    /** Lazily-built relations module (modules/relations.ts — Phase 2 batch D):
     *  the add-relation combobox (search/filter/keyboard nav), the RelationRow
     *  → RelationView shaping for the relations list, the subtask progress
     *  counters and the add/remove mutation flows. The template-bound
     *  `relationForm` stays a plain root field — in-place writes go through
     *  the state-IN getter; the shared `taskTitle` lookup is injected as a
     *  hook (root-owned generic board helper). */
    _relationsModule(): RelationsModule {
      if (this._relations) return this._relations;
      this._relations = createRelationsModule({
        // state-in
        detail: () => this.detail,
        board: () => this.board,
        relationForm: () => this.relationForm,
        // hooks
        toast: (m, k) => this.toast(m, k),
        actor: () => this.actor(),
        refreshDetail: (f) => this.refreshDetail(f),
        loadBoard: (f) => this.loadBoard(f),
        taskTitle: (id) => this.taskTitle(id),
      });
      return this._relations;
    },

    startEditing(): void {
      this._taskEditModule().startEditing();
    },

    cancelEditing(): void {
      this._taskEditModule().cancelEditing();
    },

    editDraftDirty(): boolean {
      return this._taskEditModule().editDraftDirty();
    },

    discardEditDraftIfConfirmed(): boolean {
      return this._taskEditModule().discardEditDraftIfConfirmed();
    },

    saveDescriptionDraft(): void {
      this._taskEditModule().saveDescriptionDraft();
    },

    clearDescriptionDraft(): void {
      this._taskEditModule().clearDescriptionDraft();
    },

    /** Save flow — behavior owned by modules/task-edit.ts (Phase 2 batch B). */
    async saveTaskEdit(): Promise<void> {
      return await this._taskEditModule().saveTaskEdit();
    },

    // -------------------------------------------------------- drag and drop
    draggedTaskId: null as string | null,
    draggedFromStatus: null as Status | null,
    dragOverStatus: null as Status | null,
    isDragging: false,

    _drag: null as DragModule | null,

    /** Build (on first call) or return the drag module bound to THIS component instance. */
    _dragModule(): DragModule {
      if (this._drag) return this._drag;
      const m = createDragModule({
        // state-in
        showArchived: () => this.showArchived,
        board: () => this.board,
        detail: () => this.detail,
        draggedTaskId: () => this.draggedTaskId,
        draggedFromStatus: () => this.draggedFromStatus,
        dragOverStatus: () => this.dragOverStatus,
        swipeState: () => this.swipeState,
        cardMenuTaskId: () => this.cardMenuTaskId,
        busyTaskIds: () => this.busyTaskIds,
        isTaskBusy: (id) => this.isTaskBusy(id),
        // state-out
        setDraggedTaskId: (v) => {
          this.draggedTaskId = v;
        },
        setDraggedFromStatus: (v) => {
          this.draggedFromStatus = v;
        },
        setDragOverStatus: (v) => {
          this.dragOverStatus = v;
        },
        setIsDragging: (v) => {
          this.isDragging = v;
        },
        setSwipeState: (v) => {
          this.swipeState = v;
        },
        setCardMenuTaskId: (v) => {
          this.cardMenuTaskId = v;
        },
        // methods-out
        toast: (msg, kind) => this.toast(msg, kind),
        actor: () => this.actor(),
        loadBoard: (force) => this.loadBoard(force),
        refreshDetail: (force) => this.refreshDetail(force),
        applyDetail: (t) => {
          this.applyDetail(t);
        },
      });
      this._drag = m;
      return m;
    },

    /** Template helper — behavior owned by modules/drag.ts (Phase 2 batch D). */
    dragTargetLegal(status: Status): boolean {
      return this._dragModule().dragTargetLegal(status);
    },

    onDragStart(e: DragEvent, task: TaskSummary, fromStatus: Status): void {
      this._dragModule().onDragStart(e, task, fromStatus);
    },

    onDragEnd(): void {
      this._dragModule().onDragEnd();
    },

    onDragOver(e: DragEvent, status: Status): void {
      this._dragModule().onDragOver(e, status);
    },

    onDragLeave(e: DragEvent, status: Status): void {
      this._dragModule().onDragLeave(e, status);
    },

    async onDrop(e: DragEvent, targetStatus: Status): Promise<void> {
      await this._dragModule().onDrop(e, targetStatus);
    },

    /** AC1: plain click → open the card's drawer (behavior unchanged).
     *  Ctrl/Cmd+Click toggles this card in the multi-selection (bar appears);
     *  Shift+Click range-selects within its column from the anchor. The
     *  checkbox input stops propagation, so it never lands here. */
    handleCardClick(id: string, e?: MouseEvent): void {
      if (this.isDragging) return;
      if (e?.shiftKey && !e.ctrlKey && !e.metaKey) {
        this.rangeSelectInColumn(id);
        return;
      }
      if (e?.ctrlKey || e?.metaKey) {
        this.toggleSelectTask(id);
        return;
      }
      void this.openTask(id);
    },

    // ---------------------------------- touch: swipe-to-status + per-card move menu
    // Explicit touch alternative to HTML5 drag-and-drop (which never fires on
    // touch devices). A mostly-horizontal swipe moves the card one column in
    // that direction; the ⋮ button opens a floating "Move to" dropdown anchored
    // to the trigger (absolutely positioned, dismissed by outside clicks and
    // Escape). Both funnel into moveTaskStatus(), so 409-rebase + rollback
    // behaviour is shared. Behavior owned by modules/drag.ts (Phase 2 batch D);
    // showMobileMenu/isMobile are CHROME state and stay at root.
    showMobileMenu: false,
    /** Mobile breakpoint flag mirroring Tailwind's `md:` (min-width 768px).
     *  The hamburger's slide-down panel renders x-if'd on this flag so the two
     *  mobile surfaces (panel vs. header strip) are exclusive in the DOM —
     *  Playwright strict-mode testids require each id to match exactly once at
     *  any viewport width. Updated from a matchMedia listener in _runBoot(). */
    isMobile: false,
    cardMenuTaskId: null as string | null,
    swipeState: {
      id: null,
      startX: 0,
      startY: 0,
      dx: 0,
      fromStatus: null,
      active: false,
    } as SwipeState,

    swiping(id: string): boolean {
      return this._dragModule().swiping(id);
    },

    swipeDirection(id: string): "right" | "left" {
      return this._dragModule().swipeDirection(id);
    },

    swipeTargetLabel(id: string): string {
      return this._dragModule().swipeTargetLabel(id);
    },

    onCardTouchStart(e: TouchEvent, task: TaskSummary): void {
      this._dragModule().onCardTouchStart(e, task);
    },

    onCardTouchMove(e: TouchEvent): void {
      this._dragModule().onCardTouchMove(e);
    },

    onCardTouchEnd(e: TouchEvent, task: TaskSummary): void {
      this._dragModule().onCardTouchEnd(e, task);
    },

    onCardTouchCancel(_e: TouchEvent): void {
      this._dragModule().onCardTouchCancel(_e);
    },

    toggleCardMenu(id: string): void {
      this._dragModule().toggleCardMenu(id);
    },

    async moveCardTo(id: string, status: Status): Promise<void> {
      await this._dragModule().moveCardTo(id, status);
    },

    /** Optimistically move a task to targetStatus, then persist with PATCH —
     *  behavior owned by modules/drag.ts (Phase 2 batch D). The detail module's
     *  operational bar reaches this through its deps.moveTaskStatus hook. */
    async moveTaskStatus(taskId: string, targetStatus: Status, initialVersion?: number): Promise<void> {
      await this._dragModule().moveTaskStatus(taskId, targetStatus, initialVersion);
    },

    // -------------------------------------------------------- operational bar
    statusMenuOpen: false,
    ownerPopoverOpen: false,

    /** Operational-bar toggles + version-guarded status move — behavior owned
     *  by modules/detail.ts (Phase 2 batch D). */
    toggleStatusMenu(): void {
      this._detailModule().toggleStatusMenu();
    },

    toggleOwnerPopover(): void {
      this._detailModule().toggleOwnerPopover();
    },

    async moveStatus(status: Status): Promise<void> {
      return await this._detailModule().moveStatus(status);
    },

    // ------------------------------------------------------------------- notes
    notesDraft: "",
    /** The task id whose notes this scratchpad currently reflects/edits. Pins
     *  the draft to a task so saveNotes() can refuse cross-task writes (AC 3). */
    notesDraftOwner: null as string | null,
    notesDirty: false,
    savingNotes: false,
    notesShowPreview: false,

    /** The close/switch held behind the unsaved-notes confirmation — null when
     *  no prompt is on screen. */
    unsavedNotesAction: null as { kind: "close" } | { kind: "open"; taskId: string } | null,

    /** Notes scratchpad + unsaved-notes confirm lifecycle — behavior owned by
     *  modules/detail.ts (Phase 2 batch D; its confirm focus session is
     *  closure-internal there, as is resetNotesDraft). */
    saveNotesDraft(): void {
      this._detailModule().saveNotesDraft();
    },

    /** Show the unsaved-notes confirm and open its focus session. "Keep
     *  editing" (least destructive) receives initial focus. */
    promptUnsavedNotes(action: { kind: "close" } | { kind: "open"; taskId: string }): void {
      this._detailModule().promptUnsavedNotes(action);
    },

    /** "Keep editing" — dismiss the prompt, draft untouched. Focus returns to
     *  whatever had focus inside the drawer when the prompt opened (stacked
     *  session: only THIS layer is closed). */
    cancelUnsavedNotes(): void {
      this._detailModule().cancelUnsavedNotes();
    },

    /** "Discard changes" — drop the draft, then run the deferred close/switch.
     *  The confirm session is closed first; a drawer close afterwards restores
     *  focus to the original board trigger (the switch path re-focuses the new
     *  drawer content via openTask). */
    async confirmUnsavedNotes(): Promise<void> {
      return await this._detailModule().confirmUnsavedNotes();
    },

    /** PUT /notes replaces the whole blob and carries NO version — a concurrent
     *  write can still 409; then we refresh (keeping the user's draft) and say so. */
    async saveNotes(): Promise<void> {
      return await this._detailModule().saveNotes();
    },

    // -------------------------------------------------------- acceptance crits
    acBusySeq: 0,
    newAcText: "",
    addingAc: false,
    editingAcSeq: 0,
    editingAcText: "",
    savingAcSeq: 0,
    deletingAcSeq: 0,
    reorderingAcs: false,
    draggedAcSeq: 0,

    /** [moved → modules/acs.ts] Delegation shims — acceptance criteria: the
     *  bodies moved verbatim (only `this.` became deps accessors). */
    async toggleAc(ac: AcRow, ev?: Event): Promise<void> {
      return await this._acsModule().toggleAc(ac, ev);
    },

    async addAc(): Promise<void> {
      return await this._acsModule().addAc();
    },

    async handleAcPaste(e: ClipboardEvent): Promise<void> {
      return await this._acsModule().handleAcPaste(e);
    },

    async moveAc(seq: number, direction: "up" | "down"): Promise<void> {
      return await this._acsModule().moveAc(seq, direction);
    },

    async dropAc(targetSeq: number): Promise<void> {
      return await this._acsModule().dropAc(targetSeq);
    },

    startEditingAc(ac: AcRow): void {
      this._acsModule().startEditingAc(ac);
    },

    cancelEditingAc(): void {
      this._acsModule().cancelEditingAc();
    },

    async saveAcEdit(seq: number): Promise<void> {
      return await this._acsModule().saveAcEdit(seq);
    },

    async deleteAc(seq: number): Promise<void> {
      return await this._acsModule().deleteAc(seq);
    },

    acDoneCount(): number {
      return this._acsModule().acDoneCount();
    },

    /** AC progress ring inner SVG (geometry + slot math in src/ac-math.ts). */
    acRingInner(): string {
      return this._acsModule().acRingInner();
    },

    acProgressLabel(): string {
      return this._acsModule().acProgressLabel();
    },

    completedLabel(ac: AcRow): string {
      return this._acsModule().completedLabel(ac);
    },

    // ----------------------------------------------------------------- comments
    commentForm: { kind: "progress" as CommentKind, body: "" },
    /** Write/Preview toggle for the COMMENT markdown field (always present in
     *  the drawer). Default = Write; reset on every openTask() (AC 1). */
    commentShowPreview: false,
    postingComment: false,
    loadingOlderComments: false,
    totalComments: 0,

    /** [moved → modules/comments.ts] Delegation shims — comments: the bodies
     *  moved verbatim (only `this.` became deps accessors). */
    restoreCommentDraftForTask(id: string): void {
      this._commentsModule().restoreCommentDraftForTask(id);
    },

    saveCommentDraft(): void {
      this._commentsModule().saveCommentDraft();
    },

    hasMoreOlderComments(): boolean {
      return this._commentsModule().hasMoreOlderComments();
    },

    olderCommentsRemaining(): number {
      return this._commentsModule().olderCommentsRemaining();
    },

    async initComments(id: string): Promise<void> {
      return await this._commentsModule().initComments(id);
    },

    mergeComments(incoming: CommentRow[]): void {
      this._commentsModule().mergeComments(incoming);
    },

    async loadOlderComments(): Promise<void> {
      return await this._commentsModule().loadOlderComments();
    },

    /** Append a typed comment (no task version bump). Behavior owned by
     *  modules/comments.ts. */
    async postComment(): Promise<void> {
      return await this._commentsModule().postComment();
    },

    // ---------------------------------------------------------------- artifacts
    artifactForm: { open: false, kind: "url" as ArtifactKind, ref: "", summary: "", file: null as File | null },
    attachingArtifact: false,
    previewModal: { open: false, src: "", alt: "" },
    copiedArtifactId: null as string | null,
    copiedArtifactTimer: null as any,
    deletingArtifactId: "" as string,
    artifactTextExpanded: {} as Record<string, boolean>,
    artifactTextContent: {} as Record<string, string>,
    artifactTextLoading: {} as Record<string, boolean>,
    artifactTextError: {} as Record<string, string>,

    onArtifactFileSelected(event: Event): void {
      this._artifactsModule().onArtifactFileSelected(event);
    },

    artifactDragOver: false,

    async uploadFileArtifact(file: File, summary?: string, kind?: ArtifactKind): Promise<ArtifactRow | null> {
      return await this._artifactsModule().uploadFileArtifact(file, summary, kind);
    },

    async handleArtifactDrop(e: DragEvent): Promise<void> {
      return await this._artifactsModule().handleArtifactDrop(e);
    },

    async handlePaste(e: ClipboardEvent): Promise<void> {
      return await this._artifactsModule().handlePaste(e);
    },

    resetArtifactForm(): void {
      this._artifactsModule().resetArtifactForm();
    },

    async attachArtifact(): Promise<void> {
      return await this._artifactsModule().attachArtifact();
    },

    openImagePreview(src: string, alt: string): void {
      this._artifactsModule().openImagePreview(src, alt);
    },

    closeImagePreview(): void {
      this._artifactsModule().closeImagePreview();
    },

    isStoredArtifact(ref: string | null | undefined): boolean {
      return this._artifactsModule().isStoredArtifact(ref);
    },

    isStoredImage(a: { kind: string; ref: string; summary?: string }): boolean {
      return this._artifactsModule().isStoredImage(a);
    },

    isStoredVideo(a: { kind: string; ref: string; summary?: string }): boolean {
      return this._artifactsModule().isStoredVideo(a);
    },

    isStoredAudio(a: { kind: string; ref: string; summary?: string }): boolean {
      return this._artifactsModule().isStoredAudio(a);
    },

    isStoredNonImage(a: { kind: string; ref: string; summary?: string }): boolean {
      return this._artifactsModule().isStoredNonImage(a);
    },

    isStoredText(a: { kind: string; ref: string; summary?: string }): boolean {
      return this._artifactsModule().isStoredText(a);
    },

    async toggleArtifactPreview(a: { id: string; ref: string }): Promise<void> {
      return await this._artifactsModule().toggleArtifactPreview(a);
    },

    async deleteArtifact(artifactId: string): Promise<void> {
      return await this._artifactsModule().deleteArtifact(artifactId);
    },

    // ---------------------------------------------------------------- claims
    // (state + handlers live in modules/claims.ts — Phase 2 batch C)
    claimBusy: false,
    nowTick: Date.now(),
    _tickerId: null as ReturnType<typeof setInterval> | null,

    /** Live-ticking MM:SS countdown until lease expiry for the claim capsule. */
    claimCountdown(t?: Pick<TaskSummary, "claim_expires_at"> | null): string {
      return this._claimsModule().claimCountdown(t);
    },

    /** Whether a task's claim lease has EXPIRED. Reads `nowTick` (the same
     *  value claimCountdown() uses) so Alpine tracks it: the muted "expired"
     *  styling flips live the moment a lease crosses expiry, without any poll. */
    claimExpired(t: Pick<TaskSummary, "claimed_by" | "claim_expires_at">): boolean {
      return this._claimsModule().claimExpired(t);
    },

    async claimCurrentTask(): Promise<void> {
      return await this._claimsModule().claimCurrentTask();
    },

    claimTask(): Promise<void> {
      return this.claimCurrentTask();
    },

    async releaseCurrentTask(): Promise<void> {
      return await this._claimsModule().releaseCurrentTask();
    },

    releaseTask(): Promise<void> {
      return this.releaseCurrentTask();
    },

    async heartbeatCurrentTask(): Promise<void> {
      return await this._claimsModule().heartbeatCurrentTask();
    },

    heartbeatTask(): Promise<void> {
      return this.heartbeatCurrentTask();
    },

    // ---------------------------------------------------------------- relations
    relationForm: {
      kind: "parent" as string,
      targetId: "",
      search: "",
      open: false,
      busy: false,
      activeIndex: -1,
    },

    /** Add-relation combobox + RelationRow → RelationView shaping owned by
     *  modules/relations.ts (Phase 2 batch D); `relationForm` stays root. */
    selectRelationTarget(t: TaskSummary): void {
      this._relationsModule().selectRelationTarget(t);
    },

    onRelationComboboxKeyDown(dir: "up" | "down"): void {
      this._relationsModule().onRelationComboboxKeyDown(dir);
    },

    onRelationComboboxEnter(): void {
      this._relationsModule().onRelationComboboxEnter();
    },

    filteredRelationTasks(): TaskSummary[] {
      return this._relationsModule().filteredRelationTasks();
    },

    availableRelationTasks(): TaskSummary[] {
      return this._relationsModule().availableRelationTasks();
    },

    relationRows(): RelationView[] {
      return this._relationsModule().relationRows();
    },

    detailSubtaskTotal(): number {
      return this._relationsModule().detailSubtaskTotal();
    },

    detailSubtaskDone(): number {
      return this._relationsModule().detailSubtaskDone();
    },

    detailSubtaskPercent(): number {
      return this._relationsModule().detailSubtaskPercent();
    },

    async addRelation(): Promise<void> {
      return await this._relationsModule().addRelation();
    },

    async removeRelation(r: RelationRow): Promise<void> {
      return await this._relationsModule().removeRelation(r);
    },

    taskTitle(id: string): string {
      const b = this.board;
      if (b) {
        for (const s of STATUSES) {
          const row = b.tasks[s]?.find((t) => t.id === id);
          if (row) return row.title;
        }
      }
      return shortId(id);
    },

    /** Native discard prompt shared by every guarded close path. True = the
     *  user accepted throwing the draft away. Defensive under node:test, where
     *  there is no window to ask: a non-UI caller is never blocked. */
    confirmDiscard(message: string): boolean {
      if (typeof window === "undefined" || typeof window.confirm !== "function") return true;
      return window.confirm(message);
    },

    showShortcutHelp: false,

    handleGlobalKeydown(e: KeyboardEvent): void {
      if (!e) return;
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      if (isEditableTarget(e.target as HTMLElement | null)) return;

      // Dispatch table in src/keyboard.ts; this root keeps the DOM focus and
      // surface-toggle steps.
      const action = globalKeyAction(e.key, {
        showNewTask: this.showNewTask,
        showProjectSettings: this.showProjectSettings,
        showShortcutHelp: this.showShortcutHelp,
        detailOpen: this.detailOpen,
        showAuthModal: this.showAuthModal,
      });
      if (action === "new-task") {
        e.preventDefault();
        this.openNewTask("todo");
      } else if (action === "toggle-dashboard") {
        if (this.appBooted && this.selectedProjectId) {
          e.preventDefault();
          this.toggleDashboard();
        }
      } else if (action === "focus-search") {
        e.preventDefault();
        const searchInput = document.querySelector('input[x-model="filterSearch"]') as HTMLInputElement | null;
        if (searchInput) {
          searchInput.focus();
          searchInput.select();
        }
      } else if (action === "toggle-help") {
        e.preventDefault();
        this.showShortcutHelp = !this.showShortcutHelp;
      }
    },

    /** Escape key: close the top-most overlay or cancel edit mode. Dirty
     *  surfaces (task edit, new task, project settings) confirm first via
     *  confirmDiscard() — Escape never silently discards a draft. The priority
     *  ORDER of the cascade lives in src/keyboard.ts (escapeAction); this root
     *  keeps each surface's imperative close step. */
    dismissTop(): void {
      const act = escapeAction({
        showShortcutHelp: this.showShortcutHelp,
        imagePreviewOpen: Boolean(this.previewModal?.open),
        unsavedNotesPending: Boolean(this.unsavedNotesAction),
        showAuthModal: this.showAuthModal,
        showProjectSettings: this.showProjectSettings,
        showArchiveConfirm: this.showArchiveConfirm,
        editingAcActive: this.editingAcSeq !== 0,
        editingTaskActive: Boolean(this.editingTask),
        showTaskOpenCodeConfig: this.showTaskOpenCodeConfig,
        statusMenuOpen: this.statusMenuOpen,
        ownerPopoverOpen: this.ownerPopoverOpen,
        priorityPopoverOpen: this.priorityPopoverOpen,
        duePopoverOpen: this.duePopoverOpen,
        batchMenusOpen: Boolean(this.batchMoveMenuOpen || this.batchPriorityMenuOpen || this.batchAssignMenuOpen),
        detailOpen: this.detailOpen,
        showNewTask: this.showNewTask,
        confirmNewTaskDiscard: this.confirmNewTaskDiscard,
        projectMenuOpen: this.projectMenuOpen,
        headerMenuOpen: this.headerMenuOpen,
        showNewProject: this.showNewProject,
        cardMenuOpen: Boolean(this.cardMenuTaskId),
        showMobileMenu: this.showMobileMenu,
        hasSelection: this.selectedTaskIds.size > 0,
        dashboardOpen: this.dashboardOpen,
      });
      if (!act) return;
      switch (act) {
        case "close-help":
          this.showShortcutHelp = false;
          break;
        case "close-preview":
          this.closeImagePreview();
          break;
        case "keep-editing-notes":
          this.cancelUnsavedNotes(); // Escape = "keep editing"
          break;
        case "close-auth":
          this.closeAuthModal();
          break;
        case "cancel-archive-confirm":
          this.cancelArchiveConfirm();
          break;
        case "request-close-settings":
          this.requestCloseProjectSettings();
          break;
        case "cancel-ac-edit":
          this.cancelEditingAc();
          break;
        case "cancel-task-edit":
          // The edit form is the topmost surface inside the drawer: a dirty
          // draft must be confirmed BEFORE Escape exits edit mode.
          if (!this.discardEditDraftIfConfirmed()) return;
          this.cancelEditing();
          break;
        case "close-open-code-config":
          this.showTaskOpenCodeConfig = false;
          break;
        case "close-status-menu":
          this.statusMenuOpen = false;
          break;
        case "close-owner-popover":
          this.ownerPopoverOpen = false;
          break;
        case "close-priority-popover":
          this.priorityPopoverOpen = false;
          break;
        case "close-due-popover":
          this.duePopoverOpen = false;
          break;
        case "close-batch-menus":
          // Batch dropdowns sit in the board layer: a first Escape closes just
          // the open menu — it never touches the selection itself.
          this._closeBatchMenus();
          break;
        case "close-detail":
          this.requestCloseDetail();
          break;
        case "cancel-new-task-confirm":
          this.cancelNewTaskConfirm();
          break;
        case "request-close-new-task":
          this.requestCloseNewTask();
          break;
        case "close-project-menu":
          this.closeProjectMenu(true); // header dropdowns are topmost
          break;
        case "close-header-menu":
          this.closeHeaderMenu(true);
          break;
        case "close-new-project":
          this.showNewProject = false;
          break;
        case "close-card-menu":
          this.cardMenuTaskId = null;
          break;
        case "close-mobile-menu":
          this.showMobileMenu = false;
          break;
        case "clear-selection":
          // AC4: last-resort Escape clears the multi-selection — only reached
          // when every other surface (drawer, dialogs, menus) is already closed.
          this.clearSelection();
          break;
        case "close-dashboard":
          this.closeDashboard();
          break;
      }
    },

    isWebHref(s: string | null | undefined): boolean {
      return typeof s === "string" && /^(https?:|mailto:)/i.test(s.trim());
    },

    // ------------------------------------------------------------- template glue
    STATUSES,
    STATUS_LABEL,
    MEMBER_ROLES,
    SORT_OPTIONS,
    canMove,
    /** TEST hook only (task 01M3MMYBHECPH5Z5FRHJDS5G4J): lets a browser test
     *  shorten the per-request timeout so hang paths are exercised in ~1s
     *  instead of 20. Never referenced by any template or production code path;
     *  values ≤ 0 restore the default (API_TIMEOUT_MS). */
    setApiTimeoutMsForTests,
    statusLabel,
    priorityClass,
    priorityName,
    commentKindClass,
    artifactKindClass,
    // UI primitive layer (Phase 3) — web/src/ui pure contracts. Flat-root
    // entries the index.html templates bind directly; values byte-identical to
    // the pre-Phase-3 markup (locked by ui-* unit tests + E2E).
    btnClass,
    menuPanelClass: MENU_PANEL_CLASS,
    menuItemClass,
    EMPTY_STATE,
    /** Toast item base chrome (ui/toast.ts) — the row's static :class element. */
    toastBaseClass: TOAST_BASE_CLASS,
    toastClassList,
    toastErrorPrefix,
    /** Board card's full dynamic :class — see ui/card.ts boardCardClasses; the
     *  root resolves every state input (selection, drag, claim, block, busy). */
    boardCardClass(task: TaskSummary, status: Status): string {
      return boardCardClasses({
        density: this.cardDensity,
        selected: this.isTaskSelected(task.id),
        dragging: this.draggedTaskId === task.id,
        claimed: !!task.claimed_by && !this.claimExpired(task),
        blocked: this.isBlocked(task.id),
        inDoneColumn: status === "done",
        busy: this.isTaskBusy(task.id),
      });
    },
    /** Density-dependent sub-part padding (claim capsule, etc.) — see
     *  ui/card.ts CARD_DENSITY_PARTS; resolves `this.cardDensity` internally. */
    cardDensityClass(part: CardDensityPart): string {
      return uiCardDensityClass(this.cardDensity, part);
    },
    isTextArtifact,
    isOverdue,
    dueLabel,
    claimHint,
    formatClaimCountdown,
    isClaimExpired,
    actorLabel,
    initials,
    shortId,
    relTime,
    formatEventDetail,
    /** Sanitized-markdown renderer (raw source → marked.parse → DOM-based
     *  sanitize in ./md), memoized so poll re-renders don't re-parse unchanged bodies. */
    _mdCache: new Map<string, string>(),
    md(src: string | null | undefined): string {
      if (!src || !src.trim()) return "";
      const hit = this._mdCache.get(src);
      if (hit !== undefined) return hit;
      const out = renderMarkdown(src);
      if (this._mdCache.size > MD_CACHE_MAX) this._mdCache.clear();
      this._mdCache.set(src, out);
      return out;
    },

    // ---------------------------------------------------------------------- init
    _bootstrap: null as BootstrapModule | null,

    /** Lazily-built boot module (modules/bootstrap.ts — Phase 2 bootstrap
     *  hatch): owns the one-shot boot sequence (the init memo, ticker +
     *  SSE-reload debounce creation, window/matchMedia/$watch listener
     *  registration, first-load deep-link resolution) and the boot-error Retry
     *  path. Every template-bound name stays flat root state above; only the
     *  boot memo now lives in the module's closure (never rendered, never poked). */
    _bootstrapModule(): BootstrapModule {
      if (this._bootstrap) return this._bootstrap;
      this._bootstrap = createBootstrapModule({
        // state-in
        selectedProjectId: () => this.selectedProjectId,
        projects: () => this.projects,
        loadError: () => this.loadError,
        bootError: () => this.bootError,
        bootRetrying: () => this.bootRetrying,
        polling: () => this.polling,
        themeMq: () => this._themeMq,
        theme: () => this.theme,
        // state-out
        setAppBooted: (v) => { this.appBooted = v; },
        setBootError: (v) => { this.bootError = v; },
        setBootRetrying: (v) => { this.bootRetrying = v; },
        setShowNewProject: (v) => { this.showNewProject = v; },
        setSelectedProjectId: (v) => { this.selectedProjectId = v; },
        setIsMobile: (v) => { this.isMobile = v; },
        setNowTick: (ts) => { this.nowTick = ts; },
        setTickerId: (id) => { this._tickerId = id; },
        setSseReloadDebounce: (d) => { this.sseReloadDebounce = d; },
        // hooks — root-owned orchestration kept outside the module
        loadProjects: (force) => this.loadProjects(force),
        loadBoard: (force) => this.loadBoard(force),
        refreshSessionIdentity: () => this.refreshSessionIdentity(),
        openDeepLinkedTask: (id) => this.openDeepLinkedTask(id),
        openDeepLinkedDashboard: (pid, fromBootError) => this.openDeepLinkedDashboard(pid, fromBootError),
        connectSse: () => this.connectSse(),
        startPolling: () => this.startPolling(),
        disconnectSse: (resetBackoff) => this.disconnectSse(resetBackoff),
        onPopState: () => this.onPopState(),
        initFromUrl: () => this.initFromUrl(),
        applyTheme: () => this.applyTheme(),
        setDocumentTitle: () => this.setDocumentTitle(),
        scheduleFullTextSearch: () => this.scheduleFullTextSearch(),
        syncUrlState: (immediate) => this.syncUrlState(immediate),
        isRestoringUrlState: () => this._boardViewModule().isRestoringUrlState(),
        focusWhenVisible: (el) => this._focusWhenVisible(el),
        // Alpine $watch seam — no-op under node:test (no component proxy).
        watch: (prop, cb) => {
          if (typeof (this as any).$watch === "function") (this as any).$watch(prop, cb);
        },
      });
      return this._bootstrap;
    },

    /** Boot entry point — behavior owned by modules/bootstrap.ts (Phase 2
     *  bootstrap hatch). Memoized there: Alpine's double init() invocation
     *  returns the FIRST boot's promise on the second call. */
    init(): Promise<void> {
      return this._bootstrapModule().init();
    },

    /** Success-path boot continuation, shared by init() and retryBoot() —
     *  behavior owned by modules/bootstrap.ts (Phase 2 bootstrap hatch). */
    continueBoot(
      deepLinkedTaskId: string | null,
      deepLinkedDashboardProjectId: string | null = null,
    ): Promise<void> {
      return this._bootstrapModule().continueBoot(deepLinkedTaskId, deepLinkedDashboardProjectId);
    },

    /** Retry button on the boot-error screen — behavior owned by modules/
     *  bootstrap.ts (Phase 2 bootstrap hatch). */
    retryBoot(): Promise<void> {
      return this._bootstrapModule().retryBoot();
    },
  };
}
