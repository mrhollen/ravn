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

