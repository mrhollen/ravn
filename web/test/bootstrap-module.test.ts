// Unit tests for web/src/modules/bootstrap.ts (Phase 2 bootstrap hatch).
// Drives createBootstrapModule() with stub deps — no DOM by default (the
// window/matchMedia/$watch branches are exercised through small fake windows),
// no network. The root-level surface (init/retryBoot/continueBoot shims, the
// Alpine double-init memo against the real component) stays covered in
// test/app.test.ts; this file covers the moved internals: boot sequencing and
// ordering, the success-path continuation, the loadError screen, and the
// retryBoot state machine.

import test from "node:test";
import assert from "node:assert/strict";
import { createBootstrapModule, type BootstrapDeps } from "../src/modules/bootstrap";
import { createDebounce } from "../src/refresh";
import type { ThemeMode } from "../src/theme";
import type { ProjectWithCounts } from "../src/types";

// ------------------------------------------------------------------- helpers
function proj(id: string): ProjectWithCounts {
  return { id, name: "P" + id } as unknown as ProjectWithCounts;
}

interface Fixture {
  deps: BootstrapDeps;
  state: {
    selectedProjectId: string;
    projects: ProjectWithCounts[];
    loadError: string;
    bootError: boolean;
    bootRetrying: boolean;
    polling: boolean;
    theme: ThemeMode;
    filterUnblocked: boolean;
    filterBlocked: boolean;
  };
  /** One entry per hook invocation, in call order — for sequencing asserts. */
  calls: string[];
  watchProps: string[];
  watchCallbacks: Map<string, () => void>;
  tickerId: { v: ReturnType<typeof setInterval> | null };
  debounce: { v: ReturnType<typeof createDebounce> | null };
  out: Record<string, boolean>;
}

interface DepsOverrides {
  /** Run right when loadProjects() resolves — flip state before boot continues. */
  afterLoadProjects?: (state: Fixture["state"]) => void;
  isRestoringUrlState?: () => boolean;
}

/** Build a recording deps object. Every hook appends to calls[] so tests can
 *  assert exact sequencing. The real node setInterval is used for the ticker
 *  (the module unrefs it, so it never holds the event loop); cleanup clears
 *  it through the mirrored id. */
function makeFixture(overrides: DepsOverrides = {}): Fixture {
  const state: Fixture["state"] = {
    selectedProjectId: "stored-stale",
    projects: [proj("p-1"), proj("p-2")],
    loadError: "",
    bootError: false,
    bootRetrying: false,
    polling: true,
    theme: "system",
    filterUnblocked: false,
    filterBlocked: false,
  };
  const calls: string[] = [];
  const watchProps: string[] = [];
  const watchCallbacks = new Map<string, () => void>();
  const tickerId: Fixture["tickerId"] = { v: null };
  const debounce: Fixture["debounce"] = { v: null };
  const out: Record<string, boolean> = {};

  const deps: BootstrapDeps = {
    // state-in
    selectedProjectId: () => state.selectedProjectId,
    projects: () => state.projects,
    loadError: () => state.loadError,
    bootError: () => state.bootError,
    bootRetrying: () => state.bootRetrying,
    polling: () => state.polling,
    themeMq: () => null,
    theme: () => state.theme,
    filterUnblocked: () => state.filterUnblocked,
    filterBlocked: () => state.filterBlocked,
    // state-out
    setAppBooted: (v) => { calls.push("setAppBooted"); out.appBooted = v; },
    setBootError: (v) => { calls.push("setBootError"); state.bootError = v; out.bootError = v; },
    setBootRetrying: (v) => { calls.push("setBootRetrying"); state.bootRetrying = v; out.bootRetrying = v; },
    setShowNewProject: (v) => { calls.push("setShowNewProject"); out.showNewProject = v; },
    setSelectedProjectId: (v) => { calls.push("setSelectedProjectId"); state.selectedProjectId = v; },
    setIsMobile: () => { calls.push("setIsMobile"); },
    setNowTick: () => { calls.push("setNowTick"); },
    setTickerId: (id) => { calls.push("setTickerId"); tickerId.v = id; },
    setSseReloadDebounce: (d) => { calls.push("setSseReloadDebounce"); debounce.v = d; },
    setFilterUnblocked: (b) => { state.filterUnblocked = b; },
    setFilterBlocked: (b) => { state.filterBlocked = b; },
    // hooks
    loadProjects: async (force) => {
      calls.push("loadProjects" + (force === true ? "(true)" : ""));
      overrides.afterLoadProjects?.(state);
    },
    loadBoard: async (force) => { calls.push("loadBoard" + (force === true ? "(true)" : "")); },
    refreshSessionIdentity: async () => { calls.push("refreshSessionIdentity"); },
    openDeepLinkedTask: async (id) => { calls.push("openDeepLinkedTask:" + id); },
    openDeepLinkedDashboard: async (pid, fromBootError) => {
      calls.push("openDeepLinkedDashboard:" + pid + (fromBootError ? "(bootError)" : ""));
    },
    connectSse: () => { calls.push("connectSse"); },
    startPolling: () => { calls.push("startPolling"); },
    disconnectSse: (resetBackoff) => { calls.push("disconnectSse" + (resetBackoff === true ? "(true)" : "")); },
    onPopState: () => { calls.push("onPopState"); },
    initFromUrl: () => { calls.push("initFromUrl"); },
    applyTheme: () => { calls.push("applyTheme"); },
    setDocumentTitle: () => { calls.push("setDocumentTitle"); },
    scheduleFullTextSearch: () => { calls.push("scheduleFullTextSearch"); },
    syncUrlState: (immediate) => { calls.push("syncUrlState" + (immediate === true ? "(true)" : "")); },
    isRestoringUrlState: () => overrides.isRestoringUrlState?.() ?? false,
    focusWhenVisible: (el) => { calls.push("focusWhenVisible:" + (el === null ? "null" : "el")); },
    watch: (prop, cb) => { calls.push("watch"); watchProps.push(prop); watchCallbacks.set(prop, cb); },
  };

  return { deps, state, calls, watchProps, watchCallbacks, tickerId, debounce, out };
}

