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
      execSync('go build -trimpath -o bin/ravn-server ./cmd/ravn-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-mobile-'));
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
    // Long realistic title for the drawer-header title-room test below.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Long title that used to wrap every word in the drawer header at phone widths',
      description: 'Seeded so the mobile drawer-header assertions run against a wrapping title.',
      priority: 2,
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
    await expect(page).toHaveTitle('Rävn');
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

  test('top bar is one compact row at 375px with the settings trigger; secondary strip collapses', async ({ browser }) => {
    const { context, page } = await openMobileBoard(browser);

    // The mobile hamburger toggle is present...
    const menuToggle = page.getByRole('button', { name: 'Board menu' });
    await expect(menuToggle).toBeVisible();
    // ...and the secondary control (live indicator) collapses away behind it.
    // The Projects dropdown — now a SPLIT control of select trigger + attached
    // settings gear — stays in row 1 (a primary control); project creation lives
    // INSIDE its panel, not in the strip. Project settings moved out of the
    // strip into the gear, so the compact row still fits at 375px with no
    // horizontal overflow.
    const projectTrigger = page.getByTestId('project-select');
    await expect(projectTrigger).toBeVisible();
    const gear = page.getByTestId('project-settings-btn');
    await expect(gear).toBeVisible();
    // The gear is ATTACHED to the right edge of the select (same unit, same row):
    // it starts at/after the trigger's right edge and shares its vertical span.
    const sbox = await projectTrigger.boundingBox();
    const gbox = await gear.boundingBox();
    expect(sbox).not.toBeNull();
    expect(gbox).not.toBeNull();
    expect(gbox!.x, 'gear starts at the select\'s right edge').toBeGreaterThanOrEqual(sbox!.x + sbox!.width - 2);
    expect(Math.abs(gbox!.y - sbox!.y), 'gear shares the trigger\'s top').toBeLessThanOrEqual(1);
    expect(Math.abs(gbox!.height - sbox!.height), 'gear matches the trigger\'s height').toBeLessThanOrEqual(1);
    // …and the gear stays within the 375px viewport (no horizontal overflow).
    expect(gbox!.x + gbox!.width).toBeLessThanOrEqual(MOBILE_VIEWPORT.width);
    await expect(page.getByTestId('new-project-item')).not.toBeVisible();

    // The consolidated settings-menu trigger is REACHABLE IN ROW 1 at 375px
    // with no horizontal overflow (the chosen mobile mapping: visible in the
    // compact header row, not hidden behind the hamburger).
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();
    const tbox = await trigger.boundingBox();
    expect(tbox).not.toBeNull();
    expect(tbox!.x).toBeGreaterThanOrEqual(0);
    expect(tbox!.x + tbox!.width).toBeLessThanOrEqual(MOBILE_VIEWPORT.width);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      MOBILE_VIEWPORT.width,
      'row-1 trigger must not widen the document',
    );

    // The inline actor input is gone from the strip: hidden while the menu is closed…
    await expect(page.getByPlaceholder('you')).not.toBeVisible();

    // …and the primary top bar (select + New task + hamburger + settings trigger)
    // still fits a single row: 44px control height + py-3 padding = ~68px, well
    // under the two-row budget (~96px).
    const header = page.locator('header').first();
    await expect(header).toBeVisible();
    const collapsed = await header.boundingBox();
    expect(collapsed).not.toBeNull();
    expect(collapsed!.height).toBeLessThanOrEqual(96);

    // Expanding the strip reveals the live indicator below row 1 (project
    // settings no longer live here — they moved into the gear on the dropdown)…
    await menuToggle.click();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'true');
    await expect(page.locator('[data-testid="live-indicator"]')).toBeVisible();

    // …and the settings menu opens from its row-1 trigger: theme, identity and
    // data controls appear inside the panel (not in the strip), staying within
    // the 375px viewport.
    await trigger.click();
    const panel = page.getByTestId('header-menu');
    await expect(panel).toBeVisible();
    await expect(page.getByPlaceholder('you')).toBeVisible();
    const mbox = await panel.boundingBox();
    expect(mbox).not.toBeNull();
    expect(mbox!.x).toBeGreaterThanOrEqual(0);
    expect(mbox!.x + mbox!.width).toBeLessThanOrEqual(MOBILE_VIEWPORT.width);

    // Escape closes the menu first; a second Escape collapses the strip.
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
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

    // `.tap-target-exempt` (style.css mobile media query) marks intentionally
    // compact controls — segmented theme toggle, per-card 3-dot move trigger,
    // inline AC/relation action buttons — that are EXCLUDED from the mobile
    // 44px min-size rule by design so they keep their tight sizing at narrow
    // widths. They are skipped here; every other visible control is still
    // asserted to meet the minimum below.
    const targets = page.locator(
      'button:visible:not(.tap-target-exempt), select:visible:not(.tap-target-exempt), input:visible:not([type="checkbox"]):not([type="radio"]):not(.tap-target-exempt), textarea:visible:not(.tap-target-exempt)',
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

    // The exemption must actually apply: the per-card 3-dot move trigger stays
    // a compact icon button (≈24px) instead of ballooning to a forced 44x44
    // block that would push the priority badge and wrap card titles.
    const menuToggle = page.locator('[data-testid="card-menu-toggle"]').first();
    await expect(menuToggle).toBeVisible();
    const mbox = await menuToggle.boundingBox();
    expect(mbox, 'card menu toggle missing').not.toBeNull();
    expect(mbox!.height, 'exempted card menu trigger must stay compact (<44px)').toBeLessThan(44);
    expect(mbox!.width, 'exempted card menu trigger must stay compact (<44px)').toBeLessThan(44);

    await context.close();
  });

  test('board columns: stacked at 375px, 3-up on every tablet and desktop width', async ({ browser }) => {
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

    // md lower bound (768px, tablet portrait edge): ALL three columns sit
    // side-by-side on ONE row — no orphaned 'Done' alone on a second row with an
    // empty half-screen (task AC1/AC2). The board is a fixed 3-status kanban, so
    // every width >= md renders 3-up.
    await page.setViewportSize({ width: 768, height: 1024 });
    const p1 = await todo.boundingBox();
    const p2 = await doing.boundingBox();
    const p3 = await done.boundingBox();
    expect(p1).not.toBeNull();
    expect(p2).not.toBeNull();
    expect(p3).not.toBeNull();
    expect(p1!.x, 'To Do is leftmost').toBeLessThan(p2!.x);
    expect(p2!.x, 'Doing is middle').toBeLessThan(p3!.x);
    expect(Math.abs(p1!.y - p2!.y), 'same visual row as To Do').toBeLessThanOrEqual(4);
    expect(Math.abs(p2!.y - p3!.y), 'same visual row as Doing').toBeLessThanOrEqual(4);
    // No column is alone on a lower row: Done's top must match the others.
    expect(p3!.y).toBeGreaterThan(p1!.y - 4);
    expect(p3!.y).toBeLessThan(p1!.y + 4);
    // No horizontal overflow inside the viewport at exactly 768px.
    expect(p3!.x + p3!.width, 'Done stays inside the 768px viewport').toBeLessThanOrEqual(768);

    // md upper bound (1023px, tablet landscape edge — one pixel below lg):
    // still three columns on one row.
    await page.setViewportSize({ width: 1023, height: 768 });
    const l1 = await todo.boundingBox();
    const l2 = await doing.boundingBox();
    const l3 = await done.boundingBox();
    expect(l1!.x).toBeLessThan(l2!.x);
    expect(l2!.x).toBeLessThan(l3!.x);
    expect(Math.abs(l1!.y - l2!.y), 'same visual row at 1023px').toBeLessThanOrEqual(4);
    expect(Math.abs(l2!.y - l3!.y), 'no orphan below 1023px').toBeLessThanOrEqual(4);
    expect(l3!.x + l3!.width, 'Done stays inside the 1023px viewport').toBeLessThanOrEqual(1023);

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

    // Swipe right: todo -> doing. The card's accessible name is its title only
    // (aria-labelledby → the <h3>); column status comes from the section heading.
    let card = page.getByRole('article', { name: 'Swipe me' });
    await expect(card).toBeVisible();
    // CDP touch points are dispatched in viewport coordinates: keep the card's
    // center on-screen (with the 3rd seeded card the columns can push it below
    // the fold after the first move) or the gesture misses the target.
    await card.scrollIntoViewIfNeeded();
    await swipeHorizontal(cdp, card, 'right');

    await expect(doingCol.getByText('Swipe me')).toBeVisible();
    await expect(todoCol.getByText('Swipe me')).toHaveCount(0);
    // The detail drawer must NOT have opened as a side effect of the swipe.
    await expect(page.getByRole('button', { name: 'Post comment' })).toBeHidden();

    // Swipe left: doing -> todo.
    card = page.getByRole('article', { name: 'Swipe me' });
    await expect(card).toBeVisible();
    await card.scrollIntoViewIfNeeded();
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

    let card = page.getByRole('article', { name: 'Menu mover' });
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
    card = page.getByRole('article', { name: 'Menu mover' });
    await card.getByRole('button', { name: 'Move task to another status' }).click();
    await card.locator('[data-testid="card-move-done"]').click();
    await expect(doneCol.getByText('Menu mover')).toBeVisible();
    await expect(doingCol.getByText('Menu mover')).toHaveCount(0);

    // Move it back to To Do through the menu (valid transition from Done).
    card = page.getByRole('article', { name: 'Menu mover' });
    await card.getByRole('button', { name: 'Move task to another status' }).click();
    await card.locator('[data-testid="card-move-todo"]').click();
    await expect(todoCol.getByText('Menu mover')).toBeVisible();

    await context.close();
  });

  // Ticket 01M3MMWHCNEYPQ9K2HMEB6G2RB: the drawer header pinned every action
  // button on the title's row; with the mobile 44px tap-target minimums the
  // h2 was starved to its 4.5rem floor (an ~15-line title column at 360–390px).
  // Below sm (640px) the cluster now wraps onto its own right-aligned toolbar
  // line and "Start Task in OpenCode" collapses to icon-only — asserted here at
  // the reference 390x844 viewport, with record screenshots at 360 and 768.
  test('drawer header: title keeps ≥60% of viewport width and OpenCode button is icon-only at 390px', async ({ browser }, testInfo) => {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true });
    const page = await context.newPage();
    const errors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        errors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => errors.push(`[uncaught pageerror] ${err.message}`));

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');

    // Below md a card click opens the full-page view — the mobile fallback skin.
    const card = page.locator('article', { hasText: 'Long title that used to wrap every word' });
    await expect(card).toBeVisible();
    await card.click();

    const title = page.locator('#task-detail-title');
    await expect(title).toBeVisible();

    // (a) "Start Task in OpenCode" collapses to icon-only below sm: its label
    //     span is hidden…
    const opencodeBtn = page.locator('[data-testid="start-opencode-btn"]');
    await expect(opencodeBtn).toBeVisible();
    const label = opencodeBtn.locator('span');
    await expect(label).toBeHidden();
    // …yet the button keeps its accessible name (aria-label) and stays a
    // primary tap target under the NON-EXEMPT mobile 44px rule (icon-only:
    // ≥44px wide by min-width, comfortably below a labelled-button width).
    const ob = await opencodeBtn.boundingBox();
    expect(ob, 'opencode button box missing').not.toBeNull();
    expect(ob!.height, 'tap-target height').toBeGreaterThanOrEqual(44);
    expect(ob!.width, 'icon-only width (≥44px min, <88px)').toBeGreaterThanOrEqual(44);
    expect(ob!.width, 'icon-only width (<88px)').toBeLessThan(88);
    await expect(opencodeBtn).toHaveAttribute('aria-label', 'Start Task in OpenCode');

    // (b) The title gets at least 60% of the viewport width in the header row.
    const tbox = await title.boundingBox();
    expect(tbox, 'title box missing').not.toBeNull();
    expect(tbox!.width).toBeGreaterThanOrEqual(0.6 * 390);

    // No horizontal overflow at the reference width.
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391);

    await page.screenshot({ path: path.join(testInfo.outputDir, 'drawer-header-390.png') });

    // 360px (tightest phone): same two-tier header, title still ≥60% wide.
    await page.setViewportSize({ width: 360, height: 740 });
    const tbox360 = await title.boundingBox();
    expect(tbox360!, 'title box at 360px').not.toBeNull();
    expect(tbox360!.width).toBeGreaterThanOrEqual(0.6 * 360);
    await page.screenshot({ path: path.join(testInfo.outputDir, 'drawer-header-360.png') });

    // 768px (sm+): the single-row header returns and the label is visible again;
    // title and cluster share one row.
    await page.setViewportSize({ width: 768, height: 1024 });
    await expect(label).toBeVisible();
    const tbox768 = await title.boundingBox();
    const ob768 = await opencodeBtn.boundingBox();
    expect(tbox768!, 'title box at 768px').not.toBeNull();
    expect(ob768!, 'opencode box at 768px').not.toBeNull();
    expect(Math.abs(tbox768!.y - ob768!.y), 'title and cluster share one row at sm+').toBeLessThanOrEqual(4);
    await page.screenshot({ path: path.join(testInfo.outputDir, 'drawer-header-768.png') });

    expect(errors).toEqual([]);
    await context.close();
  });
});
