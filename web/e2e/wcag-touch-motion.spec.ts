// WCAG touch-target sizing (2.5.8) + reduced motion (2.2.2 / 2.3.3) for the
// pointer-coarse and prefers-reduced-motion overrides added in style.css.
//
// Companion to mobile.spec.ts (narrow-viewport coverage): these tests exercise
// the POINTER-TYPE axis independently of viewport width:
//   - a touch tablet (820x1180, hasTouch → Chromium flips `pointer: coarse`)
//     must get real 44px touch boxes even though its wide viewport would keep
//     desktop compact sizing under the old max-width:767px rule alone;
//   - a fine-pointer desktop (1280x720) must NOT change layout: the named
//     compact controls stay compact, with a 24px WCAG-2.5.8 floor instead of
//     their former ~20px icon-only boxes;
//   - prefers-reduced-motion must freeze the looping SSE halo and the pulsing
//     claimed-task badge while keeping every status indicator legible/static.
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

const ACTOR = { name: 'e2e-touch-motion', type: 'agent' as const };
const TASK_TITLE = 'Touch Motion A11y Task';
const BLOCKER_TITLE = 'Blocking Task for Relation Row';

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
      // Retry until the server is listening.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

/** Non-exempt visible interactive controls — same selector set as the width-
 *  based rule in style.css, so every element asserted here is subject to it. */
const TARGET_SELECTOR =
  'button:visible:not(.tap-target-exempt), select:visible:not(.tap-target-exempt), input:visible:not([type="checkbox"]):not([type="radio"]):not(.tap-target-exempt), textarea:visible:not(.tap-target-exempt)';

async function boxSize(locator: import('@playwright/test').Locator): Promise<{ w: number; h: number }> {
  const b = await locator.boundingBox();
  expect(b, 'element has no bounding box').not.toBeNull();
  return { w: b!.width, h: b!.height };
}

