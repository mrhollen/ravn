import test from "node:test";
import assert from "node:assert/strict";

import { FOCUSABLE_SELECTOR, focusableIn, nextTrapIndex, openDialogFocus, pickFocusable, focusWhenReady } from "../src/focus.ts";
import { withGlobals } from "./test-helpers.ts";

test("FocusTrap: nextTrapIndex wraps forward and backward (AC1)", () => {
  // 4 focusables: Tab from last wraps to first; Shift+Tab from first wraps to last.
  assert.equal(nextTrapIndex(4, 0, true), 1);
  assert.equal(nextTrapIndex(4, 2, true), 3);
  assert.equal(nextTrapIndex(4, 3, true), 0); // forward wrap
  assert.equal(nextTrapIndex(4, 3, false), 2);
  assert.equal(nextTrapIndex(4, 1, false), 0);
  assert.equal(nextTrapIndex(4, 0, false), 3); // backward wrap
});

test("FocusTrap: nextTrapIndex handles a single focusable (stays put)", () => {
  assert.equal(nextTrapIndex(1, 0, true), 0);
  assert.equal(nextTrapIndex(1, 0, false), 0);
});

test("FocusTrap: nextTrapIndex with no focusables and outside focus", () => {
  assert.equal(nextTrapIndex(0, 0, true), -1); // nothing to trap — caller preventDefaults
  assert.equal(nextTrapIndex(0, -1, false), -1);
  // Focus was outside the trap: start at the nearer end.
  assert.equal(nextTrapIndex(3, -1, true), 0);
  assert.equal(nextTrapIndex(3, -1, false), 2);
  // Out-of-range indices degrade to the same ends (no crash).
  assert.equal(nextTrapIndex(3, 99, true), 0);
  assert.equal(nextTrapIndex(3, -7, false), 2);
});

test("FocusTrap: pickFocusable keeps tabbable AND visible elements only", () => {
  const els = [
    { tabIndex: 0 }, // button/input — keep
    { tabIndex: -1 }, // explicit non-tabbable — drop
    { tabIndex: 5 }, // custom positive tabindex — keep
  ];
  assert.deepEqual(pickFocusable(els), [{ tabIndex: 0 }, { tabIndex: 5 }]); // default: all shown

  // Hidden (x-show display:none) elements are dropped even when tabbable.
  const vis = [
    { tabIndex: 0, hidden: false },
    { tabIndex: 0, hidden: true },
    { tabIndex: -1, hidden: false },
  ];
  assert.deepEqual(
    pickFocusable(vis, (el) => !(el as any).hidden),
    [{ tabIndex: 0, hidden: false }],
  );

  // Empty input → empty output.
  assert.deepEqual(pickFocusable([]), []);
});

// ---------------------------------------------------------------------------
// Minimal hand-rolled DOM fakes for the dialog-level tests — no DOM library.
// FakeEl stands in for the browser's HTMLElement (the ONLY runtime global
// focus.ts touches, via instanceof) and carries just enough shape for what the
// module actually calls: querySelector(All), checkVisibility, contains, focus.
// ---------------------------------------------------------------------------

class FakeEl {
  tabIndex = 0;
  focused = false;
  visible = true;
  offsetParent: unknown = {};
  position = "static"; // consulted by the getComputedStyle fallback only
  detached = false;
  parent: FakeEl | null = null;
  doc: any = null; // owning fake document (wired so focus() moves activeElement)
  focusables: FakeEl[] = []; // stand-in result of querySelectorAll(FOCUSABLE_SELECTOR)
  selectorHits: Record<string, FakeEl> = {}; // stand-in for querySelector(sel)
  seenSelectors: string[] = [];
  focusCount = 0;

  focus(): void {
    this.focusCount++;
    if (this.doc) this.doc.activeElement = this;
  }
  contains(o: unknown): boolean {
    let n = o as FakeEl | null;
    while (n && typeof (n as any).parent !== "undefined") {
      if (n === this) return true;
      n = n.parent;
    }
    return false;
  }
  querySelector(sel: string): FakeEl | null {
    this.seenSelectors.push(sel);
    return this.selectorHits[sel] ?? null;
  }
  querySelectorAll(sel: string): FakeEl[] {
    this.seenSelectors.push(sel);
    return this.focusables;
  }
  checkVisibility(): boolean {
    return this.visible;
  }
}

