// Ticket 01M3MMVDQYMJQEP3GPB9BZF4HP — blank canvas flash during board loading
// and project switching. While `board === null && !loadError` (with a project
// selected + at least one known project) the app must render an animated,
// theme-matched 3-column skeleton instead of an empty <main>, for BOTH the
// initial page load and project switches; real columns replace it in place.
//
// Self-contained like the other specs: ephemeral ravn-server on a random high
// port (never :8080), temp DB, serves pre-built web/dist. Slow board fetches
// are simulated with Playwright route interception (the same page.route
// pattern board.spec.ts uses for its failure/retry tests) — no fake timers.
import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');

async function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

async function waitForServer(url: string, timeoutMs = 15000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const res = await fetch(`${url}/healthz`);
      if (res.ok) return;
    } catch {
      // Retry until server starts listening
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

const actor = { name: 'e2e-seed', type: 'agent' };

async function seedProject(serverUrl: string, name: string): Promise<any> {
  const res = await fetch(`${serverUrl}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, actor }),
  });
  expect(res.ok).toBe(true);
  return res.json();
}

async function seedTask(serverUrl: string, projectId: string, title: string): Promise<void> {
  const res = await fetch(`${serverUrl}/api/v1/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project_id: projectId, title, priority: 2, actor }),
  });
  expect(res.ok).toBe(true);
}

// zinc palette reference values (Tailwind v4) as sRGB triples — same
// convention theme.spec.ts uses for its surface assertions.
const ZINC_200: readonly [number, number, number] = [228, 228, 231]; // light placeholder
const ZINC_800: readonly [number, number, number] = [39, 39, 42]; // dark placeholder

/** Computed bg-color of the skeleton's first animated placeholder → sRGB string.
 *  Canvas round-trip (theme.spec.ts pattern) normalizes whatever serialization
 *  Chrome reports — oklch() from Tailwind v4 included. */
async function placeholderBgRgb(page: any): Promise<string> {
  return page.evaluate(() => {
    const el = document.querySelector<HTMLElement>(
      '[data-testid="board-loading-skeleton"] span.animate-pulse',
    );
    if (!el) return 'missing';
    const cssColor = getComputedStyle(el).backgroundColor;
    const c = document.createElement('canvas');
    c.width = c.height = 1;
    const ctx = c.getContext('2d')!;
    ctx.fillStyle = '#000';
    try {
      ctx.fillStyle = cssColor;
    } catch {
      /* unknown color syntax — keep black */
    }
    ctx.fillRect(0, 0, 1, 1);
    const d = ctx.getImageData(0, 0, 1, 1).data;
    return `rgb(${d[0]}, ${d[1]}, ${d[2]})`;
  });
}

/** true when a serialized rgb string is within ±4 of the expected sRGB triple. */
function nearRgb(received: string, expected: readonly [number, number, number]): boolean {
  const m = received.match(/rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)/);
  if (!m) return false;
  const got = [Number(m[1]), Number(m[2]), Number(m[3])];
  return expected.every((v, i) => Math.abs(v - got[i]) <= 4);
}

