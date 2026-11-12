// Unit tests for web/src/modules/project-settings.ts (Phase 2 batch B slice
// "project-settings"). Drives createProjectSettingsModule() with a mutable
// stub state + recorded hook calls and monkey-patched api methods — no DOM,
// no network. The root-level surface (the delegation shims) stays covered in
// test/app.test.ts; this file covers the moved internals: form seeding + the
// pristine baseline re-based on the authoritative fetch, the discard guard,
// the archive-confirm flow with its reselection fallback, save/restore
// payload semantics, and the members panel (T9) lifecycle.

import test from "node:test";
import assert from "node:assert/strict";
import { createProjectSettingsModule } from "../src/modules/project-settings";
import type { ProjectSettingsDeps, ProjectSettingsFormState, MembersReadState } from "../src/modules/project-settings";
import { api } from "../src/api";
import type { MemberRole, MembersRosterDTO, ProjectWithCounts, SessionIdentity } from "../src/types";

// ------------------------------------------------------------------- harness
const emptyForm = (): ProjectSettingsFormState => ({
  name: "",
  description: "",
  opencode_server_url: "",
  opencode_directory: "",
  opencode_model: "",
  opencode_agent: "",
  opencode_variant: "",
});

function makeIdentity(name = "ada"): SessionIdentity {
  return { id: "id-1", name, kind: "human", auth_method: "password", created_at: "2026-01-01T00:00:00Z" };
}

function project(id: string, name: string, extra: Record<string, unknown> = {}): ProjectWithCounts {
  return { id, name, description: "", counts: { todo: 0, doing: 0, done: 0 }, archived: false, ...extra } as ProjectWithCounts;
}

/** One mutable state object wired into the deps accessors — the module reads
 *  and writes exactly through those, so assertions read straight off `state`. */
function makeHarness(init?: Partial<Record<string, unknown>>) {
  const state: Record<string, any> = {
    selectedProjectId: "",
    projects: [] as ProjectWithCounts[],
    identity: null as SessionIdentity | null,
    showProjectSettings: false,
    projectSettingsForm: emptyForm(),
    projectSettingsBusy: false,
    showArchiveConfirm: false,
    archiveConfirmInput: "",
    showNewProject: false,
    membersState: "" as MembersReadState,
    membersRoster: null as MembersRosterDTO | null,
    membersBusy: false,
    memberAddName: "",
    memberAddRole: "viewer" as MemberRole,
  };
  Object.assign(state, init);

  const calls = {
    toast: [] as Array<{ msg: string; kind?: "error" | "info" }>,
    confirmDiscard: [] as string[],
    openDialog: [] as string[],
    closeDialog: [] as unknown[],
    focusByTestid: [] as string[],
    clearBoard: 0,
    loadProjects: 0,
    loadBoard: 0,
    connectSse: 0,
    disconnectSse: [] as Array<boolean | undefined>,
    fetchOpenCodeOptions: [] as string[],
  };

  let confirmAnswer = false;
  const deps: ProjectSettingsDeps = {
    selectedProjectId: () => state.selectedProjectId,
    projects: () => state.projects,
    identity: () => state.identity,
    showProjectSettings: () => state.showProjectSettings,
    projectSettingsForm: () => state.projectSettingsForm,
    projectSettingsBusy: () => state.projectSettingsBusy,
    archiveConfirmInput: () => state.archiveConfirmInput,
    membersRoster: () => state.membersRoster,
    membersBusy: () => state.membersBusy,
    memberAddName: () => state.memberAddName,
    memberAddRole: () => state.memberAddRole,
    setShowProjectSettings: (v) => { state.showProjectSettings = v; },
    setProjectSettingsForm: (f) => { state.projectSettingsForm = f; },
    setProjectSettingsBusy: (v) => { state.projectSettingsBusy = v; },
    setShowArchiveConfirm: (v) => { state.showArchiveConfirm = v; },
    setArchiveConfirmInput: (v) => { state.archiveConfirmInput = v; },
    setSelectedProjectId: (v) => { state.selectedProjectId = v; },
    clearBoard: () => { calls.clearBoard++; },
    setShowNewProject: (v) => { state.showNewProject = v; },
    setMembersState: (s) => { state.membersState = s; },
    setMembersRoster: (r) => { state.membersRoster = r; },
    setMembersBusy: (v) => { state.membersBusy = v; },
    setMemberAddName: (v) => { state.memberAddName = v; },
    setMemberAddRole: (r) => { state.memberAddRole = r; },
    toast: (msg, kind) => { calls.toast.push({ msg, kind }); return 1; },
    actor: () => ({ name: "tester", type: "agent" }),
    confirmDiscard: (m) => { calls.confirmDiscard.push(m); return confirmAnswer; },
    openDialog: (id) => { calls.openDialog.push(id); return null; },
    closeDialog: (f) => { calls.closeDialog.push(f); },
    focusByTestid: (t) => { calls.focusByTestid.push(t); },
    loadProjects: async () => { calls.loadProjects++; },
    loadBoard: async () => { calls.loadBoard++; return {}; },
    connectSse: () => { calls.connectSse++; },
    disconnectSse: (reset) => { calls.disconnectSse.push(reset); },
    fetchOpenCodeOptions: (pid) => { calls.fetchOpenCodeOptions.push(pid); return Promise.resolve(null); },
  };

  const mod = createProjectSettingsModule(deps);
  return {
    state,
    calls,
    deps,
    mod,
    setConfirmAnswer: (b: boolean) => { confirmAnswer = b; },
  };
}

