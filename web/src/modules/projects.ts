// Projects feature module — Phase 2 batch A of the frontend modularization
// epic. Owns the BEHAVIOR of two concerns from app.ts (batch A slice 2/3):
//   1. the project LIST lifecycle: parallel open+archived fetch with the
//      sequence guard + in-flight coalescing cores from src/refresh.ts, the
//      ?mine=created scoping (epic T7), and graceful archived-fetch
//      degradation;
//   2. the create-project DIALOG: opening the form (without resetting its
//      draft) and the submit flow (guard → api.createProject → refresh lists
//      → adopt selection → reconnect SSE → load board).
// The option-label / current-selection-label derivations move with it.
// Every body moved here verbatim from app.ts; see the ownership map in
// docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html
// reads/writes still lives on the flat component root as a plain field, and
// is touched here exclusively through the typed accessors on {@link ProjectsDeps}
// (see modules/auth.ts for the full rationale: Alpine's reactive proxy only
// observes mutations made THROUGH the proxy, so module-owned nested state is
// unsafe). The sequence guard and in-flight coalescer are INTERNAL state —
// never rendered, never part of the flat namespace — and are owned here.

import { ApiError, api, errMsg } from "../api";
import { coalesceInFlight, createSequenceGuard } from "../refresh";
import type { ActorInput, ProjectWithCounts, SessionIdentity } from "../types";

/** Persisted selected-project id (moved from app.ts; value unchanged — the
 *  composition root still re-exports it and persists it on every selection
 *  path that stays there). */
export const LS_PROJECT = "ravn.project";

/** The new-project form draft (bound as `newProject` on the root). */
export interface ProjectDraft {
  name: string;
  description: string;
}

/** Typed dependency surface for the projects module (state-in / methods-out,
 *  per docs/FRONTEND_ARCHITECTURE.md §4). */
export interface ProjectsDeps {
  // -- state-in: live accessors into root-owned project fields --------------
  projects(): ProjectWithCounts[];
  archivedProjects(): ProjectWithCounts[];
  loadError(): string;
  selectedProjectId(): string;
  /** "My projects" filter (epic T7) — scopes the OPEN list fetch. */
  myProjectsOnly(): boolean;
  identity(): SessionIdentity | null;
  newProjectDraft(): ProjectDraft;
  projectBusy(): boolean;

  // -- state-out: root writes, invoked so mutations flow through the root ----
  setProjects(v: ProjectWithCounts[]): void;
  setArchivedProjects(v: ProjectWithCounts[]): void;
  setLoadError(v: string): void;
  setNewProjectDraft(d: ProjectDraft): void;
  setProjectBusy(b: boolean): void;
  setShowNewProject(b: boolean): void;

  // -- methods-out: everything the module calls back into the root for -------
  /** The acting identity (the root "actor" concern). */
  actor(): ActorInput;
  /** 401 on the list fetch — open the session modal. */
  openAuthModal(): void;
  /** Create success — reconnect SSE for the fresh project. */
  connectSse(): void;
  /** Create success — load the fresh board. */
  loadBoard(): Promise<void>;
  /** Create failure — surface the error toast. */
  toast(msg: string, kind: "error" | "info"): void;
  /** "+ New project…" — collapse the Projects dropdown (controller still
   *  root-owned in this batch; the shared menu controller lands with slice 3). */
  closeProjectMenu(): void;
  /** Create success — make the new project THE selection: set the bound
   *  field AND persist it (the same two-line persistence selectProject()
   *  performs for user-driven switches; that cascade deliberately stays at
   *  the composition root). */
  adoptSelectedProject(id: string): void;
  /** Open form — focus the name input once it is rendered (root seam over
   *  the shared _focusWhenVisible helper + the element id). */
  focusCreateNameInput(): void;
}

export interface ProjectsModule {
  loadProjects(silent?: boolean): Promise<void>;
  projectOptionLabel(p: ProjectWithCounts): string;
  projectSelectLabel(): string;
  openNewProject(): void;
  createProject(): Promise<void>;
}

/** Create the projects module. One instance per component root (the root
 *  builds it lazily — see modules/auth.ts for why). */
