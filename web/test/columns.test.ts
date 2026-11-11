// Unit tests for src/columns.ts — per-column board view assembly: filter →
// sort → Done cap, plus the status-order mapping. Pure data in / pure ColumnView
// arrays out; no DOM. Mirrors the app.ts behavior it extracted (behavior
// parity only).

import test from "node:test";
import assert from "node:assert/strict";

import { visibleColumns, assembleColumn, isDoneCapActive } from "../src/columns.ts";
import type { BoardFilterState } from "../src/filters.ts";
import { STATUSES, STATUS_LABEL } from "../src/types.ts";
import type { Status, TaskSummary } from "../src/types.ts";

const NOW = Date.parse("2026-11-11T12:00:00Z");

function task(id: string, status: Status, due_at: string | null = null, extra: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id,
    title: `t-${id}`,
    description: "",
    status,
    priority: 0,
    assignee: null,
    due_at,
    archived: false,
    claim_expires_at: null,
    last_comment_kind: null,
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    ...extra,
  };
}

function state(over: Partial<BoardFilterState> = {}): BoardFilterState {
  return {
    priority: "",
    assignee: "",
    unblockedOnly: false,
    stalled: false,
    needsAttention: false,
    due: "",
    search: "",
    nowTick: NOW,
    isBlocked: () => false,
    fullTextMatchIds: new Set<string>(),
    ...over,
  };
}

const COL_OPTS = { hoistParents: false, sortKey: "" as const, sortDir: "desc" as const };

test("visibleColumns: status order + labels mirror STATUSES/STATUS_LABEL", () => {
  const rows = (s: Status): TaskSummary[] =>
    s === "todo" ? [task("a1", "todo"), task("a2", "todo")] : [];
  const cols = visibleColumns(rows, state(), { ...COL_OPTS, doneLimit: 20 });

  assert.deepEqual(
    cols.map((c) => c.status),
    STATUSES as unknown as string[],
  );
  for (const c of cols) assert.equal(c.label, STATUS_LABEL[c.status]);
  const todo = cols.find((c) => c.status === "todo")!;
  assert.deepEqual(todo.tasks.map((t) => t.id), ["a1", "a2"]);
  assert.equal(todo.totalCount, 2);
});

test("visibleColumns caps the Done column at doneLimit unless searching or archived", () => {
  const rows = (s: Status): TaskSummary[] =>
    s === "done" ? [task("d1", "done"), task("d2", "done"), task("d3", "done")] : [];

  const capped = visibleColumns(rows, state(), { ...COL_OPTS, doneLimit: 2 });
  assert.equal(capped.find((c) => c.status === "done")!.tasks.length, 2);
  // Header pill still reads the FULL post-filter count.
  assert.equal(capped.find((c) => c.status === "done")!.totalCount, 3);

  // An active search bypasses the cap — a matched done row must never hide.
  // (The term matches via the server full-text union so the rows survive the
  // filter and the bypass is actually exercised.)
  const searching = visibleColumns(
    rows,
    state({ search: "x", fullTextMatchIds: new Set(["d1", "d2", "d3"]) }),
    { ...COL_OPTS, doneLimit: 2 },
  );
  assert.equal(searching.find((c) => c.status === "done")!.tasks.length, 3);

  // Archived view bypasses the cap too (that mode has no "show more").
  const archived = visibleColumns(rows, state(), { ...COL_OPTS, doneLimit: 2, showArchived: true });
  assert.equal(archived.find((c) => c.status === "done")!.tasks.length, 3);
});

test("assembleColumn applies filter → sort → Done cap for a single status", () => {
  // Priority filter (state-in "2"), then Done cap at 2.
  const rows = (s: Status): TaskSummary[] =>
    s === "done"
      ? [task("p0", "done", null, { priority: 0 }), task("p2a", "done", null, { priority: 2 }), task("p2b", "done", null, { priority: 2 })]
      : [];
  const col = assembleColumn("done", rows, state({ priority: "2" }), { ...COL_OPTS, doneLimit: 2 });

  assert.equal(col.status, "done");
  assert.equal(col.label, STATUS_LABEL.done);
  // Filter kept the two priority-2 rows; cap at 2 shows both; pill = 2.
  assert.deepEqual(col.tasks.map((t) => t.id).sort(), ["p2a", "p2b"]);
  assert.equal(col.totalCount, 2);
});

test("isDoneCapActive: done only, bypassed by search or archived view", () => {
  assert.equal(isDoneCapActive("done", false, false, 25, 20), true);
  assert.equal(isDoneCapActive("todo", false, false, 25, 20), false);
  assert.equal(isDoneCapActive("done", true, false, 25, 20), false); // searching
  assert.equal(isDoneCapActive("done", false, true, 25, 20), false); // archived
  assert.equal(isDoneCapActive("done", false, false, 20, 20), false); // at cap
});
