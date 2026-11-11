import test from "node:test";
import assert from "node:assert/strict";

import {
  applyFilters,
  attentionBadgeKind,
  countStalledExpired,
  hasAnyFilter,
  isDescriptionNotesMatch,
  matchesAssignee,
  matchesDueWindow,
  matchesNeedsAttention,
  matchesPriority,
  matchesSearch,
  matchesStalled,
  matchesUnblocked,
  normalizedSearch,
} from "../src/filters.ts";
import type { BoardFilterState } from "../src/filters.ts";
import type { TaskSummary } from "../src/types.ts";
import { localYmd } from "./test-helpers.ts";

// Fixed "now" for stalled/idle assertions — the module takes nowMs as a
// parameter (no Date.now() inside), so the suite is deterministic.
const NOW = Date.parse("2026-11-11T12:00:00Z");

function task(id: string, over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id,
    title: `t-${id}`,
    status: "todo",
    priority: 0,
    version: 1,
    created_at: "2026-11-11T09:00:00Z",
    updated_at: "2026-11-11T10:00:00Z",
    ...over,
  } as TaskSummary;
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

/** due_at whose UTC components are today±offset (what a date input stores). */
function dueAt(offsetDays: number): string {
  return `${localYmd(offsetDays)}T00:00:00Z`;
}

test("normalizedSearch trims and lowercases", () => {
  assert.equal(normalizedSearch("  Hello World  "), "hello world");
  assert.equal(normalizedSearch(""), "");
  assert.equal(normalizedSearch("   "), "");
});

test("hasAnyFilter: all-off is false, whitespace-only search counts as off", () => {
  const off = state();
  assert.equal(hasAnyFilter(off), false);
  assert.equal(hasAnyFilter(state({ search: "   " })), false);
  for (const over of [
    { priority: "0" },
    { assignee: "grace" },
    { assignee: "unassigned" },
    { unblockedOnly: true },
    { stalled: true },
    { needsAttention: true },
    { due: "today" },
  ]) {
    assert.equal(hasAnyFilter(state(over)), true, JSON.stringify(over));
  }
  assert.equal(hasAnyFilter(state({ search: " x" })), true);
});

test("matchesPriority is exact numeric equality on the parsed value", () => {
  const t = task("a", { priority: 3 });
  assert.equal(matchesPriority(t, Number("3")), true);
  assert.equal(matchesPriority(t, Number("2")), false);
  assert.equal(matchesPriority(task("b"), Number("0")), true); // P0 (urgent) is a valid value
});

test("matchesAssignee: unassigned special value + case-insensitive names", () => {
  const none = task("a");
  const emptyName = task("b", { assignee: { name: "", type: "human" } });
  const grace = task("c", { assignee: { name: "Grace", type: "agent" } });
  assert.equal(matchesAssignee(none, "unassigned"), true);
  assert.equal(matchesAssignee(emptyName, "unassigned"), true); // nameless assignee object ≈ unassigned
  assert.equal(matchesAssignee(grace, "unassigned"), false);
  assert.equal(matchesAssignee(grace, "grace"), true);
  assert.equal(matchesAssignee(grace, "GRACE"), true);
  assert.equal(matchesAssignee(grace, "bob"), false);
  assert.equal(matchesAssignee(none, "grace"), false); // named match never keeps unassigned rows
});

test("matchesUnblocked inverts the state-in blocked accessor", () => {
  const t = task("a");
  assert.equal(matchesUnblocked(t, () => false), true);
  assert.equal(matchesUnblocked(t, (id) => id === "a"), false);
});

test("matchesStalled: doing + lapsed lease OR >60min idle; todo/done never", () => {
  const lapsed = task("a", { status: "doing", claimed_by: "agent-1", claim_expires_at: "2026-11-11T11:00:00Z" });
  const idle = task("b", { status: "doing", updated_at: "2026-11-11T10:30:00Z" }); // 90 min old
  const fresh = task("c", { status: "doing", updated_at: "2026-11-11T11:55:00Z" }); // 5 min old
  const todoLapsed = task("d", { status: "todo", claimed_by: "agent-1", claim_expires_at: "2026-11-11T11:00:00Z" });
  assert.equal(matchesStalled(lapsed, NOW), true);
  assert.equal(matchesStalled(idle, NOW), true);
  assert.equal(matchesStalled(fresh, NOW), false);
  assert.equal(matchesStalled(todoLapsed, NOW), false);
});

test("matchesNeedsAttention: open + newest comment question/handoff only", () => {
  assert.equal(matchesNeedsAttention(task("a", { last_comment_kind: "question" })), true);
  assert.equal(matchesNeedsAttention(task("b", { status: "doing", last_comment_kind: "handoff" })), true);
  assert.equal(matchesNeedsAttention(task("c", { status: "done", last_comment_kind: "question" })), false);
  assert.equal(matchesNeedsAttention(task("d", { last_comment_kind: "progress" })), false);
  assert.equal(matchesNeedsAttention(task("e")), false); // no comments at all
});

