import test from "node:test";
import assert from "node:assert/strict";

import { createMenuController, menuFirstLastIndex, menuMoveIndex, type MenuDeps } from "../src/modules/menus.ts";
import { withGlobals } from "./test-helpers.ts";

// --- fake DOM stand-ins ------------------------------------------------------

/** Minimal element stand-in: records focus() calls and attribute lookups. */
class FakeEl {
  focused = 0;
  attrs: Record<string, string>;
  constructor(attrs: Record<string, string> = {}) {
    this.attrs = attrs;
  }
  focus(): void {
    this.focused++;
  }
  getAttribute(name: string): string | null {
    return name in this.attrs ? this.attrs[name] : null;
  }
}

/** Fake document: records capture-phase click listeners, exposes activeElement. */
function fakeDocument(active: unknown = null) {
  const listeners: Array<(e: Event) => void> = [];
  return {
    listeners,
    activeElement: active,
    addEventListener(type: string, fn: (e: Event) => void, capture?: boolean): void {
      if (type === "click" && capture) listeners.push(fn);
    },
    removeEventListener(type: string, fn: (e: Event) => void, capture?: boolean): void {
      if (type === "click" && capture) {
        const i = listeners.indexOf(fn);
        if (i !== -1) listeners.splice(i, 1);
      }
    },
  };
}

interface Harness {
  s: { open: boolean };
  deps: MenuDeps;
  ctrl: ReturnType<typeof createMenuController>;
  doc: ReturnType<typeof fakeDocument>;
  items: FakeEl[];
  trigger: FakeEl;
  inside: (t: unknown) => boolean;
}

/** One controller over an in-memory open flag + configurable containment seam. */
function makeHarness(opts?: { items?: FakeEl[]; active?: unknown }): Harness {
  const s = { open: false };
  const doc = fakeDocument(opts?.active ?? null);
  const items = opts?.items ?? [new FakeEl(), new FakeEl(), new FakeEl()];
  const trigger = new FakeEl();
  // Emulate the browser: focusing an item makes it document.activeElement.
  for (const it of items) {
    const orig = it.focus.bind(it);
    it.focus = () => {
      orig();
      doc.activeElement = it;
    };
  }
  let insideImpl = (t: unknown) => true;
  const deps: MenuDeps = {
    isOpen: () => s.open,
    setOpen: (b) => {
      s.open = b;
    },
    // The module queries items off the panel element itself.
    panel: () => ({ querySelectorAll: () => items }) as unknown as HTMLElement | null,
    trigger: () => trigger as unknown as HTMLElement | null,
    insideControl: (t) => insideImpl(t),
  };
  const ctrl = createMenuController(deps, { itemSelector: "button", activeKind: "element", pickInitial: menuFirstLastIndex });
  return {
    s,
    deps,
    ctrl,
    doc,
    items,
    trigger,
    inside: (fn) => {
      insideImpl = fn as (t: unknown) => boolean;
    },
  };
}

// --- pure index math -----------------------------------------------------------

test("menuMoveIndex: Home/End jump to the ends", () => {
  assert.equal(menuMoveIndex(5, 2, "home"), 0);
  assert.equal(menuMoveIndex(5, 2, "end"), 4);
});

test("menuMoveIndex: ±1 clamps at the bounds (no wrap)", () => {
  assert.equal(menuMoveIndex(5, 0, -1), 0, "clamped at the top");
  assert.equal(menuMoveIndex(5, 4, 1), 4, "clamped at the bottom");
  assert.equal(menuMoveIndex(5, 2, 1), 3);
  assert.equal(menuMoveIndex(5, 2, -1), 1);
});

test("menuMoveIndex: nothing focused yet — Down starts first, Up starts last", () => {
  assert.equal(menuMoveIndex(5, -1, 1), 0);
  assert.equal(menuMoveIndex(5, -1, -1), 4);
});

test("menuMoveIndex: empty panel yields no focus target", () => {
  assert.equal(menuMoveIndex(0, 0, 1), -1);
  assert.equal(menuMoveIndex(0, -1, "home"), -1);
});

test("menuFirstLastIndex maps keyboard targets to the ends", () => {
  assert.equal(menuFirstLastIndex(4, "first"), 0);
  assert.equal(menuFirstLastIndex(4, "last"), 3);
  assert.equal(menuFirstLastIndex(0, "first"), -1);
});

// --- open/close lifecycle (non-DOM: no window, so the focus step is skipped) -----

test("open sets the bound flag and installs exactly one capture-phase click listener", () => {
  const h = makeHarness();
  withGlobals({ document: h.doc }, () => {
    h.ctrl.open("first");
    assert.equal(h.s.open, true);
    assert.equal(h.doc.listeners.length, 1);
    // Re-opening while open is a no-op — no second listener.
    h.ctrl.open("first");
    assert.equal(h.doc.listeners.length, 1);
  });
});

