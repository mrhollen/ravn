import test from "node:test";
import assert from "node:assert/strict";

import { createAuthModule, type AuthDeps } from "../src/modules/auth.ts";
import { api } from "../src/api.ts";
import type { DialogFocus } from "../src/focus.ts";
import { withFetch, withGlobals, withWindow } from "./test-helpers.ts";

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

/** In-memory localStorage — api.getAuthToken/setAuthToken persist only through it. */
function memStorage(): any {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => (m.has(k) ? m.get(k)! : null),
    setItem: (k: string, v: string) => {
      m.set(k, String(v));
    },
    removeItem: (k: string) => {
      m.delete(k);
    },
  };
}

/** A stable marker standing in for the DialogFocus handle returned by openDialog. */
const FOCUS = { handle: "auth-modal-focus" } as unknown as DialogFocus;

/** Flushes real macrotasks (the module's setTimeout(0) refocus path). */
async function drain(): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
}

interface Harness {
  s: Record<string, any>;
  calls: string[];
  deps: AuthDeps;
  mod: ReturnType<typeof createAuthModule>;
}

/** In-memory stand-in for the flat component root + call recorder for hooks. */
function makeHarness(over: Partial<AuthDeps> = {}): Harness {
  const s: Record<string, any> = {
    actorName: "",
    authToken: "",
    showAuthModal: false,
    tokenInput: "",
    authFocus: null,
    identity: null,
    authModalTab: "token",
    loadError: "",
    selectedProjectId: null,
    projectForbidden: true, // start "forbidden" so clears are observable
    loginUsername: "",
    loginPassword: "",
    loginBusy: false,
    loginError: "",
  };
  const calls: string[] = [];
  const deps: AuthDeps = {
    // state-in
    actorName: () => s.actorName,
    authToken: () => s.authToken,
    showAuthModal: () => s.showAuthModal,
    tokenInput: () => s.tokenInput,
    authFocus: () => s.authFocus,
    identity: () => s.identity,
    loginUsername: () => s.loginUsername,
    loginPassword: () => s.loginPassword,
    loadError: () => s.loadError,
    hasSelectedProject: () => Boolean(s.selectedProjectId),
    // state-out
    setAuthToken: (v) => {
      s.authToken = v;
    },
    setShowAuthModal: (v) => {
      s.showAuthModal = v;
    },
    setTokenInput: (v) => {
      s.tokenInput = v;
    },
    setAuthFocus: (f) => {
      s.authFocus = f;
    },
    setIdentity: (id) => {
      s.identity = id;
    },
    setAuthModalTab: (t) => {
      calls.push(`setAuthModalTab:${t}`);
      s.authModalTab = t;
    },
    setLoginBusy: (b) => {
      s.loginBusy = b;
    },
    setLoginError: (m) => {
      s.loginError = m;
    },
    setLoginPassword: (p) => {
      s.loginPassword = p;
    },
    // methods-out
    clearLoadError: () => {
      calls.push("clearLoadError");
      s.loadError = "";
    },
    clearBootError: () => {
      calls.push("clearBootError");
    },
    clearProjectForbidden: () => {
      calls.push("clearProjectForbidden");
      s.projectForbidden = false;
    },
    loadProjects: (silent) => {
      calls.push(`loadProjects:${silent ?? false}`);
      return Promise.resolve();
    },
    continueBoot: (id) => {
      calls.push(`continueBoot:${id ?? "null"}`);
      return Promise.resolve();
    },
    connectSse: () => {
      calls.push("connectSse");
    },
    disconnectSse: (rb) => {
      calls.push(`disconnectSse:${rb}`);
    },
    loadBoard: () => {
      calls.push("loadBoard");
      return Promise.resolve();
    },
    openAuthFocusSession: () => {
      calls.push("openDialog");
      return FOCUS;
    },
    closeDialog: (f) => {
      calls.push(f === FOCUS ? "closeDialog:focus" : `closeDialog:${String(f)}`);
    },
    // DOM seams
    authModalRoot: () => null,
    pageActiveElement: () => null,
  };
  Object.assign(deps, over);
  return { s, calls, deps, mod: createAuthModule(deps) };
}

// ---------------------------------------------------------------- acting identity

test("auth: actingIdentityName prefers identity, then actor name, then You", () => {
  const h = makeHarness();
  assert.equal(h.mod.actingIdentityName(), "You");
  h.s.actorName = "  Dev  ";
  assert.equal(h.mod.actingIdentityName(), "Dev");
  h.s.identity = { id: "", name: "Ada", kind: "human", auth_method: "", created_at: "" };
  assert.equal(h.mod.actingIdentityName(), "Ada");
});

