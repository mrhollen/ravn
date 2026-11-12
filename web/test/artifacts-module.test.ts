// Unit tests for src/modules/artifacts.ts — the artifact attachment + preview
// surface extracted from web/src/app.ts (Phase 2 batch C). Covers: the stored-
// media predicates (isStored*), file-selection kind inference, upload guard/
// inference/persist flows (uploadFileArtifact, handleArtifactDrop, handlePaste),
// attach (file vs ref paths), preview modal open/close, text-preview toggle +
// fetch lifecycle, delete (confirm guard, same-task guard, cache cleanup) and
// copyArtifactRef (clipboard success/failure + copiedId timer).
//
// Pure module: mutable stub state wired into ArtifactsDeps accessors + counters
// for every out-bound write + monkey-patched api methods / global fetch /
// document, all restored in finally blocks (same pattern as comments-module).

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { createArtifactsModule } from "../src/modules/artifacts";
import { api } from "../src/api";
import type { ActorInput, ArtifactKind, ArtifactRow, TaskDetail } from "../src/types";

// ----------------------------------------------------------------------- helpers
function makeArtifact(id: string, over: Partial<ArtifactRow> = {}): ArtifactRow {
  return { id, kind: "other", ref: `/artifacts/${id}`, summary: `summary ${id}`, actor: { name: "u", type: "human" }, created_at: "2026-01-01T00:00:00Z", ...over };
}

function makeTask(over: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: "T1",
    project_id: "P1",
    seq: 1,
    title: "t",
    description: "",
    status: "todo",
    priority: 2,
    due_at: null,
    created_by: { name: "u", type: "human" },
    version: 3,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    completed_by: null,
    completed_at: null,
    assignee: undefined,
    acceptance_criteria: [],
    comments: [],
    artifacts: [makeArtifact("a1"), makeArtifact("a2")],
    ...over,
  };
}

const ACTOR: ActorInput = { name: "tester", type: "agent" };

interface Ctx {
  mod: ReturnType<typeof createArtifactsModule>;
  state: Record<string, unknown>;
  toasts: Array<{ msg?: string; kind?: string }>;
  confirmMessages: string[];
  confirmAnswer: boolean;
}

function makeCtx(detail: TaskDetail | null): Ctx {
  const state: Record<string, unknown> = {
    detail,
    artifactForm: { open: false, kind: "url" as ArtifactKind, ref: "", summary: "", file: null as File | null },
    attachingArtifact: false,
    previewModal: { open: false, src: "", alt: "" },
    copiedArtifactId: null as string | null,
    copiedArtifactTimer: null as ReturnType<typeof setTimeout> | null,
    deletingArtifactId: "",
    artifactTextExpanded: {} as Record<string, boolean>,
    artifactTextContent: {} as Record<string, string>,
    artifactTextLoading: {} as Record<string, boolean>,
    artifactTextError: {} as Record<string, string>,
    artifactDragOver: false,
    detailOpen: true,
  };
  const ctx: Ctx = { mod: undefined as never, state, toasts: [], confirmMessages: [], confirmAnswer: true };

  ctx.mod = createArtifactsModule({
    // state-in
    detail: () => state.detail as TaskDetail | null,
    artifactForm: () => state.artifactForm as (typeof state)["artifactForm"],
    attachingArtifact: () => state.attachingArtifact as boolean,
    previewModal: () => state.previewModal as (typeof state)["previewModal"],
    copiedArtifactId: () => state.copiedArtifactId as string | null,
    copiedArtifactTimer: () => state.copiedArtifactTimer as ReturnType<typeof setTimeout> | null,
    deletingArtifactId: () => state.deletingArtifactId as string,
    artifactTextExpanded: () => state.artifactTextExpanded as Record<string, boolean>,
    artifactTextContent: () => state.artifactTextContent as Record<string, string>,
    artifactTextLoading: () => state.artifactTextLoading as Record<string, boolean>,
    artifactTextError: () => state.artifactTextError as Record<string, string>,
    artifactDragOver: () => state.artifactDragOver as boolean,
    detailOpen: () => state.detailOpen as boolean,
    // state-out
    setDetail: (d) => { state.detail = d; },
    setArtifactForm: (f) => { state.artifactForm = f; },
    setAttachingArtifact: (v) => { state.attachingArtifact = v; },
    setPreviewModal: (p) => { state.previewModal = p; },
    setCopiedArtifactId: (id) => { state.copiedArtifactId = id; },
    setCopiedArtifactTimer: (t) => { state.copiedArtifactTimer = t; },
    setDeletingArtifactId: (v) => { state.deletingArtifactId = v; },
    setArtifactTextExpanded: (r) => { state.artifactTextExpanded = r; },
    setArtifactTextContent: (r) => { state.artifactTextContent = r; },
    setArtifactTextLoading: (r) => { state.artifactTextLoading = r; },
    setArtifactTextError: (r) => { state.artifactTextError = r; },
    setArtifactDragOver: (v) => { state.artifactDragOver = v; },
    // hooks
    toast: (msg, kind) => { ctx.toasts.push({ msg, kind }); return 0; },
    actor: () => ACTOR,
    isWebHref: (s) => typeof s === "string" && /^https?:\/\//.test(s),
    confirmDiscard: (msg) => { ctx.confirmMessages.push(msg); return ctx.confirmAnswer; },
  });
  return ctx;
}

