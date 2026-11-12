// New task dialog module — Phase 2 batch B slice "new-task" of the frontend
// modularization epic. Owns, moved from app.ts:
//   1. the dialog lifecycle — openNewTask(status) / openNewSubtask(parentTask)
//      and the single close path (create success) closeNewTask();
//   2. the draft-state derivation + guarded close (newTaskDirty,
//      requestCloseNewTask confirm-first — user-initiated dismissals must never
//      lose a half-typed draft);
//   3. createTask — payload assembly, the version-guarded non-todo follow-up
//      move, the optional subtask relation link, the possible_duplicates
//      advisory and the post-POST refresh fan-out;
//   4. the duplicates advisory surface (clearDuplicates).
// Every body moved verbatim from app.ts except `this.` → closure/deps renames
// (see docs/FRONTEND_ARCHITECTURE.md §2).
//
// State-ownership note (Alpine constraint): every template-bound name
// (showNewTask, confirmNewTaskDiscard, newTask, taskBusy, duplicates,
// newTaskParentId, newTaskTarget — the modal header renders it via
// statusLabel()) still lives on the flat component root, touched here
// exclusively through the typed accessors on NewTaskDeps. The dialog focus
// session is INTERNAL — never rendered, never part of the flat namespace — and
// is owned in this module's closure.

import { ApiError, api, errMsg } from "../api";
import { dateInputToIso, splitLines } from "../util";
import type { DialogFocus } from "../focus";
import type { ActorInput, Status, TaskDetail, TaskSummary } from "../types";

// ----------------------------------------------------------------------- types
/** The seven new-task form fields (the template x-models every one of them). */
export interface NewTaskFormState {
  title: string;
  description: string;
  priority: number;
  due: string;
  acs: string;
  assigneeName: string;
  assigneeType: "human" | "agent";
}

// ----------------------------------------------------------------------- deps
export interface NewTaskDeps {
  // state-in
  selectedProjectId: () => string;
  detail: () => TaskDetail | null;
  newTask: () => NewTaskFormState;
  taskBusy: () => boolean;
  newTaskParentId: () => string | null;
  newTaskTarget: () => Status;
  // state-out
  setShowNewTask: (v: boolean) => void;
  setNewTaskTarget: (s: Status) => void;
  setConfirmNewTaskDiscard: (v: boolean) => void;
  setDuplicates: (t: TaskSummary[]) => void;
  setTaskBusy: (v: boolean) => void;
  setNewTask: (f: NewTaskFormState) => void;
  setNewTaskParentId: (v: string | null) => void;
  // hooks — root-owned orchestration that stays outside this module
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
  openDialog: (id: string, initialSelector?: string) => DialogFocus | null;
  closeDialog: (focus: DialogFocus | null) => void;
  loadProjects: () => Promise<void>;
  loadBoard: (force?: boolean) => Promise<unknown>;
  refreshDetail: (force?: boolean) => Promise<unknown>;
}