test("matchesDueWindow: overdue / today / week on calendar days", () => {
  const todo = (days: number): TaskSummary => task(`d${days}`, { due_at: dueAt(days) });
  // overdue: strictly before today's LOCAL day; due-today stays on time.
  assert.equal(matchesDueWindow(todo(-1), "overdue"), true);
  assert.equal(matchesDueWindow(todo(0), "overdue"), false);
  assert.equal(matchesDueWindow(task("x", { status: "done", due_at: dueAt(-5) }), "overdue"), false);
  // today: exactly the local calendar day.
  assert.equal(matchesDueWindow(todo(0), "today"), true);
  assert.equal(matchesDueWindow(todo(-1), "today"), false);
  assert.equal(matchesDueWindow(todo(1), "today"), false);
  assert.equal(matchesDueWindow(task("y", { status: "done", due_at: dueAt(0) }), "today"), false);
  assert.equal(matchesDueWindow(task("z"), "today"), false); // no due date
  // week: today..+7 days inclusive of start, exclusive of end.
  assert.equal(matchesDueWindow(todo(0), "week"), true);
  assert.equal(matchesDueWindow(todo(6), "week"), true);
  assert.equal(matchesDueWindow(todo(7), "week"), false); // endExclusive boundary
  assert.equal(matchesDueWindow(todo(-1), "week"), false);
});

test("matchesSearch: title, id or server full-text union", () => {
  const set = new Set(["only-notes"]);
  assert.equal(matchesSearch(task("a", { title: "Fix LOGIN bug" }), "login", set), true);
  assert.equal(matchesSearch(task("idmatch"), "idmatch", set), true);
  assert.equal(matchesSearch(task("only-notes"), "login", set), true); // description/notes hit via server set
  assert.equal(matchesSearch(task("other"), "login", new Set()), false);
});

test("isDescriptionNotesMatch: only when title AND id lack the term", () => {
  const set = new Set(["hit"]);
  assert.equal(isDescriptionNotesMatch(task("noq"), "", set), false);
  assert.equal(isDescriptionNotesMatch(task("noq", { title: "login fix" }), "login", set), false);
  assert.equal(isDescriptionNotesMatch(task("login"), "login", set), false); // id contains it
  assert.equal(isDescriptionNotesMatch(task("hit"), "login", set), true);
  assert.equal(isDescriptionNotesMatch(task("miss"), "login", new Set()), false);
});

test("countStalledExpired counts doing rows stalled at nowMs", () => {
  const lapsed = task("a", { status: "doing", claimed_by: "agent-1", claim_expires_at: "2026-11-11T11:00:00Z" });
  const idle = task("b", { status: "doing", updated_at: "2026-11-11T10:30:00Z" });
  const fresh = task("c", { status: "doing", updated_at: "2026-11-11T11:55:00Z" });
  assert.equal(countStalledExpired([], NOW), 0);
  assert.equal(countStalledExpired([lapsed, idle, fresh], NOW), 2);
});

test("attentionBadgeKind maps the newest comment kind for open tasks", () => {
  assert.equal(attentionBadgeKind(task("a", { last_comment_kind: "question" })), "question");
  assert.equal(attentionBadgeKind(task("b", { status: "doing", last_comment_kind: "handoff" })), "handoff");
  assert.equal(attentionBadgeKind(task("c", { last_comment_kind: "decision" })), null);
  assert.equal(attentionBadgeKind(task("d", { status: "done", last_comment_kind: "question" })), null);
});

test("applyFilters: no active filter returns the SAME array reference", () => {
  const rows = [task("a"), task("b")];
  assert.equal(applyFilters(rows, state()), rows);
});

test("applyFilters narrows per active dimension", () => {
  const high = task("a", { priority: 3 });
  const low = task("b", { priority: 1 });
  assert.deepEqual(applyFilters([high, low], state({ priority: "3" })).map((t) => t.id), ["a"]);

  const unassigned = task("c");
  const taken = task("d", { assignee: { name: "Grace", type: "agent" } });
  assert.deepEqual(applyFilters([unassigned, taken], state({ assignee: "unassigned" })).map((t) => t.id), ["c"]);
  assert.deepEqual(applyFilters([unassigned, taken], state({ assignee: "grace" })).map((t) => t.id), ["d"]);

  const blocked = task("e");
  const open = task("f");
  assert.deepEqual(
    applyFilters([blocked, open], state({ unblockedOnly: true, isBlocked: (id) => id === "e" })).map((t) => t.id),
    ["f"],
  );

  const lapsed = task("g", { status: "doing", claimed_by: "agent-1", claim_expires_at: "2026-11-11T11:00:00Z" });
  assert.deepEqual(applyFilters([lapsed, low], state({ stalled: true })).map((t) => t.id), ["g"]);

  const asking = task("h", { last_comment_kind: "question" });
  assert.deepEqual(applyFilters([asking, low], state({ needsAttention: true })).map((t) => t.id), ["h"]);

  const overdueRow = task("i", { due_at: dueAt(-1) });
  assert.deepEqual(applyFilters([overdueRow, low], state({ due: "overdue" })).map((t) => t.id), ["i"]);

  assert.deepEqual(applyFilters([task("login-fix"), task("other")], state({ search: "LOGIN" })).map((t) => t.id), [
    "login-fix",
  ]);
});

test("applyFilters AND-composes multiple active dimensions in order", () => {
  const keep = task("a", { priority: 3, assignee: { name: "Grace", type: "agent" }, last_comment_kind: "question" });
  const wrongPrio = task("b", { priority: 1, assignee: { name: "Grace", type: "agent" }, last_comment_kind: "question" });
  const wrongOwner = task("c", { priority: 3, assignee: { name: "Bob", type: "human" }, last_comment_kind: "question" });
  assert.deepEqual(
    applyFilters([keep, wrongPrio, wrongOwner], state({ priority: "3", assignee: "grace", needsAttention: true })).map(
      (t) => t.id,
    ),
    ["a"],
  );
});