test.describe('Board loading skeleton', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/ravn-server ./cmd/ravn-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-skel-'));
    const serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${serverPort}`;
    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${serverPort}`,
        RAVN_DATA: path.join(tempDir, 'ravn.db'),
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '',
        OPENCODE_ALLOW_PRIVATE_IPS: 'true',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    serverProcess.stderr?.on('data', (d) => {
      if (process.env.DEBUG_E2E) console.error('[server stderr]', d.toString());
    });
    await waitForServer(serverUrl);
  });

  test.afterAll(async () => {
    if (serverProcess && !serverProcess.killed) {
      serverProcess.kill('SIGTERM');
      await new Promise<void>((resolve) => {
        serverProcess.on('exit', () => resolve());
        setTimeout(() => {
          try {
            serverProcess.kill('SIGKILL');
          } catch {}
          resolve();
        }, 2000);
      });
    }
    if (tempDir && fs.existsSync(tempDir)) {
      fs.rmSync(tempDir, { recursive: true, force: true });
    }
    expect(fs.existsSync(tempDir)).toBe(false);
  });

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Wait until the board for `projectId` is rendered (a seeded card visible) —
   *  works whichever project boot auto-selected. */
  async function waitForBoardCard(page: any, title: string): Promise<void> {
    await expect(page.locator('article', { hasText: title })).toBeVisible();
  }

  test('AC1: initial page load shows skeleton columns while the board fetch is pending, then renders real columns', async ({
    page,
  }) => {
    const project = await seedProject(serverUrl, 'Skel Boot Project');
    await seedTask(serverUrl, project.id, 'Alpha Skel Card');

    // Delay EVERY board GET (any project) while the request is in flight — the
    // boot continuation's loadBoard lands inside this window.
    await page.route(new RegExp(`/api/v1/projects/.+/board(\\?|$)`), async (route) => {
      await new Promise((r) => setTimeout(r, 1500));
      route.continue();
    });

    await page.goto(serverUrl);

    // Skeleton is up while the boot fetch is pending: visible root with three lanes.
    const skel = page.getByTestId('board-loading-skeleton');
    await expect(skel).toBeVisible();
    await expect(skel.locator('section')).toHaveCount(3);

    // Drive to the seeded project explicitly (boot auto-selects the FIRST
    // project in this shared DB, not necessarily ours). If it is already the
    // selection this is a no-op — either way the delayed fetch settles below.
    await selectProjectViaDropdown(page, project.id);

    // The real board replaces the skeleton in place once the fetch settles.
    await waitForBoardCard(page, 'Alpha Skel Card');
    await expect(skel).toBeHidden();
  });

  test('AC2: switching projects never blanks the canvas — skeleton while the new board loads, old cards gone', async ({
    page,
  }) => {
    const projectA = await seedProject(serverUrl, 'Skel Switch A');
    await seedTask(serverUrl, projectA.id, 'Alpha Switch Card');
    const projectB = await seedProject(serverUrl, 'Skel Switch B');
    await seedTask(serverUrl, projectB.id, 'Beta Switch Card');

    await page.goto(serverUrl);
    // Boot auto-selects the first project; drive to A explicitly so the switch
    // under test is always A -> B.
    await selectProjectViaDropdown(page, projectA.id);
    await waitForBoardCard(page, 'Alpha Switch Card');

    // Delay ONLY project B's board fetch: clicking the option nulls `board`
    // (selectProject) and starts this slow round-trip.
    await page.route(
      new RegExp(`/api/v1/projects/${projectB.id}/board(\\?|$)`),
      async (route) => {
        await new Promise((r) => setTimeout(r, 1500));
        route.continue();
      },
    );

    const skel = page.getByTestId('board-loading-skeleton');
    await selectProjectViaDropdown(page, projectB.id);

    // No blank canvas: while B's board is pending the skeleton occupies the
    // main area (A's cards are already invalidated — no stale cross-project
    // content either).
    await expect(skel).toBeVisible();
    await expect(skel.locator('section')).toHaveCount(3);
    await expect(page.locator('article', { hasText: 'Alpha Switch Card' })).not.toBeVisible();

    // The switched board replaces the skeleton once the fetch settles.
    await waitForBoardCard(page, 'Beta Switch Card');
    await expect(skel).toBeHidden();
  });

  test('AC3: skeleton placeholders match the active theme (light zinc-200-ish vs dark zinc-800-ish)', async ({
    page,
  }) => {
    // Decoy seeded FIRST so boot auto-selection always lands somewhere other
    // than our project — the switch below is then a real re-fetch in every
    // run shape (shared or standalone DB), keeping the skeleton up.
    await seedProject(serverUrl, 'Skel Theme Decoy');
    const project = await seedProject(serverUrl, 'Skel Theme Project');
    await seedTask(serverUrl, project.id, 'Gamma Skel Card');

    // Hold EVERY board fetch long enough to capture BOTH theme variants while a
    // skeleton is up.
    const holdRoute = new RegExp(`/api/v1/projects/.+/board(\\?|$)`);
    await page.route(holdRoute, async (route) => {
      await new Promise((r) => setTimeout(r, 2500));
      route.continue();
    });

    // Light theme capture.
    await page.goto(serverUrl);
    const skel = page.getByTestId('board-loading-skeleton');
    await expect(skel).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.classList.contains('dark'))).toBe(false);
    const lightBg = await placeholderBgRgb(page);
    expect(nearRgb(lightBg, ZINC_200), `light placeholder ${lightBg} ≈ zinc-200`).toBe(true);

    // Drive to the seeded project (its board fetch is held → skeleton again).
    await selectProjectViaDropdown(page, project.id);
    await expect(skel).toBeVisible();

    // Dark theme capture — same page, skeleton still up (UI toggle per
    // theme.spec.ts; no reload, so the held fetch keeps the skeleton showing).
    await page.getByTestId('header-menu-trigger').click();
    await expect(page.getByTestId('header-menu')).toBeVisible();
    await page.locator('[data-testid="theme-dark"]').click();
    expect(
      await page.evaluate(() => document.documentElement.classList.contains('dark')),
      'dark class present in dark mode',
    ).toBe(true);
    await expect(skel).toBeVisible();
    const darkBg = await placeholderBgRgb(page);
    expect(nearRgb(darkBg, ZINC_800), `dark placeholder ${darkBg} ≈ zinc-800`).toBe(true);
    expect(darkBg, 'light and dark placeholders must differ').not.toBe(lightBg);

    // Let the board land so the test ends on a rendered board.
    await expect(page.locator('article', { hasText: 'Gamma Skel Card' })).toBeVisible();
  });
});
