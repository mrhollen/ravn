// Unit tests for src/theme.ts (Phase 1 extraction from app.ts).
// safeStorage falls back to in-memory storage when window/localStorage is absent,
// so no DOM fakes are needed; keys are cleared between tests.

import test from "node:test";
import assert from "node:assert/strict";

import {
  LS_THEME,
  isThemeMode,
  persistTheme,
  readStoredTheme,
  resolveDark,
} from "../src/theme.ts";
import { safeStorage } from "../src/util.ts";

const LEGACY_KEY = "ravn.theme";

test("theme: isThemeMode accepts only the three modes", () => {
  assert.equal(isThemeMode("light"), true);
  assert.equal(isThemeMode("dark"), true);
  assert.equal(isThemeMode("system"), true);
  assert.equal(isThemeMode(""), false);
  assert.equal(isThemeMode("purple"), false);
  assert.equal(isThemeMode(null), false);
  assert.equal(isThemeMode(1), false);
  assert.equal(isThemeMode(undefined), false);
});

test("theme: readStoredTheme — default, persisted, legacy fallback, precedence, malformed", () => {
  safeStorage.clear();
  // unset → system
  assert.equal(readStoredTheme(), "system");
  // persisted value round-trips
  persistTheme("dark");
  assert.equal(readStoredTheme(), "dark");
  // current key missing but legacy present → legacy wins
  safeStorage.removeItem(LS_THEME);
  assert.equal(safeStorage.getItem(LEGACY_KEY), "dark");
  assert.equal(readStoredTheme(), "dark");
  // malformed legacy only → system
  safeStorage.clear();
  safeStorage.setItem(LEGACY_KEY, "not-a-mode");
  assert.equal(readStoredTheme(), "system");
  // current key takes precedence over legacy
  safeStorage.clear();
  safeStorage.setItem(LEGACY_KEY, "light");
  safeStorage.setItem(LS_THEME, "dark");
  assert.equal(readStoredTheme(), "dark");
  // malformed current value (no legacy fallback value) → system
  safeStorage.clear();
  safeStorage.setItem(LS_THEME, "");
  assert.equal(readStoredTheme(), "system");
});

test("theme: readStoredTheme falls back to system when storage throws", () => {
  const origGetItem = safeStorage.getItem;
  safeStorage.getItem = () => {
    throw new Error("storage blocked");
  };
  try {
    assert.equal(readStoredTheme(), "system");
  } finally {
    safeStorage.getItem = origGetItem;
  }
});

test("theme: persistTheme writes both current and legacy keys", () => {
  safeStorage.clear();
  persistTheme("light");
  assert.equal(safeStorage.getItem(LS_THEME), "light");
  assert.equal(safeStorage.getItem(LEGACY_KEY), "light");
  persistTheme("system");
  assert.equal(safeStorage.getItem(LS_THEME), "system");
  assert.equal(safeStorage.getItem(LEGACY_KEY), "system");
  safeStorage.clear();
});

test("theme: resolveDark — explicit modes pin, system follows the OS preference", () => {
  assert.equal(resolveDark("light", true), false);
  assert.equal(resolveDark("light", false), false);
  assert.equal(resolveDark("dark", false), true);
  assert.equal(resolveDark("dark", true), true);
  assert.equal(resolveDark("system", true), true);
  assert.equal(resolveDark("system", false), false);
});
