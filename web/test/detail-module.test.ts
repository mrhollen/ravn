// Unit tests for src/modules/detail.ts — the task-detail lifecycle extracted
// from web/src/app.ts (Phase 2 batch D). Covers: openTask guards (unsaved
// notes AC 3, dirty edit draft, cross-task scratchpad reset, full-page
// replaceState on switch, mobile fallback, in-drawer focus-session reuse) and
// its cascade resets; requestCloseDetail gates; closeDetail's full reset
// cascade + the two-shape history sync (pushed → back(), deep link →
// replaceState(boardUrl), never back() from a deep link); openFullPage
// push-vs-adopt; openDeepLinkedTask fetch-fail/unauthorized/project-adoption;
// restorePopStateTask (popstate back/forward restoration — distinct from the
// first-load deep link: conditional board reload + blocked-cache reset, SSE
// only when detail closed, no new-project recovery); the opencode start panel
// (init precedence, toggle + options re-fetch, dirty
// tracking, activeSession parsing, start lifecycle); copyTaskId;
// refreshDetail/retryDetailLoad ownership guard + drawer error state + 404
// wording; applyDetail comment merge/sort, assignee seed, learnBlockedFromDetail
// and the notes-restore matrix; operational-bar mutual exclusion + moveStatus
// delegation; and the notes scratchpad lifecycle (save/clear drafts, prompt/
// cancel/confirm, saveNotes AC-3 cross-task guard + 409 branch).
//
// Pure module: mutable stub state wired into DetailDeps accessors + call logs
// for every hook + monkey-patched api methods (withApi), all restored in
// finally blocks (same pattern as relations-module.test.ts). DOM globals are
// faked per-test via withGlobals from test-helpers where a body reads them.

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createDetailModule } from "../src/modules/detail";
import { api, ApiError } from "../src/api";
import { safeStorage } from "../src/util";
import { notesDraftKey } from "../src/drafts";
import type { DialogFocus } from "../src/focus";
import { withGlobals } from "./test-helpers";
import type {
  ActorInput,
  OpenCodeOptionsResponse,
  ProjectWithCounts,
  StartOpenCodeInput,
  StartOpenCodeResponse,
  Status,
  TaskDetail,
  TaskSummary,
} from "../src/types";

// ----------------------------------------------------------------------- helpers
const ACTOR: ActorInput = { name: "tester", type: "agent" };

function makeTask(id: string, over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id,
    project_id: "P1",
    seq: 1,
    title: `title of ${id}`,
    description: "",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 7,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    completed_by: null,
    completed_at: null,
    notes: "",
    blocked: false,
    blockers: [],
    comments: [],
    relations: [],
    ...over,
  };
}

function makeComment(id: string, createdAt: string, over: Record<string, unknown> = {}) {
  return {
    id,
    task_id: "T1",
    kind: "progress" as const,
    body: `body ${id}`,
    created_by: ACTOR,
    created_at: createdAt,
    ...over,
  };
}

function makeProject(id: string, over: Partial<ProjectWithCounts> = {}): ProjectWithCounts {
  return {
    id,
    name: `project ${id}`,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    task_counts: { todo: 0, doing: 0, done: 0 },
    ...over,
  };
}

function makeSummary(id: string): TaskSummary {
  return {
    id,
    title: `title of ${id}`,
    status: "todo",
    priority: 2,
    version: 8,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-03T00:01:00Z",
  };
}

function freshRelationForm() {
  return { kind: "parent" as const, targetId: "T9", search: "leftover", open: true, busy: false, activeIndex: 3 };
}

/** Records history mutations; `pathname` is the current (fake) location. */
function fakeWindow(pathname: string, innerWidth?: number) {
  const hist = { pushes: [] as string[], replaces: [] as string[], backs: 0 };
  const win = {
    location: { pathname },
    history: {
      pushState: (_s: unknown, _t: unknown, url: string) => { hist.pushes.push(url); win.location.pathname = url; },
      replaceState: (_s: unknown, _t: unknown, url: string) => { hist.replaces.push(url); win.location.pathname = url; },
      back: () => { hist.backs++; },
    },
  };
  if (innerWidth !== undefined) win.innerWidth = innerWidth;
  return { win, hist };
}

/** Flush microtask queues so `.then(...)` continuations have run. */
const flush = (): Promise<void> => new Promise((r) => setImmediate(r));

/** Swap the global `navigator` (getter-only on modern Node) for `fn`'s duration. */
async function withNavigator<T>(nav: unknown, fn: () => Promise<T>): Promise<T> {
  const desc = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  if (desc) Object.defineProperty(globalThis, "navigator", { value: nav, configurable: true });
  else (globalThis as Record<string, unknown>).navigator = nav;
  try { return await fn(); } finally {
    if (desc) Object.defineProperty(globalThis, "navigator", desc);
    else delete (globalThis as Record<string, unknown>).navigator;
  }
}

async function withApi<T>(name: string, impl: unknown, fn: () => Promise<T>): Promise<T> {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  try { return await fn(); } finally { (api as Record<string, unknown>)[name] = orig; }
}

interface Ctx {
  s: Record<string, any>; // mutable root-state stub
  calls: {
    openDialog: Array<{ id: string; sel?: string; af?: boolean }>;
    closeDialog: string[];
    toasts: Array<{ msg: string; kind?: string; action?: { label: string; href?: string } }>;
    learnBlocked: Array<[string, string, boolean, unknown]>;
    updateDocumentTitle: number;
    loadBoard: Array<boolean | undefined>;
    connectSse: number;
    openAuthModal: number;
    closeDashboard: number;
    closeDashboardNav: number;
    resetBlockedCache: string[];
    restoreCommentDraft: string[];
    initComments: string[];
    moveTaskStatus: Array<[string, Status, number | undefined]>;
    fetchOpenCodeOptions: string[];
    focusInitial: string[];
  };
  deps: Parameters<typeof createDetailModule>[0];
  mod: ReturnType<typeof createDetailModule>;
}

