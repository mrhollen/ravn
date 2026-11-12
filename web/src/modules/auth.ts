// Auth feature module — Phase 2 batch A of the frontend modularization epic.
//
// Owns the BEHAVIOR of the auth concern: the token-persistence flows (save /
// clear / sign-in / sign-out), the session-identity refresh, and the auth-modal
// open/close choreography (focus-session hand-off + the tab-switch refocus
// decision). Every body moved here verbatim from app.ts (batch A); see the
// ownership map in docs/FRONTEND_ARCHITECTURE.md §2.
//
// State-ownership note (Alpine constraint): every bound name index.html
// reads/writes still lives on the flat component root as a plain field. Alpine
// 3.x wraps the x-data object in Vue's reactive proxy, which only observes
// mutations made THROUGH the proxy — a module-owned nested state object mutated
// from its own closures would not re-render. The module therefore touches those
// fields exclusively through the typed accessors on {@link AuthDeps} (getters +
// setters the root implements as `this.x = v`), and calls back into the root
// for everything else via the method-out hooks below. No reference to the
// component itself is ever passed in, and nothing here is typed `any`.

import { ApiError, api, errMsg } from "../api";
import { clearStoredSession, normalizeIdentity, storeSession } from "../identity";
import type { DialogFocus } from "../focus";
import { taskRouteIdFromPath } from "../routes";
import type { SessionIdentity } from "../types";

/** Typed dependency surface for the auth module (state-in / methods-out, per
 *  docs/FRONTEND_ARCHITECTURE.md §4). */
export interface AuthDeps {
  // -- state-in: live accessors into root-owned auth fields (never the root) --
  /** Free-form actor name (the root "actor" concern). */
  actorName(): string;
  authToken(): string;
  showAuthModal(): boolean;
  tokenInput(): string;
  authFocus(): DialogFocus | null;
  identity(): SessionIdentity | null;
  loginUsername(): string;
  loginPassword(): string;
  loadError(): string;
  hasSelectedProject(): boolean;

  // -- state-out: root writes, invoked so mutations flow through the root ----
  setAuthToken(v: string): void;
  setShowAuthModal(v: boolean): void;
  setTokenInput(v: string): void;
  setAuthFocus(f: DialogFocus | null): void;
  setIdentity(id: SessionIdentity | null): void;
  setAuthModalTab(tab: "login" | "token"): void;
  setLoginBusy(b: boolean): void;
  setLoginError(msg: string): void;
  setLoginPassword(p: string): void;

  // -- methods-out: everything the module calls back into the root for -------
  /** `loadError = ""`. */
  clearLoadError(): void;
  /** `bootError = false`. */
  clearBootError(): void;
  /** `projectForbidden = false`. */
  clearProjectForbidden(): void;
  /** Full reload of both project lists. */
  loadProjects(silent?: boolean): Promise<void>;
  /** Boot continuation: keep/auto-select project, board + SSE. */
  continueBoot(deepLinkedTaskId: string | null): Promise<void>;
  connectSse(): void;
  disconnectSse(resetBackoff: boolean): void;
  loadBoard(): Promise<void>;
  /** The root's openDialog focus session for the auth modal. */
  openAuthFocusSession(): DialogFocus | null;
  /** The root's single close path for a dialog focus session. */
  closeDialog(f: DialogFocus | null): void;

  // -- DOM seams (narrow, injected for testability — precedent: focus.ts) ----
  /** `document.getElementById("cb-auth-modal")`, or null outside a browser. */
  authModalRoot(): Element | null;
  /** `document.activeElement`, or null outside a browser. */
  pageActiveElement(): Element | null;
}

export interface AuthModule {
  actingIdentityName(): string;
  openAuthModal(tab?: "login" | "token"): void;
  closeAuthModal(): void;
  saveAuthToken(): void;
  clearAuthToken(): void;
  doLogin(): Promise<void>;
  logout(): Promise<void>;
  refreshSessionIdentity(): Promise<void>;
}

