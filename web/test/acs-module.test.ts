// Unit tests for src/modules/acs.ts — the acceptance-criteria drawer section
// extracted from web/src/app.ts (Phase 2 batch C). Covers: toggleAc success
// patch + failed-toggle checkbox revert; addAc/handleAcPaste multi-line fan-out
// with parseChecklistText stripping; moveAc/dropAc reorder guards, seq payloads
// and response replacement; the inline edit session (start/cancel/save); delete
// with editing-session cleanup; and the four display helpers over ac-math.
//
// The module is pure: a mutable stub state object wired into the AcsDeps
// accessors + counters for every out-bound write + monkey-patched api methods,
// all restored in finally blocks (same pattern as task-edit-module.test.ts).

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createAcsModule } from "../src/modules/acs";
import { api } from "../src/api";
import { acDoneCount, acProgressLabel, acRingInner, completedLabel } from "../src/ac-math";
import type { AcRow, ActorInput, TaskDetail } from "../src/types";

// ----------------------------------------------------------------------- helpers
function makeAc(seq: number, text = `ac ${seq}`, done = false): AcRow {
  return { seq, text, done };
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
    acceptance_criteria: [makeAc(1, "one"), makeAc(2, "two", true)],
    ...over,
  };
}

const ACTOR: ActorInput = { name: "tester", type: "agent" };

interface Ctx {
  mod: ReturnType<typeof createAcsModule>;
  state: Record<string, unknown>;
  detailWrites: number[];
  toasts: Array<{ msg?: string; kind?: string }>;
}

