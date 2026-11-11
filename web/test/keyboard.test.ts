// Unit tests for src/keyboard.ts — the Escape cascade priority (pinned level-by-
// level and pairwise, so inserting a surface without reordering the chain fails
// loudly) and the global bare-key dispatch table.

import test from "node:test";
import assert from "node:assert/strict";

import { escapeAction, globalKeyAction, isEditableTarget } from "../src/keyboard.ts";
import type { EscapeAction, EscapeContext } from "../src/keyboard.ts";

/** The cascade levels in STRICT priority order (top-most first). The two
 *  nested-confirm levels pair their parent flag with the confirm flag — a
 *  plain parent flag is its own lower level. */
const CHAIN: { set: (c: EscapeContext) => void; token: EscapeAction }[] = [
  { set: (c) => { c.showShortcutHelp = true; }, token: "close-help" },
  { set: (c) => { c.imagePreviewOpen = true; }, token: "close-preview" },
  { set: (c) => { c.unsavedNotesPending = true; }, token: "keep-editing-notes" },
  { set: (c) => { c.showAuthModal = true; }, token: "close-auth" },
  { set: (c) => { c.showProjectSettings = true; c.showArchiveConfirm = true; }, token: "cancel-archive-confirm" },
  { set: (c) => { c.showProjectSettings = true; }, token: "request-close-settings" },
  { set: (c) => { c.editingAcActive = true; }, token: "cancel-ac-edit" },
  { set: (c) => { c.editingTaskActive = true; }, token: "cancel-task-edit" },
  { set: (c) => { c.showTaskOpenCodeConfig = true; }, token: "close-open-code-config" },
  { set: (c) => { c.statusMenuOpen = true; }, token: "close-status-menu" },
  { set: (c) => { c.ownerPopoverOpen = true; }, token: "close-owner-popover" },
  { set: (c) => { c.priorityPopoverOpen = true; }, token: "close-priority-popover" },
  { set: (c) => { c.duePopoverOpen = true; }, token: "close-due-popover" },
  { set: (c) => { c.batchMenusOpen = true; }, token: "close-batch-menus" },
  { set: (c) => { c.detailOpen = true; }, token: "close-detail" },
  { set: (c) => { c.showNewTask = true; c.confirmNewTaskDiscard = true; }, token: "cancel-new-task-confirm" },
  { set: (c) => { c.showNewTask = true; }, token: "request-close-new-task" },
  { set: (c) => { c.projectMenuOpen = true; }, token: "close-project-menu" },
  { set: (c) => { c.headerMenuOpen = true; }, token: "close-header-menu" },
  { set: (c) => { c.showNewProject = true; }, token: "close-new-project" },
  { set: (c) => { c.cardMenuOpen = true; }, token: "close-card-menu" },
  { set: (c) => { c.showMobileMenu = true; }, token: "close-mobile-menu" },
  { set: (c) => { c.hasSelection = true; }, token: "clear-selection" },
];

test("escapeAction: every level selects its own token when it is the only open surface", () => {
  for (const level of CHAIN) {
    const ctx: EscapeContext = {};
    level.set(ctx);
    assert.equal(escapeAction(ctx), level.token, `level ${level.token}`);
  }
});

test("escapeAction: a higher-priority surface always shadows every lower one (all pairs)", () => {
  for (let i = 0; i < CHAIN.length; i++) {
    for (let j = i + 1; j < CHAIN.length; j++) {
      const ctx: EscapeContext = {};
      CHAIN[i].set(ctx);
      CHAIN[j].set(ctx);
      assert.equal(escapeAction(ctx), CHAIN[i].token, `${CHAIN[i].token} must beat ${CHAIN[j].token}`);
    }
  }
});

test("escapeAction: no open surface → null (nothing to dismiss)", () => {
  assert.equal(escapeAction({}), null);
});

test("globalKeyAction: bare-key dispatch table", () => {
  const closed: Parameters<typeof globalKeyAction>[1] = {};
  // "n" opens a new task — both casings (caps lock), and only when no tracked surface is open.
  assert.equal(globalKeyAction("n", closed), "new-task");
  assert.equal(globalKeyAction("N", closed), "new-task");
  assert.equal(globalKeyAction("n", { showNewTask: true }), null);
  assert.equal(globalKeyAction("n", { showProjectSettings: true }), null);
  assert.equal(globalKeyAction("n", { showShortcutHelp: true }), null);
  // "/" and "?" always act, even with surfaces open.
  assert.equal(globalKeyAction("/", closed), "focus-search");
  assert.equal(globalKeyAction("/", { showNewTask: true }), "focus-search");
  assert.equal(globalKeyAction("?", closed), "toggle-help");
  assert.equal(globalKeyAction("?", { showShortcutHelp: true }), "toggle-help"); // toggle, not open-only
  // Anything else is ignored.
  assert.equal(globalKeyAction("a", closed), null);
  assert.equal(globalKeyAction("Escape", closed), null);
  assert.equal(globalKeyAction("", closed), null);
});

test("isEditableTarget: form controls and contenteditable suppress bare-key shortcuts", () => {
  assert.equal(isEditableTarget(null), false);
  assert.equal(isEditableTarget(undefined), false);
  assert.equal(isEditableTarget({ tagName: "INPUT" }), true);
  assert.equal(isEditableTarget({ tagName: "input" }), true); // tag casing varies
  assert.equal(isEditableTarget({ tagName: "TEXTAREA" }), true);
  assert.equal(isEditableTarget({ tagName: "SELECT" }), true);
  assert.equal(isEditableTarget({ tagName: "DIV", isContentEditable: true }), true);
  assert.equal(isEditableTarget({ tagName: "DIV" }), false);
  assert.equal(isEditableTarget({}), false);
});
