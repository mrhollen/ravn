// Unit tests for src/modules/relations.ts — the task-detail relations section
// extracted from web/src/app.ts (Phase 2 batch D). Covers: selectRelationTarget
// form writes; combobox keyboard nav (wrap-around, empty-list guard) and Enter
// (select active vs fall-through to addRelation); filteredRelationTasks
// (current-task exclusion + dedupe, per-kind already-related exclusion AC3,
// search over title/full-id/shortId/status, blocked-by done-last sorting AC2);
// the RelationRow → RelationView shaping (direction-aware labels/badges/blocker
// flag, key format, status lookup, title hook); the subtask progress counters;
// and the add/remove mutation flows (guard ordering, kind→wire mapping, search
// resolution incl. unknown-text pass-through verbatim, busy lifecycle).
//
// Pure module: mutable stub state wired into RelationsDeps accessors + counters
// for every out-bound write + monkey-patched api methods, all restored in
// finally blocks (same pattern as claims-module.test.ts). The module touches
// no DOM — no document stubs needed.

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createRelationsModule } from "../src/modules/relations";
import type { RelationForm } from "../src/modules/relations";
import { api, ApiError } from "../src/api";
import type { ActorInput, Board, RelationRow, Status, TaskDetail, TaskSummary } from "../src/types";

// ----------------------------------------------------------------------- helpers
const ACTOR: ActorInput = { name: "tester", type: "agent" };

function makeTask(id: string, over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id,
    project_id: "P1",
    seq: 1,
    title: `title of ${id}`,
    description: "",
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
    relations: [],
    ...over,
  };
}

function makeSummary(id: string, over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id,
    project_id: "P1",
    seq: 1,
    title: `title of ${id}`,
    description: "",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 8,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-03T00:01:00Z",
    ...over,
  };
}

function makeBoard(tasksByStatus: Partial<Record<Status, TaskSummary[]>>): Board {
  return {
    project_id: "P1",
    counts: { todo: 0, doing: 0, done: 0 },
    tasks: {
      todo: tasksByStatus.todo ?? [],
      doing: tasksByStatus.doing ?? [],
      done: tasksByStatus.done ?? [],
    },
  };
}

function freshForm(): RelationForm {
  return { kind: "parent", targetId: "", search: "", open: false, busy: false, activeIndex: -1 };
}

interface Ctx {
  mod: ReturnType<typeof createRelationsModule>;
  detail: TaskDetail | null;
  board: Board | null;
  form: RelationForm;
  toasts: Array<{ msg?: string; kind?: string }>;
  refreshCalls: number[];
  loadBoardCalls: number[];
  taskTitles: string[];
}

