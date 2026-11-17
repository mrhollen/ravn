// Unit tests for modules/batch.ts — Phase 2 final-sweep extraction of the
// board multi-select + batch action bar from app.ts. Drives the factory with a
// recording fixture (state-in/out lambdas over plain fields, hooks captured) so
// every parity-relevant behavior is pinned without the Alpine root: selection
// semantics (toggle / shift-range / select-all-visible / clear), menu mutual
// exclusion, and the per-task AC3 execution engine (optimistic moves, partial
// failure keeping ONLY failed ids, 409 re-read + retry-once, already-applied
// skip, busy/empty guards).

import { test } from "node:test";
import assert from "node:assert/strict";
import { createBatchModule } from "../src/modules/batch";
import { api, ApiError } from "../src/api";
import type { Board, Status, TaskSummary } from "../src/types";

// --------------------------------------------------------------------------- helpers

function makeTask(id: string, status: Status = "todo", version = 1, extra: Record<string, unknown> = {}): TaskSummary {
  return {
    id,
    title: `Task ${id}`,
    status,
    priority: 1,
    version,
    created_at: "2026-01-01T00:00:00Z",
    ...extra,
  } as TaskSummary;
}

function makeBoard(rows: Array<[Status, TaskSummary[]]>): Board {
  const board: Board = {
    project_id: "proj-batch",
    counts: { todo: 0, doing: 0, done: 0 },
    tasks: { todo: [], doing: [], done: [] },
  };
  for (const [status, list] of rows) {
    board.tasks[status] = list;
    board.counts[status] = list.length;
  }
  return board;
}

interface Fixture {
  m: ReturnType<typeof createBatchModule>;
  state: {
    selectedTaskIds: Set<string>;
    anchor: string | null;
    busy: boolean;
    moveOpen: boolean;
    priOpen: boolean;
    assignOpen: boolean;
  };
  board: Board | null;
  toasts: Array<{ msg: string; kind?: "error" | "info" }>;
  loadBoardCalls: unknown[][];
  setColumns(cols: unknown[]): void;
  setAssignees(names: string[]): void;
}

function makeFixture(board: Board | null = makeBoard([])): Fixture {
  const state: Fixture["state"] = {
    selectedTaskIds: new Set<string>(),
    anchor: null,
    busy: false,
    moveOpen: false,
    priOpen: false,
    assignOpen: false,
  };
  let cols: unknown[] = [];
  let assignees: string[] = [];
  const fixture: Fixture = {
    m: null as any,
    state,
    board,
    toasts: [],
    loadBoardCalls: [],
    setColumns(c) { cols = c; },
    setAssignees(n) { assignees = n; },
  };
  const deps = {
    selectedTaskIds: () => state.selectedTaskIds,
    selectionAnchorId: () => state.anchor,
    batchBusy: () => state.busy,
    batchMoveMenuOpen: () => state.moveOpen,
    batchPriorityMenuOpen: () => state.priOpen,
    batchAssignMenuOpen: () => state.assignOpen,
    board: () => board,
    setSelectedTaskIds: (v: Set<string>) => { state.selectedTaskIds = v; },
    setSelectionAnchorId: (v: string | null) => { state.anchor = v; },
    setBatchBusy: (v: boolean) => { state.busy = v; },
    setBatchMoveMenuOpen: (v: boolean) => { state.moveOpen = v; },
    setBatchPriorityMenuOpen: (v: boolean) => { state.priOpen = v; },
    setBatchAssignMenuOpen: (v: boolean) => { state.assignOpen = v; },
    columns: () => cols as any,
    availableAssignees: () => assignees,
    actor: () => ({ name: "tester", type: "agent" as const }),
    toast: (msg: string, kind?: "error" | "info") => { fixture.toasts.push({ msg, kind }); return 1; },
    loadBoard: (...args: unknown[]) => { fixture.loadBoardCalls.push(args); return Promise.resolve(); },
  };
  fixture.m = createBatchModule(deps as any);
  return fixture;
}

/** Swap the module's api.updateTask / api.getTask for the test body, restoring
 *  the originals afterwards (same monkey-patch pattern app.test.ts uses). */
async function withApi(
  patch: { updateTask?: (...args: unknown[]) => Promise<unknown>; getTask?: (...args: unknown[]) => Promise<unknown> },
  fn: () => Promise<void>,
): Promise<void> {
  const origUpdate = (api as any).updateTask;
  const origGet = (api as any).getTask;
  if (patch.updateTask) (api as any).updateTask = patch.updateTask;
  if (patch.getTask) (api as any).getTask = patch.getTask;
  try {
    await fn();
  } finally {
    (api as any).updateTask = origUpdate;
    (api as any).getTask = origGet;
  }
}

