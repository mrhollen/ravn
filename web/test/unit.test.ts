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
import { beginMove, canMove, endMove, friendlyTransitionError, legalTargets, withTimeout } from "../src/moves.ts";
import { canApplyDetail, coalesceInFlight, createDebounce, createSequenceGuard, shouldApplyBoard } from "../src/refresh.ts";

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
  // The leak condition: scratchpad still holds a dirty draft owned by A, and
  // the user has just opened B (open-drawer context — required since
  // 01M3MMY78F1TRP6MKDRT0SKQWB made applyDetail ownership-guarded).
  app.detailOpen = true;
  app.detailId = "B";
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

  // Re-open A (open-drawer context restored): applyDetail populates A's server
  // notes into a clean scratchpad.
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDraft = "";
  app.notesDirty = false;
  app.applyDetail(fakeDetail("A", "saved notes of A"));
  assert.equal(app.notesDraft, "saved notes of A");
  assert.equal(app.notesDirty, false);
});

// ---------------------------------------------------------------------------
// canApplyDetail — drawer ownership guard (task 01M3MMY78F1TRP6MKDRT0SKQWB)
// ---------------------------------------------------------------------------

test("Drawer: canApplyDetail only admits an open drawer's own payload", () => {
  assert.equal(canApplyDetail("A", "A", true), true, "open + matching id applies");
  assert.equal(canApplyDetail("A", "B", true), false, "foreign task id never applies (switch mid-fetch)");
  assert.equal(canApplyDetail("A", "A", false), false, "closed drawer ignores in-flight fetches");
  assert.equal(canApplyDetail(null, "A", true), false, "null payload id fails closed");
  assert.equal(canApplyDetail("", "A", true), false, "empty payload id fails closed");
  assert.equal(canApplyDetail("A", null, true), false, "no active task id fails closed");
});

test("Drawer: a stale foreign response must NOT reset a dirty draft of the open task", () => {
  const app = crowbanApp();
  app.detailOpen = true;
  app.detailId = "B";
  // User is editing B's notes…
  app.notesDirty = true;
  app.notesDraft = "notes for B + my edit";
  app.notesDraftOwner = "B";

  // …when a slow task-A response (started before the switch) lands, it must be
  // discarded wholesale — including leaving the dirty draft completely intact.
  const detailBefore = app.detail;
  app.applyDetail(fakeDetail("A", "notes for A"));
  assert.equal(app.notesDirty, true, "dirty flag untouched by a discarded foreign payload");
  assert.equal(app.notesDraft, "notes for B + my edit", "draft text untouched");
  assert.equal(app.notesDraftOwner, "B", "draft ownership untouched");
  assert.equal(app.detail, detailBefore, "detail row untouched");
});

