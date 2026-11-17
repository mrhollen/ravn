/**
 * Pure cores for inbound realtime (SSE) events — the dispatch core, extracted
 * from app.connectSse()/handleSseEvent() so the routing rules are unit-testable
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
 *
 * Also hosts the pure display-string formatting of event diffs
 * (`formatEventDetail`, moved from app.ts as Phase 2 close-out F1): it maps an
 * activity-event payload (kind/before/after) to the strings rendered in the
 * Task Detail Activity list.
 */

import type { RealtimeEventPayload } from "./types";
import { shortId } from "./util";

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
  "artifact-removed",
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
  /** Silent refresh of the open project dashboard view. */
  refreshDashboard?: () => void;
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
  dashboardRefreshed: boolean;
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
    dashboardRefreshed: false,
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
    dashboardRefreshed: false,
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

  if (hooks.refreshDashboard) {
    hooks.refreshDashboard();
    result.dashboardRefreshed = true;
  }

  // Targeted in-place refresh: when the event names a task and the drawer is
  // open for it, re-fetch that detail (comments/notes/claim/status) instead of
  // waiting for the coalesced round.
  if (payload.task_id && hooks.isDetailOpenFor(payload.task_id)) {
    void Promise.resolve(hooks.refreshDetail(payload.task_id));
    result.detailRefreshTaskId = payload.task_id;
  }

  return result;
}

/** Human-readable event diff formatter for the Task Detail Activity list. */
export function formatEventDetail(e?: { kind: string; before?: string; after?: string } | null): string {
  if (!e || !e.kind) return "";
  let b: any = null;
  let a: any = null;
  if (e.before) {
    try {
      b = JSON.parse(e.before);
    } catch {}
  }
  if (e.after) {
    try {
      a = JSON.parse(e.after);
    } catch {}
  }

  switch (e.kind) {
    case "status-changed": {
      const from = b?.status;
      const to = a?.status;
      if (from && to) return `${from} → ${to}`;
      if (to) return `status: ${to}`;
      return "";
    }
    case "updated": {
      const diffs: string[] = [];
      if (b?.priority !== undefined && a?.priority !== undefined && b.priority !== a.priority) {
        diffs.push(`P${b.priority} → P${a.priority}`);
      } else if (a?.priority !== undefined && b?.priority === undefined) {
        diffs.push(`priority P${a.priority}`);
      }
      if (b?.title !== undefined && a?.title !== undefined && b.title !== a.title) {
        diffs.push("title updated");
      }
      if (b?.due_at !== undefined || a?.due_at !== undefined) {
        if (!a?.due_at) diffs.push("due date cleared");
        else diffs.push("due date updated");
      }
      if (b?.description !== undefined && a?.description !== undefined && b.description !== a.description) {
        diffs.push("description updated");
      }
      return diffs.join(", ");
    }
    case "assigned": {
      const assigneeName = a?.assignee?.name || (typeof a?.assignee === "string" ? a.assignee : "");
      if (assigneeName) return `assignee: ${assigneeName}`;
      if (b?.assignee && !a?.assignee) return "owner removed";
      return "";
    }
    case "claimed": {
      const holder = a?.holder?.name;
      if (holder) return `claimed by ${holder}`;
      return "claimed";
    }
    case "released": {
      return "claim released";
    }
    case "heartbeat": {
      return "lease extended";
    }
    case "noted": {
      return "notes updated";
    }
    case "commented": {
      const kind = a?.kind;
      return kind ? `commented (${kind})` : "commented";
    }
    case "ac-toggled": {
      const seq = a?.seq ?? b?.seq;
      const done = a?.done;
      if (seq !== undefined) {
        return done ? `AC #${seq} completed` : `AC #${seq} reopened`;
      }
      return "";
    }
    case "ac-added": {
      const seq = a?.seq;
      return seq !== undefined ? `AC #${seq} added` : "AC added";
    }
    case "ac-updated": {
      if (a?.reorder || b?.reorder) return "ACs reordered";
      const seq = a?.seq ?? b?.seq;
      return seq !== undefined ? `AC #${seq} updated` : "AC updated";
    }
    case "ac-removed": {
      const seq = b?.seq ?? a?.seq;
      return seq !== undefined ? `AC #${seq} removed` : "AC removed";
    }
    case "relation-changed": {
      const target = a || b;
      const action = target?.action || (a ? "added" : "removed");
      const kind = target?.kind ? ` ${target.kind}` : "";
      const other = target?.other_id ? ` ${shortId(target.other_id)}` : "";
      return `${action}${kind}${other}`;
    }
    case "artifact-added": {
      const kind = a?.kind || "artifact";
      const filename = a?.filename ? ` (${a.filename})` : "";
      return `attached ${kind}${filename}`;
    }
    case "artifact-removed": {
      const kind = b?.kind || a?.kind || "artifact";
      return `removed ${kind}`;
    }
    case "created": {
      return "task created";
    }
    default:
      return "";
  }
}
