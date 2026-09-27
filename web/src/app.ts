// The single Alpine.js root component for the crowban board UI. One factory
// returning a state + methods object; registered as `Alpine.data("app", …)` in
// main.ts and used via x-data="app()" in index.html.
//
// Scope: VISIBILITY + light intervention. Humans can create projects/tasks,
// move status, save notes, toggle ACs, comment, attach artifacts. Claims are
// READ-ONLY here (agents manage leases via MCP).

import { ApiError, api, errMsg } from "./api";
import { renderMarkdown } from "./md";
import type {
  AcRow,
  ActorInput,
  ArtifactKind,
  Board,
  CommentKind,
  ProjectWithCounts,
  Status,
  TaskDetail,
  TaskSummary,
} from "./types";
import { STATUSES, STATUS_LABEL } from "./types";
import {
  actorLabel,
  artifactKindClass,
  claimHint,
  commentKindClass,
  dateInputToIso,
  dueLabel,
  isOverdue,
  initials,
  priorityClass,
  priorityName,
  relTime,
  shortId,
  splitLines,
  statusLabel,
} from "./util";

const LS_ACTOR = "crowban.actor";
const LS_PROJECT = "crowban.project";
const POLL_MS = 15_000;
const MD_CACHE_MAX = 500;

export interface Toast {
  id: number;
  msg: string;
  kind: "error" | "info";
}

interface RelationView {
  key: string;
  label: string;
  otherId: string;
  title: string;
}

