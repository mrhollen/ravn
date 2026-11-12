// Unit tests for web/src/modules/new-task.ts (Phase 2 batch B slice "new-task").
// Drives createNewTaskModule() with a mutable stub state and monkey-patched api
// methods — no DOM, no network. The root-level surface (the delegation shims)
// stays covered in test/app.test.ts; this file covers the moved internals: the
// dialog lifecycle (open/close/reset), the draft-dirty derivation + guarded
// close, and createTask — payload assembly, the version-guarded non-todo move,
// the optional subtask relation link, the duplicates advisory and the refresh
// fan-out.

import test from "node:test";
import assert from "node:assert/strict";
import { createNewTaskModule } from "../src/modules/new-task";
import type { NewTaskDeps, NewTaskFormState } from "../src/modules/new-task";
import { api, ApiError } from "../src/api";
import { dateInputToIso, splitLines } from "../src/util";

// ------------------------------------------------------------------- harness
function blankForm(): NewTaskFormState {
  return { title: "", description: "", priority: 0, due: "", acs: "", assigneeName: "", assigneeType: "human" };
}

/** One mutable state object wired into the deps accessors — the module reads
 *  and writes exactly through those, so assertions read straight off `state`. */
function makeHarness(init?: Record<string, any>) {
  const state: Record<string, any> = {
    selectedProjectId: "01PROJECT",
    detail: null as any,
    newTask: blankForm(),
    taskBusy: false,
    newTaskParentId: null as string | null,
    newTaskTarget: "todo" as any,
    showNewTask: false,
    confirmNewTaskDiscard: false,
    duplicates: [] as any[],
  };
  Object.assign(state, init);

  const calls = {
    openDialog: [] as [string, string?][],
    closeDialog: [] as unknown[],
    toasts: [] as [string, string?][],
    loadProjects: 0,
    loadBoard: [] as (boolean | undefined)[],
    refreshDetail: [] as (boolean | undefined)[],
  };

  const deps: NewTaskDeps = {
    selectedProjectId: () => state.selectedProjectId,
    detail: () => state.detail,
    newTask: () => state.newTask,
    taskBusy: () => state.taskBusy,
    newTaskParentId: () => state.newTaskParentId,
    newTaskTarget: () => state.newTaskTarget,
    setShowNewTask: (v) => { state.showNewTask = v; },
    setNewTaskTarget: (s) => { state.newTaskTarget = s; },
    setConfirmNewTaskDiscard: (v) => { state.confirmNewTaskDiscard = v; },
    setDuplicates: (t) => { state.duplicates = t; },
    setTaskBusy: (v) => { state.taskBusy = v; },
    setNewTask: (f) => { state.newTask = f; },
    setNewTaskParentId: (v) => { state.newTaskParentId = v; },
    toast: (m, k) => { calls.toasts.push([m, k]); return calls.toasts.length; },
    actor: () => ({ name: "tester", type: "agent" }),
    openDialog: (id, sel) => { calls.openDialog.push([id, sel]); return { id, sel } as any; },
    closeDialog: (f) => { calls.closeDialog.push(f); },
    loadProjects: async () => { calls.loadProjects += 1; },
    loadBoard: async (force) => { calls.loadBoard.push(force); },
    refreshDetail: async (force) => { calls.refreshDetail.push(force); },
  };

  const mod = createNewTaskModule(deps);
  return { state, calls, mod };
}

function patch(method: string, impl: (...args: any[]) => Promise<unknown>) {
  const orig = (api as any)[method];
  (api as any)[method] = impl;
  return () => { (api as any)[method] = orig; };
}

const CREATED = { id: "01NEW", version: 1 } as any;

// ------------------------------------------------------------ dialog lifecycle
test("openNewTask: opens the dialog, resets draft + advisory, focuses the first field", () => {
  const h = makeHarness({ showNewTask: true, newTask: { ...blankForm(), title: "stale" }, duplicates: [{ id: "x" }] });
  h.mod.openNewTask();
  assert.equal(h.state.showNewTask, true);
  assert.deepEqual(h.state.newTask, blankForm());
  assert.deepEqual(h.state.duplicates, []);
  assert.equal(h.state.confirmNewTaskDiscard, false);
  assert.deepEqual(h.calls.openDialog, [["cb-new-task", 'input[x-model="newTask.title"]']]);
});