function makeCtx(detail: TaskDetail | null, board: Board | null = null): Ctx {
  const ctx: Ctx = {
    mod: undefined as never,
    detail,
    board,
    form: freshForm(),
    toasts: [],
    refreshCalls: [],
    loadBoardCalls: [],
    taskTitles: [],
  };
  ctx.mod = createRelationsModule({
    // state-in
    detail: () => ctx.detail,
    board: () => ctx.board,
    relationForm: () => ctx.form,
    // hooks
    toast: (msg, kind) => { ctx.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
    refreshDetail: (f) => { ctx.refreshCalls.push(f); return Promise.resolve(); },
    loadBoard: (f) => { ctx.loadBoardCalls.push(f); return Promise.resolve(); },
    taskTitle: (id) => { ctx.taskTitles.push(id); return `TITLE[${id}]`; },
  });
  return ctx;
}

type RelApi = "addRelation" | "removeRelation";

/** Run fn with the given api method patched; always restore it. */
async function withApi<T>(name: RelApi, impl: unknown, fn: () => Promise<T>): Promise<T> {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  try { return await fn(); } finally { (api as Record<string, unknown>)[name] = orig; }
}

// ----------------------------------------------------------------------- selectRelationTarget
describe("selectRelationTarget", () => {
  test("writes the picked candidate into the form and closes the dropdown", () => {
    const c = makeCtx(makeTask("ME"));
    c.mod.selectRelationTarget(makeSummary("01TARGET000000000000000XYZ", { title: "The Picked One" }));
    assert.equal(c.form.targetId, "01TARGET000000000000000XYZ");
    // shortId is first-8-chars + ellipsis for ids longer than 10 chars.
    // first 8 chars = "01TARGET", so shortId("01TARGET…") → "01TARGET…"
    assert.equal(c.form.search, "The Picked One (01TARGET…)");
    assert.equal(c.form.open, false);
    assert.equal(c.form.activeIndex, -1);
  });
});

// ----------------------------------------------------------------------- combobox keyboard nav
describe("onRelationComboboxKeyDown / onRelationComboboxEnter", () => {
  function ctxWithTasks(): Ctx {
    const c = makeCtx(
      makeTask("01ME"),
      makeBoard({
        todo: [makeSummary("01ME"), makeSummary("01A", { title: "A" }), makeSummary("01B", { title: "B" })],
      }),
    );
    return c;
  }

  test("no detail or board → no-op (open/activeIndex untouched)", () => {
    const c = makeCtx(null);
    c.form.open = true;
    c.form.activeIndex = 3;
    c.mod.onRelationComboboxKeyDown("down");
    assert.equal(c.form.open, true);
    assert.equal(c.form.activeIndex, 3);
  });

  test('"down" opens the dropdown and advances with wrap-around', () => {
    const c = ctxWithTasks();
    assert.equal(c.mod.filteredRelationTasks().length, 2); // 01ME excluded
    c.mod.onRelationComboboxKeyDown("down");
    assert.equal(c.form.open, true);
    assert.equal(c.form.activeIndex, 0);
    c.mod.onRelationComboboxKeyDown("down");
    assert.equal(c.form.activeIndex, 1);
    c.mod.onRelationComboboxKeyDown("down"); // wraps 1 → 0
    assert.equal(c.form.activeIndex, 0);
  });

  test('"up" wraps from the top to the last candidate and otherwise steps back', () => {
    const c = ctxWithTasks();
    c.form.activeIndex = 1;
    c.mod.onRelationComboboxKeyDown("up");
    assert.equal(c.form.activeIndex, 0);
    c.mod.onRelationComboboxKeyDown("up"); // wraps 0 → last (len-1)
    assert.equal(c.form.activeIndex, 1);
  });

  test('Enter with an open dropdown and valid activeIndex selects the candidate (no api call)', async () => {
    const c = ctxWithTasks();
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      c.form.open = true;
      c.form.activeIndex = 1;
      c.mod.onRelationComboboxEnter();
    });
    assert.equal(rec.length, 0, "selection must not hit the api");
    assert.equal(c.form.targetId, "01B");
    assert.equal(c.form.open, false);
  });

  test("Enter with a closed dropdown and search text submits via addRelation", async () => {
    const c = ctxWithTasks();
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      c.form.search = "01B";
      c.mod.onRelationComboboxEnter();
    });
    assert.equal(rec.length, 1);
    assert.equal(rec[0][2], "child"); // default form kind is "parent" → wire kind "child" (mapped in addRelation)
  });

  test("Enter with a closed dropdown and empty form does nothing", async () => {
    const c = ctxWithTasks();
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      c.mod.onRelationComboboxEnter();
    });
    assert.equal(rec.length, 0);
  });

  test("Enter while busy does nothing even with search text (busy guard wins)", async () => {
    const c = ctxWithTasks();
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      c.form.busy = true;
      c.form.search = "B";
      c.mod.onRelationComboboxEnter();
    });
    assert.equal(rec.length, 0);
  });
});

