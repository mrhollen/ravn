// Shared header-dropdown controller — Phase 2 batch A of the frontend
// modularization epic (batch A slice 3/3). The Projects dropdown and the
// header settings menu are structurally the SAME custom-dropdown widget:
// a bound open flag, a capture-phase outside-click listener that lives
// exactly as long as the menu is open, frame-polled focus of an initial item,
// and roving arrow/Home/End navigation over the panel's items. This module
// owns that shared machinery; per-menu state (the two `*MenuOpen` flags the
// template binds) stays at the composition root and is reached through the
// typed accessors on {@link MenuDeps} — same state-ownership pattern as
// modules/auth.ts and modules/projects.ts (see docs/FRONTEND_ARCHITECTURE.md).
//
// What deliberately does NOT move here: per-menu ORCHESTRATION at the root —
// opening the settings menu collapses the Projects dropdown (mutual
// exclusion), pickProject() runs the selectProject() cascade, and
// openTokenFromMenu() hands off to the auth modal. The root keeps thin
// wrappers under the exact names index.html binds.
//
// DOM policy: the controller is a DOM widget by nature — it takes its whole
// knowledge of the page as injected data (testid lookups, item selector) and
// guards every global access (`typeof document !== "undefined"`), exactly
// like the focus.ts core it builds on.

import { focusElementOnceVisible } from "../focus";

/** Keyboard target for the initial focused item when a menu opens. */
export type MenuFocusTarget = "first" | "last" | "selected";
/** Arrow/Home/End direction for roving navigation. */
export type MenuMoveDir = number | "home" | "end";

/** Pure roving-focus index math (shared by both header menus): Home/End jump
 *  to the ends; ±1 clamps at the bounds (NO wrap); when nothing is focused
 *  yet (`activeIdx` -1), Down lands on the first item and Up on the last. */
export function menuMoveIndex(n: number, activeIdx: number, dir: MenuMoveDir): number {
  if (n <= 0) return -1;
  if (dir === "home") return 0;
  if (dir === "end") return n - 1;
  return activeIdx < 0 ? (dir > 0 ? 0 : n - 1) : Math.min(n - 1, Math.max(0, activeIdx + dir));
}

/** Pure initial-focus index for the "first"/"last" targets. */
export function menuFirstLastIndex(n: number, target: MenuFocusTarget): number {
  if (n <= 0) return -1;
  return target === "last" ? n - 1 : 0;
}

/** Narrow dependency surface for ONE menu controller: the bound open flag
 *  (in/out — it stays a root field because index.html binds it) and the
 *  injected lookups that keep all page knowledge OUT of this module. */
export interface MenuDeps {
  isOpen(): boolean;
  setOpen(open: boolean): void;
  /** The panel element holding the navigable items (null in non-DOM envs). */
  panel(): HTMLElement | null;
  /** The trigger element focus returns to when closing with restore. */
  trigger(): HTMLElement | null;
  /** Outside-click boundary: true when `target` sits inside the whole control
   *  (trigger or panel) — those clicks are the control's own business. */
  insideControl(target: EventTarget | null): boolean;
}

/** Per-menu differences, injected as data rather than branches. */
export interface MenuConfig {
  /** Selector for navigable items within the panel ("button" vs all focusables). */
  itemSelector: string;
  /** The active element must be this kind to count as "the focused item" when
   *  computing the next roving index. */
  activeKind: "button" | "element";
  /** Which item receives initial focus on open, per keyboard target. */
  pickInitial(items: HTMLElement[], target: MenuFocusTarget): number;
}

export interface MenuController {
  /** Open and focus the configured initial item. No-op while already open. */
  open(target?: MenuFocusTarget): void;
  /** Close. `restoreFocus` returns focus to the trigger (Esc / a pick);
   *  outside clicks close WITHOUT restoring — focus is already where the user
   *  clicked. No-op while already closed. */
  close(restoreFocus?: boolean): void;
  /** Roving arrow/Home/End navigation between the panel's items. */
  move(dir: MenuMoveDir): void;
}

/** Create one menu controller. One instance per menu (the root builds each
 *  lazily, mirroring _authModule()/_projectsModule()). */
export function createMenuController(deps: MenuDeps, config: MenuConfig): MenuController {
  // The capture-phase outside-click listener lives exactly as long as the
  // menu is open (installed in open(), removed in close()) — internal state,
  // never rendered.
  let outsideHandler: ((e: Event) => void) | null = null;

  function panelItems(): HTMLElement[] {
    const root = deps.panel();
    return root ? Array.from(root.querySelectorAll<HTMLElement>(config.itemSelector)) : [];
  }

  function open(target: MenuFocusTarget = "first"): void {
    if (deps.isOpen()) return;
    deps.setOpen(true);
    if (typeof document !== "undefined") {
      const onDocClick = (e: Event): void => {
        // Inside the control (trigger or items): their own handlers decide.
        if (!deps.insideControl(e.target)) close(); // outside click — no focus restore; close() removes this listener itself
      };
      outsideHandler = onDocClick;
      document.addEventListener("click", onDocClick, true);
    }
    if (typeof window === "undefined" || typeof document === "undefined") return;
    // The items exist in the DOM from boot (x-for rendered once / static
    // markup; the panel is merely x-show-hidden), so resolve the target NOW
    // and let focusElementOnceVisible wait out the display flush.
    const items = panelItems();
    if (items.length === 0) return;
    focusElementOnceVisible(items[config.pickInitial(items, target)] ?? null);
  }

  function close(restoreFocus = false): void {
    if (!deps.isOpen()) return;
    deps.setOpen(false);
    if (outsideHandler && typeof document !== "undefined") {
      document.removeEventListener("click", outsideHandler, true);
      outsideHandler = null;
    }
    if (restoreFocus) focusElementOnceVisible(deps.trigger());
  }

  function move(dir: MenuMoveDir): void {
    if (typeof document === "undefined") return;
    const items = panelItems();
    if (items.length === 0) return;
    const ae = document.activeElement;
    const isItem = config.activeKind === "button" ? ae instanceof HTMLButtonElement : ae instanceof HTMLElement;
    const activeIdx = ae && isItem ? items.indexOf(ae as HTMLElement) : -1;
    items[menuMoveIndex(items.length, activeIdx, dir)]?.focus();
  }

  return { open, close, move };
}
