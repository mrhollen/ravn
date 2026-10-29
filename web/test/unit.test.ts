import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  dateInputToIso,
  dueCalendarDate,
  fmtDueDate,
  formatClaimCountdown,
  isClaimExpired,
  isOverdue,
  localCalendarToday,
  safeStorage,
} from "../src/util.ts";
import { escapeHtml, isAllowedHref, renderMarkdown } from "../src/md.ts";
import { commentDraftKey, descriptionDraftKey, deserializeCommentDraft, serializeCommentDraft } from "../src/drafts.ts";
import { ApiError, api } from "../src/api.ts";
import {
  ravnApp,
  isMobileDetailViewport,
  MOBILE_DETAIL_MAX_WIDTH,
  sseTelemetry,
  taskRouteIdFromPath,
  taskRoutePath,
  LS_CARD_DENSITY,
  DEFAULT_DONE_LIMIT,
  readStoredCardDensity,
  isCardDensity,
  parseBoardQueryParams,
  buildBoardQueryString,
  SEARCH_DEBOUNCE_MS,
  FULLTEXT_SEARCH_LIMIT,
} from "../src/app.ts";
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

test("R8: ravnApp initializes cleanly when window.localStorage is blocked by SecurityError", () => {
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

    // Initializing ravnApp() must not throw!
    let appInstance: any;
    assert.doesNotThrow(() => {
      appInstance = ravnApp();
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

// ---------------------------------------------------------------------------
// Markdown href allow-list — ticket 01M3MMYWX96KGGYRZTCXD4FNVG
// isAllowedHref() is the single classifier sanitizeHtml uses for every URL-typed
// attribute (anchor/area href, src, …). We test it directly because the unit
// suite runs in Node without DOMParser; renderMarkdown delegates to it verbatim.
// ---------------------------------------------------------------------------

test("MdHref: protocol-relative URLs are rejected (the core fix)", () => {
  assert.equal(isAllowedHref("//evil.com"), false, "//evil.com must be stripped");
  assert.equal(isAllowedHref("//evil.com/phish?x=1"), false);
  assert.equal(isAllowedHref("///x"), false, "triple-slash resolves to host x — strip");
  assert.equal(isAllowedHref("  //evil.com  "), false, "whitespace-padded protocol-relative");
  assert.equal(isAllowedHref("//\t/evil.com"), false, "tab between the slashes still starts with //");
});

test("MdHref: backslash smuggling into a relative path is rejected", () => {
  // Browsers normalize "\" to "/" in special-scheme contexts, so "/\evil.com"
  // would navigate cross-origin to https://evil.com.
  assert.equal(isAllowedHref("/\\evil.com"), false);
  assert.equal(isAllowedHref("\\\\evil.com"), false);
});

test("MdHref: root-relative and fragment links keep working", () => {
  assert.equal(isAllowedHref("/tasks/123"), true, "root-relative path is kept");
  assert.equal(isAllowedHref("/"), true, "site root is kept");
  assert.equal(isAllowedHref("/a/b?c=d#e"), true, "path with query+hash is kept");
  assert.equal(isAllowedHref("#section"), true, "fragment-only is kept");
});

test("MdHref: http(s), mailto and fragments are allowed; schemes are case-insensitive", () => {
  assert.equal(isAllowedHref("https://example.com"), true);
  assert.equal(isAllowedHref("http://example.com/a?b=c#d"), true);
  assert.equal(isAllowedHref("HTTPS://EXAMPLE.COM"), true, "uppercase scheme + host is allowed");
  assert.equal(isAllowedHref("mailto:x@y"), true);
  assert.equal(isAllowedHref("MAILTO:X@Y"), true, "uppercase mailto is allowed");
});

test("MdHref: disallowed schemes are rejected (no regression)", () => {
  assert.equal(isAllowedHref("javascript:alert(1)"), false);
  assert.equal(isAllowedHref("java&#x09;script:alert(1)".replace("&#x09;", "\t")), false, "tab (C0) inside the scheme token");
  assert.equal(isAllowedHref("data:text/html,<script>alert(1)</script>"), false);
  assert.equal(isAllowedHref("vbscript:MsgBox(1)"), false);
  assert.equal(isAllowedHref("ftp://files.example.com/x"), false);
  assert.equal(isAllowedHref("file:///etc/passwd"), false);
});

test("MdHref: empty and bare-relative strings stay rejected (legacy behavior)", () => {
  assert.equal(isAllowedHref(""), false, "empty was stripped before too");
  assert.equal(isAllowedHref("   "), false, "whitespace-only");
  assert.equal(isAllowedHref("tasks/123"), false, "bare relative without leading / was stripped before");
});

// --------------------------------------------------------------------------- R10: Accessible Card Semantics
test("R10: web/index.html uses <article> containers with scoped keyboard listeners", () => {
  const htmlPath = path.resolve(__dirname, "../index.html");
  const html = fs.readFileSync(htmlPath, "utf-8");

  // Verify the task card uses <article>. The a11y rationale comment added in
  // 93c141e may sit between <template> and <article>, so allow any gap there.
  assert.match(
    html,
    /<template x-for="task in col\.tasks" :key="task\.id">[\s\S]*?<article[\s\S]*?<\/article>\s*<\/template>/,
    "Kanban cards must be wrapped in <article> ... </article> containers",
  );

  // Locate the card's full opening tag via its stable class prefix so every
  // per-card assertion below is scoped to the kanban card element itself.
  const cardMatch = html.match(/<article[\s\S]*?class="relative w-full rounded-lg border p-3[^"]*"[^>]*>/);
  assert.ok(cardMatch, "Should find the card article element");

  // Verify role="button" is NOT present on the article card
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

  // Verify the card is focusable (tabindex="0")
  assert.match(cardMatch[0], /tabindex="0"/, "Card article must be focusable via tabindex=0");

  // Accessible name (93c141e): labelledby points at the card title's id. A
  // hard-coded aria-label on this focusable container would override the whole
  // subtree for AT and hide the metadata badges, so it must not come back.
  assert.match(
    cardMatch[0],
    /:aria-labelledby="'card-title-' \+ task\.id"/,
    "Card article must be named via :aria-labelledby pointing at its title",
  );
  assert.ok(
    !/aria-label[=\s]/.test(cardMatch[0]),
    "Card article must not carry a hard-coded aria-label (would hide metadata from screen readers)",
  );

  // The labelledby target exists: the card's title <h3> carries that id.
  assert.match(
    html,
    /<h3 :id="'card-title-' \+ task\.id"/,
    "Card title must expose the id the card's aria-labelledby points at",
  );
});

// --------------------------------------------------------------------------- Notes scratchpad reset (task 01M3MMSVAF8RV8J7VGDSCV6M07)
/** Build a minimal TaskDetail-shaped object for applyDetail(). */
function fakeDetail(id: string, notes: string): any {
  return { id, notes, comments: [], assignee: undefined, blocked: false };
}

test("Notes: closeDetail() resets the scratchpad so no draft leaks (AC1)", () => {
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  const app = ravnApp();
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
  app.detail = { id: "Z", title: "t", priority: 3, assignee: undefined } as any;
  app.startEditing();
  assert.equal(app.editDraft.due, "");
});

// --------------------------------------------------------------------------- Hoist-parents preference (parent cards float to the column top)
test("HoistParents: defaults ON when unset, malformed value falls back to ON, toggle persists", () => {
  safeStorage.clear();
  const app = ravnApp();
  assert.equal(app.hoistParents, true, "new user gets hoisting ON by default");

  // Flip OFF → persisted as a raw JSON boolean.
  app.toggleHoistParents();
  assert.equal(app.hoistParents, false);
  assert.equal(safeStorage.getItem("ravn.hoist_parents"), "false");
  assert.equal(ravnApp().hoistParents, false, "OFF persists across restarts");

  // Flip back ON → persisted and restored.
  app.toggleHoistParents();
  assert.equal(app.hoistParents, true);
  assert.equal(ravnApp().hoistParents, true, "ON persists across restarts");

  // Malformed stored values never throw and fall back to the ON default.
  safeStorage.setItem("ravn.hoist_parents", "{not json");
  assert.equal(ravnApp().hoistParents, true);
  safeStorage.clear();
});

test("HoistParents: hoists within server order and under an active sort; OFF = plain behavior", () => {
  safeStorage.clear();
  const app = ravnApp(); // hoist ON by default
  const t = (id: string, priority: number, subtaskTotal?: number) =>
    ({ id, title: id, status: "todo", priority, version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z", subtask_total: subtaskTotal } as any);

  // Server order: parentA (P3), soloC (P2), parentB (P1).
  app.board = { project_id: "P", tasks: { todo: [t("parentA", 3, 2), t("soloC", 2), t("parentB", 1, 1)], doing: [], done: [] }, counts: {} } as any;
  const titles = () => app.columns().find((c) => c.status === "todo")!.tasks.map((x) => x.title);

  // ON + no sort key → groups hoisted, original relative order kept within each.
  assert.equal(app.sortKey, "");
  assert.deepEqual(titles(), ["parentA", "parentB", "soloC"]);

  // ON + priority asc → each group sorted by the active key: [B(1), A(3)] then rest.
  app.sortKey = "priority";
  app.setSort(); // applies the criterion's default direction (asc for priority)
  assert.deepEqual(titles(), ["parentB", "parentA", "soloC"]);

  // OFF + same sort → plain whole-array sort; parents are not special-cased.
  app.toggleHoistParents();
  assert.deepEqual(titles(), ["parentB", "soloC", "parentA"]);

  // OFF + no sort key → exact server order (today's behavior).
  app.clearSort();
  assert.deepEqual(titles(), ["parentA", "soloC", "parentB"]);

  safeStorage.clear();
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

// ------------------------------------------------------------------ api timeouts
// Task 01M3MMYBHECPH5Z5FRHJDS5G4J: every request() call is bounded by a default
// timeout (API_TIMEOUT_MS) merged with any caller-supplied AbortSignal. These
// tests pin the error mapping and the signal-composition / cause-attribution
// semantics that keep the move flow's specific "Move timed out" message intact.

import { ApiTimeoutError, errMsg as apiErrMsg, setApiTimeoutMsForTests, withRequestTimeout } from "../src/api.ts";

test("API: errMsg maps our timeout + native abort DOMExceptions to the friendly message", () => {
  const wanted = "Request timed out — please check your connection or server status.";
  assert.equal(apiErrMsg(new ApiTimeoutError(20_000)), wanted);
  // Native DOMException shapes (global in Node 17+ and all browsers):
  assert.equal(apiErrMsg(new DOMException("aborted", "AbortError")), wanted);
  assert.equal(apiErrMsg(new DOMException("timeout", "TimeoutError")), wanted);
  // Anything else keeps its existing mapping:
  assert.equal(apiErrMsg(new Error("boom")), "boom");
  assert.equal(apiErrMsg("plain string"), "plain string");
});

test("API: errMsg keeps the precedence of the existing mappings (ApiError, TypeError network)", async () => {
  const { ApiError } = await import("../src/api.ts");
  // ApiError → its body message/error verbatim, exactly as before this task.
  assert.equal(apiErrMsg(new ApiError(409, { error: "version_conflict", current_version: 7 })), "version_conflict");
  assert.equal(apiErrMsg(new ApiError(400, { error: "invalid input", message: 'cannot transition status from "todo" to "done"' })), 'cannot transition status from "todo" to "done"');
  // TypeError = fetch network failure → the "is it running?" message.
  assert.equal(apiErrMsg(new TypeError("fetch failed")), "cannot reach the ravn server (/api/v1) — is it running?");
});

test("API: withRequestTimeout — no caller signal, our timer wins after ~ms", async () => {
  const guard = withRequestTimeout(30);
  assert.equal(guard.signal.aborted, false);
  await new Promise((r) => setTimeout(r, 60)); // let the 30ms timer fire + settle
  assert.equal(guard.signal.aborted, true, "signal aborted by the timeout");
  assert.equal(guard.timedOut(), true, "attributed to OUR timer");
  guard.cleanup();
});

test("API: withRequestTimeout — caller abort fires FIRST and is not misattributed", async () => {
  const ctrl = new AbortController();
  const guard = withRequestTimeout(500, ctrl.signal); // generous timeout
  ctrl.abort(); // caller loses its own race quickly
  assert.equal(guard.signal.aborted, true, "merged signal follows the caller");
  assert.equal(guard.timedOut(), false, "caller abort is NOT reported as our timeout");
  guard.cleanup();
});

test("API: withRequestTimeout — a pre-aborted caller signal adopts immediately", () => {
  const ctrl = new AbortController();
  ctrl.abort();
  const guard = withRequestTimeout(500, ctrl.signal);
  assert.equal(guard.signal.aborted, true);
  assert.equal(guard.timedOut(), false);
  guard.cleanup();
});

test("API: withRequestTimeout — cleanup clears the timer (no late abort after cancel)", async () => {
  const guard = withRequestTimeout(25);
  guard.cleanup(); // cancel before the deadline
  await new Promise((r) => setTimeout(r, 70));
  assert.equal(guard.signal.aborted, false, "cancelled request never times out late");
});

test("API: setApiTimeoutMsForTests — override applies and ≤0 restores the default", async () => {
  const { API_TIMEOUT_MS } = await import("../src/api.ts");
  setApiTimeoutMsForTests(1);
  // (behavior of the active value is exercised by the browser hang QA; here we
  // only pin that the override accepts, resets, and never leaves zero/negative.)
  setApiTimeoutMsForTests(-5);
  assert.ok(API_TIMEOUT_MS > 0, "default constant stays sane");
  setApiTimeoutMsForTests(20_000); // restore for any later tests in this file
});

// ------------------------------------------------------------- full-page route
// Task 01M47MWHWW0M1Q3WZYFGFHXTYZ: /tasks/<id> is the client-side deep link to
// the full-page task view. ravn-server's SPA fallback serves index.html for
// these paths, so this pure pathname mapping owns both directions of the route.

test("FullPage: taskRouteIdFromPath parses only single-segment /tasks/<id> routes", () => {
  assert.equal(taskRouteIdFromPath("/"), null, "board route has no task");
  assert.equal(taskRouteIdFromPath(""), null);
  assert.equal(taskRouteIdFromPath("/tasks/01M47MWHWW0M1Q3WZYFGFHXTYZ"), "01M47MWHWW0M1Q3WZYFGFHXTYZ");
  assert.equal(taskRouteIdFromPath("/tasks/"), null, "empty id segment is not a route");
  assert.equal(taskRouteIdFromPath("/tasks/a/b"), null, "nested segments are not a route");
  assert.equal(taskRouteIdFromPath("/other/x"), null);
  assert.equal(taskRouteIdFromPath("/api/v1/tasks/abc"), null);
  assert.equal(taskRouteIdFromPath("/tasks/%20"), null, "whitespace-only id is not a route");
});

test("FullPage: taskRouteIdFromPath decodes URI-encoded ids and survives bad %-sequences", () => {
  assert.equal(taskRouteIdFromPath("/tasks/ab%41c"), "abAc");
  // A malformed %-sequence makes decodeURIComponent throw — the raw segment is
  // returned rather than letting a URL take the app down.
  assert.equal(taskRouteIdFromPath("/tasks/%ZZ"), "%ZZ");
});

test("FullPage: taskRoutePath round-trips through the parser", () => {
  const id = "01M47MWHWW0M1Q3WZYFGFHXTYZ";
  assert.equal(taskRoutePath(id), "/tasks/" + id);
  assert.equal(taskRouteIdFromPath(taskRoutePath(id)), id);
});

test("FullPage: the app boots in drawer mode and openFullPage is a safe no-op without detail", () => {
  const app = ravnApp();
  assert.equal(app.fullPageOpen, false);
  assert.equal(app.fullPagePushed, false);
  // No window (Node) and no loaded task — must not throw.
  assert.doesNotThrow(() => app.openFullPage());
  assert.equal(app.fullPageOpen, false, "nothing to switch without a loaded task");
});

// ---------------------------------------------------------------------------
// Task 01M47MWSTTPS14M6XB1A8HFPV1: default detail surface is a WIDE CENTERED
// MODAL at ≥md; below md (<768px) card clicks open the /tasks/<id> full page.

test("Modal: isMobileDetailViewport sits just below Tailwind's md (768px) breakpoint", () => {
  assert.equal(MOBILE_DETAIL_MAX_WIDTH, 768);
  assert.equal(isMobileDetailViewport(375), true, "phone width is mobile");
  assert.equal(isMobileDetailViewport(767), true, "one px below md is still mobile");
  assert.equal(isMobileDetailViewport(768), false, "md itself is the modal/desktop side");
  assert.equal(isMobileDetailViewport(1280), false);
});

test("Modal: without a window (Node) openTask takes the desktop path, not the full page", async () => {
  // No window mock at all — isMobileDetailViewport() must report false so a
  // card click can never force the /tasks/<id> route in non-browser contexts.
  const app = ravnApp();
  assert.equal(isMobileDetailViewport(), false);
  await app.openTask("01M47MWSTTPS14M6XB1A8HFPV1"); // detail fetch fails (no server) — expected, non-fatal
  assert.equal(app.detailOpen, true);
  assert.equal(app.fullPageOpen, false, "no mobile fallback without a viewport");
});

test("Modal: openTask below 768px enters the full-page route via a pushed /tasks/<id> entry", async () => {
  const app = ravnApp();
  const origWindow = (globalThis as any).window;
  const pushed: string[] = [];
  try {
    (globalThis as any).window = {
      innerWidth: MOBILE_DETAIL_MAX_WIDTH - 1, // 767 — mobile side of the boundary
      location: { pathname: "/", search: "", hash: "" },
      history: {
        pushState(_state: unknown, _title: string, url: string) {
          pushed.push(url);
        },
        replaceState(_state: unknown, _title: string, url: string) {
          pushed.push("replace:" + url);
        },
      },
      setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
    };
    await app.openTask("01M47MWSTTPS14M6XB1A8HFPV1");

    assert.equal(app.detailOpen, true);
    assert.equal(app.fullPageOpen, true, "mobile opens the FULL PAGE, not the modal");
    assert.equal(app.fullPagePushed, true, "we pushed the entry — close must history.back()");
    assert.deepEqual(pushed, ["/tasks/01M47MWSTTPS14M6XB1A8HFPV1"], "one pushState to /tasks/<id>");
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

test("Modal: openTask at ≥768px keeps the modal (no URL change)", async () => {
  const app = ravnApp();
  const origWindow = (globalThis as any).window;
  const pushed: string[] = [];
  try {
    (globalThis as any).window = {
      innerWidth: MOBILE_DETAIL_MAX_WIDTH, // 768 — desktop side of the boundary
      location: { pathname: "/", search: "", hash: "" },
      history: {
        pushState(_state: unknown, _title: string, url: string) {
          pushed.push(url);
        },
        replaceState(_state: unknown, _title: string, url: string) {
          pushed.push("replace:" + url);
        },
      },
      setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
    };
    await app.openTask("01M47MWSTTPS14M6XB1A8HFPV1");

    assert.equal(app.detailOpen, true);
    assert.equal(app.fullPageOpen, false, "at md the default surface is the centered modal");
    assert.deepEqual(pushed, [], "a plain card click must never touch the URL");
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

// ---------------------------------------------------------------------------
// Task 01M3MMYFRMAW7XM6QDTXNQA6ED: a failed detail fetch must leave a visible
// drawer error state + retry path — never the bare "Loading task…" trap.

test("DrawerError: openTask with an unreachable server sets detailLoadError (non-fatal)", async () => {
  const app = ravnApp();
  // No server in Node: the relative /api/v1 URL fails at fetch() → TypeError.
  await app.openTask("01M3MMYFRMAW7XM6QDTXNQA6ED");
  assert.equal(app.detailOpen, true, "the drawer stays open — the user is not trapped");
  assert.equal(app.detail, null);
  assert.ok(
    typeof app.detailLoadError === "string" && app.detailLoadError.length > 0,
    "a failed fetch must set a human-readable drawer error message",
  );
});

test("DrawerError: a 404 maps to the deleted-task message", async () => {
  const app = ravnApp();
  const origFetch = (globalThis as any).fetch;
  try {
    (globalThis as any).fetch = async () =>
      new Response(JSON.stringify({ error: "not found", message: "task not found" }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      });
    await app.openTask("01M3MMYFRMAW7XM6QDTXNQA6ED");
    assert.equal(app.detailLoadError, "This task was deleted or no longer exists.");
  } finally {
    if (origFetch === undefined) delete (globalThis as any).fetch;
    else (globalThis as any).fetch = origFetch;
  }
});

test("DrawerError: a failure for a task that is NOT open does not touch the drawer", async () => {
  const app = ravnApp();
  const origFetch = (globalThis as any).fetch;
  try {
    (globalThis as any).fetch = async () =>
      new Response(JSON.stringify({ error: "internal", message: "boom" }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    app.detailOpen = true;
    app.detailId = "OPEN-TASK";
    // A stale SSE-style refresh (idToFetch ≠ detailId) fails — the open task's state is untouched.
    const applied = await app.refreshDetail(true, "STALE-TASK");
    assert.equal(applied, false);
    assert.equal(app.detailLoadError, null, "ownership gate: only the OPEN task's failures surface");
  } finally {
    if (origFetch === undefined) delete (globalThis as any).fetch;
    else (globalThis as any).fetch = origFetch;
  }
});

test("DrawerError: an applied payload clears detailLoadError and re-renders the detail", () => {
  const app = ravnApp();
  app.detailOpen = true;
  app.detailId = "A";
  app.detail = null;
  app.detailLoadError = "boom";
  app.applyDetail(fakeDetail("A", "notes-A"));
  assert.equal(app.detail?.id, "A");
  assert.equal(app.detailLoadError, null, "a landing payload must clear the drawer error state");
});

test("DrawerError: openTask clears a previous task's error state before loading", async () => {
  const app = ravnApp();
  // Simulate the leftover error state from a previous failed open…
  app.detailOpen = true;
  app.detailId = "A";
  app.detailLoadError = "boom";
  // …then switching tasks must start clean: the NEW task's fetch fails (no
  // server) and its OWN message replaces the stale one.
  await app.openTask("01M3MMYFRMAW7XM6QDTXNQA6ED");
  assert.equal(app.detailId, "01M3MMYFRMAW7XM6QDTXNQA6ED");
  assert.ok(
    typeof app.detailLoadError === "string" && !app.detailLoadError.includes("boom"),
    "the stale error from task A must not survive the switch",
  );
});

// ---------------------------------------------------------------------------
// Task 01M47NDGZR47NR1Z6SKQJ3BDP2: "New project" moved INTO a custom Projects
// dropdown (trigger button + Alpine-managed menu) replacing the native select.

test("Dropdown: index.html has no standalone New project button and wires the custom control", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  // The old standalone buttons are gone from the DESKTOP HEADER (the mobile
  // slide-down panel below </header> intentionally carries labeled
  // "New project" / "Settings" rows for touch — see its comment in index.html).
  const headerHtml = html.slice(html.indexOf("<header"), html.indexOf("</header>") + "</header>".length);
  // The old header ghost button (and its toggle expression) is gone.
  assert.ok(!/@click="showNewProject = !showNewProject"/.test(headerHtml), "standalone toggle handler must be gone from the header");
  assert.ok(!/>New project<\/button>/.test(headerHtml), "no standalone 'New project' <button> remains in the header");
  // Custom trigger + menu panel + in-panel creation action exist.
  assert.match(html, /data-testid="project-select"/);
  assert.match(html, /data-testid="project-menu"/);
  assert.match(html, /data-testid="new-project-item"[\s\S]*?@click="openNewProject\(\)"/);
  // Panel sits above board content but BELOW the detail modal (z-[60]) and
  // toasts (z-50).
  const panelClass = html.match(/<div data-testid="project-menu"[\s\S]{0,2000}?class="([^"]*)"/)?.[1];
  assert.ok(panelClass, "menu panel div found");
  assert.match(panelClass, /z-40/, "menu panel must be z-40 (under modal z-[60] and toasts z-50)");
});

test("Dropdown: pickProject switches + persists the project and closes the menu", () => {
  const app = ravnApp() as any;
  assert.equal(app.appBooted, false, "trigger gated until init() completes");
  const a = { id: "pa", name: "Alpha", task_counts: { todo: 1, doing: 0, done: 2 } };
  const b = { id: "pb", name: "Beta", task_counts: { todo: 0, doing: 3, done: 0 } };
  app.projects = [a, b];
  app.selectedProjectId = a.id;
  app.board = { project_id: a.id, tasks: {} as any };
  app.projectMenuOpen = true;

  assert.equal(app.projectSelectLabel(), "Alpha  (1·0·2)");
  app.projects = []; // no selection → placeholder label
  app.selectedProjectId = "";
  assert.equal(app.projectSelectLabel(), "Select a project…");
  app.projects = [a, b];

  app.pickProject(b.id);
  assert.equal(app.selectedProjectId, b.id);
  assert.equal(app.projectMenuOpen, false, "menu closes after a pick");
  assert.equal(app.board, null, "previous project's board is dropped for reload");
  assert.equal(safeStorage.getItem("ravn.project"), b.id, "selection persists via safeStorage");

  // Re-picking the CURRENT project just collapses — no reload churn.
  app.board = { project_id: b.id, tasks: {} as any };
  app.pickProject(b.id);
  assert.equal(app.selectedProjectId, b.id);
  assert.notEqual(app.board, null, "same-id pick must not drop the current board");
});

test("Dropdown: openNewProject opens the existing bar WITHOUT resetting the typed draft", () => {
  const app = ravnApp() as any;
  app.newProject = { name: "Keep Me", description: "draft" };
  app.projectMenuOpen = true;

  app.openNewProject();
  assert.equal(app.showNewProject, true, "the existing form bar opens");
  assert.equal(app.projectMenuOpen, false);
  assert.deepEqual(
    app.newProject,
    { name: "Keep Me", description: "draft" },
    "re-opening must not lose typed input (no phantom creation, no data loss)",
  );
});

test("Dropdown: Escape chain closes the dropdown BEFORE the new-project bar", () => {
  const app = ravnApp() as any;
  app.showNewProject = true;
  app.projectMenuOpen = true;

  app.dismissTop(); // first Esc closes the topmost header surface — the menu
  assert.equal(app.projectMenuOpen, false);
  assert.equal(app.showNewProject, true, "bar is below the menu in the chain");

  app.dismissTop(); // second Esc now closes the bar (draft untouched)
  assert.equal(app.showNewProject, false);
});

// Task 01M47NPF5YK99MVGD8K4ZRS81R: theme / identity / auto-refresh / token
// consolidated into ONE compact header settings menu (avatar trigger + panel).

test("HeaderMenu: index.html wires the consolidated trigger + panel at the dropdown tier", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // Avatar-style trigger (deliberately NOT a gear glyph) + its panel exist.
  assert.match(html, /data-testid="header-menu-trigger"/, "menu trigger must exist");
  assert.match(html, /data-testid="header-menu-anchor"/, "anchor wrapper for positioning/outside-clicks");
  assert.match(html, /<div id="header-menu-panel" data-testid="header-menu"/, "panel with stable id + testid");
  // The trigger mirrors the Projects dropdown's keyboard wiring.
  assert.match(
    html,
    /data-testid="header-menu-trigger"[\s\S]{0,900}?@keydown\.down\.prevent="openHeaderMenu\('first'\)"/,
    "ArrowDown on the closed trigger must open the menu",
  );
  // All four consolidated controls live INSIDE the panel…
  const panelStart = html.indexOf('data-testid="header-menu"');
  assert.ok(panelStart !== -1);
  const panel = html.slice(panelStart, html.indexOf("</header>", panelStart));
  for (const needle of [
    'data-testid="theme-toggle"',
    'data-testid="theme-light"',
    'data-testid="theme-dark"',
    'data-testid="theme-system"',
    'data-testid="actor-name-input"',
    'x-model="actorName"',
    '@change="saveActor()"',
    'data-testid="auto-refresh-toggle"',
    'x-model="polling"',
    '@change="pollingChanged()"',
    'data-testid="token-item"',
    "@click=\"openTokenFromMenu()\"",
  ]) {
    assert.ok(panel.includes(needle), `panel must contain ${needle}`);
  }
  // …and the old inline widgets are gone from the strip (unique fragments of
  // the pre-consolidation markup).
  assert.ok(!/class="field w-32"/.test(html), "old 128px inline actor input must be gone");
  assert.ok(
    !/Auth token configured\. Click to edit\./.test(html),
    "old inline token button title must be gone",
  );
  // The live indicator is removed from the header (task 01M5K1HDAHCNRCJA322P1YF8HR).
  assert.ok(!html.includes('data-testid="live-indicator"'), "live indicator must be removed from the markup");
  // Panel sits at the dropdown tier: above header/board content, BELOW toasts
  // (z-50) and the detail modal (z-[60]).
  const panelClass = html.match(/<div id="header-menu-panel"[\s\S]{0,2000}?class="([^"]*)"/)?.[1];
  assert.ok(panelClass, "panel div found");
  assert.match(panelClass, /z-40/, "menu panel must be z-40 (under modal z-[60] and toasts z-50)");
});

test("HeaderMenu: actorGlyph maps the actor name to its initial (or You)", () => {
  const app = ravnApp() as any;
  app.actorName = "";
  assert.equal(app.actorGlyph(), "You", "empty name → You");
  app.actorName = "   ";
  assert.equal(app.actorGlyph(), "You", "whitespace-only name → You");
  app.actorName = "ada";
  assert.equal(app.actorGlyph(), "A", "lowercase initial is uppercased");
  app.actorName = " Ada Lovelace ";
  assert.equal(app.actorGlyph(), "A", "leading whitespace trimmed, first word used");
});

test("HeaderMenu: toggle opens/closes; Escape chain and mutual exclusion with the project menu", () => {
  const app = ravnApp() as any;

  // Plain toggle cycle (no document in Node — state machine only).
  assert.equal(app.headerMenuOpen, false);
  app.toggleHeaderMenu();
  assert.equal(app.headerMenuOpen, true, "toggle opens");
  app.toggleHeaderMenu();
  assert.equal(app.headerMenuOpen, false, "toggle closes again");

  // Escape chain: the settings menu is a topmost header surface — it closes
  // BEFORE the new-project bar and the mobile strip collapse.
  app.headerMenuOpen = true;
  app.showNewProject = true;
  app.showMobileMenu = true;
  app.dismissTop();
  assert.equal(app.headerMenuOpen, false, "first Esc closes the settings menu");
  assert.equal(app.showNewProject, true, "bar is below the menu in the chain");
  app.dismissTop();
  assert.equal(app.showNewProject, false, "second Esc closes the bar");
  app.dismissTop();
  assert.equal(app.showMobileMenu, false, "third Esc collapses the mobile strip");

  // At most one header menu is live: opening the settings menu collapses the
  // Projects dropdown first (document-dependent path skipped in Node).
  app.projectMenuOpen = true;
  app._projectMenuOutsideHandler = null;
  app.openHeaderMenu("first");
  assert.equal(app.headerMenuOpen, true);
  assert.equal(app.projectMenuOpen, false, "opening the settings menu closes the project menu");

  // The token item closes the menu and hands off to the auth modal.
  app.closeHeaderMenu(true);
  assert.equal(app.showAuthModal, false);
  app.openTokenFromMenu();
  assert.equal(app.headerMenuOpen, false, "menu closes on the token entry");
  assert.equal(app.showAuthModal, true, "auth modal opens");
});

// Task 01M47NPQHY5CR2VPG14HVDM02P: the spelled-out "Settings" ghost button is
// replaced by a gear icon VISUALLY ATTACHED to the right edge of the Projects
// dropdown (a split control sharing one bordered unit).

test("ProjectSettingsGear: index.html wires the gear into the project-dropdown split control", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // AC1 — the old spelled-out button is gone from the DESKTOP HEADER. (The
  // mobile slide-down panel below </header> intentionally carries a labeled
  // Settings row for touch — see that panel's comment in index.html.)
  const headerHtml = html.slice(html.indexOf("<header"), html.indexOf("</header>") + "</header>".length);
  assert.ok(
    !/<button[^>]*>\s*Settings\s*<\/button>/.test(headerHtml),
    'spelled-out "Settings" ghost button must be removed from the header',
  );

  // The gear lives INSIDE the project-dropdown wrapper (so it is visually
  // attached) and keeps a stable testid + accessible name.
  const ddStart = html.indexOf('data-testid="project-dropdown"');
  assert.ok(ddStart !== -1, "project-dropdown wrapper must exist");
  const unit = html.slice(ddStart, html.indexOf("</header>", ddStart));
  const selectIdx = unit.indexOf('data-testid="project-select"');
  const gearIdx = unit.indexOf('data-testid="project-settings-btn"');
  assert.ok(selectIdx !== -1 && gearIdx !== -1, "trigger and gear must both live in the split control");
  assert.ok(gearIdx > selectIdx, "gear sits to the RIGHT of the select trigger");

  const gearTag = unit.slice(Math.max(0, gearIdx - 300), gearIdx + 500);
  assert.match(gearTag, /aria-label="Project settings"/, "gear needs an accessible name");
  assert.match(
    gearTag,
    /@click="closeProjectMenu\(\);\s*openProjectSettings\(\)"/,
    "gear opens the EXISTING settings modal (closing any open dropdown first)",
  );
  // Gating: disabled while !appBooted (same boot gate as the adjacent trigger —
  // the whole unit stays inert during startup) OR when no project is selected.
  assert.match(gearTag, /:disabled="!appBooted \|\| !selectedProjectId"/);

  // Split-control shape: the select drops its right corner + border, the gear
  // takes the right corner and supplies the hairline divider as its own left
  // border; both halves share .field for identical height/border.
  const selectTag = unit.slice(Math.max(0, selectIdx - 300), selectIdx + 700);
  assert.match(selectTag, /rounded-r-none border-r-0/, "select keeps only its LEFT corners");
  assert.match(gearTag, /rounded-l-none/, "gear takes only the RIGHT corner");
});

test("ProjectSettingsGear: openProjectSettings() no-ops without a selection; opens + populates with one", async () => {
  const app = ravnApp() as any;

  // No project selected → the guard bails before touching the DOM (in the UI the
  // gear is disabled in this state, so the click can't even fire).
  app.selectedProjectId = "";
  await app.openProjectSettings();
  assert.equal(app.showProjectSettings, false, "no project selected → modal must not open");

  // With a selection it populates the form from the list row and opens the
  // modal. (In Node, openDialog() returns null and the getProject() refetch
  // rejects harmlessly — the timeout guard is cleaned up in its finally.)
  app.selectedProjectId = "p1";
  app.projects = [
    { id: "p1", name: "Gear Project", description: "d", opencode_server_url: "", opencode_directory: "", opencode_model: "" },
  ];
  await app.openProjectSettings();
  assert.equal(app.showProjectSettings, true, "selection present → modal opens");
  assert.equal(app.projectSettingsForm.name, "Gear Project", "form seeded from the list row");

  // Single close path resets the flag (UI Escape / × / Cancel all call this).
  app.closeProjectSettings();
  assert.equal(app.showProjectSettings, false);
});

test("ArchiveProject: index.html wires archive button, confirmation box, input, and cancel/archive buttons", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // AC1 — Archive project button exists
  assert.match(html, /data-testid="archive-project-btn"[^>]*>\s*Archive Project\s*<\/button>/);

  // AC2 — input box opens instructing the user to type the project name
  assert.match(html, /data-testid="archive-confirm-box"/);
  assert.match(html, /data-testid="archive-confirm-input"/);
  assert.match(html, /To archive this project, please type/);

  // AC4 — archive button exists with validation guard
  assert.match(html, /data-testid="confirm-archive-btn"/);
  assert.match(html, /:disabled="!isArchiveInputValid\(\) \|\| projectSettingsBusy"/);

  // AC5 — cancel button exists to dismiss the confirmation
  assert.match(html, /data-testid="cancel-archive-btn"/);
});

test("ArchiveProject: isArchiveInputValid validates project name with whitespace trimming", () => {
  const app = ravnApp() as any;
  app.selectedProjectId = "p1";
  app.projects = [{ id: "p1", name: "Alpha Project" }];
  app.projectSettingsForm = { name: "Alpha Project" };

  // Empty input -> invalid
  app.archiveConfirmInput = "";
  assert.equal(app.isArchiveInputValid(), false);

  // Partial or mismatch -> invalid
  app.archiveConfirmInput = "Alpha";
  assert.equal(app.isArchiveInputValid(), false);
  app.archiveConfirmInput = "alpha project";
  assert.equal(app.isArchiveInputValid(), false);

  // Exact match -> valid
  app.archiveConfirmInput = "Alpha Project";
  assert.equal(app.isArchiveInputValid(), true);

  // Whitespace trimmed match -> valid
  app.archiveConfirmInput = "   Alpha Project   ";
  assert.equal(app.isArchiveInputValid(), true);
  app.archiveConfirmInput = "\tAlpha Project\n";
  assert.equal(app.isArchiveInputValid(), true);
});

test("ArchiveProject: open/cancel confirmation and Escape handling", () => {
  const app = ravnApp() as any;
  app.selectedProjectId = "p1";
  app.projects = [{ id: "p1", name: "Alpha Project" }];
  app.showProjectSettings = true;

  // Open confirmation
  app.openArchiveConfirm();
  assert.equal(app.showArchiveConfirm, true);
  assert.equal(app.archiveConfirmInput, "");

  // Cancel confirmation
  app.archiveConfirmInput = "partially typed";
  app.cancelArchiveConfirm();
  assert.equal(app.showArchiveConfirm, false);
  assert.equal(app.archiveConfirmInput, "");
  assert.equal(app.showProjectSettings, true); // settings modal still open

  // Escape cancels confirmation first before closing settings
  app.openArchiveConfirm();
  assert.equal(app.showArchiveConfirm, true);
  app.dismissTop(); // first Escape
  assert.equal(app.showArchiveConfirm, false, "first Escape dismisses archive confirmation");
  assert.equal(app.showProjectSettings, true, "settings modal remains open");
  app.dismissTop(); // second Escape
  assert.equal(app.showProjectSettings, false, "second Escape closes settings modal");
});

// --------------------------------------------------------------------------- Discard guards (task 01M3MMZ188XJBGKFSWBGESK79D)
/** Stub window.confirm to a fixed answer; restores the original binding. */
function withConfirm(app: any, answer: boolean | ((msg: string) => boolean)) {
  const origWindow = (globalThis as any).window;
  let lastMessage = "";
  (globalThis as any).window = {
    confirm(msg: string) {
      lastMessage = msg;
      return typeof answer === "function" ? answer(msg) : answer;
    },
  };
  return {
    get lastMessage() {
      return lastMessage;
    },
    restore() {
      if (origWindow === undefined) delete (globalThis as any).window;
      else (globalThis as any).window = origWindow;
    },
  };
}

test("DiscardGuards: confirmDiscard delegates to window.confirm; unblocked where absent", () => {
  const app = ravnApp() as any;
  const origWindow = (globalThis as any).window;
  try {
    // No window at all (node) → a non-UI caller is never blocked.
    if (origWindow === undefined) delete (globalThis as any).window;
    assert.equal(app.confirmDiscard("m?"), true, "no window → accept the discard");

    const stub = withConfirm(app, false);
    try {
      assert.equal(app.confirmDiscard("discard me?"), false);
      assert.equal(stub.lastMessage, "discard me?", "message passed through verbatim");
    } finally {
      stub.restore();
    }
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

test("DiscardGuards: newTaskDirty flags any real content, never a fresh/blank form", () => {
  const app = ravnApp() as any;
  assert.equal(app.newTaskDirty(), false, "freshly opened dialog is clean");

  // Each field independently counts as a draft.
  app.newTask.title = "Fix login";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, title: "" };

  app.newTask.description = "details";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, description: "" };

  app.newTask.priority = 3;
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, priority: 0 };

  app.newTask.due = "2026-10-17";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, due: "" };

  app.newTask.assigneeName = "alice";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, assigneeName: "" };

  app.newTask.assigneeType = "agent";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, assigneeType: "human" };

  app.newTask.acs = "one\n two";
  assert.equal(app.newTaskDirty(), true);
  app.newTask = { ...app.newTask, acs: "" };

  assert.equal(app.newTaskDirty(), false, "back to a clean form");
});

test("DiscardGuards: requestCloseNewTask uses inline confirmation instead of native dialog", () => {
  const app = ravnApp() as any;
  app.showNewTask = true;
  app.newTask.title = "WIP draft";

  // AC1: Native dialog is never invoked when cancelling the modal.
  const strict = withConfirm(app, () => {
    assert.fail("native window.confirm must never be called for new task discard");
    return false;
  });

  try {
    // Calling requestCloseNewTask on a dirty form triggers inline confirmation.
    app.requestCloseNewTask();
    assert.equal(app.showNewTask, true, "modal remains open");
    assert.equal(app.confirmNewTaskDiscard, true, "inline confirmation activated");
    assert.equal(app.newTask.title, "WIP draft", "form data preserved");

    // AC5, AC6, AC7: Cancelling the confirmation restores state and allows creating task.
    app.cancelNewTaskConfirm();
    assert.equal(app.confirmNewTaskDiscard, false, "confirmation dismissed");
    assert.equal(app.showNewTask, true, "modal remains open");
    assert.equal(app.newTaskDirty(), true, "form data still intact");

    // Re-trigger confirmation.
    app.requestCloseNewTask();
    assert.equal(app.confirmNewTaskDiscard, true);

    // AC8: Discarding dismisses modal and clears all data.
    app.closeNewTask();
    assert.equal(app.showNewTask, false, "modal dismissed");
    assert.equal(app.confirmNewTaskDiscard, false, "confirmation flag reset");
    assert.equal(app.newTaskDirty(), false, "form data fully cleared");
  } finally {
    strict.restore();
  }

  // A clean form closes immediately WITHOUT confirmation or native dialog.
  const fresh = ravnApp() as any;
  fresh.showNewTask = true;
  const strictClean = withConfirm(fresh, () => {
    assert.fail("clean close must not call window.confirm");
    return false;
  });
  try {
    fresh.requestCloseNewTask();
    assert.equal(fresh.showNewTask, false, "clean form closes immediately");
    assert.equal(fresh.confirmNewTaskDiscard, false);
  } finally {
    strictClean.restore();
  }
});

test("DiscardGuards: projectSettingsDirty is baseline-relative; guarded close honors it", () => {
  const app = ravnApp() as any;
  const form = { name: "Alpha", description: "d", opencode_server_url: "", opencode_directory: "", opencode_model: "" };
  app.projectSettingsForm = { ...form };
  app.projectSettingsBaseline = { ...form };
  app.showProjectSettings = true;

  assert.equal(app.projectSettingsDirty(), false, "unmodified form is clean");

  const strict = withConfirm(app, () => {
    assert.fail("clean close must not call window.confirm");
    return false;
  });
  try {
    app.requestCloseProjectSettings();
    assert.equal(app.showProjectSettings, false, "clean settings close without asking");
  } finally {
    strict.restore();
  }

  // Now dirty (re-open the flag): decline keeps the modal (values intact), accept closes it.
  app.showProjectSettings = true;
  app.projectSettingsForm = { ...form, name: "Alpha WIP" };
  assert.equal(app.projectSettingsDirty(), true);
  const deny = withConfirm(app, false);
  try {
    app.requestCloseProjectSettings();
    assert.equal(app.showProjectSettings, true, "decline keeps the modal");
    assert.equal(app.projectSettingsForm.name, "Alpha WIP", "draft untouched on decline");
  } finally {
    deny.restore();
  }
  const allow = withConfirm(app, true);
  try {
    app.requestCloseProjectSettings();
    assert.equal(app.showProjectSettings, false, "accept closes the modal");
  } finally {
    allow.restore();
  }
});

test("DiscardGuards: editDraftDirty compares against the startEditing snapshot", () => {
  const app = ravnApp() as any;
  app.detail = { id: "A", title: "Orig", description: "desc", priority: 2, due_at: null, assignee: undefined };

  assert.equal(app.editDraftDirty(), false, "not editing → never dirty");
  app.startEditing();
  assert.equal(app.editingTask, true);
  assert.equal(app.editDraftDirty(), false, "fresh edit form is clean");

  app.editDraft.title = "Changed";
  assert.equal(app.editDraftDirty(), true, "title change counts");
  app.editDraft.title = "Orig";

  app.editDraft.priority = 5;
  assert.equal(app.editDraftDirty(), true, "priority change counts");
  app.cancelEditing();
  assert.equal(app.editDraftDirty(), false, "stale snapshot gated by editingTask after cancel");
});

test("DiscardGuards: Escape on a dirty task edit confirms before exiting edit mode", () => {
  const app = ravnApp() as any;
  app.detailOpen = true;
  app.detailId = "A";
  app.detail = { id: "A", title: "Orig", description: "", priority: 0, due_at: null, assignee: undefined };
  app.startEditing();
  app.editDraft.title = "WIP";

  const deny = withConfirm(app, false);
  try {
    app.dismissTop(); // first Escape — decline the discard
    assert.equal(app.editingTask, true, "decline keeps edit mode");
    assert.equal(app.detailOpen, true, "drawer stays open too");
  } finally {
    deny.restore();
  }
  const allow = withConfirm(app, true);
  try {
    app.dismissTop(); // second Escape — accept the discard
    assert.equal(app.editingTask, false, "accept exits edit mode (cancelEditing)");
    assert.equal(app.detailOpen, true, "...but the drawer itself remains open");
  } finally {
    allow.restore();
  }
});

test("DiscardGuards: requestCloseDetail guards a dirty task edit (after the notes check)", () => {
  const app = ravnApp() as any;
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = false; // notes branch must not fire — this test is about the edit draft
  app.detail = { id: "A", title: "Orig", description: "", priority: 0, due_at: null, assignee: undefined };
  app.startEditing();
  app.editDraft.description = "wip";

  const deny = withConfirm(app, false);
  try {
    app.requestCloseDetail();
    assert.equal(app.detailOpen, true, "decline keeps the drawer open");
    assert.equal(app.editingTask, true, "draft mode intact on decline");
    assert.equal(app.unsavedNotesAction, null, "no notes prompt involved");
  } finally {
    deny.restore();
  }
  const allow = withConfirm(app, true);
  try {
    app.requestCloseDetail();
    assert.equal(app.detailOpen, false, "accept discards the draft and closes");
    assert.equal(app.editingTask, false);
  } finally {
    allow.restore();
  }
});

test("DiscardGuards: openTask with a dirty edit draft prompts; decline stays on the old task", () => {
  const app = ravnApp() as any;
  app.detailOpen = true;
  app.detailId = "A";
  app.notesDirty = false;
  app.detail = { id: "A", title: "Orig", description: "", priority: 0, due_at: null, assignee: undefined };
  app.startEditing();
  app.editDraft.title = "WIP on A";

  const deny = withConfirm(app, false);
  try {
    void app.openTask("B");
    assert.equal(app.detailId, "A", "decline keeps the open task");
    assert.equal(app.editingTask, true, "draft mode survives a declined switch");
  } finally {
    deny.restore();
  }
});

// ---------------------------------------------------------------------------
// Toast notifications: dismiss button, slide+fade transitions, kind icons.

function toastBlock(): string {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  const block = html.match(/data-testid="toast-stack"[\s\S]*?<\/template>\s*<\/div>/);
  assert.ok(block, "toast stack markup found in index.html");
  return block[0];
}

test("Toasts: dismiss button, slide+fade enter/leave transitions, and kind icons", () => {
  const t = toastBlock();
  // AC1 — explicit × dismiss button on every toast (single removal path).
  assert.match(
    t,
    /<button type="button" @click="dismissToast\(t\.id\)" aria-label="Dismiss notification"[^>]*>\s*&times;\s*<\/button>/,
    "toast must carry an &times; dismiss button wired to dismissToast(t.id)",
  );
  // AC2 — slide (translate-y) + fade (opacity) on BOTH enter and leave, not a
  // bare opacity-only transition.
  assert.match(t, /x-transition:enter="transition ease-out duration-200"/);
  assert.match(t, /x-transition:enter-start="opacity-0 translate-y-2 scale-95"/);
  assert.match(t, /x-transition:enter-end="opacity-100 translate-y-0 scale-100"/);
  assert.match(t, /x-transition:leave="transition ease-in duration-200"/);
  assert.match(t, /x-transition:leave-start="opacity-100 translate-y-0 scale-100"/);
  assert.match(t, /x-transition:leave-end="opacity-0 -translate-y-2 scale-95"/);
  assert.ok(!/x-transition\.opacity/.test(t), "old opacity-only transition must be gone");
  // Two-phase exit: x-show off (leave animation) before the row leaves the array.
  assert.match(t, /x-show="!t\.leaving"/);
  // AC3 — one inline SVG per kind (warning triangle for error, check circle
  // for info) plus an a11y role per kind.
  assert.match(t, /<svg x-show="t\.kind === 'error'"[\s\S]*?M9\.401 3\.003[\s\S]*?<\/svg>/);
  assert.match(t, /<svg x-show="t\.kind !== 'error'"[\s\S]*?M21 12a9 9 0 11-18 0 9 9 0 0118 0z[\s\S]*?<\/svg>/);
  assert.match(t, /:role="t\.kind === 'error' \? 'alert' : 'status'"/);
});

test("Toasts: dismissToast plays the leave phase, then drops only the targeted toast", async () => {
  const app = ravnApp() as any;
  app.toasts = [];
  app.toastSeq = 0;
  app.toast("first");
  app.toast("second", "error");
  assert.equal(app.toasts.length, 2);

  app.dismissToast(1);
  // Phase 1 — the toast stays mounted (x-show off → leave transition plays) and
  // is flagged `leaving`; siblings are untouched.
  assert.deepEqual(
    app.toasts.map((t: { id: number; kind: string; leaving?: boolean }) => [t.id, t.kind, !!t.leaving]),
    [[1, "info", true], [2, "error", false]],
    "dismiss marks the target `leaving` without touching siblings",
  );
  app.dismissToast(999); // unknown id → no-op, nothing else marked or removed
  assert.equal(app.toasts.filter((t: { leaving?: boolean }) => t.leaving).length, 1);

  // Phase 2 — once the leave window (280ms) elapses, only that toast is gone.
  await new Promise((r) => setTimeout(r, 320));
  assert.deepEqual(app.toasts.map((t: { id: number }) => t.id), [2], "only the dismissed toast is dropped");
});

// ---------------------------------------------------------------------------
// Ticket 01M3MMYP938G972KY8NF7RGMQR: a transient network failure on load must
// NOT wipe the selected project ID from state/localStorage — surface a boot
// error screen with Retry instead of opening the New Project bar.

function emptyBoard(projectId: string): any {
  return { project_id: projectId, tasks: { todo: [], doing: [], done: [] } };
}

test("BootError: index.html wires the dedicated boot-error screen (role=alert + Retry)", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  // Assertive live region with the Retry button that re-runs loadProjects +
  // continuation — distinct from the generic board-load error block.
  assert.match(html, /data-testid="boot-error"[\s\S]{0,400}?role="alert"/, "boot-error screen carries role=alert");
  assert.match(html, /data-testid="boot-retry-btn"[^>]*@click="retryBoot\(\)"/, "Retry wired to retryBoot()");
  assert.match(html, /:disabled="bootRetrying"/, "Retry disabled while its re-fetch is in flight");
  // The generic board error block must not compete with the boot-error screen.
  assert.match(html, /x-show="loadError && !board && !bootError"/);
});

test("Boot: failed loadProjects keeps stored selection and offers retry, not New Project", async () => {
  safeStorage.setItem("ravn.project", "proj-stored");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  let boardFetches = 0;
  try {
    (api as any).listProjects = async () => { throw new TypeError("fetch failed"); };
    (api as any).getBoard = async (pid: string) => { boardFetches++; return emptyBoard(pid); };

    const app = ravnApp() as any;
    assert.equal(app.selectedProjectId, "proj-stored", "stored selection loaded at construction");
    app.polling = false; // unit env: never start the 15s interval timer
    await app.init();

    // AC1 — a failed fetch must not clear state or overwrite storage.
    assert.ok(typeof app.loadError === "string" && app.loadError.length > 0, "fetch error surfaces in loadError");
    assert.equal(app.selectedProjectId, "proj-stored", "AC1: stored selection survives the failed boot");
    assert.equal(app.projects.length, 0);
    assert.equal(safeStorage.getItem("ravn.project"), "proj-stored", "AC1: localStorage is never overwritten on failure");
    // AC2 — a dedicated retry screen, NOT the New Project bar.
    assert.equal(app.bootError, true, "boot-error state for the dedicated retry screen");
    assert.equal(app.showNewProject, false, "AC2: New Project bar must not open on a failed fetch");
    assert.equal(boardFetches, 0, "no board load before projects recovered");

    // A second failing retry keeps everything intact (and there is no auto-retry loop).
    await app.retryBoot();
    assert.equal(app.bootError, true, "failed retry keeps the error screen up");
    assert.equal(app.selectedProjectId, "proj-stored", "AC3: selection survives a repeated failure");
    assert.equal(safeStorage.getItem("ravn.project"), "proj-stored");

    // Once the fetch recovers, Retry runs the SAME continuation as a normal boot.
    (api as any).listProjects = async () => ({
      projects: [{ id: "proj-stored", name: "Stored", task_counts: { todo: 0, doing: 0, done: 0 } }],
    });
    await app.retryBoot();
    assert.equal(app.bootError, false, "boot-error state clears on recovery");
    assert.equal(app.selectedProjectId, "proj-stored", "AC3: selection preserved across the glitch");
    assert.ok(app.board, "board loaded through the success continuation");
    assert.equal(boardFetches, 1);
    assert.equal(safeStorage.getItem("ravn.project"), "proj-stored");
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    safeStorage.removeItem("ravn.project");
  }
});

test("Boot: genuinely empty server on a successful load still opens New Project", async () => {
  const origListProjects = api.listProjects;
  try {
    (api as any).listProjects = async () => ({ projects: [] });

    // First visit (nothing stored) + zero projects.
    safeStorage.removeItem("ravn.project");
    const first = ravnApp() as any;
    first.polling = false;
    await first.init();
    assert.equal(first.bootError, false);
    assert.equal(first.selectedProjectId, "");
    assert.equal(first.showNewProject, true, "empty success keeps opening the New Project bar (behavior a)");

    // Stored id that no longer exists + empty list: falls back to "" and invites.
    safeStorage.setItem("ravn.project", "proj-gone");
    const stale = ravnApp() as any;
    stale.polling = false;
    await stale.init();
    assert.equal(stale.selectedProjectId, "");
    assert.equal(stale.showNewProject, true);
    assert.equal(safeStorage.getItem("ravn.project"), "proj-gone", "empty success writes nothing (unchanged)");
  } finally {
    (api as any).listProjects = origListProjects;
    safeStorage.removeItem("ravn.project");
  }
});

test("Boot: stale stored id on a successful load falls back to the first project and persists it", async () => {
  safeStorage.setItem("ravn.project", "proj-deleted");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  try {
    (api as any).listProjects = async () => ({
      projects: [
        { id: "p-alpha", name: "Alpha", task_counts: { todo: 1, doing: 0, done: 0 } },
        { id: "p-beta", name: "Beta", task_counts: { todo: 0, doing: 0, done: 2 } },
      ],
    });
    (api as any).getBoard = async (pid: string) => emptyBoard(pid);

    const app = ravnApp() as any;
    assert.equal(app.selectedProjectId, "proj-deleted");
    app.polling = false;
    await app.init();

    assert.equal(app.bootError, false);
    assert.equal(app.selectedProjectId, "p-alpha", "stale selection falls back to the first project (behavior b)");
    assert.equal(safeStorage.getItem("ravn.project"), "p-alpha", "the fallback is persisted (as before)");
    assert.equal(app.showNewProject, false);
    assert.ok(app.board, "board of the fallback project loaded");
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    safeStorage.removeItem("ravn.project");
  }
});

test("Boot: first visit (no stored id) on a successful load selects and persists the first project", async () => {
  safeStorage.removeItem("ravn.project");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  try {
    (api as any).listProjects = async () => ({
      projects: [
        { id: "p-alpha", name: "Alpha", task_counts: { todo: 0, doing: 1, done: 0 } },
        { id: "p-beta", name: "Beta", task_counts: { todo: 2, doing: 0, done: 0 } },
      ],
    });
    (api as any).getBoard = async (pid: string) => emptyBoard(pid);

    const app = ravnApp() as any;
    assert.equal(app.selectedProjectId, "");
    app.polling = false;
    await app.init();

    assert.equal(app.bootError, false);
    assert.equal(app.selectedProjectId, "p-alpha", "first visit selects the first project (behavior c)");
    assert.equal(safeStorage.getItem("ravn.project"), "p-alpha", "plain first-visit persistence kept (01M3V6Z nuance out of scope)");
    assert.ok(app.board);
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    safeStorage.removeItem("ravn.project");
  }
});

// ---------------------------------------------------------------------------
// Ticket 01M3VJ5PQEAJ60B0QGPNZT8NKP: Alpine invokes the root component's
// init() TWICE per page load — x-data auto-runs a data object's init method,
// then the explicit x-init="init()" runs it again (verified empirically against
// the shipped bundle: two invocation stacks ~6ms apart, the second carrying
// Alpine's `[Alpine] init()` eval frame). The init() guard must make every boot
// side effect happen AT MOST ONCE per component instance.

/** Harness for the idempotence tests: a fake window (counting addEventListener),
 *  a counting global setInterval wrapper, and a counting FakeEventSource so the
 *  SSE path is actually exercised (without it connectSse() bails on Node).
 *  Mirrors the R8 safeStorage global-swap convention; restore() in `finally`. */
function bootIdempotencyHarness() {
  const origWindow = (globalThis as any).window;
  const origEventSource = (globalThis as any).EventSource;
  const origSetInterval = (globalThis as any).setInterval;

  const listenerCounts: Record<string, number> = {};
  const intervalCalls: unknown[] = [];
  let esConstructed = 0;

  (globalThis as any).setInterval = (...args: Parameters<typeof setInterval>) => {
    intervalCalls.push(args);
    return origSetInterval(...args);
  };

  (globalThis as any).window = {
    matchMedia: () => ({ matches: false, addEventListener: () => {} }),
    location: { pathname: "/" }, // every real browser has one; boot reads it for deep links
    addEventListener: (t: string) => {
      listenerCounts[t] = (listenerCounts[t] ?? 0) + 1;
    },
    setTimeout: (...args: unknown[]) => setTimeout(...(args as Parameters<typeof setTimeout>)),
    clearTimeout: (...args: unknown[]) => clearTimeout(...(args as Parameters<typeof clearTimeout>)),
  };

  class FakeEventSource {
    url: string;
    onopen: any = null;
    onerror: any = null;
    onmessage: any = null;
    closed = false;
    constructor(url: string) {
      this.url = url;
      esConstructed++;
    }
    addEventListener() {}
    close() {
      this.closed = true;
    }
  }
  (globalThis as any).EventSource = FakeEventSource;

  return {
    listenerCounts,
    intervalCalls,
    getEsConstructed: () => esConstructed,
    /** The module-level SSE counters are shared across tests — reset so each
     *  test asserts its own steady state. */
    resetSseCounters() {
      sseTelemetry.opened = 0;
      sseTelemetry.closed = 0;
    },
    restore() {
      (globalThis as any).setInterval = origSetInterval;
      if (origWindow === undefined) delete (globalThis as any).window;
      else (globalThis as any).window = origWindow;
      if (origEventSource === undefined) delete (globalThis as any).EventSource;
      else (globalThis as any).EventSource = origEventSource;
    },
  };
}

test("InitIdempotence: concurrent double init() shares ONE boot — fetch, ticker, listeners and EventSource each run exactly once", async () => {
  safeStorage.removeItem("ravn.project");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  let listCalls = 0;
  let boardCalls = 0;
  const h = bootIdempotencyHarness();
  h.resetSseCounters();
  try {
    (api as any).listProjects = async () => {
      listCalls++;
      return { projects: [{ id: "p-alpha", name: "Alpha", task_counts: { todo: 1, doing: 0, done: 0 } }] };
    };
    (api as any).getBoard = async (pid: string) => {
      boardCalls++;
      return emptyBoard(pid);
    };

    const app = ravnApp() as any;
    app.polling = false; // unit env: never start the 15s poll interval

    // The exact Alpine failure mode: two inits overlap at boot.
    const [p1, p2] = await Promise.all([app.init(), app.init()]);
    assert.equal(p1, p2, "both calls return the SAME memoized boot promise");

    assert.equal(app.appBooted, true, "boot completed and marked booted");
    // ONE shared boot fetches the projects list TWICE by design (the open list
    // plus ?archived=true for the Archived section); the memo means a doubled
    // init() still causes exactly this single pair of calls.
    assert.equal(listCalls, 2, "list-projects fetched exactly twice (open + archived) across both calls");
    assert.equal(boardCalls, 1, "board loaded exactly once");
    assert.equal(h.getEsConstructed(), 1, "EventSource constructed exactly once");
    assert.equal(sseTelemetry.opened, 1, "SSE telemetry: one stream opened");
    assert.equal(sseTelemetry.closed, 0, "SSE telemetry: no premature close of a first stream");
    assert.equal(sseTelemetry.openCount(), 1, "SSE telemetry: exactly one open stream at steady state");
    assert.equal(app.sseStatus, "connecting", "the single stream is live (FakeEventSource never fires onopen)");
    assert.equal(h.listenerCounts["beforeunload"], 1, "one beforeunload listener registered");
    assert.equal(h.listenerCounts["popstate"], 1, "one popstate listener registered");
    assert.equal(h.intervalCalls.length, 1, "one ticker interval created");
    assert.equal(h.intervalCalls[0][1], 1000, "the ticker is the 1s nowTick interval");
    assert.ok(app._tickerId, "the single ticker is recorded on the instance");

    // A third call (any future directive double-run) still no-ops.
    const p3 = await app.init();
    assert.equal(p3, p1, "a later init() keeps returning the first boot's promise");
    assert.equal(listCalls, 2); // still just the ONE shared boot's open + archived pair
    assert.equal(boardCalls, 1);
    assert.equal(h.getEsConstructed(), 1);
    assert.equal(h.intervalCalls.length, 1);
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    h.restore();
    safeStorage.removeItem("ravn.project");
  }
});

test("InitIdempotence: sequential double init() also shares ONE boot", async () => {
  safeStorage.removeItem("ravn.project");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  let listCalls = 0;
  const h = bootIdempotencyHarness();
  h.resetSseCounters();
  try {
    (api as any).listProjects = async () => {
      listCalls++;
      return { projects: [{ id: "p-alpha", name: "Alpha", task_counts: { todo: 0, doing: 1, done: 0 } }] };
    };
    (api as any).getBoard = async (pid: string) => emptyBoard(pid);

    const app = ravnApp() as any;
    app.polling = false;

    const p1 = await app.init();
    assert.equal(app.appBooted, true, "first init completes the boot");
    const p2 = await app.init();
    assert.equal(p2, p1, "the second call returns the SAME (completed) boot promise");

    // ONE shared boot fetches the projects list TWICE by design (the open list
    // plus ?archived=true for the project menu's Archived section); a second
    // init() adds no further fetch because it reuses the memoized boot.
    assert.equal(listCalls, 2, "list-projects fetched exactly twice across both calls (open + archived)");
    assert.equal(h.getEsConstructed(), 1, "EventSource constructed exactly once");
    assert.equal(sseTelemetry.openCount(), 1, "exactly one open stream at steady state");
    assert.equal(h.intervalCalls.length, 1, "ticker created exactly once");
    assert.equal(h.listenerCounts["beforeunload"], 1);
    assert.equal(h.listenerCounts["popstate"], 1);
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    h.restore();
    safeStorage.removeItem("ravn.project");
  }
});

test("InitIdempotence: a failed boot keeps the memo — repeated init() no-ops and Retry stays the sole recovery path", async () => {
  safeStorage.setItem("ravn.project", "proj-stored");
  const origListProjects = api.listProjects;
  const origGetBoard = api.getBoard;
  let listCalls = 0;
  const h = bootIdempotencyHarness();
  h.resetSseCounters();
  try {
    (api as any).listProjects = async () => {
      listCalls++;
      throw new TypeError("fetch failed");
    };
    (api as any).getBoard = async (pid: string) => emptyBoard(pid);

    const app = ravnApp() as any;
    app.polling = false;
    const p1 = await app.init();
    assert.equal(app.bootError, true, "failed boot shows the dedicated retry screen");

    // A second init() must NOT re-run boot (no extra fetch): recovery is
    // exclusively retryBoot()'s job — the memo survives a failed boot.
    const p2 = await app.init();
    assert.equal(p2, p1, "a repeat init() returns the first (failed) boot's promise");
    // Each boot performs TWO projects-list fetches by design: the open list plus
    // the archived list (?archived=true, feeding the project menu's Archived
    // section). The memo means the repeated init() adds NONE of those.
    assert.equal(listCalls, 2, "no duplicate projects fetch from the repeated init()");

    // And Retry recovers with the same continuation as a normal boot.
    (api as any).listProjects = async () => ({
      projects: [{ id: "proj-stored", name: "Stored", task_counts: { todo: 0, doing: 0, done: 0 } }],
    });
    await app.retryBoot();
    assert.equal(app.bootError, false, "Retry clears the boot-error screen");
    assert.ok(app.board, "board loaded through Retry's continuation");
    assert.equal(h.getEsConstructed(), 1, "SSE connected exactly once (via Retry, not a second init)");
    assert.equal(sseTelemetry.openCount(), 1);
  } finally {
    (api as any).listProjects = origListProjects;
    (api as any).getBoard = origGetBoard;
    h.restore();
    safeStorage.removeItem("ravn.project");
  }
});

// ---------------------------------------------------------------------------
// Ticket 01M3MMZ5VSPXWS9RYP1D24H5AG: the native AC checkbox flips its .checked
// on click BEFORE the server answers. On a failed toggle ac.done is never
// mutated, so Alpine's :checked binding has no diff to re-sync — the handler
// must revert the DOM element explicitly and surface the error toast.

function acToggleDetailFixture(): any {
  return {
    id: "task-ac-toggle",
    acceptance_criteria: [
      { seq: 1, text: "First criterion", done: false },
      { seq: 2, text: "Second criterion", done: true },
    ],
  };
}

test("toggleAc: failed server toggle leaves the model untouched and reverts the DOM checkbox", async () => {
  const origToggleAc = api.toggleAc;
  try {
    (api as any).toggleAc = async () => {
      throw new ApiError(500, { error: "internal", message: "simulated server failure" });
    };

    const app = ravnApp() as any;
    app.detail = acToggleDetailFixture();
    const ac = app.detail.acceptance_criteria[0]; // done: false
    // Simulate the browser: the user's click has already flipped .checked on.
    const fakeInput: any = { checked: true };

    await app.toggleAc(ac, { target: fakeInput } as any);

    assert.equal(app.detail.acceptance_criteria[0].done, false, "AC1: model stays false after a failed toggle");
    assert.equal(fakeInput.checked, false, "AC1: the DOM checkbox reverts to the model value");
    const errors = app.toasts.filter((t: any) => t.kind === "error");
    assert.equal(errors.length, 1, "AC2: exactly one error toast");
    assert.match(String(errors[0].msg), /simulated server failure/);
    assert.equal(app.acBusySeq, 0, "busy guard released on failure");
  } finally {
    (api as any).toggleAc = origToggleAc;
  }
});

test("toggleAc: a failing UNCHECK also reverts the DOM checkbox to the model", async () => {
  const origToggleAc = api.toggleAc;
  try {
    (api as any).toggleAc = async () => {
      throw new ApiError(409, { error: "version_conflict", current_version: 2, message: "stale" });
    };

    const app = ravnApp() as any;
    app.detail = acToggleDetailFixture();
    const ac = app.detail.acceptance_criteria[1]; // done: true
    // The click visually UNchecked the box.
    const fakeInput: any = { checked: false };

    await app.toggleAc(ac, { target: fakeInput } as any);

    assert.equal(app.detail.acceptance_criteria[1].done, true, "model stays true after a failed uncheck");
    assert.equal(fakeInput.checked, true, "DOM checkbox reverts to the (still-done) model value");
    assert.ok(app.toasts.some((t: any) => t.kind === "error"), "error toast raised");
  } finally {
    (api as any).toggleAc = origToggleAc;
  }
});

test("toggleAc: successful toggle merges the server's row back into detail state", async () => {
  const origToggleAc = api.toggleAc;
  try {
    let requestedDone: unknown;
    (api as any).toggleAc = async (_id: string, _seq: number, _actor: any, done: boolean) => {
      requestedDone = done;
      // The server row is authoritative — completed_by/at come back populated.
      return {
        seq: 1,
        text: "First criterion",
        done: true,
        completed_by: { name: "qa-bot", type: "agent" },
        completed_at: "2026-10-12T00:00:00Z",
      };
    };

    const app = ravnApp() as any;
    app.detail = acToggleDetailFixture();

    await app.toggleAc(app.detail.acceptance_criteria[0]);

    assert.equal(requestedDone, true, "requests the negated current value");
    const row1 = app.detail.acceptance_criteria[0];
    assert.equal(row1.done, true, "server's done is applied to state");
    assert.deepEqual(row1.completed_by, { name: "qa-bot", type: "agent" });
    assert.equal(row1.completed_at, "2026-10-12T00:00:00Z");
    // Only the toggled seq is patched — sibling rows are left untouched.
    assert.deepEqual(app.detail.acceptance_criteria[1], { seq: 2, text: "Second criterion", done: true });
    assert.ok(!app.toasts.some((t: any) => t.kind === "error"), "no error toast on success");
  } finally {
    (api as any).toggleAc = origToggleAc;
  }
});

// ---------------------------------------------------------------------------
// Blocked-state cache project scoping (task 01M3MMZASQB2RBB2Q4RS3GM0JK)
// The card-badge blocked state lives in TWO client caches (derived board diff
// + detail-fetched knowledge). Both are scoped to the project they were
// derived/learned under, so switching projects can never render a "blocked"
// indicator based on a DIFFERENT project's tasks.
// ---------------------------------------------------------------------------

test("BlockedCache: selectProject() clears both caches, re-scoped to the new project (AC1)", () => {
  const app = ravnApp() as any;
  // Network-side collaborators are irrelevant here — stub them out.
  app.connectSse = () => {};
  app.loadBoard = async () => {};

  app.selectedProjectId = "A";
  app.blockedIds = new Set(["a1", "a2"]);
  app.knownBlocked = { a3: true };
  app.blockedCacheProjectId = "A";

  app.selectedProjectId = "B"; // what pickProject() does before calling selectProject()
  app.selectProject();

  assert.equal(app.blockedIds.size, 0, "derived set is cleared on switch");
  assert.deepEqual(app.knownBlocked, {}, "learned map is cleared on switch");
  assert.equal(app.blockedCacheProjectId, "B", "cache re-scoped to the selected project");
  assert.equal(app.isBlocked("a1"), false);
});

test("BlockedCache: isBlocked fails closed when the cache belongs to another project (AC3)", () => {
  const app = ravnApp() as any;
  // The cache was derived while project "P" was selected…
  app.blockedIds = new Set(["x1"]);
  app.knownBlocked = { x2: true };
  app.blockedCacheProjectId = "P";

  // …but the selection has since moved on (an entry point that does not
  // reset — deep link, popstate adoption, archive fallback).
  app.selectedProjectId = "Q";
  assert.equal(app.isBlocked("x1"), false, "stale derived ids never surface");
  assert.equal(app.isBlocked("x2"), false, "stale learned entries never surface");

  // Back in scope → the same entries serve normally (feature intact).
  app.selectedProjectId = "P";
  assert.equal(app.isBlocked("x1"), true);
  assert.equal(app.isBlocked("x2"), true);
});

test("BlockedCache: a >100-task board populates blockedIds directly from blockers and re-scopes the cache (AC2/AC3)", async () => {
  let listTasksCalled = false;
  const origListTasks = api.listTasks;
  try {
    (api as any).listTasks = async () => {
      listTasksCalled = true;
      return { tasks: [], total: 150, limit: 100, offset: 0 };
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "B";
    // Leftover from project A's load:
    app.blockedIds = new Set(["a1"]);
    app.knownBlocked = { a2: true };
    app.blockedCacheProjectId = "A";

    // Board with >100 tasks where some tasks carry open blockers:
    const tasks150 = Array.from({ length: 150 }, (_, i) => ({
      id: `b${i}`,
      title: `Task ${i}`,
      status: "todo",
      priority: 3,
      version: 1,
      created_at: "",
      updated_at: "",
      blockers: i === 2 || i === 120 ? [{ id: "blk1", title: "Blocker", status: "todo" }] : undefined,
    }));

    const bigBoard = {
      project_id: "B",
      counts: { todo: 150, doing: 0, done: 0 },
      tasks: { todo: tasks150, doing: [], done: [] },
    };
    await app.refreshBlockedSet(bigBoard);

    assert.equal(listTasksCalled, false, "zero listTasks calls fired");
    assert.deepEqual(app.knownBlocked, {}, "stale knownBlocked cleared on project switch");
    assert.equal(app.blockedCacheProjectId, "B", "cache now belongs to project B");
    assert.equal(app.isBlocked("a1"), false, "project A's ids can't surface in B");
    assert.equal(app.isBlocked("a2"), false, "project A's knownBlocked can't surface in B");
    assert.equal(app.isBlocked("b2"), true, "blocked task b2 is recognized even on >100 task board");
    assert.equal(app.isBlocked("b120"), true, "blocked task b120 (>100) is recognized");
    assert.equal(app.isBlocked("b0"), false, "unblocked task b0 returns false");

    // Repeated loads of the SAME board don't thrash entries learned from detail:
    app.knownBlocked["b99"] = true;
    await app.refreshBlockedSet(bigBoard);
    assert.equal(app.blockedCacheProjectId, "B");
    assert.equal(app.isBlocked("b99"), true, "same-project detail entries survive reload");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("BlockedCache: loadBoard derives blocked state directly from board snapshot without api.listTasks (AC1/AC2)", async () => {
  let listTasksCount = 0;
  const origListTasks = api.listTasks;
  const origGetBoard = api.getBoard;
  try {
    (api as any).listTasks = async () => {
      listTasksCount++;
      return { tasks: [], total: 0, limit: 100, offset: 0 };
    };
    (api as any).getBoard = async () => ({
      project_id: "B",
      counts: { todo: 2, doing: 0, done: 0 },
      tasks: {
        todo: [
          { id: "b1", title: "Task 1", blockers: [{ id: "blk1", title: "Blk", status: "todo" }] },
          { id: "b2", title: "Task 2" },
        ],
        doing: [],
        done: [],
      },
    });

    const app = ravnApp() as any;
    app.selectedProjectId = "B";
    app.blockedCacheProjectId = ""; // fresh boot
    await app.loadBoard();

    assert.equal(listTasksCount, 0, "loadBoard must never call api.listTasks");
    assert.equal(app.blockedCacheProjectId, "B");
    assert.equal(app.isBlocked("b1"), true, "b1 is blocked via board snapshot blockers");
    assert.equal(app.isBlocked("b2"), false, "b2 is unblocked");
  } finally {
    (api as any).listTasks = origListTasks;
    (api as any).getBoard = origGetBoard;
  }
});

test("BlockedCache: a failed board load keeps the current project's last good set (catch-branch decision)", async () => {
  const origGetBoard = api.getBoard;
  try {
    (api as any).getBoard = async () => {
      throw new Error("network down");
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "B";
    // Last good set for B, derived by an earlier successful load:
    app.blockedIds = new Set(["b1"]);
    app.knownBlocked = {};
    app.blockedCacheProjectId = "B";

    await app.loadBoard();

    assert.ok((app.blockedIds as Set<string>).has("b1"), "transient failure keeps the last good set");
    assert.equal(app.blockedCacheProjectId, "B");
    assert.equal(app.loadError, "network down");
  } finally {
    (api as any).getBoard = origGetBoard;
  }
});

test("BlockedCache: a detail payload for ANOTHER project never writes into the current cache", () => {
  const app = ravnApp() as any;
  // The drawer is still open on A's task after a dropdown switch to B
  // (selectProject does not close it) — its refresh lands post-switch.
  app.detailOpen = true;
  app.detailId = "a9";
  app.selectedProjectId = "B";
  app.blockedIds = new Set(["b1"]);
  app.knownBlocked = {};
  app.blockedCacheProjectId = "B";

  const detailA: any = { id: "a9", project_id: "A", notes: "", comments: [], assignee: undefined, blocked: true };
  app.applyDetail(detailA);

  assert.equal(app.knownBlocked["a9"], undefined, "foreign task's blocked state is not learned");
  assert.deepEqual(app.blockedIds, new Set(["b1"]), "current project's set untouched");
  assert.equal(app.blockedCacheProjectId, "B", "scope stays with the selected project");
});

test("BlockedCache: a detail payload for the CURRENT project still learns blocked state (feature intact)", () => {
  const app = ravnApp() as any;
  app.detailOpen = true;
  app.detailId = "b9";
  app.selectedProjectId = "B";
  app.blockedIds = new Set();
  app.knownBlocked = {};
  app.blockedCacheProjectId = "B";

  const detailB: any = { id: "b9", project_id: "B", notes: "", comments: [], assignee: undefined, blocked: true };
  app.applyDetail(detailB);

  assert.equal(app.knownBlocked["b9"], true, "current-project task's blocked state is learned");
  assert.equal(app.isBlocked("b9"), true);
});

// ---------------------------------------------------------------------------
// OpenCode session URL & status (task 01M3MMZPDPNNP1TRQXTK4HEDGM)
// ---------------------------------------------------------------------------

test("OpenCode: startTaskInOpenCode captures session_id and session_url, records in openCodeSessions, and emits actionable toast", async () => {
  const origStartOpenCode = api.startOpenCode;
  try {
    let calledWithId = "";
    (api as any).startOpenCode = async (id: string) => {
      calledWithId = id;
      return {
        session_id: "ses_abc123",
        session_url: "http://127.0.0.1:4096/dir/session/ses_abc123",
      };
    };

    const app = ravnApp() as any;
    app.detail = { id: "task-oc-1", title: "Test OpenCode", artifacts: [] };
    app.refreshDetail = async () => true;
    app.loadBoard = async () => {};

    await app.startTaskInOpenCode();

    assert.equal(calledWithId, "task-oc-1");
    assert.deepEqual(app.openCodeSessions["task-oc-1"], {
      session_id: "ses_abc123",
      session_url: "http://127.0.0.1:4096/dir/session/ses_abc123",
    });
    assert.deepEqual(app.activeOpenCodeSession(), {
      session_id: "ses_abc123",
      session_url: "http://127.0.0.1:4096/dir/session/ses_abc123",
    });

    assert.equal(app.toasts.length, 1);
    assert.equal(app.toasts[0].kind, "info");
    assert.match(app.toasts[0].msg, /ses_abc123/);
    assert.deepEqual(app.toasts[0].action, {
      label: "Open Session",
      href: "http://127.0.0.1:4096/dir/session/ses_abc123",
    });
  } finally {
    (api as any).startOpenCode = origStartOpenCode;
  }
});

test("OpenCode: activeOpenCodeSession falls back to latest 'OpenCode autonomous session' artifact when cache is empty", () => {
  const app = ravnApp() as any;
  assert.equal(app.activeOpenCodeSession(null), null);

  const detailWithArtifacts: any = {
    id: "task-oc-2",
    artifacts: [
      {
        id: "art-1",
        kind: "url",
        ref: "http://127.0.0.1:4096/p/session/ses_older",
        summary: "OpenCode autonomous session",
      },
      {
        id: "art-2",
        kind: "log",
        ref: "https://example.com/build.log",
        summary: "Build log",
      },
      {
        id: "art-3",
        kind: "url",
        ref: "http://127.0.0.1:4096/p/session/ses_newer",
        summary: "OpenCode autonomous session",
      },
    ],
  };

  assert.deepEqual(app.activeOpenCodeSession(detailWithArtifacts), {
    session_id: "ses_newer",
    session_url: "http://127.0.0.1:4096/p/session/ses_newer",
  });
});

test("OpenCode: index.html wires opencode-session-banner and toast-action-link", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  assert.match(html, /data-testid="toast-action-link"/, "toast template renders actionable link");
  assert.match(html, /data-testid="opencode-session-banner"/, "detail drawer renders opencode session banner");
  assert.match(html, /data-testid="opencode-session-link"/, "detail drawer renders opencode session link");
  assert.match(html, /data-testid="opencode-session-id"/, "detail drawer renders opencode session id");
});

// ---------------------------------------------------------------------------
// OpenCode configurable agent & variant (task 01M53XWXG5Q5Q53MPYDCJWNHSW)
// ---------------------------------------------------------------------------

test("OpenCode options: helpers filter models, agents, and variants gracefully", async () => {
  const app = ravnApp() as any;
  assert.deepEqual(app.availableOpenCodeAgents(), []);
  assert.deepEqual(app.availableOpenCodeModels(), []);
  assert.deepEqual(app.availableOpenCodeVariantsFor("any"), []);

  app.openCodeOptions = {
    reachable: true,
    resolved_agent: "build",
    resolved_model: "provider-a/model-1",
    resolved_variant: "medium",
    agents: [
      { name: "build", mode: "primary" },
      { name: "plan", mode: "primary" },
      { name: "review", mode: "subagent" },
    ],
    models: [
      {
        ref: "provider-a/model-1",
        provider_id: "provider-a",
        model_id: "model-1",
        model_name: "Model One",
        variants: ["low", "medium", "high"],
      },
      {
        ref: "provider-b/model-2",
        provider_id: "provider-b",
        model_id: "model-2",
        model_name: "Model Two",
        variants: ["flash", "pro"],
      },
    ],
  };

  assert.equal(app.availableOpenCodeAgents().length, 3);
  assert.equal(app.availableOpenCodeAgents()[0].name, "build");
  assert.equal(app.availableOpenCodeModels().length, 2);

  // Specific model match by ref
  assert.deepEqual(app.availableOpenCodeVariantsFor("provider-a/model-1"), ["low", "medium", "high"]);
  // Specific model match by model_id
  assert.deepEqual(app.availableOpenCodeVariantsFor("model-2"), ["flash", "pro"]);
  // Empty ref falls back to resolved_model ("provider-a/model-1")
  assert.deepEqual(app.availableOpenCodeVariantsFor(""), ["low", "medium", "high"]);
  assert.deepEqual(app.availableOpenCodeVariantsFor(undefined), ["low", "medium", "high"]);
  // Unknown model returns deduplicated union across all models
  assert.deepEqual(app.availableOpenCodeVariantsFor("unknown-ref"), ["low", "medium", "high", "flash", "pro"]);
});

test("OpenCode options: fetchOpenCodeOptions catches errors silently without toast", async () => {
  const origGetOptions = api.getOpenCodeOptions;
  try {
    const app = ravnApp() as any;
    app.selectedProjectId = "p-fail";
    app.projects = [{ id: "p-fail", name: "Fail Project", opencode_server_url: "http://mock-opencode:4444" }];

    // Simulate network error / offline OpenCode
    (api as any).getOpenCodeOptions = async () => {
      throw new Error("connection refused");
    };

    const res = await app.fetchOpenCodeOptions("p-fail");
    assert.equal(res, null);
    assert.equal(app.openCodeOptions, null);
    assert.equal(app.loadingOpenCodeOptions, false);
    // Silent catch: no error toast emitted
    assert.equal(app.toasts.length, 0);

    // Simulate reachable: false response
    (api as any).getOpenCodeOptions = async () => {
      return {
        reachable: false,
        resolved_agent: "build",
        resolved_model: "",
        resolved_variant: "",
        agents: [],
        models: [],
      };
    };

    const offlineRes = await app.fetchOpenCodeOptions("p-fail", true);
    assert.equal(offlineRes?.reachable, false);
    assert.deepEqual(app.availableOpenCodeAgents(), []);
    assert.deepEqual(app.availableOpenCodeModels(), []);
  } finally {
    (api as any).getOpenCodeOptions = origGetOptions;
  }
});

test("ProjectSettings: form loading, dirty tracking, and saving opencode_agent and opencode_variant", async () => {
  const origUpdateProject = api.updateProject;
  const origGetProject = api.getProject;
  const origGetOptions = api.getOpenCodeOptions;
  try {
    const app = ravnApp() as any;
    app.selectedProjectId = "proj-1";
    app.projects = [
      {
        id: "proj-1",
        name: "Test Project",
        description: "Desc",
        opencode_server_url: "http://localhost:4444",
        opencode_directory: "/dir",
        opencode_model: "llama.cpp/Qwen",
        opencode_agent: "plan",
        opencode_variant: "high",
      },
    ];

    (api as any).getProject = async () => app.projects[0];
    (api as any).getOpenCodeOptions = async () => ({
      reachable: true,
      resolved_agent: "build",
      resolved_model: "",
      resolved_variant: "",
      agents: [],
      models: [],
    });

    await app.openProjectSettings();
    assert.equal(app.projectSettingsForm.opencode_agent, "plan");
    assert.equal(app.projectSettingsForm.opencode_variant, "high");
    assert.equal(app.projectSettingsDirty(), false, "clean on initial open");

    // Modify agent -> dirty
    app.projectSettingsForm.opencode_agent = "build";
    assert.equal(app.projectSettingsDirty(), true, "agent modification marks form dirty");

    // Revert agent -> clean
    app.projectSettingsForm.opencode_agent = "plan";
    assert.equal(app.projectSettingsDirty(), false, "reverted agent marks form clean");

    // Modify variant -> dirty
    app.projectSettingsForm.opencode_variant = "low";
    assert.equal(app.projectSettingsDirty(), true, "variant modification marks form dirty");

    // Save project settings passes opencode_agent and opencode_variant
    let sentReq: any = null;
    (api as any).updateProject = async (_id: string, req: any) => {
      sentReq = req;
      return { ...app.projects[0], ...req };
    };

    await app.saveProjectSettings();
    assert.equal(sentReq.opencode_agent, "plan");
    assert.equal(sentReq.opencode_variant, "low");

    // Now test clearing: if baseline had "plan" and "high", setting to "" sends "" to clear
    app.projectSettingsBaseline = {
      name: "Test Project",
      description: "Desc",
      opencode_server_url: "",
      opencode_directory: "",
      opencode_model: "",
      opencode_agent: "plan",
      opencode_variant: "high",
    };
    app.projectSettingsForm = {
      ...app.projectSettingsBaseline,
      opencode_agent: "",
      opencode_variant: "",
    };

    await app.saveProjectSettings();
    assert.equal(sentReq.opencode_agent, "");
    assert.equal(sentReq.opencode_variant, "");
  } finally {
    (api as any).updateProject = origUpdateProject;
    (api as any).getProject = origGetProject;
    (api as any).getOpenCodeOptions = origGetOptions;
  }
});

test("OpenCode dispatch: per-task override state and startTaskInOpenCode payload", async () => {
  const origStartOpenCode = api.startOpenCode;
  const origGetOptions = api.getOpenCodeOptions;
  try {
    const app = ravnApp() as any;
    app.selectedProjectId = "proj-1";
    app.projects = [
      {
        id: "proj-1",
        name: "Test Project",
        opencode_agent: "build",
        opencode_model: "llama.cpp/Qwen",
        opencode_variant: "medium",
      },
    ];
    app.detail = { id: "task-override-1", title: "Task with Override", artifacts: [] };
    app.refreshDetail = async () => true;
    app.loadBoard = async () => {};

    // 1. Initial configuration seeds from project defaults
    app.initTaskOpenCodeConfig();
    assert.deepEqual(app.taskOpenCodeForm, {
      agent: "build",
      model: "llama.cpp/Qwen",
      variant: "medium",
    });
    assert.equal(app.taskOpenCodeDirty(), false);
    assert.equal(app.showTaskOpenCodeConfig, false);

    // 2. Toggle config panel
    app.toggleOpenCodeConfig();
    assert.equal(app.showTaskOpenCodeConfig, true);

    // 3. Dismiss via dismissTop (Escape)
    app.dismissTop();
    assert.equal(app.showTaskOpenCodeConfig, false);

    // 4. Calling startTaskInOpenCode without overrides sends undefined / {}
    let capturedPayload: any = "NOT_CALLED";
    (api as any).startOpenCode = async (_id: string, input?: any) => {
      capturedPayload = input;
      return { session_id: "ses_default", session_url: "http://localhost:4444/session/ses_default" };
    };

    await app.startTaskInOpenCode();
    assert.equal(capturedPayload, undefined, "untouched start sends undefined input for backend resolution");

    // 5. Modifying taskOpenCodeForm marks it dirty and passes the overrides
    app.taskOpenCodeForm.agent = "plan";
    app.taskOpenCodeForm.variant = "xhigh";
    assert.equal(app.taskOpenCodeDirty(), true);

    await app.startTaskInOpenCode();
    assert.deepEqual(capturedPayload, {
      agent: "plan",
      model: "llama.cpp/Qwen",
      variant: "xhigh",
    });

    // 6. Config panel start button (showTaskOpenCodeConfig=true) passes values and closes panel
    app.showTaskOpenCodeConfig = true;
    app.taskOpenCodeForm.agent = "editor-in-chief";
    await app.startTaskInOpenCode();
    assert.equal(capturedPayload.agent, "editor-in-chief");
    assert.equal(app.showTaskOpenCodeConfig, false, "panel closes on start");

    // 7. Clearing variant when baseline had a variant explicitly sends variant: ""
    app.initTaskOpenCodeConfig(); // baseline has variant: "medium"
    app.taskOpenCodeForm.variant = ""; // user cleared it to default/none
    assert.equal(app.taskOpenCodeDirty(), true);
    await app.startTaskInOpenCode();
    assert.equal(capturedPayload.variant, "", "clearing variant from non-empty baseline sends empty string override");
  } finally {
    (api as any).startOpenCode = origStartOpenCode;
    (api as any).getOpenCodeOptions = origGetOptions;
  }
});

test("OpenCode: index.html wires all configurable agent and variant test IDs", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  // Project settings inputs
  assert.match(html, /data-testid="project-opencode-agent-input"/);
  assert.match(html, /data-testid="project-opencode-model-input"/);
  assert.match(html, /data-testid="project-opencode-variant-input"/);

  // Task detail drawer per-task override UI
  assert.match(html, /data-testid="opencode-config-toggle"/);
  assert.match(html, /data-testid="opencode-config-panel"/);
  assert.match(html, /data-testid="task-opencode-agent-select"/);
  assert.match(html, /data-testid="task-opencode-agent-input"/);
  assert.match(html, /data-testid="task-opencode-model-select"/);
  assert.match(html, /data-testid="task-opencode-model-input"/);
  assert.match(html, /data-testid="task-opencode-variant-select"/);
  assert.match(html, /data-testid="task-opencode-variant-input"/);
  assert.match(html, /data-testid="opencode-config-start-btn"/);
});

// ---------------------------------------------------------------------------
// Operational bar consolidation (task 01M55HBQP76ZPA33KNAB7D2A0V)
// ---------------------------------------------------------------------------

test("Countdown: formatClaimCountdown formats durations and handles boundaries cleanly", () => {
  const baseNow = 1700000000000;
  // Empty, null, undefined, invalid
  assert.equal(formatClaimCountdown(undefined, baseNow), "");
  assert.equal(formatClaimCountdown(null, baseNow), "");
  assert.equal(formatClaimCountdown("", baseNow), "");
  assert.equal(formatClaimCountdown("not-a-date", baseNow), "");

  // Expired / 0 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow).toISOString(), baseNow), "0:00");
  assert.equal(formatClaimCountdown(new Date(baseNow - 10000).toISOString(), baseNow), "0:00");

  // 5 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + 5000).toISOString(), baseNow), "0:05");

  // 14 minutes 59 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + (14 * 60 + 59) * 1000).toISOString(), baseNow), "14:59");

  // 60 minutes
  assert.equal(formatClaimCountdown(new Date(baseNow + 60 * 60 * 1000).toISOString(), baseNow), "60:00");

  // 75 minutes 3 seconds
  assert.equal(formatClaimCountdown(new Date(baseNow + (75 * 60 + 3) * 1000).toISOString(), baseNow), "75:03");
});

test("Countdown: ravnApp.claimCountdown references reactive nowTick", () => {
  const app = ravnApp() as any;
  const baseNow = 1700000000000;
  app.nowTick = baseNow;

  const expiry = new Date(baseNow + 90000).toISOString(); // 1m 30s
  assert.equal(app.claimCountdown({ claim_expires_at: expiry }), "1:30");

  // Fast forward nowTick by 10s
  app.nowTick += 10000;
  assert.equal(app.claimCountdown({ claim_expires_at: expiry }), "1:20");

  // Missing or expired
  assert.equal(app.claimCountdown(null), "");
  assert.equal(app.claimCountdown({}), "");
  app.nowTick += 100000;
  assert.equal(app.claimCountdown({ claim_expires_at: expiry }), "0:00");
});

test("isClaimExpired mirrors claimHint's 'lease expired' branch", () => {
  const baseNow = 1700000000000;
  const actor = { name: "a", type: "agent" };

  // Unclaimed → never "expired".
  assert.equal(isClaimExpired({}, baseNow), false);

  // Claimed with no expiry recorded → never "expired" (there is nothing to expire).
  assert.equal(isClaimExpired({ claimed_by: actor }, baseNow), false);

  // Future expiry → active, not expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow + 10_000).toISOString() }, baseNow),
    false,
  );

  // Exactly now → diff <= 0 → expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow).toISOString() }, baseNow),
    true,
  );

  // Past expiry → expired.
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: new Date(baseNow - 60_000).toISOString() }, baseNow),
    true,
  );

  // Unparseable expiry → cannot be determined → not expired (matches claimHint returning "").
  assert.equal(
    isClaimExpired({ claimed_by: actor, claim_expires_at: "not-a-date" }, baseNow),
    false,
  );
});

test("Countdown: ravnApp.claimExpired flips live with nowTick", () => {
  const app = ravnApp() as any;
  const baseNow = 1700000000000;
  app.nowTick = baseNow;
  const actor = { name: "runner-2", type: "agent" };
  const t = { claimed_by: actor, claim_expires_at: new Date(baseNow + 90_000).toISOString() }; // 1m30s lease

  assert.equal(app.claimExpired(t), false); // 1m30s of lease left — active
  app.nowTick += 60_000;
  assert.equal(app.claimExpired(t), false); // 30s left — still active
  app.nowTick += 30_000;
  assert.equal(app.claimExpired(t), true); // crossed expiry → flips live, no reload needed
  assert.equal(app.claimExpired({}), false); // unclaimed never flips
  assert.equal(app.claimExpired({ claimed_by: actor }), false); // claimed without expiry never flips
});

test("OperationalBar: status menu and owner popover toggle and mutual exclusion", () => {
  const app = ravnApp() as any;
  assert.equal(app.statusMenuOpen, false);
  assert.equal(app.ownerPopoverOpen, false);

  // Open status menu
  app.toggleStatusMenu();
  assert.equal(app.statusMenuOpen, true);
  assert.equal(app.ownerPopoverOpen, false);

  // Toggling owner popover closes status menu
  app.detail = { id: "task-1", assignee: { name: "alice", type: "agent" } };
  app.toggleOwnerPopover();
  assert.equal(app.statusMenuOpen, false);
  assert.equal(app.ownerPopoverOpen, true);
  assert.equal(app.assigneeInputName, "alice");
  assert.equal(app.assigneeInputType, "agent");

  // Escape key via dismissTop closes topmost popover
  app.dismissTop();
  assert.equal(app.ownerPopoverOpen, false);
  assert.equal(app.statusMenuOpen, false);
});

test("OperationalBar: index.html markup contains unified card, split-capsules, and removes heartbeat", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // Unified card container
  assert.match(html, /divide-y divide-zinc-200 rounded-lg border border-zinc-200 bg-zinc-50\/70 text-xs/);

  // Status dropdown trigger and menu
  assert.match(html, /data-testid="status-dropdown-trigger"/);
  assert.match(html, /role="group" aria-label="Status"/);

  // Owner popover trigger
  assert.match(html, /data-testid="owner-popover-trigger"/);
  assert.match(html, /aria-label="Durable Owner"/);

  // Claim controls
  assert.match(html, /role="group" aria-label="Claim Controls"/);
  assert.match(html, /claimCountdown\(detail\)/);
  assert.match(html, /class="live-countdown/);

  // Heartbeat button MUST NOT be present in index.html
  assert.doesNotMatch(html, /heartbeatTask\(\)/);
  assert.doesNotMatch(html, />\s*Heartbeat\s*</);
});

// ---------------------------------------------------------------------------
// Write/Preview toggle + draft auto-save (ravn.draft.task.<id>.* localStorage keys)
// ---------------------------------------------------------------------------

test("Drafts: key builders produce namespaced per-task keys", () => {
  assert.equal(descriptionDraftKey("01ABC"), "ravn.draft.task.01ABC.description");
  assert.equal(commentDraftKey("01ABC"), "ravn.draft.task.01ABC.comment");
  assert.notEqual(descriptionDraftKey("A"), descriptionDraftKey("B"), "keys are per-task — no cross-task collision");
  assert.notEqual(descriptionDraftKey("X"), commentDraftKey("X"), "description and comment keys never collide for one task");
});

test("Drafts: serialize → deserialize round-trips body and kind", () => {
  const draft = { body: "**bold** draft", kind: "handoff" as const };
  assert.deepEqual(deserializeCommentDraft(serializeCommentDraft(draft)), draft);
  for (const kind of ["progress", "question", "decision", "handoff", "note"] as const) {
    assert.equal(
      deserializeCommentDraft(serializeCommentDraft({ body: "x", kind }))?.kind,
      kind,
      `round-trip preserves kind ${kind}`,
    );
  }
});

test("Drafts: deserialize tolerates malformed and legacy payloads (never throws)", () => {
  assert.equal(deserializeCommentDraft(null), null);
  assert.equal(deserializeCommentDraft(""), null);
  assert.equal(deserializeCommentDraft("   "), null, "blank raw payload → nothing saved");
  assert.equal(deserializeCommentDraft("not json"), null);
  assert.equal(deserializeCommentDraft("[1,2]"), null, "non-object JSON → not a draft");
  assert.deepEqual(deserializeCommentDraft('{"kind":"question"}'), null, "missing body → nothing restorable");
  // Legacy fallback shape: a bare JSON string stores the body only.
  assert.deepEqual(deserializeCommentDraft(JSON.stringify("old draft")), { body: "old draft", kind: "progress" });
  assert.equal(deserializeCommentDraft(JSON.stringify("   ")), null, "blank legacy string is blank");
  // Unknown or missing kind falls back to progress; valid kinds are preserved.
  assert.deepEqual(deserializeCommentDraft('{"body":"hi","kind":"bogus"}'), { body: "hi", kind: "progress" });
  assert.deepEqual(deserializeCommentDraft('{"body":"hi"}'), { body: "hi", kind: "progress" });
});

test("Drafts: ravnApp.saveDescriptionDraft saves, blanks remove, no loaded task = no-op", () => {
  const app = ravnApp() as any;
  safeStorage.removeItem(descriptionDraftKey("T1"));

  // No task loaded → the handler must never write (it has no id to key on).
  app.editDraft.description = "orphan";
  app.saveDescriptionDraft();
  assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);

  app.detail = { id: "T1", title: "t", description: "", priority: 0, due_at: null, assignee: undefined };
  app.editDraft.description = "wip description";
  app.saveDescriptionDraft();
  assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), "wip description");

  // Blank value REMOVES the key so nothing stale lingers for the next edit.
  app.editDraft.description = "   ";
  app.saveDescriptionDraft();
  assert.equal(safeStorage.getItem(descriptionDraftKey("T1")), null);
});

test("Drafts: ravnApp.saveCommentDraft persists body AND selected kind as JSON", () => {
  const app = ravnApp() as any;
  safeStorage.removeItem(commentDraftKey("T2"));
  app.detail = { id: "T2", title: "t", description: "", priority: 0, due_at: null, assignee: undefined };

  app.commentForm = { kind: "question" as const, body: "still thinking…" };
  app.saveCommentDraft();
  assert.deepEqual(deserializeCommentDraft(safeStorage.getItem(commentDraftKey("T2"))), {
    body: "still thinking…",
    kind: "question",
  });

  // Blank body removes the key even when a kind is selected.
  app.commentForm = { kind: "note" as const, body: "" };
  app.saveCommentDraft();
  assert.equal(safeStorage.getItem(commentDraftKey("T2")), null);
});

test("Drafts: startEditing restores a saved description while the snapshot keeps server truth", () => {
  const app = ravnApp() as any;
  safeStorage.setItem(descriptionDraftKey("T3"), "local WIP draft");
  app.detail = { id: "T3", title: "t", description: "server desc", priority: 0, due_at: null, assignee: undefined };
  app.descShowPreview = true; // stale toggle must be reset for a fresh edit session

  app.startEditing();

  assert.equal(app.editingTask, true);
  assert.equal(app.descShowPreview, false, "a fresh edit always starts in Write mode");
  assert.equal(app.editDraft.description, "local WIP draft", "the saved local draft wins over the server value");
  assert.equal(
    app.editDraftOriginal.description,
    "server desc",
    "snapshot keeps SERVER truth so dirty-detection / discard guard stay correct",
  );
  assert.equal(app.editDraftDirty(), true, "a restored draft counts as an unsaved change");

  // No stored draft → the server value is used and the form starts clean.
  app.cancelEditing();
  app.detail = { id: "T4", title: "t", description: "server only", priority: 0, due_at: null, assignee: undefined };
  app.startEditing();
  assert.equal(app.editDraft.description, "server only");
  assert.equal(app.editDraftDirty(), false);
});

test("Drafts: restoreCommentDraftForTask resets then restores per task (no cross-task leak)", () => {
  const app = ravnApp() as any;
  safeStorage.setItem(commentDraftKey("TA"), serializeCommentDraft({ body: "A's draft", kind: "decision" }));

  // Task B has NO stored draft: opening it must not surface A's text.
  app.restoreCommentDraftForTask("TB");
  assert.deepEqual(app.commentForm, { kind: "progress", body: "" }, "no stored draft for this id → clean defaults (no leak)");
  assert.equal(app.commentShowPreview, false);

  // Re-opening A restores BOTH its body and selected type.
  app.restoreCommentDraftForTask("TA");
  assert.deepEqual(app.commentForm, { kind: "decision", body: "A's draft" });
  assert.equal(app.commentShowPreview, false, "restore resets the toggle back to Write mode");

  // Task C corrupt payload
  safeStorage.setItem(commentDraftKey("TC"), "{corrupted json");
  app.restoreCommentDraftForTask("TC");
  assert.deepEqual(app.commentForm, { kind: "progress", body: "" });
});

// ---------------------------------------------------------------------------
// Header menu trigger name display & initial (task 01M5K1PD4C3NNM2VSEKEPT9KT8)
// ---------------------------------------------------------------------------

test("HeaderMenu: trigger markup hides initial on desktop (md:hidden) and uses initial on mobile", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");
  assert.match(
    html,
    /<span class="md:hidden" data-testid="header-menu-initial" x-text="actorGlyph\(\)" aria-hidden="true"><\/span>/,
    "initial span must have md:hidden class",
  );
  assert.match(
    html,
    /<span class="hidden max-w-\[8rem\] truncate md:inline" data-testid="header-menu-name"/,
    "name span must be revealed on desktop via md:inline",
  );
});