async function withApi<T>(name: string, impl: unknown, fn: () => Promise<T>): Promise<T> {
  const orig = (api as Record<string, unknown>)[name];
  (api as Record<string, unknown>)[name] = impl;
  try { return await fn(); } finally { (api as Record<string, unknown>)[name] = orig; }
}

/** Patch global fetch for the duration of fn. */
async function withFetch<T>(impl: (...args: unknown[]) => Promise<unknown>, fn: () => Promise<T>): Promise<T> {
  const orig = globalThis.fetch;
  (globalThis as Record<string, unknown>).fetch = impl;
  try { return await fn(); } finally { (globalThis as Record<string, unknown>).fetch = orig; }
}

/** openImagePreview/closeImagePreview/resetArtifactForm touch document.
 *  A getElementById-returns-null stub is enough for the node test env. */
function withMinDoc<T>(fn: () => T): T {
  const orig = (globalThis as Record<string, unknown>).document;
  (globalThis as Record<string, unknown>).document = { getElementById: () => null };
  try { return fn(); } finally {
    if (orig === undefined) delete (globalThis as Record<string, unknown>).document;
    else (globalThis as Record<string, unknown>).document = orig;
  }
}

async function withMinDocAsync<T>(fn: () => Promise<T>): Promise<T> {
  const orig = (globalThis as Record<string, unknown>).document;
  (globalThis as Record<string, unknown>).document = { getElementById: () => null };
  try { return await fn(); } finally {
    if (orig === undefined) delete (globalThis as Record<string, unknown>).document;
    else (globalThis as Record<string, unknown>).document = orig;
  }
}

function imgFile(name = "shot.png"): File { return new File(["x"], name, { type: "image/png" }); }
function logFile(name = "build.log"): File { return new File(["log"], name, { type: "text/plain" }); }

