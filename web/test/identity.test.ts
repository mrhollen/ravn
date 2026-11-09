import test from "node:test";
import assert from "node:assert/strict";

import { clearStoredSession, loadStoredSession, LS_SESSION, normalizeIdentity, storeSession } from "../src/identity.ts";
import { withGlobals } from "./test-helpers.ts";

// identity.ts is deliberately pure + defensive; these cover the normalization
// edge cases and the guarded last-known-session cache (safeStorage falls back to an
// in-memory map here, so no real window/localStorage or DOM global is required).

test("normalizeIdentity: a well-formed wire identity passes through trimmed", () => {
  const id = normalizeIdentity({
    id: "abc123",
    name: "  ada ",
    kind: "human",
    auth_method: "password",
    created_at: "2026-01-01T00:00:00Z",
  });
  assert.deepEqual(id, {
    id: "abc123",
    name: "ada",
    kind: "human",
    auth_method: "password",
    created_at: "2026-01-01T00:00:00Z",
  });
});

test("normalizeIdentity: missing/blank/wrongly-typed payloads degrade to null or defaults, never throw", () => {
  assert.equal(normalizeIdentity(null), null);
  assert.equal(normalizeIdentity(undefined), null);
  assert.equal(normalizeIdentity(42), null); // non-object
  assert.equal(normalizeIdentity("ada"), null); // string is not an object
  assert.equal(normalizeIdentity({}), null); // no name at all → not usable as an identity
  assert.equal(normalizeIdentity({ name: "   " }), null); // blank name → not usable
  assert.equal(normalizeIdentity({ id: "x", name: 123 }), null); // non-string name

  // A name with no other fields is enough; missing kind defaults to "human".
  const minimal = normalizeIdentity({ name: "runner-2" });
  assert.deepEqual(minimal, { id: "", name: "runner-2", kind: "human", auth_method: "", created_at: "" });

  // A non-human kind + a partial-but-valid identity is preserved.
  assert.equal(normalizeIdentity({ name: "bot", kind: "agent" })?.kind, "agent");
});

test("loadStoredSession / storeSession round-trip the cached identity (guarded storage)", () => {
  clearStoredSession(); // deterministic start regardless of shared in-memory state
  assert.equal(loadStoredSession(), null, "empty cache → null");

  const id = normalizeIdentity({ name: "ada", kind: "human" })!;
  storeSession(id);
  const loaded = loadStoredSession();
  assert.ok(loaded, "a stored session is read back");
  assert.deepEqual(loaded!.identity, id);
  assert.ok(typeof loaded!.stored_at === "string" && loaded!.stored_at.length > 0, "carries a stored_at timestamp");

  clearStoredSession();
  assert.equal(loadStoredSession(), null, "clear drops the cache");
});

test("loadStoredSession tolerates corrupt / blank caches via the storage seam (returns null, never throws)", async () => {
  // Drive safeStorage's window.localStorage seam with hand-written raw strings — this is the
  // only way to exercise the reader against input that storeSession would never emit.
   const makeLs = (raw: string) => ({
    getItem: (k: string) => (k === LS_SESSION ? raw : null),
    setItem: () => { /* not used */ },
    removeItem: () => { /* not used */ },
  });

  // safeStorage resolves window.localStorage; seed that seam with the raw string.
  await withGlobals({ window: { localStorage: makeLs("{ this is not json") } }, () => {
    assert.equal(loadStoredSession(), null, "corrupt JSON degrades to null");
  });
  await withGlobals({ window: { localStorage: makeLs('{"identity":{"name":"   "}}') } }, () => {
    assert.equal(loadStoredSession(), null, "a blank-name envelope degrades to null");
  });
  await withGlobals({ window: { localStorage: makeLs("null") } }, () => {
    assert.equal(loadStoredSession(), null, "JSON null literal degrades to null");
  });
});

test("loadStoredSession accepts both the full envelope and a bare identity object", async () => {
  const makeLs = (raw: string) => ({
    getItem: (k: string) => (k === LS_SESSION ? raw : null),
    setItem: () => { },
    removeItem: () => { },
  });

  // Bare identity object (no envelope, no stored_at) is still readable.
  await withGlobals(
    { window: { localStorage: makeLs(JSON.stringify({ name: "ada", kind: "agent" })) } },
    () => {
      const loaded = loadStoredSession();
      assert.equal(loaded?.identity.name, "ada");
      assert.equal(loaded?.identity.kind, "agent");
      assert.ok(typeof loaded?.stored_at === "string", "a stored_at is synthesised when absent");
    },
  );

  // The full envelope (as storeSession writes it) reads the same identity back.
  const id = normalizeIdentity({ name: "ada" })!;
  await withGlobals(
    { window: { localStorage: makeLs(JSON.stringify({ identity: id, stored_at: "2026-01-01T00:00:00Z" })) } },
    () => assert.deepEqual(loadStoredSession()?.identity, id),
  );
});

test("LS_SESSION key is stable and namespaced under ravn_", () => {
  assert.equal(LS_SESSION, "ravn_session_identity");
});