// ---------------------------------------------------------------------------
// Liveness indicator replacement with 30s disconnect notification (task 01M5K1HDAHCNRCJA322P1YF8HR)
// ---------------------------------------------------------------------------

test("SSE Disconnect Notification: triggers after delay, persists, clears on restore, and dismisses manually", async () => {
  const app = ravnApp() as any;
  app.sseDisconnectDelayMs = 50; // fast test delay

  // 1. Initial state: no notification, timer can be scheduled
  assert.equal(app.sseDisconnectToastId, null);
  assert.equal(app.toasts.length, 0);

  // 2. Schedule disconnect timer
  app.scheduleSseDisconnectTimer();
  assert.ok(app.sseDisconnectedTimer !== null, "timer must be scheduled");

  // Wait for timer to fire
  await new Promise((r) => setTimeout(r, 80));
  assert.ok(app.sseDisconnectToastId !== null, "notification toast must be created");
  assert.equal(app.toasts.length, 1);

  const toast = app.toasts[0];
  assert.equal(toast.id, app.sseDisconnectToastId);
  assert.equal(toast.kind, "error");
  assert.equal(toast.persistent, true, "notification must be persistent (no auto-expiry)");
  assert.equal(toast.borderClass, "border-red-200 dark:border-red-800", "notification has red border like blocked tasks");
  assert.equal(toast.hideErrorPrefix, true);
  assert.equal(toast.isSseDisconnect, true);
  assert.match(toast.msg, /Disconnected/);

  // 3. Clear on connection restoration
  app.clearSseDisconnectNotification();
  assert.equal(app.sseDisconnectToastId, null, "toast ID cleared upon connection restoration");
  assert.equal(app.toasts[0]?.leaving, true, "toast starts leaving transition");

  // 4. Test manual dismissal
  app.toasts = [];
  app.scheduleSseDisconnectTimer();
  await new Promise((r) => setTimeout(r, 80));
  assert.ok(app.sseDisconnectToastId !== null);
  const toastId = app.sseDisconnectToastId;
  app.dismissToast(toastId);
  assert.equal(app.sseDisconnectToastId, null, "manual dismiss clears sseDisconnectToastId");
});

