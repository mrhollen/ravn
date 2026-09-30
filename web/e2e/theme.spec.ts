import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import { randomUUID } from 'crypto';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// E2E coverage for the dark-theme task (AC1 toggle in header, AC2 high-contrast
// dark surfaces on board/cards/modals/drawer, AC3 localStorage persistence).
// Mirrors the ephemeral-server scaffolding of board.spec.ts.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

// zinc palette reference values (Tailwind v4 default theme) as sRGB triples.
const ZINC_950: readonly [number, number, number] = [9, 9, 11]; // dark body background
const ZINC_900: readonly [number, number, number] = [24, 24, 27]; // dark header / drawer / modal surface
const ZINC_800: readonly [number, number, number] = [39, 39, 42]; // dark card / elevated surface
const ZINC_100: readonly [number, number, number] = [244, 244, 245]; // light body background

/** Self-contained (serialized into the page): computed bg-color → sRGB string.
 *  Canvas round-trip normalizes whatever serialization Chrome uses (rgb/oklch). */
const computedBgRgb = (el: Element): string => {
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
};

/** true when a serialized rgb string is within ±2 of the expected sRGB triple. */
function nearRgb(received: string, expected: readonly [number, number, number]): boolean {
  const m = received.match(/rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)/);
  if (!m) return false;
  const got = [Number(m[1]), Number(m[2]), Number(m[3])];
  return expected.every((v, i) => Math.abs(v - got[i]) <= 2);
}

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

/** Seed a project + one task via the API and return the project id.
 *  The server enforces unique project names, so use a unique name per call. */
async function seedBoard(serverUrl: string): Promise<string> {
  const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: `Theme E2E ${randomUUID()}`,
      description: 'Seeded for dark-theme e2e tests',
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(projRes.ok).toBe(true);
  const project = await projRes.json();
  expect(project.id).toBeTruthy();

  const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      project_id: project.id,
      title: 'Theme Card Task',
      description: 'Used to verify dark card rendering',
      priority: 2,
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(taskRes.ok).toBe(true);
  return project.id;
}

function elementBg(page: import('@playwright/test').Page, selector: string): Promise<string> {
  return page.evaluate((args) => {
    const f = new Function(`return (${args.src})`)() as (el: Element) => string;
    const el = document.querySelector(args.sel);
    if (!el) throw new Error(`no element matching ${args.sel}`);
    return f(el);
  }, { src: computedBgRgb.toString(), sel: selector });
}

async function pageBg(page: import('@playwright/test').Page): Promise<string> {
  return page.evaluate((src) => {
    const f = new Function(`return (${src})`)() as (el: Element) => string;
    return f(document.body);
  }, computedBgRgb.toString());
}

function locatorBg(locator: import('@playwright/test').Locator): Promise<string> {
  return locator.evaluate((el, src) => {
    const f = new Function(`return (${src})`)() as (e: Element) => string;
    return f(el);
  }, computedBgRgb.toString());
}

function hasDarkClass(page: import('@playwright/test').Page) {
  return page.evaluate(() => document.documentElement.classList.contains('dark'));
}

async function lsTheme(page: import('@playwright/test').Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('crowban.theme'));
}