// ----------------------------------------------------------------------- filteredRelationTasks
describe("filteredRelationTasks (AC2/AC3)", () => {
  const ME = "01ME";

  function board(): Board {
    return makeBoard({
      todo: [makeSummary(ME), makeSummary("01P", { title: "Parent Task", priority: 5 }), makeSummary("01X", { title: "Alpha Feature", priority: 3 })],
      doing: [makeSummary("01B", { title: "Blocker Task", status: "doing", priority: 0 })],
      done: [makeSummary("01D", { title: "Done Beta", status: "done", priority: 0 })],
    });
  }

  test("missing detail or board → empty list", () => {
    assert.deepEqual(makeCtx(null, board()).mod.filteredRelationTasks(), []);
    assert.deepEqual(makeCtx(makeTask(ME)).mod.filteredRelationTasks(), []); // no board
  });

  test("current task excluded; deduped across columns (Set seen)", () => {
    const c = makeCtx(makeTask(ME), board());
    const ids = c.mod.filteredRelationTasks().map((t) => t.id);
    assert.ok(!ids.includes(ME), "current task never listed");
    // Quirk: a summary duplicated across two columns appears ONCE (first column wins).
    const dup = makeSummary("01DUP", { title: "Dup" });
    const c2 = makeCtx(makeTask(ME), makeBoard({ todo: [dup], doing: [makeSummary(ME), dup] }));
    const ids2 = c2.mod.filteredRelationTasks().map((t) => t.id);
    assert.deepEqual(ids2, ["01DUP"]);
  });

  function exclusionCases(): Array<{ formKind: string; edges: RelationRow[]; excluded: string[] }> {
    return [
      { formKind: "parent", edges: [{ task_id: ME, other_task_id: "01P", kind: "child" }], excluded: ["01P"] },           // 01P is my parent
      { formKind: "child", edges: [{ task_id: "01P", other_task_id: ME, kind: "child" }], excluded: ["01P"] },            // 01P is my child
      { formKind: "blocked-by", edges: [{ task_id: ME, other_task_id: "01B", kind: "blocked-by" }], excluded: ["01B"] },   // blocked by 01B
      { formKind: "blocking", edges: [{ task_id: "01B", other_task_id: ME, kind: "blocked-by" }], excluded: ["01B"] },     // I block 01B
      { formKind: "duplicate-of", edges: [{ task_id: "01X", other_task_id: ME, kind: "duplicate-of" }], excluded: ["01X"] }, // symmetric either direction
      { formKind: "related", edges: [{ task_id: ME, other_task_id: "01D", kind: "related" }], excluded: ["01D"] },         // symmetric either direction
    ];
  }

  for (const { formKind, edges, excluded } of exclusionCases()) {
    test(`kind="${formKind}": already-related edge(s) exclude the other task`, () => {
      const c = makeCtx(makeTask(ME, { relations: edges }), board());
      c.form.kind = formKind;
      const ids = c.mod.filteredRelationTasks().map((t) => t.id);
      for (const x of excluded) assert.ok(!ids.includes(x), `${x} should be excluded for kind=${formKind}`);
    });
  }

  test("exclusion is kind-scoped: an edge of another kind does NOT exclude", () => {
    // I am related to 01D (related) — picking kind "blocked-by" still offers 01D.
    const c = makeCtx(makeTask(ME, { relations: [{ task_id: ME, other_task_id: "01D", kind: "related" }] }), board());
    c.form.kind = "blocked-by";
    assert.ok(c.mod.filteredRelationTasks().some((t) => t.id === "01D"));
  });

  test("search matches title, full id, shortId and status (case-insensitive)", () => {
    const b = makeBoard({
      todo: [
        makeSummary(ME),
        makeSummary("01X", { title: "Alpha Feature", priority: 3 }),
        makeSummary("01J3ZK8WQ9X7M2P4R6S8T0U1", { title: "Long Ulid Task", priority: 1 }),
      ],
      doing: [makeSummary("01B", { title: "Blocker Task", status: "doing", priority: 0 })],
    });
    const c = makeCtx(makeTask(ME), b);
    // title substring, case-insensitive
    c.form.search = "ALPHA";
    assert.deepEqual(c.mod.filteredRelationTasks().map((t) => t.id), ["01X"]);
    // full id substring, case-insensitive
    c.form.search = "01x";
    assert.deepEqual(c.mod.filteredRelationTasks().map((t) => t.id), ["01X"]);
    // shortId: a 26-char ULID → sid = first-8-chars + ellipsis ("01j3zk8w…")
    c.form.search = "01j3zk8w…";
    assert.deepEqual(c.mod.filteredRelationTasks().map((t) => t.id), ["01J3ZK8WQ9X7M2P4R6S8T0U1"]);
    // status substring match → the doing task only
    c.form.search = "doing";
    assert.deepEqual(c.mod.filteredRelationTasks().map((t) => t.id), ["01B"]);
  });

  test("AC2: kind=blocked-by sorts open tasks before done, then by priority asc", () => {
    const c = makeCtx(makeTask(ME), board());
    c.form.kind = "blocked-by";
    // 01X (todo,P3), 01P (todo,P5), 01B (doing,P0) all open → before done 01D.
    const ids = c.mod.filteredRelationTasks().map((t) => t.id);
    assert.equal(ids[ids.length - 1], "01D", "done task last");
    assert.deepEqual(ids.slice(0, 3), ["01B", "01X", "01P"], "open tasks by priority asc");
  });

  test("non-blocked-by kinds keep board column order (todo → doing → done)", () => {
    const c = makeCtx(makeTask(ME), board());
    c.form.kind = "related";
    assert.deepEqual(c.mod.filteredRelationTasks().map((t) => t.id), ["01P", "01X", "01B", "01D"]);
  });

  test("availableRelationTasks is a plain alias of filteredRelationTasks (unbound — surface parity)", () => {
    const c = makeCtx(makeTask(ME), board());
    assert.deepEqual(c.mod.availableRelationTasks(), c.mod.filteredRelationTasks());
  });
});

