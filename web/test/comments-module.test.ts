// Unit tests for src/modules/comments.ts — the comments drawer section
// extracted from web/src/app.ts (Phase 2 batch C). Covers: draft
// reset-then-restore on open; debounced draft persistence (persist vs remove);
// older-comment pagination math + load flow (offset, total ratchet, same-task
// merge guard, error toast); and postComment (guards, success fan-out with
// draft-key removal, same-task guard, error path).
//
// The module is pure: a mutable stub state object wired into the CommentsDeps
// accessors + counters for every out-bound write + monkey-patched api/safeStorage
// methods, all restored in finally blocks (same pattern as acs-module.test.ts).

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createCommentsModule } from "../src/modules/comments";
import { api } from "../src/api";
import { safeStorage } from "../src/util";
import { commentDraftKey } from "../src/drafts";
import type { ActorInput, CommentRow, TaskDetail } from "../src/types";

// ----------------------------------------------------------------------- helpers
function makeComment(id: string, createdAt = "2026-01-02T00:00:00Z"): CommentRow {
  return { id, kind: "progress", body: `body ${id}`, actor: { name: "u", type: "human" }, created_at: createdAt };
}

function makeTask(over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: "T1",
    project_id: "P1",
    seq: 1,
    title: "Server truth title",
    description: "server desc",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 7,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    completed_by: null,
    completed_at: null,
    assignee: undefined,
    acceptance_criteria: [],
    comments: [makeComment("c1"), makeComment("c2")],
    ...over,
  };
}

const ACTOR: ActorInput = { name: "tester", type: "agent" };

interface Ctx {
  mod: ReturnType<typeof createCommentsModule>;
  state: Record<string, unknown>;
  toasts: Array<{ msg?: string; kind?: string }>;
}