function makeCtx(): Ctx {
  const s: Record<string, any> = {
    detailOpen: false,
    detailId: null as string | null,
    detail: null as TaskDetail | null,
    fullPageOpen: false,
    fullPagePushed: false,
    dashboardOpen: false,
    dashboardPushed: true,
    selectedProjectId: "P1",
    projects: [] as ProjectWithCounts[],
    detailRetrying: false,
    statusMenuOpen: false,
    ownerPopoverOpen: false,
    priorityPopoverOpen: false,
    duePopoverOpen: false,
    notesDirty: false,
    notesDraft: "",
    notesDraftOwner: null as string | null,
    savingNotes: false,
    unsavedNotesAction: null,
    totalComments: 0,
    copiedArtifactId: null as string | null,
    copiedArtifactTimer: null as number | null,
    relationForm: freshRelationForm(),
    startingOpenCode: false,
    showTaskOpenCodeConfig: false,
    taskOpenCodeForm: { agent: "", model: "", variant: "" },
    taskOpenCodeBaseline: { agent: "", model: "", variant: "" },
    openCodeSessions: {} as Record<string, StartOpenCodeResponse>,
    openCodeOptions: null as OpenCodeOptionsResponse | null,
    openCodeOptionsProjectId: "P1",
    detailLoadError: null as string | null,
    loadingOlderComments: false,
    editingTask: false,
    descShowPreview: false,
    assigneeInputName: "",
    assigneeInputType: "human" as "human" | "agent",
    editingAcSeq: 0,
    editingAcText: "",
    newAcText: "",
    showNewProject: false,
  };

  const calls: Ctx["calls"] = {
    openDialog: [],
    closeDialog: [],
    toasts: [],
    learnBlocked: [],
    updateDocumentTitle: 0,
    loadBoard: [],
    connectSse: 0,
    openAuthModal: 0,
    closeDashboard: 0,
    closeDashboardNav: 0,
    resetBlockedCache: [] as string[],
    restoreCommentDraft: [],
    initComments: [],
    moveTaskStatus: [],
    fetchOpenCodeOptions: [],
    focusInitial: [],
  };

  const get = (k: string) => () => s[k];
  const set = (k: string) => (v: any) => { s[k] = v; };

  const deps: Ctx["deps"] = {
    // state-in
    detail: get("detail"),
    detailId: get("detailId"),
    detailOpen: get("detailOpen"),
    fullPageOpen: get("fullPageOpen"),
    fullPagePushed: get("fullPagePushed"),
    dashboardOpen: get("dashboardOpen"),
    selectedProjectId: get("selectedProjectId"),
    projects: get("projects"),
    detailRetrying: get("detailRetrying"),
    statusMenuOpen: get("statusMenuOpen"),
    ownerPopoverOpen: get("ownerPopoverOpen"),
    priorityPopoverOpen: get("priorityPopoverOpen"),
    duePopoverOpen: get("duePopoverOpen"),
    notesDirty: get("notesDirty"),
    notesDraft: get("notesDraft"),
    notesDraftOwner: get("notesDraftOwner"),
    savingNotes: get("savingNotes"),
    unsavedNotesAction: get("unsavedNotesAction"),
    totalComments: get("totalComments"),
    copiedArtifactTimer: get("copiedArtifactTimer"),
    relationForm: () => s.relationForm,
    startingOpenCode: get("startingOpenCode"),
    showTaskOpenCodeConfig: get("showTaskOpenCodeConfig"),
    taskOpenCodeForm: get("taskOpenCodeForm"),
    taskOpenCodeBaseline: get("taskOpenCodeBaseline"),
    openCodeSessions: get("openCodeSessions"),
    openCodeOptions: get("openCodeOptions"),
    openCodeOptionsProjectId: get("openCodeOptionsProjectId"),
    // state-out
    setDetail: set("detail"),
    setDetailId: set("detailId"),
    setDetailOpen: set("detailOpen"),
    setFullPageOpen: set("fullPageOpen"),
    setFullPagePushed: set("fullPagePushed"),
    setDashboardPushed: set("dashboardPushed"),
    setSelectedProjectId: set("selectedProjectId"),
    setDetailLoadError: set("detailLoadError"),
    setDetailRetrying: set("detailRetrying"),
    setStatusMenuOpen: set("statusMenuOpen"),
    setOwnerPopoverOpen: set("ownerPopoverOpen"),
    setPriorityPopoverOpen: set("priorityPopoverOpen"),
    setDuePopoverOpen: set("duePopoverOpen"),
    setNotesDirty: set("notesDirty"),
    setNotesDraft: set("notesDraft"),
    setNotesDraftOwner: set("notesDraftOwner"),
    setNotesShowPreview: () => { /* not asserted; keep shape */ },
    setUnsavedNotesAction: set("unsavedNotesAction"),
    setSavingNotes: set("savingNotes"),
    setTotalComments: set("totalComments"),
    setLoadingOlderComments: set("loadingOlderComments"),
    setCopiedArtifactId: set("copiedArtifactId"),
    setCopiedArtifactTimer: set("copiedArtifactTimer"),
    setEditingTask: set("editingTask"),
    setDescShowPreview: set("descShowPreview"),
    setAssigneeInputName: set("assigneeInputName"),
    setAssigneeInputType: set("assigneeInputType"),
    setEditingAcSeq: set("editingAcSeq"),
    setEditingAcText: set("editingAcText"),
    setNewAcText: set("newAcText"),
    setStartingOpenCode: set("startingOpenCode"),
    setShowTaskOpenCodeConfig: set("showTaskOpenCodeConfig"),
    setTaskOpenCodeForm: (f) => { s.taskOpenCodeForm = f; },
    setTaskOpenCodeBaseline: (f) => { s.taskOpenCodeBaseline = f; },
    setOpenCodeSessions: (x) => { s.openCodeSessions = x; },
    setShowNewProject: set("showNewProject"),
    // hooks
    toast: (msg, kind, action) => { calls.toasts.push({ msg, kind, action }); return calls.toasts.length; },
    actor: () => ACTOR,
    isTaskBusy: (id) => Boolean(s.busyIds?.has(id)),
    moveTaskStatus: async (id, status, v) => { calls.moveTaskStatus.push([id, status, v]); },
    editDraftDirty: () => s.editDraftDirtyFlag ?? false,
    discardEditDraftIfConfirmed: () => { if (s.discardRejected) return false; return true; },
    restoreCommentDraftForTask: (id) => { calls.restoreCommentDraft.push(id); },
    initComments: async (id) => { calls.initComments.push(id); },
    openDialog: (id, sel, af) => {
      calls.openDialog.push({ id, sel, af });
      const f: DialogFocus = {
        focusInitial: () => { calls.focusInitial.push(id); },
        close: () => {},
      };
      return f;
    },
    closeDialog: (f) => { if (f) calls.closeDialog.push("dialog"); },
    learnBlockedFromDetail: (pid, tid, blocked, blockers) => { calls.learnBlocked.push([pid, tid, blocked, blockers]); },
    updateDocumentTitle: () => { calls.updateDocumentTitle++; },
    boardUrl: () => "/board?x=1",
    loadBoard: async (force) => { calls.loadBoard.push(force); },
    connectSse: () => { calls.connectSse++; },
    openAuthModal: () => { calls.openAuthModal++; },
    closeDashboard: () => { calls.closeDashboard++; },
    closeDashboardNav: () => { calls.closeDashboardNav++; },
    resetBlockedCache: (pid) => { calls.resetBlockedCache.push(pid); },
    fetchOpenCodeOptions: async (pid) => { calls.fetchOpenCodeOptions.push(pid); return s.optionsResult ?? null; },
  };

  const mod = createDetailModule(deps);
  return { s, calls, deps, mod };
}