// ----------------------------------------------------------------------- RelationRow → RelationView shaping
describe("relationRows (RelationRow → RelationView shaping)", () => {
  const ME = "01ME";
  function shapedBoard(): Board {
    return makeBoard({ todo: [makeSummary(ME)], doing: [makeSummary("01B", { status: "doing" })], done: [makeSummary("01D", { status: "done" })] });
  }

  test("no detail → empty list", () => {
    assert.deepEqual(makeCtx(null, shapedBoard()).mod.relationRows(), []);
  });

  function check(r: RelationRow, expect: { label: string; otherId: string; isBlocker?: boolean; status?: Status }): void {
    const c = makeCtx(makeTask(ME, { relations: [r] }), shapedBoard());
    const rows = c.mod.relationRows();
    assert.equal(rows.length, 1);
    const v = rows[0];
    assert.equal(v.label, expect.label);
    assert.equal(v.otherId, expect.otherId);
    assert.ok(!!v.badgeClass, "badge class always set");
    if (expect.isBlocker !== undefined) assert.equal(!!v.isBlocker, expect.isBlocker);
    assert.equal(v.status ?? undefined, expect.status ?? undefined);
    assert.equal(v.key, `${r.task_id}:${r.other_task_id}:${r.kind}`);
    assert.equal(v.raw, r, "raw wire row passed through untouched");
    // Title comes from the injected root lookup (module stays decoupled).
    assert.deepEqual(c.taskTitles, [expect.otherId]);
    assert.equal(v.title, `TITLE[${expect.otherId}]`);
  }

  test("child edge, I am task_id → 'Parent' row pointing at other_task_id", () => {
    check({ task_id: ME, other_task_id: "01P", kind: "child" }, { label: "Parent", otherId: "01P", status: undefined });
  });

  test("child edge, I am other_task_id → 'Child' row pointing at task_id (done child)", () => {
    check({ task_id: "01D", other_task_id: ME, kind: "child" }, { label: "Child", otherId: "01D", status: "done" });
  });

  test("blocked-by edge, I am task_id → 'Blocked by' with isBlocker flag (red badge)", () => {
    check({ task_id: ME, other_task_id: "01B", kind: "blocked-by" }, { label: "Blocked by", otherId: "01B", isBlocker: true, status: "doing" });
  });

  test("blocked-by edge, I am other_task_id → 'Blocking' without the blocker flag", () => {
    check({ task_id: "01B", other_task_id: ME, kind: "blocked-by" }, { label: "Blocking", otherId: "01B", isBlocker: false, status: "doing" });
  });

  test("duplicate-of edges resolve both directions; 'related' is direction-agnostic", () => {
    check({ task_id: ME, other_task_id: "01X", kind: "duplicate-of" }, { label: "Duplicate of", otherId: "01X" });
    check({ task_id: "01X", other_task_id: ME, kind: "duplicate-of" }, { label: "Duplicated by", otherId: "01X" });
    check({ task_id: ME, other_task_id: "01X", kind: "related" }, { label: "Related to", otherId: "01X" });
    check({ task_id: "01X", other_task_id: ME, kind: "related" }, { label: "Related to", otherId: "01X" });
  });

  test("unknown kind falls into the 'Related to' branch (wire kinds are a closed set — defensive)", () => {
    check({ task_id: ME, other_task_id: "01X", kind: "mystery-kind" as never }, { label: "Related to", otherId: "01X" });
  });

  test("status lookup: missing board or absent row → undefined status (row still rendered)", () => {
    const c = makeCtx(makeTask(ME, { relations: [{ task_id: ME, other_task_id: "01GHOST", kind: "related" }] }), null);
    assert.equal(c.mod.relationRows()[0].status, undefined);
    const c2 = makeCtx(makeTask(ME, { relations: [{ task_id: ME, other_task_id: "01GHOST", kind: "related" }] }), shapedBoard());
    assert.equal(c2.mod.relationRows()[0].status, undefined);
  });

  test("multiple edges keep input order; keys unique per (task, other, kind)", () => {
    const c = makeCtx(
      makeTask(ME, { relations: [
        { task_id: ME, other_task_id: "01B", kind: "blocked-by" },
        { task_id: ME, other_task_id: "01D", kind: "related" },
      ] }),
      shapedBoard(),
    );
    const rows = c.mod.relationRows();
    assert.deepEqual(rows.map((r) => r.key), [
      `${ME}:01B:blocked-by`,
      `${ME}:01D:related`,
    ]);
  });
});

