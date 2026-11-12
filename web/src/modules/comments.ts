// Comments drawer section — state + handlers (Phase 2, batch C).
//
// Extracted from app.ts as a plain factory module (same pattern as acs.ts).
// All five bound state fields (commentForm/commentShowPreview/
// postingComment/loadingOlderComments/totalComments) are template-bound in
// index.html so they stay flat plain root fields; the module reaches them
// through CommentsDeps and owns no closure-internal state. Bodies moved
// VERBATIM — `this.` rewritten to deps accessors, otherwise byte-identical.
// app.ts keeps thin delegation shims (openTask's cascade writes
// totalComments/loadingOlderComments as plain fields and calls the
// restore/init shims; drafts.test.ts pokes commentForm +
// restoreCommentDraftForTask/saveCommentDraft through the surface).
//
// Mutation note: mergeComments reassigns `detail.comments` (property write on
// the shared object) and postComment clears `commentForm.body` — both work
// through the state-IN getters because they mutate the same objects the root
// holds; only whole-field reassignments go through setters.

import { api, errMsg } from "../api";
import type { ActorInput, CommentKind, CommentRow, TaskDetail } from "../types";
import { safeStorage } from "../util";
import {
  commentDraftKey,
  deserializeCommentDraft,
  serializeCommentDraft,
} from "../drafts";

export interface CommentFormState {
  kind: CommentKind;
  body: string;
}

export interface CommentsDeps {
  // -- state-in ---------------------------------------------------------
  detail: () => TaskDetail | null;
  commentForm: () => CommentFormState;
  commentShowPreview: () => boolean;
  postingComment: () => boolean;
  loadingOlderComments: () => boolean;
  totalComments: () => number;

  // -- state-out --------------------------------------------------------
  setCommentForm: (f: CommentFormState) => void;
  setCommentShowPreview: (v: boolean) => void;
  setPostingComment: (v: boolean) => void;
  setLoadingOlderComments: (v: boolean) => void;
  setTotalComments: (v: number) => void;

  // -- hooks ------------------------------------------------------------
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
}

export function createCommentsModule(deps: CommentsDeps) {
  /** Reset-then-restore for a task OPEN (AC 2): the form goes back to
   *  defaults FIRST so one task's unsaved comment can never leak into the
   *  next, then a stored draft under THIS id (JSON body+kind) is re-applied.
   *  Called from openTask(), which every drawer/full-page open funnels
   *  through — drafts stay in storage across close/navigation/reload. */
  function restoreCommentDraftForTask(id: string): void {
    deps.setCommentShowPreview(false);
    deps.setCommentForm({ kind: "progress", body: "" });
    const parsed = deserializeCommentDraft(safeStorage.getItem(commentDraftKey(id)));
    if (parsed) deps.setCommentForm({ body: parsed.body, kind: parsed.kind });
  }

  /** Debounced textarea-input handler (@input.debounce.400ms): persists the
   *  CURRENT comment draft (body + selected type as JSON) under the loaded
   *  task's id (AC 2). No task loaded → no-op; blank body → key removed. */
  function saveCommentDraft(): void {
    const id = deps.detail()?.id;
    if (!id) return;
    const raw = deps.commentForm().body ?? "";
    if (raw.trim() !== "") safeStorage.setItem(commentDraftKey(id), serializeCommentDraft({ body: raw, kind: deps.commentForm().kind }));
    else safeStorage.removeItem(commentDraftKey(id));
  }

  function hasMoreOlderComments(): boolean {
    return !!deps.detail() && deps.totalComments() > deps.detail()!.comments.length;
  }

  function olderCommentsRemaining(): number {
    return Math.max(0, deps.totalComments() - (deps.detail()?.comments.length ?? 0));
  }

  async function initComments(id: string): Promise<void> {
    try {
      const res = await api.listComments(id, { limit: 50, offset: 0 });
      if (deps.detail() && deps.detail()!.id === id) {
        deps.setTotalComments(res.total);
        mergeComments(res.comments);
      }
    } catch {
      /* non-fatal */
    }
  }

  function mergeComments(incoming: CommentRow[]): void {
    const d = deps.detail();
    if (!d) return;
    const map = new Map<string, CommentRow>();
    for (const c of d.comments) {
      map.set(c.id, c);
    }
    for (const c of incoming) {
      map.set(c.id, c);
    }
    const sorted = Array.from(map.values()).sort((a, b) => {
      const ta = Date.parse(a.created_at);
      const tb = Date.parse(b.created_at);
      if (ta !== tb) return ta - tb;
      return a.id.localeCompare(b.id);
    });
    d.comments = sorted;
    if (d.comments.length > deps.totalComments()) {
      deps.setTotalComments(d.comments.length);
    }
  }

  async function loadOlderComments(): Promise<void> {
    const d = deps.detail();
    if (!d || deps.loadingOlderComments()) return;
    deps.setLoadingOlderComments(true);
    try {
      const res = await api.listComments(d.id, {
        offset: d.comments.length,
        limit: 50,
      });
      deps.setTotalComments(res.total);
      const cur = deps.detail();
      if (cur && cur.id === d.id) {
        mergeComments(res.comments);
      }
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setLoadingOlderComments(false);
    }
  }

  /** Append a typed comment (no task version bump). Appends are NOT
   *  idempotent — on network error we don't auto-retry. */
  async function postComment(): Promise<void> {
    const t = deps.detail();
    const body = deps.commentForm().body.trim();
    if (!t || !body || deps.postingComment()) return;
    deps.setPostingComment(true);
    try {
      const c = await api.addComment(t.id, {
        actor: deps.actor(),
        kind: deps.commentForm().kind,
        body,
      });
      const d = deps.detail();
      if (d && d.id === t.id) {
        mergeComments([c]);
        deps.setTotalComments(Math.max(deps.totalComments() + 1, d.comments.length));
      }
      deps.commentForm().body = "";
      // Comment posted → the local draft has served its purpose; clear it so
      // re-opening this task never resurrects text already on screen (AC 3).
      safeStorage.removeItem(commentDraftKey(t.id));
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setPostingComment(false);
    }
  }

  return {
    restoreCommentDraftForTask,
    saveCommentDraft,
    hasMoreOlderComments,
    olderCommentsRemaining,
    initComments,
    mergeComments,
    loadOlderComments,
    postComment,
  };
}

export type CommentsModule = ReturnType<typeof createCommentsModule>;
