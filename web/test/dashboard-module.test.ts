import test from "node:test";
import assert from "node:assert/strict";

import { api, ApiError } from "../src/api.ts";
import {
  createDashboardModule,
  DEFAULT_HISTORY_BUCKET,
  DEFAULT_STALLED_MINUTES,
  effectiveRemainingSec,
  formatIdleMinutes,
  formatRemainingSeconds,
  isReportEmpty,
  priorityBreakdownEntries,
  statusSummaryEntries,
  totalReportTasks,
  workloadEntries,
  type DashboardDeps,
} from "../src/modules/dashboard.ts";
import type { ProjectReport, ProjectReportHistory, ReportHistoryBucket } from "../src/types.ts";
import { withFetch } from "./test-helpers.ts";

function sampleReport(overrides: Partial<ProjectReport> = {}): ProjectReport {
  return {
    project_id: "proj-1",
    counts: {
      by_status: { todo: 3, doing: 2, done: 5 },
      by_priority: { "0": 1, "1": 2, "2": 4, "3": 2, "4": 1, "5": 0 },
    },
    active_claims: [
      {
        id: "t-claim",
        title: "Active lease task",
        claimed_by: { type: "agent", name: "runner-1" },
        claim_expires_at: "2026-11-16T12:15:00Z",
        remaining_sec: 900,
      },
    ],
    expired_claims: [{ id: "t-exp", title: "Zombie lease task" }],
    stalled_tasks: [
      {
        id: "t-stalled",
        title: "Stalled work",
        last_activity_at: "2026-11-16T10:00:00Z",
        idle_minutes: 120,
      },
    ],
    bottlenecks: [{ id: "t-blk", title: "Blocker root", blocks: 2 }],
    open_blocked_by: [
      {
        blocker: { id: "t-blk", title: "Blocker root" },
        blocked: [
          { id: "t-b1", title: "Blocked child 1" },
          { id: "t-b2", title: "Blocked child 2" },
        ],
      },
    ],
    recently_completed: [
      {
        id: "t-done",
        title: "Finished item",
        completed_at: "2026-11-16T11:30:00Z",
        assignee: { type: "human", name: "ada" },
      },
    ],
    workload: {
      ada: 3,
      "runner-1": 1,
      "": 1,
    },
    ...overrides,
  };
}

function emptyReport(projectId = "proj-empty"): ProjectReport {
  return {
    project_id: projectId,
    counts: {
      by_status: { todo: 0, doing: 0, done: 0 },
      by_priority: { "0": 0, "1": 0, "2": 0, "3": 0, "4": 0, "5": 0 },
    },
    active_claims: [],
    expired_claims: [],
    stalled_tasks: [],
    bottlenecks: [],
    open_blocked_by: [],
    recently_completed: [],
    workload: {},
  };
}

function sampleHistory(overrides: Partial<ProjectReportHistory> = {}): ProjectReportHistory {
  return {
    project_id: "proj-1",
    days: 30,
    bucket: "day",
    series: [
      { bucket_start: "2026-11-15T00:00:00Z", opened: 2, completed: 1, cumulative_completed: 4 },
      { bucket_start: "2026-11-16T00:00:00Z", opened: 1, completed: 2, cumulative_completed: 6 },
    ],
    ...overrides,
  };
}

function emptyHistory(projectId = "proj-empty", bucket: ReportHistoryBucket = "day"): ProjectReportHistory {
  return {
    project_id: projectId,
    days: 30,
    bucket,
    series: [],
  };
}

// ------------------------------------------------------------------ pure helpers

test("Dashboard: totalReportTasks & isReportEmpty handle null, empty, and populated reports", () => {
  assert.equal(totalReportTasks(null), 0);
  assert.equal(isReportEmpty(null), true);
  assert.equal(totalReportTasks(emptyReport()), 0);
  assert.equal(isReportEmpty(emptyReport()), true);
  assert.equal(totalReportTasks(sampleReport()), 10);
  assert.equal(isReportEmpty(sampleReport()), false);
});

