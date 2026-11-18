// Shared test-only helpers for the per-module web unit suites.
// Kept dependency-free: no DOM library, just small pure utilities that were
// previously defined in one place inside the old monolithic unit.test.ts and
// are now needed by more than one module's tests.

/** Local-calendar "YYYY-MM-DD" offset by N days (test helper only). */
export function localYmd(offsetDays: number): string {
  const n = new Date();
  n.setDate(n.getDate() + offsetDays);
  return `${n.getFullYear()}-${String(n.getMonth() + 1).padStart(2, "0")}-${String(n.getDate()).padStart(2, "0")}`;
}

/**
 * Temporarily replace one or more globals on `globalThis` with the given fakes
 * for the duration of `fn`, then restore each key to its previous value — deleting
 * any key that was not previously defined. Centralizes the save/set/restore dance
 * (a global captured, a fake installed, and a try/finally restoring it) that was
 * previously copy-pasted across tests as inline window/localStorage/fetch fakes.
 *
 * Works for both sync and async `fn`: a returned Promise is only restored after it
 * settles (success or rejection); synchronous callbacks are restored on return or
 * throw. Returns whatever `fn` returns, so existing assertions pass through intact.
 */
export function withGlobals<T>(fakes: Record<string, unknown>, fn: () => T): T | Promise<T> {
  const prev: Array<[string, boolean, unknown]> = [];
  for (const k of Object.keys(fakes)) {
    prev.push([k, Object.prototype.hasOwnProperty.call(globalThis, k), (globalThis as any)[k]]);
  }
  for (const [k, v] of Object.entries(fakes)) (globalThis as any)[k] = v;
  const restore = () => {
    for (const [k, had, val] of prev) {
      if (had) (globalThis as any)[k] = val;
      else delete (globalThis as any)[k];
    }
  };
  let r: T;
  try {
    r = fn();
  } catch (err) {
    restore();
    throw err;
  }
  if (r && typeof (r as Promise<T>).then === "function") {
    return (r as Promise<T>).then(
      (v) => ((restore(), v)),
      (e) => ((restore(), Promise.reject(e))),
    );
  }
  restore();
  return r;
}

/** Swap the global `window` for the duration of `fn` and restore it after. */
export function withWindow<T>(win: unknown, fn: () => T): T | Promise<T> {
  return withGlobals({ window: win }, fn);
}

/** Swap the global `fetch` for the duration of `fn` and restore it after. */
export function withFetch<T>(f: unknown, fn: () => T): T | Promise<T> {
  return withGlobals({ fetch: f }, fn);
}

// ---------------------------------------------------------------------------
// Minimal hand-rolled DOM fakes for dialog-level tests — no DOM library.
// FakeEl stands in for the browser's HTMLElement (the ONLY runtime global
// focus.ts touches, via instanceof) and carries just enough shape for what the
// module actually calls: querySelector(All), checkVisibility, contains, focus.
// Shared by test/focus.test.ts and the root-level dialog tests in
// test/app.test.ts (hoisted here so one harness serves both).
// ---------------------------------------------------------------------------

export class FakeEl {
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

/** A fake `document` with the exact surface focus.ts touches. `elements` is
 *  the id → element table for getElementById (absent ids resolve to null, as in
 *  a real document). */
export function makeDoc(elements: Record<string, unknown> = {}, active: unknown = null): any {
  const doc: any = {
    activeElement: active,
    body: { focused: false, focus(this: any) { this.focused = true; } },
    getElementById: (id: string) => elements[id] ?? null,
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

/** A minimal keyboard event for the fake document's keydown listeners. */
export function keydownEv(key: string, target: unknown, extra: Record<string, unknown> = {}): any {
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

