// Task-detail relations section — state + handlers (Phase 2, batch D).
//
// Extracted from app.ts as a plain factory module (same pattern as claims.ts /
// artifacts.ts): the add-relation combobox (search/filter/keyboard nav), the
// `RelationRow → RelationView` shaping for the relations list (direction-aware
// for child and blocked-by; the wire never exposes a "parent" kind), the
// subtask progress counters, and the add/remove mutation flows.
//
// `relationForm` is template-bound in index.html (kind select, combobox input,
// suggestions list, submit disable) → it stays a flat plain root field; the
// module reaches it through RelationsDeps and owns no closure-internal state.
// Bodies moved VERBATIM — `this.` rewritten to deps accessors (in-place form
// writes go through the state-IN getter), otherwise byte-identical. The
// shared board-row title lookup (`taskTitle`) stays root-owned (a generic
// board helper) and is injected as a hook; the openTask/closeDetail resets of
// `relationForm` ride with their detail-lifecycle owners at root. app.ts keeps
// thin delegation shims for every method — the flat bound-name surface is
// unchanged.

import { api, errMsg } from "../api";
import type { ActorInput, Board, RelationRow, Status, TaskDetail, TaskSummary } from "../types";
import { STATUSES } from "../types";
import { shortId } from "../util";

/** Add-relation combobox form state. Template-bound → flat root field; the
 *  module mutates its properties in place through the state-IN getter. */
export interface RelationForm {
  kind: string;
  targetId: string;
  search: string;
  open: boolean;
  busy: boolean;
  activeIndex: number;
}

/** Shaped row for the relations list (P1 sliver that landed with this module). */
export interface RelationView {
  key: string;
  label: string;
  otherId: string;
  title: string;
  status?: Status;
  isBlocker?: boolean;
  badgeClass?: string;
  raw: RelationRow;
}

export interface RelationsDeps {
  // -- state-in ---------------------------------------------------------
  detail: () => TaskDetail | null;
  board: () => Board | null;
  /** Template-bound add-relation form (root-owned). */
  relationForm: () => RelationForm;

  // -- hooks ------------------------------------------------------------
  actor: () => ActorInput;
  toast: (msg: string, kind?: "error" | "info") => number;
  refreshDetail: (force?: boolean) => Promise<unknown>;
  loadBoard: (force?: boolean) => Promise<unknown>;
  /** Shared board-row title lookup (root-owned; falls back to `shortId`). */
  taskTitle: (id: string) => string;
}