// ----------------------------------------------------------------------- subtask progress
describe("detailSubtaskTotal / detailSubtaskDone / detailSubtaskPercent", () => {
  const ME = "01ME";

  test("only child edges with other_task_id === me count as subtasks", () => {
    const c = makeCtx(makeTask(ME, { relations: [
      { task_id: "01C1", other_task_id: ME, kind: "child" },   // C1 is my child ✓
      { task_id: ME, other_task_id: "01P", kind: "child" },    // P is my parent ✗
      { task_id: "01B", other_task_id: ME, kind: "blocked-by" }, // not a child ✗
    ] }));
    assert.equal(c.mod.detailSubtaskTotal(), 1);
  });

  test("no detail → zero counters and percent guard", () => {
    const c = makeCtx(null, makeBoard({}));
    assert.equal(c.mod.detailSubtaskTotal(), 0);
    assert.equal(c.mod.detailSubtaskDone(), 0);
    assert.equal(c.mod.detailSubtaskPercent(), 0);
  });

  test("done counts only children whose board row has status done; no board → 0", () => {
    const rels: RelationRow[] = [
      { task_id: "01C1", other_task_id: ME, kind: "child" }, // done (in done column)
      { task_id: "01C2", other_task_id: ME, kind: "child" }, // still todo
    ];
    const c = makeCtx(makeTask(ME, { relations: rels }), makeBoard({ todo: [makeSummary("01C2")], done: [makeSummary("01C1", { status: "done" })] }));
    assert.equal(c.mod.detailSubtaskTotal(), 2);
    assert.equal(c.mod.detailSubtaskDone(), 1);
    assert.equal(c.mod.detailSubtaskPercent(), 50);
    const c2 = makeCtx(makeTask(ME, { relations: rels }), null);
    assert.equal(c2.mod.detailSubtaskDone(), 0, "without a board snapshot nothing can be verified done");
  });

  test("percent rounding: 1/3 → 33, 2/3 → 67; total=0 → 0 (div-by-zero guard)", () => {
    const mk = (children: string[], doneIds: string[]) => {
      const rels = children.map((id) => ({ task_id: id, other_task_id: ME, kind: "child" as const }));
      return makeCtx(makeTask(ME, { relations: rels }), makeBoard({
        todo: [makeSummary("open")],
        done: doneIds.map((id) => makeSummary(id, { status: "done" })),
      }));
    };
    assert.equal(mk(["a", "b", "c"], ["a"]).mod.detailSubtaskPercent(), 33);
    assert.equal(mk(["a", "b", "c"], ["a", "b"]).mod.detailSubtaskPercent(), 67);
    const none = mk([], []);
    assert.equal(none.mod.detailSubtaskTotal(), 0);
    assert.equal(none.mod.detailSubtaskPercent(), 0);
  });

  test("Math.min(100, …) cap is defensive — done can never exceed total in real data", () => {
    // Quirk: the module counts done ⊆ total from the same edge list, so the
    // round() result is always ≤ 100 for consistent data; the cap exists only
    // against a desynced board snapshot (e.g. two children on one row). We pin
    // it with an inconsistent fake: both edges resolve to the SAME done row.
    const rels: RelationRow[] = [
      { task_id: "01C", other_task_id: ME, kind: "child" },
      { task_id: "01C", other_task_id: ME, kind: "child" }, // duplicate edge (server guarantees uniqueness)
    ];
    const c = makeCtx(makeTask(ME, { relations: rels }), makeBoard({ done: [makeSummary("01C", { status: "done" })] }));
    assert.equal(c.mod.detailSubtaskTotal(), 2);
    assert.equal(c.mod.detailSubtaskDone(), 2, "each edge independently finds the done row");
    assert.equal(c.mod.detailSubtaskPercent(), 100);
  });
});

