// File-size guardrail for web/src (Phase 0 of the frontend modularization epic).
//
// Enforces that no TypeScript source under web/src/ exceeds a 800-line cap.
// The design, the allowlist rules, and what happens when app.ts plateaus
// above the cap are documented in docs/FRONTEND_ARCHITECTURE.md §6 — keep this
// file and that section in sync when either changes.
//
// Why a unit test instead of a lint rule: the web stack is dependency-light by
// design (docs/TESTING.md §3), and `npm run test:unit` already runs every
// web/test/*.test.ts locally AND in both CI workflows, so this guardrail needs
// no new deps and no workflow edits.
//
// Allowlist rules (the list only ever shrinks):
//   - An entry is deleted the moment its file drops to <= CAP — test 2 fails on
//     stale entries, so nobody has to remember to clean up.
//   - Entries are never added silently: adding one requires justification in the
//     commit message naming the extraction that will remove it. The expectation
//     is that ALLOWLIST becomes empty as the Phase 1/2 extractions land.
//   - CAP is never raised. A concern that needs more than 800 lines is split
//     along surface lines (docs/FRONTEND_ARCHITECTURE.md §2), not capped up.

import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const CAP = 800;

/** Relative web/src paths allowed to exceed CAP until extraction brings them
 *  under it. Currently only the app.ts monolith — see module ownership map in
 *  docs/FRONTEND_ARCHITECTURE.md §2 for the shrink plan. */
const ALLOWLIST: ReadonlySet<string> = new Set(["app.ts"]);

/** Newline-separated segment count; a trailing final newline does not add an
 *  extra blank line (matches `wc -l` and editor line numbers). */
function countLines(text: string): number {
  if (text.length === 0) return 0;
  const lines = text.split(/\r\n|\n/);
  if (lines[lines.length - 1] === "") lines.pop();
  return lines.length;
}

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const SRC_DIR = path.resolve(__dirname, "../src");

function srcFiles(): string[] {
  return fs.readdirSync(SRC_DIR).filter((f) => f.endsWith(".ts")).sort();
}

test("FileSizeGuard: no web/src .ts file exceeds the cap without an allowlist entry", () => {
  const files = srcFiles();
  assert.ok(files.length > 0, "web/src exists and contains TypeScript sources");

  const offenders: string[] = [];
  for (const f of files) {
    const lines = countLines(fs.readFileSync(path.join(SRC_DIR, f), "utf-8"));
    if (lines > CAP && !ALLOWLIST.has(f)) {
      offenders.push(`  src/${f}: ${lines} lines > cap ${CAP}`);
    }
  }

  assert.deepEqual(
    offenders,
    [],
    `Files over the ${CAP}-line cap need an allowlist entry (justified in the commit) ` +
      `or a split along surface lines (docs/FRONTEND_ARCHITECTURE.md §2):\n${offenders.join("\n")}`,
  );
});

test("FileSizeGuard: allowlist has no stale or phantom entries", () => {
  const files = srcFiles();

  // Every listed entry must exist — a typo would otherwise rot silently.
  for (const f of [...ALLOWLIST].sort()) {
    assert.ok(
      files.includes(f),
      `allowlist entry "${f}" does not exist in web/src — delete it`,
    );
  }

  // An allowlisted file that has shrunk under the cap is a violation: the whole
  // point is that the list only ever shrinks.
  const stale: string[] = [];
  for (const f of files) {
    if (!ALLOWLIST.has(f)) continue;
    const lines = countLines(fs.readFileSync(path.join(SRC_DIR, f), "utf-8"));
    if (lines <= CAP) {
      stale.push(`  src/${f}: now ${lines} lines (<= cap ${CAP}) — remove it from ALLOWLIST`);
    }
  }

  assert.deepEqual(
    stale,
    [],
    `Stale allowlist entries:\n${stale.join("\n")}`,
  );
});
