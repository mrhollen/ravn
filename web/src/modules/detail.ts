// Task-detail lifecycle (Phase 2 batch D) — extracted from web/src/app.ts.
//
// Owns the whole open/close concern for the detail surface:
//   - openTask / requestCloseDetail / closeDetail lifecycle, including the
//     cascade resets of state owned by OTHER modules (relation form, AC edit
//     session, comment counters, artifact copy flag, opencode flags) — those
//     "ride with their owner" and are wired here through DetailDeps;
//   - the full-page view (/tasks/<id>) + deep-link adoption: openFullPage's
//     push-vs-adopt decision and closeDetail's two-shape history sync
//     (pushed entry → back(), deep link → replaceState(boardUrl) — NEVER
//     back() from a deep link, which would navigate out of the app);
//   - applyDetail / refreshDetail patching with its ownership guard
//     (canApplyDetail) and drawer error state;
//   - the operational bar (status menu / owner popover toggles, moveStatus);
//   - the notes scratchpad (draft persistence pair, unsaved-notes confirm
//     lifecycle, saveNotes with the AC-3 cross-task guard).
//
// Convention (Phase 2): every template-bound name stays a flat plain field on
// the Alpine component root; this module owns NO state of its own except the
// two dialog focus sessions (never rendered, never part of the flat namespace
// — same convention as modules/new-task.ts). Bodies moved verbatim: `this.x`
// became deps accessors, cross-module calls became hooks.

import { ApiError, api, errMsg } from "../api";
import { focusElementOnceVisible, type DialogFocus } from "../focus";
import { notesDraftKey } from "../drafts";
import { isMobileDetailViewport, taskRouteIdFromPath, taskRoutePath } from "../routes";
import { canApplyDetail } from "../refresh";
import { copyTextToClipboard, safeStorage } from "../util";
import { LS_PROJECT } from "./projects";
import type { RelationForm } from "./relations";
import type {
  ActorInput,
  BlockerRef,
  CommentRow,
  OpenCodeOptionsResponse,
  ProjectWithCounts,
  StartOpenCodeInput,
  StartOpenCodeResponse,
  Status,
  TaskDetail,
} from "../types";

/** The close/switch action held behind the unsaved-notes confirmation — null
 *  when no prompt is on screen. */
type UnsavedNotesAction = { kind: "close" } | { kind: "open"; taskId: string };

/** agent/model/variant triple for the OpenCode start panel. */
type OpenCodeSelection = { agent: string; model: string; variant: string };

export interface DetailDeps {
  // ------------------------------------------------------------- state-in
  detail: () => TaskDetail | null;
  detailId: () => string | null;
  detailOpen: () => boolean;
  fullPageOpen: () => boolean;
  fullPagePushed: () => boolean;
  dashboardOpen: () => boolean;
  selectedProjectId: () => string;
  projects: () => ProjectWithCounts[];
  detailRetrying: () => boolean;
  statusMenuOpen: () => boolean;
  ownerPopoverOpen: () => boolean;
  priorityPopoverOpen: () => boolean;
  duePopoverOpen: () => boolean;
  notesDirty: () => boolean;
  notesDraft: () => string;
  notesDraftOwner: () => string | null;
  savingNotes: () => boolean;
  unsavedNotesAction: () => UnsavedNotesAction | null;
  totalComments: () => number;
  copiedArtifactTimer: () => number | null;
  relationForm: () => RelationForm;
  startingOpenCode: () => boolean;
  showTaskOpenCodeConfig: () => boolean;
  taskOpenCodeForm: () => OpenCodeSelection;
  taskOpenCodeBaseline: () => OpenCodeSelection;
  openCodeSessions: () => Record<string, StartOpenCodeResponse>;
  openCodeOptions: () => OpenCodeOptionsResponse | null;
  openCodeOptionsProjectId: () => string;