// ----------------------------------------------------------------------- openTask
describe("openTask", () => {
  test("dirty notes on a different task prompts and does NOT open (AC 3)", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "A";
    c.s.notesDirty = true;
    await withApi("getTask", async () => assert.fail("must not fetch"), async () => {
      await c.mod.openTask("B");
    });
    assert.equal(c.s.detailOpen, true); // untouched — prompt first
    assert.deepEqual(c.s.unsavedNotesAction, { kind: "open", taskId: "B" });
    assert.equal(c.calls.openDialog.length, 1); // the cb-notes-confirm session
    assert.equal(c.calls.openDialog[0].id, "cb-notes-confirm");
  });

  test("re-opening the SAME task with dirty notes is a refresh, no prompt", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "A";
    c.s.notesDirty = true;
    await withApi("getTask", async (id: string) => makeTask(id), async () => {
      await c.mod.openTask("A");
    });
    assert.equal(c.s.unsavedNotesAction, null);
    assert.equal(c.s.detailId, "A");
  });

  test("dirty edit draft: declined discard aborts the open", async () => {
    const c = makeCtx();
    c.s.editDraftDirtyFlag = true;
    c.s.discardRejected = true;
    await withApi("getTask", async () => assert.fail("must not fetch"), async () => {
      await c.mod.openTask("B");
    });
    assert.equal(c.s.detailOpen, false);
  });

  test("scratchpad owned by another task is reset up front (AC 2)", async () => {
    const c = makeCtx();
    c.s.notesDraftOwner = "OLD";
    c.s.notesDraft = "old draft";
    c.s.notesDirty = true; // dirty but not gated (drawer closed) → allowed through
    await withApi("getTask", async (id: string) => makeTask(id), async () => {
      await c.mod.openTask("B");
    });
    // The stale "old draft" never survived: the scratchpad was reset up front
    // and applyDetail then repopulated from the fetched payload.
    assert.equal(c.s.notesDraft, "", "payload notes — not the stale draft");
    assert.equal(c.s.notesDirty, false);
    assert.equal(c.s.notesDraftOwner, "B", "re-owned by the task just opened");
  });

  test("task switch while full page is open replaceStates (never push)", async () => {
    const c = makeCtx();
    c.s.fullPageOpen = true;
    c.s.detailId = "A";
    const { win, hist } = fakeWindow("/tasks/A");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.openTask("B");
      });
    });
    assert.deepEqual(hist.pushes, []);
    assert.deepEqual(hist.replaces, ["/tasks/B"]);
  });

  test("desktop open: cascades reset + opens the drawer session with autoFocus off", async () => {
    const c = makeCtx();
    c.s.statusMenuOpen = true;
    c.s.editingTask = true;
    c.s.descShowPreview = true;
    c.s.totalComments = 0;
    await withApi("getTask", async (id: string) => makeTask(id, { comments: [makeComment("c1", "2026-01-01T00:00:00Z")] as any }), async () => {
      await c.mod.openTask("T1");
    });
    assert.equal(c.s.detailOpen, true);
    assert.equal(c.s.detailId, "T1");
    assert.equal(c.s.statusMenuOpen, false);
    assert.equal(c.s.ownerPopoverOpen, false);
    assert.equal(c.s.priorityPopoverOpen, false);
    assert.equal(c.s.duePopoverOpen, false);
    assert.equal(c.s.relationForm.targetId, "");
    assert.equal(c.s.relationForm.search, "");
    assert.equal(c.s.relationForm.open, false);
    assert.equal(c.s.relationForm.activeIndex, -1);
    assert.equal(c.s.editingTask, false);
    assert.equal(c.s.descShowPreview, false);
    assert.deepEqual(c.calls.restoreCommentDraft, ["T1"]);
    assert.equal(c.s.totalComments, 1); // reset to 0 on open, then applyDetail grew it to the payload size
    assert.equal(c.calls.initComments[0], "T1");
    const drawer = c.calls.openDialog.find((d) => d.id === "cb-detail-drawer");
    assert.ok(drawer, "drawer session opened");
    assert.equal(drawer!.sel, '[data-testid="detail-close-btn"]');
    assert.equal(drawer!.af, false);
    assert.deepEqual(c.calls.focusInitial, ["cb-detail-drawer"]); // applied → focusInitial
  });

  test("in-drawer switch reuses the existing focus session (no close/reopen)", async () => {
    const c = makeCtx();
    await withApi("getTask", async (id: string) => makeTask(id), async () => {
      await c.mod.openTask("A");
      await c.mod.openTask("B");
    });
    assert.equal(c.calls.openDialog.filter((d) => d.id === "cb-detail-drawer").length, 1);
    assert.equal(c.calls.closeDialog.length, 0); // stale-session close never fired
  });

  test("mobile viewport (<768px) opens full page and pushes the route", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/", 375);
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.openTask("M1");
      });
    });
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, true);
    assert.deepEqual(hist.pushes, ["/tasks/M1"]);
  });

  test("failed fetch sets the drawer error state and focuses Retry (no DOM crash)", async () => {
    const c = makeCtx();
    await withApi("getTask", async () => { throw new ApiError(500, { error: "boom", message: "server said no" }); }, async () => {
      await c.mod.openTask("T1"); // document is undefined in node — must not throw
    });
    assert.equal(c.s.detailOpen, true);
    assert.equal(c.s.detail, null);
    assert.equal(c.s.detailLoadError, "server said no");
    assert.deepEqual(c.calls.toasts.map((t) => t.msg), ["server said no"]);
  });
});

// ----------------------------------------------------------------------- close flows
describe("requestCloseDetail / closeDetail", () => {
  test("dirty notes gate the close with a confirm prompt (AC 3)", () => {
    const c = makeCtx();
    c.s.notesDirty = true;
    c.s.detailOpen = true;
    c.mod.requestCloseDetail();
    assert.equal(c.s.detailOpen, true);
    assert.deepEqual(c.s.unsavedNotesAction, { kind: "close" });
    assert.equal(c.calls.openDialog.length, 1);
    assert.equal(c.calls.openDialog[0].id, "cb-notes-confirm");
    assert.equal(c.calls.openDialog[0].sel, "button.btn-ghost");
  });

  test("dirty edit draft declined → close aborted", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.discardRejected = true;
    c.mod.requestCloseDetail();
    assert.equal(c.s.detailOpen, true);
  });

  test("clean close runs the full reset cascade", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.fullPageOpen = true;
    c.s.fullPagePushed = true;
    c.s.statusMenuOpen = true;
    c.s.ownerPopoverOpen = true;
    c.s.editingAcSeq = 3;
    c.s.editingAcText = "x";
    c.s.newAcText = "y";
    c.s.startingOpenCode = true;
    c.s.showTaskOpenCodeConfig = true;
    c.s.copiedArtifactId = "art1";
    c.s.notesDirty = true;
    c.s.notesDraft = "keep? no — drawer closed";
    const cleared: number[] = [];
    const realClear = globalThis.clearTimeout;
    await withGlobals({ clearTimeout: (id?: number) => { if (typeof id === "number") cleared.push(id); return undefined; } }, async () => {
      c.s.copiedArtifactTimer = 42;
      c.mod.closeDetail();
    });
    assert.deepEqual(cleared, [42]);
    assert.equal(c.s.copiedArtifactTimer, null);
    assert.equal(c.s.statusMenuOpen, false);
    assert.equal(c.s.ownerPopoverOpen, false);
    assert.equal(c.s.priorityPopoverOpen, false);
    assert.equal(c.s.duePopoverOpen, false);
    assert.equal(c.s.relationForm.targetId, "");
    assert.equal(c.s.relationForm.activeIndex, -1);
    assert.equal(c.s.editingTask, false);
    assert.equal(c.s.detailLoadError, null);
    assert.equal(c.s.editingAcSeq, 0);
    assert.equal(c.s.editingAcText, "");
    assert.equal(c.s.newAcText, "");
    assert.equal(c.s.totalComments, 0);
    assert.equal(c.s.loadingOlderComments, false);
    assert.equal(c.s.startingOpenCode, false);
    assert.equal(c.s.showTaskOpenCodeConfig, false);
    assert.equal(c.s.copiedArtifactId, null);
    // AC 1: the scratchpad never outlives its drawer
    assert.equal(c.s.notesDirty, false);
    assert.equal(c.s.notesDraft, "");
    assert.equal(c.s.notesDraftOwner, null);
    assert.equal(c.s.unsavedNotesAction, null);
    assert.equal(c.s.detailOpen, false);
    assert.equal(c.s.fullPageOpen, false);
    assert.equal(c.s.fullPagePushed, false);
    assert.ok(c.calls.updateDocumentTitle >= 1);
    void realClear; // referenced for clarity only
  });

  test("history sync: pushed full page on a task route pops (back())", async () => {
    const c = makeCtx();
    c.s.fullPageOpen = true;
    c.s.fullPagePushed = true;
    const { win, hist } = fakeWindow("/tasks/T1");
    await withGlobals({ window: win }, async () => { c.mod.closeDetail(); });
    assert.equal(hist.backs, 1);
    assert.deepEqual(hist.replaces, []);
  });

  test("history sync: DEEP LINKED full page replaces with boardUrl — NEVER back()", async () => {
    const c = makeCtx();
    c.s.fullPageOpen = true;
    c.s.fullPagePushed = false; // deep link — no entry of ours behind it
    const { win, hist } = fakeWindow("/tasks/T1");
    await withGlobals({ window: win }, async () => { c.mod.closeDetail(); });
    assert.equal(hist.backs, 0, "back() from a deep link would navigate out of the app");
    assert.deepEqual(hist.replaces, ["/board?x=1"]);
  });

  test("history sync: non-task pathname (already back) makes no change", async () => {
    const c = makeCtx();
    c.s.fullPageOpen = true;
    c.s.fullPagePushed = true;
    const { win, hist } = fakeWindow("/");
    await withGlobals({ window: win }, async () => { c.mod.closeDetail(); });
    assert.equal(hist.backs, 0);
    assert.deepEqual(hist.replaces, []);
  });

  test("history sync: syncHistory=false skips history mutation entirely", async () => {
    const c = makeCtx();
    c.s.fullPageOpen = true;
    c.s.fullPagePushed = true;
    const { win, hist } = fakeWindow("/tasks/T1");
    await withGlobals({ window: win }, async () => { c.mod.closeDetail(false); });
    assert.equal(hist.backs, 0);
    assert.deepEqual(hist.replaces, []);
    assert.equal(c.s.detailOpen, false); // state still resets
  });

  test("non-full-page close never touches history", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/tasks/T1"); // on task URL but not full page (e.g. modal over it)
    await withGlobals({ window: win }, async () => { c.mod.closeDetail(); });
    assert.equal(hist.backs, 0);
    assert.deepEqual(hist.replaces, []);
  });
});

