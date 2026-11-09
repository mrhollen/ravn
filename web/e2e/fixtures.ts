// Shared ephemeral ravn-server fixtures for the Playwright e2e suite.
//
// ============================================================================
// THE MODEL
// ----------------------------------------------------------------------------
// Every spec in this directory runs against a REAL ravn-server serving the
// pre-built web/dist, on an ephemeral SQLite DB on a random high port (never
// :8080). All of that lifecycle — build-checks, free-port allocation,
// `spawn(bin/ravn-server)` with RAVN_* env, /healthz polling and SIGTERM→SIGKILL
// teardown — lives HERE, not in the specs. Specs must NOT contain any inline
// spawn / getFreePort / waitForServer / healthz code (verified by CI grep).
//
// Two entry points are exposed via `test.extend`:
//
//   1. `serverUrl` (WORKER-SCOPED): one shared ravn-server + temp DB per worker.
//      A worker is a single test-runner process, so at most `workers` shared
//      servers exist concurrently for the whole run — typically 2 in CI and up
//      to ~CPUs/2 (≈6 on this box) locally. This replaces the old model where
//      all 38 spec files each booted their own server (~38 startups per run).
//
//   2. `isolatedServer` (TEST-SCOPED opt-out): a fresh dedicated ravn-server +
//      temp DB created per test and torn down after it. Use it — and ONLY it —
//      when a spec asserts on *global* server state that cannot coexist with
//      other specs' data on the shared worker DB:
//        • an EMPTY project list (e.g. "No projects yet." / gear-disabled-empty
//          boot) → the shared DB is non-empty, so auto-select would pick a
//          foreign project; or
//        • mutually exclusive global counts, or TOKEN-REQUIRED mode (set
//          RAVN_TOKEN via `test.use({ isolatedServerEnv: { RAVN_TOKEN } })`).
//      Documented exception: this is the only place a spec owns its own server.
//
// ============================================================================
// DATA-ISOLATION CONTRACT (read before writing a spec)
// ----------------------------------------------------------------------------
// • Specs isolate by using UNIQUE project names (randomUUID or a distinctive
//   per-spec prefix). They must NEVER assume an empty database: the shared
//   worker DB accumulates data from every spec that ran on that worker earlier.
// • A bare `page.goto(serverUrl)` boots the app, which auto-selects the FIRST
//   project in canonical server order (a stale/absent stored id falls back to
//   projects[0]). On a shared DB that "first project" is NOT necessarily yours —
//   so any spec that asserts its own seeded content right after a bare goto MUST
//   explicitly select it first, e.g. `selectProjectViaDropdown(page, id)`, or
//   pre-store the id via `addInitScript` / a `?project=` URL. Specs that only
//   assert project-agnostic state (toasts, header chrome, keyboard focus, boot
//   errors) need no selection at all.
// • If you cannot avoid asserting global/empty/token-state, switch that spec to
//   `isolatedServer` rather than reaching for a bespoke per-spec server.
// ============================================================================
import { test as base } from '@playwright/test';
// Re-export Playwright value + types specs need so spec files source everything
// from './fixtures' and stay free of direct '@playwright/test' imports.
export { expect } from '@playwright/test';
export type { BoundingBox, Page } from '@playwright/test';

import { spawn, execSync, type ChildProcessWithoutNullStreams } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename); // web/e2e
const webRoot = path.resolve(__dirname, '..'); // web/
const repoRoot = path.resolve(__dirname, '../..'); // repo root
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(webRoot, 'dist');

/** A running ephemeral ravn-server and everything needed to tear it down. */
export interface ServerHandle {
  url: string;
  port: number;
  process: ChildProcessWithoutNullStreams;
  /** Temp dir holding ravn.db + scratch (exposed to isolatedServer users as dataDir). */
  tempDir: string;
  /** Server-side artifact storage root (RAVN_ARTIFACTS_DIR), created under tempDir. */
  artifactsDir: string;
}

/** Minimal handle handed to tests that opt out via `isolatedServer`. */
export interface IsolatedServer {
  /** Base URL (e.g. `http://127.0.0.1:<port>`) of this test's dedicated server. */
  url: string;
  /** The dedicated server's temp dir — a private scratch area to write test files. */
  dataDir: string;
  /** Server-side artifact storage root (RAVN_ARTIFACTS_DIR); inspect uploads on disk here. */
  artifactsDir: string;
}

