import test from "node:test";
import assert from "node:assert/strict";

import { api, ApiError, ApiTimeoutError, errMsg as apiErrMsg, setApiTimeoutMsForTests, withRequestTimeout } from "../src/api.ts";
import { withFetch, withGlobals } from "./test-helpers.ts";

/** Records every mocked fetch call and answers 200 application/json by
 *  default (a resFactory may override per URL) — NO real network I/O. */
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

/** json(status, body, opts) — a canned error/success Response with JSON ctype. */
function jsonResp(status: number, body: unknown, extra?: any): Response {
  return new Response(JSON.stringify(body), { status, ...extra, headers: { "content-type": "application/json" } });
}

/** Awaiting the reject and asserting it is an ApiError with the exact status + body. */
async function expectApiError(p: Promise<unknown>, status: number, body: object): Promise<ApiError> {
  let caught: unknown;
  try {
    await p;
  } catch (e) {
    caught = e;
  }
  assert.ok(caught instanceof ApiError, `expected an ApiError, got ${String(caught)}`);
  assert.equal((caught as ApiError).status, status);
  assert.deepEqual((caught as ApiError).body, body);
  return caught as ApiError;
}

test("API: errMsg maps our timeout + native abort DOMExceptions to the friendly message", () => {
  const wanted = "Request timed out — please check your connection or server status.";
  assert.equal(apiErrMsg(new ApiTimeoutError(20_000)), wanted);
  // Native DOMException shapes (global in Node 17+ and all browsers):
  assert.equal(apiErrMsg(new DOMException("aborted", "AbortError")), wanted);
  assert.equal(apiErrMsg(new DOMException("timeout", "TimeoutError")), wanted);
  // Anything else keeps its existing mapping:
  assert.equal(apiErrMsg(new Error("boom")), "boom");
  assert.equal(apiErrMsg("plain string"), "plain string");
});

test("API: errMsg keeps the precedence of the existing mappings (ApiError, TypeError network)", async () => {
  const { ApiError } = await import("../src/api.ts");
  // ApiError → its body message/error verbatim, exactly as before this task.
  assert.equal(apiErrMsg(new ApiError(409, { error: "version_conflict", current_version: 7 })), "version_conflict");
  assert.equal(apiErrMsg(new ApiError(400, { error: "invalid_input", message: 'cannot transition status from "todo" to "done"' })), 'cannot transition status from "todo" to "done"');
  // TypeError = fetch network failure → the "is it running?" message.
  assert.equal(apiErrMsg(new TypeError("fetch failed")), "cannot reach the ravn server (/api/v1) — is it running?");
});

test("API: withRequestTimeout — no caller signal, our timer wins after ~ms", async () => {
  const guard = withRequestTimeout(30);
  assert.equal(guard.signal.aborted, false);
  await new Promise((r) => setTimeout(r, 60)); // let the 30ms timer fire + settle
  assert.equal(guard.signal.aborted, true, "signal aborted by the timeout");
  assert.equal(guard.timedOut(), true, "attributed to OUR timer");
  guard.cleanup();
});

test("API: withRequestTimeout — caller abort fires FIRST and is not misattributed", async () => {
  const ctrl = new AbortController();
  const guard = withRequestTimeout(500, ctrl.signal); // generous timeout
  ctrl.abort(); // caller loses its own race quickly
  assert.equal(guard.signal.aborted, true, "merged signal follows the caller");
  assert.equal(guard.timedOut(), false, "caller abort is NOT reported as our timeout");
  guard.cleanup();
});

test("API: withRequestTimeout — a pre-aborted caller signal adopts immediately", () => {
  const ctrl = new AbortController();
  ctrl.abort();
  const guard = withRequestTimeout(500, ctrl.signal);
  assert.equal(guard.signal.aborted, true);
  assert.equal(guard.timedOut(), false);
  guard.cleanup();
});

test("API: withRequestTimeout — cleanup clears the timer (no late abort after cancel)", async () => {
  const guard = withRequestTimeout(25);
  guard.cleanup(); // cancel before the deadline
  await new Promise((r) => setTimeout(r, 70));
  assert.equal(guard.signal.aborted, false, "cancelled request never times out late");
});

