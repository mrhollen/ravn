// Unit tests for src/modules/task-edit.ts — the task edit slice extracted from
// web/src/app.ts (Phase 2 batch B). Covers: startEditing seed + snapshot + saved
// draft restore; cancel/discard guards (editDraftDirty gating, confirmed-discard
// dropping the stored draft); the description persistence pair; and saveTaskEdit
// payload assembly (due/assignee set-or-clear), success fan-out, the 409
// version-conflict retry (applyDetail hook + preserved draft) and its failure
// branches.
//
// The module is pure: a mutable stub state object wired into the TaskEditDeps
// accessors + counters for every out-bound write + monkey-patched api methods,
// all restored in finally blocks (same pattern as opencode-module.test.ts).

import { describe, test, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { createTaskEditModule } from "../src/modules/task-edit";
import type { TaskEditDraftState } from "../src/modules/task-edit";
import { api, ApiError } from "../src/api";
import { descriptionDraftKey } from "../src/drafts";
import { dateInputToIso, dueCalendarDate, safeStorage } from "../src/util";
import type { TaskDetail, UpdateTaskReq } from "../src/types";

// ----------------------------------------------------------------------- helpers
function blankDraft(): TaskEditDraftState {
  return {
    title: "",
    description: "",
    priority: 0,
    due: "",
    assigneeName: "",
    assigneeType: "human",
  };
}

function makeTask(over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: "T1",
    project_id: "P1",
    seq: 1,
    title: "Server truth title",
    description: "server desc",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 7,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    completed_by: null,
    completed_at: null,
    assignee: undefined,
    ...over,
  };
}

/** 409 that passes ApiError.isVersionConflict (needs numeric current_version). */
function versionConflict(): ApiError {
  return new ApiError(409, { error: "version_conflict", current_version: 9 } as never);
}

interface Ctx {
  state: {
    detail: TaskDetail | null;
    editDraft: TaskEditDraftState;
    editDraftOriginal: TaskEditDraftState;
    editingTask: boolean;
    savingEdit: boolean;
    descShowPreview: boolean;
    // quick-metadata popovers + assignee inputs (flat root fields)
    priorityPopoverOpen: boolean;
    duePopoverOpen: boolean;
    dueInputVal: string;
    priorityBusy: boolean;
    dueBusy: boolean;
    assigneeInputName: string;
    assigneeInputType: "human" | "agent";
    assigneeBusy: boolean;
    // operational-bar flags written through the module's mutex setters
    statusMenuOpen: boolean;
    ownerPopoverOpen: boolean;
  };
  counters: Record<string, number>;
  toasts: { msg: string; kind?: string }[];
  flags: { confirm: boolean };
  applyDetails: TaskDetail[];
  mod: ReturnType<typeof createTaskEditModule>;
}

