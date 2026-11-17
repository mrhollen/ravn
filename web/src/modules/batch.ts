// Board-level MULTI-SELECT + batch action bar — final extraction of Phase 2
// (the frontend modularization epic). Owns the BEHAVIOR of the multi-select
// feature from app.ts: checkbox / modifier-click selection (Ctrl/Cmd toggle,
// Shift range within the clicked column's VISIBLE order), the floating
// bottom batch-action bar menus (Move to / Set priority / Assign owner), and
// the robust per-task execution engine (AC3) behind every action: version-
// guarded PATCHes with re-read + retry-once on 409 — the same discipline as
// the archive flow's confirmArchiveAllDone. Selection is in-session ONLY
// (never synced to the URL — it is ephemeral working state, like drag/sort).
// Every body moved here verbatim from app.ts; see docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html reads/
// writes still lives on the flat component root as a plain field, touched here
// exclusively through the typed accessors on {@link BatchDeps}. `selectedTaskIds`
// is REPLACED (never mutated in place) so Alpine's proxy tracks the change —
// Set mutation methods are invisible to it. The per-task engine (`runBatch`)
// and `assigneeTypeFor` are module-INTERNAL: no template binds them and no
// root-level test references them, so they get no delegation shim.
// `closeBatchMenus` is exposed (root name `_closeBatchMenus`) because the
// Escape cascade's "close-batch-menus" step must close the open dropdown
// WITHOUT clearing the selection — a deliberately different behavior from
// `clearSelection`.

import { ApiError, api } from "../api";
import type { ColumnView } from "../columns";
import { STATUSES, STATUS_LABEL } from "../types";
import type { ActorInput, Board, Status, TaskSummary, UpdateTaskReq } from "../types";

/** Typed dependency surface for the batch module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface BatchDeps {
  // -- state-in: live accessors into root-owned fields ------------------------
  selectedTaskIds(): Set<string>;
  /** Shift+Click range anchor (the `_selectionAnchorId` root field — poked
   *  directly by app.test.ts, hence it stays at the root and is mirrored in). */
  selectionAnchorId(): string | null;
  batchBusy(): boolean;
  batchMoveMenuOpen(): boolean;
  batchPriorityMenuOpen(): boolean;
  batchAssignMenuOpen(): boolean;
  /** Same object identity as the root's `board`; localApply callbacks mutate
   *  its status buckets in place through this getter. */
  board(): Board | null;

  // -- state-out: root writes, invoked so mutations flow through the root -----
  setSelectedTaskIds(v: Set<string>): void;
  setSelectionAnchorId(v: string | null): void;
  setBatchBusy(v: boolean): void;
  setBatchMoveMenuOpen(v: boolean): void;
  setBatchPriorityMenuOpen(v: boolean): void;
  setBatchAssignMenuOpen(v: boolean): void;

  // -- methods-out: root hooks -------------------------------------------------
  columns(): ColumnView[];
  availableAssignees(): string[];
  actor(): ActorInput;
  toast(msg: string, kind?: "error" | "info"): number;
  loadBoard(silent?: boolean): Promise<void>;
}

