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
 *  surfaced as a 400 `invalid input` ApiError:
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
