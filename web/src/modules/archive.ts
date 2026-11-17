// Board-level ARCHIVE management — Phase 2 batch D of the frontend
// modularization epic. Owns the BEHAVIOR of the archived-view feature from
// app.ts: single-task archive/unarchive with optimistic removal + rollback +
// Undo toast (AC1), the bulk "Archive all Done" two-click confirm flow (AC2),
// and the "Show archived" view mode toggle (AC3) with its paged fetch.
// Archived rows live in a SEPARATE data source from `board` — the board
// endpoint (allOpenTasks) only ever returns NON-archived tasks, so the two can
// never mix; toggling OFF restores the previous board EXACTLY because `board`
// was never mutated by this feature.
// Every body moved here verbatim from app.ts; see docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html reads/
// writes still lives on the flat component root as a plain field, touched here
// exclusively through the typed accessors on {@link ArchiveDeps}. The archived
// fetch sequence guard is INTERNAL state — never rendered, never part of the
 // flat namespace. The per-task in-flight lock (busyTaskIds) is SHARED with the
 // move flow: same Set object, mutated in place through the state-IN getter.

import { ApiError, api, errMsg } from "../api";
import { beginMove, endMove } from "../moves";
import { createSequenceGuard } from "../refresh";
import { STATUSES } from "../types";
import type { ActorInput, Board, Status, TaskDetail, TaskSummary } from "../types";

/** Typed dependency surface for the archive module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface ArchiveDeps {
  // -- state-in: live accessors into root-owned fields ------------------------
  board(): Board | null;
  showArchived(): boolean;
  archivedBoardTasks(): Record<Status, TaskSummary[]> | null;
  selectedProjectId(): string;
  detail(): TaskDetail | null;
  archiveAllDoneConfirmOpen(): boolean;
  archiveAllDoneBusy(): boolean;
  /** Per-task in-flight lock — SHARED with the move flow (same Set identity). */
  busyTaskIds(): Set<string>;

  // -- state-out: root writes, invoked so mutations flow through the root -----
  setShowArchived(v: boolean): void;
  setArchivedBoardTasks(v: Record<Status, TaskSummary[]> | null): void;
  setArchiveAllDoneConfirmOpen(v: boolean): void;
  setArchiveAllDoneBusy(v: boolean): void;

  // -- methods-out: root hooks -------------------------------------------------
  toast(
    msg: string,
    kind?: "error" | "info",
    action?: { label: string; href?: string; onAction?: () => void }
  ): number;
  actor(): ActorInput;
  clearSelection(): void;
  refreshDetail(force?: boolean): Promise<boolean>;
  applyDetail(t: TaskDetail): void;
  loadBoard(silent?: boolean): Promise<void>;
  /** The root `refreshArchivedView` shim — routed back through the root so
   *  test stubs of the flat namespace still observe internal refreshes. */
  refreshArchivedView(): Promise<void>;
}

