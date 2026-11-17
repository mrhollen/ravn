// Unit tests for src/modules/archive.ts — the board-level ARCHIVE management
// extracted from web/src/app.ts (Phase 2 batch D). Covers: the archived-view
// toggle (AC3) with its paged fetch + supersede discipline, single-task
// archive/unarchive (AC1) with optimistic removal, rollback, Undo and the 409
// retry-once discipline, the bulk "Archive all Done" flow (AC2), and the
 // project-switch reset seam.
//
// The module is pure: a mutable stub state object wired into the ArchiveDeps
// accessors + counters for every out-bound write + monkey-patched api methods,
// all restored in finally blocks (same pattern as claims-module.test.ts).

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createArchiveModule } from "../src/modules/archive";
import { api, ApiError, errMsg } from "../src/api";
import type { ActorInput, Board, Status, TaskDetail, TaskSummary } from "../src/types";

// ----------------------------------------------------------------------- fixtures
const ACTOR: ActorInput = { name: "tester", type: "agent" };

function archTask(id: string, status: Status, version = 1, over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id, title: "T-" + id, description: "", status, priority: 0, due_at: null,
    created_by: { name: "u", type: "human" }, version,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z",
    claimed_by: null, claim_expires_at: null, ac_total: 0, comment_count: 0,
    archived: false, ...over,
  };
}

function boardWith(projectId: string, byStatus: Partial<Record<Status, TaskSummary[]>> = {}): Board {
  const tasks = { todo: byStatus.todo ?? [], doing: byStatus.doing ?? [], done: byStatus.done ?? [] };
  return {
    project_id: projectId,
    tasks,
    counts: { todo: tasks.todo.length, doing: tasks.doing.length, done: tasks.done.length },
  };
}

function detailFor(id: string, over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id, project_id: "P1", seq: 1, title: "detail", description: "", status: "todo",
    priority: 0, due_at: null, created_by: { name: "u", type: "human" }, version: 3,
    created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-02T00:00:00Z",
    completed_by: null, completed_at: null, archived: false, ...over,
  };
}

interface ToastRec { msg: string; kind?: string; action?: { label: string; onAction?: () => void } }

interface Ctx {
  mod: ReturnType<typeof createArchiveModule>;
  showArchived: boolean;
  archivedBoardTasks: Record<Status, TaskSummary[]> | null;
  selectedProjectId: string;
  board: Board | null;
  detail: TaskDetail | null;
  archiveAllDoneConfirmOpen: boolean;
  archiveAllDoneBusy: boolean;
  busyTaskIds: Set<string>;
  toasts: ToastRec[];
  refreshDetailCalls: Array<boolean | undefined>;
  loadBoardCalls: Array<boolean | undefined>;
  rootRefreshCalls: number; // root-routed archived-view refreshes (the deps seam)
  clearSelectionCalls: number;
}

function makeCtx(over: {
  board?: Board | null; detail?: TaskDetail | null; selectedProjectId?: string;
  showArchived?: boolean; archivedBoardTasks?: Record<Status, TaskSummary[]> | null;
} = {}): Ctx {
  const c: Ctx = {
    mod: undefined as never,
    showArchived: over.showArchived ?? false,
    archivedBoardTasks: over.archivedBoardTasks ?? null,
    selectedProjectId: over.selectedProjectId ?? "P1",
    board: over.board ?? boardWith("P1"),
    detail: over.detail ?? null,
    archiveAllDoneConfirmOpen: false,
    archiveAllDoneBusy: false,
    busyTaskIds: new Set<string>(),
    toasts: [],
    refreshDetailCalls: [],
    loadBoardCalls: [],
    rootRefreshCalls: 0,
    clearSelectionCalls: 0,
  };
  c.mod = createArchiveModule({
    // state-in
    board: () => c.board,
    showArchived: () => c.showArchived,
    archivedBoardTasks: () => c.archivedBoardTasks,
    selectedProjectId: () => c.selectedProjectId,
    detail: () => c.detail,
    archiveAllDoneConfirmOpen: () => c.archiveAllDoneConfirmOpen,
    archiveAllDoneBusy: () => c.archiveAllDoneBusy,
    busyTaskIds: () => c.busyTaskIds,
    // state-out
    setShowArchived: (v) => { c.showArchived = v; },
    setArchivedBoardTasks: (v) => { c.archivedBoardTasks = v; },
    setArchiveAllDoneConfirmOpen: (v) => { c.archiveAllDoneConfirmOpen = v; },
    setArchiveAllDoneBusy: (v) => { c.archiveAllDoneBusy = v; },
    // methods-out
    toast: (msg, kind, action) => { c.toasts.push({ msg, kind, action }); return 0; },
    actor: () => ACTOR,
    clearSelection: () => { c.clearSelectionCalls++; },
    refreshDetail: (f) => { c.refreshDetailCalls.push(f); return Promise.resolve(true); },
    applyDetail: (t) => { c.detail = t; },
    loadBoard: (f) => { c.loadBoardCalls.push(f); return Promise.resolve(); },
    // the root shim delegates back to the module; here we count instead,
    // mirroring how app.test.ts stubs the flat namespace.
    refreshArchivedView: () => { c.rootRefreshCalls++; return Promise.resolve(); },
  });
  return c;
}

