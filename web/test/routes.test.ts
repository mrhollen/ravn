// Unit tests for src/routes.ts (Phase 1 extraction from app.ts): full-page task
// route parse/build, board query-string round-tripping with hostile inputs, and
// the mobile viewport breakpoint. Pure — no DOM required.

import test from "node:test";
import assert from "node:assert/strict";

import {
  MOBILE_DETAIL_MAX_WIDTH,
  buildBoardQueryString,
  isMobileDetailViewport,
  parseBoardQueryParams,
  taskRouteIdFromPath,
  taskRoutePath,
} from "../src/routes.ts";

test("routes: taskRouteIdFromPath — valid routes decode, everything else is null", () => {
  assert.equal(taskRouteIdFromPath("/tasks/01J3ZK8WQ9X7M2P4R6S8T0U2V4"), "01J3ZK8WQ9X7M2P4R6S8T0U2V4");
  assert.equal(taskRouteIdFromPath("/tasks/%30%31ABC"), "01ABC"); // percent-encoded
  assert.equal(taskRouteIdFromPath(""), null); // board route
  assert.equal(taskRouteIdFromPath("/"), null);
  assert.equal(taskRouteIdFromPath("/projects"), null);
  assert.equal(taskRouteIdFromPath("/tasks/"), null); // empty id segment
  assert.equal(taskRouteIdFromPath("/tasks/a/b"), null); // nested slash
  assert.equal(taskRouteIdFromPath("/task/x"), null); // not the exact prefix
  assert.equal(taskRouteIdFromPath("/tasks/%20%20"), null); // whitespace-only id
});

test("routes: taskRouteIdFromPath — malformed %-sequence falls back to the raw segment", () => {
  // "%zz" is an invalid escape; decodeURIComponent throws and the parser must
  // NOT crash — it returns the raw segment instead.
  assert.equal(taskRouteIdFromPath("/tasks/%zz1"), "%zz1");
});

test("routes: taskRoutePath encodes the id and pins the /tasks/ prefix", () => {
  assert.equal(taskRoutePath("abc-123"), "/tasks/abc-123");
  assert.equal(taskRoutePath("a b/c?d"), "/tasks/a%20b%2Fc%3Fd");
});

test("routes: parseBoardQueryParams — full valid payload round-trips", () => {
  const p = parseBoardQueryParams(
    "?project=proj-1&priority=3&owner=ada&due=today&unblocked=1&stalled=true&attention=true&q=hello%20world&sort=due_at&dir=asc"
  );
  assert.deepEqual(p, {
    project: "proj-1",
    priority: "3",
    owner: "ada",
    due: "today",
    unblocked: true,
    stalled: true,
    attention: true,
    q: "hello world",
    sort: "due_at",
    dir: "asc",
  });
});

test("routes: parseBoardQueryParams — empty / trivial searches yield no params", () => {
  assert.deepEqual(parseBoardQueryParams(""), {});
  assert.deepEqual(parseBoardQueryParams("?"), {});
  assert.deepEqual(parseBoardQueryParams("project=%20%20&priority=&q="), {}); // whitespace/empty values
});

test("routes: parseBoardQueryParams — hostile or out-of-range values are dropped", () => {
  const p = parseBoardQueryParams(
    "?project=p&priority=all&owner=ALL&due=bogus&unblocked=0&stalled=false&attention=no&q=%20&sort=bogus&dir=sideways"
  );
  assert.deepEqual(p, { project: "p" }); // only the clean value survives

  // priority outside 0..5 or non-numeric → dropped (checked per-value)
  assert.deepEqual(parseBoardQueryParams("?priority=9"), {});
  assert.deepEqual(parseBoardQueryParams("?priority=-1"), {});
  assert.deepEqual(parseBoardQueryParams("?priority=abc"), {});
  // priority=0 is VALID (P0 exists) — must survive
  assert.deepEqual(parseBoardQueryParams("?priority=0"), { priority: "0" });
  // sort without a dir / unknown dir never leak
  assert.deepEqual(parseBoardQueryParams("?sort=priority&dir=bogus"), { sort: "priority" });
});

test("routes: buildBoardQueryString — empty state is '', only active fields serialize", () => {
  assert.equal(buildBoardQueryString({}), "");
  assert.equal(buildBoardQueryString({ project: "", q: "   " }), ""); // blank values skipped
  const s = buildBoardQueryString({ project: " p1 ", priority: "2", owner: " ada ", due: "WEEK", unblocked: true, stalled: true, attention: true, q: " hi ", sort: "priority", dir: "desc" });
  assert.equal(s, "?project=p1&priority=2&owner=ada&due=week&unblocked=1&stalled=1&attention=1&q=hi&sort=priority&dir=desc");
});

test("routes: buildBoardQueryString — 'all' priority and orphan dir are omitted", () => {
  assert.equal(buildBoardQueryString({ priority: "all" }), "");
  assert.equal(buildBoardQueryString({ owner: "All" }), "?owner=All"); // builder compares owner case-sensitively (parse lowercases)
  assert.equal(buildBoardQueryString({ dir: "asc" }), ""); // dir without sort is dropped
});

test("routes: parse(build(x)) round-trips a canonical param set", () => {
  const full = {
    project: "proj-9",
    priority: "1",
    owner: "grace",
    due: "week",
    unblocked: true,
    stalled: true,
    attention: true,
    q: "fix login",
    sort: "created_at",
    dir: "asc",
  } as const;
  assert.deepEqual(parseBoardQueryParams(buildBoardQueryString(full)), { ...full });
});

test("routes: mobile breakpoint — pinned at md (768px), exclusive below, desktop-safe default", () => {
  assert.equal(MOBILE_DETAIL_MAX_WIDTH, 768);
  assert.equal(isMobileDetailViewport(375), true); // phone
  assert.equal(isMobileDetailViewport(767), true); // just under md
  assert.equal(isMobileDetailViewport(768), false); // AT md → modal/desktop path
  assert.equal(isMobileDetailViewport(1440), false);
  // No explicit width and no window (Node): treated as infinite → desktop path.
  if (typeof window === "undefined") {
    assert.equal(isMobileDetailViewport(), false);
  }
});