// ----------------------------------------------------------------------- predicates
describe("isStored* predicates", () => {
  const c = makeCtx(makeTask());
  test("isStoredArtifact recognizes both storage prefixes only", () => {
    assert.equal(c.mod.isStoredArtifact("/artifacts/abc"), true);
    assert.equal(c.mod.isStoredArtifact("/api/v1/artifacts/abc"), true);
    assert.equal(c.mod.isStoredArtifact("https://example.com/x"), false);
    assert.equal(c.mod.isStoredArtifact(null), false);
  });

  test("isStoredImage: screenshot kind OR image extension in ref/summary", () => {
    assert.equal(c.mod.isStoredImage({ kind: "screenshot", ref: "/artifacts/x.bin" }), true);
    assert.equal(c.mod.isStoredImage({ kind: "other", ref: "/artifacts/x.png" }), true);
    assert.equal(c.mod.isStoredImage({ kind: "other", ref: "/artifacts/x", summary: "see x.jpg" }), true);
    assert.equal(c.mod.isStoredImage({ kind: "other", ref: "/artifacts/x.bin" }), false);
    assert.equal(c.mod.isStoredImage({ kind: "screenshot", ref: "https://e.com/x.png" }), false, "non-stored ref never counts");
  });

  test("isStoredVideo / isStoredAudio / isStoredNonImage follow the same shape rules", () => {
    assert.equal(c.mod.isStoredVideo({ kind: "other", ref: "/artifacts/x.mp4" }), true);
    assert.equal(c.mod.isStoredAudio({ kind: "other", summary: "x.flac", ref: "/artifacts/x" }), true);
    assert.equal(c.mod.isStoredNonImage({ kind: "log", ref: "/artifacts/x.log" }), true);
    assert.equal(c.mod.isStoredNonImage({ kind: "screenshot", ref: "/artifacts/x.png" }), false);
  });

  test("isStoredText: fetchable (stored OR web href) AND text-like", () => {
    assert.equal(c.mod.isStoredText({ kind: "log", ref: "/artifacts/x" }), true);
    assert.equal(c.mod.isStoredText({ kind: "other", ref: "https://example.com/a.md" }), true, "web href + .md");
    assert.equal(c.mod.isStoredText({ kind: "other", ref: "ftp://e.com/a.md" }), false, "not a web href (isWebHref hook)");
    assert.equal(c.mod.isStoredText({ kind: "screenshot", ref: "/artifacts/x.png" }), false);
    assert.equal(c.mod.isStoredText({ kind: "log", ref: "" } as never), false);
  });
});

// ----------------------------------------------------------------------- file selection
describe("onArtifactFileSelected", () => {
  test("image file → kind screenshot + file stored on the form (in place)", () => {
    const c = makeCtx(makeTask());
    c.mod.onArtifactFileSelected({ target: { files: [imgFile()] } } as never);
    const f = c.state.artifactForm as { file: File | null; kind: ArtifactKind };
    assert.ok(f.file instanceof File);
    assert.equal(f.kind, "screenshot");
  });

  test("non-image file → kind other", () => {
    const c = makeCtx(makeTask());
    c.mod.onArtifactFileSelected({ target: { files: [logFile()] } } as never);
    assert.equal((c.state.artifactForm as { kind: ArtifactKind }).kind, "other");
  });

  test("no file → form.file stays null (parity: kind untouched)", () => {
    const c = makeCtx(makeTask());
    (c.state.artifactForm as { kind: ArtifactKind }).kind = "screenshot";
    c.mod.onArtifactFileSelected({ target: { files: [] } } as never);
    const f = c.state.artifactForm as { file: File | null; kind: ArtifactKind };
    assert.equal(f.file, null);
    assert.equal(f.kind, "screenshot");
  });
});

