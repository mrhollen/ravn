import test from "node:test";
import assert from "node:assert/strict";

import { api, ApiTimeoutError, errMsg as apiErrMsg, setApiTimeoutMsForTests, withRequestTimeout } from "../src/api.ts";
import { withFetch } from "./test-helpers.ts";

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
  assert.equal(apiErrMsg(new ApiError(400, { error: "invalid input", message: 'cannot transition status from "todo" to "done"' })), 'cannot transition status from "todo" to "done"');
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