// ----------------------------------------------------------------------- openFullPage
describe("openFullPage", () => {
  test("closes the dashboard and pushes the task route", async () => {
    const c = makeCtx();
    c.s.dashboardOpen = true;
    c.s.dashboardPushed = true;
    c.s.detail = makeTask("T1");
    const { win, hist } = fakeWindow("/board?x=1");
    await withGlobals({ window: win }, async () => { c.mod.openFullPage(); });
    assert.equal(c.calls.closeDashboard, 1);
    assert.equal(c.s.dashboardPushed, false);
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, true);
    assert.deepEqual(hist.pushes, ["/tasks/T1"]);
  });

  test("URL already on the same task route → adopt (no push, not pushed)", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    const { win, hist } = fakeWindow("/tasks/T1");
    await withGlobals({ window: win }, async () => { c.mod.openFullPage(); });
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, false);
    assert.deepEqual(hist.pushes, []);
  });

  test("no id available → no-op", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/");
    await withGlobals({ window: win }, async () => { c.mod.openFullPage(); });
    assert.equal(c.s.fullPageOpen, false);
    assert.deepEqual(hist.pushes, []);
  });

  test("no window (node) → no-op, no crash", () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    c.mod.openFullPage();
    assert.equal(c.s.fullPageOpen, false);
  });
});

// ----------------------------------------------------------------------- deep link
describe("openDeepLinkedTask", () => {
  test("success adopts the task's project, loads board + SSE, opens full page unpushed", async () => {
    const c = makeCtx();
    c.s.selectedProjectId = "P0";
    await withApi("getTask", async (id: string) => makeTask(id, { project_id: "PX" }), async () => {
      await c.mod.openDeepLinkedTask("D1");
    });
    assert.equal(c.s.selectedProjectId, "PX", "project adopted so SSE/poll/relations target it");
    assert.deepEqual(c.calls.loadBoard, [undefined]);
    assert.equal(c.calls.connectSse, 1);
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, false, "the URL was given by the browser — we did not push it");
    assert.equal(c.s.detail?.id, "D1");
  });

  test("401 → auth modal + URL reset to boardUrl + new-project offer (no selected project)", async () => {
    const c = makeCtx();
    c.s.selectedProjectId = "";
    const { win, hist } = fakeWindow("/tasks/D1");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async () => { throw new ApiError(401, { error: "unauthorized" }); }, async () => {
        await c.mod.openDeepLinkedTask("D1");
      });
    });
    assert.equal(c.calls.openAuthModal, 1);
    assert.deepEqual(hist.replaces, ["/board?x=1"]);
    assert.equal(c.s.showNewProject, true);
    assert.equal(c.calls.loadBoard.length, 0);
  });

  test("other fetch error → toast + URL reset; selected project gets board + SSE", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/tasks/D1");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async () => { throw new ApiError(500, { error: "boom", message: "kaput" }); }, async () => {
        await c.mod.openDeepLinkedTask("D1");
      });
    });
    assert.equal(c.calls.openAuthModal, 0);
    assert.deepEqual(c.calls.toasts.map((t) => t.msg), ["kaput"]);
    assert.deepEqual(hist.replaces, ["/board?x=1"]);
    assert.deepEqual(c.calls.loadBoard, [undefined]);
    assert.equal(c.calls.connectSse, 1);
    assert.equal(c.s.showNewProject, false);
  });
});

