import test from "node:test";
import assert from "node:assert/strict";

import {
  createProjectsOverviewModule,
  filterAndSortOverviewProjects,
  OVERVIEW_SORT_OPTIONS,
  projectCompletionStats,
  type ProjectsOverviewDeps,
  type ProjectsOverviewSort,
} from "../src/modules/projects-overview.ts";
import type { ProjectOverviewRow } from "../src/types.ts";
import { withFetch } from "./test-helpers.ts";

function captureFetch(
  calls: Array<{ url: string; init?: any }>,
  resFactory?: (url: string, init?: any) => Response | Promise<Response>,
): (url: string | URL, init?: any) => Promise<Response> {
  return async (url: string | URL, init?: any) => {
    calls.push({ url: String(url), init });
    if (resFactory) return resFactory(String(url), init);
    return new Response(JSON.stringify({ projects: [], total: 0 }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
}

function jsonResp(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function makeRow(over: Partial<ProjectOverviewRow> & { id: string; name: string }): ProjectOverviewRow {
  return {
    description: "",
    archived: false,
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-10T00:00:00Z",
    task_counts: { todo: 0, doing: 0, done: 0 },
    health: {
      active_claims: 0,
      expired_claims: 0,
      stalled: 0,
      needs_attention: 0,
      last_activity_at: "2026-11-10T00:00:00Z",
    },
    ...over,
  };
}

function makeHarness(rootState?: Record<string, any>) {
  const s: Record<string, any> = {
    overviewProjects: [] as ProjectOverviewRow[],
    overviewLoading: false,
    overviewError: "",
    overviewArchived: false,
    overviewSearch: "",
    overviewSort: "recent" as ProjectsOverviewSort,
    myProjectsOnly: false,
    identity: null,
    ...rootState,
  };
  const calls: string[] = [];
  const deps: ProjectsOverviewDeps = {
    overviewProjects: () => s.overviewProjects,
    overviewLoading: () => s.overviewLoading,
    overviewError: () => s.overviewError,
    overviewArchived: () => s.overviewArchived,
    overviewSearch: () => s.overviewSearch,
    overviewSort: () => s.overviewSort,
    myProjectsOnly: () => s.myProjectsOnly,
    identity: () => s.identity,
    setOverviewProjects: (rows) => { s.overviewProjects = rows; },
    setOverviewLoading: (loading) => { s.overviewLoading = loading; },
    setOverviewError: (err) => { s.overviewError = err; },
    setOverviewArchived: (archived) => { s.overviewArchived = archived; },
    setOverviewSearch: (search) => { s.overviewSearch = search; },
    setOverviewSort: (sort) => { s.overviewSort = sort; },
    openAuthModal: () => { calls.push("openAuthModal"); },
  };
  return { s, calls, deps, mod: createProjectsOverviewModule(deps) };
}

test("ProjectsOverview: OVERVIEW_SORT_OPTIONS exposes the 4 canonical sort modes", () => {
  assert.deepEqual(
    OVERVIEW_SORT_OPTIONS.map((o) => o.value),
    ["recent", "open_work", "name", "completion"],
  );
});

test("ProjectsOverview: projectCompletionStats computes totals, openCount, and rounded pct", () => {
  assert.deepEqual(projectCompletionStats(null), {
    total: 0,
    todo: 0,
    doing: 0,
    done: 0,
    openCount: 0,
    pct: 0,
  });

  assert.deepEqual(
    projectCompletionStats(makeRow({ id: "p1", name: "Empty" })),
    { total: 0, todo: 0, doing: 0, done: 0, openCount: 0, pct: 0 },
  );

  assert.deepEqual(
    projectCompletionStats(
      makeRow({ id: "p2", name: "Active", task_counts: { todo: 1, doing: 2, done: 4 } }),
    ),
    { total: 7, todo: 1, doing: 2, done: 4, openCount: 3, pct: 57 },
  );

  assert.deepEqual(
    projectCompletionStats(
      makeRow({ id: "p3", name: "Done", task_counts: { todo: 0, doing: 0, done: 3 } }),
    ),
    { total: 3, todo: 0, doing: 0, done: 3, openCount: 0, pct: 100 },
  );
});

test("ProjectsOverview: filterAndSortOverviewProjects filters over name + description case-insensitively", () => {
  const rows = [
    makeRow({ id: "1", name: "Alpha Platform", description: "Core gateway" }),
    makeRow({ id: "2", name: "Billing Service", description: "Invoice pipeline for Alpha" }),
    makeRow({ id: "3", name: "Catalog", description: "Search index" }),
  ];

  const byNameOrDesc = filterAndSortOverviewProjects(rows, { search: "  ALPHA ", sort: "name" });
  assert.deepEqual(byNameOrDesc.map((r) => r.id), ["1", "2"]);

  const noMatch = filterAndSortOverviewProjects(rows, { search: "nonexistent" });
  assert.deepEqual(noMatch, []);

  const emptySearch = filterAndSortOverviewProjects(rows, { search: "   ", sort: "name" });
  assert.deepEqual(emptySearch.map((r) => r.id), ["1", "2", "3"]);
});

test("ProjectsOverview: filterAndSortOverviewProjects sorts by recent, open_work, name, completion with tiebreaks", () => {
  const alphaOld = makeRow({
    id: "alpha-old",
    name: "Alpha",
    task_counts: { todo: 1, doing: 1, done: 2 }, // open=2, total=4, pct=50
    health: { active_claims: 0, expired_claims: 0, stalled: 0, needs_attention: 0, last_activity_at: "2026-11-10T10:00:00Z" },
  });
  const betaTieRecent = makeRow({
    id: "beta-tie",
    name: "Beta",
    task_counts: { todo: 4, doing: 1, done: 5 }, // open=5, total=10, pct=50
    health: { active_claims: 1, expired_claims: 0, stalled: 0, needs_attention: 0, last_activity_at: "2026-11-12T10:00:00Z" },
  });
  const gammaTieRecent = makeRow({
    id: "gamma-tie",
    name: "Gamma",
    task_counts: { todo: 5, doing: 0, done: 0 }, // open=5, total=5, pct=0
    health: { active_claims: 0, expired_claims: 0, stalled: 0, needs_attention: 0, last_activity_at: "2026-11-12T10:00:00Z" },
  });
  const deltaComplete = makeRow({
    id: "delta-done",
    name: "Delta",
    task_counts: { todo: 0, doing: 0, done: 3 }, // open=0, total=3, pct=100
    health: { active_claims: 0, expired_claims: 0, stalled: 0, needs_attention: 0, last_activity_at: "2026-11-11T10:00:00Z" },
  });

  const input = [gammaTieRecent, alphaOld, deltaComplete, betaTieRecent];

  // recent: last_activity_at desc, tiebreak name asc
  assert.deepEqual(
    filterAndSortOverviewProjects(input, { sort: "recent" }).map((r) => r.id),
    ["beta-tie", "gamma-tie", "delta-done", "alpha-old"],
  );

  // open_work: (todo+doing) desc, tiebreak last_activity_at desc
  const betaOlderOpen = makeRow({
    ...betaTieRecent,
    health: { ...betaTieRecent.health, last_activity_at: "2026-11-09T10:00:00Z" },
  });
  assert.deepEqual(
    filterAndSortOverviewProjects([betaOlderOpen, gammaTieRecent, alphaOld, deltaComplete], { sort: "open_work" }).map((r) => r.id),
    ["gamma-tie", "beta-tie", "alpha-old", "delta-done"],
  );

  // name: localeCompare asc
  assert.deepEqual(
    filterAndSortOverviewProjects(input, { sort: "name" }).map((r) => r.id),
    ["alpha-old", "beta-tie", "delta-done", "gamma-tie"],
  );

  // completion: pct desc, tiebreak total desc
  assert.deepEqual(
    filterAndSortOverviewProjects(input, { sort: "completion" }).map((r) => r.id),
    ["delta-done", "beta-tie", "alpha-old", "gamma-tie"],
  );

  // invalid timestamp falls back cleanly to 0
  const badTs = makeRow({
    id: "bad-ts",
    name: "Zeta",
    updated_at: "not-a-date",
    health: { active_claims: 0, expired_claims: 0, stalled: 0, needs_attention: 0, last_activity_at: "invalid" },
  });
  assert.deepEqual(
    filterAndSortOverviewProjects([badTs, alphaOld], { sort: "recent" }).map((r) => r.id),
    ["alpha-old", "bad-ts"],
  );
});

test("ProjectsOverview: loadOverview fetches /projects/overview, respects mine/archived, and updates state", async () => {
  const row = makeRow({ id: "p1", name: "Alpha" });
  const h = makeHarness({
    myProjectsOnly: true,
    identity: { id: "id-1", name: "ada", kind: "human", auth_method: "password", created_at: "2026-11-01T00:00:00Z" },
    overviewError: "stale error",
  });
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, () => jsonResp(200, { projects: [row], total: 1 })),
    () => h.mod.loadOverview(),
  );
  assert.deepEqual(calls.map((c) => c.url), ["/api/v1/projects/overview?mine=created"]);
  assert.deepEqual(h.s.overviewProjects, [row]);
  assert.equal(h.s.overviewLoading, false);
  assert.equal(h.s.overviewError, "");
  assert.deepEqual(h.mod.filteredProjects(), [row]);
});

test("ProjectsOverview: toggleArchived & setArchived pass ?archived=true without ?mine", async () => {
  const archivedRow = makeRow({ id: "a1", name: "Archived One", archived: true });
  const h = makeHarness({
    overviewProjects: [makeRow({ id: "p1", name: "Active" })],
    myProjectsOnly: true,
    identity: { id: "id-1", name: "ada", kind: "human", auth_method: "password", created_at: "2026-11-01T00:00:00Z" },
  });
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, () => jsonResp(200, { projects: [archivedRow], total: 1 })),
    () => h.mod.toggleArchived(),
  );
  assert.equal(h.s.overviewArchived, true);
  assert.deepEqual(calls.map((c) => c.url), ["/api/v1/projects/overview?archived=true"]);
  assert.deepEqual(h.s.overviewProjects, [archivedRow]);
});