function makeCtx(task: TaskDetail | null = makeTask()): Ctx {
  const state = {
    detail: task,
    editDraft: blankDraft(),
    editDraftOriginal: blankDraft(),
    editingTask: false,
    savingEdit: false,
    descShowPreview: true, // stale — startEditing must reset it
    priorityPopoverOpen: false,
    duePopoverOpen: false,
    dueInputVal: "",
    priorityBusy: false,
    dueBusy: false,
    assigneeInputName: "",
    assigneeInputType: "human",
    assigneeBusy: false,
    statusMenuOpen: false,
    ownerPopoverOpen: false,
  };
  const counters: Record<string, number> = {
    setEditingTask: 0,
    setSavingEdit: 0,
    setDescShowPreview: 0,
    setEditDraft: 0,
    setEditDraftOriginal: 0,
    confirmDiscard: 0,
    refreshDetail: 0,
    loadBoard: 0,
  };
  const toasts: { msg: string; kind?: string }[] = [];
  const applyDetails: TaskDetail[] = [];
  const flags = { confirm: true };

  const mod = createTaskEditModule({
    // state-in
    detail: () => state.detail,
    editDraft: () => state.editDraft,
    editDraftOriginal: () => state.editDraftOriginal,
    editingTask: () => state.editingTask,
    savingEdit: () => state.savingEdit,
    // state-out
    setEditingTask: (v) => { state.editingTask = v; counters.setEditingTask++; },
    setSavingEdit: (v) => { state.savingEdit = v; counters.setSavingEdit++; },
    setDescShowPreview: (v) => { state.descShowPreview = v; counters.setDescShowPreview++; },
    setEditDraft: (d) => { state.editDraft = d; counters.setEditDraft++; },
    setEditDraftOriginal: (d) => { state.editDraftOriginal = d; counters.setEditDraftOriginal++; },
    // state-in — quick-metadata popovers + assignee inputs
    priorityPopoverOpen: () => state.priorityPopoverOpen,
    duePopoverOpen: () => state.duePopoverOpen,
    dueInputVal: () => state.dueInputVal,
    priorityBusy: () => state.priorityBusy,
    dueBusy: () => state.dueBusy,
    assigneeInputName: () => state.assigneeInputName,
    assigneeInputType: () => state.assigneeInputType,
    assigneeBusy: () => state.assigneeBusy,
    // state-out — popovers/inputs + the operational-bar flags written via mutex
    setPriorityPopoverOpen: (v) => { state.priorityPopoverOpen = v; },
    setDuePopoverOpen: (v) => { state.duePopoverOpen = v; },
    setDueInputVal: (v) => { state.dueInputVal = v; },
    setPriorityBusy: (v) => { state.priorityBusy = v; },
    setDueBusy: (v) => { state.dueBusy = v; },
    setOwnerPopoverOpen: (v) => { state.ownerPopoverOpen = v; },
    setStatusMenuOpen: (v) => { state.statusMenuOpen = v; },
    setAssigneeInputName: (v) => { state.assigneeInputName = v; },
    setAssigneeInputType: (v) => { state.assigneeInputType = v; },
    setAssigneeBusy: (v) => { state.assigneeBusy = v; },
    // hooks — root-owned orchestration kept outside the module
    toast: (msg, kind) => { toasts.push({ msg, kind }); },
    actor: () => ({ name: "tester", type: "agent" }),
    confirmDiscard: () => { counters.confirmDiscard++; return flags.confirm; },
    refreshDetail: async () => { counters.refreshDetail++; },
    loadBoard: async () => { counters.loadBoard++; },
    applyDetail: (t) => { applyDetails.push(t); state.detail = t; },
  });

  return { state, counters, toasts, flags, applyDetails, mod };
}

type ApiRec = Record<string, unknown>;
const apiRec = () => api as unknown as ApiRec;

beforeEach(() => {
  for (const id of ["T1", "T2", "T3"]) safeStorage.removeItem(descriptionDraftKey(id));
});

// --------------------------------------------------------------------- startEditing
describe("task-edit module: startEditing seeds draft + snapshot", () => {
  test("no detail → no-op (nothing touched)", () => {
    const c = makeCtx(null);
    c.mod.startEditing();
    assert.equal(c.counters.setEditDraft, 0);
    assert.equal(c.counters.setEditDraftOriginal, 0);
    assert.equal(c.state.editingTask, false);
  });

  test("seeds all six fields from the task; calendar-day due round-trips", () => {
    const c = makeCtx(makeTask({ due_at: "2026-12-01T05:30:00Z" }));
    c.mod.startEditing();
    assert.equal(c.state.editDraft.title, "Server truth title");
    assert.equal(c.state.editDraft.description, "server desc");
    assert.equal(c.state.editDraft.priority, 2);
    assert.equal(c.state.editDraft.due, dueCalendarDate("2026-12-01T05:30:00Z"));
    assert.equal(c.state.editDraft.assigneeName, "");
    assert.equal(c.state.editDraft.assigneeType, "human");
    assert.equal(c.counters.setEditDraft, 1);
    assert.equal(c.counters.setEditDraftOriginal, 1);
  });

  test("assignee name + type copied; null-ish fields defaulted", () => {
    const c = makeCtx(makeTask({
      title: "",
      description: null,
      priority: null,
      assignee: { name: "bob", type: "agent" },
    }));
    c.mod.startEditing();
    assert.equal(c.state.editDraft.title, "");
    assert.equal(c.state.editDraft.description, "");
    assert.equal(c.state.editDraft.priority, 0);
    assert.equal(c.state.editDraft.assigneeName, "bob");
    assert.equal(c.state.editDraft.assigneeType, "agent");
  });

  test("snapshot is a COPY of the draft (not the same reference)", () => {
    const c = makeCtx();
    c.mod.startEditing();
    const snapTitle = c.state.editDraftOriginal.title;
    c.state.editDraft.title = "mutated"; // simulate user typing
    assert.notEqual(c.state.editDraftOriginal, c.state.editDraft);
    assert.equal(c.state.editDraftOriginal.title, snapTitle);
  });

  test("stored description draft wins over server value; snapshot keeps server truth", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "local WIP");
    const c = makeCtx(makeTask({ description: "server desc" }));
    c.mod.startEditing();
    assert.equal(c.state.editDraft.description, "local WIP");
    assert.equal(c.state.editDraftOriginal.description, "server desc");
  });

  test("blank stored draft is NOT restored", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "   ");
    const c = makeCtx();
    c.mod.startEditing();
    assert.equal(c.state.editDraft.description, "server desc");
  });

  test("fresh session forces Write mode (stale preview reset) and enters edit mode", () => {
    const c = makeCtx();
    assert.equal(c.state.descShowPreview, true, "precondition: stale preview on");
    c.mod.startEditing();
    assert.equal(c.state.descShowPreview, false);
    assert.equal(c.state.editingTask, true);
  });
});