export function createRelationsModule(deps: RelationsDeps) {
  return {
    selectRelationTarget(t: TaskSummary): void {
      deps.relationForm().targetId = t.id;
      deps.relationForm().search = `${t.title} (${shortId(t.id)})`;
      deps.relationForm().open = false;
      deps.relationForm().activeIndex = -1;
    },

    onRelationComboboxKeyDown(dir: "up" | "down"): void {
      const list = this.filteredRelationTasks();
      if (list.length === 0) return;
      deps.relationForm().open = true;
      if (dir === "down") {
        deps.relationForm().activeIndex = (deps.relationForm().activeIndex + 1) % list.length;
      } else {
        deps.relationForm().activeIndex =
          deps.relationForm().activeIndex <= 0 ? list.length - 1 : deps.relationForm().activeIndex - 1;
      }
    },

    onRelationComboboxEnter(): void {
      const list = this.filteredRelationTasks();
      if (deps.relationForm().open && deps.relationForm().activeIndex >= 0 && deps.relationForm().activeIndex < list.length) {
        this.selectRelationTarget(list[deps.relationForm().activeIndex]);
      } else if (!deps.relationForm().busy && (deps.relationForm().targetId || deps.relationForm().search.trim())) {
        this.addRelation();
      }
    },

    filteredRelationTasks(): TaskSummary[] {
      const d = deps.detail();
      const b = deps.board();
      if (!b || !d) return [];
      const currentId = d.id;
      const kind = deps.relationForm().kind;
      const q = (deps.relationForm().search || "").trim().toLowerCase();

      // Collect all tasks except current task
      const all: TaskSummary[] = [];
      const seen = new Set<string>();
      for (const s of STATUSES) {
        for (const t of b.tasks[s] ?? []) {
          if (t.id !== currentId && !seen.has(t.id)) {
            seen.add(t.id);
            all.push(t);
          }
        }
      }

      // AC3: exclude tasks already related with the same kind
      const existingRelations = d.relations ?? [];
      const isAlreadyRelated = (taskId: string): boolean => {
        return existingRelations.some((r) => {
          if (kind === "parent") {
            return r.kind === "child" && r.task_id === d.id && r.other_task_id === taskId;
          } else if (kind === "child") {
            return r.kind === "child" && r.task_id === taskId && r.other_task_id === d.id;
          } else if (kind === "blocked-by") {
            return r.kind === "blocked-by" && r.task_id === d.id && r.other_task_id === taskId;
          } else if (kind === "blocking") {
            return r.kind === "blocked-by" && r.task_id === taskId && r.other_task_id === d.id;
          } else if (kind === "duplicate-of") {
            return r.kind === "duplicate-of" && (r.task_id === taskId || r.other_task_id === taskId);
          } else if (kind === "related") {
            return r.kind === "related" && (r.task_id === taskId || r.other_task_id === taskId);
          }
          return false;
        });
      };

      const eligible = all.filter((t) => !isAlreadyRelated(t.id));

      // Filter by search query
      const filtered = q
        ? eligible.filter((t) => {
            const sid = shortId(t.id).toLowerCase();
            return (
              t.title.toLowerCase().includes(q) ||
              t.id.toLowerCase().includes(q) ||
              sid.includes(q) ||
              t.status.toLowerCase().includes(q)
            );
          })
        : eligible;

      // AC2: If kind is 'blocked-by', sort open tasks before done tasks
      if (kind === "blocked-by") {
        return filtered.sort((a, b) => {
          if (a.status === "done" && b.status !== "done") return 1;
          if (a.status !== "done" && b.status === "done") return -1;
          return a.priority - b.priority;
        });
      }

      return filtered;
    },

    availableRelationTasks(): TaskSummary[] {
      return this.filteredRelationTasks();
    },

    /** Resolve relation edges to display rows (direction-aware for child and
     *  blocked-by; the wire never exposes a "parent" kind). */
    relationRows(): RelationView[] {
      const d = deps.detail();
      if (!d) return [];
      const me = d.id;
      const b = deps.board();
      return d.relations.map((r) => {
        let label: string;
        let otherId: string;
        let isBlocker = false;
        let badgeClass = "bg-zinc-100 text-zinc-600 dark:bg-zinc-700 dark:text-zinc-300";
        if (r.kind === "child") {
          // Stored edge: task_id is the child, other_task_id the parent.
          if (r.task_id === me) {
            label = "Parent";
            otherId = r.other_task_id;
            badgeClass = "bg-purple-100 text-purple-800 ring-1 ring-purple-300 dark:bg-purple-950 dark:text-purple-300 dark:ring-purple-800";
          } else {
            label = "Child";
            otherId = r.task_id;
            badgeClass = "bg-indigo-100 text-indigo-800 ring-1 ring-indigo-300 dark:bg-indigo-950 dark:text-indigo-300 dark:ring-indigo-800";
          }
        } else if (r.kind === "blocked-by") {
          if (r.task_id === me) {
            label = "Blocked by";
            otherId = r.other_task_id;
            isBlocker = true;
            badgeClass = "bg-red-100 text-red-800 ring-1 ring-red-300 dark:bg-red-950 dark:text-red-300 dark:ring-red-800 font-bold";
          } else {
            label = "Blocking";
            otherId = r.task_id;
            badgeClass = "bg-amber-100 text-amber-800 ring-1 ring-amber-300 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800";
          }
        } else if (r.kind === "duplicate-of") {
          if (r.task_id === me) {
            label = "Duplicate of";
            otherId = r.other_task_id;
          } else {
            label = "Duplicated by";
            otherId = r.task_id;
          }
          badgeClass = "bg-zinc-100 text-zinc-600 ring-1 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700";
        } else {
          label = "Related to";
          otherId = r.task_id === me ? r.other_task_id : r.task_id;
          badgeClass = "bg-sky-100 text-sky-800 ring-1 ring-sky-300 dark:bg-sky-950 dark:text-sky-300 dark:ring-sky-800";
        }

        let status: Status | undefined;
        if (b) {
          for (const s of STATUSES) {
            const found = b.tasks[s]?.find((t) => t.id === otherId);
            if (found) {
              status = found.status;
              break;
            }
          }
        }

        return {
          key: `${r.task_id}:${r.other_task_id}:${r.kind}`,
          label,
          otherId,
          title: deps.taskTitle(otherId),
          status,
          isBlocker,
          badgeClass,
          raw: r,
        };
      });
    },

    detailSubtaskTotal(): number {
      const d = deps.detail();
      if (!d) return 0;
      let total = 0;
      for (const r of d.relations) {
        if (r.kind === "child" && r.other_task_id === d.id) {
          total++;
        }
      }
      return total;
    },

    detailSubtaskDone(): number {
      const d = deps.detail();
      if (!d) return 0;
      let done = 0;
      const b = deps.board();
      for (const r of d.relations) {
        if (r.kind === "child" && r.other_task_id === d.id) {
          if (b) {
            for (const s of STATUSES) {
              const row = b.tasks[s]?.find((t) => t.id === r.task_id);
              if (row && row.status === "done") {
                done++;
                break;
              }
            }
          }
        }
      }
      return done;
    },

    detailSubtaskPercent(): number {
      const total = this.detailSubtaskTotal();
      if (total <= 0) return 0;
      return Math.min(100, Math.round((this.detailSubtaskDone() / total) * 100));
    },

    async addRelation(): Promise<void> {
      const d = deps.detail();
      let targetId = deps.relationForm().targetId.trim();
      if (!targetId && deps.relationForm().search.trim()) {
        const q = deps.relationForm().search.trim();
        const match = this.filteredRelationTasks().find(
          (t) =>
            t.id === q ||
            t.id.toLowerCase() === q.toLowerCase() ||
            shortId(t.id).toLowerCase() === q.toLowerCase(),
        );
        targetId = match ? match.id : q;
      }
      const kind = deps.relationForm().kind;
      if (!d || !targetId || deps.relationForm().busy) return;
      if (targetId === d.id) {
        deps.toast("Cannot relate a task to itself", "error");
        return;
      }
      deps.relationForm().busy = true;
      try {
        if (kind === "parent") {
          // Setting target as parent of this task -> this task is a child of target
          await api.addRelation(d.id, targetId, "child", deps.actor());
        } else if (kind === "child") {
          // Setting target as child of this task -> this task is a parent of target
          await api.addRelation(d.id, targetId, "parent", deps.actor());
        } else if (kind === "blocking") {
          // This task blocks target -> target is blocked by this task
          await api.addRelation(targetId, d.id, "blocked-by", deps.actor());
        } else {
          // blocked-by, duplicate-of, related
          await api.addRelation(d.id, targetId, kind, deps.actor());
        }
        deps.relationForm().targetId = "";
        deps.relationForm().search = "";
        deps.relationForm().open = false;
        deps.relationForm().activeIndex = -1;
        await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
        deps.toast("Relation added");
      } catch (e) {
        // Surface 409 parent conflict or any error verbatim without corrupting state
        deps.toast(errMsg(e), "error");
      } finally {
        deps.relationForm().busy = false;
      }
    },

    async removeRelation(r: RelationRow): Promise<void> {
      const d = deps.detail();
      if (!d) return;
      try {
        await api.removeRelation(r.task_id, r.other_task_id, r.kind, deps.actor());
        await Promise.all([deps.refreshDetail(true), deps.loadBoard(true)]);
        deps.toast("Relation removed");
      } catch (e) {
        deps.toast(errMsg(e), "error");
      }
    },
  };
}

export type RelationsModule = ReturnType<typeof createRelationsModule>;
