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

  /** The theme selector moved into the consolidated header settings menu
   *  (avatar trigger at the right edge of the header) — open it first. */
  async function openHeaderMenu(page: import('@playwright/test').Page): Promise<void> {
    await page.getByTestId('header-menu-trigger').click();
    await expect(page.getByTestId('header-menu')).toBeVisible();
  }

  // ------------------------------------------------------------------ AC1: toggle
  test('header exposes Light / Dark / System theme toggle (AC1)', async ({ page }) => {
    await page.goto(serverUrl);
    // The old inline strip widget is gone: with the menu CLOSED the group and
    // its buttons are hidden…
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();
    await expect(page.locator('[data-testid="theme-toggle"]')).not.toBeVisible();

    // …the toggle now lives inside the consolidated header settings menu.
    await openHeaderMenu(page);
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

      await openHeaderMenu(page);
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

      await openHeaderMenu(page);
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
    await openHeaderMenu(page);
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

  // ------------------------------------------------ WCAG 1.4.3: contrast guard
  // Tickets 01M3MMT8P0FSZK6QSZBTQVCDP4 (board/header) + 01M3MMTBKW2JPMD14JBAYYHN3Z
  // (task detail drawer). Guards secondary text in BOTH themes against the EFFECTIVE
  // background (ancestors' translucent surfaces composited), not just the element's
  // own backgroundColor.

  /** WCAG relative-luminance channel transform. */
  function srgbChannel(v: number): number {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  }

  function luminance(rgb: readonly [number, number, number]): number {
    return (
      0.2126 * srgbChannel(rgb[0]) +
      0.7152 * srgbChannel(rgb[1]) +
      0.0722 * srgbChannel(rgb[2])
    );
  }

  function contrastRatio(fg: readonly [number, number, number], bg: readonly [number, number, number]): number {
    const l1 = luminance(fg);
    const l2 = luminance(bg);
    return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
  }

  function parseRgb(s: string): [number, number, number] {
    const m = s.match(/rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)/);
    if (!m) throw new Error(`unparseable color: ${s}`);
    return [Number(m[1]), Number(m[2]), Number(m[3])];
  }

  /** Self-contained (serialized into the page): returns {fg, bg} as sRGB strings.
   *  fg = the element's computed color; bg = the EFFECTIVE opaque background behind its
   *  text — the ancestor chain's backgroundColors composited translucent-over-opaque,
   *  starting from a white canvas base. Both go through a 1×1-canvas round-trip so any
   *  serialization Chrome emits (rgb/oklch/hex) normalizes to rgb(r, g, b). */
  const sampleFgBg = (el: Element): [string, string] => {
    const toRgb = (cssColor: string): number[] | null => {
      if (cssColor === 'transparent') return null;
      const c = document.createElement('canvas');
      c.width = c.height = 1;
      const ctx = c.getContext('2d')!;
      ctx.clearRect(0, 0, 1, 1);
      try {
        ctx.fillStyle = cssColor;
      } catch {
        return null;
      }
      ctx.fillRect(0, 0, 1, 1);
      const d = ctx.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const fg = toRgb(getComputedStyle(el).color) ?? [0, 0, 0, 1];

    const over = (top: number[], under: number[]): number[] => {
      const a = top[3];
      return [
        Math.round(top[0] * a + under[0] * (1 - a)),
        Math.round(top[1] * a + under[1] * (1 - a)),
        Math.round(top[2] * a + under[2] * (1 - a)),
      ];
    };
    const layers: number[][] = [];
    let node: Element | null = el;
    while (node && node.nodeType === 1) {
      const rgba = toRgb(getComputedStyle(node).backgroundColor);
      if (rgba && !(rgba[3] === 0)) layers.push(rgba);
      node = node.parentElement;
    }
    // Compose outermost ancestor first, innermost (the element itself) last.
    let under: number[] = [255, 255, 255];
    for (let i = layers.length - 1; i >= 0; i--) {
      under = over(layers[i], under);
    }
    return [`rgb(${fg[0]}, ${fg[1]}, ${fg[2]})`, `rgb(${under[0]}, ${under[1]}, ${under[2]})`];
  };

  interface ContrastSample {
    fg: string;
    bg: string;
    ratio: number;
  }

  async function sampleContrast(locator: import('@playwright/test').Locator): Promise<ContrastSample> {
    const [fg, bg] = await locator.first().evaluate((el, src) => {
      const f = new Function(`return (${src})`)() as (e: Element) => [string, string];
      return f(el);
    }, sampleFgBg.toString());
    return { fg, bg, ratio: contrastRatio(parseRgb(fg), parseRgb(bg)) };
  }

  function expectContrast(name: string, s: ContrastSample, min = 4.5): void {
    expect(
      s.ratio,
      `${name}: contrast ${s.ratio.toFixed(2)} (${s.fg} on effective bg ${s.bg}) is below the WCAG 1.4.3 minimum of ${min}`,
    ).toBeGreaterThanOrEqual(min);
  }

  /** Seed + optionally claim a task so both lease states can be exercised. */
  async function seedClaimedTask(serverUrl: string): Promise<{ projectId: string; taskId: string }> {
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: `Contrast E2E ${randomUUID()}`,
        description: 'Seeded for contrast-guard tests',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Contrast Card Task',
        description: 'Body text rendered in the detail drawer',
        priority: 2,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    const claimRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: { name: 'e2e-seed', type: 'agent' } }),
    });
    expect(claimRes.ok).toBe(true);
    return { projectId: project.id, taskId: task.id };
  }

  /** Open the seeded task's detail drawer. */
  async function openDrawer(page: import('@playwright/test').Page): Promise<void> {
    const card = page.locator('article[draggable="true"]').first();
    await expect(card).toBeVisible();
    await card.click();
    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
  }

  /** Force the board onto a specific project. On first boot (no stored
   *  crowban.project) app.ts continueBoot auto-selects projects[0] — which is
   *  NOT necessarily the project these tests just seeded — so every assertion
   *  below must run against an explicitly selected, known board. */
  async function selectProject(
    page: import('@playwright/test').Page,
    projectId: string,
    expectCardTitle: string,
  ): Promise<void> {
    await page.locator('[data-testid="project-select"]').click();
    const option = page.locator(`[data-testid="project-option"][data-project-id="${projectId}"]`);
    await expect(option).toBeVisible();
    await option.click();
    // Board reload is async: wait until the seeded card actually renders.
    await expect(page.locator('article', { hasText: expectCardTitle })).toBeVisible({ timeout: 10_000 });
  }

  test.describe('light theme', () => {
    test.use({ colorScheme: 'light' });

    test('secondary text meets WCAG 1.4.3 on board and drawer (unclaimed)', async ({ page }) => {
      const projectId = await seedBoard(serverUrl);
      await page.goto(serverUrl);
      expect(await hasDarkClass(page)).toBe(false);
      await selectProject(page, projectId, 'Theme Card Task');

      // Board: empty-column note, filter label, dashed add-task button.
      expectContrast('board "No tasks" note', await sampleContrast(page.getByText('No tasks', { exact: true })));
      expectContrast('board "Search" filter label', await sampleContrast(page.getByText('Search', { exact: true })));
      expectContrast(
        'board add-task button',
        await sampleContrast(page.getByRole('button', { name: /^\+ Add task to / }).first()),
      );

      // Drawer (unclaimed): metadata row, section title, empty-state lines, lease note.
      await openDrawer(page);
      expectContrast('drawer metadata row ("updated")', await sampleContrast(page.getByText('updated', { exact: true })));
      expectContrast(
        'drawer section title (Notes)',
        await sampleContrast(page.locator('h3.section-title', { hasText: 'Notes' }).first()),
      );
      expectContrast('drawer "No comments yet."', await sampleContrast(page.getByText('No comments yet.', { exact: true })));
      expectContrast('drawer "No artifacts attached."', await sampleContrast(page.getByText('No artifacts attached.', { exact: true })));
      expectContrast('drawer "No relations"', await sampleContrast(page.getByText('No relations', { exact: true })));
      expectContrast(
        'drawer lease note',
        await sampleContrast(page.getByText(/No active lease/)),
      );

      // Activity log entries (seeded task has creation events).
      const activityLi = page
        .locator('h3.section-title', { hasText: 'Activity' })
        // NOTE: Chromium's document.evaluate (behind Playwright's `xpath=` engine) rejects
        // the `>` child combinator ("The result is not a node set"); use `/` instead.
        .locator('xpath=following-sibling::ul[1]/li')
        .first();
      expectContrast('drawer activity entry', await sampleContrast(activityLi));

      // "Unsaved changes" chip appears once the notes textarea is touched.
      const notes = page.locator('textarea[placeholder^="Last run"]');
      await notes.click();
      await notes.type('run note');
      expectContrast(
        'drawer "Unsaved changes" chip',
        await sampleContrast(page.getByText('Unsaved changes', { exact: true })),
      );
    });

    test('claim box + card hint meet WCAG 1.4.3 (claimed)', async ({ page }) => {
      const { projectId } = await seedClaimedTask(serverUrl);
      await page.goto(serverUrl);
      expect(await hasDarkClass(page)).toBe(false);
      await selectProject(page, projectId, 'Contrast Card Task');

      // Card claim hint: "until HH:MM (…)" next to the claimed-by name.
      expectContrast(
        'card claim hint',
        await sampleContrast(page.locator('article').getByText(/until \d{2}:\d{2}/)),
      );

      // Drawer claim box: "Expires:" row + its hint span (on the translucent indigo surface).
      await openDrawer(page);
      expectContrast(
        'drawer "Expires:" label',
        await sampleContrast(page.getByText('Expires:', { exact: true })),
      );
      expectContrast(
        'drawer claim-expiry hint',
        await sampleContrast(page.locator('aside').getByText(/\(until \d{2}:\d{2}/)),
      );
      // "Held by" line inherits the box's indigo-950 text on the indigo surface.
      expectContrast(
        'drawer "Held by" line',
        await sampleContrast(page.locator('aside').getByText(/Held by/)),
      );
    });
  });

  test.describe('dark theme', () => {
    test.use({ colorScheme: 'light' }); // forced light OS → explicit dark pick below

    test('secondary text meets WCAG 1.4.3 on board and drawer (claimed)', async ({ page }) => {
      const { projectId } = await seedClaimedTask(serverUrl);
      await page.goto(serverUrl);
      expect(await hasDarkClass(page)).toBe(false);
      await selectProject(page, projectId, 'Contrast Card Task');
      await openHeaderMenu(page);
      await page.locator('[data-testid="theme-dark"]').click();
      expect(await hasDarkClass(page)).toBe(true);
      // The board's column sections use `transition-colors` (150ms): sample only after the
      // theme switch animation settles, otherwise translucent surfaces are read mid-fade.
      await page.waitForTimeout(300);

      // Board: empty-column note, filter label.
      expectContrast('board "No tasks" note', await sampleContrast(page.getByText('No tasks', { exact: true })));
      expectContrast('board "Search" filter label', await sampleContrast(page.getByText('Search', { exact: true })));
      // Card claim hint on the dark elevated card surface.
      expectContrast(
        'card claim hint',
        await sampleContrast(page.locator('article').getByText(/until \d{2}:\d{2}/)),
      );

      // Drawer: metadata row, section title, empty-state lines, activity entries.
      await openDrawer(page);
      expectContrast('drawer metadata row ("updated")', await sampleContrast(page.getByText('updated', { exact: true })));
      expectContrast(
        'drawer section title (Notes)',
        await sampleContrast(page.locator('h3.section-title', { hasText: 'Notes' }).first()),
      );
      expectContrast('drawer "No comments yet."', await sampleContrast(page.getByText('No comments yet.', { exact: true })));
      expectContrast('drawer "No artifacts attached."', await sampleContrast(page.getByText('No artifacts attached.', { exact: true })));
      expectContrast('drawer "No relations"', await sampleContrast(page.getByText('No relations', { exact: true })));

      const activityLi = page
        .locator('h3.section-title', { hasText: 'Activity' })
        // NOTE: Chromium's document.evaluate (behind Playwright's `xpath=` engine) rejects
        // the `>` child combinator ("The result is not a node set"); use `/` instead.
        .locator('xpath=following-sibling::ul[1]/li')
        .first();
      expectContrast('drawer activity entry', await sampleContrast(activityLi));

      // Claim box on the translucent dark indigo surface.
      expectContrast(
        'drawer "Expires:" label',
        await sampleContrast(page.getByText('Expires:', { exact: true })),
      );
      expectContrast(
        'drawer claim-expiry hint',
        await sampleContrast(page.locator('aside').getByText(/\(until \d{2}:\d{2}/)),
      );

      // "Unsaved changes" chip (amber) on the dark drawer.
      const notes = page.locator('textarea[placeholder^="Last run"]');
      await notes.click();
      await notes.type('run note');
      expectContrast(
        'drawer "Unsaved changes" chip',
        await sampleContrast(page.getByText('Unsaved changes', { exact: true })),
      );
    });
  });
});
