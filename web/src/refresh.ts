// Refresh coalescing + stale-response protection for SSE-triggered reloads.
//
// Problem (task 01M3MMXYJXBSD4JTR6EK463M7E): every SSE event fired its own
// `loadBoard(true)` + `loadProjects(true)`. An agent batch of N events within
// milliseconds meant N parallel board rounds (~3 HTTP requests each: board +
// 2×listTasks for the blocked-set diff) with NO sequencing — an early SLOW
// response could resolve after a later fast one and overwrite fresh state, and
// a project A response could land after the user had already selected B.
//
// Two small pure helpers fix that:
//   - createDebounce: TRAILING debounce (burst < window → exactly ONE reload
//     after the quiet period). Window is 200ms at the SSE call site: agent
//     batches typically land in <100ms so one window coalesces a whole burst,
//     while human-perceived update latency stays under ~250ms (AC 1's bound).
//   - sequence tokens + shouldApplyBoard: every load captures the newest token
//     at start; a response whose token is no longer the newest — or whose
//     payload belongs to a project that is no longer selected — is discarded.
//     "Last initiated request wins", regardless of network arrival order.
//
// Deliberately DOM-free and timer-injectable so node unit tests can drive a
// virtual clock. Scope: board/projects reload sequencing only — the drawer's
// own refresh race (01M3MMY78F1TR6MKDRT0SKQWB) and api.ts request timeouts
// (01M3MMYBHECPH5Z5FRHJDS5G4J) are separate tasks.

/** Quiet window used for SSE-triggered reload coalescing (see file header). */
export const SSE_RELOAD_DEBOUNCE_MS = 200;

export interface TimerApi {
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(id: unknown): void;
}

const defaultTimers: TimerApi = {
  setTimer: (fn, ms) => setTimeout(fn, ms),
  clearTimer: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

/** Trailing debounce: every schedule() restarts the quiet window; `fn` runs
 *  exactly once, after the LAST schedule + ms. cancel() drops a pending call. */
export function createDebounce(
  fn: () => void,
  ms: number,
  timers: TimerApi = defaultTimers,
): { schedule(): void; cancel(): void; isPending(): boolean } {
  let timer: unknown = null;

  const clear = (): void => {
    if (timer !== null) {
      timers.clearTimer(timer);
      timer = null;
    }
  };

  return {
    schedule(): void {
      clear(); // trailing semantics — each request restarts the window
      timer = timers.setTimer(() => {
        timer = null;
        fn();
      }, ms);
    },
    cancel(): void {
      clear();
    },
    isPending(): boolean {
      return timer !== null;
    },
  };
}

/** Monotonic request-sequence guard. `next()` issues a new token at the moment
 *  a load STARTS (and thereby retires every older one); `latest()` READS the
 *  newest issued token without changing it; `isFresh` tells whether a given
 *  token is still the newest — i.e. no newer load has been initiated since. */
export function createSequenceGuard(): {
  next(): number;
  latest(): number;
  isFresh(token: number): boolean;
} {
  let latest = 0;
  return {
    next(): number {
      return ++latest;
    },
    latest(): number {
      return latest;
    },
    isFresh(token: number): boolean {
      return token === latest;
    },
  };
}

export interface BoardApplyDecision {
  token: number;
  latestToken: number;
  /** project_id carried by the fetched payload (Board.project_id). */
  payloadProjectId: string | null;
  /** The selected project at the moment the response is about to be applied. */
  selectedProjectId: string;
}

/** In-flight coalescing for duplicate CONCURRENT calls. Observed at boot:
 *  Alpine re-runs init() in some environments, racing two list-projects calls;
 *  a sequence guard alone then discards the first response and that caller
 *  resumes against empty state. Coalescing makes the second call JOIN the
 *  running one (shared promise), so no caller ever sees the gap: one network
 *  request per quiet interval, every awaiter gets the same settled result. */
export function coalesceInFlight<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
): { call: (...args: A) => Promise<R>; inFlight(): boolean } {
  let current: Promise<R> | null = null;
  return {
    call: (...args: A): Promise<R> => {
      if (!current) {
        current = fn(...args).finally(() => {
          current = null;
        });
      }
      return current;
    },
    inFlight(): boolean {
      return current !== null;
    },
  };
}

/** Pure apply-gate for a fetched board payload. A payload may overwrite state
 *  only if (a) its request was still the newest initiated when it arrived — a
 *  newer load's data must win even if this response lands later — and (b) it
 *  actually belongs to the currently selected project (a late response for
 *  project A must never render into project B's board). Unknown ownership is
 *  treated as "do not apply" (fail closed). */
export function shouldApplyBoard(d: BoardApplyDecision): boolean {
  if (d.token !== d.latestToken) return false; // stale — a newer load was initiated
  if (!d.payloadProjectId || !d.selectedProjectId) return false; // fail closed
  return d.payloadProjectId === d.selectedProjectId;
}