function select(f: Fixture, ids: string[]): void {
  f.state.selectedTaskIds = new Set(ids);
  f.state.anchor = ids[0] ?? null;
}

// --------------------------------------------------------------------------- selection

test("batch: isTaskSelected / selectedCount mirror the root Set", () => {
  const f = makeFixture();
  select(f, ["t-1", "t-2"]);
  assert.equal(f.m.isTaskSelected("t-1"), true);
  assert.equal(f.m.isTaskSelected("nope"), false);
  assert.equal(f.m.selectedCount(), 2);
});

test("batch: toggleSelectTask adds/removes and re-anchors, replacing the Set (Alpine-visible)", () => {
  const f = makeFixture();
  select(f, ["t-1"]);
  const before = f.state.selectedTaskIds;

  f.m.toggleSelectTask("t-2");
  assert.notEqual(f.state.selectedTaskIds, before, "the Set is REPLACED — Set mutations are invisible to Alpine's proxy");
  assert.ok(f.state.selectedTaskIds.has("t-1") && f.state.selectedTaskIds.has("t-2"));
  assert.equal(f.state.anchor, "t-2", "a plain toggle re-anchors to the clicked card");

  f.m.toggleSelectTask("t-1");
  assert.ok(!f.state.selectedTaskIds.has("t-1"), "toggling again removes it");
  assert.ok(f.state.selectedTaskIds.has("t-2"));
  assert.equal(f.state.anchor, "t-1", "a remove toggle re-anchors too");
});

test("batch: shift-range with no anchor degrades to a plain toggle (which anchors)", () => {
  const f = makeFixture();
  f.setColumns([{ status: "todo", label: "To Do", tasks: [makeTask("t-1"), makeTask("t-2")], totalCount: 2 }]);
  assert.equal(f.state.anchor, null);

  f.m.rangeSelectInColumn("t-2");
  assert.deepEqual([...f.state.selectedTaskIds], ["t-2"], "no span — just the clicked card");
  assert.equal(f.state.anchor, "t-2", "…and it becomes the anchor");
});

test("batch: shift-range spans FROM the anchor INCLUSIVE and is additive across columns", () => {
  const f = makeFixture();
  const todo = [makeTask("t-1"), makeTask("t-2"), makeTask("t-3")];
  const doing = [makeTask("d-1", "doing")];
  f.setColumns([
    { status: "todo", label: "To Do", tasks: todo, totalCount: 3 },
    { status: "doing", label: "Doing", tasks: doing, totalCount: 1 },
  ]);
  select(f, ["d-1"]); // a selection in ANOTHER column must survive

  f.m.rangeSelectInColumn("t-3"); // no anchor yet → toggle t-3 (anchors it)
  assert.deepEqual([...f.state.selectedTaskIds].sort(), ["d-1", "t-3"]);

  f.state.anchor = "t-1"; // Excel-style: anchor re-pinned to the first card
  f.m.rangeSelectInColumn("t-3"); // span t-1..t-3 inclusive, ADDITIVELY
  assert.deepEqual([...f.state.selectedTaskIds].sort(), ["d-1", "t-1", "t-2", "t-3"]);
  assert.equal(f.state.anchor, "t-1", "the anchor deliberately does NOT move after a range select");

  f.m.rangeSelectInColumn("t-2"); // next shift-click still spans FROM t-1
  // Range select is ADDITIVE — it only ever adds ids to the current selection,
  // so t-3 (spanned earlier) stays selected even though it's outside this span.
  assert.deepEqual([...f.state.selectedTaskIds].sort(), ["d-1", "t-1", "t-2", "t-3"]);
});

test("batch: shift-range degrades to a single-card toggle when the anchor is not in that column's visible order", () => {
  const f = makeFixture();
  f.setColumns([
    { status: "todo", label: "To Do", tasks: [makeTask("t-1")], totalCount: 1 },
    { status: "doing", label: "Doing", tasks: [makeTask("d-1", "doing")], totalCount: 1 },
  ]);
  select(f, ["t-1"]);

  f.m.rangeSelectInColumn("d-1"); // cross-column range is undefined → toggle + re-anchor
  assert.deepEqual([...f.state.selectedTaskIds].sort(), ["d-1", "t-1"], "the clicked card toggled in; t-1 untouched by a span");
  assert.equal(f.state.anchor, "d-1", "re-anchored to the clicked card");
});