// ----------------------------------------------------------------------- popstate restore
describe("restorePopStateTask (popstate back/forward onto /tasks/<id>)", () => {
  test("success adopts the task's project: blocked-cache reset + board reload, full page unpushed", async () => {
    const c = makeCtx();
    c.s.selectedProjectId = "P0";
    const { win, hist } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id, { project_id: "PX" }), async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.s.selectedProjectId, "PX", "project adopted so SSE/poll/relations target it");
    assert.deepEqual(c.calls.resetBlockedCache, ["PX"]);
    assert.deepEqual(c.calls.loadBoard, [undefined]);
    // The entry is already in history — a close must replaceState, not back().
    assert.equal(c.s.fullPagePushed, false);
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.detail?.id, "T2", "opened as full page via openTask");
    assert.equal(c.calls.connectSse, 1, "detail was not open → SSE connected once");
    assert.deepEqual(hist.pushes, [], "popstate restore never pushes history");
    assert.equal(hist.backs, 0);
  });

  test("same project → NO board reload / cache reset; SSE still connected (detail closed)", async () => {
    const c = makeCtx(); // selectedProjectId P1 == makeTask's default project
    const { win, hist } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.s.selectedProjectId, "P1", "no adoption for the already-selected project");
    assert.deepEqual(c.calls.resetBlockedCache, []);
    assert.deepEqual(c.calls.loadBoard, []);
    assert.equal(c.calls.connectSse, 1);
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, false);
    assert.deepEqual(hist.pushes, []);
  });

  test("detail already open (different task) → no board reload and NO extra SSE connect", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    const { win, hist } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.s.detailId, "T2", "switched to the restored task");
    assert.deepEqual(c.calls.resetBlockedCache, []);
    assert.deepEqual(c.calls.loadBoard, []);
    assert.equal(c.calls.connectSse, 0, "detail was already open — SSE left alone");
    assert.deepEqual(hist.pushes, []);
  });

  test("an open dashboard is closed via the nav hook before restoration (and not when closed)", async () => {
    const c = makeCtx();
    c.s.dashboardOpen = true;
    const { win } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.calls.closeDashboardNav, 1);

    const c2 = makeCtx(); // dashboard already closed → nothing to close
    await withApi("getTask", async (id: string) => makeTask(id), async () => {
      await c2.mod.restorePopStateTask("T2");
    });
    assert.equal(c2.calls.closeDashboardNav, 0);
  });

  test("401 → auth modal + URL reset to boardUrl; nothing is opened", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async () => { throw new ApiError(401, { error: "unauthorized" }); }, async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.calls.openAuthModal, 1);
    assert.deepEqual(c.calls.toasts, []);
    assert.deepEqual(hist.replaces, ["/board?x=1"], "a dead route can't strand the history state");
    assert.equal(c.s.detailOpen, false);
    assert.equal(c.s.fullPageOpen, false);
    assert.deepEqual(c.calls.loadBoard, []);
  });

  test("other fetch error → toast + URL reset to boardUrl (no new-project recovery)", async () => {
    const c = makeCtx();
    const { win, hist } = fakeWindow("/tasks/T2");
    await withGlobals({ window: win }, async () => {
      await withApi("getTask", async () => { throw new ApiError(500, { error: "boom", message: "kaput" }); }, async () => {
        await c.mod.restorePopStateTask("T2");
      });
    });
    assert.equal(c.calls.openAuthModal, 0);
    assert.deepEqual(c.calls.toasts.map((t) => t.msg), ["kaput"]);
    assert.deepEqual(hist.replaces, ["/board?x=1"]);
    assert.equal(c.s.fullPageOpen, false);
    assert.equal(c.s.showNewProject, false, "popstate restore has no first-load fallback");
  });

  test("forward nav onto the already-open task just re-arms page mode (no fetch)", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T2";
    c.s.fullPagePushed = true; // stepping forward onto an entry we had pushed
    let fetched = false;
    await withApi("getTask", async () => { fetched = true; return makeTask("T2"); }, async () => {
      await c.mod.restorePopStateTask("T2");
    });
    assert.equal(fetched, false, "forward re-arm never refetches");
    assert.equal(c.s.fullPageOpen, true);
    assert.equal(c.s.fullPagePushed, false, "the entry is the browser's — a close must replaceState");
    assert.equal(c.calls.updateDocumentTitle, 1);
    assert.equal(c.calls.connectSse, 0);
    assert.deepEqual(c.calls.loadBoard, []);
  });
});

// ----------------------------------------------------------------------- opencode
describe("opencode start panel", () => {
  test("initTaskOpenCodeConfig: project settings > resolved options > defaults", () => {
    const c = makeCtx();
    c.s.projects = [makeProject("P1", { opencode_agent: "review", opencode_model: "m-x" })];
    c.s.openCodeOptions = { resolved_agent: "build", resolved_model: "r-m", resolved_variant: "r-v" } as OpenCodeOptionsResponse;
    c.mod.initTaskOpenCodeConfig();
    assert.equal(c.s.showTaskOpenCodeConfig, false);
    // Per-field fallback: agent/model come from project settings, variant
    // (unset on the project) falls through to the resolved options.
    assert.deepEqual(c.s.taskOpenCodeForm, { agent: "review", model: "m-x", variant: "r-v" });
    assert.deepEqual(c.s.taskOpenCodeBaseline, { agent: "review", model: "m-x", variant: "r-v" });

    const c2 = makeCtx();
    c2.s.openCodeOptions = { resolved_agent: "build", resolved_model: "r-m", resolved_variant: "r-v" } as OpenCodeOptionsResponse;
    c2.mod.initTaskOpenCodeConfig();
    assert.deepEqual(c2.s.taskOpenCodeForm, { agent: "build", model: "r-m", variant: "r-v" });

    const c3 = makeCtx(); // no project, no options
    c3.mod.initTaskOpenCodeConfig();
    assert.deepEqual(c3.s.taskOpenCodeForm, { agent: "build", model: "", variant: "" });
  });

  test("toggle opens and re-fetches stale options, reseeding only when not dirty", async () => {
    const c = makeCtx();
    c.s.openCodeOptionsProjectId = "STALE";
    c.s.optionsResult = { resolved_agent: "fresh-a", resolved_model: "fresh-m", resolved_variant: "" } as OpenCodeOptionsResponse;
    c.mod.toggleOpenCodeConfig();
    assert.equal(c.s.showTaskOpenCodeConfig, true);
    assert.deepEqual(c.calls.fetchOpenCodeOptions, ["P1"]);
    await flush();
    assert.deepEqual(c.s.taskOpenCodeForm, { agent: "fresh-a", model: "fresh-m", variant: "" });
    assert.deepEqual(c.s.taskOpenCodeBaseline, { agent: "fresh-a", model: "fresh-m", variant: "" });
  });

  test("toggle re-fetch does NOT clobber a dirty form", async () => {
    const c = makeCtx();
    c.s.openCodeOptionsProjectId = "STALE";
    c.s.optionsResult = { resolved_agent: "fresh-a", resolved_model: "", resolved_variant: "" } as OpenCodeOptionsResponse;
    c.s.taskOpenCodeForm = { agent: "user-edited", model: "", variant: "" };
    c.mod.toggleOpenCodeConfig();
    await flush();
    assert.equal(c.s.taskOpenCodeForm.agent, "user-edited");
  });

  test("fresh options for the selected project are not re-fetched", () => {
    const c = makeCtx();
    c.s.openCodeOptions = { resolved_agent: "a" } as OpenCodeOptionsResponse;
    c.mod.toggleOpenCodeConfig();
    assert.deepEqual(c.calls.fetchOpenCodeOptions, []);
  });

  test("taskOpenCodeDirty tracks any of the three fields", () => {
    const c = makeCtx();
    assert.equal(c.mod.taskOpenCodeDirty(), false);
    c.s.taskOpenCodeForm = { agent: "x", model: "", variant: "" };
    assert.equal(c.mod.taskOpenCodeDirty(), true);
    c.s.taskOpenCodeForm = { agent: "", model: "y", variant: "" };
    assert.equal(c.mod.taskOpenCodeDirty(), true);
    c.s.taskOpenCodeForm = { agent: "", model: "", variant: "z" };
    assert.equal(c.mod.taskOpenCodeDirty(), true);
  });

  test("activeOpenCodeSession: cached first, then newest matching artifact, query stripped", () => {
    const c = makeCtx();
    const cached: StartOpenCodeResponse = { session_id: "cached", session_url: "/oc/cached" };
    c.s.openCodeSessions = { T1: cached };
    c.s.detail = makeTask("T1", { artifacts: [{ summary: "OpenCode autonomous session", ref: "/oc/ignored?x=1" }] as any });
    assert.equal(c.mod.activeOpenCodeSession()?.session_id, "cached");

    delete (c.s.openCodeSessions as Record<string, StartOpenCodeResponse>).T1;
    c.s.detail = makeTask("T1", { artifacts: [{ summary: "OpenCode autonomous session", ref: "/oc/session/sess-7?x=1" }] as any });
    const sess = c.mod.activeOpenCodeSession();
    assert.deepEqual(sess, { session_id: "sess-7", session_url: "/oc/session/sess-7?x=1" }, "query string stripped from the id");

    // A matching ref WITHOUT a /session/ segment falls back to the full ref as id.
    c.s.detail = makeTask("T1", { artifacts: [{ summary: "OpenCode autonomous session", ref: "/oc/ignored?x=1" }] as any });
    assert.deepEqual(c.mod.activeOpenCodeSession(), { session_id: "/oc/ignored?x=1", session_url: "/oc/ignored?x=1" });
  });

  test("activeOpenCodeSession: no match → null; missing detail → null", () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1", { artifacts: [{ summary: "screenshot", ref: "/artifacts/1.png" }] as any });
    assert.equal(c.mod.activeOpenCodeSession(), null);
    assert.equal(c.mod.activeOpenCodeSession(null), null);
  });

  test("startTaskInOpenCode: dirty form builds the input and caches the session", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    c.s.taskOpenCodeBaseline = { agent: "build", model: "", variant: "base-v" };
    c.s.taskOpenCodeForm = { agent: "  review ", model: "m-2", variant: "" };
    let seen: StartOpenCodeInput | undefined;
    await withApi("startOpenCode", async (_id: string, input?: StartOpenCodeInput) => {
      seen = input;
      return { session_id: "s-1", session_url: "/oc/s-1" } as StartOpenCodeResponse;
    }, async () => {
      await c.mod.startTaskInOpenCode();
    });
    assert.deepEqual(seen, { agent: "review", model: "m-2", variant: "" }, "baseline variant present → field sent even when cleared");
    assert.equal(c.s.openCodeSessions["T1"].session_id, "s-1");
    assert.equal(c.s.showTaskOpenCodeConfig, false);
    assert.equal(c.s.startingOpenCode, false, "finally reset");
    const toast = c.calls.toasts.find((t) => t.msg.includes("s-1"));
    assert.ok(toast);
    assert.deepEqual(toast!.action, { label: "Open Session", href: "/oc/s-1" });
    assert.deepEqual(c.calls.loadBoard, [true]);
  });

  test("startTaskInOpenCode: clean form + closed panel sends no input; error toasts", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    let seen: StartOpenCodeInput | undefined = "unset" as any;
    await withApi("startOpenCode", async (_id: string, input?: StartOpenCodeInput) => {
      seen = input;
      throw new ApiError(500, { error: "boom", message: "nope" });
    }, async () => {
      await c.mod.startTaskInOpenCode();
    });
    assert.equal(seen, undefined);
    assert.deepEqual(c.calls.toasts.map((t) => t.msg), ["nope"]);
    assert.equal(c.s.startingOpenCode, false);
  });

  test("startTaskInOpenCode: guards (no detail / already starting)", async () => {
    const c = makeCtx();
    let hits = 0;
    await withApi("startOpenCode", async () => { hits++; return {} as StartOpenCodeResponse; }, async () => {
      await c.mod.startTaskInOpenCode(); // no detail
      c.s.detail = makeTask("T1");
      c.s.startingOpenCode = true;
      await c.mod.startTaskInOpenCode(); // busy guard
    });
    assert.equal(hits, 0);
  });

  test("startTaskInOpenCode: explicit custom input passes through untouched", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    let seen: StartOpenCodeInput | undefined;
    await withApi("startOpenCode", async (_id, input) => { seen = input; return {} as StartOpenCodeResponse; }, async () => {
      await c.mod.startTaskInOpenCode({ agent: "explicit" });
    });
    assert.deepEqual(seen, { agent: "explicit" });
  });
});

