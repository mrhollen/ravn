// Button primitive — Phase 3 component layer of the frontend modularization
// epic. The flat-root constraint (docs/FRONTEND_ARCHITECTURE.md §2) rules out
// nested Alpine components, so a "component" here is a typed pure contract:
// props in → class string out. Styling lives in style.css (.btn-* component
// classes); the NAME of each variant's class is single-sourced in ui/tokens.ts
// and drift-guarded by test/ui-tokens.test.ts.
//
// Consumption: templates bind via a flat root entry (app.ts exposes `btnClass`),
// e.g.  :class="btnClass('primary')"  — the variant prop is the union below, so
// an unknown variant fails typecheck instead of rendering an unstyled button.

import { BUTTON_CLASS, type ButtonVariant } from "./tokens";

export type { ButtonVariant };

/** Compose the class string for one button.
 *
 *  `variant` selects the shared component class; optional extra fragments are
 *  appended (spacing, width) and falsy extras are dropped — mirroring how a
 *  static `class="btn-primary mt-2"` attribute would have read. */
export function btnClass(
  variant: ButtonVariant,
  ...extra: Array<string | false | null | undefined>
): string {
  const parts: string[] = [BUTTON_CLASS[variant]];
  for (const e of extra) if (e) parts.push(e);
  return parts.join(" ");
}