function makeDoc(active: unknown = null): any {
  const doc: any = {
    activeElement: active,
    body: { focused: false, focus(this: any) { this.focused = true; } },
    listeners: {} as Record<string, (e: any) => void>,
    removedListeners: [] as string[],
    addEventListener(t: string, fn: (e: any) => void) { doc.listeners[t] = fn; },
    removeEventListener(t: string, fn: unknown) {
      if (doc.listeners[t] === fn) delete doc.listeners[t];
      doc.removedListeners.push(t);
    },
    contains(el: unknown): boolean {
      return !!el && !((el as FakeEl).detached ?? false);
    },
  };
  return doc;
}

function keydownEv(key: string, target: unknown, extra: Record<string, unknown> = {}): any {
  const e: any = {
    key,
    target,
    shiftKey: false,
    defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true; },
    ...extra,
  };
  return e;
}

test("FocusTrap: focusableIn returns [] when there is no document (SSR-safe)", () => {
  assert.deepEqual(focusableIn({ querySelectorAll: () => [] } as any), []);
});

test("DialogFocus: focusableIn keeps visible, tabbable elements in DOM order (checkVisibility path)", () => {
  const root = new FakeEl();
  const a = new FakeEl(); a.parent = root;
  const hidden = new FakeEl(); hidden.parent = root; hidden.visible = false;
  const noTab = new FakeEl(); noTab.parent = root; noTab.tabIndex = -1;
  root.focusables = [a, hidden, noTab];

  withGlobals({ document: makeDoc() }, () => {
    assert.deepEqual(focusableIn(root), [a], "hidden + non-tabbable elements are dropped");
    assert.equal(root.seenSelectors[0], FOCUSABLE_SELECTOR, "asks the container for the shared focusable selector");
  });
});

test("DialogFocus: visibility fallback without checkVisibility uses offsetParent and fixed positioning", () => {
  // Plain objects whose shape is all isVisible() touches in the no-checkVisibility
  // branch: `typeof el.checkVisibility === "function"` is false, then it falls
  // back to offsetParent / getComputedStyle(...).position (older browsers).
  const byOffset = { tabIndex: 0, offsetParent: {} } as any; // offsetParent non-null → visible
  const byFixed = { tabIndex: 0, offsetParent: null, position: "fixed" } as any; // fixed + no offsetParent → visible
  const gone = { tabIndex: 0, offsetParent: null, position: "static" } as any; // invisible

  const root = new FakeEl();
  root.focusables = [byOffset, byFixed, gone];
  withGlobals(
    { document: makeDoc(), getComputedStyle: (el: any) => ({ position: el.position ?? "static" }) },
    () => {
      assert.deepEqual(focusableIn(root), [byOffset, byFixed]);
    },
  );
});

test("DialogFocus: session focuses the first focusable, honors a visible initialSelector, and never stomps an in-dialog position", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; doc.activeElement = trigger;

  let session: any;
  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const root = new FakeEl();
    const a = new FakeEl(); a.parent = root; a.doc = doc;
    const b = new FakeEl(); b.parent = root; b.doc = doc;
    root.focusables = [a, b];

    session = openDialogFocus(root);
    assert.equal(typeof doc.listeners.keydown, "function", "the trap binds a document keydown handler");

    // No initialSelector → the first visible focusable.
    session.focusInitial();
    assert.equal(doc.activeElement, a, "focus moved INTO the dialog");

    // The user tabbed to b → focusInitial must not yank focus back.
    doc.activeElement = b;
    session.focusInitial();
    assert.equal(doc.activeElement, b, "an existing in-dialog position is preserved");

    session.close();
  });
});

test("DialogFocus: initialSelector wins when visible and falls back to the first focusable when hidden", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; doc.activeElement = trigger;

  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const root = new FakeEl();
    const c = new FakeEl(); c.parent = root; c.doc = doc;
    root.focusables = [c];
    const pref = new FakeEl(); pref.parent = root; pref.doc = doc;
    root.selectorHits[".safe"] = pref;

    let s: any = openDialogFocus(root, { initialSelector: ".safe" });
    s.focusInitial();
    assert.equal(doc.activeElement, pref, "visible preferred target gets focus");
    assert.ok(root.seenSelectors.includes(".safe"), "the selector was resolved lazily at focus time");
    s.close();

    const hiddenRoot = new FakeEl();
    const e1 = new FakeEl(); e1.parent = hiddenRoot; e1.doc = doc;
    hiddenRoot.focusables = [e1];
    const hp = new FakeEl(); hp.parent = hiddenRoot; hp.visible = false;
    hiddenRoot.selectorHits[".safe"] = hp;

    s = openDialogFocus(hiddenRoot, { initialSelector: ".safe" });
    s.focusInitial();
    assert.equal(doc.activeElement, e1, "hidden preferred target falls back to first focusable");
    s.close();
  });
});

