// ui/tokens.ts — class token convention (Phase 3). The load-bearing tests here
// are the DRIFT GUARDS: every "name" token must exist in src/style.css, and no
// .btn-* selector may exist outside BUTTON_CLASS — so a renamed/deleted CSS
// class or a new one added without a token fails this suite.

import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  AC_RING_DONE_STROKE,
  AC_RING_OPEN_STROKE,
  BUTTON_CLASS,
  CARD_ACCENT_DONE_CLASS,
  CARD_BUSY_CLASS,
  CARD_BG,
  CARD_BORDER,
  CARD_COMPACT_CLASS,
  CARD_DRAGGING_CLASS,
  CARD_GRAB_CLASS,
  CARD_SELECTED_CLASS,
  CHART_AXIS_TEXT_FILL,
  CHART_GRID_STROKE,
  CUMULATIVE_AREA_FILL,
  CUMULATIVE_DOT_CLASSES,
  CUMULATIVE_LINE_STROKE,
  FOCUS_RING_CLASS,
  MODAL_BACKDROP_CLASS,
  MODAL_CLOSE_CLASS,
  MODAL_HEADER_ICON_CLASS,
  STATUS_BADGE_CLASS,
  STATUS_BAR_CLASS,
  STATUS_DOT_CLASS,
  STATUS_DOT_NAME,
  STATUS_PILL_CLASS,
  THROUGHPUT_BAR_FILL,
} from "../src/ui/tokens";
import { EMPTY_STATE } from "../src/ui/empty";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CSS = fs.readFileSync(path.resolve(__dirname, "../src/style.css"), "utf-8");
const INDEX_HTML = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

test("tokens: every button variant class exists in style.css", () => {
  for (const [variant, name] of Object.entries(BUTTON_CLASS)) {
    assert.match(
      CSS,
      new RegExp(`\\.${name.replace(/-/g, "-")}\\s*[,{]`),
      `BUTTON_CLASS.${variant} -> "${name}" not found in style.css`,
    );
  }
});

test("tokens: no .btn-* selector exists outside BUTTON_CLASS (drift guard)", () => {
  const defined = new Set(
    [...CSS.matchAll(/\.btn-[a-z]+/g)].map((m) => m[0].slice(1)),
  );
  const known = new Set(Object.values(BUTTON_CLASS));
  assert.deepEqual(
    [...defined].filter((d) => !known.has(d)).sort(),
    [],
    "style.css defines a .btn-* selector that has no BUTTON_CLASS entry",
  );
  for (const k of known) {
    assert.ok(defined.has(k), `BUTTON_CLASS value "${k}" not defined in style.css`);
  }
});

test("tokens: single-value name tokens exist in style.css", () => {
  const names = [
    FOCUS_RING_CLASS,
    STATUS_DOT_NAME,
    CARD_COMPACT_CLASS,
    CARD_ACCENT_DONE_CLASS,
    MODAL_BACKDROP_CLASS,
    MODAL_HEADER_ICON_CLASS,
    MODAL_CLOSE_CLASS,
  ];
  for (const n of names) {
    assert.match(CSS, new RegExp(`\\.${n}\\s*[,{]`), `"${n}" not found in style.css`);
  }
});

test("tokens: status records cover exactly the three statuses with non-empty values", () => {
  const STATUSES = ["todo", "doing", "done"] as const;
  for (const [label, rec] of [
    ["STATUS_DOT_CLASS", STATUS_DOT_CLASS],
    ["STATUS_BAR_CLASS", STATUS_BAR_CLASS],
    ["STATUS_PILL_CLASS", STATUS_PILL_CLASS],
    ["STATUS_BADGE_CLASS", STATUS_BADGE_CLASS],
  ] as const) {
    assert.deepEqual(
      Object.keys(rec).sort(),
      [...STATUSES].sort(),
      `${label} must cover todo/doing/done`,
    );
    for (const v of Object.values(rec)) {
      assert.ok(v.trim().length > 0, `${label}: empty value`);
    }
  }
});

test("tokens: pill and badge tints differ only on `todo` (documented divergence)", () => {
  // The board count pill and the dashboard badge were authored with different
  // zinc grays for todo; doing/done are identical. Lock that in so a future
  // "cleanup" does not silently unify them.
  assert.notEqual(STATUS_PILL_CLASS.todo, STATUS_BADGE_CLASS.todo);
  assert.equal(STATUS_PILL_CLASS.doing, STATUS_BADGE_CLASS.doing);
  assert.equal(STATUS_PILL_CLASS.done, STATUS_BADGE_CLASS.done);
});

test("tokens: value tokens are non-empty strings", () => {
  const values = [
    CARD_DRAGGING_CLASS,
    CARD_SELECTED_CLASS,
    CARD_BUSY_CLASS,
    CARD_GRAB_CLASS,
    CHART_GRID_STROKE,
    CHART_AXIS_TEXT_FILL,
    THROUGHPUT_BAR_FILL,
    CUMULATIVE_AREA_FILL,
    CUMULATIVE_LINE_STROKE,
    CUMULATIVE_DOT_CLASSES,
    AC_RING_DONE_STROKE,
    AC_RING_OPEN_STROKE,
    ...Object.values(CARD_BORDER),
    ...Object.values(CARD_BG),
  ];
  for (const v of values) {
    assert.equal(typeof v, "string");
    assert.ok(v.trim().length > 0);
  }
});

test("tokens: empty-state tints are locked to the shipped strings", () => {
  // "Shipped" = the board per-column "No tasks" hint and the projects empty
  // body; byte-for-byte lock so an edit here can never drift the look.
  // rowHint = the board per-column "No tasks" <p>; text = the projects empty
  // body line (each token carries its full shipped class string).
  assert.strictEqual(EMPTY_STATE.rowHint, "px-1 py-3 text-center text-xs italic text-zinc-600 dark:text-zinc-400");
  assert.strictEqual(EMPTY_STATE.text, "mt-1 text-xs text-zinc-600 dark:text-zinc-400");
});

test("tokens: empty-state tints are consumed through the flat-root entry", () => {
  // The templates bind EMPTY_STATE.{rowHint,text} instead of re-typing the
  // utilities, so guard the USAGE rather than a raw literal in index.html.
  assert.ok(INDEX_HTML.includes("EMPTY_STATE.rowHint"), "board column hint should bind EMPTY_STATE.rowHint");
  assert.ok(INDEX_HTML.includes("EMPTY_STATE.text"), "projects empty state should bind EMPTY_STATE.text");
});
