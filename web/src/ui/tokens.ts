// Class token convention — Phase 3 component layer of the frontend
// modularization epic. ONE file owns the class values that were previously
// re-typed across web/src (Tailwind utility fragments) and style.css (component
// class names), so a design change edits exactly one place:
//
//   A. Component-class NAMES (kind "name"): these are selectors defined in
//      src/style.css (@layer components). The name string lives here; the
//      styling itself stays in CSS. ui.tokens.test asserts that every exported
//      name exists in style.css — a renamed/deleted selector fails the unit
//      suite instead of silently degrading markup at runtime.
//
//   B. Utility-string tokens (kind "value"): Tailwind utility fragments that
//      are composed into class strings by TS code (SVG chart/ring markup,
//      status colors for board headers and dashboard). Each token documents
//      the surfaces that consume it. Consumers MUST import from here rather
//      than re-typing the fragment — that is what makes a value "shared".
//
// What deliberately stays OUT of this file:
//   - index.html-only utility strings (buttons' full @apply bodies, one-off
//     modal layout classes): they are authored in one place already. They join
//     this file the moment a second TS consumer appears.
//   - Semantic state→value logic (e.g. "which tint applies to this card"):
//     that lives in the primitives that build on these tokens (ui/card.ts,
//     modules/board-view.ts), not here.

import type { Status } from "../types";

// ------------------------------------------------------------------ kind A: names

/** Button variant → component class name (style.css @layer components). */
export const BUTTON_CLASS = {
  primary: "btn-primary",
  secondary: "btn-secondary",
  danger: "btn-danger",
  ghost: "btn-ghost",
} as const;

export type ButtonVariant = keyof typeof BUTTON_CLASS;

/** Keyboard-focus ring (WCAG 2.4.x) — style.css `.focus-ring`. */
export const FOCUS_RING_CLASS = "focus-ring";
/** Column-header status dot (named class on purpose — see style.css). */
export const STATUS_DOT_NAME = "status-dot";
/** Card density modifier — compact padding override (style.css). */
export const CARD_COMPACT_CLASS = "card-compact";
/** Done-column recede (opacity) accent (style.css). */
export const CARD_ACCENT_DONE_CLASS = "card-accent-done";
/** Modal backdrop fill+blur (style.css). */
export const MODAL_BACKDROP_CLASS = "modal-backdrop";
/** Modal header icon chip (style.css). */
export const MODAL_HEADER_ICON_CLASS = "modal-header-icon";
/** Modal close button (style.css). */
export const MODAL_CLOSE_CLASS = "modal-close";

// ------------------------------------------------- kind B: value — board & status

/** Board column-header dot fill per status (modules/board-view.ts colDotClass).
 *  Light-only by design: the saturated fills read correctly on both themes. */
export const STATUS_DOT_CLASS: Record<Status, string> = {
  todo: "bg-zinc-400",
  doing: "bg-amber-500",
  done: "bg-emerald-500",
};

/** Dashboard status-bar segment fill (modules/dashboard.ts workload bars).
 *  Carries the dark-theme lightening that the board dots deliberately lack. */
export const STATUS_BAR_CLASS: Record<Status, string> = {
  todo: "bg-zinc-400 dark:bg-zinc-500",
  doing: "bg-amber-500 dark:bg-amber-400",
  done: "bg-emerald-500 dark:bg-emerald-400",
};

/** Board column-header count pill tint per status (board-view colCountPillClass). */
export const STATUS_PILL_CLASS: Record<Status, string> = {
  todo: "bg-zinc-100 text-zinc-600 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700",
  doing: "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800",
  done: "bg-emerald-100 text-emerald-800 ring-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:ring-emerald-800",
};

/** Dashboard status badge tint (modules/dashboard.ts report rows). Distinct
 *  from STATUS_PILL_CLASS on `todo` (700/200 grays vs 600/300) — the two
 *  surfaces were authored independently and must keep rendering as shipped. */
export const STATUS_BADGE_CLASS: Record<Status, string> = {
  todo: "bg-zinc-100 text-zinc-700 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-200 dark:ring-zinc-700",
  doing: "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800",
  done: "bg-emerald-100 text-emerald-800 ring-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:ring-emerald-800",
};

// ------------------------------------------------------ kind B: value — card states

/** Card rendered while it is the active drag source (index.html board card). */
export const CARD_DRAGGING_CLASS =
  "opacity-40 border-dashed border-zinc-400 bg-zinc-50 dark:border-zinc-600 dark:bg-zinc-900";

// NOTE: the strings below were lifted verbatim from index.html's board-card
// :class array (Phase 3 audit) — do not "improve" them, they are the shipped
// visual contract.
export const CARD_BORDER = {
  selected: "border-indigo-400 ring-2 ring-indigo-300/70 dark:border-indigo-500 dark:ring-indigo-500/40",
  claimed: "border-indigo-200/80 hover:border-indigo-300 dark:border-indigo-800 dark:hover:border-indigo-700",
  blocked: "border-red-200 hover:border-red-300 dark:border-red-800 dark:hover:border-red-700",
  neutral: "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700 dark:hover:border-zinc-600",
} as const;

export const CARD_BG = {
  selected: "bg-indigo-50/80 hover:shadow dark:bg-indigo-950/40",
  default: "bg-white hover:shadow dark:bg-zinc-800",
} as const;

/** Multi-select marker (style.css visibility rule keys off this class). */
export const CARD_SELECTED_CLASS = "has-selection";
/** Busy (in-flight mutation) card — pointer events suspended. */
export const CARD_BUSY_CLASS = "pointer-events-none opacity-60";
/** Idle card cursor affordance. */
export const CARD_GRAB_CLASS = "cursor-grab active:cursor-grabbing";

// --------------------------------------------------- kind B: value — chart SVG

/** Dashboard chart grid lines (modules/dashboard-charts.ts). */
export const CHART_GRID_STROKE = "stroke-zinc-200 dark:stroke-zinc-800";
/** Chart axis tick labels. */
export const CHART_AXIS_TEXT_FILL = "fill-zinc-600 dark:fill-zinc-400";
/** Throughput bar fill (daily/weekly buckets). */
export const THROUGHPUT_BAR_FILL = "fill-emerald-500 dark:fill-emerald-400";
/** Cumulative-completed area under the line. */
export const CUMULATIVE_AREA_FILL = "fill-indigo-500/15 dark:fill-indigo-400/15";
/** Cumulative-completed line stroke. */
export const CUMULATIVE_LINE_STROKE = "stroke-indigo-600 dark:stroke-indigo-400";
/** Cumulative line data-point dots (fill + rim). */
export const CUMULATIVE_DOT_CLASSES = "fill-indigo-600 dark:fill-indigo-400 stroke-white dark:stroke-zinc-900";

// ------------------------------------------------------- kind B: value — AC ring

/** AC progress-ring segment stroke for a COMPLETED criterion (ac-math.ts). */
export const AC_RING_DONE_STROKE = "stroke-emerald-500";
/** …and for an open one. */
export const AC_RING_OPEN_STROKE = "stroke-zinc-300 dark:stroke-zinc-600";