// ---------------------------------------------------------------- cancel / guards
describe("task-edit module: cancel + discard guards", () => {
  test("cancelEditing exits edit mode and drops the stored draft for the loaded task", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "wip");
    const c = makeCtx();
    c.mod.cancelEditing();
    assert.equal(c.state.editingTask, false);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
  });

  test("cancelEditing with no task loaded: exits, storage untouched", () => {
    safeStorage.setItem(descriptionDraftKey("T2"), "other");
    const c = makeCtx(null);
    c.mod.cancelEditing();
    assert.equal(c.state.editingTask, false);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T2")), "other");
  });

  test("editDraftDirty is gated on editingTask (stale snapshot never fires)", () => {
    const c = makeCtx();
    c.mod.startEditing();
    assert.equal(c.mod.editDraftDirty(), false, "fresh draft matches snapshot");
    c.state.editDraft = { ...c.state.editDraft, title: "changed" };
    assert.equal(c.mod.editDraftDirty(), true);
    // exit edit mode without touching the draft → stale diff must NOT fire
    c.state.editingTask = false;
    assert.equal(c.mod.editDraftDirty(), false);
  });

  test("each dirty field is detected", () => {
    const cases: Array<[keyof TaskEditDraftState, unknown]> = [
      ["title", "x"], ["description", "y"], ["priority", 5],
      ["due", "2027-01-01"], ["assigneeName", "n"], ["assigneeType", "agent"],
    ];
    for (const [k, v] of cases) {
      const c = makeCtx();
      c.mod.startEditing();
      (c.state.editDraft as unknown as Record<string, unknown>)[k as string] = v;
      assert.equal(c.mod.editDraftDirty(), true, `${k} change detected`);
    }
  });

  test("discardEditDraftIfConfirmed: clean draft → true without asking", () => {
    const c = makeCtx();
    c.mod.startEditing();
    assert.equal(c.mod.discardEditDraftIfConfirmed(), true);
    assert.equal(c.counters.confirmDiscard, 0);
  });

  test("dirty + confirmed → clears the stored draft and returns true", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "wip");
    const c = makeCtx();
    c.mod.startEditing();
    c.state.editDraft = { ...c.state.editDraft, title: "x" };
    c.flags.confirm = true;
    assert.equal(c.mod.discardEditDraftIfConfirmed(), true);
    assert.equal(c.counters.confirmDiscard, 1);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
  });

  test("dirty + declined → false, draft preserved", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "wip");
    const c = makeCtx();
    c.mod.startEditing();
    c.state.editDraft = { ...c.state.editDraft, title: "x" };
    c.flags.confirm = false;
    assert.equal(c.mod.discardEditDraftIfConfirmed(), false);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), "wip");
  });
});

// ------------------------------------------------------- description draft storage
describe("task-edit module: saveDescriptionDraft / clearDescriptionDraft", () => {
  test("no loaded task → no-op (never writes)", () => {
    const c = makeCtx(null);
    c.state.editDraft.description = "orphan";
    c.mod.saveDescriptionDraft();
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
  });

  test("non-blank saves; blank REMOVES the key", () => {
    const c = makeCtx();
    c.state.editDraft.description = "wip text";
    c.mod.saveDescriptionDraft();
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), "wip text");
    c.state.editDraft.description = "   ";
    c.mod.saveDescriptionDraft();
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
  });

  test("clearDescriptionDraft removes; idempotent; no task → safe no-op", () => {
    safeStorage.setItem(descriptionDraftKey("T1"), "x");
    const c = makeCtx();
    c.mod.clearDescriptionDraft();
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
    c.mod.clearDescriptionDraft(); // idempotent
    const c2 = makeCtx(null);
    c2.mod.clearDescriptionDraft();
  });

  test("undefined description treated as blank", () => {
    const c = makeCtx();
    c.state.editDraft.description = undefined as unknown as string;
    c.mod.saveDescriptionDraft();
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
  });
});