test("close clears the flag and removes its own listener; a second close is a no-op", () => {
  const h = makeHarness();
  withGlobals({ document: h.doc }, () => {
    h.ctrl.open("first");
    h.ctrl.close();
    assert.equal(h.s.open, false);
    assert.equal(h.doc.listeners.length, 0);
    h.ctrl.close(true); // restoreFocus variant, already closed — no throw, no-op
    assert.equal(h.doc.listeners.length, 0);
  });
});

test("close(true) is safe without a window (focus step degrades to a no-op)", () => {
  const h = makeHarness();
  withGlobals({ document: h.doc }, () => {
    h.ctrl.open("first");
    h.ctrl.close(true);
    assert.equal(h.s.open, false);
  });
});

test("an outside click closes the menu; a click inside the control does not", () => {
  const h = makeHarness();
  const outer = {}; // an Event target NOT contained in the control
  const inner = {}; // a trigger/item target the control DOES contain
  // Containment seam answers true only for `inner`.
  withGlobals({ document: h.doc }, () => {
    h.inside((t) => t === inner);

    h.ctrl.open("first");
    assert.equal(h.doc.listeners.length, 1);
    h.doc.listeners[0]({ target: outer } as unknown as Event);
    assert.equal(h.s.open, false, "outside click closes");
    assert.equal(h.doc.listeners.length, 0, "the listener removes itself via close()");

    h.ctrl.open("first");
    h.doc.listeners[0]({ target: inner } as unknown as Event);
    assert.equal(h.s.open, true, "click inside the control is the control's own business");
    assert.equal(h.doc.listeners.length, 1, "listener stays installed");
  });
});

test("a second click event after close cannot re-close (listener already removed)", () => {
  const h = makeHarness();
  withGlobals({ document: h.doc }, () => {
    h.ctrl.open("first");
    h.inside(() => false);
    h.doc.listeners[0]({ target: {} } as unknown as Event);
    assert.equal(h.s.open, false);
    assert.equal(h.doc.listeners.length, 0); // nothing left to fire again
  });
});

// --- roving focus navigation (move) ---------------------------------------------

test("move: arrows step and clamp; Home/End jump — active element counted by instanceof", () => {
  const items = [new FakeEl(), new FakeEl(), new FakeEl(), new FakeEl()];
  const h = makeHarness({ items, active: items[1] });
  // The controller checks `ae instanceof HTMLElement` for activeKind "element".
  withGlobals({ document: h.doc, HTMLElement: FakeEl }, () => {
    h.ctrl.move(1);
    assert.equal(items[2].focused, 1, "Down steps to the next item");
    h.ctrl.move(1);
    assert.equal(items[3].focused, 1, "clamped at the last item");
    h.ctrl.move(-1);
    assert.equal(items[2].focused, 2, "Up steps back");
    h.ctrl.move("home");
    assert.equal(items[0].focused, 1, "Home jumps to first");
    h.ctrl.move("end");
    assert.equal(items[3].focused, 2, "End jumps to last");
  });
});

test("move: nothing focused yet — Down starts at the first item", () => {
  const items = [new FakeEl(), new FakeEl(), new FakeEl()];
  const h = makeHarness({ items, active: null });
  withGlobals({ document: h.doc, HTMLElement: FakeEl }, () => {
    h.ctrl.move(1);
    assert.equal(items[0].focused, 1);
  });
});

test("move: nothing focused yet — Up starts at the last item", () => {
  const items = [new FakeEl(), new FakeEl(), new FakeEl()];
  const h = makeHarness({ items, active: null });
  withGlobals({ document: h.doc, HTMLElement: FakeEl }, () => {
    h.ctrl.move(-1);
    assert.equal(items[2].focused, 1);
  });
});

test("move: an active element OUTSIDE the panel is treated as not focused", () => {
  const items = [new FakeEl(), new FakeEl()];
  const foreign = new FakeEl(); // an HTMLElement but not one of the items
  const h = makeHarness({ items, active: foreign });
  withGlobals({ document: h.doc, HTMLElement: FakeEl }, () => {
    h.ctrl.move(1);
    assert.equal(items[0].focused, 1, "Down falls back to first");
    assert.equal(foreign.focused, 0);
  });
});

test("move: empty panel is a no-op (no throw)", () => {
  const h = makeHarness({ items: [], active: null });
  withGlobals({ document: h.doc }, () => {
    h.ctrl.move(1); // must not throw
  });
  assert.equal(h.doc.listeners.length, 0);
});

test("move: a non-HTMLElement activeElement is ignored (activeKind element)", () => {
  const items = [new FakeEl(), new FakeEl()];
  const h = makeHarness({ items, active: "not-an-element" });
  withGlobals({ document: h.doc, HTMLElement: FakeEl }, () => {
    h.ctrl.move(1);
    assert.equal(items[0].focused, 1);
  });
});
