// Unit tests for src/modules/claims.ts — the claim/release/heartbeat drawer
// section extracted from web/src/app.ts (Phase 2 batch C). Covers: the pure
// display helpers (countdown formatting, live expiry flip) against an injected
// nowTick clock; claim/release/happy paths with detail patching under the
// same-task guard; busy guards; error toasts (incl. the NotClaimed heartbeat
// special case); and the alias surface (claimTask/releaseTask/heartbeatTask).
//
// The module is pure: a mutable stub state object wired into the ClaimsDeps
// accessors + counters for every out-bound write + monkey-patched api methods,
// all restored in finally blocks (same pattern as acs-module.test.ts).

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createClaimsModule } from "../src/modules/claims";
import { api, ApiError } from "../src/api";
import type { ActorInput, TaskDetail, TaskSummary } from "../src/types";

// ----------------------------------------------------------------------- helpers
function makeTask(over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: "T1",
    project_id: "P1",
    seq: 1,
    title: "Server truth title",
    description: "server desc",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 7,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    completed_by: null,
    completed_at: null,
    assignee: undefined,
    ...over,
  };
}

const ACTOR: ActorInput = { name: "tester", type: "agent" };
const BASE = Date.parse("2026-01-03T00:00:00Z");

function freshSummary(over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id: "T1",
    project_id: "P1",
    seq: 1,
    title: "t",
    description: "",
    status: "doing",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 8,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-03T00:01:00Z",
    claimed_by: ACTOR,
    claim_expires_at: new Date(BASE + 90_000).toISOString(),
    ...over,
  };
}

interface Ctx {
  mod: ReturnType<typeof createClaimsModule>;
  state: Record<string, unknown>;
  detail: TaskDetail | null;
  toasts: Array<{ msg?: string; kind?: string }>;
  refreshCalls: number[];
  loadBoardCalls: number[];
}