export function createBatchModule(deps: BatchDeps) {
  function isTaskSelected(id: string): boolean {
    return deps.selectedTaskIds().has(id);
  }

  function selectedCount(): number {
    return deps.selectedTaskIds().size;
  }

  /** AC1: checkbox toggle. Re-anchors range selection to this card so the
   *  next Shift+Click spans from it. Pure state — no network. */
  function toggleSelectTask(id: string): void {
    const next = new Set(deps.selectedTaskIds());
    if (next.has(id)) next.delete(id);
    else next.add(id);
    deps.setSelectedTaskIds(next);
    deps.setSelectionAnchorId(id);
  }

  /** AC1: Shift+Click range select within ONE column — from the anchored
   *  card to the clicked card, inclusive, over that column's currently
   *  RENDERED (post-filter, post-sort, post-cap) order. Additive: cards
   *  selected in other columns stay selected. Fallbacks keep it predictable:
   *  no anchor yet → plain toggle of the clicked card (which sets the
   *  anchor); anchor not visible in this column → plain toggle + re-anchor. */
  function rangeSelectInColumn(id: string): void {
    const anchor = deps.selectionAnchorId();
    if (!anchor) {
      toggleSelectTask(id);
      return;
    }
    const col = deps.columns().find((c) => c.tasks.some((t) => t.id === id));
    const anchorIdx = col ? col.tasks.findIndex((t) => t.id === anchor) : -1;
    if (!col || anchorIdx < 0) {
      // The anchor isn't in this column's visible order — a cross-column
      // range is undefined, so degrade to a single-card toggle here.
      toggleSelectTask(id);
      return;
    }
    const ids = col.tasks.map((t) => t.id);
    const from = Math.min(anchorIdx, ids.indexOf(id));
    const to = Math.max(anchorIdx, ids.indexOf(id));
    const next = new Set(deps.selectedTaskIds());
    for (let i = from; i <= to; i++) next.add(ids[i]);
    deps.setSelectedTaskIds(next);
    // Anchor intentionally NOT moved: the next shift-click still spans from it.
  }

  /** AC2: select every card currently RENDERED across all three columns —
   *  exactly the lists columns() hands to the template, so active filters
   *  AND the Done-cap expansion state are honored by construction (beyond-
   *  cap older done cards stay out until expanded; search bypasses the cap,
   *  so a searched set is fully selectable). */
  function selectAllVisible(): void {
    if (!deps.board()) return;
    const next = new Set(deps.selectedTaskIds());
    for (const col of deps.columns()) for (const t of col.tasks) next.add(t.id);
    deps.setSelectedTaskIds(next);
  }

  /** AC4 + bar "Clear": drop the whole selection and its transient UI. */
  function clearSelection(): void {
    if (deps.selectedTaskIds().size === 0 && !deps.selectionAnchorId()) return;
    deps.setSelectedTaskIds(new Set<string>());
    deps.setSelectionAnchorId(null);
    closeBatchMenus();
  }

  /** Collapse the batch-bar menus. Called internally by the toggle trio and
   *  runBatch's pre-flight, and at root (as `_closeBatchMenus`) by the Escape
   *  cascade — which closes just the open dropdown, never the selection. */
  function closeBatchMenus(except?: "move" | "priority" | "assign"): void {
    if (except !== "move") deps.setBatchMoveMenuOpen(false);
    if (except !== "priority") deps.setBatchPriorityMenuOpen(false);
    if (except !== "assign") deps.setBatchAssignMenuOpen(false);
  }

  function toggleBatchMoveMenu(): void {
    const next = !deps.batchMoveMenuOpen();
    deps.setBatchMoveMenuOpen(next);
    if (next) closeBatchMenus("move");
  }

  function toggleBatchPriorityMenu(): void {
    const next = !deps.batchPriorityMenuOpen();
    deps.setBatchPriorityMenuOpen(next);
    if (next) closeBatchMenus("priority");
  }

  function toggleBatchAssignMenu(): void {
    const next = !deps.batchAssignMenuOpen();
    deps.setBatchAssignMenuOpen(next);
    if (next) closeBatchMenus("assign");
  }

  /** Distinct assignee names present on the current project's tasks (the
   *  same source as availableAssignees()), plus the owner-droplist "Clear"
   *  affordance handled in the template. */
  function batchAssigneeOptions(): string[] {
    return deps.availableAssignees();
  }

  /** Internal: owner type for a name — reuse the type already attached to a
   *  board row carrying that assignee (agents stay agents), else default
   *  human. */
  function assigneeTypeFor(name: string): "human" | "agent" {
    const b = deps.board();
    if (b) {
      for (const s of STATUSES) {
        const row = b.tasks[s]?.find((t) => t.assignee?.name === name);
        if (row?.assignee?.type === "agent") return "agent";
      }
    }
    return "human";
  }

  // ---------------------------- AC3: robust batch execution ---------------
  /** Shared engine for every batch action. Runs per-task, sequentially, each
   *  PATCH version-guarded with the FRESHEST version in board state; a 409
   *  re-reads that ONE task (api.getTask) and retries exactly once — the same
   *  discipline as confirmArchiveAllDone(). Rows are located on board.tasks
   *  (filters only hide cards, they never remove them from the snapshot); a
   *  row no longer in the board (SSE refresh raced us) falls back to a direct
   *  read for its version. Optimistic local mutation per success; ONE final
   *  silent loadBoard settles counts/blocked state. Selection policy: full
   *  success clears it, partial failure keeps ONLY the failed ids selected
   *  so the user can re-run just those. Consolidated summary toast always. */
  async function runBatch(
    patchFor: (row: TaskSummary | null) => Partial<UpdateTaskReq>,
    localApply: ((id: string, row: TaskSummary, result: TaskSummary) => void) | null,
    alreadyApplied?: (fresh: { archived?: boolean }) => boolean,
    /** Builds the success message from the ok-count; the engine appends the
     *  "· N failed" suffix and picks error/info kind itself. */
    toastText?: (ok: number) => string,
  ): Promise<void> {
    const ids = [...deps.selectedTaskIds()];
    if (ids.length === 0 || deps.batchBusy()) return;
    deps.setBatchBusy(true);
    closeBatchMenus();
    let ok = 0;
    const failed: string[] = [];
    for (const id of ids) {
      let row: TaskSummary | null = null;
      const b = deps.board();
      if (b) {
        for (const s of STATUSES) {
          const found = b.tasks[s]?.find((t) => t.id === id);
          if (found) {
            row = found;
            break;
          }
        }
      }
      try {
        let version = row?.version ?? 0;
        if (!row) version = (await api.getTask(id, { commentLimit: 1 })).version;
        try {
          const result = await api.updateTask(id, {
            actor: deps.actor(),
            version,
            ...patchFor(row),
          });
          if (row && localApply) localApply(id, row, result);
          ok++;
        } catch (e) {
          if (!ApiError.isVersionConflict(e)) throw e;
          // Stale version: re-read THIS task and retry once (AC3).
          const fresh = await api.getTask(id, { commentLimit: 1 });
          if (alreadyApplied && alreadyApplied(fresh)) {
            // Someone else already made this exact change — treat as success.
            if (row && localApply) localApply(id, row, row);
            ok++;
            continue;
          }
          const result = await api.updateTask(id, {
            actor: deps.actor(),
            version: fresh.version,
            ...patchFor(row),
          });
          if (row && localApply) localApply(id, row, result);
          ok++;
        }
      } catch {
        failed.push(id);
      }
    }
    deps.setBatchBusy(false);
    if (ok > 0) await deps.loadBoard(true).catch(() => undefined);
    if (failed.length === 0) {
      deps.setSelectedTaskIds(new Set<string>());
    } else {
      deps.setSelectedTaskIds(new Set(failed)); // keep only the failures selected
    }
    deps.setSelectionAnchorId(null);
    const suffix = failed.length > 0 ? ` · ${failed.length} failed` : "";
    const base = toastText ? toastText(ok) : `${ok} ${ok === 1 ? "task" : "tasks"}`;
    deps.toast(base + suffix, failed.length > 0 ? "error" : "info");
  }

  // ---------------------------- batch actions (AC2/AC3) --------------------
  /** Move every selected card to a status. Rows already in that status are
   *  still patched (server no-op) so the count stays honest; blocked tasks
   *  crossing into Done fail per-row and surface in the summary, matching
   *  the single-move flow's no-override-blockers policy. */
  async function batchMoveTo(status: Status): Promise<void> {
    await runBatch(
      () => ({ status }),
      (id, row, result) => {
        const b = deps.board();
        if (!b) return;
        const from = row.status;
        if (from !== status) {
          b.tasks[from] = b.tasks[from].filter((t) => t.id !== id);
          if ((b.counts[from] ?? 0) > 0) b.counts[from]--;
          b.tasks[status] = [...b.tasks[status], { ...row, status }];
          b.counts[status] = (b.counts[status] ?? 0) + 1;
        }
        const current = b.tasks[status].find((t) => t.id === id);
        if (current) Object.assign(current, result);
      },
      undefined,
      (ok) => `Moved ${ok} ${ok === 1 ? "task" : "tasks"} to ${STATUS_LABEL[status]}`,
    );
  }

  async function batchSetPriority(priority: number): Promise<void> {
    await runBatch(
      () => ({ priority }),
      (id, row, result) => {
        const b = deps.board();
        if (!b || !row.status) return;
        const bucket = b.tasks[row.status];
        const idx = bucket.findIndex((t) => t.id === id);
        if (idx >= 0) bucket[idx] = { ...bucket[idx], ...result };
      },
      undefined,
      (ok) => `Updated ${ok} ${ok === 1 ? "task" : "tasks"} to priority P${priority}`,
    );
  }

  /** Assign a durable owner to every selected card; name=null clears it.
   *  The dropdown offers the project's existing assignee names (AC2) with a
   *  Clear option; when none exist the bar falls back to free text. */
  async function batchAssignOwner(name: string | null): Promise<void> {
    const trimmed = name === null ? "" : name.trim();
    if (name !== null && trimmed === "") return;
    const type = assigneeTypeFor(trimmed);
    await runBatch(
      () => (trimmed ? { assignee: { name: trimmed, type } } : { clear_assignee: true }),
      (id, row, result) => {
        const b = deps.board();
        if (!b || !row.status) return;
        const bucket = b.tasks[row.status];
        const idx = bucket.findIndex((t) => t.id === id);
        if (idx >= 0) bucket[idx] = { ...bucket[idx], assignee: result.assignee, version: result.version };
      },
      undefined,
      (ok) =>
        trimmed
          ? `Assigned ${ok} ${ok === 1 ? "task" : "tasks"} to ${trimmed}`
          : `Cleared owner on ${ok} ${ok === 1 ? "task" : "tasks"}`,
    );
  }

  /** Archive every selected card. Mirrors confirmArchiveAllDone's loop:
   *  per-row version-guarded PATCH, one re-read + retry on 409 (skipping the
   *  re-patch when someone already archived it), optimistic removal per
   *  success, one settling reload at the end. */
  async function batchArchiveSelected(): Promise<void> {
    await runBatch(
      () => ({ archived: true }),
      (id, row, result) => {
        const b = deps.board();
        if (!b) return;
        const status = row.status || result.status; // status is stable across an archive
        b.tasks[status] = b.tasks[status].filter((t) => t.id !== id);
        if ((b.counts[status] ?? 0) > 0) b.counts[status]--;
      },
      (fresh) => fresh.archived === true,
      (ok) => `Archived ${ok} selected ${ok === 1 ? "task" : "tasks"}`,
    );
  }

  return {
    isTaskSelected,
    selectedCount,
    toggleSelectTask,
    rangeSelectInColumn,
    selectAllVisible,
    clearSelection,
    /** Also exposed at the root as `_closeBatchMenus` — the Escape cascade's
     *  "close-batch-menus" step closes the OPEN dropdown without touching the
     *  selection itself (clearSelection would do both). */
    closeBatchMenus,
    toggleBatchMoveMenu,
    toggleBatchPriorityMenu,
    toggleBatchAssignMenu,
    batchAssigneeOptions,
    batchMoveTo,
    batchSetPriority,
    batchAssignOwner,
    batchArchiveSelected,
  };
}

export type BatchModule = ReturnType<typeof createBatchModule>;
