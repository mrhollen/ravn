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
import { createSseModule, sseTelemetry } from "./modules/sse";
import type { SseModule } from "./modules/sse";
import { createOpenCodeModule } from "./modules/opencode";
import type { OpenCodeModule } from "./modules/opencode";
import { createNewTaskModule } from "./modules/new-task";
import type { NewTaskModule } from "./modules/new-task";
import { createTaskEditModule } from "./modules/task-edit";
import type { TaskEditModule } from "./modules/task-edit";
import { createAcsModule } from "./modules/acs";
import type { AcsModule } from "./modules/acs";
import { createCommentsModule } from "./modules/comments";
import { createArtifactsModule, type ArtifactsModule } from "./modules/artifacts";
import type { CommentsModule } from "./modules/comments";
import { createProjectSettingsModule } from "./modules/project-settings";
import type { ProjectSettingsModule } from "./modules/project-settings";
import { escapeAction, globalKeyAction, isEditableTarget } from "./keyboard";
import {
  beginMove,
  canMove,
  committedSwipeDirection,
  dropAllowed,
  endMove,
  friendlyTransitionError,
  illegalMoveMessage,
  MOVE_TIMEOUT_MS,
  MoveTimedOutError,
  swipeActivated,
  swipeDirection as swipeDirFor,
  swipeTargetLabel as swipeLabelFor,
  swipeTargetStatus,
  withTimeout,
} from "./moves";
import { canApplyDetail, createDebounce, createSequenceGuard, SSE_RELOAD_DEBOUNCE_MS } from "./refresh";
import { renderMarkdown } from "./md";
import { MEMBER_ROLES } from "./members";
import { notesDraftKey } from "./drafts";
import { persistTheme, readStoredTheme, resolveDark, type ThemeMode } from "./theme";
import {
  SORT_OPTIONS,
  readStoredHoistParents,
  readStoredSort,
} from "./sort";
import {
  isMobileDetailViewport,
  parseBoardQueryParams,
  taskRouteIdFromPath,
  taskRoutePath,
  type BoardQueryParams,
} from "./routes";
import type { ColumnView } from "./columns";
import type {
  AcRow,
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
  MineScope,
  OpenCodeAgentOption,
  OpenCodeModelOption,
  OpenCodeOptionsResponse,
  ProjectWithCounts,
  RelationRow,
  SessionIdentity,
  SseConnectionStatus,
  StartOpenCodeInput,
  StartOpenCodeResponse,
  Status,
  TaskDetail,
  TaskSummary,
  UpdateTaskReq,
} from "./types";
import { STATUSES, STATUS_LABEL } from "./types";
import {
  actorLabel,
  artifactKindClass,
  claimHint,
  commentKindClass,
  copyTextToClipboard,
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
/** Persisted per-actor board scope (epic T7); "all" means no mine filter. */
export const LS_TASK_SCOPE = "ravn.task_scope";
/** Persisted "My projects" filter (epic T7): true → the open project list is fetched
 *  with ?mine=created so only projects the signed-in identity created are shown. */
export const LS_MY_PROJECTS = "ravn.my_projects";

/** Board data-source scope: the unscoped default plus the three task `mine` values. */
export type TaskScope = "all" | MineScope;
const TASK_SCOPES: readonly TaskScope[] = ["all", "assigned", "created", "activity"];

/** Read + validate the persisted board scope, falling back to "all" on any bad value. */
export function readStoredTaskScope(): TaskScope {
  try {
    const v = safeStorage.getItem(LS_TASK_SCOPE) ?? "all";
    return (TASK_SCOPES as readonly string[]).includes(v) ? (v as TaskScope) : "all";
  } catch {
    return "all";
  }
}

/** Read + validate the persisted "My projects" flag; falls back to false on any bad value. */
export function readStoredMyProjects(): boolean {
  try {
    return safeStorage.getItem(LS_MY_PROJECTS) === "1";
  } catch {
    return false;
  }
}

/** Human-readable event diff formatter for the Task Detail Activity list. */
export function formatEventDetail(e?: { kind: string; before?: string; after?: string } | null): string {
  if (!e || !e.kind) return "";
  let b: any = null;
  let a: any = null;
  if (e.before) {
    try {
      b = JSON.parse(e.before);
    } catch {}
  }
  if (e.after) {
    try {
      a = JSON.parse(e.after);
    } catch {}
  }

  switch (e.kind) {
    case "status-changed": {
      const from = b?.status;
      const to = a?.status;
      if (from && to) return `${from} → ${to}`;
      if (to) return `status: ${to}`;
      return "";
    }
    case "updated": {
      const diffs: string[] = [];
      if (b?.priority !== undefined && a?.priority !== undefined && b.priority !== a.priority) {
        diffs.push(`P${b.priority} → P${a.priority}`);
      } else if (a?.priority !== undefined && b?.priority === undefined) {
        diffs.push(`priority P${a.priority}`);
      }
      if (b?.title !== undefined && a?.title !== undefined && b.title !== a.title) {
        diffs.push("title updated");
      }
      if (b?.due_at !== undefined || a?.due_at !== undefined) {
        if (!a?.due_at) diffs.push("due date cleared");
        else diffs.push("due date updated");
      }
      if (b?.description !== undefined && a?.description !== undefined && b.description !== a.description) {
        diffs.push("description updated");
      }
      return diffs.join(", ");
    }
    case "assigned": {
      const assigneeName = a?.assignee?.name || (typeof a?.assignee === "string" ? a.assignee : "");
      if (assigneeName) return `assignee: ${assigneeName}`;
      if (b?.assignee && !a?.assignee) return "owner removed";
      return "";
    }
    case "claimed": {
      const holder = a?.holder?.name;
      if (holder) return `claimed by ${holder}`;
      return "claimed";
    }
    case "released": {
      return "claim released";
    }
    case "heartbeat": {
      return "lease extended";
    }
    case "noted": {
      return "notes updated";
    }
    case "commented": {
      const kind = a?.kind;
      return kind ? `commented (${kind})` : "commented";
    }
    case "ac-toggled": {
      const seq = a?.seq ?? b?.seq;
      const done = a?.done;
      if (seq !== undefined) {
        return done ? `AC #${seq} completed` : `AC #${seq} reopened`;
      }
      return "";
    }
    case "ac-added": {
      const seq = a?.seq;
      return seq !== undefined ? `AC #${seq} added` : "AC added";
    }
    case "ac-updated": {
      if (a?.reorder || b?.reorder) return "ACs reordered";
      const seq = a?.seq ?? b?.seq;
      return seq !== undefined ? `AC #${seq} updated` : "AC updated";
    }
    case "ac-removed": {
      const seq = b?.seq ?? a?.seq;
      return seq !== undefined ? `AC #${seq} removed` : "AC removed";
    }
    case "relation-changed": {
      const target = a || b;
      const action = target?.action || (a ? "added" : "removed");
      const kind = target?.kind ? ` ${target.kind}` : "";
      const other = target?.other_id ? ` ${shortId(target.other_id)}` : "";
      return `${action}${kind}${other}`;
    }
    case "artifact-added": {
      const kind = a?.kind || "artifact";
      const filename = a?.filename ? ` (${a.filename})` : "";
      return `attached ${kind}${filename}`;
    }
    case "artifact-removed": {
      const kind = b?.kind || a?.kind || "artifact";
      return `removed ${kind}`;
    }
    case "created": {
      return "task created";
    }
    default:
      return "";
  }
}
export { LS_TOKEN };
export const DEFAULT_DONE_LIMIT = 20;
/** Separator between the segments of a dynamic document.title (WCAG 2.4.2,
 *  task 01M3MMVV92639YJ5822824NTV1): "<task> – <project> – Rävn". */
export const TITLE_SEPARATOR = " – ";
const MD_CACHE_MAX = 500;
// Max visual card offset (px) while swiping. Commit/slop thresholds still use
// the raw finger displacement — this clamp only limits what is drawn.
const MAX_SWIPE_PX = 96;

// Phase 2 (sse): the SSE preamble — LEGACY_NAMED_SSE_EVENT_KINDS doc + const and the
// sseTelemetry singleton counters — moved to modules/sse.ts; re-exported below so the
// existing import surface (tests, main.ts) stays intact.
export { sseTelemetry } from "./modules/sse";

export interface Toast {
  id: number;
  msg: string;
  kind: "error" | "info";
  /** `href` renders an external <a>; `onAction` renders a <button> that runs a
   *  local JS callback (e.g. Undo) instead — exactly one of the two is set. */
  action?: { label: string; href?: string; onAction?: () => void };
  /** Optional border class to override or customize the border. */
  borderClass?: string;
  /** When true, the toast will NOT auto-dismiss after 6.5s. */
  persistent?: boolean;
  /** When true, suppresses the "Error: " prefix even if kind is "error". */
  hideErrorPrefix?: boolean;
  /** Marker for the SSE disconnect notification toast. */
  isSseDisconnect?: boolean;
  /** True while the leave transition plays (x-show hides it), then the row is
   *  dropped from the list. x-for removes nodes instantly, so the exit
   *  animation has to be driven through x-show first. */
  leaving?: boolean;
}

interface RelationView {
  key: string;
  label: string;
  otherId: string;
  title: string;
  status?: Status;
  isBlocker?: boolean;
  badgeClass?: string;
  raw: RelationRow;
}

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
    /** Boot memo (the init() guard — see init()): holds the in-flight or
     *  completed _runBoot() promise so a second invocation returns it instead
     *  of re-running boot. Null until the first init(); cleared only if that
     *  boot threw a genuine (non-network) error. */
    _bootPromise: null as Promise<void> | null,
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
    showArchived: false,
    /** Per-status groups of ARCHIVED tasks, fetched with listTasks({archived:true}).
     *  null = not loaded yet. Kept after toggling OFF so re-enabling is instant;
     *  refreshed by any archive/unarchive that changes the archived set. */
    archivedBoardTasks: null as Record<Status, TaskSummary[]> | null,
    /** Guards the archived-view fetch: rapid toggle-off/toggle-on must let only
     *  the NEWEST fetch land (mirrors the board module's sequence guards). */
    _archivedSeq: createSequenceGuard(),
    /** Done-column bulk archive confirmation step (two-click confirm inline in
     *  the column footer). Distinct from the PROJECT archive's showArchiveConfirm. */
    archiveAllDoneConfirmOpen: false,
    archiveAllDoneBusy: false,

    /** True when the columns should render the ARCHIVED data source instead of
     *  the live board (and the done cap is bypassed — archived sets are small
     *  and there is no "show more" affordance in that mode). */
    archivedColumns(): Record<Status, TaskSummary[]> | null {
      return this.showArchived ? this.archivedBoardTasks : null;
    },

    /** Fetch the selected project's full archived set (paged at 100/page — the
     *  API max) grouped by status. Returns null when superseded by a newer load
     *  or a project switch mid-flight, so callers know NOT to apply. */
    async _fetchArchivedTasks(projectId: string): Promise<Record<Status, TaskSummary[]> | null> {
      const token = this._archivedSeq.next();
      const out: TaskSummary[] = [];
      let offset = 0;
      for (;;) {
        const res = await api.listTasks({ project_id: projectId, archived: true, limit: 100, offset });
        const page = res.tasks ?? [];
        out.push(...page);
        offset += page.length;
        if (offset >= (res.total ?? 0) || page.length === 0) break;
      }
      // Superseded by a newer load (or the user switched projects mid-flight):
      // applying this response would show the wrong project's rows.
      if (!this._archivedSeq.isFresh(token) || projectId !== this.selectedProjectId) return null;
      const tasks: Record<Status, TaskSummary[]> = { todo: [], doing: [], done: [] };
      for (const t of out) tasks[t.status].push(t);
      return tasks;
    },

    /** Toggle the "Show archived" board view. OFF is a pure flag flip — the
     *  untouched `board` renders again, byte-for-byte (AC3's exact-restore
     *  guarantee). ON fetches the full archived set for the selected project and
     *  groups it by status. Failure reverts the flag and toasts. */
    async toggleShowArchived(): Promise<void> {
      if (this.showArchived) {
        this.showArchived = false; // instant restore of the previous board
        return;
      }
      const projectId = this.selectedProjectId;
      if (!projectId) return;
      try {
        const tasks = await this._fetchArchivedTasks(projectId);
        if (tasks === null) return; // superseded — do not flip, do not apply
        this.clearSelection(); // selection applies to live cards only — never into the archived view
        this.archivedBoardTasks = tasks;
        this.showArchived = true;
      } catch (e) {
        this.toast(errMsg(e), "error");
      }
    },

    /** Re-fetch the archived view after a mutation that changed its contents
     *  (unarchiving from within it, or an archive we just performed). Only runs
     *  while the mode is ON; failures are silent — the toasts on the mutations
     *  themselves already told the user what happened. */
    async refreshArchivedView(): Promise<void> {
      if (!this.showArchived || !this.selectedProjectId) return;
      try {
        const tasks = await this._fetchArchivedTasks(this.selectedProjectId);
        if (tasks !== null) this.archivedBoardTasks = tasks;
      } catch {
        /* keep the last known view */
      }
    },

    /** AC1: archive or unarchive ONE task — from the card ⋮ menu / the archived
     *  view's per-card Unarchive, or from the task detail header. Reuses the
     *  move flow's concurrency primitives (busyTaskIds lock) and its 409
     *  re-read + retry-once discipline. Archive is optimistic (the card leaves
     *  its column immediately) with rollback on failure and a ~5s Undo toast;
     *  unarchive relies on the post-PATCH board reload for the card's return. */
    async setTaskArchived(taskId: string, archived: boolean): Promise<void> {
      // Locate the row: live board card, ARCHIVED-view card (when that mode is
      // ON the cards come from a different array), or the open detail payload.
      let summary: TaskSummary | null = null;
      let inBoardStatus: Status | null = null;
      if (this.board) {
        for (const s of STATUSES) {
          const found = this.board.tasks[s]?.find((t) => t.id === taskId);
          if (found) {
            summary = found;
            inBoardStatus = s;
            break;
          }
        }
      }
      let inArchivedStatus: Status | null = null;
      if (!summary && this.showArchived && this.archivedBoardTasks) {
        for (const s of STATUSES) {
          const found = this.archivedBoardTasks[s]?.find((t) => t.id === taskId);
          if (found) {
            summary = found;
            inArchivedStatus = s;
            break;
          }
        }
      }
      const detailOpen = !!this.detail && this.detail.id === taskId;
      if (!summary && !detailOpen) return;

      // Per-task in-flight lock (shared with the move flow): the same id never
      // double-fires; every OTHER card stays interactive meanwhile.
      if (!beginMove(this.busyTaskIds, taskId)) {
        this.toast("That task is still busy — one moment.", "info");
        return;
      }

      const versionToUse = summary?.version ?? (this.detail ? this.detail.version : 0);
      const patch = (version: number): Promise<TaskSummary> =>
        api.updateTask(taskId, { actor: this.actor(), version, archived });

      // Optimistic removal for ARCHIVE: the card leaves its column now. Failure
      // rolls this back; success re-syncs with loadBoard regardless. Unarchive
      // removes from the archived view instead (the card "returns" to the live
      // board on the reload — we don't guess its post-refresh position).
      type Snap = { where: "board" | "archived"; status: Status; task: TaskSummary };
      let snap: Snap | null = null;
      if (archived && summary && inBoardStatus && this.board) {
        snap = { where: "board", status: inBoardStatus, task: summary };
        this.board.tasks[inBoardStatus] = this.board.tasks[inBoardStatus].filter((t) => t.id !== taskId);
        if ((this.board.counts[inBoardStatus] ?? 0) > 0) this.board.counts[inBoardStatus]--;
      } else if (!archived && summary && inArchivedStatus && this.archivedBoardTasks) {
        snap = { where: "archived", status: inArchivedStatus, task: summary };
        this.archivedBoardTasks[inArchivedStatus] = this.archivedBoardTasks[inArchivedStatus].filter((t) => t.id !== taskId);
      }
      const rollbackSnap = (): void => {
        if (!snap) return;
        if (snap.where === "board" && this.board) {
          this.board.tasks[snap.status] = [...this.board.tasks[snap.status], snap.task];
          this.board.counts[snap.status] = (this.board.counts[snap.status] ?? 0) + 1;
        } else if (snap.where === "archived" && this.archivedBoardTasks) {
          this.archivedBoardTasks[snap.status] = [...this.archivedBoardTasks[snap.status], snap.task];
        }
      };

      const resync = (): Promise<void> =>
        (async () => {
          await Promise.all([
            detailOpen ? this.refreshDetail(true) : undefined,
            this.loadBoard(true),
            this.showArchived ? this.refreshArchivedView() : undefined,
          ]);
        })();

      try {
        const updated = await patch(versionToUse);
        if (detailOpen && this.detail) this.detail.archived = archived;
        await resync();
        if (archived) {
          // Undo window = the toast's default auto-expiry (~6.5s). Undo re-applies
          // with the FRESH version returned by THIS patch, then re-syncs everything.
          const undoVersion = updated.version;
          this.toast("Task archived", "info", {
            label: "Undo",
            onAction: () => this._undoArchive(taskId, undoVersion),
          });
        } else {
          this.toast("Task restored from archive");
        }
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          // Concurrent edit elsewhere — re-read the fresh row and retry ONCE.
          try {
            const fresh = await api.getTask(taskId, { commentLimit: 1 });
            if (detailOpen && this.detail) this.applyDetail(fresh);
            if (fresh.archived !== archived) {
              await patch(fresh.version);
            }
            await resync();
            this.toast("Changed elsewhere — retried with the fresh version.", "info");
          } catch (e2) {
            rollbackSnap();
            await Promise.all([
              detailOpen ? this.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
              this.loadBoard(true).catch(() => undefined),
            ]);
            if (ApiError.isVersionConflict(e2)) {
              this.toast("Changed elsewhere — refreshed (your change was not applied).", "info");
            } else {
              this.toast(errMsg(e2), "error");
            }
          }
        } else {
          rollbackSnap();
          await Promise.all([
            detailOpen ? this.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
            this.loadBoard(true).catch(() => undefined),
          ]);
          this.toast(errMsg(e), "error");
        }
      } finally {
        endMove(this.busyTaskIds, taskId);
      }
    },

    /** The Undo action behind the archive toast: re-apply archived:false with
     *  the fresh version from the original patch. Throws on failure so
     *  runToastAction surfaces an error toast (the board reloads to reality). */
    async _undoArchive(taskId: string, version: number): Promise<void> {
      try {
        await api.updateTask(taskId, { actor: this.actor(), version, archived: false });
        await Promise.all([
          this.loadBoard(true),
          this.showArchived ? this.refreshArchivedView() : Promise.resolve(),
        ]);
      } catch (e) {
        await this.loadBoard(true).catch(() => undefined);
        throw e;
      }
    },

    // ------------------------------------------------- "Archive all Done" (bulk)
    openArchiveAllDoneConfirm(): void {
      this.archiveAllDoneConfirmOpen = true;
    },

    cancelArchiveAllDoneConfirm(): void {
      this.archiveAllDoneConfirmOpen = false;
    },

    /** AC2: sequentially archive EVERY done task of the project (the full
     *  board.tasks.done list — the cap only limits RENDERING, so hidden older
     *  cards are archived too). Each row is a version-guarded PATCH; a 409
     *  re-reads that ONE task and retries it once. Optimistic removal happens
     *  per success; a final silent reload settles counts/blocked state. */
    async confirmArchiveAllDone(): Promise<void> {
      if (this.archiveAllDoneBusy || !this.board) return;
      const done = [...(this.board.tasks.done ?? [])];
      this.archiveAllDoneConfirmOpen = false;
      if (done.length === 0) return;
      this.archiveAllDoneBusy = true;
      let ok = 0;
      let failed = 0;
      try {
        for (const t of done) {
          const removeLocal = (): void => {
            if (!this.board) return;
            this.board.tasks.done = this.board.tasks.done.filter((x) => x.id !== t.id);
            if ((this.board.counts.done ?? 0) > 0) this.board.counts.done--;
          };
          try {
            await api.updateTask(t.id, { actor: this.actor(), version: t.version, archived: true });
            ok++;
            removeLocal();
          } catch (e) {
            if (ApiError.isVersionConflict(e)) {
              // Stale version: re-read board state for THIS task and retry once.
              try {
                const fresh = await api.getTask(t.id, { commentLimit: 1 });
                if (!fresh.archived) {
                  await api.updateTask(t.id, { actor: this.actor(), version: fresh.version, archived: true });
                }
                ok++;
                removeLocal();
              } catch {
                failed++;
              }
            } else {
              failed++;
            }
          }
        }
      } finally {
        this.archiveAllDoneBusy = false;
      }
      await this.loadBoard(true).catch(() => undefined);
      if (this.showArchived) await this.refreshArchivedView();
      const n = ok === 1 ? "task" : "tasks";
      if (failed === 0) this.toast(`Archived ${ok} done ${n}`);
      else this.toast(`Archived ${ok} done ${n} · ${failed} failed`, "error");
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

    isTaskSelected(id: string): boolean {
      return this.selectedTaskIds.has(id);
    },

    selectedCount(): number {
      return this.selectedTaskIds.size;
    },

    /** AC1: checkbox toggle. Re-anchors range selection to this card so the
     *  next Shift+Click spans from it. Pure state — no network. */
    toggleSelectTask(id: string): void {
      const next = new Set(this.selectedTaskIds);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      this.selectedTaskIds = next;
      this._selectionAnchorId = id;
    },

    /** AC1: Shift+Click range select within ONE column — from the anchored
     *  card to the clicked card, inclusive, over that column's currently
     *  RENDERED (post-filter, post-sort, post-cap) order. Additive: cards
     *  selected in other columns stay selected. Fallbacks keep it predictable:
     *  no anchor yet → plain toggle of the clicked card (which sets the
     *  anchor); anchor not visible in this column → plain toggle + re-anchor. */
    rangeSelectInColumn(id: string): void {
      const anchor = this._selectionAnchorId;
      if (!anchor) {
        this.toggleSelectTask(id);
        return;
      }
      const col = this.columns().find((c) => c.tasks.some((t) => t.id === id));
      const anchorIdx = col ? col.tasks.findIndex((t) => t.id === anchor) : -1;
      if (!col || anchorIdx < 0) {
        // The anchor isn't in this column's visible order — a cross-column
        // range is undefined, so degrade to a single-card toggle here.
        this.toggleSelectTask(id);
        return;
      }
      const ids = col.tasks.map((t) => t.id);
      const from = Math.min(anchorIdx, ids.indexOf(id));
      const to = Math.max(anchorIdx, ids.indexOf(id));
      const next = new Set(this.selectedTaskIds);
      for (let i = from; i <= to; i++) next.add(ids[i]);
      this.selectedTaskIds = next;
      // Anchor intentionally NOT moved: the next shift-click still spans from it.
    },

    /** AC2: select every card currently RENDERED across all three columns —
     *  exactly the lists columns() hands to the template, so active filters
     *  AND the Done-cap expansion state are honored by construction (beyond-
     *  cap older done cards stay out until expanded; search bypasses the cap,
     *  so a searched set is fully selectable). */
    selectAllVisible(): void {
      if (!this.board) return;
      const next = new Set(this.selectedTaskIds);
      for (const col of this.columns()) for (const t of col.tasks) next.add(t.id);
      this.selectedTaskIds = next;
    },

    /** AC4 + bar "Clear": drop the whole selection and its transient UI. */
    clearSelection(): void {
      if (this.selectedTaskIds.size === 0 && !this._selectionAnchorId) return;
      this.selectedTaskIds = new Set<string>();
      this._selectionAnchorId = null;
      this.batchMoveMenuOpen = false;
      this.batchPriorityMenuOpen = false;
      this.batchAssignMenuOpen = false;
    },

    _closeBatchMenus(except?: "move" | "priority" | "assign"): void {
      if (except !== "move") this.batchMoveMenuOpen = false;
      if (except !== "priority") this.batchPriorityMenuOpen = false;
      if (except !== "assign") this.batchAssignMenuOpen = false;
    },

    toggleBatchMoveMenu(): void {
      this.batchMoveMenuOpen = !this.batchMoveMenuOpen;
      if (this.batchMoveMenuOpen) this._closeBatchMenus("move");
    },

    toggleBatchPriorityMenu(): void {
      this.batchPriorityMenuOpen = !this.batchPriorityMenuOpen;
      if (this.batchPriorityMenuOpen) this._closeBatchMenus("priority");
    },

    toggleBatchAssignMenu(): void {
      this.batchAssignMenuOpen = !this.batchAssignMenuOpen;
      if (this.batchAssignMenuOpen) this._closeBatchMenus("assign");
    },

    /** Distinct assignee names present on the current project's tasks (the
     *  same source as availableAssignees()), plus the owner-droplist "Clear"
     *  affordance handled in the template. */
    batchAssigneeOptions(): string[] {
      return this.availableAssignees();
    },

    /** Owner type for a name: reuse the type already attached to a board row
     *  carrying that assignee (agents stay agents), else default human. */
    _assigneeTypeFor(name: string): "human" | "agent" {
      const b = this.board;
      if (b) {
        for (const s of STATUSES) {
          const row = b.tasks[s]?.find((t) => t.assignee?.name === name);
          if (row?.assignee?.type === "agent") return "agent";
        }
      }
      return "human";
    },

    // ---------------------------- AC3: robust batch execution ---------------
    /** Shared engine for every batch action. Runs per-task, sequentially, each
     *  PATCH version-guarded with the FRESHEST version in board state; a 409
     *  re-reads that ONE task (api.getTask) and retries exactly once — the same
     *  discipline as confirmArchiveAllDone(). Rows are located on board.tasks
     *  (filters only hide cards, they never remove them from the snapshot); a
     *  row no longer in the board (SSE refresh raced us) falls back to a direct
     *  read for its version. Optimistic local mutation per success; ONE final
     *  silent loadBoard settles counts/blocked state. Selection policy: full
     *  success clears it, partial failure keeps ONLY the failed ids selected
     *  so the user can re-run just those. Consolidated summary toast always. */
    async _runBatch(
      patchFor: (row: TaskSummary | null) => Partial<UpdateTaskReq>,
      localApply: ((id: string, row: TaskSummary, result: TaskSummary) => void) | null,
      alreadyApplied?: (fresh: { archived?: boolean }) => boolean,
      /** Builds the success message from the ok-count; the engine appends the
       *  "· N failed" suffix and picks error/info kind itself. */
      toastText?: (ok: number) => string,
    ): Promise<void> {
      const ids = [...this.selectedTaskIds];
      if (ids.length === 0 || this.batchBusy) return;
      this.batchBusy = true;
      this._closeBatchMenus();
      let ok = 0;
      const failed: string[] = [];
      for (const id of ids) {
        let row: TaskSummary | null = null;
        if (this.board) {
          for (const s of STATUSES) {
            const found = this.board.tasks[s]?.find((t) => t.id === id);
            if (found) {
              row = found;
              break;
            }
          }
        }
        try {
          let version = row?.version ?? 0;
          if (!row) version = (await api.getTask(id, { commentLimit: 1 })).version;
          try {
            const result = await api.updateTask(id, {
              actor: this.actor(),
              version,
              ...patchFor(row),
            });
            if (row && localApply) localApply(id, row, result);
            ok++;
          } catch (e) {
            if (!ApiError.isVersionConflict(e)) throw e;
            // Stale version: re-read THIS task and retry once (AC3).
            const fresh = await api.getTask(id, { commentLimit: 1 });
            if (alreadyApplied && alreadyApplied(fresh)) {
              // Someone else already made this exact change — treat as success.
              if (row && localApply) localApply(id, row, row);
              ok++;
              continue;
            }
            const result = await api.updateTask(id, {
              actor: this.actor(),
              version: fresh.version,
              ...patchFor(row),
            });
            if (row && localApply) localApply(id, row, result);
            ok++;
          }
        } catch {
          failed.push(id);
        }
      }
      this.batchBusy = false;
      if (ok > 0) await this.loadBoard(true).catch(() => undefined);
      if (failed.length === 0) {
        this.selectedTaskIds = new Set<string>();
      } else {
        this.selectedTaskIds = new Set(failed); // keep only the failures selected
      }
      this._selectionAnchorId = null;
      const suffix = failed.length > 0 ? ` · ${failed.length} failed` : "";
      const base = toastText ? toastText(ok) : `${ok} ${ok === 1 ? "task" : "tasks"}`;
      this.toast(base + suffix, failed.length > 0 ? "error" : "info");
    },

    // ---------------------------- batch actions (AC2/AC3) --------------------
    /** Move every selected card to a status. Rows already in that status are
     *  still patched (server no-op) so the count stays honest; blocked tasks
     *  crossing into Done fail per-row and surface in the summary, matching
     *  the single-move flow's no-override-blockers policy. */
    async batchMoveTo(status: Status): Promise<void> {
      await this._runBatch(
        () => ({ status }),
        (id, row, result) => {
          if (!this.board) return;
          const from = row.status;
          if (from !== status) {
            this.board.tasks[from] = this.board.tasks[from].filter((t) => t.id !== id);
            if ((this.board.counts[from] ?? 0) > 0) this.board.counts[from]--;
            this.board.tasks[status] = [...this.board.tasks[status], { ...row, status }];
            this.board.counts[status] = (this.board.counts[status] ?? 0) + 1;
          }
          const current = this.board.tasks[status].find((t) => t.id === id);
          if (current) Object.assign(current, result);
        },
        undefined,
        (ok) => `Moved ${ok} ${ok === 1 ? "task" : "tasks"} to ${STATUS_LABEL[status]}`,
      );
    },

    async batchSetPriority(priority: number): Promise<void> {
      await this._runBatch(
        () => ({ priority }),
        (id, row, result) => {
          if (!this.board || !row.status) return;
          const bucket = this.board.tasks[row.status];
          const idx = bucket.findIndex((t) => t.id === id);
          if (idx >= 0) bucket[idx] = { ...bucket[idx], ...result };
        },
        undefined,
        (ok) => `Updated ${ok} ${ok === 1 ? "task" : "tasks"} to priority P${priority}`,
      );
    },

    /** Assign a durable owner to every selected card; name=null clears it.
     *  The dropdown offers the project's existing assignee names (AC2) with a
     *  Clear option; when none exist the bar falls back to free text. */
    async batchAssignOwner(name: string | null): Promise<void> {
      const trimmed = name === null ? "" : name.trim();
      if (name !== null && trimmed === "") return;
      const type = this._assigneeTypeFor(trimmed);
      await this._runBatch(
        () => (trimmed ? { assignee: { name: trimmed, type } } : { clear_assignee: true }),
        (id, row, result) => {
          if (!this.board || !row.status) return;
          const bucket = this.board.tasks[row.status];
          const idx = bucket.findIndex((t) => t.id === id);
          if (idx >= 0) bucket[idx] = { ...bucket[idx], assignee: result.assignee, version: result.version };
        },
        undefined,
        (ok) =>
          trimmed
            ? `Assigned ${ok} ${ok === 1 ? "task" : "tasks"} to ${trimmed}`
            : `Cleared owner on ${ok} ${ok === 1 ? "task" : "tasks"}`,
      );
    },

    /** Archive every selected card. Mirrors confirmArchiveAllDone's loop:
     *  per-row version-guarded PATCH, one re-read + retry on 409 (skipping the
     *  re-patch when someone already archived it), optimistic removal per
     *  success, one settling reload at the end. */
    async batchArchiveSelected(): Promise<void> {
      await this._runBatch(
        () => ({ archived: true }),
        (id, row, result) => {
          if (!this.board) return;
          const status = row.status || result.status; // status is stable across an archive
          this.board.tasks[status] = this.board.tasks[status].filter((t) => t.id !== id);
          if ((this.board.counts[status] ?? 0) > 0) this.board.counts[status]--;
        },
        (fresh) => fresh.archived === true,
        (ok) => `Archived ${ok} selected ${ok === 1 ? "task" : "tasks"}`,
      );
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
      this.showArchived = false;
      this.archivedBoardTasks = null;
      this.archiveAllDoneConfirmOpen = false;
      this._archivedSeq.next();
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
      this.connectSse();
      void this.loadBoard();
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
      });
      return this._sse;
    },

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
    /** Focus session for the drawer (trap + restore). Survives in-drawer task
     *  switches so focus still returns to the original board trigger. */
    detailFocus: null as DialogFocus | null,
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

    async openTask(id: string): Promise<void> {
      // Unsaved notes belong to the task being left — ask before dropping them
      // (AC 3). Re-opening the SAME task is just a refresh: its in-progress
      // draft is intentionally preserved (see applyDetail).
      if (this.notesDirty && this.detailOpen && this.detailId !== id) {
        this.promptUnsavedNotes({ kind: "open", taskId: id });
        return;
      }
      // A dirty task-edit draft belongs to the task being left — same rule as
      // unsaved notes above. (If both drafts are dirty, the user confirms each
      // explicitly: the notes prompt first, then this one.)
      if (this.editDraftDirty() && this.detailId !== id) {
        if (!this.discardEditDraftIfConfirmed()) return;
      }
      // Defensive: a scratchpad that belongs to another task is discarded up
      // front so it can never surface inside the drawer about to open (AC 2).
      if (this.notesDraftOwner !== null && this.notesDraftOwner !== id) {
        this.resetNotesDraft();
      }
      // A task switch while the full page is open keeps the URL pointing at the
      // NEW task (the deep link stays truthful and shareable) without growing
      // history — replaceState, never push.
      if (this.fullPageOpen && typeof window !== "undefined" && this.detailId !== id) {
        window.history.replaceState(null, "", taskRoutePath(id));
      }
      // Mobile fallback (task 01M47MWSTTPS14M6XB1A8HFPV1): below md (768px) a
      // card click opens the FULL-PAGE view via the same /tasks/<id> route the
      // "Open full page" button pushes, so phones get deep-link + browser-back
      // semantics for free. At ≥md the wide centered modal is the default.
      if (!this.fullPageOpen && isMobileDetailViewport()) {
        this.openFullPage(id);
      }
      // In-drawer switch (discard-&-switch): keep the existing session so focus
      // restoration still targets the element that originally opened the drawer.
      const switching = this.detailOpen && this.detailFocus !== null;
      this.detailOpen = true;
      this.detailId = id;
      this.detail = null;
      // Never carry a previous task's error state into the one being opened.
      this.detailLoadError = null;
      this.statusMenuOpen = false;
      this.ownerPopoverOpen = false;
      this.priorityPopoverOpen = false;
      this.duePopoverOpen = false;
      this.relationForm.targetId = "";
      this.relationForm.search = "";
      this.relationForm.open = false;
      this.relationForm.activeIndex = -1;
      this.editingTask = false;
      // Write/Preview toggles never outlive an open: every drawer opens in
      // Write mode for both markdown fields (AC 1).
      this.descShowPreview = false;
      // Reset-then-restore: the comment form starts clean so one task's unsaved
      // comment can never leak into the next, then a stored draft for THIS id is
      // re-applied (drafts live in localStorage, not state — they survive
      // navigation/close/reload by design, AC 2).
      this.restoreCommentDraftForTask(id);
      this.totalComments = 0;
      this.loadingOlderComments = false;
      this.initTaskOpenCodeConfig();
      if (!switching) {
        // Defensive: a stale session must never survive to capture a new trigger.
        this.closeDialog(this.detailFocus);
        this.detailFocus = null;
        // The × button is the safe initial focus (not "Start Task in OpenCode").
        // autoFocus off: content arrives via fetch, so focus moves explicitly
        // once (below) instead of racing the render.
        this.detailFocus = this.openDialog('cb-detail-drawer', '[data-testid="detail-close-btn"]', false);
      }
      const applied = await this.refreshDetail(false);
      void this.initComments(id);
      if (!applied && this.detailOpen) {
        // The fetch for this task failed (or the drawer closed mid-flight): the
        // drawer shows its error state. Move focus to Retry as soon as it is
        // rendered — the block mounts via x-if on Alpine's own schedule, so
        // _focusWhenVisible polls frames instead of focusing blind.
        const retryBtn =
          typeof document === "undefined"
            ? null
            : document.querySelector<HTMLElement>('[data-testid="detail-retry-btn"]');
        this._focusWhenVisible(retryBtn);
      } else {
        // Content arrives via fetch; only move focus once the dialog actually has
        // focusables (no-op if the user already tabbed somewhere inside).
        this.detailFocus?.focusInitial();
      }
    },

    /** Detail-close entry point for the backdrop, × buttons and Escape —
     *  valid from BOTH skins (centered modal and full page): asks first when
     *  notes have unsaved edits (AC 3) or a task-edit draft differs from the
     *  loaded values (task 01M3MMZ188XJBGKFSWBGESK79D). */
    requestCloseDetail(): void {
      if (this.notesDirty) {
        this.promptUnsavedNotes({ kind: "close" });
        return;
      }
      // A dirty task-edit draft is guarded by the same no-silent-loss rule.
      if (!this.discardEditDraftIfConfirmed()) return;
      this.closeDetail();
    },

    closeDetail(): void {
      const wasFullPage = this.fullPageOpen;
      const pushedEntry = this.fullPagePushed;
      this.statusMenuOpen = false;
      this.ownerPopoverOpen = false;
      this.priorityPopoverOpen = false;
      this.duePopoverOpen = false;
      this.relationForm.targetId = "";
      this.relationForm.search = "";
      this.relationForm.open = false;
      this.relationForm.activeIndex = -1;
      this.editingTask = false;
      // The drawer's error state never outlives its drawer (same rule as the
      // notes scratchpad). openTask() also clears it, so a re-open starts clean.
      this.detailLoadError = null;
      this.editingAcSeq = 0;
      this.editingAcText = "";
      this.newAcText = "";
      this.totalComments = 0;
      this.loadingOlderComments = false;
      this.startingOpenCode = false;
      this.showTaskOpenCodeConfig = false;
      this.copiedArtifactId = null;
      if (this.copiedArtifactTimer) {
        clearTimeout(this.copiedArtifactTimer);
        this.copiedArtifactTimer = null;
      }
      // AC 1: the notes scratchpad never outlives its drawer — otherwise a
      // dirty draft would leak into whatever task is opened next.
      this.resetNotesDraft();
      this.unsavedNotesAction = null;
      // A confirm session must not outlive its drawer either (defensive).
      this.closeDialog(this.confirmNotesFocus);
      this.confirmNotesFocus = null;
      // Restore focus to the board trigger that opened the drawer.
      this.closeDialog(this.detailFocus);
      this.detailFocus = null;
      this.detailOpen = false;
      this.fullPageOpen = false;
      this.fullPagePushed = false;
      // History sync: a FULL-PAGE close must leave the URL honest. An entry we
      // pushed is popped — the resulting popstate lands on "/" and is a no-op
      // because state is already closed. A deep-linked open has NO board entry
      // behind it, so popping would navigate OUT of the app: replace instead.
      // (When closing from a popstate-driven back, the pathname is already "/"
      // and neither branch fires — idempotent.)
      if (
        wasFullPage &&
        typeof window !== "undefined" &&
        taskRouteIdFromPath(window.location.pathname) !== null
      ) {
        if (pushedEntry) window.history.back();
        else window.history.replaceState(null, "", this.boardUrl());
      }
      // Drawer closed → the tab title reverts to the board view. (Recomputes:
      // detailOpen is now false, so any stale `detail` object can't leak its
      // title into the board state — WCAG 2.4.2.)
      this.updateDocumentTitle();
    },

    /** Switch the open task's detail to the FULL-PAGE view. Pushes /tasks/<id>
     *  so browser back returns to the board; a URL that is already on a task
     *  route (deep link) is adopted as-is instead of piling another entry on
     *  top. `id` defaults to the loaded detail's id — openTask() passes it
     *  explicitly for mobile opens, where the fetch has not landed yet. */
    openFullPage(id?: string): void {
      const taskId = id ?? this.detail?.id;
      if (!taskId || typeof window === "undefined") return;
      this.fullPageOpen = true;
      if (taskRouteIdFromPath(window.location.pathname) !== taskId) {
        window.history.pushState(null, "", taskRoutePath(taskId));
        this.fullPagePushed = true;
      } else {
        this.fullPagePushed = false; // deep link — no entry of ours to pop later
      }
    },

    /** Reconcile app state with the URL after a browser navigation (back /
     *  forward). The pathname is the source of truth for the full-page view:
     *  `/tasks/<id>` opens that task as a full page (adopting its project for
     *  board/SSE context); anything else closes an open full page. Never
     *  writes history here — the navigation already happened. */
    onPopState(): void {
      if (typeof window === "undefined") return;
      const id = taskRouteIdFromPath(window.location.pathname);
      if (id) {
        if (!this.detailOpen || this.detailId !== id) {
          void (async (): Promise<void> => {
            try {
              const t = await api.getTask(id, { commentLimit: 50 });
              if (this.selectedProjectId !== t.project_id) {
                this.selectedProjectId = t.project_id;
                try {
                  safeStorage.setItem(LS_PROJECT, t.project_id);
                } catch { /* private mode */ }
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
        }
      } else {
        if (this.fullPageOpen) {
          this.closeDetail();
        }
        const oldProject = this.selectedProjectId;
        const search = window.location.search || "";
        const params = parseBoardQueryParams(search);
        this.applyQueryParams(params, true);
        if (this.selectedProjectId && this.selectedProjectId !== oldProject) {
          void this.loadBoard();
        }
      }
    },

    /** Deep link on first load: /tasks/<id> in the initial URL. Fetches the
     *  task, adopts its project as the board context (so SSE/poll/relations
     *  targets are correct), and opens it as a full page. A failed fetch resets
     *  the URL to "/" so a dead deep link can't strand the history state, and
     *  still lands the user on a usable board. */
    async openDeepLinkedTask(id: string): Promise<void> {
      let t: TaskDetail;
      try {
        t = await api.getTask(id, { commentLimit: 50 });
      } catch (e) {
        if (ApiError.isUnauthorized(e)) this.openAuthModal();
        else this.toast(errMsg(e), "error");
        if (typeof window !== "undefined") window.history.replaceState(null, "", this.boardUrl());
        if (this.selectedProjectId) {
          await this.loadBoard();
          this.connectSse();
        } else {
          this.showNewProject = true;
        }
        return;
      }
      if (this.selectedProjectId !== t.project_id) {
        this.selectedProjectId = t.project_id;
        try {
          safeStorage.setItem(LS_PROJECT, t.project_id);
        } catch { /* private mode */ }
      }
      await this.loadBoard();
      this.connectSse();
      // The URL was given to us by the browser — we did not push it.
      this.fullPageOpen = true;
      this.fullPagePushed = false;
      await this.openTask(id);
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

    initTaskOpenCodeConfig(): void {
      this.showTaskOpenCodeConfig = false;
      const p = this.projects.find((x) => x.id === this.selectedProjectId);
      const agent = p?.opencode_agent || this.openCodeOptions?.resolved_agent || "build";
      const model = p?.opencode_model || this.openCodeOptions?.resolved_model || "";
      const variant = p?.opencode_variant || this.openCodeOptions?.resolved_variant || "";
      this.taskOpenCodeForm = { agent, model, variant };
      this.taskOpenCodeBaseline = { agent, model, variant };
    },

    toggleOpenCodeConfig(): void {
      this.showTaskOpenCodeConfig = !this.showTaskOpenCodeConfig;
      if (this.showTaskOpenCodeConfig && (!this.openCodeOptions || this.openCodeOptionsProjectId !== this.selectedProjectId)) {
        void this.fetchOpenCodeOptions(this.selectedProjectId).then((opts) => {
          if (opts && !this.taskOpenCodeDirty()) {
            const pNow = this.projects.find((x) => x.id === this.selectedProjectId);
            const a = pNow?.opencode_agent || opts.resolved_agent || "build";
            const m = pNow?.opencode_model || opts.resolved_model || "";
            const v = pNow?.opencode_variant || opts.resolved_variant || "";
            this.taskOpenCodeForm = { agent: a, model: m, variant: v };
            this.taskOpenCodeBaseline = { agent: a, model: m, variant: v };
          }
        });
      }
    },

    taskOpenCodeDirty(): boolean {
      return (
        (this.taskOpenCodeForm.agent ?? "") !== (this.taskOpenCodeBaseline.agent ?? "") ||
        (this.taskOpenCodeForm.model ?? "") !== (this.taskOpenCodeBaseline.model ?? "") ||
        (this.taskOpenCodeForm.variant ?? "") !== (this.taskOpenCodeBaseline.variant ?? "")
      );
    },

    activeOpenCodeSession(d?: TaskDetail | null): StartOpenCodeResponse | null {
      const target = d === undefined ? this.detail : d;
      if (!target) return null;
      const cached = this.openCodeSessions[target.id];
      if (cached && cached.session_url) return cached;
      const artifacts = target.artifacts;
      if (Array.isArray(artifacts)) {
        for (let i = artifacts.length - 1; i >= 0; i--) {
          const a = artifacts[i];
          const ref = a?.ref?.trim();
          if (a?.summary === "OpenCode autonomous session" && ref) {
            const idx = ref.lastIndexOf("/session/");
            const tail = idx !== -1 ? ref.slice(idx + "/session/".length).replace(/[/?#].*$/, "") : "";
            const session_id = tail || ref;
            return { session_id, session_url: ref };
          }
        }
      }
      return null;
    },

    async startTaskInOpenCode(customInput?: StartOpenCodeInput): Promise<void> {
      const d = this.detail;
      if (!d || this.startingOpenCode) return;
      this.startingOpenCode = true;
      try {
        let input: StartOpenCodeInput | undefined = customInput;
        if (!input && (this.taskOpenCodeDirty() || this.showTaskOpenCodeConfig)) {
          const p: StartOpenCodeInput = {};
          if (this.taskOpenCodeForm.agent?.trim()) p.agent = this.taskOpenCodeForm.agent.trim();
          if (this.taskOpenCodeForm.model?.trim()) p.model = this.taskOpenCodeForm.model.trim();
          const variantTrim = this.taskOpenCodeForm.variant?.trim() ?? "";
          if (variantTrim || (this.taskOpenCodeBaseline.variant ?? "").trim()) {
            p.variant = variantTrim;
          }
          input = p;
        }
        const res = await api.startOpenCode(d.id, input);
        if (res && res.session_url) {
          this.openCodeSessions = { ...this.openCodeSessions, [d.id]: res };
        }
        this.toast(
          res?.session_id ? `OpenCode session started (${res.session_id})` : "OpenCode session started",
          "info",
          res?.session_url ? { label: "Open Session", href: res.session_url } : undefined,
        );
        this.showTaskOpenCodeConfig = false;
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.startingOpenCode = false;
      }
    },

    // ------------------------------------------------------------------ copy id
    /** Copy the FULL task ID to the clipboard from the truncated header badge.
     *  Prefers the Async Clipboard API and falls back to execCommand for plain-
     *  http deployments where it is unavailable (see copyTextToClipboard). */
    async copyTaskId(id: string): Promise<void> {
      if (!id) return;
      const copied = await copyTextToClipboard(id);
      this.toast(copied ? "Copied task ID to clipboard" : "Couldn't copy the task ID", copied ? "info" : "error");
    },

    /** Copy an artifact ref (file path or URL) to clipboard with brief visual confirmation.
     *  Behavior owned by modules/artifacts.ts. */
    async copyArtifactRef(ref: string, id?: string): Promise<void> {
      return await this._artifactsModule().copyArtifactRef(ref, id);
    },

    /** Fetch GET /tasks/{id} (full resume payload, comment tail 50) and apply it.
     *  An in-progress notes edit (notesDirty) is NEVER clobbered by a refresh.
     *  Ownership-guarded (task 01M3MMY78F1TRP6MKDRT0SKQWB): by the time the
     *  fetch resolves, the drawer may have been closed or switched to another
     *  task — a payload for a no-longer-active id is discarded (returns false)
     *  instead of overwriting the active task's detail. */
    async refreshDetail(silent = false, idToFetch?: string): Promise<boolean> {
      const id = idToFetch ?? this.detailId ?? this.detail?.id;
      if (!id) return false;
      try {
        const t = await api.getTask(id, { commentLimit: 50 });
        if (!canApplyDetail(t.id, this.detailId, this.detailOpen)) {
          return false; // drawer closed or switched since the fetch started — drop stale payload
        }
        this.applyDetail(t);
        return true;
      } catch (e) {
        if (!silent) this.toast(errMsg(e), "error");
        // Drawer error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED): a failed fetch
        // for the task CURRENTLY open becomes a visible message in the drawer —
        // previously it left only a bare "Loading task…" with no way out. The
        // ownership gate mirrors canApplyDetail(): a failure for a task we no
        // longer have open must not touch the drawer's state. Silent SSE/poll
        // refreshes flow through here too; they cannot clobber a visible error
        // (they only refresh its message), and a successful one clears it via
        // applyDetail(). No automatic retry loop — re-fetching is user-driven
        // via retryDetailLoad() (the Retry button).
        if (this.detailOpen && this.detailId === id) {
          this.detailLoadError =
            e instanceof ApiError && e.status === 404
              ? "This task was deleted or no longer exists."
              : errMsg(e);
        }
        return false;
      }
    },

    /** Re-fetch the currently open task — the Retry button in the drawer's
     *  error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED). Non-silent, so a second
     *  failure toasts as well. refreshDetail()'s ownership guard keeps a stale
     *  result from landing on a task the user switched away mid-flight. */
    async retryDetailLoad(): Promise<void> {
      if (!this.detailOpen || this.detailRetrying) return;
      const id = this.detailId;
      if (!id) return;
      this.detailRetrying = true;
      try {
        await this.refreshDetail(false, id);
      } finally {
        this.detailRetrying = false;
      }
    },

    applyDetail(t: TaskDetail): void {
      // Ownership guard (task 01M3MMY78F1TRP6MKDRT0SKQWB), second layer under
      // refreshDetail's: covers direct call sites too (409-retry re-reads).
      // Discarding BEFORE any state write also guarantees a stale/foreign
      // payload can never reset or clobber an in-progress notes draft.
      if (!canApplyDetail(t.id, this.detailId, this.detailOpen)) {
        return;
      }
      // A payload that actually lands means the drawer recovered: clear any
      // pending fetch-error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED). Covers
      // every apply path — refreshDetail success and the 409-rebase re-reads.
      this.detailLoadError = null;
      const existingComments = (this.detail && this.detail.id === t.id) ? this.detail.comments : [];
      const map = new Map<string, CommentRow>();
      for (const c of existingComments) {
        map.set(c.id, c);
      }
      for (const c of t.comments) {
        map.set(c.id, c);
      }
      const sorted = Array.from(map.values()).sort((a, b) => {
        const ta = Date.parse(a.created_at);
        const tb = Date.parse(b.created_at);
        if (ta !== tb) return ta - tb;
        return a.id.localeCompare(b.id);
      });
      t.comments = sorted;
      this.detail = t;
      // Task detail loaded → the tab title names it (WCAG 2.4.2). Covers every
      // load path: openTask, deep link, popstate and SSE re-fetches (a remote
      // rename while the drawer is open re-titles the tab too).
      this.updateDocumentTitle();
      if (t.comments.length > this.totalComments) {
        this.totalComments = t.comments.length;
      }
      this.assigneeInputName = t.assignee?.name ?? "";
      this.assigneeInputType = (t.assignee?.type as "human" | "agent") || "human";
      // Learn the blocked state for card badges — owned by modules/board.ts
      // (scope guard: only while the task's project IS the selected one; board
      // loads own the cache scope, see selectProject).
      this._boardModule().learnBlockedFromDetail(t.project_id, t.id, t.blocked, t.blockers);
      // Never clobber an in-progress edit of THIS task; a scratchpad that
      // belongs to a DIFFERENT task is discarded so drafts cannot leak across
      // tasks (AC 2).
      if (!(this.notesDirty && this.notesDraftOwner === t.id)) {
        const savedNotes = safeStorage.getItem(notesDraftKey(t.id));
        if (savedNotes !== null && savedNotes.trim() !== "") {
          this.notesDraft = savedNotes;
          this.notesDirty = true;
        } else {
          this.notesDraft = t.notes;
          this.notesDirty = false;
        }
        this.notesDraftOwner = t.id;
        this.notesShowPreview = false;
      }
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

    /** Template helper: may the CURRENTLY-DRAGGED card be dropped on `status`?
     *  (False while nothing legal is known — grey-out styling for illegal
     *  columns reads this. The transition map itself lives in moves.ts.) */
    dragTargetLegal(status: Status): boolean {
      return dropAllowed(this.draggedFromStatus, status);
    },

    onDragStart(e: DragEvent, task: TaskSummary, fromStatus: Status): void {
      // The archived view is read-only for positioning: its cards live in a
      // separate array the optimistic move code doesn't own, so drag/swipe is
      // disabled there (Unarchive is the only action available).
      if (this.showArchived) {
        e.preventDefault();
        return;
      }
      // Only the card's OWN in-flight move blocks it (per-task lock); tell the
      // user instead of silently refusing the drag.
      if (this.isTaskBusy(task.id)) {
        e.preventDefault();
        this.toast("That task is still moving — one moment.", "info");
        return;
      }
      this.isDragging = true;
      this.draggedTaskId = task.id;
      this.draggedFromStatus = fromStatus;
      if (e.dataTransfer) {
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", task.id);
      }
    },

    onDragEnd(): void {
      this.draggedTaskId = null;
      this.draggedFromStatus = null;
      this.dragOverStatus = null;
      window.setTimeout(() => {
        this.isDragging = false;
      }, 50);
    },

    onDragOver(e: DragEvent, status: Status): void {
      if (!this.draggedTaskId || !this.draggedFromStatus) return;
      // Illegal transition → do NOT preventDefault: the browser then shows a
      // no-drop cursor and never fires @drop, so illegal columns can't take
      // the card (the grey-out styling below tells the user why).
      if (!canMove(this.draggedFromStatus, status)) return;
      e.preventDefault();
      if (e.dataTransfer) {
        e.dataTransfer.dropEffect = "move";
      }
      if (this.dragOverStatus !== status) {
        this.dragOverStatus = status;
      }
    },

    onDragLeave(e: DragEvent, status: Status): void {
      const currentTarget = e.currentTarget as HTMLElement | null;
      const relatedTarget = e.relatedTarget as Node | null;
      if (currentTarget && relatedTarget && currentTarget.contains(relatedTarget)) {
        return;
      }
      if (this.dragOverStatus === status) {
        this.dragOverStatus = null;
      }
    },

    async onDrop(e: DragEvent, targetStatus: Status): Promise<void> {
      e.preventDefault();
      const taskId = this.draggedTaskId || e.dataTransfer?.getData("text/plain");
      const fromStatus = this.draggedFromStatus;
      this.dragOverStatus = null;
      this.draggedTaskId = null;
      this.draggedFromStatus = null;
      window.setTimeout(() => {
        this.isDragging = false;
      }, 50);

      if (!taskId || !fromStatus || fromStatus === targetStatus) {
        return;
      }
      // Belt-and-braces with the dragover guard: a drop on an illegal target
      // is ignored (with feedback), never sent to the server.
      if (!canMove(fromStatus, targetStatus)) {
        this.toast(illegalMoveMessage(fromStatus, targetStatus), "info");
        return;
      }
      await this.moveTaskStatus(taskId, targetStatus);
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
    // behaviour is shared.
    showMobileMenu: false,
    /** Mobile breakpoint flag mirroring Tailwind's `md:` (min-width 768px).
     *  The hamburger's slide-down panel renders x-if'd on this flag so the two
     *  mobile surfaces (panel vs. header strip) are exclusive in the DOM —
     *  Playwright strict-mode testids require each id to match exactly once at
     *  any viewport width. Updated from a matchMedia listener in _runBoot(). */
    isMobile: false,
    cardMenuTaskId: null as string | null,
    swipeState: {
      id: null as string | null,
      startX: 0,
      startY: 0,
      dx: 0,
      fromStatus: null as Status | null,
      active: false,
    },

    swiping(id: string): boolean {
      return this.swipeState.active && this.swipeState.id === id;
    },

    swipeDirection(id: string): "right" | "left" {
      return swipeDirFor(this.swiping(id), this.swipeState.dx);
    },

    swipeTargetLabel(id: string): string {
      return swipeLabelFor(this.swipeState.fromStatus, this.swipeDirection(id));
    },

    onCardTouchStart(e: TouchEvent, task: TaskSummary): void {
      // Archived view disables swipe-to-move (see onDragStart).
      if (this.showArchived) return;
      if (e.touches.length !== 1) return;
      const t = e.touches[0];
      this.swipeState = {
        id: task.id,
        startX: t.clientX,
        startY: t.clientY,
        dx: 0,
        fromStatus: task.status,
        active: false,
      };
    },

    onCardTouchMove(e: TouchEvent): void {
      const s = this.swipeState;
      if (!s || !s.id) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - s.startX;
      const dy = t.clientY - s.startY;
      // Activate once movement is clearly horizontal (past slop, vs vertical).
      if (!s.active && swipeActivated(dx, dy)) {
        s.active = true;
      }
      if (s.active) {
        // Clamp only the displayed offset so a long fling can't throw the card
        // far off-screen; slop + commit thresholds keep using raw dx.
        s.dx = Math.max(-MAX_SWIPE_PX, Math.min(MAX_SWIPE_PX, dx));
        // Owning the gesture: stop vertical scroll and suppress the follow-up
        // compatibility click so the card doesn't open its detail drawer.
        e.preventDefault();
      }
    },

    onCardTouchEnd(e: TouchEvent, task: TaskSummary): void {
      const s = this.swipeState;
      if (!s || !s.id || s.id !== task.id) return;
      const endX = e.changedTouches[0]?.clientX ?? s.startX + s.dx;
      const dx = endX - s.startX;
      this.swipeState = { id: null, startX: 0, startY: 0, dx: 0, fromStatus: null, active: false };
      if (!s.active || !s.fromStatus) return;
      const dir = committedSwipeDirection(dx);
      const target = dir ? swipeTargetStatus(s.fromStatus, dir) : null;
      if (!target) return;
      // Swipes move exactly one column at a time. The only adjacent pair that
      // the domain transition map rejects is done → doing (left-swipe on a Done
      // card); refuse it with feedback instead of hitting the 400 + rollback.
      if (!canMove(s.fromStatus, target)) {
        this.toast(illegalMoveMessage(s.fromStatus, target), "info");
        return;
      }
      void this.moveTaskStatus(task.id, target);
    },

    onCardTouchCancel(_e: TouchEvent): void {
      // A cancelled gesture (system interrupt, etc.) never commits a move —
      // just snap the card back home via the release transition.
      this.swipeState = { id: null, startX: 0, startY: 0, dx: 0, fromStatus: null, active: false };
    },

    toggleCardMenu(id: string): void {
      this.cardMenuTaskId = this.cardMenuTaskId === id ? null : id;
    },

    async moveCardTo(id: string, status: Status): Promise<void> {
      this.cardMenuTaskId = null;
      await this.moveTaskStatus(id, status);
    },

    /** Optimistically move a task to targetStatus, then persist with PATCH.
     *  Reuses the 409 re-read + re-apply-once logic.
     *  Rolls back the optimistic board move on failure so no orphan/ghost cards remain.
     *
     *  Concurrency (per task, not per board):
     *   - only a duplicate concurrent move of the SAME taskId is rejected — and
     *     with a toast, never a silent drop; every other card stays interactive
     *     while this task's PATCH is in flight (busyTaskIds set);
     *   - every move PATCH runs under withTimeout(MOVE_TIMEOUT_MS), so a stalled
     *     request aborts, rolls back, toasts, and releases the per-task lock —
     *     it can never freeze the card (or the board) until page refresh. */
    async moveTaskStatus(taskId: string, targetStatus: Status, initialVersion?: number): Promise<void> {
      // Locate task in current board columns or open detail
      let currentStatus: Status | null = null;
      let taskSummary: TaskSummary | null = null;
      if (this.board) {
        for (const s of STATUSES) {
          const found = this.board.tasks[s]?.find((t) => t.id === taskId);
          if (found) {
            currentStatus = s;
            taskSummary = found;
            break;
          }
        }
      }
      if (!taskSummary && this.detail && this.detail.id === taskId) {
        taskSummary = this.detail;
        currentStatus = this.detail.status;
      }

      // No-op moves (unknown card, drop onto own column) need no lock at all.
      if (!taskSummary || !currentStatus || currentStatus === targetStatus) {
        return;
      }

      // Per-task in-flight lock: same id already moving → feedback, not silence.
      if (!beginMove(this.busyTaskIds, taskId)) {
        this.toast("That task is still moving — one moment.", "info");
        return;
      }

      const versionToUse = initialVersion ?? taskSummary.version;

      // Move-related PATCH under the timeout guard. A stall becomes a
      // MoveTimedOutError so the catch below can roll back + toast honestly; a
      // real API error passes through untouched (409 handling stays intact).
      const patch = (version: number): Promise<TaskSummary> =>
        withTimeout(MOVE_TIMEOUT_MS, (signal) =>
          api.updateTask(taskId, { actor: this.actor(), version, status: targetStatus }, { signal }),
        ).then((r) => {
          if (r.value !== undefined) return r.value;
          throw r.timedOut ? new MoveTimedOutError() : r.error;
        });

      // 1. Snapshot board state for rollback
      const prevBoardTasks: Record<Status, TaskSummary[]> | null = this.board
        ? {
            todo: [...this.board.tasks.todo],
            doing: [...this.board.tasks.doing],
            done: [...this.board.tasks.done],
          }
        : null;
      const prevBoardCounts = this.board ? { ...this.board.counts } : null;
      const prevDetailStatus = this.detail && this.detail.id === taskId ? this.detail.status : null;

      const rollback = () => {
        if (this.board && prevBoardTasks && prevBoardCounts) {
          this.board.tasks = prevBoardTasks;
          this.board.counts = prevBoardCounts;
        }
        if (this.detail && this.detail.id === taskId && prevDetailStatus !== null) {
          this.detail.status = prevDetailStatus;
        }
      };

      // 2. Optimistic update
      if (this.board) {
        this.board.tasks[currentStatus] = this.board.tasks[currentStatus].filter((t) => t.id !== taskId);
        const movedTask: TaskSummary = { ...taskSummary, status: targetStatus };
        this.board.tasks[targetStatus] = [...this.board.tasks[targetStatus], movedTask];
        if (this.board.counts[currentStatus] > 0) {
          this.board.counts[currentStatus]--;
        }
        this.board.counts[targetStatus]++;
      }

      if (this.detail && this.detail.id === taskId) {
        this.detail.status = targetStatus;
      }

      try {
        const updated = await patch(versionToUse);
        if (this.board) {
          const inBoard = this.board.tasks[targetStatus]?.find((t) => t.id === taskId);
          if (inBoard) {
            Object.assign(inBoard, updated);
          }
        }
        await Promise.all([
          this.detail && this.detail.id === taskId ? this.refreshDetail(true) : Promise.resolve(),
          this.loadBoard(true),
        ]);
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          try {
            const fresh = await api.getTask(taskId, { commentLimit: 50 });
            if (this.detail && this.detail.id === taskId) {
              this.applyDetail(fresh);
            }
            if (fresh.status !== targetStatus) {
              // Retry under the same timeout guard — a stall here must release
              // the per-task lock too (handled by the e2 branch + finally).
              await patch(fresh.version);
            }
            await Promise.all([
              this.detail && this.detail.id === taskId ? this.refreshDetail(true) : Promise.resolve(),
              this.loadBoard(true),
            ]);
            this.toast("Changed elsewhere — retried with the fresh version.", "info");
          } catch (e2) {
            rollback();
            await Promise.all([
              this.detail && this.detail.id === taskId ? this.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
              this.loadBoard(true).catch(() => undefined),
            ]);
            if (ApiError.isVersionConflict(e2)) {
              this.toast("Changed elsewhere — refreshed (your move was not applied).", "info");
            } else if (e2 instanceof MoveTimedOutError) {
              // Server answered the first PATCH with a conflict but never
              // completed the retry — state may be half-applied, so say "may".
              this.toast("Move timed out — your change may not have saved. Please try again.", "error");
            } else {
              this.toast(friendlyTransitionError(e2) ?? errMsg(e2), "error");
            }
          }
        } else if (e instanceof MoveTimedOutError) {
          rollback();
          await this.loadBoard(true).catch(() => undefined);
          this.toast("Move timed out — your change was not saved. Please try again.", "error");
        } else {
          rollback();
          await this.loadBoard(true).catch(() => undefined);
          // Server-side rejections (e.g. another agent moved the card mid-drag)
          // surface with friendly wording for known error shapes, raw text otherwise.
          this.toast(friendlyTransitionError(e) ?? errMsg(e), "error");
        }
      } finally {
        endMove(this.busyTaskIds, taskId);
      }
    },

    // -------------------------------------------------------- operational bar
    statusMenuOpen: false,
    ownerPopoverOpen: false,

    toggleStatusMenu(): void {
      this.statusMenuOpen = !this.statusMenuOpen;
      this.ownerPopoverOpen = false;
      this.priorityPopoverOpen = false;
      this.duePopoverOpen = false;
    },

    toggleOwnerPopover(): void {
      this.ownerPopoverOpen = !this.ownerPopoverOpen;
      this.statusMenuOpen = false;
      this.priorityPopoverOpen = false;
      this.duePopoverOpen = false;
      if (this.ownerPopoverOpen && this.detail) {
        this.assigneeInputName = this.detail.assignee?.name ?? "";
        this.assigneeInputType = (this.detail.assignee?.type as "human" | "agent") || "human";
      }
    },

    /** Version-guarded status move from detail drawer. On 409 version_conflict:
     *  re-read the task, re-apply the SAME intent once with the fresh version;
     *  a second conflict just refreshes and says so (per docs/API.md error contract). */
    async moveStatus(status: Status): Promise<void> {
      this.statusMenuOpen = false;
      const t = this.detail;
      // Per-task: only the open task's own in-flight move blocks its buttons.
      if (!t || this.isTaskBusy(t.id) || t.status === status) return;
      await this.moveTaskStatus(t.id, status, t.version);
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
    /** Focus session for the confirm modal (stacked above the drawer's). */
    confirmNotesFocus: null as DialogFocus | null,

    /** Clear the notes scratchpad so a closed or abandoned drawer can never
     *  leak its draft into another task (AC 1 + AC 2). */
    resetNotesDraft(): void {
      this.notesDirty = false;
      this.notesDraft = "";
      this.notesDraftOwner = null;
      this.notesShowPreview = false;
    },

    saveNotesDraft(): void {
      const id = this.detail?.id;
      if (!id) return;
      const raw = this.notesDraft ?? "";
      if (raw.trim() !== "") safeStorage.setItem(notesDraftKey(id), raw);
      else safeStorage.removeItem(notesDraftKey(id));
    },

    clearNotesDraft(id?: string): void {
      const targetId = id || this.detail?.id;
      if (!targetId) return;
      safeStorage.removeItem(notesDraftKey(targetId));
    },

    /** Show the unsaved-notes confirm and open its focus session. "Keep
     *  editing" (least destructive) receives initial focus. */
    promptUnsavedNotes(action: { kind: "close" } | { kind: "open"; taskId: string }): void {
      this.unsavedNotesAction = action;
      this.confirmNotesFocus = this.openDialog("cb-notes-confirm", "button.btn-ghost");
    },

    /** "Keep editing" — dismiss the prompt, draft untouched. Focus returns to
     *  whatever had focus inside the drawer when the prompt opened (stacked
     *  session: only THIS layer is closed). */
    cancelUnsavedNotes(): void {
      this.unsavedNotesAction = null;
      this.closeDialog(this.confirmNotesFocus);
      this.confirmNotesFocus = null;
    },

    /** "Discard changes" — drop the draft, then run the deferred close/switch.
     *  The confirm session is closed first; a drawer close afterwards restores
     *  focus to the original board trigger (the switch path re-focuses the new
     *  drawer content via openTask). */
    async confirmUnsavedNotes(): Promise<void> {
      const action = this.unsavedNotesAction;
      this.unsavedNotesAction = null;
      if (this.notesDraftOwner) {
        safeStorage.removeItem(notesDraftKey(this.notesDraftOwner));
      }
      this.resetNotesDraft();
      this.closeDialog(this.confirmNotesFocus);
      this.confirmNotesFocus = null;
      if (action?.kind === "open") await this.openTask(action.taskId);
      else if (action?.kind === "close") this.closeDetail();
    },

    /** PUT /notes replaces the whole blob and carries NO version — a concurrent
     *  write can still 409; then we refresh (keeping the user's draft) and say so. */
    async saveNotes(): Promise<void> {
      const t = this.detail;
      if (!t || this.savingNotes) return;
      // AC 3 guard: a draft may only ever be saved to the task it belongs to.
      // Unreachable through the UI (openTask/closeDetail gate drafts), but if
      // stale state ever appears we must never PUT one task's draft onto another.
      if (this.notesDraftOwner !== null && this.notesDraftOwner !== t.id) {
        this.resetNotesDraft();
        await this.refreshDetail(true);
        this.toast("Notes draft was from a different task — discarded and refreshed.", "error");
        return;
      }
      this.savingNotes = true;
      try {
        await api.putNotes(t.id, { actor: this.actor(), notes: this.notesDraft });
        safeStorage.removeItem(notesDraftKey(t.id));
        this.notesDirty = false;
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          // notesDraft survives the refresh because notesDirty is still true
          await this.refreshDetail(true).catch(() => undefined);
          this.toast("Notes changed elsewhere — refreshed. Review before re-saving.", "info");
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.savingNotes = false;
      }
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
    claimBusy: false,
    nowTick: Date.now(),
    _tickerId: null as ReturnType<typeof setInterval> | null,

    /** Live-ticking MM:SS countdown until lease expiry for the claim capsule. */
    claimCountdown(t?: Pick<TaskSummary, "claim_expires_at"> | null): string {
      return formatClaimCountdown(t?.claim_expires_at, this.nowTick);
    },

    /** Whether a task's claim lease has EXPIRED. Reads `this.nowTick` (the same
     *  value claimCountdown() uses) so Alpine tracks it: the muted "expired"
     *  styling flips live the moment a lease crosses expiry, without any poll. */
    claimExpired(t: Pick<TaskSummary, "claimed_by" | "claim_expires_at">): boolean {
      return isClaimExpired(t, this.nowTick);
    },

    async claimCurrentTask(): Promise<void> {
      const d = this.detail;
      if (!d || this.claimBusy) return;
      this.claimBusy = true;
      try {
        const fresh = await api.claimTask(d.id, this.actor());
        if (this.detail && this.detail.id === d.id) {
          this.detail.status = fresh.status;
          this.detail.version = fresh.version;
          this.detail.claimed_by = fresh.claimed_by;
          this.detail.claim_expires_at = fresh.claim_expires_at;
          this.detail.updated_at = fresh.updated_at;
        }
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
        this.toast(`Claimed task as ${this.actor().name}`);
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.claimBusy = false;
      }
    },

    claimTask(): Promise<void> {
      return this.claimCurrentTask();
    },

    async releaseCurrentTask(): Promise<void> {
      const d = this.detail;
      if (!d || this.claimBusy) return;
      this.claimBusy = true;
      try {
        const fresh = await api.releaseTask(d.id, this.actor());
        if (this.detail && this.detail.id === d.id) {
          this.detail.status = fresh.status;
          this.detail.version = fresh.version;
          this.detail.claimed_by = undefined;
          this.detail.claim_expires_at = undefined;
          this.detail.updated_at = fresh.updated_at;
        }
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
        this.toast("Released claim on task");
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.claimBusy = false;
      }
    },

    releaseTask(): Promise<void> {
      return this.releaseCurrentTask();
    },

    async heartbeatCurrentTask(): Promise<void> {
      const d = this.detail;
      if (!d || this.claimBusy) return;
      this.claimBusy = true;
      try {
        const fresh = await api.heartbeatTask(d.id, this.actor());
        if (this.detail && this.detail.id === d.id) {
          this.detail.claim_expires_at = fresh.claim_expires_at;
        }
        await this.refreshDetail(true);
        this.toast("Heartbeat recorded — lease extended");
      } catch (e) {
        if (ApiError.isNotClaimed(e)) {
          // No lease exists at all (not a conflict with a live holder) — say
          // the actionable part plainly instead of echoing the raw body.
          this.toast("No claim lease on this task — claim it first.", "error");
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.claimBusy = false;
      }
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

    selectRelationTarget(t: TaskSummary): void {
      this.relationForm.targetId = t.id;
      this.relationForm.search = `${t.title} (${shortId(t.id)})`;
      this.relationForm.open = false;
      this.relationForm.activeIndex = -1;
    },

    onRelationComboboxKeyDown(dir: "up" | "down"): void {
      const list = this.filteredRelationTasks();
      if (list.length === 0) return;
      this.relationForm.open = true;
      if (dir === "down") {
        this.relationForm.activeIndex = (this.relationForm.activeIndex + 1) % list.length;
      } else {
        this.relationForm.activeIndex =
          this.relationForm.activeIndex <= 0 ? list.length - 1 : this.relationForm.activeIndex - 1;
      }
    },

    onRelationComboboxEnter(): void {
      const list = this.filteredRelationTasks();
      if (this.relationForm.open && this.relationForm.activeIndex >= 0 && this.relationForm.activeIndex < list.length) {
        this.selectRelationTarget(list[this.relationForm.activeIndex]);
      } else if (!this.relationForm.busy && (this.relationForm.targetId || this.relationForm.search.trim())) {
        this.addRelation();
      }
    },

    filteredRelationTasks(): TaskSummary[] {
      const d = this.detail;
      const b = this.board;
      if (!b || !d) return [];
      const currentId = d.id;
      const kind = this.relationForm.kind;
      const q = (this.relationForm.search || "").trim().toLowerCase();

      // Collect all tasks except current task
      const all: TaskSummary[] = [];
      const seen = new Set<string>();
      for (const s of STATUSES) {
        for (const t of b.tasks[s] ?? []) {
          if (t.id !== currentId && !seen.has(t.id)) {
            seen.add(t.id);
            all.push(t);
          }
        }
      }

      // AC3: exclude tasks already related with the same kind
      const existingRelations = d.relations ?? [];
      const isAlreadyRelated = (taskId: string): boolean => {
        return existingRelations.some((r) => {
          if (kind === "parent") {
            return r.kind === "child" && r.task_id === d.id && r.other_task_id === taskId;
          } else if (kind === "child") {
            return r.kind === "child" && r.task_id === taskId && r.other_task_id === d.id;
          } else if (kind === "blocked-by") {
            return r.kind === "blocked-by" && r.task_id === d.id && r.other_task_id === taskId;
          } else if (kind === "blocking") {
            return r.kind === "blocked-by" && r.task_id === taskId && r.other_task_id === d.id;
          } else if (kind === "duplicate-of") {
            return r.kind === "duplicate-of" && (r.task_id === taskId || r.other_task_id === taskId);
          } else if (kind === "related") {
            return r.kind === "related" && (r.task_id === taskId || r.other_task_id === taskId);
          }
          return false;
        });
      };

      const eligible = all.filter((t) => !isAlreadyRelated(t.id));

      // Filter by search query
      const filtered = q
        ? eligible.filter((t) => {
            const sid = shortId(t.id).toLowerCase();
            return (
              t.title.toLowerCase().includes(q) ||
              t.id.toLowerCase().includes(q) ||
              sid.includes(q) ||
              t.status.toLowerCase().includes(q)
            );
          })
        : eligible;

      // AC2: If kind is 'blocked-by', sort open tasks before done tasks
      if (kind === "blocked-by") {
        return filtered.sort((a, b) => {
          if (a.status === "done" && b.status !== "done") return 1;
          if (a.status !== "done" && b.status === "done") return -1;
          return a.priority - b.priority;
        });
      }

      return filtered;
    },

    availableRelationTasks(): TaskSummary[] {
      return this.filteredRelationTasks();
    },

    /** Resolve relation edges to display rows (direction-aware for child and
     *  blocked-by; the wire never exposes a "parent" kind). */
    relationRows(): RelationView[] {
      const d = this.detail;
      if (!d) return [];
      const me = d.id;
      const b = this.board;
      return d.relations.map((r) => {
        let label: string;
        let otherId: string;
        let isBlocker = false;
        let badgeClass = "bg-zinc-100 text-zinc-600 dark:bg-zinc-700 dark:text-zinc-300";
        if (r.kind === "child") {
          // Stored edge: task_id is the child, other_task_id the parent.
          if (r.task_id === me) {
            label = "Parent";
            otherId = r.other_task_id;
            badgeClass = "bg-purple-100 text-purple-800 ring-1 ring-purple-300 dark:bg-purple-950 dark:text-purple-300 dark:ring-purple-800";
          } else {
            label = "Child";
            otherId = r.task_id;
            badgeClass = "bg-indigo-100 text-indigo-800 ring-1 ring-indigo-300 dark:bg-indigo-950 dark:text-indigo-300 dark:ring-indigo-800";
          }
        } else if (r.kind === "blocked-by") {
          if (r.task_id === me) {
            label = "Blocked by";
            otherId = r.other_task_id;
            isBlocker = true;
            badgeClass = "bg-red-100 text-red-800 ring-1 ring-red-300 dark:bg-red-950 dark:text-red-300 dark:ring-red-800 font-bold";
          } else {
            label = "Blocking";
            otherId = r.task_id;
            badgeClass = "bg-amber-100 text-amber-800 ring-1 ring-amber-300 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800";
          }
        } else if (r.kind === "duplicate-of") {
          if (r.task_id === me) {
            label = "Duplicate of";
            otherId = r.other_task_id;
          } else {
            label = "Duplicated by";
            otherId = r.task_id;
          }
          badgeClass = "bg-zinc-100 text-zinc-600 ring-1 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700";
        } else {
          label = "Related to";
          otherId = r.task_id === me ? r.other_task_id : r.task_id;
          badgeClass = "bg-sky-100 text-sky-800 ring-1 ring-sky-300 dark:bg-sky-950 dark:text-sky-300 dark:ring-sky-800";
        }

        let status: Status | undefined;
        if (b) {
          for (const s of STATUSES) {
            const found = b.tasks[s]?.find((t) => t.id === otherId);
            if (found) {
              status = found.status;
              break;
            }
          }
        }

        return {
          key: `${r.task_id}:${r.other_task_id}:${r.kind}`,
          label,
          otherId,
          title: this.taskTitle(otherId),
          status,
          isBlocker,
          badgeClass,
          raw: r,
        };
      });
    },

    detailSubtaskTotal(): number {
      const d = this.detail;
      if (!d) return 0;
      let total = 0;
      for (const r of d.relations) {
        if (r.kind === "child" && r.other_task_id === d.id) {
          total++;
        }
      }
      return total;
    },

    detailSubtaskDone(): number {
      const d = this.detail;
      if (!d) return 0;
      let done = 0;
      const b = this.board;
      for (const r of d.relations) {
        if (r.kind === "child" && r.other_task_id === d.id) {
          if (b) {
            for (const s of STATUSES) {
              const row = b.tasks[s]?.find((t) => t.id === r.task_id);
              if (row && row.status === "done") {
                done++;
                break;
              }
            }
          }
        }
      }
      return done;
    },

    detailSubtaskPercent(): number {
      const total = this.detailSubtaskTotal();
      if (total <= 0) return 0;
      return Math.min(100, Math.round((this.detailSubtaskDone() / total) * 100));
    },

    async addRelation(): Promise<void> {
      const d = this.detail;
      let targetId = this.relationForm.targetId.trim();
      if (!targetId && this.relationForm.search.trim()) {
        const q = this.relationForm.search.trim();
        const match = this.filteredRelationTasks().find(
          (t) =>
            t.id === q ||
            t.id.toLowerCase() === q.toLowerCase() ||
            shortId(t.id).toLowerCase() === q.toLowerCase(),
        );
        targetId = match ? match.id : q;
      }
      const kind = this.relationForm.kind;
      if (!d || !targetId || this.relationForm.busy) return;
      if (targetId === d.id) {
        this.toast("Cannot relate a task to itself", "error");
        return;
      }
      this.relationForm.busy = true;
      try {
        if (kind === "parent") {
          // Setting target as parent of this task -> this task is a child of target
          await api.addRelation(d.id, targetId, "child", this.actor());
        } else if (kind === "child") {
          // Setting target as child of this task -> this task is a parent of target
          await api.addRelation(d.id, targetId, "parent", this.actor());
        } else if (kind === "blocking") {
          // This task blocks target -> target is blocked by this task
          await api.addRelation(targetId, d.id, "blocked-by", this.actor());
        } else {
          // blocked-by, duplicate-of, related
          await api.addRelation(d.id, targetId, kind, this.actor());
        }
        this.relationForm.targetId = "";
        this.relationForm.search = "";
        this.relationForm.open = false;
        this.relationForm.activeIndex = -1;
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
        this.toast("Relation added");
      } catch (e) {
        // Surface 409 parent conflict or any error verbatim without corrupting state
        this.toast(errMsg(e), "error");
      } finally {
        this.relationForm.busy = false;
      }
    },

    async removeRelation(r: RelationRow): Promise<void> {
      const d = this.detail;
      if (!d) return;
      try {
        await api.removeRelation(r.task_id, r.other_task_id, r.kind, this.actor());
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
        this.toast("Relation removed");
      } catch (e) {
        this.toast(errMsg(e), "error");
      }
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
      });
      if (action === "new-task") {
        e.preventDefault();
        this.openNewTask("todo");
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
    /** Boot guard (ticket 01M3VJ5PQEAJ60B0QGPNZT8NKP): Alpine invokes the
     *  component's init() TWICE per page load — once because x-data auto-runs
     *  a data object's init method, and again from the root element's explicit
     *  x-init="init()" (directive order runs x-data first; verified empirically
     *  against the shipped bundle: two invocation stacks ~6ms apart, the second
     *  carrying Alpine's `[Alpine] init()` eval frame). The memo makes any
     *  repeat call return the FIRST boot's promise instead of re-running it, so
     *  no duplicate ticker intervals, debouncers, window listeners, projects
     *  fetches or SSE EventSources are created. (loadProjects' in-flight
     *  coalescer had already masked only the fetch; everything else still
     *  duplicated — including one open-then-immediately-closed EventSource.)
     *
     *  The memo is never re-triggered on a failed boot by design: _runBoot()
     *  catches every network error into loadError/state, and the boot-error
     *  screen's Retry runs retryBoot(), which re-runs loadProjects +
     *  continueBoot directly WITHOUT calling init(). A rejected promise would
     *  therefore only mean a genuine programming bug — in that case clear the
     *  memo so a later call can retry. */
    async init(): Promise<void> {
      if (!this._bootPromise) {
        this._bootPromise = this._runBoot();
        this._bootPromise.catch(() => {
          // Only reachable on a genuine (non-network) throw — see above.
          this._bootPromise = null;
        });
      }
      return this._bootPromise;
    },

    /** The actual boot sequence — run at most ONCE per component instance
     *  (see the init() guard above). Kept as its own method so the memo stays
     *  trivially inspectable and unit-testable. */
    async _runBoot(): Promise<void> {
      this._tickerId = setInterval(() => {
        this.nowTick = Date.now();
      }, 1000);
      if (typeof (this._tickerId as any)?.unref === "function") {
        (this._tickerId as any).unref();
      }
      // Coalescer for SSE-triggered board/projects reloads (handleSseEvent):
      // a burst of events → ONE round after the quiet window. Created before any
      // network work so even an early event burst can't fire uncoalesced loads.
      this.sseReloadDebounce = createDebounce(() => {
        void this.loadBoard(true);
        void this.loadProjects(true);
      }, SSE_RELOAD_DEBOUNCE_MS);
      if (typeof window !== "undefined") {
        window.addEventListener("beforeunload", () => {
          if (this._tickerId) clearInterval(this._tickerId);
          this.disconnectSse(true);
        });
        // Track live OS preference changes — only relevant while in system mode.
        this._themeMq?.addEventListener("change", () => {
          if (this.theme === "system") this.applyTheme();
        });
        // Mobile breakpoint flag mirroring Tailwind's `md:` (min-width 768px):
        // drives the x-if on the slide-down panel so its controls never coexist
        // with their desktop/header instances in the DOM.
        const mobileMq = window.matchMedia("(min-width: 768px)");
        this.isMobile = !mobileMq.matches;
        mobileMq.addEventListener("change", () => {
          this.isMobile = !mobileMq.matches;
        });
        // Browser back/forward is a first-class way into and out of the full-
        // page task view (/tasks/<id>) — reconcile on every popstate.
        window.addEventListener("popstate", () => this.onPopState());

        // Restore URL query parameters (filters, sort, project)
        this.initFromUrl();
      }
      if (typeof (this as any).$watch === "function") {
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
          (this as any).$watch(prop, () => {
            if (!this._boardViewModule().isRestoringUrlState()) {
              this.syncUrlState(prop !== "filterSearch");
            }
          });
        }
        // Full-text search (01M5XTAW3RZ4QJCSW9C3CS255R): EVERY path that mutates
        // the query or the project re-arms the debounced desc/notes search —
        // typed input (both desktop and mobile bind filterSearch), Reset
        // filters, boot restore from ?q=... (applyQueryParams runs with the
        // board-view module's URL-restore flag set, hence these are SEPARATE
        // watchers that deliberately do NOT gate on it), browser back/forward, and project
        // switches. Clearing the query lands in scheduleFullTextSearch's empty
        // branch: timer cancelled, in-flight retired, match set dropped.
        (this as any).$watch("filterSearch", () => this.scheduleFullTextSearch());
        (this as any).$watch("selectedProjectId", () => this.scheduleFullTextSearch());
      }
      this.applyTheme();
      if (typeof document !== "undefined") {
        this.setDocumentTitle();
      }
      await this.loadProjects();
      // Fire-and-forget identity probe: learns WHO the presenting bearer is (multi-user
      // session) so the header can show it reactively; a non-session/off-mode token
      // simply resolves to "no identity" without disturbing boot or the token.
      void this.refreshSessionIdentity();
      // Deep link: the initial URL may already be a full-page task route.
      const deepLinkedTaskId =
        typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
      if (this.loadError) {
        // Boot projects fetch failed — network blip or backend still starting.
        // Do NOT validate the selection against an empty list that is the
        // PRODUCT of the failure: keep selectedProjectId as-is, write nothing
        // to storage, and open no New Project bar (ticket
        // 01M3MMYP938G972KY8NF7RGMQR). Surface a boot-error screen with a
        // Retry that re-runs exactly this continuation once the fetch recovers.
        if (deepLinkedTaskId) {
          // A full-page task open carries its own fetch + error/retry state, so
          // it proceeds even when the projects list failed to load.
          await this.openDeepLinkedTask(deepLinkedTaskId);
        } else {
          this.bootError = true;
          // Move focus to Retry as soon as it is rendered — x-show flushes on
          // Alpine's own schedule, so poll frames instead of focusing blind
          // (same convention as the drawer's fetch-failure state).
          const retryBtn =
            typeof document === "undefined"
              ? null
              : document.querySelector<HTMLElement>('[data-testid="boot-retry-btn"]');
          this._focusWhenVisible(retryBtn);
        }
      } else {
        await this.continueBoot(deepLinkedTaskId);
      }
      if (this.polling) this.startPolling();
      // Boot finished: the Projects dropdown trigger may now be clicked
      // (e2e helpers wait on it becoming enabled). Set last so a failed
      // early load can't unlock an inert control.
      this.appBooted = true;
    },

    /** Success-path boot continuation, shared by init() and retryBoot():
     *  validate the stored selection against the freshly loaded list (a stale
     *  id falls back to the first project), persist it, load the board and
     *  connect SSE — or invite creating a project when the server is genuinely
     *  empty. Only ever called AFTER a successful projects fetch. */
    async continueBoot(deepLinkedTaskId: string | null): Promise<void> {
      const known = this.projects.some((p) => p.id === this.selectedProjectId);
      if (!known || !this.selectedProjectId) {
        this.selectedProjectId = this.projects[0]?.id ?? "";
      }
      if (deepLinkedTaskId) {
        await this.openDeepLinkedTask(deepLinkedTaskId);
      } else if (this.selectedProjectId) {
        try {
          safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
        } catch { /* ignore */ }
        await this.loadBoard();
        this.connectSse();
        // Boot with a restored search query (?q=…): initFromUrl() applied the
        // query BEFORE the $watch handlers were registered (init order), so those
        // initial mutations never fired scheduleFullTextSearch — kick the
        // debounced desc/notes search here so a shared board-search URL actually
        // surfaces its matches (empty query is a harmless no-op branch).
        this.scheduleFullTextSearch();
      } else {
        // No projects yet — invite creating one.
        this.showNewProject = true;
      }
      this.syncUrlState(true);
    },

    /** Retry button on the boot-error screen (ticket 01M3MMYP938G972KY8NF7RGMQR):
     *  re-runs the projects load and — only when it succeeds — the exact same
     *  continuation as init()'s success path. A second failure keeps the stored
     *  selection intact and just refreshes the error message; recovery is
     *  user-driven (no automatic retry loop, same convention as the drawer). */
    async retryBoot(): Promise<void> {
      if (!this.bootError || this.bootRetrying) return;
      this.bootRetrying = true;
      try {
        await this.loadProjects();
        if (this.loadError) return; // still failing — boot-error screen stays up
        this.bootError = false;
        const deepLinkedTaskId =
          typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
        await this.continueBoot(deepLinkedTaskId);
      } finally {
        this.bootRetrying = false;
      }
    },
  };
}