/** Clear the real (unref'd) ticker mirrored onto fixture.tickerId. */
function clearTicker(f: Fixture): void {
  if (f.tickerId.v !== null) clearInterval(f.tickerId.v);
}

// ------------------------------------------------------------------ boot happy path

test("bootstrap: success path boots in order and falls back to the first project", async () => {
  const f = makeFixture();
  const m = createBootstrapModule(f.deps);
  await m.init();
  clearTicker(f);

  // Selection was stale → validated against the list and reset to the first one.
  assert.equal(f.state.selectedProjectId, "p-1", "stale stored selection falls back to projects[0]");
  const expectedCalls = [
    "setTickerId",
    "setSseReloadDebounce",
    // 14 watch registrations: the twelve URL-sync props + the two full-text-search watchers
    ...Array.from({ length: 14 }, () => "watch"),
    "applyTheme",
    "loadProjects",
    "refreshSessionIdentity",
    "setSelectedProjectId",
    "loadBoard",
    "connectSse",
    "scheduleFullTextSearch",
    "syncUrlState(true)",
    "startPolling",
    "setAppBooted",
  ];
  assert.deepEqual(f.calls, expectedCalls, "exact boot ordering (no window/document: no listeners, no doc title)");
  assert.equal(f.out.appBooted, true);
  assert.ok(f.tickerId.v !== null, "the ticker interval id is mirrored to the root field");
  assert.equal(typeof f.debounce.v?.schedule, "function",
    "the SSE-reload debounce is created and mirrored before any network work");
  // The URL-sync watchers: twelve props, filterSearch + selectedProjectId watched twice.
  assert.deepEqual(watchPropsSorted(f.watchProps), [
    "filterAssignee", "filterBlocked", "filterDue", "filterNeedsAttention", "filterPriority", "filterSearch",
    "filterSearch", "filterStalled", "filterStatus", "filterUnblocked", "selectedProjectId", "selectedProjectId",
    "sortDir", "sortKey",
  ]);
});

test("bootstrap: filterUnblocked and filterBlocked watchers enforce mutual exclusion unless restoring URL", async () => {
  let restoring = false;
  const f = makeFixture({ isRestoringUrlState: () => restoring });
  const m = createBootstrapModule(f.deps);
  await m.init();
  clearTicker(f);

  // Enabling filterUnblocked clears filterBlocked
  f.state.filterBlocked = true;
  f.state.filterUnblocked = true;
  f.watchCallbacks.get("filterUnblocked")?.();
  assert.equal(f.state.filterBlocked, false);

  // Enabling filterBlocked clears filterUnblocked
  f.state.filterBlocked = true;
  f.state.filterUnblocked = true;
  f.watchCallbacks.get("filterBlocked")?.();
  assert.equal(f.state.filterUnblocked, false);

  // No-op during URL restoration
  restoring = true;
  f.state.filterBlocked = true;
  f.state.filterUnblocked = true;
  const callsBefore = f.calls.length;
  f.watchCallbacks.get("filterUnblocked")?.();
  assert.equal(f.state.filterBlocked, true);
  assert.equal(f.calls.length, callsBefore);
});

function watchPropsSorted(xs: string[]): string[] { return [...xs].sort(); }

