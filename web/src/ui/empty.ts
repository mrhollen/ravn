// Empty-state primitive — Phase 3 component layer. Two shipped looks for
// "there is nothing here":
//   - the per-column board hint ("No tasks"): muted, centered, italic;
//   - the empty-SURFACE body line under a heading (no projects yet, forbidden
//     project, load/boot errors): small muted text under a semibold title.
// Both were re-typed per call site in index.html; they are single-sourced here
// and consumed through flat template-glue entries (app.ts `emptyStateClass`).

/** Centered italic row hint inside an empty column/lane. */
export const EMPTY_ROW_HINT_CLASS =
  "px-1 py-3 text-center text-xs italic text-zinc-600 dark:text-zinc-400";

/** Body line of an empty-state card, under its heading. */
export const EMPTY_TEXT_CLASS = "mt-1 text-xs text-zinc-600 dark:text-zinc-400";

export type EmptyStatePart = keyof typeof EMPTY_STATE;

export const EMPTY_STATE = {
  rowHint: EMPTY_ROW_HINT_CLASS,
  text: EMPTY_TEXT_CLASS,
} as const;
