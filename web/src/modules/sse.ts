// Realtime SSE + polling-fallback module — Phase 2 batch B slice "sse" of the
// frontend modularization epic. Owns, moved from app.ts:
//   1. EventSource lifecycle — the single-stream guarantee (see sseTelemetry),
//      manual reconnect with exponential backoff, and the membership-deny guard
//      that stops the reconnect loop for a project we may not access;
//   2. the replay cursor (server-side Last-Event-ID) — advanced per received
//      frame and sent on reconnect so the server replays what we missed while
//      disconnected; dropped on intentional teardown;
//   3. the disconnect-notification timer + one persistent error toast, shown
//      SSE_DISCONNECT_TOAST_DELAY_MS after the stream is down;
//   4. the polling fallback — a ~15s board (+ open detail + project counts)
//      poll that keeps the UI moving when SSE is not connected.
// Every body moved verbatim from app.ts (see docs/FRONTEND_ARCHITECTURE.md §2).
//
// State-ownership note (Alpine constraint): the template-bound names sseStatus
// and polling still live on the flat component root, touched here exclusively
// through the typed accessors on {@link SseDeps} (see modules/auth.ts). The
// EventSource handle, timers, backoff state and replay cursor are INTERNAL —
// never rendered, never part of the flat namespace — and are owned in this
// module's closure.

import { api } from "../api";
import { dispatchRealtimeEvent, KNOWN_REALTIME_KINDS } from "../realtime";
import type { SseConnectionStatus } from "../types";

// ---------------------------------------------------------------------------
// Moved constants (Phase 2 batch B) — the composition root re-exports
// sseTelemetry so the existing import surface (tests, main.ts) stays intact.

/** NAMED-event listener registrations kept for backward compatibility with
 *  server builds that still emit `event: <kind>` frames (pre-fix wire format).
 *  Per the WHATWG spec a named frame ONLY fires listeners registered for that
 *  exact name — onmessage never sees it — so this list must cover every kind
 *  an OLD server can emit. The PRIMARY path is es.onmessage below: current
 *  servers emit unnamed (default) frames with the kind inside the JSON
 *  payload, and dispatchRealtimeEvent() routes by payload.kind — so a brand-
 *  new server-side kind never needs to be added here or anywhere in the
 *  client. "message" is deliberately NOT listed: default frames are handled
 *  by onmessage alone, and registering a named "message" listener too would
 *  double-dispatch every frame. */
export const LEGACY_NAMED_SSE_EVENT_KINDS = [...KNOWN_REALTIME_KINDS];

/** TEST-SUPPORT telemetry for the SSE singleton guarantee (ticket
 *  01M3VJ5PQEAJ60B0QGPNZT8NKP): counts EventSource constructions and close
 *  paths so unit + browser tests can assert "exactly one open stream" without
 *  reaching into a live socket. No production behavior reads these counters —
 *  they only accumulate. Browser tests reach them via the component reference
 *  (`Alpine.$data(root)._sseTelemetry`, see `_sseTelemetry` in app.ts) instead
 *  of a window flag, so shipped bundles stay clean. */
export const sseTelemetry = {
  /** Every successful `new EventSource(...)` in connectSse(). */
  opened: 0,
  /** Every close path that actually closed a live stream (disconnectSse +
   *  handleSseDisconnect). A double-close can't skew this: the counter fires
   *  only while `sse` is non-null. */
  closed: 0,
  /** Currently-open streams = opened − closed. Steady state must be exactly 1
   *  after boot and STAYS 1 across reconnect cycles (each close+open nets to
   *  zero). */
  openCount(): number {
    return this.opened - this.closed;
  },
};

/** Polling-fallback interval (~15s) — the "no websockets" refresh cadence. */
export const POLL_MS = 15_000;
const SSE_BACKOFF_INITIAL_MS = 2000;
const SSE_BACKOFF_MAX_MS = 16_000;
/** How long we sit disconnected before surfacing the one-time toast. */
export const SSE_DISCONNECT_TOAST_DELAY_MS = 30_000;

// -------------------------------------------------------------- pure helpers
/** One step of the exponential reconnect backoff: 2s, 4s, 8s, capped at 16s. */
export function nextBackoffMs(current: number): number {
  return Math.min(current * 2, SSE_BACKOFF_MAX_MS);
}

/** Replay-cursor gate: send `last_event_id` only when we have a cursor AND it
 *  belongs to the project we are (re)connecting — the project match guards
 *  against replaying an old project's backlog after a switch. */
