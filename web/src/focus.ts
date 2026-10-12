// Focus management for dialog surfaces (WCAG 2.1 AA 2.4.3): a zero-dependency
// Tab trap + focus restoration. Every open modal/drawer runs one DialogFocus
// session:
//   - remembers document.activeElement when opened (the trigger element),
//   - moves focus INTO the dialog (explicit initial element, else first
//     visible focusable),
//   - keeps Tab/Shift+Tab cycling inside while open,
//   - restores focus to the remembered trigger on close (fallback: body).
//
// Escape is deliberately NOT handled here — the app's dismissTop() chain owns
// the per-surface close semantics. Callers MUST call session.close() from EVERY
// close path (Esc / × / Cancel / submit). Sessions stack: when two dialogs are
// open at once (unsaved-notes confirm over the detail drawer), the NEWEST trap
// wins Tab handling, and closing the top one restores focus into the one below.

/** CSS selector for elements a keyboard user can reach with Tab. */
export const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type=hidden])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

/** Whether an element is actually visible (x-show uses display:none). */
function isVisible(el: HTMLElement): boolean {
  if (typeof el.checkVisibility === "function") return el.checkVisibility();
  return el.offsetParent !== null || getComputedStyle(el).position === "fixed";
}

/** Pure core of focusableIn: keep elements that are tabbable AND shown. */
export function pickFocusable<T extends { tabIndex: number }>(
  els: T[],
  isShown: (el: T) => boolean = () => true,
): T[] {
  return els.filter((el) => el.tabIndex >= 0 && isShown(el));
}

/** Visible focusable descendants of `container`, in DOM order. */
export function focusableIn(container: HTMLElement): HTMLElement[] {
  if (typeof document === "undefined") return [];
  const els = Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
  return pickFocusable(els, isVisible);
}

/** Pure Tab-wrap computation: the index to focus next in a trap of n elements.
 *  `current` is the focused element's index, or -1 when focus was outside the
 *  trap (in which case start at the nearer end). */
export function nextTrapIndex(n: number, current: number, forward: boolean): number {
  if (n <= 0) return -1;
  if (current < 0 || current >= n) return forward ? 0 : n - 1;
  const i = current + (forward ? 1 : -1);
  return ((i % n) + n) % n;
}

export interface DialogFocus {
  /** Move focus into the dialog. Idempotent no-op if focus already lives inside. */
  focusInitial(): void;
  /** Restore focus to the opening trigger and unbind the trap. Safe to call twice. */
  close(): void;
}

interface TrapEntry {
  root: HTMLElement;
  onTab: (e: KeyboardEvent) => void;
}

/** Live traps in open order; the newest (last) is the topmost dialog. */
const traps: TrapEntry[] = [];
/** One live session per surface root — re-opening while open must NOT stack
 *  a second trap for the same element (leaked traps yank focus into hidden UI). */
const sessionsByRoot = new WeakMap<HTMLElement, DialogFocus>();
let docBound = false;

function docKeydown(e: KeyboardEvent): void {
  if (e.key !== "Tab" || e.defaultPrevented || traps.length === 0) return;
  const target = e.target instanceof HTMLElement ? e.target : null;
  // Newest trap that contains the focused element handles the Tab.
  for (let i = traps.length - 1; i >= 0; i--) {
    if (target && traps[i].root.contains(target)) {
      traps[i].onTab(e);
      return;
    }
  }
  // Focus escaped every trap (body / background) — yank it back into the
  // topmost dialog. If it has no focusables yet (content still loading), the
  // preventDefault alone keeps the tab from reaching the background.
  e.preventDefault();
  const els = focusableIn(traps[traps.length - 1].root);
  els[0]?.focus();
}

function ensureDocBound(): void {
  if (docBound || typeof document === "undefined") return;
  docBound = true;
  document.addEventListener("keydown", docKeydown);
}