/** Build the auth module bound to one component instance's dependency surface. */
export function createAuthModule(deps: AuthDeps): AuthModule {
  // Sibling calls go through these closures — object-literal methods can't name
  // their siblings without a captured receiver.

  /** Single close path for the auth modal (× / Cancel / backdrop / save / clear / Esc). */
  const closeAuthModal = (): void => {
    deps.setShowAuthModal(false);
    deps.closeDialog(deps.authFocus());
    deps.setAuthFocus(null);
  };

  /** The name to show as "acting as". A live session identity wins over the free-form
   *  actor override; otherwise the (trimmed) actor name; otherwise "You". */
  const actingIdentityName = (): string => {
    return deps.identity()?.name || deps.actorName().trim() || "You";
  };

  const openAuthModal = (tab?: "login" | "token"): void => {
    const wasOpen = deps.showAuthModal();
    deps.setTokenInput(deps.authToken());
    deps.setShowAuthModal(true);
    if (tab) deps.setAuthModalTab(tab);
    if (!wasOpen) {
      // Fresh open: establish the ONE focus session (focus trap + restore-target
      // capture). Focus the active panel's first input — only the active panel is in
      // the DOM thanks to x-if, so "input" is unambiguous.
      deps.setAuthFocus(deps.openAuthFocusSession());
    } else {
      // Already open (e.g. the boot-401 auto-open lands on the token tab and the user
      // then clicks Sign in): do NOT re-capture a second focus session — that would
      // clone a null restore-target and leak the first trap. Just switch panels and,
      // once x-if has committed the new panel to the DOM, move focus to its input —
      // but ONLY if focus isn't already inside the modal: a user (or test) may have
      // focused/typed into the new input before this 0ms timer fires, and re-focusing
      // then would steal subsequent keystrokes into the first field.
      setTimeout(() => {
        const root = deps.authModalRoot();
        if (!root) return;
        const active = deps.pageActiveElement();
        if (active instanceof HTMLInputElement && root.contains(active)) return;
        root.querySelector<HTMLElement>("input")?.focus();
      }, 0);
    }
  };

  /** Submit the Sign-in form: exchange credentials for a session token (stored as the
   *  Bearer credential on every call), adopt the returned identity, and resume boot. */
  const doLogin = async (): Promise<void> => {
    const username = deps.loginUsername().trim();
    if (!username || !deps.loginPassword()) {
      deps.setLoginError("Enter your username and password.");
      return;
    }
    deps.setLoginBusy(true);
    deps.setLoginError("");
    try {
      const res = await api.login(username, deps.loginPassword());
      api.setAuthToken(res.token); // becomes the Bearer on all subsequent calls + SSE ?token=
      deps.setAuthToken(api.getAuthToken());
      const id = normalizeIdentity(res.identity);
      if (id) {
        deps.setIdentity(id);
        storeSession(id);
      }
      deps.setLoginPassword(""); // never linger a password in state/DOM
      deps.clearProjectForbidden();
      closeAuthModal();
      deps.clearLoadError();
      await deps.loadProjects();
      if (deps.loadError()) return;
      const deepLinkedTaskId =
        typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
      // Mirror saveAuthToken()'s post-auth continuation: clear boot error, keep a
      // valid manual selection else auto-select, load board + SSE.
      deps.clearBootError();
      void deps.continueBoot(deepLinkedTaskId);
    } catch (e) {
      if (ApiError.isUnauthorized(e)) deps.setLoginError("Invalid username or password.");
      else deps.setLoginError(errMsg(e));
    } finally {
      deps.setLoginBusy(false);
    }
  };

  /** Sign out: revoke the session server-side, drop the Bearer credential + identity
   *  cache, and reload so the (now unscoped) lists are authoritative again. Best-effort
   *  revoke — a network failure to DELETE still clears local state. */
  const logout = async (): Promise<void> => {
    if (api.getAuthToken()) {
      try {
        await api.revokeSession();
      } catch {
        /* best-effort: the token is dropped locally regardless */
      }
    }
    api.setAuthToken("");
    deps.setAuthToken("");
    deps.setIdentity(null);
    clearStoredSession();
    deps.clearProjectForbidden();
    deps.setLoginError("");
    deps.disconnectSse(true);
    void deps.loadProjects().then(() => {
      if (deps.hasSelectedProject()) {
        void deps.loadBoard();
        deps.connectSse();
      }
    });
  };

  /** Inspect the presenting bearer via GET /auth/session to learn WHO is acting. Fire-
   *  and-forget at boot: a non-session bearer (shared/operator token, or an off-mode
   *  server with no auth route) legitimately returns no identity — that's NOT an error,
   *  so we just clear any stale cached identity in that case without touching the token. */
  const refreshSessionIdentity = async (): Promise<void> => {
    if (!api.getAuthToken()) return; // nothing to inspect
    try {
      const res = await api.getSession();
      const id = normalizeIdentity(res.identity);
      deps.setIdentity(id);
      if (id) storeSession(id);
      else clearStoredSession();
    } catch {
      // No live session for this bearer — drop the cached identity so the header
      // doesn't keep showing a name whose session is gone. Token is left alone: it
      // may still be a valid shared/operator/off credential.
      deps.setIdentity(null);
      clearStoredSession();
    }
  };

  const saveAuthToken = (): void => {
    api.setAuthToken(deps.tokenInput());
    deps.setAuthToken(api.getAuthToken());
    closeAuthModal();
    deps.clearLoadError();
    void deps.loadProjects().then(() => {
      // A bad/expired token re-triggers the auth modal + loadError inside
      // loadProjectsCore; leave the boot-error/retry path to surface it.
      if (deps.loadError()) return;
      // First-visit enters the auth modal from a 401 during boot, which left
      // bootError set — clear it now that projects loaded so the freshly
      // auto-selected board isn't hidden behind the boot-error overlay.
      deps.clearBootError();
      const deepLinkedTaskId =
        typeof window !== "undefined" ? taskRouteIdFromPath(window.location.pathname) : null;
      // Reuse the boot continuation: keeps a valid manual selection, else
      // auto-selects the first project (canonical server order), persists it,
      // loads its board + SSE; with zero projects shows the empty state.
      void deps.continueBoot(deepLinkedTaskId);
    });
  };

  const clearAuthToken = (): void => {
    api.setAuthToken("");
    deps.setAuthToken("");
    deps.setTokenInput("");
    closeAuthModal();
    deps.clearLoadError();
    if (deps.hasSelectedProject()) deps.connectSse();
    void deps.loadProjects().then(() => {
      if (deps.hasSelectedProject()) void deps.loadBoard();
    });
  };

  return {
    actingIdentityName,
    openAuthModal,
    closeAuthModal,
    saveAuthToken,
    clearAuthToken,
    doLogin,
    logout,
    refreshSessionIdentity,
  };
}
