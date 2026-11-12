// Unit tests for web/src/modules/sse.ts (Phase 2 batch B slice "sse").
// Drives createSseModule() with stub deps and faked EventSource/timers — no DOM,
// no network. The root-level surface (connectSse/disconnectSse shims, telemetry)
// stays covered in test/app.test.ts; this file covers the moved internals:
// backoff math, replay-cursor gates, disconnect-toast lifecycle, and polling.

import test from "node:test";
import assert from "node:assert/strict";
import {
  buildSseUrl,
  createSseModule,
  nextBackoffMs,
  POLL_MS,
  SSE_DISCONNECT_TOAST_DELAY_MS,
  shouldSendReplayCursor,
  sseTelemetry,
} from "../src/modules/sse";

// ---------------------------------------------------------------- fake timers
type Pending = { fn: () => void; delay: number };

/** Capture window.setTimeout/interval registrations instead of scheduling them. */
function installWindowFakes() {
  const timeouts: Pending[] = [];
  const intervals: Pending[] = [];
  const clearedTimeouts = new Set<number>();
  let nextId = 1;
  const fakeWindow: Record<string, unknown> = {
    setTimeout: (fn: () => void, delay: number) => {
      const id = nextId++;
      timeouts.push({ fn: () => { if (!clearedTimeouts.has(id)) fn(); }, delay });
      return id;
    },
    clearTimeout: (id: number) => { clearedTimeouts.add(id); },
    setInterval: (fn: () => void, delay: number) => {
      const id = nextId++;
      intervals.push({ fn, delay });
      return id;
    },
    clearInterval: (id: number) => { /* captured above is enough */ void id; },
  };
  (globalThis as any).window = fakeWindow;
  return { timeouts, intervals };
}

// ------------------------------------------------------------- fake EventSource
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  closed = false;
  onopen: ((e?: unknown) => void) | null = null;
  onerror: ((e?: unknown) => void) | null = null;
  onmessage: ((e: { data: string; lastEventId: string }) => void) | null = null;
  private listeners: Record<string, Array<(e: unknown) => void>> = {};

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(kind: string, cb: (e: unknown) => void): void {
    (this.listeners[kind] ??= []).push(cb);
  }
  close(): void { this.closed = true; }

  // test drivers
  fireOpen(): void { this.onopen?.({}); }
  fireMessage(data: string, lastEventId = "frame-1"): void {
    const e = { data, lastEventId };
    this.onmessage?.(e);
    for (const cb of this.listeners["task.updated"] ?? []) cb(e); // legacy named path
  }
}

function installFakeEventSource(): FakeEventSource[] {
  FakeEventSource.instances = [];
  (globalThis as any).EventSource = FakeEventSource;
  return FakeEventSource.instances;
}

function resetTelemetry(): void {
  sseTelemetry.opened = 0;
  sseTelemetry.closed = 0;
}

// ------------------------------------------------------------- stub dep factory
type SseDepsStub = {
  selectedProjectId: () => string | null;
  projectForbidden: () => boolean;
  authToken: () => string | null;
  polling: () => boolean;
  sseStatus: () => "connected" | "connecting" | "disconnected";
  setSseStatus: (s: unknown) => void;
  setProjectForbidden: (v: boolean) => void;
  toast: (msg: string, kind: string, opts?: unknown) => number;
  dismissToast: (id: number) => void;
  scheduleReload: () => void;
  isDetailOpenFor: (taskId: string) => boolean;
  refreshDetail: (taskId: string) => Promise<boolean>;
  refreshOpenDetail: () => Promise<void>;
  loadProjectsSilent: () => Promise<void>;
  loadBoardSilent: () => Promise<void>;
};

