// Pure helper for per-actor board scoping (epic T7): when the user filters the
// board to "mine" (assigned / created / activity), we fetch a flat task LIST via
// listTasks({ mine }) and must reshape it into the same Board shape columns() reads
// from api.getBoard(). Keeping this transformation pure makes it trivially unit-
// testable without any DOM or fetch.

import type { Board, StatusCounts, Status, TaskSummary } from "./types";

/** Group a flat (possibly scoped) task list into the three-status Board snapshot that
 *  `columns()` consumes. Rows whose status isn't one of the known three are dropped
 *  defensively (never crash the board on an unknown/future status). `counts` is derived
 *  from the grouped rows so column headers stay truthful even when scoped — note that a
 *  scoped list omits the board-only card badges (ac_total, comment_count, …) by design,
 *  which simply means those chips don't render in the scoped view. */
export function boardFromTaskList(projectId: string, tasks: TaskSummary[]): Board {
  const grouped: Record<Status, TaskSummary[]> = { todo: [], doing: [], done: [] };
  for (const t of tasks ?? []) {
    if (!t || !(t.status in grouped)) continue; // skip null/unknown-status rows
    grouped[t.status].push(t);
  }
  const counts: StatusCounts = {
    todo: grouped.todo.length,
    doing: grouped.doing.length,
    done: grouped.done.length,
  };
  return { project_id: projectId, counts, tasks: grouped as Record<Status, TaskSummary[]> };
}