export function createNewTaskModule(deps: NewTaskDeps) {
  // Internal state (flat root field before Phase 2): never rendered, owned here.
  /** Focus session for the new-task modal (trap + restore). */
  let newTaskFocus: DialogFocus | null = null;

  function openNewSubtask(parentTask: { id: string } | null | undefined): void {
    if (!parentTask) return;
    openNewTask("todo");
    deps.setNewTaskParentId(parentTask.id);
  }

  function openNewTask(status: Status = "todo"): void {
    deps.setNewTaskTarget(status);
    deps.setConfirmNewTaskDiscard(false);
    deps.setNewTask({
      title: "",
      description: "",
      priority: 0,
      due: "",
      acs: "",
      assigneeName: "",
      assigneeType: "human",
    });
    deps.setDuplicates([]);
    deps.setShowNewTask(true);
    // First form FIELD first (not the submit button) per a11y convention.
    newTaskFocus = deps.openDialog('cb-new-task', 'input[x-model="newTask.title"]');
  }

  /** Single close path for the new-task modal (create success). User-initiated
   *  dismissals (× / Cancel / backdrop / Esc) must go through
   *  requestCloseNewTask() so a half-typed draft is never lost. */
  function closeNewTask(): void {
    deps.setShowNewTask(false);
    deps.setConfirmNewTaskDiscard(false);
    deps.setDuplicates([]);
    deps.setNewTaskParentId(null);
    deps.setNewTask({
      title: "",
      description: "",
      priority: 0,
      due: "",
      acs: "",
      assigneeName: "",
      assigneeType: "human",
    });
    deps.closeDialog(newTaskFocus);
    newTaskFocus = null;
  }

  /** True when the new-task form holds any content — openNewTask() resets
   *  every field, so a freshly opened dialog is never dirty. */
  function newTaskDirty(): boolean {
    const n = deps.newTask();
    return Boolean(
      n.title.trim() ||
        n.description.trim() ||
        n.due !== "" ||
        n.priority !== 0 ||
        n.assigneeName.trim() ||
        n.assigneeType !== "human" ||
        n.acs.trim(),
    );
  }

  /** Guarded close for the new-task dialog (× / Cancel / backdrop / Esc):
   *  shows confirmation in the button row when a draft would be lost.
   *  createTask() closes UNCONDITIONALLY via closeNewTask() after a successful POST. */
  function requestCloseNewTask(): void {
    if (newTaskDirty()) {
      deps.setConfirmNewTaskDiscard(true);
      return;
    }
    closeNewTask();
  }

  function cancelNewTaskConfirm(): void {
    deps.setConfirmNewTaskDiscard(false);
  }

  function clearDuplicates(): void {
    deps.setDuplicates([]);
  }

  async function createTask(): Promise<void> {
    const title = deps.newTask().title.trim();
    if (deps.taskBusy() || !title || !deps.selectedProjectId()) return;
    deps.setTaskBusy(true);
    let created: TaskSummary | null = null;
    const parentId = deps.newTaskParentId();
    try {
      const n = deps.newTask();
      const assigneeName = n.assigneeName.trim();
      const res = await api.createTask({
        actor: deps.actor(),
        title,
        description: n.description.trim() || undefined,
        project_id: deps.selectedProjectId(),
        priority: n.priority,
        due_at: dateInputToIso(n.due),
        assignee: assigneeName ? { name: assigneeName, type: n.assigneeType } : undefined,
        acceptance_criteria: splitLines(n.acs),
      });
      created = res.task;
      // Target column other than To Do: the create lands in todo (v1) and we
      // follow up with ONE version-guarded move. A race here is advisory —
      // the next poll settles the truth.
      if (deps.newTaskTarget() !== "todo") {
        try {
          await api.updateTask(created.id, {
            actor: deps.actor(),
            version: created.version,
            status: deps.newTaskTarget(),
          });
        } catch (e2) {
          if (!ApiError.isVersionConflict(e2)) throw e2;
          deps.toast("New task changed elsewhere before the move — check its column.", "info");
        }
      }
      if (parentId) {
        try {
          await api.addRelation(created.id, parentId, "child", deps.actor());
        } catch (relErr) {
          deps.toast("Task created, but failed to link subtask: " + errMsg(relErr), "error");
        }
      }
      deps.setDuplicates(res.possible_duplicates ?? []);
      closeNewTask();
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setTaskBusy(false);
      if (created) {
        const detail = deps.detail();
        const promises: Promise<unknown>[] = [deps.loadProjects(), deps.loadBoard(true)];
        if (parentId && detail && detail.id === parentId) {
          promises.push(deps.refreshDetail(true));
        }
        void Promise.all(promises);
      } else {
        void deps.loadBoard(true); // board may have changed even on failure paths we care about
      }
    }
  }

  return {
    openNewSubtask,
    openNewTask,
    closeNewTask,
    newTaskDirty,
    requestCloseNewTask,
    cancelNewTaskConfirm,
    clearDuplicates,
    createTask,
  };
}

export type NewTaskModule = ReturnType<typeof createNewTaskModule>;