function makeCtx(detail: TaskDetail | null): Ctx {
  const state: Record<string, unknown> = { nowTick: BASE, claimBusy: false };
  const ctx: Ctx = {
    mod: undefined as never,
    state,
    detail,
    toasts: [],
    refreshCalls: [],
    loadBoardCalls: [],
  };
  ctx.mod = createClaimsModule({
    // state-in
    detail: () => ctx.detail,
    nowTick: () => state.nowTick as number,
    claimBusy: () => state.claimBusy as boolean,
    // state-out
    setClaimBusy: (v) => { state.claimBusy = v; },
    // hooks
    toast: (msg, kind) => { ctx.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
    refreshDetail: (f) => { ctx.refreshCalls.push(f); return Promise.resolve(); },
    loadBoard: (f) => { ctx.loadBoardCalls.push(f); return Promise.resolve(); },
  });
  return ctx;
}

type ClaimApi = "claimTask" | "releaseTask" | "heartbeatTask";

/** Restore the given api method after the calling test body runs. */
function withApiRestored(
  name: ClaimApi,
  patch: (current: unknown) => unknown,
): { run: <T>(fn: () => Promise<T> | T) => Promise<T>; key: string } {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = patch(orig);
  return {
    key: name,
    async run(fn) {
      try {
        return await fn();
      } finally {
        (api as Record<string, unknown>)[name] = orig;
      }
    },
  };
}

// ----------------------------------------------------------------------- display helpers
describe("claimCountdown (pure, driven by injected nowTick)", () => {
  test("formats MM:SS against the pinned clock and ticks down", () => {
    const c = makeCtx(null);
    state_now(c, BASE);
    const exp = new Date(BASE + 90_000).toISOString();
    assert.equal(c.mod.claimCountdown({ claim_expires_at: exp }), "1:30");
    c.state.nowTick = BASE + 10_000;
    assert.equal(c.mod.claimCountdown({ claim_expires_at: exp }), "1:20");
    c.state.nowTick = BASE + 95_000;
    assert.equal(c.mod.claimCountdown({ claim_expires_at: exp }), "0:00");
  });

  test("empty for null / missing / unparseable expiry", () => {
    const c = makeCtx(null);
    assert.equal(c.mod.claimCountdown(null), "");
    assert.equal(c.mod.claimCountdown(undefined), "");
    assert.equal(c.mod.claimCountdown({}), "");
    assert.equal(c.mod.claimCountdown({ claim_expires_at: "not-a-date" }), "");
  });
});

describe("claimExpired (pure, driven by injected nowTick)", () => {
  test("flips live the moment the lease crosses expiry", () => {
    const c = makeCtx(null);
    const t: Pick<TaskSummary, "claimed_by" | "claim_expires_at"> = {
      claimed_by: ACTOR,
      claim_expires_at: new Date(BASE + 90_000).toISOString(),
    };
    assert.equal(c.mod.claimExpired(t), false); // 1m30s left — active
    c.state.nowTick = BASE + 60_000;
    assert.equal(c.mod.claimExpired(t), false); // 30s left — still active
    c.state.nowTick = BASE + 90_000;
    assert.equal(c.mod.claimExpired(t), true); // crossed expiry → flips live
  });

  test("unclaimed or claimed-without-expiry never flips", () => {
    const c = makeCtx(null);
    assert.equal(c.mod.claimExpired({}), false);
    assert.equal(c.mod.claimExpired({ claimed_by: ACTOR }), false);
    c.state.nowTick = BASE + 10 * 3600_000;
    assert.equal(c.mod.claimExpired({ claim_expires_at: "bad" }), false); // unparseable → not expired
  });
});

function state_now(c: Ctx, ms: number): void { c.state.nowTick = ms; }

// ----------------------------------------------------------------------- claimCurrentTask
describe("claimCurrentTask", () => {
  test("guard: no detail → no api call, busy untouched", async () => {
    const c = makeCtx(null);
    const rec: unknown[][] = [];
    const w = withApiRestored("claimTask", () => async (...a: unknown[]) => { rec.push(a); return freshSummary(); });
    await w.run(() => c.mod.claimCurrentTask());
    assert.equal(rec.length, 0);
    assert.equal(c.state.claimBusy, false);
    assert.equal(c.toasts.length, 0);
  });

  test("guard: another claim in flight → no api call", async () => {
    const c = makeCtx(makeTask());
    c.state.claimBusy = true;
    const rec: unknown[][] = [];
    const w = withApiRestored("claimTask", () => async (...a: unknown[]) => { rec.push(a); return freshSummary(); });
    await w.run(() => c.mod.claimCurrentTask());
    assert.equal(rec.length, 0);
  });

  test("success: patches detail under same-task guard, refresh+board reloads, success toast, busy reset", async () => {
    const c = makeCtx(makeTask({ status: "todo", version: 7 }));
    const rec: unknown[][] = [];
    const w = withApiRestored("claimTask", () => async (...a: unknown[]) => { rec.push(a); return freshSummary(); });
    await w.run(() => c.mod.claimCurrentTask());

    assert.equal(rec.length, 1);
    assert.equal(rec[0][0], "T1");
    assert.deepEqual(rec[0][1], ACTOR); // actor hook passed through
    assert.equal(c.detail!.status, "doing");
    assert.equal(c.detail!.version, 8);
    assert.deepEqual(c.detail!.claimed_by, ACTOR);
    assert.equal(c.detail!.claim_expires_at, freshSummary().claim_expires_at);
    assert.equal(c.detail!.updated_at, "2026-01-03T00:01:00Z");
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.deepEqual(c.toasts, [{ msg: "Claimed task as tester", kind: undefined }]);
    assert.equal(c.state.claimBusy, false); // reset in finally
  });

  test("mid-flight switch → detail NOT patched, but reloads + toast still run", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("claimTask", () => async () => {
      ctx_switch(c, makeTask({ id: "T2" })); // user opened a different task while in flight
      return freshSummary();
    });
    await w.run(() => c.mod.claimCurrentTask());
    // The switched-in detail is untouched (guard on id), yet the reloads and
    // toast fire regardless — parity with the extracted root behavior.
    assert.equal((c.detail as TaskDetail).id, "T2");
    assert.equal(c.detail!.version, 7); // not bumped by the stale response
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.equal(c.toasts.length, 1);
  });

  test("api failure → error toast, busy reset, no reloads", async () => {
    const c = makeCtx(makeTask());
    const w = withApiRestored("claimTask", () => async () => { throw new Error("boom"); });
    await w.run(() => c.mod.claimCurrentTask());
    assert.deepEqual(c.toasts, [{ msg: "boom", kind: "error" }]);
    assert.equal(c.state.claimBusy, false);
    assert.deepEqual(c.refreshCalls, []);
  });
});