test("Drawer: a response for a closed drawer is discarded (AC3 core)", () => {
  const app = crowbanApp();
  app.detailOpen = true;
  app.detailId = "A";
  app.applyDetail(fakeDetail("A", "notes for A"));
  assert.equal(app.detail?.id, "A");

  // Drawer closes (clean) — an in-flight response for A landing afterwards…
  app.closeDetail();
  const draftAfterClose = app.notesDraft; // closeDetail already reset the scratchpad
  app.applyDetail(fakeDetail("A", "late notes for A"));
  assert.equal(app.detailOpen, false);
  assert.equal(app.notesDraft, draftAfterClose, "no state writes after close");

  // …and reopening a DIFFERENT task must not be clobbered by that late payload.
  // (openTask sets these synchronously before its fetch; set them directly to
  // avoid openTask's DOM-touching path, which node doesn't have.)
  app.detailOpen = true;
  app.detailId = "B";
  app.detail = null;
  app.applyDetail(fakeDetail("A", "late notes for A"));
  assert.equal(app.detail, null, "late foreign payload did not install itself into B's drawer");
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

// ---------------------------------------------------------------------------
// moves.ts — per-task in-flight lock + request timeout (task 01M3MMY2SYX3RR9NZHJTSVBS1C)
// ---------------------------------------------------------------------------

test("Moves: beginMove allows a task, then blocks a DUPLICATE of the same id only", () => {
  const busy = new Set<string>();
  assert.equal(beginMove(busy, "A"), true); // A starts moving
  assert.equal(beginMove(busy, "A"), false); // duplicate concurrent move of A is blocked
  assert.equal(beginMove(busy, "B"), true); // a DIFFERENT task stays fully interactive
  assert.deepEqual(
    [...busy].sort(),
    ["A", "B"],
  );
});

test("Moves: endMove releases the lock so the same task can move again (idempotent)", () => {
  const busy = new Set<string>();
  beginMove(busy, "A");
  endMove(busy, "A");
  assert.equal(beginMove(busy, "A"), true); // re-allowed after release
  endMove(busy, "A");
  endMove(busy, "A"); // idempotent — no throw
  assert.equal([...busy].length, 0);
});

test("Moves: withTimeout resolves fast calls untouched and does not abort", async () => {
  let seenAborted = false;
  const r = await withTimeout(50, async (signal) => {
    seenAborted = signal.aborted;
    return "ok";
  });
  assert.equal(r.value, "ok");
  assert.equal(r.timedOut, false);
  assert.equal(seenAborted, false); // fast path never trips the abort
});

test("Moves: withTimeout reports timedOut=true when the deadline passes", async () => {
  const r = await withTimeout(20, (signal) => new Promise((_res, rej) => {
    signal.addEventListener("abort", () => rej(new Error("aborted")));
  }));
  assert.equal(r.value, undefined);
  assert.equal(r.timedOut, true); // we aborted it ourselves — a stall, not an API error
  assert.ok(r.error instanceof Error);
});

test("Moves: withTimeout distinguishes a real error from a timeout (timedOut=false)", async () => {
  const boom = new Error("version_conflict");
  const r = await withTimeout(100, async () => {
    throw boom;
  });
  assert.equal(r.value, undefined);
  assert.equal(r.timedOut, false); // the fn threw its own error before the deadline
  assert.equal(r.error, boom);
});

test("Moves: withTimeout clears its timer once the request settles (no dangling abort)", async () => {
  // A call that resolves well before a SHORT deadline must leave no pending
  // abort — if the timer leaked, it would fire during the wait below.
  let abortedAfterSettle = false;
  const r = await withTimeout(60, (signal) => {
    signal.addEventListener("abort", () => (abortedAfterSettle = true));
    return "done";
  });
  assert.equal(r.value, "done");
  // Past the 60ms deadline: a leaked timer would have aborted by now.
  await new Promise((res) => setTimeout(res, 100));
  assert.equal(abortedAfterSettle, false);
});

// ---------------------------------------------------------------------------
// refresh.ts — SSE reload debounce + sequence/ownership guards (task 01M3MMXYJXBSD4JTR6EK463M7E)
// ---------------------------------------------------------------------------

/** Virtual clock with real timer semantics (fire in due order, one at a time). */
function fakeClock() {
  let now = 0;
  let nextId = 1;
  const timers = new Map<number, { at: number; fn: () => void }>();
  return {
    setTimer(fn: () => void, ms: number) {
      const id = nextId++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimer(id: unknown) {
      timers.delete(id as number);
    },
    /** Advance the clock, running any timers that come due along the way. */
    advance(ms: number) {
      const target = now + ms;
      for (;;) {
        let earliestId: number | null = null;
        let earliestAt = Infinity;
        for (const [id, t] of timers) {
          if (t.at <= target && t.at < earliestAt) {
            earliestAt = t.at;
            earliestId = id;
          }
        }
        if (earliestId === null) break;
        now = earliestAt;
        const t = timers.get(earliestId)!;
        timers.delete(earliestId);
        t.fn();
      }
      now = target;
    },
  };
}

test("Refresh: trailing debounce coalesces a burst into ONE call after the quiet window", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);

  // Burst of 4 "events" within the 200ms window (each restarts the window).
  d.schedule(); // t=0
  clock.advance(50);
  d.schedule(); // t=50
  clock.advance(70);
  d.schedule(); // t=120
  clock.advance(30);
  d.schedule(); // t=150

  clock.advance(49); // t=199: window from last (150+200=350) not elapsed yet
  assert.equal(calls, 0, "no reload before the quiet period ends");
  clock.advance(50); // t=249 — still before t=350
  assert.equal(calls, 0, "window is measured from the LAST event, not the first");

  clock.advance(101); // t=350: due
  assert.equal(calls, 1, "burst produced EXACTLY one reload");
  clock.advance(500); // nothing scheduled since → no more calls
  assert.equal(calls, 1);
});

test("Refresh: a lone schedule fires once at the window edge", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);
  d.schedule();
  clock.advance(199);
  assert.equal(calls, 0);
  clock.advance(1); // exactly t=200
  assert.equal(calls, 1);
});

test("Refresh: cancel() drops the pending call; isPending reflects state", () => {
  const clock = fakeClock();
  let calls = 0;
  const d = createDebounce(() => calls++, 200, clock as never);
  assert.equal(d.isPending(), false);
  d.schedule();
  assert.equal(d.isPending(), true);
  d.cancel();
  assert.equal(d.isPending(), false);
  clock.advance(1000);
  assert.equal(calls, 0, "cancelled reload never fired");
});

test("Refresh: sequence guard — next() retires older tokens, latest() is a read", () => {
  const g = createSequenceGuard();
  const t1 = g.next();
  const t2 = g.next();
  assert.equal(g.latest(), t2, "latest() reads without issuing");
  assert.equal(g.isFresh(t2), true);
  assert.equal(g.isFresh(t1), false, "an earlier-initiated load is stale after a newer one");
});

test("Refresh: shouldApplyBoard — fresh + owned applies; stale or foreign-project never", () => {
  const ok = { token: 5, latestToken: 5, payloadProjectId: "P1", selectedProjectId: "P1" };
  assert.equal(shouldApplyBoard(ok), true);

  assert.equal(
    shouldApplyBoard({ ...ok, token: 4 }),
    false,
    "stale response (newer load initiated) must not overwrite fresh state",
  );
  assert.equal(
    shouldApplyBoard({ ...ok, selectedProjectId: "P2" }),
    false,
    "late project-A payload must never render into selected project B",
  );
  assert.equal(shouldApplyBoard({ ...ok, payloadProjectId: null }), false, "unknown ownership fails closed");
  assert.equal(shouldApplyBoard({ ...ok, selectedProjectId: "" }), false, "no project selected → fail closed");
});