test("API: setApiTimeoutMsForTests — override applies and ≤0 restores the default", async () => {
  const { API_TIMEOUT_MS } = await import("../src/api.ts");
  setApiTimeoutMsForTests(1);
  // (behavior of the active value is exercised by the browser hang QA; here we
  // only pin that the override accepts, resets, and never leaves zero/negative.)
  setApiTimeoutMsForTests(-5);
  assert.ok(API_TIMEOUT_MS > 0, "default constant stays sane");
  setApiTimeoutMsForTests(20_000); // restore for any later tests in this file
});

test("ArchiveMgmt: api.listProjects(true) targets /projects?archived=true; listTasks serializes archived=true", async () => {
  const urls: string[] = [];
  await withFetch(async (url: string | URL, init?: any) => {
    urls.push(String(url));
    if (String(url).includes("/projects")) {
      return new Response(JSON.stringify({ projects: [], total: 0 }), { status: 200 });
    }
    return new Response(JSON.stringify({ tasks: [], total: 0 }), { status: 200 });
  }, async () => {
    await api.listProjects(true);
    await api.listTasks({ project_id: "proj-1", archived: true, limit: 100, offset: 0 });
  });
   assert.match(urls[0], /\/projects\?archived=true$/, "includeArchived flips the query filter");
  const taskUrl = urls[1];
  assert.match(taskUrl, /archived=true/, "listTasks passes archived=true");
  assert.match(taskUrl, /project_id=proj-1/, "project scoping rides along");
});

test("API: ApiError from a 4xx JSON body carries status + exact server message", async () => {
  await withFetch(
    captureFetch([], () => jsonResp(400, { error: "invalid_input", message: 'cannot transition status from "todo" to "done"', resource: "task" })),
    async () => {
      const e = await expectApiError(api.listTasks(), 400, {
        error: "invalid_input",
        message: 'cannot transition status from "todo" to "done"',
        resource: "task",
      });
      // Constructor prefers the server's human message verbatim.
      assert.equal(e.message, 'cannot transition status from "todo" to "done"');
    },
  );
});

test("API: ApiError from a 5xx JSON body without `message` falls back to \"<status> <error>\"", async () => {
  await withFetch(
    captureFetch([], () => jsonResp(500, { error: "internal server error" })),
    async () => {
      const e = await expectApiError(api.getBoard("p-1"), 500, { error: "internal server error" });
      assert.equal(e.message, "500 internal server error");
    },
  );
});

test("API: non-JSON error bodies are tolerated (no parse attempt; statusText/status fills `error`)", async () => {
  // text/plain with an explicit statusText → the reason phrase becomes `error`.
  await withFetch(
    captureFetch([], () => new Response("unacceptable", { status: 406, statusText: "Not Acceptable", headers: { "content-type": "text/plain" } })),
    async () => {
      const e = await expectApiError(api.getProject("p-1"), 406, { error: "Not Acceptable" });
      assert.equal(e.message, "406 Not Acceptable");
    },
  );
  // No statusText (empty string) → the numeric status string is used instead.
  await withFetch(
    captureFetch([], () => new Response("bad gateway", { status: 502, headers: { "content-type": "text/plain" } })),
    async () => {
      const e = await expectApiError(api.getProject("p-2"), 502, { error: "502" });
      assert.equal(e.message, "502 502");
    },
  );
});

test("API: a JSON-declared body that fails to parse becomes an `internal` ApiError", async () => {
  await withFetch(
    captureFetch([], () => new Response("{definitely not json", { status: 503, headers: { "content-type": "application/json" } })),
    async () => {
      const e = await expectApiError(api.getProject("p-3"), 503, { error: "internal", message: "unexpected response (503)" });
      assert.equal(e.message, "unexpected response (503)");
    },
  );
});