test("batch: selectAllVisible adopts exactly the rendered column tasks, merged with the existing selection; no board → no-op", () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1"), makeTask("t-2")]], ["done", [makeTask("d-x", "done")]]]));
  f.setColumns([
    { status: "todo", label: "To Do", tasks: f.board!.tasks.todo, totalCount: 2 },
    { status: "doing", label: "Doing", tasks: [], totalCount: 0 },
    // a done card NOT rendered (beyond the cap) must stay out
  ]);
  select(f, ["t-9"]);

  f.m.selectAllVisible();
  assert.deepEqual([...f.state.selectedTaskIds].sort(), ["t-1", "t-2", "t-9"],
    "rendered todo + pre-existing selection; hidden done card excluded");

  const none = makeFixture(null);
  select(none, ["t-9"]);
  none.m.selectAllVisible();
  assert.deepEqual([...none.state.selectedTaskIds], ["t-9"], "without a board the method is a pure no-op");
});

test("batch: selectAllVisible honors columns() output even when it lags board (the cap)", () => {
  const f = makeFixture(makeBoard([["done", [makeTask("a", "done"), makeTask("b", "done")]]]));
  // Only ONE done card is rendered — the other is beyond the Done cap.
  f.setColumns([{ status: "done", label: "Done", tasks: [f.board!.tasks.done[0]], totalCount: 1 }]);
  f.m.selectAllVisible();
  assert.deepEqual([...f.state.selectedTaskIds], ["a"], "the hidden card is NOT selectable until expanded");
});

test("batch: clearSelection wipes selection, anchor and all three menus; no-op when already empty", () => {
  const f = makeFixture();
  select(f, ["t-1"]);
  f.state.moveOpen = true;
  f.state.priOpen = true;
  f.state.assignOpen = true;

  f.m.clearSelection();
  assert.equal(f.state.selectedTaskIds.size, 0);
  assert.equal(f.state.anchor, null);
  assert.equal(f.state.moveOpen, false);
  assert.equal(f.state.priOpen, false);
  assert.equal(f.state.assignOpen, false);

  // Early-return path: nothing selected + no anchor → menu state is left alone.
  f.state.assignOpen = true;
  f.m.clearSelection();
  assert.equal(f.state.assignOpen, true, "the early return must not touch the menus");
});

test("batch: opening one batch menu collapses the other two (bar shows at most one dropdown)", () => {
  const f = makeFixture();
  f.state.priOpen = true;
  f.state.assignOpen = true;

  f.m.toggleBatchMoveMenu();
  assert.equal(f.state.moveOpen, true);
  assert.equal(f.state.priOpen, false, "opening the move menu closes priority");
  assert.equal(f.state.assignOpen, false, "…and assign");

  f.m.toggleBatchPriorityMenu();
  assert.equal(f.state.priOpen, true);
  assert.equal(f.state.moveOpen, false);

  f.m.toggleBatchAssignMenu();
  f.m.toggleBatchAssignMenu(); // open then close — only that menu toggles
  assert.equal(f.state.assignOpen, false);
});

test("batch: batchAssigneeOptions delegates to the board module's availableAssignees", () => {
  const f = makeFixture();
  f.setAssignees(["alice", "bob"]);
  assert.deepEqual(f.m.batchAssigneeOptions(), ["alice", "bob"]);
});

// --------------------------------------------------------------------------- AC3 engine

test("batch: batchMoveTo applies optimistic status moves + count deltas, settles with one silent loadBoard and clears the selection", async () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1"), makeTask("t-2")]]]));
  select(f, ["t-1", "t-2"]);
  let updateCalls: unknown[][] = [];

  await withApi(
    {
      updateTask: async (...args: unknown[]) => {
        updateCalls.push(args);
        const [id] = args as [string, any];
        return makeTask(id, "done", (f.board.tasks.todo.find((t) => t.id === id)?.version ?? 1) + 1);
      },
    },
    async () => {
      await f.m.batchMoveTo("done");
    },
  );

  assert.equal(updateCalls.length, 2, "one PATCH per selected card");
  assert.deepEqual(f.board.tasks.todo, [], "todo emptied optimistically");
  assert.equal(f.board.tasks.done.length, 2);
  assert.equal(f.board.counts.todo, 0);
  assert.equal(f.board.counts.done, 2);
  assert.equal(f.state.selectedTaskIds.size, 0, "full success clears the selection");
  assert.equal(f.state.anchor, null);
  assert.equal(f.state.busy, false, "the busy flag resets in both outcomes");
  assert.deepEqual(f.loadBoardCalls, [[true]], "exactly ONE settling reload (silent force)");
  assert.deepEqual(f.toasts, [{ msg: "Moved 2 tasks to Done", kind: "info" }]);
});

test("batch: partial failure keeps ONLY the failed ids selected and surfaces an error summary", async () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1"), makeTask("t-2")]]]));
  select(f, ["t-1", "t-2"]);

  await withApi(
    {
      updateTask: async (id: string) => {
        if (id === "t-2") throw new ApiError(500, { error: "boom", message: "upstream exploded" } as any);
        return makeTask(id, "done", 2);
      },
    },
    async () => {
      await f.m.batchMoveTo("done");
    },
  );

  assert.deepEqual([...f.state.selectedTaskIds], ["t-2"], "the user can re-run just the failed card");
  assert.equal(f.board.tasks.done.length, 1, "only the successful move applied optimistically");
  assert.deepEqual(f.toasts, [{ msg: "Moved 1 task to Done · 1 failed", kind: "error" }]);
  assert.equal(f.state.busy, false);
});