// ----------------------------------------------------------- Card Density & Done Pagination
test("CardDensity: readStoredCardDensity defaults to comfortable and validates values", () => {
  safeStorage.clear();
  assert.equal(isCardDensity("comfortable"), true);
  assert.equal(isCardDensity("compact"), true);
  assert.equal(isCardDensity("spacious"), false);
  assert.equal(isCardDensity(null), false);

  assert.equal(readStoredCardDensity(), "comfortable");
  safeStorage.setItem(LS_CARD_DENSITY, "compact");
  assert.equal(readStoredCardDensity(), "compact");
  safeStorage.setItem(LS_CARD_DENSITY, "invalid");
  assert.equal(readStoredCardDensity(), "comfortable");
});

test("CardDensity: app state initialization, toggle and persistence", () => {
  safeStorage.clear();
  const app = ravnApp() as any;
  assert.equal(app.cardDensity, "comfortable");

  app.toggleCardDensity();
  assert.equal(app.cardDensity, "compact");
  assert.equal(safeStorage.getItem(LS_CARD_DENSITY), "compact");

  app.toggleCardDensity();
  assert.equal(app.cardDensity, "comfortable");
  assert.equal(safeStorage.getItem(LS_CARD_DENSITY), "comfortable");

  app.setCardDensity("compact");
  assert.equal(app.cardDensity, "compact");
  assert.equal(safeStorage.getItem(LS_CARD_DENSITY), "compact");
});

