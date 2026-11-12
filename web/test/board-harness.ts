// Shared fake state/deps harness for the board module unit tests (Phase 2,
// batch B). Pure-module tests build a module against fakes that read/write a
// plain state object, so every assertion is deterministic — no component root,
// no DOM. Used by board-module.test.ts (core) and board-view-module.test.ts
// (view); root-level wiring regressions live in app.test.ts.

import type { Board, TaskSummary } from "../src/types.ts";
import type { SortDir, SortKey } from "../src/sort.ts";
import type { BoardDeps } from "../src/modules/board.ts";
import type { BoardViewDeps } from "../src/modules/board-view.ts";

// --------------------------------------------------------------------------- fixtures

export function task(id: string, over: Partial<TaskSummary> = {}): TaskSummary {
  return {
    id,
    title: `Task ${id}`,
    status: "todo",
    priority: 3,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

export function board(projectId: string, tasks: Partial<Record<string, TaskSummary[]>>): Board {
  return { project_id: projectId, counts: {}, tasks: { todo: [], doing: [], done: [], ...tasks } } as Board;
}

// --------------------------------------------------------------------------- fake state + deps

export interface FakeState {
  selectedProjectId: string;
  taskScope: "all" | "assigned" | "created" | "activity";
  board: Board | null;
  showArchived: boolean;
  doneLimit: number;
  nowTick: number;
  hoistParents: boolean;
  sortKey: SortKey;
  sortDir: SortDir;
  cardDensity: "comfortable" | "compact";
  blockerPopoverTaskId: string | null;
  filterPriority: string;
  filterAssignee: string;
  filterUnblocked: boolean;
  filterStalled: boolean;
  filterNeedsAttention: boolean;
  filterDue: string;
  filterSearch: string;
  // view-only extras (backing for the methods-out hooks)
  archived: Record<string, TaskSummary[]> | null;
}

export function makeState(over: Partial<FakeState> = {}): FakeState {
  return {
    selectedProjectId: "P",
    taskScope: "all",
    board: null,
    showArchived: false,
    doneLimit: 20,
    nowTick: Date.now(),
    hoistParents: true,
    sortKey: "",
    sortDir: "desc",
    cardDensity: "comfortable",
    blockerPopoverTaskId: null,
    filterPriority: "",
    filterAssignee: "",
    filterUnblocked: false,
    filterStalled: false,
    filterNeedsAttention: false,
    filterDue: "",
    filterSearch: "",
    archived: null,
    ...over,
  };
}

export interface Hooks {
  syncUrlState?: (immediate?: boolean) => void;
  openAuthModal?: () => void;
  isBlocked?: (id: string) => boolean;
  fullTextMatchIds?: () => ReadonlySet<string>;
  syncBlocked?: (b: Board) => void;
  hasActiveFilters?: () => boolean;
}

export function makeBoardDeps(state: FakeState, hooks: Hooks = {}): BoardDeps {
  return {
    selectedProjectId: () => state.selectedProjectId,
    board: () => state.board,
    sortKey: () => state.sortKey,
    sortDir: () => state.sortDir,
    hoistParents: () => state.hoistParents,
    cardDensity: () => state.cardDensity,
    blockerPopoverTaskId: () => state.blockerPopoverTaskId,
    filterPriority: () => state.filterPriority,
    filterAssignee: () => state.filterAssignee,
    filterUnblocked: () => state.filterUnblocked,
    filterStalled: () => state.filterStalled,
    filterNeedsAttention: () => state.filterNeedsAttention,
    filterDue: () => state.filterDue,
    filterSearch: () => state.filterSearch,
    setSortKey: (k) => { state.sortKey = k; },
    setSortDir: (d) => { state.sortDir = d; },
    setHoistParents: (b) => { state.hoistParents = b; },
    setCardDensity: (v) => { state.cardDensity = v; },
    setBlockerPopoverTaskId: (v) => { state.blockerPopoverTaskId = v; },
    setFilterPriority: (v) => { state.filterPriority = v; },
    setFilterAssignee: (v) => { state.filterAssignee = v; },
    setFilterUnblocked: (b) => { state.filterUnblocked = b; },
    setFilterStalled: (b) => { state.filterStalled = b; },
    setFilterNeedsAttention: (b) => { state.filterNeedsAttention = b; },
    setFilterDue: (v) => { state.filterDue = v; },
    setFilterSearch: (v) => { state.filterSearch = v; },
    syncUrlState: (immediate) => hooks.syncUrlState?.(immediate),
  };
}

export function makeBoardViewDeps(state: FakeState, hooks: Hooks = {}): BoardViewDeps {
  return {
    selectedProjectId: () => state.selectedProjectId,
    taskScope: () => state.taskScope,
    board: () => state.board,
    showArchived: () => state.showArchived,
    doneLimit: () => state.doneLimit,
    nowTick: () => state.nowTick,
    hoistParents: () => state.hoistParents,
    sortKey: () => state.sortKey,
    sortDir: () => state.sortDir,
    filterPriority: () => state.filterPriority,
    filterAssignee: () => state.filterAssignee,
    filterUnblocked: () => state.filterUnblocked,
    filterStalled: () => state.filterStalled,
    filterNeedsAttention: () => state.filterNeedsAttention,
    filterDue: () => state.filterDue,
    filterSearch: () => state.filterSearch,
    setBoard: (b) => { state.board = b; },
    setProjectForbidden: () => { /* recorded via hooks in tests that need it */ },
    setLoadError: () => { /* no-op for unit purposes */ },
    setSelectedProjectId: (v) => { state.selectedProjectId = v; },
    setSortKey: (k) => { state.sortKey = k; },
    setSortDir: (d) => { state.sortDir = d; },
    setFilterPriority: (v) => { state.filterPriority = v; },
    setFilterAssignee: (v) => { state.filterAssignee = v; },
    setFilterUnblocked: (b) => { state.filterUnblocked = b; },
    setFilterStalled: (b) => { state.filterStalled = b; },
    setFilterNeedsAttention: (b) => { state.filterNeedsAttention = b; },
    setFilterDue: (v) => { state.filterDue = v; },
    setFilterSearch: (v) => { state.filterSearch = v; },
    openAuthModal: () => hooks.openAuthModal?.(),
    archivedColumns: () => state.archived,
    isBlocked: (id) => hooks.isBlocked?.(id) ?? false,
    fullTextMatchIds: () => hooks.fullTextMatchIds?.() ?? new Set<string>(),
    syncBlocked: (b) => hooks.syncBlocked?.(b),
    hasActiveFilters: () => hooks.hasActiveFilters?.() ?? false,
  };
}
