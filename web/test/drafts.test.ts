import test from "node:test";
import assert from "node:assert/strict";

import { safeStorage } from "../src/util.ts";
import { commentDraftKey, descriptionDraftKey, deserializeCommentDraft, serializeCommentDraft } from "../src/drafts.ts";
import { ravnApp } from "../src/app.ts";

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
