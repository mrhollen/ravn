/**
 * Pure dispatch core for inbound realtime (SSE) events — extracted from
 * app.connectSse()/handleSseEvent() so the routing rules are unit-testable
 * without a browser, an EventSource, or the full Alpine component.
 *
 * Wire-format contract (see internal/interface/http/handlers_events.go): the
 * server emits EVERY event as a DEFAULT (unnamed) SSE message frame —
 * `id:` + one `data:` JSON payload, no `event:` line — with the routing field
 * inside the payload (`kind`). Per the WHATWG EventSource spec a NAMED frame
 * only fires listeners registered for that exact name (onmessage never sees
 * it), so unnamed frames are the only way for ONE handler to receive all
 * events. Consequence: adding a new event kind on the server side requires NO
 * client code change — this dispatcher treats every well-formed payload the
 * same, dispatching known kinds to the targeted refresh paths and unknown
 * kinds to the identical safe generic fallback. This module never throws on
 * unknown or malformed shapes: it returns a result descriptor instead.
 */

import type { RealtimeEventPayload } from "./types";

/** The canonical server event kinds this client generation was written
 *  against (mirrors domain.EventKind). Routing does NOT depend on this set —
 *  it only feeds the `knownKind` report field and, via app.ts, registers the
 *  legacy named-event listeners kept as defense-in-depth for older server
 *  builds that still emit `event: <kind>` frames. "message" is deliberately
 *  excluded: default frames are handled by onmessage alone (registering a
 *  named "message" listener too would double-dispatch every frame). */
export const KNOWN_REALTIME_KINDS: ReadonlySet<string> = new Set([
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
]);

/** Minimal shape of an inbound SSE delivery — MessageEvent exposes exactly
 *  these two fields that matter, and tests can feed plain objects. */
export interface RealtimeSseMessage {
  /** Raw `data:` text (null/"" when the frame carried no payload). */
  data?: string | null;
  /** The frame's SSE `id:` line, if any — feeds the replay cursor. */
  lastEventId?: string;
}

/** Component callbacks injected by app.handleSseEvent so this function stays
 *  pure (no `this`, no DOM, no fetch) and fully unit-testable. */
export interface RealtimeDispatchHooks {
  /** Project currently selected in the UI; events for another project are dropped. */
  selectedProjectId: () => string | null | undefined;
  /** True when the task-detail drawer is open for `taskId` (its id or the row it shows). */
  isDetailOpenFor: (taskId: string) => boolean;
  /** Record the frame id as the new replay cursor (no-op without a frame id). */
  advanceCursor: (lastEventId: string, projectId: string | null | undefined) => void;
  /** Coalesced silent board + projects refresh — the generic fallback every event takes. */
  scheduleReload: () => void;
  /** Targeted in-place re-fetch of the open drawer's task (returns applied). */
  refreshDetail: (taskId: string) => Promise<boolean> | boolean;
}

export type RealtimeDispatchStatus = "empty" | "malformed" | "scoped-out" | "dispatched";

/** What dispatchRealtimeEvent did — asserted directly by unit tests. */
export interface RealtimeDispatchResult {
  status: RealtimeDispatchStatus;
  /** The payload's kind, present only for well-formed payloads that passed the project-scoping check. */
  kind?: string;
  /** True when `kind` is in KNOWN_REALTIME_KINDS (reporting only — routing is identical either way). */
  knownKind: boolean;
  cursorAdvanced: boolean;
  reloadScheduled: boolean;
  detailRefreshTaskId?: string;
}

/** A realtime payload must be an object with a non-empty string kind. */
function isObjectPayload(v: unknown): v is RealtimeEventPayload {
  return typeof v === "object" && v !== null && typeof (v as RealtimeEventPayload).kind === "string" && (v as RealtimeEventPayload).kind !== "";
}

/** Dispatch one inbound realtime event. Never throws — every failure mode
 *  (empty frame, bad JSON, non-object payload, foreign project) is reported
 *  through the result instead of escaping. */
export function dispatchRealtimeEvent(
  msg: RealtimeSseMessage,
  hooks: RealtimeDispatchHooks
): RealtimeDispatchResult {
  const base = (status: RealtimeDispatchStatus): RealtimeDispatchResult => ({
    status,
    knownKind: false,
    cursorAdvanced: false,
    reloadScheduled: false,
  });

  if (!msg.data) {
    return base("empty");
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(msg.data);
  } catch {
    // Malformed payload — drop it quietly (the connection stays alive).
    return base("malformed");
  }
  if (!isObjectPayload(parsed)) {
    // Well-formed JSON but not a realtime event (bare number, array, or an
    // object without a string kind): nothing to dispatch, no pointless reload.
    return base("malformed");
  }

  const payload = parsed;

  // Project scoping check — mirrors the pre-refactor early return exactly:
  // a foreign-project payload neither advances the cursor nor refreshes.
  if (payload.project_id && payload.project_id !== hooks.selectedProjectId()) {
    return base("scoped-out");
  }

  const result: RealtimeDispatchResult = {
    status: "dispatched",
    kind: payload.kind,
    knownKind: KNOWN_REALTIME_KINDS.has(payload.kind),
    cursorAdvanced: false,
    reloadScheduled: false,
  };

  // Advance the replay cursor: the frame id is exactly what a reconnect needs
  // to ask the server for whatever was missed.
  if (msg.lastEventId) {
    hooks.advanceCursor(msg.lastEventId, hooks.selectedProjectId() ?? undefined);
    result.cursorAdvanced = true;
  }

  // Every well-scoped event — known OR unknown kind — takes the coalesced
  // silent board + projects refresh (the generic fallback that keeps the UI
  // in sync for any future server-side kind). A burst of events schedules ONE
  // round after the quiet period via the debounce inside scheduleReload.
  hooks.scheduleReload();
  result.reloadScheduled = true;

  // Targeted in-place refresh: when the event names a task and the drawer is
  // open for it, re-fetch that detail (comments/notes/claim/status) instead of
  // waiting for the coalesced round.
  if (payload.task_id && hooks.isDetailOpenFor(payload.task_id)) {
    void Promise.resolve(hooks.refreshDetail(payload.task_id));
    result.detailRefreshTaskId = payload.task_id;
  }

  return result;
}
