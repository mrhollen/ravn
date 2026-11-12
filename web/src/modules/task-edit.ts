// Task edit module — Phase 2 batch B slice "task-edit" of the frontend
// modularization epic. Owns, moved from app.ts:
//   1. startEditing — seed the draft from the loaded task (calendar-day due
//      via the shared helper) and take the pristine snapshot, layering a
//      stored description draft over the draft only (AC 2);
//   2. cancelEditing + the discard guard pair (editDraftDirty /
//      discardEditDraftIfConfirmed — confirmed discards also drop the local
//      description draft so it can't resurrect on the next session, AC 3);
//   3. the description draft persistence pair (saveDescriptionDraft debounced
//      @input handler + clearDescriptionDraft) over safeStorage;
//   4. saveTaskEdit — payload assembly with due/assignee set-or-clear
//      semantics and the version-conflict retry that re-reads the fresh task
//      (applyDetail hook), preserves the draft and retries once.
// Every body moved verbatim from app.ts except `this.` → closure/deps renames
// (see docs/FRONTEND_ARCHITECTURE.md §2).
//
// State-ownership note (Alpine constraint): every template-bound name
// (editingTask, savingEdit, descShowPreview, editDraft) and the snapshot
// editDraftOriginal — never rendered, but poked by web/test/drafts.test.ts, so
// it stays in the flat namespace too — live on the root, touched here only via
// the typed accessors on TaskEditDeps. This module keeps NO closure-internal
// state; there is no dialog focus session to own.

import { ApiError, api, errMsg } from "../api";
import { descriptionDraftKey } from "../drafts";
import { dateInputToIso, dueCalendarDate, safeStorage } from "../util";
import type { ActorInput, TaskDetail, UpdateTaskReq } from "../types";

// ----------------------------------------------------------------------- types
/** The six task-edit form fields (the template x-models the editable ones). */
export interface TaskEditDraftState {
  title: string;
  description: string;
  priority: number;
  due: string;
  assigneeName: string;
  assigneeType: "human" | "agent";
}

// ----------------------------------------------------------------------- deps
export interface TaskEditDeps {
  // state-in
  detail: () => TaskDetail | null;
  editDraft: () => TaskEditDraftState;
  editDraftOriginal: () => TaskEditDraftState;
  editingTask: () => boolean;
  savingEdit: () => boolean;
  // state-out
  setEditingTask: (v: boolean) => void;
  setSavingEdit: (v: boolean) => void;
  setDescShowPreview: (v: boolean) => void;
  setEditDraft: (d: TaskEditDraftState) => void;
  setEditDraftOriginal: (d: TaskEditDraftState) => void;
  // hooks — root-owned orchestration that stays outside this module
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
  confirmDiscard: (message: string) => boolean;
  refreshDetail: (force?: boolean) => Promise<unknown>;
  loadBoard: (force?: boolean) => Promise<unknown>;
  /** Re-land a freshly fetched task on the drawer state (409-retry path). */
  applyDetail: (t: TaskDetail) => void;
}

