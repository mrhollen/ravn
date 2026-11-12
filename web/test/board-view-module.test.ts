// Unit tests for modules/board-view.ts — board VIEW pipeline (Phase 2, batch B
// of the frontend modularization epic): stalled/attention helpers, URL query
// sync, listTasksParams mapping, counts/columns and the loadBoard lifecycle.
// Pure-module tests against the shared fake-deps harness (board-harness.ts);
// root-level wiring regressions live in app.test.ts.

import test from "node:test";
import assert from "node:assert/strict";

import { api, ApiError } from "../src/api.ts";
import { safeStorage } from "../src/util.ts";
import { LS_SORT, persistSort } from "../src/sort.ts";
import { createBoardViewModule } from "../src/modules/board-view.ts";
import { withWindow } from "./test-helpers.ts";
import { board, makeBoardViewDeps, makeState, task } from "./board-harness.ts";

// ===========================================================================
// modules/board-view.ts — stalled alert + attention badge
// ===========================================================================

test("board view: stalledExpiredCount counts lapsed-lease AND idle doing rows; label is plural-aware", () => {
  const base = Date.parse("2026-11-01T12:00:00Z");
  const staleLease = task("s1", { status: "doing", claimed_by: { type: "agent", name: "a" }, claim_expires_at: new Date(base - 60_000).toISOString(), updated_at: new Date(base - 5 * 60_000).toISOString() });
  const idle = task("s2", { status: "doing", updated_at: new Date(base - 61 * 60_000).toISOString() });
  const fresh = task("ok", { status: "doing", updated_at: new Date(base - 5 * 60_000).toISOString() });

  const mk = (doing: TaskSummary[]) => createBoardViewModule(makeBoardViewDeps(makeState({ board: board("P", { doing }), nowTick: base })));

  const mod = mk([staleLease, idle, fresh]);
  assert.equal(mod.stalledExpiredCount(), 2, "lapsed lease + >60min idle; fresh row excluded");
  assert.equal(mod.stalledAlertLabel(), "2 abandoned/stalled tasks");

  const single = mk([idle]);
  assert.equal(single.stalledAlertLabel(), "1 abandoned/stalled task", "singular");

  // nowTick drives re-evaluation: the same snapshot is fresh one hour later…
  const ticked = createBoardViewModule(makeBoardViewDeps(makeState({ board: board("P", { doing: [fresh] }), nowTick: base + 61 * 60_000 })));
  assert.equal(ticked.stalledExpiredCount(), 1, "idle age re-evaluates against the live tick");
});

test("board view: attentionBadgeKind badges only open question/handoff threads", () => {
  const state = makeState();
  const mod = createBoardViewModule(makeBoardViewDeps(state));
  assert.equal(mod.attentionBadgeKind(task("t", { last_comment_kind: "question" })), "question");
  assert.equal(mod.attentionBadgeKind(task("t", { status: "doing", last_comment_kind: "handoff" })), "handoff");
  assert.equal(mod.attentionBadgeKind(task("t", { status: "done", last_comment_kind: "question" })), null, "done never badges");
  assert.equal(mod.attentionBadgeKind(task("t", { last_comment_kind: "progress" })), null, "routine kinds never badge");
  assert.equal(mod.attentionBadgeKind(task("t")), null, "no thread never badges");
});

test("board view: toggleStalledFilter inverts the filter through the root field", () => {
  const state = makeState({ filterStalled: false });
  const mod = createBoardViewModule(makeBoardViewDeps(state));
  mod.toggleStalledFilter();
  assert.equal(state.filterStalled, true);
  mod.toggleStalledFilter();
  assert.equal(state.filterStalled, false);
});

// ===========================================================================
// modules/board-view.ts — URL query sync
// ===========================================================================

