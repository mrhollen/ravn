// The single Alpine.js root component for the Rävn board UI. One factory
// returning a state + methods object; registered as `Alpine.data("app", …)` in
// main.ts and used via x-data="app()" in index.html.
//
// Scope: VISIBILITY + light intervention. Humans can create projects/tasks,
// move status, save notes, toggle ACs, comment, attach artifacts. Claims are
// READ-ONLY here (agents manage leases via MCP).

import { ApiError, api, errMsg, LS_TOKEN, setApiTimeoutMsForTests } from "./api";
import { FOCUSABLE_SELECTOR, focusWhenReady, openDialogFocus, type DialogFocus } from "./focus";
import { beginMove, canMove, endMove, friendlyTransitionError, MOVE_TIMEOUT_MS, MoveTimedOutError, withTimeout } from "./moves";
import { canApplyDetail, coalesceInFlight, createDebounce, createSequenceGuard, SSE_RELOAD_DEBOUNCE_MS, shouldApplyBoard } from "./refresh";
import { renderMarkdown } from "./md";
import { commentDraftKey, descriptionDraftKey, deserializeCommentDraft, serializeCommentDraft } from "./drafts";
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
  OpenCodeAgentOption,
  OpenCodeModelOption,
  OpenCodeOptionsResponse,
  ProjectWithCounts,
  RealtimeEventPayload,
  RelationRow,
  SseConnectionStatus,
  StartOpenCodeInput,
  StartOpenCodeResponse,
  Status,
  TaskDetail,
  TaskSummary,
  UpdateProjectReq,
  UpdateTaskReq,
} from "./types";
import { STATUSES, STATUS_LABEL } from "./types";
import {
  actorLabel,
  artifactKindClass,
  claimHint,
  commentKindClass,
  dateInputToIso,
  dueCalendarDate,
  dueLabel,
  formatClaimCountdown,
  isClaimExpired,
  isOverdue,
  localCalendarToday,
  initials,
  priorityClass,
  priorityName,
  relTime,
  safeStorage,
  shortId,
  splitLines,
  statusLabel,
} from "./util";

export const LS_ACTOR = "ravn.actor";
export const LS_PROJECT = "ravn.project";
export const LS_SORT = "ravn.sort";
export const LS_THEME = "ravn_theme";
export { LS_TOKEN };
const POLL_MS = 15_000;
const MD_CACHE_MAX = 500;

// --------------------------------------------------------------------------- theme
/** UI theme choice. "system" (default) follows the OS prefers-color-scheme. */
export type ThemeMode = "light" | "dark" | "system";

function isThemeMode(v: unknown): v is ThemeMode {
  return v === "light" || v === "dark" || v === "system";
}

/** Read the persisted theme choice; anything malformed falls back to system. */
function readStoredTheme(): ThemeMode {
  try {
    const raw = safeStorage.getItem(LS_THEME) ?? safeStorage.getItem("ravn.theme");
    return isThemeMode(raw) ? raw : "system";
  } catch {
    return "system";
  }
}

// ---------------------------------------------------------------- client-side sort
/** Sort criteria for the board columns. "" = canonical server order (no sort). */
export type SortKey = "" | "updated_at" | "created_at" | "due_at" | "priority" | "title";
export type SortDir = "asc" | "desc";

export const SORT_OPTIONS: { value: SortKey; label: string }[] = [
  { value: "", label: "Server order (default)" },
  { value: "updated_at", label: "Last Modified" },
  { value: "created_at", label: "Created At" },
  { value: "due_at", label: "Due Date" },
  { value: "priority", label: "Priority" },
  { value: "title", label: "Title (A→Z)" },
];

/** Sensible initial direction per criterion, applied when the user picks a new key. */
const DEFAULT_SORT_DIR: Record<Exclude<SortKey, "">, SortDir> = {
  updated_at: "desc", // newest first
  created_at: "desc", // newest first
  due_at: "asc", // nearest first
  priority: "asc", // P0 (urgent) first
  title: "asc", // alphabetical
};

function isSortKey(v: unknown): v is SortKey {
  return typeof v === "string" && SORT_OPTIONS.some((o) => o.value === v);
}

/** Read the persisted sort choice; anything malformed falls back to no sort. */
function readStoredSort(): { key: SortKey; dir: SortDir } {
  try {
    const raw = safeStorage.getItem(LS_SORT);
    if (!raw) return { key: "", dir: "desc" };
    const parsed = JSON.parse(raw) as { key?: unknown; dir?: unknown };
    const key: SortKey = isSortKey(parsed.key) ? parsed.key : "";
    const dir: SortDir = parsed.dir === "asc" ? "asc" : "desc";
    return { key, dir };
  } catch {
    return { key: "", dir: "desc" };
  }
}

// ------------------------------------------------------ full-page task route
/** Parse the client-side full-page task route `/tasks/<id>` out of a pathname.
 *  Returns null for the board route ("") or anything that is not exactly one
 *  non-empty id segment (no nested slashes, no whitespace-only ids). The server
 *  already deep-links these paths to index.html (SPA fallback in
 *  cmd/ravn-server), so the pathname can be trusted as-is. */
export function taskRouteIdFromPath(pathname: string): string | null {
  const m = /^\/tasks\/([^/]+)$/.exec(pathname);
  if (!m) return null;
  try {
    const id = decodeURIComponent(m[1]);
    return id.trim() === "" ? null : id;
  } catch {
    return m[1]; // malformed %-sequence — use the raw segment rather than throw
  }
}

/** Pathname for task `id`'s full-page route. */
export function taskRoutePath(id: string): string {
  return "/tasks/" + encodeURIComponent(id);
}

// ---------------------------------------------------------------- detail surface
/** Tailwind's `md` breakpoint (px). Below this width the task-detail view has
 *  no room for the centered modal, so card clicks open the FULL-PAGE route
 *  (/tasks/<id>) instead — same code path "Open full page" uses, keeping the
 *  URL deep-linkable and browser-back consistent on phones. */
export const MOBILE_DETAIL_MAX_WIDTH = 768;

/** True when the current viewport is below the md breakpoint (mobile).
 *  `width` defaults to window.innerWidth; passed explicitly in unit tests.
 *  Returns false without a window (Node) so the modal/desktop path is used. */
export function isMobileDetailViewport(width?: number): boolean {
  const w = width ?? (typeof window !== "undefined" ? window.innerWidth : Number.POSITIVE_INFINITY);
  return w < MOBILE_DETAIL_MAX_WIDTH;
}

/** Last-resort clipboard write for non-secure contexts (plain http on a LAN),
 *  where navigator.clipboard is unavailable: hidden textarea + execCommand.
 *  Returns true only when the browser reports the copy succeeded. */
function copyViaExecCommand(text: string): boolean {
  if (typeof document === "undefined" || !document.body) return false;
  const ta = document.createElement("textarea");
  ta.value = text;
  // Keep it out of the flow, off-screen, and unfocusable by tab order.
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.top = "-9999px";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    ta.remove();
  }
  return ok;
}

/** Comparator for one criterion + direction. Ties always break by id (ascending)
 *  so the order is deterministic regardless of direction. */
function compareTasks(a: TaskSummary, b: TaskSummary, key: SortKey, dir: SortDir): number {
  let cmp = 0;
  switch (key) {
    case "priority":
      cmp = a.priority - b.priority;
      break;
    case "title":
      cmp = a.title.localeCompare(b.title, undefined, { sensitivity: "base", numeric: true });
      break;
    case "due_at": {
      // No due date sorts last (mirrors the server's nulls-last convention).
      const da = a.due_at ? Date.parse(a.due_at) : Infinity;
      const db = b.due_at ? Date.parse(b.due_at) : Infinity;
      cmp = da === db ? 0 : da < db ? -1 : 1;
      break;
    }
    case "created_at":
    case "updated_at": {
      const va = Date.parse(a[key]);
      const vb = Date.parse(b[key]);
      cmp = Number.isNaN(va) || Number.isNaN(vb) ? 0 : va - vb;
      break;
    }
    default:
      return a.id.localeCompare(b.id);
  }
  if (cmp === 0) return a.id.localeCompare(b.id);
  return dir === "asc" ? cmp : -cmp;
}

const SSE_EVENT_KINDS = [
  "message",
  "created",
  "updated",
  "status-changed",
  "claimed",
  "released",
  "heartbeat",
  "assigned",
  "noted",
  "commented",
  "ac-toggled",
  "artifact-added",
  "relation-changed",
  "ac-added",
  "ac-updated",
  "ac-removed",
];

/** TEST-SUPPORT telemetry for the SSE singleton guarantee (ticket
 *  01M3VJ5PQEAJ60B0QGPNZT8NKP): counts EventSource constructions and close
 *  paths so unit + browser tests can assert "exactly one open stream" without
 *  reaching into a live socket. No production behavior reads these counters —
 *  they only accumulate. Browser tests reach them via the component reference
 *  (`Alpine.$data(root)._sseTelemetry`, see `_sseTelemetry` below) instead of
 *  a window flag, so shipped bundles stay clean. */
export const sseTelemetry = {
  /** Every successful `new EventSource(...)` in connectSse(). */
  opened: 0,
  /** Every close path that actually closed a live stream (disconnectSse +
   *  handleSseDisconnect). A double-close can't skew this: the counter fires
   *  only while `this.sse` is non-null. */
  closed: 0,
  /** Currently-open streams = opened − closed. Steady state must be exactly 1
   *  after boot and STAYS 1 across reconnect cycles (each close+open nets to
   *  zero). */
  openCount(): number {
    return this.opened - this.closed;
  },
};

