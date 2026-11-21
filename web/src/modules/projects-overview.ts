// Cross-project Progress Overview module (/projects).
// Owns pure completion/filter/sort helpers and sequence-guarded loading of
// GET /api/v1/projects/overview (zero N+1 calls).

import { ApiError, api, errMsg } from "../api";
import { createSequenceGuard } from "../refresh";
import type { ProjectOverviewRow, SessionIdentity } from "../types";

export type ProjectsOverviewSort = "recent" | "open_work" | "name" | "completion";

export interface OverviewSortOption {
  value: ProjectsOverviewSort;
  label: string;
}

export const OVERVIEW_SORT_OPTIONS: readonly OverviewSortOption[] = [
  { value: "recent", label: "Recent activity" },
  { value: "open_work", label: "Open work" },
  { value: "name", label: "Name (A→Z)" },
  { value: "completion", label: "Completion %" },
];

export interface ProjectCompletionStats {
  total: number;
  todo: number;
  doing: number;
  done: number;
  openCount: number;
  pct: number;
}

/** Compute canonical task totals, open work count, and completion percentage for an overview row. */
export function projectCompletionStats(
  row: Pick<ProjectOverviewRow, "task_counts"> | null | undefined,
): ProjectCompletionStats {
  const todo = Math.max(0, row?.task_counts?.todo ?? 0);
  const doing = Math.max(0, row?.task_counts?.doing ?? 0);
  const done = Math.max(0, row?.task_counts?.done ?? 0);
  const openCount = todo + doing;
  const total = openCount + done;
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return { total, todo, doing, done, openCount, pct };
}

function activityTimestampMs(row: ProjectOverviewRow): number {
  const iso = row.health?.last_activity_at || row.updated_at || "";
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? 0 : ms;
}

/** Case-insensitive substring filter over name + description, sorted by the chosen criterion. */
export function filterAndSortOverviewProjects(
  rows: readonly ProjectOverviewRow[],
  opts: { search?: string; sort?: ProjectsOverviewSort } = {},
): ProjectOverviewRow[] {
  const q = (opts.search ?? "").trim().toLowerCase();
  const filtered = q
    ? rows.filter((row) => {
        const name = (row.name ?? "").toLowerCase();
        const desc = (row.description ?? "").toLowerCase();
        return name.includes(q) || desc.includes(q);
      })
    : [...rows];

  const sort: ProjectsOverviewSort = opts.sort ?? "recent";
  filtered.sort((a, b) => {
    switch (sort) {
      case "open_work": {
        const aOpen = (a.task_counts?.todo ?? 0) + (a.task_counts?.doing ?? 0);
        const bOpen = (b.task_counts?.todo ?? 0) + (b.task_counts?.doing ?? 0);
        if (bOpen !== aOpen) return bOpen - aOpen;
        const actDiff = activityTimestampMs(b) - activityTimestampMs(a);
        if (actDiff !== 0) return actDiff;
        return (a.name ?? "").localeCompare(b.name ?? "");
      }
      case "name":
        return (a.name ?? "").localeCompare(b.name ?? "");
      case "completion": {
        const sa = projectCompletionStats(a);
        const sb = projectCompletionStats(b);
        if (sb.pct !== sa.pct) return sb.pct - sa.pct;
        if (sb.total !== sa.total) return sb.total - sa.total;
        return (a.name ?? "").localeCompare(b.name ?? "");
      }
      case "recent":
      default: {
        const actDiff = activityTimestampMs(b) - activityTimestampMs(a);
        if (actDiff !== 0) return actDiff;
        return (a.name ?? "").localeCompare(b.name ?? "");
      }
    }
  });
  return filtered;
}

export interface ProjectsOverviewDeps {
  overviewProjects(): ProjectOverviewRow[];
  overviewLoading(): boolean;
  overviewError(): string;
  overviewArchived(): boolean;
  overviewSearch(): string;
  overviewSort(): ProjectsOverviewSort;
  myProjectsOnly(): boolean;
  identity(): SessionIdentity | null;
  setOverviewProjects(rows: ProjectOverviewRow[]): void;
  setOverviewLoading(loading: boolean): void;
  setOverviewError(err: string): void;
  setOverviewArchived(archived: boolean): void;
  setOverviewSearch(search: string): void;
  setOverviewSort(sort: ProjectsOverviewSort): void;
  openAuthModal(): void;
}

export function createProjectsOverviewModule(deps: ProjectsOverviewDeps) {
  const overviewSeq = createSequenceGuard();

  async function loadOverview(silent = false): Promise<void> {
    const token = overviewSeq.next();
    if (!silent || deps.overviewProjects().length === 0) {
      deps.setOverviewLoading(true);
      if (!silent) deps.setOverviewError("");
    }
    try {
      const archived = deps.overviewArchived();
      const mine = !archived && deps.myProjectsOnly() && deps.identity() ? "created" : undefined;
      const res = await api.getProjectsOverview({
        ...(archived ? { archived: true } : {}),
        ...(mine ? { mine } : {}),
      });
      if (!overviewSeq.isFresh(token)) return;
      deps.setOverviewProjects(Array.isArray(res.projects) ? res.projects : []);
      deps.setOverviewError("");
    } catch (e) {
      if (!overviewSeq.isFresh(token)) return;
      if (!silent || deps.overviewProjects().length === 0) {
        deps.setOverviewError(errMsg(e));
      }
      if (ApiError.isUnauthorized(e)) {
        deps.openAuthModal();
      }
    } finally {
      if (overviewSeq.isFresh(token)) {
        deps.setOverviewLoading(false);
      }
    }
  }

  function setArchived(archived: boolean): Promise<void> {
    deps.setOverviewArchived(archived);
    deps.setOverviewProjects([]);
    deps.setOverviewError("");
    return loadOverview(false);
  }

  function toggleArchived(): Promise<void> {
    return setArchived(!deps.overviewArchived());
  }

  function filteredProjects(): ProjectOverviewRow[] {
    return filterAndSortOverviewProjects(deps.overviewProjects(), {
      search: deps.overviewSearch(),
      sort: deps.overviewSort(),
    });
  }

  return {
    loadOverview,
    setArchived,
    toggleArchived,
    filteredProjects,
  };
}

export type ProjectsOverviewModule = ReturnType<typeof createProjectsOverviewModule>;