// ---------------------------------------------------------------------- saveTaskEdit
describe("task-edit module: saveTaskEdit", () => {
  test("guard: no detail → early return, no API call", async () => {
    const c = makeCtx(null);
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
    assert.equal(c.state.savingEdit, false);
  });

  test("guard: blank title → early return", async () => {
    const c = makeCtx();
    c.state.editingTask = true;
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
  });

  test("guard: savingEdit already true → early return", async () => {
    const c = makeCtx();
    c.state.editingTask = true;
    c.state.savingEdit = true;
    c.state.editDraft.title = "x";
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
  });

  test("success: payload assembly + fan-out + draft cleared + exits edit mode", async () => {
    const t = makeTask({ due_at: null, assignee: undefined });
    safeStorage.setItem(descriptionDraftKey("T1"), "wip");
    const c = makeCtx(t);
    c.state.editingTask = true;
    c.state.editDraft = {
      title: "  New title  ", description: " new desc ", priority: 4,
      due: "", assigneeName: "", assigneeType: "human",
    };

    const orig = apiRec().updateTask;
    const seen: UpdateTaskReq[] = [];
    apiRec().updateTask = async (_id: string, req: UpdateTaskReq) => { seen.push(req); return {}; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.equal(seen.length, 1);
    const req = seen[0];
    assert.equal(req.version, 7);
    assert.equal(req.title, "New title"); // trimmed
    assert.equal(req.description, "new desc"); // trimmed
    assert.equal(req.priority, 4);
    assert.deepEqual(req.actor, { name: "tester", type: "agent" });
    // no due on task and draft blank → neither due_at nor clear_due
    assert.equal(req.due_at, undefined);
    assert.equal(req.clear_due, undefined);
    // no assignee on task and draft blank → nothing
    assert.equal(req.assignee, undefined);
    assert.equal(req.clear_assignee, undefined);

    assert.equal(c.state.editingTask, false);
    assert.equal(c.counters.refreshDetail, 1);
    assert.equal(c.counters.loadBoard, 1);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null, "draft dropped after save");
    assert.equal(c.toasts.length, 0);
  });

  test("due: draft date set → due_at via the shared helper round-trip", async () => {
    const c = makeCtx(makeTask({ due_at: null }));
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "", priority: 1, due: "2027-03-15", assigneeName: "", assigneeType: "human" };

    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => { req = r; return {}; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.equal(req.due_at, dateInputToIso("2027-03-15"));
    assert.equal(req.clear_due, undefined);
  });

  test("due: draft blank but task HAD a due → clear_due", async () => {
    const c = makeCtx(makeTask({ due_at: "2026-06-01T00:00:00Z" }));
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "", priority: 0, due: "", assigneeName: "", assigneeType: "human" };

    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => { req = r; return {}; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.equal(req.due_at, undefined);
    assert.equal(req.clear_due, true);
  });

  test("assignee: draft name set → assignee object (trimmed, type fallback)", async () => {
    const c = makeCtx(makeTask({ assignee: undefined }));
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "", priority: 0, due: "", assigneeName: " bob ", assigneeType: "" as unknown as "human" };

    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => { req = r; return {}; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.deepEqual(req.assignee, { name: "bob", type: "human" });
    assert.equal(req.clear_assignee, undefined);
  });

  test("assignee: draft blank but task HAD assignee → clear_assignee", async () => {
    const c = makeCtx(makeTask({ assignee: { name: "old", type: "human" } }));
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "", priority: 0, due: "", assigneeName: "", assigneeType: "human" };

    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => { req = r; return {}; };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.equal(req.assignee, undefined);
    assert.equal(req.clear_assignee, true);
  });

  test("non-conflict API error → error toast, stays in edit mode, savingEdit reset", async () => {
    const c = makeCtx();
    c.state.editingTask = true;
    c.state.editDraft.title = "t";

    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => { throw new ApiError(500, { error: "boom", message: "nope" }); };
    try { await c.mod.saveTaskEdit(); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.editingTask, true, "stays in edit mode");
    assert.equal(c.state.savingEdit, false, "finally resets the saving flag");
    assert.equal(c.counters.refreshDetail, 0, "no refresh on plain error");
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
    assert.ok(c.toasts[0].msg.length > 0);
  });

  test("409 → getTask fresh → applyDetail hook → retry with fresh version; success", async () => {
    const t = makeTask({ version: 7, due_at: null, assignee: undefined });
    safeStorage.setItem(descriptionDraftKey("T1"), "wip");
    const c = makeCtx(t);
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "d", priority: 1, due: "", assigneeName: "", assigneeType: "human" };

    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    let uCalls: UpdateTaskReq[] = [];
    let gCalled = 0;
    apiRec().getTask = async () => { gCalled++; return makeTask({ version: 9 }); };
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      uCalls.push(r);
      if (r.version === 7) throw versionConflict();
      return {};
    };
    try { await c.mod.saveTaskEdit(); } finally {
      apiRec().updateTask = origU;
      apiRec().getTask = origG;
    }

    assert.equal(gCalled, 1);
    assert.equal(c.applyDetails.length, 1, "applyDetail hook fired once");
    assert.equal(c.applyDetails[0].version, 9);
    assert.equal(uCalls.length, 2);
    assert.equal(uCalls[1].version, 9, "retry used the fresh version");
    assert.equal(c.state.editingTask, false);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null, "draft cleared exactly once");
    assert.equal(c.counters.refreshDetail, 1);
    assert.equal(c.toasts.length, 1);
    assert.match(c.toasts[0].msg, /retried with the fresh version/);
    assert.equal(c.toasts[0].kind, "info");
  });

  test("409 → retry ALSO conflicts → refresh + info toast, STAYS in edit mode, draft preserved", async () => {
    const t = makeTask({ version: 7 });
    safeStorage.setItem(descriptionDraftKey("T1"), "wip-kept");
    const c = makeCtx(t);
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "d", priority: 1, due: "", assigneeName: "", assigneeType: "human" };

    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    apiRec().updateTask = async () => { throw versionConflict(); };
    try { await c.mod.saveTaskEdit(); } finally {
      apiRec().updateTask = origU;
      apiRec().getTask = origG;
    }

    assert.equal(c.state.editingTask, true, "stays in edit mode with draft preserved");
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), "wip-kept", "draft NOT dropped");
    assert.equal(c.counters.refreshDetail, 1, "the catch-wrapped refresh on the e2 branch");
    assert.equal(c.toasts.length, 1);
    assert.match(c.toasts[0].msg, /refreshed \(your edit was not applied\)/);
    assert.equal(c.toasts[0].kind, "info");
  });

  test("409 → retry fails with NON-conflict error → error toast, draft preserved", async () => {
    const t = makeTask({ version: 7 });
    safeStorage.setItem(descriptionDraftKey("T1"), "wip-kept2");
    const c = makeCtx(t);
    c.state.editingTask = true;
    c.state.editDraft = { title: "t", description: "", priority: 0, due: "", assigneeName: "", assigneeType: "human" };

    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      if (r.version === 7) throw versionConflict();
      throw new ApiError(500, { error: "boom", message: "still broken" });
    };
    try { await c.mod.saveTaskEdit(); } finally {
      apiRec().updateTask = origU;
      apiRec().getTask = origG;
    }

    assert.equal(c.state.editingTask, true);
    assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), "wip-kept2");
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
  });
});

