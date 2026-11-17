// Card primitive — Phase 3 component layer. The board task card is the app's
// densest visual contract: one <article> whose :class array mixes density,
// selection, drag, claim, block, done and busy states. Flat-root constraint →
// this is a typed pure function (state in → exact class string out), not a DOM
// component. The template keeps the static base classes in `class="..."` and
// binds only the dynamic part:  :class="boardCardClass(task, col.status)"  via
// the flat root entry added in app.ts.
//
// Parity rule: boardCardClasses() reproduces the PRE-PRIMITIVE Alpine :class
// array element-for-element (same order, same fragments) so the rendered class
// attribute is byte-identical — verified by test/ui-card.test.ts against the
// shipped strings and by E2E screenshot comparison (see docs/FRONTEND_ARCHITECTURE.md).

import {
  CARD_ACCENT_DONE_CLASS,
  CARD_BG,
  CARD_BORDER,
  CARD_BUSY_CLASS,
  CARD_COMPACT_CLASS,
  CARD_DRAGGING_CLASS,
  CARD_GRAB_CLASS,
  CARD_SELECTED_CLASS,
} from "./tokens";

export type CardDensity = "comfortable" | "compact";

/** Everything the card's dynamic :class depends on — plain values only (the
 *  root method resolves them from live state before calling in). */
export interface BoardCardState {
  density: CardDensity;
  /** Multi-select checkbox marks the card (style.css visibility rule keys off CARD_SELECTED_CLASS). */
  selected: boolean;
  /** This card is the active drag source. */
  dragging: boolean;
  /** Live claim (claimed_by set and lease NOT expired — an expired lease is inert). */
  claimed: boolean;
  /** Blocker badge state (isBlocked). */
  blocked: boolean;
  /** The card sits in the Done column. */
  inDoneColumn: boolean;
  /** In-flight mutation for this card (pointer events suspended). */
  busy: boolean;
}

/** Compose the board card's dynamic class string. Precedence mirrors the
 *  shipped template: dragging wins over every tint; selection wins over
 *  claimed/blocked/neutral border+bg; done-recede only when neither drag nor
 *  busy suppresses it. */
export function boardCardClasses(s: BoardCardState): string {
  const parts: string[] = [];
  if (s.density === "compact") parts.push(CARD_COMPACT_CLASS);
  if (s.selected) parts.push(CARD_SELECTED_CLASS);
  if (s.dragging) {
    parts.push(CARD_DRAGGING_CLASS);
  } else {
    const border = s.selected
      ? CARD_BORDER.selected
      : s.claimed
        ? CARD_BORDER.claimed
        : s.blocked
          ? CARD_BORDER.blocked
          : CARD_BORDER.neutral;
    parts.push(border, s.selected ? CARD_BG.selected : CARD_BG.default);
  }
  if (s.inDoneColumn && !s.dragging && !s.busy) parts.push(CARD_ACCENT_DONE_CLASS);
  parts.push(s.busy ? CARD_BUSY_CLASS : CARD_GRAB_CLASS);
  return parts.join(" ");
}

/** Density-dependent sub-part paddings/sizes that vary with cardDensity —
 *  lifted verbatim from the three density ternaries in index.html (claim
 *  capsule, subtask-progress row, meta row). Consumers read
 *  CARD_DENSITY_PARTS[cardDensity].<part>. */
export const CARD_DENSITY_PARTS = {
  comfortable: {
    claimCapsule: "mt-2 px-2 py-0.5 text-[11px]",
    subtaskProgress: "mt-2 p-1.5 gap-1",
    metaRow: "mt-2 gap-x-3 gap-y-1 text-xs",
  },
  compact: {
    claimCapsule: "mt-1 px-1.5 py-0 text-[10px]",
    subtaskProgress: "mt-1 p-1 gap-0.5",
    metaRow: "mt-1 gap-x-2 gap-y-0.5 text-[11px]",
  },
} as const satisfies Record<CardDensity, Record<string, string>>;

export type CardDensityPart = keyof (typeof CARD_DENSITY_PARTS)["comfortable"];

/** Convenience accessor for templates: part of the current density's parts. */
export function cardDensityClass(
  density: CardDensity,
  part: CardDensityPart,
): string {
  return CARD_DENSITY_PARTS[density][part];
}