test("board view: buildBoardQueryString/boardUrl serialize active state; no filters → just the project", () => {
  const state = makeState({ selectedProjectId: "P" });
  const mod = createBoardViewModule(makeBoardViewDeps(state));
  // The selected project is always serialized, so a filter-free board URL is
  // /?project=P — never bare /.
  assert.equal(mod.buildBoardQueryString(), "?project=P");
  assert.equal(mod.boardUrl(), "/?project=P");

  state.filterPriority = "3";
  state.filterStalled = true;
  state.filterSearch = "term";
  state.sortKey = "title";
  state.sortDir = "asc";
  assert.equal(
    mod.buildBoardQueryString(),
    "?project=P&priority=3&stalled=1&q=term&sort=title&dir=asc",
  );
  assert.equal(mod.boardUrl(), "/?project=P&priority=3&stalled=1&q=term&sort=title&dir=asc");
});

test("board view: applyQueryParams on popstate restores EVERY field (missing ones reset to defaults)", () => {
  const prevSort = safeStorage.getItem(LS_SORT);
  try {
    const state = makeState({
      selectedProjectId: "OLD",
      filterPriority: "5",
      filterAssignee: "Old",
      filterDue: "week",
      filterUnblocked: true,
      filterStalled: true,
      filterNeedsAttention: true,
      filterSearch: "old",
      sortKey: "created_at",
    });
    const mod = createBoardViewModule(makeBoardViewDeps(state));

    // Popstate with ONLY project+q set: every other dimension resets.
    mod.applyQueryParams({ project: "Q", q: "fresh" }, true);

    assert.equal(state.selectedProjectId, "Q");
    assert.equal(state.filterSearch, "fresh");
    assert.equal(state.filterPriority, "", "absent priority reset on popstate");
    assert.equal(state.filterAssignee, "");
    assert.equal(state.filterDue, "");
    assert.equal(state.filterUnblocked, false);
    assert.equal(state.filterStalled, false);
    assert.equal(state.filterNeedsAttention, false);

    // No sort in the URL → the persisted preference is restored.
    persistSort("updated_at", "asc");
    mod.applyQueryParams({ q: "x" }, true);
    assert.equal(state.sortKey, "updated_at", "popstate with no sort param re-applies the stored preference");
    assert.equal(state.sortDir, "asc");
    assert.equal(mod.isRestoringUrlState(), false, "restore flag always cleared afterward");
  } finally {
    if (prevSort === null) safeStorage.removeItem(LS_SORT);
    else safeStorage.setItem(LS_SORT, prevSort);
  }
});

test("board view: applyQueryParams without popstate leaves absent fields untouched (initial-URL partial restore)", () => {
  const state = makeState({ filterPriority: "5", filterSearch: "kept" });
  const mod = createBoardViewModule(makeBoardViewDeps(state));
  mod.applyQueryParams({ stalled: true }, false);
  assert.equal(state.filterStalled, true);
  assert.equal(state.filterPriority, "5", "absent fields survive a non-popstate apply");
  assert.equal(state.filterSearch, "kept");
});

test("board view: initFromUrl reads the boot URL (and is a no-op without one)", () => {
  const state = makeState({ selectedProjectId: "" });
  const mod = createBoardViewModule(makeBoardViewDeps(state));

  withWindow({ location: { search: "?project=Q&stalled=1" } }, () => mod.initFromUrl());
  assert.equal(state.selectedProjectId, "Q");
  assert.equal(state.filterStalled, true);

  const bare = makeState();
  createBoardViewModule(makeBoardViewDeps(bare)).initFromUrl(); // no window at all → silent no-op
  assert.equal(bare.selectedProjectId, "P");
});

