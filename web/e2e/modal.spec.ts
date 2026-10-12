import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { expectProjectSelected } from './helpers';

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
        // The failed-detail-fetch tests deliberately 404 the task route via
        // page.route — Chrome logs that as a resource error. Expected noise,
        // not an app bug (the app handles it in its drawer error state).
        if (msg.text().includes('status of 404')) return;
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
    // The custom Projects dropdown trigger shows the auto-selected seeded project.
    await expectProjectSelected(page, 'Modal E2E project');
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

    // Named actor so the claim toast text is unambiguous (input lives in the
    // consolidated header settings menu now).
    await page.getByTestId('header-menu-trigger').click();
    const actorInput = page.getByPlaceholder('you');
    await expect(actorInput).toBeVisible();
    await actorInput.fill('e2e-modal-tester');
    await actorInput.dispatchEvent('change');
    await page.keyboard.press('Escape'); // close the menu (focus returns to the trigger)

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

  test('AC1-AC3: dialog semantics and accessibility attributes on modals and drawer', async ({
    page,
  }) => {
    await openBoard(page);

    // 1. Task Detail drawer (role="dialog", aria-modal="true", aria-labelledby="task-detail-title")
    const card = page.locator('article', { hasText: 'Modal fixture' });
    await card.click();
    const detailDrawer = page.locator('#cb-detail-drawer');
    await expect(detailDrawer).toBeVisible();
    await expect(detailDrawer).toHaveAttribute('role', 'dialog');
    await expect(detailDrawer).toHaveAttribute('aria-modal', 'true');
    await expect(detailDrawer).toHaveAttribute('aria-labelledby', 'task-detail-title');
    const detailTitle = page.locator('#task-detail-title');
    await expect(detailTitle).toBeVisible();
    await expect(detailTitle).toHaveText('Modal fixture');
    // Screen readers announce dialog role and accessible title
    await expect(page.getByRole('dialog', { name: 'Modal fixture' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(detailDrawer).toBeHidden();

    // 2. New Task modal (role="dialog", aria-modal="true", aria-labelledby="new-task-title")
    await page.getByRole('button', { name: '+ New task' }).click();
    const newTaskModal = page.locator('#cb-new-task');
    await expect(newTaskModal).toBeVisible();
    await expect(newTaskModal).toHaveAttribute('role', 'dialog');
    await expect(newTaskModal).toHaveAttribute('aria-modal', 'true');
    await expect(newTaskModal).toHaveAttribute('aria-labelledby', 'new-task-title');
    const newTaskTitle = page.locator('#new-task-title');
    await expect(newTaskTitle).toBeVisible();
    await expect(newTaskTitle).toContainText('New task');
    await expect(page.getByRole('dialog', { name: /New task/i })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(newTaskModal).toBeHidden();

    // 3. Project Settings modal (role="dialog", aria-modal="true", aria-labelledby="project-settings-title")
    await page.getByTestId('project-settings-btn').click();
    const projectSettingsModal = page.locator('#cb-project-settings');
    await expect(projectSettingsModal).toBeVisible();
    await expect(projectSettingsModal).toHaveAttribute('role', 'dialog');
    await expect(projectSettingsModal).toHaveAttribute('aria-modal', 'true');
    await expect(projectSettingsModal).toHaveAttribute('aria-labelledby', 'project-settings-title');
    const projectSettingsTitle = page.locator('#project-settings-title');
    await expect(projectSettingsTitle).toBeVisible();
    await expect(projectSettingsTitle).toHaveText('Project settings');
    await expect(page.getByRole('dialog', { name: 'Project settings' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(projectSettingsModal).toBeHidden();

    // 4. Auth Token modal (role="dialog", aria-modal="true", aria-labelledby="auth-modal-title")
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('token-item').click();
    const authModal = page.locator('#cb-auth-modal');
    await expect(authModal).toBeVisible();
    await expect(authModal).toHaveAttribute('role', 'dialog');
    await expect(authModal).toHaveAttribute('aria-modal', 'true');
    await expect(authModal).toHaveAttribute('aria-labelledby', 'auth-modal-title');
    const authTitle = page.locator('#auth-modal-title');
    await expect(authTitle).toBeVisible();
    await expect(authTitle).toHaveText('API Authentication Token');
    await expect(page.getByRole('dialog', { name: 'API Authentication Token' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(authModal).toBeHidden();

    // Reopen detail drawer so QA screenshot shows the accessible dialog open
    await card.click();
    await expect(detailDrawer).toBeVisible();
  });

  // -----------------------------------------------------------------------
  // Task 01M3MMYFRMAW7XM6QDTXNQA6ED: a failed task fetch must not trap the
  // user in "Loading task…". The header bar (title + Close) stays visible, an
  // error state with Retry is offered, and Retry re-arms once the route works.

  /** Install a page.route that 404s ONLY the fixture task's detail GET
   *  (`/tasks/<id>?comment_limit=…` — NOT its `/comments` child route); every
   *  other request passes through. Returns a handle to re-arm the route. */
  function failDetailFetch(page: import('@playwright/test').Page): () => void {
    let failing = true;
    const detailUrl = new RegExp(`/api/v1/tasks/${fixtureTask.id}([?#]|$)`);
    void page.route(detailUrl, (route) => {
      if (!failing || route.request().method() !== 'GET') return route.continue();
      return route.fulfill({
        status: 404,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'not found', message: 'task not found' }),
      });
    });
    return () => {
      failing = false; // re-arm: the real server answers from here on
    };
  }

  test('AC1+AC3: a failed task fetch keeps an always-visible header with Close', async ({ page }) => {
    await openBoard(page);
    failDetailFetch(page); // this test stays in the failed state end to end

    await page.locator('article', { hasText: 'Modal fixture' }).click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    // The bare "Loading task…" trap is gone once the fetch fails…
    await expect(page.getByText('Loading task…', { exact: true })).toBeHidden();

    // …and is replaced by the error state (AC2, asserted here for visibility):
    // friendly 404 message inside an assertive live region + a Retry button.
    // (The headline uses a typographic apostrophe — match it via regex so the
    // assertion doesn't depend on which quote glyph the template spells it with.)
    const alert = drawer.getByRole('alert');
    await expect(alert).toContainText(/Couldn.t load this task/);
    await expect(alert).toContainText('This task was deleted or no longer exists.');
    await expect(page.getByTestId('detail-retry-btn')).toBeVisible();

    // AC1: the persistent header bar is rendered even with no detail — a Close
    // button the user can rely on (no backdrop/Escape guessing required, AC3).
    const closeBtn = page.getByTestId('drawer-bar-close-btn');
    await expect(closeBtn).toBeVisible();

    // Focus follows the failure into the drawer and lands on Retry.
    await expect(page.getByTestId('detail-retry-btn')).toBeFocused();

    // AC3: clicking the header Close dismisses the drawer entirely.
    await closeBtn.click();
    await expect(drawer).toBeHidden();
    await expect(page.locator('main')).toBeVisible();
  });

  test('AC2: Retry re-fetches and loads the detail once the route recovers', async ({ page }) => {
    await openBoard(page);
    const stopFailing = failDetailFetch(page);

    const drawer = page.locator('#cb-detail-drawer');
    await page.locator('article', { hasText: 'Modal fixture' }).click();

    // Failed state first (message + Retry, exactly as the previous test asserts).
    const retryBtn = page.getByTestId('detail-retry-btn');
    await expect(retryBtn).toBeVisible();
    await expect(drawer.getByRole('alert')).toContainText('This task was deleted or no longer exists.');

    // The route recovers; Retry must fetch the real payload and render the detail.
    stopFailing();
    await retryBtn.click();

    // Scope to the drawer's own title id — a PAGE-SCOPED heading query can also
    // resolve the board card's <h3> (same accessible name) whenever Chromium's
    // aria-modal pruning of content behind the dialog does not hold at sample
    // time (observed under CI load: the Retry button is :disabled mid-fetch,
    // focus drops to <body>, and both headings surface → strict-mode violation).
    const detailTitle = page.locator('#task-detail-title');
    await expect(detailTitle).toBeVisible();
    await expect(detailTitle).toHaveText('Modal fixture');
    // Both transient blocks unmount (x-if) once the detail renders…
    await expect(page.getByTestId('detail-retry-btn')).toHaveCount(0);
    await expect(page.getByText('Loading task…', { exact: true })).toBeHidden();
    // …and the persistent bar yields to the loaded header's own Close button.
    await expect(page.getByTestId('drawer-bar-close-btn')).toHaveCount(0);
    await expect(page.getByTestId('detail-close-btn')).toBeVisible();
  });
});
