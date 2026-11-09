import test from "node:test";
import assert from "node:assert/strict";

import { ApiError } from "../src/api.ts";
import {
  MEMBER_ROLES,
  canManageRoster,
  isMembershipDisabledError,
  isMemberRole,
  memberMutationErrorCopy,
  normalizeRoster,
} from "../src/members.ts";
import type { ApiErrorBody } from "../src/types.ts";

// members.ts is deliberately pure (no DOM, no fetch): roster normalization, the
// machine-readable feature-off detection, the client-side manage decision, and
// the human copy for mutation failures. ApiError instances are constructed
// directly — request()/fetch never run here.

function err(status: number, body: Record<string, unknown>): ApiError {
  return new ApiError(status, body as ApiErrorBody);
}

const ROSTER = {
  owner: { type: "human", name: "alice" },
  members: [
    { username: "bob", role: "editor" },
    { username: "carol", role: "admin" },
  ],
};

test("MEMBER_ROLES: exactly the three storable roles, ascending, no owner", () => {
  assert.deepEqual(MEMBER_ROLES, ["viewer", "editor", "admin"]);
});

test("isMemberRole: accepts the three storable roles and rejects everything else", () => {
  for (const r of MEMBER_ROLES) assert.equal(isMemberRole(r), true, `${r} is storable`);
  for (const bad of ["Admin", "SUPERUSER", "", "   ", 42, null, undefined, {}]) {
    assert.equal(isMemberRole(bad), false, `${String(bad)} is not storable`);
  }
});

test("normalizeRoster: a well-formed body passes through (names trimmed)", () => {
  const out = normalizeRoster({ owner: { type: "human", name: " alice " }, members: [{ username: " bob ", role: "editor" }] });
  assert.deepEqual(out, { owner: { type: "human", name: "alice" }, members: [{ username: "bob", role: "editor" }] });
});

test("normalizeRoster: the owner may also hold a stored row — both are kept, never dropped or duplicated", () => {
  const out = normalizeRoster({
    owner: { type: "human", name: "alice" },
    members: [
      { username: "alice", role: "admin" }, // owner duplication: the server allows this
      { username: "bob", role: "viewer" },
    ],
  });
  assert.deepEqual(out, {
    owner: { type: "human", name: "alice" },
    members: [
      { username: "alice", role: "admin" },
      { username: "bob", role: "viewer" },
    ],
  });
});

test("normalizeRoster: malformed rows are dropped, never rendered blank; roles normalize case", () => {
  const out = normalizeRoster({
    owner: null,
    members: [
      { username: "good", role: "ADMIN" }, // case-insensitive → "admin"
      { username: "", role: "viewer" }, // blank username → dropped
      { username: "   ", role: "viewer" }, // whitespace-only → dropped
      { username: 42, role: "viewer" }, // non-string username → dropped
      { username: "evil", role: "superuser" }, // not a storable role → dropped
      { username: "noro" }, // missing role → dropped
      null, // garbage entry → dropped
      "stray-string", // garbage entry → dropped
      { username: "good", role: "editor" }, // duplicate of first valid row → dropped (keep first)
    ],
  });
  assert.deepEqual(out, { owner: null, members: [{ username: "good", role: "admin" }] });
});

test("normalizeRoster: garbage input degrades to null (panel treats that as unavailable)", () => {
  for (const bad of [null, undefined, "", 42, ["owner"], true]) {
    assert.equal(normalizeRoster(bad), null, `${String(bad)} → null`);
  }
});

test("normalizeRoster: non-object owner / non-array members degrade to the minimum roster", () => {
  const out = normalizeRoster({ owner: "alice", members: "nope" });
  assert.deepEqual(out, { owner: null, members: [] });
  const bare = normalizeRoster({});
  assert.deepEqual(bare, { owner: null, members: [] });
});

test("isMembershipDisabledError: true only for the 403 with the machine-readable reason", () => {
  const off = err(403, { error: "forbidden", resource: "members", reason: "membership_scoping_disabled" });
  assert.equal(isMembershipDisabledError(off), true);

  // Same 403 shape, a DIFFERENT reason (or none) is not feature-off.
  assert.equal(isMembershipDisabledError(err(403, { error: "forbidden", resource: "members" })), false);
  assert.equal(isMembershipDisabledError(err(403, { error: "forbidden", resource: "members", reason: "role_denied" })), false);
  // A non-403 carrying the reason is not the feature-off refusal.
  assert.equal(isMembershipDisabledError(err(404, { error: "not_found", resource: "members", reason: "membership_scoping_disabled" })), false);
  // Non-ApiError values never match.
  assert.equal(isMembershipDisabledError(new Error("nope")), false);
  assert.equal(isMembershipDisabledError({ status: 403, body: { reason: "membership_scoping_disabled" } }), false);
});

