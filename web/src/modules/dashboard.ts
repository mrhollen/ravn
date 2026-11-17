// Project health dashboard module — owns fetching, state lifecycle, and pure
// view-model / formatting helpers for the point-in-time GET /projects/{id}/report
// digest (status/priority counts, active & expired claims, workload, stalled
// tasks, bottlenecks & open_blocked_by groups, and recently completed tasks).

import { ApiError, api, errMsg } from "../api";
import { createDebounce, createSequenceGuard, SSE_RELOAD_DEBOUNCE_MS, type TimerApi } from "../refresh";
import { STATUSES, STATUS_LABEL } from "../types";
import type { ActiveClaim, ProjectReport, Status } from "../types";
import { priorityClass, priorityName } from "../util";

/** Default idle threshold (minutes) for stalled task reporting. */
export const DEFAULT_STALLED_MINUTES = 60;

/** Canonical priority levels P0..P5 rendered in the breakdown section. */
export const REPORT_PRIORITIES: readonly number[] = [0, 1, 2, 3, 4, 5];

export interface StatusSummaryEntry {
  status: Status;
  label: string;
  count: number;
  pct: number;
  barClass: string;
  badgeClass: string;
}

export interface PriorityBreakdownEntry {
  priority: number;
  label: string;
  name: string;
  count: number;
  pct: number;
  badgeClass: string;
}

export interface WorkloadEntry {
  key: string;
  label: string;
  count: number;
  pct: number;
  unassigned: boolean;
}

const STATUS_BAR_CLASS: Record<Status, string> = {
  todo: "bg-zinc-400 dark:bg-zinc-500",
  doing: "bg-amber-500 dark:bg-amber-400",
  done: "bg-emerald-500 dark:bg-emerald-400",
};

const STATUS_BADGE_CLASS: Record<Status, string> = {
  todo: "bg-zinc-100 text-zinc-700 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-200 dark:ring-zinc-700",
  doing: "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800",
  done: "bg-emerald-100 text-emerald-800 ring-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:ring-emerald-800",
};

/** Total non-archived tasks tallied across all three statuses in the report. */
export function totalReportTasks(report: ProjectReport | null | undefined): number {
  if (!report) return 0;
  let total = 0;
  for (const s of STATUSES) {
    total += report.counts?.by_status?.[s] ?? 0;
  }
  return total;
}

/** True when the report represents a project with zero non-archived tasks. */
export function isReportEmpty(report: ProjectReport | null | undefined): boolean {
  return totalReportTasks(report) === 0;
}

/** Normalize by_status counts into ordered status summary rows with share %. */
export function statusSummaryEntries(report: ProjectReport | null | undefined): StatusSummaryEntry[] {
  const total = totalReportTasks(report);
  return STATUSES.map((status) => {
    const count = report?.counts?.by_status?.[status] ?? 0;
    return {
      status,
      label: STATUS_LABEL[status],
      count,
      pct: total > 0 ? Math.round((count / total) * 100) : 0,
      barClass: STATUS_BAR_CLASS[status],
      badgeClass: STATUS_BADGE_CLASS[status],
    };
  });
}

/** Normalize by_priority counts into P0..P5 breakdown rows with share %. */
export function priorityBreakdownEntries(report: ProjectReport | null | undefined): PriorityBreakdownEntry[] {
  const total = totalReportTasks(report);
  return REPORT_PRIORITIES.map((priority) => {
    const count = report?.counts?.by_priority?.[String(priority)] ?? 0;
    return {
      priority,
      label: `P${priority}`,
      name: priorityName(priority),
      count,
      pct: total > 0 ? Math.round((count / total) * 100) : 0,
      badgeClass: priorityClass(priority),
    };
  });
}

/** Format remaining lease seconds as a concise human string (e.g. "45s", "14m 30s", "2h 5m"). */
export function formatRemainingSeconds(remainingSec: number): string {
  const sec = Math.max(0, Math.floor(Number.isFinite(remainingSec) ? remainingSec : 0));
  if (sec < 60) return `${sec}s`;
  const mins = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (mins < 60) {
    return remSec > 0 ? `${mins}m ${remSec}s` : `${mins}m`;
  }
  const hours = Math.floor(mins / 60);
  const remMin = mins % 60;
  return remMin > 0 ? `${hours}h ${remMin}m` : `${hours}h`;
}

/** Compute live remaining lease seconds using `claim_expires_at` when valid, else `remaining_sec`. */
export function effectiveRemainingSec(
  claim: Pick<ActiveClaim, "claim_expires_at" | "remaining_sec">,
  nowMs?: number,
): number {
  if (nowMs !== undefined && claim.claim_expires_at) {
    const expMs = Date.parse(claim.claim_expires_at);
    if (!Number.isNaN(expMs)) {
      return Math.max(0, Math.floor((expMs - nowMs) / 1000));
    }
  }
  return Math.max(0, Math.floor(claim.remaining_sec ?? 0));
}