test("API: ApiError static predicates match exactly their 409/401 shapes", () => {
  const vconf = new ApiError(409, { error: "version_conflict", current_version: 7 } as any);
  assert.equal(ApiError.isVersionConflict(vconf), true, "version_conflict with a numeric current_version");
  assert.equal(ApiError.isVersionConflict(new ApiError(409, { error: "claim_conflict" })), false, "other 409 kinds");
  assert.equal(ApiError.isVersionConflict(new ApiError(400, { error: "version_conflict", current_version: 7 } as any)), false, "non-409");
  assert.equal(ApiError.isVersionConflict(new ApiError(409, { error: "version_conflict" } as any)), false, "missing current_version");
  assert.equal(ApiError.isVersionConflict(new Error("nope")), false, "not an ApiError at all");

  assert.equal(ApiError.isClaimConflict(new ApiError(409, { error: "claim_conflict", message: "held by x" })), true);
  assert.equal(ApiError.isClaimConflict(new ApiError(500, { error: "claim_conflict" })), false);
  assert.equal(ApiError.isNotClaimed(new ApiError(409, { error: "not_claimed" })), true);
  assert.equal(ApiError.isNotClaimed(new ApiError(409, { error: "claim_conflict" })), false);
  assert.equal(ApiError.isParentConflict(new ApiError(409, { error: "parent_conflict" })), true);
  assert.equal(ApiError.isParentConflict(vconf), false);
  assert.equal(ApiError.isUnauthorized(new ApiError(401, { error: "unauthorized", message: "bad token" })), true);
  assert.equal(ApiError.isUnauthorized(new ApiError(403, { error: "forbidden" })), false);
});

test("API: request() maps a hung fetch to ApiTimeoutError when OUR deadline fires (mocked fetch)", async () => {
  setApiTimeoutMsForTests(25); // TEST-ONLY override — no real seconds
  try {
    await withFetch(
      async (_url, init?: any) => {
        // A "dead" connection: never resolves; rejects only when the merged
        // signal aborts, exactly like fetch does.
        const fail = (rej: (e: unknown) => void) => rej(new DOMException("aborted", "AbortError"));
        if (init.signal.aborted) return Promise.reject(new DOMException("aborted", "AbortError"));
        return new Promise((_ok, rej) => init.signal.addEventListener("abort", () => fail(rej), { once: true }));
      },
      async () => {
        let caught: unknown;
        try {
          await api.listProjects();
        } catch (e) {
          caught = e;
        }
        assert.ok(caught instanceof ApiTimeoutError, `expected ApiTimeoutError, got ${String(caught)}`);
        assert.equal((caught as ApiTimeoutError).timeoutMs, 25);
        // The shared mapper still yields the friendly copy for callers.
        assert.equal(apiErrMsg(caught), "Request timed out — please check your connection or server status.");
      },
    );
  } finally {
    setApiTimeoutMsForTests(0); // restore the default deadline
  }
});

test("API: a caller-initiated abort passes through untouched (never an ApiTimeoutError)", async () => {
  const ctrl = new AbortController();
  setTimeout(() => ctrl.abort(), 15); // after request() installed its listener
  await withFetch(
    async (_url, init?: any) => {
      return new Promise((_ok, rej) => {
        if (init.signal.aborted) return rej(new DOMException("aborted", "AbortError"));
        init.signal.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError")), { once: true });
      });
    },
    async () => {
      const p = api.updateTask("t-1", { title: "x" } as any, { signal: ctrl.signal });
      await assert.rejects(p, (e) => e instanceof DOMException && e.name === "AbortError" && !(e instanceof ApiTimeoutError));
    },
  );
});

test("API: network TypeError rethrows as-is; a non-Error throw is wrapped in an Error", async () => {
  await withFetch(
    async () => {
      throw new TypeError("fetch failed");
    },
    async () => {
      let caught: unknown;
      try {
        await api.getTask("t-1");
      } catch (e) {
        caught = e;
      }
      assert.ok(caught instanceof TypeError, "network failure keeps its identity for errMsg()");
      assert.equal(apiErrMsg(caught), "cannot reach the ravn server (/api/v1) — is it running?");
    },
  );
  await withFetch(
    async () => {
      throw "boom-string"; // a non-Error rejection must not escape as-is
    },
    async () => {
      await assert.rejects(api.getTask("t-2"), (e) => e instanceof Error && !(e instanceof ApiTimeoutError) && e.message === "boom-string");
    },
  );
});

