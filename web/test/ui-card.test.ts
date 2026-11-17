// ui/card.ts — board card class composition (Phase 3). Expected strings below
// are hand-computed from the PRE-primitive index.html :class array: Alpine
// flattens nested arrays, drops empty strings, joins with single spaces — the
// order here is that exact render order.

import test from "node:test";
import assert from "node:assert/strict";
import {
  CARD_DENSITY_PARTS,
  boardCardClasses,
  cardDensityClass,
  type BoardCardState,
} from "../src/ui/card";

const B = {
  neutral: "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700 dark:hover:border-zinc-600",
  claimed: "border-indigo-200/80 hover:border-indigo-300 dark:border-indigo-800 dark:hover:border-indigo-700",
  blocked: "border-red-200 hover:border-red-300 dark:border-red-800 dark:hover:border-red-700",
  selected: "border-indigo-400 ring-2 ring-indigo-300/70 dark:border-indigo-500 dark:ring-indigo-500/40",
} as const;
const BG = {
  def: "bg-white hover:shadow dark:bg-zinc-800",
  selected: "bg-indigo-50/80 hover:shadow dark:bg-indigo-950/40",
} as const;
const GRAB = "cursor-grab active:cursor-grabbing";
const BUSY = "pointer-events-none opacity-60";
const DRAG = "opacity-40 border-dashed border-zinc-400 bg-zinc-50 dark:border-zinc-600 dark:bg-zinc-900";

function base(over: Partial<BoardCardState> = {}): BoardCardState {
  return {
    density: "comfortable",
    selected: false,
    dragging: false,
    claimed: false,
    blocked: false,
    inDoneColumn: false,
    busy: false,
    ...over,
  };
}

test("card: plain idle todo card (shipped look, byte-identical)", () => {
  assert.equal(boardCardClasses(base()), `${B.neutral} ${BG.def} ${GRAB}`);
});

test("card: compact adds card-compact FIRST (before every state part)", () => {
  assert.equal(
    boardCardClasses(base({ density: "compact" })),
    `card-compact ${B.neutral} ${BG.def} ${GRAB}`,
  );
});

test("card: selected wins border+bg and adds has-selection in order", () => {
  assert.equal(
    boardCardClasses(base({ selected: true })),
    `has-selection ${B.selected} ${BG.selected} ${GRAB}`,
  );
});

test("card: claimed tint beats blocked/neutral; selection beats claimed", () => {
  assert.equal(
    boardCardClasses(base({ claimed: true, blocked: true })),
    `${B.claimed} ${BG.def} ${GRAB}`,
  );
  assert.equal(
    boardCardClasses(base({ claimed: true, selected: true })),
    `has-selection ${B.selected} ${BG.selected} ${GRAB}`,
  );
});

test("card: blocked tint for an unclaimed card", () => {
  assert.equal(
    boardCardClasses(base({ blocked: true })),
    `${B.blocked} ${BG.def} ${GRAB}`,
  );
});

test("card: dragging replaces border+bg with the single drag class; selection marker survives", () => {
  // The trailing busy/grab element is independent of drag state (shipped array).
  assert.equal(boardCardClasses(base({ dragging: true })), `${DRAG} ${GRAB}`);
  assert.equal(
    boardCardClasses(base({ dragging: true, selected: true })),
    `has-selection ${DRAG} ${GRAB}`,
  );
});

test("card: done-recede applies only when not dragged/busy", () => {
  assert.equal(
    boardCardClasses(base({ inDoneColumn: true })),
    `${B.neutral} ${BG.def} card-accent-done ${GRAB}`,
  );
  assert.equal(
    boardCardClasses(base({ inDoneColumn: true, busy: true })),
    `${B.neutral} ${BG.def} ${BUSY}`,
  );
  assert.equal(
    boardCardClasses(base({ inDoneColumn: true, dragging: true })),
    `${DRAG} ${GRAB}`,
  );
});

test("card: busy suspends grab affordance", () => {
  assert.equal(boardCardClasses(base({ busy: true })), `${B.neutral} ${BG.def} ${BUSY}`);
});

test("density parts: exact shipped fragments per density", () => {
  assert.deepEqual(CARD_DENSITY_PARTS.comfortable, {
    claimCapsule: "mt-2 px-2 py-0.5 text-[11px]",
    subtaskProgress: "mt-2 p-1.5 gap-1",
    metaRow: "mt-2 gap-x-3 gap-y-1 text-xs",
  });
  assert.deepEqual(CARD_DENSITY_PARTS.compact, {
    claimCapsule: "mt-1 px-1.5 py-0 text-[10px]",
    subtaskProgress: "mt-1 p-1 gap-0.5",
    metaRow: "mt-1 gap-x-2 gap-y-0.5 text-[11px]",
  });
  assert.equal(cardDensityClass("compact", "metaRow"), "mt-1 gap-x-2 gap-y-0.5 text-[11px]");
});
