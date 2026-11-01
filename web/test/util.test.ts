import test from "node:test";
import assert from "node:assert/strict";

import {
  dateInputToIso,
  dueCalendarDate,
  fmtDueDate,
  formatClaimCountdown,
  isClaimExpired,
  isOverdue,
  isStalledExpired,
  isTextArtifact,
  needsAttention,
  safeStorage,
} from "../src/util.ts";
import { localYmd, withWindow } from "./test-helpers.ts";

test("R8: safeStorage basic in-memory operations", () => {
  safeStorage.clear();
  assert.equal(safeStorage.getItem("test_key"), null);

  safeStorage.setItem("test_key", "test_val");
  assert.equal(safeStorage.getItem("test_key"), "test_val");

  safeStorage.setItem("test_key", "new_val");
  assert.equal(safeStorage.getItem("test_key"), "new_val");

  safeStorage.removeItem("test_key");
  assert.equal(safeStorage.getItem("test_key"), null);

  safeStorage.setItem("k1", "v1");
  safeStorage.setItem("k2", "v2");
  safeStorage.clear();
  assert.equal(safeStorage.getItem("k1"), null);
  assert.equal(safeStorage.getItem("k2"), null);
});

test("R8: safeStorage handles DOMException: SecurityError gracefully", () => {
  // Mock window.localStorage to throw a SecurityError (mimics sandboxed iframe or blocked cookies)
  const win: any = {};
  Object.defineProperty(win, "localStorage", {
    get() {
      const err = new Error("The operation is insecure.");
      err.name = "SecurityError";
      throw err;
    },
    configurable: true,
  });

  withWindow(win, () => {
    // None of these should throw
    assert.doesNotThrow(() => {
      safeStorage.setItem("secure_test", "in_memory_data");
    });
    assert.equal(safeStorage.getItem("secure_test"), "in_memory_data");

    assert.doesNotThrow(() => {
      safeStorage.removeItem("secure_test");
    });
    assert.equal(safeStorage.getItem("secure_test"), null);
  });
});

test("R8: safeStorage handles localStorage.setItem QuotaExceededError gracefully", () => {
  const fakeStore = new Map<string, string>();
  const win: any = {
    localStorage: {
      getItem(k: string) {
        return fakeStore.get(k) ?? null;
      },
      setItem(_k: string, _v: string) {
        const err = new Error("QuotaExceededError");
        err.name = "QuotaExceededError";
        throw err;
      },
      removeItem(k: string) {
        fakeStore.delete(k);
      },
      clear() {
        fakeStore.clear();
      },
    },
  };

  withWindow(win, () => {
    // setItem throws QuotaExceededError, should fall back to memoryStorage
    assert.doesNotThrow(() => {
      safeStorage.setItem("quota_key", "saved_in_mem");
    });
    assert.equal(safeStorage.getItem("quota_key"), "saved_in_mem");
  });
});

test("Due: dueCalendarDate extracts the UTC calendar day in every timezone", () => {
  // This app's own storage format (UTC midnight) — same answer in any TZ.
  assert.equal(dueCalendarDate("2026-10-01T00:00:00Z"), "2026-10-01");
  assert.equal(dueCalendarDate("2028-02-29T00:00:00Z"), "2028-02-29"); // leap day (2028 is a leap year)
  assert.equal(dueCalendarDate("1999-12-31T23:59:59Z"), "1999-12-31");
  // Non-Z offsets written by external tools (instant == Oct 1 00:00Z).
  assert.equal(dueCalendarDate("2026-10-01T08:00:00+08:00"), "2026-10-01");
  assert.equal(dueCalendarDate("2026-10-01T05:30:00+05:30"), "2026-10-01");
  assert.equal(dueCalendarDate("2026-09-30T19:00:00-05:00"), "2026-10-01"); // western offset, same instant
  assert.equal(dueCalendarDate("2026-10-01T12:00:00Z"), "2026-10-01"); // mid-day instant, same calendar day
  assert.equal(dueCalendarDate("not-a-date"), "");
});

