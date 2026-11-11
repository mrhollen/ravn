// Unit tests for src/sort.ts (Phase 1 extraction from app.ts). Covers the
// criterion vocabulary, persisted reader/writer, compareTasks tie-breaking,
// and the hoist-parents / Done-newest-first column pass — including the
// array-identity no-op path that columns() relies on.

import test from "node:test";
import assert from "node:assert/strict";

import {
  LS_HOIST_PARENTS,
  LS_SORT,
  SORT_OPTIONS,
  DEFAULT_SORT_DIR,
  compareTasks,
  isParentTask,
  isSortKey,
  persistHoistParents,
  persistSort,
  readStoredHoistParents,
  readStoredSort,
  sortColumnTasks,
  type SortKey,
} from "../src/sort.ts";
import type { TaskSummary } from "../src/types.ts";
import { safeStorage } from "../src/util.ts";

/** Minimal board row; callers override whatever the case under test needs. */
const task = (id: string, over: Partial<TaskSummary> = {}): TaskSummary =>
  ({
    id,
    title: "t-" + id,
    status: "todo",
    priority: 0,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    ...over,
  }) as TaskSummary;

const ids = (rows: TaskSummary[]): string[] => rows.map((t) => t.id);

test("sort: isSortKey accepts exactly the option values", () => {
  for (const o of SORT_OPTIONS) assert.equal(isSortKey(o.value), true, o.value);
  assert.equal(isSortKey("bogus"), false);
  assert.equal(isSortKey(3), false);
  assert.equal(isSortKey(null), false);
});

test("sort: DEFAULT_SORT_DIR covers every non-empty criterion with a sane direction", () => {
  for (const o of SORT_OPTIONS.filter((x) => x.value !== "")) {
    const v = o.value as Exclude<SortKey, "">;
    assert.ok(DEFAULT_SORT_DIR[v] === "asc" || DEFAULT_SORT_DIR[v] === "desc", `bad dir for ${v}`);
  }
  assert.equal(DEFAULT_SORT_DIR.due_at, "asc"); // nearest first
  assert.equal(DEFAULT_SORT_DIR.priority, "asc"); // P0 first
});

test("sort: readStoredSort — default, round-trip, partial + malformed values, storage error", () => {
  safeStorage.clear();
  assert.deepEqual(readStoredSort(), { key: "", dir: "desc" }); // unset
  persistSort("due_at", "asc");
  assert.deepEqual(readStoredSort(), { key: "due_at", dir: "asc" });
  // unknown key falls back to no sort; unknown dir falls back to desc
  safeStorage.setItem(LS_SORT, JSON.stringify({ key: "bogus", dir: "sideways" }));
  assert.deepEqual(readStoredSort(), { key: "", dir: "desc" });
  // valid key with missing dir keeps the key, defaults the dir
  safeStorage.setItem(LS_SORT, JSON.stringify({ key: "priority" }));
  assert.deepEqual(readStoredSort(), { key: "priority", dir: "desc" });
  // unparseable payload → catch → default
  safeStorage.setItem(LS_SORT, "{not-json");
  assert.deepEqual(readStoredSort(), { key: "", dir: "desc" });
  // storage itself throwing → catch → default
  const orig = safeStorage.getItem;
  safeStorage.getItem = () => {
    throw new Error("blocked");
  };
  try {
    assert.deepEqual(readStoredSort(), { key: "", dir: "desc" });
  } finally {
    safeStorage.getItem = orig;
    safeStorage.clear();
  }
});