/** Monkey-patch api methods for the duration of a test; returns the restore fn. */
function patchApi(methods: Record<string, unknown>) {
  const restore: Array<[string, unknown]> = [];
  for (const [k, v] of Object.entries(methods)) {
    restore.push([k, (api as any)[k]]);
    (api as any)[k] = v;
  }
  return () => {
    for (const [k, orig] of restore) (api as any)[k] = orig;
  };
}

// ------------------------------------------------------------------ open/dirty

test("openProjectSettings: no-op without a selected project", async () => {
  const h = makeHarness();
  const restore = patchApi({ getProject: async () => assert.fail("must not fetch") });
  try {
    await h.mod.openProjectSettings();
    assert.equal(h.state.showProjectSettings, false);
    assert.deepEqual(h.calls.openDialog, []);
  } finally {
    restore();
  }
});

test("openProjectSettings: seeds the form from the list row, re-baselines on the authoritative fetch", async () => {
  const h = makeHarness({
    selectedProjectId: "p1",
    projects: [project("p1", "Row", { opencode_agent: "plan", opencode_variant: "high" })],
  });
  const restore = patchApi({
    getProject: async (id: string) => ({
      id,
      name: "Fetched",
      description: "fetched-desc",
      opencode_server_url: "http://oc",
      opencode_directory: "",
      opencode_model: "",
      opencode_agent: "build",
      opencode_variant: "low",
    }),
  });
  try {
    await h.mod.openProjectSettings();
    assert.equal(h.state.showProjectSettings, true);
    // The fetch is authoritative for every field.
    assert.deepEqual(h.state.projectSettingsForm, {
      name: "Fetched",
      description: "fetched-desc",
      opencode_server_url: "http://oc",
      opencode_directory: "",
      opencode_model: "",
      opencode_agent: "build",
      opencode_variant: "low",
    });
    assert.equal(h.mod.projectSettingsDirty(), false, "pristine after the re-based fetch");
    // Opening resets the archive-confirm + member-add state.
    assert.equal(h.state.showArchiveConfirm, false);
    assert.equal(h.state.archiveConfirmInput, "");
    assert.equal(h.state.memberAddName, "");
    assert.equal(h.state.memberAddRole, "viewer");
    assert.equal(h.state.membersRoster, null);
    // No session/token in the harness → the roster short-circuits.
    assert.equal(h.state.membersState, "unavailable");
    // The fetched server URL triggers an options refresh.
    assert.deepEqual(h.calls.fetchOpenCodeOptions, ["p1"]);
  } finally {
    restore();
  }
});

test("openProjectSettings: a failed fetch keeps the list-row values (and their baseline)", async () => {
  const h = makeHarness({
    selectedProjectId: "p1",
    projects: [project("p1", "Row", { description: "row-desc" })],
  });
  const restore = patchApi({ getProject: async () => { throw new Error("offline"); } });
  try {
    await h.mod.openProjectSettings();
    assert.equal(h.state.projectSettingsForm.name, "Row");
    assert.equal(h.state.projectSettingsForm.description, "row-desc");
    assert.equal(h.mod.projectSettingsDirty(), false, "baseline is the list-row seed");
  } finally {
    restore();
  }
});