test("canManageRoster: owner or stored admin may manage; everyone else read-only", () => {
  const roster = normalizeRoster(ROSTER)!;
  // The owner (created_by) manages even without a stored row.
  assert.equal(canManageRoster(roster, "alice"), true);
  // A stored ADMIN row grants management regardless of ownership.
  assert.equal(canManageRoster(roster, "carol"), true);
  // Editor/viewer rows never grant it; unknown callers neither.
  assert.equal(canManageRoster(roster, "bob"), false);
  assert.equal(canManageRoster(roster, "mallory"), false);
  // No caller at all (token-mode / anonymous) → no write controls.
  assert.equal(canManageRoster(roster, null), false);
  assert.equal(canManageRoster(roster, ""), false);
  assert.equal(canManageRoster(roster, "   "), false);
  // No roster (feature off / not visible) → never manageable.
  assert.equal(canManageRoster(null, "alice"), false);
});

test("canManageRoster: caller name matches after trimming; owner without a stored row still counts", () => {
  assert.equal(canManageRoster(normalizeRoster(ROSTER)!, "  alice  "), true);
  const noRow = normalizeRoster({ owner: { type: "human", name: "dave" }, members: [] })!;
  assert.equal(canManageRoster(noRow, "dave"), true);
  assert.equal(canManageRoster(noRow, "eve"), false);
});

test("memberMutationErrorCopy: feature-off gets its own copy", () => {
  const off = err(403, { error: "forbidden", resource: "members", reason: "membership_scoping_disabled", message: "membership management requires RAVN_SCOPING=membership — …" });
  assert.equal(memberMutationErrorCopy(off), "Membership management is disabled on this server.");
});

test("memberMutationErrorCopy: unknown identity → provisioning copy, with and without the target name", () => {
  const e = err(404, { error: "not_found", resource: "identity", message: 'identity "ghost" does not exist — provision it first via POST /api/v1/auth/accounts' });
  assert.match(memberMutationErrorCopy(e, "ghost"), /No account named ghost exists/);
  assert.match(memberMutationErrorCopy(e, "ghost"), /create one first/);
  // Without a target name the copy must not dangle an empty slot.
  assert.doesNotMatch(memberMutationErrorCopy(e), /named\s/);
});

test("memberMutationErrorCopy: 'not a member' (resource:member) is its own copy, distinct from unknown identity", () => {
  const e = err(404, { error: "not_found", resource: "member", message: 'identity "bob" is not a member of this project — see GET /api/v1/projects/{id}/members for who is' });
  assert.equal(memberMutationErrorCopy(e, "bob"), "bob is not a member of this project.");
});

test("memberMutationErrorCopy: actionable server copies (hierarchy, last-admin, disabled identity) pass through verbatim", () => {
  const hierarchy = err(403, { error: "forbidden", resource: "members", message: 'acting role "editor" may only assign roles at or below its own; requested "admin"' });
  assert.equal(memberMutationErrorCopy(hierarchy, "carol"), hierarchy.body.message);

  const lastAdmin = err(409, { error: "conflict", resource: "members", message: '"bob" holds the project\'s only stored-admin role and the project has no owner — promote another member to admin first, or act with the shared operator token' });
  assert.match(memberMutationErrorCopy(lastAdmin, "bob"), /promote another member to admin first/);

  const disabled = err(403, { error: "forbidden", resource: "identity", message: 'identity "ghost" is disabled and cannot be granted or demoted; removing its row via DELETE remains possible' });
  assert.match(memberMutationErrorCopy(disabled, "ghost"), /is disabled/);
});

test("memberMutationErrorCopy: unknown failures fall back to the generic errMsg copy", () => {
  assert.equal(memberMutationErrorCopy(new Error("boom")), "boom");
  const noMessage = err(500, { error: "internal" });
  assert.match(memberMutationErrorCopy(noMessage), /500|internal/);
});