test("ProjectsOverview: sequence guard drops stale responses from earlier loadOverview calls", async () => {
  const h = makeHarness();
  let resolveFirst!: (r: Response) => void;
  const firstPending = new Promise<Response>((r) => { resolveFirst = r; });
  const rowOld = makeRow({ id: "old", name: "Old" });
  const rowNew = makeRow({ id: "new", name: "New" });
  let count = 0;

  await withFetch(
    async () => {
      count++;
      if (count === 1) return firstPending;
      return jsonResp(200, { projects: [rowNew], total: 1 });
    },
    async () => {
      const p1 = h.mod.loadOverview();
      const p2 = h.mod.loadOverview();
      await p2;
      assert.deepEqual(h.s.overviewProjects, [rowNew]);
      resolveFirst(jsonResp(200, { projects: [rowOld], total: 1 }));
      await p1;
      assert.deepEqual(h.s.overviewProjects, [rowNew], "stale first response must not overwrite newer data");
    },
  );
});

test("ProjectsOverview: 401 calls openAuthModal and sets overviewError; silent refresh keeps existing rows on error", async () => {
  const h = makeHarness();
  await withFetch(
    captureFetch([], () => jsonResp(401, { error: "unauthorized", message: "token required" })),
    () => h.mod.loadOverview(false),
  );
  assert.deepEqual(h.calls, ["openAuthModal"]);
  assert.equal(h.s.overviewError, "token required");
  assert.equal(h.s.overviewLoading, false);

  const existing = makeRow({ id: "p1", name: "Existing" });
  const h2 = makeHarness({ overviewProjects: [existing], overviewError: "" });
  await withFetch(
    captureFetch([], () => jsonResp(500, { error: "internal", message: "boom" })),
    () => h2.mod.loadOverview(true),
  );
  assert.deepEqual(h2.s.overviewProjects, [existing]);
  assert.equal(h2.s.overviewError, "", "silent refresh with cached rows keeps error empty");
});
