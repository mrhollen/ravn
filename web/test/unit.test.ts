import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { safeStorage } from "../src/util.ts";
import { escapeHtml, renderMarkdown } from "../src/md.ts";
import { crowbanApp } from "../src/app.ts";

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