test("Dashboard: statusSummaryEntries produces ordered todo/doing/done percentages", () => {
  const rows = statusSummaryEntries(sampleReport());
  assert.deepEqual(
    rows.map((r) => ({ status: r.status, label: r.label, count: r.count, pct: r.pct })),
    [
      { status: "todo", label: "To Do", count: 3, pct: 30 },
      { status: "doing", label: "Doing", count: 2, pct: 20 },
      { status: "done", label: "Done", count: 5, pct: 50 },
    ],
  );
  const emptyRows = statusSummaryEntries(emptyReport());
  assert.deepEqual(emptyRows.map((r) => r.pct), [0, 0, 0]);
});

test("Dashboard: priorityBreakdownEntries always returns P0..P5 with counts and percentages", () => {
  const rows = priorityBreakdownEntries(sampleReport());
  assert.equal(rows.length, 6);
  assert.deepEqual(
    rows.map((r) => ({ label: r.label, count: r.count, pct: r.pct })),
    [
      { label: "P0", count: 1, pct: 10 },
      { label: "P1", count: 2, pct: 20 },
      { label: "P2", count: 4, pct: 40 },
      { label: "P3", count: 2, pct: 20 },
      { label: "P4", count: 1, pct: 10 },
      { label: "P5", count: 0, pct: 0 },
    ],
  );
});

test("Dashboard: formatRemainingSeconds & effectiveRemainingSec format durations and clamp negatives", () => {
  assert.equal(formatRemainingSeconds(-5), "0s");
  assert.equal(formatRemainingSeconds(0), "0s");
  assert.equal(formatRemainingSeconds(45), "45s");
  assert.equal(formatRemainingSeconds(120), "2m");
  assert.equal(formatRemainingSeconds(905), "15m 5s");
  assert.equal(formatRemainingSeconds(3600), "1h");
  assert.equal(formatRemainingSeconds(3665), "1h 1m");

  const claim = { claim_expires_at: "2026-11-16T12:15:00Z", remaining_sec: 900 };
  assert.equal(effectiveRemainingSec(claim), 900);
  const nowMs = Date.parse("2026-11-16T12:10:00Z");
  assert.equal(effectiveRemainingSec(claim, nowMs), 300);
  assert.equal(effectiveRemainingSec(claim, Date.parse("2026-11-16T12:20:00Z")), 0);
  assert.equal(effectiveRemainingSec({ claim_expires_at: "not-a-date", remaining_sec: 42 }, nowMs), 42);
});

test("Dashboard: formatIdleMinutes formats minutes, hours, and days cleanly", () => {
  assert.equal(formatIdleMinutes(-1), "0m");
  assert.equal(formatIdleMinutes(45), "45m");
  assert.equal(formatIdleMinutes(60), "1h");
  assert.equal(formatIdleMinutes(95), "1h 35m");
  assert.equal(formatIdleMinutes(1440), "1d");
  assert.equal(formatIdleMinutes(1500), "1d 1h");
});

test("Dashboard: workloadEntries maps '' to Unassigned and sorts count desc with Unassigned last on ties", () => {
  const entries = workloadEntries(sampleReport());
  assert.deepEqual(
    entries.map((e) => ({ key: e.key, label: e.label, count: e.count, unassigned: e.unassigned, pct: e.pct })),
    [
      { key: "ada", label: "ada", count: 3, unassigned: false, pct: 100 },
      { key: "runner-1", label: "runner-1", count: 1, unassigned: false, pct: 33 },
      { key: "", label: "Unassigned", count: 1, unassigned: true, pct: 33 },
    ],
  );
  assert.deepEqual(workloadEntries(emptyReport()), []);
  assert.deepEqual(workloadEntries(null), []);
});

// ------------------------------------------------------------------ api client

