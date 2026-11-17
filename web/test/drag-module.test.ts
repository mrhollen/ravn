// Unit tests for src/modules/drag.ts — board card DRAG-AND-DROP, touch
// swipe-to-status and the per-card "Move to" menu extracted from web/src/app.ts
// (Phase 2 batch D). Covers: the drag lifecycle handlers (archived-view guard,
// per-task busy lock, illegal-transition guards), the swipe gesture state
// machine (slop/activation, display clamp, commit thresholds, the done→doing
// refusal), toggleCardMenu/moveCardTo, and the shared moveTaskStatus engine
// (optimistic update + rollback, 409 re-read + retry-once, busy guard, version
// precedence). The pure transition/swipe math is covered by moves-module tests;
// here it is pinned only as consumed.
//
// The module is pure: a mutable stub state object wired into the DragDeps
// accessors + counters for every out-bound write + monkey-patched api methods,
// all restored in finally blocks (same pattern as archive-module.test.ts).
// DOM events are duck-typed fakes — no browser globals needed.

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createDragModule } from "../src/modules/drag";
import type { SwipeState } from "../src/modules/drag";
import { api, ApiError, errMsg } from "../src/api";
import type { ActorInput, Board, Status, TaskDetail, TaskSummary } from "../src/types";

// Browser-global fake: onDragEnd/onDrop defer the isDragging release via
// window.setTimeout(…, 50) (a browser API). node:test has no DOM — alias
// `window` to the node global so the real timer runs (no clock mocking needed).
if (typeof (globalThis as Record<string, unknown>).window === "undefined") {
  (globalThis as Record<string, unknown>).window = globalThis;
}

// ----------------------------------------------------------------------- fixtures
const ACTOR: ActorInput = { name: "tester", type: "agent" };

function dragTask(id: string, status: Status, version = 1, over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id, title: "T-" + id, description: "", status, priority: 0, due_at: null,
    created_by: { name: "u", type: "human" }, version,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z",
    claimed_by: null, claim_expires_at: null, ac_total: 0, comment_count: 0,
    archived: false, ...over,
  };
}

function boardWith(byStatus: Partial<Record<Status, TaskSummary[]>> = {}): Board {
  const tasks = { todo: byStatus.todo ?? [], doing: byStatus.doing ?? [], done: byStatus.done ?? [] };
  return {
    project_id: "P1",
    tasks,
    counts: { todo: tasks.todo.length, doing: tasks.doing.length, done: tasks.done.length },
  };
}

function detailFor(id: string, status: Status = "todo", over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id, project_id: "P1", seq: 1, title: "detail", description: "", status,
    priority: 0, due_at: null, created_by: { name: "u", type: "human" }, version: 3,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z",
    completed_by: null, completed_at: null, archived: false, ...over,
  };
}

function idleSwipe(): SwipeState {
  return { id: null, startX: 0, startY: 0, dx: 0, fromStatus: null, active: false };
}

interface ToastRec { msg: string; kind?: string }

interface Ctx {
  mod: ReturnType<typeof createDragModule>;
  showArchived: boolean;
  board: Board | null;
  detail: TaskDetail | null;
  draggedTaskId: string | null;
  draggedFromStatus: Status | null;
  dragOverStatus: Status | null;
  isDragging: boolean;
  swipeState: SwipeState;
  cardMenuTaskId: string | null;
  busyTaskIds: Set<string>;
  toasts: ToastRec[];
  refreshDetailCalls: Array<boolean | undefined>;
  loadBoardCalls: Array<boolean | undefined>;
  applyDetailCalls: TaskDetail[];
}

