// Select/dropdown primitive — Phase 3 component layer. The "project menu"
// pattern (header split-control + absolutely-positioned menu panel with
// roving keyboard focus) had its BEHAVIOR extracted already (modules/menus.ts,
// the shared controller); this file single-sources its CLASS contract so every
// dropdown that joins the pattern reuses one visual vocabulary instead of
// re-typing Tailwind fragments in index.html.
//
// Flat-root consumption: app.ts exposes `menuPanelClass` and `menuItemClass`
// as flat template-glue entries; the project menu binds them, and any future
// dropdown (the avatar preferences panel is a candidate) adopts the same two.

/** Menu panel placement + chrome. Carries its own z-40 tier — the documented
 *  tier below toasts (z-50) and dialog surfaces (z-[60]+); keep it here so all
 *  dropdowns share one layering value. */
export const MENU_PANEL_CLASS =
  "absolute left-0 right-0 top-full z-40 mt-1.5 max-h-[24rem] min-w-[15rem] overflow-auto rounded-md border border-zinc-200 bg-white py-1 shadow-lg dark:border-zinc-700 dark:bg-zinc-950";

/** Base row of a menu item: full-width left-aligned button with the shared
 *  hover tint + keyboard focus ring (style.css .focus-ring). */
const MENU_ITEM_BASE =
  "flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-zinc-100 focus-ring dark:hover:bg-zinc-800";

export interface MenuItemClassMods {
  /** Weighted label (e.g. "+ New project…"). */
  emphasized?: boolean;
  /** Muted row that is also disabled while busy (archived Restore rows):
   *  dimmed text + the standard disabled affordance. */
  mutedDisabled?: boolean;
}

/** Compose a menu-item class string from the base contract + modifiers.
 *  Falsy/absent modifiers are dropped so an unadorned item renders exactly
 *  MENU_ITEM_BASE (the shipped project-option look). */
export function menuItemClass(mods: MenuItemClassMods = {}): string {
  const parts: string[] = [MENU_ITEM_BASE];
  if (mods.emphasized) parts.push("font-medium");
  if (mods.mutedDisabled)
    parts.push(
      "text-zinc-500 dark:text-zinc-400 disabled:cursor-not-allowed disabled:opacity-50",
    );
  return parts.join(" ");
}
