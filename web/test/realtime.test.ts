import test from "node:test";
import assert from "node:assert/strict";

import { dispatchRealtimeEvent, formatEventDetail, KNOWN_REALTIME_KINDS, type RealtimeDispatchHooks } from "../src/realtime.ts";
import { ravnApp } from "../src/app.ts";
import { withGlobals } from "./test-helpers.ts";


function realtimeDispatchFixture(overrides: { selectedProjectId?: string | null; openDetailTaskIds?: Set<string> } = {}) {
  const calls = {
    cursor: [] as Array<{ lastEventId: string; projectId: string | null | undefined }>,
    reloads: 0,
    detailRefreshes: [] as string[],
    dashboardRefreshes: 0,
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
    refreshDashboard: () => {
      calls.dashboardRefreshes++;
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
  assert.equal(result.dashboardRefreshed, true, "dashboard refresh hook invoked");
  assert.equal(calls.dashboardRefreshes, 1);
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
    assert.equal(calls.dashboardRefreshes, 0, `body ${JSON.stringify(body)}: no dashboard refresh`);
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
  assert.equal(result.dashboardRefreshed, false);
  assert.equal(calls.reloads, 0);
  assert.equal(calls.dashboardRefreshes, 0);
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
    "artifact-removed",
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

test("RealtimeDispatch: refreshDashboard hook is optional (omitting it leaves dashboardRefreshed=false)", () => {
  const hooks: RealtimeDispatchHooks = {
    selectedProjectId: () => "proj-1",
    isDetailOpenFor: () => false,
    advanceCursor: () => {},
    scheduleReload: () => {},
    refreshDetail: () => false,
  };
  const result = dispatchRealtimeEvent({ data: realtimePayload(), lastEventId: "frame-opt" }, hooks);
  assert.equal(result.status, "dispatched");
  assert.equal(result.dashboardRefreshed, false);
});

// --- formatEventDetail (moved from app.ts as Phase 2 close-out F1) ---

test("formatEventDetail: empty/unknown input and unknown kinds render nothing", () => {
  assert.equal(formatEventDetail(null), "");
  assert.equal(formatEventDetail(undefined), "");
  assert.equal(formatEventDetail({ kind: "" }), "");
  assert.equal(formatEventDetail({ kind: "task-archived", after: "{}" }), "", "unknown kind is the safe fallback");
});

test("formatEventDetail: status-changed renders from → to, or a bare new status", () => {
  assert.equal(
    formatEventDetail({ kind: "status-changed", before: JSON.stringify({ status: "todo" }), after: JSON.stringify({ status: "doing" }) }),
    "todo → doing"
  );
  assert.equal(formatEventDetail({ kind: "status-changed", after: JSON.stringify({ status: "done" }) }), "status: done");
  assert.equal(formatEventDetail({ kind: "status-changed" }), "", "no payload at all renders nothing");
});

test("formatEventDetail: malformed before/after JSON degrades to the other side, or empty", () => {
  assert.equal(
    formatEventDetail({ kind: "status-changed", before: "{not json", after: JSON.stringify({ status: "doing" }) }),
    "status: doing",
    "a bad before is treated as absent — the new status still renders"
  );
  assert.equal(
    formatEventDetail({ kind: "updated", before: JSON.stringify({ title: "Old" }), after: "{not json" }),
    "",
    "both sides unusable → no diff segments"
  );
});

test("formatEventDetail: updated joins every changed field, priority and due-date aware", () => {
  assert.equal(
    formatEventDetail({ kind: "updated", before: JSON.stringify({ priority: 2 }), after: JSON.stringify({ priority: 4 }) }),
    "P2 → P4"
  );
  assert.equal(
    formatEventDetail({ kind: "updated", before: "{}", after: JSON.stringify({ priority: 3 }) }),
    "priority P3",
    "a first-set priority has no arrow form"
  );
  assert.equal(
    formatEventDetail({ kind: "updated", before: JSON.stringify({ title: "Old", due_at: "2026-01-01" }), after: JSON.stringify({ title: "New", due_at: null }) }),
    "title updated, due date cleared"
  );
  assert.equal(
    formatEventDetail({ kind: "updated", before: "{}", after: JSON.stringify({ description: "x", due_at: "2026-01-01" }) }),
    "due date updated"
  );
  assert.equal(formatEventDetail({ kind: "updated", before: "{}", after: "{}" }), "", "unchanged fields render nothing");
});

test("formatEventDetail: assigned covers object assignee, string assignee and owner removal", () => {
  assert.equal(
    formatEventDetail({ kind: "assigned", after: JSON.stringify({ assignee: { name: "alice" } }) }),
    "assignee: alice"
  );
  assert.equal(formatEventDetail({ kind: "assigned", after: JSON.stringify({ assignee: "bob" }) }), "assignee: bob");
  assert.equal(
    formatEventDetail({ kind: "assigned", before: JSON.stringify({ assignee: { name: "alice" } }) }),
    "owner removed"
  );
});

test("formatEventDetail: claim lifecycle kinds render their fixed copy", () => {
  assert.equal(
    formatEventDetail({ kind: "claimed", after: JSON.stringify({ holder: { name: "bot-1" } }) }),
    "claimed by bot-1"
  );
  assert.equal(formatEventDetail({ kind: "claimed" }), "claimed");
  assert.equal(formatEventDetail({ kind: "released" }), "claim released");
  assert.equal(formatEventDetail({ kind: "heartbeat" }), "lease extended");
});

test("formatEventDetail: noted and commented (with and without comment kind)", () => {
  assert.equal(formatEventDetail({ kind: "noted" }), "notes updated");
  assert.equal(formatEventDetail({ kind: "commented", after: JSON.stringify({ kind: "decision" }) }), "commented (decision)");
  assert.equal(formatEventDetail({ kind: "commented" }), "commented");
});

test("formatEventDetail: AC events carry the seq, with completion/opening semantics and reorder detection", () => {
  assert.equal(
    formatEventDetail({ kind: "ac-toggled", before: JSON.stringify({ seq: 1, done: false }), after: JSON.stringify({ seq: 1, done: true }) }),
    "AC #1 completed"
  );
  assert.equal(formatEventDetail({ kind: "ac-toggled", after: JSON.stringify({ seq: 2, done: false }) }), "AC #2 reopened");
  assert.equal(formatEventDetail({ kind: "ac-toggled" }), "", "no seq anywhere renders nothing");
  assert.equal(
    formatEventDetail({ kind: "ac-added", after: JSON.stringify({ seq: 3, text: "Add test" }) }),
    "AC #3 added"
  );
  assert.equal(formatEventDetail({ kind: "ac-added" }), "AC added");
  assert.equal(
    formatEventDetail({ kind: "ac-updated", after: JSON.stringify({ reorder: true, seq: 1 }) }),
    "ACs reordered",
    "a reorder flag wins over the per-seq copy"
  );
  assert.equal(formatEventDetail({ kind: "ac-updated", before: JSON.stringify({ seq: 4 }) }), "AC #4 updated");
  assert.equal(
    formatEventDetail({ kind: "ac-removed", before: JSON.stringify({ seq: 5, text: "x" }) }),
    "AC #5 removed",
    "removal reads the seq from BEFORE (the deleted row)"
  );
});

test("formatEventDetail: relation-changed composes action + kind + shortened target id", () => {
  assert.equal(
    formatEventDetail({ kind: "relation-changed", after: JSON.stringify({ action: "add", kind: "child", other_id: "01M5XTCCKRABBA2Y3N38V8RY63" }) }),
    "add child 01M5XTCC…"
  );
  assert.equal(
    formatEventDetail({ kind: "relation-changed", before: JSON.stringify({ action: "remove", kind: "blocked-by", other_id: "01M5XTCCKRABBA2Y3N38V8RY63" }) }),
    "remove blocked-by 01M5XTCC…",
    "a removal reads the payload from BEFORE (the deleted edge)"
  );
});

test("formatEventDetail: artifact attach/remove and created render their copy", () => {
  assert.equal(
    formatEventDetail({ kind: "artifact-added", after: JSON.stringify({ kind: "screenshot", filename: "shot.png" }) }),
    "attached screenshot (shot.png)"
  );
  assert.equal(formatEventDetail({ kind: "artifact-added", after: "{}" }), "attached artifact");
  assert.equal(
    formatEventDetail({ kind: "artifact-removed", before: JSON.stringify({ kind: "log" }) }),
    "removed log"
  );
  assert.equal(formatEventDetail({ kind: "created" }), "task created");
});