function makeCtx(detail: TaskDetail | null): Ctx {
  const state: Record<string, unknown> = {
    detail,
    commentForm: { kind: "progress", body: "" },
    commentShowPreview: false,
    postingComment: false,
    loadingOlderComments: false,
    totalComments: 0,
  };
  const ctx: Ctx = { mod: undefined as never, state, toasts: [] };

  ctx.mod = createCommentsModule({
    // state-in
    detail: () => state.detail as TaskDetail | null,
    commentForm: () => state.commentForm as { kind: string; body: string },
    commentShowPreview: () => state.commentShowPreview as boolean,
    postingComment: () => state.postingComment as boolean,
    loadingOlderComments: () => state.loadingOlderComments as boolean,
    totalComments: () => state.totalComments as number,
    // state-out
    setCommentForm: (f) => { state.commentForm = f; },
    setCommentShowPreview: (v) => { state.commentShowPreview = v; },
    setPostingComment: (v) => { state.postingComment = v; },
    setLoadingOlderComments: (v) => { state.loadingOlderComments = v; },
    setTotalComments: (v) => { state.totalComments = v; },
    // hooks
    toast: (msg, kind) => { ctx.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
  });
  return ctx;
}

/** Patch one api method for the duration of fn; restored in finally. */
async function withApi<T>(name: string, impl: unknown, fn: () => Promise<T>): Promise<T> {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  try { return await fn(); } finally { (api as Record<string, unknown>)[name] = orig; }
}

/** Patch safeStorage.getItem/setItem/removeItem for the duration of fn. */
async function withStorage<T>(patch: Partial<Record<"getItem" | "setItem" | "removeItem", unknown>>, fn: () => Promise<T>): Promise<T> {
  const orig = { getItem: safeStorage.getItem, setItem: safeStorage.setItem, removeItem: safeStorage.removeItem };
  for (const k of Object.keys(patch) as Array<keyof typeof patch>) (safeStorage as never as Record<string, unknown>)[k] = patch[k];
  try { return await fn(); } finally {
    safeStorage.getItem = orig.getItem;
    safeStorage.setItem = orig.setItem;
    safeStorage.removeItem = orig.removeItem;
  }
}

// ----------------------------------------------------------------------- draft restore
describe("restoreCommentDraftForTask", () => {
  test("resets to defaults FIRST, then re-applies a stored draft under this id", async () => {
    const c = makeCtx(makeTask());
    (c.state.commentForm as { body: string }).body = "leftover from another task";
    (c.state as { commentShowPreview: boolean }).commentShowPreview = true;

    await withStorage({
      getItem: (key: string) => (key === commentDraftKey("T1") ? JSON.stringify({ body: "draft T1", kind: "note" }) : null),
    }, async () => {
      c.mod.restoreCommentDraftForTask("T1");
    });

    assert.equal((c.state.commentForm as { body: string }).body, "draft T1");
    assert.equal((c.state.commentForm as { kind: string }).kind, "note");
    assert.equal(c.state.commentShowPreview, false);
  });

  test("no stored draft → stays at the reset defaults", async () => {
    const c = makeCtx(makeTask());
    (c.state.commentForm as { body: string }).body = "leftover";

    await withStorage({ getItem: () => null }, async () => {
      c.mod.restoreCommentDraftForTask("T1");
    });

    assert.equal((c.state.commentForm as { body: string }).body, "");
    assert.equal((c.state.commentForm as { kind: string }).kind, "progress");
  });
});

// ----------------------------------------------------------------------- draft save
describe("saveCommentDraft", () => {
  test("no loaded task → no storage write at all", async () => {
    const c = makeCtx(null);
    (c.state.commentForm as { body: string }).body = "orphan text";
    let writes = 0;
    await withStorage({ setItem: () => { writes++; }, removeItem: () => { writes++; } }, async () => {
      c.mod.saveCommentDraft();
    });
    assert.equal(writes, 0);
  });

  test("non-blank body → serialized under this task's key", async () => {
    const c = makeCtx(makeTask());
    (c.state.commentForm as { body: string; kind: string }).body = "my thought";
    let stored: string | null = null;
    await withStorage({ setItem: (_k: string, v: string) => { stored = v; } }, async () => {
      c.mod.saveCommentDraft();
    });
    assert.ok(stored);
    assert.match(stored!, /my thought/);
  });

  test("blank body → the key is REMOVED (draft cleared)", async () => {
    const c = makeCtx(makeTask());
    (c.state.commentForm as { body: string }).body = "   ";
    let removedKey: string | null = null;
    await withStorage({ removeItem: (k: string) => { removedKey = k; } }, async () => {
      c.mod.saveCommentDraft();
    });
    assert.equal(removedKey, commentDraftKey("T1"));
  });
});

// ----------------------------------------------------------------------- pagination math
describe("older-comment math", () => {
  test("hasMoreOlderComments / olderCommentsRemaining reflect total vs loaded", () => {
    const c = makeCtx(makeTask()); // 2 comments loaded
    (c.state as { totalComments: number }).totalComments = 5;
    assert.equal(c.mod.hasMoreOlderComments(), true);
    assert.equal(c.mod.olderCommentsRemaining(), 3);

    (c.state as { totalComments: number }).totalComments = 2;
    assert.equal(c.mod.hasMoreOlderComments(), false);
    assert.equal(c.mod.olderCommentsRemaining(), 0);
  });

  test("no detail → hasMore is false; remaining returns the raw total (parity)", () => {
    const c = makeCtx(null);
    (c.state as { totalComments: number }).totalComments = 9;
    assert.equal(c.mod.hasMoreOlderComments(), false);
    // Parity quirk: olderCommentsRemaining is null-unsafe by design — with no
    // detail it returns the total unclamped. Kept verbatim, not "fixed".
    assert.equal(c.mod.olderCommentsRemaining(), 9);
  });
});

// ----------------------------------------------------------------------- initComments
describe("initComments", () => {
  test("success → total set + rows merged (only when still the open task)", async () => {
    const c = makeCtx(makeTask());
    await withApi("listComments", async (_id: string, _o?: unknown) => ({ comments: [makeComment("c3", "2026-01-03T00:00:00Z")], total: 3 }), async () => {
      await c.mod.initComments("T1");
    });
    assert.equal(c.state.totalComments, 3);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.comments.map((x) => x.id), ["c1", "c2", "c3"]);
  });

  test("detail switched mid-flight → no merge, no total write", async () => {
    const c = makeCtx(makeTask());
    await withApi("listComments", async (_id: string) => {
      (c.state.detail as TaskDetail | null)!.id = "T2"; // simulate refresh away
      return { comments: [makeComment("zzz")], total: 9 };
    }, async () => {
      await c.mod.initComments("T1");
    });
    const d = c.state.detail as TaskDetail;
    assert.equal(d.comments.length, 2, "stale rows not merged into another task");
    assert.equal(c.state.totalComments, 0);
  });

  test("API failure is swallowed (non-fatal) — no toast", async () => {
    const c = makeCtx(makeTask());
    await withApi("listComments", async () => { throw new Error("500"); }, async () => {
      await c.mod.initComments("T1");
    });
    assert.equal(c.toasts.length, 0);
    assert.equal(c.state.totalComments, 0);
  });
});

