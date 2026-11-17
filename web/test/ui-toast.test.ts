// ui/toast.ts — toast item class contract (Phase 3). Strings verbatim from the
// pre-primitive index.html toast template.

import test from "node:test";
import assert from "node:assert/strict";
import { TOAST_BASE_CLASS, toastClassList, toastErrorPrefix, toastKindClasses } from "../src/ui/toast";

test("toast: kind tints are the shipped reds/neutrals", () => {
  assert.equal(
    toastKindClasses("error"),
    "border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-300",
  );
  assert.equal(
    toastKindClasses("info"),
    "border-zinc-200 bg-white text-zinc-700 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-200",
  );
});

test("toast: class list mirrors the shipped :class array (kind, then border override)", () => {
  assert.deepEqual(toastClassList({ kind: "error" }), [
    toastKindClasses("error"),
    "",
  ]);
  assert.deepEqual(toastClassList({ kind: "info", borderClass: "border-indigo-300" }), [
    toastKindClasses("info"),
    "border-indigo-300",
  ]);
});

test("toast: error prefix only for visible errors", () => {
  assert.equal(toastErrorPrefix({ kind: "error" }), "Error: ");
  assert.equal(toastErrorPrefix({ kind: "error", hideErrorPrefix: true }), "");
  assert.equal(toastErrorPrefix({ kind: "info" }), "");
});

test("toast: base class is the shipped row layout", () => {
  assert.equal(
    TOAST_BASE_CLASS,
    "pointer-events-auto flex items-start gap-2 rounded-lg border px-3 py-2 text-sm shadow-md",
  );
});
