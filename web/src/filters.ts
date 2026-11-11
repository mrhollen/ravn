// Board filter core (Phase 1 extraction from app.ts — frontend modularization
// epic): the AND-composed predicate family applied in template order and the
// server full-text union search. State comes IN via a typed snapshot interface;
// state goes OUT as filtered arrays. Per-column view assembly (filter → sort →
// Done cap) lives in src/columns.ts. No DOM, no Alpine — the composition root
// owns every `filter*` field, the debounce/sequence machinery, and URL sync.

import type { TaskSummary } from "./types";
import { dueCalendarDate, isOverdue, isStalledExpired, localCalendarToday, needsAttention } from "./util";

/** Active board filter state (state-in). Mirrors the root's `filter*` fields
 *  plus the live ticker; the root builds one snapshot per render. */
export interface BoardFilterState {
  /** "" = off, else the raw select value "0".."5". */
  priority: string;
  /** "" = off; "unassigned" special value; else exact name (case-insensitive). */
  assignee: string;
  unblockedOnly: boolean;
  stalled: boolean;
  needsAttention: boolean;
  /** "" | "today" | "overdue" | "week". */
  due: string;
  /** Raw search input (may carry surrounding whitespace). */
  search: string;
  /** Root ticker value (ms epoch) driving live stalled re-evaluation. */
  nowTick: number;
  /** State-in accessor: true when the task has unresolved open blockers. */
  isBlocked: (id: string) => boolean;
  /** Server-side full-text matches for the last settled query (union source). */
  fullTextMatchIds: ReadonlySet<string>;
}

/** True when any filter dimension is active — drives the "Clear" affordance
 *  and URL sync. Search counts only when non-blank. */
export function hasAnyFilter(
  f: Pick<BoardFilterState, "priority" | "assignee" | "unblockedOnly" | "stalled" | "needsAttention" | "due" | "search">,
): boolean {
  return (
    f.priority !== "" ||
    f.assignee !== "" ||
    f.unblockedOnly ||
    f.stalled ||
    f.needsAttention ||
    f.due !== "" ||
    f.search.trim() !== ""
  );
}

/** Normalized search term: trimmed + lowercased. Single source for both the
 *  board filter and AC2's match-source indicator. */
export function normalizedSearch(query: string): string {
  return query.trim().toLowerCase();
}

/** Priority dimension: exact numeric equality on the (parsed) select value. */
export function matchesPriority(t: TaskSummary, priority: number): boolean {
  return t.priority === priority;
}

/** Assignee dimension: "unassigned" also matches rows whose assignee object
 *  exists but carries no name; a set name compares case-insensitively. */
export function matchesAssignee(t: TaskSummary, assignee: string): boolean {
  if (assignee === "unassigned") return !t.assignee || !t.assignee.name;
  const name = assignee.toLowerCase();
  return t.assignee?.name.toLowerCase() === name;
}

/** Unblocked dimension: the task has no unresolved open blockers. */
export function matchesUnblocked(t: TaskSummary, isBlocked: (id: string) => boolean): boolean {
  return !isBlocked(t.id);
}

/** Expired/stalled (epic 01M5XSNR8VWZTP66GT7ABNSJC1): doing tasks with a lapsed
 *  claim lease or no update in > 60 min. nowTick-driven, so the visible set
 *  re-evaluates live each second — exactly like the alert pill's count
 *  (countStalledExpired). */
export function matchesStalled(t: TaskSummary, nowMs: number): boolean {
  return isStalledExpired(t, nowMs);
}

/** Needs attention (epic child 01M5XTAYKJGP878ZEZB7TJW4ZB): OPEN tasks whose
 *  newest comment is a `question` (awaiting a human answer) or `handoff`
 *  (awaiting review). Done tasks are never kept by this predicate. */
export function matchesNeedsAttention(t: TaskSummary): boolean {
  return needsAttention(t);
}

