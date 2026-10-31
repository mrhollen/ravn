import test from "node:test";
import assert from "node:assert/strict";

import { canApplyDetail, coalesceInFlight, createDebounce, createSequenceGuard, shouldApplyBoard } from "../src/refresh.ts";


function fakeClock() {
  let now = 0;
  let nextId = 1;
  const timers = new Map<number, { at: number; fn: () => void }>();
  return {
    setTimer(fn: () => void, ms: number) {
      const id = nextId++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimer(id: unknown) {
      timers.delete(id as number);
    },
    /** Advance the clock, running any timers that come due along the way. */
    advance(ms: number) {
      const target = now + ms;
      for (;;) {
        let earliestId: number | null = null;
        let earliestAt = Infinity;
        for (const [id, t] of timers) {
          if (t.at <= target && t.at < earliestAt) {
            earliestAt = t.at;
            earliestId = id;
          }
        }
        if (earliestId === null) break;
        now = earliestAt;
        const t = timers.get(earliestId)!;
        timers.delete(earliestId);
        t.fn();
      }
      now = target;
    },
  };
}

test("Drawer: canApplyDetail only admits an open drawer's own payload", () => {
  assert.equal(canApplyDetail("A", "A", true), true, "open + matching id applies");
  assert.equal(canApplyDetail("A", "B", true), false, "foreign task id never applies (switch mid-fetch)");
  assert.equal(canApplyDetail("A", "A", false), false, "closed drawer ignores in-flight fetches");
  assert.equal(canApplyDetail(null, "A", true), false, "null payload id fails closed");
  assert.equal(canApplyDetail("", "A", true), false, "empty payload id fails closed");
  assert.equal(canApplyDetail("A", null, true), false, "no active task id fails closed");
});

test("Refresh: trailing debounce coalesces a burst into ONE call after the quiet window", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);

  // Burst of 4 "events" within the 200ms window (each restarts the window).
  d.schedule(); // t=0
  clock.advance(50);
  d.schedule(); // t=50
  clock.advance(70);
  d.schedule(); // t=120
  clock.advance(30);
  d.schedule(); // t=150

  clock.advance(49); // t=199: window from last (150+200=350) not elapsed yet
  assert.equal(calls, 0, "no reload before the quiet period ends");
  clock.advance(50); // t=249 — still before t=350
  assert.equal(calls, 0, "window is measured from the LAST event, not the first");

  clock.advance(101); // t=350: due
  assert.equal(calls, 1, "burst produced EXACTLY one reload");
  clock.advance(500); // nothing scheduled since → no more calls
  assert.equal(calls, 1);
});

test("Refresh: a lone schedule fires once at the window edge", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);
  d.schedule();
  clock.advance(199);
  assert.equal(calls, 0);
  clock.advance(1); // exactly t=200
  assert.equal(calls, 1);
});

test("Refresh: cancel() drops the pending call; isPending reflects state", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);
  assert.equal(d.isPending(), false);
  d.schedule();
  assert.equal(d.isPending(), true);
  d.cancel();
  assert.equal(d.isPending(), false);
  clock.advance(1000);
  assert.equal(calls, 0, "cancelled reload never fired");
});

test("Refresh: sequence guard — next() retires older tokens, latest() is a read", () => {
  const g = createSequenceGuard();
  const t1 = g.next();
  const t2 = g.next();
  assert.equal(g.latest(), t2, "latest() reads without issuing");
  assert.equal(g.isFresh(t2), true);
  assert.equal(g.isFresh(t1), false, "an earlier-initiated load is stale after a newer one");
});

test("Refresh: shouldApplyBoard — fresh + owned applies; stale or foreign-project never", () => {
  const ok = { token: 5, latestToken: 5, payloadProjectId: "P1", selectedProjectId: "P1" };
  assert.equal(shouldApplyBoard(ok), true);

  assert.equal(
    shouldApplyBoard({ ...ok, token: 4 }),
    false,
    "stale response (newer load initiated) must not overwrite fresh state",
  );
  assert.equal(
    shouldApplyBoard({ ...ok, selectedProjectId: "P2" }),
    false,
    "late project-A payload must never render into selected project B",
  );
  assert.equal(shouldApplyBoard({ ...ok, payloadProjectId: null }), false, "unknown ownership fails closed");
  assert.equal(shouldApplyBoard({ ...ok, selectedProjectId: "" }), false, "no project selected → fail closed");
});

test("Refresh: coalesceInFlight — concurrent duplicates share ONE execution", async () => {
  let runs = 0;
  const slow = (ms: number) => new Promise((res) => setTimeout(res, ms));
  const c = coalesceInFlight(async (x: number) => { runs++; await slow(20); return x * 10; });

  const p1 = c.call(1); // first caller starts the flight
  await new Promise((r) => setTimeout(r, 5)); // while in flight…
  const p2 = c.call(99); // …a duplicate JOINS it (args ignored, shared promise)
  assert.equal(c.inFlight(), true);

  const [r1, r2] = await Promise.all([p1, p2]);
  assert.equal(runs, 1, "underlying fn ran exactly once");
  assert.equal(r1, 10);
  assert.equal(r2, 10, "joiner gets the same settled result");
  assert.equal(c.inFlight(), false, "flight cleared after settle");

  await c.call(2); // a later call runs again (no caching)
  assert.equal(runs, 2, "coalescing is per-interval, not a memo cache");
});

test("Refresh: coalesceInFlight — a failed flight resets so the next call retries", async () => {
  let runs = 0;
  let failNext = true;
  const c = coalesceInFlight(async (): Promise<string> => {
    runs++;
    if (failNext) throw new Error("boom");
    return "ok";
  });

  const p1 = c.call();
  const p2 = c.call(); // joins the failing flight
  await assert.rejects(() => p1, /boom/);
  await assert.rejects(() => p2, /boom/, "joiner sees the same rejection");
  assert.equal(c.inFlight(), false, "failed flight is released too");

  failNext = false;
  assert.equal(await c.call(), "ok", "after a failure the next call re-runs (no stuck coalescer)");
  assert.equal(runs, 2, "exactly two executions: the failed flight + the successful retry");
});
