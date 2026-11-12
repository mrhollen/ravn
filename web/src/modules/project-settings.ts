// Project settings dialog module — Phase 2 batch B slice "project-settings" of
// the frontend modularization epic. Owns, moved from app.ts:
//   1. the settings form lifecycle — seed from the list row on open, then the
//      authoritative getProject re-fetch (and the pristine baseline that is
//      re-based on it, powering the "has anything actually changed?" discard
//      guard, task 01M3MMZ188XJBGKFSWBGESK79D);
//   2. the guarded close path (× / Cancel / backdrop / Esc confirm-first) and
//      the single close path used by save / archive success;
//   3. the archive-confirm flow (type-the-project-name validation) plus
//      archiveProject / restoreProject, including the reselection fallback when
//      the selected project leaves the list;
//   4. saveProjectSettings — payload assembly with the baseline-relative
//      empty-string-or-undefined clearing semantics for the opencode fields;
//   5. the in-dialog members panel (T9) — roster read state, manage decision,
//      add / role-change / remove and their never-throw refetch loops.
// Every body moved verbatim from app.ts except `this.` → closure/deps renames
// and the focus step, which now goes through the focusByTestid hook so DOM
// access stays on the root (see docs/FRONTEND_ARCHITECTURE.md §2).
//
// State-ownership note (Alpine constraint): every template-bound name
// (showProjectSettings, projectSettingsForm, projectSettingsBusy,
// showArchiveConfirm, archiveConfirmInput, membersState, membersRoster,
// membersBusy, memberAddName, memberAddRole) still lives on the flat component
// root, touched here exclusively through the typed accessors on
// {@link ProjectSettingsDeps}. The pristine baseline copy and the dialog focus
// session are INTERNAL — never rendered, never part of the flat namespace —
// and are owned in this module's closure.

import { api, errMsg } from "../api";
import { canManageRoster, memberMutationErrorCopy, normalizeRoster } from "../members";
import { LS_PROJECT } from "./projects";
import { safeStorage } from "../util";
import type { DialogFocus } from "../focus";
import type {
  ActorInput,
  MemberRole,
  MembersRosterDTO,
  ProjectWithCounts,
  SessionIdentity,
  UpdateProjectReq,
} from "../types";

// ----------------------------------------------------------------------- types
/** The seven settings form fields (the template x-models every one of them). */
export interface ProjectSettingsFormState {
  name: string;
  description: string;
  opencode_server_url: string;
  opencode_directory: string;
  opencode_model: string;
  opencode_agent: string;
  opencode_variant: string;
}

/** Roster read state for the settings modal's Members section (T9). */
export type MembersReadState = "" | "loading" | "ready" | "unavailable";

// ----------------------------------------------------------------------- deps
export interface ProjectSettingsDeps {
  // state-in
  selectedProjectId: () => string;
  projects: () => ProjectWithCounts[];
  identity: () => SessionIdentity | null;
  showProjectSettings: () => boolean;
  projectSettingsForm: () => ProjectSettingsFormState;
  projectSettingsBusy: () => boolean;
  archiveConfirmInput: () => string;
  membersRoster: () => MembersRosterDTO | null;
  membersBusy: () => boolean;
  memberAddName: () => string;
  memberAddRole: () => MemberRole;
  // state-out
  setShowProjectSettings: (v: boolean) => void;
  setProjectSettingsForm: (f: ProjectSettingsFormState) => void;
  setProjectSettingsBusy: (v: boolean) => void;
  setShowArchiveConfirm: (v: boolean) => void;
  setArchiveConfirmInput: (v: string) => void;
  setSelectedProjectId: (v: string) => void;
  clearBoard: () => void;
  setShowNewProject: (v: boolean) => void;
  setMembersState: (s: MembersReadState) => void;
  setMembersRoster: (r: MembersRosterDTO | null) => void;
  setMembersBusy: (v: boolean) => void;
  setMemberAddName: (v: string) => void;
  setMemberAddRole: (r: MemberRole) => void;
  // hooks — root-owned orchestration that stays outside this module
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
  confirmDiscard: (message: string) => boolean;
  openDialog: (id: string, initialSelector?: string) => DialogFocus | null;
  closeDialog: (focus: DialogFocus | null) => void;
  /** Focus a dialog element by data-testid (guard + query + focus on the root). */
  focusByTestid: (testId: string) => void;
  loadProjects: () => Promise<void>;
  loadBoard: () => Promise<unknown>;
  connectSse: () => void;
  disconnectSse: (resetBackoff?: boolean) => void;
  /** Open-code option refresh after the authoritative fetch — owned by
   *  modules/opencode.ts (Phase 2 batch B slice "opencode"). Result unused here. */
  fetchOpenCodeOptions: (projectId: string, force?: boolean) => Promise<unknown>;
}