type PatchApi = "listTasks" | "updateTask" | "getTask";
function patchApi(name: PatchApi, impl: unknown): { restore: () => void } {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  return { restore: () => { (api as Record<string, unknown>)[name] = orig; } };
}

function conflict(): ApiError {
  return new ApiError(409, { error: "version_conflict", message: "stale", current_version: 2 });
}

// ----------------------------------------------------------------------- archivedColumns
describe("archivedColumns (columns-source selector)", () => {
  test("null while the mode is OFF, regardless of cached data", () => {
    const c = makeCtx({ showArchived: true, archivedBoardTasks: boardWith("P1").tasks });
    c.showArchived = false;
    assert.equal(c.mod.archivedColumns(), null);
  });

  test("the cached groups while ON (null until the first fetch lands)", () => {
    const c = makeCtx({ showArchived: true, archivedBoardTasks: null });
    assert.equal(c.mod.archivedColumns(), null);
    const groups = { todo: [archTask("a", "todo")], doing: [], done: [] };
    c.archivedBoardTasks = groups;
    assert.equal(c.mod.archivedColumns(), groups);
  });
});

// ----------------------------------------------------------------------- toggleShowArchived (AC3)
describe("toggleShowArchived (AC3)", () => {
  test("OFF is a pure flag flip — no fetch, cached data kept for instant re-enable", async () => {
    const c = makeCtx({ showArchived: true });
    const restored = boardWith("P1").tasks;
    c.archivedBoardTasks = restored;
    let listed = 0;
    const p = patchApi("listTasks", async () => { listed++; return { tasks: [], total: 0 }; });
    try {
      await c.mod.toggleShowArchived();
    } finally {
      p.restore();
    }
    assert.equal(c.showArchived, false);
    assert.equal(listed, 0, "no network on OFF");
    assert.equal(c.archivedBoardTasks, restored, "cached groups survive the flip");
  });

  test("ON with no selected project is a no-op", async () => {
    const c = makeCtx({ selectedProjectId: "" });
    let listed = 0;
    const p = patchApi("listTasks", async () => { listed++; return { tasks: [], total: 0 }; });
    try {
      await c.mod.toggleShowArchived();
    } finally {
      p.restore();
    }
    assert.equal(c.showArchived, false);
    assert.equal(listed, 0);
  });

  test("ON pages the full archived set at 100/page, groups by status, clears selection", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("live", "todo")] }) });
    const seenParams: Array<Record<string, unknown>> = [];
    // 150 rows across two pages — verifies the pagination loop.
    const page1 = Array.from({ length: 100 }, (_, i) => archTask(`p1-${i}`, i % 2 === 0 ? "todo" : "done"));
    const page2 = Array.from({ length: 50 }, (_, i) => archTask(`p2-${i}`, i % 3 === 0 ? "doing" : "todo"));
    const p = patchApi("listTasks", async (params: any) => {
      seenParams.push(params);
      return params.offset === 0
        ? { tasks: page1, total: 150 }
        : { tasks: page2, total: 150 };
    });
    try {
      await c.mod.toggleShowArchived();
    } finally {
      p.restore();
    }
    assert.equal(seenParams.length, 2, "two pages fetched");
    assert.deepEqual(seenParams[0], { project_id: "P1", archived: true, limit: 100, offset: 0 });
    assert.deepEqual(seenParams[1], { project_id: "P1", archived: true, limit: 100, offset: 100 });
    assert.equal(c.showArchived, true);
    assert.ok(c.archivedBoardTasks);
    // Exact expected grouping: page1 has 50 todo/50 done; page2 (i%3===0 → doing)
    // has 17 doing (i=0..48 step 3) and 33 todo.
    assert.equal(c.archivedBoardTasks.todo.length, 50 + 33);
    assert.equal(c.archivedBoardTasks.doing.length, 17);
    assert.equal(c.archivedBoardTasks.done.length, 50);
    assert.equal(c.clearSelectionCalls, 1, "selection never carries into the archived view");
    assert.equal(c.board?.tasks.todo.length, 1, "the live board was never touched");
  });

  test("supersede: a newer toggle in flight voids the older fetch (only the newest lands)", async () => {
    const c = makeCtx();
    let releaseFirst: (v?: unknown) => void;
    const firstGate = new Promise((res) => { releaseFirst = res; });
    let calls = 0;
    const p = patchApi("listTasks", async () => {
      calls++;
      if (calls === 1) await firstGate; // the FIRST call's page is slow…
      return { tasks: [archTask(`row-${calls}`, "todo")], total: 1 }; // …each call gets its OWN row
    });
    try {
      const r1 = c.mod.toggleShowArchived();
      const r2 = c.mod.toggleShowArchived(); // immediately supersede (call #2)
      releaseFirst!();
      await Promise.all([r1, r2]);
    } finally {
      p.restore();
    }
    assert.equal(c.showArchived, true, "the NEWER fetch flipped the mode");
    assert.ok(c.archivedBoardTasks?.todo.some((t) => t.id === "row-2"), "newest call's rows applied");
    assert.ok(!c.archivedBoardTasks?.todo.some((t) => t.id === "row-1"), "superseded call's rows dropped");
  });

  test("project switch mid-flight: the late response is dropped (rows of the wrong project)", async () => {
    const c = makeCtx();
    let release: (v?: unknown) => void;
    const gate = new Promise((res) => { release = res; });
    const p = patchApi("listTasks", async () => { await gate; return { tasks: [archTask("late", "todo")], total: 1 }; });
    try {
      const r = c.mod.toggleShowArchived();
      c.selectedProjectId = "P2"; // user switched projects while the fetch ran
      release!();
      await r;
    } finally {
      p.restore();
    }
    assert.equal(c.showArchived, false, "not flipped — the rows describe the previous project");
    assert.equal(c.archivedBoardTasks, null);
  });

  test("failure: error toast, flag stays OFF, live board intact", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("live", "todo")] }) });
    const p = patchApi("listTasks", async () => { throw new Error("network down"); });
    try {
      await c.mod.toggleShowArchived();
    } finally {
      p.restore();
    }
    assert.equal(c.showArchived, false);
    assert.equal(c.archivedBoardTasks, null);
    assert.match(c.toasts.at(-1)!.msg, /network down/);
    assert.equal(c.toasts.at(-1)!.kind, "error");
    assert.equal(c.board?.tasks.todo.length, 1);
  });
});