function makeCtx(detail: TaskDetail | null): Ctx {
  const state: Record<string, unknown> = {
    detail,
    acBusySeq: 0,
    newAcText: "",
    addingAc: false,
    editingAcSeq: 0,
    editingAcText: "",
    savingAcSeq: 0,
    deletingAcSeq: 0,
    reorderingAcs: false,
    draggedAcSeq: 0,
  };
  const ctx: Ctx = { mod: undefined as never, state, detailWrites: [], toasts: [] };

  ctx.mod = createAcsModule({
    // state-in
    detail: () => state.detail as TaskDetail | null,
    acBusySeq: () => state.acBusySeq as number,
    newAcText: () => state.newAcText as string,
    addingAc: () => state.addingAc as boolean,
    editingAcSeq: () => state.editingAcSeq as number,
    editingAcText: () => state.editingAcText as string,
    savingAcSeq: () => state.savingAcSeq as number,
    deletingAcSeq: () => state.deletingAcSeq as number,
    reorderingAcs: () => state.reorderingAcs as boolean,
    draggedAcSeq: () => state.draggedAcSeq as number,
    // state-out
    setDetail: (t) => { state.detail = t; ctx.detailWrites.push(ctx.detailWrites.length + 1); },
    setAcBusySeq: (v) => { state.acBusySeq = v; },
    setNewAcText: (v) => { state.newAcText = v; },
    setAddingAc: (v) => { state.addingAc = v; },
    setEditingAcSeq: (v) => { state.editingAcSeq = v; },
    setEditingAcText: (v) => { state.editingAcText = v; },
    setSavingAcSeq: (v) => { state.savingAcSeq = v; },
    setDeletingAcSeq: (v) => { state.deletingAcSeq = v; },
    setReorderingAcs: (v) => { state.reorderingAcs = v; },
    setDraggedAcSeq: (v) => { state.draggedAcSeq = v; },
    // hooks
    toast: (msg, kind) => { ctx.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
  });
  return ctx;
}

/** Restore the given api methods after the calling test body runs. */
function withApiRestored(
  name: "toggleAc" | "addAc" | "reorderAcs" | "editAcText" | "deleteAc",
  patch: (current: unknown) => unknown,
): { run: <T>(fn: () => Promise<T> | T) => Promise<T>; key: string } {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = patch(orig);
  return {
    key: name,
    async run(fn) {
      try {
        return await fn();
      } finally {
        (api as Record<string, unknown>)[name] = orig;
      }
    },
  };
}

// ----------------------------------------------------------------------- toggleAc
describe("toggleAc", () => {
  test("guard: no detail → no api call, busy untouched", async () => {
    const c = makeCtx(null);
    const rec: unknown[] = [];
    const w = withApiRestored("toggleAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(1); });
    await w.run(() => c.mod.toggleAc(makeAc(1)));
    assert.equal(rec.length, 0);
    assert.equal(c.state.acBusySeq, 0);
  });

  test("guard: another toggle in flight → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.acBusySeq = 2;
    const rec: unknown[] = [];
    const w = withApiRestored("toggleAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(1); });
    await w.run(() => c.mod.toggleAc(makeAc(1)));
    assert.equal(rec.length, 0);
    assert.equal(c.state.acBusySeq, 2, "busy flag left alone on the guard path");
  });

  test("success → server row merged into detail by seq; busy resets", async () => {
    const c = makeCtx(makeTask());
    const seen: unknown[] = [];
    const w = withApiRestored("toggleAc", () => async (...a: unknown[]) => {
      seen.push(a);
      return { seq: 2, text: "two", done: false };
    });
    await w.run(() => c.mod.toggleAc(makeAc(2, "two", true)));

    assert.equal(seen.length, 1);
    assert.deepEqual(seen[0], ["T1", 2, ACTOR, false]);
    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria[1].done, false);
    assert.equal(d.acceptance_criteria[0].text, "one");
    assert.equal(c.state.acBusySeq, 0);
    assert.equal(c.toasts.length, 0);
  });

  test("detail switched to another task mid-flight → no patch", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("toggleAc", () => async () => {
      // Simulate a refresh that swapped the open task for another one.
      c.state.detail = { ...makeTask(), id: "T2" };
      return makeAc(1, "one", true);
    });
    await w.run(() => c.mod.toggleAc(makeAc(1)));
    assert.equal(c.toasts.length, 0);
    assert.equal(c.state.acBusySeq, 0);
    const d = c.state.detail as TaskDetail;
    assert.equal(d.id, "T2");
    assert.deepEqual(d.acceptance_criteria[0], makeAc(1, "one"), "stale response not applied to another task");
  });

  test("failure → checkbox reverted to pre-click state + error toast", async () => {
    const c = makeCtx(makeTask());
    let failed = true;
    const w = withApiRestored("toggleAc", () => async () => {
      if (failed) throw new Error("boom");
      return makeAc(1);
    });
    // Fake change event: the native click has already flipped .checked to true.
    const ev = { target: { checked: true } } as unknown as Event;
    await w.run(() => c.mod.toggleAc(makeAc(1, "one", false), ev));
    assert.equal((ev.target as { checked: boolean }).checked, false, "reverted to ac.done");
    assert.equal(c.toasts.length, 1);
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.acBusySeq, 0);
  });

  test("failure without event target → no DOM poke", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("toggleAc", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.toggleAc(makeAc(1)));
    assert.equal(c.toasts.length, 1);
    assert.equal(c.state.acBusySeq, 0);
  });
});

