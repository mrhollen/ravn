// Card DRAG-AND-DROP + touch swipe-to-status + per-card "Move to" menu — Phase 2
// batch D of the frontend modularization epic. Owns the BEHAVIOR of board card
// repositioning from app.ts: HTML5 drag/drop (with illegal-transition grey-out
// and drop guards), the touch alternative (swipe one column, or the ⋮ menu),
// and the shared optimistic `moveTaskStatus` engine both funnel into — with its
// per-task in-flight lock (busyTaskIds), 409 re-read + retry-once discipline,
// timeout-guarded PATCH, and rollback of the optimistic board move. The pure
// transition/swipe math already lives in src/moves.ts (untouched here).
// Every body moved here verbatim from app.ts; see docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html reads/
// writes still lives on the flat component root as a plain field, touched here
// exclusively through the typed accessors on {@link DragDeps}. The per-task
// in-flight lock (busyTaskIds) is SHARED with the archive flow: same Set
// object, mutated in place through the state-IN getter.

import { ApiError, api, errMsg } from "../api";
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
} from "../moves";
import { STATUSES } from "../types";
import type { ActorInput, Board, Status, TaskDetail, TaskSummary } from "../types";

/** Display clamp for the in-flight swipe offset (px). Long flings must not
 *  throw the card far off-screen; slop + commit thresholds keep using raw dx. */
const MAX_SWIPE_PX = 96;

/** In-progress touch-swipe gesture state (the flat root `swipeState` field,
 *  read by index.html for the live transform + direction pill). */
export interface SwipeState {
  id: string | null;
  startX: number;
  startY: number;
  dx: number;
  fromStatus: Status | null;
  active: boolean;
}

function idleSwipeState(): SwipeState {
  return { id: null, startX: 0, startY: 0, dx: 0, fromStatus: null, active: false };
}

/** Typed dependency surface for the drag module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface DragDeps {
  // -- state-in: live accessors into root-owned fields ------------------------
  showArchived(): boolean;
  board(): Board | null;
  detail(): TaskDetail | null;
  draggedTaskId(): string | null;
  draggedFromStatus(): Status | null;
  dragOverStatus(): Status | null;
  swipeState(): SwipeState;
  cardMenuTaskId(): string | null;
  /** Per-task in-flight lock — SHARED with the archive flow (same Set identity). */
  busyTaskIds(): Set<string>;
  isTaskBusy(id: string): boolean;

  // -- state-out: root writes, invoked so mutations flow through the root -----
  setDraggedTaskId(v: string | null): void;
  setDraggedFromStatus(v: Status | null): void;
  setDragOverStatus(v: Status | null): void;
  setIsDragging(v: boolean): void;
  setSwipeState(v: SwipeState): void;
  setCardMenuTaskId(v: string | null): void;

  // -- methods-out: root hooks -------------------------------------------------
  toast(msg: string, kind?: "error" | "info"): number;
  actor(): ActorInput;
  loadBoard(silent?: boolean): Promise<void>;
  refreshDetail(force?: boolean): Promise<boolean>;
  applyDetail(t: TaskDetail): void;
}