export function shouldSendReplayCursor(
  lastEventId: string,
  cursorProjectId: string,
  selectedProjectId: string | null | undefined
): boolean {
  return lastEventId !== "" && cursorProjectId === selectedProjectId;
}

/** Build the project events URL (token auth + optional replay cursor) — pure
 *  so the cursor/token bookkeeping is unit-testable without a live socket. */
export function buildSseUrl(opts: {
  projectId: string;
  token?: string | null;
  lastEventId?: string;
}): string {
  const params: string[] = [];
  if (opts.token) {
    params.push(`token=${encodeURIComponent(opts.token)}`);
  }
  if (opts.lastEventId) {
    params.push(`last_event_id=${encodeURIComponent(opts.lastEventId)}`);
  }
  return `/api/v1/projects/${encodeURIComponent(opts.projectId)}/events${
    params.length ? `?${params.join("&")}` : ""
  }`;
}

// ----------------------------------------------------------------------- deps
/** The subset of toast options this module passes (full shape: Toast in app.ts). */
export interface SseToastOptions {
  persistent?: boolean;
  borderClass?: string;
  hideErrorPrefix?: boolean;
  isSseDisconnect?: boolean;
}

export interface SseDeps {
  // state-in
  selectedProjectId: () => string | null | undefined;
  projectForbidden: () => boolean;
  authToken: () => string | null;
  polling: () => boolean;
  sseStatus: () => SseConnectionStatus;
  // state-out
  setSseStatus: (status: SseConnectionStatus) => void;
  setProjectForbidden: (forbidden: boolean) => void;
  // hooks — root-owned orchestration that stays outside this module
  toast: (msg: string, kind: "error" | "info", opts?: SseToastOptions) => number;
  dismissToast: (id: number) => void;
  /** Coalesced SSE-triggered board + projects reload (root owns the debounce). */
  scheduleReload: () => void;
  isDetailOpenFor: (taskId: string) => boolean;
  /** Silent drawer re-fetch for an event naming the open task; resolves false
   *  when the payload was discarded or the fetch failed. */
  refreshDetail: (taskId: string) => Promise<boolean>;
  /** Silent refresh of the open detail + comments; a no-op when the drawer is closed. */
  refreshOpenDetail: () => Promise<void>;
  loadProjectsSilent: () => Promise<void>;
  loadBoardSilent: () => Promise<void>;
}

