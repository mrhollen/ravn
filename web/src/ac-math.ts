// Acceptance-criteria display math (Phase 1 extraction from app.ts — frontend
// modularization epic): done counts, the progress label, the SVG progress-ring
// geometry and the completed-by/when line. Pure data in (AcRow arrays) →
// numbers/strings out; no DOM, no Alpine. The detail drawer's `detail`
// snapshot is the caller's concern.

import type { AcRow } from "./types";
import { actorLabel, relTime } from "./util";

/** Number of criteria checked off — the numerator of the progress ring/label. */
export function acDoneCount(acs: readonly AcRow[]): number {
  return acs.filter((a) => a.done).length;
}

/** Progress label under the AC section header ("2 of 5 acceptance criteria
 *  complete"). */
export function acProgressLabel(acs: readonly AcRow[]): string {
  return `${acDoneCount(acs)} of ${acs.length} acceptance criteria complete`;
}

/** One SVG <circle> per criterion for the AC progress ring. Each sits on a
 *  100-unit circle (r = 15.915 in a 36x36 viewBox), so dash/offset map straight
 *  onto stroke-dasharray/stroke-dashoffset. Done slots fill emerald, open ones
 *  stay hollow; the inter-slot gap shrinks as N grows so many criteria still
 *  read as distinct segments. Returned as a string (not x-for) because Alpine's
 *  x-for cannot anchor nodes inside inline SVG. Only numeric values and two
 *  fixed class tokens are interpolated — no user text — so this is safe to pass
 *  to :innerHTML on the <g>. */
export function acRingInner(acs: readonly AcRow[]): string {
  const n = acs.length;
  if (n === 0) return "";
  const slot = 100 / n;
  const gap = n > 1 ? Math.min(2, slot * 0.35) : 0;
  const dash = Math.max(slot - gap, 0.5);
  return acs
    .map((ac, i) => {
      const cls = ac.done ? "stroke-emerald-500" : "stroke-zinc-300 dark:stroke-zinc-600";
      return (
        `<circle cx="18" cy="18" r="15.915" fill="none" stroke-width="6" ` +
        `class="${cls}" stroke-dasharray="${dash.toFixed(3)} ${(100 - dash).toFixed(3)}" ` +
        `stroke-dashoffset="${(-i * slot).toFixed(3)}"></circle>`
      );
    })
    .join("");
}

/** Completed-by line for one criterion: actor name (or "unknown") plus the
 *  relative completion time when present ("Grace · 2h ago"). */
export function completedLabel(ac: AcRow): string {
  const who = ac.completed_by ? actorLabel(ac.completed_by) : "unknown";
  const when = ac.completed_at ? relTime(ac.completed_at) : "";
  return `${who}${when ? ` · ${when}` : ""}`;
}