  // ------------------------------------------------------------- state-out
  setDetail: (v: TaskDetail | null) => void;
  setDetailId: (v: string | null) => void;
  setDetailOpen: (v: boolean) => void;
  setFullPageOpen: (v: boolean) => void;
  setFullPagePushed: (v: boolean) => void;
  setDashboardPushed: (v: boolean) => void;
  setSelectedProjectId: (v: string) => void;
  setDetailLoadError: (v: string | null) => void;
  setDetailRetrying: (v: boolean) => void;
  setStatusMenuOpen: (v: boolean) => void;
  setOwnerPopoverOpen: (v: boolean) => void;
  setPriorityPopoverOpen: (v: boolean) => void;
  setDuePopoverOpen: (v: boolean) => void;
  setNotesDirty: (v: boolean) => void;
  setNotesDraft: (v: string) => void;
  setNotesDraftOwner: (v: string | null) => void;
  setNotesShowPreview: (v: boolean) => void;
  setUnsavedNotesAction: (v: UnsavedNotesAction | null) => void;
  setSavingNotes: (v: boolean) => void;
  setTotalComments: (v: number) => void;
  setLoadingOlderComments: (v: boolean) => void;
  setCopiedArtifactId: (v: string | null) => void;
  setCopiedArtifactTimer: (v: number | null) => void;
  setEditingTask: (v: boolean) => void;
  setDescShowPreview: (v: boolean) => void;
  setAssigneeInputName: (v: string) => void;
  setAssigneeInputType: (v: "human" | "agent") => void;
  setEditingAcSeq: (v: number) => void;
  setEditingAcText: (v: string) => void;
  setNewAcText: (v: string) => void;
  setStartingOpenCode: (v: boolean) => void;
  setShowTaskOpenCodeConfig: (v: boolean) => void;
  setTaskOpenCodeForm: (f: OpenCodeSelection) => void;
  setTaskOpenCodeBaseline: (f: OpenCodeSelection) => void;
  setOpenCodeSessions: (s: Record<string, StartOpenCodeResponse>) => void;
  setShowNewProject: (v: boolean) => void;

  // ----------------------------------------------------------------- hooks
  toast: (msg: string, kind?: "error" | "info", action?: { label: string; href?: string }) => number;
  actor: () => ActorInput;
  isTaskBusy: (id: string) => boolean;
  moveTaskStatus: (taskId: string, status: Status, initialVersion?: number) => Promise<void>;
  editDraftDirty: () => boolean;
  discardEditDraftIfConfirmed: () => boolean;
  restoreCommentDraftForTask: (id: string) => void;
  initComments: (id: string) => Promise<void>;
  openDialog: (id: string, initialSelector?: string, autoFocus?: boolean) => DialogFocus | null;
  closeDialog: (f: DialogFocus | null) => void;
  learnBlockedFromDetail: (
    projectId: string,
    taskId: string,
    blocked: boolean,
    blockers: BlockerRef[],
  ) => void;
  updateDocumentTitle: () => void;
  boardUrl: () => string;
  loadBoard: (force?: boolean) => Promise<void>;
  connectSse: () => void;
  openAuthModal: () => void;
  closeDashboard: () => void;
  fetchOpenCodeOptions: (projectId: string) => Promise<OpenCodeOptionsResponse | null>;
}