// ----------------------------------------------------------------------- addRelation
describe("addRelation (mutation flow)", () => {
  const ME = "01ME";
  // 26-char ULID so the shortId (first-8-chars + ellipsis) path is exercised.
  const TARGET = "01TGT0000000000000000000AA";

  function ctx(): Ctx {
    return makeCtx(makeTask(ME, { relations: [] }), makeBoard({ todo: [makeSummary(ME), makeSummary(TARGET)] }));
  }

  test("no detail → no api call, silent", async () => {
    const c = makeCtx(null);
    c.form.targetId = TARGET;
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, () => c.mod.addRelation());
    assert.equal(rec.length, 0);
    assert.deepEqual(c.toasts, []);
  });

  test("empty targetId + empty search → no api call, silent", async () => {
    const c = ctx();
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, () => c.mod.addRelation());
    assert.equal(rec.length, 0);
    assert.equal(c.form.busy, false);
  });

  test("guard ordering quirk: busy check beats the self-relation check → silent return, no toast", async () => {
    const c = ctx();
    c.form.targetId = ME; // self-relation…
    c.form.busy = true;   // …but a request is in flight first
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, () => c.mod.addRelation());
    assert.equal(rec.length, 0);
    assert.deepEqual(c.toasts, [], "the busy early-return precedes the self-check — no error toast");
  });

  test("self-relation (not busy) → 'Cannot relate a task to itself' error toast, no api call", async () => {
    const c = ctx();
    c.form.targetId = ME;
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, () => c.mod.addRelation());
    assert.equal(rec.length, 0);
    assert.deepEqual(c.toasts, [{ msg: "Cannot relate a task to itself", kind: "error" }]);
    assert.equal(c.form.busy, false, "busy was never set — the guard returns before it");
  });

  function wireMapCases(): Array<{ formKind: string; expectArgs: [string, string, string] }> {
    return [
      { formKind: "parent", expectArgs: [ME, TARGET, "child"] },            // target becomes my parent
      { formKind: "child", expectArgs: [ME, TARGET, "parent"] },            // target becomes my child
      { formKind: "blocking", expectArgs: [TARGET, ME, "blocked-by"] },     // I block target
      { formKind: "blocked-by", expectArgs: [ME, TARGET, "blocked-by"] },   // verbatim
      { formKind: "duplicate-of", expectArgs: [ME, TARGET, "duplicate-of"] },
      { formKind: "related", expectArgs: [ME, TARGET, "related"] },
    ];
  }

  for (const { formKind, expectArgs } of wireMapCases()) {
    test(`kind="${formKind}" maps to wire call api.addRelation(${JSON.stringify(expectArgs)})`, async () => {
      const c = ctx();
      c.form.kind = formKind;
      c.form.targetId = TARGET;
      const rec: unknown[][] = [];
      await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, () => c.mod.addRelation());
      assert.equal(rec.length, 1);
      assert.deepEqual(rec[0].slice(0, 3), expectArgs);
      assert.deepEqual(rec[0][3], ACTOR, "actor hook passed through");
    });
  }

  test("search text resolution: exact id, lowercased full id and shortId all resolve to the real task", async () => {
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      let c = ctx();
      c.form.search = TARGET; // exact id
      await c.mod.addRelation();

      c = ctx();
      c.form.search = TARGET.toLowerCase(); // case-insensitive full id
      await c.mod.addRelation();

      c = ctx();
      c.form.search = "01TGT000…"; // shortId (first-8-chars + ellipsis, see util.shortId)
      await c.mod.addRelation();
    });
    assert.equal(rec.length, 3);
    for (const call of rec) assert.equal(call[1], TARGET, `resolved to the real task id ${TARGET}`);
  });

  test("Quirk pinned: unknown search text passes through VERBATIM to the api (no client-side validation)", async () => {
    const rec: unknown[][] = [];
    await withApi("addRelation", async (...a: unknown[]) => { rec.push(a); return {} as never; }, async () => {
      const c = ctx();
      c.form.search = "not-a-task"; // matches nothing in the board
      await c.mod.addRelation();
    });
    assert.equal(rec[0][1], "not-a-task", "module trusts the server to reject — verbatim pass-through");
  });

  test("success: form reset, detail + board refreshed, 'Relation added' toast, busy released in finally", async () => {
    const c = ctx();
    c.form.targetId = TARGET;
    c.form.open = true;
    c.form.activeIndex = 4;
    let sawBusyDuringAwait = false;
    await withApi("addRelation", async () => { sawBusyDuringAwait = c.form.busy; return {} as never; }, () => c.mod.addRelation());
    assert.ok(sawBusyDuringAwait, "busy set before the api call resolves");
    assert.deepEqual({ targetId: c.form.targetId, search: c.form.search, open: c.form.open, activeIndex: c.form.activeIndex },
      { targetId: "", search: "", open: false, activeIndex: -1 });
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.deepEqual(c.toasts, [{ msg: "Relation added", kind: undefined }]);
    assert.equal(c.form.busy, false);
  });

  test("api failure → error toast verbatim (incl. ApiError body), no reloads, busy released", async () => {
    const c = ctx();
    c.form.targetId = TARGET;
    await withApi("addRelation", async () => { throw new Error("boom"); }, () => c.mod.addRelation());
    assert.deepEqual(c.toasts, [{ msg: "boom", kind: "error" }]);
    assert.deepEqual(c.refreshCalls, []);
    assert.equal(c.form.busy, false);

    const conflict = new ApiError(409, { error: "parent_conflict", message: "task already has a parent" } as never);
    c.toasts.length = 0;
    await withApi("addRelation", async () => { throw conflict; }, () => c.mod.addRelation());
    assert.deepEqual(c.toasts, [{ msg: "task already has a parent", kind: "error" }], "409 parent-conflict body surfaced verbatim");
  });
});