export function createDragModule(deps: DragDeps) {
  // ------------------------------------------------------------- drag and drop

  /** Template helper: may the CURRENTLY-DRAGGED card be dropped on `status`?
   *  (False while nothing legal is known — grey-out styling for illegal
   *  columns reads this. The transition map itself lives in moves.ts.) */
  function dragTargetLegal(status: Status): boolean {
    return dropAllowed(deps.draggedFromStatus(), status);
  }

  function onDragStart(e: DragEvent, task: TaskSummary, fromStatus: Status): void {
    // The archived view is read-only for positioning: its cards live in a
    // separate array the optimistic move code doesn't own, so drag/swipe is
    // disabled there (Unarchive is the only action available).
    if (deps.showArchived()) {
      e.preventDefault();
      return;
    }
    // Only the card's OWN in-flight move blocks it (per-task lock); tell the
    // user instead of silently refusing the drag.
    if (deps.isTaskBusy(task.id)) {
      e.preventDefault();
      deps.toast("That task is still moving — one moment.", "info");
      return;
    }
    deps.setIsDragging(true);
    deps.setDraggedTaskId(task.id);
    deps.setDraggedFromStatus(fromStatus);
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", task.id);
    }
  }

  function onDragEnd(): void {
    deps.setDraggedTaskId(null);
    deps.setDraggedFromStatus(null);
    deps.setDragOverStatus(null);
    window.setTimeout(() => {
      deps.setIsDragging(false);
    }, 50);
  }

  function onDragOver(e: DragEvent, status: Status): void {
    if (!deps.draggedTaskId() || !deps.draggedFromStatus()) return;
    // Illegal transition → do NOT preventDefault: the browser then shows a
    // no-drop cursor and never fires @drop, so illegal columns can't take
    // the card (the grey-out styling below tells the user why).
    if (!canMove(deps.draggedFromStatus() as Status, status)) return;
    e.preventDefault();
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = "move";
    }
    if (deps.dragOverStatus() !== status) {
      deps.setDragOverStatus(status);
    }
  }

  function onDragLeave(e: DragEvent, status: Status): void {
    const currentTarget = e.currentTarget as HTMLElement | null;
    const relatedTarget = e.relatedTarget as Node | null;
    if (currentTarget && relatedTarget && currentTarget.contains(relatedTarget)) {
      return;
    }
    if (deps.dragOverStatus() === status) {
      deps.setDragOverStatus(null);
    }
  }

  async function onDrop(e: DragEvent, targetStatus: Status): Promise<void> {
    e.preventDefault();
    const taskId = deps.draggedTaskId() || e.dataTransfer?.getData("text/plain");
    const fromStatus = deps.draggedFromStatus();
    deps.setDragOverStatus(null);
    deps.setDraggedTaskId(null);
    deps.setDraggedFromStatus(null);
    window.setTimeout(() => {
      deps.setIsDragging(false);
    }, 50);

    if (!taskId || !fromStatus || fromStatus === targetStatus) {
      return;
    }
    // Belt-and-braces with the dragover guard: a drop on an illegal target
    // is ignored (with feedback), never sent to the server.
    if (!canMove(fromStatus, targetStatus)) {
      deps.toast(illegalMoveMessage(fromStatus, targetStatus), "info");
      return;
    }
    await moveTaskStatus(taskId, targetStatus);
  }

  // ---------------------------------- touch: swipe-to-status + per-card move menu
  // Explicit touch alternative to HTML5 drag-and-drop (which never fires on
  // touch devices). A mostly-horizontal swipe moves the card one column in
  // that direction; the ⋮ button opens a floating "Move to" dropdown anchored
  // to the trigger (absolutely positioned, dismissed by outside clicks and
  // Escape). Both funnel into moveTaskStatus(), so 409-rebase + rollback
  // behaviour is shared.

  function swiping(id: string): boolean {
    const s = deps.swipeState();
    return s.active && s.id === id;
  }

  function swipeDirection(id: string): "right" | "left" {
    return swipeDirFor(swiping(id), deps.swipeState().dx);
  }

  function swipeTargetLabel(id: string): string {
    return swipeLabelFor(deps.swipeState().fromStatus, swipeDirection(id));
  }

  function onCardTouchStart(e: TouchEvent, task: TaskSummary): void {
    // Archived view disables swipe-to-move (see onDragStart).
    if (deps.showArchived()) return;
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    deps.setSwipeState({
      id: task.id,
      startX: t.clientX,
      startY: t.clientY,
      dx: 0,
      fromStatus: task.status,
      active: false,
    });
  }

  function onCardTouchMove(e: TouchEvent): void {
    const s = deps.swipeState();
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
  }

  function onCardTouchEnd(e: TouchEvent, task: TaskSummary): void {
    const s = deps.swipeState();
    if (!s || !s.id || s.id !== task.id) return;
    const endX = e.changedTouches[0]?.clientX ?? s.startX + s.dx;
    const dx = endX - s.startX;
    deps.setSwipeState(idleSwipeState());
    if (!s.active || !s.fromStatus) return;
    const dir = committedSwipeDirection(dx);
    const target = dir ? swipeTargetStatus(s.fromStatus, dir) : null;
    if (!target) return;
    // Swipes move exactly one column at a time. The only adjacent pair that
    // the domain transition map rejects is done → doing (left-swipe on a Done
    // card); refuse it with feedback instead of hitting the 400 + rollback.
    if (!canMove(s.fromStatus, target)) {
      deps.toast(illegalMoveMessage(s.fromStatus, target), "info");
      return;
    }
    void moveTaskStatus(task.id, target);
  }

  function onCardTouchCancel(_e: TouchEvent): void {
    // A cancelled gesture (system interrupt, etc.) never commits a move —
    // just snap the card back home via the release transition.
    deps.setSwipeState(idleSwipeState());
  }

  function toggleCardMenu(id: string): void {
    deps.setCardMenuTaskId(deps.cardMenuTaskId() === id ? null : id);
  }

  async function moveCardTo(id: string, status: Status): Promise<void> {
    deps.setCardMenuTaskId(null);
    await moveTaskStatus(id, status);
  }

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
  async function moveTaskStatus(taskId: string, targetStatus: Status, initialVersion?: number): Promise<void> {
    // Locate task in current board columns or open detail
    let currentStatus: Status | null = null;
    let taskSummary: TaskSummary | null = null;
    const board = deps.board();
    if (board) {
      for (const s of STATUSES) {
        const found = board.tasks[s]?.find((t) => t.id === taskId);
        if (found) {
          currentStatus = s;
          taskSummary = found;
          break;
        }
      }
    }
    const detail = deps.detail();
    if (!taskSummary && detail && detail.id === taskId) {
      taskSummary = detail;
      currentStatus = detail.status;
    }

    // No-op moves (unknown card, drop onto own column) need no lock at all.
    if (!taskSummary || !currentStatus || currentStatus === targetStatus) {
      return;
    }

    // Per-task in-flight lock: same id already moving → feedback, not silence.
    if (!beginMove(deps.busyTaskIds(), taskId)) {
      deps.toast("That task is still moving — one moment.", "info");
      return;
    }

    const versionToUse = initialVersion ?? taskSummary.version;

    // Move-related PATCH under the timeout guard. A stall becomes a
    // MoveTimedOutError so the catch below can roll back + toast honestly; a
    // real API error passes through untouched (409 handling stays intact).
    const patch = (version: number): Promise<TaskSummary> =>
      withTimeout(MOVE_TIMEOUT_MS, (signal) =>
        api.updateTask(taskId, { actor: deps.actor(), version, status: targetStatus }, { signal }),
      ).then((r) => {
        if (r.value !== undefined) return r.value;
        throw r.timedOut ? new MoveTimedOutError() : r.error;
      });

    // 1. Snapshot board state for rollback
    const b = deps.board();
    const prevBoardTasks: Record<Status, TaskSummary[]> | null = b
      ? {
          todo: [...b.tasks.todo],
          doing: [...b.tasks.doing],
          done: [...b.tasks.done],
        }
      : null;
    const prevBoardCounts = b ? { ...b.counts } : null;
    const d = deps.detail();
    const prevDetailStatus = d && d.id === taskId ? d.status : null;

    const rollback = () => {
      const bb = deps.board();
      if (bb && prevBoardTasks && prevBoardCounts) {
        bb.tasks = prevBoardTasks;
        bb.counts = prevBoardCounts;
      }
      const dd = deps.detail();
      if (dd && dd.id === taskId && prevDetailStatus !== null) {
        dd.status = prevDetailStatus;
      }
    };

    // 2. Optimistic update
    if (deps.board()) {
      deps.board()!.tasks[currentStatus] = deps.board()!.tasks[currentStatus].filter((t) => t.id !== taskId);
      const movedTask: TaskSummary = { ...taskSummary, status: targetStatus };
      deps.board()!.tasks[targetStatus] = [...deps.board()!.tasks[targetStatus], movedTask];
      if (deps.board()!.counts[currentStatus] > 0) {
        deps.board()!.counts[currentStatus]--;
      }
      deps.board()!.counts[targetStatus]++;
    }

    if (deps.detail() && deps.detail()!.id === taskId) {
      deps.detail()!.status = targetStatus;
    }

    try {
      const updated = await patch(versionToUse);
      if (deps.board()) {
        const inBoard = deps.board()!.tasks[targetStatus]?.find((t) => t.id === taskId);
        if (inBoard) {
          Object.assign(inBoard, updated);
        }
      }
      await Promise.all([
        deps.detail() && deps.detail()!.id === taskId ? deps.refreshDetail(true) : Promise.resolve(),
        deps.loadBoard(true),
      ]);
    } catch (e) {
      if (ApiError.isVersionConflict(e)) {
        try {
          const fresh = await api.getTask(taskId, { commentLimit: 50 });
          if (deps.detail() && deps.detail()!.id === taskId) {
            deps.applyDetail(fresh);
          }
          if (fresh.status !== targetStatus) {
            // Retry under the same timeout guard — a stall here must release
            // the per-task lock too (handled by the e2 branch + finally).
            await patch(fresh.version);
          }
          await Promise.all([
            deps.detail() && deps.detail()!.id === taskId ? deps.refreshDetail(true) : Promise.resolve(),
            deps.loadBoard(true),
          ]);
          deps.toast("Changed elsewhere — retried with the fresh version.", "info");
        } catch (e2) {
          rollback();
          await Promise.all([
            deps.detail() && deps.detail()!.id === taskId ? deps.refreshDetail(true).catch(() => undefined) : Promise.resolve(),
            deps.loadBoard(true).catch(() => undefined),
          ]);
          if (ApiError.isVersionConflict(e2)) {
            deps.toast("Changed elsewhere — refreshed (your move was not applied).", "info");
          } else if (e2 instanceof MoveTimedOutError) {
            // Server answered the first PATCH with a conflict but never
            // completed the retry — state may be half-applied, so say "may".
            deps.toast("Move timed out — your change may not have saved. Please try again.", "error");
          } else {
            deps.toast(friendlyTransitionError(e2) ?? errMsg(e2), "error");
          }
        }
      } else if (e instanceof MoveTimedOutError) {
        rollback();
        await deps.loadBoard(true).catch(() => undefined);
        deps.toast("Move timed out — your change was not saved. Please try again.", "error");
      } else {
        rollback();
        await deps.loadBoard(true).catch(() => undefined);
        // Server-side rejections (e.g. another agent moved the card mid-drag)
        // surface with friendly wording for known error shapes, raw text otherwise.
        deps.toast(friendlyTransitionError(e) ?? errMsg(e), "error");
      }
    } finally {
      endMove(deps.busyTaskIds(), taskId);
    }
  }

  return {
    dragTargetLegal,
    onDragStart,
    onDragEnd,
    onDragOver,
    onDragLeave,
    onDrop,
    swiping,
    swipeDirection,
    swipeTargetLabel,
    onCardTouchStart,
    onCardTouchMove,
    onCardTouchEnd,
    onCardTouchCancel,
    toggleCardMenu,
    moveCardTo,
    moveTaskStatus,
  };
}

export type DragModule = ReturnType<typeof createDragModule>;
