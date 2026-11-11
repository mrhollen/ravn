// Keyboard shortcut decisions (Phase 1 extraction from app.ts — frontend
// modularization epic): the global bare-key dispatch ("n" / "/" / "?") and the
// Escape cascade that closes the top-most overlay. Pure state-in → action-token
// out; the root keeps every imperative surface method, DOM focus calls and
// confirm prompts. Pinning the cascade's PRIORITY ORDER in a pure function is
// the point — inserting a new overlay surface without reordering the chain
// would silently steal Escapes from surfaces below it.

/** Overlay/panel states the Escape cascade keys off of (all optional — absent
 *  fields are treated as closed). Field names mirror the root's state where
 *  they're boolean-shaped; non-boolean sources are pre-reduced to booleans by
 *  the caller (e.g. `editingAcSeq !== 0` → editingAcActive). */
export interface EscapeContext {
  showShortcutHelp?: boolean;
  imagePreviewOpen?: boolean;
  unsavedNotesPending?: boolean;
  showAuthModal?: boolean;
  showProjectSettings?: boolean;
  /** Nested inside project settings: a first Escape cancels the confirm, not the settings. */
  showArchiveConfirm?: boolean;
  editingAcActive?: boolean;
  /** The edit form is topmost inside the drawer; dirty drafts still run their
   *  confirm step imperatively in the root (the token just selects that branch). */
  editingTaskActive?: boolean;
  showTaskOpenCodeConfig?: boolean;
  statusMenuOpen?: boolean;
  ownerPopoverOpen?: boolean;
  priorityPopoverOpen?: boolean;
  duePopoverOpen?: boolean;
  /** Any of the board-layer batch dropdowns (move / priority / assign). */
  batchMenusOpen?: boolean;
  detailOpen?: boolean;
  showNewTask?: boolean;
  /** Nested confirm inside new-task: a first Escape cancels the confirm. */
  confirmNewTaskDiscard?: boolean;
  projectMenuOpen?: boolean;
  headerMenuOpen?: boolean;
  showNewProject?: boolean;
  cardMenuOpen?: boolean;
  showMobileMenu?: boolean;
  /** Multi-selection is the last-resort Escape target (AC4). */
  hasSelection?: boolean;
}

/** What Escape should do, in strict top-most-first priority order. Null = no
 *  surface to dismiss. The ordering below MUST stay in lockstep with the root's
 *  historical if/else-if chain — the unit tests pin it. */
export type EscapeAction =
  | "close-help"
  | "close-preview"
  | "keep-editing-notes"
  | "close-auth"
  | "cancel-archive-confirm"
  | "request-close-settings"
  | "cancel-ac-edit"
  | "cancel-task-edit"
  | "close-open-code-config"
  | "close-status-menu"
  | "close-owner-popover"
  | "close-priority-popover"
  | "close-due-popover"
  | "close-batch-menus"
  | "close-detail"
  | "cancel-new-task-confirm"
  | "request-close-new-task"
  | "close-project-menu"
  | "close-header-menu"
  | "close-new-project"
  | "close-card-menu"
  | "close-mobile-menu"
  | "clear-selection";

export function escapeAction(ctx: EscapeContext): EscapeAction | null {
  if (ctx.showShortcutHelp) return "close-help";
  if (ctx.imagePreviewOpen) return "close-preview";
  if (ctx.unsavedNotesPending) return "keep-editing-notes"; // Escape = "keep editing"
  if (ctx.showAuthModal) return "close-auth";
  if (ctx.showProjectSettings) {
    // Nested confirm first: settings stays open, only the confirm is cancelled.
    return ctx.showArchiveConfirm ? "cancel-archive-confirm" : "request-close-settings";
  }
  if (ctx.editingAcActive) return "cancel-ac-edit";
  if (ctx.editingTaskActive) return "cancel-task-edit";
  if (ctx.showTaskOpenCodeConfig) return "close-open-code-config";
  if (ctx.statusMenuOpen) return "close-status-menu";
  if (ctx.ownerPopoverOpen) return "close-owner-popover";
  if (ctx.priorityPopoverOpen) return "close-priority-popover";
  if (ctx.duePopoverOpen) return "close-due-popover";
  if (ctx.batchMenusOpen) return "close-batch-menus"; // never touches the selection itself
  if (ctx.detailOpen) return "close-detail";
  if (ctx.showNewTask) {
    return ctx.confirmNewTaskDiscard ? "cancel-new-task-confirm" : "request-close-new-task";
  }
  if (ctx.projectMenuOpen) return "close-project-menu"; // header dropdowns are topmost
  if (ctx.headerMenuOpen) return "close-header-menu";
  if (ctx.showNewProject) return "close-new-project";
  if (ctx.cardMenuOpen) return "close-card-menu";
  if (ctx.showMobileMenu) return "close-mobile-menu";
  if (ctx.hasSelection) return "clear-selection"; // AC4: last resort
  return null;
}

/** Surfaces that suppress the bare-key shortcuts while open (the "n" shortcut
 *  must not fight an already-open surface). */
export interface GlobalKeyContext {
  showNewTask?: boolean;
  showProjectSettings?: boolean;
  showShortcutHelp?: boolean;
}

export type GlobalKeyAction = "new-task" | "focus-search" | "toggle-help";

/** Action for a bare (modifier-free, non-editable) keypress — null to ignore.
 *  "/" and "?" always act; "n" only when no tracked surface is open. */
export function globalKeyAction(key: string, ctx: GlobalKeyContext): GlobalKeyAction | null {
  if (key === "n" || key === "N") {
    return !(ctx.showNewTask || ctx.showProjectSettings || ctx.showShortcutHelp) ? "new-task" : null;
  }
  if (key === "/") return "focus-search";
  if (key === "?") return "toggle-help";
  return null;
}

/** True when the keydown landed in a form control / editable node — bare-key
 *  shortcuts must not fire while typing. Structural param so it stays pure and
 *  testable without DOM fixtures. */
export function isEditableTarget(
  target: { tagName?: string; isContentEditable?: boolean } | null | undefined,
): boolean {
  if (!target) return false;
  const tag = target.tagName ? String(target.tagName).toLowerCase() : "";
  return tag === "input" || tag === "textarea" || tag === "select" || Boolean(target.isContentEditable);
}