test("Dashboard API: getProjectReport & getProjectReportHistory build URLs and query params", async () => {
  const urls: string[] = [];
  await withFetch(async (url: string | URL) => {
    const u = String(url);
    urls.push(u);
    const body = u.includes("/report/history") ? emptyHistory("p 1") : emptyReport("p 1");
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }, async () => {
    await api.getProjectReport("p 1");
    await api.getProjectReport("proj-2", { stalledMinutes: 90, since: "2026-11-15T00:00:00Z" });
    await api.getProjectReportHistory("p 1");
    await api.getProjectReportHistory("proj-2", { days: 60, bucket: "week" });
  });

  assert.equal(urls[0], "/api/v1/projects/p%201/report");
  assert.equal(
    urls[1],
    "/api/v1/projects/proj-2/report?stalled_minutes=90&since=2026-11-15T00%3A00%3A00Z",
  );
  assert.equal(urls[2], "/api/v1/projects/p%201/report/history");
  assert.equal(
    urls[3],
    "/api/v1/projects/proj-2/report/history?days=60&bucket=week",
  );
});

// ------------------------------------------------------------------ module lifecycle

function makeModuleFixture(overrides: Partial<{
  projectId: string;
  open: boolean;
  stalledMinutes: number;
  nowTick: number;
  bucket: ReportHistoryBucket;
}> = {}) {
  const state = {
    projectId: overrides.projectId ?? "proj-1",
    open: overrides.open ?? false,
    report: null as ProjectReport | null,
    loading: false,
    error: "",
    history: null as ProjectReportHistory | null,
    historyLoading: false,
    historyError: "",
    bucket: overrides.bucket ?? DEFAULT_HISTORY_BUCKET,
    forbidden: false,
    stalledMinutes: overrides.stalledMinutes ?? DEFAULT_STALLED_MINUTES,
    nowTick: overrides.nowTick ?? Date.parse("2026-11-16T12:10:00Z"),
    authOpened: 0,
    titleUpdates: 0,
  };
  const scheduled: Array<{ fn: () => void; ms: number; cleared: boolean }> = [];

  const deps: DashboardDeps = {
    selectedProjectId: () => state.projectId,
    dashboardOpen: () => state.open,
    dashboardReport: () => state.report,
    dashboardHistory: () => state.history,
    historyBucket: () => state.bucket,
    stalledMinutes: () => state.stalledMinutes,
    nowTick: () => state.nowTick,
    setDashboardOpen: (v) => { state.open = v; },
    setDashboardReport: (r) => { state.report = r; },
    setDashboardLoading: (v) => { state.loading = v; },
    setDashboardError: (e) => { state.error = e; },
    setDashboardHistory: (h) => { state.history = h; },
    setDashboardHistoryLoading: (v) => { state.historyLoading = v; },
    setDashboardHistoryError: (e) => { state.historyError = e; },
    setHistoryBucket: (b) => { state.bucket = b; },
    setProjectForbidden: (v) => { state.forbidden = v; },
    openAuthModal: () => { state.authOpened++; },
    updateDocumentTitle: () => { state.titleUpdates++; },
    timers: {
      setTimer: (fn, ms) => {
        const entry = { fn, ms, cleared: false };
        scheduled.push(entry);
        return scheduled.length;
      },
      clearTimer: (id) => {
        const idx = Number(id) - 1;
        if (scheduled[idx]) scheduled[idx].cleared = true;
      },
    },
  };
  return { state, scheduled, mod: createDashboardModule(deps) };
}