test("bootstrap: the init() memo returns the FIRST boot's promise on a second call", async () => {
  const f = makeFixture();
  const m = createBootstrapModule(f.deps);
  // (async-init surface promises are per-call wrappers; the shared inner
  //  boot promise is what the memo guarantees — assert via the accessor.)
  const p1 = m.init();
  const p2 = m.init(); // the Alpine double-init scenario
  assert.equal(m.bootPromise(), m.bootPromise(), "the memo holds one stable boot promise");
  await Promise.all([p1, p2]);
  clearTicker(f);
  assert.equal(f.calls.filter((c) => c === "loadProjects").length, 1, "boot ran exactly once");
});

test("bootstrap: empty project list invites creating a project (no board load, no SSE)", async () => {
  const f = makeFixture();
  f.state.projects = [];
  const m = createBootstrapModule(f.deps);
  await m.init();
  clearTicker(f);

  assert.equal(f.state.selectedProjectId, "", "falls back to the empty string");
  assert.equal(f.out.showNewProject, true, "the New Project bar is offered");
  assert.ok(!f.calls.includes("loadBoard"), "no board load with no project");
  assert.ok(!f.calls.includes("connectSse"), "no SSE with no project");
  assert.ok(f.calls.includes("syncUrlState(true)"), "URL state still synced at the end");
});

test("bootstrap: a failed projects fetch raises the boot-error screen and never validates selection", async () => {
  const f = makeFixture({
    afterLoadProjects: (s) => { s.loadError = "network down"; },
  });
  const m = createBootstrapModule(f.deps);
  await m.init();
  clearTicker(f);

  assert.equal(f.out.bootError, true, "boot-error screen is raised");
  assert.equal(f.state.selectedProjectId, "stored-stale",
    "the stored selection is NOT re-validated against the failed (empty) list");
  assert.ok(!f.calls.includes("loadBoard"), "no board load after a failed boot fetch");
  assert.ok(!f.calls.includes("connectSse"), "no SSE after a failed boot fetch");
  assert.ok(!f.calls.includes("setShowNewProject"), "no New Project bar — the error screen owns the failure");
  assert.ok(f.calls.includes("focusWhenVisible:null"),
    "focus is steered to Retry (null element under node: no document)");
  assert.equal(f.out.appBooted, true,
    "appBooted still flips last (parity with the original _runBoot tail) — the error screen needs it");
});

// --------------------------------------------------------------------- retryBoot

test("bootstrap: retryBoot() is a no-op without bootError and while already retrying", async () => {
  const f = makeFixture();
  const m = createBootstrapModule(f.deps);
  await m.retryBoot();
  assert.equal(f.calls.length, 0, "no work when there is nothing to retry");

  // In-flight guard: a hanging loadProjects keeps bootRetrying true.
  const g = makeFixture();
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => { release = r; });
  g.deps.loadProjects = async (force) => { g.calls.push("loadProjects" + (force === true ? "(true)" : "")); await gate; };
  const gm = createBootstrapModule(g.deps);
  g.state.bootError = true;
  const first = gm.retryBoot();
  assert.equal(g.state.bootRetrying, true, "retrial is in flight");
  await gm.retryBoot(); // must be swallowed by the bootRetrying guard
  release();
  await first;
  clearTicker(f);
  assert.equal(g.calls.filter((c) => c.startsWith("loadProjects")).length, 1,
    "the concurrent retry did not start a second projects load");
  assert.equal(g.state.bootRetrying, false, "bootRetrying clears in the finally");
});

test("bootstrap: retryBoot() keeps the error screen on a second failure and recovers on success", async () => {
  const f = makeFixture();
  let attempts = 0;
  f.deps.loadProjects = async (force) => {
    f.calls.push("loadProjects" + (force === true ? "(true)" : ""));
    attempts++;
    if (attempts <= 2) f.state.loadError = "still down";
    else f.state.loadError = "";
  };
  const m = createBootstrapModule(f.deps);

  // Failure #1 (boot) — establish the error screen.
  await m.init();
  clearTicker(f);
  assert.equal(f.out.bootError, true);

  // Retry attempt 1: loadProjects fails again → screen stays up.
  const staleSelection = f.state.selectedProjectId;
  await m.retryBoot();
  assert.equal(f.state.loadError, "still down");
  assert.equal(f.out.bootError, true, "a second failure keeps the boot-error screen");
  assert.equal(f.state.selectedProjectId, staleSelection, "the selection is left untouched on failure");
  assert.ok(!f.calls.includes("connectSse"), "no SSE connection while still failing");
  assert.equal(f.state.bootRetrying, false, "bootRetrying clears even when the retry fails");

  // Retry attempt 2: loadProjects recovers → exact success continuation.
  await m.retryBoot();
  assert.equal(attempts, 3, "two projects loads (init + first failed retry) before the recovery retry");
  assert.equal(f.out.bootError, false, "the error screen clears on recovery");
  assert.equal(f.state.selectedProjectId, "p-1", "recovery validates the selection like a fresh boot");
  assert.ok(f.calls.includes("loadBoard"), "the board loads after recovery");
  assert.ok(f.calls.includes("connectSse"), "SSE connects after recovery");
  assert.ok(f.calls.includes("syncUrlState(true)"), "URL state synced by the continuation");
});