// ----------------------------------------------------------------------- refreshArchivedView
describe("refreshArchivedView", () => {
  test("no-op while OFF or without a project (silent)", async () => {
    const c = makeCtx();
    let listed = 0;
    const p = patchApi("listTasks", async () => { listed++; return { tasks: [], total: 0 }; });
    try {
      await c.mod.refreshArchivedView(); // OFF
      c.selectedProjectId = "";
      c.showArchived = true;
      await c.mod.refreshArchivedView(); // no project
    } finally {
      p.restore();
    }
    assert.equal(listed, 0);
    assert.equal(c.toasts.length, 0);
  });

  test("re-fetches and replaces the groups while ON; failures are silent", async () => {
    const c = makeCtx({ showArchived: true, archivedBoardTasks: { todo: [archTask("old", "todo")], doing: [], done: [] } });
    let failNext = false;
    const p = patchApi("listTasks", async () => {
      if (failNext) throw new Error("blip");
      return { tasks: [archTask("new", "done")], total: 1 };
    });
    try {
      await c.mod.refreshArchivedView();
      assert.equal(c.archivedBoardTasks?.done.length, 1);
      assert.match(c.archivedBoardTasks?.done[0].id, /new/);
      failNext = true;
      await c.mod.refreshArchivedView();
    } finally {
      p.restore();
    }
    assert.equal(c.toasts.length, 0, "a refresh failure keeps the last known view quietly");
    assert.equal(c.archivedBoardTasks?.done.length, 1, "stale-but-known view kept");
  });
});