test("batch: a 409 version conflict re-reads THAT task and retries exactly once with the fresh version", async () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1", "todo", 1)]]]));
  select(f, ["t-1"]);
  const versions: number[] = [];

  await withApi(
    {
      updateTask: async (id: string, req: any) => {
        versions.push(req.version);
        if (req.version === 1) throw new ApiError(409, { error: "version_conflict", current_version: 5 } as any);
        return makeTask(id, "doing", req.version + 1);
      },
      getTask: async () => makeTask("t-1", "todo", 5),
    },
    async () => {
      await f.m.batchMoveTo("doing");
    },
  );

  assert.deepEqual(versions, [1, 5], "initial attempt with board version, then exactly one retry at the fresh version");
  assert.equal(f.state.selectedTaskIds.size, 0, "the retried task counts as success");
  assert.deepEqual(f.toasts, [{ msg: "Moved 1 task to Doing", kind: "info" }]);
});

test("batch: a 409 whose re-read shows the change ALREADY APPLIED (archive) counts as success without a second PATCH", async () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1", "todo", 1)]]]));
  select(f, ["t-1"]);
  let updates = 0;

  await withApi(
    {
      updateTask: async (id: string) => {
        updates++;
        if (updates === 1) throw new ApiError(409, { error: "version_conflict", current_version: 2 } as any);
        return makeTask(id, "todo", 3);
      },
      getTask: async () => ({ id: "t-1", archived: true, version: 2 }),
    },
    async () => {
      await f.m.batchArchiveSelected();
    },
  );

  assert.equal(updates, 1, "the already-applied skip must NOT re-patch");
  assert.deepEqual(f.board.tasks.todo, [], "…but the optimistic removal still applies");
  assert.equal(f.state.selectedTaskIds.size, 0);
  assert.deepEqual(f.toasts, [{ msg: "Archived 1 selected task", kind: "info" }]);
});

test("batch: batchAssignOwner learns agent type from an existing board row; null clears the owner; blank text is a no-op", async () => {
  const bot = makeTask("t-bot", "doing", 1, { assignee: { name: "bot-1", type: "agent" } });
  const f = makeFixture(makeBoard([["doing", [bot]], ["todo", [makeTask("t-x")]]]));
  select(f, ["t-bot", "t-x"]);
  const patches: any[] = [];

  // (a) learn the type from the row carrying that assignee
  await withApi(
    { updateTask: async (id: string, req: any) => { patches.push({ id, ...req }); return makeTask(id, "doing", 2); } },
    async () => { await f.m.batchAssignOwner("bot-1"); },
  );
  assert.equal(patches.length, 2);
  for (const p of patches) {
    assert.deepEqual(p.assignee, { name: "bot-1", type: "agent" }, "agents stay agents — learned from the board row");
  }

  // (b) clearing — a FULL success in (a) cleared the selection, so re-select
  const before2 = patches.length;
  select(f, ["t-bot", "t-x"]);
  await withApi(
    { updateTask: async (id: string, req: any) => { patches.push({ id, ...req }); return makeTask(id, "doing", 3); } },
    async () => { await f.m.batchAssignOwner(null); },
  );
  assert.equal(patches.length, before2 + 2);
  for (const p of patches.slice(before2)) {
    assert.equal(p.clear_assignee, true, "name=null sends clear_assignee");
  }

  // (c) whitespace-only free text → early return, nothing sent
  const before = patches.length;
  await withApi({ updateTask: async () => { throw new Error("must not be called"); } }, async () => {
    await f.m.batchAssignOwner("   ");
  });
  assert.equal(patches.length, before);
});

test("batch: the engine refuses an empty selection and a concurrent run (busy guard)", async () => {
  const f = makeFixture(makeBoard([["todo", [makeTask("t-1")]]]));
  let calls = 0;
  const stub = { updateTask: async (...args: unknown[]) => { calls++; return args[0] as string && makeTask(args[0] as string); } };

  await withApi(stub, async () => {
    await f.m.batchMoveTo("done"); // nothing selected
  });
  assert.equal(calls, 0, "empty selection: no PATCHes, no toast");
  assert.equal(f.toasts.length, 0);

  select(f, ["t-1"]);
  f.state.busy = true; // a run already in flight
  await withApi(stub, async () => {
    await f.m.batchMoveTo("done");
  });
  assert.equal(calls, 0, "busy: the second entry point returns early");
});