// ----------------------------------------------------------------------- deep links

/** Fake window with a fixed pathname + listener capture (no matchMedia use in
 *  these paths: the failure/continueBoot branches never touch it… actually
 *  _runBoot registers listeners when window exists, so provide matchMedia too). */
function installFakeWindow(pathname: string): { remove: () => void; listeners: Map<string, EventListener[]> } {
  const listeners = new Map<string, EventListener[]>();
  const prev = (globalThis as { window?: unknown }).window;
  (globalThis as { window?: unknown }).window = {
    location: { pathname },
    addEventListener: (type: string, fn: EventListener) => {
      const arr = listeners.get(type) ?? [];
      arr.push(fn);
      listeners.set(type, arr);
    },
    removeEventListener: () => { /* not needed by boot */ },
    matchMedia: (_q: string) => ({
      matches: true,
      addEventListener: () => { /* captured nowhere — boot only flips isMobile from it */ },
      removeEventListener: () => { /* noop */ },
    }),
  };
  return {
    remove: () => {
      if (prev === undefined) delete (globalThis as { window?: unknown }).window;
      else (globalThis as { window?: unknown }).window = prev;
    },
    listeners,
  };
}

test("bootstrap: a deep-linked full-page task proceeds even when the projects fetch failed", async () => {
  const w = installFakeWindow("/tasks/t-9");
  const f = makeFixture({ afterLoadProjects: (s) => { s.loadError = "down"; } });
  try {
    const m = createBootstrapModule(f.deps);
    await m.init();
    assert.ok(f.calls.includes("openDeepLinkedTask:t-9"), "the task route wins over the error screen");
    assert.ok(!f.calls.includes("setBootError"), "no boot-error screen — the task view owns its own retry state");
    // Window listeners were registered exactly once for each event.
    assert.equal(w.listeners.get("beforeunload")?.length, 1);
    assert.equal(w.listeners.get("popstate")?.length, 1);
    assert.ok(f.calls.includes("initFromUrl"), "URL params restored after the listeners");
  } finally {
    clearTicker(f);
    w.remove();
  }
});

test("bootstrap: a deep-linked dashboard route on a failed boot fetch is adopted with fromBootError", async () => {
  const w = installFakeWindow("/projects/proj-d/dashboard");
  const f = makeFixture({ afterLoadProjects: (s) => { s.loadError = "down"; } });
  try {
    const m = createBootstrapModule(f.deps);
    await m.init();
    assert.ok(f.calls.includes("openDeepLinkedDashboard:proj-d(bootError)"),
      "the dashboard deep link is adopted in boot-error mode (no board load of its own)");
    assert.ok(!f.calls.includes("setBootError"));
  } finally {
    clearTicker(f);
    w.remove();
  }
});

test("bootstrap: the beforeunload handler clears the ticker and disconnects SSE with backoff reset", async () => {
  const w = installFakeWindow("/");
  const f = makeFixture();
  try {
    const m = createBootstrapModule(f.deps);
    await m.init();
    assert.notEqual(f.tickerId.v, null, "sanity: ticker mirrored");

    const beforeUnload = w.listeners.get("beforeunload")?.[0];
    assert.ok(beforeUnload, "exactly one beforeunload listener is registered by boot");
    (beforeUnload as () => void)({} as Event);

    assert.ok(f.calls.includes("disconnectSse(true)"), "SSE disconnects with backoff reset on unload");
  } finally {
    clearTicker(f);
    w.remove();
  }
});

// ------------------------------------------------------------------ continueBoot direct

test("bootstrap: continueBoot() honors an already-known selection and skips the fallback write path", async () => {
  const f = makeFixture();
  f.state.selectedProjectId = "p-2"; // known → must be kept, not reset to p-1
  const m = createBootstrapModule(f.deps);
  await m.continueBoot(null);
  assert.equal(f.state.selectedProjectId, "p-2", "a valid stored selection survives the boot continuation");
  assert.ok(!f.calls.includes("setSelectedProjectId"), "no selection rewrite when the id is known");
  assert.ok(f.calls.includes("loadBoard") && f.calls.includes("connectSse"));
});

test("bootstrap: continueBoot() prefers the deep-linked task over the dashboard route", async () => {
  const f = makeFixture();
  const m = createBootstrapModule(f.deps);
  await m.continueBoot("t-1", "proj-x");
  assert.ok(f.calls.includes("openDeepLinkedTask:t-1"), "task deep link wins");
  assert.ok(!f.calls.some((c) => c.startsWith("openDeepLinkedDashboard")), "dashboard branch skipped when a task route exists");
});
