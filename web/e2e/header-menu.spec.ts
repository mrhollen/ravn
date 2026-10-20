// E2E coverage for the consolidated header settings menu
// (task 01M47NPF5YK99MVGD8K4ZRS81R): theme selector, actor identity
// ("Who are you?"), auto-refresh and the auth-token entry live in ONE compact
// dropdown anchored at the right edge of the header. The SSE liveness indicator
// stays OUT of it (immediately left of the trigger on md+, click-to-reconnect
// preserved).
//   AC1 — all four controls reachable from the single menu; old inline widgets
//         REMOVED from the strip; compact unambiguous avatar-circle trigger
//         (deliberately NOT a gear glyph)
//   AC2 — live indicator positioned immediately left of the trigger + behavior
//   AC3 — light AND dark theme rendering; 375px trigger reachable with no
//         horizontal overflow (asserted: visible in the compact header row)
//   AC4 — behavior parity: theme persists via setTheme, polling flips, actor
//         saves on commit, token item opens the existing auth modal with a
//         set/none label; keyboard contract matching the Projects dropdown
// Self-contained: ephemeral ravn-server on a random high port + temp DB,
// serving the pre-built web/dist (mirrors project-dropdown.spec.ts).
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

function hasDarkClass(page: import('@playwright/test').Page) {
  return page.evaluate(() => document.documentElement.classList.contains('dark'));
}

async function lsTheme(page: import('@playwright/test').Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('ravn.theme') ?? localStorage.getItem('ravn_theme') ?? localStorage.getItem('crowban.theme'));
}

async function lsActor(page: import('@playwright/test').Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('ravn.actor') ?? localStorage.getItem('crowban.actor'));
}