// ------------------------------------------------- quick-metadata popovers (priority & due)
describe("task-edit module: quick-metadata popover toggles", () => {
  test("togglePriorityPopover opens and closes the other three popovers (mutex)", () => {
    const c = makeCtx();
    c.state.duePopoverOpen = true;
    c.state.statusMenuOpen = true;
    c.state.ownerPopoverOpen = true;
    c.mod.togglePriorityPopover();
    assert.equal(c.state.priorityPopoverOpen, true);
    assert.equal(c.state.duePopoverOpen, false);
    assert.equal(c.state.statusMenuOpen, false);
    assert.equal(c.state.ownerPopoverOpen, false);
  });

  test("togglePriorityPopover toggles closed when open", () => {
    const c = makeCtx();
    c.state.priorityPopoverOpen = true;
    c.mod.togglePriorityPopover();
    assert.equal(c.state.priorityPopoverOpen, false);
  });

  test("toggleDuePopover seeds dueInputVal from the task's calendar day", () => {
    const c = makeCtx(makeTask({ due_at: "2026-12-01T05:30:00Z" }));
    c.state.dueInputVal = "stale";
    c.mod.toggleDuePopover();
    assert.equal(c.state.duePopoverOpen, true);
    assert.equal(c.state.dueInputVal, dueCalendarDate("2026-12-01T05:30:00Z"));
  });

  test("toggleDuePopover with no due → empty input; other popovers closed", () => {
    const c = makeCtx(makeTask({ due_at: null }));
    c.state.priorityPopoverOpen = true;
    c.state.ownerPopoverOpen = true;
    c.mod.toggleDuePopover();
    assert.equal(c.state.dueInputVal, "");
    assert.equal(c.state.priorityPopoverOpen, false);
    assert.equal(c.state.ownerPopoverOpen, false);
  });

  test("toggleDuePopover with NO task loaded does not touch the input", () => {
    const c = makeCtx(null);
    c.state.dueInputVal = "stale";
    c.mod.toggleDuePopover();
    assert.equal(c.state.duePopoverOpen, true);
    assert.equal(c.state.dueInputVal, "stale");
  });
});