test("projectSettingsDirty is baseline-relative; requestCloseProjectSettings honors it", async () => {
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha")] });
  const restore = patchApi({ getProject: async (id: string) => ({ id, name: "Alpha", description: "d" }) });
  try {
    await h.mod.openProjectSettings();
    assert.equal(h.mod.projectSettingsDirty(), false, "unmodified form is clean");

    // A clean close must not prompt.
    h.mod.requestCloseProjectSettings();
    assert.deepEqual(h.calls.confirmDiscard, [], "clean close skipped the prompt");
    assert.equal(h.state.showProjectSettings, false);

    await h.mod.openProjectSettings();
    h.state.projectSettingsForm.name = "Alpha WIP";
    assert.equal(h.mod.projectSettingsDirty(), true);

    // Decline keeps the modal with the draft intact.
    h.setConfirmAnswer(false);
    h.mod.requestCloseProjectSettings();
    assert.deepEqual(h.calls.confirmDiscard, ["Discard unsaved project settings changes?"]);
    assert.equal(h.state.showProjectSettings, true, "decline keeps the modal");
    assert.equal(h.state.projectSettingsForm.name, "Alpha WIP", "draft untouched on decline");

    // Accept closes it.
    h.setConfirmAnswer(true);
    h.mod.requestCloseProjectSettings();
    assert.equal(h.state.showProjectSettings, false, "accept closes the modal");
  } finally {
    restore();
  }
});

// ----------------------------------------------------------------- archive flow

test("isArchiveInputValid: trimmed exact match against the row name (form-name fallback)", () => {
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha")] });
  assert.equal(h.mod.isArchiveInputValid(), false, "empty input");
  h.state.archiveConfirmInput = "alpha"; // strict equality — case matters
  assert.equal(h.mod.isArchiveInputValid(), false);
  h.state.archiveConfirmInput = " Alpha ";
  assert.equal(h.mod.isArchiveInputValid(), true, "trimmed match");

  // No list row for the id → fall back to the form's name.
  h.state.selectedProjectId = "missing";
  h.state.projectSettingsForm.name = "Fallback";
  h.state.archiveConfirmInput = "fallback";
  assert.equal(h.mod.isArchiveInputValid(), false);
  h.state.archiveConfirmInput = "Fallback";
  assert.equal(h.mod.isArchiveInputValid(), true);
});

test("archiveProject: invalid input and busy are no-ops", async () => {
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha")] });
  let puts = 0;
  const restore = patchApi({ updateProject: async () => { puts++; return {}; } });
  try {
    await h.mod.archiveProject(); // empty input
    assert.equal(puts, 0);

    h.state.archiveConfirmInput = "Alpha";
    h.state.projectSettingsBusy = true;
    await h.mod.archiveProject();
    assert.equal(puts, 0, "busy blocks a double-fire");
  } finally {
    restore();
  }
});

test("archiveProject: PUT archived, closes the modal, refreshes projects", async () => {
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha"), project("p2", "Beta")] });
  let putBody: any = null;
  const restore = patchApi({ updateProject: async (_id: string, body: any) => { putBody = body; return {}; } });
  try {
    h.state.archiveConfirmInput = "Alpha";
    await h.mod.archiveProject();
    assert.equal(putBody.archived, true);
    assert.deepEqual(putBody.actor, { name: "tester", type: "agent" });
    assert.equal(h.state.showProjectSettings, false, "success closes the modal");
    assert.equal(h.calls.loadProjects, 1);
    // Still in the list → no reselection work.
    assert.equal(h.state.selectedProjectId, "p1");
    assert.equal(h.calls.connectSse, 0);
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["Project archived"]);
    assert.equal(h.state.projectSettingsBusy, false);
  } finally {
    restore();
  }
});

test("archiveProject: selected project leaves the list → reselect fallback", async () => {
  // Next project exists.
  const h1 = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha"), project("p2", "Beta")] });
  const r1 = patchApi({ updateProject: async () => ({}) });
  h1.deps.loadProjects = async () => {
    h1.calls.loadProjects++;
    h1.state.projects = [project("p2", "Beta")];
  };
  try {
    h1.state.archiveConfirmInput = "Alpha";
    await h1.mod.archiveProject();
    assert.equal(h1.state.selectedProjectId, "p2");
    assert.equal(h1.calls.connectSse, 1);
    assert.equal(h1.calls.loadBoard, 1);
  } finally {
    r1();
  }

  // No project left at all.
  const h2 = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha")] });
  const r2 = patchApi({ updateProject: async () => ({}) });
  h2.deps.loadProjects = async () => {
    h2.calls.loadProjects++;
    h2.state.projects = [];
  };
  try {
    h2.state.archiveConfirmInput = "Alpha";
    await h2.mod.archiveProject();
    assert.equal(h2.state.selectedProjectId, "");
    assert.deepEqual(h2.calls.disconnectSse, [true]);
    assert.equal(h2.calls.clearBoard, 1);
    assert.equal(h2.state.showNewProject, true);
  } finally {
    r2();
  }
});

