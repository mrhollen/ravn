// Unit tests for modules/board.ts — board CORE state (Phase 2, batch B of the
// frontend modularization epic): blocked-state cache, subtask meta, sort/hoist
// and density prefs, filters + debounced full-text search. Pure-module tests
// against the shared fake-deps harness (board-harness.ts); root-level wiring
// regressions live in app.test.ts.

import test from "node:test";
import assert from "node:assert/strict";

import { api } from "../src/api.ts";
import { safeStorage } from "../src/util.ts";
import type { SortKey } from "../src/sort.ts";
import { createBoardModule, LS_CARD_DENSITY, SEARCH_DEBOUNCE_MS } from "../src/modules/board.ts";
import { board, makeBoardDeps, makeState, task } from "./board-harness.ts";

// ===========================================================================
// modules/board.ts — blocked-state cache
// ===========================================================================

test("board core: a >100-task board derives blocked ids directly from the snapshot with zero HTTP", async () => {
  let listTasksCalled = false;
  const origListTasks = api.listTasks;
  try {
    (api as any).listTasks = async () => { listTasksCalled = true; return { tasks: [], total: 0, limit: 100, offset: 0 }; };

    const state = makeState({ selectedProjectId: "B" });
    const mod = createBoardModule(makeBoardDeps(state));
    const tasks150 = Array.from({ length: 150 }, (_, i) =>
      task(`b${i}`, { blockers: i === 2 || i === 120 ? [{ id: "blk1", title: "Blocker", status: "todo" }] : undefined }),
    );
    mod.syncBlockedFromBoard(board("B", { todo: tasks150 }));

    assert.equal(listTasksCalled, false, "no listTasks round-trip — derivation is synchronous");
    assert.equal(mod.isBlocked("b2"), true);
    assert.equal(mod.isBlocked("b120"), true, ">100th task still recognized");
    assert.equal(mod.isBlocked("b0"), false);
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("board core: isBlocked fails closed when the cache belongs to another project", () => {
  const state = makeState({ selectedProjectId: "Q" });
  const mod = createBoardModule(makeBoardDeps(state));
  // Cache was derived + learned while project "P" was selected…
  mod.syncBlockedFromBoard(board("P", { todo: [task("x1", { blockers: [{ id: "k", title: "K", status: "todo" }] })] }));
  state.selectedProjectId = "P";
  mod.learnBlockedFromDetail("P", "x2", true, []);

  // …but the selection has since moved on (an entry point that does not reset).
  state.selectedProjectId = "Q";
  assert.equal(mod.isBlocked("x1"), false, "stale derived ids never surface");
  assert.equal(mod.isBlocked("x2"), false, "stale learned entries never surface");

  // Back in scope → the same entries serve normally.
  state.selectedProjectId = "P";
  assert.equal(mod.isBlocked("x1"), true);
  assert.equal(mod.isBlocked("x2"), true);
});

test("board core: resetBlockedCache clears both caches, re-scopes, and closes an open popover", () => {
  const state = makeState({ selectedProjectId: "A", blockerPopoverTaskId: "a1" });
  const mod = createBoardModule(makeBoardDeps(state));
  mod.syncBlockedFromBoard(board("A", { todo: [task("a1", { blockers: [{ id: "k", title: "K", status: "todo" }] })] }));
  state.selectedProjectId = "A";
  mod.learnBlockedFromDetail("A", "a3", true, []);

  state.selectedProjectId = "B"; // what selectProject() does before calling selectProject()
  mod.resetBlockedCache(state.selectedProjectId);

  assert.equal(state.blockerPopoverTaskId, null, "open popover closes on switch");
  assert.equal(mod.isBlocked("a1"), false);
  assert.equal(mod.isBlocked("a3"), false);
  // The cache now belongs to B: learning for B lands.
  mod.learnBlockedFromDetail("B", "b1", true, []);
  assert.equal(mod.isBlocked("b1"), true);
});

test("board core: a detail payload for the CURRENT project learns/removes blocked state; foreign ones never write", () => {
  const state = makeState({ selectedProjectId: "B" });
  const mod = createBoardModule(makeBoardDeps(state));
  mod.syncBlockedFromBoard(board("B", { todo: [task("b1")] }));

  // Foreign (A's drawer refresh outlived a switch to B): no write.
  mod.learnBlockedFromDetail("A", "a9", true, [{ id: "k", title: "K", status: "todo" }]);
  assert.equal(mod.isBlocked("a9"), false);

  // Current project, blocked=true → learns state AND the blocker refs.
  const blks = [{ id: "k1", title: "Blocks b9", status: "doing" }];
  mod.learnBlockedFromDetail("B", "b9", true, blks);
  assert.equal(mod.isBlocked("b9"), true);
  assert.deepEqual(mod.taskBlockers(task("b9")), blks, "learned refs surface for a card without inline blockers");
  assert.equal(mod.blockerTooltipText(task("b9")), "Blocked by: Blocks b9 (doing)");

  // Current project, unblocked detail → the learned entry is removed again.
  mod.learnBlockedFromDetail("B", "b9", false, null);
  assert.equal(mod.isBlocked("b9"), false);
  assert.deepEqual(mod.taskBlockers(task("b9")), []);
});

test("board core: toggleBlockerPopover opens and closes through the root-owned field", () => {
  const state = makeState();
  const mod = createBoardModule(makeBoardDeps(state));
  mod.toggleBlockerPopover("t1");
  assert.equal(state.blockerPopoverTaskId, "t1");
  mod.toggleBlockerPopover("t1");
  assert.equal(state.blockerPopoverTaskId, null);
});

test("board core: refreshBlockedSet honors the isStillCurrent guard and never touches the network", async () => {
  const state = makeState({ selectedProjectId: "B" });
  const mod = createBoardModule(makeBoardDeps(state));
  const b = board("B", { todo: [task("b2", { blockers: [{ id: "k", title: "K", status: "todo" }] })] });

  await mod.refreshBlockedSet(b, () => false); // stale by the time it ran → no-op
  assert.equal(mod.isBlocked("b2"), false);

  await mod.refreshBlockedSet(b); // no guard → applies
  assert.equal(mod.isBlocked("b2"), true);
});

// ===========================================================================
// modules/board.ts — subtask meta, sort/density prefs, filter controls
// ===========================================================================

test("board core: subtask progress clamps and rounds (0 total → 0%)", () => {
  const state = makeState();
  const mod = createBoardModule(makeBoardDeps(state));
  assert.equal(mod.subtaskProgressPercent(task("t")), 0, "no subtasks → 0");
  assert.equal(mod.subtaskProgressPercent(task("t", { subtask_total: 4, subtask_done: 1 })), 25);
  assert.equal(mod.subtaskProgressPercent(task("t", { subtask_total: 3, subtask_done: 2 })), 67, "rounds");
  assert.equal(mod.subtaskProgressPercent(task("t", { subtask_total: 2, subtask_done: 9 })), 100, "clamps at 100");
});

test("board core: setSort normalizes unknown keys and applies the criterion's default direction", () => {
  let synced: boolean[] = [];
  const state = makeState({ sortKey: "bogus" as SortKey });
  const mod = createBoardModule(makeBoardDeps(state, { syncUrlState: (immediate) => synced.push(!!immediate) }));

  mod.setSort(); // x-model wrote an invalid value → normalized to ""
  assert.equal(state.sortKey, "", "unknown key normalized to server order");
  assert.deepEqual(synced, [true], "URL synced immediately on every sort change");

  state.sortKey = "priority";
  mod.setSort();
  assert.equal(state.sortDir, "asc", "P0-first default applied for a new criterion");
});

test("board core: toggleSortDir flips only with an active key; clearSort resets to server order", () => {
  const state = makeState({ sortKey: "updated_at", sortDir: "desc" });
  const mod = createBoardModule(makeBoardDeps(state));

  mod.toggleSortDir();
  assert.equal(state.sortDir, "asc");

  state.sortKey = "";
  mod.toggleSortDir(); // no key → direction is inert
  assert.equal(state.sortDir, "asc");

  state.sortKey = "title";
  mod.clearSort();
  assert.equal(state.sortKey, "", "key cleared");

  state.sortKey = "";
  assert.equal(mod.activeSortLabel(), "");
  state.sortKey = "title";
  assert.equal(mod.activeSortLabel(), "Title (A→Z)");
});

test("board core: card density toggles through the root field and persists", () => {
  const prev = safeStorage.getItem(LS_CARD_DENSITY);
  try {
    const state = makeState({ cardDensity: "comfortable" });
    const mod = createBoardModule(makeBoardDeps(state));

    mod.toggleCardDensity();
    assert.equal(state.cardDensity, "compact");
    assert.equal(safeStorage.getItem(LS_CARD_DENSITY), "compact", "pref persisted");

    state.cardDensity = "compact";
    mod.setCardDensity("comfortable");
    assert.equal(state.cardDensity, "comfortable");
  } finally {
    if (prev === null) safeStorage.removeItem(LS_CARD_DENSITY);
    else safeStorage.setItem(LS_CARD_DENSITY, prev);
  }
});

test("board core: hasActiveFilters is false only when every dimension is off (search ignores blanks)", () => {
  const mk = (over: Partial<FakeState>) => createBoardModule(makeBoardDeps(makeState(over)));
  assert.equal(mk({}).hasActiveFilters(), false);
  assert.equal(mk({ filterPriority: "3" }).hasActiveFilters(), true);
  assert.equal(mk({ filterAssignee: "Ada" }).hasActiveFilters(), true);
  assert.equal(mk({ filterUnblocked: true }).hasActiveFilters(), true);
  assert.equal(mk({ filterStalled: true }).hasActiveFilters(), true);
  assert.equal(mk({ filterNeedsAttention: true }).hasActiveFilters(), true);
  assert.equal(mk({ filterDue: "week" }).hasActiveFilters(), true);
  assert.equal(mk({ filterSearch: "term" }).hasActiveFilters(), true);
  assert.equal(mk({ filterSearch: "   " }).hasActiveFilters(), false, "whitespace-only search is not active");
});

test("board core: clearFilters resets all seven filters and syncs the URL immediately", () => {
  let synced: boolean[] = [];
  const state = makeState({
    filterPriority: "2",
    filterAssignee: "Ada",
    filterUnblocked: true,
    filterStalled: true,
    filterNeedsAttention: true,
    filterDue: "overdue",
    filterSearch: "x",
  });
  const mod = createBoardModule(makeBoardDeps(state, { syncUrlState: (immediate) => synced.push(!!immediate) }));

  mod.clearFilters();

  assert.equal(state.filterPriority, "");
  assert.equal(state.filterAssignee, "");
  assert.equal(state.filterUnblocked, false);
  assert.equal(state.filterStalled, false);
  assert.equal(state.filterNeedsAttention, false);
  assert.equal(state.filterDue, "");
  assert.equal(state.filterSearch, "");
  assert.deepEqual(synced, [true]);
});

test("board core: availableAssignees dedupes and sorts the board's assignee names", () => {
  const state = makeState({
    board: board("P", {
      todo: [task("t1", { assignee: { type: "human", name: "Zed" } }), task("t2", { assignee: { type: "agent", name: "Ada" } })],
      doing: [task("t3", { assignee: { type: "human", name: "Zed" } })],
    }),
  });
  const mod = createBoardModule(makeBoardDeps(state));
  assert.deepEqual(mod.availableAssignees(), ["Ada", "Zed"]);
});

// ===========================================================================
// modules/board.ts — full-text (desc/notes) search
// ===========================================================================

test("board core FTS: a typing burst coalesces into ONE debounced server query (AC1)", async () => {
  const origListTasks = api.listTasks;
  try {
    let calls: string[] = [];
    (api as any).listTasks = async (params: any) => {
      calls.push(params.search);
      return { tasks: [{ id: "d1" }], total: 1, limit: 100, offset: 0 };
    };

    const state = makeState({ selectedProjectId: "P" });
    const mod = createBoardModule(makeBoardDeps(state));

    // Two rapid keystrokes — the trailing debounce must fire exactly once.
    state.filterSearch = "quixo";
    mod.scheduleFullTextSearch();
    state.filterSearch = "quixotic";
    mod.scheduleFullTextSearch();
    await new Promise((r) => setTimeout(r, SEARCH_DEBOUNCE_MS + 100));

    assert.deepEqual(calls, ["quixotic"], "whole burst costs exactly ONE round-trip with the FINAL term");
    assert.deepEqual([...mod.fullTextMatchIds()], ["d1"]);
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("board core FTS: an out-of-order LATE response is discarded by the sequence token (AC3)", async () => {
  const origListTasks = api.listTasks;
  try {
    let calls = 0;
    (api as any).listTasks = async () => {
      calls++;
      if (calls === 1) await new Promise((r) => setTimeout(r, 60)); // the OLD query is slow
      return { tasks: [{ id: calls === 1 ? "old1" : "new1" }], total: 1, limit: 100, offset: 0 };
    };

    const state = makeState({ selectedProjectId: "P", filterSearch: "old-term" });
    const mod = createBoardModule(makeBoardDeps(state));
    const pOld = mod.runFullTextSearch(); // token N — in flight
    state.filterSearch = "new-term";
    const pNew = mod.runFullTextSearch(); // token N+1 — initiates later, wins

    await pNew;
    assert.deepEqual([...mod.fullTextMatchIds()], ["new1"]);
    await pOld; // stale slow response arrives LAST
    assert.deepEqual([...mod.fullTextMatchIds()], ["new1"], "late response must never overwrite newer state");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("board core FTS: a project switch mid-flight retires the in-flight response", async () => {
  const origListTasks = api.listTasks;
  try {
    let releaseSlow: (v: any) => void;
    const slow = new Promise<any>((r) => { releaseSlow = r; });
    (api as any).listTasks = async () => slow;

    const state = makeState({ selectedProjectId: "A", filterSearch: "term" });
    const mod = createBoardModule(makeBoardDeps(state));

    const inFlight = mod.runFullTextSearch(); // scoped to A…
    state.selectedProjectId = "B"; // …switch happens before it settles (no cancel call — deep-link path)

    releaseSlow!({ tasks: [{ id: "a1" }], total: 1, limit: 100, offset: 0 });
    await inFlight;
    assert.deepEqual([...mod.fullTextMatchIds()], [], "a late A-scoped response never lands on B");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("board core FTS: scheduleFullTextSearch's empty branch drops the match set and retires in-flight work", async () => {
  const origListTasks = api.listTasks;
  try {
    let releaseSlow: (v: any) => void;
    const slow = new Promise<any>((r) => { releaseSlow = r; });
    (api as any).listTasks = async () => slow;

    const state = makeState({ selectedProjectId: "P", filterSearch: "term" });
    const mod = createBoardModule(makeBoardDeps(state));

    // A settled query, then a direct run still in flight…
    (api as any).listTasks = async () => ({ tasks: [{ id: "stale1" }], total: 1, limit: 100, offset: 0 });
    await mod.runFullTextSearch();
    assert.deepEqual([...mod.fullTextMatchIds()], ["stale1"]);

    (api as any).listTasks = async () => slow;
    const inFlight = mod.runFullTextSearch(); // …and one more run in flight
    state.filterSearch = "";
    mod.scheduleFullTextSearch(); // the CLEAR path

    assert.deepEqual([...mod.fullTextMatchIds()], [], "match set cleared instantly");
    assert.equal(mod.isDescriptionNotesMatch(task("stale1")), false, "no chips after clear");

    releaseSlow!({ tasks: [{ id: "ghost1" }], total: 1, limit: 100, offset: 0 });
    await inFlight;
    assert.deepEqual([...mod.fullTextMatchIds()], [], "late response for the cleared query discarded");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("board core FTS: isDescriptionNotesMatch fires only for desc/notes-only hits (AC2)", async () => {
  const origListTasks = api.listTasks;
  try {
    (api as any).listTasks = async () =>
      ({ tasks: [{ id: "id-a" }, { id: "id-b" }, { id: "zzz-1234567890abcdefghijklmnop" }], total: 3, limit: 100, offset: 0 });

    const state = makeState({ selectedProjectId: "P", filterSearch: "zzz" });
    const mod = createBoardModule(makeBoardDeps(state));
    await mod.runFullTextSearch();

    assert.equal(mod.isDescriptionNotesMatch(task("id-a", { title: "No term here" })), true, "server-only hit → chip");
    assert.equal(
      mod.isDescriptionNotesMatch(task("id-b", { title: "Contains ZZZ in the title" })),
      false,
      "title match (any case) → no chip",
    );
    assert.equal(
      mod.isDescriptionNotesMatch(task("zzz-1234567890abcdefghijklmnop", { title: "Term in the ID" })),
      false,
      "id match → no chip",
    );

    state.filterSearch = "   ";
    assert.equal(mod.isDescriptionNotesMatch(task("id-a", { title: "x" })), false, "whitespace-only query → never");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