// ----------------------------------------------------------------------- addAc
describe("addAc", () => {
  test("guard: empty text parses to zero lines → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.newAcText = "   \n ";
    const rec: unknown[] = [];
    const w = withApiRestored("addAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(3); });
    await w.run(() => c.mod.addAc());
    assert.equal(rec.length, 0);
    assert.equal(c.state.newAcText, "   \n ", "input left untouched on the guard path");
  });

  test("guard: add in flight → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.newAcText = "[ ] fresh";
    c.state.addingAc = true;
    const rec: unknown[] = [];
    const w = withApiRestored("addAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(3); });
    await w.run(() => c.mod.addAc());
    assert.equal(rec.length, 0);
    assert.equal(c.state.newAcText, "[ ] fresh");
  });

  test("single line → row appended, input cleared, no multi-line toast", async () => {
    const c = makeCtx(makeTask());
    c.state.newAcText = "- [ ] ship it";
    const seen: unknown[] = [];
    const w = withApiRestored("addAc", () => async (...a: unknown[]) => {
      seen.push(a);
      return makeAc(3, "ship it");
    });
    await w.run(() => c.mod.addAc());

    assert.deepEqual(seen[0], ["T1", "ship it", ACTOR]);
    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria.length, 3);
    assert.equal(d.acceptance_criteria[2].text, "ship it");
    assert.equal(c.state.newAcText, "");
    assert.equal(c.toasts.length, 0, "no toast for a single criterion");
    assert.equal(c.state.addingAc, false);
  });

  test("multiple lines → one API call each, multi-line toast", async () => {
    const c = makeCtx(makeTask());
    c.state.newAcText = "[ ] alpha\n2. beta";
    let i = 0;
    const w = withApiRestored("addAc", () => async () => makeAc(3 + i++, ["alpha", "beta"][i - 1]));
    await w.run(() => c.mod.addAc());

    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria.length, 4);
    assert.equal(c.state.newAcText, "");
    assert.deepEqual(c.toasts, [{ msg: "2 criteria added", kind: undefined }]);
  });

  test("API failure mid-loop → error toast, input kept, busy reset", async () => {
    const c = makeCtx(makeTask());
    c.state.newAcText = "[ ] a\n[ ] b";
    let calls = 0;
    const w = withApiRestored("addAc", () => async () => {
      if (calls++ === 1) throw new Error("500");
      return makeAc(3, "a");
    });
    await w.run(() => c.mod.addAc());

    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria.length, 3, "first row landed, second did not");
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.newAcText, "[ ] a\n[ ] b", "input NOT cleared on failure (parity)");
    assert.equal(c.state.addingAc, false);
  });
});

// ----------------------------------------------------------------------- handleAcPaste
describe("handleAcPaste", () => {
  /** Mutable holder so assertions read the flag AFTER the call (a destructured
   *  getter would capture its value at destructure time). */
  function fakePasteEvent(raw: string): { ev: ClipboardEvent; prevented: { v: boolean } } {
    const prevented = { v: false };
    const ev = {
      clipboardData: { getData: (type: string) => (type === "text" ? raw : "") },
      preventDefault: () => { prevented.v = true; },
    };
    return { ev: ev as unknown as ClipboardEvent, prevented };
  }

  test("single-line paste → ignored entirely", async () => {
    const c = makeCtx(makeTask());
    const { ev, prevented } = fakePasteEvent("just one line");
    const rec: unknown[] = [];
    const w = withApiRestored("addAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(3); });
    await w.run(() => c.mod.handleAcPaste(ev));
    assert.equal(rec.length, 0);
    assert.equal(prevented.v, false, "preventDefault only on the multi-line path");
  });

  test("multi-line paste → each row added, input cleared, toast", async () => {
    const c = makeCtx(makeTask());
    const { ev, prevented } = fakePasteEvent("- [ ] p1\n- [x] p2");
    let i = 0;
    const w = withApiRestored("addAc", () => async () => makeAc(3 + i++, ["p1", "p2"][i - 1]));
    await w.run(() => c.mod.handleAcPaste(ev));

    assert.equal(prevented.v, true);
    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria.length, 4);
    assert.equal(c.state.newAcText, "");
    assert.deepEqual(c.toasts, [{ msg: "2 criteria added", kind: undefined }]);
  });

  test("guard: add in flight → no api call (still preventDefault after parse)", async () => {
    const c = makeCtx(makeTask());
    c.state.addingAc = true;
    const { ev, prevented } = fakePasteEvent("a\nb");
    const rec: unknown[] = [];
    const w = withApiRestored("addAc", () => async (...a: unknown[]) => { rec.push(a); return makeAc(3); });
    await w.run(() => c.mod.handleAcPaste(ev));
    assert.equal(rec.length, 0);
    assert.equal(prevented.v, true, "preventDefault happens before the busy guard (parity)");
  });
});

