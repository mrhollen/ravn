import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  dateInputToIso,
  dueCalendarDate,
  fmtDueDate,
  isOverdue,
  localCalendarToday,
  safeStorage,
} from "../src/util.ts";
import { escapeHtml, renderMarkdown } from "../src/md.ts";
import { crowbanApp } from "../src/app.ts";
import { nextTrapIndex, pickFocusable } from "../src/focus.ts";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// --------------------------------------------------------------------------- R8: safeStorage
test("R8: safeStorage basic in-memory operations", () => {
  safeStorage.clear();
  assert.equal(safeStorage.getItem("test_key"), null);

  safeStorage.setItem("test_key", "test_val");
  assert.equal(safeStorage.getItem("test_key"), "test_val");

  safeStorage.setItem("test_key", "new_val");
  assert.equal(safeStorage.getItem("test_key"), "new_val");

  safeStorage.removeItem("test_key");
  assert.equal(safeStorage.getItem("test_key"), null);

  safeStorage.setItem("k1", "v1");
  safeStorage.setItem("k2", "v2");
  safeStorage.clear();
  assert.equal(safeStorage.getItem("k1"), null);
  assert.equal(safeStorage.getItem("k2"), null);
});

test("R8: safeStorage handles DOMException: SecurityError gracefully", () => {
  // Mock window.localStorage to throw a SecurityError (mimics sandboxed iframe or blocked cookies)
  const origWindow = (globalThis as any).window;
  try {
    (globalThis as any).window = {};
    Object.defineProperty((globalThis as any).window, "localStorage", {
      get() {
        const err = new Error("The operation is insecure.");
        err.name = "SecurityError";
        throw err;
      },
      configurable: true,
    });

    // None of these should throw
    assert.doesNotThrow(() => {
      safeStorage.setItem("secure_test", "in_memory_data");
    });
    assert.equal(safeStorage.getItem("secure_test"), "in_memory_data");

    assert.doesNotThrow(() => {
      safeStorage.removeItem("secure_test");
    });
    assert.equal(safeStorage.getItem("secure_test"), null);
  } finally {
    if (origWindow === undefined) {
      delete (globalThis as any).window;
    } else {
      (globalThis as any).window = origWindow;
    }
  }
});

test("R8: safeStorage handles localStorage.setItem QuotaExceededError gracefully", () => {
  const origWindow = (globalThis as any).window;
  try {
    const fakeStore = new Map<string, string>();
    (globalThis as any).window = {
      localStorage: {
        getItem(k: string) {
          return fakeStore.get(k) ?? null;
        },
        setItem(_k: string, _v: string) {
          const err = new Error("QuotaExceededError");
          err.name = "QuotaExceededError";
          throw err;
        },
        removeItem(k: string) {
          fakeStore.delete(k);
        },
        clear() {
          fakeStore.clear();
        },
      },
    };

    // setItem throws QuotaExceededError, should fall back to memoryStorage
    assert.doesNotThrow(() => {
      safeStorage.setItem("quota_key", "saved_in_mem");
    });
    assert.equal(safeStorage.getItem("quota_key"), "saved_in_mem");
  } finally {
    if (origWindow === undefined) {
      delete (globalThis as any).window;
    } else {
      (globalThis as any).window = origWindow;
    }
  }
});

test("R8: crowbanApp initializes cleanly when window.localStorage is blocked by SecurityError", () => {
  const origWindow = (globalThis as any).window;
  try {
    (globalThis as any).window = {
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    };
    Object.defineProperty((globalThis as any).window, "localStorage", {
      get() {
        const err = new Error("DOMException: The operation is insecure.");
        err.name = "SecurityError";
        throw err;
      },
      configurable: true,
    });

    // Initializing crowbanApp() must not throw!
    let appInstance: any;
    assert.doesNotThrow(() => {
      appInstance = crowbanApp();
    });
    assert.ok(appInstance);
    assert.equal(typeof appInstance.actorName, "string");
    assert.equal(typeof appInstance.selectedProjectId, "string");

    // Calling saveActor / saveSort / setTheme must not throw
    assert.doesNotThrow(() => {
      appInstance.actorName = "Unit Test Actor";
      appInstance.saveActor();
      appInstance.setTheme("dark");
      appInstance.saveSort();
    });
  } finally {
    if (origWindow === undefined) {
      delete (globalThis as any).window;
    } else {
      (globalThis as any).window = origWindow;
    }
  }
});