test("API: success paths — JSON body passthrough, 204 void, non-JSON ok → undefined", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, (url) => {
      if (url.endsWith("/projects/p-1")) return new Response(JSON.stringify({ id: "p-1", name: "P" }), { status: 200, headers: { "content-type": "application/json" } });
      if (url.endsWith("/tasks/t-1/acceptance-criteria/2")) return new Response(null, { status: 204 });
      return new Response("hello html", { status: 200, headers: { "content-type": "text/plain" } });
    }),
    async () => {
      const proj = await api.getProject("p-1");
      assert.deepEqual(proj, { id: "p-1", name: "P" }, "JSON success parses to the payload");
      const removed = await api.deleteAc("t-1", 2, { name: "opencode", type: "agent" } as any);
      assert.equal(removed, undefined, "204 No Content resolves void");
      const plain = await api.getProject("p-2") as any;
      assert.equal(plain, undefined, "non-JSON ok response resolves undefined");
    },
  );
  assert.deepEqual(calls.map((c) => c.url), ["/api/v1/projects/p-1", "/api/v1/tasks/t-1/acceptance-criteria/2", "/api/v1/projects/p-2"]);
});

test("API: getAuthToken strips ?token= from the URL and persists it to localStorage", async () => {
  const store = new Map<string, string>();
  const ls = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
  };
  const replaced: string[] = [];
  const win: any = {
    location: { search: "?token=tok-123&x=1", pathname: "/board", hash: "#h" },
    history: { replaceState: (_s: unknown, _t: unknown, u: string) => replaced.push(u) },
  };
  await withGlobals({ window: win, localStorage: ls }, () => {
    assert.equal(api.getAuthToken(), "tok-123", "the URL token is returned");
    assert.deepEqual(replaced, ["/board?x=1#h"], "replaceState drops the token, keeps the rest");
    assert.equal(ls.getItem("ravn_token"), "tok-123", "URL token persisted for next boot");
  });
});

test("API: getAuthToken without a URL token falls back to localStorage (and tolerates broken windows)", async () => {
  const ls = { getItem: (_k: string) => "stored-9" };
  await withGlobals({ window: { location: { search: "?x=1" } }, localStorage: ls }, () => {
    assert.equal(api.getAuthToken(), "stored-9", "no ?token= → storage fallback");
  });
  // A window whose location getter throws is swallowed by the inner try/catch.
  const throwingWin: any = {};
  Object.defineProperty(throwingWin, "location", { get() { throw new Error("opaque"); } });
  await withGlobals({ window: throwingWin, localStorage: ls }, () => {
    assert.equal(api.getAuthToken(), "stored-9", "broken window still falls back to storage");
  });
  // Broken storage too → empty string, never a throw.
  const brokenLs = { getItem() { throw new Error("private mode"); } };
  await withGlobals({ window: throwingWin, localStorage: brokenLs }, () => {
    assert.equal(api.getAuthToken(), "", "broken storage degrades to no token");
  });
});

test("API: setAuthToken trims, clears on empty, and never throws from a private-mode storage", async () => {
  const ops: string[] = [];
  const ls = {
    getItem: (_k: string) => null,
    setItem: (k: string, v: string) => ops.push(`set:${k}=${v}`),
    removeItem: (k: string) => ops.push(`rm:${k}`),
  };
  await withGlobals({ localStorage: ls }, () => {
    api.setAuthToken("  tok-x  ");
    api.setAuthToken("");
  });
  assert.deepEqual(ops, ["set:ravn_token=tok-x", "rm:ravn_token"]);

  // Storage that throws (private browsing) must be absorbed silently.
  const broken = { getItem: () => null, setItem() { throw new Error("denied"); }, removeItem() { throw new Error("denied"); } };
  await withGlobals({ localStorage: broken }, () => {
    assert.doesNotThrow(() => api.setAuthToken("tok-y"));
    assert.doesNotThrow(() => api.setAuthToken(""));
  });
  // No storage global at all (bare Node) → early return, no throw.
  assert.doesNotThrow(() => api.setAuthToken("tok-z"));
});

test("API: a stored bearer token is sent as an Authorization header on requests", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withGlobals(
    {
      window: { location: { search: "" } },
      localStorage: { getItem: (_k: string) => "tok-9" },
    },
    async () => {
      await withFetch(captureFetch(calls), () => api.getBoard("p-1"));
    },
  );
  assert.equal(calls.length, 1);
  assert.equal(calls[0].init.headers.Authorization, "Bearer tok-9");
  assert.equal(calls[0].init.headers["Content-Type"], "application/json");
});

