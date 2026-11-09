import test from "node:test";
import assert from "node:assert/strict";

import { boardFromTaskList } from "../src/board-scope.ts";
import type { TaskSummary } from "../src/types.ts";

// boardFromTaskList reshapes a flat (possibly scoped) listTasks payload into the
// Board snapshot columns() consumes — pure, so no DOM/fetch needed. The focus is on
// grouping, counts, and defensive handling of empty/unknown-status rows.

const task = (id: string, status: TaskSummary["status"]): TaskSummary =>
  ({ id, title: "t-" + id, status, priority: 0, version: 1 } as unknown as TaskSummary);

test("boardFromTaskList groups rows by status and derives truthful counts", () => {
  const board = boardFromTaskList("proj-9", [
    task("a", "todo"),
    task("b", "doing"),
    task("c", "done"),
    task("d", "todo"),
  ]);

  assert.equal(board.project_id, "proj-9");
  assert.deepEqual(board.counts, { todo: 2, doing: 1, done: 1 });
  assert.deepEqual(
    board.tasks.todo.map((t) => t.id),
    ["a", "d"], // stable order of appearance within a column
  );
  assert.deepEqual(board.tasks.doing.map((t) => t.id), ["b"]);
  assert.deepEqual(board.tasks.done.map((t) => t.id), ["c"]);
});

test("boardFromTaskList: an empty / null list yields the zero-count, empty-board shape", () => {
  const board = boardFromTaskList("p0", []);
  assert.equal(board.project_id, "p0");
  assert.deepEqual(board.counts, { todo: 0, doing: 0, done: 0 });
  assert.deepEqual(board.tasks, { todo: [], doing: [], done: [] });

  // A null-ish payload must not throw (a failed/scoped fetch can hand back undefined).
  const empty = boardFromTaskList("p0", undefined as unknown as TaskSummary[]);
  assert.deepEqual(empty.counts, { todo: 0, doing: 0, done: 0 });
});

test("boardFromTaskList drops rows with an unknown or missing status instead of crashing", () => {
  const board = boardFromTaskList("px", [
    task("ok", "doing"),
    ({ id: "weird", title: "x", status: "archived" } as unknown as TaskSummary), // not a live column
    (null as unknown as TaskSummary),
    ({ id: "nostatus", title: "y" } as unknown as TaskSummary), // no status at all
  ]);
  assert.deepEqual(board.counts, { todo: 0, doing: 1, done: 0 });
  assert.deepEqual(board.tasks.doing.map((t) => t.id), ["ok"]);
});

test("boardFromTaskList preserves scoped-only rows (attribution fields ride along untouched)", () => {
  const withCreator = { ...task("m", "todo"), created_by: "uid-1", owner: { type: "human", name: "ada" } };
  const board = boardFromTaskList("pm", [withCreator]);
  assert.equal(board.tasks.todo[0].created_by, "uid-1");
  assert.deepEqual(board.tasks.todo[0].owner, { type: "human", name: "ada" });
});