test("board view: syncUrlState writes the query via replaceState (immediate + debounced) and never on task routes", async () => {
  const state = makeState({ selectedProjectId: "P" });
  let url = "";
  let replaces = 0;
  // Faithful location: a real browser's replaceState updates window.location,
  // and syncUrlState's dedup check reads it back — so the fake must too.
  const loc = { pathname: "/", search: "", hash: "" };
  const applyUrl = (newUrl: string) => {
    const h = newUrl.includes("#") ? newUrl.slice(newUrl.indexOf("#")) : "";
    let rest = h ? newUrl.slice(0, newUrl.indexOf("#")) : newUrl;
    loc.pathname = rest.includes("?") ? rest.slice(0, rest.indexOf("?")) || "/" : rest || "/";
    loc.search = rest.includes("?") ? rest.slice(rest.indexOf("?")) : "";
    loc.hash = h;
  };
  const fakeWin = {
    location: loc,
    history: {
      replaceState(_s: unknown, _t: string, newUrl: string) {
        replaces++;
        url = newUrl;
        applyUrl(newUrl);
      },
    },
  };

  // The fake window must stay installed across the debounce await — doSync
  // reads `window` when the timer fires.
  await withWindow(fakeWin, async () => {
    const mod = createBoardViewModule(makeBoardViewDeps(state));

    state.filterSearch = "term";
    mod.syncUrlState(true);
    assert.equal(replaces, 1, "immediate sync writes synchronously");
    assert.equal(url, "/?project=P&q=term");

    // Same URL again → deduped (no second write).
    mod.syncUrlState(true);
    assert.equal(replaces, 1, "identical URL does not re-write");

    // Debounced: coalesced one write after the quiet window.
    state.filterSearch = "term2";
    mod.syncUrlState();
    mod.syncUrlState();
    assert.equal(replaces, 1, "debounced writes are pending, not immediate");
    await new Promise((r) => setTimeout(r, 200)); // > the module's 150ms window
  });
  assert.equal(replaces, 2);
  assert.equal(url, "/?project=P&q=term2");

  // Full-page task route: board URL sync must stay out of the way.
  const taskWin = {
    location: { pathname: "/tasks/abc", search: "", hash: "" },
    history: { replaceState() { throw new Error("must not write on a task route"); } },
  };
  withWindow(taskWin, () => {
    createBoardViewModule(makeBoardViewDeps(state)).syncUrlState(true);
  });
});

// ===========================================================================
// modules/board-view.ts — listTasksParams mapping
// ===========================================================================

test("board view: listTasksParams maps the filter state onto the server query (priority number, unassigned dropped, due windows)", () => {
  const mk = (over: Partial<FakeState>) => createBoardViewModule(makeBoardViewDeps(makeState(over)));

  assert.deepEqual(mk({}).listTasksParams(), { project_id: "P" }, "no filters → bare project query");

  const p = mk({ filterPriority: "3", filterAssignee: "Ada", filterUnblocked: true, filterSearch: "  x  " }).listTasksParams();
  assert.equal(p.priority, 3, "select value parsed to a number");
  assert.equal(p.assignee_name, "Ada");
  assert.equal(p.unblocked, true);
  assert.equal(p.search, "x", "search trimmed");

  const un = mk({ filterAssignee: "unassigned" }).listTasksParams();
  assert.equal(un.assignee_name, undefined, '"unassigned" is a client-side dimension — never sent');

  const overdue = mk({ filterDue: "overdue" }).listTasksParams();
  assert.ok(overdue.due_before && Number.isFinite(Date.parse(overdue.due_before)), "overdue → now window");
  const week = mk({ filterDue: "week" }).listTasksParams();
  assert.ok(week.due_before! > overdue.due_before!, "week window extends past now");
});

// ===========================================================================
// modules/board-view.ts — counts, empty-state banner, columns
// ===========================================================================

test("board view: status/visible/hidden counts derive from the board snapshot", () => {
  const state = makeState({
    board: board("P", {
      todo: [task("t1", { title: "alpha one" }), task("t2", { title: "alpha two" })],
      doing: [task("d1", { status: "doing", title: "beta" })],
      done: [task("x1", { status: "done", title: "gamma" }), task("x2", { status: "done", title: "delta" })],
    }),
  });
  const mod = createBoardViewModule(makeBoardViewDeps(state));

  assert.deepEqual(mod.statusCounts(), { todo: 2, doing: 1, done: 2 });
  assert.equal(mod.totalTaskCount(), 5);
  assert.equal(mod.visibleTaskCount(), 5);
  assert.equal(mod.hiddenTaskCount(), 0);

  state.filterSearch = "alpha"; // only the two alpha titles match — no full-text ids
  assert.equal(mod.visibleTaskCount(), 2);
  assert.equal(mod.hiddenTaskCount(), 3, "hidden = total - visible");
});