test("archiveProject: a failed PUT toasts the error and keeps the modal open", async () => {
  // showProjectSettings: true — archiving happens from the open settings modal.
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Alpha")], showProjectSettings: true });
  const restore = patchApi({ updateProject: async () => { throw new Error("boom"); } });
  try {
    h.state.archiveConfirmInput = "Alpha";
    await h.mod.archiveProject();
    assert.equal(h.state.showProjectSettings, true);
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["boom"]);
    assert.equal(h.calls.toast[0].kind, "error");
    assert.equal(h.state.projectSettingsBusy, false);
  } finally {
    restore();
  }
});

test("openArchiveConfirm / cancelArchiveConfirm focus the right elements and reset the input", () => {
  const h = makeHarness({ archiveConfirmInput: "stale" });
  h.mod.openArchiveConfirm();
  assert.equal(h.state.showArchiveConfirm, true);
  assert.equal(h.state.archiveConfirmInput, "");
  assert.deepEqual(h.calls.focusByTestid, ["archive-confirm-input"]);

  h.state.archiveConfirmInput = "x";
  h.mod.cancelArchiveConfirm();
  assert.equal(h.state.showArchiveConfirm, false);
  assert.equal(h.state.archiveConfirmInput, "");
  assert.deepEqual(h.calls.focusByTestid, ["archive-confirm-input", "archive-project-btn"]);
});

test("restoreProject: PUT archived:false then refresh; empty id is a no-op", async () => {
  const h = makeHarness();
  let putBody: any = null;
  const restore = patchApi({ updateProject: async (_id: string, body: any) => { putBody = body; return {}; } });
  try {
    await h.mod.restoreProject("p9");
    assert.equal(putBody.archived, false);
    assert.deepEqual(putBody.actor, { name: "tester", type: "agent" });
    assert.equal(h.calls.loadProjects, 1);
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["Project restored"]);
    assert.equal(h.state.projectSettingsBusy, false);

    const before = h.calls.loadProjects;
    await h.mod.restoreProject("");
    assert.equal(h.calls.loadProjects, before, "empty id is a no-op");
  } finally {
    restore();
  }
});

// ------------------------------------------------------------------------ save

test("saveProjectSettings: empty name and busy are no-ops", async () => {
  const h = makeHarness({ selectedProjectId: "p1" });
  let puts = 0;
  const restore = patchApi({ updateProject: async () => { puts++; return {}; } });
  try {
    await h.mod.saveProjectSettings(); // blank name
    assert.equal(puts, 0);

    h.state.projectSettingsForm.name = "Name";
    h.state.projectSettingsBusy = true;
    await h.mod.saveProjectSettings();
    assert.equal(puts, 0, "busy blocks a double-fire");
  } finally {
    restore();
  }
});

test("saveProjectSettings: baseline-relative clearing semantics for the opencode fields", async () => {
  const h = makeHarness({ selectedProjectId: "p1", projects: [project("p1", "Test Project")] });
  let sent: any = null;
  const restore = patchApi({
    getProject: async (id: string) => ({
      id,
      name: "Test Project",
      description: "Desc",
      opencode_agent: "plan",
      opencode_variant: "high",
    }),
    updateProject: async (_id: string, req: any) => { sent = req; return {}; },
  });
  try {
    await h.mod.openProjectSettings();
    assert.equal(h.state.projectSettingsForm.opencode_agent, "plan");
    assert.equal(h.state.projectSettingsForm.opencode_variant, "high");
    assert.equal(h.mod.projectSettingsDirty(), false);

    // Change the variant → dirty; save sends the changed value + unchanged agent.
    h.state.projectSettingsForm.opencode_variant = "low";
    assert.equal(h.mod.projectSettingsDirty(), true);
    await h.mod.saveProjectSettings();
    assert.equal(sent.opencode_agent, "plan", "unchanged field re-sends its value");
    assert.equal(sent.opencode_variant, "low", "changed field sends the new value");
    assert.equal(h.state.showProjectSettings, false, "save closes the modal");
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["Project updated"]);

    // Clearing: re-opening re-bases on the fetch (agent/variant still set there);
    // empty form values then send "" — NOT undefined — to clear server-side.
    await h.mod.openProjectSettings();
    assert.equal(h.mod.projectSettingsDirty(), false, "re-open re-bases the form");
    h.state.projectSettingsForm.opencode_agent = "";
    h.state.projectSettingsForm.opencode_variant = "";
    await h.mod.saveProjectSettings();
    assert.equal(sent.opencode_agent, "");
    assert.equal(sent.opencode_variant, "");
  } finally {
    restore();
  }
});

