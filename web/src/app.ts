// The single Alpine.js root component for the crowban board UI. One factory
// returning a state + methods object; registered as `Alpine.data("app", …)` in
// main.ts and used via x-data="app()" in index.html.
//
// Scope: VISIBILITY + light intervention. Humans can create projects/tasks,
// move status, save notes, toggle ACs, comment, attach artifacts. Claims are
// READ-ONLY here (agents manage leases via MCP).

import { ApiError, api, errMsg } from "./api";
import { focusWhenReady, openDialogFocus, type DialogFocus } from "./focus";
import { beginMove, endMove, MOVE_TIMEOUT_MS, MoveTimedOutError, withTimeout } from "./moves";
import { canApplyDetail, coalesceInFlight, createDebounce, createSequenceGuard, SSE_RELOAD_DEBOUNCE_MS, shouldApplyBoard } from "./refresh";
import { renderMarkdown } from "./md";
import type {
  AcRow,
  ActorInput,
  ArtifactKind,
  Board,
  CommentKind,
  CommentRow,
  ListTasksParams,
  ProjectWithCounts,
  RealtimeEventPayload,
  RelationRow,
  SseConnectionStatus,
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
  dateInputToIso,
  dueCalendarDate,
  dueLabel,
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

const LS_ACTOR = "crowban.actor";
const LS_PROJECT = "crowban.project";
const LS_SORT = "crowban.sort";
const LS_THEME = "crowban.theme";
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
    const raw = safeStorage.getItem(LS_THEME);
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

export interface Toast {
  id: number;
  msg: string;
  kind: "error" | "info";
}

interface RelationView {
  key: string;
  label: string;
  otherId: string;
  title: string;
  raw: RelationRow;
}

export function crowbanApp() {
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
      } catch {
        /* private mode — non-fatal */
      }
      this.applyTheme();
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

    toast(msg: string, kind: "error" | "info" = "info"): void {
      const id = ++this.toastSeq;
      this.toasts = [...this.toasts, { id, msg, kind }];
      window.setTimeout(() => {
        this.toasts = this.toasts.filter((t) => t.id !== id);
      }, 6500);
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
      if (this.selectedProjectId) this.connectSse();
      void this.loadProjects().then(() => {
        if (this.selectedProjectId) void this.loadBoard();
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

    // ------------------------------------------------------------ new project
    showNewProject: false,
    newProject: { name: "", description: "" },
    projectBusy: false,

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

    // ------------------------------------------------------------ project settings
    showProjectSettings: false,
    projectSettingsForm: {
      name: "",
      description: "",
      opencode_server_url: "",
      opencode_directory: "",
      opencode_model: "",
    },
    projectSettingsBusy: false,
    /** Focus session for the project settings modal (trap + restore). */
    projectSettingsFocus: null as DialogFocus | null,

    async openProjectSettings(): Promise<void> {
      if (!this.selectedProjectId) return;
      const p = this.projects.find((x) => x.id === this.selectedProjectId);
      if (p) {
        this.projectSettingsForm = {
          name: p.name,
          description: p.description ?? "",
          opencode_server_url: p.opencode_server_url ?? "",
          opencode_directory: p.opencode_directory ?? "",
          opencode_model: p.opencode_model ?? "",
        };
      }
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
        };
      } catch {
        /* keep current list info */
      }
    },

    /** Single close path for the settings modal (× / Cancel / backdrop / save / Esc). */
    closeProjectSettings(): void {
      this.showProjectSettings = false;
      this.closeDialog(this.projectSettingsFocus);
      this.projectSettingsFocus = null;
    },

    async saveProjectSettings(): Promise<void> {
      const name = this.projectSettingsForm.name.trim();
      if (!this.selectedProjectId || !name || this.projectSettingsBusy) return;
      this.projectSettingsBusy = true;
      try {
        await api.updateProject(this.selectedProjectId, {
          actor: this.actor(),
          name,
          description: this.projectSettingsForm.description.trim() || undefined,
          opencode_server_url: this.projectSettingsForm.opencode_server_url.trim() || undefined,
          opencode_directory: this.projectSettingsForm.opencode_directory.trim() || undefined,
          opencode_model: this.projectSettingsForm.opencode_model.trim() || undefined,
        });
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
     *  detail fetches (knownBlocked). */
    blockedIds: new Set<string>(),
    knownBlocked: {} as Record<string, boolean>,

    isBlocked(id: string): boolean {
      return this.blockedIds.has(id) || !!this.knownBlocked[id];
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
     *  Skipped when the project has more than `limit` open tasks (MVP).
     *  `isStillCurrent` lets the caller veto writing a stale diff after awaits. */
    async refreshBlockedSet(b: Board, isStillCurrent?: () => boolean): Promise<void> {
      const stillCurrent = (): boolean => (isStillCurrent ? isStillCurrent() : true);
      const total = STATUSES.reduce((n, s) => n + (b.tasks[s]?.length ?? 0), 0);
      if (total === 0) {
        if (stillCurrent()) this.blockedIds = new Set();
        return;
      }
      const limit = 100;
      try {
        const [all, un] = await Promise.all([
          api.listTasks({ project_id: b.project_id, limit }),
          api.listTasks({ project_id: b.project_id, unblocked: true, limit }),
        ]);
        if (!stillCurrent()) return; // superseded — keep whatever we had
        if (all.total > limit || un.total > limit) return; // too big to diff cheaply
        const unIds = new Set(un.tasks.map((t) => t.id));
        this.blockedIds = new Set(
          all.tasks.filter((t) => !unIds.has(t.id)).map((t) => t.id),
        );
      } catch {
        /* keep whatever we had — advisory feature only */
      }
    },

    // ------------------------------------------------------------ realtime SSE
    sse: null as EventSource | null,
    sseStatus: "disconnected" as SseConnectionStatus,
    sseBackoffMs: 2000,
    sseReconnectTimer: 0 as number,

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
      const url = `/api/v1/projects/${encodeURIComponent(this.selectedProjectId)}/events${
        token ? `?token=${encodeURIComponent(token)}` : ""
      }`;

      let es: EventSource;
      try {
        es = new EventSource(url);
      } catch {
        this.handleSseDisconnect();
        return;
      }

      this.sse = es;

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
      }
      if (resetBackoff) {
        this.sseBackoffMs = 2000;
        this.sseStatus = "disconnected";
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

    /** Single close path for the new-task modal (× / Cancel / backdrop / create / Esc). */
    closeNewTask(): void {
      this.showNewTask = false;
      this.duplicates = [];
      this.closeDialog(this.newTaskFocus);
      this.newTaskFocus = null;
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

    async openTask(id: string): Promise<void> {
      // Unsaved notes belong to the task being left — ask before dropping them
      // (AC 3). Re-opening the SAME task is just a refresh: its in-progress
      // draft is intentionally preserved (see applyDetail).
      if (this.notesDirty && this.detailOpen && this.detailId !== id) {
        this.promptUnsavedNotes({ kind: "open", taskId: id });
        return;
      }
      // Defensive: a scratchpad that belongs to another task is discarded up
      // front so it can never surface inside the drawer about to open (AC 2).
      if (this.notesDraftOwner !== null && this.notesDraftOwner !== id) {
        this.resetNotesDraft();
      }
      // In-drawer switch (discard-&-switch): keep the existing session so focus
      // restoration still targets the element that originally opened the drawer.
      const switching = this.detailOpen && this.detailFocus !== null;
      this.detailOpen = true;
      this.detailId = id;
      this.detail = null;
      this.editingTask = false;
      this.totalComments = 0;
      this.loadingOlderComments = false;
      if (!switching) {
        // Defensive: a stale session must never survive to capture a new trigger.
        this.closeDialog(this.detailFocus);
        this.detailFocus = null;
        // The × button is the safe initial focus (not "Start Task in OpenCode").
        // autoFocus off: content arrives via fetch, so focus moves explicitly
        // once (below) instead of racing the render.
        this.detailFocus = this.openDialog('cb-detail-drawer', 'button[aria-label="Close"]', false);
      }
      await this.refreshDetail(false);
      void this.initComments(id);
      // Content arrives via fetch; only move focus once the dialog actually has
      // focusables (no-op if the user already tabbed somewhere inside).
      this.detailFocus?.focusInitial();
    },

    /** Drawer-close entry point for the backdrop, × buttons and Escape: asks
     *  first when notes have unsaved edits (AC 3). */
    requestCloseDetail(): void {
      if (this.notesDirty) {
        this.promptUnsavedNotes({ kind: "close" });
        return;
      }
      this.closeDetail();
    },

    closeDetail(): void {
      this.editingTask = false;
      this.editingAcSeq = 0;
      this.editingAcText = "";
      this.newAcText = "";
      this.totalComments = 0;
      this.loadingOlderComments = false;
      this.startingOpenCode = false;
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
    },

    // ---------------------------------------------------------------- opencode
    startingOpenCode: false,

    async startTaskInOpenCode(): Promise<void> {
      const d = this.detail;
      if (!d || this.startingOpenCode) return;
      this.startingOpenCode = true;
      try {
        await api.startOpenCode(d.id);
        this.toast("OpenCode session started");
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.startingOpenCode = false;
      }
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
        return false;
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
      const kb = { ...this.knownBlocked };
      if (t.blocked) kb[t.id] = true;
      else delete kb[t.id];
      this.knownBlocked = kb;
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
      const d = this.detail;
      if (!d || this.assigneeBusy) return;
      const name = this.assigneeInputName.trim();
      const type = this.assigneeInputType || "human";
      await this.updateAssignee(name, type);
    },

    async clearAssignee(): Promise<void> {
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
    editDraft: {
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
      this.editingTask = true;
    },

    cancelEditing(): void {
      this.editingTask = false;
    },

    async saveTaskEdit(): Promise<void> {
      const t = this.detail;
      const title = this.editDraft.title.trim();
      if (!t || !title || this.savingEdit) return;

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
      if (!this.draggedTaskId) return;
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
      await this.moveTaskStatus(taskId, targetStatus);
    },

    handleCardClick(id: string): void {
      if (this.isDragging) return;
      void this.openTask(id);
    },

    // ---------------------------------- touch: swipe-to-status + per-card move menu
    // Explicit touch alternative to HTML5 drag-and-drop (which never fires on
    // touch devices). A mostly-horizontal swipe moves the card one column in
    // that direction; the ⋮ button opens a "Move to" status picker row. Both
    // funnel into moveTaskStatus(), so 409-rebase + rollback behaviour is shared.
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
      if (target) void this.moveTaskStatus(task.id, target);
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
              this.toast(errMsg(e2), "error");
            }
          }
        } else if (e instanceof MoveTimedOutError) {
          rollback();
          await this.loadBoard(true).catch(() => undefined);
          this.toast("Move timed out — your change was not saved. Please try again.", "error");
        } else {
          rollback();
          await this.loadBoard(true).catch(() => undefined);
          this.toast(errMsg(e), "error");
        }
      } finally {
        endMove(this.busyTaskIds, taskId);
      }
    },

    /** Version-guarded status move from detail drawer. On 409 version_conflict:
     *  re-read the task, re-apply the SAME intent once with the fresh version;
     *  a second conflict just refreshes and says so (per docs/API.md error contract). */
    async moveStatus(status: Status): Promise<void> {
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

    /** Toggle one criterion; the 200 response carries the updated AC row. */
    async toggleAc(ac: AcRow): Promise<void> {
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

    completedLabel(ac: AcRow): string {
      const who = ac.completed_by ? actorLabel(ac.completed_by) : "unknown";
      const when = ac.completed_at ? relTime(ac.completed_at) : "";
      return `${who}${when ? ` · ${when}` : ""}`;
    },

    // ----------------------------------------------------------------- comments
    commentForm: { kind: "progress" as CommentKind, body: "" },
    postingComment: false,
    loadingOlderComments: false,
    totalComments: 0,

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
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.postingComment = false;
      }
    },

    // ---------------------------------------------------------------- artifacts
    artifactForm: { open: false, kind: "url" as ArtifactKind, ref: "", summary: "" },
    attachingArtifact: false,

    async attachArtifact(): Promise<void> {
      const t = this.detail;
      const ref = this.artifactForm.ref.trim();
      if (!t || !ref || this.attachingArtifact) return;
      this.attachingArtifact = true;
      try {
        const a = await api.addArtifact(
          t.id,
          this.artifactForm.kind,
          ref,
          this.artifactForm.summary.trim() || undefined,
          this.actor(),
        );
        const d = this.detail;
        if (d && d.id === t.id) this.detail = { ...d, artifacts: [...d.artifacts, a] };
        this.artifactForm = { open: false, kind: "url", ref: "", summary: "" };
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.attachingArtifact = false;
      }
    },

    // ---------------------------------------------------------------- claims
    claimBusy: false,

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
        this.toast(errMsg(e), "error");
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
      return d.relations.map((r) => {
        let label: string;
        let otherId: string;
        if (r.kind === "child") {
          // Stored edge: task_id is the child, other_task_id the parent.
          if (r.task_id === me) {
            label = "Parent";
            otherId = r.other_task_id;
          } else {
            label = "Child";
            otherId = r.task_id;
          }
        } else if (r.kind === "blocked-by") {
          if (r.task_id === me) {
            label = "Blocked by";
            otherId = r.other_task_id;
          } else {
            label = "Blocking";
            otherId = r.task_id;
          }
        } else if (r.kind === "duplicate-of") {
          if (r.task_id === me) {
            label = "Duplicate of";
            otherId = r.other_task_id;
          } else {
            label = "Duplicated by";
            otherId = r.task_id;
          }
        } else {
          label = "Related to";
          otherId = r.task_id === me ? r.other_task_id : r.task_id;
        }
        return {
          key: `${r.task_id}:${r.other_task_id}:${r.kind}`,
          label,
          otherId,
          title: this.taskTitle(otherId),
          raw: r,
        };
      });
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

    /** Escape key: close the top-most overlay or cancel edit mode. */
    dismissTop(): void {
      if (this.unsavedNotesAction) this.cancelUnsavedNotes(); // Escape = "keep editing"
      else if (this.showAuthModal) this.closeAuthModal();
      else if (this.showProjectSettings) this.closeProjectSettings();
      else if (this.editingAcSeq !== 0) this.cancelEditingAc();
      else if (this.editingTask) this.cancelEditing();
      else if (this.detailOpen) this.requestCloseDetail();
      else if (this.showNewTask) this.closeNewTask();
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
    statusLabel,
    priorityClass,
    priorityName,
    commentKindClass,
    artifactKindClass,
    isOverdue,
    dueLabel,
    claimHint,
    actorLabel,
    initials,
    shortId,
    relTime,
    /** Sanitized-markdown renderer (escaped source → marked → DOM post-pass),
     *  memoized so poll re-renders don't re-parse unchanged bodies. */
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
    async init(): Promise<void> {
      // Coalescer for SSE-triggered board/projects reloads (handleSseEvent):
      // a burst of events → ONE round after the quiet window. Created before any
      // network work so even an early event burst can't fire uncoalesced loads.
      this.sseReloadDebounce = createDebounce(() => {
        void this.loadBoard(true);
        void this.loadProjects(true);
      }, SSE_RELOAD_DEBOUNCE_MS);
      if (typeof window !== "undefined") {
        window.addEventListener("beforeunload", () => {
          this.disconnectSse(true);
        });
        // Track live OS preference changes — only relevant while in system mode.
        this._themeMq?.addEventListener("change", () => {
          if (this.theme === "system") this.applyTheme();
        });
      }
      this.applyTheme();
      await this.loadProjects();
      const known = this.projects.some((p) => p.id === this.selectedProjectId);
      if (!known || !this.selectedProjectId) {
        this.selectedProjectId = this.projects[0]?.id ?? "";
      }
      if (this.selectedProjectId) {
        try {
          safeStorage.setItem(LS_PROJECT, this.selectedProjectId);
        } catch { /* ignore */ }
        await this.loadBoard();
        this.connectSse();
      } else {
        // No projects yet — invite creating one.
        this.showNewProject = true;
      }
      if (this.polling) this.startPolling();
    },
  };
}