function makeCtx(over: { board?: Board | null; detail?: TaskDetail | null; showArchived?: boolean } = {}): Ctx {
  const c: Ctx = {
    mod: undefined as never,
    showArchived: over.showArchived ?? false,
    board: over.board ?? boardWith(),
    detail: over.detail ?? null,
    draggedTaskId: null,
    draggedFromStatus: null,
    dragOverStatus: null,
    isDragging: false,
    swipeState: idleSwipe(),
    cardMenuTaskId: null,
    busyTaskIds: new Set<string>(),
    toasts: [],
    refreshDetailCalls: [],
    loadBoardCalls: [],
    applyDetailCalls: [],
  };
  c.mod = createDragModule({
    // state-in
    showArchived: () => c.showArchived,
    board: () => c.board,
    detail: () => c.detail,
    draggedTaskId: () => c.draggedTaskId,
    draggedFromStatus: () => c.draggedFromStatus,
    dragOverStatus: () => c.dragOverStatus,
    swipeState: () => c.swipeState,
    cardMenuTaskId: () => c.cardMenuTaskId,
    busyTaskIds: () => c.busyTaskIds,
    isTaskBusy: (id) => c.busyTaskIds.has(id),
    // state-out
    setDraggedTaskId: (v) => { c.draggedTaskId = v; },
    setDraggedFromStatus: (v) => { c.draggedFromStatus = v; },
    setDragOverStatus: (v) => { c.dragOverStatus = v; },
    setIsDragging: (v) => { c.isDragging = v; },
    setSwipeState: (v) => { c.swipeState = v; },
    setCardMenuTaskId: (v) => { c.cardMenuTaskId = v; },
    // methods-out
    toast: (msg, kind) => { c.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
    loadBoard: (f) => { c.loadBoardCalls.push(f); return Promise.resolve(); },
    refreshDetail: (f) => { c.refreshDetailCalls.push(f); return Promise.resolve(true); },
    applyDetail: (t) => { c.applyDetailCalls.push(t); c.detail = t; },
  });
  return c;
}

type PatchApi = "updateTask" | "getTask";
function patchApi(name: PatchApi, impl: unknown): { restore: () => void } {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  return { restore: () => { (api as Record<string, unknown>)[name] = orig; } };
}

function conflict(): ApiError {
  return new ApiError(409, { error: "version_conflict", message: "stale", current_version: 2 });
}

// Duck-typed DOM event fakes ----------------------------------------------------
interface DragFake {
  e: DragEvent;
  prevented: boolean;
  dt?: { effectAllowed?: string; dropEffect?: string; setDataCalls: Array<[string, string]>; getDataImpl?: () => string };
}

function dragEvent(over: { dataTransfer?: boolean; getData?: string; currentTarget?: unknown; relatedTarget?: unknown } = {}): DragFake {
  const f: DragFake = { e: undefined as never, prevented: false };
  if (over.dataTransfer) {
    f.dt = { setDataCalls: [] };
    f.e = {
      preventDefault: () => { f.prevented = true; },
      dataTransfer: {
        effectAllowed: "",
        dropEffect: "",
        setData: (t: string, v: string) => { f.dt!.setDataCalls.push([t, v]); },
        getData: (_t: string) => over.getData ?? "",
      },
    } as unknown as DragEvent;
  } else {
    f.e = {
      preventDefault: () => { f.prevented = true; },
      dataTransfer: null,
      ...(over.currentTarget !== undefined || over.relatedTarget !== undefined ? {
        currentTarget: over.currentTarget,
        relatedTarget: over.relatedTarget,
      } : {}),
    } as unknown as DragEvent;
  }
  return f;
}

function touchEvent(startX: number, startY: number, count = 1): TouchEvent {
  const mk = () => ({ clientX: startX, clientY: startY });
  return {
    touches: Array.from({ length: count }, mk),
    changedTouches: [],
    preventDefault: () => { throw new Error("unexpected preventDefault"); },
  } as unknown as TouchEvent;
}

const wait50 = (): Promise<void> => new Promise((r) => setTimeout(r, 60));

// ----------------------------------------------------------------------- dragTargetLegal
describe("dragTargetLegal", () => {
  test("no source in flight → every column stays legal (grey-out reads this)", () => {
    const c = makeCtx();
    assert.equal(c.mod.dragTargetLegal("done"), true);
    assert.equal(c.mod.dragTargetLegal("todo"), true);
  });

  test("follows the domain transition map for the current source", () => {
    const c = makeCtx();
    c.draggedFromStatus = "todo";
    assert.equal(c.mod.dragTargetLegal("doing"), true);
    assert.equal(c.mod.dragTargetLegal("done"), false);
    c.draggedFromStatus = "done";
    assert.equal(c.mod.dragTargetLegal("todo"), true);
    assert.equal(c.mod.dragTargetLegal("doing"), false);
  });
});

// ----------------------------------------------------------------------- onDragStart / onDragEnd
describe("onDragStart", () => {
  test("archived view is read-only: refuses before touching any state", () => {
    const c = makeCtx({ showArchived: true });
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragStart(f.e, dragTask("T1", "todo"), "todo");
    assert.equal(f.prevented, true);
    assert.equal(c.isDragging, false);
    assert.equal(c.draggedTaskId, null);
  });

  test("a task with its own move in flight is refused with a toast (per-task lock)", () => {
    const c = makeCtx();
    c.busyTaskIds.add("T1");
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragStart(f.e, dragTask("T1", "todo"), "todo");
    assert.equal(f.prevented, true);
    assert.deepEqual(c.toasts, [{ msg: "That task is still moving — one moment.", kind: "info" }]);
    assert.equal(c.draggedTaskId, null);
  });

  test("normal start sets the flat drag state and primes dataTransfer", () => {
    const c = makeCtx();
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragStart(f.e, dragTask("T1", "todo"), "todo");
    assert.equal(c.isDragging, true);
    assert.equal(c.draggedTaskId, "T1");
    assert.equal(c.draggedFromStatus, "todo");
    assert.equal((f.e.dataTransfer as { effectAllowed: string }).effectAllowed, "move");
    assert.deepEqual(f.dt!.setDataCalls, [["text/plain", "T1"]]);
  });

  test("no dataTransfer (defensive path) does not crash and still sets state", () => {
    const c = makeCtx();
    const f = dragEvent({});
    c.mod.onDragStart(f.e, dragTask("T2", "doing"), "doing");
    assert.equal(c.draggedTaskId, "T2");
    assert.equal(c.isDragging, true);
  });
});

describe("onDragEnd", () => {
  test("clears all drag fields and defers isDragging release by ~50ms", async () => {
    const c = makeCtx();
    c.draggedTaskId = "T1";
    c.draggedFromStatus = "todo";
    c.dragOverStatus = "doing";
    c.isDragging = true;
    c.mod.onDragEnd();
    assert.equal(c.draggedTaskId, null);
    assert.equal(c.draggedFromStatus, null);
    assert.equal(c.dragOverStatus, null);
    assert.equal(c.isDragging, true, "still set synchronously — released on the 50ms timer");
    await wait50();
    assert.equal(c.isDragging, false);
  });
});

// ----------------------------------------------------------------------- onDragOver / onDragLeave
describe("onDragOver", () => {
  test("without an in-flight drag: no preventDefault, no hover state", () => {
    const c = makeCtx();
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragOver(f.e, "doing");
    assert.equal(f.prevented, false);
    assert.equal(c.dragOverStatus, null);
  });

  test("illegal transition: does NOT preventDefault (browser shows no-drop, never fires @drop)", () => {
    const c = makeCtx();
    c.draggedTaskId = "T1";
    c.draggedFromStatus = "todo";
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragOver(f.e, "done"); // todo → done is not an allowed step
    assert.equal(f.prevented, false);
    assert.equal(c.dragOverStatus, null);
  });

  test("legal transition: preventDefault + dropEffect move + hover recorded", () => {
    const c = makeCtx();
    c.draggedTaskId = "T1";
    c.draggedFromStatus = "todo";
    const f = dragEvent({ dataTransfer: true });
    c.mod.onDragOver(f.e, "doing");
    assert.equal(f.prevented, true);
    assert.equal((f.e.dataTransfer as { dropEffect: string }).dropEffect, "move");
    assert.equal(c.dragOverStatus, "doing");
  });
});

describe("onDragLeave", () => {
  test("leaving into a child element is ignored (dragOverStatus kept)", () => {
    const c = makeCtx();
    c.dragOverStatus = "doing";
    const f = dragEvent({ currentTarget: { contains: () => true }, relatedTarget: {} });
    c.mod.onDragLeave(f.e, "doing");
    assert.equal(c.dragOverStatus, "doing");
  });

  test("genuine leave of the hovered column clears it", () => {
    const c = makeCtx();
    c.dragOverStatus = "doing";
    const f = dragEvent({ currentTarget: { contains: () => false }, relatedTarget: {} });
    c.mod.onDragLeave(f.e, "doing");
    assert.equal(c.dragOverStatus, null);
  });

  test("leaving a column that is not the hovered one leaves it untouched", () => {
    const c = makeCtx();
    c.dragOverStatus = "doing";
    const f = dragEvent({ currentTarget: { contains: () => false }, relatedTarget: {} });
    c.mod.onDragLeave(f.e, "todo");
    assert.equal(c.dragOverStatus, "doing");
  });
});

// ----------------------------------------------------------------------- onDrop
describe("onDrop", () => {
  test("drop onto own column is a no-op (state cleared, nothing sent)", async () => {
    const c = makeCtx({ board: boardWith({ doing: [dragTask("T1", "doing")] }) });
    // Real lifecycle: start the drag first so the deferred-release path runs.
    c.mod.onDragStart(dragEvent({ dataTransfer: true }).e, dragTask("T1", "doing"), "doing");
    assert.equal(c.isDragging, true);
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "doing"); });
    try {
      await c.mod.onDrop(dragEvent({}).e, "doing");
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, []);
      assert.equal(c.draggedTaskId, null);
      assert.equal(c.isDragging, true, "still set synchronously — released on the 50ms timer");
      await wait50();
      assert.equal(c.isDragging, false);
    } finally {
      p.restore();
    }
  });

  test("drop on an illegal target: friendly refusal, never sent to the server", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    c.draggedTaskId = "T1";
    c.draggedFromStatus = "todo";
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "done"); });
    try {
      await c.mod.onDrop(dragEvent({}).e, "done");
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, [{ msg: "Can't move To Do → Done — that's not an allowed step.", kind: "info" }]);
    } finally {
      p.restore();
    }
  });

  test("legal drop: optimistic move + PATCH with the card version + silent reload", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo", 2)], doing: [] }) });
    c.draggedTaskId = "T1";
    c.draggedFromStatus = "todo";
    let body: Record<string, unknown> | null = null;
    const p = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      body = b;
      return dragTask("T1", "doing", 3);
    });
    try {
      await c.mod.onDrop(dragEvent({}).e, "doing");
      assert.deepEqual(body!, { actor: ACTOR, version: 2, status: "doing" });
      // Optimistic board mutation settled by the (stubbed) reload.
      assert.equal(c.board!.tasks.todo.length, 0);
      assert.equal(c.board!.tasks.doing[0].status, "doing");
      assert.deepEqual(c.board!.counts, { todo: 0, doing: 1, done: 0 });
      // Server response merged back into the board row.
      assert.equal(c.board!.tasks.doing[0].version, 3);
      assert.deepEqual(c.loadBoardCalls, [true]);
      assert.equal(c.busyTaskIds.size, 0, "per-task lock released");
    } finally {
      p.restore();
    }
  });

  test("missing flat draggedTaskId falls back to the dataTransfer payload", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T9", "todo")] }) });
    c.draggedFromStatus = "todo"; // flat id was lost — dataTransfer carries it
    let seen: string | null = null;
    const p = patchApi("updateTask", async (id: string) => { seen = id; return dragTask(id, "doing"); });
    try {
      await c.mod.onDrop(dragEvent({ dataTransfer: true, getData: "T9" }).e, "doing");
      assert.equal(seen, "T9");
    } finally {
      p.restore();
    }
  });
});

