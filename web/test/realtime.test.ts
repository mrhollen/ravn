import test from "node:test";
import assert from "node:assert/strict";

import { dispatchRealtimeEvent, KNOWN_REALTIME_KINDS, type RealtimeDispatchHooks } from "../src/realtime.ts";
import { ravnApp } from "../src/app.ts";
import { withGlobals } from "./test-helpers.ts";


function realtimeDispatchFixture(overrides: { selectedProjectId?: string | null; openDetailTaskIds?: Set<string> } = {}) {
  const calls = {
    cursor: [] as Array<{ lastEventId: string; projectId: string | null | undefined }>,
    reloads: 0,
    detailRefreshes: [] as string[],
  };
  const hooks: RealtimeDispatchHooks = {
    selectedProjectId: () => overrides.selectedProjectId ?? "proj-1",
    isDetailOpenFor: (id) => overrides.openDetailTaskIds?.has(id) ?? false,
    advanceCursor: (lastEventId, projectId) => {
      calls.cursor.push({ lastEventId, projectId });
    },
    scheduleReload: () => {
      calls.reloads++;
    },
    refreshDetail: (taskId) => {
      calls.detailRefreshes.push(taskId);
      return Promise.resolve(true);
    },
  };
  return { calls, hooks };
}

function realtimePayload(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    event_id: "ev-1",
    project_id: "proj-1",
    task_id: "task-9",
    kind: "status-changed",
    status: "doing",
    timestamp: "2026-10-29T00:00:00Z",
    ...overrides,
  });
}

test("RealtimeDispatch: known kind routes to the coalesced reload + in-place drawer refresh and advances the cursor", () => {
  const { calls, hooks } = realtimeDispatchFixture({ openDetailTaskIds: new Set(["task-9"]) });

  const result = dispatchRealtimeEvent({ data: realtimePayload(), lastEventId: "frame-123" }, hooks);

  assert.equal(result.status, "dispatched");
  assert.equal(result.kind, "status-changed");
  assert.equal(result.knownKind, true, "a canonical kind is reported as known");
  assert.equal(result.cursorAdvanced, true, "replay cursor advances to the frame id");
  assert.deepEqual(calls.cursor, [{ lastEventId: "frame-123", projectId: "proj-1" }]);
  assert.equal(result.reloadScheduled, true, "coalesced board + projects refresh scheduled");
  assert.equal(calls.reloads, 1);
  assert.equal(result.detailRefreshTaskId, "task-9", "open drawer re-fetched in place");
  assert.deepEqual(calls.detailRefreshes, ["task-9"]);
});

test("RealtimeDispatch: UNKNOWN (future server) kind still triggers the generic fallback refresh — no client code change required", () => {
  const { calls, hooks } = realtimeDispatchFixture();

  // A kind absent from this client's KNOWN_REALTIME_KINDS — e.g. a new
  // task-archived / project-updated event from a newer server build.
  let result: any;
  assert.doesNotThrow(() => {
    result = dispatchRealtimeEvent(
      { data: realtimePayload({ kind: "task-archived", project_id: undefined, task_id: undefined }), lastEventId: "frame-456" },
      hooks
    );
  }, "an unrecognized kind must never throw");

  assert.equal(result.status, "dispatched");
  assert.equal(result.kind, "task-archived");
  assert.equal(result.knownKind, false, "the new kind is reported as unknown…");
  assert.equal(result.reloadScheduled, true, "…yet still takes the safe generic refresh (board + projects)");
  assert.equal(calls.reloads, 1);
  assert.equal(result.cursorAdvanced, true, "and the cursor advances so a reconnect replays nothing twice");
  assert.deepEqual(calls.detailRefreshes, [], "no task_id → no drawer refresh for this event");
});

