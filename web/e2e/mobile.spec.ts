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
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

// iPhone-class viewport: 375px wide (the AC reference width), touch enabled.
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

test.describe('Mobile experience (375px, touch)', () => {
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
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-mobile-'));
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

    // Seed exactly one project (the app auto-selects the first project on load).
    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Mobile E2E Project',
      description: 'Seeded for the mobile experience e2e spec',
      actor: { name: 'e2e-mobile-seed', type: 'agent' },
    });
    projectId = proj.id;

    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Swipe me',
      description: 'Moved via a touch swipe in the e2e test',
      priority: 3,
      actor: { name: 'e2e-mobile-seed', type: 'agent' },
    });
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Menu mover',
      description: 'Moved via the per-card move menu in the e2e test',
      priority: 1,
      actor: { name: 'e2e-mobile-seed', type: 'agent' },
    });
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

  test.beforeEach(async () => {
    uncaughtConsoleErrors = [];
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  async function openMobileBoard(browser: import('@playwright/test').Browser) {
    const context = await browser.newContext({ viewport: MOBILE_VIEWPORT, hasTouch: true });
    const page = await context.newPage();
    const cdp = await context.newCDPSession(page);
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        if (msg.text().includes('status of 409')) return;
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');
    // The seeded project is auto-selected by the app on load — the custom
    // Projects dropdown trigger shows it.
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeVisible();
    await expect(trigger).toContainText('Mobile E2E Project');
    return { context, page, cdp };
  }

  function column(page: import('@playwright/test').Page, label: string) {
    return page.locator('section', { has: page.getByRole('heading', { name: label }) });
  }

  /** Simulate a horizontal swipe across an element's center via CDP touch events. */
  async function swipeHorizontal(
    cdp: import('@playwright/test').CDPSession,
    locator: import('@playwright/test').Locator,
    direction: 'right' | 'left',
  ): Promise<void> {
    const box = await locator.boundingBox();
    expect(box).not.toBeNull();
    const cx = Math.round(box!.x + box!.width / 2);
    const cy = Math.round(box!.y + box!.height / 2);
    const sign = direction === 'right' ? 1 : -1;

    const dispatch = (type: string, x: number | null) =>
      cdp.send('Input.dispatchTouchEvent', {
        type,
        touchPoints: x === null ? [] : [{ x, y: cy }],
      });

    await dispatch('touchStart', cx);
    // Walk the finger across in small steps so the gesture reads as a swipe.
    for (const step of [15, 40, 65, 95]) {
      await new Promise((r) => setTimeout(r, 12));
      await dispatch('touchMove', cx + sign * step);
    }
    await new Promise((r) => setTimeout(r, 12));
    await dispatch('touchEnd', null);
  }

  test('top bar is one compact row at 375px and collapses secondary controls', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    // The mobile hamburger toggle is present...
    const menuToggle = page.getByRole('button', { name: 'Board menu' });
    await expect(menuToggle).toBeVisible();
    // ...and the secondary controls are collapsed away. The Projects dropdown
    // trigger stays in row 1 (a primary control); project creation lives INSIDE
    // its panel, not in the secondary strip.
    await expect(page.getByPlaceholder('you')).not.toBeVisible();
    await expect(page.getByTestId('project-select')).toBeVisible();
    await expect(page.getByTestId('new-project-item')).not.toBeVisible();

    // The primary top bar (select + New task + hamburger) fits in a single row:
    // 44px control height + py-3 padding = ~68px, well under the two-row budget (~96px).
    const header = page.locator('header').first();
    await expect(header).toBeVisible();
    const collapsed = await header.boundingBox();
    expect(collapsed).not.toBeNull();
    expect(collapsed!.height).toBeLessThanOrEqual(96);

    // Expanding the menu reveals every secondary control below row 1...
    await menuToggle.click();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'true');
    await expect(page.getByPlaceholder('you')).toBeVisible();
    await expect(page.locator('[data-testid="live-indicator"]')).toBeVisible();

    // ...and Escape collapses it again.
    await page.keyboard.press('Escape');
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'false');
    await expect(page.getByPlaceholder('you')).not.toBeVisible();

    await context.close();
  });

  test('Projects dropdown opens at 375px and reaches "+ New project…" inside it', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    // The trigger is part of row 1 and meets the tap-target minimum.
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeEnabled();
    const tbox = await trigger.boundingBox();
    expect(tbox).not.toBeNull();
    expect(tbox!.height).toBeGreaterThanOrEqual(44);
    expect(tbox!.width).toBeGreaterThanOrEqual(44);

    // Open the dropdown.
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu).toBeVisible();
    // The panel stays within the 375px viewport (no horizontal overflow).
    const mbox = await menu.boundingBox();
    expect(mbox).not.toBeNull();
    expect(mbox!.x).toBeGreaterThanOrEqual(0);
    expect(mbox!.x + mbox!.width).toBeLessThanOrEqual(MOBILE_VIEWPORT.width);

    // The trailing action item opens the new-project form bar below the header.
    await page.getByTestId('new-project-item').click();
    await expect(menu).toBeHidden();
    const nameInput = page.locator('#cb-new-project-name-input');
    await expect(nameInput).toBeVisible();
    const nbox = await nameInput.boundingBox();
    expect(nbox).not.toBeNull();
    expect(nbox!.height).toBeGreaterThanOrEqual(44);

    // Cancel closes the bar without creating anything.
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(nameInput).not.toBeVisible();

    await context.close();
  });

  test('all visible interactive controls meet the 44x44px tap-target minimum at 375px', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    const targets = page.locator(
      'button:visible, select:visible, input:visible:not([type="checkbox"]):not([type="radio"]), textarea:visible',
    );
    const count = await targets.count();
    expect(count).toBeGreaterThan(0);

    for (let i = 0; i < count; i++) {
      const el = targets.nth(i);
      const box = await el.boundingBox();
      expect(box, `tap target #${i} (${await el.getAttribute('class')}) missing`).not.toBeNull();
      expect(
        box!.height,
        `control "${await el.innerText().catch(() => '')}" height ${box!.height}px < 44px`,
      ).toBeGreaterThanOrEqual(44);
      if (await el.evaluate((n) => n instanceof HTMLButtonElement)) {
        expect(
          box!.width,
          `button "${await el.innerText().catch(() => '')}" width ${box!.width}px < 44px`,
        ).toBeGreaterThanOrEqual(44);
      }
    }

    await context.close();
  });

  test('board columns: stacked at 375px, 2-up at md, 3-up at lg', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    const todo = column(page, 'To Do');
    const doing = column(page, 'Doing');
    const done = column(page, 'Done');
    for (const col of [todo, doing, done]) {
      await expect(col).toBeVisible();
    }

    // 375px: single column — all three stack vertically at the same x.
    const m1 = await todo.boundingBox();
    const m2 = await doing.boundingBox();
    const m3 = await done.boundingBox();
    expect(m1).not.toBeNull();
    expect(m1!.x).toBeCloseTo(m2!.x, 0);
    expect(m1!.x).toBeCloseTo(m3!.x, 0);
    expect(m2!.y).toBeGreaterThan(m1!.y);
    expect(m3!.y).toBeGreaterThan(m2!.y);
    expect(m1!.width).toBeGreaterThan(280);

    // md (768px and up): two columns per row — To Do + Doing side by side, Done wraps.
    await page.setViewportSize({ width: 800, height: 900 });
    const t1 = await todo.boundingBox();
    const d1 = await doing.boundingBox();
    const f1 = await done.boundingBox();
    expect(t1!.x).toBeLessThan(d1!.x);
    expect(Math.abs(t1!.y - d1!.y)).toBeLessThanOrEqual(4);
    expect(f1!.y).toBeGreaterThan(t1!.y + 20);

    // lg (1024px and up): three columns on one row.
    await page.setViewportSize({ width: 1100, height: 900 });
    const t2 = await todo.boundingBox();
    const d2 = await doing.boundingBox();
    const f2 = await done.boundingBox();
    expect(t2!.x).toBeLessThan(d2!.x);
    expect(d2!.x).toBeLessThan(f2!.x);
    expect(Math.abs(t2!.y - d2!.y)).toBeLessThanOrEqual(4);
    expect(Math.abs(d2!.y - f2!.y)).toBeLessThanOrEqual(4);

    await context.close();
  });

  test('touch swipe moves a card between columns (and back)', async ({ browser }) => {
    const { context, page, cdp } = await openMobileBoard(browser);

    const todoCol = column(page, 'To Do');
    const doingCol = column(page, 'Doing');

    // Swipe right: todo -> doing. The aria-label includes the current status.
    let card = page.getByRole('article', { name: 'Task: Swipe me, status To Do' });
    await expect(card).toBeVisible();
    await swipeHorizontal(cdp, card, 'right');

    await expect(doingCol.getByText('Swipe me')).toBeVisible();
    await expect(todoCol.getByText('Swipe me')).toHaveCount(0);
    // The detail drawer must NOT have opened as a side effect of the swipe.
    await expect(page.getByRole('button', { name: 'Post comment' })).toBeHidden();

    // Swipe left: doing -> todo.
    card = page.getByRole('article', { name: 'Task: Swipe me, status Doing' });
    await expect(card).toBeVisible();
    await swipeHorizontal(cdp, card, 'left');

    await expect(todoCol.getByText('Swipe me')).toBeVisible();
    await expect(doingCol.getByText('Swipe me')).toHaveCount(0);

    await context.close();
  });

  test('per-card move menu moves a task between statuses on touch', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    const todoCol = column(page, 'To Do');
    const doingCol = column(page, 'Doing');
    const doneCol = column(page, 'Done');

    let card = page.getByRole('article', { name: 'Task: Menu mover, status To Do' });
    await expect(card).toBeVisible();

    // Open the per-card move menu.
    const toggle = card.getByRole('button', { name: 'Move task to another status' });
    await toggle.click();
    await expect(card.locator('[data-testid="card-move-doing"]')).toBeVisible();
    // The current status is shown but not actionable...
    await expect(card.locator('[data-testid="card-move-todo"]')).toHaveClass(/cursor-default/);

    // Pick "Doing" (valid transition from To Do).
    await card.locator('[data-testid="card-move-doing"]').click();
    await expect(doingCol.getByText('Menu mover')).toBeVisible();
    await expect(todoCol.getByText('Menu mover')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Post comment' })).toBeHidden();

    // Move from Doing to Done.
    card = page.getByRole('article', { name: 'Task: Menu mover, status Doing' });
    await card.getByRole('button', { name: 'Move task to another status' }).click();
    await card.locator('[data-testid="card-move-done"]').click();
    await expect(doneCol.getByText('Menu mover')).toBeVisible();
    await expect(doingCol.getByText('Menu mover')).toHaveCount(0);

    // Move it back to To Do through the menu (valid transition from Done).
    card = page.getByRole('article', { name: 'Task: Menu mover, status Done' });
    await card.getByRole('button', { name: 'Move task to another status' }).click();
    await card.locator('[data-testid="card-move-todo"]').click();
    await expect(todoCol.getByText('Menu mover')).toBeVisible();

    await context.close();
  });
});
