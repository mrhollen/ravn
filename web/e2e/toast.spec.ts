import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// E2E coverage for the toast overhaul: dismiss button (AC1), slide+fade
// enter/leave transitions (AC2), and contextual kind icons (AC3). Mirrors the
// ephemeral-server scaffolding of task-id-copy.spec.ts — no data seeding is
// needed because toasts are driven straight through the Alpine app instance.

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

/** Load the app and wait until the root Alpine instance is initialized. */
async function gotoApp(page: import('@playwright/test').Page, serverUrl: string): Promise<void> {
  await page.goto(serverUrl);
  await page.waitForFunction(() => {
    const win = window as any;
    const root = document.querySelector('[x-data="app()"]');
    return !!(root && win.Alpine && win.Alpine.$data(root));
  });
}

test.describe('toast notifications', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-toast-e2e-'));
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

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
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

  test('AC1+AC3: each toast shows its kind icon and dismisses via the × button', async ({ page }) => {
    await gotoApp(page, serverUrl);

    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.toast('e2e info toast check');
      app.toast('e2e error toast check', 'error');
    });

    const stack = page.locator('[data-testid="toast-stack"]');
    const infoToast = stack.getByRole('status').filter({ hasText: 'e2e info toast check' });
    const errorToast = stack.getByRole('alert').filter({ hasText: 'e2e error toast check' });
    await expect(infoToast).toBeVisible();
    await expect(errorToast).toBeVisible();

    // AC3 — exactly one inline SVG is visible per toast: the icon of its kind
    // (the other kind's svg stays x-show hidden). DOM order: [error svg, info svg].
    const infoIcons = await infoToast.evaluate((el) =>
      [...el.querySelectorAll('svg')].map((s) => getComputedStyle(s).display !== 'none'),
    );
    expect(infoIcons).toEqual([false, true], 'info toast shows only the check icon');
    const errorIcons = await errorToast.evaluate((el) =>
      [...el.querySelectorAll('svg')].map((s) => getComputedStyle(s).display !== 'none'),
    );
    expect(errorIcons).toEqual([true, false], 'error toast shows only the warning icon');

    // AC1 — the × button removes its own toast (after the leave transition),
    // leaving siblings untouched.
    await infoToast.getByRole('button', { name: 'Dismiss notification' }).click();
    await expect(infoToast).not.toBeAttached({ timeout: 3000 });
    await expect(errorToast).toBeVisible();

    await errorToast.getByRole('button', { name: 'Dismiss notification' }).click();
    await expect(errorToast).not.toBeAttached({ timeout: 3000 });
    await expect(stack.locator(':scope > div')).toHaveCount(0, { timeout: 3000 });

    // Leave a fresh pair on screen so the test-finished screenshot is QA
    // evidence for icons + dismiss button (same convention as modal.spec).
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.toast('Info toast with check icon');
      app.toast('Error toast with warning icon', 'error');
    });
    await expect(stack.locator(':scope > div')).toHaveCount(2, { timeout: 3000 });
  });

  test('AC2: a dismissing toast slides + fades through intermediate states', async ({ page }) => {
    await gotoApp(page, serverUrl);

    // Drive everything inside one evaluate so the opacity sampling starts in
    // the same tick as the removal click (Node round-trips would blur timing).
    interface TransitionProbe {
      found: boolean;
      hasButton: boolean;
      samples: number[];
      /** Distinct computed motion values seen mid-exit (translate|transform|scale). */
      distinctMotion: string[];
      sawLeaveClasses: boolean;
      detached: boolean;
    }
    const result = await page.evaluate(async (): Promise<TransitionProbe> => {
      const win = window as any;
      const app = win.Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.toast('e2e transition check');
      // Let the ENTER transition fully finish first, so the leave starts from a
      // clean resting state (clicking mid-enter would race Alpine's cancel path).
      await new Promise((r) => setTimeout(r, 300));
      const el = [...document.querySelectorAll('[data-testid="toast-stack"] > *')].find(
        (n) => n instanceof HTMLElement && (n.textContent || '').includes('e2e transition check'),
      ) as HTMLElement | undefined;
      if (!el) return { found: false, hasButton: false, samples: [], distinctMotion: [], sawLeaveClasses: false, detached: false };
      const btn = el.querySelector('button');
      if (!btn) return { found: true, hasButton: false, samples: [], distinctMotion: [], sawLeaveClasses: false, detached: false };
      btn.click(); // start the leave transition
      const samples: number[] = [];
      const motionValues = new Set<string>();
      let sawLeaveClasses = false;
      const end = performance.now() + 400;
      while (performance.now() < end) {
        if (!el.isConnected) break;
        const s = getComputedStyle(el);
        samples.push(parseFloat(s.opacity));
        motionValues.add(`${s.translate}|${s.transform}|${s.scale}`);
        if (el.className.includes('-translate-y-2') || el.className.includes('scale-95')) {
          sawLeaveClasses = true;
        }
        await new Promise((r) => setTimeout(r, 16));
      }
      return { found: true, hasButton: true, samples, distinctMotion: [...motionValues], sawLeaveClasses, detached: !el.isConnected };
    });

    expect(result.found).toBe(true);
    expect(result.hasButton).toBe(true, 'toast must render its × dismiss button');
    expect(result.detached).toBe(true, 'toast must leave the DOM after its exit transition');
    // Fade — opacity passes strictly between 0 and 1 while exiting.
    expect(result.samples.length).toBeGreaterThan(1);
    expect(result.samples.some((o) => o > 0.02 && o < 0.98)).toBe(
      true,
      `opacity must animate through intermediate values (samples: ${result.samples.join(', ')})`,
    );
    // Slide — the leave classes are applied AND a motion property actually
    // takes on multiple distinct computed values mid-exit (translate 0px → -8px).
    expect(result.sawLeaveClasses).toBe(true, 'leave-end slide classes must be applied while exiting');
    expect(result.distinctMotion.length).toBeGreaterThan(1,
      `motion properties must animate through intermediate values (observed: ${result.distinctMotion.join(' / ')})`);
  });

  test('AC1-AC3: aria live regions for toast container, individual toasts, SSE indicator, and duplicate advisory', async ({
    page,
  }) => {
    await gotoApp(page, serverUrl);

    // 1. Verify toast stack container live region attributes (AC1)
    const stack = page.locator('[data-testid="toast-stack"]');
    await expect(stack).toHaveAttribute('role', 'status');
    await expect(stack).toHaveAttribute('aria-live', 'polite');

    // Trigger info and error toasts to verify individual role and aria-live attributes
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.toast('Test info announcement', 'info');
      app.toast('Test error announcement', 'error');
    });

    const infoToast = stack.locator(':scope > div').filter({ hasText: 'Test info announcement' });
    const errorToast = stack.locator(':scope > div').filter({ hasText: 'Test error announcement' });
    await expect(infoToast).toHaveAttribute('role', 'status');
    await expect(infoToast).toHaveAttribute('aria-live', 'polite');
    await expect(errorToast).toHaveAttribute('role', 'alert');
    await expect(errorToast).toHaveAttribute('aria-live', 'assertive');

    // 2. Verify SSE connection indicator live region attributes (AC2)
    const liveIndicator = page.locator('[data-testid="live-indicator"]');
    await expect(liveIndicator).toBeVisible();
    await expect(liveIndicator).toHaveAttribute('role', 'status');
    await expect(liveIndicator).toHaveAttribute('aria-live', 'polite');
    await expect(liveIndicator).toHaveAttribute('aria-atomic', 'true');

    // 3. Verify duplicate task advisory live region attributes (AC3)
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.duplicates = [{ id: '01TESTDUP000000000000000000', title: 'Duplicate test task' }];
    });
    const duplicateBanner = page.locator('main').getByRole('alert').filter({ hasText: 'similar tasks already exist' });
    await expect(duplicateBanner).toBeVisible();
    await expect(duplicateBanner).toHaveAttribute('aria-live', 'polite');
    await expect(duplicateBanner).toHaveAttribute('aria-atomic', 'true');
  });
});