// --------------------------------------------------------------------------- R9: Markdown
test("R9: escapeHtml escapes HTML special characters", () => {
  assert.equal(escapeHtml("<script>alert(1)</script>"), "&lt;script&gt;alert(1)&lt;/script&gt;");
  assert.equal(escapeHtml("a && b > c < d"), "a &amp;&amp; b &gt; c &lt; d");
});

test("R9: renderMarkdown handles empty and fallback without crashing", () => {
  assert.equal(renderMarkdown(""), "");
  assert.equal(renderMarkdown(null), "");
  assert.equal(renderMarkdown(undefined), "");
  assert.equal(renderMarkdown("   "), "");

  // When DOMParser is absent, renderMarkdown gracefully degrades to escaped paragraph
  const res = renderMarkdown("Hello *world*");
  assert.ok(res.length > 0);
  assert.ok(res.includes("Hello"));
});

// --------------------------------------------------------------------------- R10: Accessible Card Semantics
test("R10: web/index.html uses <article> containers with scoped keyboard listeners", () => {
  const htmlPath = path.resolve(__dirname, "../index.html");
  const html = fs.readFileSync(htmlPath, "utf-8");

  // Verify the task card uses <article>
  assert.match(
    html,
    /<template x-for="task in col\.tasks" :key="task\.id">\s*<article[\s\S]*?<\/article>\s*<\/template>/,
    "Kanban cards must be wrapped in <article> ... </article> containers",
  );

  // Verify role="button" is NOT present on the article card
  const cardMatch = html.match(/<article[\s\S]*?class="relative w-full rounded-lg border p-3/);
  assert.ok(cardMatch, "Should find the card article element");
  assert.ok(
    !cardMatch[0].includes('role="button"'),
    "Card article must not have role='button'",
  );

  // Verify keyboard navigation is scoped to self (.self)
  assert.match(
    html,
    /@keydown\.enter\.self="openTask\(task\.id\)"/,
    "@keydown.enter.self must be scoped with .self",
  );
  assert.match(
    html,
    /@keydown\.space\.self\.prevent="openTask\(task\.id\)"/,
    "@keydown.space.self.prevent must be scoped with .self",
  );

  // Verify tabindex="0" and aria-label are present on the card
  assert.match(html, /tabindex="0"/);
  assert.match(html, /:aria-label="'Task: ' \+ task\.title \+ ', status ' \+ statusLabel\(col\.status\)"/);
});

// --------------------------------------------------------------------------- Notes scratchpad reset (task 01M3MMSVAF8RV8J7VGDSCV6M07)
/** Build a minimal TaskDetail-shaped object for applyDetail(). */
function fakeDetail(id: string, notes: string): any {
  return { id, notes, comments: [], assignee: undefined, blocked: false };
}

test("Notes: closeDetail() resets the scratchpad so no draft leaks (AC1)", () => {
  const app = crowbanApp();
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = true;
  app.notesDraft = "unsaved draft of A";
  app.notesDraftOwner = "A";

  app.closeDetail();

  assert.equal(app.detailOpen, false);
  assert.equal(app.notesDirty, false);
  assert.equal(app.notesDraft, "");
  assert.equal(app.notesDraftOwner, null);
});

test("Notes: opening another task with a dirty draft prompts and does NOT switch (AC2/AC3)", () => {
  const app = crowbanApp();
  // Drawer open on A with an unsaved edit…
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = true;
  app.notesDraft = "draft of A";
  app.notesDraftOwner = "A";

  // …user opens B: must be held behind the confirm prompt, draft intact.
  void app.openTask("B");

  assert.deepEqual(app.unsavedNotesAction, { kind: "open", taskId: "B" });
  assert.equal(app.detailId, "A", "must not switch until confirmed");
  assert.equal(app.notesDraft, "draft of A", "dirty draft must survive the prompt");
});

test("Notes: keep-editing (cancel) preserves the draft and leaves drawer open (AC3)", () => {
  const app = crowbanApp();
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = true;
  app.notesDraft = "draft of A";
  app.notesDraftOwner = "A";

  app.requestCloseDetail(); // close requested while dirty → prompt
  assert.deepEqual(app.unsavedNotesAction, { kind: "close" });
  assert.equal(app.detailOpen, true, "drawer must stay open behind the prompt");

  app.cancelUnsavedNotes();
  assert.equal(app.unsavedNotesAction, null);
  assert.equal(app.detailOpen, true);
  assert.equal(app.notesDirty, true);
  assert.equal(app.notesDraft, "draft of A", "keep-editing must preserve the draft");
});

test("Notes: applyDetail never carries one task's draft into another (AC2 core)", () => {
  const app = crowbanApp();
  // The leak condition: scratchpad still holds a dirty draft owned by A.
  app.notesDirty = true;
  app.notesDraft = "draft of A";
  app.notesDraftOwner = "A";

  // Applying task B's detail must load B's notes, not keep A's draft.
  app.applyDetail(fakeDetail("B", "notes for B"));
  assert.equal(app.notesDraft, "notes for B");
  assert.equal(app.notesDirty, false);
  assert.equal(app.notesDraftOwner, "B");

  // But a refresh of the SAME task must NOT clobber an in-progress edit.
  app.notesDirty = true;
  app.notesDraft = "notes for B + my edit";
  app.applyDetail(fakeDetail("B", "notes for B"));
  assert.equal(app.notesDraft, "notes for B + my edit", "same-task refresh keeps the draft");
});

test("Notes: clean close does not prompt and a fresh open loads server notes (regression)", () => {
  const app = crowbanApp();
  // Open A, no edits, close — must be silent (no prompt).
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = false;
  app.notesDraftOwner = "A";
  app.requestCloseDetail();
  assert.equal(app.unsavedNotesAction, null, "clean close must not prompt");
  assert.equal(app.detailOpen, false);

  // Re-open A: applyDetail populates A's server notes into a clean scratchpad.
  app.notesDraft = "";
  app.notesDirty = false;
  app.applyDetail(fakeDetail("A", "saved notes of A"));
  assert.equal(app.notesDraft, "saved notes of A");
  assert.equal(app.notesDirty, false);
});

// --------------------------------------------------------------------------- Due dates as calendar days (task 01M3MMT0T5678NXKC41JX05Z63)
// Run the suite under TZ=UTC, TZ=America/New_York and TZ=Pacific/Kiritimati —
// every due-date assertion below must hold in ALL of them.

/** Local-calendar "YYYY-MM-DD" offset by N days (test helper only). */
function localYmd(offsetDays: number): string {
  const n = new Date();
  n.setDate(n.getDate() + offsetDays);
  return `${n.getFullYear()}-${String(n.getMonth() + 1).padStart(2, "0")}-${String(n.getDate()).padStart(2, "0")}`;
}

/** Minimal TaskSummary row for columns() filtering. */
function dueTask(id: string, daysFromToday: number, status: "todo" | "doing" | "done" = "todo"): any {
  return { id, title: `due ${id}`, status, priority: 3, version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", due_at: `${localYmd(daysFromToday)}T00:00:00Z` };
}

test("Due: dueCalendarDate extracts the UTC calendar day in every timezone", () => {
  // This app's own storage format (UTC midnight) — same answer in any TZ.
  assert.equal(dueCalendarDate("2026-10-01T00:00:00Z"), "2026-10-01");
  assert.equal(dueCalendarDate("2028-02-29T00:00:00Z"), "2028-02-29"); // leap day (2028 is a leap year)
  assert.equal(dueCalendarDate("1999-12-31T23:59:59Z"), "1999-12-31");
  // Non-Z offsets written by external tools (instant == Oct 1 00:00Z).
  assert.equal(dueCalendarDate("2026-10-01T08:00:00+08:00"), "2026-10-01");
  assert.equal(dueCalendarDate("2026-10-01T05:30:00+05:30"), "2026-10-01");
  assert.equal(dueCalendarDate("2026-09-30T19:00:00-05:00"), "2026-10-01"); // western offset, same instant
  assert.equal(dueCalendarDate("2026-10-01T12:00:00Z"), "2026-10-01"); // mid-day instant, same calendar day
  assert.equal(dueCalendarDate("not-a-date"), "");
});

test("Due: fmtDueDate renders the encoded day identically in every timezone (off-by-one regression)", () => {
  // The original bug: this rendered "Sep 30" in browsers west of UTC.
  assert.equal(fmtDueDate("2026-10-01T00:00:00Z"), "Oct 1");
  assert.equal(fmtDueDate("2028-02-29T00:00:00Z"), "Feb 29, 2028"); // other year → includes the year
  // Year appears when it isn't the current one.
  const otherYear = fmtDueDate("2025-01-02T00:00:00Z");
  assert.ok(otherYear.includes("Jan 2"), `got "${otherYear}"`);
  assert.ok(otherYear.includes("2025"), `got "${otherYear}"`);
  const currentYear = fmtDueDate(`${new Date().getFullYear()}-07-04T00:00:00Z`);
  assert.ok(!/19\d{2}|20\d{2}/.test(currentYear), `current-year label must omit the year, got "${currentYear}"`);
  // Garbage passes through untouched.
  assert.equal(fmtDueDate("nonsense"), "nonsense");
});

test("Due: isOverdue boundary — a task due today is never overdue during its due day", () => {
  // The original bug marked this overdue from ~8pm local the previous evening.
  assert.equal(isOverdue({ due_at: `${localYmd(0)}T00:00:00Z`, status: "todo" }), false, "due today → not overdue");
  assert.equal(isOverdue({ due_at: `${localYmd(-1)}T00:00:00Z`, status: "todo" }), true, "due yesterday → overdue");
  assert.equal(isOverdue({ due_at: `${localYmd(1)}T00:00:00Z`, status: "todo" }), false, "due tomorrow → not overdue");
  assert.equal(isOverdue({ due_at: undefined as unknown as string, status: "todo" }), false);
  assert.equal(isOverdue({ due_at: `${localYmd(-5)}T00:00:00Z`, status: "done" }), false, "done tasks never overdue");
});

test("Due: dateInputToIso round-trips through the picker without shifting the day", () => {
  const picked = "2026-10-01"; // what the <input type="date"> holds
  const stored = dateInputToIso(picked);
  assert.equal(stored, "2026-10-01T00:00:00Z", "storage format unchanged (UTC midnight)");
  assert.equal(dueCalendarDate(stored!), picked, "stored day === picked day");
});

test("Due: 'Due today' filter matches exactly today and excludes older overdue tasks (AC3)", () => {
  const app = crowbanApp();
  const todayTask = dueTask("TODAY", 0);
  const oldOverdue = dueTask("OLD", -9); // the leak: used to appear in "today"
  const tomorrow = dueTask("TOMORROW", 1);
  const doneToday = dueTask("DONE_TODAY", 0, "done");
  app.board = { project_id: "P", tasks: { todo: [todayTask, oldOverdue, tomorrow], doing: [], done: [doneToday] }, counts: { todo: 3, doing: 0, done: 1 } } as any;

  app.filterDue = "today";
  const todayIds = app.columns().find((c) => c.status === "todo")!.tasks.map((t) => t.id).sort();
  assert.deepEqual(todayIds, ["TODAY"], `older overdue tasks must not leak into "Due today", got ${todayIds}`);

  app.filterDue = "overdue";
  const overdueIds = app.columns().find((c) => c.status === "todo")!.tasks.map((t) => t.id).sort();
  assert.deepEqual(overdueIds, ["OLD"], `only genuinely past-due tasks match "Overdue", got ${overdueIds}`);

  app.filterDue = "week";
  const weekIds = app.columns().find((c) => c.status === "todo")!.tasks.map((t) => t.id).sort();
  assert.deepEqual(weekIds, ["TODAY", "TOMORROW"], `"This week" is a forward window: ${weekIds}`);

  app.filterDue = "";
  const allIds = app.columns().find((c) => c.status === "todo")!.tasks.map((t) => t.id).sort();
  assert.deepEqual(allIds, ["OLD", "TODAY", "TOMORROW"]);
});

test("Due: editing round-trip preserves the calendar day shown on the card (duplicate task nuance)", () => {
  const app = crowbanApp();
  // Canonical stored value (this app's own write format).
  app.detail = { id: "X", title: "t", description: "", priority: 3, due_at: `${localYmd(0)}T00:00:00Z`, assignee: undefined } as any;
  app.startEditing();
  assert.equal(app.editDraft.due, localYmd(0), "picker shows the same day the card badge shows");
  assert.equal(dateInputToIso(app.editDraft.due), (app.detail as any).due_at, "re-saving unchanged shifts nothing (bit-identical)");

  // Offset-bearing value written by an external tool: picker and re-save stay on the UTC calendar day.
  app.detail = { id: "Y", title: "t", description: "", priority: 3, due_at: `${localYmd(2)}T08:00:00+08:00`, assignee: undefined } as any;
  app.startEditing();
  assert.equal(app.editDraft.due, localYmd(2), "offset value → same UTC calendar day in the picker");
  const resaved = dateInputToIso(app.editDraft.due)!;
  assert.equal(dueCalendarDate(resaved), localYmd(2), "re-save keeps the calendar day even for offset values");

  // No due date → empty picker.
  app.detail = { id: "Z", title: "t", description: "", priority: 3, assignee: undefined } as any;
  app.startEditing();
  assert.equal(app.editDraft.due, "");
});

// --------------------------------------------------------------------------- Focus trap (task 01M3MMSVRFBPCK329KFSDR1WHV)
test("FocusTrap: nextTrapIndex wraps forward and backward (AC1)", () => {
  // 4 focusables: Tab from last wraps to first; Shift+Tab from first wraps to last.
  assert.equal(nextTrapIndex(4, 0, true), 1);
  assert.equal(nextTrapIndex(4, 2, true), 3);
  assert.equal(nextTrapIndex(4, 3, true), 0); // forward wrap
  assert.equal(nextTrapIndex(4, 3, false), 2);
  assert.equal(nextTrapIndex(4, 1, false), 0);
  assert.equal(nextTrapIndex(4, 0, false), 3); // backward wrap
});

test("FocusTrap: nextTrapIndex handles a single focusable (stays put)", () => {
  assert.equal(nextTrapIndex(1, 0, true), 0);
  assert.equal(nextTrapIndex(1, 0, false), 0);
});

test("FocusTrap: nextTrapIndex with no focusables and outside focus", () => {
  assert.equal(nextTrapIndex(0, 0, true), -1); // nothing to trap — caller preventDefaults
  assert.equal(nextTrapIndex(0, -1, false), -1);
  // Focus was outside the trap: start at the nearer end.
  assert.equal(nextTrapIndex(3, -1, true), 0);
  assert.equal(nextTrapIndex(3, -1, false), 2);
  // Out-of-range indices degrade to the same ends (no crash).
  assert.equal(nextTrapIndex(3, 99, true), 0);
  assert.equal(nextTrapIndex(3, -7, false), 2);
});

test("FocusTrap: pickFocusable keeps tabbable AND visible elements only", () => {
  const els = [
    { tabIndex: 0 }, // button/input — keep
    { tabIndex: -1 }, // explicit non-tabbable — drop
    { tabIndex: 5 }, // custom positive tabindex — keep
  ];
  assert.deepEqual(pickFocusable(els), [{ tabIndex: 0 }, { tabIndex: 5 }]); // default: all shown

  // Hidden (x-show display:none) elements are dropped even when tabbable.
  const vis = [
    { tabIndex: 0, hidden: false },
    { tabIndex: 0, hidden: true },
    { tabIndex: -1, hidden: false },
  ];
  assert.deepEqual(
    pickFocusable(vis, (el) => !(el as any).hidden),
    [{ tabIndex: 0, hidden: false }],
  );

  // Empty input → empty output.
  assert.deepEqual(pickFocusable([]), []);
});