// ----------------------------------------------------------------------- uploadFileArtifact
describe("uploadFileArtifact", () => {
  test("guards: no detail or already busy → null without calling api", async () => {
    let calls = 0;
    const impl = async () => { calls++; return makeArtifact("n"); };
    const c1 = makeCtx(null);
    await withApi("uploadArtifact", impl, async () => { assert.equal(await c1.mod.uploadFileArtifact(imgFile()), null); });
    assert.equal(calls, 0);

    const c2 = makeCtx(makeTask());
    (c2.state as { attachingArtifact: boolean }).attachingArtifact = true;
    await withApi("uploadArtifact", impl, async () => { assert.equal(await c2.mod.uploadFileArtifact(imgFile()), null); });
    assert.equal(calls, 0);
  });

  test("infers kind from file type/name when not given; explicit kind wins", async () => {
    const seen: Array<{ kind?: unknown }> = [];
    const impl = async (_id: string, _f: File, kind?: unknown) => { seen.push({ kind }); return makeArtifact("n", { kind }); };

    let c = makeCtx(makeTask());
    await withApi("uploadArtifact", impl, async () => { await c.mod.uploadFileArtifact(imgFile()); });
    assert.equal(seen[0].kind, "screenshot");

    c = makeCtx(makeTask());
    await withApi("uploadArtifact", impl, async () => { await c.mod.uploadFileArtifact(logFile()); });
    assert.equal(seen[1].kind, "log");

    c = makeCtx(makeTask());
    await withApi("uploadArtifact", impl, async () => { await c.mod.uploadFileArtifact(logFile(), undefined, "trace"); });
    assert.equal(seen[2].kind, "trace");
  });

  test("success → row returned, detail patched under same-task guard, toast, busy reset", async () => {
    const c = makeCtx(makeTask()); // 2 artifacts
    await withApi("uploadArtifact", async (_id: string, _f: File, kind: unknown, summary?: unknown) => makeArtifact("a3", { kind, summary }), async () => {
      const row = await c.mod.uploadFileArtifact(imgFile(), undefined);
      assert.ok(row && row.id === "a3");
      const d = c.state.detail as TaskDetail;
      assert.equal(d.artifacts.length, 3);
      assert.equal(d.artifacts[2].id, "a3");
      assert.equal(c.state.attachingArtifact, false);
    });
    assert.ok((c.toasts[0].msg ?? "").startsWith("Attached"));
  });

  test("API failure → error toast, null returned, busy reset (parity)", async () => {
    const c = makeCtx(makeTask());
    await withApi("uploadArtifact", async () => { throw new Error("500"); }, async () => {
      assert.equal(await c.mod.uploadFileArtifact(imgFile()), null);
    });
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.attachingArtifact, false);
  });
});

// ----------------------------------------------------------------------- handleArtifactDrop / handlePaste
describe("handleArtifactDrop", () => {
  test("clears dragOver and uploads each dropped file in order", async () => {
    const c = makeCtx(makeTask());
    (c.state as { artifactDragOver: boolean }).artifactDragOver = true;
    const files = [imgFile(), logFile()];
    let calls = 0;
    await withApi("uploadArtifact", async () => { calls++; return makeArtifact(`n${calls}`); }, async () => {
      await c.mod.handleArtifactDrop({ dataTransfer: { files } } as never);
    });
    assert.equal(c.state.artifactDragOver, false);
    assert.equal(calls, 2);
    assert.equal((c.state.detail as TaskDetail).artifacts.length, 4);
  });

  test("no files → dragOver still cleared, no api call", async () => {
    const c = makeCtx(makeTask());
    let calls = 0;
    await withApi("uploadArtifact", async () => { calls++; return makeArtifact("n"); }, async () => {
      await c.mod.handleArtifactDrop({ dataTransfer: { files: [] } } as never);
    });
    assert.equal(c.state.artifactDragOver, false);
    assert.equal(calls, 0);
  });
});

describe("handlePaste", () => {
  function pasteEvent(file: File | null) {
    const prevented = { v: false };
    const textarea = { tagName: "TEXTAREA", value: "abc", selectionStart: 3, selectionEnd: 3, dispatchEvent() {} };
    return {
      e: {
        preventDefault() { prevented.v = true; },
        clipboardData: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
        target: textarea,
      } as never,
      prevented,
      textarea,
    };
  }

  test("guards: detail not open / no detail → ignored without preventDefault", async () => {
    let calls = 0;
    const impl = async () => { calls++; return makeArtifact("n"); };
    const c1 = makeCtx(makeTask());
    (c1.state as { detailOpen: boolean }).detailOpen = false;
    const p1 = pasteEvent(imgFile());
    await withApi("uploadArtifact", impl, async () => { await c1.mod.handlePaste(p1.e); });
    assert.equal(p1.prevented.v, false);

    const c2 = makeCtx(null);
    const p2 = pasteEvent(imgFile());
    await withApi("uploadArtifact", impl, async () => { await c2.mod.handlePaste(p2.e); });
    assert.equal(calls, 0);
  });

  test("image item → preventDefault + upload as screenshot + markdown ref spliced into textarea", async () => {
    const c = makeCtx(makeTask());
    const p = pasteEvent(imgFile("clip.png"));
    await withApi("uploadArtifact", async (_id: string, f: File, kind?: unknown, summary?: unknown) => makeArtifact("a3", { ref: "/artifacts/a3", summary }), async () => {
      await c.mod.handlePaste(p.e);
    });
    assert.equal(p.prevented.v, true);
    assert.equal((c.state.detail as TaskDetail).artifacts.length, 3);
    assert.match(p.textarea.value, /!\[screenshot\]\(\/artifacts\/a3\)/);
  });
});