// ----------------------------------------------------------------------- swipe state + handlers
describe("swiping / swipeDirection / swipeTargetLabel", () => {
  test("idle state: nothing is swiping; direction reads 'right'; labels are '' at both ends", () => {
    const c = makeCtx();
    assert.equal(c.mod.swiping("T1"), false);
    assert.equal(c.mod.swipeDirection("T1"), "right", "inactive gestures never display left (moves.ts parity)");
    c.swipeState = { ...idleSwipe(), id: "T1", fromStatus: "todo" };
    assert.equal(c.mod.swiping("T1"), false, "not active yet");
    assert.equal(c.mod.swiping("OTHER"), false);
    assert.equal(c.mod.swipeTargetLabel("T1"), "Doing", "todo → right");
  });

  test("active leftward gesture: direction flips, label follows the adjacent column", () => {
    const c = makeCtx();
    c.swipeState = { ...idleSwipe(), id: "T1", fromStatus: "doing", dx: -40, active: true };
    assert.equal(c.mod.swiping("T1"), true);
    assert.equal(c.mod.swipeDirection("T1"), "left");
    assert.equal(c.mod.swipeTargetLabel("T1"), "To Do");
  });

  test("no target past the last column → empty label", () => {
    const c = makeCtx();
    c.swipeState = { ...idleSwipe(), id: "T1", fromStatus: "done", dx: 40, active: true };
    assert.equal(c.mod.swipeDirection("T1"), "right");
    assert.equal(c.mod.swipeTargetLabel("T1"), "");
  });
});