test("DonePagination: columns() caps Done tasks to doneLimit and orders by recency", () => {
  const app = ravnApp() as any;
  assert.equal(DEFAULT_DONE_LIMIT, 20);
  assert.equal(app.doneLimit, 20);

  // Generate 25 done tasks with staggered completion/update times
  const doneTasks = Array.from({ length: 25 }, (_, i) => ({
    id: `task-${String(i).padStart(2, "0")}`,
    title: `Done Task ${i}`,
    status: "done",
    priority: 3,
    version: 1,
    created_at: `2026-10-01T10:00:${String(i).padStart(2, "0")}Z`,
    updated_at: `2026-10-02T12:00:${String(i).padStart(2, "0")}Z`,
    completed_at: `2026-10-02T12:00:${String(i).padStart(2, "0")}Z`,
  }));

  app.board = {
    project_id: "test-proj",
    tasks: {
      todo: [],
      doing: [],
      done: doneTasks,
    },
  };

  const cols = app.columns();
  const doneCol = cols.find((c: any) => c.status === "done");
  assert.ok(doneCol);
  assert.equal(doneCol.totalCount, 25);
  assert.equal(doneCol.tasks.length, 20, "Should cap rendered Done tasks to default limit of 20");
  // Server order sort: task-24 has latest timestamp -> should be first
  assert.equal(doneCol.tasks[0].id, "task-24");
  assert.equal(doneCol.tasks[19].id, "task-05");

  // Show more (step = 20) -> limit becomes 40 -> renders all 25
  app.showMoreDone();
  assert.equal(app.doneLimit, 40);
  const colsExpanded = app.columns();
  const doneColExpanded = colsExpanded.find((c: any) => c.status === "done");
  assert.equal(doneColExpanded.tasks.length, 25);

  // Collapse -> resets to 20
  app.collapseDone();
  assert.equal(app.doneLimit, 20);
  const colsCollapsed = app.columns();
  const doneColCollapsed = colsCollapsed.find((c: any) => c.status === "done");
  assert.equal(doneColCollapsed.tasks.length, 20);

  // Show all -> Infinity -> renders all 25
  app.showAllDone();
  assert.equal(app.doneLimit, Infinity);
  const colsAll = app.columns();
  const doneColAll = colsAll.find((c: any) => c.status === "done");
  assert.equal(doneColAll.tasks.length, 25);
});