describe("task-edit module: updatePriority", () => {
  test("guard: no detail → popover closes, no API call", async () => {
    const c = makeCtx(null);
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.updatePriority(3); } finally { apiRec().updateTask = orig; }
    assert.equal(c.state.priorityPopoverOpen, false, "popover closed even on the guard path");
    assert.equal(called, 0);
    assert.equal(c.state.priorityBusy, false);
  });

  test("guard: same priority → no API call", async () => {
    const c = makeCtx(makeTask({ priority: 2 }));
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.updatePriority(2); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
  });

  test("guard: already busy → no API call", async () => {
    const c = makeCtx();
    c.state.priorityBusy = true;
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.updatePriority(4); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
  });

  test("success: patches the open task's row fields in place + fan-out + toast", async () => {
    const t = makeTask({ priority: 2, version: 7 });
    const c = makeCtx(t);
    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      req = r;
      return { priority: 5, version: 8, updated_at: "2026-11-12T00:00:00Z" };
    };
    try { await c.mod.updatePriority(5); } finally { apiRec().updateTask = orig; }

    assert.equal(req.version, 7);
    assert.equal(req.priority, 5);
    // in-place patch (same object reference)
    assert.equal(c.state.detail, t, "detail is the same object");
    assert.equal(t.priority, 5);
    assert.equal(t.version, 8);
    assert.equal(t.updated_at, "2026-11-12T00:00:00Z");
    assert.equal(c.counters.refreshDetail, 1);
    assert.equal(c.counters.loadBoard, 1);
    assert.deepEqual(c.toasts, [{ msg: "Priority set to P5", kind: undefined }]);
  });

  test("success with a SWITCHED task during flight → no in-place patch", async () => {
    const t = makeTask({ priority: 2, version: 7 });
    const c = makeCtx(t);
    const other = makeTask({ id: "OTHER", priority: 1, version: 3 });
    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => {
      c.state.detail = other; // simulate drawer switching mid-flight
      return { priority: 5, version: 8, updated_at: "x" };
    };
    try { await c.mod.updatePriority(5); } finally { apiRec().updateTask = orig; }

    assert.equal(other.priority, 1, "the other task was NOT patched");
    assert.equal(c.counters.refreshDetail, 1, "fan-out still happened");
  });

  test("409 → re-read (no options arg) + applyDetail + retry once with fresh version", async () => {
    const c = makeCtx(makeTask({ priority: 2, version: 7 }));
    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    const getTaskArgs: unknown[][] = [];
    apiRec().getTask = async (...a: unknown[]) => { getTaskArgs.push(a); return makeTask({ priority: 2, version: 9 }); };
    let uCalls: UpdateTaskReq[] = [];
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      uCalls.push(r);
      if (r.version === 7) throw versionConflict();
      return { priority: 5, version: 10, updated_at: "x" };
    };
    try { await c.mod.updatePriority(5); } finally { apiRec().updateTask = origU; apiRec().getTask = origG; }

    assert.equal(getTaskArgs.length, 1);
    assert.equal(getTaskArgs[0].length, 1, "re-read passes NO options (parity with the original)");
    assert.equal(c.applyDetails.length, 1);
    assert.equal(uCalls.length, 2);
    assert.equal(uCalls[1].version, 9);
    assert.equal(uCalls[1].priority, 5);
    assert.deepEqual(c.toasts, [{ msg: "Priority set to P5", kind: undefined }], "success toast exactly once");
  });

  test("409 → retry ALSO fails → error toast (no double toast on the success path)", async () => {
    const c = makeCtx(makeTask({ priority: 2, version: 7 }));
    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      if (r.version === 7) throw versionConflict();
      throw new ApiError(500, { error: "boom", message: "nope" });
    };
    try { await c.mod.updatePriority(5); } finally { apiRec().updateTask = origU; apiRec().getTask = origG; }

    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.priorityBusy, false, "finally resets the busy flag");
  });

  test("non-conflict error → error toast, busy reset", async () => {
    const c = makeCtx();
    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => { throw new ApiError(403, { error: "forbidden" }); };
    try { await c.mod.updatePriority(1); } finally { apiRec().updateTask = orig; }
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.priorityBusy, false);
  });
});

