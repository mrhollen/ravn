// Pure helpers for multi-user session identity (epic T7): normalizing a raw wire
// identity into a well-formed SessionIdentity and caching the last-known one so the
// header can show it before the authoritative async /auth/session probe resolves.
// No component state — safe to import from unit tests and app.ts alike.

import { safeStorage } from "./util";
import type { SessionIdentity } from "./types";

/** localStorage key for the last-known session identity cache (separate from the
 *  bearer token, which lives in LS_TOKEN). The LIVE /auth/session probe is always
 *  authoritative and may replace or clear this on boot. */
export const LS_SESSION = "ravn_session_identity";

/** Normalize a raw wire identity (LoginResponse.identity / SessionResponse.identity)
 *  into a well-formed SessionIdentity. Defensive on purpose: a missing, partial, or
 *  wrongly-typed payload degrades to an empty-ish identity rather than throwing during
 *  boot. Returns null when there is no usable display name at all — callers treat that
 *  as "no acting session" and fall back to the free-form actor name. */
export function normalizeIdentity(raw: unknown): SessionIdentity | null {
  if (!raw || typeof raw !== "object") return null;
  const o = raw as Record<string, unknown>;
  const name = typeof o.name === "string" ? o.name.trim() : "";
  if (!name) return null;
  return {
    id: typeof o.id === "string" ? o.id : "",
    name,
    kind: typeof o.kind === "string" && o.kind ? o.kind : "human",
    auth_method: typeof o.auth_method === "string" ? o.auth_method : "",
    created_at: typeof o.created_at === "string" ? o.created_at : "",
  };
}

/** Cached last-known session identity, written by login/logout and read at boot. */
export interface StoredSession {
  identity: SessionIdentity;
  stored_at: string; // RFC3339 timestamp of when it was cached
}

/** Read + normalize the cached session. Returns null on absence or a corrupt/empty
 *  cache (never throws — a broken localStorage must not halt initialization). */
export function loadStoredSession(): StoredSession | null {
  let raw: string | null = null;
  try {
    raw = safeStorage.getItem(LS_SESSION);
  } catch {
    return null;
  }
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    // Tolerate both the full envelope and a bare identity object.
    const idObj = parsed && typeof parsed === "object" && "identity" in parsed
      ? (parsed.identity as unknown)
      : parsed;
    const identity = normalizeIdentity(idObj);
    if (!identity) return null;
    return {
      identity,
      stored_at: typeof parsed?.stored_at === "string" ? (parsed.stored_at as string) : new Date(0).toISOString(),
    };
  } catch {
    return null;
  }
}

/** Cache a session identity so the next boot shows it pre-probe. Non-fatal on
 *  storage failure (private mode / quota) — the cache is best-effort only. */
export function storeSession(identity: SessionIdentity): void {
  const payload: StoredSession = { identity, stored_at: new Date().toISOString() };
  try {
    safeStorage.setItem(LS_SESSION, JSON.stringify(payload));
  } catch {
    /* storage blocked — the authoritative probe will re-establish it */
  }
}

/** Drop the cached session identity (logout / no-live-session probe result). */
export function clearStoredSession(): void {
  try {
    safeStorage.removeItem(LS_SESSION);
  } catch {
    /* ignore */
  }
}