test("openNewSubtask: null/undefined parent is a no-op", () => {
  const h = makeHarness();
  h.mod.openNewSubtask(null);
  h.mod.openNewSubtask(undefined);
  assert.equal(h.state.showNewTask, false);
  assert.equal(h.state.newTaskParentId, null);
  assert.equal(h.calls.openDialog.length, 0);
});

test("openNewSubtask: opens the todo dialog and records the parent id", () => {
  const h = makeHarness();
  h.mod.openNewSubtask({ id: "01PARENT" });
  assert.equal(h.state.showNewTask, true);
  assert.equal(h.state.newTaskParentId, "01PARENT");
});

test("closeNewTask: resets everything and closes the focus session", () => {
  const h = makeHarness({ showNewTask: true, newTaskParentId: "01PARENT" });
  h.mod.openNewTask(); // establishes a focus session
  h.mod.closeNewTask();
  assert.equal(h.state.showNewTask, false);
  assert.equal(h.state.confirmNewTaskDiscard, false);
  assert.equal(h.state.newTaskParentId, null);
  assert.deepEqual(h.state.newTask, blankForm());
  assert.deepEqual(h.state.duplicates, []);
  assert.equal(h.calls.closeDialog.length, 1);
});

// ------------------------------------------------------------------ dirty guard
test("newTaskDirty: a freshly opened form is never dirty", () => {
  const h = makeHarness();
  assert.equal(h.mod.newTaskDirty(), false);
});

test("newTaskDirty: every single field makes the draft dirty", () => {
  const cases: Array<Partial<NewTaskFormState>> = [
    { title: "x" },
    { description: "d" },
    { due: "2026-11-12" },
    { priority: 3 },
    { assigneeName: "bob" },
    { assigneeType: "agent" },
    { acs: "one thing" },
  ];
  for (const c of cases) {
    const h = makeHarness({ newTask: { ...blankForm(), ...c } });
    assert.equal(h.mod.newTaskDirty(), true, JSON.stringify(c));
  }
});

test("requestCloseNewTask: clean draft closes immediately", () => {
  const h = makeHarness({ showNewTask: true });
  h.mod.openNewTask();
  h.mod.requestCloseNewTask();
  assert.equal(h.state.showNewTask, false);
  assert.equal(h.state.confirmNewTaskDiscard, false);
});

test("requestCloseNewTask: dirty draft arms the confirm row and keeps the dialog", () => {
  const h = makeHarness({ showNewTask: true, newTask: { ...blankForm(), title: "keep me" } });
  h.mod.openNewTask();
  h.state.newTask.title = "re-typed after open"; // form edited post-open
  h.mod.requestCloseNewTask();
  assert.equal(h.state.showNewTask, true);
  assert.equal(h.state.confirmNewTaskDiscard, true);
});

test("cancelNewTaskConfirm: clears the confirm row and preserves the draft", () => {
  const h = makeHarness({ showNewTask: true, newTask: { ...blankForm(), title: "keep me" } });
  h.mod.openNewTask();
  h.state.newTask.title = "keep me";
  h.mod.requestCloseNewTask();
  h.mod.cancelNewTaskConfirm();
  assert.equal(h.state.confirmNewTaskDiscard, false);
  assert.equal(h.state.showNewTask, true);
  assert.equal(h.state.newTask.title, "keep me");
});

test("clearDuplicates: empties the advisory list", () => {
  const h = makeHarness({ duplicates: [{ id: "a" }, { id: "b" }] });
  h.mod.clearDuplicates();
  assert.deepEqual(h.state.duplicates, []);
});