/** Allocate an OS-assigned free port on loopback and release it immediately.
 *  Exported so specs that need a scratch port for a MOCK server (e.g. board's
 *  OpenCode provider stub) can reuse it instead of redefining the helper. */
export async function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

/** Poll /healthz until the server answers OK (or time out). */
async function waitForServer(url: string, timeoutMs = 15000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const res = await fetch(`${url}/healthz`);
      if (res.ok) return;
    } catch {
      // Not listening yet — retry.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

/** Newest mtime (ms) of any regular file under dir, walking recursively. */
function newestFileMtimeMs(dir: string): number {
  let newest = 0;
  const stack = [dir];
  while (stack.length > 0) {
    const d = stack.pop() as string;
    for (const entry of fs.readdirSync(d, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        stack.push(path.join(d, entry.name));
        continue;
      }
      let mtime = 0;
      try {
        mtime = fs.statSync(path.join(d, entry.name)).mtimeMs;
      } catch {
        // File vanished mid-walk (or unreadable) — skip it.
      }
      if (mtime > newest) newest = mtime;
    }
  }
  return newest;
}

/** A build artifact is STALE when it is missing, or when any of the given source
 *  files/directories is newer than it. Missing source entries are ignored. */
function artifactIsStale(artifact: string, sources: string[]): boolean {
  if (!fs.existsSync(artifact)) return true;
  const mtime = fs.statSync(artifact).mtimeMs;
  for (const src of sources) {
    let isNewer = false;
    try {
      const st = fs.statSync(src);
      isNewer = st.isDirectory() ? newestFileMtimeMs(src) > mtime : st.mtimeMs > mtime;
    } catch {
      // Source path absent (e.g. go.sum in a stripped checkout) — not stale.
    }
    if (isNewer) return true;
  }
  return false;
}

/** Idempotent, freshness-aware build-checks: no-op when web/dist + bin/ravn-server
 *  exist AND are newer than their sources. The mtime guard matters because a STALE
 *  pre-membership ravn-server binary silently serves no /api/v1/projects/{id}/members
 *  routes (every T9 E2E 404s) while an "exists" check would happily reuse it. CI is
 *  unaffected: `make build` / the pipeline's web build always produce fresh artifacts,
 *  so the rebuilds below are no-ops there and only fire locally after code changes.
 *
 *  Exported because playwright.config.ts runs this ONCE from globalSetup BEFORE any
 *  worker starts — a stale-binary race between workers must not be decided at spawn
 *  time (two workers rebuilding bin/ravn-server concurrently → `spawn ETXTBSY`). */
export function ensurePrereqs(): void {
  if (artifactIsStale(path.join(staticDir, 'index.html'), [
    path.join(webRoot, 'src'),
    path.join(webRoot, 'index.html'),
    path.join(webRoot, 'vite.config.ts'),
    path.join(webRoot, 'package.json'),
  ])) {
    execSync('npm run build', { cwd: webRoot, stdio: 'inherit' });
  }
  if (artifactIsStale(serverBin, [
    path.join(repoRoot, 'cmd', 'ravn-server'),
    path.join(repoRoot, 'internal'),
    path.join(repoRoot, 'go.mod'),
    path.join(repoRoot, 'go.sum'),
  ])) {
    buildServerBinary();
  }
}

/** Build bin/ravn-server ATOMICALLY: compile to a per-PID temp file in the same
 *  directory, then rename over the target. Writing directly with `go build -o`
 *  fails with ETXTBSY when the current binary is EXECUTING (a dev server running
 *  from bin/ or another e2e worker's spawned server), and a torn write would
 *  corrupt it. The rename replaces the path atomically; already-spawned processes
 *  keep their old inode, new spawns get the fresh binary. */
function buildServerBinary(): void {
  const tmp = `${serverBin}.tmp.${process.pid}`;
  try {
    execSync(`go build -trimpath -o "${tmp}" ./cmd/ravn-server`, {
      cwd: repoRoot,
      stdio: 'inherit',
    });
    fs.chmodSync(tmp, 0o755);
    fs.renameSync(tmp, serverBin);
  } finally {
    try {
      if (fs.existsSync(tmp)) fs.rmSync(tmp);
    } catch {}
  }
}

/**
 * Boot a single ephemeral ravn-server. The shared worker fixture calls this ONCE
 * per worker; `isolatedServer` calls it once per test (with optional env).
 * Always binds a random high port — :8080 is hard-refused.
 */
async function bootServer(extraEnv: Record<string, string> = {}): Promise<ServerHandle> {
  ensurePrereqs();
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-'));
  const dbPath = path.join(tempDir, 'ravn.db');
  // Always pin artifact storage under this server's own temp dir so any spec can
  // inspect uploads on disk (exposed as IsolatedServer.artifactsDir) and no two
  // servers share a store. Overridable via extraEnv if a test needs a custom dir.
  const artifactsDir = path.join(tempDir, 'artifacts');
  fs.mkdirSync(artifactsDir, { recursive: true });

  const port = await getFreePort();
  if (port === 8080) throw new Error('refusing to bind :8080 for e2e');
  if (port <= 1024) throw new Error(`unexpected privileged port ${port} for e2e`);
  const url = `http://127.0.0.1:${port}`;

  const proc = spawn(serverBin, [], {
    env: {
      ...process.env,
      RAVN_ADDR: `127.0.0.1:${port}`,
      RAVN_DATA: dbPath,
      RAVN_ARTIFACTS_DIR: artifactsDir,
      RAVN_STATIC_DIR: staticDir,
      RAVN_TOKEN: '', // zero-config local mode by default
      OPENCODE_ALLOW_PRIVATE_IPS: 'true', // SSE/same-origin parity (sse-singleton)
      ...extraEnv, // isolated-server overrides (e.g. a real RAVN_TOKEN)
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  proc.stderr?.on('data', (d) => {
    if (process.env.DEBUG_E2E) console.error('[server stderr]', d.toString());
  });

  await waitForServer(url);
  return { url, port, process: proc, tempDir, artifactsDir };
}

/** Graceful shutdown: SIGTERM → 2s grace → SIGKILL, then remove the temp DB. */
async function stopServer(handle: ServerHandle): Promise<void> {
  const { process: proc, tempDir } = handle;
  if (proc && !proc.killed) {
    proc.kill('SIGTERM');
    await new Promise<void>((resolve) => {
      const timeout = setTimeout(() => {
        try {
          proc.kill('SIGKILL');
        } catch {}
        resolve();
      }, 2000);
      proc.on('exit', () => {
        clearTimeout(timeout);
        resolve();
      });
    });
  }
  if (tempDir && fs.existsSync(tempDir)) {
    try {
      fs.rmSync(tempDir, { recursive: true, force: true });
    } catch {}
  }
}

/** WORKER-SCOPED shared server URL — see the module header for the model. */
export type WorkerFixtures = {
  serverUrl: string;
};

/** TEST-SCOPED opt-out fixture + its overridable env option. */
export type TestFixtures = {
  /** A fresh dedicated server + empty DB for one test (see module header). */
  isolatedServer: IsolatedServer;
  /** Extra env for `isolatedServer`; override via `test.use(...)` per spec/test. */
  isolatedServerEnv: Record<string, string>;
};

export const test = base.extend<TestFixtures, WorkerFixtures>({
  // One shared server + temp DB per worker. Setup runs before the worker's first
  // beforeAll/test; teardown after its last. Worker-scoped → at most `workers`
  // servers exist concurrently for the entire run.
  serverUrl: [
    async ({}, use) => {
      const handle = await bootServer();
      try {
        await use(handle.url);
      } finally {
        await stopServer(handle);
      }
    },
    { scope: 'worker' },
  ],

  // Opt-in env for the isolated server (default: none → zero-config local mode).
  // first-visit-autoselect sets this to RAVN_TOKEN=<real> via test.use(...) for
  // token-required mode. Declared as an option so it is overridable per spec/test.
  isolatedServerEnv: [{}, { option: true }],

  // Test-scoped (default): brand-new server + empty DB per test, torn down after.
  // The documented escape hatch from the shared worker DB for global/empty/token
  // state assertions (see module header).
  isolatedServer: async ({ isolatedServerEnv }, use) => {
    const handle = await bootServer(isolatedServerEnv);
    try {
      await use({ url: handle.url, dataDir: handle.tempDir, artifactsDir: handle.artifactsDir });
    } finally {
      await stopServer(handle);
    }
  },
});