test("sort: readStoredHoistParents — default ON, persisted OFF, malformed → ON", () => {
  safeStorage.clear();
  assert.equal(readStoredHoistParents(), true); // missing → ON (default)
  persistHoistParents(false);
  assert.equal(readStoredHoistParents(), false);
  persistHoistParents(true);
  assert.equal(readStoredHoistParents(), true);
  safeStorage.setItem(LS_HOIST_PARENTS, JSON.stringify("not-a-bool"));
  assert.equal(readStoredHoistParents(), true); // non-boolean → ON
  safeStorage.setItem(LS_HOIST_PARENTS, "{broken");
  assert.equal(readStoredHoistParents(), true); // unparseable → catch → ON
  const orig = safeStorage.getItem;
  safeStorage.getItem = () => {
    throw new Error("blocked");
  };
  try {
    assert.equal(readStoredHoistParents(), true);
  } finally {
    safeStorage.getItem = orig;
    safeStorage.clear();
  }
});

test("sort: compareTasks — priority asc/desc with id tie-break", () => {
  const a = task("a", { priority: 2 });
  const b = task("b", { priority: 0 });
  assert.ok(compareTasks(a, b, "priority", "asc") > 0);
  assert.ok(compareTasks(b, a, "priority", "asc") < 0);
  assert.ok(compareTasks(a, b, "priority", "desc") < 0); // inverted
  const t1 = task("x1", { priority: 1 });
  const t2 = task("x2", { priority: 1 });
  assert.ok(compareTasks(t2, t1, "priority", "asc") > 0); // tie → id ascending
  assert.ok(compareTasks(t2, t1, "priority", "desc") > 0); // tie-break ignores dir
});

test("sort: compareTasks — title uses numeric, case-insensitive collation", () => {
  const a2 = task("a2", { title: "item 2" });
  const a10 = task("a10", { title: "item 10" });
  assert.ok(compareTasks(a2, a10, "title", "asc") < 0); // numeric: 2 before 10
  assert.ok(compareTasks(a2, a10, "title", "desc") > 0);
  const upper = task("u", { title: "Apple" });
  const lower = task("l", { title: "apple" });
  assert.ok(compareTasks(upper, lower, "title", "asc") > 0); // case-insensitive tie → id ("l" < "u")
  const tie = [task("z1", { title: "same" }), task("z2", { title: "same" })];
  assert.ok(compareTasks(tie[0], tie[1], "title", "asc") < 0); // tie → id
});

test("sort: compareTasks — due_at sorts nulls last in both directions", () => {
  const soon = task("soon", { due_at: "2026-01-05T00:00:00Z" });
  const late = task("late", { due_at: "2026-03-05T00:00:00Z" });
  const none = task("none");
  assert.ok(compareTasks(soon, late, "due_at", "asc") < 0);
  assert.ok(compareTasks(late, soon, "due_at", "desc") < 0);
  assert.ok(compareTasks(none, soon, "due_at", "asc") > 0); // undated last in asc (nulls-last)
  // Original quirk preserved verbatim: in desc mode the Infinity flips, so an
  // undated row sorts FIRST — the module must mirror app.ts exactly.
  assert.ok(compareTasks(none, soon, "due_at", "desc") < 0);
  const same1 = task("s1", { due_at: "2026-02-01T00:00:00Z" });
  const same2 = task("s2", { due_at: "2026-02-01T00:00:00Z" });
  assert.ok(compareTasks(same2, same1, "due_at", "asc") > 0); // tie → id
});

test("sort: compareTasks — created_at/updated_at parse ISO instants; NaN degrades to id tie-break", () => {
  const old = task("old", { updated_at: "2026-01-01T00:00:00Z", created_at: "2025-01-01T00:00:00Z" });
  const new_ = task("new", { updated_at: "2026-06-01T00:00:00Z", created_at: "2026-01-01T00:00:00Z" });
  assert.ok(compareTasks(old, new_, "updated_at", "desc") > 0); // newest first
  assert.ok(compareTasks(old, new_, "created_at", "asc") < 0); // oldest first
  const bad1 = task("b1", { created_at: "", updated_at: "" });
  const bad2 = task("b2", { created_at: "", updated_at: "" });
  assert.ok(compareTasks(bad2, bad1, "created_at", "asc") > 0); // both NaN → id tie-break
});

