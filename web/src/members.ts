// Membership management (multi-user epic T9): PURE logic for the project-settings
// Members panel. No DOM, no fetch — the app wires these to api.* and Alpine state;
// unit tests exercise them directly (same spirit as identity.ts / board-scope.ts).
//
// The wire contract lives in types.ts (MemberRole / MembersRosterDTO); the REST
// verbs live in api.ts. What this module owns: defensive roster normalization,
// feature-off detection on the machine-readable 403 reason, the client-side
// manage-permission decision, and human copy for mutation failures.
import { ApiError, errMsg } from "./api";
import type { ActorRef, ApiErrorBody, MemberEntryDTO, MemberRole, MembersRosterDTO } from "./types";

/** The membership endpoints' error envelope: the standard ApiErrorBody plus the
 *  machine-readable `reason` field (e.g. "membership_scoping_disabled" on the
 *  feature-off refusal), which is not part of the base contract in types.ts. */
type MembersErrorBody = ApiErrorBody & { reason?: unknown };

/** The storable roles in ascending permission order (order drives the UI's
 *  role <select> options). Owner is deliberately absent — it is not a role. */
export const MEMBER_ROLES: readonly MemberRole[] = ["viewer", "editor", "admin"];

export function isMemberRole(v: unknown): v is MemberRole {
  return v === "viewer" || v === "editor" || v === "admin";
}

/** Defensively normalize a GET /projects/{id}/members body into the roster shape.
 *  Returns null for garbage input (the panel treats that as "unavailable", never
 *  renders it). Malformed ROWS inside a valid envelope are dropped rather than
 *  rendered blank: a row needs a non-empty string username and one of the three
 *  storable roles (case-insensitive on the wire, lowercased here). The owner may
 *  legitimately also appear in members — both are kept; the UI badges that row. */
export function normalizeRoster(raw: unknown): MembersRosterDTO | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const obj = raw as Record<string, unknown>;

  let owner: ActorRef | null = null;
  if (obj.owner && typeof obj.owner === "object" && !Array.isArray(obj.owner)) {
    const o = obj.owner as Record<string, unknown>;
    const name = typeof o.name === "string" ? o.name.trim() : "";
    if (name) owner = { type: typeof o.type === "string" ? o.type : "", name };
  }

  const members: MemberEntryDTO[] = [];
  const seen = new Set<string>();
  if (Array.isArray(obj.members)) {
    for (const entry of obj.members) {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
      const m = entry as Record<string, unknown>;
      const username = typeof m.username === "string" ? m.username.trim() : "";
      const role = typeof m.role === "string" ? (m.role.toLowerCase() as string) : "";
      if (!username || !isMemberRole(role)) continue; // malformed row → dropped
      if (seen.has(username)) continue; // defensive: the server cannot send dupes
      seen.add(username);
      members.push({ username, role });
    }
  }

  return { owner, members };
}

/** True when the error is T9's feature-off refusal: 403 with the machine-readable
 *  reason "membership_scoping_disabled" (every membership verb answers it on a
 *  deployment that booted without RAVN_SCOPING=membership). Callers switch on THIS,
 *  never on message prose. */
export function isMembershipDisabledError(e: unknown): boolean {
  return (
    e instanceof ApiError &&
    e.status === 403 &&
    (e.body as MembersErrorBody).reason === "membership_scoping_disabled"
  );
}

/** Can the acting caller manage THIS roster? Deterministic from data the UI
 *  already holds — no probe request needed:
 *   - the owner (roster.owner matches the session identity) manages, or
 *   - a stored admin row for the caller manages.
 *  Returns false when there is no session identity at all: token-mode callers
 *  (shared operator / anonymous) cannot prove owner/admin status client-side —
 *  hiding the write controls for them is the safe failure mode (the server
 *  remains authoritative and simply accepts what it allows). Editor/viewer rows
 *  are read-only. */
export function canManageRoster(
  roster: MembersRosterDTO | null | undefined,
  callerName: string | null | undefined
): boolean {
  if (!roster) return false;
  const name = (callerName ?? "").trim();
  if (!name) return false;
  if (roster.owner && roster.owner.name.trim() === name) return true;
  return roster.members.some((m) => m.username === name && m.role === "admin");
}

/** Human one-liner for a membership MUTATION failure, friendlier than the raw
 *  wire copy where that copy is API-documentation prose (endpoints, flags).
 *  Falls back to the server's message when it is already actionable (hierarchy
 *  guard, last-admin guard, disabled identity) or to errMsg() otherwise. */
export function memberMutationErrorCopy(e: unknown, targetName?: string): string {
  const who = (targetName ?? "").trim();
  if (isMembershipDisabledError(e)) {
    return "Membership management is disabled on this server.";
  }
  if (e instanceof ApiError) {
    const body = e.body; // ApiErrorBody: error + message? + resource? — reason? is handled above
    if (e.status === 404 && body.resource === "identity") {
      // Server copy reads: identity "x" does not exist — provision it first via
      // POST /api/v1/auth/accounts… That endpoint is operator-only; say the UI truth.
      return who
        ? `No account named ${who} exists on this server yet — they must create one first, then retry.`
        : "That account does not exist on this server yet — it must be created first.";
    }
    if (e.status === 404 && body.resource === "member") {
      // "Not a member" is a different outcome from "identity unknown" — and the
      // fix here is adding them, not provisioning.
      return who ? `${who} is not a member of this project.` : "That user is not a member of this project.";
    }
    if (typeof body.message === "string" && body.message) {
      return body.message; // guards already write actionable copy ("…promote another admin first…")
    }
  }
  return errMsg(e);
}