export function createSseModule(deps: SseDeps) {
  // Internal state (flat root fields before Phase 2): never rendered, owned here.
  let sse: EventSource | null = null;
  let sseBackoffMs = SSE_BACKOFF_INITIAL_MS;
  let sseReconnectTimer: number = 0;
  let sseDisconnectedTimer: any = null;
  let sseDisconnectToastId: number | null = null;
  // Replay cursor (server-side Last-Event-ID): the last frame id received on
  // THIS connection + the project it belongs to. Reconnects send it as the
  // last_event_id query param so the server replays events missed while we
  // were disconnected; the project match guards against replaying an old
  // project's backlog after a switch.
  let sseLastEventId = "";
  let sseLastEventProjectId = "";
  let pollTimer: number = 0;

  function scheduleSseDisconnectTimer(): void {
    if (deps.sseStatus() === "connected") return;
    if (sseDisconnectedTimer || sseDisconnectToastId) return;
    sseDisconnectedTimer = setTimeout(() => {
      sseDisconnectedTimer = null;
      if (deps.sseStatus() !== "connected" && !sseDisconnectToastId) {
        showSseDisconnectNotification();
      }
    }, SSE_DISCONNECT_TOAST_DELAY_MS);
  }

  function clearSseDisconnectTimer(): void {
    if (sseDisconnectedTimer) {
      clearTimeout(sseDisconnectedTimer);
      sseDisconnectedTimer = null;
    }
  }

  function showSseDisconnectNotification(): void {
    if (sseDisconnectToastId) return;
    sseDisconnectToastId = deps.toast(
      "Disconnected from server. Realtime updates paused.",
      "error",
      {
        persistent: true,
        borderClass: "border-red-200 dark:border-red-800",
        hideErrorPrefix: true,
        isSseDisconnect: true,
      }
    );
  }

  function clearSseDisconnectNotification(): void {
    if (sseDisconnectToastId) {
      const id = sseDisconnectToastId;
      sseDisconnectToastId = null;
      deps.dismissToast(id);
    }
  }

  /** Clear the disconnect-toast guard when its toast is dismissed by hand (the
   *  root's dismissToast × path) so a LATER disconnect may surface one again. */
  function clearSseDisconnectNotificationIf(toastId: number): void {
    if (sseDisconnectToastId === toastId) {
      sseDisconnectToastId = null;
    }
  }

  function connectSse(): void {
    disconnectSse(false);

    if (typeof window === "undefined" || typeof EventSource === "undefined") {
      return;
    }

    const projectId = deps.selectedProjectId();
    if (!projectId) {
      deps.setSseStatus("disconnected");
      scheduleSseDisconnectTimer();
      return;
    }

    deps.setSseStatus("connecting");
    // A fresh connect attempt is for a possibly-accessible project; drop any prior
    // membership-deny state. loadBoard's 403 sets projectForbidden, and the guard in
    // handleSseDisconnect() stops the reconnect loop while it is active — an EventSource
    // opened against a denied project errors immediately, so that gate is what prevents
    // the infinite-reconnect loop (kept synchronous on purpose: existing SSE tests assert
    // the stream is constructed the moment connectSse() returns).
    deps.setProjectForbidden(false);

    const token = deps.authToken() || api.getAuthToken();
    // This client reconnects manually on error (see handleSseDisconnect), so
    // the browser's native Last-Event-ID auto-reconnect never fires — pass the
    // cursor ourselves and the server replays whatever we missed.
    const lastEventId = shouldSendReplayCursor(
      sseLastEventId,
      sseLastEventProjectId,
      projectId
    )
      ? sseLastEventId
      : undefined;
    const url = buildSseUrl({ projectId, token, lastEventId });

    let es: EventSource;
    try {
      es = new EventSource(url);
    } catch {
      handleSseDisconnect();
      return;
    }

    sse = es;
    sseTelemetry.opened++; // TEST-SUPPORT: count every constructed stream

    es.onopen = () => {
      if (sse === es) {
        deps.setSseStatus("connected");
        sseBackoffMs = SSE_BACKOFF_INITIAL_MS;
        clearSseDisconnectTimer();
        clearSseDisconnectNotification();
      }
    };

    const onEvent = (e: MessageEvent) => {
      if (sse !== es) return;
      handleSseEvent(e);
    };

    // PRIMARY PATH: the server emits every event as a DEFAULT (unnamed)
    // message frame with the kind inside the JSON payload, so this single
    // onmessage handler receives EVERYTHING — including kinds this client
    // was never written to know about (dispatchRealtimeEvent() routes by
    // payload.kind and falls back to the generic refresh for unknown ones).
    es.onmessage = onEvent;

    // DEFENSE-IN-DEPTH: also listen for the legacy NAMED frames an older
    // server build may still emit (`event: <kind>`), so a mixed-version
    // deploy keeps updating the UI. Both paths land in handleSseEvent.
    for (const kind of LEGACY_NAMED_SSE_EVENT_KINDS) {
      es.addEventListener(kind, onEvent);
    }

    es.onerror = () => {
      if (sse !== es) return;
      handleSseDisconnect();
    };
  }

  /** Thin adapter over the pure dispatchRealtimeEvent() core (see
   *  realtime.ts): wires component state and refresh methods in as hooks.
   *  Routing happens by PAYLOAD KIND, not by EventSource event name — known
   *  kinds and unknown/future kinds both take the coalesced silent reload,
   *  plus an in-place drawer re-fetch when the event names the open task. */
  function handleSseEvent(e: MessageEvent): void {
    try {
      dispatchRealtimeEvent(
        { data: e.data, lastEventId: e.lastEventId },
        {
          selectedProjectId: deps.selectedProjectId,
          isDetailOpenFor: deps.isDetailOpenFor,
          advanceCursor: (lastEventId, projectId) => {
            // MessageEvent.lastEventId carries the SSE frame id — exactly what
            // a reconnect needs to catch up; the project match guards against
            // replaying an old project's backlog after a switch.
            sseLastEventId = lastEventId;
            sseLastEventProjectId = projectId ?? "";
          },
          scheduleReload: () => {
            // Coalesced silent reload: a burst of events (agent batch ops)
            // within SSE_RELOAD_DEBOUNCE_MS schedules ONE board + ONE projects
            // round after the quiet period instead of N parallel ones. This is
            // also the generic fallback that keeps the UI in sync for event
            // kinds this client has never seen.
            deps.scheduleReload();
          },
          refreshDetail: (taskId) => deps.refreshDetail(taskId),
        }
      );
    } catch {
      /* defensive: dispatchRealtimeEvent never throws, but a stream handler
         must survive anything the socket can deliver */
    }
  }

  function handleSseDisconnect(): void {
    if (sse) {
      sse.close();
      sse = null;
      sseTelemetry.closed++; // TEST-SUPPORT: a live stream was actually closed
    }
    // Membership deny: a project we may not access must NOT spin an exponential-
    // backoff reconnect loop. connectSse's preflight and loadBoard set this on a 403;
    // surface the one-shot "no access" state (sseStatus disconnected) and stop retrying.
    if (deps.projectForbidden()) {
      deps.setSseStatus("disconnected");
      return;
    }

    deps.setSseStatus("connecting");
    scheduleSseDisconnectTimer();

    if (sseReconnectTimer) {
      window.clearTimeout(sseReconnectTimer);
      sseReconnectTimer = 0;
    }

    const delay = sseBackoffMs;
    // Exponential backoff: 2s, 4s, 8s, up to 16s
    sseBackoffMs = nextBackoffMs(sseBackoffMs);

    sseReconnectTimer = window.setTimeout(() => {
      sseReconnectTimer = 0;
      if (deps.selectedProjectId()) {
        connectSse();
      } else {
        deps.setSseStatus("disconnected");
        scheduleSseDisconnectTimer();
      }
    }, delay);
  }

  function disconnectSse(resetBackoff = true): void {
    if (sseReconnectTimer) {
      window.clearTimeout(sseReconnectTimer);
      sseReconnectTimer = 0;
    }
    if (sse) {
      sse.close();
      sse = null;
      sseTelemetry.closed++; // TEST-SUPPORT: a live stream was actually closed
    }
    if (resetBackoff) {
      sseBackoffMs = SSE_BACKOFF_INITIAL_MS;
      deps.setSseStatus("disconnected");
      scheduleSseDisconnectTimer();
      // Intentional teardown (project switch, logout): drop the replay cursor
      // so a later reconnect starts fresh instead of bursting a stale
      // backlog. Error-driven reconnects go through disconnectSse(false) and
      // keep the cursor on purpose.
      sseLastEventId = "";
      sseLastEventProjectId = "";
    }
  }

  // ------------------------------------------------------------------ polling
  /** Lightweight ~15s poll of board (+ open detail + project counts). No websockets. */
  function pollingChanged(): void {
    if (deps.polling()) startPolling();
    else stopPolling();
  }

  /** Visible + spoken status word for the mobile "Status & Refresh" SSE button
   *  (data-testid="mobile-sse-status"). Single source of truth so the button's
   *  x-text and its :aria-label prefix can never drift apart — WCAG 4.1.2
   *  requires the accessible name to CONTAIN the visible text, so the label is
   *  built as sseStatusWord() + ' — ' + the stateful sentence. The mobile-menu
   *  spec asserts those full sentences unanchored; the prefix preserves them. */
  function sseStatusWord(): string {
    if (deps.sseStatus() === "connected") return "Live";
    if (deps.sseStatus() === "connecting") return "Reconnecting…";
    return deps.polling() ? "Polling every 15s" : "Offline — click to connect";
  }

  function startPolling(): void {
    stopPolling();
    pollTimer = window.setInterval(() => void pollTick(), POLL_MS);
  }

  function stopPolling(): void {
    if (pollTimer) {
      window.clearInterval(pollTimer);
      pollTimer = 0;
    }
  }

  async function pollTick(): Promise<void> {
    if (document.hidden || !deps.selectedProjectId()) return;
    await deps.loadProjectsSilent();
    await deps.loadBoardSilent();
    await deps.refreshOpenDetail();
  }

  return {
    connectSse, disconnectSse, handleSseEvent, handleSseDisconnect,
    scheduleSseDisconnectTimer, clearSseDisconnectTimer, showSseDisconnectNotification,
    clearSseDisconnectNotification, clearSseDisconnectNotificationIf, pollingChanged,
    sseStatusWord, startPolling, stopPolling, pollTick,
    // TEST-SUPPORT read-only accessors (same pattern as sseTelemetry): expose
    // timing state that no product code renders or reads, for browser tests.
    backoffMs: () => sseBackoffMs,
    pollTimerId: () => pollTimer,
  };
}

export type SseModule = ReturnType<typeof createSseModule>;