// ----------------------------------------------------------------------- copy id
describe("copyTaskId", () => {
  test("empty id is a no-op", async () => {
    const c = makeCtx();
    await c.mod.copyTaskId("");
    assert.equal(c.calls.toasts.length, 0);
  });

  test("clipboard available → info toast", async () => {
    const c = makeCtx();
    await withNavigator({ clipboard: { writeText: async () => {} } }, async () => {
      await c.mod.copyTaskId("01J3");
    });
    assert.deepEqual(c.calls.toasts, [{ msg: "Copied task ID to clipboard", kind: "info", action: undefined }]);
  });

  test("clipboard unavailable (node fallback fails) → error toast", async () => {
    const c = makeCtx();
    // No navigator.clipboard and no document.body → copyViaExecCommand is false.
    await withNavigator(undefined, async () => {
      await c.mod.copyTaskId("01J3");
    });
    assert.deepEqual(c.calls.toasts.map((t) => t.kind), ["error"]);
  });
});

// ----------------------------------------------------------------------- refresh / apply
describe("refreshDetail", () => {
  test("no id to fetch → false, no api call", async () => {
    const c = makeCtx();
    let hits = 0;
    await withApi("getTask", async () => { hits++; return makeTask("X"); }, async () => {
      assert.equal(await c.mod.refreshDetail(false), false);
    });
    assert.equal(hits, 0);
  });

  test("stale payload (drawer switched) is dropped — no detail write", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "CURRENT";
    await withApi("getTask", async (_id: string) => makeTask("OLD"), async () => {
      assert.equal(await c.mod.refreshDetail(false), false);
    });
    assert.equal(c.s.detail, null);
  });

  test("stale payload (drawer closed mid-flight) is dropped", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    // Close the drawer just before the fetch resolves:
    await withApi("getTask", async (_id: string) => { c.s.detailOpen = false; return makeTask("T1"); }, async () => {
      assert.equal(await c.mod.refreshDetail(false), false);
    });
    assert.equal(c.s.detail, null);
  });

  test("failure for the open task sets the drawer error + toasts (non-silent)", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    await withApi("getTask", async () => { throw new ApiError(500, { error: "boom", message: "kaput" }); }, async () => {
      assert.equal(await c.mod.refreshDetail(false), false);
    });
    assert.equal(c.s.detailLoadError, "kaput");
    assert.deepEqual(c.calls.toasts.map((t) => t.msg), ["kaput"]);
  });

  test("silent failure does not toast", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    await withApi("getTask", async () => { throw new ApiError(500, { error: "boom", message: "kaput" }); }, async () => {
      assert.equal(await c.mod.refreshDetail(true), false);
    });
    assert.equal(c.s.detailLoadError, "kaput");
    assert.equal(c.calls.toasts.length, 0);
  });

  test("404 gets the deleted-task wording", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    await withApi("getTask", async () => { throw new ApiError(404, { error: "not_found" }); }, async () => {
      assert.equal(await c.mod.refreshDetail(false), false);
    });
    assert.equal(c.s.detailLoadError, "This task was deleted or no longer exists.");
  });

  test("failure for a task we are NOT open on leaves the drawer untouched", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "CURRENT";
    await withApi("getTask", async (_id: string) => { throw new ApiError(500, { error: "boom" }); }, async () => {
      assert.equal(await c.mod.refreshDetail(false, "OTHER"), false);
    });
    assert.equal(c.s.detailLoadError, null);
  });

  test("success applies the payload", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    await withApi("getTask", async (id: string) => makeTask(id, { notes: "n" }), async () => {
      assert.equal(await c.mod.refreshDetail(false), true);
    });
    assert.equal(c.s.detail?.id, "T1");
  });
});