// ----------------------------------------------------------------------- mergeComments
describe("mergeComments", () => {
  test("dedupes by id (incoming wins) and sorts by created_at then id", () => {
    const c = makeCtx(makeTask()); // existing c1,c2 @ T0
    c.mod.mergeComments([makeComment("c2", "2026-01-04T00:00:00Z"), makeComment("c3", "2026-01-01T00:00:00Z")]);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.comments.map((x) => x.id), ["c3", "c1", "c2"]);
    assert.equal(d.comments.find((x) => x.id === "c2")!.created_at, "2026-01-04T00:00:00Z", "incoming row replaced the old one");
  });

  test("total ratchets up to the merged length but never down", () => {
    const c = makeCtx(makeTask()); // 2 comments
    (c.state as { totalComments: number }).totalComments = 10;
    c.mod.mergeComments([makeComment("c9")]); // now 3
    assert.equal(c.state.totalComments, 10, "existing higher total preserved");

    const c2 = makeCtx(makeTask()); // 2 comments, total 0
    c2.mod.mergeComments([makeComment("c9"), makeComment("c8")]); // now 4
    assert.equal(c2.state.totalComments, 4, "total raised to merged length");
  });

  test("no detail → no-op", () => {
    const c = makeCtx(null);
    assert.doesNotThrow(() => c.mod.mergeComments([makeComment("x")]));
    assert.equal(c.toasts.length, 0);
  });
});

// ----------------------------------------------------------------------- loadOlderComments
describe("loadOlderComments", () => {
  test("busy guard → no fetch", async () => {
    const c = makeCtx(makeTask());
    (c.state as { loadingOlderComments: boolean }).loadingOlderComments = true;
    let calls = 0;
    await withApi("listComments", async () => { calls++; return { comments: [], total: 2 }; }, async () => {
      await c.mod.loadOlderComments();
    });
    assert.equal(calls, 0);
  });

  test("fetches at offset = current length and merges (same task)", async () => {
    const c = makeCtx(makeTask()); // 2 loaded
    let seenOffset: number | undefined;
    await withApi("listComments", async (_id: string, o?: { limit?: number; offset?: number }) => {
      seenOffset = o?.offset;
      return { comments: [makeComment("c3", "2026-01-05T00:00:00Z")], total: 3 };
    }, async () => {
      await c.mod.loadOlderComments();
    });
    assert.equal(seenOffset, 2);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.comments.map((x) => x.id), ["c1", "c2", "c3"]);
    assert.equal(c.state.totalComments, 3);
    assert.equal(c.state.loadingOlderComments, false);
  });

  test("detail switched mid-flight → merge skipped (total still updated)", async () => {
    const original = makeTask(); // 2 loaded
    const c = makeCtx(original);
    await withApi("listComments", async () => {
      // Swap in a DIFFERENT object — mutating the shared one would change the
      // reference `d` captured before the await too, and the same-task guard
      // could never fail.
      c.state.detail = { ...makeTask(), id: "T2" };
      return { comments: [makeComment("zzz")], total: 3 };
    }, async () => {
      await c.mod.loadOlderComments();
    });
    assert.equal(original.comments.length, 2, "stale rows not merged into the old task");
    const d = c.state.detail as TaskDetail;
    assert.equal(d.id, "T2");
    assert.equal(c.state.totalComments, 3, "total written before the same-task guard (parity)");
  });

  test("API failure → error toast, busy reset", async () => {
    const c = makeCtx(makeTask());
    await withApi("listComments", async () => { throw new Error("500"); }, async () => {
      await c.mod.loadOlderComments();
    });
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.loadingOlderComments, false);
  });
});

