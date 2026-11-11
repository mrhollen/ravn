// Per-task move concurrency + request-timeout helpers for board status moves.
//
// The old design held a single `busyStatus: string` lock, so ONE in-flight
// PATCH froze interaction with EVERY card (and a stalled fetch — api.ts has no
// timeout — locked the whole board until refresh). These small pure helpers
// keep the logic unit-testable outside the Alpine app object:
//   - beginMove/endMove: one in-flight move PER TASK ID; other tasks stay fully
//     interactive while a given task's PATCH is outstanding.
//   - withTimeout: aborts the wrapped request after MOVE_TIMEOUT_MS so a stuck
//     call can never hold a lock forever. Deliberately scoped to move call
//     sites only — the general missing fetch timeout in api.ts is tracked
//     separately (task 01M3MMYBHECPH5Z5FRHJDS5G4J) and stays out of scope here.

import { STATUSES } from "./types";
import type { Status } from "./types";
import { statusLabel } from "./util";

/** How long a status-move request may run before we abort it and release the lock. */
export const MOVE_TIMEOUT_MS = 10_000;

/** Client-side mirror of the server's status transition map.
 *
 *  CROSS-REFERENCE (keep in sync when either side changes):
 *    internal/domain/status.go — `AllowedTransitions` + `CanTransition`,
 *    matrix pinned by TestCanTransition in internal/domain/status_test.go
 *    (introduced by commit 686164b, task 01M3MMX6H2QA4YPH43HGJHFJFH).
 *
 *    todo → doing              doing → done | todo            done → todo
 *
 *  Semantics mirrored exactly: same-status is NOT an allowed transition at the
 *  domain level (the API layer short-circuits unchanged status as a no-op, so
 *  clients never send it), and unknown statuses fail closed on both sides. */
export const LEGAL_TRANSITIONS: Record<Status, readonly Status[]> = {
  todo: ["doing"],
  doing: ["done", "todo"],
  done: ["todo"],
} as const;

/** True when moving a task from `from` to `to` is a legal domain transition
 *  (see LEGAL_TRANSITIONS cross-reference). Unknown statuses fail closed. */
export function canMove(from: string, to: string): boolean {
  return LEGAL_TRANSITIONS[from as Status]?.includes(to as Status) ?? false;
}

/** The legal destination statuses for a task currently in `from` (a copy —
 *  callers may mutate the result). Unknown status → empty list. */
export function legalTargets(from: string): Status[] {
  return [...(LEGAL_TRANSITIONS[from as Status] ?? [])];
}

/** Maps the server's transition rejection onto a friendly client message
 *  (task 01M3QWNGTJAKEAE05S5C8CDBD5, added scope). Server shape —
 *  internal/application/tasks.go returns domain.TransitionError{From, To},
 *  surfaced as a 400 `invalid_input` ApiError:
 *    "cannot transition status from \"todo\" to \"done\""
 *  This fires when the card's state changed mid-drag (another agent moved it),
 *  i.e. when a visually-legal drop becomes illegal server-side. Returns null
 *  for anything that is not this exact error shape. */