test("DialogFocus: re-opening an open root returns the SAME session; close() restores the trigger exactly once and unbinds", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; doc.activeElement = trigger;

  let s1: any;
  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const root = new FakeEl();
    const a = new FakeEl(); a.parent = root; a.doc = doc;
    root.focusables = [a];

    s1 = openDialogFocus(root);
    assert.equal(openDialogFocus(root), s1, "no second trap stacks for the same root while open");
    s1.close();
    assert.equal(trigger.focusCount, 1, "close() refocuses the remembered trigger once");
    s1.close();
    assert.equal(trigger.focusCount, 1, "double-close never double-restores");
    assert.deepEqual(doc.removedListeners, ["keydown"], "the last closed trap unbinds the document handler");
  });
});

test("DialogFocus: close() falls back to <body> when the trigger is gone from the document", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; trigger.detached = true; doc.activeElement = trigger;

  let s: any;
  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const root = new FakeEl();
    const a = new FakeEl(); a.parent = root; a.doc = doc;
    root.focusables = [a];
    s = openDialogFocus(root);
    s.close();
    assert.equal(doc.body.focused, true, "unrestorable trigger → body receives focus");
    assert.equal(trigger.focusCount, 0, "the detached trigger was never focused");
  });
});

test("DialogFocus: Tab/Shift+Tab wrap inside the trap; other keys and prevented Tabs pass through", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; doc.activeElement = trigger;

  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const root = new FakeEl();
    const a = new FakeEl(); a.parent = root; a.doc = doc;
    const b = new FakeEl(); b.parent = root; b.doc = doc;
    const c = new FakeEl(); c.parent = root; c.doc = doc;
    root.focusables = [a, b, c];

    const s = openDialogFocus(root);
    const h = doc.listeners.keydown;
    assert.equal(typeof h, "function", "document handler bound");

    // Focus sits outside the trap (on the trigger) → Tab yanks it to the first element.
    let e = keydownEv("Tab", trigger);
    h(e);
    assert.equal(doc.activeElement, a, "outside focus re-enters at the nearer end");
    assert.equal(e.defaultPrevented, true);

    // Forward wrap: c → a.
    doc.activeElement = c;
    e = keydownEv("Tab", c); h(e);
    assert.equal(doc.activeElement, a, "forward Tab wraps last → first");

    // Backward wrap: a → c.
    e = keydownEv("Tab", a, { shiftKey: true }); h(e);
    assert.equal(doc.activeElement, c, "Shift+Tab wraps first → last");

    // Non-Tab keys are untouched.
    doc.activeElement = b;
    const before = b.focusCount;
    e = keydownEv("Enter", b); h(e);
    assert.equal(b.focusCount, before, "other keys cause no focus change");
    assert.equal(e.defaultPrevented, false, "other keys are not preventDefault-ed");

    // An already-prevented Tab (another handler owns it) is left alone.
    doc.activeElement = a;
    e = keydownEv("Tab", a, { defaultPrevented: true }); h(e);
    assert.equal(doc.activeElement, a, "prevented Tab passes through");

    s.close();
  });
});

