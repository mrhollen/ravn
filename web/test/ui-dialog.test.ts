// ui/dialog.ts — DialogController open/close events (Phase 3). Everything is
// exercised through fake deps; the focus.ts core itself is covered by
// test/focus.test.ts.

import test from "node:test";
import assert from "node:assert/strict";
import { createDialogController } from "../src/ui/dialog";
import type { DialogFocus } from "../src/focus";

function makeSession(): DialogFocus {
  return { close() {} };
}

interface Log {
  open: Array<[string, DialogFocus | null]>;
  close: Array<[string, DialogFocus]>;
}

function harness(rootPresent = true) {
  const log: Log = { open: [], close: [] };
  const started: Array<{ root: HTMLElement; selector?: string }> = [];
  let endedCount = 0;
  const controller = createDialogController({
    getElementById: (id) =>
      rootPresent ? ({ id } as unknown as HTMLElement) : null,
    startSession: (root, selector) => {
      started.push({ root, selector });
      return makeSession();
    },
    endSession: () => {
      endedCount++;
    },
    onOpen: (id, session) => log.open.push([id, session]),
    onClose: (id, session) => log.close.push([id, session]),
  });
  return { controller, log, started, ends: () => endedCount };
}

test("dialog: open resolves the element, starts a session and fires onOpen", () => {
  const h = harness();
  assert.equal(h.controller.open("cb-test"), true);
  assert.equal(h.log.open.length, 1);
  assert.equal(h.log.open[0][0], "cb-test");
  assert.ok(h.log.open[0][1]);
  assert.equal(h.started.length, 1);
  assert.equal(h.controller.isOpen(), true);
});

test("dialog: open passes the initial selector through", () => {
  const h = harness();
  h.controller.open("cb-test", "input");
  assert.equal(h.started[0].selector, "input");
});

test("dialog: open with a missing element reports false and fires nothing", () => {
  const h = harness(false);
  assert.equal(h.controller.open("ghost"), false);
  assert.deepEqual(h.log.open, []);
  assert.deepEqual(h.log.close, []);
  assert.equal(h.started.length, 0);
  assert.equal(h.controller.isOpen(), false);
});

test("dialog: close ends the session once and fires onClose exactly once", () => {
  const h = harness();
  h.controller.open("cb-test");
  h.controller.close();
  assert.equal(h.ends(), 1);
  assert.equal(h.log.close.length, 1);
  assert.equal(h.log.close[0][0], "cb-test");
  assert.equal(h.controller.isOpen(), false);

  // Idempotent: a second close is a no-op.
  h.controller.close();
  assert.equal(h.ends(), 1);
  assert.equal(h.log.close.length, 1);
});

test("dialog: close without any open is a safe no-op", () => {
  const h = harness();
  h.controller.close();
  assert.equal(h.ends(), 0);
  assert.deepEqual(h.log.close, []);
});

test("dialog: re-open after close starts a fresh session", () => {
  const h = harness();
  h.controller.open("cb-test");
  h.controller.close();
  assert.equal(h.controller.open("cb-test"), true);
  assert.equal(h.log.open.length, 2);
  assert.equal(h.started.length, 2);
});