function stubDeps(overrides: Partial<SseDepsStub> = {}): SseDepsStub & { calls: string[] } {
  const state = { status: "disconnected" as const, forbidden: false };
  const calls: string[] = [];
  let toastSeq = 100;
  const base: SseDepsStub = {
    selectedProjectId: () => "p1",
    projectForbidden: () => state.forbidden,
    authToken: () => "tok-abc",
    polling: () => true,
    sseStatus: () => state.status,
    setSseStatus: (s) => { state.status = s as never; calls.push(`setSseStatus:${s}`); },
    setProjectForbidden: (v) => { state.forbidden = v; calls.push(`setProjectForbidden:${v}`); },
    toast: (msg, kind, opts) => { calls.push(`toast:${kind}:${msg}`); void opts; return toastSeq++; },
    dismissToast: (id) => { calls.push(`dismissToast:${id}`); },
    scheduleReload: () => { calls.push("scheduleReload"); },
    isDetailOpenFor: () => false,
    refreshDetail: async () => false,
    refreshOpenDetail: async () => {},
    loadProjectsSilent: async () => { calls.push("loadProjectsSilent"); },
    loadBoardSilent: async () => { calls.push("loadBoardSilent"); },
  };
  return { ...base, ...overrides, calls } as SseDepsStub & { calls: string[] };
}

const SSE_PAYLOAD = (project: string) => JSON.stringify({ kind: "task.updated", project_id: project });

// ------------------------------------------------------------------ pure helpers
test("sse module: nextBackoffMs doubles each step and caps at 16s", () => {
  assert.equal(nextBackoffMs(2000), 4000);
  assert.equal(nextBackoffMs(4000), 8000);
  assert.equal(nextBackoffMs(8000), 16000);
  assert.equal(nextBackoffMs(16000), 16000, "cap");
  assert.equal(nextBackoffMs(32000), 16000, "never grows past the cap");
});

test("sse module: shouldSendReplayCursor gates on cursor presence AND project match", () => {
  assert.equal(shouldSendReplayCursor("7", "p1", "p1"), true);
  assert.equal(shouldSendReplayCursor("7", "p1", "p2"), false, "project switch drops the stale cursor");
  assert.equal(shouldSendReplayCursor("", "p1", "p1"), false, "no cursor yet");
  assert.equal(shouldSendReplayCursor("7", "p1", null), false);
});

test("sse module: buildSseUrl assembles token + replay cursor params", () => {
  assert.equal(buildSseUrl({ projectId: "p1" }), "/api/v1/projects/p1/events");
  assert.equal(
    buildSseUrl({ projectId: "p1", token: "t k" }),
    "/api/v1/projects/p1/events?token=t%20k",
  );
  assert.equal(
    buildSseUrl({ projectId: "p1", token: "tok", lastEventId: "42" }),
    "/api/v1/projects/p1/events?token=tok&last_event_id=42",
  );
  assert.match(buildSseUrl({ projectId: "a b" }), /\/api\/v1\/projects\/a%20b\/events/);
});

// -------------------------------------------------------- disconnect-toast lifecycle
/** Run fn with the GLOBAL (bare) setTimeout captured instead of scheduled. */
function withCapturedGlobalSetTimeout(fn: (captured: Pending[]) => void): void {
  const captured: Pending[] = [];
  const real = globalThis.setTimeout.bind(globalThis);
  (globalThis as any).setTimeout = (t: () => void, delay: number) => {
    captured.push({ fn: t, delay });
    return 1;
  };
  try {
    fn(captured);
  } finally {
    (globalThis as any).setTimeout = real;
  }
}

test("sse module: disconnect toast arms on the 30s delay and fires the persistent error toast", () => {
  const deps = stubDeps(); // sseStatus starts "disconnected"
  const m = createSseModule(deps as never);
  withCapturedGlobalSetTimeout((captured) => {
    assert.equal(captured.length, 0, "no timer before the first schedule");
    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 1);
    assert.equal(captured[0].delay, SSE_DISCONNECT_TOAST_DELAY_MS, "arms on SSE_DISCONNECT_TOAST_DELAY_MS");

    // toast appears when the delay elapses
    captured[0].fn();
    assert.deepEqual(deps.calls.filter((c) => c.startsWith("toast:")), [
      "toast:error:Disconnected from server. Realtime updates paused.",
    ]);

    // guard: a second schedule while the toast is up must not re-arm
    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 1, "no second timer while the toast is up");
  });
});

test("sse module: disconnect timer does not arm while connected", () => {
  const deps = stubDeps({ sseStatus: () => "connected" });
  const m = createSseModule(deps as never);
  withCapturedGlobalSetTimeout((captured) => {
    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 0, "connected → no timer");
  });
});

