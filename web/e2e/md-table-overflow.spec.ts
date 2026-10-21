import { test, expect } from '@playwright/test';
import { selectProjectViaDropdown } from './helpers';
import { spawn, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// E2E coverage for the wide-GFM-table overflow fix:
//   AC1 — rendered markdown tables are wrapped in an overflow-x-auto container
//   AC2 — wide tables scroll internally, no drawer/document horizontal blow-out
//   AC3 — long headers + multiline cells render correctly in light AND dark modes
// Mirrors the ephemeral-server scaffolding of m4.spec.ts (own server on a
// random high port, temp DB, serves web/dist).

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');
const testResultsDir = path.resolve(__dirname, '../test-results');

// One run of 300 identical characters: no spaces, so the browser cannot
// soft-wrap it — this guarantees the table's min-content width far exceeds
// the drawer and the wrapper must expose an internal horizontal scrollbar.
const UNBREAKABLE = 'a'.repeat(300);

// WIDE GFM table: 6 columns (satisfies "4+ columns"), long headers, one cell
// with a long unbreakable token (forces internal hscroll) and one multiline
// <br> cell (AC3 "multiline text" requirement).
const WIDE_TABLE_MD = [
  '| Alpha Header | Beta Header | Gamma Header Very Long UnbreakableTokenAlpha1234567890 | Delta Header | Epsilon Multiline | Zeta Final Column That Is Also Quite Long |',
  '|---|---|---|---|---|---|',
  `| one | two | aaaa${UNBREAKABLE}bbbb | four | line one<br>line two<br>line three | six |`,
].join('\n');

const TASK_TITLE = 'Wide Table Overflow Task';

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
      // Retry
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

/** Seed a project + the one wide-table task via the API; return the project id. */
async function seedWideTableProject(serverUrl: string): Promise<string> {
  const pRes = await fetch(`${serverUrl}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: `MD Table Overflow E2E ${Date.now()}`,
      actor: { name: 'test', type: 'human' },
    }),
  });
  expect(pRes.ok).toBe(true);
  const proj = await pRes.json();

  const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      project_id: proj.id,
      title: TASK_TITLE,
      description: WIDE_TABLE_MD,
      priority: 2,
      actor: { name: 'test', type: 'human' },
    }),
  });
  expect(taskRes.ok).toBe(true);
  return proj.id;
}

test.describe('wide GFM markdown tables scroll within themselves', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-mdtable-'));
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

    await waitForServer(serverUrl);
    projectId = await seedWideTableProject(serverUrl);
    // Evidence directory for the named AC3 screenshots (screenshot:'on' also
    // auto-writes here, but this guarantees it exists before the first shot).
    fs.mkdirSync(testResultsDir, { recursive: true });
  });

  test.afterAll(async () => {
    if (serverProcess && !serverProcess.killed) {
      serverProcess.kill('SIGTERM');
    }
    if (tempDir && fs.existsSync(tempDir)) {
      try {
        fs.rmSync(tempDir, { recursive: true, force: true });
      } catch {
        /* ignore */
      }
    }
  });

  /** Navigate to the board, open the seeded task's drawer and return strict
   *  locators for the wrapper + rendered table. Scopes the wrapper locator to
   *  [data-testid="task-description"] (the displayed server description): the
   *  hidden edit-form draft preview is also a .md-body in this drawer, so bare
   *  page-level class matching can resolve the wrong node. */
  async function openTaskDrawer(page: import('@playwright/test').Page) {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const card = page.locator('article', { hasText: TASK_TITLE });
    await expect(card).toBeVisible();
    await card.click();

    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
    const mdBody = drawer.getByTestId('task-description');
    await expect(mdBody).toBeVisible();
    const wrap = mdBody.locator('div.md-table-wrap').first();
    await expect(wrap).toBeVisible();
    return { drawer, mdBody, wrap };
  }

  /** AC2 core assertion: NO document-level horizontal scrollbar from the wide
   *  table (allow a 1px rounding tolerance). */
  async function assertNoPageHScroll(page: import('@playwright/test').Page): Promise<void> {
    const docSw = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(
      docSw,
      `document scroll width ${docSw} exceeds viewport ${page.viewportSize()!.width} — wide table blew out the page`,
    ).toBeLessThanOrEqual(page.viewportSize()!.width + 1);
  }

  // ----------------------------------------------------------------- AC1: wrapper
  test('AC1: rendered markdown table is wrapped in an overflow-x-auto container', async ({ page }) => {
    const { wrap } = await openTaskDrawer(page);

    // In view mode exactly one wrapper exists on the page (the edit-form draft
    // preview and comment previews render nothing until their text is non-empty,
    // which only happens in edit mode), so a page-level locator is unambiguous.
    const m = page.locator('div.md-table-wrap').first();
    await expect(m).toBeVisible();
    await expect(page.locator('div.md-table-wrap')).toHaveCount(1);
    await expect(wrap).toHaveCSS('overflow-x', 'auto');
    // The rendered table is a child of the wrapper.
    await expect(wrap.locator('table')).toHaveCount(1);
  });

  // ------------------------------------------------- AC2: internal scroll only
  test('AC2: wide table scrolls internally without page-level horizontal scroll', async ({ page }) => {
    const { wrap } = await openTaskDrawer(page);

    // The unbreakable token forces the table wider than its container, so an
    // internal scrollbar must be available inside the wrapper.
    const m = await wrap.evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth }));
    expect(m.sw).toBeGreaterThan(m.cw);

    // …and neither the drawer nor the document may grow past the viewport.
    await assertNoPageHScroll(page);
  });

  // ------------------------------------- AC3: light + dark rendering (screens)
  test('AC3: wide table with long headers and multiline text renders in light and dark themes', async ({ page }) => {
    // Named evidence PNGs (ephemeral, test-results only): the config's
    // screenshot:'on' auto-shot captures the final (dark) state, so the light
    // phase gets its own explicitly-named capture for AC3.
    const shotPath = (name: string) => path.join(testResultsDir, name);
    const isDark = () => page.evaluate(() => document.documentElement.classList.contains('dark'));

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    // --- LIGHT phase (chosen explicitly so the host OS preference cannot interfere) ---
    await page.getByTestId('header-menu-trigger').click();
    await expect(page.getByTestId('header-menu')).toBeVisible();
    await page.locator('[data-testid="theme-light"]').click();
    await expect(page.locator('[data-testid="theme-light"]')).toHaveAttribute('aria-pressed', 'true');
    expect(await isDark()).toBe(false);

    const { wrap } = await openTaskDrawer(page);

    // Long headers rendered in thead, six columns, multiline + unbreakable cells intact.
    await expect(wrap.locator('thead tr')).toHaveCount(1);
    await expect(wrap.locator('tbody tr')).toHaveCount(1);
    await expect(wrap.locator('th')).toHaveCount(6);
    await expect(wrap.locator('td')).toHaveCount(6);
    await expect(wrap.locator('th').nth(2)).toContainText('Gamma Header Very Long UnbreakableTokenAlpha1234567890');
    const multiCell = wrap.locator('td').nth(4);
    await expect(multiCell).toContainText('line one');
    await expect(multiCell).toContainText('line two');
    await expect(multiCell).toContainText('line three');
    await expect(multiCell.locator('br')).toHaveCount(2);
    // The unbreakable token cell survived markdown + sanitization intact.
    await expect(wrap.locator('td').nth(2)).toHaveText(/a{250,}bbbb/);

    // No page-level horizontal scroll in light mode.
    await assertNoPageHScroll(page);
    await page.screenshot({ path: shotPath('md-table-overflow-ac3-light.png') });

    // --- DARK phase ---
    // The open drawer's scrim (fixed, z-[60]) intercepts pointer events over the
    // header, so the menu trigger is unreachable while the drawer is open:
    // close it, flip the theme with the board visible, then re-open and assert.
    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
    await drawer.locator('button[aria-label="Close"]').first().click();
    await expect(drawer).toBeHidden();

    await page.getByTestId('header-menu-trigger').click();
    await expect(page.getByTestId('header-menu')).toBeVisible();
    await page.locator('[data-testid="theme-dark"]').click();
    await expect(page.locator('[data-testid="theme-dark"]')).toHaveAttribute('aria-pressed', 'true');
    expect(await isDark()).toBe(true);
    // transition-colors (150ms): let the theme switch settle before asserting.
    await page.waitForTimeout(300);

    // Re-open the drawer (no reload — the board and the light→dark choice both
    // persist) and re-run the full assertion set in dark mode.
    const card = page.locator('article', { hasText: TASK_TITLE });
    await expect(card).toBeVisible();
    await card.click();
    await expect(drawer).toBeVisible();
    const mdBodyDark = drawer.getByTestId('task-description');
    await expect(mdBodyDark).toBeVisible();
    const wrapDark = mdBodyDark.locator('div.md-table-wrap').first();

    // Wrapper + columns still rendered in dark mode, internal scroll preserved,
    // no page-level horizontal scroll either.
    await expect(wrapDark).toBeVisible();
    await expect(wrapDark).toHaveCSS('overflow-x', 'auto');
    await expect(wrapDark.locator('thead tr')).toHaveCount(1);
    await expect(wrapDark.locator('tbody tr')).toHaveCount(1);
    await expect(wrapDark.locator('th')).toHaveCount(6);
    await expect(wrapDark.locator('td')).toHaveCount(6);
    const m = await wrapDark.evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth }));
    expect(m.sw).toBeGreaterThan(m.cw);
    await assertNoPageHScroll(page);
    await page.screenshot({ path: shotPath('md-table-overflow-ac3-dark.png') });
  });
});