test.describe('theme (dark mode)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-theme-e2e-'));
    const serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: path.join(tempDir, 'crowban.db'),
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '',
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

  // ------------------------------------------------------------------ AC1: toggle
  test('header exposes Light / Dark / System theme toggle (AC1)', async ({ page }) => {
    await page.goto(serverUrl);
    const group = page.locator('[data-testid="theme-toggle"]');
    await expect(group).toBeVisible();

    for (const [testid, label] of [
      ['theme-light', 'Light'],
      ['theme-dark', 'Dark'],
      ['theme-system', 'System'],
    ] as const) {
      const btn = page.locator(`[data-testid="${testid}"]`);
      await expect(btn).toBeVisible();
      await expect(btn).toHaveText(label);
    }

    // Default mode is System → its button is the only pressed one.
    await expect(page.locator('[data-testid="theme-system"]')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('[data-testid="theme-light"]')).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('[data-testid="theme-dark"]')).toHaveAttribute('aria-pressed', 'false');

    // Clicking updates the pressed state immediately.
    await page.locator('[data-testid="theme-dark"]').click();
    await expect(page.locator('[data-testid="theme-dark"]')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('[data-testid="theme-system"]')).toHaveAttribute('aria-pressed', 'false');
    // And the dark class is applied to <html> right away.
    expect(await hasDarkClass(page)).toBe(true);
  });

  // ------------------------------------------------- AC3: persistence (system)
  test.describe('system default under OS dark preference', () => {
    test.use({ colorScheme: 'dark' });

    test('renders the dark theme with no stored choice (AC3)', async ({ page }) => {
      await page.goto(serverUrl);
      expect(await lsTheme(page)).toBeNull(); // nothing stored yet → system mode
      expect(await hasDarkClass(page)).toBe(true);
      expect(nearRgb(await pageBg(page), ZINC_950)).toBe(true);
    });

    test('system mode tracks live OS preference changes', async ({ page }) => {
      await seedBoard(serverUrl);
      await page.goto(serverUrl);
      expect(await hasDarkClass(page)).toBe(true); // started in system + dark OS

      await page.emulateMedia({ colorScheme: 'light' });
      // CDP media emulation applies asynchronously — poll the live re-apply.
      await expect.poll(() => hasDarkClass(page), { timeout: 3000 }).toBe(false);

      await page.emulateMedia({ colorScheme: 'dark' });
      await expect.poll(() => hasDarkClass(page), { timeout: 3000 }).toBe(true);
    });
  });

  test('OS light preference renders the light theme with no stored choice', async ({
    page,
  }) => {
    await page.goto(serverUrl);
    expect(await lsTheme(page)).toBeNull();
    expect(await hasDarkClass(page)).toBe(false);
    expect(nearRgb(await pageBg(page), ZINC_100)).toBe(true);
  });

  // ----------------------------------------------------- AC3: persistence (pick)
  test.describe('explicit Light under OS dark preference', () => {
    test.use({ colorScheme: 'dark' });

    test('overrides the OS preference and survives a reload (AC3)', async ({ page }) => {
      await seedBoard(serverUrl);
      await page.goto(serverUrl);
      expect(await hasDarkClass(page)).toBe(true); // system default under dark OS

      await page.locator('[data-testid="theme-light"]').click();
      expect(await hasDarkClass(page)).toBe(false);
      expect(nearRgb(await pageBg(page), ZINC_100)).toBe(true);
      expect(await lsTheme(page)).toBe('light');

      await page.reload();
      // OS still prefers dark, but the stored explicit choice wins.
      expect(await hasDarkClass(page)).toBe(false);
      expect(nearRgb(await pageBg(page), ZINC_100)).toBe(true);
      expect(await lsTheme(page)).toBe('light');
    });
  });

  test('explicit Dark survives a reload and renders high-contrast surfaces (AC2 + AC3)', async ({
    page,
  }) => {
    await seedBoard(serverUrl);
    await page.goto(serverUrl);
    expect(await hasDarkClass(page)).toBe(false); // system default under light OS

    await page.locator('[data-testid="theme-dark"]').click();
    expect(await lsTheme(page)).toBe('dark');
    expect(await hasDarkClass(page)).toBe(true);

    await page.reload();
    expect(await hasDarkClass(page)).toBe(true);
    expect(await lsTheme(page)).toBe('dark');

    // Body + header use the dark surface palette (high contrast vs. light mode).
    expect(nearRgb(await pageBg(page), ZINC_950)).toBe(true);
    const headerBg = await elementBg(page, 'header');
    expect(nearRgb(headerBg, ZINC_900)).toBe(true);

    // Board cards render on the elevated dark surface.
    const card = page.locator('article[draggable="true"]').first();
    await expect(card).toBeVisible();
    expect(nearRgb(await locatorBg(card), ZINC_800)).toBe(true);

    // Detail drawer shell is dark and legible.
    await card.click();
    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
    expect(nearRgb(await locatorBg(drawer), ZINC_900)).toBe(true);

    // "Start Task in OpenCode" button stays readable on the dark drawer.
    const opencodeBtn = page.locator('[data-testid="start-opencode-btn"]');
    await expect(opencodeBtn).toBeVisible();
  });

  // ------------------------------------------------------------ AC2: modals
  test('new-task modal renders with a dark, high-contrast surface (AC2)', async ({ page }) => {
    await seedBoard(serverUrl);
    await page.goto(serverUrl);
    await page.locator('[data-testid="theme-dark"]').click();

    await page.getByRole('button', { name: '+ New task' }).click();
    const panel = page
      .locator('div.mx-auto')
      .filter({ has: page.getByRole('heading', { name: /New task/ }) });
    await expect(panel).toBeVisible();
    expect(nearRgb(await locatorBg(panel), ZINC_900)).toBe(true);

    // Overlay scrim stays dark; form field uses the recessed dark input surface.
    const field = panel.locator('input').first();
    expect(nearRgb(await locatorBg(field), ZINC_950)).toBe(true);
  });
});