// ----------------------------------------------------------------------- releaseCurrentTask
describe("releaseCurrentTask", () => {
  test("success: clears claim fields, bumps version, reloads, toast", async () => {
    const c = makeCtx(makeTask({ status: "doing", version: 8, claimed_by: ACTOR, claim_expires_at: new Date(BASE).toISOString() }));
    const rec: unknown[][] = [];
    const w = withApiRestored("releaseTask", () => async (...a: unknown[]) => {
      rec.push(a);
      return freshSummary({ status: "todo", version: 9, claimed_by: undefined as never, claim_expires_at: undefined as never });
    });
    await w.run(() => c.mod.releaseCurrentTask());
    assert.equal(rec[0][1], ACTOR);
    // Release path UNCONDITIONALLY clears the local claim fields when same-task.
    assert.equal(c.detail!.status, "todo");
    assert.equal(c.detail!.version, 9);
    assert.equal(c.detail!.claimed_by, undefined);
    assert.equal(c.detail!.claim_expires_at, undefined);
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, [true]);
    assert.deepEqual(c.toasts, [{ msg: "Released claim on task", kind: undefined }]);
  });

  test("api failure → error toast, busy reset", async () => {
    const c = makeCtx(makeTask({ claimed_by: ACTOR }));
    const w = withApiRestored("releaseTask", () => async () => { throw new Error("denied"); });
    await w.run(() => c.mod.releaseCurrentTask());
    assert.deepEqual(c.toasts, [{ msg: "denied", kind: "error" }]);
    assert.equal(c.detail!.claimed_by, ACTOR); // untouched on failure
    assert.equal(c.state.claimBusy, false);
  });
});

// ----------------------------------------------------------------------- heartbeatCurrentTask
describe("heartbeatCurrentTask", () => {
  test("success: bumps ONLY claim_expires_at (no version/status patch), refreshes detail (not board)", async () => {
    const c = makeCtx(makeTask({ status: "doing", version: 8, claimed_by: ACTOR }));
    const extended = new Date(BASE + 500_000).toISOString();
    const w = withApiRestored("heartbeatTask", () => async () => freshSummary({ claim_expires_at: extended, version: 99 }));
    await w.run(() => c.mod.heartbeatCurrentTask());
    assert.equal(c.detail!.claim_expires_at, extended);
    // Heartbeat is NOT a task mutation — version/status stay as they were.
    assert.equal(c.detail!.version, 8);
    assert.deepEqual(c.refreshCalls, [true]);
    assert.deepEqual(c.loadBoardCalls, []);
    assert.deepEqual(c.toasts, [{ msg: "Heartbeat recorded — lease extended", kind: undefined }]);
  });

  test("NotClaimed 409 → actionable 'claim it first' toast (not raw body)", async () => {
    const c = makeCtx(makeTask());
    const notClaimed = new ApiError(409, { error: "not_claimed", message: "no lease" } as never);
    const w = withApiRestored("heartbeatTask", () => async () => { throw notClaimed; });
    await w.run(() => c.mod.heartbeatCurrentTask());
    assert.deepEqual(c.toasts, [{ msg: "No claim lease on this task — claim it first.", kind: "error" }]);
  });

  test("other 409 (claim conflict) → generic error toast, not the special case", async () => {
    const c = makeCtx(makeTask());
    const conflict = new ApiError(409, { error: "claim_conflict", message: "conflict with alice" } as never);
    assert.equal(ApiError.isNotClaimed(conflict), false);
    const w = withApiRestored("heartbeatTask", () => async () => { throw conflict; });
    await w.run(() => c.mod.heartbeatCurrentTask());
    assert.deepEqual(c.toasts, [{ msg: "conflict with alice", kind: "error" }]);
  });

  test("no detail → no api call", async () => {
    const c = makeCtx(null);
    const rec: unknown[][] = [];
    const w = withApiRestored("heartbeatTask", () => async (...a: unknown[]) => { rec.push(a); return freshSummary(); });
    await w.run(() => c.mod.heartbeatCurrentTask());
    assert.equal(rec.length, 0);
  });
});

// ----------------------------------------------------------------------- aliases
describe("alias surface (claimTask / releaseTask / heartbeatTask)", () => {
  test("each alias delegates to its *CurrentTask implementation", async () => {
    const c = makeCtx(makeTask());
    const claimed: unknown[][] = [];
    const released: unknown[][] = [];
    const beats: unknown[][] = [];
    // Claim alias
    let w = withApiRestored("claimTask", () => async (...a: unknown[]) => { claimed.push(a); return freshSummary(); });
    await w.run(() => c.mod.claimTask());
    assert.equal(claimed.length, 1);
    // Release alias
    w = withApiRestored("releaseTask", () => async (...a: unknown[]) => { released.push(a); return freshSummary({ status: "todo" }); });
    await w.run(() => c.mod.releaseTask());
    assert.equal(released.length, 1);
    // Heartbeat alias
    w = withApiRestored("heartbeatTask", () => async (...a: unknown[]) => { beats.push(a); return freshSummary(); });
    await w.run(() => c.mod.heartbeatTask());
    assert.equal(beats.length, 1);
  });
});

function ctx_switch(c: Ctx, next: TaskDetail): void { c.detail = next; }