// --------------------------------------------------------------------- createTask
test("createTask: busy / blank title / no project are early returns (no api calls)", async () => {
  let createCalls = 0;
  const unpatch = patch("createTask", async () => { createCalls += 1; return { task: CREATED }; });
  try {
    // PARITY NOTE (do not "fix"): the early return fires BEFORE taskBusy is
    // touched — an already-busy root stays busy (the in-flight call's finally
    // owns it); the other two guards leave the untouched flag false.
    const cases: Array<[Record<string, any>, boolean]> = [
      [{ taskBusy: true }, true],
      [{ newTask: { ...blankForm(), title: "   " } }, false],
      [{ selectedProjectId: "" }, false],
    ];
    for (const [init, busyAfter] of cases) {
      const h = makeHarness(init);
      if (!init.newTask) h.state.newTask.title = "real title";
      await h.mod.createTask();
      assert.equal(h.state.taskBusy, busyAfter, JSON.stringify(Object.keys(init)));
    }
    assert.equal(createCalls, 0);
  } finally { unpatch(); }
});

test("createTask (todo): assembles the payload, stores the advisory, closes and refreshes", async () => {
  const seen: any[] = [];
  const unpatch = patch("createTask", async (body) => {
    seen.push(body);
    return { task: CREATED, possible_duplicates: [{ id: "01DUP" }] };
  });
  try {
    const h = makeHarness({
      newTask: { ...blankForm(), title: " Ship it ", description: " why not ", priority: 2, due: "2026-12-01", acs: "a\nb", assigneeName: " bob ", assigneeType: "agent" },
    });
    h.mod.openNewTask(); // fresh reset first… then re-fill the draft
    h.state.newTask = { ...blankForm(), title: " Ship it ", description: " why not ", priority: 2, due: "2026-12-01", acs: "a\nb", assigneeName: " bob ", assigneeType: "agent" };
    await h.mod.createTask();
    assert.equal(seen.length, 1);
    assert.deepEqual(seen[0], {
      actor: { name: "tester", type: "agent" },
      title: "Ship it",
      description: "why not",
      project_id: "01PROJECT",
      priority: 2,
      due_at: dateInputToIso("2026-12-01"),
      assignee: { name: "bob", type: "agent" },
      acceptance_criteria: splitLines("a\nb"),
    });
    // PARITY NOTE (do not "fix"): createTask stores res.possible_duplicates and
    // THEN closeNewTask() resets the advisory to [] — the net effect is that a
    // fresh create never leaves the board advisory populated. Reproduced
    // verbatim; the banner itself is covered by e2e/toast.spec.ts (direct poke).
    assert.deepEqual(h.state.duplicates, []);
    assert.equal(h.state.showNewTask, false);
    assert.equal(h.state.taskBusy, false);
    assert.equal(h.calls.loadProjects, 1);
    assert.deepEqual(h.calls.loadBoard, [true]);
    assert.equal(h.calls.refreshDetail.length, 0);
  } finally { unpatch(); }
});

test("createTask (todo): blank description/acs send undefined / empty and skip the assignee", async () => {
  const seen: any[] = [];
  const unpatch = patch("createTask", async (body) => { seen.push(body); return { task: CREATED }; });
  try {
    const h = makeHarness();
    h.state.newTask.title = "minimal";
    await h.mod.createTask();
    assert.equal(seen[0].description, undefined);
    assert.equal(seen[0].assignee, undefined);
    assert.deepEqual(seen[0].acceptance_criteria, splitLines(""));
    assert.equal(h.state.duplicates.length, 0, "undefined possible_duplicates defaults to []");
  } finally { unpatch(); }
});