export function createTaskEditModule(deps: TaskEditDeps) {
  function startEditing(): void {
    const t = deps.detail();
    if (!t) return;
    deps.setEditDraft({
      title: t.title ?? "",
      description: t.description ?? "",
      priority: t.priority ?? 0,
      // Calendar day via the shared helper — always equals what the card
      // shows, and re-saving it round-trips to the same stored day.
      due: t.due_at ? dueCalendarDate(t.due_at) : "",
      assigneeName: t.assignee?.name ?? "",
      assigneeType: (t.assignee?.type as "human" | "agent") || "human",
    });
    // Snapshot FIRST (server truth), then layer the local draft on top — see below.
    deps.setEditDraftOriginal({ ...deps.editDraft() });
    const savedDesc = safeStorage.getItem(descriptionDraftKey(t.id));
    if (savedDesc !== null && savedDesc.trim() !== "") {
      // Draft auto-save restore (AC 2): the user's unsaved local draft wins
      // over the server value, while editDraftOriginal keeps the SERVER truth
      // so dirty-detection and the discard guard stay correct.
      const d = deps.editDraft();
      d.description = savedDesc;
    }
    // A fresh edit session always starts in Write mode (AC 1).
    deps.setDescShowPreview(false);
    deps.setEditingTask(true);
  }

  function cancelEditing(): void {
    deps.setEditingTask(false);
    // The Cancel button is an explicit discard of the in-flight edit — drop
    // the local description draft with it (AC 3). Idempotent if the draft was
    // already cleared by a confirmed-discard path.
    clearDescriptionDraft();
  }

  /** True when the in-flight task edit differs from the values loaded when
   *  edit mode started. Gated on editingTask so a stale snapshot can never
   *  fire once save/cancel has exited edit mode. */
  function editDraftDirty(): boolean {
    if (!deps.editingTask()) return false;
    const d = deps.editDraft();
    const o = deps.editDraftOriginal();
    return (
      d.title !== o.title ||
        d.description !== o.description ||
        d.priority !== o.priority ||
        d.due !== o.due ||
        d.assigneeName !== o.assigneeName ||
        d.assigneeType !== o.assigneeType
    );
  }

  /** Ask before throwing away an in-flight task edit (shared by the drawer's
   *  close path and Escape). True = safe to proceed with the discard. */
  function discardEditDraftIfConfirmed(): boolean {
    if (!editDraftDirty()) return true;
    const ok = deps.confirmDiscard("Discard unsaved task edits? Changes to this task will be lost.");
    // An explicitly CONFIRMED discard is exactly when the local description
    // draft stops existing (AC 3): it lives in storage, so leaving it would
    // resurrect itself on the next startEditing().
    if (ok) clearDescriptionDraft();
    return ok;
  }

  /** Debounced textarea-input handler (@input.debounce.400ms): persists the
   *  CURRENT description draft under the loaded task's id (AC 2). No task
   *  loaded → no-op. Blank value → removes the key so nothing stale lingers. */
  function saveDescriptionDraft(): void {
    const id = deps.detail()?.id;
    if (!id) return;
    const raw = deps.editDraft().description ?? "";
    if (raw.trim() !== "") safeStorage.setItem(descriptionDraftKey(id), raw);
    else safeStorage.removeItem(descriptionDraftKey(id));
  }

  /** Drop the loaded task's description draft from storage (AC 3). No-op when
   *  no task is loaded; safe to call repeatedly. */
  function clearDescriptionDraft(): void {
    const id = deps.detail()?.id;
    if (!id) return;
    safeStorage.removeItem(descriptionDraftKey(id));
  }

  async function saveTaskEdit(): Promise<void> {
    const t = deps.detail();
    const title = deps.editDraft().title.trim();
    if (!t || !title || deps.savingEdit()) return;

    // Captured BEFORE any await: the draft that gets cleared on success is
    // the one for THIS task, even if detail moves during the flight (AC 3).
    const descKey = descriptionDraftKey(t.id);
    deps.setSavingEdit(true);
    const req: UpdateTaskReq = {
      actor: deps.actor(),
      version: t.version,
      title,
      description: deps.editDraft().description.trim(),
      priority: deps.editDraft().priority,
    };

    const dueIso = dateInputToIso(deps.editDraft().due);
    if (dueIso) {
      req.due_at = dueIso;
    } else if (t.due_at) {
      req.clear_due = true;
    }

    const assigneeName = deps.editDraft().assigneeName.trim();
    if (assigneeName) {
      req.assignee = {
        name: assigneeName,
        type: deps.editDraft().assigneeType || "human",
      };
    } else if (t.assignee) {
      req.clear_assignee = true;
    }

    try {
      await api.updateTask(t.id, req);
      deps.setEditingTask(false);
      // Save succeeded → the description is server truth now; the local draft
      // must not survive (AC 3).
      safeStorage.removeItem(descKey);
      await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
    } catch (e) {
      if (ApiError.isVersionConflict(e)) {
        try {
          const fresh = await api.getTask(t.id, { commentLimit: 50 });
          deps.applyDetail(fresh);
          // Re-apply once using fresh version while preserving user's editDraft
          const retryReq: UpdateTaskReq = {
            ...req,
            version: fresh.version,
            assignee: assigneeName
              ? { name: assigneeName, type: deps.editDraft().assigneeType || "human" }
              : undefined,
            clear_assignee: !assigneeName && !!fresh.assignee,
          };
          await api.updateTask(t.id, retryReq);
          deps.setEditingTask(false);
          // 409-retry also landed the save → clear the draft exactly once.
          safeStorage.removeItem(descKey);
          await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
          deps.toast("Changed elsewhere — retried with the fresh version.", "info");
        } catch (e2) {
          await deps.refreshDetail(true).catch(() => undefined);
          // Stay in edit mode with editDraft preserved across conflict
          if (ApiError.isVersionConflict(e2)) {
            deps.toast("Changed elsewhere — refreshed (your edit was not applied).", "info");
          } else {
            deps.toast(errMsg(e2), "error");
          }
        }
      } else {
        deps.toast(errMsg(e), "error");
      }
    } finally {
      deps.setSavingEdit(false);
    }
  }

  return {
    startEditing,
    cancelEditing,
    editDraftDirty,
    discardEditDraftIfConfirmed,
    saveDescriptionDraft,
    clearDescriptionDraft,
    saveTaskEdit,
  };
}

export type TaskEditModule = ReturnType<typeof createTaskEditModule>;