// ----------------------------------------------------------------------- setTaskArchived (AC1)
describe("setTaskArchived (AC1)", () => {
  test("no row anywhere and no open detail: complete no-op", async () => {
    const c = makeCtx({ board: boardWith("P1") });
    let patched = 0;
    const p = patchApi("updateTask", async () => { patched++; return {}; });
    try {
      await c.mod.setTaskArchived("ghost", true);
    } finally {
      p.restore();
    }
    assert.equal(patched, 0);
    assert.equal(c.busyTaskIds.size, 0, "the lock was never taken");
    assert.equal(c.toasts.length, 0);
  });

  test("busy guard: the same id already moving is vetoed (shared move-flow lock)", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    c.busyTaskIds.add("t1");
    let patched = 0;
    const p = patchApi("updateTask", async () => { patched++; return {}; });
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore();
    }
    assert.equal(patched, 0);
    assert.match(c.toasts.at(-1)!.msg, /still busy/);
    assert.equal(c.toasts.at(-1)!.kind, "info");
  });

  test("archive: optimistic removal, version-guarded PATCH, resync, Undo toast", async () => {
    const c = makeCtx({
      board: boardWith("P1", { todo: [archTask("t1", "todo"), archTask("t2", "todo")] }),
    });
    let reqSeen: any = null;
    const p = patchApi("updateTask", async (_id: string, req: any) => { reqSeen = req; return { id: "t1", version: 5 }; });
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore();
    }
    // Optimistic removal applied (card gone, count decremented)…
    assert.equal(c.board!.tasks.todo.length, 1);
    assert.equal(c.board!.counts.todo, 1);
    // …with the actor + version guard…
    assert.deepEqual(reqSeen, { actor: ACTOR, version: 1, archived: true });
    // …resync ran (loadBoard forced; no detail open → no refreshDetail)…
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.equal(c.refreshDetailCalls.length, 0);
    assert.equal(c.rootRefreshCalls, 0, "archived view OFF → its refresh skipped");
    // …and the Undo toast carries the fresh version from THIS patch.
    const t = c.toasts.at(-1)!;
    assert.equal(t.msg, "Task archived");
    assert.equal(t.action?.label, "Undo");
    assert.equal(c.busyTaskIds.size, 0, "lock released in finally");

    // Fire the Undo action: re-apply archived:false with version 5, resync again.
    let undoReq: any = null;
    const p2 = patchApi("updateTask", async (_id: string, req: any) => { undoReq = req; return { id: "t1", version: 6 }; });
    try {
      await t.action!.onAction!();
    } finally {
      p2.restore();
    }
    assert.deepEqual(undoReq, { actor: ACTOR, version: 5, archived: false });
    assert.equal(c.loadBoardCalls.length, 2);
  });

  test("archive with the same task open in detail: refreshDetail + in-place detail.archived flip", async () => {
    const c = makeCtx({
      board: boardWith("P1", { doing: [archTask("t1", "doing")] }),
      detail: detailFor("t1", { status: "doing" }),
      showArchived: true,
    });
    const p = patchApi("updateTask", async () => ({ id: "t1", version: 4 }));
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore();
    }
    assert.equal(c.refreshDetailCalls.length, 1);
    assert.equal(c.refreshDetailCalls[0], true);
    assert.equal(c.detail!.archived, true, "the open detail row flips in place");
    assert.equal(c.rootRefreshCalls, 1, "archived view ON → routed refresh fired");
  });

  test("unarchive: removes from the ARCHIVED set (card returns on reload), distinct toast", async () => {
    const c = makeCtx({
      board: boardWith("P1"), // live board empty — the card only exists in the archived view
      showArchived: true,
      archivedBoardTasks: { todo: [archTask("t-a", "todo")], doing: [], done: [] },
    });
    let reqSeen: any = null;
    const p = patchApi("updateTask", async (_id: string, req: any) => { reqSeen = req; return { id: "t-a", version: 3 }; });
    try {
      await c.mod.setTaskArchived("t-a", false);
    } finally {
      p.restore();
    }
    assert.deepEqual(reqSeen, { actor: ACTOR, version: 1, archived: false });
    assert.equal(c.archivedBoardTasks!.todo.length, 0, "optimistically gone from the archived column");
    assert.equal(c.loadBoardCalls.length, 1);
    assert.equal(c.rootRefreshCalls, 1);
    assert.match(c.toasts.at(-1)!.msg, /restored from archive/);
    assert.equal(c.toasts.at(-1)!.action, undefined, "no Undo on restore");
  });

  test("non-409 failure: rollback restores the optimistic removal and toasts the error", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    const p = patchApi("updateTask", async () => { throw new ApiError(500, { error: "boom", message: "upstream exploded" }); });
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore();
    }
    assert.equal(c.board!.tasks.todo.length, 1, "card restored to its column");
    assert.equal(c.board!.counts.todo, 1, "count rolled back");
    assert.deepEqual(c.loadBoardCalls, [true], "reload settles reality (swallowed)");
    assert.match(c.toasts.at(-1)!.msg, /upstream exploded/);
    assert.equal(c.toasts.at(-1)!.kind, "error");
    assert.equal(c.busyTaskIds.size, 0);
  });

  test("409: re-read the fresh row and retry ONCE with its version (fresh value differs)", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    let updateCalls: Array<{ id: string; req: any }> = [];
    let getTaskCalls = 0;
    const p = patchApi("updateTask", async (id: string, req: any) => {
      updateCalls.push({ id, req });
      if (updateCalls.length === 1) throw conflict();
      return { id, version: 9 };
    });
    const g = patchApi("getTask", async () => { getTaskCalls++; return detailFor("t1", { version: 2, archived: false }); });
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(getTaskCalls, 1, "re-read once");
    assert.equal(updateCalls.length, 2, "the retry fired — fresh.archived (false) !== target (true)");
    assert.deepEqual(updateCalls[1].req, { actor: ACTOR, version: 2, archived: true }, "retry used the FRESH version");
    assert.match(c.toasts.at(-1)!.msg, /retried with the fresh version/);
    assert.equal(c.toasts.at(-1)!.kind, "info");
    // Optimistic removal is NOT rolled back after a successful retry.
    assert.equal(c.board!.tasks.todo.length, 0);
    assert.deepEqual(c.loadBoardCalls, [true]);
  });

  test("409 with the fresh row already at the target value: no second PATCH (idempotent)", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    let updateCalls = 0;
    const p = patchApi("updateTask", async () => {
      updateCalls++;
      if (updateCalls === 1) throw conflict();
      return {};
    });
    const g = patchApi("getTask", async () => detailFor("t1", { version: 2, archived: true })); // already archived elsewhere
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(updateCalls, 1, "no second patch — the row is already archived");
    assert.match(c.toasts.at(-1)!.msg, /retried with the fresh version/);
    assert.deepEqual(c.loadBoardCalls, [true]);
  });

  test("409 where the RETRY fails again (conflict): rollback + refreshed-info toast", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    let updateCalls = 0;
    const p = patchApi("updateTask", async () => {
      updateCalls++;
      throw conflict(); // both the first patch and the retry conflict
    });
    const g = patchApi("getTask", async () => detailFor("t1", { version: 2, archived: false }));
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(updateCalls, 2, "exactly one retry — then give up");
    assert.equal(c.board!.tasks.todo.length, 1, "rolled back to the optimistic pre-state");
    assert.equal(c.board!.counts.todo, 1);
    assert.match(c.toasts.at(-1)!.msg, /refreshed \(your change was not applied\)/);
    assert.equal(c.toasts.at(-1)!.kind, "info");
  });

  test("409 where the RETRY fails with a plain error: rollback + error toast", async () => {
    const c = makeCtx({ board: boardWith("P1", { todo: [archTask("t1", "todo")] }) });
    let updateCalls = 0;
    const p = patchApi("updateTask", async () => {
      updateCalls++;
      if (updateCalls === 1) throw conflict();
      throw new Error("retried and it exploded");
    });
    const g = patchApi("getTask", async () => detailFor("t1", { version: 2, archived: false }));
    try {
      await c.mod.setTaskArchived("t1", true);
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(updateCalls, 2);
    assert.equal(c.board!.tasks.todo.length, 1);
    assert.match(c.toasts.at(-1)!.msg, /retried and it exploded/);
    assert.equal(c.toasts.at(-1)!.kind, "error");
  });

  test("version source: the board row's version wins; an open detail (no row) supplies its own", async () => {
    const c = makeCtx({ board: boardWith("P1") }); // card NOT on the live board…
    const d = detailFor("t9", { version: 7 });
    c.detail = d;
    let reqSeen: any = null;
    const p = patchApi("updateTask", async (_id: string, req: any) => { reqSeen = req; return { id: "t9", version: 8 }; });
    try {
      await c.mod.setTaskArchived("t9", true);
    } finally {
      p.restore();
    }
    assert.equal(reqSeen.version, 7, "detail payload's version used when no card row exists");
  });

  test("Undo failure: loadBoard runs (swallowed) and the error rethrows for runToastAction", async () => {
    const c = makeCtx({ board: boardWith("P1") });
    const p = patchApi("updateTask", async () => { throw new Error("undo exploded"); });
    try {
      await assert.rejects(c.mod._undoArchive("t1", 5), /undo exploded/);
    } finally {
      p.restore();
    }
    assert.deepEqual(c.loadBoardCalls, [true], "board reloaded to reality on the failure path too");
  });
});