// ----------------------------------------------------------------------- moveAc
describe("moveAc", () => {
  test("guard: reorder in flight → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.reorderingAcs = true;
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.moveAc(1, "down"));
    assert.equal(rec.length, 0);
  });

  test("boundary guards: first up / last down → no api call", async () => {
    const c = makeCtx(makeTask());
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.moveAc(1, "up"));
    await w.run(() => c.mod.moveAc(2, "down"));
    assert.equal(rec.length, 0);
    assert.equal(c.state.reorderingAcs, false);
  });

  test("move down → swapped seqs sent; response rows replace detail's list", async () => {
    const c = makeCtx(makeTask()); // [seq1 "one", seq2 "two"]
    const seen: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => {
      seen.push(a);
      return [makeAc(2, "two"), makeAc(1, "one")];
    });
    await w.run(() => c.mod.moveAc(1, "down"));

    assert.deepEqual(seen[0], ["T1", [2, 1], ACTOR]);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.acceptance_criteria.map((x) => x.seq), [2, 1]);
    assert.equal(c.state.reorderingAcs, false);
  });

  test("unknown seq → no api call", async () => {
    const c = makeCtx(makeTask());
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.moveAc(99, "down"));
    assert.equal(rec.length, 0);
  });

  test("failure → error toast, flag reset", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("reorderAcs", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.moveAc(1, "down"));
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.reorderingAcs, false);
  });
});

// ----------------------------------------------------------------------- dropAc
describe("dropAc", () => {
  test("no drag source → draggedAcSeq zeroed, no api call", async () => {
    const c = makeCtx(makeTask());
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.dropAc(2));
    assert.equal(rec.length, 0);
    assert.equal(c.state.draggedAcSeq, 0);
  });

  test("drop onto self → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.draggedAcSeq = 2;
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.dropAc(2));
    assert.equal(rec.length, 0);
    assert.equal(c.state.draggedAcSeq, 0, "drag state cleared even on the no-op path");
  });

  test("unknown source or target seq → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.draggedAcSeq = 42;
    const rec: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => { rec.push(a); return []; });
    await w.run(() => c.mod.dropAc(1));
    assert.equal(rec.length, 0);
  });

  test("drag first onto second → spliced seqs sent; response replaces list", async () => {
    const c = makeCtx(makeTask({ acceptance_criteria: [makeAc(1), makeAc(2), makeAc(3)] }));
    c.state.draggedAcSeq = 1;
    const seen: unknown[] = [];
    const w = withApiRestored("reorderAcs", () => async (...a: unknown[]) => {
      seen.push(a);
      return [makeAc(2), makeAc(1), makeAc(3)];
    });
    await w.run(() => c.mod.dropAc(2));

    assert.deepEqual(seen[0], ["T1", [2, 1, 3], ACTOR]);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.acceptance_criteria.map((x) => x.seq), [2, 1, 3]);
    assert.equal(c.state.reorderingAcs, false);
  });

  test("failure → error toast, flag reset", async () => {
    const c = makeCtx(makeTask());
    c.state.draggedAcSeq = 1;
    const w = withApiRestored("reorderAcs", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.dropAc(2));
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.reorderingAcs, false);
  });
});

// ----------------------------------------------------------------------- edit session
describe("inline AC edit session", () => {
  test("startEditingAc seeds seq + text; cancelEditingAc clears both", () => {
    const c = makeCtx(makeTask());
    c.mod.startEditingAc(makeAc(2, "two", true));
    assert.equal(c.state.editingAcSeq, 2);
    assert.equal(c.state.editingAcText, "two");
    c.mod.cancelEditingAc();
    assert.equal(c.state.editingAcSeq, 0);
    assert.equal(c.state.editingAcText, "");
  });

  test("saveAcEdit guards: no detail / blank text / save in flight", async () => {
    const rec: unknown[] = [];
    const w = withApiRestored("editAcText", () => async (...a: unknown[]) => { rec.push(a); return makeAc(1, "x"); });

    const c1 = makeCtx(null);
    await w.run(() => c1.mod.saveAcEdit(1));
    assert.equal(rec.length, 0);

    const c2 = makeCtx(makeTask());
    c2.state.editingAcText = "   ";
    await w.run(() => c2.mod.saveAcEdit(1));
    assert.equal(rec.length, 0);

    c2.state.editingAcText = "renamed";
    c2.state.savingAcSeq = 5;
    await w.run(() => c2.mod.saveAcEdit(1));
    assert.equal(rec.length, 0);
    assert.equal(c2.state.savingAcSeq, 5);
  });

  test("save success → trimmed text sent, row patched, session cleared", async () => {
    const c = makeCtx(makeTask());
    c.state.editingAcSeq = 1;
    c.state.editingAcText = "  renamed one  ";
    const seen: unknown[] = [];
    const w = withApiRestored("editAcText", () => async (...a: unknown[]) => {
      seen.push(a);
      return makeAc(1, "renamed one");
    });
    await w.run(() => c.mod.saveAcEdit(1));

    assert.deepEqual(seen[0], ["T1", 1, "renamed one", ACTOR]);
    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria[0].text, "renamed one");
    assert.equal(c.state.editingAcSeq, 0);
    assert.equal(c.state.editingAcText, "");
    assert.equal(c.state.savingAcSeq, 0);
  });

  test("save failure → error toast, session fields kept (parity)", async () => {
    const c = makeCtx(makeTask());
    c.state.editingAcSeq = 1;
    c.state.editingAcText = "renamed one";
    const w = withApiRestored("editAcText", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.saveAcEdit(1));

    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.editingAcSeq, 1, "session NOT cleared on failure");
    assert.equal(c.state.editingAcText, "renamed one");
    assert.equal(c.state.savingAcSeq, 0);
  });
});