describe("retryDetailLoad", () => {
  test("guards: closed drawer and in-flight retry are no-ops", async () => {
    const c = makeCtx();
    let hits = 0;
    await withApi("getTask", async (id: string) => { hits++; return makeTask(id); }, async () => {
      c.s.detailId = "T1"; // but not open
      await c.mod.retryDetailLoad();
      c.s.detailOpen = true;
      c.s.detailRetrying = true;
      await c.mod.retryDetailLoad();
    });
    assert.equal(hits, 0);
  });

  test("happy path toggles the flag around the fetch (even on failure)", async () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    let during = false;
    await withApi("getTask", async () => { during = c.s.detailRetrying; throw new ApiError(500, { error: "boom" }); }, async () => {
      await c.mod.retryDetailLoad();
    });
    assert.equal(during, true);
    assert.equal(c.s.detailRetrying, false, "finally cleared");
  });
});

describe("applyDetail", () => {
  test("ownership guard: closed drawer → zero writes", () => {
    const c = makeCtx();
    const t = makeTask("T1", { notes: "n" });
    c.mod.applyDetail(t);
    assert.equal(c.s.detail, null);
    assert.equal(c.calls.updateDocumentTitle, 0);
    assert.equal(c.calls.learnBlocked.length, 0);
  });

  test("ownership guard: id mismatch → zero writes", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "OTHER";
    c.mod.applyDetail(makeTask("T1"));
    assert.equal(c.s.detail, null);
  });

  test("merges comments with the existing same-task set, sorted by time then id", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.s.detail = makeTask("T1", { comments: [makeComment("c2", "2026-01-02T00:00:00Z"), makeComment("c1", "2026-01-01T00:00:00Z")] as any });
    const incoming = makeTask("T1", { comments: [makeComment("c3", "2026-01-03T00:00:00Z"), makeComment("c1", "2026-01-01T00:00:00Z")] as any });
    c.mod.applyDetail(incoming);
    assert.deepEqual(c.s.detail.comments.map((x: any) => x.id), ["c1", "c2", "c3"]);
  });

  test("comments from a different task are NOT merged in", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.s.detail = makeTask("T0", { comments: [makeComment("other", "2026-01-01T00:00:00Z")] as any });
    c.mod.applyDetail(makeTask("T1", { comments: [] } as any));
    assert.deepEqual(c.s.detail.comments, []);
  });

  test("seeds assignee inputs, learns blocked state, grows totalComments only", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.s.totalComments = 50;
    c.mod.applyDetail(makeTask("T1", {
      assignee: { name: "Ada", type: "agent" },
      blocked: true,
      blockers: [{ id: "B1", title: "blocked by" }] as any,
      comments: [makeComment("c1", "2026-01-01T00:00:00Z")] as any,
    }));
    assert.equal(c.s.assigneeInputName, "Ada");
    assert.equal(c.s.assigneeInputType, "agent");
    assert.deepEqual(c.calls.learnBlocked, [["P1", "T1", true, [{ id: "B1", title: "blocked by" }]]]);
    assert.equal(c.s.totalComments, 50, "1 ≤ 50 — never shrinks");
    // A follow-up refresh for the SAME task merges the existing comment set
    // forward (the server payload would include them anyway): total only ever
    // grows relative to the merged size.
    c.s.totalComments = 1;
    c.mod.applyDetail(makeTask("T1"));
    assert.equal(c.s.totalComments, 1, "merged size 1 is not a growth — left alone");
    assert.ok(c.s.detail.comments.some((x: any) => x.id === "c1"), "existing same-task comment survives the merge");
  });

  test("assignee missing → empty name + human default", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.mod.applyDetail(makeTask("T1"));
    assert.equal(c.s.assigneeInputName, "");
    assert.equal(c.s.assigneeInputType, "human");
  });

  test("notes restore: stored draft wins (dirty=true)", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    safeStorage.setItem(notesDraftKey("T1"), "local edit");
    try {
      c.mod.applyDetail(makeTask("T1", { notes: "server notes" }));
      assert.equal(c.s.notesDraft, "local edit");
      assert.equal(c.s.notesDirty, true);
      assert.equal(c.s.notesDraftOwner, "T1");
    } finally {
      safeStorage.removeItem(notesDraftKey("T1"));
    }
  });

  test("notes restore: no stored draft → server notes (dirty=false)", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.mod.applyDetail(makeTask("T1", { notes: "server notes" }));
    assert.equal(c.s.notesDraft, "server notes");
    assert.equal(c.s.notesDirty, false);
  });

  test("notes restore: in-progress edit of THIS task is never clobbered", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.s.notesDirty = true;
    c.s.notesDraftOwner = "T1";
    c.s.notesDraft = "mid-typing";
    c.mod.applyDetail(makeTask("T1", { notes: "server notes" }));
    assert.equal(c.s.notesDraft, "mid-typing");
    assert.equal(c.s.notesDirty, true);
  });

  test("a landed payload clears a pending fetch error", () => {
    const c = makeCtx();
    c.s.detailOpen = true;
    c.s.detailId = "T1";
    c.s.detailLoadError = "old error";
    c.mod.applyDetail(makeTask("T1"));
    assert.equal(c.s.detailLoadError, null);
  });
});

// ----------------------------------------------------------------------- operational bar
describe("operational bar", () => {
  test("toggleStatusMenu flips itself and closes all other popovers", () => {
    const c = makeCtx();
    c.s.ownerPopoverOpen = true;
    c.s.priorityPopoverOpen = true;
    c.s.duePopoverOpen = true;
    c.mod.toggleStatusMenu();
    assert.equal(c.s.statusMenuOpen, true);
    assert.equal(c.s.ownerPopoverOpen, false);
    assert.equal(c.s.priorityPopoverOpen, false);
    assert.equal(c.s.duePopoverOpen, false);
    c.mod.toggleStatusMenu();
    assert.equal(c.s.statusMenuOpen, false);
  });

  test("toggleOwnerPopover seeds the assignee inputs from the open task", () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1", { assignee: { name: "Bob", type: "agent" } });
    c.mod.toggleOwnerPopover();
    assert.equal(c.s.ownerPopoverOpen, true);
    assert.equal(c.s.statusMenuOpen, false);
    assert.equal(c.s.assigneeInputName, "Bob");
    assert.equal(c.s.assigneeInputType, "agent");
  });

  test("moveStatus: no detail / busy / same-status all short-circuit", async () => {
    const c = makeCtx();
    await c.mod.moveStatus("doing"); // no detail
    assert.equal(c.calls.moveTaskStatus.length, 0);

    const busy = makeCtx();
    busy.s.detail = makeTask("T1", { status: "todo" });
    busy.s.busyIds = new Set(["T1"]);
    await busy.mod.moveStatus("doing");
    assert.equal(busy.calls.moveTaskStatus.length, 0);

    const same = makeCtx();
    same.s.detail = makeTask("T1", { status: "doing" });
    await same.mod.moveStatus("doing");
    assert.equal(same.calls.moveTaskStatus.length, 0);
  });

  test("moveStatus delegates with the stored version and closes the menu", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1", { status: "todo", version: 9 });
    await c.mod.moveStatus("doing");
    assert.equal(c.s.statusMenuOpen, false);
    assert.deepEqual(c.calls.moveTaskStatus, [["T1", "doing", 9]]);
  });
});

