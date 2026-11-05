import test from "node:test";
import assert from "node:assert/strict";

import { beginMove, canMove, endMove, friendlyTransitionError, legalTargets, withTimeout } from "../src/moves.ts";

test("Moves: beginMove allows a task, then blocks a DUPLICATE of the same id only", () => {
  const busy = new Set<string>();
  assert.equal(beginMove(busy, "A"), true); // A starts moving
  assert.equal(beginMove(busy, "A"), false); // duplicate concurrent move of A is blocked
  assert.equal(beginMove(busy, "B"), true); // a DIFFERENT task stays fully interactive
  assert.deepEqual(
    [...busy].sort(),
    ["A", "B"],
  );
});

test("Moves: endMove releases the lock so the same task can move again (idempotent)", () => {
  const busy = new Set<string>();
  beginMove(busy, "A");
  endMove(busy, "A");
  assert.equal(beginMove(busy, "A"), true); // re-allowed after release
  endMove(busy, "A");
  endMove(busy, "A"); // idempotent — no throw
  assert.equal([...busy].length, 0);
});

test("Moves: withTimeout resolves fast calls untouched and does not abort", async () => {
  let seenAborted = false;
  const r = await withTimeout(50, async (signal) => {
    seenAborted = signal.aborted;
    return "ok";
  });
  assert.equal(r.value, "ok");
  assert.equal(r.timedOut, false);
  assert.equal(seenAborted, false); // fast path never trips the abort
});

test("Moves: withTimeout reports timedOut=true when the deadline passes", async () => {
  const r = await withTimeout(20, (signal) => new Promise((_res, rej) => {
    signal.addEventListener("abort", () => rej(new Error("aborted")));
  }));
  assert.equal(r.value, undefined);
  assert.equal(r.timedOut, true); // we aborted it ourselves — a stall, not an API error
  assert.ok(r.error instanceof Error);
});

test("Moves: withTimeout distinguishes a real error from a timeout (timedOut=false)", async () => {
  const boom = new Error("version_conflict");
  const r = await withTimeout(100, async () => {
    throw boom;
  });
  assert.equal(r.value, undefined);
  assert.equal(r.timedOut, false); // the fn threw its own error before the deadline
  assert.equal(r.error, boom);
});

test("Moves: withTimeout clears its timer once the request settles (no dangling abort)", async () => {
  // A call that resolves well before a SHORT deadline must leave no pending
  // abort — if the timer leaked, it would fire during the wait below.
  let abortedAfterSettle = false;
  const r = await withTimeout(60, (signal) => {
    signal.addEventListener("abort", () => (abortedAfterSettle = true));
    return "done";
  });
  assert.equal(r.value, "done");
  // Past the 60ms deadline: a leaked timer would have aborted by now.
  await new Promise((res) => setTimeout(res, 100));
  assert.equal(abortedAfterSettle, false);
});

test("Moves: canMove mirrors the full 9-pair server transition matrix", () => {
  const pairs: Array<[string, string, boolean]> = [
    // legal (the only three)
    ["todo", "doing", true],
    ["doing", "done", true],
    ["doing", "todo", true],
    ["done", "todo", true],
    // illegal — skipping a step / going backwards through doing
    ["todo", "done", false],
    ["done", "doing", false],
    // self-transitions: rejected by domain.CanTransition (API no-ops them)
    ["todo", "todo", false],
    ["doing", "doing", false],
    ["done", "done", false],
  ];
  for (const [from, to, want] of pairs) {
    assert.equal(canMove(from, to), want, `canMove(${from}, ${to})`);
  }
});

test("Moves: canMove fails closed on unknown/empty statuses (like the server)", () => {
  for (const [from, to] of [
    ["", "todo"],
    ["todo", ""],
    ["unknown", "unknown"],
    ["doing", "invalid"],
  ] as Array<[string, string]>) {
    assert.equal(canMove(from, to), false, `canMove(${from}, ${to})`);
  }
});

test("Moves: legalTargets returns each status' allowed destinations (server order)", () => {
  assert.deepEqual(legalTargets("todo"), ["doing"]);
  assert.deepEqual(legalTargets("doing"), ["done", "todo"]);
  assert.deepEqual(legalTargets("done"), ["todo"]);
  assert.deepEqual(legalTargets("nonsense"), []);
  const copy = legalTargets("doing");
  copy.push("nope");
  assert.deepEqual(legalTargets("doing"), ["done", "todo"], "returns a fresh copy, not the map entry");
});

test("Moves: friendlyTransitionError maps the server's ErrInvalid message to a friendly toast", () => {
  // Exact server shape — internal/application/tasks.go wraps domain.ErrInvalid:
  const e = new Error('invalid_input: cannot transition status from "todo" to "done"');
  assert.equal(friendlyTransitionError(e), "Can't move To Do → Done — that's not an allowed step.");

  const e2 = new Error('invalid_input: cannot transition status from "done" to "doing"');
  assert.equal(friendlyTransitionError(e2), "Can't move Done → Doing — that's not an allowed step.");

  // Anything else (network errors, version conflicts, plain failures) passes through untouched.
  assert.equal(friendlyTransitionError(new Error("version conflict")), null);
  assert.equal(friendlyTransitionError(new TypeError("fetch failed")), null);
  assert.equal(friendlyTransitionError(null), null);
  assert.equal(friendlyTransitionError("cannot transition status from \"todo\" to \"done\""), null, "non-Error values are not interpreted");
});
