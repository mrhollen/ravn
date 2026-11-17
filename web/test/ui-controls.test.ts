// ui/button.ts + ui/menu.ts + ui/empty.ts — flat-root primitive contracts
// (Phase 3). Expected strings are the shipped markup fragments verbatim.

import test from "node:test";
import assert from "node:assert/strict";
import { btnClass } from "../src/ui/button";
import { MENU_PANEL_CLASS, menuItemClass } from "../src/ui/menu";
import { EMPTY_ROW_HINT_CLASS, EMPTY_STATE, EMPTY_TEXT_CLASS } from "../src/ui/empty";

test("button: variant maps to its single component class", () => {
  assert.equal(btnClass("primary"), "btn-primary");
  assert.equal(btnClass("secondary"), "btn-secondary");
  assert.equal(btnClass("danger"), "btn-danger");
  assert.equal(btnClass("ghost"), "btn-ghost");
});

test("button: extra fragments append; falsy extras drop", () => {
  assert.equal(btnClass("primary", "mt-2"), "btn-primary mt-2");
  assert.equal(
    btnClass("secondary", "w-full", false, null, undefined),
    "btn-secondary w-full",
  );
});

test("menu: panel class is the shipped project-menu contract (z-40 tier included)", () => {
  assert.equal(
    MENU_PANEL_CLASS,
    "absolute left-0 right-0 top-full z-40 mt-1.5 max-h-[24rem] min-w-[15rem] overflow-auto rounded-md border border-zinc-200 bg-white py-1 shadow-lg dark:border-zinc-700 dark:bg-zinc-950",
  );
});

test("menu: base item without modifiers is exactly the shipped project-option look", () => {
  assert.equal(
    menuItemClass(),
    "flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-zinc-100 focus-ring dark:hover:bg-zinc-800",
  );
});

test("menu: modifiers append without reordering the base", () => {
  assert.equal(menuItemClass({ emphasized: true }), menuItemClass() + " font-medium");
  assert.equal(
    menuItemClass({ mutedDisabled: true }),
    menuItemClass() +
      " text-zinc-500 dark:text-zinc-400 disabled:cursor-not-allowed disabled:opacity-50",
  );
});

test("empty-state: shipped fragments verbatim, object and constants agree", () => {
  assert.equal(EMPTY_ROW_HINT_CLASS, "px-1 py-3 text-center text-xs italic text-zinc-600 dark:text-zinc-400");
  assert.equal(EMPTY_TEXT_CLASS, "mt-1 text-xs text-zinc-600 dark:text-zinc-400");
  assert.equal(EMPTY_STATE.rowHint, EMPTY_ROW_HINT_CLASS);
  assert.equal(EMPTY_STATE.text, EMPTY_TEXT_CLASS);
});
