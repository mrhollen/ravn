import test from "node:test";
import assert from "node:assert/strict";

import { createProjectsModule, LS_PROJECT, type ProjectsDeps, type ProjectsModule } from "../src/modules/projects.ts";
import { withFetch, withGlobals } from "./test-helpers.ts";

// --- fetch capture / response helpers (mirrors auth-module.test.ts) ----------

/** Records every mocked fetch call and answers 200 application/json by default. */
function captureFetch(
  calls: Array<{ url: string; init?: any }>,
  resFactory?: (url: string, init?: any) => Response | Promise<Response>,
): (url: string | URL, init?: any) => Promise<Response> {
  return async (url: string | URL, init?: any) => {
    calls.push({ url: String(url), init });
    if (resFactory) return resFactory(String(url), init);
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
}

function jsonResp(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

const P1 = { id: "p1", name: "Alpha", task_counts: { todo: 1, doing: 2, done: 3 } };
const P2 = { id: "p2", name: "Beta", task_counts: { todo: 0, doing: 0, done: 5 } };
const A1 = { id: "a1", name: "Old", task_counts: { todo: 0, doing: 0, done: 9 } };

function listBody(projects: unknown[]): Response {
  return jsonResp(200, { projects });
}

// --- harness -----------------------------------------------------------------

interface Harness {
  s: Record<string, any>; // in-memory stand-in for the flat component root
  calls: string[]; // ordered hook log (method-out + state-out via adopt)
  deps: ProjectsDeps;
  mod: ProjectsModule;
}

/** Builds a fresh module instance over an in-memory root. */
function makeHarness(rootState?: Record<string, any>): Harness {
  const s: Record<string, any> = {
    projects: [] as any[],
    archivedProjects: [] as any[],
    loadError: "",
    selectedProjectId: "",
    myProjectsOnly: false,
    identity: null,
    newProject: { name: "", description: "" },
    projectBusy: false,
    showNewProject: false,
    ...rootState,
  };
  const calls: string[] = [];
  const deps: ProjectsDeps = {
    projects: () => s.projects,
    archivedProjects: () => s.archivedProjects,
    loadError: () => s.loadError,
    selectedProjectId: () => s.selectedProjectId,
    myProjectsOnly: () => s.myProjectsOnly,
    identity: () => s.identity,
    newProjectDraft: () => s.newProject,
    projectBusy: () => s.projectBusy,
    setProjects: (v) => {
      s.projects = v;
    },
    setArchivedProjects: (v) => {
      s.archivedProjects = v;
    },
    setLoadError: (v) => {
      s.loadError = v;
    },
    setNewProjectDraft: (d) => {
      s.newProject = d;
    },
    setProjectBusy: (b) => {
      s.projectBusy = b;
    },
    setShowNewProject: (b) => {
      s.showNewProject = b;
    },
    actor: () => ({ name: "tester", type: "human" }),
    openAuthModal: () => calls.push("openAuthModal"),
    connectSse: () => calls.push("connectSse"),
    loadBoard: async () => {
      calls.push("loadBoard");
    },
    toast: (m, k) => calls.push(`toast:${k}:${m}`),
    closeProjectMenu: () => calls.push("closeProjectMenu"),
    adoptSelectedProject: (id) => {
      s.selectedProjectId = id;
      calls.push(`adopt:${id}`);
    },
    focusCreateNameInput: () => calls.push("focusCreateNameInput"),
  };
  return { s, calls, deps, mod: createProjectsModule(deps) };
}

// --- projectOptionLabel / projectSelectLabel ---------------------------------

test("projectOptionLabel renders name with padded task counts", () => {
  const h = makeHarness();
  assert.equal(h.mod.projectOptionLabel(P1 as any), "Alpha  (1·2·3)");
});

test("projectSelectLabel shows the selected project's label", () => {
  const h = makeHarness({ projects: [P1, P2], selectedProjectId: "p2" });
  assert.equal(h.mod.projectSelectLabel(), "Beta  (0·0·5)");
});

test("projectSelectLabel falls back to the placeholder when nothing matches", () => {
  const h = makeHarness({ projects: [P1], selectedProjectId: "zzz" });
  assert.equal(h.mod.projectSelectLabel(), "Select a project…");
});

// --- loadProjects: success paths ----------------------------------------------

test("loadProjects (non-silent) applies open+archived lists and clears the error", async () => {
  const h = makeHarness({ loadError: "boom" });
  const calls: any[] = [];
  await withFetch(
    captureFetch(calls, (url) => {
      if (url.includes("archived=true")) return listBody([A1]);
      return listBody([P1, P2]);
    }),
    () => h.mod.loadProjects(),
  );
  assert.deepEqual(h.s.projects, [P1, P2]);
  assert.deepEqual(h.s.archivedProjects, [A1]);
  assert.equal(h.s.loadError, "");
  assert.ok(calls.some((c) => c.url === "/api/v1/projects"));
  assert.ok(calls.some((c) => c.url === "/api/v1/projects?archived=true"));
});

test("my-projects filter + session identity scopes the OPEN list with ?mine=created; archived stays unscoped", async () => {
  const h = makeHarness({ myProjectsOnly: true, identity: { name: "tester", type: "human" } });
  const calls: any[] = [];
  await withFetch(
    captureFetch(calls, (url) => listBody(url.includes("mine=created") ? [P1] : [])),
    () => h.mod.loadProjects(),
  );
  assert.ok(
    calls.some((c) => c.url === "/api/v1/projects?mine=created"),
    `open list must be mine-scoped, got: ${JSON.stringify(calls.map((c) => c.url))}`,
  );
  assert.ok(
    calls.some((c) => c.url === "/api/v1/projects?archived=true" && !c.url.includes("mine")),
    "archived fetch must never carry the mine param",
  );
});

test("my-projects filter WITHOUT a session identity omits ?mine (logged-out view sees all)", async () => {
  const h = makeHarness({ myProjectsOnly: true, identity: null });
  const calls: any[] = [];
  await withFetch(captureFetch(calls, (url) => listBody([])), () => h.mod.loadProjects());
  assert.ok(
    !calls.some((c) => c.url.includes("mine=")),
    `no mine param expected without identity, got: ${JSON.stringify(calls.map((c) => c.url))}`,
  );
});

test("silent load does NOT clear a pre-existing loadError", async () => {
  const h = makeHarness({ loadError: "still showing" });
  await withFetch(captureFetch([], (url) => listBody([])), () => h.mod.loadProjects(true));
  assert.equal(h.s.loadError, "still showing");
});

// --- loadProjects: degradation + failure paths ---------------------------------

test("failed archived fetch degrades gracefully: open list applies, previous archived kept, error cleared", async () => {
  const h = makeHarness({ archivedProjects: [A1] });
  await withFetch(
    captureFetch([], (url) => {
      if (url.includes("archived=true")) return jsonResp(500, { message: "archive down" });
      return listBody([P1]);
    }),
    () => h.mod.loadProjects(),
  );
  assert.deepEqual(h.s.projects, [P1]);
  assert.deepEqual(h.s.archivedProjects, [A1], "previous archived list must be preserved");
  assert.equal(h.s.loadError, "");
});

test("failed open-list fetch (non-silent) sets loadError and leaves lists untouched", async () => {
  const h = makeHarness({ projects: [P2] });
  await withFetch(
    captureFetch([], (url) => {
      if (url.includes("archived=true")) return listBody([]);
      return jsonResp(500, { message: "server exploded" });
    }),
    () => h.mod.loadProjects(),
  );
  assert.equal(h.s.loadError, "server exploded");
  assert.deepEqual(h.s.projects, [P2], "stale list must survive a failed refresh");
});

test("401 on the open-list fetch opens the auth modal (non-silent sets the error too)", async () => {
  const h = makeHarness();
  await withFetch(
    captureFetch([], (url) => {
      if (url.includes("archived=true")) return listBody([]);
      return jsonResp(401, { error: "unauthorized" });
    }),
    () => h.mod.loadProjects(),
  );
  assert.ok(h.calls.includes("openAuthModal"));
  assert.notEqual(h.s.loadError, "");
});

test("silent 401 still opens the auth modal but does not touch loadError", async () => {
  const h = makeHarness({ loadError: "" });
  await withFetch(
    captureFetch([], (url) => {
      if (url.includes("archived=true")) return listBody([]);
      return jsonResp(401, { error: "unauthorized" });
    }),
    () => h.mod.loadProjects(true),
  );
  assert.ok(h.calls.includes("openAuthModal"));
  assert.equal(h.s.loadError, "");
});

// --- loadProjects: in-flight coalescing ----------------------------------------

test("concurrent duplicate loadProjects calls join one request per endpoint (boot double-init)", async () => {
  const h = makeHarness();
  const calls: any[] = [];
  let resolveFirst: (r: Response) => void = () => {};
  const pending = new Promise<Response>((r) => {
    resolveFirst = r;
  });
  await withFetch(
    captureFetch(calls, (url) => {
      if (url === "/api/v1/projects" && calls.filter((c) => c.url === "/api/v1/projects").length === 1) {
        return pending; // hold the first open-list call in flight
      }
      return listBody(url.includes("archived=true") ? [A1] : [P1]);
    }),
    async () => {
      const a = h.mod.loadProjects(false);
      const b = h.mod.loadProjects(true); // arrives while the first is still in flight
      resolveFirst(listBody([P1]));
      await Promise.all([a, b]);
    },
  );
  assert.equal(
    calls.filter((c) => c.url === "/api/v1/projects").length,
    1,
    "the duplicate call must JOIN the in-flight request, not issue a second fetch",
  );
  assert.deepEqual(h.s.projects, [P1]);
});

// --- openNewProject -------------------------------------------------------------

test("openNewProject opens the form without resetting a typed draft", () => {
  const h = makeHarness({ newProject: { name: "typed", description: "kept" } });
  withGlobals({ window: {}, document: {} }, () => {
    h.mod.openNewProject();
  });
  assert.equal(h.s.showNewProject, true);
  assert.deepEqual(h.s.newProject, { name: "typed", description: "kept" }, "draft must survive");
  assert.ok(h.calls.includes("closeProjectMenu"));
  assert.ok(h.calls.includes("focusCreateNameInput"), "focus seam runs when window+document exist");
});

test("openNewProject skips the focus seam in non-DOM environments", () => {
  const h = makeHarness();
  h.mod.openNewProject();
  assert.equal(h.s.showNewProject, true);
  assert.ok(!h.calls.includes("focusCreateNameInput"));
});

// --- createProject: guards -------------------------------------------------------

test("createProject with a blank name is a no-op (no fetch, no busy flag)", async () => {
  const h = makeHarness({ newProject: { name: "   ", description: "" } });
  const calls: any[] = [];
  await withFetch(captureFetch(calls), () => h.mod.createProject());
  assert.equal(calls.length, 0);
  assert.equal(h.s.projectBusy, false);
});

test("createProject while busy is a no-op", async () => {
  const h = makeHarness({ projectBusy: true, newProject: { name: "X", description: "" } });
  const calls: any[] = [];
  await withFetch(captureFetch(calls), () => h.mod.createProject());
  assert.equal(calls.length, 0);
});

// --- createProject: success flow ---------------------------------------------------

test("createProject posts the trimmed draft, adopts the new project, reconnects SSE and loads the board", async () => {
  const h = makeHarness({ newProject: { name: "  New Proj  ", description: "  desc  " } });
  const calls: any[] = [];
  await withFetch(
    captureFetch(calls, (url, init) => {
      if (init?.method === "POST") return jsonResp(201, { id: "p9", name: "New Proj" });
      return listBody([]);
    }),
    () => h.mod.createProject(),
  );
  const post = calls.find((c) => c.init?.method === "POST");
  assert.ok(post && post.url === "/api/v1/projects");
  const body = JSON.parse(String(post!.init.body));
  assert.deepEqual(body, { actor: { name: "tester", type: "human" }, name: "New Proj", description: "desc" });

  assert.equal(h.s.showNewProject, false);
  assert.deepEqual(h.s.newProject, { name: "", description: "" }, "draft clears only on success");
  assert.equal(h.s.selectedProjectId, "p9", "the new project becomes THE selection");
  assert.ok(h.calls.includes("adopt:p9"));
  assert.ok(h.calls.includes("connectSse"));
  assert.ok(h.calls.includes("loadBoard"));
  assert.equal(h.s.projectBusy, false, "busy flag resets in finally");
});

test("createProject omits an empty description from the request body", async () => {
  const h = makeHarness({ newProject: { name: "No Desc", description: "   " } });
  const calls: any[] = [];
  await withFetch(
    captureFetch(calls, (url, init) => (init?.method === "POST" ? jsonResp(201, { id: "p9" }) : listBody([]))),
    () => h.mod.createProject(),
  );
  const body = JSON.parse(String(calls.find((c) => c.init?.method === "POST")!.init.body));
  assert.equal("description" in body, false);
});

// --- createProject: failure flow ----------------------------------------------------

test("createProject failure toasts the error and preserves the typed draft", async () => {
  // showNewProject: true — the form is open (the user is mid-entry) before submit fails.
  const h = makeHarness({ newProject: { name: "Kept", description: "keep me" }, showNewProject: true });
  await withFetch(
    captureFetch([], (url, init) => (init?.method === "POST" ? jsonResp(400, { message: "name too short" }) : listBody([]))),
    () => h.mod.createProject(),
  );
  assert.ok(h.calls.includes("toast:error:name too short"));
  assert.equal(h.s.showNewProject, true, "the form stays open for retry");
  assert.deepEqual(h.s.newProject, { name: "Kept", description: "keep me" }, "draft survives failure");
  assert.ok(!h.calls.includes("adopt:"), "nothing is adopted on failure");
  assert.ok(!h.calls.includes("connectSse"));
  assert.equal(h.s.projectBusy, false);
});

// --- LS_PROJECT constant ----------------------------------------------------------------

test("LS_PROJECT keeps its persisted key", () => {
  assert.equal(LS_PROJECT, "ravn.project");
});