test("API: project endpoints hit the expected URL + method (mocked fetch)", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, (_url) => new Response(JSON.stringify({ id: "p-9" }), { status: 201, headers: { "content-type": "application/json" } })),
    async () => {
      const created = await api.createProject({ name: "P", actor: { name: "opencode", type: "agent" } as any });
      assert.deepEqual(created, { id: "p-9" }, "201 body parses through");
      await api.getProject("p-9");
      await api.updateProject("p-9", { description: "d" } as any);
      await api.getBoard("p-9");
      await api.listProjects();
    },
  );
  assert.deepEqual(
    calls.map((c) => [c.init?.method ?? "GET", c.url]),
    [
      ["POST", "/api/v1/projects"],
      ["GET", "/api/v1/projects/p-9"],
      ["PUT", "/api/v1/projects/p-9"],
      ["GET", "/api/v1/projects/p-9/board"],
      ["GET", "/api/v1/projects"],
    ],
  );
  assert.equal(calls[0].init.body, JSON.stringify({ name: "P", actor: { name: "opencode", type: "agent" } }), "JSON body stringified");
});

test("API: task list/detail query serialization — empty values dropped, opts clamped server-side only", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(captureFetch(calls), async () => {
    await api.listTasks(); // no params → bare path
    await api.listTasks({ project_id: "", search: null as any, limit: 0 } as any); // only the falsy-but-set limit survives
    await api.getTask("t-1"); // no opts
    await api.getTask("t-2", { commentLimit: 5, eventLimit: 7 });
  });
  assert.deepEqual(
    calls.map((c) => c.url),
    [
      "/api/v1/tasks",
      "/api/v1/tasks?limit=0",
      "/api/v1/tasks/t-1",
      "/api/v1/tasks/t-2?comment_limit=5&event_limit=7",
    ],
  );
});

test("API: task mutation endpoints — PATCH/PUT bodies and actor headers (mocked fetch)", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, (url) => {
      // The DELETE answers 204 like the real void endpoints do.
      if (url.endsWith("/acceptance-criteria/5")) return new Response(null, { status: 204 });
      return new Response(JSON.stringify({ id: "t-1", version: 2 }), { status: 200, headers: { "content-type": "application/json" } });
    }),
    async () => {
      const actor = { name: "opencode", type: "agent" };
      await api.updateTask("t-1", { title: "new", version: 1 });
      await api.putNotes("t-1", { notes: "found X", actor } as any);
      await api.toggleAc("t-1", 2, actor, true, "verified by e2e"); // note included
      await api.toggleAc("t-1", 3, actor, false); // note omitted
      await api.addAc("t-1", "new criterion", actor);
      await api.editAcText("t-1", 4, "reworded", actor);
      const removed = await api.deleteAc("t-1", 5, actor);
      assert.equal(removed, undefined, "204 void endpoint resolves undefined");
    },
  );
  assert.deepEqual(
    calls.map((c) => [c.init?.method ?? "GET", c.url]),
    [
      ["PATCH", "/api/v1/tasks/t-1"],
      ["PUT", "/api/v1/tasks/t-1/notes"],
      ["PATCH", "/api/v1/tasks/t-1/acceptance-criteria/2"],
      ["PATCH", "/api/v1/tasks/t-1/acceptance-criteria/3"],
      ["POST", "/api/v1/tasks/t-1/acceptance-criteria"],
      ["PATCH", "/api/v1/tasks/t-1/acceptance-criteria/4"],
      ["DELETE", "/api/v1/tasks/t-1/acceptance-criteria/5"],
    ],
  );
  const t2 = JSON.parse(calls[2].init.body);
  assert.deepEqual(t2, { actor: { name: "opencode", type: "agent" }, done: true, note: "verified by e2e" });
  const t3 = JSON.parse(calls[3].init.body);
  assert.equal(t3.note, undefined, "no note → field absent");
  // DELETE carries the actor in headers too.
  assert.equal(calls[6].init.headers["X-Actor-Name"], "opencode");
  assert.equal(calls[6].init.headers["X-Actor-Type"], "agent");
});