test("board view: showFilteredEmptyState requires active filters + a non-empty board that renders nothing", () => {
  const withFilters: Hooks = { hasActiveFilters: () => true };
  const board5 = board("P", { todo: [task("t1"), task("t2")] });

  const banner = (over: Partial<FakeState> = {}, hooks: Hooks = withFilters) =>
    createBoardViewModule(makeBoardViewDeps(makeState(over), hooks));

  assert.equal(banner({ board: board5, filterSearch: "zzz" }).showFilteredEmptyState(), true, "filtered to nothing");
  assert.equal(banner({ board: board5 }, { hasActiveFilters: () => false }).showFilteredEmptyState(), false, "no filters → no banner");
  assert.equal(banner({}, withFilters).showFilteredEmptyState(), false, "truly empty project keeps the plain empty look");
  assert.equal(banner({ board: board5, filterSearch: "zzz", showArchived: true }).showFilteredEmptyState(), false, "archived view never banners");
});

test("board view: columns union local title matches with server full-text ids and bypass the done cap while searching (AC1)", () => {
  const doneTasks = Array.from({ length: 25 }, (_, i) =>
    task("d" + String(i).padStart(2, "0"), { status: "done", completed_at: `2026-01-${String(i + 1).padStart(2, "0")}T00:00:00Z` }),
  );
  const ftm = new Set<string>();
  const state = makeState({ board: board("P", { done: doneTasks }) });
  const mod = createBoardViewModule(makeBoardViewDeps(state, { fullTextMatchIds: () => ftm }));

  const before = mod.columns().find((c) => c.status === "done")!;
  assert.equal(before.tasks.length, 20, "normal cap applies unfiltered");
  assert.equal(before.totalCount, 25);

  state.filterSearch = "ancient-term"; // title never matches…
  ftm.add("d00"); // …the server matched the OLDEST done task via notes
  const searching = mod.columns().find((c) => c.status === "done")!;
  assert.equal(searching.totalCount, 1);
  assert.deepEqual(searching.tasks.map((t) => t.id), ["d00"], "cap bypassed — no match hides behind the cut");

  state.filterSearch = "";
  ftm.clear();
  const after = mod.columns().find((c) => c.status === "done")!;
  assert.equal(after.tasks.length, 20, "normal cap restored on clear");
});

test("board view: show archived swaps the data source only (same filters and sort)", () => {
  const live = board("P", { todo: [task("live1")] });
  const archivedRows = { todo: [task("arch1")], doing: [], done: [] };
  const state = makeState({ board: live, showArchived: true, archived: archivedRows });
  const mod = createBoardViewModule(makeBoardViewDeps(state));

  const cols = mod.columns();
  assert.deepEqual(cols.find((c) => c.status === "todo")!.tasks.map((t) => t.id), ["arch1"], "archived rows replace the live board");

  // The same client filters still apply to the archived source.
  state.filterSearch = "live";
  assert.equal(mod.columns().find((c) => c.status === "todo")!.tasks.length, 0);
});

// ===========================================================================
// modules/board-view.ts — loadBoard lifecycle
// ===========================================================================

test("board view: loadBoard (all scope) fetches getBoard, applies it, and syncs blocked state", async () => {
  const origGetBoard = api.getBoard;
  try {
    let got: string[] = [];
    (api as any).getBoard = async (pid: string) => { got.push(pid); return board(pid, { todo: [task("b1")] }); };

    const state = makeState({ selectedProjectId: "B" });
    const synced: Board[] = [];
    const mod = createBoardViewModule(makeBoardViewDeps(state, { syncBlocked: (b) => synced.push(b) }));

    await mod.loadBoard();

    assert.deepEqual(got, ["B"]);
    assert.equal(state.board?.project_id, "B");
    assert.equal(synced.length, 1, "blocked state re-derived from every fresh snapshot");
    assert.equal(synced[0].project_id, "B");
  } finally {
    (api as any).getBoard = origGetBoard;
  }
});