test("sort: compareTasks — key '' is plain canonical id order regardless of direction", () => {
  const m = task("m");
  const a = task("a");
  assert.ok(compareTasks(m, a, "", "asc") > 0);
  assert.ok(compareTasks(m, a, "", "desc") > 0);
});

test("sort: isParentTask — only rows reporting at least one child", () => {
  assert.equal(isParentTask(task("p", { subtask_total: 3 })), true);
  assert.equal(isParentTask(task("z", { subtask_total: 0 })), false);
  assert.equal(isParentTask(task("n")), false); // omitted → not a parent
});

test("sort: sortColumnTasks — hoist ON + active key sorts each group, parents first, input untouched", () => {
  const childA = task("c1", { updated_at: "2026-05-01T00:00:00Z" });
  const parentB = task("p1", { subtask_total: 2, updated_at: "2026-01-01T00:00:00Z" });
  const childC = task("c2", { updated_at: "2026-03-01T00:00:00Z" });
  const parentA = task("p2", { subtask_total: 1, updated_at: "2026-04-01T00:00:00Z" });
  const input = [childA, parentB, childC, parentA];
  const snapshot = ids(input);

  const out = sortColumnTasks(input, "todo", "updated_at", "asc", true);
  assert.deepEqual(ids(out), ["p1", "p2", "c2", "c1"]); // parents (asc by updated) then children (asc by updated)
  assert.notEqual(out, input); // new array — board arrays never mutated
  assert.deepEqual(ids(input), snapshot);
});

test("sort: sortColumnTasks — hoist ON + server order keeps relative order (Done is newest-first)", () => {
  const p2 = task("p2", { subtask_total: 1 });
  const t1 = task("t1");
  const p3 = task("p3", { subtask_total: 4 });
  const todoIn = [p2, t1, p3];
  const outTodo = sortColumnTasks(todoIn, "todo", "", "desc", true);
  assert.deepEqual(ids(outTodo), ["p2", "p3", "t1"]); // group order preserved, parents hoisted

  const d1 = task("d1", { updated_at: "2026-01-01T00:00:00Z" });
  const d2 = task("d2", { updated_at: "2026-03-01T00:00:00Z" });
  const dp = task("dp", { subtask_total: 1, updated_at: "2026-02-01T00:00:00Z" });
  const outDone = sortColumnTasks([d1, d2, dp], "done", "", "asc", true);
  assert.deepEqual(ids(outDone), ["dp", "d2", "d1"]); // Done groups order newest-first
});

test("sort: sortColumnTasks — Done newest-first uses completed_at when present (fallback chain)", () => {
  const a = task("a", { updated_at: "2026-05-01T00:00:00Z" });
  const b = { ...task("b", { updated_at: "2026-01-01T00:00:00Z" }), completed_at: "2026-09-01T00:00:00Z" } as TaskSummary;
  const c = task("c", { updated_at: "2026-07-01T00:00:00Z" });
  assert.deepEqual(ids(sortColumnTasks([a, b, c], "done", "", "asc", false)), ["b", "c", "a"]);
});

test("sort: sortColumnTasks — hoist OFF + active key flat-sorts a copy", () => {
  const input = [task("x", { priority: 3 }), task("y", { priority: 1 }), task("z", { priority: 2 })];
  const snapshot = ids(input);
  const out = sortColumnTasks(input, "doing", "priority", "asc", false);
  assert.deepEqual(ids(out), ["y", "z", "x"]);
  assert.notEqual(out, input);
  assert.deepEqual(ids(input), snapshot); // original order untouched
});

test("sort: sortColumnTasks — no-op path returns the SAME array reference", () => {
  const input = [task("a"), task("b")];
  const out = sortColumnTasks(input, "todo", "", "asc", false);
  assert.equal(out, input); // identity preserved (columns() pass-through contract)
});
