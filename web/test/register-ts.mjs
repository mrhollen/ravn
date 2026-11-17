// Extensionless-specifier resolver that lets the unit suite run on Node's
// NATIVE TypeScript type stripping instead of jiti/register.
//
// Why we run natively (P5 F6, epic 01M75VGJX1HE3JM4XJMXMJ81V9):
//   Node's --experimental-test-coverage maps V8 script coverage RANGES (byte
//   offsets inside the executed script) onto the ON-DISK source text to compute
//   per-line/branch/function statistics. Node's stripTypeScriptTypes output is
//   length-preserving — type-only declarations are whitespace-padded in place —
//   so every offset in the executed script corresponds 1:1 to a position in the
//   .ts file on disk and line attribution is exact. jiti's babel transform
//   instead shrinks type-heavy files (interfaces/annotations removed, CJS
//   preamble prepended), so offsets drift and everything after the first
//   stripped block is mis-attributed — e.g. src/ui/dialog.ts reported 42% line
//   coverage while its own tests show 100% branch/function coverage (a NODE_V8_COVERAGE
//   dump confirmed full execution). See docs/TESTING.md § "Coverage measurement"
//   for the investigation and before/after numbers.
//
// Node version notes:
//   --experimental-strip-types exists since v22.6 and is accepted as a no-op on
//   v23.6+ (where stripping is on by default), so one script line works on both
//   CI's Node 22 and modern local Nodes. module.registerHooks needs >= 22.15.
//
// This file adds no dependency: it only resolves extensionless relative
// specifiers ("./foo", "../bar") to the existing .ts files so the codebase's
// current import style works under strict ESM resolution.
import * as nodeModule from "node:module"; // namespace import: links even without registerHooks
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

const { registerHooks } = nodeModule;
if (typeof registerHooks !== "function") {
  throw new Error(`register-ts.mjs needs module.registerHooks (Node >= 22.15); got ${process.version}`);
}

const hasExt = (s) => /\.[cm]?[jt]sx?$/.test(s);

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (
      (specifier.startsWith("./") || specifier.startsWith("../")) &&
      !hasExt(specifier) &&
      context.parentURL?.startsWith("file://")
    ) {
      const candidate = new URL(specifier + ".ts", context.parentURL);
      if (existsSync(fileURLToPath(candidate))) {
        return { url: candidate.href, shortCircuit: true };
      }
    }
    return nextResolve(specifier, context);
  },
});