// ----------------------------------------------------------------------- notes scratchpad
describe("notes scratchpad", () => {
  test("saveNotesDraft writes storage when non-blank, removes when blank", () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    c.s.notesDraft = "hello";
    c.mod.saveNotesDraft();
    assert.equal(safeStorage.getItem(notesDraftKey("T1")), "hello");
    c.s.notesDraft = "   ";
    c.mod.saveNotesDraft();
    assert.equal(safeStorage.getItem(notesDraftKey("T1")), null);
  });

  test("saveNotesDraft without a detail is a no-op", () => {
    const c = makeCtx();
    c.s.notesDraft = "x";
    c.mod.saveNotesDraft(); // must not throw
    assert.equal(c.calls.toasts.length, 0);
  });

  test("clearNotesDraft targets the explicit id, defaulting to the open task", () => {
    const c = makeCtx();
    safeStorage.setItem(notesDraftKey("A"), "a");
    safeStorage.setItem(notesDraftKey("B"), "b");
    try {
      c.s.detail = makeTask("B");
      c.mod.clearNotesDraft("A");
      assert.equal(safeStorage.getItem(notesDraftKey("A")), null);
      assert.equal(safeStorage.getItem(notesDraftKey("B")), "b");
      c.mod.clearNotesDraft();
      assert.equal(safeStorage.getItem(notesDraftKey("B")), null);
    } finally {
      safeStorage.removeItem(notesDraftKey("A"));
      safeStorage.removeItem(notesDraftKey("B"));
    }
  });

  test("prompt/cancel: draft untouched, confirm session closed", () => {
    const c = makeCtx();
    c.s.notesDraft = "keep me";
    c.mod.promptUnsavedNotes({ kind: "close" });
    assert.deepEqual(c.s.unsavedNotesAction, { kind: "close" });
    c.mod.cancelUnsavedNotes();
    assert.equal(c.s.unsavedNotesAction, null);
    assert.equal(c.s.notesDraft, "keep me");
    assert.equal(c.calls.closeDialog.length, 1);
  });

  test("confirm (discard) drops the stored key + draft, then runs the deferred CLOSE", async () => {
    const c = makeCtx();
    safeStorage.setItem(notesDraftKey("T1"), "stored");
    try {
      c.s.notesDraftOwner = "T1";
      c.s.notesDraft = "unsaved";
      c.mod.promptUnsavedNotes({ kind: "close" });
      await c.mod.confirmUnsavedNotes();
      assert.equal(safeStorage.getItem(notesDraftKey("T1")), null);
      assert.equal(c.s.notesDraft, "");
      assert.equal(c.s.notesDirty, false);
      assert.equal(c.s.detailOpen, false, "deferred close ran");
    } finally {
      safeStorage.removeItem(notesDraftKey("T1"));
    }
  });

  test("confirm (discard) with a deferred OPEN drops the draft, then re-opens the target", async () => {
    const c = makeCtx();
    safeStorage.setItem(notesDraftKey("T1"), "stored");
    try {
      c.s.detailOpen = true;
      c.s.detailId = "T1";
      c.s.notesDraftOwner = "T1";
      c.mod.promptUnsavedNotes({ kind: "open", taskId: "T2" });
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.confirmUnsavedNotes();
      });
      assert.equal(safeStorage.getItem(notesDraftKey("T1")), null, "the stored draft for the abandoned task is gone");
      assert.equal(c.s.detailId, "T2");
      // The re-open repopulates the scratchpad from T2's payload (no stored draft there):
      assert.equal(c.s.notesDraftOwner, "T2");
      assert.equal(c.s.notesDirty, false);
    } finally {
      safeStorage.removeItem(notesDraftKey("T1"));
    }
  });

  test("saveNotes: no detail or saving → no-op", async () => {
    const c = makeCtx();
    let hits = 0;
    await withApi("putNotes", async () => { hits++; }, async () => {
      await c.mod.saveNotes();
      c.s.detail = makeTask("T1");
      c.s.savingNotes = true;
      await c.mod.saveNotes();
    });
    assert.equal(hits, 0);
  });

  test("saveNotes AC-3 guard: cross-task draft is discarded + refreshed, never PUT", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    c.s.notesDraftOwner = "SOMEONE-ELSE";
    let hits = 0;
    await withApi("putNotes", async () => { hits++; }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.saveNotes();
      });
    });
    assert.equal(hits, 0);
    assert.equal(c.s.notesDraft, "");
    assert.deepEqual(c.calls.toasts.map((t) => t.kind), ["error"]);
    assert.ok(c.calls.toasts[0].msg.includes("different task"));
  });

  test("saveNotes happy path: PUT + storage cleared + refresh + loadBoard(force)", async () => {
    const c = makeCtx();
    safeStorage.setItem(notesDraftKey("T1"), "stored");
    try {
      c.s.detail = makeTask("T1");
      c.s.notesDraft = "final";
      let putSeen: unknown;
      await withApi("putNotes", async (id: string, body: unknown) => { putSeen = [id, body]; }, async () => {
        await withApi("getTask", async (x: string) => makeTask(x), async () => {
          await c.mod.saveNotes();
        });
      });
      assert.deepEqual(putSeen, ["T1", { actor: ACTOR, notes: "final" }]);
      assert.equal(safeStorage.getItem(notesDraftKey("T1")), null);
      assert.equal(c.s.notesDirty, false);
      assert.deepEqual(c.calls.loadBoard, [true]);
    } finally {
      safeStorage.removeItem(notesDraftKey("T1"));
    }
  });

  test("saveNotes 409 version conflict: info toast, draft preserved for review", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    c.s.notesDirty = true;
    c.s.notesDraftOwner = "T1";
    c.s.notesDraft = "my version";
    await withApi("putNotes", async () => { throw new ApiError(409, { error: "version_conflict", current_version: 8, current: makeSummary("T1") }); }, async () => {
      await withApi("getTask", async (id: string) => makeTask(id), async () => {
        await c.mod.saveNotes();
      });
    });
    // notesDirty was still true when the re-read landed → draft untouched
    assert.equal(c.s.notesDraft, "my version");
    const t = c.calls.toasts[c.calls.toasts.length - 1];
    assert.equal(t.kind, "info");
    assert.ok(t.msg.includes("changed elsewhere"));
  });

  test("saveNotes other error → error toast", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    await withApi("putNotes", async () => { throw new ApiError(500, { error: "boom", message: "down" }); }, async () => {
      await c.mod.saveNotes();
    });
    assert.equal(c.calls.toasts[0].kind, "error");
  });

  test("saveNotes failure leaves savingNotes cleared (finally)", async () => {
    const c = makeCtx();
    c.s.detail = makeTask("T1");
    await withApi("putNotes", async () => { throw new ApiError(500, { error: "boom" }); }, async () => {
      await c.mod.saveNotes();
    });
    assert.equal(c.s.savingNotes, false);
  });
});