test("Due: fmtDueDate renders the encoded day identically in every timezone (off-by-one regression)", () => {
  // The original bug: this rendered "Sep 30" in browsers west of UTC.
  assert.equal(fmtDueDate("2026-10-01T00:00:00Z"), "Oct 1");
  assert.equal(fmtDueDate("2028-02-29T00:00:00Z"), "Feb 29, 2028"); // other year → includes the year
  // Year appears when it isn't the current one.
  const otherYear = fmtDueDate("2025-01-02T00:00:00Z");
  assert.ok(otherYear.includes("Jan 2"), `got "${otherYear}"`);
  assert.ok(otherYear.includes("2025"), `got "${otherYear}"`);
  const currentYear = fmtDueDate(`${new Date().getFullYear()}-07-04T00:00:00Z`);
  assert.ok(!/19\d{2}|20\d{2}/.test(currentYear), `current-year label must omit the year, got "${currentYear}"`);
  // Garbage passes through untouched.
  assert.equal(fmtDueDate("nonsense"), "nonsense");
});

test("Due: isOverdue boundary — a task due today is never overdue during its due day", () => {
  // The original bug marked this overdue from ~8pm local the previous evening.
  assert.equal(isOverdue({ due_at: `${localYmd(0)}T00:00:00Z`, status: "todo" }), false, "due today → not overdue");
  assert.equal(isOverdue({ due_at: `${localYmd(-1)}T00:00:00Z`, status: "todo" }), true, "due yesterday → overdue");
  assert.equal(isOverdue({ due_at: `${localYmd(1)}T00:00:00Z`, status: "todo" }), false, "due tomorrow → not overdue");
  assert.equal(isOverdue({ due_at: undefined as unknown as string, status: "todo" }), false);
  assert.equal(isOverdue({ due_at: `${localYmd(-5)}T00:00:00Z`, status: "done" }), false, "done tasks never overdue");
});

test("Due: dateInputToIso round-trips through the picker without shifting the day", () => {
  const picked = "2026-10-01"; // what the <input type="date"> holds
  const stored = dateInputToIso(picked);
  assert.equal(stored, "2026-10-01T00:00:00Z", "storage format unchanged (UTC midnight)");
  assert.equal(dueCalendarDate(stored!), picked, "stored day === picked day");
});

test("Countdown: formatClaimCountdown formats durations and handles boundaries cleanly", () => {
  const baseNow = 1700000000000;
  // Empty, null, undefined, invalid
  assert.equal(formatClaimCountdown(undefined, baseNow), "");
  assert.equal(formatClaimCountdown(null, baseNow), "");
  assert.equal(formatClaimCountdown("", baseNow), "");
  assert.equal(formatClaimCountdown("not-a-date", baseNow), "");

  // Expired / 0 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow).toISOString(), baseNow), "0:00");
  assert.equal(formatClaimCountdown(new Date(baseNow - 10000).toISOString(), baseNow), "0:00");

  // 5 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + 5000).toISOString(), baseNow), "0:05");

  // 14 minutes 59 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + (14 * 60 + 59) * 1000).toISOString(), baseNow), "14:59");

  // 60 minutes
  assert.equal(formatClaimCountdown(new Date(baseNow + 60 * 60 * 1000).toISOString(), baseNow), "60:00");

  // 75 minutes 3 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + (75 * 60 + 3) * 1000).toISOString(), baseNow), "75:03");
});

test("isClaimExpired mirrors claimHint's 'lease expired' branch", () => {
  const baseNow = 1700000000000;
  const actor = { name: "a", type: "agent" };

  // Unclaimed → never "expired".
  assert.equal(isClaimExpired({}, baseNow), false);

  // Claimed with no expiry recorded → never "expired" (there is nothing to expire).
  assert.equal(isClaimExpired({ claimed_by: actor }, baseNow), false);

  // Future expiry → active, not expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow + 10_000).toISOString() }, baseNow),
    false,
  );

  // Exactly now → diff <= 0 → expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow).toISOString() }, baseNow),
    true,
  );

  // Past expiry → expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow - 60_000).toISOString() }, baseNow),
    true,
  );

  // Unparseable expiry → cannot be determined → not expired (matches claimHint returning "").
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: "not-a-date" }, baseNow),
    false,
  );
});