describe("onCardTouchStart / onCardTouchMove", () => {
  test("archived view ignores the gesture entirely", () => {
    const c = makeCtx({ showArchived: true });
    c.mod.onCardTouchStart(touchEvent(10, 5), dragTask("T1", "todo"));
    assert.equal(c.swipeState.id, null);
  });

  test("multi-touch is not a swipe candidate", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(10, 5, 2), dragTask("T1", "todo"));
    assert.equal(c.swipeState.id, null);
  });

  test("single touch arms the gesture (inactive until movement clears the slop)", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    assert.equal(c.swipeState.id, "T1");
    assert.equal(c.swipeState.startX, 100);
    assert.equal(c.swipeState.fromStatus, "todo");
    assert.equal(c.swipeState.active, false);
  });

  test("small vertical movement stays inactive and never preventsDefault", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    let prevented = 0;
    const e = { touches: [{ clientX: 102, clientY: 60 }], preventDefault: () => { prevented++; } } as unknown as TouchEvent;
    c.mod.onCardTouchMove(e);
    assert.equal(c.swipeState.active, false, "|dx|=2 < slop 24");
    assert.equal(prevented, 0);
  });

  test("clearly-horizontal movement activates the gesture and clamps the DISPLAYED dx to 96px", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    let prevented = 0;
    const e = { touches: [{ clientX: 500, clientY: 42 }], preventDefault: () => { prevented++; } } as unknown as TouchEvent;
    c.mod.onCardTouchMove(e);
    assert.equal(c.swipeState.active, true);
    assert.equal(c.swipeState.dx, 96, "long fling clamped to MAX_SWIPE_PX for display");
    assert.equal(prevented, 1, "active gesture owns the scroll + suppresses compat click");
  });

  test("no armed gesture → move handler is a no-op", () => {
    const c = makeCtx();
    let prevented = 0;
    const e = { touches: [{ clientX: 900, clientY: 0 }], preventDefault: () => { prevented++; } } as unknown as TouchEvent;
    c.mod.onCardTouchMove(e);
    assert.equal(prevented, 0);
    assert.deepEqual(c.swipeState, idleSwipe());
  });
});