// ----------------------------------------------------------------------- attach
describe("attachArtifact", () => {
  test("guards: no detail / nothing to attach / busy → no api call", async () => {
    let calls = 0;
    const impl = async () => { calls++; return makeArtifact("n"); };

    const c1 = makeCtx(null);
    await withApi("addArtifact", impl, async () => { await c1.mod.attachArtifact(); });
    assert.equal(calls, 0);

    const c2 = makeCtx(makeTask());
    (c2.state.artifactForm as { ref: string }).ref = "   ";
    await withApi("addArtifact", impl, async () => { await c2.mod.attachArtifact(); });
    assert.equal(calls, 0);

    (c2.state.artifactForm as { ref: string }).ref = "https://e.com/x";
    (c2.state as { attachingArtifact: boolean }).attachingArtifact = true;
    await withApi("addArtifact", impl, async () => { await c2.mod.attachArtifact(); });
    assert.equal(calls, 0);
  });

  test("ref path → addArtifact(kind, ref, summary-or-undefined), detail patched, form reset", async () => {
    const c = makeCtx(makeTask());
    (c.state.artifactForm as { kind: ArtifactKind; ref: string; summary: string }).kind = "url";
    (c.state.artifactForm as { kind: ArtifactKind; ref: string; summary: string }).ref = " https://e.com/x ";
    (c.state.artifactForm as { kind: ArtifactKind; ref: string; summary: string }).summary = "  note ";
    let seen: Record<string, unknown> = {};
    await withMinDocAsync(async () => withApi("addArtifact", async (_id: string, ...rest: unknown[]) => { seen = { rest }; return makeArtifact("a3"); }, async () => {
      await c.mod.attachArtifact();
    }));
    const [kind, ref, summary] = seen.rest as Array<unknown>;
    assert.equal(kind, "url");
    assert.equal(ref, "https://e.com/x", "ref trimmed");
    assert.equal(summary, "note", "summary trimmed");
    assert.equal((c.state.detail as TaskDetail).artifacts.length, 3);
    const f = c.state.artifactForm as { ref: string; file: File | null };
    assert.equal(f.ref, "");
    assert.equal(f.file, null, "form reset after success");
  });

  test("file path → uploadArtifact with form kind/summary", async () => {
    const c = makeCtx(makeTask());
    (c.state.artifactForm as { kind: ArtifactKind; summary: string }).kind = "screenshot";
    (c.state.artifactForm as { kind: ArtifactKind; summary: string }).summary = "";
    (c.state.artifactForm as { file: File | null }).file = imgFile();
    let seen: Record<string, unknown> = {};
    await withMinDocAsync(async () => withApi("uploadArtifact", async (_id: string, ...rest: unknown[]) => { seen = { rest }; return makeArtifact("a3"); }, async () => {
      await c.mod.attachArtifact();
    }));
    const [file, kind, summary] = seen.rest as Array<unknown>;
    assert.ok(file instanceof File);
    assert.equal(kind, "screenshot");
    assert.equal(summary, undefined, "blank summary → undefined (parity)");
  });

  test("API failure → error toast; form NOT reset (user can retry), busy reset", async () => {
    const c = makeCtx(makeTask());
    (c.state.artifactForm as { ref: string }).ref = "https://e.com/x";
    await withApi("addArtifact", async () => { throw new Error("500"); }, async () => {
      await c.mod.attachArtifact();
    });
    assert.equal(c.toasts[0].kind, "error");
    assert.equal((c.state.artifactForm as { ref: string }).ref, "https://e.com/x", "form kept on failure (parity)");
    assert.equal(c.state.attachingArtifact, false);
  });
});

