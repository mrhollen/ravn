// Per-column board view assembly (Phase 1 extraction from app.ts — frontend
// modularization epic): filter → sort → Done cap for every status column.
// State comes IN as a typed snapshot (BoardFilterState) plus view options; state
// goes OUT as returned ColumnView arrays. No DOM, no Alpine — the composition
// root owns every `filter*` field, the debounce/sequence machinery, and URL
// sync. The predicate family lives in src/filters.ts; sort + parent hoist in
// src/sort.ts.

import { STATUSES, STATUS_LABEL } from "./types";
import type { Status, TaskSummary } from "./types";
import { applyFilters, normalizedSearch, type BoardFilterState } from "./filters";
import { sortColumnTasks, type SortDir, type SortKey } from "./sort";

/** One rendered board column. */
export interface ColumnView {
  status: Status;
  label: string;
  tasks: TaskSummary[];
  totalCount: number;
}

/** View-level options (sort + pagination) applied to every column. */
export interface BoardViewOptions {
  hoistParents: boolean;
  sortKey: SortKey;
  sortDir: SortDir;
  /** Done-column pagination cap (Infinity = no cap). */
  doneLimit: number;
  /** Archived-view mode: bypasses the Done cap — archived sets are small, that
   *  mode has no "show more" affordance, and capping would silently hide rows. */
  showArchived: boolean;
}

/** Build ONE status column: `rows(status)` → applyFilters → sortColumnTasks →
 *  Done cap. Exported so tests can target a single column without assembling
 *  the whole board. */
export function assembleColumn(
  status: Status,
  rows: (status: Status) => TaskSummary[],
  f: BoardFilterState,
  opts: BoardViewOptions,
): ColumnView {
  let tasks = applyFilters(rows(status), f);
  // With hoistParents ON (default) parents float to the top of their column;
  // under "Server order" Done still orders newest-first.
  tasks = sortColumnTasks(tasks, status, opts.sortKey, opts.sortDir, opts.hoistParents);

  const totalCount = tasks.length;
  const searching = normalizedSearch(f.search) !== "";
  // Bypassed while a search is active so a matched done task can never hide
  // behind the "20 most recent" cut (AC integration requirement); ALSO
  // bypassed in the archived view.
  const renderedTasks =
    isDoneCapActive(status, searching, opts.showArchived, totalCount, opts.doneLimit)
      ? tasks.slice(0, opts.doneLimit)
      : tasks;

  return { status, label: STATUS_LABEL[status], tasks: renderedTasks, totalCount };
}

/** Assemble the per-status column views in STATUSES order. The client-side
 *  sort is re-applied on every render so SSE/poll refreshes keep the user's
 *  ordering without fighting live updates (the sort + parent-hoist pass lives
 *  in src/sort.ts). The header count pill reads totalCount — the FULL
 *  post-filter count either way — so it stays accurate. */
export function visibleColumns(
  rows: (status: Status) => TaskSummary[],
  f: BoardFilterState,
  opts: BoardViewOptions,
): ColumnView[] {
  return STATUSES.map((s) => assembleColumn(s, rows, f, opts));
}

/** True when the Done column's "20 most recent" cap applies: done status, no
 *  active search (a matched done row must never hide behind the cut), not in
 *  the archived view (that mode has no "show more" affordance), and more rows
 *  than the cap. */
export function isDoneCapActive(
  status: Status,
  searching: boolean,
  showArchived: boolean,
  totalCount: number,
  doneLimit: number,
): boolean {
  return status === "done" && !searching && !showArchived && totalCount > doneLimit;
}