test("API: comment + artifact endpoints — query opts, summary optionality, FormData upload", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, (_url) => new Response(JSON.stringify({ id: "c-1" }), { status: 201, headers: { "content-type": "application/json" } })),
    async () => {
      const actor = { name: "opencode", type: "agent" };
      await api.addComment("t-1", { kind: "note", body: "hi", actor });
      await api.listComments("t-1");
      await api.listComments("t-1", { limit: 2, offset: 10 });
      await api.addArtifact("t-1", "screenshot", "/artifacts/a1", undefined, actor); // no summary
      const withSummary = await api.addArtifact("t-1", "log", "https://x/log.txt", "the log", actor);
      assert.deepEqual(withSummary, { id: "c-1" });

      // uploadArtifact builds a multipart FormData body — fetch must NOT get
      // a JSON Content-Type for it (let the browser set the boundary).
      const file = new File(["png-bytes"], "shot.png", { type: "image/png" });
      await api.uploadArtifact("t-1", file, "screenshot", "evidence", actor);

      await api.deleteArtifact("t-1", "art-1", actor);
    },
  );
  assert.deepEqual(
    calls.map((c) => [c.init?.method ?? "GET", c.url]),
    [
      ["POST", "/api/v1/tasks/t-1/comments"],
      ["GET", "/api/v1/tasks/t-1/comments"],
      ["GET", "/api/v1/tasks/t-1/comments?limit=2&offset=10"],
      ["POST", "/api/v1/tasks/t-1/artifacts"],
      ["POST", "/api/v1/tasks/t-1/artifacts"],
      ["POST", "/api/v1/tasks/t-1/artifacts"],
      ["DELETE", "/api/v1/tasks/t-1/artifacts/art-1"],
    ],
  );
  const deleteCall = calls[6];
  assert.equal(deleteCall.init.headers["X-Actor-Name"], "opencode");
  assert.equal(deleteCall.init.headers["X-Actor-Type"], "agent");
  const noSummary = JSON.parse(calls[3].init.body);
  assert.equal(noSummary.summary, undefined);
  const fdCall = calls[5];
  assert.ok(fdCall.init.body instanceof FormData, "upload uses a real FormData body");
  assert.equal(Object.prototype.hasOwnProperty.call(fdCall.init.headers, "Content-Type"), false, "no JSON Content-Type on multipart");
  assert.equal(fdCall.init.body.get("kind"), "screenshot");
  assert.equal(fdCall.init.body.get("actor_name"), "opencode");
  assert.equal(fdCall.init.body.get("summary"), "evidence");
  const uploadedFile = fdCall.init.body.get("file") as any;
  assert.equal(uploadedFile?.name, "shot.png");
});

test("API: opencode + collaboration endpoints — trimmed qs, ttl/note optionality, relation verbs", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(captureFetch(calls), async () => {
    const actor = { name: "opencode", type: "agent" };
    await api.getOpenCodeOptions("p-1"); // no params → bare path
    await api.getOpenCodeOptions("p-2", { server_url: "  http://x  ", directory: "" } as any); // trim + drop empty
    await api.startOpenCode("t-1"); // input omitted → {} payload
    assert.deepEqual(JSON.parse(calls[2].init.body), {}, "no input defaults to an empty object");
    await api.claimTask("t-1", actor, 120);
    await api.claimTask("t-1", actor); // no ttl
    await api.releaseTask("t-1", actor, "stalled on DB");
    await api.heartbeatTask("t-1", actor);
    await api.addRelation("t-1", "t-2", "blocked-by", actor);
    await api.removeRelation("t-1", "t-2", "blocked-by", actor);
  });
  assert.deepEqual(
    calls.map((c) => [c.init?.method ?? "GET", c.url]),
    [
      ["GET", "/api/v1/projects/p-1/opencode/options"],
      ["GET", "/api/v1/projects/p-2/opencode/options?server_url=http%3A%2F%2Fx"],
      ["POST", "/api/v1/tasks/t-1/opencode/start"],
      ["POST", "/api/v1/tasks/t-1/claim"],
      ["POST", "/api/v1/tasks/t-1/claim"],
      ["POST", "/api/v1/tasks/t-1/release"],
      ["POST", "/api/v1/tasks/t-1/heartbeat"],
      ["POST", "/api/v1/tasks/t-1/relations"],
      ["DELETE", "/api/v1/tasks/t-1/relations/t-2/blocked-by"],
    ],
  );
  assert.equal(JSON.parse(calls[3].init.body).ttl_sec, 120, "ttl present when > 0");
  assert.equal(JSON.parse(calls[4].init.body).ttl_sec, undefined, "ttl omitted when absent");
  assert.equal(JSON.parse(calls[5].init.body).note, "stalled on DB");
  assert.equal(calls[8].init.headers["X-Actor-Name"], "opencode", "removeRelation carries the actor header");
});