export function createArchiveModule(deps: ArchiveDeps) {
  /** Guards the archived-view fetch: rapid toggle-off/toggle-on must let only
   *  the NEWEST fetch land (mirrors the board module's sequence guards). */
  const archivedSeq = createSequenceGuard();

  /** True when the columns should render the ARCHIVED data source instead of
   *  the live board (and the done cap is bypassed — archived sets are small
   *  and there is no "show more" affordance in that mode). */
  function archivedColumns(): Record<Status, TaskSummary[]> | null {
    return deps.showArchived() ? deps.archivedBoardTasks() : null;
  }

  /** Fetch the selected project's full archived set (paged at 100/page — the
   *  API max) grouped by status. Returns null when superseded by a newer load
   *  or a project switch mid-flight, so callers know NOT to apply. */
  async function _fetchArchivedTasks(projectId: string): Promise<Record<Status, TaskSummary[]> | null> {
    const token = archivedSeq.next();
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
    if (!archivedSeq.isFresh(token) || projectId !== deps.selectedProjectId()) return null;
    const tasks: Record<Status, TaskSummary[]> = { todo: [], doing: [], done: [] };
    for (const t of out) tasks[t.status].push(t);
    return tasks;
  }

  /** Toggle the "Show archived" board view. OFF is a pure flag flip — the
   *  untouched `board` renders again, byte-for-byte (AC3's exact-restore
   *  guarantee). ON fetches the full archived set for the selected project and
   *  groups it by status. Failure reverts the flag and toasts. */
  async function toggleShowArchived(): Promise<void> {
    if (deps.showArchived()) {
      deps.setShowArchived(false); // instant restore of the previous board
      return;
    }
    const projectId = deps.selectedProjectId();
    if (!projectId) return;
    try {
      const tasks = await _fetchArchivedTasks(projectId);
      if (tasks === null) return; // superseded — do not flip, do not apply
      deps.clearSelection(); // selection applies to live cards only — never into the archived view
      deps.setArchivedBoardTasks(tasks);
      deps.setShowArchived(true);
    } catch (e) {
      deps.toast(errMsg(e), "error");
    }
  }

  /** Re-fetch the archived view after a mutation that changed its contents
   *  (unarchiving from within it, or an archive we just performed). Only runs
   *  while the mode is ON; failures are silent — the toasts on the mutations
   *  themselves already told the user what happened. */
  async function refreshArchivedView(): Promise<void> {
    if (!deps.showArchived() || !deps.selectedProjectId()) return;
    try {
      const tasks = await _fetchArchivedTasks(deps.selectedProjectId());
      if (tasks !== null) deps.setArchivedBoardTasks(tasks);
    } catch {
      /* keep the last known view */
    }
  }

  /** AC1: archive or unarchive ONE task — from the card ⋮ menu / the archived
   *  view's per-card Unarchive, or from the task detail header. Reuses the
   *  move flow's concurrency primitives (busyTaskIds lock) and its 409
   *  re-read + retry-once discipline. Archive is optimistic (the card leaves
   *  its column immediately) with rollback on failure and a ~5s Undo toast;
   *  unarchive relies on the post-PATCH board reload for the card's return. */
  async function setTaskArchived(taskId: string, archived: boolean): Promise<void> {
    // Locate the row: live board card, ARCHIVED-view card (when that mode is
    // ON the cards come from a different array), or the open detail payload.
    let summary: TaskSummary | null = null;
    let inBoardStatus: Status | null = null;
    if (deps.board()) {
      for (const s of STATUSES) {
        const found = deps.board()!.tasks[s]?.find((t) => t.id === taskId);
        if (found) {
          summary = found;
          inBoardStatus = s;
          break;
        }
      }
    }
    let inArchivedStatus: Status | null = null;
    if (!summary && deps.showArchived() && deps.archivedBoardTasks()) {
      for (const s of STATUSES) {
        const found = deps.archivedBoardTasks()![s]?.find((t) => t.id === taskId);
        if (found) {
          summary = found;
          inArchivedStatus = s;
          break;
        }
      }
    }
    const detail = deps.detail();
    const detailOpen = !!detail && detail.id === taskId;
    if (!summary && !detailOpen) return;

    // Per-task in-flight lock (shared with the move flow): the same id never
    // double-fires; every OTHER card stays interactive meanwhile.
    if (!beginMove(deps.busyTaskIds(), taskId)) {
      deps.toast("That task is still busy — one moment.", "info");
      return;
    }

    const versionToUse = summary?.version ?? (deps.detail() ? deps.detail()!.version : 0);
    const patch = (version: number): Promise<TaskSummary> =>
      api.updateTask(taskId, { actor: deps.actor(), version, archived });

    // Optimistic removal for ARCHIVE: the card leaves its column now. Failure
    // rolls this back; success re-syncs with loadBoard regardless. Unarchive
    // removes from the archived view instead (the card "returns" to the live
    // board on the reload — we don't guess its post-refresh position).
    type Snap = { where: "board" | "archived"; status: Status; task: TaskSummary };
    let snap: Snap | null = null;
    if (archived && summary && inBoardStatus && deps.board()) {
      snap = { where: "board", status: inBoardStatus, task: summary };
      const board = deps.board()!;
      board.tasks[inBoardStatus] = board.tasks[inBoardStatus].filter((t) => t.id !== taskId);
      if ((board.counts[inBoardStatus] ?? 0) > 0) board.counts[inBoardStatus]--;
    } else if (!archived && summary && inArchivedStatus && deps.archivedBoardTasks()) {
      snap = { where: "archived", status: inArchivedStatus, task: summary };
      const archivedTasks = deps.archivedBoardTasks()!;
      archivedTasks[inArchivedStatus] = archivedTasks[inArchivedStatus].filter((t) => t.id !== taskId);
    }
    const rollbackSnap = (): void => {
      if (!snap) return;
      if (snap.where === "board" && deps.board()) {
        const board = deps.board()!;
        board.tasks[snap.status] = [...board.tasks[snap.status], snap.task];
        board.counts[snap.status] = (board.counts[snap.status] ?? 0) + 1;
      } else if (snap.where === "archived" && deps.archivedBoardTasks()) {
        const archivedTasks = deps.archivedBoardTasks()!;
        archivedTasks[snap.status] = [...archivedTasks[snap.status], snap.task];
      }
    };

    const resync = (): Promise<void> =>
      (async () => {
        await Promise.all([
          detailOpen ? deps.refreshDetail(true) : undefined,
          deps.loadBoard(true),
          deps.showArchived() ? deps.refreshArchivedView() : undefined,
        ]);
      })();

    try {
      const updated = await patch(versionToUse);
      if (detailOpen && deps.detail()) deps.detail()!.archived = archived;
      await resync();
      if (archived) {
        // Undo window = the toast's default auto-expiry (~6.5s). Undo re-applies
        // with the FRESH version returned by THIS patch, then re-syncs everything.
        const undoVersion = updated.version;
        deps.toast("Task archived", "info", {
          label: "Undo",
          onAction: () => _undoArchive(taskId, undoVersion),
        });
      } else {
        deps.toast("Task restored from archive");
      }
    } catch (e) {
      if (ApiError.isVersionConflict(e)) {
        // Concurrent edit elsewhere — re-read the fresh row and retry ONCE.
        try {
          const fresh = await api.getTask(taskId, { commentLimit: 1 });
          if (detailOpen && deps.detail()) deps.applyDetail(fresh);
          if (fresh.archived !== archived) {
            await patch(fresh.version);
          }
          await resync();
          deps.toast("Changed elsewhere — retried with the fresh version.", "info");
        } catch (e2) {
          rollbackSnap();
          await Promise.all([
            detailOpen ? deps.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
            deps.loadBoard(true).catch(() => undefined),
          ]);
          if (ApiError.isVersionConflict(e2)) {
            deps.toast("Changed elsewhere — refreshed (your change was not applied).", "info");
          } else {
            deps.toast(errMsg(e2), "error");
          }
        }
      } else {
        rollbackSnap();
        await Promise.all([
          detailOpen ? deps.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
          deps.loadBoard(true).catch(() => undefined),
        ]);
        deps.toast(errMsg(e), "error");
      }
    } finally {
      endMove(deps.busyTaskIds(), taskId);
    }
  }

  /** The Undo action behind the archive toast: re-apply archived:false with
   *  the fresh version from the original patch. Throws on failure so
   *  runToastAction surfaces an error toast (the board reloads to reality). */
  async function _undoArchive(taskId: string, version: number): Promise<void> {
    try {
      await api.updateTask(taskId, { actor: deps.actor(), version, archived: false });
      await Promise.all([
        deps.loadBoard(true),
        deps.showArchived() ? deps.refreshArchivedView() : Promise.resolve(),
      ]);
    } catch (e) {
      await deps.loadBoard(true).catch(() => undefined);
      throw e;
    }
  }

  // ------------------------------------------------- "Archive all Done" (bulk)
  function openArchiveAllDoneConfirm(): void {
    deps.setArchiveAllDoneConfirmOpen(true);
  }

  function cancelArchiveAllDoneConfirm(): void {
    deps.setArchiveAllDoneConfirmOpen(false);
  }

  /** AC2: sequentially archive EVERY done task of the project (the full
   *  board.tasks.done list — the cap only limits RENDERING, so hidden older
   *  cards are archived too). Each row is a version-guarded PATCH; a 409
   *  re-reads that ONE task and retries it once. Optimistic removal happens
   *  per success; a final silent reload settles counts/blocked state. */
  async function confirmArchiveAllDone(): Promise<void> {
    if (deps.archiveAllDoneBusy() || !deps.board()) return;
    const done = [...(deps.board()!.tasks.done ?? [])];
    deps.setArchiveAllDoneConfirmOpen(false);
    if (done.length === 0) return;
    deps.setArchiveAllDoneBusy(true);
    let ok = 0;
    let failed = 0;
    try {
      for (const t of done) {
        const removeLocal = (): void => {
          if (!deps.board()) return;
          const board = deps.board()!;
          board.tasks.done = board.tasks.done.filter((x) => x.id !== t.id);
          if ((board.counts.done ?? 0) > 0) board.counts.done--;
        };
        try {
          await api.updateTask(t.id, { actor: deps.actor(), version: t.version, archived: true });
          ok++;
          removeLocal();
        } catch (e) {
          if (ApiError.isVersionConflict(e)) {
            // Stale version: re-read board state for THIS task and retry once.
            try {
              const fresh = await api.getTask(t.id, { commentLimit: 1 });
              if (!fresh.archived) {
                await api.updateTask(t.id, { actor: deps.actor(), version: fresh.version, archived: true });
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
      deps.setArchiveAllDoneBusy(false);
    }
    await deps.loadBoard(true).catch(() => undefined);
    if (deps.showArchived()) await deps.refreshArchivedView();
    const n = ok === 1 ? "task" : "tasks";
    if (failed === 0) deps.toast(`Archived ${ok} done ${n}`);
    else deps.toast(`Archived ${ok} done ${n} · ${failed} failed`, "error");
  }

  /** Project-switch cascade (selectProject): the archived view belongs to the
   *  project being LEFT — exit the mode, drop its data, and retire any
   *  in-flight archived fetch (its rows describe the previous project).
   *  Mirrors how selectProject invalidates board/search. */
  function resetForProjectSwitch(): void {
    deps.setShowArchived(false);
    deps.setArchivedBoardTasks(null);
    deps.setArchiveAllDoneConfirmOpen(false);
    archivedSeq.next();
  }

  return {
    archivedColumns,
    _fetchArchivedTasks,
    toggleShowArchived,
    refreshArchivedView,
    setTaskArchived,
    _undoArchive,
    openArchiveAllDoneConfirm,
    cancelArchiveAllDoneConfirm,
    confirmArchiveAllDone,
    resetForProjectSwitch,
  };
}

export type ArchiveModule = ReturnType<typeof createArchiveModule>;