test("sse module: manual ×-dismiss of the disconnect toast re-arms via the root seam", () => {
  const deps = stubDeps();
  const m = createSseModule(deps as never);
  withCapturedGlobalSetTimeout((captured) => {
    m.scheduleSseDisconnectTimer();
    captured[0].fn(); // toast up, id 100 (first stub toast())
    assert.equal(captured.length, 1);

    // root's dismissToast() routes the × click here; matching id clears the guard…
    m.clearSseDisconnectNotificationIf(100);
    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 2, "guard cleared → arms again for a later disconnect");

    // …but a foreign id must not touch the active guard
    m.clearSseDisconnectNotificationIf(9999);
    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 2, "foreign dismiss id leaves the guard intact");
  });
});

test("sse module: programmatic clear (reconnect path) dismisses the toast and re-arms", () => {
  const deps = stubDeps();
  const m = createSseModule(deps as never);
  withCapturedGlobalSetTimeout((captured) => {
    m.scheduleSseDisconnectTimer();
    captured[0].fn(); // toast up, id 100
    deps.calls.length = 0;
    m.clearSseDisconnectNotification();
    assert.deepEqual(deps.calls, ["dismissToast:100"], "clear dismisses the live toast");

    m.scheduleSseDisconnectTimer();
    assert.equal(captured.length, 2, "cleared → arms again");
  });
});

test("sse module: clearSseDisconnectNotification is a no-op without an active toast", () => {
  const deps = stubDeps();
  const m = createSseModule(deps as never);
  m.clearSseDisconnectNotification();
  assert.equal(deps.calls.length, 0);
});

// ---------------------------------------------------------------- connect / cursor
test("sse module: connectSse opens one stream with token; reconnect sends replay cursor", () => {
  resetTelemetry();
  const deps = stubDeps();
  installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);

  m.connectSse();
  assert.equal(FakeEventSource.instances.length, 1);
  assert.match(FakeEventSource.instances[0].url, /\/api\/v1\/projects\/p1\/events\?token=tok-abc$/);
  assert.equal(sseTelemetry.openCount(), 1);

  // a frame advances the replay cursor and takes the generic reload path
  FakeEventSource.instances[0].fireMessage(SSE_PAYLOAD("p1"));
  assert.ok(deps.calls.includes("scheduleReload"), "known kind schedules the coalesced reload");

  // error-driven reconnect keeps the cursor (disconnectSse(false)) → URL carries it
  m.disconnectSse(false);
  assert.equal(sseTelemetry.openCount(), 0, "close path accounted");
  m.connectSse();
  assert.equal(FakeEventSource.instances.length, 2);
  assert.match(FakeEventSource.instances[1].url, /last_event_id=frame-1/);
  assert.equal(sseTelemetry.openCount(), 1, "exactly one open stream after a reconnect cycle");

  // intentional teardown clears the cursor → no last_event_id on the next connect
  m.disconnectSse(true);
  m.connectSse();
  assert.doesNotMatch(FakeEventSource.instances[2].url, /last_event_id/);
});

test("sse module: project switch drops the foreign-project replay cursor", () => {
  resetTelemetry();
  const deps = stubDeps({ selectedProjectId: () => "p1" });
  installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);

  m.connectSse();
  FakeEventSource.instances[0].fireMessage(SSE_PAYLOAD("p1")); // cursor now belongs to p1
  (deps as any).selectedProjectId = () => "p2";
  m.disconnectSse(false); // keep cursor across a reconnect-style transition
  m.connectSse();
  assert.doesNotMatch(FakeEventSource.instances[1].url, /last_event_id/, "p1's cursor must not replay into p2");
});

test("sse module: connectSse with no selected project stays disconnected and arms the toast timer", () => {
  resetTelemetry();
  const deps = stubDeps({ selectedProjectId: () => null });
  installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);
  m.connectSse();
  assert.equal(FakeEventSource.instances.length, 0, "no stream without a project");
  assert.equal(deps.calls[deps.calls.length - 1], "setSseStatus:disconnected");
});