export interface Toast {
  id: number;
  msg: string;
  kind: "error" | "info";
  action?: { label: string; href: string };
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
      if (this.theme === "light") return false;
      if (this.theme === "dark") return true;
      return !!this._themeMq?.matches;
    },

    setTheme(mode: ThemeMode): void {
      this.theme = mode;
      try {
        safeStorage.setItem(LS_THEME, mode);
        safeStorage.setItem("ravn.theme", mode);
      } catch {
        /* private mode — non-fatal */
      }
      this.applyTheme();
    },

    // -------------------------------------------------------- document title
    /** Update document title with Rävn branding. */
    setDocumentTitle(title?: string): void {
      if (typeof document === "undefined") return;
      document.title = title ? `${title} · Rävn` : "Rävn";
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

    toast(msg: string, kind: "error" | "info" = "info", action?: { label: string; href: string }): void {
      const id = ++this.toastSeq;
      const item: Toast = { id, msg, kind };
      if (action) item.action = action;
      this.toasts = [...this.toasts, item];
      // Global setTimeout (not window.setTimeout): identical in the browser,
      // but keeps async error paths unit-testable without a full window mock.
      setTimeout(() => {
        this.dismissToast(id);
      }, 6500);
    },

    /** Manually drop one toast (its × button). Same removal path as auto-expiry.
     *  Two-phase: mark it `leaving` so the x-show leave transition (slide + fade)
     *  plays, then drop the row once that animation has finished — Alpine's
     *  x-for removes nodes instantly, so a direct filter would kill the exit. */
    dismissToast(id: number): void {
      this.toasts = this.toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t));
      setTimeout(() => {
        this.toasts = this.toasts.filter((t) => t.id !== id);
      }, 280); // leave transition is duration-200; keep the margin generous
    },

    // ------------------------------------------------------------------ auth token
    authToken: api.getAuthToken(),
    showAuthModal: false,
    tokenInput: "",
    /** Focus session for the auth modal (trap + restore); null while closed. */
    authFocus: null as DialogFocus | null,

    openAuthModal(): void {
      this.tokenInput = this.authToken;
      this.showAuthModal = true;
      // Token input first — least destructive control in this dialog.
      this.authFocus = this.openDialog("cb-auth-modal", "input");
    },

    /** Single close path for the auth modal (× / Cancel / backdrop / save / clear / Esc). */
    closeAuthModal(): void {
      this.showAuthModal = false;
      this.closeDialog(this.authFocus);
      this.authFocus = null;
    },

    saveAuthToken(): void {
      api.setAuthToken(this.tokenInput);
      this.authToken = api.getAuthToken();
      this.closeAuthModal();
      this.loadError = "";
      void this.loadProjects().then(() => {
        // A bad/expired token re-triggers the auth modal + loadError inside
        // loadProjectsCore; leave the boot-error/retry path to surface it.
        if (this.loadError) return;
        // First-visit enters the auth modal from a 401 during boot, which left
        // bootError set — clear it now that projects loaded so the freshly
        // auto-selected board isn't hidden behind the boot-error overlay.
        this.bootError = false;
        const deepLinkedTaskId =
          typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
        // Reuse the boot continuation: keeps a valid manual selection, else
        // auto-selects the first project (canonical server order), persists it,
        // loads its board + SSE; with zero projects shows the empty state.
        void this.continueBoot(deepLinkedTaskId);
      });
    },

    clearAuthToken(): void {
      api.setAuthToken("");
      this.authToken = "";
      this.tokenInput = "";
      this.closeAuthModal();
      this.loadError = "";
      if (this.selectedProjectId) this.connectSse();
      void this.loadProjects().then(() => {
        if (this.selectedProjectId) void this.loadBoard();
      });
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
     *  rendered, so deliberately plain fields outside template concerns. */
    _projectSeq: createSequenceGuard(),
    _boardSeq: createSequenceGuard(),
    /** Trailing debounce coalescing SSE-triggered board/projects reloads into
     *  one round per quiet window (SSE_RELOAD_DEBOUNCE_MS). Created in init(). */
    sseReloadDebounce: null as ReturnType<typeof createDebounce> | null,

    /** In-flight coalescer for loadProjects, created lazily on first use (so
     *  the closure's `this` is always the live app instance): a concurrent
     *  duplicate — observed at boot, where Alpine re-runs init() — JOINS the
     *  running request instead of racing it, so no caller ever resumes against
     *  state that a sibling call's sequence guard discarded. */
    _projectsCoalesced: null as ReturnType<typeof coalesceInFlight<boolean[], void>> | null,

    loadProjects(silent = false): Promise<void> {
      if (!this._projectsCoalesced) {
        this._projectsCoalesced = coalesceInFlight((s: boolean) => this.loadProjectsCore(s));
      }
      return this._projectsCoalesced.call(silent);
    },

    async loadProjectsCore(silent: boolean): Promise<void> {
      const token = this._projectSeq.next(); // newest initiated list-projects wins
      try {
        const res = await api.listProjects();
        // A slower EARLIER request must never overwrite counts a newer request
        // already delivered (out-of-order network responses).
        if (!this._projectSeq.isFresh(token)) return;
        this.projects = res.projects;
        if (!silent) this.loadError = "";
      } catch (e) {
        if (!silent) this.loadError = errMsg(e);
        if (ApiError.isUnauthorized(e)) {
          this.openAuthModal();
        }
      }
    },

    projectOptionLabel(p: ProjectWithCounts): string {
      const c = p.task_counts;
      return `${p.name}  (${c.todo}·${c.doing}·${c.done})`;
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
    _projectMenuOutsideHandler: null as ((e: Event) => void) | null,

    projectSelectLabel(): string {
      const p = this.projects.find((x) => x.id === this.selectedProjectId);
      return p ? this.projectOptionLabel(p) : "Select a project…";
    },

    toggleProjectMenu(): void {
      if (this.projectMenuOpen) this.closeProjectMenu();
      else this.openProjectMenu("selected");
    },

    /** Open the menu and focus an option: "selected" = current project (or the
     *  first one when nothing is selected), "first"/"last" for ArrowDown/Up on
     *  a closed trigger. The capture-phase outside-click listener lives exactly
     *  as long as the menu is open (removed in closeProjectMenu). */
    openProjectMenu(focusTarget: "selected" | "first" | "last" = "selected"): void {
      if (this.projectMenuOpen) return;
      this.projectMenuOpen = true;
      if (typeof document !== "undefined") {
        const onDocClick = (e: Event): void => {
          // Inside the control (trigger or items): their own handlers decide.
          const wrap = document.querySelector<HTMLElement>('[data-testid="project-dropdown"]');
          if (wrap && e.target instanceof Node && wrap.contains(e.target)) return;
          this.closeProjectMenu(); // outside click — no focus restore
        };
        this._projectMenuOutsideHandler = onDocClick;
        document.addEventListener("click", onDocClick, true);
      }
      if (typeof window === "undefined" || typeof document === "undefined") return;
      // The options exist in the DOM from boot (x-for rendered once; the panel
      // is merely x-show-hidden), so resolve the target NOW and let
      // _focusWhenVisible wait out the display flush before focusing.
      const root = document.querySelector<HTMLElement>('[data-testid="project-menu"]');
      if (!root) return;
      const items = Array.from(root.querySelectorAll<HTMLButtonElement>("button"));
      if (items.length === 0) return;
      let idx = 0;
      if (focusTarget === "last") idx = items.length - 1;
      else if (focusTarget === "selected") {
        const i = items.findIndex((el) => el.getAttribute("data-project-id") === this.selectedProjectId);
        if (i !== -1) idx = i;
      }
      this._focusWhenVisible(items[idx] ?? null);
    },

    /** Focus `el` once it is actually visible. x-show/x-cloak display updates
     *  flush on Alpine's own schedule — measured LATER than both a single
     *  requestAnimationFrame AND setTimeout(0) (see focus.ts) — so "focus one
     *  tick after open" can land while the surface is still display:none and
     *  .focus() silently no-ops forever. Instead: poll each animation frame
     *  until visible, bounded by a wall-clock deadline (1500ms) so an element
     *  that never renders can't spin. Mirrors focusWhenReady(). */
    _focusWhenVisible(el: HTMLElement | null): void {
      if (!el || typeof window === "undefined" || typeof requestAnimationFrame !== "function") return;
      const isVisible = (n: HTMLElement): boolean =>
        typeof n.checkVisibility === "function"
          ? n.checkVisibility({ checkOpacity: false, checkVisibilityCSS: true })
          : n.offsetParent !== null || getComputedStyle(n).position === "fixed";
      const deadline = Date.now() + 1500; // ~2s wall-clock horizon, frame-rate independent
      const step = (): void => {
        if (isVisible(el)) {
          el.focus();
          return;
        }
        if (Date.now() >= deadline) return; // never rendered — stop, don't focus blind
        requestAnimationFrame(step);
      };
      step();
    },

    /** Close the menu. `restoreFocus` sends focus back to the trigger (Esc / a
     *  pick); outside clicks close WITHOUT restoring — focus is already where
     *  the user clicked. */
    closeProjectMenu(restoreFocus = false): void {
      if (!this.projectMenuOpen) return;
      this.projectMenuOpen = false;
      if (this._projectMenuOutsideHandler && typeof document !== "undefined") {
        document.removeEventListener("click", this._projectMenuOutsideHandler, true);
        this._projectMenuOutsideHandler = null;
      }
      if (restoreFocus && typeof window !== "undefined" && typeof document !== "undefined") {
        this._focusWhenVisible(document.querySelector<HTMLElement>('[data-testid="project-select"]'));
      }
    },

    /** Arrow/Home/End navigation between menu items (buttons in DOM order —
     *  project options, then the "+ New project…" action). */
    moveProjectFocus(dir: number | "home" | "end"): void {
      const root =
        typeof document === "undefined" ? null : document.querySelector<HTMLElement>('[data-testid="project-menu"]');
      if (!root) return;
      const items = Array.from(root.querySelectorAll<HTMLButtonElement>("button"));
      if (items.length === 0) return;
      const active = document.activeElement instanceof HTMLButtonElement ? document.activeElement : null;
      let next: number;
      if (dir === "home") next = 0;
      else if (dir === "end") next = items.length - 1;
      else {
        const idx = active ? items.indexOf(active) : -1;
        next = idx === -1 ? (dir > 0 ? 0 : items.length - 1) : Math.min(items.length - 1, Math.max(0, idx + dir));
      }
      items[next]?.focus();
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
    _headerMenuOutsideHandler: null as ((e: Event) => void) | null,

    /** Trigger glyph: the actor's initial, or "You" when no name is set. */
    actorGlyph(): string {
      const n = this.actorName.trim();
      return n ? n.slice(0, 1).toUpperCase() : "You";
    },

    toggleHeaderMenu(): void {
      if (this.headerMenuOpen) this.closeHeaderMenu();
      else this.openHeaderMenu("first");
    },

    /** Open the menu and focus its first/last focusable control. The
     *  capture-phase outside-click listener lives exactly as long as the menu
     *  is open (removed in closeHeaderMenu). Opening it collapses the Projects
     *  dropdown so at most one header menu is ever live. */
    openHeaderMenu(focusTarget: "first" | "last" = "first"): void {
      if (this.headerMenuOpen) return;
      if (this.projectMenuOpen) this.closeProjectMenu();
      this.headerMenuOpen = true;
      if (typeof document !== "undefined") {
        const onDocClick = (e: Event): void => {
          // Inside the control (trigger or panel): their own handlers decide.
          const wrap = document.querySelector<HTMLElement>('[data-testid="header-menu-anchor"]');
          if (wrap && e.target instanceof Node && wrap.contains(e.target)) return;
          this.closeHeaderMenu(); // outside click — no focus restore
        };
        this._headerMenuOutsideHandler = onDocClick;
        document.addEventListener("click", onDocClick, true);
      }
      if (typeof window === "undefined" || typeof document === "undefined") return;
      // The controls exist in the DOM from boot (the panel is merely
      // x-show-hidden), so resolve the target NOW and let _focusWhenVisible
      // wait out the display flush before focusing.
      const root = document.querySelector<HTMLElement>('[data-testid="header-menu"]');
      if (!root) return;
      const items = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
      if (items.length === 0) return;
      const idx = focusTarget === "last" ? items.length - 1 : 0;
      this._focusWhenVisible(items[idx] ?? null);
    },

    /** Close the menu. `restoreFocus` sends focus back to the trigger (Esc /
     *  programmatic close); outside clicks close WITHOUT restoring — focus is
     *  already where the user clicked. */
    closeHeaderMenu(restoreFocus = false): void {
      if (!this.headerMenuOpen) return;
      this.headerMenuOpen = false;
      if (this._headerMenuOutsideHandler && typeof document !== "undefined") {
        document.removeEventListener("click", this._headerMenuOutsideHandler, true);
        this._headerMenuOutsideHandler = null;
      }
      if (restoreFocus && typeof window !== "undefined" && typeof document !== "undefined") {
        this._focusWhenVisible(document.querySelector<HTMLElement>('[data-testid="header-menu-trigger"]'));
      }
    },

    /** Arrow/Home/End navigation between the panel's focusable controls in DOM
     *  order (theme buttons, actor input, auto-refresh checkbox, token item).
     *  The actor input is single-line: Up/Down have no caret role there
     *  (Left/Right do), so arrows uniformly hop controls — same contract as
     *  the Projects dropdown. */
    moveHeaderFocus(dir: number | "home" | "end"): void {
      if (typeof document === "undefined") return;
      const active = document.activeElement;
      const root = document.querySelector<HTMLElement>('[data-testid="header-menu"]');
      if (!root) return;
      const items = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
      if (items.length === 0) return;
      let next: number;
      if (dir === "home") next = 0;
      else if (dir === "end") next = items.length - 1;
      else {
        const idx = active instanceof HTMLElement ? items.indexOf(active) : -1;
        next = idx === -1 ? (dir > 0 ? 0 : items.length - 1) : Math.min(items.length - 1, Math.max(0, idx + dir));
      }
      items[next]?.focus();
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
     *  the draft clears only on successful create. */
    openNewProject(): void {
      this.closeProjectMenu();
      this.showNewProject = true;
      if (typeof window === "undefined" || typeof document === "undefined") return;
      // The name input exists from boot (static markup under an x-show bar);
      // wait out the display flush before focusing.
      this._focusWhenVisible(document.getElementById("cb-new-project-name-input"));
    },

    async createProject(): Promise<void> {
      const name = this.newProject.name.trim();
      if (this.projectBusy || !name) return;
      this.projectBusy = true;
      try {
        const p = await api.createProject({
          actor: this.actor(),
          name,
          description: this.newProject.description.trim() || undefined,
        });
        this.showNewProject = false;
        this.newProject = { name: "", description: "" };
        await this.loadProjects();
        this.selectedProjectId = p.id;
        try {
          safeStorage.setItem(LS_PROJECT, p.id);
        } catch { /* ignore */ }
        this.connectSse();
        await this.loadBoard();
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.projectBusy = false;
      }
    },

    // ------------------------------------------------------------ opencode options
    openCodeOptions: null as OpenCodeOptionsResponse | null,
    openCodeOptionsProjectId: "" as string,
    loadingOpenCodeOptions: false,

    availableOpenCodeAgents(): OpenCodeAgentOption[] {
      return this.openCodeOptions?.agents ?? [];
    },

    availableOpenCodeModels(): OpenCodeModelOption[] {
      return this.openCodeOptions?.models ?? [];
    },

    availableOpenCodeVariantsFor(modelRef?: string): string[] {
      const models = this.availableOpenCodeModels();
      if (!models.length) return [];
      const target = (modelRef ?? "").trim() || (this.openCodeOptions?.resolved_model ?? "").trim();
      if (target) {
        const found = models.find((m) => m.ref === target || m.model_id === target);
        if (found && Array.isArray(found.variants)) {
          return [...new Set(found.variants)];
        }
      }
      const union = new Set<string>();
      for (const m of models) {
        if (Array.isArray(m.variants)) {
          for (const v of m.variants) {
            if (v) union.add(v);
          }
        }
      }
      return [...union];
    },

    async fetchOpenCodeOptions(projectId?: string, force = false): Promise<OpenCodeOptionsResponse | null> {
      const pid = projectId || this.selectedProjectId;
      if (!pid) return null;
      const p = this.projects.find((x) => x.id === pid);
      if (p && !p.opencode_server_url && (!this.showProjectSettings || !this.projectSettingsForm.opencode_server_url)) {
        this.openCodeOptions = null;
        return null;
      }
      if (!force && this.openCodeOptions && this.openCodeOptionsProjectId === pid) {
        return this.openCodeOptions;
      }
      this.loadingOpenCodeOptions = true;
      try {
        const res = await api.getOpenCodeOptions(pid);
        this.openCodeOptions = res;
        this.openCodeOptionsProjectId = pid;
        return res;
      } catch {
        this.openCodeOptions = null;
        return null;
      } finally {
        this.loadingOpenCodeOptions = false;
      }
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
    /** Pristine copy of the form captured when the dialog opens (and re-synced
     *  after the authoritative fetch) — the "has anything actually changed?"
     *  reference for the discard guard (task 01M3MMZ188XJBGKFSWBGESK79D). */
    projectSettingsBaseline: {
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
    /** Focus session for the project settings modal (trap + restore). */
    projectSettingsFocus: null as DialogFocus | null,

    async openProjectSettings(): Promise<void> {
      if (!this.selectedProjectId) return;
      this.showArchiveConfirm = false;
      this.archiveConfirmInput = "";
      const p = this.projects.find((x) => x.id === this.selectedProjectId);
      if (p) {
        this.projectSettingsForm = {
          name: p.name,
          description: p.description ?? "",
          opencode_server_url: p.opencode_server_url ?? "",
          opencode_directory: p.opencode_directory ?? "",
          opencode_model: p.opencode_model ?? "",
          opencode_agent: p.opencode_agent ?? "",
          opencode_variant: p.opencode_variant ?? "",
        };
      } else {
        this.projectSettingsForm = {
          name: "",
          description: "",
          opencode_server_url: "",
          opencode_directory: "",
          opencode_model: "",
          opencode_agent: "",
          opencode_variant: "",
        };
      }
      // Pristine reference for the discard guard (re-synced after the fetch).
      this.projectSettingsBaseline = { ...this.projectSettingsForm };
      this.showProjectSettings = true;
      // Name field first — least destructive control in this dialog.
      this.projectSettingsFocus = this.openDialog("cb-project-settings", "input");

      try {
        const full = await api.getProject(this.selectedProjectId);
        this.projectSettingsForm = {
          name: full.name,
          description: full.description ?? "",
          opencode_server_url: full.opencode_server_url ?? "",
          opencode_directory: full.opencode_directory ?? "",
          opencode_model: full.opencode_model ?? "",
          opencode_agent: full.opencode_agent ?? "",
          opencode_variant: full.opencode_variant ?? "",
        };
        // The fetch is authoritative — re-base "pristine" on it.
        this.projectSettingsBaseline = { ...this.projectSettingsForm };
        if (full.opencode_server_url) {
          void this.fetchOpenCodeOptions(this.selectedProjectId);
        }
      } catch {
        /* keep current list info */
      }
    },

    /** Single close path for the settings modal (save / archive success).
     *  User-initiated dismissals (× / Cancel / backdrop / Esc) must go through
     *  requestCloseProjectSettings() so unsaved changes are never lost. */
    closeProjectSettings(): void {
      this.showProjectSettings = false;
      this.showArchiveConfirm = false;
      this.archiveConfirmInput = "";
      this.closeDialog(this.projectSettingsFocus);
      this.projectSettingsFocus = null;
    },

    /** True when any settings field differs from what was loaded on open. */
    projectSettingsDirty(): boolean {
      const f = this.projectSettingsForm;
      const b = this.projectSettingsBaseline;
      return (
        (f.name ?? "") !== (b.name ?? "") ||
        (f.description ?? "") !== (b.description ?? "") ||
        (f.opencode_server_url ?? "") !== (b.opencode_server_url ?? "") ||
        (f.opencode_directory ?? "") !== (b.opencode_directory ?? "") ||
        (f.opencode_model ?? "") !== (b.opencode_model ?? "") ||
        (f.opencode_agent ?? "") !== (b.opencode_agent ?? "") ||
        (f.opencode_variant ?? "") !== (b.opencode_variant ?? "")
      );
    },

    /** Guarded close for the settings dialog (× / Cancel / backdrop / Esc):
     *  asks first when unsaved changes would be lost. */
    requestCloseProjectSettings(): void {
      if (
        this.projectSettingsDirty() &&
        !this.confirmDiscard("Discard unsaved project settings changes?")
      )
        return;
      this.closeProjectSettings();
    },

    openArchiveConfirm(): void {
      this.showArchiveConfirm = true;
      this.archiveConfirmInput = "";
      if (typeof window !== "undefined" && typeof document !== "undefined") {
        this._focusWhenVisible(document.querySelector<HTMLElement>('[data-testid="archive-confirm-input"]'));
      }
    },

    cancelArchiveConfirm(): void {
      this.showArchiveConfirm = false;
      this.archiveConfirmInput = "";
      if (typeof window !== "undefined" && typeof document !== "undefined") {
        this._focusWhenVisible(document.querySelector<HTMLElement>('[data-testid="archive-project-btn"]'));
      }
    },

    isArchiveInputValid(): boolean {
      const p = this.projects.find((x) => x.id === this.selectedProjectId);
      const target = (p ? p.name : this.projectSettingsForm.name).trim();
      return Boolean(target && this.archiveConfirmInput.trim() === target);
    },

    async archiveProject(): Promise<void> {
      if (!this.selectedProjectId || !this.isArchiveInputValid() || this.projectSettingsBusy) return;
      this.projectSettingsBusy = true;
      const targetId = this.selectedProjectId;
      try {
        await api.updateProject(targetId, {
          actor: this.actor(),
          archived: true,
        });
        this.closeProjectSettings();
        await this.loadProjects();
        const stillSelected = this.projects.find((x) => x.id === this.selectedProjectId);
        if (!stillSelected) {
          this.selectedProjectId = this.projects[0]?.id ?? "";
          if (this.selectedProjectId) {
            try {
              safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
            } catch { /* ignore */ }
            this.connectSse();
            await this.loadBoard();
          } else {
            try {
              safeStorage.removeItem(LS_PROJECT);
            } catch { /* ignore */ }
            this.disconnectSse(true);
            this.board = null;
            this.showNewProject = true;
          }
        }
        this.toast("Project archived");
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.projectSettingsBusy = false;
      }
    },

    async saveProjectSettings(): Promise<void> {
      const name = this.projectSettingsForm.name.trim();
      if (!this.selectedProjectId || !name || this.projectSettingsBusy) return;
      this.projectSettingsBusy = true;
      try {
        const agentTrim = this.projectSettingsForm.opencode_agent?.trim() ?? "";
        const variantTrim = this.projectSettingsForm.opencode_variant?.trim() ?? "";
        const serverUrlTrim = this.projectSettingsForm.opencode_server_url?.trim() ?? "";
        const directoryTrim = this.projectSettingsForm.opencode_directory?.trim() ?? "";
        const modelTrim = this.projectSettingsForm.opencode_model?.trim() ?? "";

        const updatePayload: UpdateProjectReq = {
          actor: this.actor(),
          name,
          description: this.projectSettingsForm.description?.trim() || undefined,
          opencode_server_url: serverUrlTrim || (this.projectSettingsBaseline.opencode_server_url ? "" : undefined),
          opencode_directory: directoryTrim || (this.projectSettingsBaseline.opencode_directory ? "" : undefined),
          opencode_model: modelTrim || (this.projectSettingsBaseline.opencode_model ? "" : undefined),
          opencode_agent: agentTrim || (this.projectSettingsBaseline.opencode_agent ? "" : undefined),
          opencode_variant: variantTrim || (this.projectSettingsBaseline.opencode_variant ? "" : undefined),
        };
        await api.updateProject(this.selectedProjectId, updatePayload);
        await this.loadProjects();
        this.closeProjectSettings();
        this.toast("Project updated");
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.projectSettingsBusy = false;
      }
    },

    // ------------------------------------------------------------------- board
    selectedProjectId: safeStorage.getItem(LS_PROJECT) ?? "",
    board: null as Board | null,

    /** IDs of tasks known to be blocked right now — summary rows don't carry
     *  the flag, so it is derived (see refreshBlockedSet) plus learned from
     *  detail fetches (knownBlocked). BOTH structures are scoped to
     *  `blockedCacheProjectId`, the project they were derived/learned under:
     *  readers (isBlocked → card badges + the "Unblocked only" filter) see an
     *  empty cache whenever the scope differs from the selected project, so a
     *  switch can never render a "blocked" indicator based on ANOTHER
     *  project's tasks — including entry points that change selectedProjectId
     *  without going through selectProject() (deep-link / popstate adoption,
     *  archive fallback, boot). */
    blockedIds: new Set<string>(),
    knownBlocked: {} as Record<string, boolean>,
    knownBlockers: {} as Record<string, BlockerRef[]>,
    blockerPopoverTaskId: null as string | null,
    /** Project the blocked-state cache belongs to; "" = nothing derived yet. */
    blockedCacheProjectId: "" as string,

    isBlocked(id: string): boolean {
      // Fail closed: a cache owned by another project counts as empty.
      if (this.blockedCacheProjectId !== this.selectedProjectId) return false;
      if (this.blockedIds.has(id) || !!this.knownBlocked[id]) return true;
      const b = this.board;
      if (b && b.project_id === this.selectedProjectId) {
        for (const s of STATUSES) {
          const t = b.tasks[s]?.find((task) => task.id === id);
          if (t?.blockers && t.blockers.length > 0) return true;
        }
      }
      return false;
    },

    /** Drop every derived/learned blocked entry and re-scope the cache to
     *  `projectId` ("" = fully empty). Called on project switch and whenever
     *  a board payload arrives for a project the cache does not belong to. */
    resetBlockedCache(projectId: string): void {
      this.blockedIds = new Set();
      this.knownBlocked = {};
      this.knownBlockers = {};
      this.blockerPopoverTaskId = null;
      this.blockedCacheProjectId = projectId;
    },

    taskBlockers(task: TaskSummary): BlockerRef[] {
      if (this.blockedCacheProjectId !== this.selectedProjectId) return [];
      if (task.blockers && task.blockers.length > 0) return task.blockers;
      if (this.knownBlockers[task.id] && this.knownBlockers[task.id].length > 0) {
        return this.knownBlockers[task.id];
      }
      return [];
    },

    blockerTooltipText(task: TaskSummary): string {
      const blks = this.taskBlockers(task);
      if (blks.length > 0) {
        return "Blocked by: " + blks.map((b) => b.title + " (" + b.status + ")").join(", ");
      }
      return "Has an open blocker";
    },

    toggleBlockerPopover(taskId: string): void {
      this.blockerPopoverTaskId = this.blockerPopoverTaskId === taskId ? null : taskId;
    },

    subtaskTotal(task: TaskSummary): number {
      return task.subtask_total ?? 0;
    },

    subtaskDone(task: TaskSummary): number {
      return task.subtask_done ?? 0;
    },

    subtaskProgressPercent(task: TaskSummary): number {
      const total = task.subtask_total ?? 0;
      if (total <= 0) return 0;
      const done = task.subtask_done ?? 0;
      return Math.min(100, Math.max(0, Math.round((done / total) * 100)));
    },

    // ------------------------------------------------------ client-side sorting
    /** Purely cosmetic ordering inside each column; the server order is never
     *  changed. Persisted so reloads keep the user's choice (like actor name). */
    sortKey: readStoredSort().key,
    sortDir: readStoredSort().dir,

    /** Change handler: x-model has already written the raw value into sortKey;
     *  normalize it and apply the criterion's default direction. */
    setSort(): void {
      const k: SortKey = isSortKey(this.sortKey) ? this.sortKey : "";
      this.sortKey = k;
      if (k !== "") this.sortDir = DEFAULT_SORT_DIR[k];
      this.saveSort();
    },

    toggleSortDir(): void {
      if (this.sortKey === "") return;
      this.sortDir = this.sortDir === "asc" ? "desc" : "asc";
      this.saveSort();
    },

    clearSort(): void {
      if (this.sortKey === "") return;
      this.sortKey = "";
      this.saveSort();
    },

    saveSort(): void {
      try {
        safeStorage.setItem(LS_SORT, JSON.stringify({ key: this.sortKey, dir: this.sortDir }));
      } catch {
        /* private mode — non-fatal */
      }
    },

    activeSortLabel(): string {
      const o = SORT_OPTIONS.find((x) => x.value === this.sortKey);
      return o && o.value !== "" ? o.label : "";
    },

    // ------------------------------------------------------------------ filters
    filterPriority: "" as string,
    filterAssignee: "" as string,
    filterUnblocked: false,
    filterDue: "" as string,
    filterSearch: "",

    availableAssignees(): string[] {
      const b = this.board;
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
    },

    hasActiveFilters(): boolean {
      return (
        this.filterPriority !== "" ||
        this.filterAssignee !== "" ||
        this.filterUnblocked ||
        this.filterDue !== "" ||
        this.filterSearch.trim() !== ""
      );
    },

    clearFilters(): void {
      this.filterPriority = "";
      this.filterAssignee = "";
      this.filterUnblocked = false;
      this.filterDue = "";
      this.filterSearch = "";
    },

    listTasksParams(): ListTasksParams {
      const p: ListTasksParams = { project_id: this.selectedProjectId };
      if (this.filterPriority !== "") p.priority = Number(this.filterPriority);
      if (this.filterAssignee && this.filterAssignee !== "unassigned") {
        p.assignee_name = this.filterAssignee;
      }
      if (this.filterUnblocked) p.unblocked = true;
      if (this.filterDue === "overdue") {
        p.due_before = new Date().toISOString();
      } else if (this.filterDue === "today") {
        const end = new Date();
        end.setHours(23, 59, 59, 999);
        p.due_before = end.toISOString();
      } else if (this.filterDue === "week") {
        const end = new Date();
        end.setDate(end.getDate() + 7);
        end.setHours(23, 59, 59, 999);
        p.due_before = end.toISOString();
      }
      if (this.filterSearch.trim()) {
        p.search = this.filterSearch.trim();
      }
      return p;
    },

    /** Per-status OPEN counts for the header's project count pill
     *  (todo·doing·done). Derived from board.tasks — the same arrays the
     *  columns render — so the pill can never disagree with the board.
     *  Pre-filter counts by design: it summarizes the whole project, not
     *  the current filtered view. All three keys are always present in
     *  board.tasks ([] when empty), so no undefined guards needed beyond
     *  a missing board. */
    statusCounts(): Record<Status, number> {
      const b = this.board;
      if (!b) return { todo: 0, doing: 0, done: 0 };
      const counts = {} as Record<Status, number>;
      for (const s of STATUSES) counts[s] = b.tasks[s]?.length ?? 0;
      return counts;
    },

    /** Column-header status dot color (To Do zinc, Doing amber, Done emerald). */
    colDotClass(status: Status): string {
      return status === "todo"
        ? "bg-zinc-400"
        : status === "doing"
          ? "bg-amber-500"
          : "bg-emerald-500";
    },

    /** Column-header count pill tint, matching the column's status. */
    colCountPillClass(status: Status): string {
      if (status === "doing")
        return "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800";
      if (status === "done")
        return "bg-emerald-100 text-emerald-800 ring-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:ring-emerald-800";
      return "bg-zinc-100 text-zinc-600 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700";
    },

    columns(): { status: Status; label: string; tasks: TaskSummary[] }[] {
      const b = this.board;
      return STATUSES.map((s) => {
        let tasks = b ? b.tasks[s] ?? [] : [];
        if (this.filterPriority !== "") {
          const p = Number(this.filterPriority);
          tasks = tasks.filter((t) => t.priority === p);
        }
        if (this.filterAssignee === "unassigned") {
          tasks = tasks.filter((t) => !t.assignee || !t.assignee.name);
        } else if (this.filterAssignee !== "") {
          const name = this.filterAssignee.toLowerCase();
          tasks = tasks.filter((t) => t.assignee?.name.toLowerCase() === name);
        }
        if (this.filterUnblocked) {
          tasks = tasks.filter((t) => !this.isBlocked(t.id));
        }
        // Calendar-day comparisons (due dates are days, not instants): "YYYY-MM-DD"
        // sorts lexicographically, so ranges are plain string compares. This keeps
        // the filters TZ-stable and stops past-due tasks leaking into "today".
        if (this.filterDue === "overdue") {
          tasks = tasks.filter((t) => isOverdue(t));
        } else if (this.filterDue === "today" || this.filterDue === "week") {
          const today = localCalendarToday();
          const weekEnd = new Date();
          weekEnd.setDate(weekEnd.getDate() + 7);
          const endExclusive = `${weekEnd.getFullYear()}-${String(weekEnd.getMonth() + 1).padStart(2, "0")}-${String(weekEnd.getDate()).padStart(2, "0")}`;
          tasks = tasks.filter((t) => {
            if (!t.due_at || t.status === "done") return false;
            const due = dueCalendarDate(t.due_at);
            if (!due) return false;
            if (this.filterDue === "today") return due === today;
            return due >= today && due < endExclusive;
          });
        }
        const q = this.filterSearch.trim().toLowerCase();
        if (q) {
          tasks = tasks.filter(
            (t) => t.title.toLowerCase().includes(q) || t.id.toLowerCase().includes(q),
          );
        }
        // Client-side sort: re-applied on every render, so SSE/poll refreshes
        // keep the user's ordering without fighting live updates. Copies —
        // never mutates the board arrays the optimistic-move code owns.
        if (this.sortKey !== "") {
          tasks = [...tasks].sort((a, b) => compareTasks(a, b, this.sortKey, this.sortDir));
        }
        return {
          status: s,
          label: STATUS_LABEL[s],
          tasks,
        };
      });
    },

    selectProject(): void {
      try {
        safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
      } catch { /* ignore */ }
      this.board = null;
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
      this._boardSeq.next();
      this.connectSse();
      void this.loadBoard();
    },

    async loadBoard(silent = false): Promise<void> {
      if (!this.selectedProjectId) return;
      const token = this._boardSeq.next(); // newest initiated board load wins
      try {
        const b = await api.getBoard(this.selectedProjectId);
        // Out-of-order + project-switch protection: a stale response (a newer
        // load started after this one, or the user moved to another project in
        // flight) must never overwrite current state.
        const mayApply = (): boolean =>
          shouldApplyBoard({
            token,
            latestToken: this._boardSeq.latest(),
            payloadProjectId: b.project_id,
            selectedProjectId: this.selectedProjectId,
          });
        if (!mayApply()) return; // stale (newer load started) or foreign project — never overwrite
        this.board = b;
        if (!silent) this.loadError = "";
        await this.refreshBlockedSet(b, () =>
          // The 2×listTasks diff must not write results that belong to a
          // superseded load (project switched or newer load in flight).
          mayApply(),
        );
      } catch (e) {
        if (!silent) this.loadError = errMsg(e);
        if (ApiError.isUnauthorized(e)) {
          this.openAuthModal();
        }
      }
    },

    /** Derive which open tasks are blocked without N extra calls: one
     *  list_tasks plus the same call with unblocked=true, then diff by id.
     *  Writes are ownership-gated via `isStillCurrent`, so a superseded load
     *  (project switched or a newer board load in flight) never lands its
     *  diff — and everything written is re-scoped to the payload's project
     *  (blockedCacheProjectId), so another project's set can never survive a
     *  switch. Projects with more than `limit` open tasks can't be diffed
     *  cheaply: for those the cache is re-scoped to that project and left
     *  EMPTY for it instead of inheriting whatever was derived before;
     *  per-task detail loads (applyDetail → knownBlocked) still fill in
     *  individual badges. */
    async refreshBlockedSet(b: Board, isStillCurrent?: () => boolean): Promise<void> {
      const stillCurrent = (): boolean => (isStillCurrent ? isStillCurrent() : true);
      const total = STATUSES.reduce((n, s) => n + (b.tasks[s]?.length ?? 0), 0);
      if (total === 0) {
        // An empty board owns an EMPTY cache — anything derived under a
        // different project is stale by definition.
        if (stillCurrent()) this.resetBlockedCache(b.project_id);
        return;
      }
      const limit = 100;
      try {
        const [all, un] = await Promise.all([
          api.listTasks({ project_id: b.project_id, limit }),
          api.listTasks({ project_id: b.project_id, unblocked: true, limit }),
        ]);
        if (!stillCurrent()) return; // superseded — keep whatever we had
        if (all.total > limit || un.total > limit) {
          // Too big to diff cheaply. A DIFFERENT project's set must never be
          // retained here — that is exactly the cross-project leak this cache
          // guards against: re-scope to this project and start empty. If the
          // cache already belongs to THIS project (repeated loads of the same
          // large board), its entries stay valid and are kept.
          if (this.blockedCacheProjectId !== b.project_id) {
            this.resetBlockedCache(b.project_id);
          }
          return;
        }
        const unIds = new Set(un.tasks.map((t) => t.id));
        this.blockedIds = new Set(
          all.tasks.filter((t) => !unIds.has(t.id)).map((t) => t.id),
        );
        this.blockedCacheProjectId = b.project_id;
      } catch {
        // Transient network failure: keep whatever we had (deliberate — an
        // advisory feature should not lose good data on a blip). Safe, because
        // the cache is project-scoped and isBlocked() only serves entries
        // owned by the CURRENTLY selected project: this can never surface a
        // different project's blocked state.
      }
    },

    // ------------------------------------------------------------ realtime SSE
    sse: null as EventSource | null,
    sseStatus: "disconnected" as SseConnectionStatus,
    sseBackoffMs: 2000,
    sseReconnectTimer: 0 as number,
    // Replay cursor (server-side Last-Event-ID): the last frame id received on
    // THIS connection + the project it belongs to. Reconnects send it as the
    // last_event_id query param so the server replays events missed while we
    // were disconnected; the project match guards against replaying an old
    // project's backlog after a switch.
    sseLastEventId: "" as string,
    sseLastEventProjectId: "" as string,
    /** TEST-SUPPORT: reference to the module-level SSE singleton counters so
     *  browser tests can read them via Alpine.$data(root)._sseTelemetry
     *  (see sseTelemetry above). Carried on the component instead of a window
     *  flag so shipped bundles stay free of global test hooks. */
    _sseTelemetry: sseTelemetry,

    connectSse(): void {
      this.disconnectSse(false);

      if (typeof window === "undefined" || typeof EventSource === "undefined") {
        return;
      }

      if (!this.selectedProjectId) {
        this.sseStatus = "disconnected";
        return;
      }

      this.sseStatus = "connecting";

      const token = this.authToken || api.getAuthToken();
      const params: string[] = [];
      if (token) {
        params.push(`token=${encodeURIComponent(token)}`);
      }
      // This client reconnects manually on error (see handleSseDisconnect), so
      // the browser's native Last-Event-ID auto-reconnect never fires — pass the
      // cursor ourselves and the server replays whatever we missed.
      if (this.sseLastEventId && this.sseLastEventProjectId === this.selectedProjectId) {
        params.push(`last_event_id=${encodeURIComponent(this.sseLastEventId)}`);
      }
      const url = `/api/v1/projects/${encodeURIComponent(this.selectedProjectId)}/events${
        params.length ? `?${params.join("&")}` : ""
      }`;

      let es: EventSource;
      try {
        es = new EventSource(url);
      } catch {
        this.handleSseDisconnect();
        return;
      }

      this.sse = es;
      sseTelemetry.opened++; // TEST-SUPPORT: count every constructed stream

      es.onopen = () => {
        if (this.sse === es) {
          this.sseStatus = "connected";
          this.sseBackoffMs = 2000;
        }
      };

      const onEvent = (e: MessageEvent) => {
        if (this.sse !== es) return;
        this.handleSseEvent(e);
      };

      for (const kind of SSE_EVENT_KINDS) {
        es.addEventListener(kind, onEvent);
      }
      es.onmessage = onEvent;

      es.onerror = () => {
        if (this.sse !== es) return;
        this.handleSseDisconnect();
      };
    },

    handleSseEvent(e: MessageEvent): void {
      try {
        if (!e.data) return;
        const data: RealtimeEventPayload = JSON.parse(e.data);

        // Project scoping check
        if (data.project_id && data.project_id !== this.selectedProjectId) {
          return;
        }

        // Advance the replay cursor: MessageEvent.lastEventId carries the SSE
        // frame id, which is exactly what a reconnect needs to catch up.
        if (e.lastEventId) {
          this.sseLastEventId = e.lastEventId;
          this.sseLastEventProjectId = this.selectedProjectId;
        }

        // Coalesced silent reload: a burst of events (agent batch ops) within
        // SSE_RELOAD_DEBOUNCE_MS schedules ONE board + ONE projects round after
        // the quiet period instead of N parallel ones; each individual event
        // still refreshes at most once per window. (The drawer's in-place task
        // refresh below stays per-event — its race is a separate concern.)
        this.sseReloadDebounce?.schedule();

        // If the task detail drawer is currently open for the event's task_id,
        // re-fetch the task detail in-place (api.getTask(data.task_id)) to show
        // new comments, notes, claims, and status changes instantly.
        if (
          data.task_id &&
          this.detailOpen &&
          (this.detailId === data.task_id || this.detail?.id === data.task_id)
        ) {
          void this.refreshDetail(true, data.task_id).then((applied) => {
            // refreshDetail now returns false when its payload was discarded by
            // the ownership guard (drawer closed or switched mid-flight) — and
            // even for an applied payload we re-verify at settle time, so a
            // stale SSE event can never trigger comment side effects after the
            // drawer closed or moved on.
            if (!applied) return;
            if (this.detailOpen && this.detail && this.detail.id === data.task_id) {
              void this.initComments(data.task_id);
            }
          });
        }
      } catch {
        /* malformed SSE payload — non-fatal */
      }
    },

    handleSseDisconnect(): void {
      if (this.sse) {
        this.sse.close();
        this.sse = null;
        sseTelemetry.closed++; // TEST-SUPPORT: a live stream was actually closed
      }
      this.sseStatus = "connecting";

      if (this.sseReconnectTimer) {
        window.clearTimeout(this.sseReconnectTimer);
        this.sseReconnectTimer = 0;
      }

      const delay = this.sseBackoffMs;
      // Exponential backoff: 2s, 4s, 8s, up to 16s
      this.sseBackoffMs = Math.min(this.sseBackoffMs * 2, 16000);

      this.sseReconnectTimer = window.setTimeout(() => {
        this.sseReconnectTimer = 0;
        if (this.selectedProjectId) {
          this.connectSse();
        } else {
          this.sseStatus = "disconnected";
        }
      }, delay);
    },

    disconnectSse(resetBackoff = true): void {
      if (this.sseReconnectTimer) {
        window.clearTimeout(this.sseReconnectTimer);
        this.sseReconnectTimer = 0;
      }
      if (this.sse) {
        this.sse.close();
        this.sse = null;
        sseTelemetry.closed++; // TEST-SUPPORT: a live stream was actually closed
      }
      if (resetBackoff) {
        this.sseBackoffMs = 2000;
        this.sseStatus = "disconnected";
        // Intentional teardown (project switch, logout): drop the replay cursor
        // so a later reconnect starts fresh instead of bursting a stale
        // backlog. Error-driven reconnects go through disconnectSse(false) and
        // keep the cursor on purpose.
        this.sseLastEventId = "";
        this.sseLastEventProjectId = "";
      }
    },

    // ------------------------------------------------------------------ polling
    /** Lightweight ~15s poll of board (+ open detail + project counts). No websockets. */
    polling: true,
    pollTimer: 0 as number,

    pollingChanged(): void {
      if (this.polling) this.startPolling();
      else this.stopPolling();
    },

    startPolling(): void {
      this.stopPolling();
      this.pollTimer = window.setInterval(() => void this.pollTick(), POLL_MS);
    },

    stopPolling(): void {
      if (this.pollTimer) {
        window.clearInterval(this.pollTimer);
        this.pollTimer = 0;
      }
    },

    async pollTick(): Promise<void> {
      if (document.hidden || !this.selectedProjectId) return;
      await this.loadProjects(true);
      await this.loadBoard(true);
      if (this.detailOpen && this.detail) {
        await this.refreshDetail(true);
        void this.initComments(this.detail.id);
      }
    },

    // ---------------------------------------------------------------- new task
    showNewTask: false,
    newTaskTarget: "todo" as Status,
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
    /** Focus session for the new-task modal (trap + restore). */
    newTaskFocus: null as DialogFocus | null,

    openNewTask(status: Status = "todo"): void {
      this.newTaskTarget = status;
      this.newTask = {
        title: "",
        description: "",
        priority: 0,
        due: "",
        acs: "",
        assigneeName: "",
        assigneeType: "human",
      };
      this.duplicates = [];
      this.showNewTask = true;
      // First form FIELD first (not the submit button) per a11y convention.
      this.newTaskFocus = this.openDialog('cb-new-task', 'input[x-model="newTask.title"]');
    },

    /** Single close path for the new-task modal (create success). User-initiated
     *  dismissals (× / Cancel / backdrop / Esc) must go through
     *  requestCloseNewTask() so a half-typed draft is never lost. */
    closeNewTask(): void {
      this.showNewTask = false;
      this.duplicates = [];
      this.closeDialog(this.newTaskFocus);
      this.newTaskFocus = null;
    },

    /** True when the new-task form holds any content — openNewTask() resets
     *  every field, so a freshly opened dialog is never dirty. */
    newTaskDirty(): boolean {
      const n = this.newTask;
      return Boolean(
        n.title.trim() ||
          n.description.trim() ||
          n.due !== "" ||
          n.priority !== 0 ||
          n.assigneeName.trim() ||
          n.assigneeType !== "human" ||
          n.acs.trim(),
      );
    },

    /** Guarded close for the new-task dialog (× / Cancel / backdrop / Esc):
     *  asks first when a draft would be lost. createTask() closes
     *  UNCONDITIONALLY via closeNewTask() after a successful POST. */
    requestCloseNewTask(): void {
      if (
        this.newTaskDirty() &&
        !this.confirmDiscard("Discard this new task draft? Unsaved details will be lost.")
      )
        return;
      this.closeNewTask();
    },

    clearDuplicates(): void {
      this.duplicates = [];
    },

    async createTask(): Promise<void> {
      const title = this.newTask.title.trim();
      if (this.taskBusy || !title || !this.selectedProjectId) return;
      this.taskBusy = true;
      let created: TaskSummary | null = null;
      try {
        const assigneeName = this.newTask.assigneeName.trim();
        const res = await api.createTask({
          actor: this.actor(),
          title,
          description: this.newTask.description.trim() || undefined,
          project_id: this.selectedProjectId,
          priority: this.newTask.priority,
          due_at: dateInputToIso(this.newTask.due),
          assignee: assigneeName ? { name: assigneeName, type: this.newTask.assigneeType } : undefined,
          acceptance_criteria: splitLines(this.newTask.acs),
        });
        created = res.task;
        // Target column other than To Do: the create lands in todo (v1) and we
        // follow up with ONE version-guarded move. A race here is advisory —
        // the next poll settles the truth.
        if (this.newTaskTarget !== "todo") {
          try {
            await api.updateTask(created.id, {
              actor: this.actor(),
              version: created.version,
              status: this.newTaskTarget,
            });
          } catch (e2) {
            if (!ApiError.isVersionConflict(e2)) throw e2;
            this.toast("New task changed elsewhere before the move — check its column.", "info");
          }
        }
        this.duplicates = res.possible_duplicates ?? [];
        this.closeNewTask();
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.taskBusy = false;
        if (created) void Promise.all([this.loadProjects(), this.loadBoard(true)]);
        else void this.loadBoard(true); // board may have changed even on failure paths we care about
      }
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
        else window.history.replaceState(null, "", "/");
      }
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
              window.history.replaceState(null, "", "/");
            }
          })();
        } else {
          // Forward navigation onto the already-open task: just re-arm page mode.
          this.fullPageOpen = true;
          this.fullPagePushed = false;
        }
      } else if (this.fullPageOpen) {
        this.closeDetail();
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
        if (typeof window !== "undefined") window.history.replaceState(null, "", "/");
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
     *  http deployments where it is unavailable (see copyViaExecCommand). */
    async copyTaskId(id: string): Promise<void> {
      if (!id) return;
      let copied = false;
      try {
        if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(id);
          copied = true;
        } else {
          copied = copyViaExecCommand(id);
        }
      } catch {
        copied = false; // e.g. permission denied — try the legacy path once
      }
      if (!copied) copied = copyViaExecCommand(id);
      this.toast(copied ? "Copied task ID to clipboard" : "Couldn't copy the task ID", copied ? "info" : "error");
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
      if (t.comments.length > this.totalComments) {
        this.totalComments = t.comments.length;
      }
      this.assigneeInputName = t.assignee?.name ?? "";
      this.assigneeInputType = (t.assignee?.type as "human" | "agent") || "human";
      // Learn the blocked state for card badges (the board diff is authoritative
      // on poll; details fill in between polls and cover >100-task projects).
      // Only while the task's project IS the selected one: a detail refresh
      // that outlives a project switch must neither add entries to nor delete
      // from the cache of the project now on screen — board loads own the
      // scope (see blockedCacheProjectId / selectProject).
      if (t.project_id === this.selectedProjectId) {
        const kb = { ...this.knownBlocked };
        const kblks = { ...this.knownBlockers };
        if (t.blocked) {
          kb[t.id] = true;
          kblks[t.id] = t.blockers || [];
        } else {
          delete kb[t.id];
          delete kblks[t.id];
        }
        this.knownBlocked = kb;
        this.knownBlockers = kblks;
      }
      // Never clobber an in-progress edit of THIS task; a scratchpad that
      // belongs to a DIFFERENT task is discarded so drafts cannot leak across
      // tasks (AC 2).
      if (!(this.notesDirty && this.notesDraftOwner === t.id)) {
        this.notesDraft = t.notes;
        this.notesDirty = false;
        this.notesDraftOwner = t.id;
      }
    },

    // ------------------------------------------------------------ assignee
    assigneeInputName: "",
    assigneeInputType: "human" as "human" | "agent",
    assigneeBusy: false,

    async saveAssignee(): Promise<void> {
      this.ownerPopoverOpen = false;
      const d = this.detail;
      if (!d || this.assigneeBusy) return;
      const name = this.assigneeInputName.trim();
      const type = this.assigneeInputType || "human";
      await this.updateAssignee(name, type);
    },

    async clearAssignee(): Promise<void> {
      this.ownerPopoverOpen = false;
      this.assigneeInputName = "";
      await this.updateAssignee("", "human");
    },

    async updateAssignee(name: string, type: "human" | "agent" = "human"): Promise<void> {
      const d = this.detail;
      if (!d || this.assigneeBusy) return;
      this.assigneeBusy = true;
      const trimmed = name.trim();
      const req: UpdateTaskReq = {
        actor: this.actor(),
        version: d.version,
      };
      if (trimmed) {
        req.assignee = { name: trimmed, type };
      } else {
        req.clear_assignee = true;
      }
      try {
        const updated = await api.updateTask(d.id, req);
        if (this.detail && this.detail.id === d.id) {
          this.detail.assignee = updated.assignee;
          this.detail.version = updated.version;
          this.detail.updated_at = updated.updated_at;
          this.assigneeInputName = updated.assignee?.name ?? "";
          this.assigneeInputType = (updated.assignee?.type as "human" | "agent") || "human";
        }
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
        this.toast(trimmed ? `Assigned to ${trimmed}` : "Assignee cleared");
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          try {
            const fresh = await api.getTask(d.id);
            this.applyDetail(fresh);
            const retryReq: UpdateTaskReq = {
              actor: this.actor(),
              version: fresh.version,
              assignee: trimmed ? { name: trimmed, type } : undefined,
              clear_assignee: !trimmed,
            };
            const updated = await api.updateTask(d.id, retryReq);
            if (this.detail && this.detail.id === d.id) {
              this.detail.assignee = updated.assignee;
              this.detail.version = updated.version;
              this.detail.updated_at = updated.updated_at;
              this.assigneeInputName = updated.assignee?.name ?? "";
              this.assigneeInputType = (updated.assignee?.type as "human" | "agent") || "human";
            }
            await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
            this.toast(trimmed ? `Assigned to ${trimmed}` : "Assignee cleared");
          } catch (e2) {
            this.toast(errMsg(e2), "error");
          }
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.assigneeBusy = false;
      }
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

    startEditing(): void {
      const t = this.detail;
      if (!t) return;
      this.editDraft = {
        title: t.title ?? "",
        description: t.description ?? "",
        priority: t.priority ?? 0,
        // Calendar day via the shared helper — always equals what the card
        // shows, and re-saving it round-trips to the same stored day.
        due: t.due_at ? dueCalendarDate(t.due_at) : "",
        assigneeName: t.assignee?.name ?? "",
        assigneeType: (t.assignee?.type as "human" | "agent") || "human",
      };
      // Snapshot FIRST (server truth), then layer the local draft on top — see below.
      this.editDraftOriginal = { ...this.editDraft };
      const savedDesc = safeStorage.getItem(descriptionDraftKey(t.id));
      if (savedDesc !== null && savedDesc.trim() !== "") {
        // Draft auto-save restore (AC 2): the user's unsaved local draft wins
        // over the server value, while editDraftOriginal keeps the SERVER truth
        // so dirty-detection and the discard guard stay correct.
        this.editDraft.description = savedDesc;
      }
      // A fresh edit session always starts in Write mode (AC 1).
      this.descShowPreview = false;
      this.editingTask = true;
    },

    cancelEditing(): void {
      this.editingTask = false;
      // The Cancel button is an explicit discard of the in-flight edit — drop
      // the local description draft with it (AC 3). Idempotent if the draft was
      // already cleared by a confirmed-discard path.
      this.clearDescriptionDraft();
    },

    /** True when the in-flight task edit differs from the values loaded when
     *  edit mode started. Gated on editingTask so a stale snapshot can never
     *  fire once save/cancel has exited edit mode. */
    editDraftDirty(): boolean {
      if (!this.editingTask) return false;
      const d = this.editDraft;
      const o = this.editDraftOriginal;
      return (
        d.title !== o.title ||
        d.description !== o.description ||
        d.priority !== o.priority ||
        d.due !== o.due ||
        d.assigneeName !== o.assigneeName ||
        d.assigneeType !== o.assigneeType
      );
    },

    /** Ask before throwing away an in-flight task edit (shared by the drawer's
     *  close path and Escape). True = safe to proceed with the discard. */
    discardEditDraftIfConfirmed(): boolean {
      if (!this.editDraftDirty()) return true;
      const ok = this.confirmDiscard("Discard unsaved task edits? Changes to this task will be lost.");
      // An explicitly CONFIRMED discard is exactly when the local description
      // draft stops existing (AC 3): it lives in storage, so leaving it would
      // resurrect itself on the next startEditing().
      if (ok) this.clearDescriptionDraft();
      return ok;
    },

    /** Debounced textarea-input handler (@input.debounce.400ms): persists the
     *  CURRENT description draft under the loaded task's id (AC 2). No task
     *  loaded → no-op. Blank value → removes the key so nothing stale lingers. */
    saveDescriptionDraft(): void {
      const id = this.detail?.id;
      if (!id) return;
      const raw = this.editDraft.description ?? "";
      if (raw.trim() !== "") safeStorage.setItem(descriptionDraftKey(id), raw);
      else safeStorage.removeItem(descriptionDraftKey(id));
    },

    /** Drop the loaded task's description draft from storage (AC 3). No-op when
     *  no task is loaded; safe to call repeatedly. */
    clearDescriptionDraft(): void {
      const id = this.detail?.id;
      if (!id) return;
      safeStorage.removeItem(descriptionDraftKey(id));
    },

    async saveTaskEdit(): Promise<void> {
      const t = this.detail;
      const title = this.editDraft.title.trim();
      if (!t || !title || this.savingEdit) return;

      // Captured BEFORE any await: the draft that gets cleared on success is
      // the one for THIS task, even if detail moves during the flight (AC 3).
      const descKey = descriptionDraftKey(t.id);
      this.savingEdit = true;
      const req: UpdateTaskReq = {
        actor: this.actor(),
        version: t.version,
        title,
        description: this.editDraft.description.trim(),
        priority: this.editDraft.priority,
      };

      const dueIso = dateInputToIso(this.editDraft.due);
      if (dueIso) {
        req.due_at = dueIso;
      } else if (t.due_at) {
        req.clear_due = true;
      }

      const assigneeName = this.editDraft.assigneeName.trim();
      if (assigneeName) {
        req.assignee = {
          name: assigneeName,
          type: this.editDraft.assigneeType || "human",
        };
      } else if (t.assignee) {
        req.clear_assignee = true;
      }

      try {
        await api.updateTask(t.id, req);
        this.editingTask = false;
        // Save succeeded → the description is server truth now; the local draft
        // must not survive (AC 3).
        safeStorage.removeItem(descKey);
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          try {
            const fresh = await api.getTask(t.id, { commentLimit: 50 });
            this.applyDetail(fresh);
            // Re-apply once using fresh version while preserving user's editDraft
            const retryReq: UpdateTaskReq = {
              ...req,
              version: fresh.version,
              assignee: assigneeName
                ? { name: assigneeName, type: this.editDraft.assigneeType || "human" }
                : undefined,
              clear_assignee: !assigneeName && !!fresh.assignee,
            };
            await api.updateTask(t.id, retryReq);
            this.editingTask = false;
            // 409-retry also landed the save → clear the draft exactly once.
            safeStorage.removeItem(descKey);
            await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
            this.toast("Changed elsewhere — retried with the fresh version.", "info");
          } catch (e2) {
            await this.refreshDetail(true).catch(() => undefined);
            // Stay in edit mode with editDraft preserved across conflict
            if (ApiError.isVersionConflict(e2)) {
              this.toast("Changed elsewhere — refreshed (your edit was not applied).", "info");
            } else {
              this.toast(errMsg(e2), "error");
            }
          }
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.savingEdit = false;
      }
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
      return this.draggedFromStatus ? canMove(this.draggedFromStatus, status) : true;
    },

    onDragStart(e: DragEvent, task: TaskSummary, fromStatus: Status): void {
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
        this.toast(`Can't move ${statusLabel(fromStatus)} → ${statusLabel(targetStatus)} — that's not an allowed step.`, "info");
        return;
      }
      await this.moveTaskStatus(taskId, targetStatus);
    },

    handleCardClick(id: string): void {
      if (this.isDragging) return;
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
      return this.swiping(id) && this.swipeState.dx < 0 ? "left" : "right";
    },

    swipeTargetLabel(id: string): string {
      const from = this.swipeState.fromStatus;
      if (!from) return "";
      const idx = STATUSES.indexOf(from);
      const target =
        this.swipeDirection(id) === "right" ? STATUSES[idx + 1] : STATUSES[idx - 1];
      return target ? statusLabel(target) : "";
    },

    onCardTouchStart(e: TouchEvent, task: TaskSummary): void {
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
      if (!s.active && Math.abs(dx) > 24 && Math.abs(dx) > 1.5 * Math.abs(dy)) {
        s.active = true;
      }
      if (s.active) {
        s.dx = dx;
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
      const idx = STATUSES.indexOf(s.fromStatus);
      const target: Status | null =
        dx > 48 ? (STATUSES[idx + 1] ?? null) : dx < -48 ? (STATUSES[idx - 1] ?? null) : null;
      if (!target) return;
      // Swipes move exactly one column at a time. The only adjacent pair that
      // the domain transition map rejects is done → doing (left-swipe on a Done
      // card); refuse it with feedback instead of hitting the 400 + rollback.
      if (!canMove(s.fromStatus, target)) {
        this.toast(`Can't move ${statusLabel(s.fromStatus)} → ${statusLabel(target)} — that's not an allowed step.`, "info");
        return;
      }
      void this.moveTaskStatus(task.id, target);
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
    },

    toggleOwnerPopover(): void {
      this.ownerPopoverOpen = !this.ownerPopoverOpen;
      this.statusMenuOpen = false;
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

    /** Toggle one criterion; the 200 response carries the updated AC row, which
     *  is merged back into detail (authoritative). `ev` is the change event —
     *  on a FAILED toggle it lets us revert the native checkbox: the click flips
     *  its .checked before we know the server said no, and ac.done is never
     *  mutated on failure, so Alpine's :checked binding has no diff to re-sync
     *  and would otherwise leave the box visually checked (split state). */
    async toggleAc(ac: AcRow, ev?: Event): Promise<void> {
      const t = this.detail;
      if (!t || this.acBusySeq !== 0) return;
      this.acBusySeq = ac.seq;
      try {
        const updated = await api.toggleAc(t.id, ac.seq, this.actor(), !ac.done);
        const d = this.detail; // may have been refreshed meanwhile — only patch ours
        if (d && d.id === t.id) {
          this.detail = {
            ...d,
            acceptance_criteria: d.acceptance_criteria.map((x) =>
              x.seq === updated.seq ? updated : x,
            ),
          };
        }
      } catch (e) {
        // ac.done is still its pre-click value — put the DOM back in line with it.
        // Duck-typed on purpose: unit tests drive this path with a plain fake
        // event object (no DOM globals exist under node:test).
        const el = ev?.target as unknown as { checked?: boolean } | null;
        if (el && typeof el.checked === "boolean") el.checked = ac.done;
        this.toast(errMsg(e), "error");
      } finally {
        this.acBusySeq = 0;
      }
    },

    async addAc(): Promise<void> {
      const t = this.detail;
      const text = this.newAcText.trim();
      if (!t || !text || this.addingAc) return;
      this.addingAc = true;
      try {
        const fresh = await api.addAc(t.id, text, this.actor());
        const d = this.detail;
        if (d && d.id === t.id) {
          this.detail = {
            ...d,
            acceptance_criteria: [...d.acceptance_criteria, fresh],
          };
        }
        this.newAcText = "";
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.addingAc = false;
      }
    },

    startEditingAc(ac: AcRow): void {
      this.editingAcSeq = ac.seq;
      this.editingAcText = ac.text;
    },

    cancelEditingAc(): void {
      this.editingAcSeq = 0;
      this.editingAcText = "";
    },

    async saveAcEdit(seq: number): Promise<void> {
      const t = this.detail;
      const text = this.editingAcText.trim();
      if (!t || !text || this.savingAcSeq !== 0) return;
      this.savingAcSeq = seq;
      try {
        const updated = await api.editAcText(t.id, seq, text, this.actor());
        const d = this.detail;
        if (d && d.id === t.id) {
          this.detail = {
            ...d,
            acceptance_criteria: d.acceptance_criteria.map((x) =>
              x.seq === seq ? updated : x,
            ),
          };
        }
        this.editingAcSeq = 0;
        this.editingAcText = "";
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.savingAcSeq = 0;
      }
    },

    async deleteAc(seq: number): Promise<void> {
      const t = this.detail;
      if (!t || this.deletingAcSeq !== 0) return;
      this.deletingAcSeq = seq;
      try {
        await api.deleteAc(t.id, seq, this.actor());
        const d = this.detail;
        if (d && d.id === t.id) {
          this.detail = {
            ...d,
            acceptance_criteria: d.acceptance_criteria.filter((x) => x.seq !== seq),
          };
        }
        if (this.editingAcSeq === seq) {
          this.cancelEditingAc();
        }
        this.toast("Acceptance criterion removed");
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.deletingAcSeq = 0;
      }
    },

    acDoneCount(): number {
      return this.detail ? this.detail.acceptance_criteria.filter((a) => a.done).length : 0;
    },

    /** One SVG <circle> per criterion for the AC progress ring. Each sits on a
     *  100-unit circle (r = 15.915 in a 36x36 viewBox), so dash/offset map
     *  straight onto stroke-dasharray/stroke-dashoffset. Done slots fill, open
     *  ones stay hollow; the inter-slot gap shrinks as N grows so many criteria
     *  still read as distinct segments. Returned as a string (not x-for) because
     *  Alpine's x-for cannot anchor nodes inside inline SVG. Only numeric values
     *  and two fixed class tokens are interpolated — no user text — so this is
     *  safe to pass to :innerHTML on the <g>. */
    acRingInner(): string {
      const acs = this.detail ? this.detail.acceptance_criteria : [];
      const n = acs.length;
      if (n === 0) return "";
      const slot = 100 / n;
      const gap = n > 1 ? Math.min(2, slot * 0.35) : 0;
      const dash = Math.max(slot - gap, 0.5);
      return acs
        .map((ac, i) => {
          const cls = ac.done ? "stroke-emerald-500" : "stroke-zinc-300 dark:stroke-zinc-600";
          return (
            `<circle cx="18" cy="18" r="15.915" fill="none" stroke-width="6" ` +
            `class="${cls}" stroke-dasharray="${dash.toFixed(3)} ${(100 - dash).toFixed(3)}" ` +
            `stroke-dashoffset="${(-i * slot).toFixed(3)}"></circle>`
          );
        })
        .join("");
    },

    acProgressLabel(): string {
      const total = this.detail ? this.detail.acceptance_criteria.length : 0;
      return `${this.acDoneCount()} of ${total} acceptance criteria complete`;
    },

    completedLabel(ac: AcRow): string {
      const who = ac.completed_by ? actorLabel(ac.completed_by) : "unknown";
      const when = ac.completed_at ? relTime(ac.completed_at) : "";
      return `${who}${when ? ` · ${when}` : ""}`;
    },

    // ----------------------------------------------------------------- comments
    commentForm: { kind: "progress" as CommentKind, body: "" },
    /** Write/Preview toggle for the COMMENT markdown field (always present in
     *  the drawer). Default = Write; reset on every openTask() (AC 1). */
    commentShowPreview: false,
    postingComment: false,
    loadingOlderComments: false,
    totalComments: 0,

    /** Reset-then-restore for a task OPEN (AC 2): the form goes back to
     *  defaults FIRST so one task's unsaved comment can never leak into the
     *  next, then a stored draft under THIS id (JSON body+kind) is re-applied.
     *  Called from openTask(), which every drawer/full-page open funnels
     *  through — drafts stay in storage across close/navigation/reload. */
    restoreCommentDraftForTask(id: string): void {
      this.commentShowPreview = false;
      this.commentForm = { kind: "progress", body: "" };
      const parsed = deserializeCommentDraft(safeStorage.getItem(commentDraftKey(id)));
      if (parsed) this.commentForm = { body: parsed.body, kind: parsed.kind };
    },

    /** Debounced textarea-input handler (@input.debounce.400ms): persists the
     *  CURRENT comment draft (body + selected type as JSON) under the loaded
     *  task's id (AC 2). No task loaded → no-op; blank body → key removed. */
    saveCommentDraft(): void {
      const id = this.detail?.id;
      if (!id) return;
      const raw = this.commentForm.body ?? "";
      if (raw.trim() !== "") safeStorage.setItem(commentDraftKey(id), serializeCommentDraft({ body: raw, kind: this.commentForm.kind }));
      else safeStorage.removeItem(commentDraftKey(id));
    },

    hasMoreOlderComments(): boolean {
      return !!this.detail && this.totalComments > this.detail.comments.length;
    },

    olderCommentsRemaining(): number {
      return Math.max(0, this.totalComments - (this.detail?.comments.length ?? 0));
    },

    async initComments(id: string): Promise<void> {
      try {
        const res = await api.listComments(id, { limit: 50, offset: 0 });
        if (this.detail && this.detail.id === id) {
          this.totalComments = res.total;
          this.mergeComments(res.comments);
        }
      } catch {
        /* non-fatal */
      }
    },

    mergeComments(incoming: CommentRow[]): void {
      if (!this.detail) return;
      const map = new Map<string, CommentRow>();
      for (const c of this.detail.comments) {
        map.set(c.id, c);
      }
      for (const c of incoming) {
        map.set(c.id, c);
      }
      const sorted = Array.from(map.values()).sort((a, b) => {
        const ta = Date.parse(a.created_at);
        const tb = Date.parse(b.created_at);
        if (ta !== tb) return ta - tb;
        return a.id.localeCompare(b.id);
      });
      this.detail.comments = sorted;
      if (this.detail.comments.length > this.totalComments) {
        this.totalComments = this.detail.comments.length;
      }
    },

    async loadOlderComments(): Promise<void> {
      const d = this.detail;
      if (!d || this.loadingOlderComments) return;
      this.loadingOlderComments = true;
      try {
        const res = await api.listComments(d.id, {
          offset: d.comments.length,
          limit: 50,
        });
        this.totalComments = res.total;
        if (this.detail && this.detail.id === d.id) {
          this.mergeComments(res.comments);
        }
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.loadingOlderComments = false;
      }
    },

    /** Append a typed comment (no task version bump). Appends are NOT
     *  idempotent — on network error we don't auto-retry. */
    async postComment(): Promise<void> {
      const t = this.detail;
      const body = this.commentForm.body.trim();
      if (!t || !body || this.postingComment) return;
      this.postingComment = true;
      try {
        const c = await api.addComment(t.id, {
          actor: this.actor(),
          kind: this.commentForm.kind,
          body,
        });
        const d = this.detail;
        if (d && d.id === t.id) {
          this.mergeComments([c]);
          this.totalComments = Math.max(this.totalComments + 1, d.comments.length);
        }
        this.commentForm.body = "";
        // Comment posted → the local draft has served its purpose; clear it so
        // re-opening this task never resurrects text already on screen (AC 3).
        safeStorage.removeItem(commentDraftKey(t.id));
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.postingComment = false;
      }
    },

    // ---------------------------------------------------------------- artifacts
    artifactForm: { open: false, kind: "url" as ArtifactKind, ref: "", summary: "", file: null as File | null },
    attachingArtifact: false,
    previewModal: { open: false, src: "", alt: "" },

    onArtifactFileSelected(event: Event): void {
      const target = event.target as HTMLInputElement;
      const file = target.files?.[0] || null;
      this.artifactForm.file = file;
      if (file) {
        if (file.type.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(file.name)) {
          this.artifactForm.kind = "screenshot";
        } else {
          this.artifactForm.kind = "other";
        }
      }
    },

    resetArtifactForm(): void {
      this.artifactForm = { open: false, kind: "url", ref: "", summary: "", file: null };
      const fileInput = document.getElementById("artifactFileInput") as HTMLInputElement | null;
      if (fileInput) fileInput.value = "";
    },

    async attachArtifact(): Promise<void> {
      const t = this.detail;
      const file = this.artifactForm.file;
      const ref = this.artifactForm.ref.trim();
      if (!t || (!file && !ref) || this.attachingArtifact) return;
      this.attachingArtifact = true;
      try {
        let a: ArtifactRow;
        if (file) {
          a = await api.uploadArtifact(
            t.id,
            file,
            this.artifactForm.kind,
            this.artifactForm.summary.trim() || undefined,
            this.actor(),
          );
        } else {
          a = await api.addArtifact(
            t.id,
            this.artifactForm.kind,
            ref,
            this.artifactForm.summary.trim() || undefined,
            this.actor(),
          );
        }
        const d = this.detail;
        if (d && d.id === t.id) this.detail = { ...d, artifacts: [...d.artifacts, a] };
        this.resetArtifactForm();
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.attachingArtifact = false;
      }
    },

    openImagePreview(src: string, alt: string): void {
      this.previewModal = { open: true, src, alt };
      const dialog = document.getElementById("artifactPreviewDialog") as HTMLDialogElement | null;
      if (dialog && typeof dialog.showModal === "function") {
        if (!dialog.open) {
          dialog.showModal();
        }
      }
    },

    closeImagePreview(): void {
      this.previewModal.open = false;
      const dialog = document.getElementById("artifactPreviewDialog") as HTMLDialogElement | null;
      if (dialog && dialog.open && typeof dialog.close === "function") {
        dialog.close();
      }
    },

    isStoredArtifact(ref: string | null | undefined): boolean {
      return typeof ref === "string" && (ref.startsWith("/artifacts/") || ref.startsWith("/api/v1/artifacts/"));
    },

    isStoredImage(a: { kind: string; ref: string; summary?: string }): boolean {
      if (!this.isStoredArtifact(a.ref)) return false;
      if (a.kind === "screenshot") return true;
      const imageExt = /\.(png|jpe?g|gif|webp|svg|bmp|avif)\b/i;
      return imageExt.test(a.ref) || imageExt.test(a.summary || "");
    },

    isStoredVideo(a: { kind: string; ref: string; summary?: string }): boolean {
      if (!this.isStoredArtifact(a.ref)) return false;
      const videoExt = /\.(mp4|webm|ogg|mov)\b/i;
      return videoExt.test(a.ref) || videoExt.test(a.summary || "");
    },

    isStoredAudio(a: { kind: string; ref: string; summary?: string }): boolean {
      if (!this.isStoredArtifact(a.ref)) return false;
      const audioExt = /\.(mp3|wav|ogg|aac|flac|m4a)\b/i;
      return audioExt.test(a.ref) || audioExt.test(a.summary || "");
    },

    isStoredNonImage(a: { kind: string; ref: string; summary?: string }): boolean {
      return this.isStoredArtifact(a.ref) && !this.isStoredImage(a) && !this.isStoredVideo(a) && !this.isStoredAudio(a);
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
      busy: false,
    },

    availableRelationTasks(): TaskSummary[] {
      const d = this.detail;
      const b = this.board;
      if (!b) return [];
      const currentId = d?.id;
      const out: TaskSummary[] = [];
      for (const s of STATUSES) {
        for (const t of b.tasks[s] ?? []) {
          if (t.id !== currentId) {
            out.push(t);
          }
        }
      }
      return out;
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
      const targetId = this.relationForm.targetId.trim();
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

    /** Escape key: close the top-most overlay or cancel edit mode. Dirty
     *  surfaces (task edit, new task, project settings) confirm first via
     *  confirmDiscard() — Escape never silently discards a draft. */
    dismissTop(): void {
      if (this.previewModal && this.previewModal.open) this.closeImagePreview();
      else if (this.unsavedNotesAction) this.cancelUnsavedNotes(); // Escape = "keep editing"
      else if (this.showAuthModal) this.closeAuthModal();
      else if (this.showProjectSettings) {
        if (this.showArchiveConfirm) this.cancelArchiveConfirm();
        else this.requestCloseProjectSettings();
      }
      else if (this.editingAcSeq !== 0) this.cancelEditingAc();
      else if (this.editingTask) {
        // The edit form is the topmost surface inside the drawer: a dirty
        // draft must be confirmed BEFORE Escape exits edit mode.
        if (!this.discardEditDraftIfConfirmed()) return;
        this.cancelEditing();
      }
      else if (this.showTaskOpenCodeConfig) this.showTaskOpenCodeConfig = false;
      else if (this.statusMenuOpen) this.statusMenuOpen = false;
      else if (this.ownerPopoverOpen) this.ownerPopoverOpen = false;
      else if (this.detailOpen) this.requestCloseDetail();
      else if (this.showNewTask) this.requestCloseNewTask();
      else if (this.projectMenuOpen) this.closeProjectMenu(true); // header dropdowns are topmost
      else if (this.headerMenuOpen) this.closeHeaderMenu(true);
      else if (this.showNewProject) this.showNewProject = false;
      else if (this.cardMenuTaskId) this.cardMenuTaskId = null;
      else if (this.showMobileMenu) this.showMobileMenu = false;
    },

    isWebHref(s: string | null | undefined): boolean {
      return typeof s === "string" && /^(https?:|mailto:)/i.test(s.trim());
    },

    // ------------------------------------------------------------- template glue
    STATUSES,
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
    isOverdue,
    dueLabel,
    claimHint,
    formatClaimCountdown,
    isClaimExpired,
    actorLabel,
    initials,
    shortId,
    relTime,
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
        // Browser back/forward is a first-class way into and out of the full-
        // page task view (/tasks/<id>) — reconcile on every popstate.
        window.addEventListener("popstate", () => this.onPopState());
      }
      this.applyTheme();
      if (typeof document !== "undefined") {
        this.setDocumentTitle();
      }
      await this.loadProjects();
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
      } else {
        // No projects yet — invite creating one.
        this.showNewProject = true;
      }
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

export const crowbanApp = ravnApp;