// --------------------------------------------------------------------- members

test("canManageMembers / isOwnerMember decision matrix", () => {
  const h = makeHarness({ identity: makeIdentity("ada") });
  assert.equal(h.mod.canManageMembers(), false, "no roster yet");
  assert.equal(h.mod.isOwnerMember("ada"), false);

  h.state.membersRoster = { owner: { name: "ada", type: "human" }, members: [] };
  assert.equal(h.mod.canManageMembers(), true, "the owner manages");
  assert.equal(h.mod.isOwnerMember("ada"), true);
  assert.equal(h.mod.isOwnerMember("bo"), false);

  h.state.membersRoster = {
    owner: { name: "zoe", type: "human" },
    members: [{ username: "ada", role: "admin" }],
  };
  assert.equal(h.mod.canManageMembers(), true, "a stored admin row manages");

  h.state.membersRoster = {
    owner: { name: "zoe", type: "human" },
    members: [{ username: "ada", role: "viewer" }],
  };
  assert.equal(h.mod.canManageMembers(), false, "a viewer row does not manage");

  h.state.identity = null;
  h.state.membersRoster = { owner: { name: "ada", type: "human" }, members: [] };
  assert.equal(h.mod.canManageMembers(), false, "no identity → read-only (safe failure)");
});

test("loadMembers: no session and no token → unavailable without a request", async () => {
  const h = makeHarness({ selectedProjectId: "p1" }); // identity null, Node has no token
  let listed = 0;
  const restore = patchApi({ listMembers: async () => { listed++; return {}; } });
  try {
    await h.mod.loadMembers();
    assert.equal(listed, 0);
    assert.equal(h.state.membersState, "unavailable");
    assert.equal(h.state.membersRoster, null);
  } finally {
    restore();
  }
});

test("loadMembers: normalized roster lands while the modal is open", async () => {
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  let listed = 0;
  const restore = patchApi({
    listMembers: async (pid: string) => {
      listed++;
      assert.equal(pid, "p1");
      return { owner: { name: "ada", type: "human" }, members: [{ username: "bo", role: "ADMIN" }] };
    },
  });
  try {
    await h.mod.loadMembers();
    assert.equal(listed, 1);
    assert.equal(h.state.membersState, "ready");
    // Role lowercased by the normalizer.
    assert.deepEqual(h.state.membersRoster, {
      owner: { name: "ada", type: "human" },
      members: [{ username: "bo", role: "admin" }],
    });
  } finally {
    restore();
  }
});

test("loadMembers: a malformed body lands in unavailable; a thrown request too", async () => {
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  const restore = patchApi({ listMembers: async () => "garbage" });
  try {
    await h.mod.loadMembers();
    assert.equal(h.state.membersState, "unavailable");
    assert.equal(h.state.membersRoster, null);
  } finally {
    restore();
  }

  const h2 = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  const r2 = patchApi({ listMembers: async () => { throw new Error("503"); } });
  try {
    await h2.mod.loadMembers();
    assert.equal(h2.state.membersState, "unavailable");
    assert.equal(h2.state.membersRoster, null);
  } finally {
    r2();
  }
});

test("loadMembers: late resolution after the modal closed is dropped", async () => {
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  let resolve!: (v: unknown) => void;
  const pending = new Promise<unknown>((res) => { resolve = res; });
  const restore = patchApi({ listMembers: async () => pending });
  try {
    const inFlight = h.mod.loadMembers();
    h.state.showProjectSettings = false; // modal closed while the request was in flight
    resolve({ owner: null, members: [{ username: "bo", role: "editor" }] });
    await inFlight;
    assert.equal(h.state.membersRoster, null, "stale resolution dropped");
  } finally {
    restore();
  }
});

