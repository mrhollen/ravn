// Toast item + stack primitives — Phase 3 component layer. The STACK remains
// root-owned state (toasts array + toastSeq on the flat root, per the Phase 0
// state-ownership map) — what is single-sourced here is the ITEM: its typed
// shape and its class contract (kind tint + optional border override), which
// were previously re-typed inline in index.html's toast template.
//
// Flat-root consumption: app.ts imports the Toast type from here (re-exported
// for import compatibility) and exposes `toastClassList` / `toastErrorPrefix`
// as flat template-glue entries.

export type ToastKind = "error" | "info";

/** A toast row. Mirrors the pre-Phase-3 root `Toast` interface field-for-field
 *  (app.ts keeps re-exporting it). */
export interface Toast {
  id: number;
  msg: string;
  kind: ToastKind;
  /** `href` renders an external <a>; `onAction` renders a <button> that runs a
   *  local JS callback (e.g. Undo) instead — exactly one of the two is set. */
  action?: { label: string; href?: string; onAction?: () => void };
  /** Optional border class to override or customize the border. */
  borderClass?: string;
  /** When true, the toast will NOT auto-dismiss after 6.5s. */
  persistent?: boolean;
  /** When true, suppresses the "Error: " prefix even if kind is "error". */
  hideErrorPrefix?: boolean;
  /** Marker for the SSE disconnect notification toast. */
  isSseDisconnect?: boolean;
  /** True while the leave transition plays (x-show hides it), then the row is
   *  dropped from the list. x-for removes nodes instantly, so the exit
   *  animation has to be driven through x-show first. */
  leaving?: boolean;
}

/** Static base of every toast row (position-independent layout). */
export const TOAST_BASE_CLASS =
  "pointer-events-auto flex items-start gap-2 rounded-lg border px-3 py-2 text-sm shadow-md";

/** Kind tint — error reds vs info neutrals, light + dark. */
export function toastKindClasses(kind: ToastKind): string {
  return kind === "error"
    ? "border-red-200 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-300"
    : "border-zinc-200 bg-white text-zinc-700 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-200";
}

/** The toast row's :class value: [kind tint, border override]. Mirrors the
 *  shipped template array element-for-element (a missing/empty borderClass
 *  contributes an empty string, which Alpine skips). */
export function toastClassList(
  t: Pick<Toast, "kind" | "borderClass">,
): string[] {
  return [toastKindClasses(t.kind), t.borderClass || ""];
}

/** The bold prefix rendered before the message body ("" for info toasts and
 *  for error toasts with hideErrorPrefix set). */
export function toastErrorPrefix(
  t: Pick<Toast, "kind" | "hideErrorPrefix">,
): string {
  return t.kind === "error" && !t.hideErrorPrefix ? "Error: " : "";
}