// ----------------------------------------------------------------------- preview modal
describe("preview modal", () => {
  test("openImagePreview replaces the modal state (whole-field)", () => {
    const c = makeCtx(makeTask());
    withMinDoc(() => c.mod.openImagePreview("/artifacts/x.png", "alt"));
    assert.deepEqual(c.state.previewModal, { open: true, src: "/artifacts/x.png", alt: "alt" });
  });

  test("closeImagePreview clears .open in place (property write)", () => {
    const c = makeCtx(makeTask());
    (c.state as { previewModal: { open: boolean; src: string; alt: string } }).previewModal = { open: true, src: "s", alt: "a" };
    withMinDoc(() => c.mod.closeImagePreview());
    assert.equal((c.state.previewModal as { open: boolean }).open, false);
  });
});

// ----------------------------------------------------------------------- text preview toggle
describe("toggleArtifactPreview", () => {
  test("expand fetches once; collapse refetches nothing; content persisted in cache", async () => {
    const c = makeCtx(makeTask());
    let calls = 0;
    await withFetch(async () => { calls++; return { ok: true, text: async () => "BUILD OK" } as never; }, async () => {
      await c.mod.toggleArtifactPreview({ id: "a1", ref: "/artifacts/a1.txt" });
      assert.equal((c.state.artifactTextExpanded as Record<string, boolean>)["a1"], true);
      assert.equal((c.state.artifactTextContent as Record<string, string>)["a1"], "BUILD OK");

      await c.mod.toggleArtifactPreview({ id: "a1", ref: "/artifacts/a1.txt" });
      assert.equal((c.state.artifactTextExpanded as Record<string, boolean>)["a1"], false);
    });
    assert.equal(calls, 1);
    // Collapse kept the cached content (parity) — re-expanding will not fetch.
    assert.equal((c.state.artifactTextContent as Record<string, string>)["a1"], "BUILD OK");
  });

  test("HTTP error → message surfaced in artifactTextError; loading always reset", async () => {
    const c = makeCtx(makeTask());
    await withFetch(async () => ({ ok: false, status: 404, statusText: "Not Found" }) as never, async () => {
      await c.mod.toggleArtifactPreview({ id: "a1", ref: "/artifacts/a1.bin" });
    });
    assert.equal((c.state.artifactTextError as Record<string, string>)["a1"], "HTTP 404 Not Found");
    assert.equal((c.state.artifactTextLoading as Record<string, boolean>)["a1"], false);
  });

  test("fetch throwing → fallback message in artifactTextError", async () => {
    const c = makeCtx(makeTask());
    await withFetch(async () => { throw new Error("ECONNREFUSED"); }, async () => {
      await c.mod.toggleArtifactPreview({ id: "a1", ref: "/artifacts/a1.bin" });
    });
    assert.equal((c.state.artifactTextError as Record<string, string>)["a1"], "ECONNREFUSED");
  });
});