test("Dashboard module: openDashboard / closeDashboard / toggleDashboard manage state, title, report, and history", async () => {
  const { state, mod } = makeModuleFixture();
  const report = sampleReport();
  const history = sampleHistory();

  await withFetch(async (url: string | URL) => {
    const body = String(url).includes("/report/history") ? history : report;
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }, async () => {
    mod.openDashboard();
    assert.equal(state.open, true);
    assert.equal(state.titleUpdates, 1);
    await new Promise((r) => setTimeout(r, 0));
    assert.deepEqual(state.report, report);
    assert.deepEqual(state.history, history);
    assert.equal(state.loading, false);
    assert.equal(state.historyLoading, false);
    assert.equal(mod.totalTasks(), 10);
    assert.equal(mod.isEmpty(), false);
    assert.equal(mod.isHistoryEmpty(), false);
    assert.equal(mod.throughputModel().summary.totalCompleted, 3);
    assert.equal(mod.cumulativeModel().summary.latestTotal, 6);
    assert.match(mod.throughputSvgInner(), /data-chart-bar=/);
    assert.match(mod.cumulativeSvgInner(), /data-chart-line="cumulative"/);
    assert.match(mod.throughputAriaLabel(), /Throughput bar chart/);
    assert.match(mod.cumulativeAriaLabel(), /Cumulative completed tasks/);
    assert.equal(mod.statusEntries()[0].count, 3);
    assert.equal(mod.priorityEntries()[2].count, 4);
    assert.equal(mod.workloadEntries()[0].label, "ada");
    assert.equal(mod.activeClaimRemaining(report.active_claims[0]), "5m");

    mod.toggleDashboard();
    assert.equal(state.open, false);
    assert.equal(state.titleUpdates, 2);

    mod.toggleDashboard();
    assert.equal(state.open, true);
    await new Promise((r) => setTimeout(r, 0));
  });
});