test.describe('pointer-coarse touch targets + prefers-reduced-motion', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;

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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-touch-motion-e2e-'));
    const port = await getFreePort();
    expect(port).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${port}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${port}`,
        RAVN_DATA: path.join(tempDir, 'ravn.db'),
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
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
  });

  /** Seed a project with one task carrying an AC (drawer Edit/✕ rows) that is
   *  blocked-by a second task (drawer relation row + Remove button). */
  async function seed(): Promise<{ projectId: string }> {
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: `Touch Motion ${Date.now()}`, actor: ACTOR }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const makeTask = async (title: string, acceptance_criteria?: string[]) => {
      const res = await fetch(`${serverUrl}/api/v1/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project_id: project.id, title, priority: 2, actor: ACTOR, acceptance_criteria }),
      });
      expect(res.ok).toBe(true);
      return (await res.json()).task;
    };

    const task = await makeTask(TASK_TITLE, ['Criterion sized for touch']);
    const blocker = await makeTask(BLOCKER_TITLE);

    const relRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/relations`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ other_id: blocker.id, kind: 'blocked-by', actor: ACTOR }),
    });
    expect(relRes.ok).toBe(true);

    return { projectId: project.id };
  }

  test('touch tablet at 820x1180: every non-exempt control and each .coarse-44 control reaches 44px', async ({ browser }) => {
    const { projectId } = await seed();
    const context = await browser.newContext({ viewport: { width: 820, height: 1180 }, hasTouch: true });
    const page = await context.newPage();

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projectId);

    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    await expect(todoCol.getByRole('heading', { name: TASK_TITLE })).toBeVisible();

    // Generic coarse-pointer minimum: every non-exempt visible control.
    const targets = page.locator(TARGET_SELECTOR);
    const count = await targets.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < count; i++) {
      const el = targets.nth(i);
      const { w, h } = await boxSize(el);
      expect(h, `control #${i} height ${h}px < 44px`).toBeGreaterThanOrEqual(44);
      if (await el.evaluate((n) => n instanceof HTMLButtonElement)) {
        expect(w, `button #${i} width ${w}px < 44px`).toBeGreaterThanOrEqual(44);
      }
    }

    // The named compact control (card 3-dot trigger) is .tap-target-exempt so
    // the generic rule skips it — but .coarse-44 must give it a REAL 44x44 box.
    const menuToggle = page.locator('[data-testid="card-menu-toggle"]').first();
    await expect(menuToggle).toBeVisible();
    const { w: mw, h: mh } = await boxSize(menuToggle);
    expect(mh, `coarse-44 card menu toggle height ${mh}px < 44px`).toBeGreaterThanOrEqual(44);
    expect(mw, `coarse-44 card menu toggle width ${mw}px < 44px`).toBeGreaterThanOrEqual(44);

    // Drawer: AC Edit / Delete and the relation Remove rows are .coarse-44 too.
    await todoCol.getByRole('heading', { name: TASK_TITLE }).click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: TASK_TITLE })).toBeVisible();

    const editBtn = drawer.getByRole('button', { name: 'Edit criterion' });
    await expect(editBtn).toBeVisible();
    const { w: ew, h: eh } = await boxSize(editBtn);
    expect(eh, `coarse-44 AC Edit height ${eh}px < 44px`).toBeGreaterThanOrEqual(44);
    expect(ew, `coarse-44 AC Edit width ${ew}px < 44px`).toBeGreaterThanOrEqual(44);

    const delBtn = drawer.getByRole('button', { name: /Delete criterion/ });
    await expect(delBtn).toBeVisible();
    const { w: dw, h: dh } = await boxSize(delBtn);
    expect(dh, `coarse-44 AC Delete height ${dh}px < 44px`).toBeGreaterThanOrEqual(44);
    expect(dw, `coarse-44 AC Delete width ${dw}px < 44px`).toBeGreaterThanOrEqual(44);

    const removeRel = drawer.locator('button[aria-label="Remove relation"]');
    await expect(removeRel).toBeVisible();
    const { w: rw, h: rh } = await boxSize(removeRel);
    expect(rh, `coarse-44 Remove relation height ${rh}px < 44px`).toBeGreaterThanOrEqual(44);
    expect(rw, `coarse-44 Remove relation width ${rw}px < 44px`).toBeGreaterThanOrEqual(44);

    await context.close();
  });

  test('fine-pointer desktop at 1280x720: named compact controls stay compact with a 24px floor (no layout shift)', async ({ browser }) => {
    const { projectId } = await seed();
    const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
    const page = await context.newPage();

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projectId);

    // Without a coarse pointer the .coarse-44 block must NOT apply: the card
    // trigger keeps its compact icon size (well under 44) instead of ballooning.
    const menuToggle = page.locator('[data-testid="card-menu-toggle"]').first();
    await expect(menuToggle).toBeVisible();
    const { w: mw, h: mh } = await boxSize(menuToggle);
    expect(mh, `card menu toggle grew to ${mh}px on fine pointer`).toBeLessThan(44);
    expect(mw, `card menu toggle grew to ${mw}px on fine pointer`).toBeLessThan(44);

    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    await todoCol.getByRole('heading', { name: TASK_TITLE }).click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: TASK_TITLE })).toBeVisible();

    // AC Edit/Delete and Remove relation keep their compact look, but no longer
    // render as ~20px icon-only boxes: 24x24 is the WCAG 2.5.8 minimum size.
    for (const [name, btn] of [
      ['AC Edit', drawer.getByRole('button', { name: 'Edit criterion' })],
      ['AC Delete', drawer.getByRole('button', { name: /Delete criterion/ })],
      ['Remove relation', drawer.locator('button[aria-label="Remove relation"]')],
    ] as const) {
      await expect(btn).toBeVisible();
      const { w, h } = await boxSize(btn);
      expect(h, `${name} height ${h}px below the 24px minimum`).toBeGreaterThanOrEqual(24);
      expect(w, `${name} width ${w}px below the 24px minimum`).toBeGreaterThanOrEqual(24);
      expect(h, `${name} lost its compact size on fine pointer (${h}px)`).toBeLessThan(44);
    }

    await context.close();
  });

  test('prefers-reduced-motion: SSE halo stops (solid dot stays) and the claim pulse freezes', async ({ browser }) => {
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: `Reduced Motion ${Date.now()}`, actor: ACTOR }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ project_id: project.id, title: 'Reduced Motion Claim Task', priority: 2, actor: ACTOR }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    // Default 15-minute lease → ACTIVE throughout the test.
    const claimRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: ACTOR }),
    });
    expect(claimRes.ok).toBe(true);

    /** Wait until the live SSE stream reports connected (real server, no mocks). */
    const waitSseConnected = (page: import('@playwright/test').Page) =>
      page.waitForFunction(
        () => {
          const app = (window as any).Alpine?.$data(document.querySelector('[x-data]'));
          return app?.sseStatus === 'connected';
        },
        { timeout: 15000 },
      );

    // --- SSE halo: the status pill is rendered in the mobile slide-down panel
    // (removed from the header at desktop widths), so this part runs narrow.
    const mobile = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true, reducedMotion: 'reduce' });
    const mpage = await mobile.newPage();
    await mpage.goto(serverUrl);
    await expect(mpage).toHaveTitle('Rävn');
    await selectProjectViaDropdown(mpage, project.id);
    await waitSseConnected(mpage);

    await mpage.getByRole('button', { name: 'Board menu' }).click();
    const panel = mpage.getByTestId('mobile-menu-panel');
    await expect(panel).toBeVisible();

    // SSE connected → the halo span exists in the DOM. Under reduced motion it
    // must compute animation:none + opacity:0 (hidden), leaving the solid dot.
    const sseButton = panel.getByTestId('mobile-sse-status');
    const halo = sseButton.locator('span.animate-ping');
    await expect(halo).toBeVisible();
    const haloAnim = await halo.evaluate((n) => getComputedStyle(n).animationName);
    expect(haloAnim, 'animate-ping halo must not run under reduced motion').toBe('none');
    const haloOpacity = await halo.evaluate((n) => getComputedStyle(n).opacity);
    expect(haloOpacity, 'halo copy must be fully hidden under reduced motion').toBe('0');

    // The solid status dot (sibling of the halo inside the h-2 w-2 wrapper)
    // stays visible and colored — the static "connected" indicator.
    const dot = sseButton.locator('span.relative.inline-flex.h-2');
    await expect(dot).toBeVisible();
    const dotColor = await dot.evaluate((n) => getComputedStyle(n).backgroundColor);
    expect(dotColor, 'solid dot must keep its status color').not.toBe('rgba(0, 0, 0, 0)');

    // Control sanity: the SAME element in a default-motion context must be
    // animating, proving the assertion is not passing vacuously.
    const live = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const lpage = await live.newPage();
    await lpage.goto(serverUrl);
    await expect(lpage).toHaveTitle('Rävn');
    await selectProjectViaDropdown(lpage, project.id);
    await waitSseConnected(lpage);
    await lpage.getByRole('button', { name: 'Board menu' }).click();
    const liveHalo = lpage.getByTestId('mobile-menu-panel').getByTestId('mobile-sse-status').locator('span.animate-ping');
    await expect(liveHalo).toBeVisible();
    const liveAnim = await liveHalo.evaluate((n) => getComputedStyle(n).animationName);
    expect(liveAnim, 'halo must actually animate without reduced motion').not.toBe('none');

    // --- Claim badge: the Claim Controls group lives in the detail drawer, so
    // this part runs at a desktop width where the aside drawer is available.
    const desktop = await browser.newContext({ viewport: { width: 1280, height: 720 }, reducedMotion: 'reduce' });
    const dpage = await desktop.newPage();
    await dpage.goto(serverUrl);
    await expect(dpage).toHaveTitle('Rävn');
    await selectProjectViaDropdown(dpage, project.id);

    await dpage
      .locator('section', { has: dpage.getByRole('heading', { name: 'Doing' }) })
      .getByRole('heading', { name: 'Reduced Motion Claim Task' })
      .click();
    const claimControls = dpage.locator('[role="group"][aria-label="Claim Controls"]');
    const pulseIcon = claimControls.locator('svg.animate-pulse').first();
    await expect(pulseIcon).toBeVisible();
    // The Alpine :class binding still adds animate-pulse, but the reduced-motion
    // override must stop the animation while keeping the badge visible.
    const pulseAnim = await pulseIcon.evaluate((n) => getComputedStyle(n).animationName);
    expect(pulseAnim, 'animate-pulse badge must not run under reduced motion').toBe('none');
    const pulseOpacity = await pulseIcon.evaluate((n) => getComputedStyle(n).opacity);
    expect(pulseOpacity, 'pulsing badge must stay visible when the pulse stops').not.toBe('0');

    await mobile.close();
    await live.close();
    await desktop.close();
  });
});