/** Due window on calendar days ("YYYY-MM-DD" sorts lexicographically, so ranges
 *  are plain string compares — TZ-stable, and past-due tasks never leak into
 *  "today"). `due` must be an ACTIVE value ("today" | "overdue" | "week"); the
 *  caller gates on the raw select value. */
export function matchesDueWindow(t: TaskSummary, due: string): boolean {
  if (due === "overdue") return isOverdue(t);
  const today = localCalendarToday();
  const weekEnd = new Date();
  weekEnd.setDate(weekEnd.getDate() + 7);
  const endExclusive = `${weekEnd.getFullYear()}-${String(weekEnd.getMonth() + 1).padStart(2, "0")}-${String(weekEnd.getDate()).padStart(2, "0")}`;
  if (!t.due_at || t.status === "done") return false;
  const dueDay = dueCalendarDate(t.due_at);
  if (!dueDay) return false;
  if (due === "today") return dueDay === today;
  // "week": due today or within the next seven calendar days.
  return dueDay >= today && dueDay < endExclusive;
}

/** Search: instant local title/id substring match UNION the debounced
 *  server-side full-text matches (description/notes hits, which the client
 *  never sees in the board snapshot). The union is what keeps a card whose
 *  TITLE doesn't contain the term but whose description or notes do visible
 *  while typing (AC1). */
export function matchesSearch(t: TaskSummary, query: string, fullTextMatchIds: ReadonlySet<string>): boolean {
  const q = normalizedSearch(query);
  return t.title.toLowerCase().includes(q) || t.id.toLowerCase().includes(q) || fullTextMatchIds.has(t.id);
}

/** AC2's match-source indicator: true when the active search matched this card
 *  ONLY via description/notes — its title and id do NOT contain the substring
 *  (cards whose title/id already match show no chip). */
export function isDescriptionNotesMatch(task: TaskSummary, query: string, fullTextMatchIds: ReadonlySet<string>): boolean {
  const q = normalizedSearch(query);
  if (!q) return false;
  if (task.title.toLowerCase().includes(q)) return false;
  if (task.id.toLowerCase().includes(q)) return false;
  return fullTextMatchIds.has(task.id);
}

/** Number of `doing` rows that are abandoned/stalled-expired at `nowMs` — an
 *  expired claim lease OR no update for > 60 minutes. */
export function countStalledExpired(tasks: readonly TaskSummary[], nowMs: number): number {
  let n = 0;
  for (const t of tasks) if (isStalledExpired(t, nowMs)) n++;
  return n;
}

/** Which high-signal comment badge a card's meta cluster should show: "question"
 *  or "handoff" when the task is OPEN (todo/doing) and its newest comment has
 *  that kind (needsAttention), null otherwise — done tasks, routine kinds
 *  (progress/note/decision) and no-comment threads never badge. */
export function attentionBadgeKind(task: TaskSummary): "question" | "handoff" | null {
  if (!needsAttention(task)) return null;
  return task.last_comment_kind === "question" ? "question" : "handoff";
}

/** AND-compose every active dimension in template order (priority → assignee
 *  → unblocked → stalled → needs-attention → due window → search union).
 *  Inactive dimensions leave the array reference untouched. */
export function applyFilters(tasks: TaskSummary[], f: BoardFilterState): TaskSummary[] {
  let out = tasks;
  if (f.priority !== "") out = out.filter((t) => matchesPriority(t, Number(f.priority)));
  if (f.assignee !== "") out = out.filter((t) => matchesAssignee(t, f.assignee));
  if (f.unblockedOnly) out = out.filter((t) => matchesUnblocked(t, f.isBlocked));
  if (f.stalled) out = out.filter((t) => matchesStalled(t, f.nowTick));
  if (f.needsAttention) out = out.filter((t) => matchesNeedsAttention(t));
  if (f.due === "overdue" || f.due === "today" || f.due === "week") {
    out = out.filter((t) => matchesDueWindow(t, f.due));
  }
  if (normalizedSearch(f.search) !== "") out = out.filter((t) => matchesSearch(t, f.search, f.fullTextMatchIds));
  return out;
}