// ------------------------------------------------------------------ reconnect loop
test("sse module: handleSseDisconnect backs off exponentially and reconnects while a project is selected", () => {
  resetTelemetry();
  const deps = stubDeps();
  const fakes = installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);

  m.connectSse(); // one live stream
  m.handleSseDisconnect();
  assert.equal(FakeEventSource.instances[0].closed, true, "live stream closed");
  assert.equal(sseTelemetry.openCount(), 0);
  assert.ok(deps.calls.includes("setSseStatus:connecting"), "UI shows connecting while retrying");

  // first retry at the initial 2s backoff, then 4s
  assert.equal(fakes.timeouts.length, 1);
  assert.equal(fakes.timeouts[0].delay, 2000);
  fakes.timeouts[0].fn();
  assert.equal(FakeEventSource.instances.length, 2, "reconnected");
  m.handleSseDisconnect();
  assert.equal(fakes.timeouts[1].delay, 4000, "backoff doubled");
});

test("sse module: membership deny stops the reconnect loop", () => {
  resetTelemetry();
  const deps = stubDeps({ projectForbidden: () => true });
  const fakes = installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);

  m.handleSseDisconnect();
  assert.equal(fakes.timeouts.length, 0, "no reconnect scheduled while the project is forbidden");
  assert.equal(deps.calls[deps.calls.length - 1], "setSseStatus:disconnected");
});

test("sse module: onopen resets backoff and clears the disconnect notification", () => {
  resetTelemetry();
  const deps = stubDeps();
  const fakes = installWindowFakes();
  installFakeEventSource();
  const m = createSseModule(deps as never);

  m.connectSse();
  FakeEventSource.instances[0].fireOpen();
  assert.ok(deps.calls.includes("setSseStatus:connected"));
  m.handleSseDisconnect(); // after a successful connect, backoff must restart at 2s
  assert.equal(fakes.timeouts[fakes.timeouts.length - 1].delay, 2000, "backoff reset by a successful open");
});

// ------------------------------------------------------------------ polling fallback
test("sse module: sseStatusWord reports the four user-visible states", () => {
  const deps = stubDeps();
  const m = createSseModule(deps as never);
  deps.sseStatus = () => "connected";
  assert.equal(m.sseStatusWord(), "Live");
  deps.sseStatus = () => "connecting";
  assert.equal(m.sseStatusWord(), "Reconnecting…");
  deps.sseStatus = () => "disconnected";
  deps.polling = () => true;
  assert.equal(m.sseStatusWord(), "Polling every 15s");
  deps.polling = () => false;
  assert.equal(m.sseStatusWord(), "Offline — click to connect");
});

test("sse module: pollingChanged starts/stops the interval at POLL_MS", () => {
  const deps = stubDeps({ polling: () => true });
  const fakes = installWindowFakes();
  const m = createSseModule(deps as never);
  m.pollingChanged();
  assert.equal(fakes.intervals.length, 1);
  assert.equal(fakes.intervals[0].delay, POLL_MS);
  (deps as any).polling = () => false;
  m.pollingChanged();
  // startPolling() first stops the existing interval — captured list unchanged in length
  assert.equal(fakes.intervals.length, 1, "stop clears via clearInterval, no second interval");
});

test("sse module: pollTick refreshes projects, board and open detail in order; skips when hidden", async () => {
  const deps = stubDeps();
  const fakes = installWindowFakes(); // window needed for nothing here, but keeps globals sane
  void fakes;
  (globalThis as any).document = { hidden: false };
  const m = createSseModule(deps as never);

  await m.pollTick();
  assert.deepEqual(
    deps.calls.filter((c) => c === "loadProjectsSilent" || c === "loadBoardSilent"),
    ["loadProjectsSilent", "loadBoardSilent"],
  );

  (globalThis as any).document = { hidden: true };
  deps.calls.length = 0;
  await m.pollTick();
  assert.equal(deps.calls.length, 0, "background tab: no polling work");

  (globalThis as any).document = { hidden: false };
  (deps as any).selectedProjectId = () => null;
  deps.calls.length = 0;
  await m.pollTick();
  assert.equal(deps.calls.length, 0, "no project selected: no polling work");
});
