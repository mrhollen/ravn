// Claims drawer section — state + handlers (Phase 2, batch C).
//
// Extracted from app.ts as a plain factory module (same pattern as acs.ts /
// comments.ts). `claimBusy` is template-bound in index.html (button disable /
// "Claiming…" label), and `nowTick`/`_tickerId` are the shared 1s ticker that
// ALSO drives the board stalled filter → all three stay flat plain root
// fields; the module reaches them through ClaimsDeps and owns no closure-
// internal state. Bodies moved VERBATIM — `this.` rewritten to deps
// accessors, otherwise byte-identical. The pure countdown/expiry display
// helpers already live in src/util.ts (formatClaimCountdown / isClaimExpired).
// app.ts keeps thin delegation shims (claimTask/releaseTask/heartbeatTask
// aliases stay at root pointing at the *CurrentTask shims; index.html binds
// claimTask()/releaseTask() and heartbeatTask() is intentionally NOT bound —
// test/app.test.ts asserts its absence from the HTML).

import { api, ApiError, errMsg } from "../api";
import type { ActorInput, TaskDetail, TaskSummary } from "../types";
import { formatClaimCountdown, isClaimExpired } from "../util";

export interface ClaimsDeps {
  // -- state-in ---------------------------------------------------------
  detail: () => TaskDetail | null;
  /** Shared 1s clock (root-owned: the board stalled filter reads it too). */
  nowTick: () => number;
  claimBusy: () => boolean;

  // -- state-out --------------------------------------------------------
  setClaimBusy: (v: boolean) => void;

  // -- hooks ------------------------------------------------------------
  actor: () => ActorInput;
  toast: (msg: string, kind?: "error" | "info") => number;
  refreshDetail: (force?: boolean) => Promise<unknown>;
  loadBoard: (force?: boolean) => Promise<unknown>;
}

export function createClaimsModule(deps: ClaimsDeps) {
  return {
    /** Live-ticking MM:SS countdown until lease expiry for the claim capsule. */
    claimCountdown(t?: Pick<TaskSummary, "claim_expires_at"> | null): string {
      return formatClaimCountdown(t?.claim_expires_at, deps.nowTick());
    },

    /** Whether a task's claim lease has EXPIRED. Reads `nowTick` (the same
     *  value claimCountdown() uses) so Alpine tracks it: the muted "expired"
     *  styling flips live the moment a lease crosses expiry, without any poll. */
    claimExpired(t: Pick<TaskSummary, "claimed_by" | "claim_expires_at">): boolean {
      return isClaimExpired(t, deps.nowTick());
    },

    async claimCurrentTask(): Promise<void> {
      const d = deps.detail();
      if (!d || deps.claimBusy()) return;
      deps.setClaimBusy(true);
      try {
        const fresh = await api.claimTask(d.id, deps.actor());
        const cur = deps.detail();
        if (cur && cur.id === d.id) {
          cur.status = fresh.status;
          cur.version = fresh.version;
          cur.claimed_by = fresh.claimed_by;
          cur.claim_expires_at = fresh.claim_expires_at;
          cur.updated_at = fresh.updated_at;
        }
        await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
        deps.toast(`Claimed task as ${deps.actor().name}`);
      } catch (e) {
        deps.toast(errMsg(e), "error");
      } finally {
        deps.setClaimBusy(false);
      }
    },

    async releaseCurrentTask(): Promise<void> {
      const d = deps.detail();
      if (!d || deps.claimBusy()) return;
      deps.setClaimBusy(true);
      try {
        const fresh = await api.releaseTask(d.id, deps.actor());
        const cur = deps.detail();
        if (cur && cur.id === d.id) {
          cur.status = fresh.status;
          cur.version = fresh.version;
          cur.claimed_by = undefined;
          cur.claim_expires_at = undefined;
          cur.updated_at = fresh.updated_at;
        }
        await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
        deps.toast("Released claim on task");
      } catch (e) {
        deps.toast(errMsg(e), "error");
      } finally {
        deps.setClaimBusy(false);
      }
    },

    async heartbeatCurrentTask(): Promise<void> {
      const d = deps.detail();
      if (!d || deps.claimBusy()) return;
      deps.setClaimBusy(true);
      try {
        const fresh = await api.heartbeatTask(d.id, deps.actor());
        const cur = deps.detail();
        if (cur && cur.id === d.id) {
          cur.claim_expires_at = fresh.claim_expires_at;
        }
        await deps.refreshDetail(true);
        deps.toast("Heartbeat recorded — lease extended");
      } catch (e) {
        if (ApiError.isNotClaimed(e)) {
          // No lease exists at all (not a conflict with a live holder) — say
          // the actionable part plainly instead of echoing the raw body.
          deps.toast("No claim lease on this task — claim it first.", "error");
        } else {
          deps.toast(errMsg(e), "error");
        }
      } finally {
        deps.setClaimBusy(false);
      }
    },

    // -- alias surface (root shims keep calling these) --------------------
    claimTask(): Promise<void> {
      return this.claimCurrentTask();
    },

    releaseTask(): Promise<void> {
      return this.releaseCurrentTask();
    },

    heartbeatTask(): Promise<void> {
      return this.heartbeatCurrentTask();
    },
  };
}

export type ClaimsModule = ReturnType<typeof createClaimsModule>;