export function createProjectsModule(deps: ProjectsDeps): ProjectsModule {
  // -- internal state (never rendered, never part of the flat namespace) -----
  /** Request-sequence guard: a response may only apply if no NEWER load was
   *  initiated after it started (see web/src/refresh.ts). */
  const projectSeq = createSequenceGuard();
  /** In-flight coalescer, created lazily on first use: a concurrent duplicate
   *  — observed at boot, where Alpine re-runs init() — JOINS the running
   *  request instead of racing it, so no caller ever resumes against state
   *  that a sibling call's sequence guard discarded. */
  let coalesced: ReturnType<typeof coalesceInFlight<boolean[], void>> | null = null;

  async function loadProjectsCore(silent: boolean): Promise<void> {
    const token = projectSeq.next(); // newest initiated list-projects wins
    try {
      // Two SEPARATE fetches: the endpoint never combines open + archived in
      // one response (archived=true is a strict filter), so each list gets its
      // own call. They run in parallel — total latency stays ~1 RTT. A failed
      // ARCHIVED fetch degrades gracefully: the restore section simply keeps
      // showing the last known list instead of erroring the boot path.
      // "My projects" (epic T7): when the filter is on AND a session identity exists,
      // restrict the OPEN list to this identity's created projects (?mine=created). The
      // archived fetch stays unscoped (the endpoint never combines archived + mine), and
      // logged-out views omit the param so they keep seeing every project.
      const openList = api.listProjects(
        false,
        deps.myProjectsOnly() && deps.identity() ? "created" : undefined,
      );
      const [res, archivedRes] = await Promise.all([
        openList,
        api.listProjects(true).catch((e) => {
          if (!ApiError.isUnauthorized(e)) console.warn("archived projects fetch failed:", e);
          return null; // keep the previous archivedProjects on failure
        }),
      ]);
      // A slower EARLIER request must never overwrite counts a newer request
      // already delivered (out-of-order network responses).
      if (!projectSeq.isFresh(token)) return;
      deps.setProjects(res.projects);
      if (archivedRes) deps.setArchivedProjects(archivedRes.projects);
      if (!silent) deps.setLoadError("");
    } catch (e) {
      if (!silent) deps.setLoadError(errMsg(e));
      if (ApiError.isUnauthorized(e)) {
        deps.openAuthModal();
      }
    }
  }

  function loadProjects(silent = false): Promise<void> {
    if (!coalesced) {
      coalesced = coalesceInFlight((s: boolean) => loadProjectsCore(s));
    }
    return coalesced.call(silent);
  }

  function projectOptionLabel(p: ProjectWithCounts): string {
    const c = p.task_counts;
    return `${p.name}  (${c.todo}·${c.doing}·${c.done})`;
  }

  function projectSelectLabel(): string {
    const p = deps.projects().find((x) => x.id === deps.selectedProjectId());
    return p ? projectOptionLabel(p) : "Select a project…";
  }

  /** "+ New project…" menu item: closes the dropdown and opens the existing
   *  form bar below the header, focusing the name field. The `newProject`
   *  draft is deliberately NOT reset here — a dismissed/cancelled form must
   *  keep its typed input (no phantom creation on submit, no loss on return);
   *  the draft clears only on successful create. */
  function openNewProject(): void {
    deps.closeProjectMenu();
    deps.setShowNewProject(true);
    if (typeof window === "undefined" || typeof document === "undefined") return;
    // The name input exists from boot (static markup under an x-show bar);
    // wait out the display flush before focusing.
    deps.focusCreateNameInput();
  }

  async function createProject(): Promise<void> {
    const name = deps.newProjectDraft().name.trim();
    if (deps.projectBusy() || !name) return;
    deps.setProjectBusy(true);
    try {
      const p = await api.createProject({
        actor: deps.actor(),
        name,
        description: deps.newProjectDraft().description.trim() || undefined,
      });
      deps.setShowNewProject(false);
      deps.setNewProjectDraft({ name: "", description: "" });
      await loadProjects();
      deps.adoptSelectedProject(p.id);
      deps.connectSse();
      await deps.loadBoard();
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setProjectBusy(false);
    }
  }

  return { loadProjects, projectOptionLabel, projectSelectLabel, openNewProject, createProject };
}