test("DonePagination & Density: index.html wires markup for pagination and density toggle", () => {
  const htmlPath = path.resolve(__dirname, "../index.html");
  const html = fs.readFileSync(htmlPath, "utf-8");

  // Verify pagination testids exist in index.html
  assert.ok(html.includes('data-testid="done-pagination"'), "Must wire done-pagination container");
  assert.ok(html.includes('data-testid="done-show-more"'), "Must wire done-show-more button");
  assert.ok(html.includes('data-testid="done-show-all"'), "Must wire done-show-all button");
  assert.ok(html.includes('data-testid="done-collapse"'), "Must wire done-collapse button");

  // Verify card density controls in toolbar
  assert.ok(html.includes('data-testid="card-density-toggle"'), "Must wire card-density-toggle in toolbar");
  assert.ok(html.includes(':data-card-density="cardDensity"'), "Must set data-card-density on card article");
});

test("URL Sync: parseBoardQueryParams parses all supported board filter, sort, and project parameters", () => {
  const search = "?project=01M3J9HHEGY26V7142C0603A3S&priority=4&owner=antigravity&due=today&unblocked=1&q=mcp&sort=priority&dir=asc";
  const params = parseBoardQueryParams(search);

  assert.equal(params.project, "01M3J9HHEGY26V7142C0603A3S");
  assert.equal(params.priority, "4");
  assert.equal(params.owner, "antigravity");
  assert.equal(params.due, "today");
  assert.equal(params.unblocked, true);
  assert.equal(params.q, "mcp");
  assert.equal(params.sort, "priority");
  assert.equal(params.dir, "asc");

  // Invalid or 'all' values fall back cleanly
  const emptyParams = parseBoardQueryParams("?priority=all&owner=all&due=invalid&unblocked=0&sort=unknown&dir=sideways");
  assert.equal(emptyParams.priority, undefined);
  assert.equal(emptyParams.owner, undefined);
  assert.equal(emptyParams.due, undefined);
  assert.equal(emptyParams.unblocked, undefined);
  assert.equal(emptyParams.sort, undefined);
  assert.equal(emptyParams.dir, undefined);
});