describe("onCardTouchEnd", () => {
  test("right-swipe on a To Do card commits the one-column move through the engine", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    c.mod.onCardTouchMove({ touches: [{ clientX: 200, clientY: 41 }], preventDefault: () => {} } as unknown as TouchEvent);
    let body: Record<string, unknown> | null = null;
    const p = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      body = b;
      return dragTask("T1", "doing");
    });
    try {
      c.mod.onCardTouchEnd({ changedTouches: [{ clientX: 200 }] } as unknown as TouchEvent, dragTask("T1", "todo"));
      // The engine's PATCH stub runs during the synchronous prefix of the
      // fire-and-forget move (withTimeout invokes its callback eagerly), so
      // `body` is already set by the time onCardTouchEnd returns.
      assert.deepEqual(body!, { actor: ACTOR, version: 1, status: "doing" });
      assert.equal(c.swipeState.id, null, "gesture reset on release");
      await wait50();
    } finally {
      p.restore();
    }
  });

  test("left-swipe on a Done card is refused with feedback (the one illegal adjacent pair)", () => {
    const c = makeCtx({ board: boardWith({ done: [dragTask("T1", "done")] }) });
    c.mod.onCardTouchStart(touchEvent(300, 40), dragTask("T1", "done"));
    // Activate the gesture with a clearly-leftward pull.
    c.mod.onCardTouchMove({ touches: [{ clientX: 200, clientY: 41 }], preventDefault: () => {} } as unknown as TouchEvent);
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "doing"); });
    try {
      c.mod.onCardTouchEnd({ changedTouches: [{ clientX: 200 }] } as unknown as TouchEvent, dragTask("T1", "done"));
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, [{ msg: "Can't move Done → Doing — that's not an allowed step.", kind: "info" }]);
    } finally {
      p.restore();
    }
  });

  test("release under the commit threshold snaps back home (no move)", () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    // Activate the gesture (dx=30 clears the 24px slop) but stay under the
    // 48px commit threshold on release.
    c.mod.onCardTouchMove({ touches: [{ clientX: 130, clientY: 40 }], preventDefault: () => {} } as unknown as TouchEvent);
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "doing"); });
    try {
      // dx = 30 < SWIPE_COMMIT_PX (48)
      c.mod.onCardTouchEnd({ changedTouches: [{ clientX: 130 }] } as unknown as TouchEvent, dragTask("T1", "todo"));
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, []);
      assert.equal(c.swipeState.id, null, "still reset — the card animates back via idle state");
    } finally {
      p.restore();
    }
  });

  test("release on a DIFFERENT card id is ignored (state kept for the armed card)", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    c.mod.onCardTouchEnd({ changedTouches: [{ clientX: 300 }] } as unknown as TouchEvent, dragTask("OTHER", "doing"));
    assert.equal(c.swipeState.id, "T1", "the armed gesture survives a stray release");
    assert.equal(c.swipeState.active, false);
  });
});