test("auth: actingIdentityName falls through an empty-named identity to the actor name", () => {
  const h = makeHarness();
  h.s.actorName = "Dev";
  h.s.identity = { id: "", name: "", kind: "human", auth_method: "", created_at: "" };
  assert.equal(h.mod.actingIdentityName(), "Dev");
});

// ---------------------------------------------------------------- open / close modal

test("auth: openAuthModal fresh open seeds tokenInput and captures one focus session", () => {
  const h = makeHarness();
  h.s.authToken = "tok-1";
  h.mod.openAuthModal();
  assert.equal(h.s.showAuthModal, true);
  assert.equal(h.s.tokenInput, "tok-1");
  assert.equal(h.s.authFocus, FOCUS);
  assert.ok(h.calls.includes("openDialog"));
});

test("auth: openAuthModal(tab) switches the active panel; no tab leaves it", () => {
  const h = makeHarness();
  h.mod.openAuthModal("login");
  assert.equal(h.s.authModalTab, "login");
  assert.ok(h.calls.includes("setAuthModalTab:login"));

  const h2 = makeHarness();
  h2.mod.openAuthModal();
  assert.equal(h2.s.authModalTab, "token", "no tab argument must not touch the panel");
});

test("auth: closeAuthModal is the single close path (trap restore + clear focus)", () => {
  const h = makeHarness();
  h.s.showAuthModal = true;
  h.s.authFocus = FOCUS;
  h.mod.closeAuthModal();
  assert.equal(h.s.showAuthModal, false);
  assert.equal(h.s.authFocus, null);
  assert.ok(h.calls.includes("closeDialog:focus"));
});

class FakeInput {}

test("auth: tab-switch while open refocuses only when focus is outside the modal", async () => {
  // (a) An input INSIDE the modal already holds focus → do NOT steal it back.
  {
    const h = makeHarness();
    h.s.showAuthModal = true;
    const active = new FakeInput() as any;
    const inputEl: any = { focused: false, focus() { this.focused = true; } };
    const root: any = { contains: (el: any) => el === active, querySelector: () => inputEl };
    h.deps.authModalRoot = () => root;
    h.deps.pageActiveElement = () => active;
    await withGlobals({ HTMLInputElement: FakeInput }, async () => {
      h.mod.openAuthModal("login");
      await drain();
    });
    assert.equal(inputEl.focused, false, "must not steal focus from an already-focused modal input");
  }
  // (b) Focus sits OUTSIDE the modal → move it to the new panel's first input.
  {
    const h = makeHarness();
    h.s.showAuthModal = true;
    const body: any = {};
    const inputEl: any = { focused: false, focus() { this.focused = true; } };
    const root: any = { contains: () => false, querySelector: () => inputEl };
    h.deps.authModalRoot = () => root;
    h.deps.pageActiveElement = () => body;
    await withGlobals({ HTMLInputElement: FakeInput }, async () => {
      h.mod.openAuthModal("token");
      await drain();
    });
    assert.equal(inputEl.focused, true);
  }
  // (c) No modal root element (non-DOM environment) → no-op, must not throw.
  {
    const h = makeHarness();
    h.s.showAuthModal = true;
    await withGlobals({ HTMLInputElement: FakeInput }, async () => {
      h.mod.openAuthModal("token");
      await drain();
    });
    assert.equal(h.s.authFocus, null, "already-open path must not re-capture a focus session");
  }
});

// ---------------------------------------------------------------- sign-in (doLogin)

test("auth: doLogin validates credentials before any network call", async () => {
  const h = makeHarness();
  await h.mod.doLogin();
  assert.equal(h.s.loginError, "Enter your username and password.");
  assert.equal(h.s.loginBusy, false);
  h.s.loginUsername = "ada"; // but no password
  await h.mod.doLogin();
  assert.equal(h.s.loginError, "Enter your username and password.");
});

test("auth: doLogin success adopts token + identity and resumes boot", async () => {
  const h = makeHarness();
  h.s.loginUsername = "ada";
  h.s.loginPassword = "pw";
  h.s.showAuthModal = true;
  h.s.authFocus = FOCUS;
  const calls: Array<{ url: string }> = [];
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/tasks/01TASK" } }, () =>
      withFetch(
        captureFetch(calls, (url) =>
          url.includes("/auth/login")
            ? jsonResp(200, { token: "tok-9", expires_at: "2030-01-01T00:00:00Z", identity: { name: "Ada" } })
            : jsonResp(200, {}),
        ),
        async () => {
          await h.mod.doLogin();
          assert.deepEqual(
            calls.map((c) => c.url),
            ["/api/v1/auth/login"],
            "only the login POST may hit the network",
          );
          assert.equal(api.getAuthToken(), "tok-9");
          assert.equal(h.s.authToken, "tok-9");
          assert.equal(h.s.identity?.name, "Ada");
          assert.equal(h.s.loginPassword, "", "the password must never linger in state");
          assert.equal(h.s.showAuthModal, false);
          assert.ok(h.calls.includes("clearProjectForbidden"));
          assert.ok(h.calls.includes("loadProjects:false"));
          assert.ok(h.calls.includes("clearBootError"));
          assert.ok(h.calls.includes("continueBoot:01TASK"), "deep link must be handed to the boot continuation");
        },
      ),
    ),
  );
});