test("board view: loadBoard (mine scope) reshapes the flat listTasks result into a board", async () => {
  const origListTasks = api.listTasks;
  const origGetBoard = api.getBoard;
  try {
    let gotMine: any = null;
    let getBoardCalled = false;
    (api as any).listTasks = async (params: any) => {
      gotMine = params;
      return { tasks: [task("m1"), task("m2", { status: "doing" })], total: 2, limit: 100, offset: 0 };
    };
    (api as any).getBoard = async () => { getBoardCalled = true; throw new Error("must not be used in scoped mode"); };

    const state = makeState({ selectedProjectId: "B", taskScope: "assigned" });
    const mod = createBoardViewModule(makeBoardViewDeps(state));

    await mod.loadBoard();

    assert.equal(getBoardCalled, false, "scoped fetch never hits getBoard");
    assert.equal(gotMine.mine, "assigned");
    assert.equal(gotMine.project_id, "B");
    assert.deepEqual(state.board?.project_id, "B");
    const b = state.board!;
    assert.equal(b.tasks.todo.length, 1);
    assert.equal(b.tasks.doing.length, 1, "rows reshaped into the status buckets");
  } finally {
    (api as any).listTasks = origListTasks;
    (api as any).getBoard = origGetBoard;
  }
});

test("board view: a stale or invalidated board load never overwrites current state", async () => {
  const origGetBoard = api.getBoard;
  try {
    let releaseFirst: (v: any) => void;
    const first = new Promise<any>((r) => { releaseFirst = r; });
    let calls = 0;
    (api as any).getBoard = async () => {
      calls++;
      if (calls === 1) return first; // slow, will be invalidated…
      return board("C", {}); // newer load for the project we actually are on now
    };

    const state = makeState({ selectedProjectId: "B" });
    const mod = createBoardViewModule(makeBoardViewDeps(state));

    const pSlow = mod.loadBoard(); // token N (project B)
    mod.invalidatePendingLoads(); // project-switch seam bumps the sequence…
    state.selectedProjectId = "C";
    const pNew = mod.loadBoard(); // …token N+1, current selection
    await pNew;
    assert.equal(state.board?.project_id, "C", "newest load applied");

    releaseFirst!({ ...board("B", {}), project_id: "B" });
    await pSlow;
    assert.equal(state.board?.project_id, "C", "stale/invalidated payload discarded");
  } finally {
    (api as any).getBoard = origGetBoard;
  }
});

test("board view: a 401 board read opens the auth modal; a 403 shows the no-access state without it", async () => {
  const origGetBoard = api.getBoard;
  try {
    const mk = (thrown: unknown, over: Partial<FakeState> = {}) => {
      let authModal = 0;
      const state = makeState(over);
      (api as any).getBoard = async () => { throw thrown; };
      return { state, mod: createBoardViewModule(makeBoardViewDeps(state, { openAuthModal: () => authModal++ })), get: () => authModal };
    };

    const unauthorized = mk(new ApiError(401, { error: "unauthorized", message: "no" }), { board: board("B", {}) });
    await unauthorized.mod.loadBoard();
    assert.equal(unauthorized.get(), 1, "401 → auth modal");
    assert.equal(unauthorized.state.board?.project_id, "B", "board kept — the failure is about credentials, not data");

    const forbidden = mk(new ApiError(403, { error: "forbidden", message: "no" }));
    await forbidden.mod.loadBoard();
    assert.equal(forbidden.get(), 0, "403 → no auth modal (it's a membership issue)");
    assert.equal(forbidden.state.board, null, "board cleared into the one-shot no-access state");

    const plain = mk(new Error("network down"));
    await plain.mod.loadBoard();
    assert.equal(plain.get(), 0);
  } finally {
    (api as any).getBoard = origGetBoard;
  }
});