export function createDetailModule(deps: DetailDeps) {
  // Dialog focus sessions (drawer + unsaved-notes confirm stacked above it).
  // Survive in-drawer task switches so focus still returns to the original
  // board trigger — same lifetime as when they were root fields.
  let detailFocus: DialogFocus | null = null;
  let confirmNotesFocus: DialogFocus | null = null;

  async function openTask(id: string): Promise<void> {
    // Unsaved notes belong to the task being left — ask before dropping them
    // (AC 3). Re-opening the SAME task is just a refresh: its in-progress
    // draft is intentionally preserved (see applyDetail).
    if (deps.notesDirty() && deps.detailOpen() && deps.detailId() !== id) {
      promptUnsavedNotes({ kind: "open", taskId: id });
      return;
    }
    // A dirty task-edit draft belongs to the task being left — same rule as
    // unsaved notes above. (If both drafts are dirty, the user confirms each
    // explicitly: the notes prompt first, then this one.)
    if (deps.editDraftDirty() && deps.detailId() !== id) {
      if (!deps.discardEditDraftIfConfirmed()) return;
    }
    // Defensive: a scratchpad that belongs to another task is discarded up
    // front so it can never surface inside the drawer about to open (AC 2).
    if (deps.notesDraftOwner() !== null && deps.notesDraftOwner() !== id) {
      resetNotesDraft();
    }
    // A task switch while the full page is open keeps the URL pointing at the
    // NEW task (the deep link stays truthful and shareable) without growing
    // history — replaceState, never push.
    if (deps.fullPageOpen() && typeof window !== "undefined" && deps.detailId() !== id) {
      window.history.replaceState(null, "", taskRoutePath(id));
    }
    // Mobile fallback (task 01M47MWSTTPS14M6XB1A8HFPV1): below md (768px) a
    // card click opens the FULL-PAGE view via the same /tasks/<id> route the
    // "Open full page" button pushes, so phones get deep-link + browser-back
    // semantics for free. At ≥md the wide centered modal is the default.
    if (!deps.fullPageOpen() && isMobileDetailViewport()) {
      openFullPage(id);
    }
    // In-drawer switch (discard-&-switch): keep the existing session so focus
    // restoration still targets the element that originally opened the drawer.
    const switching = deps.detailOpen() && detailFocus !== null;
    deps.setDetailOpen(true);
    deps.setDetailId(id);
    deps.setDetail(null);
    // Never carry a previous task's error state into the one being opened.
    deps.setDetailLoadError(null);
    deps.setStatusMenuOpen(false);
    deps.setOwnerPopoverOpen(false);
    deps.setPriorityPopoverOpen(false);
    deps.setDuePopoverOpen(false);
    deps.relationForm().targetId = "";
    deps.relationForm().search = "";
    deps.relationForm().open = false;
    deps.relationForm().activeIndex = -1;
    deps.setEditingTask(false);
    // Write/Preview toggles never outlive an open: every drawer opens in
    // Write mode for both markdown fields (AC 1).
    deps.setDescShowPreview(false);
    // Reset-then-restore: the comment form starts clean so one task's unsaved
    // comment can never leak into the next, then a stored draft for THIS id is
    // re-applied (drafts live in localStorage, not state — they survive
    // navigation/close/reload by design, AC 2).
    deps.restoreCommentDraftForTask(id);
    deps.setTotalComments(0);
    deps.setLoadingOlderComments(false);
    initTaskOpenCodeConfig();
    if (!switching) {
      // Defensive: a stale session must never survive to capture a new trigger.
      deps.closeDialog(detailFocus);
      detailFocus = null;
      // The × button is the safe initial focus (not "Start Task in OpenCode").
      // autoFocus off: content arrives via fetch, so focus moves explicitly
      // once (below) instead of racing the render.
      detailFocus = deps.openDialog('cb-detail-drawer', '[data-testid="detail-close-btn"]', false);
    }
    const applied = await refreshDetail(false);
    void deps.initComments(id);
    if (!applied && deps.detailOpen()) {
      // The fetch for this task failed (or the drawer closed mid-flight): the
      // drawer shows its error state. Move focus to Retry as soon as it is
      // rendered — the block mounts via x-if on Alpine's own schedule, so
      // _focusWhenVisible polls frames instead of focusing blind.
      const retryBtn =
        typeof document === "undefined"
          ? null
          : document.querySelector<HTMLElement>('[data-testid="detail-retry-btn"]');
      focusElementOnceVisible(retryBtn);
    } else {
      // Content arrives via fetch; only move focus once the dialog actually has
      // focusables (no-op if the user already tabbed somewhere inside).
      detailFocus?.focusInitial();
    }
  }

  /** Detail-close entry point for the backdrop, × buttons and Escape —
   *  valid from BOTH skins (centered modal and full page): asks first when
   *  notes have unsaved edits (AC 3) or a task-edit draft differs from the
   *  loaded values (task 01M3MMZ188XJBGKFSWBGESK79D). */
  function requestCloseDetail(): void {
    if (deps.notesDirty()) {
      promptUnsavedNotes({ kind: "close" });
      return;
    }
    // A dirty task-edit draft is guarded by the same no-silent-loss rule.
    if (!deps.discardEditDraftIfConfirmed()) return;
    closeDetail();
  }

  function closeDetail(syncHistory = true): void {
    const wasFullPage = deps.fullPageOpen();
    const pushedEntry = deps.fullPagePushed();
    deps.setStatusMenuOpen(false);
    deps.setOwnerPopoverOpen(false);
    deps.setPriorityPopoverOpen(false);
    deps.setDuePopoverOpen(false);
    deps.relationForm().targetId = "";
    deps.relationForm().search = "";
    deps.relationForm().open = false;
    deps.relationForm().activeIndex = -1;
    deps.setEditingTask(false);
    // The drawer's error state never outlives its drawer (same rule as the
    // notes scratchpad). openTask() also clears it, so a re-open starts clean.
    deps.setDetailLoadError(null);
    deps.setEditingAcSeq(0);
    deps.setEditingAcText("");
    deps.setNewAcText("");
    deps.setTotalComments(0);
    deps.setLoadingOlderComments(false);
    deps.setStartingOpenCode(false);
    deps.setShowTaskOpenCodeConfig(false);
    deps.setCopiedArtifactId(null);
    const artifactTimer = deps.copiedArtifactTimer();
    if (artifactTimer) {
      clearTimeout(artifactTimer);
      deps.setCopiedArtifactTimer(null);
    }
    // AC 1: the notes scratchpad never outlives its drawer — otherwise a
    // dirty draft would leak into whatever task is opened next.
    resetNotesDraft();
    deps.setUnsavedNotesAction(null);
    // A confirm session must not outlive its drawer either (defensive).
    deps.closeDialog(confirmNotesFocus);
    confirmNotesFocus = null;
    // Restore focus to the board trigger that opened the drawer.
    deps.closeDialog(detailFocus);
    detailFocus = null;
    deps.setDetailOpen(false);
    deps.setFullPageOpen(false);
    deps.setFullPagePushed(false);
    // History sync: a FULL-PAGE close must leave the URL honest. An entry we
    // pushed is popped — the resulting popstate lands on the prior route
    // (board "/" or dashboard "/projects/<id>/dashboard"). A deep-linked open
    // has NO prior entry behind it, so popping would navigate OUT of the app:
    // replace instead. (When closing from a popstate-driven back or direct
    // route transition, syncHistory=false skips history mutation.)
    if (
      syncHistory &&
      wasFullPage &&
      typeof window !== "undefined" &&
      taskRouteIdFromPath(window.location.pathname) !== null
    ) {
      if (pushedEntry) window.history.back();
      else window.history.replaceState(null, "", deps.boardUrl());
    }
    // Drawer closed → the tab title reverts to the active view. (Recomputes:
    // detailOpen is now false, so any stale `detail` object can't leak its
    // title into the board/dashboard state — WCAG 2.4.2.)
    deps.updateDocumentTitle();
  }

  /** Switch the open task's detail to the FULL-PAGE view. Pushes /tasks/<id>
   *  so browser back returns to the previous view (board or dashboard); a URL
   *  that is already on a task route (deep link) is adopted as-is instead of
   *  piling another entry on top. `id` defaults to the loaded detail's id —
   *  openTask() passes it explicitly for mobile opens, where the fetch has not
   *  landed yet. */
  function openFullPage(id?: string): void {
    const taskId = id ?? deps.detail()?.id;
    if (!taskId || typeof window === "undefined") return;
    if (deps.dashboardOpen()) {
      deps.setDashboardPushed(false);
      deps.closeDashboard();
    }
    deps.setFullPageOpen(true);
    if (taskRouteIdFromPath(window.location.pathname) !== taskId) {
      window.history.pushState(null, "", taskRoutePath(taskId));
      deps.setFullPagePushed(true);
    } else {
      deps.setFullPagePushed(false); // deep link — no entry of ours to pop later
    }
    deps.updateDocumentTitle();
  }

  /** Deep link on first load: /tasks/<id> in the initial URL. Fetches the
   *  task, adopts its project as the board context (so SSE/poll/relations
   *  targets are correct), and opens it as a full page. A failed fetch resets
   *  the URL to "/" so a dead deep link can't strand the history state, and
   *  still lands the user on a usable board. */
  async function openDeepLinkedTask(id: string): Promise<void> {
    let t: TaskDetail;
    try {
      t = await api.getTask(id, { commentLimit: 50 });
    } catch (e) {
      if (ApiError.isUnauthorized(e)) deps.openAuthModal();
      else deps.toast(errMsg(e), "error");
      if (typeof window !== "undefined") window.history.replaceState(null, "", deps.boardUrl());
      if (deps.selectedProjectId()) {
        await deps.loadBoard();
        deps.connectSse();
      } else {
        deps.setShowNewProject(true);
      }
      return;
    }
    if (deps.selectedProjectId() !== t.project_id) {
      deps.setSelectedProjectId(t.project_id);
      try {
        safeStorage.setItem(LS_PROJECT, t.project_id);
      } catch { /* private mode */ }
    }
    await deps.loadBoard();
    deps.connectSse();
    // The URL was given to us by the browser — we did not push it.
    deps.setFullPageOpen(true);
    deps.setFullPagePushed(false);
    await openTask(id);
  }

  // ---------------------------------------------------------------- opencode
  function initTaskOpenCodeConfig(): void {
    deps.setShowTaskOpenCodeConfig(false);
    const p = deps.projects().find((x) => x.id === deps.selectedProjectId());
    const agent = p?.opencode_agent || deps.openCodeOptions()?.resolved_agent || "build";
    const model = p?.opencode_model || deps.openCodeOptions()?.resolved_model || "";
    const variant = p?.opencode_variant || deps.openCodeOptions()?.resolved_variant || "";
    deps.setTaskOpenCodeForm({ agent, model, variant });
    deps.setTaskOpenCodeBaseline({ agent, model, variant });
  }

  function toggleOpenCodeConfig(): void {
    deps.setShowTaskOpenCodeConfig(!deps.showTaskOpenCodeConfig());
    if (deps.showTaskOpenCodeConfig() && (!deps.openCodeOptions() || deps.openCodeOptionsProjectId() !== deps.selectedProjectId())) {
      void deps.fetchOpenCodeOptions(deps.selectedProjectId()).then((opts) => {
        if (opts && !taskOpenCodeDirty()) {
          const pNow = deps.projects().find((x) => x.id === deps.selectedProjectId());
          const a = pNow?.opencode_agent || opts.resolved_agent || "build";
          const m = pNow?.opencode_model || opts.resolved_model || "";
          const v = pNow?.opencode_variant || opts.resolved_variant || "";
          deps.setTaskOpenCodeForm({ agent: a, model: m, variant: v });
          deps.setTaskOpenCodeBaseline({ agent: a, model: m, variant: v });
        }
      });
    }
  }

  function taskOpenCodeDirty(): boolean {
    return (
      (deps.taskOpenCodeForm().agent ?? "") !== (deps.taskOpenCodeBaseline().agent ?? "") ||
      (deps.taskOpenCodeForm().model ?? "") !== (deps.taskOpenCodeBaseline().model ?? "") ||
      (deps.taskOpenCodeForm().variant ?? "") !== (deps.taskOpenCodeBaseline().variant ?? "")
    );
  }

  function activeOpenCodeSession(d?: TaskDetail | null): StartOpenCodeResponse | null {
    const target = d === undefined ? deps.detail() : d;
    if (!target) return null;
    const cached = deps.openCodeSessions()[target.id];
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
  }

  async function startTaskInOpenCode(customInput?: StartOpenCodeInput): Promise<void> {
    const d = deps.detail();
    if (!d || deps.startingOpenCode()) return;
    deps.setStartingOpenCode(true);
    try {
      let input: StartOpenCodeInput | undefined = customInput;
      if (!input && (taskOpenCodeDirty() || deps.showTaskOpenCodeConfig())) {
        const p: StartOpenCodeInput = {};
        if (deps.taskOpenCodeForm().agent?.trim()) p.agent = deps.taskOpenCodeForm().agent.trim();
        if (deps.taskOpenCodeForm().model?.trim()) p.model = deps.taskOpenCodeForm().model.trim();
        const variantTrim = deps.taskOpenCodeForm().variant?.trim() ?? "";
        if (variantTrim || (deps.taskOpenCodeBaseline().variant ?? "").trim()) {
          p.variant = variantTrim;
        }
        input = p;
      }
      const res = await api.startOpenCode(d.id, input);
      if (res && res.session_url) {
        deps.setOpenCodeSessions({ ...deps.openCodeSessions(), [d.id]: res });
      }
      deps.toast(
        res?.session_id ? `OpenCode session started (${res.session_id})` : "OpenCode session started",
        "info",
        res?.session_url ? { label: "Open Session", href: res.session_url } : undefined,
      );
      deps.setShowTaskOpenCodeConfig(false);
      await Promise.all([refreshDetail(true), deps.loadBoard(true)]);
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setStartingOpenCode(false);
    }
  }

  // ------------------------------------------------------------------ copy id
  /** Copy the FULL task ID to the clipboard from the truncated header badge.
   *  Prefers the Async Clipboard API and falls back to execCommand for plain-
   *  http deployments where it is unavailable (see copyTextToClipboard). */
  async function copyTaskId(id: string): Promise<void> {
    if (!id) return;
    const copied = await copyTextToClipboard(id);
    deps.toast(copied ? "Copied task ID to clipboard" : "Couldn't copy the task ID", copied ? "info" : "error");
  }

  // ------------------------------------------------------------------ refresh
  /** Fetch GET /tasks/{id} (full resume payload, comment tail 50) and apply it.
   *  An in-progress notes edit (notesDirty) is NEVER clobbered by a refresh.
   *  Ownership-guarded (task 01M3MMY78F1TRP6MKDRT0SKQWB): by the time the
   *  fetch resolves, the drawer may have been closed or switched to another
   *  task — a payload for a no-longer-active id is discarded (returns false)
   *  instead of overwriting the active task's detail. */
  async function refreshDetail(silent = false, idToFetch?: string): Promise<boolean> {
    const id = idToFetch ?? deps.detailId() ?? deps.detail()?.id;
    if (!id) return false;
    try {
      const t = await api.getTask(id, { commentLimit: 50 });
      if (!canApplyDetail(t.id, deps.detailId(), deps.detailOpen())) {
        return false; // drawer closed or switched since the fetch started — drop stale payload
      }
      applyDetail(t);
      return true;
    } catch (e) {
      if (!silent) deps.toast(errMsg(e), "error");
      // Drawer error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED): a failed fetch
      // for the task CURRENTLY open becomes a visible message in the drawer —
      // previously it left only a bare "Loading task…" with no way out. The
      // ownership gate mirrors canApplyDetail(): a failure for a task we no
      // longer have open must not touch the drawer's state. Silent SSE/poll
      // refreshes flow through here too; they cannot clobber a visible error
      // (they only refresh its message), and a successful one clears it via
      // applyDetail(). No automatic retry loop — re-fetching is user-driven
      // via retryDetailLoad() (the Retry button).
      if (deps.detailOpen() && deps.detailId() === id) {
        deps.setDetailLoadError(
          e instanceof ApiError && e.status === 404
            ? "This task was deleted or no longer exists."
            : errMsg(e),
        );
      }
      return false;
    }
  }

  /** Re-fetch the currently open task — the Retry button in the drawer's
   *  error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED). Non-silent, so a second
   *  failure toasts as well. refreshDetail()'s ownership guard keeps a stale
   *  result from landing on a task the user switched away mid-flight. */
  async function retryDetailLoad(): Promise<void> {
    if (!deps.detailOpen() || deps.detailRetrying()) return;
    const id = deps.detailId();
    if (!id) return;
    deps.setDetailRetrying(true);
    try {
      await refreshDetail(false, id);
    } finally {
      deps.setDetailRetrying(false);
    }
  }

  function applyDetail(t: TaskDetail): void {
    // Ownership guard (task 01M3MMY78F1TRP6MKDRT0SKQWB), second layer under
    // refreshDetail's: covers direct call sites too (409-retry re-reads).
    // Discarding BEFORE any state write also guarantees a stale/foreign
    // payload can never reset or clobber an in-progress notes draft.
    if (!canApplyDetail(t.id, deps.detailId(), deps.detailOpen())) {
      return;
    }
    // A payload that actually lands means the drawer recovered: clear any
    // pending fetch-error state (ticket 01M3MMYFRMAW7XM6QDTXNQA6ED). Covers
    // every apply path — refreshDetail success and the 409-rebase re-reads.
    deps.setDetailLoadError(null);
    const current = deps.detail();
    const existingComments = (current && current.id === t.id) ? current.comments : [];
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
    deps.setDetail(t);
    // Task detail loaded → the tab title names it (WCAG 2.4.2). Covers every
    // load path: openTask, deep link, popstate and SSE re-fetches (a remote
    // rename while the drawer is open re-titles the tab too).
    deps.updateDocumentTitle();
    if (t.comments.length > deps.totalComments()) {
      deps.setTotalComments(t.comments.length);
    }
    deps.setAssigneeInputName(t.assignee?.name ?? "");
    deps.setAssigneeInputType((t.assignee?.type as "human" | "agent") || "human");
    // Learn the blocked state for card badges — owned by modules/board.ts
    // (scope guard: only while the task's project IS the selected one; board
    // loads own the cache scope, see selectProject).
    deps.learnBlockedFromDetail(t.project_id, t.id, t.blocked, t.blockers);
    // Never clobber an in-progress edit of THIS task; a scratchpad that
    // belongs to a DIFFERENT task is discarded so drafts cannot leak across
    // tasks (AC 2).
    if (!(deps.notesDirty() && deps.notesDraftOwner() === t.id)) {
      const savedNotes = safeStorage.getItem(notesDraftKey(t.id));
      if (savedNotes !== null && savedNotes.trim() !== "") {
        deps.setNotesDraft(savedNotes);
        deps.setNotesDirty(true);
      } else {
        deps.setNotesDraft(t.notes);
        deps.setNotesDirty(false);
      }
      deps.setNotesDraftOwner(t.id);
      deps.setNotesShowPreview(false);
    }
  }

  // ------------------------------------------------------------- operational bar
  function toggleStatusMenu(): void {
    deps.setStatusMenuOpen(!deps.statusMenuOpen());
    deps.setOwnerPopoverOpen(false);
    deps.setPriorityPopoverOpen(false);
    deps.setDuePopoverOpen(false);
  }

  function toggleOwnerPopover(): void {
    deps.setOwnerPopoverOpen(!deps.ownerPopoverOpen());
    deps.setStatusMenuOpen(false);
    deps.setPriorityPopoverOpen(false);
    deps.setDuePopoverOpen(false);
    const detail = deps.detail();
    if (deps.ownerPopoverOpen() && detail) {
      deps.setAssigneeInputName(detail.assignee?.name ?? "");
      deps.setAssigneeInputType((detail.assignee?.type as "human" | "agent") || "human");
    }
  }

  /** Version-guarded status move from detail drawer. On 409 version_conflict:
   *  re-read the task, re-apply the SAME intent once with the fresh version;
   *  a second conflict just refreshes and says so (per docs/API.md error contract). */
  async function moveStatus(status: Status): Promise<void> {
    deps.setStatusMenuOpen(false);
    const t = deps.detail();
    // Per-task: only the open task's own in-flight move blocks its buttons.
    if (!t || deps.isTaskBusy(t.id) || t.status === status) return;
    await deps.moveTaskStatus(t.id, status, t.version);
  }

  // ------------------------------------------------------------------- notes
  /** Clear the notes scratchpad so a closed or abandoned drawer can never
   *  leak its draft into another task (AC 1 + AC 2). */
  function resetNotesDraft(): void {
    deps.setNotesDirty(false);
    deps.setNotesDraft("");
    deps.setNotesDraftOwner(null);
    deps.setNotesShowPreview(false);
  }

  function saveNotesDraft(): void {
    const id = deps.detail()?.id;
    if (!id) return;
    const raw = deps.notesDraft() ?? "";
    if (raw.trim() !== "") safeStorage.setItem(notesDraftKey(id), raw);
    else safeStorage.removeItem(notesDraftKey(id));
  }

  function clearNotesDraft(id?: string): void {
    const targetId = id || deps.detail()?.id;
    if (!targetId) return;
    safeStorage.removeItem(notesDraftKey(targetId));
  }

  /** Show the unsaved-notes confirm and open its focus session. "Keep
   *  editing" (least destructive) receives initial focus. */
  function promptUnsavedNotes(action: UnsavedNotesAction): void {
    deps.setUnsavedNotesAction(action);
    confirmNotesFocus = deps.openDialog("cb-notes-confirm", "button.btn-ghost");
  }

  /** "Keep editing" — dismiss the prompt, draft untouched. Focus returns to
   *  whatever had focus inside the drawer when the prompt opened (stacked
   *  session: only THIS layer is closed). */
  function cancelUnsavedNotes(): void {
    deps.setUnsavedNotesAction(null);
    deps.closeDialog(confirmNotesFocus);
    confirmNotesFocus = null;
  }

  /** "Discard changes" — drop the draft, then run the deferred close/switch.
   *  The confirm session is closed first; a drawer close afterwards restores
   *  focus to the original board trigger (the switch path re-focuses the new
   *  drawer content via openTask). */
  async function confirmUnsavedNotes(): Promise<void> {
    const action = deps.unsavedNotesAction();
    deps.setUnsavedNotesAction(null);
    const owner = deps.notesDraftOwner();
    if (owner) {
      safeStorage.removeItem(notesDraftKey(owner));
    }
    resetNotesDraft();
    deps.closeDialog(confirmNotesFocus);
    confirmNotesFocus = null;
    if (action?.kind === "open") await openTask(action.taskId);
    else if (action?.kind === "close") closeDetail();
  }

  /** PUT /notes replaces the whole blob and carries NO version — a concurrent
   *  write can still 409; then we refresh (keeping the user's draft) and say so. */
  async function saveNotes(): Promise<void> {
    const t = deps.detail();
    if (!t || deps.savingNotes()) return;
    // AC 3 guard: a draft may only ever be saved to the task it belongs to.
    // Unreachable through the UI (openTask/closeDetail gate drafts), but if
    // stale state ever appears we must never PUT one task's draft onto another.
    if (deps.notesDraftOwner() !== null && deps.notesDraftOwner() !== t.id) {
      resetNotesDraft();
      await refreshDetail(true);
      deps.toast("Notes draft was from a different task — discarded and refreshed.", "error");
      return;
    }
    deps.setSavingNotes(true);
    try {
      await api.putNotes(t.id, { actor: deps.actor(), notes: deps.notesDraft() });
      safeStorage.removeItem(notesDraftKey(t.id));
      deps.setNotesDirty(false);
      await Promise.all([refreshDetail(true), deps.loadBoard(true)]);
    } catch (e) {
      if (ApiError.isVersionConflict(e)) {
        // notesDraft survives the refresh because notesDirty is still true
        await refreshDetail(true).catch(() => undefined);
        deps.toast("Notes changed elsewhere — refreshed. Review before re-saving.", "info");
      } else {
        deps.toast(errMsg(e), "error");
      }
    } finally {
      deps.setSavingNotes(false);
    }
  }

  return {
    openTask,
    requestCloseDetail,
    closeDetail,
    openFullPage,
    openDeepLinkedTask,
    initTaskOpenCodeConfig,
    toggleOpenCodeConfig,
    taskOpenCodeDirty,
    activeOpenCodeSession,
    startTaskInOpenCode,
    copyTaskId,
    refreshDetail,
    retryDetailLoad,
    applyDetail,
    toggleStatusMenu,
    toggleOwnerPopover,
    moveStatus,
    resetNotesDraft,
    saveNotesDraft,
    clearNotesDraft,
    promptUnsavedNotes,
    cancelUnsavedNotes,
    confirmUnsavedNotes,
    saveNotes,
  };
}

export type DetailModule = ReturnType<typeof createDetailModule>;
