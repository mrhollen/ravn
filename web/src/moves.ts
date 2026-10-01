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

/** How long a status-move request may run before we abort it and release the lock. */
export const MOVE_TIMEOUT_MS = 10_000;

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