describe("task-edit module: due date save/clear", () => {
  test("saveDueDate closes the popover and converts the input via the shared helper", async () => {
    const c = makeCtx();
    c.state.duePopoverOpen = true;
    c.state.dueInputVal = "2027-03-15";
    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      req = r;
      return { due_at: dateInputToIso("2027-03-15"), version: 8, updated_at: "x" };
    };
    try { await c.mod.saveDueDate(); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.duePopoverOpen, false);
    assert.equal(req.due_at, dateInputToIso("2027-03-15"));
    assert.equal(req.clear_due, undefined);
    assert.equal(c.toasts[0].msg, "Due date updated");
  });

  test("clearDueDate blanks the input and sends clear_due", async () => {
    const c = makeCtx(makeTask({ due_at: "2026-06-01T00:00:00Z" }));
    c.state.dueInputVal = "2026-06-01";
    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      req = r;
      return { due_at: null, version: 8, updated_at: "x" };
    };
    try { await c.mod.clearDueDate(); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.duePopoverOpen, false);
    assert.equal(c.state.dueInputVal, "");
    assert.equal(req.due_at, undefined);
    assert.equal(req.clear_due, true);
    assert.equal(c.toasts[0].msg, "Due date cleared");
  });

  test("updateDueDate success syncs dueInputVal to the stored calendar day", async () => {
    const c = makeCtx();
    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => ({ due_at: "2027-04-01T09:00:00Z", version: 8, updated_at: "x" });
    try { await c.mod.updateDueDate("2027-04-01T09:00:00Z"); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.dueInputVal, dueCalendarDate("2027-04-01T09:00:00Z"));
  });

  test("updateDueDate guard: no detail or busy → no API call", async () => {
    const c = makeCtx(null);
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.updateDueDate("2027-04-01"); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);

    const c2 = makeCtx();
    c2.state.dueBusy = true;
    try { await c2.mod.updateDueDate("2027-04-01"); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
    assert.equal(c2.state.dueBusy, true, "busy flag untouched on the guard path");
  });

  test("409 → retry carries the SAME intent (clear variant: due_at undefined + clear_due)", async () => {
    const c = makeCtx(makeTask({ due_at: "2026-06-01T00:00:00Z", version: 7 }));
    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    let uCalls: UpdateTaskReq[] = [];
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      uCalls.push(r);
      if (r.version === 7) throw versionConflict();
      return { due_at: null, version: 9, updated_at: "x" };
    };
    try { await c.mod.updateDueDate(null); } finally { apiRec().updateTask = origU; apiRec().getTask = origG; }

    assert.equal(uCalls.length, 2);
    assert.equal(uCalls[0].clear_due, true);
    assert.equal(uCalls[1].due_at, undefined, "retry keeps the clear intent");
    assert.equal(uCalls[1].clear_due, true);
    assert.equal(c.applyDetails.length, 1);
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].msg, "Due date cleared");
  });
});

