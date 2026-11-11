// Unit tests for src/ac-math.ts — AC display math: done counts, progress label,
// the SVG progress-ring geometry, and the completed-by/when line. Pure AcRow
// data in / strings+numbers out; no DOM.

import test from "node:test";
import assert from "node:assert/strict";

import { acDoneCount, acProgressLabel, acRingInner, completedLabel } from "../src/ac-math.ts";
import type { AcRow } from "../src/types.ts";

function ac(seq: number, over: Partial<AcRow> = {}): AcRow {
  return { seq, text: `c${seq}`, done: false, ...over };
}

test("acDoneCount counts only checked-off criteria", () => {
  assert.equal(acDoneCount([]), 0);
  assert.equal(acDoneCount([ac(1), ac(2)]), 0);
  assert.equal(acDoneCount([ac(1, { done: true }), ac(2), ac(3, { done: true })]), 2);
});

test("acProgressLabel reads '<done> of <total> acceptance criteria complete'", () => {
  assert.equal(acProgressLabel([]), "0 of 0 acceptance criteria complete");
  assert.equal(acProgressLabel([ac(1), ac(2, { done: true }), ac(3, { done: true })]), "2 of 3 acceptance criteria complete");
});

test("acRingInner: empty list renders nothing", () => {
  assert.equal(acRingInner([]), "");
});

test("acRingInner: single criterion is one full ring slot (done + open class)", () => {
  const done = acRingInner([ac(1, { done: true })]);
  assert.equal(
    done,
    `<circle cx="18" cy="18" r="15.915" fill="none" stroke-width="6" ` +
      `class="stroke-emerald-500" stroke-dasharray="100.000 0.000" stroke-dashoffset="0.000"></circle>`,
  );
  const open = acRingInner([ac(1)]);
  assert.ok(open.includes('class="stroke-zinc-300 dark:stroke-zinc-600"'));
});

test("acRingInner: N slots share the 100-unit circle with a capped gap", () => {
  const ring = acRingInner([ac(1, { done: true }), ac(2), ac(3, { done: true }), ac(4)]);
  // slot = 25, gap = min(2, 8.75) = 2 → dash 23 / remainder 77 per circle.
  assert.equal(ring.match(/<circle /g)?.length, 4);
  assert.ok(ring.includes('stroke-dasharray="23.000 77.000"'));
  assert.ok(ring.includes('stroke-dashoffset="-25.000"'));
  assert.ok(ring.includes('stroke-dashoffset="-50.000"'));
  assert.ok(ring.includes('stroke-dashoffset="-75.000"'));
  // done → emerald, open → hollow zinc (order preserved per seq).
  const classes = [...ring.matchAll(/class="([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual(classes, [
    "stroke-emerald-500",
    "stroke-zinc-300 dark:stroke-zinc-600",
    "stroke-emerald-500",
    "stroke-zinc-300 dark:stroke-zinc-600",
  ]);
});

test("acRingInner: the inter-slot gap shrinks as N grows (capped at 2)", () => {
  // n = 10 → slot 10, gap min(2, 3.5) = 2 → dash 8
  assert.ok(acRingInner(Array.from({ length: 10 }, (_, i) => ac(i + 1))).includes('stroke-dasharray="8.000 92.000"'));
  // n = 40 → slot 2.5, gap min(2, 0.875) = 0.875 → dash 1.625
  assert.ok(acRingInner(Array.from({ length: 40 }, (_, i) => ac(i + 1))).includes('stroke-dasharray="1.625 98.375"'));
});

test("completedLabel: who · when, with deterministic degradation", () => {
  assert.equal(completedLabel(ac(1)), "unknown"); // no actor at all
  assert.equal(
    completedLabel(ac(1, { completed_by: { name: "Grace Hopper", type: "human" } })),
    "Grace Hopper",
  );
  assert.equal(
    completedLabel(ac(1, { completed_by: { name: "runner-2", type: "agent" } })),
    "runner-2 (agent)",
  );
  // Unparseable timestamp echoes back verbatim (relTime's NaN fallback) — keeps this test deterministic.
  assert.equal(
    completedLabel(ac(1, { completed_by: { name: "Grace Hopper", type: "human" }, completed_at: "not-a-date" })),
    "Grace Hopper · not-a-date",
  );
});