describe("onCardTouchCancel", () => {
  test("a cancelled gesture resets the state without committing anything", () => {
    const c = makeCtx();
    c.mod.onCardTouchStart(touchEvent(100, 40), dragTask("T1", "todo"));
    c.swipeState.active = true;
    c.swipeState.dx = 96;
    c.mod.onCardTouchCancel({} as TouchEvent);
    assert.deepEqual(c.swipeState, idleSwipe());
  });
});

// ----------------------------------------------------------------------- card menu
describe("toggleCardMenu / moveCardTo", () => {
  test("toggle opens and closes the same card's menu only", () => {
    const c = makeCtx();
    c.mod.toggleCardMenu("T1");
    assert.equal(c.cardMenuTaskId, "T1");
    c.mod.toggleCardMenu("T2");
    assert.equal(c.cardMenuTaskId, "T2");
    c.mod.toggleCardMenu("T2");
    assert.equal(c.cardMenuTaskId, null);
  });

  test("moveCardTo closes the menu and funnels into the shared engine", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo", 4)] }) });
    c.cardMenuTaskId = "T1";
    let body: Record<string, unknown> | null = null;
    const p = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      body = b;
      return dragTask("T1", "doing", 5);
    });
    try {
      await c.mod.moveCardTo("T1", "doing");
      assert.equal(c.cardMenuTaskId, null);
      assert.deepEqual(body!, { actor: ACTOR, version: 4, status: "doing" });
    } finally {
      p.restore();
    }
  });
});