test("DialogFocus: a zero-focusable trap swallows Tab; stacked dialogs route Tab to the NEWEST containing trap", () => {
  const doc = makeDoc();
  const trigger = new FakeEl(); trigger.doc = doc; doc.activeElement = trigger;

  withGlobals({ document: doc, HTMLElement: FakeEl }, () => {
    const outer = new FakeEl();
    const o1 = new FakeEl(); o1.parent = outer; o1.doc = doc;
    outer.focusables = [o1];
    const inner = new FakeEl(); inner.parent = outer;
    const i1 = new FakeEl(); i1.parent = inner; i1.doc = doc;
    const i2 = new FakeEl(); i2.parent = inner; i2.doc = doc;
    inner.focusables = [i1, i2];

    const so = openDialogFocus(outer);
    const si = openDialogFocus(inner); // newest trap = topmost dialog
    const h = doc.listeners.keydown;

    let e = keydownEv("Tab", i2);
    h(e);
    assert.equal(doc.activeElement, i1, "the newest trap containing focus handles Tab (forward wrap)");

    e = keydownEv("Tab", o1);
    h(e);
    assert.equal(doc.activeElement, o1, "a target outside the inner dialog is handled by the outer trap");

    so.close();
    si.close(); // every session closes — module-level trap state must not leak
  });

  // A trap whose surface has NO focusables yet: Tab is swallowed in place.
  const doc2 = makeDoc();
  const t2 = new FakeEl(); t2.doc = doc2; doc2.activeElement = t2;
  withGlobals({ document: doc2, HTMLElement: FakeEl }, () => {
    const empty = new FakeEl(); // focusables stays []
    const nonF = new FakeEl(); nonF.parent = empty; nonF.doc = doc2; // present but not focusable
    const se = openDialogFocus(empty);
    const e = keydownEv("Tab", nonF);
    doc2.listeners.keydown(e);
    assert.equal(e.defaultPrevented, true, "Tab never escapes an (empty) trap");
    assert.equal(doc2.activeElement, t2, "nothing to focus inside — activeElement untouched");
    se.close();
  });
});

test("DialogFocus: focusWhenReady focuses on the first visible check (rAF present) and stops scheduling", () => {
  const root = new FakeEl(); // visible by default
  let fCalled = 0;
  const frames: Array<() => void> = [];
  withGlobals(
    { document: makeDoc(), window: {}, requestAnimationFrame: (cb: () => void) => { frames.push(cb); return 0; } },
    () => {
      focusWhenReady({ focusInitial() { fCalled++; }, close() {} } as any, root);
      assert.equal(fCalled, 1, "visible on the first check → focused immediately");
      assert.deepEqual(frames, [], "nothing left queued once visible");
    },
  );
});

test("DialogFocus: focusWhenReady keeps polling frames until the surface actually renders", () => {
  const root = new FakeEl(); root.visible = false;
  let fCalled = 0;
  const frames: Array<() => void> = [];
  withGlobals(
    { document: makeDoc(), requestAnimationFrame: (cb: () => void) => { frames.push(cb); return 0; } },
    () => {
      focusWhenReady({ focusInitial() { fCalled++; }, close() {} } as any, root);
      assert.equal(fCalled, 0, "still hidden → not focused yet");
      assert.equal(frames.length, 1, "another frame is scheduled while hidden");
      root.visible = true;
      frames.shift()(); // the next animation frame fires
      assert.equal(fCalled, 1, "focused the moment it renders");
      assert.equal(frames.length, 0, "no further polling after success");
    },
  );
});

test("DialogFocus: focusWhenReady stops at its wall-clock deadline when a surface never renders", () => {
  const root = new FakeEl(); root.visible = false;
  let fCalled = 0;
  const frames: Array<() => void> = [];
  const start = Date.now();
  let ticks = 0;
  class FastDate extends Date {
    static now(): number {
      ticks++;
      return start + ticks * 5000; // blows the ~1.5s budget on the very first check
    }
  }
  withGlobals(
    { document: makeDoc(), requestAnimationFrame: (cb: () => void) => { frames.push(cb); return 0; }, Date: FastDate as any },
    () => {
      focusWhenReady({ focusInitial() { fCalled++; }, close() {} } as any, root);
      assert.equal(fCalled, 0, "never rendered → never focused (no blind focus)");
      assert.equal(frames.length, 0, "deadline hit → polling stops, no late timers leak");
    },
  );
});

test("DialogFocus: focusWhenReady falls back to window.setTimeout(16) without rAF — and bails for non-checkable roots", () => {
  const root = new FakeEl(); root.visible = false;
  let fCalled = 0;
  const timers: Array<{ t: number; fn: () => void }> = [];
  withGlobals(
    { document: makeDoc(), window: { setTimeout: (fn: () => void, t: number) => { timers.push({ t, fn }); return 0; } } },
    () => {
      focusWhenReady({ focusInitial() { fCalled++; }, close() {} } as any, root);
      assert.equal(timers.length, 1, "window.setTimeout fallback is scheduled");
      assert.equal(timers[0].t, 16, "frame-ish interval");
      root.visible = true;
      timers[0].fn(); // the timer fires
      assert.equal(fCalled, 1, "focus lands once rendered");

      focusWhenReady({ focusInitial() { fCalled++; }, close() {} } as any, {} as any);
      assert.equal(fCalled, 1, "a root without checkVisibility is unpollable → immediate no-op");
    },
  );
});