test.describe('Consolidated header settings menu', () => {
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
      const serverPkg = fs.existsSync(path.join(repoRoot, 'cmd', 'ravn-server'))
        ? './cmd/ravn-server'
        : './cmd/crowban-server';
      execSync(`go build -trimpath -o bin/ravn-server ${serverPkg}`, {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-header-menu-'));
    const dbPath = path.join(tempDir, 'ravn.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
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

    // One project so the app auto-selects it on load (no new-project bar).
    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Header Menu Project',
      description: 'Seeded for the header settings menu e2e spec',
      actor: { name: 'e2e-hm-seed', type: 'agent' },
    });
    projectId = proj.id;
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Header Menu Seed Task',
      priority: 2,
      actor: { name: 'e2e-hm-seed', type: 'agent' },
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
  });

  test.beforeEach(async () => {
    uncaughtConsoleErrors = [];
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Goto the board and wait for app boot (the dropdown trigger enables when
   *  init() completes — same convention as the Projects dropdown spec). */
  async function freshPage(page: import('@playwright/test').Page) {
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
    await expect(page.getByTestId('project-select')).toBeEnabled();
  }

  test('AC1: avatar-circle trigger at the right edge; old inline widgets removed from the strip', async ({
    page,
  }, testInfo) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();

    // Compact and unambiguous: an avatar circle with the actor's initial
    // ("You" when unset) — deliberately NOT a gear/cog glyph (no svg inside).
    await expect(trigger.locator('svg')).toHaveCount(0);
    await expect(trigger).toContainText('You');
    await expect(trigger).toHaveAttribute('aria-haspopup', 'true');
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    // AC1 core: the old inline widgets are REMOVED from the strip — while the
    // menu is closed, none of them is visible anywhere on the page.
    for (const testid of ['theme-toggle', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await expect(page.getByTestId(testid)).not.toBeVisible();
    }

    // AC2: the liveness indicator stays OUT of the menu, positioned immediately
    // to the LEFT of the trigger on this desktop width…
    const live = page.locator('[data-testid="live-indicator"]');
    await expect(live).toBeVisible();
    const panelBox = await page.getByTestId('header-menu').boundingBox();
    expect(panelBox, 'panel is closed (x-show hidden) at this point').toBeNull();
    const lbox = (await live.boundingBox())!;
    const tbox = (await trigger.boundingBox())!;
    expect(lbox.x + lbox.width, 'live indicator ends at or left of the trigger').toBeLessThanOrEqual(tbox.x);
    expect(tbox.x - (lbox.x + lbox.width), '…and is immediately adjacent to it').toBeLessThanOrEqual(16);
    // …on the same header row (vertically centered with the trigger).
    expect(Math.abs(lbox.y + lbox.height / 2 - (tbox.y + tbox.height / 2))).toBeLessThanOrEqual(12);

    // Close-up evidence: indicator left of trigger, clipped to the header band.
    const hbox = (await page.locator('header').first().boundingBox())!;
    const clipX = Math.max(0, lbox.x - 12);
    await page.screenshot({
      path: path.join(testInfo.outputDir, 'live-indicator-left-of-trigger.png'),
      clip: { x: clipX, y: hbox.y + 2, width: tbox.x + tbox.width + 10 - clipX, height: hbox.height - 4 },
    });
  });

  test('AC1: menu opens and groups all four controls (light theme — screenshot evidence)', async ({
    page,
  }) => {
    await freshPage(page);

    const trigger = page.getByTestId('header-menu-trigger');
    await trigger.click();
    const panel = page.getByTestId('header-menu');
    await expect(panel).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');

    // Grouped sections with small headings — a unified menu, not a grab-bag.
    for (const name of ['Appearance', 'Identity', 'Data']) {
      await expect(panel.getByRole('heading', { name, level: 3 })).toBeVisible();
    }

    // All four controls reachable from the single menu…
    const group = page.locator('[data-testid="theme-toggle"]');
    await expect(group).toBeVisible();
    await expect(page.locator('[data-testid="theme-light"]')).toBeVisible();
    await expect(page.locator('[data-testid="theme-dark"]')).toBeVisible();
    // Default mode is System → its button is the only pressed one.
    await expect(page.locator('[data-testid="theme-system"]')).toHaveAttribute('aria-pressed', 'true');

    const actorInput = page.getByPlaceholder('you');
    await expect(actorInput).toBeVisible();
    await expect(actorInput).toHaveValue('');

    // Auto-refresh reflects the polling state (default on)…
    const toggle = page.getByTestId('auto-refresh-toggle');
    await expect(toggle).toBeChecked();
    const appState = await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      return { polling: app.polling, pollTimer: app.pollTimer };
    });
    expect(appState.polling).toBe(true);

    // …and the token entry opens from here, labelled by set/unset state.
    await expect(page.getByTestId('token-item')).toContainText('Token: none');

    // The menu is intentionally left open so the auto-captured test screenshot
    // is the light-theme evidence shot for this AC.
  });

  test('AC4: keyboard — Enter/Arrows/Home/End navigate, Escape returns focus to the trigger', async ({
    page,
  }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    /** What's focused inside the menu panel (null when focus is outside it). */
    const focusInfo = () =>
      page.evaluate(() => {
        const p = document.querySelector('[data-testid="header-menu"]');
        const el = document.activeElement;
        if (!p || !(el instanceof Element) || !p.contains(el)) return null;
        return el.getAttribute('data-testid') ?? el.tagName;
      });

    // Enter on the focused trigger opens it (native button semantics); focus
    // lands on the first control (the Light theme button).
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(panel).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');

    // Arrows walk the six controls in DOM order — section by section: theme
    // buttons, actor input (Identity), auto-refresh + token (Data)…
    for (const next of ['theme-dark', 'theme-system', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await page.keyboard.press('ArrowDown');
      await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe(next);
    }

    // …Home/End jump to the ends.
    await page.keyboard.press('Home');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');
    await page.keyboard.press('End');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('token-item');

    // Escape closes and returns focus to the trigger.
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // ArrowDown from a closed trigger opens on the FIRST control…
    await page.keyboard.press('ArrowDown');
    await expect(panel).toBeVisible();
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // …ArrowUp opens on the LAST control (the token item), same as the project
    // dropdown's contract.
    await page.keyboard.press('ArrowUp');
    await expect(panel).toBeVisible();
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('token-item');
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // Outside click closes WITHOUT restoring focus (matches the project menu):
    // a click on neutral header chrome dismisses the panel. The wordmark is now
    // a mascot <img> with only an sr-only h1, so target the visible logo image
    // (same neutral header chrome the old text wordmark occupied).
    await trigger.click();
    await expect(panel).toBeVisible();
    await page.locator('header img[alt="Rävn — MCP-first kanban · visibility UI"]').click();
    await expect(panel).toBeHidden();
  });

  test('AC4: theme choice made in the menu persists across reload (setTheme path unchanged)', async ({
    page,
  }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    await trigger.click();
    await expect(panel).toBeVisible();
    await page.locator('[data-testid="theme-dark"]').click();
    expect(await hasDarkClass(page)).toBe(true); // applied to <html> right away
    expect(await lsTheme(page)).toBe('dark'); // persisted via setTheme → localStorage

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    await page.reload();
    await expect(page.getByTestId('project-select')).toBeEnabled();
    expect(await lsTheme(page)).toBe('dark'); // survives the reload
    expect(await hasDarkClass(page)).toBe(true);
  });

  test('AC3: menu renders in the dark theme (screenshot evidence)', async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem('ravn.theme', 'dark');
      localStorage.setItem('ravn_theme', 'dark');
      localStorage.setItem('crowban.theme', 'dark');
    });
    await freshPage(page);
    expect(await hasDarkClass(page)).toBe(true); // stored explicit choice wins

    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    await trigger.click();
    await expect(panel).toBeVisible();

    // All four controls render on the dark elevated surface…
    for (const testid of ['theme-toggle', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await expect(page.getByTestId(testid)).toBeVisible();
    }
    // …and the selected theme button shows its dark-mode pressed styling.
    await expect(page.locator('[data-testid="theme-dark"]')).toHaveAttribute('aria-pressed', 'true');

    // Intentionally left open: the auto-captured screenshot is the dark-theme
    // evidence shot for this AC.
  });

  test('AC4: auto-refresh toggle from the menu flips polling on/off', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    const toggle = page.getByTestId('auto-refresh-toggle');

    const appState = () =>
      page.evaluate(() => {
        const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
        return { polling: app.polling, pollTimer: app.pollTimer };
      });

    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(toggle).toBeChecked();
    expect((await appState()).polling).toBe(true); // booted with polling on

    // Uncheck from inside the menu…
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    const off = await appState();
    expect(off.polling).toBe(false, 'x-model flipped the state');
    expect(off.pollTimer).toBe(0, 'pollingChanged() stopped the timer');

    // …and re-check starts it again.
    await toggle.click();
    await expect(toggle).toBeChecked();
    const on = await appState();
    expect(on.polling).toBe(true);
    expect(on.pollTimer).not.toBe(0, 'pollingChanged() (re)started the timer');

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC4: actor name saves on commit and the trigger glyph follows', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    await trigger.click();
    await expect(panel).toBeVisible();
    const actorInput = page.getByPlaceholder('you');
    await actorInput.fill('Ada Lovelace');
    await actorInput.dispatchEvent('change'); // x-model write + saveActor()

    expect(await lsActor(page)).toBe('Ada Lovelace'); // persisted (same LS key as before)
    await expect(trigger).toContainText('A'); // avatar glyph follows the saved name

    // The panel's identity summary shows the full name too.
    await expect(panel.getByText('Ada Lovelace')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC4: token item opens the existing auth modal; its label tracks set/unset', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    const tokenItem = page.getByTestId('token-item');

    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(tokenItem).toContainText('Token: none');

    // Clicking the item closes the menu and opens the EXISTING auth modal…
    await tokenItem.click();
    await expect(page.locator('#cb-auth-modal')).toBeVisible();
    await expect(panel).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    // …configured through the modal's own form (no contract change)…
    await page.locator('#cb-auth-modal input[type="password"]').fill('e2e-secret-token');
    await page.locator('#cb-auth-modal').getByRole('button', { name: 'Save token' }).click();
    await expect(page.locator('#cb-auth-modal')).toBeHidden();

    // …and the menu's label reflects the set state on re-open.
    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(tokenItem).toContainText('Token: set');

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC3: 375px — trigger reachable in row 1, no overflow, menu fits (screenshot evidence)', async ({
    browser,
  }, testInfo) => {
    const context = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
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
    await expect(page.getByTestId('project-select')).toBeEnabled();

    // Chosen mobile mapping: the trigger stays VISIBLE in the compact row-1
    // header (not hidden behind the hamburger) — reachable at 375px with no
    // horizontal overflow…
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();
    const tbox = (await trigger.boundingBox())!;
    expect(tbox.x).toBeGreaterThanOrEqual(0);
    expect(tbox.x + tbox.width).toBeLessThanOrEqual(375);
    // …meets the 44x44px tap-target minimum (mobile CSS min-size rule)…
    expect(tbox.height).toBeGreaterThanOrEqual(44);
    expect(tbox.width).toBeGreaterThanOrEqual(44);
    // …and adds no horizontal overflow to the document.
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

    const header = page.locator('header').first();
    const hbox = (await header.boundingBox())!;
    expect(hbox.height, 'row 1 + padding stays in the two-row budget').toBeLessThanOrEqual(96);
    expect(tbox.y >= hbox.y && tbox.y + tbox.height <= hbox.y + hbox.height).toBe(
      true,
      'trigger sits inside the collapsed header row',
    );

    // Evidence: a 375px shot of the header with the menu trigger.
    await header.screenshot({ path: path.join(testInfo.outputDir, 'header-row-375.png') });

    // The menu opens from row 1 and stays within the viewport; every control
    // is usable from inside it at this width.
    await trigger.click();
    const panel = page.getByTestId('header-menu');
    await expect(panel).toBeVisible();
    const mbox = (await panel.boundingBox())!;
    expect(mbox.x).toBeGreaterThanOrEqual(0);
    expect(mbox.x + mbox.width).toBeLessThanOrEqual(375, 'panel must not overflow the 375px viewport');
    await expect(page.getByPlaceholder('you')).toBeVisible();
    await expect(page.getByTestId('theme-toggle')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await context.close();
  });
});