test("Refresh: coalesceInFlight — concurrent duplicates share ONE execution", async () => {
  let runs = 0;
  const slow = (ms: number) => new Promise((res) => setTimeout(res, ms));
  const c = coalesceInFlight(async (x: number) => { runs++; await slow(20); return x * 10; });

  const p1 = c.call(1); // first caller starts the flight
  await new Promise((r) => setTimeout(r, 5)); // while in flight…
  const p2 = c.call(99); // …a duplicate JOINS it (args ignored, shared promise)
  assert.equal(c.inFlight(), true);

  const [r1, r2] = await Promise.all([p1, p2]);
  assert.equal(runs, 1, "underlying fn ran exactly once");
  assert.equal(r1, 10);
  assert.equal(r2, 10, "joiner gets the same settled result");
  assert.equal(c.inFlight(), false, "flight cleared after settle");

  await c.call(2); // a later call runs again (no caching)
  assert.equal(runs, 2, "coalescing is per-interval, not a memo cache");
});

test("Refresh: coalesceInFlight — a failed flight resets so the next call retries", async () => {
  let runs = 0;
  let failNext = true;
  const c = coalesceInFlight(async (): Promise<string> => {
    runs++;
    if (failNext) throw new Error("boom");
    return "ok";
  });

  const p1 = c.call();
  const p2 = c.call(); // joins the failing flight
  await assert.rejects(() => p1, /boom/);
  await assert.rejects(() => p2, /boom/, "joiner sees the same rejection");
  assert.equal(c.inFlight(), false, "failed flight is released too");

  failNext = false;
  assert.equal(await c.call(), "ok", "after a failure the next call re-runs (no stuck coalescer)");
  assert.equal(runs, 2, "exactly two executions: the failed flight + the successful retry");
});

// --------------------------------------------------------------- transition map
// The client transition map (moves.ts LEGAL_TRANSITIONS) is a MIRROR of the
// server's domain.CanTransition — internal/domain/status.go, matrix pinned by
// TestCanTransition in internal/domain/status_test.go. These tests pin the same
// pairs so a drift on either side shows up here or in `go test ./internal/...`.

test("Moves: canMove mirrors the full 9-pair server transition matrix", () => {
  const pairs: Array<[string, string, boolean]> = [
    // legal (the only three)
    ["todo", "doing", true],
    ["doing", "done", true],
    ["doing", "todo", true],
    ["done", "todo", true],
    // illegal — skipping a step / going backwards through doing
    ["todo", "done", false],
    ["done", "doing", false],
    // self-transitions: rejected by domain.CanTransition (API no-ops them)
    ["todo", "todo", false],
    ["doing", "doing", false],
    ["done", "done", false],
  ];
  for (const [from, to, want] of pairs) {
    assert.equal(canMove(from, to), want, `canMove(${from}, ${to})`);
  }
});

test("Moves: canMove fails closed on unknown/empty statuses (like the server)", () => {
  for (const [from, to] of [
    ["", "todo"],
    ["todo", ""],
    ["unknown", "unknown"],
    ["doing", "invalid"],
  ] as Array<[string, string]>) {
    assert.equal(canMove(from, to), false, `canMove(${from}, ${to})`);
  }
});

test("Moves: legalTargets returns each status' allowed destinations (server order)", () => {
  assert.deepEqual(legalTargets("todo"), ["doing"]);
  assert.deepEqual(legalTargets("doing"), ["done", "todo"]);
  assert.deepEqual(legalTargets("done"), ["todo"]);
  assert.deepEqual(legalTargets("nonsense"), []);
  const copy = legalTargets("doing");
  copy.push("nope");
  assert.deepEqual(legalTargets("doing"), ["done", "todo"], "returns a fresh copy, not the map entry");
});

test("Moves: friendlyTransitionError maps the server's ErrInvalid message to a friendly toast", () => {
  // Exact server shape — internal/application/tasks.go wraps domain.ErrInvalid:
  const e = new Error('invalid input: cannot transition status from "todo" to "done"');
  assert.equal(friendlyTransitionError(e), "Can't move To Do → Done — that's not an allowed step.");

  const e2 = new Error('invalid input: cannot transition status from "done" to "doing"');
  assert.equal(friendlyTransitionError(e2), "Can't move Done → Doing — that's not an allowed step.");

  // Anything else (network errors, version conflicts, plain failures) passes through untouched.
  assert.equal(friendlyTransitionError(new Error("version conflict")), null);
  assert.equal(friendlyTransitionError(new TypeError("fetch failed")), null);
  assert.equal(friendlyTransitionError(null), null);
  assert.equal(friendlyTransitionError("cannot transition status from \"todo\" to \"done\""), null, "non-Error values are not interpreted");
});