export function createProjectSettingsModule(deps: ProjectSettingsDeps) {
  // Internal state (flat root fields before Phase 2): never rendered, owned here.
  /** Pristine copy of the form captured when the dialog opens (and re-synced
   *  after the authoritative fetch) — the "has anything actually changed?"
   *  reference for the discard guard (task 01M3MMZ188XJBGKFSWBGESK79D). */
  let projectSettingsBaseline: ProjectSettingsFormState = {
    name: "",
    description: "",
    opencode_server_url: "",
    opencode_directory: "",
    opencode_model: "",
    opencode_agent: "",
    opencode_variant: "",
  };
  /** Focus session for the project settings modal (trap + restore). */
  let projectSettingsFocus: DialogFocus | null = null;

  async function openProjectSettings(): Promise<void> {
    if (!deps.selectedProjectId()) return;
    deps.setShowArchiveConfirm(false);
    deps.setArchiveConfirmInput("");
    const p = deps.projects().find((x) => x.id === deps.selectedProjectId());
    if (p) {
      deps.setProjectSettingsForm({
        name: p.name,
        description: p.description ?? "",
        opencode_server_url: p.opencode_server_url ?? "",
        opencode_directory: p.opencode_directory ?? "",
        opencode_model: p.opencode_model ?? "",
        opencode_agent: p.opencode_agent ?? "",
        opencode_variant: p.opencode_variant ?? "",
      });
    } else {
      deps.setProjectSettingsForm({
        name: "",
        description: "",
        opencode_server_url: "",
        opencode_directory: "",
        opencode_model: "",
        opencode_agent: "",
        opencode_variant: "",
      });
    }
    // Pristine reference for the discard guard (re-synced after the fetch).
    projectSettingsBaseline = { ...deps.projectSettingsForm() };
    deps.setShowProjectSettings(true);
    // Name field first — least destructive control in this dialog.
    projectSettingsFocus = deps.openDialog("cb-project-settings", "input");

    // Members panel (T9): fresh per open — the roster + manage decision both
    // key off selectedProjectId and the session identity.
    deps.setMemberAddName("");
    deps.setMemberAddRole("viewer");
    deps.setMembersRoster(null);
    void loadMembers();

    try {
      const full = await api.getProject(deps.selectedProjectId());
      deps.setProjectSettingsForm({
        name: full.name,
        description: full.description ?? "",
        opencode_server_url: full.opencode_server_url ?? "",
        opencode_directory: full.opencode_directory ?? "",
        opencode_model: full.opencode_model ?? "",
        opencode_agent: full.opencode_agent ?? "",
        opencode_variant: full.opencode_variant ?? "",
      });
      // The fetch is authoritative — re-base "pristine" on it.
      projectSettingsBaseline = { ...deps.projectSettingsForm() };
      if (full.opencode_server_url) {
        void deps.fetchOpenCodeOptions(deps.selectedProjectId());
      }
    } catch {
      /* keep current list info */
    }
  }

  /** Single close path for the settings modal (save / archive success).
   *  User-initiated dismissals (× / Cancel / backdrop / Esc) must go through
   *  requestCloseProjectSettings() so unsaved changes are never lost. */
  function closeProjectSettings(): void {
    deps.setShowProjectSettings(false);
    deps.setShowArchiveConfirm(false);
    deps.setArchiveConfirmInput("");
    deps.closeDialog(projectSettingsFocus);
    projectSettingsFocus = null;
  }

  /** True when any settings field differs from what was loaded on open. */
  function projectSettingsDirty(): boolean {
    const f = deps.projectSettingsForm();
    const b = projectSettingsBaseline;
    return (
      (f.name ?? "") !== (b.name ?? "") ||
      (f.description ?? "") !== (b.description ?? "") ||
      (f.opencode_server_url ?? "") !== (b.opencode_server_url ?? "") ||
      (f.opencode_directory ?? "") !== (b.opencode_directory ?? "") ||
      (f.opencode_model ?? "") !== (b.opencode_model ?? "") ||
      (f.opencode_agent ?? "") !== (b.opencode_agent ?? "") ||
      (f.opencode_variant ?? "") !== (b.opencode_variant ?? "")
    );
  }

  /** Guarded close for the settings dialog (× / Cancel / backdrop / Esc):
   *  asks first when unsaved changes would be lost. */
  function requestCloseProjectSettings(): void {
    if (
      projectSettingsDirty() &&
      !deps.confirmDiscard("Discard unsaved project settings changes?")
    )
      return;
    closeProjectSettings();
  }

  function openArchiveConfirm(): void {
    deps.setShowArchiveConfirm(true);
    deps.setArchiveConfirmInput("");
    deps.focusByTestid("archive-confirm-input");
  }

  function cancelArchiveConfirm(): void {
    deps.setShowArchiveConfirm(false);
    deps.setArchiveConfirmInput("");
    deps.focusByTestid("archive-project-btn");
  }

  function isArchiveInputValid(): boolean {
    const p = deps.projects().find((x) => x.id === deps.selectedProjectId());
    const target = (p ? p.name : deps.projectSettingsForm().name).trim();
    return Boolean(target && deps.archiveConfirmInput().trim() === target);
  }

  async function archiveProject(): Promise<void> {
    if (!deps.selectedProjectId() || !isArchiveInputValid() || deps.projectSettingsBusy()) return;
    deps.setProjectSettingsBusy(true);
    const targetId = deps.selectedProjectId();
    try {
      await api.updateProject(targetId, {
        actor: deps.actor(),
        archived: true,
      });
      closeProjectSettings();
      await deps.loadProjects();
      const stillSelected = deps.projects().find((x) => x.id === deps.selectedProjectId());
      if (!stillSelected) {
        deps.setSelectedProjectId(deps.projects()[0]?.id ?? "");
        if (deps.selectedProjectId()) {
          try {
            safeStorage.setItem(LS_PROJECT, deps.selectedProjectId());
          } catch { /* ignore */ }
          deps.connectSse();
          await deps.loadBoard();
        } else {
          try {
            safeStorage.removeItem(LS_PROJECT);
          } catch { /* ignore */ }
          deps.disconnectSse(true);
          deps.clearBoard();
          deps.setShowNewProject(true);
        }
      }
      deps.toast("Project archived");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setProjectSettingsBusy(false);
    }
  }

  /** Restore an ARCHIVED project from the project menu's "Archived" section:
   *  PUT {archived:false}, then refresh BOTH project lists. The menu stays
   *  open — the row just disappears (it left `archivedProjects`) and the
   *  project reappears as a normal option. No board work is needed unless the
   *  user picks it next (pickProject → selectProject handles that). */
  async function restoreProject(projectId: string): Promise<void> {
    if (!projectId || deps.projectSettingsBusy()) return;
    deps.setProjectSettingsBusy(true); // shared busy flag — prevents a double-fire on the same row
    try {
      await api.updateProject(projectId, { actor: deps.actor(), archived: false });
      await deps.loadProjects();
      deps.toast("Project restored");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setProjectSettingsBusy(false);
    }
  }

  async function saveProjectSettings(): Promise<void> {
    const name = deps.projectSettingsForm().name.trim();
    if (!deps.selectedProjectId() || !name || deps.projectSettingsBusy()) return;
    deps.setProjectSettingsBusy(true);
    try {
      const f = deps.projectSettingsForm();
      const agentTrim = f.opencode_agent?.trim() ?? "";
      const variantTrim = f.opencode_variant?.trim() ?? "";
      const serverUrlTrim = f.opencode_server_url?.trim() ?? "";
      const directoryTrim = f.opencode_directory?.trim() ?? "";
      const modelTrim = f.opencode_model?.trim() ?? "";

      const updatePayload: UpdateProjectReq = {
        actor: deps.actor(),
        name,
        description: f.description?.trim() || undefined,
        opencode_server_url: serverUrlTrim || (projectSettingsBaseline.opencode_server_url ? "" : undefined),
        opencode_directory: directoryTrim || (projectSettingsBaseline.opencode_directory ? "" : undefined),
        opencode_model: modelTrim || (projectSettingsBaseline.opencode_model ? "" : undefined),
        opencode_agent: agentTrim || (projectSettingsBaseline.opencode_agent ? "" : undefined),
        opencode_variant: variantTrim || (projectSettingsBaseline.opencode_variant ? "" : undefined),
      };
      await api.updateProject(deps.selectedProjectId(), updatePayload);
      await deps.loadProjects();
      closeProjectSettings();
      deps.toast("Project updated");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setProjectSettingsBusy(false);
    }
  }

  // ------------------------------------------------------------------ members
  /** Client-side manage decision for the roster (owner or stored admin; see
   *  canManageRoster). Read-only members get the roster with NO write controls. */
  function canManageMembers(): boolean {
    return canManageRoster(deps.membersRoster(), deps.identity()?.name ?? null);
  }

  /** True when a stored row is ALSO the project owner (ownership derives from
   *  created_by — the row stays editable; ownership is only badged). */
  function isOwnerMember(username: string): boolean {
    const o = deps.membersRoster()?.owner;
    return !!o && !!username && o.name.trim() === username;
  }

  /** Fetches the roster for the selected project. Never throws — every outcome
   *  (200 malformed, 403 feature-off/forbidden, 404 not-enabled) lands in
   *  membersState so the section renders or hides accordingly. */
  async function loadMembers(): Promise<void> {
    if (!deps.selectedProjectId()) return;
    // Membership management only exists on authenticated deployments (a session,
    // or at least a shared/operator token). In zero-config local mode every GET is
    // a guaranteed 403 — skip the request entirely: Chromium logs any failed resource
    // as console.error, and the E2E suite treats that as a test failure.
    if (!deps.identity() && !api.getAuthToken()) {
      deps.setMembersState("unavailable");
      return;
    }
    const pid = deps.selectedProjectId();
    deps.setMembersState("loading");
    try {
      const raw = await api.listMembers(pid);
      const roster = normalizeRoster(raw);
      // Late resolution after the modal closed / project changed: drop it.
      if (!deps.showProjectSettings() || deps.selectedProjectId() !== pid) return;
      deps.setMembersRoster(roster);
      deps.setMembersState(roster ? "ready" : "unavailable");
    } catch {
      if (deps.showProjectSettings() && deps.selectedProjectId() === pid) {
        deps.setMembersRoster(null);
        deps.setMembersState("unavailable");
      }
    }
  }

  /** Add or re-grant (server upsert: 201 created / 200 role-change), then
   *  refetch — the server emits no membership SSE events, so the local roster
   *  is the only source of truth. */
  async function addMember(): Promise<void> {
    if (deps.membersBusy() || !deps.selectedProjectId()) return;
    const username = deps.memberAddName().trim();
    if (!username) return;
    const role = deps.memberAddRole();
    deps.setMembersBusy(true);
    try {
      const resp = await api.addMember(deps.selectedProjectId(), username, role);
      deps.toast(
        resp.created ? `${username} added as ${role}` : `${username}'s role is now ${role}`
      );
      deps.setMemberAddName("");
      await loadMembers();
    } catch (e) {
      deps.toast(memberMutationErrorCopy(e, username), "error");
      void loadMembers(); // server state may have moved — resync the roster
    } finally {
      deps.setMembersBusy(false);
    }
  }

  /** Role change on an EXISTING row (x-model has already put the new role on
   *  the row). On failure the refetch below restores the server's value, which
   *  rolls the <select> back automatically. */
  async function changeMemberRole(username: string): Promise<void> {
    if (deps.membersBusy() || !deps.selectedProjectId() || !deps.membersRoster()) return;
    const row = deps.membersRoster()?.members.find((m) => m.username === username);
    if (!row) return;
    deps.setMembersBusy(true);
    try {
      await api.setMemberRole(deps.selectedProjectId(), username, row.role);
      deps.toast(`${username} is now ${row.role}`);
      await loadMembers();
    } catch (e) {
      deps.toast(memberMutationErrorCopy(e, username), "error");
      void loadMembers();
    } finally {
      deps.setMembersBusy(false);
    }
  }

  async function removeMember(username: string): Promise<void> {
    if (deps.membersBusy() || !deps.selectedProjectId()) return;
    deps.setMembersBusy(true);
    try {
      await api.removeMember(deps.selectedProjectId(), username);
      deps.toast(`${username} removed`);
      await loadMembers();
    } catch (e) {
      deps.toast(memberMutationErrorCopy(e, username), "error");
      void loadMembers();
    } finally {
      deps.setMembersBusy(false);
    }
  }

  return {
    openProjectSettings, closeProjectSettings, projectSettingsDirty,
    requestCloseProjectSettings, openArchiveConfirm, cancelArchiveConfirm,
    isArchiveInputValid, archiveProject, restoreProject, saveProjectSettings,
    canManageMembers, isOwnerMember, loadMembers, addMember, changeMemberRole,
    removeMember,
  };
}

export type ProjectSettingsModule = ReturnType<typeof createProjectSettingsModule>;
