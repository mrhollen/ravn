import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// Wide centered task-detail modal — CrowBan task 01M47MWSTTPS14M6XB1A8HFPV1.
// The default detail surface (>=md) is a WIDE CENTERED MODAL (80vw, max-w-5xl)
// sitting ABOVE the toasts; below md (<768px) card clicks open the FULL-PAGE
// /tasks/<id> route instead. Self-contained like the other specs: ephemeral
// crowban-server on a random high port (never :8080), temp DB, pre-built dist.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

const MOBILE_VIEWPORT = { width: 375, height: 812 };

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

const SEED_ACTOR = { name: 'e2e-modal-seed', type: 'agent' };

test.describe('Wide centered task-detail modal', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
  let fixtureTask: any; // plain fixture used by every test in this spec
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-modal-'));
    const dbPath = path.join(tempDir, 'crowban.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Modal E2E project',
      description: 'Seeded for the wide centered modal e2e spec',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    // Plain fixture (no ACs/comments): the panel stays compact so screenshots
    // of the centered layout are uncluttered.
    fixtureTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Modal fixture',
      description: 'Fixture for the modal spec.',
      priority: 2,
      actor: SEED_ACTOR,
    })).task; // POST /tasks → { task, possible_duplicates }
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
      if (msg.type() === 'error') {
        if (msg.text().includes('status of 409')) return;
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Load the board route and wait until the seeded project + fixture card render. */
  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');
    const projectSelect = page.locator('header select').first();
    await expect(projectSelect).toHaveValue(projectId);
    await expect(page.locator('article', { hasText: 'Modal fixture' })).toBeVisible();
  }

  test('AC1: desktop (>=768px) card click opens a WIDE CENTERED modal, not a right-hand drawer', async ({
    page,
  }) => {
    await openBoard(page);
    const viewport = page.viewportSize()!; // Desktop Chrome default: 1280x720
    expect(viewport.width).toBeGreaterThanOrEqual(768);

    const card = page.locator('article', { hasText: 'Modal fixture' });
    await card.click();

    // The overlay wrapper is fixed and covers the board (which stays visible).
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();
    await expect(wrapper).toHaveCSS('position', 'fixed');
    expect(new URL(page.url()).pathname).toBe('/'); // a plain click never routes
    await expect(page.locator('main')).toBeVisible();

    // Wide: most of the viewport, capped by max-w-5xl (64rem) at huge widths.
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel).toBeVisible();
    const box = await panel.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.width).toBeGreaterThanOrEqual(viewport.width * 0.6, 'panel must be WIDE');
    expect(box!.width).toBeLessThanOrEqual(64 * 16 + 2, 'max-w-5xl cap');

    // Centered: roughly equidistant from both edges (the old drawer sat
    // flush right), and near the vertical middle.
    const gapLeft = box!.x;
    const gapRight = viewport.width - (box!.x + box!.width);
    expect(Math.abs(gapLeft - gapRight)).toBeLessThanOrEqual(16, 'horizontally centered');
    expect(gapLeft).toBeGreaterThanOrEqual(24, 'not flush to the left edge');
    expect(gapRight).toBeGreaterThanOrEqual(24, 'NOT flush to the right edge (old drawer was)');
    const centerY = box!.y + box!.height / 2;
    expect(Math.abs(centerY - viewport.height / 2)).toBeLessThanOrEqual(4, 'near vertical center');

    // The "Open full page" affordance travels with the panel into the modal.
    await expect(page.locator('[data-testid="open-fullpage-btn"]')).toBeVisible();

    // Backdrop click closes (old behavior kept): a click on the dim area at
    // the far left targets the backdrop, not the panel.
    await wrapper.click({ position: { x: 8, y: Math.round(viewport.height / 2) } });
    await expect(wrapper).toBeHidden();
    await expect(page.locator('main')).toBeVisible();

    // Re-open so the test-finished screenshot (the QA evidence shot) shows the
    // wide centered modal over the dimmed board, not the closed state.
    await card.click();
    await expect(panel.getByRole('heading', { name: 'Modal fixture' })).toBeVisible();
  });

  test('AC2: a toast is on screen while the modal is open and the modal stacks ABOVE toasts', async ({
    page,
  }) => {
    await openBoard(page);
    // Wide-monitor scenario (the motivating case): at 1920px the capped panel
    // (max-w-5xl) leaves the top-right toast corner completely clear.
    await page.setViewportSize({ width: 1920, height: 1080 });
    const viewport = { width: 1920, height: 1080 };

    // Named actor so the claim toast text is unambiguous.
    const actorInput = page.getByPlaceholder('you');
    await actorInput.fill('e2e-modal-tester');
    await actorInput.dispatchEvent('change');

    await page.locator('article', { hasText: 'Modal fixture' }).click();
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel.getByRole('heading', { name: 'Modal fixture' })).toBeVisible();

    // Trigger a KNOWN toast from inside the modal: claim the task.
    await panel.getByRole('button', { name: 'Claim', exact: true }).click();
    const toastChip = page.getByText('Claimed task as e2e-modal-tester');
    await expect(toastChip).toBeVisible({ timeout: 10_000 });

    // The toast is really on screen (non-zero box inside the viewport)…
    const toastBox = await toastChip.boundingBox();
    expect(toastBox).not.toBeNull();
    expect(toastBox!.x + toastBox!.width).toBeLessThanOrEqual(viewport.width);
    expect(toastBox!.y).toBeGreaterThanOrEqual(0);

    // …and the computed stacking order puts the modal ABOVE the toast layer:
    // toasts can no longer cover the detail controls (the old z-40 drawer bug).
    const stacking = await page.evaluate(() => {
      const modal = document.getElementById('cb-detail-drawer') as HTMLElement;
      const toasts = document.querySelector('[data-testid="toast-stack"]') as HTMLElement;
      return {
        modalZ: getComputedStyle(modal).zIndex,
        toastZ: getComputedStyle(toasts).zIndex,
      };
    });
    expect(Number(stacking.modalZ)).toBeGreaterThan(
      Number(stacking.toastZ),
      `modal z=${stacking.modalZ} must exceed toast z=${stacking.toastZ}`,
    );

    // On this wide viewport the toast clears the capped panel entirely —
    // the evidence shot (auto-captured on pass) shows both, fully visible.
    const panelBox = await panel.boundingBox();
    expect(panelBox).not.toBeNull();
    expect(toastBox!.x).toBeGreaterThanOrEqual(panelBox!.x + panelBox!.width);

    // Leave modal + toast open: the test-finished screenshot is the QA
    // evidence for "toast on screen while the modal is open, not covered".
  });

  test('AC3: mobile (375x812) card click opens the FULL PAGE at /tasks/<id>', async ({
    browser,
  }, testInfo) => {
    const context = await browser.newContext({ viewport: MOBILE_VIEWPORT, hasTouch: true });
    const m = await context.newPage();
    m.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[mobile console.error] ${msg.text()}`);
      }
    });
    m.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[mobile pageerror] ${err.message}`);
    });

    await openBoard(m);
    await m.locator('article', { hasText: 'Modal fixture' }).click();

    // The SAME code path "Open full page" uses: real /tasks/<id> URL, so the
    // view is deep-linkable and browser back returns to the board.
    await expect(m).toHaveURL(new RegExp(`/tasks/${fixtureTask.id}$`));

    const wrapper = m.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();
    // In-flow page mode (not the fixed centered modal overlay).
    await expect(wrapper).toHaveCSS('position', 'relative');
    await expect(m.locator('main')).toBeHidden();

    const panel = m.locator('#cb-detail-drawer aside');
    await expect(panel.getByRole('heading', { name: 'Modal fixture' })).toBeVisible();
    // Page chrome: back affordance present, modal-only button absent.
    await expect(m.locator('[data-testid="fullpage-back-btn"]')).toBeVisible();
    await expect(m.locator('[data-testid="open-fullpage-btn"]')).toBeHidden();

    // Explicit evidence shot BEFORE closing the context (the auto-captured
    // test-finished screenshot would be blank once the page is gone): saved
    // into this test's test-results/ folder by Playwright.
    await testInfo.attach('mobile-full-page-open', {
      body: await m.screenshot(),
      contentType: 'image/png',
    });

    await context.close();
  });

  test('AC4: "Open full page" from the modal navigates to /tasks/<id>; browser back returns to the board', async ({
    page,
  }) => {
    await openBoard(page);
    await page.locator('article', { hasText: 'Modal fixture' }).click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/');

    // From the modal, the affordance pushes the shareable route.
    await page.locator('[data-testid="open-fullpage-btn"]').click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${fixtureTask.id}$`));
    await expect(wrapper).toHaveCSS('position', 'relative');
    await expect(page.locator('main')).toBeHidden();
    await expect(page.locator('[data-testid="fullpage-back-btn"]')).toBeVisible();
    await expect(page.locator('[data-testid="open-fullpage-btn"]')).toBeHidden();

    // Browser back pops the pushed entry → board, closed panel.
    await page.goBack();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(wrapper).toBeHidden();
    await expect(page.locator('main')).toBeVisible();
  });
});