// ----------------------------------------------------------------------- deleteAc
describe("deleteAc", () => {
  test("guard: delete in flight → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.deletingAcSeq = 1;
    const rec: unknown[] = [];
    const w = withApiRestored("deleteAc", () => async (...a: unknown[]) => { rec.push(a); });
    await w.run(() => c.mod.deleteAc(2));
    assert.equal(rec.length, 0);
  });

  test("success → row filtered out + success toast", async () => {
    const c = makeCtx(makeTask());
    const seen: unknown[] = [];
    const w = withApiRestored("deleteAc", () => async (...a: unknown[]) => { seen.push(a); });
    await w.run(() => c.mod.deleteAc(2));

    assert.deepEqual(seen[0], ["T1", 2, ACTOR]);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.acceptance_criteria.map((x) => x.seq), [1]);
    assert.equal(c.toasts[0].msg, "Acceptance criterion removed");
    assert.equal(c.state.deletingAcSeq, 0);
  });

  test("deleting the row being edited → edit session cancelled too", async () => {
    const c = makeCtx(makeTask());
    c.state.editingAcSeq = 2;
    c.state.editingAcText = "two";
    const w = withApiRestored("deleteAc", () => async () => {});
    await w.run(() => c.mod.deleteAc(2));
    assert.equal(c.state.editingAcSeq, 0);
    assert.equal(c.state.editingAcText, "");
  });

  test("deleting a non-edited row → edit session untouched", async () => {
    const c = makeCtx(makeTask());
    c.state.editingAcSeq = 1;
    c.state.editingAcText = "one";
    const w = withApiRestored("deleteAc", () => async () => {});
    await w.run(() => c.mod.deleteAc(2));
    assert.equal(c.state.editingAcSeq, 1);
    assert.equal(c.state.editingAcText, "one");
  });

  test("failure → error toast, list untouched, busy reset", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("deleteAc", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.deleteAc(1));
    assert.equal(c.toasts[0].kind, "error");
    const d = c.state.detail as TaskDetail;
    assert.equal(d.acceptance_criteria.length, 2);
    assert.equal(c.state.deletingAcSeq, 0);
  });
});

// ----------------------------------------------------------------------- display helpers
describe("display helpers", () => {
  test("delegate to ac-math with the open task's criteria (null-safe)", () => {
    const c = makeCtx(null);
    assert.equal(c.mod.acDoneCount(), acDoneCount([]));
    assert.equal(c.mod.acProgressLabel(), acProgressLabel([]));
    assert.equal(c.mod.acRingInner(), acRingInner([]));

    const task = makeTask();
    (c.state as { detail: TaskDetail | null }).detail = task;
    assert.equal(c.mod.acDoneCount(), acDoneCount(task.acceptance_criteria));
    assert.equal(c.mod.acProgressLabel(), acProgressLabel(task.acceptance_criteria));
    assert.equal(c.mod.acRingInner(), acRingInner(task.acceptance_criteria));
    assert.equal(c.mod.completedLabel(makeAc(2, "two", true)), completedLabel(makeAc(2, "two", true)));
  });
});