// ----------------------------------------------------------------------- removeRelation
describe("removeRelation (mutation flow)", () => {
  const ME = "01ME";
  const EDGE: RelationRow = { task_id: ME, other_task_id: "01B", kind: "blocked-by" };

  test("no detail → no api call, silent", async () => {
    const c = makeCtx(null);
    const rec: unknown[][] = [];
    await withApi("removeRelation", async (...a: unknown[]) => { rec.push(a); return; }, () => c.mod.removeRelation(EDGE));
    assert.equal(rec.length, 0);
    assert.deepEqual(c.toasts, []);
  });

  test("success: raw edge fields + actor hit the api, reloads run, 'Relation removed' toast", async () => {
    const c = makeCtx(makeTask(ME), null);
    const rec: unknown[][] = [];
    await withApi("removeRelation", async (...a: unknown[]) => { rec.push(a); return; }, () => c.mod.removeRelation(EDGE));
    assert.equal(rec.length, 1);
    assert.deepEqual(rec[0].slice(0, 3), [ME, "01B", "blocked-by"], "removes the RAW wire edge, not a shaped direction");
    assert.deepEqual(rec[0][3], ACTOR);
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.deepEqual(c.toasts, [{ msg: "Relation removed", kind: undefined }]);
  });

  test("api failure → error toast verbatim, no reloads", async () => {
    const c = makeCtx(makeTask(ME), null);
    await withApi("removeRelation", async () => { throw new Error("denied"); }, () => c.mod.removeRelation(EDGE));
    assert.deepEqual(c.toasts, [{ msg: "denied", kind: "error" }]);
    assert.deepEqual(c.refreshCalls, []);
  });
});
