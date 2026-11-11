// Theme core (Phase 1 extraction from app.ts — frontend modularization epic):
// UI theme choice, persisted reader/writer, and light/dark/system resolution.
// Pure + LS helpers only — no DOM; the `.dark` class toggle on <html> and the
// prefers-color-scheme listener stay in the composition root (thin adapters).

import { safeStorage } from "./util";

/** localStorage key for the persisted UI theme choice. */
export const LS_THEME = "ravn_theme";
/** Legacy key written/read alongside LS_THEME so pre-rename choices keep working. */
const LEGACY_LS_THEME = "ravn.theme";

/** UI theme choice. "system" (default) follows the OS prefers-color-scheme. */
export type ThemeMode = "light" | "dark" | "system";

export function isThemeMode(v: unknown): v is ThemeMode {
  return v === "light" || v === "dark" || v === "system";
}

/** Read the persisted theme choice; anything malformed falls back to system. */
export function readStoredTheme(): ThemeMode {
  try {
    const raw = safeStorage.getItem(LS_THEME) ?? safeStorage.getItem(LEGACY_LS_THEME);
    return isThemeMode(raw) ? raw : "system";
  } catch {
    return "system";
  }
}

/** Persist the theme choice under both current + legacy keys; storage failures
 *  (private mode) are non-fatal. */
export function persistTheme(mode: ThemeMode): void {
  try {
    safeStorage.setItem(LS_THEME, mode);
    safeStorage.setItem(LEGACY_LS_THEME, mode);
  } catch {
    /* private mode — non-fatal */
  }
}

/** Resolve whether the theme renders dark right now: explicit light/dark pin it;
 *  system follows the injected OS preference (the root reads it from
 *  window.matchMedia and passes `prefersSystemDark` in). */
export function resolveDark(mode: ThemeMode, prefersSystemDark: boolean): boolean {
  if (mode === "light") return false;
  if (mode === "dark") return true;
  return prefersSystemDark;
}