test("createTask (non-todo target): follows up with ONE version-guarded move; conflict is advisory", async () => {
  const seen: any[] = [];
  const unCreate = patch("createTask", async () => ({ task: CREATED }));
  const unUpdate = patch("updateTask", async (id, body) => { seen.push([id, body]); throw new ApiError(409, { error: "version_conflict", current_version: 7 } as any); });
  try {
    const h = makeHarness();
    h.mod.openNewTask("doing"); // target column ≠ todo
    h.state.newTask.title = "direct to doing";
    await h.mod.createTask();
    assert.deepEqual(seen, [["01NEW", { actor: { name: "tester", type: "agent" }, version: 1, status: "doing" }]]);
    assert.deepEqual(h.calls.toasts, [["New task changed elsewhere before the move — check its column.", "info"]]);
    assert.equal(h.state.showNewTask, false, "the create itself succeeded — dialog still closes");
  } finally { unCreate(); unUpdate(); }
});

test("createTask (non-todo target): a non-conflict move error falls to the outer catch (dialog stays open)", async () => {
  const unCreate = patch("createTask", async () => ({ task: CREATED }));
  const unUpdate = patch("updateTask", async () => { throw new Error("boom-move"); });
  try {
    const h = makeHarness();
    h.mod.openNewTask("doing");
    h.state.newTask.title = "will fail the move";
    await h.mod.createTask();
    assert.equal(h.state.showNewTask, true, "outer catch — not closed on create");
    assert.deepEqual(h.calls.toasts, [["boom-move", "error"]]);
  } finally { unCreate(); unUpdate(); }
});

test("createTask (subtask): links the child relation; a link failure toasts but still closes", async () => {
  const seen: any[] = [];
  const unCreate = patch("createTask", async () => ({ task: CREATED }));
  const unRel = patch("addRelation", async (...args) => { seen.push(args); throw new Error("link down"); });
  try {
    const h = makeHarness();
    h.mod.openNewSubtask({ id: "01PARENT" });
    h.state.newTask.title = "a child";
    await h.mod.createTask();
    assert.deepEqual(seen, [["01NEW", "01PARENT", "child", { name: "tester", type: "agent" }]]);
    assert.deepEqual(h.calls.toasts, [["Task created, but failed to link subtask: link down", "error"]]);
    assert.equal(h.state.showNewTask, false, "created is truthy — the dialog closes and refreshes");
    assert.deepEqual(h.calls.loadBoard, [true]);
  } finally { unCreate(); unRel(); }
});

test("createTask (subtask): refreshDetail joins the fan-out only when the detail IS the parent", async () => {
  const unCreate = patch("createTask", async () => ({ task: CREATED }));
  try {
    const h1 = makeHarness({ detail: { id: "01PARENT" } });
    h1.mod.openNewSubtask({ id: "01PARENT" });
    h1.state.newTask.title = "child of the open detail";
    await h1.mod.createTask();
    assert.deepEqual(h1.calls.refreshDetail, [true]);

    const h2 = makeHarness({ detail: { id: "OTHER" } });
    h2.mod.openNewSubtask({ id: "01PARENT" });
    h2.state.newTask.title = "child of a closed detail";
    await h2.mod.createTask();
    assert.equal(h2.calls.refreshDetail.length, 0);

    const h3 = makeHarness({ detail: null });
    h3.mod.openNewSubtask({ id: "01PARENT" });
    h3.state.newTask.title = "child with no detail open";
    await h3.mod.createTask();
    assert.equal(h3.calls.refreshDetail.length, 0);
  } finally { unCreate(); }
});

test("createTask: a create failure toasts, keeps the draft + dialog and still refreshes the board", async () => {
  const unCreate = patch("createTask", async () => { throw new Error("nope"); });
  try {
    const h = makeHarness({ showNewTask: true, newTask: { ...blankForm(), title: "draft" } });
    await h.mod.createTask();
    assert.deepEqual(h.calls.toasts, [["nope", "error"]]);
    assert.equal(h.state.showNewTask, true);
    assert.equal(h.state.newTask.title, "draft", "the half-typed draft survives");
    assert.equal(h.state.taskBusy, false, "finally clears the busy flag");
    assert.deepEqual(h.calls.loadBoard, [true], "board may have changed even on failure");
    assert.equal(h.calls.loadProjects, 0);
    assert.equal(h.calls.refreshDetail.length, 0);
  } finally { unCreate(); }
});