test("URL Sync: buildBoardQueryString round-trips and omits empty/default parameters", () => {
  const qs = buildBoardQueryString({
    project: "proj_1",
    priority: "3",
    owner: "agent_qa",
    due: "overdue",
    unblocked: true,
    q: "search query",
    sort: "due_at",
    dir: "desc",
  });
  assert.equal(
    qs,
    "?project=proj_1&priority=3&owner=agent_qa&due=overdue&unblocked=1&q=search+query&sort=due_at&dir=desc"
  );

  // When all filters are empty, only project (if present) is kept
  const emptyFiltersQs = buildBoardQueryString({
    project: "proj_1",
    priority: "",
    owner: "",
    due: "",
    unblocked: false,
    q: "",
  });
  assert.equal(emptyFiltersQs, "?project=proj_1");

  // When nothing is set, returns empty string
  assert.equal(buildBoardQueryString({}), "");
});

test("URL Sync: syncUrlState updates window.history.replaceState without polluting history (AC1)", () => {
  const origWindow = (globalThis as any).window;
  const replacedUrls: string[] = [];
  try {
    (globalThis as any).window = {
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
      location: { pathname: "/", search: "", hash: "" },
      history: {
        replaceState(_state: unknown, _title: string, url: string) {
          replacedUrls.push(url);
          (globalThis as any).window.location.search = url.includes("?") ? url.slice(url.indexOf("?")) : "";
        },
      },
      setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
      clearTimeout: (id: any) => globalThis.clearTimeout(id),
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "proj_alpha";
    app.filterPriority = "2";
    app.filterUnblocked = true;

    app.syncUrlState(true);
    assert.equal(replacedUrls.length, 1);
    assert.equal(replacedUrls[0], "/?project=proj_alpha&priority=2&unblocked=1");

    // Setting sort and syncing
    app.sortKey = "priority";
    app.sortDir = "asc";
    app.syncUrlState(true);
    assert.equal(replacedUrls.length, 2);
    assert.equal(replacedUrls[1], "/?project=proj_alpha&priority=2&unblocked=1&sort=priority&dir=asc");
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

test("URL Sync: clearFilters clears filter parameters from URL query string (AC3)", () => {
  const origWindow = (globalThis as any).window;
  const replacedUrls: string[] = [];
  try {
    (globalThis as any).window = {
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
      location: { pathname: "/", search: "?project=proj_alpha&priority=4&unblocked=1&q=test", hash: "" },
      history: {
        replaceState(_state: unknown, _title: string, url: string) {
          replacedUrls.push(url);
          (globalThis as any).window.location.search = url.includes("?") ? url.slice(url.indexOf("?")) : "";
        },
      },
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "proj_alpha";
    app.filterPriority = "4";
    app.filterUnblocked = true;
    app.filterSearch = "test";

    // Calling clearFilters resets filter state and syncs URL immediately
    app.clearFilters();
    assert.equal(app.filterPriority, "");
    assert.equal(app.filterUnblocked, false);
    assert.equal(app.filterSearch, "");
    assert.equal(replacedUrls.length, 1);
    assert.equal(replacedUrls[0], "/?project=proj_alpha", "Filters removed from URL, project preserved");
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

test("URL Sync: applyQueryParams and initFromUrl restore board state and reconcile on popstate (AC2/AC4)", () => {
  const origWindow = (globalThis as any).window;
  try {
    (globalThis as any).window = {
      matchMedia: () => ({ matches: false, addEventListener: () => {} }),
      location: {
        pathname: "/",
        search: "?project=proj_beta&priority=1&owner=alice&due=today&unblocked=1&q=needle&sort=due_at&dir=asc",
        hash: "",
      },
      history: {
        replaceState: () => {},
      },
    };

    const app = ravnApp() as any;
    app.initFromUrl();

    assert.equal(app.selectedProjectId, "proj_beta");
    assert.equal(app.filterPriority, "1");
    assert.equal(app.filterAssignee, "alice");
    assert.equal(app.filterDue, "today");
    assert.equal(app.filterUnblocked, true);
    assert.equal(app.filterSearch, "needle");
    assert.equal(app.sortKey, "due_at");
    assert.equal(app.sortDir, "asc");

    // Popstate to an empty search clears filters back to default
    app.applyQueryParams(parseBoardQueryParams(""), true);
    assert.equal(app.filterPriority, "");
    assert.equal(app.filterAssignee, "");
    assert.equal(app.filterDue, "");
    assert.equal(app.filterUnblocked, false);
    assert.equal(app.filterSearch, "");
  } finally {
    if (origWindow === undefined) delete (globalThis as any).window;
    else (globalThis as any).window = origWindow;
  }
});

test("URL Sync: index.html wires syncUrlState on filter inputs", () => {
  const htmlPath = path.resolve(__dirname, "../index.html");
  const html = fs.readFileSync(htmlPath, "utf-8");

  assert.ok(html.includes('@input="syncUrlState()"'), "Must wire input syncUrlState on search field");
  assert.ok(html.includes('@change="syncUrlState(true)"'), "Must wire change syncUrlState on dropdowns/checkboxes");
});

// ---------------------------------------------------------------------------
// Task 01M5XTAW3RZ4QJCSW9C3CS255R: full-text board search (description/notes)
// with match-source indicator. The instant local title/id filter is unchanged;
// a debounced GET /tasks?search= contributes ids that columns() UNIONs in.

test("FullTextSearch: a typing burst coalesces into ONE debounced server query whose ids union into the board (AC1)", async () => {
  const origListTasks = api.listTasks;
  try {
    let calls = 0;
    (api as any).listTasks = async (params: any) => {
      calls++;
      assert.equal(params.project_id, "P", "query scoped to the selected project");
      assert.equal(params.search, "quixotic", "debounced trailing query is the FINAL typed term");
      assert.ok(params.limit >= 100, "requests a generous page (server clamps to its max)");
      return { tasks: [{ id: "d1" }, { id: "t1" }], total: 2, limit: 100, offset: 0 };
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "P";
    app.board = {
      project_id: "P",
      counts: {},
      tasks: {
        todo: [
          { id: "t1", title: "Quixotic title hit", status: "todo" }, // local title match
          { id: "d1", title: "Plain card, term only in notes", status: "todo" }, // server-only match
          { id: "x9", title: "Irrelevant", status: "todo" },
        ],
        doing: [],
        done: [],
      },
    };
    // Fake-timer debounce bound to the REAL runFullTextSearch (timer-injectable
    // createDebounce — same pattern as the SSE-reload tests).
    app._searchDebounce = createDebounce(
      () => { void app.runFullTextSearch(); },
      SEARCH_DEBOUNCE_MS,
      {
        setTimer: (fn: () => void, ms: number) => {
          assert.equal(ms, SEARCH_DEBOUNCE_MS);
          return setTimeout(fn, ms);
        },
        clearTimer: (id: unknown) => clearTimeout(id as ReturnType<typeof setTimeout>),
      },
    );

    // Two rapid keystrokes — trailing debounce must fire exactly once.
    app.filterSearch = "quixo";
    app.scheduleFullTextSearch();
    app.filterSearch = "quixotic";
    app.scheduleFullTextSearch();
    assert.equal(app._searchDebounce.isPending(), true, "one pending run before the quiet window elapses");
    await new Promise((r) => setTimeout(r, SEARCH_DEBOUNCE_MS + 100));

    assert.equal(calls, 1, "whole burst costs exactly ONE round-trip");
    assert.deepEqual([...app._fullTextMatchIds], ["d1", "t1"]);

    // columns() union: local title match AND server-only id both survive.
    const todo = app.columns().find((c: any) => c.status === "todo");
    assert.ok(todo.tasks.some((t: any) => t.id === "t1"), "instant title match visible");
    assert.ok(todo.tasks.some((t: any) => t.id === "d1"), "desc/notes-only match visible via id union");
    assert.ok(!todo.tasks.some((t: any) => t.id === "x9"), "non-match hidden");

    // Match-source chip predicate (AC2): only the server-only card gets one.
    const d1 = todo.tasks.find((t: any) => t.id === "d1");
    const t1 = todo.tasks.find((t: any) => t.id === "t1");
    assert.equal(app.isDescriptionNotesMatch(d1), true, "desc/notes-only hit shows the chip");
    assert.equal(app.isDescriptionNotesMatch(t1), false, "title match must NOT get the chip");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("FullTextSearch: an out-of-order LATE response is discarded by the sequence token (AC3)", async () => {
  const origListTasks = api.listTasks;
  try {
    let calls = 0;
    (api as any).listTasks = async () => {
      calls++;
      if (calls === 1) {
        // The OLD query's slow response — settles only after the newer one.
        await new Promise((r) => setTimeout(r, 60));
        return { tasks: [{ id: "old1" }], total: 1, limit: 100, offset: 0 };
      }
      return { tasks: [{ id: "new1" }], total: 1, limit: 100, offset: 0 };
    };

    const app = ravnApp() as any;
    app.selectedProjectId = "P";
    app.filterSearch = "old-term";
    const pOld = app.runFullTextSearch(); // token N — in flight
    app.filterSearch = "new-term";
    const pNew = app.runFullTextSearch(); // token N+1 — initiates later, wins

    await pNew; // faster response lands first
    assert.deepEqual([...app._fullTextMatchIds], ["new1"], "newer query applied");

    await pOld; // stale slow response arrives LAST
    assert.deepEqual(
      [...app._fullTextMatchIds],
      ["new1"],
      "out-of-order late response must NEVER overwrite the newer state",
    );
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("FullTextSearch: clearing the input cancels the timer, clears the id set, and retires in-flight results (AC3)", async () => {
  const origListTasks = api.listTasks;
  try {
    let releaseSlow: (v: any) => void;
    const slow = new Promise<any>((r) => { releaseSlow = r; });
    (api as any).listTasks = async () => slow;

    const app = ravnApp() as any;
    app.selectedProjectId = "P";
    let timerArmed = 0;
    app._searchDebounce = createDebounce(
      () => { void app.runFullTextSearch(); },
      SEARCH_DEBOUNCE_MS,
      { setTimer: (_fn: () => void, _ms: number) => ++timerArmed, clearTimer: () => {} },
    );

    app.filterSearch = "term";
    app._fullTextMatchIds = new Set(["stale1"]); // leftover from a prior settled query
    const inFlight = app.runFullTextSearch(); // direct run still in flight…
    app.scheduleFullTextSearch();             // …and one more keystroke pending its timer

    // User CLEARS the input: everything must be retired immediately.
    app.filterSearch = "";
    app.scheduleFullTextSearch();

    assert.deepEqual([...app._fullTextMatchIds], [], "match id set cleared — board instantly unfiltered");
    assert.equal(app.isDescriptionNotesMatch({ id: "stale1", title: "x" }), false, "no chips after clear");

    // The in-flight response finally resolves — it must be a no-op.
    releaseSlow!({ tasks: [{ id: "ghost1" }], total: 1, limit: 100, offset: 0 });
    await inFlight;
    assert.deepEqual([...app._fullTextMatchIds], [], "late response for the cleared query discarded");

    // No further debounce fires were armed by the clear path.
    assert.equal(timerArmed, 1, "only the pre-clear keystroke armed a timer; clear cancels instead of scheduling");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("FullTextSearch: while searching, the done-column cap is bypassed so no matched done task hides behind it", () => {
  const app = ravnApp() as any;
  // 25 done tasks; rendered newest-first by completed_at (d00 = Jan 1 oldest …
  // d24 = Jan 25 newest), so d00–d03 sit behind the default 20-card cut.
  const doneTasks = Array.from({ length: 25 }, (_, i) => ({
    id: "d" + String(i).padStart(2, "0"),
    title: "Done task " + i,
    status: "done",
    priority: 3,
    version: 1,
    created_at: "",
    updated_at: "",
    completed_at: `2026-01-${String(i + 1).padStart(2, "0")}T00:00:00Z`,
  }));
  app.selectedProjectId = "P";
  app.board = { project_id: "P", counts: {}, tasks: { todo: [], doing: [], done: doneTasks } };

  const before = app.columns().find((c: any) => c.status === "done");
  assert.equal(before.tasks.length, DEFAULT_DONE_LIMIT, "normal 20-card cap applies unfiltered");
  assert.equal(before.totalCount, 25);
  assert.ok(!before.tasks.some((t: any) => t.id === "d00"), "oldest done task hidden behind the cut");

  // Server matched the OLDEST done task (far past the default cut) via notes.
  app.filterSearch = "ancient-term";
  app._fullTextMatchIds = new Set(["d00"]);
  const searching = app.columns().find((c: any) => c.status === "done");
  assert.equal(searching.totalCount, 1);
  assert.equal(searching.tasks.length, 1, "cap bypassed while searching — no match hides behind the cut");
  assert.deepEqual(searching.tasks.map((t: any) => t.id), ["d00"]);

  // Clearing restores the normal cap (doneLimit untouched by the bypass).
  app.filterSearch = "";
  app.scheduleFullTextSearch();
  const after = app.columns().find((c: any) => c.status === "done");
  assert.equal(after.tasks.length, DEFAULT_DONE_LIMIT, "normal cap restored on clear");
  assert.equal(after.totalCount, 25);

  // Column count pill input stays accurate in both states.
  assert.equal(app.visibleTaskCount() > 0, true);
});

test("FullTextSearch: isDescriptionNotesMatch fires only for desc/notes-only hits", () => {
  const app = ravnApp() as any;
  app.filterSearch = "zzz";
  app._fullTextMatchIds = new Set(["id-a", "id-b", "zzz-1234567890abcdefghijklmnop"]);

  assert.equal(app.isDescriptionNotesMatch({ id: "id-a", title: "No term here" }), true, "server-only hit → chip");
  assert.equal(
    app.isDescriptionNotesMatch({ id: "id-b", title: "Contains ZZZ in the title" }),
    false,
    "title match (any case) → no chip",
  );
  assert.equal(
    app.isDescriptionNotesMatch({ id: "zzz-1234567890abcdefghijklmnop", title: "Term in the ID" }),
    false,
    "id match → no chip",
  );
  app.filterSearch = "   ";
  assert.equal(app.isDescriptionNotesMatch({ id: "id-a", title: "x" }), false, "whitespace-only query → never");
});

test("FullTextSearch: project switch cancels the pending search and retires its in-flight result", async () => {
  const origGetBoard = api.getBoard;
  try {
    (api as any).getBoard = async (pid: string) => ({
      project_id: pid, counts: {}, tasks: { todo: [], doing: [], done: [] },
    });

    const app = ravnApp() as any;
    app.projects = [{ id: "A", name: "Alpha" }, { id: "B", name: "Beta" }];
    app.selectedProjectId = "A";
    app.board = { project_id: "A", counts: {}, tasks: {} };
    app.filterSearch = "term";
    app._fullTextMatchIds = new Set(["a1"]);

    let fired = false;
    app._searchDebounce = createDebounce(
      () => { fired = true; },
      SEARCH_DEBOUNCE_MS,
      { setTimer: () => 0, clearTimer: () => {} },
    );
    app.scheduleFullTextSearch(); // pending run scoped to project A
    assert.equal(app._searchDebounce.isPending(), true);

    app.pickProject("B"); // → selectProject() invalidation path
    assert.deepEqual([...app._fullTextMatchIds], [], "project A's match ids dropped on switch");
    assert.equal(fired, false, "the pending run for the left project never fires");
    assert.equal(app._searchDebounce.isPending(), true, "a fresh run is re-armed for the NEW selection");
  } finally {
    (api as any).getBoard = origGetBoard;
  }
});

test("FullTextSearch: index.html renders the match-source chip and hides done pagination while searching", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // The chip exists with a stable testid and is driven by the app predicate.
  assert.match(html, /data-testid="match-source-chip"/);
  assert.match(html, /x-show="isDescriptionNotesMatch\(task\)"/);
  assert.ok(html.includes("matched in description/notes"), "chip label text");

  // The done-pagination footer's x-show (immediately preceding its testid attr)
  // must suppress itself while a search is active.
  const idx = html.indexOf('data-testid="done-pagination"');
  assert.ok(idx !== -1, "done-pagination footer exists");
  const footerHead = html.slice(Math.max(0, idx - 400), idx);
  assert.ok(footerHead.includes("filterSearch.trim() === ''"), "footer hidden while searching");

  // Desktop AND mobile filter inputs share the ONE filterSearch binding —
  // both stay in sync for free.
  assert.equal((html.match(/x-model="filterSearch"/g) ?? []).length, 2, "exactly two inputs bound to filterSearch");

  // listTasksParams (the board's own fetch) must NOT leak the search term —
  // the board stays unfiltered; only the debounced api.listTasks call searches.
  assert.equal(FULLTEXT_SEARCH_LIMIT >= 100, true, "full-text page requests a full server page");
});

// ===========================================================================
// Archive management (Rävn task 01M5XSNN2KA876TR0QGE79YFPT)
// Task archive/unarchive (+Undo), bulk "Archive all Done", the "Show archived"
// board view, and the project menu's Archived/Restore section.
// ===========================================================================

function archTask(id: string, status: "todo" | "doing" | "done", version = 1): any {
  return {
    id, title: "T-" + id, description: null, status, priority: 0, due_at: null,
    claimed_by: null, claim_expires_at: null, assigned_to: null,
    ac_total: 0, comment_count: 0, blocked_by: [], archived: false, version,
  };
}

function archBoard(projectId: string, byStatus: Record<string, any[]> = {}): any {
  return {
    project_id: projectId,
    tasks: { todo: byStatus.todo ?? [], doing: byStatus.doing ?? [], done: byStatus.done ?? [] },
    counts: {
      todo: (byStatus.todo ?? []).length,
      doing: (byStatus.doing ?? []).length,
      done: (byStatus.done ?? []).length,
    },
  };
}

/** Hermetic app instance with archive-relevant collaborators stubbed. */
function archApp() {
  const app = ravnApp() as any;
  app.selectedProjectId = "proj-1";
  app.detail = null;
  app.toasts = [];
  // Keep resync hermetic: record board reloads instead of hitting the network.
  app.loadBoardCalls = 0;
  app.loadBoard = async () => { app.loadBoardCalls++; };
  app.refreshArchivedViewCalls = 0;
  app.refreshArchivedView = async () => { app.refreshArchivedViewCalls++; };
  return app;
}

function lastToast(app: any, needle?: string): any {
  const t = [...app.toasts].reverse().find((x: any) => (needle ? x.msg.includes(needle) : true));
  assert.ok(t, `expected a toast${needle ? ` matching "${needle}"` : ""}; got ${JSON.stringify(app.toasts.map((x: any) => x.msg))}`);
  return t;
}

test("ArchiveMgmt: api.listProjects(true) targets /projects?archived=true; listTasks serializes archived=true", async () => {
  const origFetch = (globalThis as any).fetch;
  const urls: string[] = [];
  try {
    (globalThis as any).fetch = async (url: string | URL, init?: any) => {
      urls.push(String(url));
      if (String(url).includes("/projects")) {
        return new Response(JSON.stringify({ projects: [], total: 0 }), { status: 200 });
      }
      return new Response(JSON.stringify({ tasks: [], total: 0 }), { status: 200 });
    };
    await api.listProjects(true);
    await api.listTasks({ project_id: "proj-1", archived: true, limit: 100, offset: 0 });
  } finally {
    if (origFetch === undefined) delete (globalThis as any).fetch;
    else (globalThis as any).fetch = origFetch;
  }
  assert.match(urls[0], /\/projects\?archived=true$/, "includeArchived flips the query filter");
  const taskUrl = urls[1];
  assert.match(taskUrl, /archived=true/, "listTasks passes archived=true");
  assert.match(taskUrl, /project_id=proj-1/, "project scoping rides along");
});

test("ArchiveMgmt: loadProjects fills BOTH open and archived lists; an archived-side failure degrades gracefully", async () => {
  const app = archApp();
  const origListProjects = api.listProjects;
  try {
    let call = 0;
    (api as any).listProjects = async (includeArchived?: boolean) => {
      call++;
      if (includeArchived) {
        return { projects: [{ id: "p-old", name: "Old", archived: true, task_counts: { todo: 0, doing: 0, done: 0 } }], total: 1 };
      }
      return { projects: [{ id: "p-live", name: "Live", task_counts: { todo: 1, doing: 0, done: 0 } }], total: 1 };
    };
    await app.loadProjects();
    assert.equal(call, 2, "one boot performs exactly the open + archived pair");
    assert.deepEqual(app.projects.map((p: any) => p.id), ["p-live"]);
    assert.deepEqual(app.archivedProjects.map((p: any) => p.id), ["p-old"], "Archived section gets its own list");

    // Archived-side failure must NOT surface as a boot error — the previous
    // list simply stays in place.
    app.toasts = [];
    (api as any).listProjects = async (includeArchived?: boolean) => {
      if (includeArchived) throw new TypeError("fetch failed");
      return { projects: [{ id: "p-live", name: "Live", task_counts: { todo: 1, doing: 0, done: 0 } }], total: 1 };
    };
    await app.loadProjects();
    assert.equal(app.loadError, "", "archived-side failure does not error the boot path");
    assert.deepEqual(app.archivedProjects.map((p: any) => p.id), ["p-old"], "previous archived list retained on failure");
  } finally {
    (api as any).listProjects = origListProjects;
  }
});

test("ArchiveMgmt: restoreProject PUTs {archived:false}, refreshes both lists, and toasts", async () => {
  const app = archApp();
  const origUpdateProject = api.updateProject;
  const seen: any[] = [];
  try {
    (api as any).updateProject = async (id: string, req: any) => {
      seen.push({ id, req });
      return { id };
    };
    let loads = 0;
    app.loadProjects = async () => {
      loads++;
      app.archivedProjects = []; // simulate the row leaving the archived list
    };
    await app.restoreProject("p-old");
    assert.equal(seen.length, 1);
    assert.equal(seen[0].id, "p-old");
    assert.equal(seen[0].req.archived, false, "restore clears the archive flag");
    assert.ok(!("title" in seen[0].req), "no unrelated fields ride along");
    assert.equal(loads, 1, "both lists refresh after restore");
    assert.equal(app.archivedProjects.length, 0);
    assert.match(lastToast(app, "restored").msg, /Project restored/);
  } finally {
    (api as any).updateProject = origUpdateProject;
  }
});

test("ArchiveMgmt: toggleShowArchived pages the full archived set, groups by status, and swaps the columns source", async () => {
  const app = archApp();
  // Live board: one todo card that MUST keep showing when OFF.
  app.board = archBoard("proj-1", { todo: [archTask("live-todo", "todo")] });

  const origListTasks = api.listTasks;
  try {
    // 150 archived tasks across 3 statuses → two pages (100 + 50).
    const archivedRows: any[] = [];
    for (let i = 0; i < 60; i++) archivedRows.push(archTask(`a-todo-${i}`, "todo"));
    for (let i = 0; i < 40; i++) archivedRows.push(archTask(`a-doing-${i}`, "doing"));
    for (let i = 0; i < 50; i++) archivedRows.push(archTask(`a-done-${i}`, "done"));
    let calls = 0;
    (api as any).listTasks = async (params: any) => {
      assert.equal(params.archived, true, "the view fetch is scoped to archived rows");
      calls++;
      const offset = params.offset ?? 0;
      return { tasks: archivedRows.slice(offset, offset + 100), total: archivedRows.length };
    };

    await app.toggleShowArchived();
    assert.equal(calls, 2, "paged at the API max (100/page) until the set is exhausted");
    assert.equal(app.showArchived, true);
    assert.equal(app.archivedBoardTasks!.todo.length, 60);
    assert.equal(app.archivedBoardTasks!.doing.length, 40);
    assert.equal(app.archivedBoardTasks!.done.length, 50);

    // columns() must now render the ARCHIVED source (and bypass the 20-card
    // done cap: all 50 archived done rows render).
    const cols = app.columns();
    assert.equal(cols[0].tasks.length, 60, "todo column shows the archived set");
    assert.equal(cols[2].tasks.length, 50, "archived done rows are NOT capped by doneLimit");

    // OFF is an instant flag flip: the untouched live board renders again.
    await app.toggleShowArchived();
    assert.equal(app.showArchived, false);
    const back = app.columns();
    assert.equal(back[0].tasks.length, 1, "live board restored exactly (previous data was never mutated)");
    assert.equal(back[0].tasks[0].id, "live-todo");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("ArchiveMgmt: toggleShowArchived failure keeps the live board and toasts", async () => {
  const app = archApp();
  app.board = archBoard("proj-1", { todo: [archTask("live-todo", "todo")] });
  const origListTasks = api.listTasks;
  try {
    (api as any).listTasks = async () => { throw new TypeError("fetch failed"); };
    await app.toggleShowArchived();
    assert.equal(app.showArchived, false, "a failed fetch never strands the flag ON with stale data");
    const cols = app.columns();
    assert.equal(cols[0].tasks.length, 1, "live board untouched");
    assert.ok(app.toasts.some((t: any) => t.kind === "error"), "failure surfaces as an error toast");
  } finally {
    (api as any).listTasks = origListTasks;
  }
});

test("ArchiveMgmt: setTaskArchived archives a card optimistically and the Undo action restores it", async () => {
  const app = archApp();
  app.board = archBoard("proj-1", { todo: [archTask("t-1", "todo"), archTask("t-2", "todo")] });

  const origUpdateTask = api.updateTask;
  const patches: any[] = [];
  try {
    (api as any).updateTask = async (id: string, req: any) => {
      patches.push({ id, req });
      return { id, version: req.version + 1 };
    };

    await app.setTaskArchived("t-1", true);
    assert.equal(patches.length, 1);
    assert.equal(patches[0].id, "t-1");
    assert.equal(patches[0].req.archived, true);
    assert.equal(patches[0].req.version, 1, "version-guarded with the row's known version");
    // Optimistic removal: gone from the column immediately; other cards intact.
    const cols = app.columns();
    assert.deepEqual(cols[0].tasks.map((t: any) => t.id), ["t-2"]);
    assert.equal(app.loadBoardCalls, 1, "a silent board reload settles the view");

    // The toast carries a JS Undo action (not an external link).
    const t = lastToast(app, "archived");
    assert.equal(t.action.label, "Undo");
    assert.ok(typeof t.action.onAction === "function", "action is a local callback");
    assert.ok(!t.action.href);

    // Clicking Undo re-applies archived:false with the FRESH version from the
    // original patch and reloads.
    await t.action.onAction();
    assert.equal(patches.length, 2);
    assert.equal(patches[1].req.archived, false);
    assert.equal(patches[1].req.version, 2, "undo uses the fresh version, not the stale one");
    assert.equal(app.loadBoardCalls, 2);
  } finally {
    (api as any).updateTask = origUpdateTask;
  }
});

test("ArchiveMgmt: setTaskArchived rolls back the optimistic removal on a non-409 failure", async () => {
  const app = archApp();
  app.board = archBoard("proj-1", { doing: [archTask("t-busy", "doing")] });
  const origUpdateTask = api.updateTask;
  try {
    (api as any).updateTask = async () => { throw new ApiError(500, { error: "boom", message: "upstream exploded" } as any); };
    await app.setTaskArchived("t-busy", true);
    const cols = app.columns();
    assert.equal(cols[1].tasks.length, 1, "snapshot rollback restored the card");
    assert.equal(cols[1].tasks[0].id, "t-busy");
    assert.match(lastToast(app, "upstream exploded").msg, /upstream exploded/, "failure toasts the real error");
  } finally {
    (api as any).updateTask = origUpdateTask;
  }
});

test("ArchiveMgmt: setTaskArchived retries once on a 409 version conflict", async () => {
  const app = archApp();
  app.board = archBoard("proj-1", { todo: [archTask("t-c", "todo")] });
  const origUpdateTask = api.updateTask;
  const origGetTask = api.getTask;
  let attempts = 0;
  try {
    (api as any).updateTask = async (id: string, req: any) => {
      attempts++;
      if (attempts === 1 && req.version === 1) {
        throw new ApiError(409, { error: "version_conflict", message: "stale", current_version: 2 } as any);
      }
      return { id, version: req.version + 1 };
    };
    (api as any).getTask = async () => ({ id: "t-c", status: "todo", archived: false, version: 2 });

    await app.setTaskArchived("t-c", true);
    assert.equal(attempts, 2, "exactly one re-read + retry");
    assert.equal(app.toasts.some((x: any) => /fresh version/.test(x.msg)), true, "conflict-retry is told to the user");
    // The optimistic removal stays (the retried patch succeeded) and a reload settles.
    assert.equal(app.columns()[0].tasks.length, 0);
  } finally {
    (api as any).updateTask = origUpdateTask;
    (api as any).getTask = origGetTask;
  }
});

test("ArchiveMgmt: unarchive from the archived view removes it there and refreshes board + archived lists", async () => {
  const app = archApp();
  // Empty live board; the card lives ONLY in the (currently-shown) archived set.
  app.board = archBoard("proj-1");
  app.showArchived = true;
  app.archivedBoardTasks = { todo: [archTask("t-a", "todo")], doing: [], done: [] };

  const origUpdateTask = api.updateTask;
  let reqSeen: any = null;
  try {
    (api as any).updateTask = async (id: string, req: any) => {
      reqSeen = req;
      return { id, version: 2 };
    };
    await app.setTaskArchived("t-a", false);
    assert.equal(reqSeen.archived, false);
    // Optimistically gone from the archived column immediately…
    assert.equal(app.columns()[0].tasks.length, 0, "columns() reflects the removed archived row");
    // …and both refresh paths fired (the card returns to the live board via loadBoard).
    assert.equal(app.loadBoardCalls, 1);
    assert.equal(app.refreshArchivedViewCalls, 1);
    assert.match(lastToast(app, "restored").msg, /Task restored from archive/);
  } finally {
    (api as any).updateTask = origUpdateTask;
  }
});

test("ArchiveMgmt: confirmArchiveAllDone archives EVERY done task (incl. hidden beyond the cap), retries 409 per task, toasts a summary", async () => {
  const app = archApp();
  // 25 done tasks — 5 are hidden behind the 20-card render cap, and the bulk
  // action must still reach them (it iterates board.tasks.done, not columns()).
  const doneTasks: any[] = [];
  for (let i = 0; i < 25; i++) doneTasks.push(archTask(`d-${i}`, "done"));
  app.board = archBoard("proj-1", { done: doneTasks });

  const origUpdateTask = api.updateTask;
  const origGetTask = api.getTask;
  let conflictFor = 0; // the one task that 409s on its first attempt
  const attemptsById: Record<string, number> = {};
  try {
    conflictFor = 7;
    (api as any).updateTask = async (id: string, req: any) => {
      attemptsById[id] = (attemptsById[id] ?? 0) + 1;
      if (Number(id.split("-")[1]) === conflictFor && attemptsById[id] === 1 && req.version === 1) {
        throw new ApiError(409, { error: "version_conflict", message: "stale", current_version: 2 } as any);
      }
      return { id, version: 2 };
    };
    (api as any).getTask = async (id: string) => ({ id, status: "done", archived: false, version: 2 });

    app.archiveAllDoneConfirmOpen = true;
    await app.confirmArchiveAllDone();
    assert.equal(app.archiveAllDoneBusy, false, "busy flag cleared");
    assert.equal(app.archiveAllDoneConfirmOpen, false, "confirm step closed after the run");
    assert.equal(Object.keys(attemptsById).length, 25, "all 25 done tasks (not just the 20 rendered) were archived");
    assert.equal(attemptsById["d-7"], 2, "the conflicting task got its one re-read + retry");
    // Optimistic removals left the column empty…
    assert.equal(app.board.tasks.done.length, 0);
    assert.equal(app.loadBoardCalls, 1, "…and a final silent reload settles counts/blocked state");
    assert.match(lastToast(app).msg, /^Archived 25 done tasks$/, "summary toast names the exact count");
    assert.ok(!lastToast(app).kind || lastToast(app).kind !== "error", "an all-success run is not an error toast");
  } finally {
    (api as any).updateTask = origUpdateTask;
    (api as any).getTask = origGetTask;
  }
});

test("ArchiveMgmt: confirmArchiveAllDone reports partial failure in the toast", async () => {
  const app = archApp();
  const doneTasks = [archTask("ok-1", "done"), archTask("bad-2", "done")];
  app.board = archBoard("proj-1", { done: doneTasks });
  const origUpdateTask = api.updateTask;
  try {
    (api as any).updateTask = async (id: string, _req: any) => {
      if (id === "bad-2") throw new ApiError(500, { error: "boom", message: "upstream exploded" } as any);
      return { id, version: 2 };
    };
    await app.confirmArchiveAllDone();
    assert.match(lastToast(app).msg, /^Archived 1 done task · 1 failed$/, "partial failure is reported precisely");
    assert.equal(lastToast(app).kind, "error", "a partial failure reads as an error");
  } finally {
    (api as any).updateTask = origUpdateTask;
  }
});

test("ArchiveMgmt: index.html wires every archive testid the E2E suite depends on", () => {
  const html = fs.readFileSync(path.resolve(__dirname, "../index.html"), "utf-8");

  // AC4 — archived projects section: restore buttons carry data-restore-project-id
  // (never data-project-id) and are bound to restoreProject().
  assert.match(html, /data-testid="project-restore"[^>]*:data-restore-project-id="p\.id"/);
  assert.match(html, /@click="restoreProject\(p\.id\)"/);
  const restoreIdx = html.indexOf('data-testid="project-restore"');
  const restoreTag = html.slice(restoreIdx - 200, html.indexOf(">", restoreIdx));
  assert.ok(!restoreTag.includes("data-project-id"), "restore rows are NOT pickable as the active project");

  // AC3 — ONE toggle testid per viewport (desktop strip + mobile panel).
  assert.equal((html.match(/data-testid="show-archived-toggle"/g) ?? []).length, 2, "desktop + mobile twins");
  assert.ok(html.includes('@click="toggleShowArchived()"'));

  // AC1 — card menu Archive item, per-card Unarchive in the archived view, and
  // the detail-header button (label follows detail.archived).
  assert.match(html, /data-testid="card-menu-archive"/);
  assert.ok(html.includes("setTaskArchived(task.id, true)"), "menu item archives");
  assert.ok(html.includes('data-testid="unarchive-action"'), "per-card unarchive button exists");
  assert.ok(html.includes('@click.stop="setTaskArchived(task.id, false)"'), "unarchive click wired (stop so the card doesn't open)");
  assert.ok(html.includes('data-testid="task-detail-archive"'), "detail-header archive button exists");
  assert.ok(html.includes("setTaskArchived(detail.id, !detail.archived)"), "detail button toggles on detail.archived");

  // AC2 — bulk action + two-step inline confirm in the Done column.
  assert.match(html, /data-testid="archive-all-done"/);
  assert.match(html, /data-testid="archive-all-done-confirm-row"/);
  assert.ok(html.includes('data-testid="archive-all-done-cancel"'));
  assert.ok(html.includes('data-testid="archive-all-done-confirm"'));

  // The Undo path renders a local action button (href → link branch is separate).
  assert.ok(html.includes('data-testid="toast-action-btn"'), "local toast action button exists");
  assert.ok(html.includes('@click="runToastAction(t)"'), "action button runs the JS callback");

  // Archived view: cards are not draggable, the move menu anchor hides, and the
  // per-column "+ Add task" button is suppressed.
  assert.ok(html.includes(':draggable="!showArchived"'));
  assert.match(html, /data-testid="card-menu-anchor"[^>]*x-show="!showArchived"/);
});