// ----------------------------------------------------------------------- bulk "Archive all Done" (AC2)
describe("confirmArchiveAllDone (AC2)", () => {
  test("busy guard and missing board: early return, nothing touched", async () => {
    const c1 = makeCtx();
    c1.archiveAllDoneBusy = true;
    let patched = 0;
    const p = patchApi("updateTask", async () => { patched++; return {}; });
    try {
      await c1.mod.confirmArchiveAllDone();
      c1.archiveAllDoneBusy = false;
      c1.board = null;
      await c1.mod.confirmArchiveAllDone();
    } finally {
      p.restore();
    }
    assert.equal(patched, 0);

    const c2 = makeCtx({ board: boardWith("P1", { done: [archTask("d1", "done")] }) });
    c2.archiveAllDoneConfirmOpen = true;
    await c2.mod.confirmArchiveAllDone(); // empty done list
    assert.equal(c2.archiveAllDoneConfirmOpen, false, "confirm step closes even when there is nothing to do");
  });

  test("archives EVERY done row (version-guarded), optimistic per success, summary toast", async () => {
    const c = makeCtx({
      board: boardWith("P1", { done: [archTask("d1", "done", 3), archTask("d2", "done", 4)] }),
      showArchived: true,
    });
    c.archiveAllDoneConfirmOpen = true;
    const reqs: Array<{ id: string; req: any }> = [];
    const p = patchApi("updateTask", async (id: string, req: any) => { reqs.push({ id, req }); return { id, version: 9 }; });
    try {
      await c.mod.confirmArchiveAllDone();
    } finally {
      p.restore();
    }
    assert.deepEqual(reqs.map((r) => r.id), ["d1", "d2"], "sequential per-task PATCH");
    assert.deepEqual(reqs[0].req, { actor: ACTOR, version: 3, archived: true });
    assert.equal(c.board!.tasks.done.length, 0, "optimistically emptied as each succeeded");
    assert.equal(c.board!.counts.done, 0);
    assert.equal(c.archiveAllDoneBusy, false, "busy cleared in finally");
    assert.equal(c.archiveAllDoneConfirmOpen, false);
    assert.deepEqual(c.loadBoardCalls, [true], "final reload settles counts/blocked state");
    assert.equal(c.rootRefreshCalls, 1, "archived view ON → refreshed for the new members");
    assert.match(c.toasts.at(-1)!.msg, /Archived 2 done tasks/);
  });

  test("singular wording for exactly one archived task", async () => {
    const c = makeCtx({ board: boardWith("P1", { done: [archTask("d1", "done")] }) });
    const p = patchApi("updateTask", async () => ({ id: "d1", version: 2 }));
    try {
      await c.mod.confirmArchiveAllDone();
    } finally {
      p.restore();
    }
    assert.match(c.toasts.at(-1)!.msg, /Archived 1 done task\b/);
  });

  test("per-task 409: re-read THAT row and retry once; already-archived elsewhere counts ok without a second PATCH", async () => {
    const c = makeCtx({ board: boardWith("P1", { done: [archTask("d1", "done"), archTask("d2", "done")] }) });
    let d2Updates = 0;
    let getTaskCalls = 0;
    const p = patchApi("updateTask", async (id: string, req: any) => {
      if (id === "d2") {
        d2Updates++;
        if (d2Updates === 1) throw conflict();
        return { id, version: 9 };
      }
      return { id, version: 2 };
    });
    const g = patchApi("getTask", async (id: string) => {
      getTaskCalls++;
      assert.equal(id, "d2");
      // The other client archived it while we were working — skip the retry.
      return detailFor("d2", { version: 4, status: "done", archived: true });
    });
    try {
      await c.mod.confirmArchiveAllDone();
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(getTaskCalls, 1);
    assert.equal(d2Updates, 1, "no second patch — already archived on the server");
    assert.equal(c.board!.tasks.done.length, 0, "both rows removed locally (one is a no-op on the server)");
    assert.match(c.toasts.at(-1)!.msg, /Archived 2 done tasks/);
  });

  test("partial failure: per-task error + retry-still-failing both count failed; summary toasts in error kind", async () => {
    const c = makeCtx({ board: boardWith("P1", { done: [archTask("d1", "done"), archTask("d2", "done"), archTask("d3", "done")] }) });
    const p = patchApi("updateTask", async (id: string) => {
      if (id === "d2") throw new ApiError(500, { error: "boom", message: "exploded" } as any);
      if (id === "d3") throw conflict(); // retry also conflicts → failed
      return { id, version: 2 };
    });
    const g = patchApi("getTask", async () => { throw new Error("re-read also down"); });
    try {
      await c.mod.confirmArchiveAllDone();
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(c.board!.tasks.done.length, 2, "d1 removed locally; d2/d3 (the failed rows) stay");
    assert.equal(c.toasts.at(-1)!.kind, "error");
    assert.match(c.toasts.at(-1)!.msg, /Archived 1 done task · 2 failed/);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.equal(c.archiveAllDoneBusy, false);
  });

  test("busy flag clears even when the loop body throws outside its per-task guard", async () => {
    const c = makeCtx({ board: boardWith("P1", { done: [archTask("d1", "done")] }) });
    // Make api.getTask itself throw a NON-conflict during d1's (first patch fails with conflict)
    const p = patchApi("updateTask", async () => { throw conflict(); });
    const g = patchApi("getTask", async () => { throw new Error("re-read exploded"); });
    try {
      await c.mod.confirmArchiveAllDone();
    } finally {
      p.restore(); g.restore();
    }
    assert.equal(c.archiveAllDoneBusy, false);
  });

  test("open/cancel toggles the confirm step flag", () => {
    const c = makeCtx();
    c.mod.openArchiveAllDoneConfirm();
    assert.equal(c.archiveAllDoneConfirmOpen, true);
    c.mod.cancelArchiveAllDoneConfirm();
    assert.equal(c.archiveAllDoneConfirmOpen, false);
  });
});

// ----------------------------------------------------------------------- project-switch reset seam
describe("resetForProjectSwitch (selectProject cascade)", () => {
  test("exits the mode, drops data + confirm step, and retires in-flight fetches", async () => {
    const c = makeCtx({ showArchived: true, archivedBoardTasks: { todo: [archTask("cached", "todo")], doing: [], done: [] } });
    let release: (v?: unknown) => void;
    const gate = new Promise((res) => { release = res; });
    const p = patchApi("listTasks", async () => { await gate; return { tasks: [archTask("late", "todo")], total: 1 }; });
    try {
      c.showArchived = false; // re-arm a fresh toggle…
      const r = c.mod.toggleShowArchived(); // …leaving one fetch in flight
      c.archiveAllDoneConfirmOpen = true;
      c.mod.resetForProjectSwitch();
      assert.equal(c.showArchived, false, "mode exited");
      assert.equal(c.archivedBoardTasks, null, "cached data dropped");
      assert.equal(c.archiveAllDoneConfirmOpen, false, "confirm step dropped too");
      release!();
      await r;
    } finally {
      p.restore();
    }
    // The late response was RETIRED by the sequence bump: nothing applied afterwards.
    assert.equal(c.showArchived, false, "the superseded toggle did not flip the mode back on");
    assert.equal(c.archivedBoardTasks, null, "the in-flight fetch's rows never landed");
  });
});

// ----------------------------------------------------------------------- misc parity pins
test("errMsg passthrough is the api module's (module does not re-wrap transport errors)", () => {
  assert.match(errMsg(new Error("down")), /down/);
});