// -------------------------------------------------------------------- assignee flows
describe("task-edit module: assignee save/clear/update", () => {
  test("saveAssignee closes the owner popover and trims the name", async () => {
    const c = makeCtx();
    c.state.ownerPopoverOpen = true;
    c.state.assigneeInputName = "  bob  ";
    c.state.assigneeInputType = "agent";
    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      req = r;
      return { assignee: { name: "bob", type: "agent" }, version: 8, updated_at: "x" };
    };
    try { await c.mod.saveAssignee(); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.ownerPopoverOpen, false);
    assert.deepEqual(req.assignee, { name: "bob", type: "agent" });
    assert.equal(req.clear_assignee, undefined);
    assert.equal(c.toasts[0].msg, "Assigned to bob");
  });

  test("clearAssignee blanks the input and sends clear_assignee", async () => {
    const c = makeCtx(makeTask({ assignee: { name: "old", type: "human" } }));
    c.state.assigneeInputName = "old";
    const orig = apiRec().updateTask;
    let req!: UpdateTaskReq;
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      req = r;
      return { assignee: null, version: 8, updated_at: "x" };
    };
    try { await c.mod.clearAssignee(); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.ownerPopoverOpen, false);
    assert.equal(c.state.assigneeInputName, "");
    assert.equal(req.assignee, undefined);
    assert.equal(req.clear_assignee, true);
    assert.equal(c.toasts[0].msg, "Assignee cleared");
  });

  test("updateAssignee success patches the row + syncs both input fields from the response", async () => {
    const c = makeCtx();
    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => ({ assignee: { name: "zed", type: "agent" }, version: 8, updated_at: "x" });
    try { await c.mod.updateAssignee("zed", "agent"); } finally { apiRec().updateTask = orig; }

    assert.deepEqual(c.state.detail?.assignee, { name: "zed", type: "agent" });
    assert.equal(c.state.detail?.version, 8);
    assert.equal(c.state.assigneeInputName, "zed");
    assert.equal(c.state.assigneeInputType, "agent");
    assert.equal(c.state.assigneeBusy, false);
  });

  test("updateAssignee with a null response assignee → inputs reset to empty/human", async () => {
    const c = makeCtx();
    const orig = apiRec().updateTask;
    apiRec().updateTask = async () => ({ assignee: null, version: 8, updated_at: "x" });
    try { await c.mod.updateAssignee("", "human"); } finally { apiRec().updateTask = orig; }

    assert.equal(c.state.assigneeInputName, "");
    assert.equal(c.state.assigneeInputType, "human");
  });

  test("guard: no detail or busy → no API call", async () => {
    const c = makeCtx(null);
    const orig = apiRec().updateTask;
    let called = 0;
    apiRec().updateTask = async () => { called++; };
    try { await c.mod.updateAssignee("a", "human"); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);

    const c2 = makeCtx();
    c2.state.assigneeBusy = true;
    try { await c2.mod.updateAssignee("a", "human"); } finally { apiRec().updateTask = orig; }
    assert.equal(called, 0);
  });

  test("409 → re-read + applyDetail + retry with the SAME intent (set variant)", async () => {
    const c = makeCtx(makeTask({ version: 7 }));
    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    let uCalls: UpdateTaskReq[] = [];
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      uCalls.push(r);
      if (r.version === 7) throw versionConflict();
      return { assignee: { name: "bob", type: "human" }, version: 9, updated_at: "x" };
    };
    try { await c.mod.updateAssignee("bob", "human"); } finally { apiRec().updateTask = origU; apiRec().getTask = origG; }

    assert.equal(uCalls.length, 2);
    assert.deepEqual(uCalls[1].assignee, { name: "bob", type: "human" });
    // parity quirk (original builds `clear_assignee: !trimmed` unconditionally):
    // the SET variant carries an explicit false, not a missing key.
    assert.equal(uCalls[1].clear_assignee, false);
    assert.equal(c.applyDetails.length, 1);
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].msg, "Assigned to bob");
  });

  test("409 → retry fails → error toast, busy reset", async () => {
    const c = makeCtx(makeTask({ version: 7 }));
    const origU = apiRec().updateTask;
    const origG = apiRec().getTask;
    apiRec().getTask = async () => makeTask({ version: 8 });
    apiRec().updateTask = async (_id: string, r: UpdateTaskReq) => {
      if (r.version === 7) throw versionConflict();
      throw new ApiError(500, { error: "boom" });
    };
    try { await c.mod.updateAssignee("bob", "human"); } finally { apiRec().updateTask = origU; apiRec().getTask = origG; }

    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.assigneeBusy, false);
  });
});