test("addMember: upsert toasts created-vs-role-change, clears the input, refetches", async () => {
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity() });
  let body: any = null;
  let listed = 0;
  const restore = patchApi({
    addMember: async (_pid: string, username: string, role: MemberRole) => {
      body = { username, role };
      return { username, role, created: true };
    },
    listMembers: async () => { listed++; return { owner: null, members: [] }; },
  });
  try {
    h.state.memberAddName = " bo ";
    h.state.memberAddRole = "editor";
    await h.mod.addMember();
    assert.deepEqual(body, { username: "bo", role: "editor" }, "trimmed username");
    assert.equal(h.state.memberAddName, "", "input cleared on success");
    assert.equal(listed, 1, "roster refetched");
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["bo added as editor"]);
    assert.equal(h.state.membersBusy, false);

    // created=false → role-change copy (the user picked the new role in the add form).
    h.state.memberAddName = "bo";
    h.state.memberAddRole = "admin";
    (api as any).addMember = async (_pid: string, u: string, r: MemberRole) => ({ username: u, role: r, created: false });
    await h.mod.addMember();
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["bo added as editor", "bo's role is now admin"]);

    // Error path: error toast, busy released (the input was cleared by the success above).
    (api as any).addMember = async () => { throw new Error("nope"); };
    h.state.memberAddName = "bo";
    await h.mod.addMember();
    assert.equal(h.calls.toast.at(-1)?.kind, "error");
    assert.equal(h.state.membersBusy, false);
  } finally {
    restore();
  }
});

test("addMember: blank input and busy are no-ops", async () => {
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity() });
  let adds = 0;
  const restore = patchApi({ addMember: async () => { adds++; return { username: "x", role: "viewer" }; } });
  try {
    await h.mod.addMember(); // blank name
    assert.equal(adds, 0);

    h.state.memberAddName = "bo";
    h.state.membersBusy = true;
    await h.mod.addMember();
    assert.equal(adds, 0, "busy blocks a double-fire");
  } finally {
    restore();
  }
});

test("changeMemberRole: sends the row's current role; error refetches", async () => {
  // showProjectSettings: true — mutations run from the panel of an OPEN modal,
  // and loadMembers only lands its resolution while the modal is open.
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  h.state.membersRoster = { owner: null, members: [{ username: "bo", role: "editor" }] };
  let sentRole: MemberRole | null = null;
  let listed = 0;
  const restore = patchApi({
    setMemberRole: async (_pid: string, _u: string, role: MemberRole) => { sentRole = role; return {}; },
    listMembers: async () => { listed++; return { owner: null, members: [{ username: "bo", role: "admin" }] }; },
  });
  try {
    await h.mod.changeMemberRole("bo");
    assert.equal(sentRole, "editor", "the x-model already put the new value on the row");
    assert.equal(h.state.membersRoster?.members[0].role, "admin", "refetch restored the server row");
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["bo is now editor"]);

    // Unknown username → no call.
    const before = listed;
    await h.mod.changeMemberRole("ghost");
    assert.equal(listed, before);
  } finally {
    restore();
  }
});

test("removeMember: toasts and refetches; error keeps the roster", async () => {
  // showProjectSettings: true — as in the real app, mutations run with the modal open.
  const h = makeHarness({ selectedProjectId: "p1", identity: makeIdentity(), showProjectSettings: true });
  h.state.membersRoster = { owner: null, members: [{ username: "bo", role: "viewer" }] };
  let removed: string | null = null;
  let listed = 0;
  const restore = patchApi({
    removeMember: async (_pid: string, u: string) => { removed = u; return {}; },
    listMembers: async () => { listed++; return { owner: null, members: [] }; },
  });
  try {
    await h.mod.removeMember("bo");
    assert.equal(removed, "bo");
    assert.equal(listed, 1);
    assert.deepEqual(h.calls.toast.map((t) => t.msg), ["bo removed"]);
    assert.equal(h.state.membersRoster?.members.length, 0);

    (api as any).removeMember = async () => { throw new Error("last admin"); };
    await h.mod.removeMember("bo");
    assert.equal(h.calls.toast.at(-1)?.kind, "error");
    assert.equal(h.state.membersBusy, false);
  } finally {
    restore();
  }
});