test("auth: doLogin stops the boot continuation when the project reload fails", async () => {
  const h = makeHarness();
  h.s.loginUsername = "ada";
  h.s.loginPassword = "pw";
  h.deps.loadProjects = () => {
    h.s.loadError = "cannot reach the ravn server (/api/v1) — is it running?";
    return Promise.resolve();
  };
  await withGlobals(
    { localStorage: memStorage() },
    () =>
      withFetch(
        captureFetch([], (url) =>
          url.includes("/auth/login") ? jsonResp(200, { token: "t", expires_at: "", identity: null }) : jsonResp(200, {}),
        ),
        async () => h.mod.doLogin(),
      ),
  );
  assert.ok(!h.calls.includes("clearBootError"));
  assert.ok(!h.calls.some((c) => c.startsWith("continueBoot")));
});

test("auth: doLogin maps 401 to the credential error copy", async () => {
  const h = makeHarness();
  h.s.loginUsername = "ada";
  h.s.loginPassword = "wrong";
  await withGlobals(
    { localStorage: memStorage() },
    () =>
      withFetch(
        captureFetch([], (url) =>
          url.includes("/auth/login") ? jsonResp(401, { error: "unauthorized", message: "bad credentials" }) : jsonResp(200, {}),
        ),
        async () => h.mod.doLogin(),
      ),
  );
  assert.equal(h.s.loginError, "Invalid username or password.");
  assert.equal(h.s.loginBusy, false);
});

test("auth: doLogin surfaces other failures via errMsg", async () => {
  const h = makeHarness();
  h.s.loginUsername = "ada";
  h.s.loginPassword = "pw";
  await withGlobals(
    { localStorage: memStorage() },
    () =>
      withFetch(
        captureFetch([], (url) => (url.includes("/auth/login") ? jsonResp(500, { error: "boom" }) : jsonResp(200, {}))),
        async () => h.mod.doLogin(),
      ),
  );
  assert.equal(h.s.loginError, "boom");
});

// ---------------------------------------------------------------- token save / clear

test("auth: saveAuthToken persists the token and resumes boot", async () => {
  const h = makeHarness();
  h.s.tokenInput = "tok-save";
  h.s.showAuthModal = true;
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/board" } }, async () => {
      h.mod.saveAuthToken();
      await drain();
      assert.equal(api.getAuthToken(), "tok-save");
      assert.equal(h.s.authToken, "tok-save");
      assert.equal(h.s.showAuthModal, false);
      assert.ok(h.calls.includes("clearLoadError"));
      assert.ok(h.calls.includes("loadProjects:false"));
      assert.ok(h.calls.includes("clearBootError"));
      assert.ok(h.calls.includes("continueBoot:null"), "/board is not a task deep link");
    }),
  );
});

test("auth: saveAuthToken leaves the boot-error path to a failed reload", async () => {
  const h = makeHarness();
  h.s.tokenInput = "tok-bad";
  h.deps.loadProjects = () => {
    h.s.loadError = "boom";
    return Promise.resolve();
  };
  await withGlobals({ localStorage: memStorage() }, async () => {
    h.mod.saveAuthToken();
    await drain();
  });
  assert.ok(!h.calls.includes("clearBootError"));
  assert.ok(!h.calls.some((c) => c.startsWith("continueBoot")));
});

test("auth: clearAuthToken drops the token and re-syncs stream + board", async () => {
  const h = makeHarness();
  h.s.selectedProjectId = "p-1";
  h.s.showAuthModal = true;
  h.s.authFocus = FOCUS;
  await withGlobals({ localStorage: memStorage() }, async () => {
    api.setAuthToken("tok-old");
    h.mod.clearAuthToken();
    await drain();
    assert.equal(api.getAuthToken(), "");
  });
  assert.equal(h.s.authToken, "");
  assert.equal(h.s.tokenInput, "");
  assert.ok(h.calls.includes("connectSse"), "project selected → reconnect immediately");
  assert.ok(h.calls.includes("loadBoard"), "board reloads after the project lists");
});