test("Dashboard module: setHistoryBucket toggles day/week, skips redundant fetches, and drops stale bucket responses", async () => {
  const { state, mod } = makeModuleFixture({ open: true });
  const urls: string[] = [];
  let resolveWeek!: (r: Response) => void;

  await withFetch((url: string | URL) => {
    const u = String(url);
    urls.push(u);
    if (u.includes("bucket=week")) {
      return new Promise<Response>((res) => { resolveWeek = res; });
    }
    return Promise.resolve(
      new Response(JSON.stringify(sampleHistory({ bucket: "day" })), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
  }, async () => {
    await mod.loadHistory(false);
    assert.equal(state.history?.bucket, "day");
    assert.equal(urls.length, 1);

    // Clicking active bucket when history is already loaded does not refetch.
    await mod.setHistoryBucket("day");
    assert.equal(urls.length, 1);

    // Switch to week, then rapidly switch back to day before week resolves.
    const weekReq = mod.setHistoryBucket("week");
    assert.equal(state.bucket, "week");
    assert.equal(state.history, null);
    await mod.setHistoryBucket("day");
    assert.equal(state.bucket, "day");
    assert.equal(state.history?.bucket, "day");

    resolveWeek(
      new Response(JSON.stringify(sampleHistory({ bucket: "week" })), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    await weekReq;
    assert.equal(state.history?.bucket, "day", "stale week response must not overwrite day");
  });
});

test("Dashboard module: loadHistory handles empty project, 401 unauthorized, 403 forbidden, and server errors", async () => {
  const { state, mod } = makeModuleFixture({ projectId: "" });
  await mod.loadHistory();
  assert.equal(state.history, null);
  assert.equal(state.historyLoading, false);
  assert.equal(state.historyError, "");

  state.projectId = "proj-1";
  await withFetch(async () => new Response(JSON.stringify({ error: "internal", message: "history boom" }), {
    status: 500,
    headers: { "content-type": "application/json" },
  }), async () => {
    await mod.loadHistory(false);
    assert.equal(state.historyError, "history boom");
    assert.equal(state.historyLoading, false);
  });

  await withFetch(async () => new Response(JSON.stringify({ error: "unauthorized", message: "auth required" }), {
    status: 401,
    headers: { "content-type": "application/json" },
  }), async () => {
    await mod.loadHistory(false);
    assert.equal(state.authOpened, 1);
    assert.equal(state.historyError, "auth required");
  });

  await withFetch(async () => new Response(JSON.stringify({ error: "forbidden", message: "no access" }), {
    status: 403,
    headers: { "content-type": "application/json" },
  }), async () => {
    state.history = sampleHistory();
    await mod.loadHistory(false);
    assert.equal(state.forbidden, true);
    assert.equal(state.history, null);
  });
});

test("Dashboard module: loadReport guards empty project, 401 unauthorized, 403 forbidden, and network error", async () => {
  const { state, mod } = makeModuleFixture({ projectId: "" });
  await mod.loadReport();
  assert.equal(state.report, null);
  assert.equal(state.loading, false);
  assert.equal(state.error, "");

  state.projectId = "proj-1";
  await withFetch(async () => new Response(JSON.stringify({ error: "unauthorized", message: "auth required" }), {
    status: 401,
    headers: { "content-type": "application/json" },
  }), async () => {
    await mod.loadReport(false);
    assert.equal(state.authOpened, 1);
    assert.equal(state.error, "auth required");
    assert.equal(state.loading, false);
  });

  await withFetch(async () => new Response(JSON.stringify({ error: "forbidden", message: "no access" }), {
    status: 403,
    headers: { "content-type": "application/json" },
  }), async () => {
    state.report = sampleReport();
    await mod.loadReport(false);
    assert.equal(state.forbidden, true);
    assert.equal(state.report, null);
  });
});

test("Dashboard module: sequence guard and project-switch check drop stale responses", async () => {
  const { state, mod } = makeModuleFixture({ projectId: "proj-1" });
  let resolveFirst!: (r: Response) => void;
  let call = 0;

  await withFetch((url: string | URL) => {
    call++;
    if (call === 1) {
      return new Promise<Response>((res) => { resolveFirst = res; });
    }
    const body = String(url).includes("/report/history")
      ? sampleHistory({ project_id: "proj-2" })
      : sampleReport({ project_id: "proj-2" });
    return Promise.resolve(new Response(JSON.stringify(body), {
      status: 200,
      headers: { "content-type": "application/json" },
    }));
  }, async () => {
    const first = mod.loadReport(false);
    state.projectId = "proj-2";
    const second = mod.loadReport(false);
    await second;
    assert.equal(state.report?.project_id, "proj-2");
    assert.equal(state.history?.project_id, "proj-2");

    resolveFirst(new Response(JSON.stringify(emptyReport("proj-1")), {
      status: 200,
      headers: { "content-type": "application/json" },
    }));
    await first;
    assert.equal(state.report?.project_id, "proj-2", "stale response from proj-1 must not overwrite proj-2");
  });
});

test("Dashboard module: refreshDashboard coalesces bursts, refreshes report+history, and skips when closed or invalidated", async () => {
  const { state, scheduled, mod } = makeModuleFixture({ open: false });

  mod.refreshDashboard();
  assert.equal(scheduled.length, 0, "no refresh scheduled while dashboard is closed");

  state.open = true;
  mod.refreshDashboard();
  mod.refreshDashboard();
  assert.equal(scheduled.length, 2);
  assert.equal(scheduled[0].cleared, true, "trailing debounce cancels earlier timer");
  assert.equal(scheduled[1].cleared, false);

  const fetchedUrls: string[] = [];
  await withFetch(async (url: string | URL) => {
    const u = String(url);
    fetchedUrls.push(u);
    const body = u.includes("/report/history") ? sampleHistory() : sampleReport();
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }, async () => {
    scheduled[1].fn();
    await new Promise((r) => setTimeout(r, 20));
    assert.equal(fetchedUrls.length, 2);
    assert.ok(fetchedUrls[0].endsWith("/report"));
    assert.ok(fetchedUrls[1].includes("/report/history"));

    // invalidatePendingLoads cancels an armed refresh and clears stale history
    mod.refreshDashboard();
    assert.equal(scheduled[2].cleared, false);
    mod.invalidatePendingLoads();
    assert.equal(scheduled[2].cleared, true);
    assert.equal(state.history, null);
  });
});