export function friendlyTransitionError(e: unknown): string | null {
  if (!(e instanceof Error)) return null;
  const m = /cannot transition status from "([^"]+)" to "([^"]+)"/.exec(e.message);
  if (!m) return null;
  return `Can't move ${statusLabel(m[1] as Status)} → ${statusLabel(m[2] as Status)} — that's not an allowed step.`;
}

/** Try to start a move for `id` on the shared in-flight set.
 *  Returns false — adding nothing — when that SAME task already has a move in
 *  flight (the caller should give feedback, not silently drop). Other task ids
 *  are never affected by an entry in the set. */
export function beginMove(busy: Set<string>, id: string): boolean {
  if (busy.has(id)) return false;
  busy.add(id);
  return true;
}

/** Release the lock for `id`. Idempotent — safe to call from every exit path. */
export function endMove(busy: Set<string>, id: string): void {
  busy.delete(id);
}

/** Thrown when a move's own timeout aborted the request — lets callers
 *  distinguish "the server never answered" from a real API error and show an
 *  honest message instead of a raw abort text. */
export class MoveTimedOutError extends Error {
  constructor() {
    super("move request timed out");
    this.name = "MoveTimedOutError";
  }
}

/** Result of a timed request: either the value, or the error plus whether the
 *  abort fired first (a stall) versus a genuine response error. */
export interface TimedResult<T> {
  value?: T;
  error?: unknown;
  /** True when the deadline passed and we aborted the request ourselves. */
  timedOut: boolean;
}

/** Run `fn(signal)` with an AbortController that fires after `ms`.
 *  NEVER throws for the timeout itself — it returns `{timedOut: true, error}`
 *  so callers can distinguish a stall from a real API failure and are forced
 *  to release their per-task lock in a finally either way. The timer is always
 *  cleared once the request settles (no dangling aborts on fast responses). */
export async function withTimeout<T>(
  ms: number,
  fn: (signal: AbortSignal) => Promise<T>,
): Promise<TimedResult<T>> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), ms);
  try {
    return { value: await fn(ctrl.signal), timedOut: false };
  } catch (e) {
    return { error: e, timedOut: ctrl.signal.aborted };
  } finally {
    clearTimeout(timer);
  }
}

// ------------------------------------------------------------------ drag + swipe decisions
// Pure drop/swipe DECISIONS shared by HTML5 drag-and-drop and the touch
// swipe-to-status gesture (both funnel into app.ts's moveTaskStatus). The DOM
// event handlers stay in the root; only the legality/threshold/message math is
// extracted here so it stays unit-testable.

/** May the currently-dragged card be dropped on `to`? Null source (drag not
 *  started / unknown) → everything stays legal: the grey-out styling for
 *  illegal columns reads this while a drag is in flight. */
export function dropAllowed(from: string | null, to: Status): boolean {
  return from ? canMove(from, to) : true;
}

/** The user-facing refusal for an illegal move (drag-drop and swipe share the
 *  exact wording). */
export function illegalMoveMessage(from: Status, to: Status): string {
  return `Can't move ${statusLabel(from)} → ${statusLabel(to)} — that's not an allowed step.`;
}

/** Swipe gesture thresholds. Slop + dominance ratio separate a horizontal
 *  intent from a vertical scroll / tap; commit requires clearly-past-threshold
 *  travel on release (swipes move exactly ONE column). */
export const SWIPE_SLOP_PX = 24;
export const SWIPE_DOMINANCE_RATIO = 1.5;
export const SWIPE_COMMIT_PX = 48;

/** Displayed swipe direction: left when an active gesture is pulling leftward
 *  (dx < 0), else right. */
export function swipeDirection(active: boolean, dx: number): "right" | "left" {
  return active && dx < 0 ? "left" : "right";
}

/** The adjacent status one step in `dir` from `from` — null at either end of
 *  STATUSES (no column that far) or when the source is unknown. */
export function swipeTargetStatus(from: Status | null, dir: "right" | "left"): Status | null {
  if (!from) return null;
  const idx = STATUSES.indexOf(from);
  const target = dir === "right" ? STATUSES[idx + 1] : STATUSES[idx - 1];
  return target ?? null;
}

/** Label chip for the swipe preview ("Move to Doing"): statusLabel of the
 *  adjacent column in `dir`, "" when there is no target. */
export function swipeTargetLabel(from: Status | null, dir: "right" | "left"): string {
  const target = swipeTargetStatus(from, dir);
  return target ? statusLabel(target) : "";
}

/** Activate a pending gesture once movement is clearly horizontal — past the
 *  slop and dominant over vertical travel (vs. a scroll). */
export function swipeActivated(dx: number, dy: number): boolean {
  return Math.abs(dx) > SWIPE_SLOP_PX && Math.abs(dx) > SWIPE_DOMINANCE_RATIO * Math.abs(dy);
}

/** Committed direction on release: |dx| must clear the commit threshold. Null =
 *  no move (snap the card back home). */
export function committedSwipeDirection(dx: number): "right" | "left" | null {
  if (dx > SWIPE_COMMIT_PX) return "right";
  if (dx < -SWIPE_COMMIT_PX) return "left";
  return null;
}