test("auth: clearAuthToken with no project skips stream + board", async () => {
  const h = makeHarness();
  await withGlobals({ localStorage: memStorage() }, async () => {
    h.mod.clearAuthToken();
    await drain();
  });
  assert.ok(!h.calls.includes("connectSse"));
  assert.ok(!h.calls.includes("loadBoard"));
});

// ---------------------------------------------------------------- logout

test("auth: logout revokes the session, clears state, and re-syncs lists", async () => {
  const h = makeHarness();
  h.s.selectedProjectId = "p-1";
  h.s.identity = { id: "", name: "Ada", kind: "human", auth_method: "", created_at: "" };
  const calls: Array<{ url: string }> = [];
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch(calls, (url) => (url.includes("/auth/session") ? new Response(null, { status: 204 }) : jsonResp(200, {}))),
        async () => {
          api.setAuthToken("tok-x");
          await h.mod.logout();
          await drain();
          assert.ok(calls.some((c) => c.url.includes("/auth/session")), "revoke DELETE must be attempted");
          assert.equal(api.getAuthToken(), "");
          assert.equal(h.s.authToken, "");
          assert.equal(h.s.identity, null);
          assert.equal(h.s.loginError, "");
          assert.ok(h.calls.includes("disconnectSse:true"));
          assert.ok(h.calls.includes("loadBoard"));
          assert.ok(h.calls.includes("connectSse"));
        },
      ),
    ),
  );
});

test("auth: logout still clears local state when the revoke DELETE fails", async () => {
  const h = makeHarness();
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch([], () => new Response(null, { status: 502 })),
        async () => {
          api.setAuthToken("tok-x");
          await h.mod.logout();
          await drain();
          assert.equal(api.getAuthToken(), "");
          assert.equal(h.s.identity, null);
        },
      ),
    ),
  );
});

test("auth: logout with no stored token skips the revoke call", async () => {
  const h = makeHarness();
  const calls: Array<{ url: string }> = [];
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch(calls),
        async () => {
          assert.equal(api.getAuthToken(), "", "fresh storage holds no token");
          await h.mod.logout();
          await drain();
        },
      ),
    ),
  );
  assert.ok(!calls.some((c) => c.url.includes("/auth/session")));
});

// ---------------------------------------------------------------- session identity refresh

test("auth: refreshSessionIdentity is a no-op without a token", async () => {
  const h = makeHarness();
  h.s.identity = { id: "", name: "Cached", kind: "human", auth_method: "", created_at: "" };
  const calls: Array<{ url: string }> = [];
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow(
      { location: { pathname: "/", search: "", hash: "" } },
      () => withFetch(captureFetch(calls), async () => h.mod.refreshSessionIdentity()),
    ),
  );
  assert.equal(h.s.identity?.name, "Cached");
  assert.ok(!calls.some((c) => c.url.includes("/auth/session")));
});

test("auth: refreshSessionIdentity adopts the live identity", async () => {
  const h = makeHarness();
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch([], (url) =>
          url.includes("/auth/session") ? jsonResp(200, { identity: { name: "Live" }, expires_at: "" }) : jsonResp(200, {}),
        ),
        async () => {
          api.setAuthToken("tok-x");
          await h.mod.refreshSessionIdentity();
          assert.equal(h.s.identity?.name, "Live");
        },
      ),
    ),
  );
});

test("auth: refreshSessionIdentity clears a stale identity when the bearer has no session", async () => {
  const h = makeHarness();
  h.s.identity = { id: "", name: "Stale", kind: "human", auth_method: "", created_at: "" };
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch([], (url) =>
          url.includes("/auth/session") ? jsonResp(200, { identity: null, expires_at: "" }) : jsonResp(200, {}),
        ),
        async () => {
          api.setAuthToken("tok-x");
          await h.mod.refreshSessionIdentity();
          assert.equal(h.s.identity, null);
        },
      ),
    ),
  );
});

test("auth: refreshSessionIdentity clears the cached identity on transport failure", async () => {
  const h = makeHarness();
  h.s.identity = { id: "", name: "Stale", kind: "human", auth_method: "", created_at: "" };
  await withGlobals({ localStorage: memStorage() }, () =>
    withWindow({ location: { pathname: "/", search: "", hash: "" } }, () =>
      withFetch(
        captureFetch([], () => new Response(null, { status: 502 })),
        async () => {
          api.setAuthToken("tok-x");
          await h.mod.refreshSessionIdentity();
          assert.equal(h.s.identity, null);
        },
      ),
    ),
  );
});