// ----------------------------------------------------------------------- moveTaskStatus (shared engine)
describe("moveTaskStatus — guards", () => {
  test("unknown card (not on board, no open detail): no lock, no patch, no toast", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("GHOST", "doing"); });
    try {
      await c.mod.moveTaskStatus("GHOST", "doing");
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, []);
      assert.equal(c.busyTaskIds.size, 0);
    } finally {
      p.restore();
    }
  });

  test("drop onto own column: early no-op before the lock", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "todo"); });
    try {
      await c.mod.moveTaskStatus("T1", "todo");
      assert.equal(calls, 0);
      assert.equal(c.busyTaskIds.size, 0);
    } finally {
      p.restore();
    }
  });

  test("duplicate concurrent move of the same id: toast, never a silent drop", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    c.busyTaskIds.add("T1"); // another engine instance already owns this card
    let calls = 0;
    const p = patchApi("updateTask", async () => { calls++; return dragTask("T1", "doing"); });
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(calls, 0);
      assert.deepEqual(c.toasts, [{ msg: "That task is still moving — one moment.", kind: "info" }]);
    } finally {
      p.restore();
    }
  });

  test("initialVersion (from the detail hook) takes precedence over the card's version", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo", 2)] }) });
    let body: Record<string, unknown> | null = null;
    const p = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      body = b;
      return dragTask("T1", "doing");
    });
    try {
      await c.mod.moveTaskStatus("T1", "doing", 7);
      assert.deepEqual(body!, { actor: ACTOR, version: 7, status: "doing" });
    } finally {
      p.restore();
    }
  });
});