export function crowbanApp() {
  return {
    // ------------------------------------------------------------------ actor
    /** Display name stored in localStorage; sent as `actor.name` on every mutation. */
    actorName: (typeof localStorage !== "undefined" ? localStorage.getItem(LS_ACTOR) : "") ?? "",

    saveActor(): void {
      try {
        localStorage.setItem(LS_ACTOR, this.actorName.trim());
      } catch {
        /* private mode — non-fatal */
      }
    },

    actor(): ActorInput {
      return { name: this.actorName.trim() || "human", type: "human" };
    },

    // ------------------------------------------------------------------ toasts
    toasts: [] as Toast[],
    toastSeq: 0,

    toast(msg: string, kind: "error" | "info" = "info"): void {
      const id = ++this.toastSeq;
      this.toasts = [...this.toasts, { id, msg, kind }];
      window.setTimeout(() => {
        this.toasts = this.toasts.filter((t) => t.id !== id);
      }, 6500);
    },

    // ---------------------------------------------------------------- projects
    projects: [] as ProjectWithCounts[],
    loadError: "",

    async loadProjects(silent = false): Promise<void> {
      try {
        const res = await api.listProjects();
        this.projects = res.projects;
        if (!silent) this.loadError = "";
      } catch (e) {
        if (!silent) this.loadError = errMsg(e);
      }
    },

    projectOptionLabel(p: ProjectWithCounts): string {
      const c = p.task_counts;
      return `${p.name}  (${c.todo}·${c.doing}·${c.done})`;
    },

    // ------------------------------------------------------------ new project
    showNewProject: false,
    newProject: { name: "", description: "" },
    projectBusy: false,

    async createProject(): Promise<void> {
      const name = this.newProject.name.trim();
      if (this.projectBusy || !name) return;
      this.projectBusy = true;
      try {
        const p = await api.createProject({
          actor: this.actor(),
          name,
          description: this.newProject.description.trim() || undefined,
        });
        this.showNewProject = false;
        this.newProject = { name: "", description: "" };
        await this.loadProjects();
        this.selectedProjectId = p.id;
        try {
          localStorage.setItem(LS_PROJECT, p.id);
        } catch { /* ignore */ }
        await this.loadBoard();
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.projectBusy = false;
      }
    },

    // ------------------------------------------------------------------- board
    selectedProjectId: (typeof localStorage !== "undefined" ? localStorage.getItem(LS_PROJECT) : "") ?? "",
    board: null as Board | null,

    /** IDs of tasks known to be blocked right now — summary rows don't carry
     *  the flag, so it is derived (see refreshBlockedSet) plus learned from
     *  detail fetches (knownBlocked). */
    blockedIds: new Set<string>(),
    knownBlocked: {} as Record<string, boolean>,

    isBlocked(id: string): boolean {
      return this.blockedIds.has(id) || !!this.knownBlocked[id];
    },

    columns(): { status: Status; label: string; tasks: TaskSummary[] }[] {
      const b = this.board;
      return STATUSES.map((s) => ({
        status: s,
        label: STATUS_LABEL[s],
        tasks: b ? b.tasks[s] ?? [] : [],
      }));
    },

    selectProject(): void {
      try {
        localStorage.setItem(LS_PROJECT, this.selectedProjectId);
      } catch { /* ignore */ }
      this.board = null;
      void this.loadBoard();
    },

    async loadBoard(silent = false): Promise<void> {
      if (!this.selectedProjectId) return;
      try {
        const b = await api.getBoard(this.selectedProjectId);
        this.board = b;
        if (!silent) this.loadError = "";
        await this.refreshBlockedSet(b);
      } catch (e) {
        if (!silent) this.loadError = errMsg(e);
      }
    },

    /** Derive which open tasks are blocked without N extra calls: one
     *  list_tasks plus the same call with unblocked=true, then diff by id.
     *  Skipped when the project has more than `limit` open tasks (MVP). */
    async refreshBlockedSet(b: Board): Promise<void> {
      const total = STATUSES.reduce((n, s) => n + (b.tasks[s]?.length ?? 0), 0);
      if (total === 0) {
        this.blockedIds = new Set();
        return;
      }
      const limit = 100;
      try {
        const [all, un] = await Promise.all([
          api.listTasks({ project_id: b.project_id, limit }),
          api.listTasks({ project_id: b.project_id, unblocked: true, limit }),
        ]);
        if (all.total > limit || un.total > limit) return; // too big to diff cheaply
        const unIds = new Set(un.tasks.map((t) => t.id));
        this.blockedIds = new Set(
          all.tasks.filter((t) => !unIds.has(t.id)).map((t) => t.id),
        );
      } catch {
        /* keep whatever we had — advisory feature only */
      }
    },

    // ------------------------------------------------------------------ polling
    /** Lightweight ~15s poll of board (+ open detail + project counts). No websockets. */
    polling: true,
    pollTimer: 0 as number,

    pollingChanged(): void {
      if (this.polling) this.startPolling();
      else this.stopPolling();
    },

    startPolling(): void {
      this.stopPolling();
      this.pollTimer = window.setInterval(() => void this.pollTick(), POLL_MS);
    },

    stopPolling(): void {
      if (this.pollTimer) {
        window.clearInterval(this.pollTimer);
        this.pollTimer = 0;
      }
    },

    async pollTick(): Promise<void> {
      if (document.hidden || !this.selectedProjectId) return;
      await this.loadProjects(true);
      await this.loadBoard(true);
      if (this.detailOpen && this.detail) await this.refreshDetail(true);
    },

    // ---------------------------------------------------------------- new task
    showNewTask: false,
    newTaskTarget: "todo" as Status,
    newTask: { title: "", description: "", priority: 0, due: "", acs: "" },
    taskBusy: false,
    /** possible_duplicates advisory from the last create — shown non-blockingly. */
    duplicates: [] as TaskSummary[],

    openNewTask(status: Status = "todo"): void {
      this.newTaskTarget = status;
      this.newTask = { title: "", description: "", priority: 0, due: "", acs: "" };
      this.duplicates = [];
      this.showNewTask = true;
    },

    closeNewTask(): void {
      this.showNewTask = false;
      this.duplicates = [];
    },

    clearDuplicates(): void {
      this.duplicates = [];
    },

    async createTask(): Promise<void> {
      const title = this.newTask.title.trim();
      if (this.taskBusy || !title || !this.selectedProjectId) return;
      this.taskBusy = true;
      let created: TaskSummary | null = null;
      try {
        const res = await api.createTask({
          actor: this.actor(),
          title,
          description: this.newTask.description.trim() || undefined,
          project_id: this.selectedProjectId,
          priority: this.newTask.priority,
          due_at: dateInputToIso(this.newTask.due),
          acceptance_criteria: splitLines(this.newTask.acs),
        });
        created = res.task;
        // Target column other than To Do: the create lands in todo (v1) and we
        // follow up with ONE version-guarded move. A race here is advisory —
        // the next poll settles the truth.
        if (this.newTaskTarget !== "todo") {
          try {
            await api.updateTask(created.id, {
              actor: this.actor(),
              version: created.version,
              status: this.newTaskTarget,
            });
          } catch (e2) {
            if (!ApiError.isVersionConflict(e2)) throw e2;
            this.toast("New task changed elsewhere before the move — check its column.", "info");
          }
        }
        this.duplicates = res.possible_duplicates ?? [];
        this.showNewTask = false;
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.taskBusy = false;
        if (created) void Promise.all([this.loadProjects(), this.loadBoard(true)]);
        else void this.loadBoard(true); // board may have changed even on failure paths we care about
      }
    },

    // ------------------------------------------------------------ task detail
    detailOpen: false,
    /** The task id currently open (or being opened) — decoupled from the row so
     *  a refresh can start even while `detail` is momentarily null. */
    detailId: null as string | null,
    detail: null as TaskDetail | null,
    /** id of the task being moved (disables status buttons mid-flight). */
    busyStatus: "",

    async openTask(id: string): Promise<void> {
      this.detailOpen = true;
      this.detailId = id;
      this.detail = null;
      await this.refreshDetail(false);
    },

    closeDetail(): void {
      this.detailOpen = false;
    },

    /** Fetch GET /tasks/{id} (full resume payload, comment tail 50) and apply it.
     *  An in-progress notes edit (notesDirty) is NEVER clobbered by a refresh. */
    async refreshDetail(silent = false): Promise<boolean> {
      const id = this.detailId ?? this.detail?.id;
      if (!id) return false;
      try {
        const t = await api.getTask(id, { commentLimit: 50 });
        this.applyDetail(t);
        return true;
      } catch (e) {
        if (!silent) this.toast(errMsg(e), "error");
        return false;
      }
    },

    applyDetail(t: TaskDetail): void {
      this.detail = t;
      // Learn the blocked state for card badges (the board diff is authoritative
      // on poll; details fill in between polls and cover >100-task projects).
      const kb = { ...this.knownBlocked };
      if (t.blocked) kb[t.id] = true;
      else delete kb[t.id];
      this.knownBlocked = kb;
      if (!this.notesDirty) this.notesDraft = t.notes;
    },

    /** Version-guarded status move. On 409 version_conflict: re-read the task,
     *  re-apply the SAME intent once with the fresh version; a second conflict
     *  just refreshes and says so (per docs/API.md error contract). */
    async moveStatus(status: Status): Promise<void> {
      const t = this.detail;
      if (!t || this.busyStatus || t.status === status) return;
      this.busyStatus = t.id;
      try {
        await api.updateTask(t.id, { actor: this.actor(), version: t.version, status });
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          try {
            const fresh = await api.getTask(t.id, { commentLimit: 50 });
            this.applyDetail(fresh);
            if (fresh.status !== status) {
              await api.updateTask(t.id, {
                actor: this.actor(),
                version: fresh.version,
                status,
              });
            }
            await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
            this.toast("Changed elsewhere — retried with the fresh version.", "info");
          } catch (e2) {
            await this.refreshDetail(true).catch(() => undefined);
            if (ApiError.isVersionConflict(e2)) {
              this.toast("Changed elsewhere — refreshed (your move was not applied).", "info");
            } else {
              this.toast(errMsg(e2), "error");
            }
          }
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.busyStatus = "";
      }
    },

    // ------------------------------------------------------------------- notes
    notesDraft: "",
    notesDirty: false,
    savingNotes: false,

    /** PUT /notes replaces the whole blob and carries NO version — a concurrent
     *  write can still 409; then we refresh (keeping the user's draft) and say so. */
    async saveNotes(): Promise<void> {
      const t = this.detail;
      if (!t || this.savingNotes) return;
      this.savingNotes = true;
      try {
        await api.putNotes(t.id, { actor: this.actor(), notes: this.notesDraft });
        this.notesDirty = false;
        await Promise.all([this.refreshDetail(true), this.loadBoard(true)]);
      } catch (e) {
        if (ApiError.isVersionConflict(e)) {
          // notesDraft survives the refresh because notesDirty is still true
          await this.refreshDetail(true).catch(() => undefined);
          this.toast("Notes changed elsewhere — refreshed. Review before re-saving.", "info");
        } else {
          this.toast(errMsg(e), "error");
        }
      } finally {
        this.savingNotes = false;
      }
    },

    // -------------------------------------------------------- acceptance crits
    acBusySeq: 0,

    /** Toggle one criterion; the 200 response carries the updated AC row. */
    async toggleAc(ac: AcRow): Promise<void> {
      const t = this.detail;
      if (!t || this.acBusySeq !== 0) return;
      this.acBusySeq = ac.seq;
      try {
        const updated = await api.toggleAc(t.id, ac.seq, this.actor(), !ac.done);
        const d = this.detail; // may have been refreshed meanwhile — only patch ours
        if (d && d.id === t.id) {
          this.detail = {
            ...d,
            acceptance_criteria: d.acceptance_criteria.map((x) =>
              x.seq === updated.seq ? updated : x,
            ),
          };
        }
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.acBusySeq = 0;
      }
    },

    acDoneCount(): number {
      return this.detail ? this.detail.acceptance_criteria.filter((a) => a.done).length : 0;
    },

    completedLabel(ac: AcRow): string {
      const who = ac.completed_by ? actorLabel(ac.completed_by) : "unknown";
      const when = ac.completed_at ? relTime(ac.completed_at) : "";
      return `${who}${when ? ` · ${when}` : ""}`;
    },

    // ----------------------------------------------------------------- comments
    commentForm: { kind: "progress" as CommentKind, body: "" },
    postingComment: false,

    /** Append a typed comment (no task version bump). Appends are NOT
     *  idempotent — on network error we don't auto-retry. */
    async postComment(): Promise<void> {
      const t = this.detail;
      const body = this.commentForm.body.trim();
      if (!t || !body || this.postingComment) return;
      this.postingComment = true;
      try {
        const c = await api.addComment(t.id, {
          actor: this.actor(),
          kind: this.commentForm.kind,
          body,
        });
        const d = this.detail;
        if (d && d.id === t.id) this.detail = { ...d, comments: [c, ...d.comments] };
        this.commentForm.body = "";
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.postingComment = false;
      }
    },

    // ---------------------------------------------------------------- artifacts
    artifactForm: { open: false, kind: "url" as ArtifactKind, ref: "", summary: "" },
    attachingArtifact: false,

    async attachArtifact(): Promise<void> {
      const t = this.detail;
      const ref = this.artifactForm.ref.trim();
      if (!t || !ref || this.attachingArtifact) return;
      this.attachingArtifact = true;
      try {
        const a = await api.addArtifact(
          t.id,
          this.artifactForm.kind,
          ref,
          this.artifactForm.summary.trim() || undefined,
          this.actor(),
        );
        const d = this.detail;
        if (d && d.id === t.id) this.detail = { ...d, artifacts: [...d.artifacts, a] };
        this.artifactForm = { open: false, kind: "url", ref: "", summary: "" };
      } catch (e) {
        this.toast(errMsg(e), "error");
      } finally {
        this.attachingArtifact = false;
      }
    },

    // ---------------------------------------------------------------- relations
    /** Resolve relation edges to display rows (direction-aware for child and
     *  blocked-by; the wire never exposes a "parent" kind). */
    relationRows(): RelationView[] {
      const d = this.detail;
      if (!d) return [];
      const me = d.id;
      return d.relations.map((r) => {
        let label: string;
        let otherId: string;
        if (r.kind === "child") {
          // stored edge: task_id is the parent, other_task_id the child
          if (r.task_id === me) {
            label = "Child";
            otherId = r.other_task_id;
          } else {
            label = "Parent";
            otherId = r.task_id;
          }
        } else if (r.kind === "blocked-by") {
          if (r.task_id === me) {
            label = "Blocked by";
            otherId = r.other_task_id;
          } else {
            label = "Blocking";
            otherId = r.task_id;
          }
        } else if (r.kind === "duplicate-of") {
          label = "Duplicate of";
          otherId = r.other_task_id;
        } else {
          label = "Related to";
          otherId = r.other_task_id;
        }
        return { key: `${r.task_id}:${r.other_task_id}:${r.kind}`, label, otherId, title: this.taskTitle(otherId) };
      });
    },

    taskTitle(id: string): string {
      const b = this.board;
      if (b) {
        for (const s of STATUSES) {
          const row = b.tasks[s]?.find((t) => t.id === id);
          if (row) return row.title;
        }
      }
      return shortId(id);
    },

    /** Escape key: close the top-most overlay (detail → task form → project form). */
    dismissTop(): void {
      if (this.detailOpen) this.closeDetail();
      else if (this.showNewTask) this.closeNewTask();
      else if (this.showNewProject) this.showNewProject = false;
    },

    isWebHref(s: string | null | undefined): boolean {
      return typeof s === "string" && /^(https?:|mailto:)/i.test(s.trim());
    },

    // ------------------------------------------------------------- template glue
    statusLabel,
    priorityClass,
    priorityName,
    commentKindClass,
    artifactKindClass,
    isOverdue,
    dueLabel,
    claimHint,
    actorLabel,
    initials,
    shortId,
    relTime,
    /** Sanitized-markdown renderer (escaped source → marked → DOM post-pass),
     *  memoized so poll re-renders don't re-parse unchanged bodies. */
    _mdCache: new Map<string, string>(),
    md(src: string | null | undefined): string {
      if (!src || !src.trim()) return "";
      const hit = this._mdCache.get(src);
      if (hit !== undefined) return hit;
      const out = renderMarkdown(src);
      if (this._mdCache.size > MD_CACHE_MAX) this._mdCache.clear();
      this._mdCache.set(src, out);
      return out;
    },

    // ---------------------------------------------------------------------- init
    async init(): Promise<void> {
      await this.loadProjects();
      const known = this.projects.some((p) => p.id === this.selectedProjectId);
      if (!known || !this.selectedProjectId) {
        this.selectedProjectId = this.projects[0]?.id ?? "";
      }
      if (this.selectedProjectId) {
        try {
          localStorage.setItem(LS_PROJECT, this.selectedProjectId);
        } catch { /* ignore */ }
        await this.loadBoard();
      } else {
        // No projects yet — invite creating one.
        this.showNewProject = true;
      }
      if (this.polling) this.startPolling();
    },
  };
}
