// Modal/dialog primitive — Phase 3 component layer. Wraps the existing focus
// trap/restore core (focus.ts DialogFocus sessions) in a small controller with
// explicit open/close EVENTS, so each dialog surface keeps one tested object
// instead of re-implementing the `let session: DialogFocus | null` bookkeeping
// (the pattern every modal module was duplicating).
//
// What this deliberately does NOT do:
//   - No DOM creation/teardown — the dialog markup stays in index.html and its
//     x-show flag on the flat root (Alpine owns visibility; this controller
//     only owns the focus session + lifecycle callbacks).
//   - No close semantics — Escape/backdrop/draft-discard guards remain caller
//     logic (each surface has different rules, e.g. new-task is confirm-first);
//     they simply call close() once their guard passes.
//   - Escape handling stays in focus.ts' session (root dismissTop chains).

import type { DialogFocus } from "../focus";

export interface DialogControllerDeps {
  /** Resolve the dialog's root element by id. Must return null when the DOM is
   *  unavailable (SSR/`typeof document === "undefined"` guards) or the id is
   *  not mounted. */
  getElementById: (id: string) => HTMLElement | null;
  /** Start a focus session for an already-mounted dialog element. Wired to
   *  focus.openDialogFocus by the root (which also applies focusWhenReady when
   *  autoFocus is requested — see root `openDialog`). */
  startSession: (root: HTMLElement, initialSelector?: string) => DialogFocus | null;
  /** End a session (restores trigger focus). Idempotent on null. */
  endSession: (session: DialogFocus | null) => void;
  /** OPEN event — fired after a session is started for `id` (session may be
   *  null when the surface could not take focus; the caller's visibility flag
   *  still opened, so surfaces must treat this as "open attempted"). */
  onOpen?: (id: string, session: DialogFocus | null) => void;
  /** CLOSE event — fired exactly once per open, when the session ends. */
  onClose?: (id: string, session: DialogFocus) => void;
}

export interface DialogController {
  /** Attempt to open `id` and start its focus session. Returns true when a
   *  session is live, false when the element was missing. Re-opening without a
   *  prior close() mirrors the pre-primitive module behavior (the previous
   *  session reference is simply overwritten) — callers always close before
   *  re-opening; this controller does not paper over that invariant. */
  open(id: string, initialSelector?: string): boolean;
  /** Close the live session and fire onClose. Idempotent: a second close()
   *  (or a close without any open) is a no-op. */
  close(): void;
  /** True while a focus session is live. */
  isOpen(): boolean;
}

export function createDialogController(deps: DialogControllerDeps): DialogController {
  let id = "";
  let session: DialogFocus | null = null;
  return {
    open(dialogId, initialSelector) {
      const root = deps.getElementById(dialogId);
      if (!root) return false;
      id = dialogId;
      session = deps.startSession(root, initialSelector);
      deps.onOpen?.(dialogId, session);
      return true;
    },
    close() {
      if (!session) return;
      const s = session;
      const openedId = id;
      session = null;
      deps.endSession(s);
      deps.onClose?.(openedId, s);
    },
    isOpen: () => session !== null,
  };
}