describe("moveTaskStatus — failure paths", () => {
  test("plain API error rolls back the optimistic board + detail move and toasts", async () => {
    const t = dragTask("T1", "todo");
    const c = makeCtx({
      board: boardWith({ todo: [t] }),
      detail: detailFor("T1", "todo"),
    });
    const p = patchApi("updateTask", async () => { throw new ApiError(400, { error: "invalid_input", message: "boom" }); });
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      // Rollback restored the pre-move shape.
      assert.equal(c.board!.tasks.todo.length, 1);
      assert.equal(c.board!.tasks.doing.length, 0);
      assert.deepEqual(c.board!.counts, { todo: 1, doing: 0, done: 0 });
      assert.equal(c.detail!.status, "todo", "detail status rolled back");
      assert.deepEqual(c.loadBoardCalls, [true]);
      assert.deepEqual(c.toasts, [{ msg: errMsg(new ApiError(400, { error: "invalid_input", message: "boom" })), kind: "error" }]);
    } finally {
      p.restore();
    }
  });

  test("known server transition rejection surfaces with friendly wording (not raw text)", async () => {
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    const serverErr = new ApiError(400, { error: "invalid_input", message: 'cannot transition status from "todo" to "doing"' });
    const p = patchApi("updateTask", async () => { throw serverErr; });
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(c.toasts.length, 1);
      assert.equal(c.toasts[0].kind, "error");
      assert.ok(!c.toasts[0].msg.includes('cannot transition status'), "raw shape must not leak: " + c.toasts[0].msg);
    } finally {
      p.restore();
    }
  });

  test("409: re-reads the fresh row, retries ONCE with the fresh version, and reports it", async () => {
    // Someone else moved the card somewhere ELSE entirely (done), so the retry
    // is required — a different case from the "already applied" branch below.
    const fresh = dragTask("T1", "done", 5);
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    let updates: Array<Record<string, unknown>> = [];
    const pU = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      updates.push(b);
      if (updates.length === 1) throw conflict();
      return dragTask("T1", "doing", 6);
    });
    const pG = patchApi("getTask", async () => fresh);
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(updates.length, 2, "retry fired exactly once");
      assert.deepEqual(updates[0].version, 1);
      assert.deepEqual(updates[1].version, 5, "retry used the fresh version");
      assert.deepEqual(c.toasts, [{ msg: "Changed elsewhere — retried with the fresh version.", kind: "info" }]);
      assert.deepEqual(c.loadBoardCalls, [true]);
    } finally {
      pG.restore();
      pU.restore();
    }
  });

  test("409 where the server already applied the same status: no second PATCH, still syncs + toasts", async () => {
    const fresh = dragTask("T1", "doing", 5); // now equals the target
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    let updates = 0;
    const pU = patchApi("updateTask", async () => {
      updates++;
      if (updates === 1) throw conflict();
      return fresh;
    });
    const pG = patchApi("getTask", async () => fresh);
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(updates, 1, "fresh.status === target → retry skipped");
      assert.deepEqual(c.toasts, [{ msg: "Changed elsewhere — retried with the fresh version.", kind: "info" }]);
    } finally {
      pG.restore();
      pU.restore();
    }
  });

  test("409 where the retry conflicts AGAIN: rollback + honest 'not applied' message", async () => {
    const fresh = dragTask("T1", "done", 5); // different status forces a retry, which 409s again
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }) });
    let updates = 0;
    const pU = patchApi("updateTask", async () => {
      updates++;
      throw conflict();
    });
    const pG = patchApi("getTask", async () => fresh);
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(updates, 2);
      assert.equal(c.board!.tasks.todo.length, 1, "rolled back");
      assert.equal(c.board!.tasks.doing.length, 0);
      assert.deepEqual(c.toasts, [{ msg: "Changed elsewhere — refreshed (your move was not applied).", kind: "info" }]);
    } finally {
      pG.restore();
      pU.restore();
    }
  });

  test("409 with the task's detail open: fresh row applied via applyDetail; no retry when status already matches", async () => {
    const fresh = dragTask("T1", "doing", 5); // now equals the target
    const c = makeCtx({ board: boardWith({ todo: [dragTask("T1", "todo")] }), detail: detailFor("T1", "todo") });
    let updates = 0;
    const pU = patchApi("updateTask", async () => {
      updates++;
      throw conflict(); // never reached twice — fresh.status === target skips the retry
    });
    const pG = patchApi("getTask", async () => fresh as unknown as TaskDetail);
    try {
      await c.mod.moveTaskStatus("T1", "doing");
      assert.equal(updates, 1, "fresh.status === target → retry PATCH skipped");
      assert.equal(c.applyDetailCalls.length, 1);
      assert.equal(c.detail!.version, 5, "root detail replaced with the fresh payload");
      assert.deepEqual(c.refreshDetailCalls, [true]);
      assert.deepEqual(c.toasts, [{ msg: "Changed elsewhere — retried with the fresh version.", kind: "info" }]);
    } finally {
      pG.restore();
      pU.restore();
    }
  });

  test("detail-only move (card not on the board): engine works off the open detail", async () => {
    const c = makeCtx({ board: boardWith(), detail: detailFor("T9", "todo", { version: 3 }) });
    let body: Record<string, unknown> | null = null;
    const p = patchApi("updateTask", async (_id: string, b: Record<string, unknown>) => {
      body = b;
      return { ...detailFor("T9", "doing"), version: 4 } as TaskSummary;
    });
    try {
      await c.mod.moveTaskStatus("T9", "doing");
      assert.deepEqual(body!, { actor: ACTOR, version: 3, status: "doing" });
      assert.equal(c.detail!.status, "doing", "optimistic detail update kept");
      assert.deepEqual(c.refreshDetailCalls, [true]);
      assert.deepEqual(c.loadBoardCalls, [true]);
    } finally {
      p.restore();
    }
  });
});
