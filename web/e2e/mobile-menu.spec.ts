import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');

// Small-phone viewport: 360px wide (narrower than the 375px mobile.spec.ts
// reference width) with touch enabled — the AC reference class for the new
// slide-down hamburger panel.
const MOBILE_VIEWPORT = { width: 360, height: 812 };

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

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`);
  return res.json();
}

test.describe('Mobile slide-down menu panel (360px)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-mobile-menu-'));
    const dbPath = path.join(tempDir, 'ravn.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${serverPort}`,
        RAVN_DATA: dbPath,
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);

    // Seed one project (auto-selected on boot) + two cards so the board has
    // content to push down when the panel opens.
    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Mobile Menu Project',
      description: 'Seeded for the mobile slide-down panel e2e spec',
      actor: { name: 'e2e-mobile-menu-seed', type: 'agent' },
    });
    projectId = proj.id;

    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'First card',
      description: 'Topmost To Do card — the element the panel must push down.',
      priority: 3,
      actor: { name: 'e2e-mobile-menu-seed', type: 'agent' },
    });
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Second card',
      description: 'Second To Do card.',
      priority: 1,
      actor: { name: 'e2e-mobile-menu-seed', type: 'agent' },
    });
  });

  test.afterAll(async () => {
    if (serverProcess && !serverProcess.killed) {
      serverProcess.kill();
    }
    await new Promise((r) => setTimeout(r, 200));
    fs.rmSync(tempDir, { recursive: true, force: true });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  async function openMobileMenuBoard(
    browser: import('@playwright/test').Browser,
  ): Promise<{ context: import('@playwright/test').BrowserContext; page: import('@playwright/test').Page }> {
    const context = await browser.newContext({ viewport: MOBILE_VIEWPORT, hasTouch: true });
    const page = await context.newPage();
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    // App booted — the dropdown trigger enables when init() completes.
    await expect(page.getByTestId('project-select')).toBeEnabled();
    // Board loaded (columns rendered) so the panel's Filters group exists and
    // there is board content to measure being pushed down.
    await expect(page.locator('main').locator('section').first()).toBeVisible();
    return { context, page };
  }

  test('AC1/AC2: grouped slide-down panel below the header with all seven controls', async ({ browser }, testInfo) => {
    const { context, page } = await openMobileMenuBoard(browser);

    const menuToggle = page.getByRole('button', { name: 'Board menu' });
    await expect(menuToggle).toBeVisible();

    const panel = page.getByTestId('mobile-menu-panel');
    await expect(panel).toBeHidden();

    // AC3 (measured here, asserted per-state below): the header row is stable.
    const header = page.locator('header').first();
    const h1 = (await header.boundingBox())!;
    const boardFirst = page.locator('main').locator('section').first();
    const topBefore = (await boardFirst.boundingBox())!.y;

    // Open the panel.
    await menuToggle.click();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'true');
    await expect(panel).toBeVisible();

    // AC3 — the header height is IDENTICAL open vs. closed: nothing renders
    // inside <header> on mobile anymore, so the toggle must not change its box.
    const h2 = (await header.boundingBox())!;
    expect(h2.height, 'header height stable across the toggle (AC3)').toBe(h1.height);

    const pbox = (await panel.boundingBox())!;
    expect(pbox.x).toBeGreaterThanOrEqual(0);
    expect(pbox.x + pbox.width).toBeLessThanOrEqual(MOBILE_VIEWPORT.width, 'panel fits the 360px viewport');
    // The panel sits in normal document flow directly below the header…
    expect(pbox.y, 'panel starts at the bottom edge of the header').toBeGreaterThanOrEqual(h1.y + h1.height - 2);

    // …and pushes the board down by at least its own height.
    const topAfter = (await boardFirst.boundingBox())!.y;
    expect(topAfter - topBefore, 'board content pushed down by ≥ panel height').toBeGreaterThanOrEqual(pbox.height - 2);

    // AC2 — grouped rows: the five groups appear in order with a label and a
    // divider between each pair.
    const groups = [
      page.getByTestId('mobile-group-actions'),
      page.getByTestId('mobile-group-theme'),
      page.getByTestId('mobile-group-identity'),
      page.getByTestId('mobile-group-status'),
      page.getByTestId('mobile-group-filters'),
    ];
    for (const g of groups) {
      await expect(g).toBeVisible();
    }
    let prevY = -1;
    for (const g of groups) {
      const y = (await g.boundingBox())!.y;
      expect(y, 'groups render top-to-bottom in DOM order').toBeGreaterThan(prevY);
      prevY = y;
    }
    await expect(panel.getByRole('heading', { name: 'Actions' })).toBeVisible();
    await expect(panel.getByRole('heading', { name: 'Theme' })).toBeVisible();
    await expect(panel.getByRole('heading', { name: 'Identity & Token' })).toBeVisible();
    await expect(panel.getByRole('heading', { name: 'Status & Refresh' })).toBeVisible();
    await expect(panel.getByRole('heading', { name: 'Filters & sort' })).toBeVisible();
    // One border-t divider between every group (4 for 5 groups); three are
    // direct section children and the one before Filters sits one level down
    // inside the board-gated x-show wrapper, so match at any depth.
    const dividers = panel.locator('div.border-t');
    expect(await dividers.count(), 'a divider between every group').toBeGreaterThanOrEqual(groups.length - 1);

    // AC1 — all seven secondary controls are present in the panel.
    await expect(panel.getByTestId('mobile-settings-btn')).toBeVisible();
    await expect(panel.getByTestId('mobile-new-project-btn')).toBeVisible();
    await expect(panel.getByTestId('mobile-theme-light')).toBeVisible();
    await expect(panel.getByTestId('mobile-theme-dark')).toBeVisible();
    await expect(panel.getByTestId('mobile-theme-system')).toBeVisible();
    await expect(panel.getByTestId('mobile-actor-input')).toBeVisible();
    await expect(panel.getByTestId('mobile-token-item')).toContainText(/Token: (set|none)/);
    await expect(panel.getByTestId('mobile-sse-status')).toBeVisible();
    await expect(panel.getByTestId('mobile-auto-refresh-toggle')).toBeChecked();

    // Tap-target sizing (≥44px) on the primary interactive rows.
    for (const id of ['mobile-settings-btn', 'mobile-new-project-btn', 'mobile-theme-light', 'mobile-token-item']) {
      const b = (await panel.getByTestId(id).boundingBox())!;
      expect(b.height, `${id} height ≥ 44px`).toBeGreaterThanOrEqual(44);
      expect(b.width, `${id} width ≥ 44px`).toBeGreaterThanOrEqual(44);
    }
    const inputBox = (await panel.getByTestId('mobile-actor-input').boundingBox())!;
    expect(inputBox.height, 'actor input height ≥ 44px').toBeGreaterThanOrEqual(44);
    const refreshLabel = await page.evaluate(() => {
      const el = document.querySelector('[data-testid="mobile-auto-refresh-toggle"]')!.closest('label') as HTMLElement;
      return el.getBoundingClientRect().height;
    });
    expect(refreshLabel, 'auto-refresh label height ≥ 44px').toBeGreaterThanOrEqual(44);

    // Evidence: the full expanded panel at 360px (light theme).
    await page.screenshot({ path: path.join(testInfo.outputDir, 'mobile-menu-expanded-360.png') });

    // Collapse again — header still stable and the board returns to its place.
    await menuToggle.click();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'false');
    await expect(panel).toBeHidden();
    const h3 = (await header.boundingBox())!;
    expect(h3.height, 'header height stable after collapsing too').toBe(h1.height);
    const topRestored = (await boardFirst.boundingBox())!.y;
    expect(Math.abs(topRestored - topBefore), 'board returns to its original offset').toBeLessThanOrEqual(2);

    await context.close();
  });

  test('panel renders correctly in dark mode at 360px (screenshot evidence)', async ({ browser }, testInfo) => {
    const context = await browser.newContext({ viewport: MOBILE_VIEWPORT, hasTouch: true });
    await context.addInitScript(() => localStorage.setItem('ravn_theme', 'dark'));
    const page = await context.newPage();
    page.on('pageerror', (err) => uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`));

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expect(page.getByTestId('project-select')).toBeEnabled();
    await expect(page.locator('main').locator('section').first()).toBeVisible();

    // Class-based dark theme is active…
    await expect
      .poll(() => page.evaluate(() => document.documentElement.classList.contains('dark')))
      .toBe(true);

    const panel = page.getByTestId('mobile-menu-panel');
    await page.getByRole('button', { name: 'Board menu' }).click();
    await expect(panel).toBeVisible();
    // …and the panel surface adopts the dark palette (zinc-900 background).
    // Compare in-page against a reference element styled with the same zinc-900
    // token: Tailwind v4 emits oklch() and Chromium's serialization varies by
    // version, but two equal colors always serialize identically.
    const bg = await panel.evaluate((el) => {
      const ref = document.createElement('div');
      ref.style.backgroundColor = 'oklch(0.21 0.006 285.885)'; // Tailwind zinc-900
      document.body.appendChild(ref);
      const panelBg = getComputedStyle(el as HTMLElement).backgroundColor;
      const isZinc900 = panelBg === getComputedStyle(ref).backgroundColor;
      ref.remove();
      return { bg: panelBg, isZinc900 };
    });
    expect(bg.isZinc900, `panel uses the dark-mode surface color (got ${bg.bg})`).toBe(true);
    expect(bg.bg, 'panel is not the light (white) surface').not.toBe('rgb(255, 255, 255)');

    await page.screenshot({ path: path.join(testInfo.outputDir, 'mobile-menu-dark-360.png') });
    await context.close();
  });

  test('desktop parity (md+): hamburger and panel are absent; the inline tier-2 strip renders', async ({ page }) => {
    // Default project viewport is desktop Chrome (1280x720). At ≥768px the
    // slide-down panel must not exist in the DOM at all (x-if="isMobile"), and
    // the hamburger is hidden — the controls stay inline as before.
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expect(page.getByTestId('project-select')).toBeEnabled();
    await expect(page.locator('main').locator('section').first()).toBeVisible();

    await expect(page.getByRole('button', { name: 'Board menu' })).toBeHidden();
    await expect(page.getByTestId('mobile-menu-panel')).toHaveCount(0);

    // The same filter/sort controls live in the desktop tier-2 strip instead.
    await expect(page.getByTestId('sort-key')).toBeVisible();
    await expect(page.getByTestId('sort-hoist-parents')).toBeVisible();

    // And the avatar preferences dropdown still carries theme/identity/token.
    await expect(page.getByTestId('header-menu-trigger')).toBeVisible();
  });
});