// ----------------------------------------------------------------------- postComment
describe("postComment", () => {
  test("guards: no detail / blank body / busy → no api call", async () => {
    let calls = 0;
    const impl = async () => { calls++; return makeComment("c9"); };

    const c1 = makeCtx(null);
    await withApi("addComment", impl, async () => { await c1.mod.postComment(); });
    assert.equal(calls, 0);

    const c2 = makeCtx(makeTask());
    (c2.state.commentForm as { body: string }).body = "   ";
    await withApi("addComment", impl, async () => { await c2.mod.postComment(); });
    assert.equal(calls, 0);

    (c2.state.commentForm as { body: string }).body = "real";
    (c2.state as { postingComment: boolean }).postingComment = true;
    await withApi("addComment", impl, async () => { await c2.mod.postComment(); });
    assert.equal(calls, 0);
  });

  test("success → merged, total ratcheted, body cleared, draft key removed", async () => {
    const c = makeCtx(makeTask()); // 2 loaded
    (c.state as { totalComments: number }).totalComments = 2;
    (c.state.commentForm as { body: string; kind: string }).body = "  new thought  ";
    let removedKey: string | null = null;
    await withStorage({ removeItem: (k: string) => { removedKey = k; } }, async () => {
      await withApi("addComment", async (_id: string, _r?: unknown) => makeComment("c3", "2026-01-06T00:00:00Z"), async () => {
        await c.mod.postComment();
      });
    });

    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.comments.map((x) => x.id), ["c1", "c2", "c3"]);
    // Parity quirk (kept verbatim, not "fixed"): mergeComments already ratchets
    // total up to the merged length (2→3), then postComment adds +1 on top
    // (max(3+1, 3) = 4). So the stored total overshoots the true count by one.
    assert.equal(c.state.totalComments, 4);
    assert.equal((c.state.commentForm as { body: string }).body, "");
    assert.equal(removedKey, commentDraftKey("T1"));
    assert.equal(c.state.postingComment, false);
    assert.equal(c.toasts.length, 0);
  });

  test("detail switched mid-flight → merge skipped but draft still cleared (parity)", async () => {
    const original = makeTask();
    const c = makeCtx(original);
    (c.state.commentForm as { body: string }).body = "thought";
    let removedKey: string | null = null;
    await withStorage({ removeItem: (k: string) => { removedKey = k; } }, async () => {
      await withApi("addComment", async (_id: string, _r?: unknown) => {
        c.state.detail = { ...makeTask(), id: "T2" };
        return makeComment("c9");
      }, async () => {
        await c.mod.postComment();
      });
    });

    assert.equal(original.comments.length, 2, "no merge into the old task");
    assert.equal((c.state.commentForm as { body: string }).body, "", "form cleared regardless (parity)");
    assert.equal(removedKey, commentDraftKey("T1"), "original task's draft key cleared regardless (parity)");
  });

  test("API failure → error toast, body kept, busy reset", async () => {
    const c = makeCtx(makeTask());
    (c.state.commentForm as { body: string }).body = "will fail";
    await withApi("addComment", async () => { throw new Error("500"); }, async () => {
      await c.mod.postComment();
    });
    assert.equal(c.toasts[0].kind, "error");
    assert.equal((c.state.commentForm as { body: string }).body, "will fail", "body kept so the user can retry");
    assert.equal(c.state.postingComment, false);
  });
});