test("isStalledExpired flags only doing tasks with a lapsed lease or >60min idle (01M5XSNR8VWZTP66GT7ABNSJC1)", () => {
  const baseNow = 1_700_000_000_000;
  const actor = { name: "a", type: "agent" };
  const freshIso = new Date(baseNow - 5 * 60_000).toISOString(); // touched 5 min ago
  const staleIso = new Date(baseNow - 61 * 60_000).toISOString(); // untouched for 61 min

  // doing + expired lease → stalled regardless of how recent the update is.
  assert.equal(
    isStalledExpired(
      { status: "doing", updated_at: freshIso, claimed_by: actor, claim_expires_at: new Date(baseNow - 60_000).toISOString() },
      baseNow,
    ),
    true,
  );

  // doing + unclaimed + idle > 60 min → stalled.
  assert.equal(isStalledExpired({ status: "doing", updated_at: staleIso }, baseNow), true);

  // doing + active lease + recently touched → healthy.
  assert.equal(
    isStalledExpired(
      { status: "doing", updated_at: freshIso, claimed_by: actor, claim_expires_at: new Date(baseNow + 800_000).toISOString() },
      baseNow,
    ),
    false,
  );

  // doing + unclaimed + recently touched → healthy.
  assert.equal(isStalledExpired({ status: "doing", updated_at: freshIso }, baseNow), false);

  // todo/done are NEVER stalled — even with an expired lease or a stale update.
  assert.equal(
    isStalledExpired(
      { status: "todo", updated_at: staleIso, claimed_by: actor, claim_expires_at: new Date(baseNow - 60_000).toISOString() },
      baseNow,
    ),
    false,
  );
  assert.equal(isStalledExpired({ status: "done", updated_at: staleIso }, baseNow), false);

  // Boundary: EXACTLY 60 min idle is not stalled (strictly-greater window).
  const exactIso = new Date(baseNow - 60 * 60_000).toISOString();
  assert.equal(isStalledExpired({ status: "doing", updated_at: exactIso }, baseNow), false);

  // Unparseable updated_at with no expired lease → cannot establish idle age → not stalled.
  assert.equal(isStalledExpired({ status: "doing", updated_at: "not-a-date" }, baseNow), false);
});

test("needsAttention flags only open tasks whose newest comment is a question/handoff (01M5XTAYKJGP878ZEZB7TJW4ZB)", () => {
  // Open tasks with a question/handoff newest comment → true.
  assert.equal(needsAttention({ status: "todo", last_comment_kind: "question" }), true);
  assert.equal(needsAttention({ status: "doing", last_comment_kind: "handoff" }), true);

  // Routine kinds never match, on any status.
  for (const kind of ["progress", "note", "decision"]) {
    assert.equal(needsAttention({ status: "todo", last_comment_kind: kind }), false, `${kind} is routine`);
  }

  // No comments at all (field omitted on the wire) → false.
  assert.equal(needsAttention({ status: "todo" }), false);
  assert.equal(needsAttention({ status: "doing" }), false);

  // Done tasks are NEVER attention — even with a question/handoff newest comment.
  assert.equal(needsAttention({ status: "done", last_comment_kind: "question" }), false);
  assert.equal(needsAttention({ status: "done", last_comment_kind: "handoff" }), false);
});

test("isTextArtifact identifies log/command-output/trace and text file extensions", () => {
  assert.equal(isTextArtifact(null), false);
  assert.equal(isTextArtifact(undefined), false);

  // Canonical kinds
  assert.equal(isTextArtifact({ kind: "log", ref: "https://example.com/output" }), true);
  assert.equal(isTextArtifact({ kind: "command-output", ref: "/artifacts/foo" }), true);
  assert.equal(isTextArtifact({ kind: "trace", ref: "/artifacts/bar" }), true);

  // Text file extensions in ref or summary
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/01ABC.log" }), true);
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/01ABC.txt" }), true);
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/output.json" }), true);
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/patch.diff" }), true);
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/01ABC", summary: "build.log" }), true);

  // Non-text files
  assert.equal(isTextArtifact({ kind: "screenshot", ref: "/artifacts/shot.png" }), false);
  assert.equal(isTextArtifact({ kind: "other", ref: "/artifacts/video.mp4" }), false);
  assert.equal(isTextArtifact({ kind: "url", ref: "https://github.com/org/repo" }), false);
});