test("RealtimeDispatch: malformed / non-object payloads are dropped quietly — never throw, never refresh", () => {
  const badBodies = ["{not json", "42", "[1,2]", "{}", '{"kind":""}', "null"];
  for (const body of badBodies) {
    const { calls, hooks } = realtimeDispatchFixture();
    let result: any;
    assert.doesNotThrow(
      () => {
        result = dispatchRealtimeEvent({ data: body, lastEventId: "frame-x" }, hooks);
      },
      `body ${JSON.stringify(body)} must not throw`
    );
    assert.equal(result.status, "malformed", `body ${JSON.stringify(body)}`);
    assert.equal(calls.reloads, 0, `body ${JSON.stringify(body)}: no pointless refresh`);
    assert.equal(calls.cursor.length, 0, `body ${JSON.stringify(body)}: cursor untouched`);
  }
});

test("RealtimeDispatch: empty data is a no-op", () => {
  const { calls, hooks } = realtimeDispatchFixture();
  for (const data of [undefined, null, ""]) {
    const result = dispatchRealtimeEvent({ data, lastEventId: "frame-y" }, hooks);
    assert.equal(result.status, "empty");
  }
  assert.equal(calls.reloads, 0, "nothing scheduled for empty frames");
});

test("RealtimeDispatch: events for another project are scoped out — no cursor advance, no reload", () => {
  const { calls, hooks } = realtimeDispatchFixture({ selectedProjectId: "proj-1" });

  const result = dispatchRealtimeEvent(
    { data: realtimePayload({ project_id: "proj-OTHER" }), lastEventId: "frame-z" },
    hooks
  );

  assert.equal(result.status, "scoped-out");
  assert.equal(result.reloadScheduled, false);
  assert.equal(calls.reloads, 0);
  assert.equal(result.cursorAdvanced, false, "a foreign-project frame must not poison the replay cursor");
  assert.equal(calls.cursor.length, 0);
});

test("RealtimeDispatch: KNOWN_REALTIME_KINDS mirrors the canonical server set and excludes 'message'", () => {
  const expected = [
    "created",
    "updated",
    "status-changed",
    "claimed",
    "released",
    "heartbeat",
    "assigned",
    "noted",
    "commented",
    "ac-toggled",
    "artifact-added",
    "relation-changed",
    "ac-added",
    "ac-updated",
    "ac-removed",
  ];
  assert.equal(KNOWN_REALTIME_KINDS.size, expected.length);
  for (const k of expected) assert.ok(KNOWN_REALTIME_KINDS.has(k), `missing canonical kind ${k}`);
  assert.ok(!KNOWN_REALTIME_KINDS.has("message"), "'message' must stay out: default frames are onmessage's job");
});

test("connectSse: onmessage is the primary handler; legacy named listeners cover every canonical kind but NOT 'message' (no double dispatch)", () => {
  // Construct BEFORE swapping globals so ravnApp()'s window-conditional
  // initializers see the same bare Node environment as the other tests.
  const app = ravnApp() as any;
  const instances: any[] = [];
  class RecordingEventSource {
    url: string;
    onopen: any = null;
    onerror: any = null;
    onmessage: any = null;
    namedListeners: string[] = [];
    closed = false;
    constructor(url: string) {
      this.url = url;
      instances.push(this);
    }
    addEventListener(type: string) {
      this.namedListeners.push(type);
    }
    close() {
      this.closed = true;
    }
  }

  // Inert timers so no disconnect-notification timer outlives the test.
  const win = { setTimeout: () => 0, clearTimeout: () => {} };
  withGlobals({ window: win, EventSource: RecordingEventSource }, () => {
    app.selectedProjectId = "proj-42";
    app.connectSse();
    assert.equal(instances.length, 1, "exactly one EventSource constructed");
    const es = instances[0];
    assert.ok(String(es.url).includes("/api/v1/projects/proj-42/events"), `unexpected stream url ${es.url}`);
    assert.equal(typeof es.onmessage, "function", "PRIMARY path: default (unnamed) frames reach onmessage");
    assert.deepEqual(
      [...es.namedListeners].sort(),
      [...KNOWN_REALTIME_KINDS].sort(),
      "legacy named listeners cover exactly the canonical kinds (old-server defense-in-depth)"
    );
    assert.ok(!es.namedListeners.includes("message"), "'message' must NOT be a named listener — it would double-dispatch every frame");
  });
  if (instances[0]) instances[0].close();
});
