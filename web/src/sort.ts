// Board sort core (Phase 1 extraction from app.ts — frontend modularization
// epic): the sort criterion + direction vocabulary, persisted reader/writer,
// the column comparators (incl. Done's newest-first fallback), and the
// per-column sort + parent-hoist pass. Pure — the sortKey/sortDir/hoistParents
// state fields stay in the composition root until the P2 board module owns them.

import { safeStorage } from "./util";
import type { Status, TaskSummary } from "./types";

/** localStorage key for the persisted sort choice. */
export const LS_SORT = "ravn.sort";
/** localStorage key for the persisted hoist-parents preference. */
export const LS_HOIST_PARENTS = "ravn.hoist_parents";

/** Sort criteria for the board columns. "" = canonical server order (no sort). */
export type SortKey = "" | "updated_at" | "created_at" | "due_at" | "priority" | "title";
export type SortDir = "asc" | "desc";

export const SORT_OPTIONS: { value: SortKey; label: string }[] = [
  { value: "", label: "Server order (default)" },
  { value: "updated_at", label: "Last Modified" },
  { value: "created_at", label: "Created At" },
  { value: "due_at", label: "Due Date" },
  { value: "priority", label: "Priority" },
  { value: "title", label: "Title (A→Z)" },
];

/** Sensible initial direction per criterion, applied when the user picks a new key. */
export const DEFAULT_SORT_DIR: Record<Exclude<SortKey, "">, SortDir> = {
  updated_at: "desc", // newest first
  created_at: "desc", // newest first
  due_at: "asc", // nearest first
  priority: "asc", // P0 (urgent) first
  title: "asc", // alphabetical
};

export function isSortKey(v: unknown): v is SortKey {
  return typeof v === "string" && SORT_OPTIONS.some((o) => o.value === v);
}

/** Read the persisted sort choice; anything malformed falls back to no sort. */
export function readStoredSort(): { key: SortKey; dir: SortDir } {
  try {
    const raw = safeStorage.getItem(LS_SORT);
    if (!raw) return { key: "", dir: "desc" };
    const parsed = JSON.parse(raw) as { key?: unknown; dir?: unknown };
    const key: SortKey = isSortKey(parsed.key) ? parsed.key : "";
    const dir: SortDir = parsed.dir === "asc" ? "asc" : "desc";
    return { key, dir };
  } catch {
    return { key: "", dir: "desc" };
  }
}

/** Persist the sort choice; storage failures (private mode) are non-fatal. */
export function persistSort(key: SortKey, dir: SortDir): void {
  try {
    safeStorage.setItem(LS_SORT, JSON.stringify({ key, dir }));
  } catch {
    /* private mode — non-fatal */
  }
}

/** Read the persisted hoist-parents preference; missing or malformed values
 *  fall back to ON — new users get parent hoisting by default. */
export function readStoredHoistParents(): boolean {
  try {
    const raw = safeStorage.getItem(LS_HOIST_PARENTS);
    if (raw === null) return true;
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "boolean" ? parsed : true;
  } catch {
    return true;
  }
}

/** Persist the hoist-parents preference; storage failures are non-fatal. */
export function persistHoistParents(on: boolean): void {
  try {
    safeStorage.setItem(LS_HOIST_PARENTS, JSON.stringify(on));
  } catch {
    /* private mode — non-fatal */
  }
}

/** Comparator for one criterion + direction. Ties always break by id (ascending)
 *  so the order is deterministic regardless of direction. */
export function compareTasks(a: TaskSummary, b: TaskSummary, key: SortKey, dir: SortDir): number {
  let cmp = 0;
  switch (key) {
    case "priority":
      cmp = a.priority - b.priority;
      break;
    case "title":
      cmp = a.title.localeCompare(b.title, undefined, { sensitivity: "base", numeric: true });
      break;
    case "due_at": {
      // No due date sorts last (mirrors the server's nulls-last convention).
      const da = a.due_at ? Date.parse(a.due_at) : Infinity;
      const db = b.due_at ? Date.parse(b.due_at) : Infinity;
      cmp = da === db ? 0 : da < db ? -1 : 1;
      break;
    }
    case "created_at":
    case "updated_at": {
      const va = Date.parse(a[key]);
      const vb = Date.parse(b[key]);
      cmp = Number.isNaN(va) || Number.isNaN(vb) ? 0 : va - vb;
      break;
    }
    default:
      return a.id.localeCompare(b.id);
  }
  if (cmp === 0) return a.id.localeCompare(b.id);
  return dir === "asc" ? cmp : -cmp;
}

/** A card is a "parent" when the board snapshot reports at least one child
 *  task on it (subtask_total is only present/populated for parents). */
export function isParentTask(task: TaskSummary): boolean {
  return (task.subtask_total ?? 0) > 0;
}

/** Newest-first comparator for the Done column under "Server order": the
 *  completion instant wins, then last update, then creation. Board rows carry
 *  no completed_at of their own — `(a as any)` picks it up when a detail-shaped
 *  row flows through (e.g. post-optimistic-move) — ISO strings compare
 *  lexicographically. */
function cmpDone(a: TaskSummary, b: TaskSummary): number {
  const timeA = (a as any).completed_at || a.updated_at || a.created_at || "";
  const timeB = (b as any).completed_at || b.updated_at || b.created_at || "";
  return timeB.localeCompare(timeA);
}

/** One sort pass over a column (invoked after filtering, before the done cap).
 *
 *  With hoistParents ON (default) parent cards float to the top of the column
 *  regardless of the sort: each group is ordered with the same active
 *  key/direction, and under "Server order" (key "") both groups keep their
 *  original relative order (except Done, which orders newest-first).
 *  Returns copies — never mutates the board arrays the optimistic-move code owns;
 *  the no-op path (no hoist, server order, non-Done) returns its input reference. */
export function sortColumnTasks(
  tasks: TaskSummary[],
  status: Status,
  key: SortKey,
  dir: SortDir,
  hoistParents: boolean,
): TaskSummary[] {
  if (hoistParents) {
    const parents = tasks.filter((t) => isParentTask(t));
    const rest = tasks.filter((t) => !isParentTask(t));
    if (key !== "") {
      const cmp = (a: TaskSummary, b: TaskSummary) => compareTasks(a, b, key, dir);
      parents.sort(cmp); // filter() copies — sorting in place is safe
      rest.sort(cmp);
    } else if (status === "done") {
      parents.sort(cmpDone);
      rest.sort(cmpDone);
    }
    return [...parents, ...rest];
  }
  if (key !== "") {
    return [...tasks].sort((a, b) => compareTasks(a, b, key, dir));
  }
  if (status === "done") {
    return [...tasks].sort(cmpDone);
  }
  return tasks;
}