/** Start a focus session for dialog surface `root`.
 *  `opts.initialSelector` picks the element to receive initial focus inside the
 *  surface (e.g. a safe default button); falls back to the first visible
 *  focusable, resolved LAZILY by focusInitial() so content that loads after
 *  open (the detail drawer) is covered too. This function does NOT move focus
 *  itself — callers decide when (see DialogFocus.focusInitial /
 *  focusWhenReady). At most ONE live session exists per surface root: calling
 *  this again for the same root while a session is open returns the existing
 *  one, so re-opening can't stack traps (a leaked trap would yank Tab focus
 *  into a hidden dialog) or re-capture the original trigger element. */
export function openDialogFocus(root: HTMLElement, opts?: { initialSelector?: string }): DialogFocus {
  const existing = sessionsByRoot.get(root);
  if (existing) return existing;

  const prev = document.activeElement instanceof HTMLElement ? document.activeElement : null;

  const handleTab = (e: KeyboardEvent): void => {
    const els = focusableIn(root);
    if (els.length === 0) {
      e.preventDefault(); // nothing to cycle to — never let Tab escape
      return;
    }
    const target = e.target instanceof HTMLElement ? e.target : null;
    const idx = target ? els.indexOf(target) : -1;
    const next = nextTrapIndex(els.length, idx, !e.shiftKey);
    e.preventDefault();
    els[next]?.focus();
  };

  traps.push({ root, onTab: handleTab });
  ensureDocBound();

  let closed = false;
  const session: DialogFocus = {
    focusInitial(): void {
      if (closed) return;
      // Don't stomp a position the user already tabbed to inside this dialog.
      const ae = document.activeElement;
      if (ae instanceof HTMLElement && root.contains(ae)) return;
      let target: HTMLElement | null = null;
      if (opts?.initialSelector) {
        const el = root.querySelector<HTMLElement>(opts.initialSelector);
        if (el && isVisible(el)) target = el;
      }
      if (!target) target = focusableIn(root)[0] ?? null;
      target?.focus();
    },
    close(): void {
      if (closed) return;
      closed = true;
      sessionsByRoot.delete(root);
      const i = traps.findIndex((t) => t.root === root && t.onTab === handleTab);
      if (i !== -1) traps.splice(i, 1);
      if (traps.length === 0 && docBound) {
        docBound = false;
        document.removeEventListener("keydown", docKeydown);
      }
      restoreFocus(prev);
    },
  };
  sessionsByRoot.set(root, session);
  return session;
}

/** Move focus into `f`'s surface once it is actually rendered.
 *  x-show/x-cloak display updates flush on Alpine's own schedule — measured
 *  LATER than a setTimeout(0) macrotask — so "focus one tick after open" can
 *  land while the surface is still display:none (and .focus() then silently
 *  no-ops forever). Instead: poll each animation frame until the root is
 *  visible, bounded by a wall-clock deadline (1500ms) so a surface that never
 *  renders can't spin. Calling focusInitial while hidden would no-op anyway,
 *  and after close() it bails on the closed flag — hence safe to run
 *  unattended. */
export function focusWhenReady(f: DialogFocus, root: HTMLElement): void {
  if (typeof document === "undefined" || typeof root.checkVisibility !== "function") return;
  const deadline = Date.now() + 1500; // ~2s wall-clock horizon, frame-rate independent
  const step = (): void => {
    if (isVisible(root)) {
      f.focusInitial();
      return;
    }
    if (Date.now() >= deadline) return; // never rendered — stop, don't focus blind
    if (typeof requestAnimationFrame === "function") requestAnimationFrame(step);
    else window.setTimeout(step, 16);
  };
  step();
}

function restoreFocus(prev: HTMLElement | null): void {
  try {
    if (prev && document.contains(prev)) prev.focus();
    else if (typeof document !== "undefined" && document.body) document.body.focus();
  } catch {
    /* the trigger may have become unfocusable — never throw from a restore */
  }
}