/** Format stalled idle minutes concisely (e.g. "75m" -> "1h 15m", "1500m" -> "1d 1h"). */
export function formatIdleMinutes(idleMinutes: number): string {
  const mins = Math.max(0, Math.floor(Number.isFinite(idleMinutes) ? idleMinutes : 0));
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  const remMin = mins % 60;
  if (hours < 24) {
    return remMin > 0 ? `${hours}h ${remMin}m` : `${hours}h`;
  }
  const days = Math.floor(hours / 24);
  const remHours = hours % 24;
  return remHours > 0 ? `${days}d ${remHours}h` : `${days}d`;
}

/** Sort workload map (`""` -> "Unassigned") by active count desc, unassigned last on ties. */
export function workloadEntries(report: ProjectReport | null | undefined): WorkloadEntry[] {
  const raw = report?.workload;
  if (!raw) return [];
  const entries = Object.entries(raw).filter(([, count]) => count > 0);
  if (entries.length === 0) return [];
  const max = Math.max(...entries.map(([, count]) => count));
  return entries
    .map(([key, count]) => ({
      key,
      label: key === "" ? "Unassigned" : key,
      count,
      pct: max > 0 ? Math.round((count / max) * 100) : 0,
      unassigned: key === "",
    }))
    .sort((a, b) => {
      if (b.count !== a.count) return b.count - a.count;
      if (a.unassigned !== b.unassigned) return a.unassigned ? 1 : -1;
      return a.label.localeCompare(b.label);
    });
}

export interface DashboardDeps {
  selectedProjectId(): string;
  dashboardOpen(): boolean;
  dashboardReport(): ProjectReport | null;
  stalledMinutes(): number;
  nowTick(): number;
  setDashboardOpen(open: boolean): void;
  setDashboardReport(report: ProjectReport | null): void;
  setDashboardLoading(loading: boolean): void;
  setDashboardError(err: string): void;
  setProjectForbidden(forbidden: boolean): void;
  openAuthModal(): void;
  updateDocumentTitle(): void;
  timers?: TimerApi;
}

export function createDashboardModule(deps: DashboardDeps) {
  const reportSeq = createSequenceGuard();
  const refreshDebounce = createDebounce(
    () => {
      if (deps.dashboardOpen() && deps.selectedProjectId()) {
        void loadReport(true);
      }
    },
    SSE_RELOAD_DEBOUNCE_MS,
    deps.timers,
  );

  async function loadReport(silent = false): Promise<void> {
    const projectId = deps.selectedProjectId();
    if (!projectId) {
      deps.setDashboardReport(null);
      deps.setDashboardLoading(false);
      deps.setDashboardError("");
      return;
    }

    const token = reportSeq.next();
    if (!silent || !deps.dashboardReport()) {
      deps.setDashboardLoading(true);
      deps.setDashboardError("");
    }

    const stalled = deps.stalledMinutes();
    const opts = stalled > 0 && stalled !== DEFAULT_STALLED_MINUTES ? { stalledMinutes: stalled } : undefined;

    try {
      const report = await api.getProjectReport(projectId, opts);
      if (!reportSeq.isFresh(token) || deps.selectedProjectId() !== projectId) return;
      if (report.project_id && report.project_id !== projectId) return;
      deps.setDashboardReport(report);
      deps.setDashboardError("");
      deps.setProjectForbidden(false);
    } catch (e) {
      if (!reportSeq.isFresh(token) || deps.selectedProjectId() !== projectId) return;
      if (!silent || !deps.dashboardReport()) {
        deps.setDashboardError(errMsg(e));
      }
      if (ApiError.isUnauthorized(e)) {
        deps.openAuthModal();
      } else if (ApiError.isForbidden(e)) {
        deps.setDashboardReport(null);
        deps.setProjectForbidden(true);
      }
    } finally {
      if (reportSeq.isFresh(token)) {
        deps.setDashboardLoading(false);
      }
    }
  }

  function refreshDashboard(): void {
    if (!deps.dashboardOpen() || !deps.selectedProjectId()) return;
    refreshDebounce.schedule();
  }

  function openDashboard(): void {
    deps.setDashboardOpen(true);
    deps.updateDocumentTitle();
    void loadReport(false);
  }

  function closeDashboard(): void {
    refreshDebounce.cancel();
    deps.setDashboardOpen(false);
    deps.updateDocumentTitle();
  }

  function toggleDashboard(): void {
    if (deps.dashboardOpen()) closeDashboard();
    else openDashboard();
  }

  function invalidatePendingLoads(): void {
    reportSeq.next();
    refreshDebounce.cancel();
  }

  return {
    loadReport,
    refreshDashboard,
    openDashboard,
    closeDashboard,
    toggleDashboard,
    invalidatePendingLoads,
    totalTasks: () => totalReportTasks(deps.dashboardReport()),
    isEmpty: () => isReportEmpty(deps.dashboardReport()),
    statusEntries: () => statusSummaryEntries(deps.dashboardReport()),
    priorityEntries: () => priorityBreakdownEntries(deps.dashboardReport()),
    workloadEntries: () => workloadEntries(deps.dashboardReport()),
    activeClaimRemaining: (claim: ActiveClaim) =>
      formatRemainingSeconds(effectiveRemainingSec(claim, deps.nowTick())),
  };
}

export type DashboardModule = ReturnType<typeof createDashboardModule>;