// --- multi-user identity (epic T3/T7): auth endpoints + listProjects mine scoping ---

test("API: api.listProjects preserves the exact legacy URL for every mine/arg combination", async () => {
  const urls: string[] = [];
  const capture = async (url: string | URL, init?: any) => {
    urls.push(String(url));
    return new Response(JSON.stringify({ projects: [], total: 0 }), { status: 200 });
  };
  await withFetch(capture, async () => {
    await api.listProjects(); // default — MUST stay query-less (existing boot relies on this)
    await api.listProjects(true); // archived only
    await api.listProjects(false, "created"); // mine only (no archived → no archived param)
    await api.listProjects(true, "created"); // both
  });
  assert.deepEqual(urls, [
    "/api/v1/projects",
    "/api/v1/projects?archived=true",
    "/api/v1/projects?mine=created",
    "/api/v1/projects?archived=true&mine=created",
  ]);
});

test("API: api.login POSTs trimmed username+password, sends the Bearer, and parses LoginResponse", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withGlobals(
    { window: { location: { search: "" } }, localStorage: { getItem: (_k: string) => "sess-1" } },
    async () => {
      const resFactory = (_url: string, _init?: any) =>
        new Response(
          JSON.stringify({ token: "tok-new", expires_at: "2026-12-31T00:00:00Z", identity: { id: "u1", name: "ada", kind: "human", auth_method: "password", created_at: "2026-01-01T00:00:00Z" } }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      const res = await withFetch(captureFetch(calls, resFactory), () => api.login("  ada ", "hunter2"));
      assert.equal(calls.length, 1);
      assert.equal(calls[0].url, "/api/v1/auth/login");
      assert.equal(calls[0].init.method, "POST");
      assert.deepEqual(JSON.parse(calls[0].init.body), { username: "ada", password: "hunter2" }, "username trimmed, password verbatim");
      assert.equal(calls[0].init.headers.Authorization, "Bearer sess-1", "existing bearer rides along");
      assert.equal(res.token, "tok-new");
      assert.equal(res.identity.name, "ada");
    },
  );
});

test("API: api.login surfaces a 401 as an unauthorized ApiError (invalid credentials)", async () => {
  await withFetch(captureFetch([], () => jsonResp(401, { error: "unauthorized", message: "invalid username or password" })), async () => {
    const e = await expectApiError(api.login("ada", "nope"), 401, { error: "unauthorized", message: "invalid username or password" });
    assert.equal(ApiError.isUnauthorized(e), true);
    assert.equal(ApiError.isForbidden(e), false);
  });
});

test("API: api.getSession hits GET /auth/session; a dead/absent session is an unauthorized ApiError", async () => {
  await withFetch(captureFetch([], () => jsonResp(401, { error: "unauthorized", message: "invalid bearer token" })), async () => {
    const e = await expectApiError(api.getSession(), 401, { error: "unauthorized", message: "invalid bearer token" });
    assert.equal(ApiError.isUnauthorized(e), true);
    assert.equal(ApiError.isForbidden(e), false, "401 is NOT a membership deny");
  });
});

test("API: api.revokeSession DELETEs /auth/session and resolves void on success", async () => {
  const calls: Array<{ url: string; init?: any }> = [];
  await withFetch(
    captureFetch(calls, (_url) => new Response(null, { status: 204 })),
    async () => {
      const out = await api.revokeSession();
      assert.equal(out, undefined, "void body resolves to undefined");
      assert.equal(calls.length, 1);
      assert.equal(calls[0].url, "/api/v1/auth/session");
      assert.equal(calls[0].init.method, "DELETE");
    },
  );
});

test("API: ApiError.isForbidden distinguishes 403 (membership deny) from 401 (unauthenticated)", async () => {
  const forbody = { error: "forbidden", message: "no access to this project" };
  await withFetch(captureFetch([], () => jsonResp(403, forbody)), async () => {
    const e = await expectApiError(api.getProject("p-deny"), 403, forbody);
    assert.equal(ApiError.isForbidden(e), true, "403 is a membership deny");
    assert.equal(ApiError.isUnauthorized(e), false, "403 is NOT unauthenticated");
  });
});