// ----------------------------------------------------------------------- delete
describe("deleteArtifact", () => {
  test("guards: no detail / already deleting → no api call", async () => {
    let calls = 0;
    const impl = async () => { calls++; };
    const c1 = makeCtx(null);
    await withApi("deleteArtifact", impl, async () => { await c1.mod.deleteArtifact("a1"); });
    assert.equal(calls, 0);

    const c2 = makeCtx(makeTask());
    (c2.state as { deletingArtifactId: string }).deletingArtifactId = "other";
    await withApi("deleteArtifact", impl, async () => { await c2.mod.deleteArtifact("a1"); });
    assert.equal(calls, 0);
  });

  test("confirmDiscard decline → aborts before any api call", async () => {
    const c = makeCtx(makeTask());
    c.confirmAnswer = false;
    let calls = 0;
    await withApi("deleteArtifact", async () => { calls++; }, async () => {
      await c.mod.deleteArtifact("a1");
    });
    assert.equal(calls, 0);
    assert.deepEqual(c.confirmMessages, ["Are you sure you want to delete this artifact?"]);
    assert.equal(c.state.deletingArtifactId, "");
  });

  test("success → api called, detail filtered under same-task guard, caches cleaned, toast", async () => {
    const c = makeCtx(makeTask());
    (c.state.artifactTextExpanded as Record<string, boolean>)["a1"] = true;
    (c.state.artifactTextContent as Record<string, string>)["a1"] = "x";
    let seen: Array<unknown> = [];
    await withApi("deleteArtifact", async (...args: unknown[]) => { seen = args; }, async () => {
      await c.mod.deleteArtifact("a1");
    });
    assert.equal(seen[0], "T1");
    assert.equal(seen[1], "a1");
    assert.deepEqual(seen[2], ACTOR);
    const d = c.state.detail as TaskDetail;
    assert.deepEqual(d.artifacts.map((a) => a.id), ["a2"]);
    assert.equal((c.state.artifactTextExpanded as Record<string, boolean>)["a1"], undefined, "expanded entry cleaned");
    assert.equal((c.state.artifactTextContent as Record<string, string>)["a1"], undefined, "content cache cleaned");
    assert.equal(c.toasts[0].msg, "Artifact deleted");
    assert.equal(c.state.deletingArtifactId, "");
  });

  test("API failure → error toast, busy reset", async () => {
    const c = makeCtx(makeTask());
    await withApi("deleteArtifact", async () => { throw new Error("500"); }, async () => {
      await c.mod.deleteArtifact("a1");
    });
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.deletingArtifactId, "");
  });
});

// ----------------------------------------------------------------------- copyArtifactRef
describe("copyArtifactRef", () => {
  /** Run fn with the document stub held until its returned promise settles. */
  async function withDocAsync<T>(fn: () => Promise<T>): Promise<T> {
    const orig = (globalThis as Record<string, unknown>).document;
    (globalThis as Record<string, unknown>).document = {
      body: { appendChild() {}, removeChild() {} },
      createElement: () => ({ value: "", setAttribute() {}, style: {}, appendChild() {}, focus() {}, select() {}, remove() {} }),
      execCommand: () => true,
    };
    try { return await fn(); } finally {
      if (orig === undefined) delete (globalThis as Record<string, unknown>).document;
      else (globalThis as Record<string, unknown>).document = orig;
    }
  }

  test("empty ref → no-op", async () => {
    const c = makeCtx(makeTask());
    await c.mod.copyArtifactRef("");
    assert.equal(c.toasts.length, 0);
    assert.equal(c.state.copiedArtifactId, null);
  });

  test("success (execCommand path) → copied id set + confirmation timer + info toast", async () => {
    const c = makeCtx(makeTask());
    await withDocAsync(async () => {
      await c.mod.copyArtifactRef("/artifacts/a1", "a1");
    });
    assert.equal(c.state.copiedArtifactId, "a1");
    assert.ok(c.state.copiedArtifactTimer !== null, "timer stored on root state");
    clearTimeout(c.state.copiedArtifactTimer as ReturnType<typeof setTimeout>); // don't leak the 2s timer
    const info = c.toasts.find((t) => t.kind === "info");
    assert.ok(info && /Copied artifact path/.test(info.msg ?? ""));
  });

  test("no clipboard available → error toast, no copied id", async () => {
    const orig = (globalThis as Record<string, unknown>).document;
    delete (globalThis as Record<string, unknown>).document;
    const c = makeCtx(makeTask());
    try {
      await c.mod.copyArtifactRef("/artifacts/a1", "a1");
    } finally {
      if (orig === undefined) delete (globalThis as Record<string, unknown>).document;
      else (globalThis as Record<string, unknown>).document = orig;
    }
    assert.equal(c.toasts[0].kind, "error");
    assert.equal(c.state.copiedArtifactId, null);
  });

  test("no id arg → still toasts success but never sets copied id", async () => {
    const c = makeCtx(makeTask());
    await withDocAsync(async () => {
      await c.mod.copyArtifactRef("/artifacts/a1");
    });
    assert.equal(c.state.copiedArtifactId, null);
    const info = c.toasts.find((t) => t.kind === "info");
    assert.ok(info && /Copied artifact path/.test(info.msg ?? ""));
  });
});
