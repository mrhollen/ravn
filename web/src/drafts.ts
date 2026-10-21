// Draft auto-save helpers for the "Write / Preview" markdown textareas in the
// task-detail drawer (description + comment). Pure functions — no DOM, no app
// state; storage access lives in the caller via safeStorage.
//
// localStorage keys (namespaced, per-task):
//   ravn.draft.task.<taskId>.description  → raw description draft string
//   ravn.draft.task.<taskId>.comment      → JSON { body: string, kind: CommentKind }
//
// Drafts are deliberately NOT cleared on drawer close / navigation / reload —
// that persistence IS the feature. They are removed only on a successful
// submit (saveTaskEdit / postComment) or an explicit discard of the edit.

import type { CommentKind } from "./types";
import { COMMENT_KINDS } from "./types";

const DRAFT_PREFIX = "ravn.draft.task.";

/** Key for a task's auto-saved description draft. */
export function descriptionDraftKey(taskId: string): string {
  return `${DRAFT_PREFIX}${taskId}.description`;
}

/** Key for a task's auto-saved comment draft (JSON body+kind). */
export function commentDraftKey(taskId: string): string {
  return `${DRAFT_PREFIX}${taskId}.comment`;
}

/** The persisted shape of an in-progress comment. */
export interface CommentDraft {
  body: string;
  kind: CommentKind;
}

/** Serialize a comment draft to the JSON string stored under commentDraftKey. */
export function serializeCommentDraft(draft: CommentDraft): string {
  return JSON.stringify({ body: draft.body, kind: draft.kind });
}

function isCommentKind(v: unknown): v is CommentKind {
  return typeof v === "string" && (COMMENT_KINDS as readonly string[]).includes(v);
}

/** Parse a stored comment-draft payload.
 *  - null/empty → null (nothing was saved)
 *  - valid JSON object with a `body` string → restored; an unknown/missing kind
 *    falls back to "progress"
 *  - legacy/fallback shape: a bare JSON string stores body only → kind "progress"
 *  - anything unparseable or missing `body` → null (never throws) */
export function deserializeCommentDraft(raw: string | null): CommentDraft | null {
  if (raw === null || raw.trim() === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed === "string") {
    return parsed.trim() === "" ? null : { body: parsed, kind: "progress" };
  }
  if (parsed && typeof parsed === "object" && typeof (parsed as { body?: unknown }).body === "string") {
    const body = (parsed as { body: string }).body;
    const kind = isCommentKind((parsed as { kind?: unknown }).kind)
      ? (parsed as { kind: CommentKind }).kind
      : "progress";
    return { body, kind };
  }
  return null;
}
