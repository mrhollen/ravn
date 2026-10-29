// Multi-select board cards + floating batch-action bar (task 01M5XTB04A8ZX1TX8ARYFG5D9F).
//   AC1 — checkbox on hover, Ctrl/Cmd+Click toggle, Shift range; a plain click
//         still opens the detail drawer exactly as before
//   AC2 — select-all-visible honors active filters; batch set-priority; batch
//         owner assign (existing-assignee dropdown + free-text fallback) / clear;
//         batch archive
//   AC3 — batch move performs a legal status hop; an illegal hop degrades to a
//         per-row failure summary toast and keeps the failed rows selected
//   AC4 — Escape closes an open batch dropdown first, then clears the selection;
//         an open drawer beats the selection
// Server state persists across tests in this file (one shared DB); each test
// gets a fresh browser context so SELECTION always starts empty. Tests therefore
// run in FILE ORDER and only make position-independent assertions after T5
// moves MSB Alpha/Bravo into Doing.
import { test, expect, type Page } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { expectProjectSelected } from './helpers';

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
      // Retry until the server is listening.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

const SEED_ACTOR = { name: 'e2e-msb-seed', type: 'agent' as const };

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`);
  return res.json();
}

async function patchJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${url} failed: ${res.status}`);
  return res.json();
}

/** All VISIBLE kanban cards (scoped to the board grid, so drawer/modals never match). */
function boardCards(page: Page) {
  return page.locator('.bg-dots article');
}

function cardByTitle(page: Page, title: string) {
  return boardCards(page).filter({ hasText: title });
}

/** The visible search-filter input (desktop strip; the mobile duplicate is hidden). */
function searchInput(page: Page) {
  return page.getByRole('textbox', { name: 'Filter cards by title or ID' }).locator('visible=true');
}

/** Hover a card (reveals the checkbox via :hover) then click its select checkbox. */
async function selectCard(page: Page, title: string): Promise<void> {
  const card = cardByTitle(page, title);
  await expect(card).toBeVisible();
  await card.hover();
  await card.getByTestId('card-select-checkbox').click();
}

function lane(page: Page, label: string) {
  return page.locator('section', { has: page.getByRole('heading', { name: label }) });
}

test.describe('Multi-select batch actions (01M5XTB04A8ZX1TX8ARYFG5D9F)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;

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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-msb-'));
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
      if (process.env.DEBUG_E2E) console.error('[server stderr]', d.toString());
    });

    await waitForServer(serverUrl);

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'MSB Suite',
      description: 'Seeded for the multi-select batch-action e2e spec',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    // All P2, no due dates → within a column the server returns creation order.
    const mk = async (title: string, assignee?: { name: string; type: 'human' | 'agent' }) =>
      (
        await postJson(`${serverUrl}/api/v1/tasks`, {
          project_id: projectId,
          title,
          priority: 2,
          actor: SEED_ACTOR,
          ...(assignee ? { assignee } : {}),
        })
      ).task;

    // Four Todo cards the selection tests operate on…
    await mk('MSB Alpha');
    await mk('MSB Bravo');
    await mk('MSB Charlie');
    await mk('MSB Delta');
    // …one Doing card that carries a durable assignee (feeds the Owner dropdown)…
    const ownerOne = await mk('MSB Owner One', { name: 'msb-owner', type: 'agent' });
    await patchJson(`${serverUrl}/api/v1/tasks/${ownerOne.id}`, { status: 'doing', version: ownerOne.version, actor: SEED_ACTOR });
    // …and one Done card via the legal todo→doing→done chain.
    const omega = await mk('MSB Omega');
    const omegaDoing = await patchJson(`${serverUrl}/api/v1/tasks/${omega.id}`, { status: 'doing', version: omega.version, actor: SEED_ACTOR });
    await patchJson(`${serverUrl}/api/v1/tasks/${omega.id}`, { status: 'done', version: omegaDoing.version, actor: SEED_ACTOR });
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
      try {
        fs.rmSync(tempDir, { recursive: true, force: true });
      } catch {
        /* ignore */
      }
    }
  });

  test('AC1: the hover checkbox selects without opening the drawer; a plain click still opens it', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');
    await expect(boardCards(page)).toHaveCount(6); // 4 todo + 1 doing + 1 done

    const alpha = cardByTitle(page, 'MSB Alpha');
    await selectCard(page, 'MSB Alpha');
    await expect(page.locator('aside')).toBeHidden('a checkbox click must never open the drawer');
    await expect(alpha.getByTestId('card-select-checkbox')).toBeChecked();

    const bar = page.getByTestId('batch-action-bar');
    await expect(bar).toBeVisible();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');

    // Second card accumulates; un-checking the first shrinks the count again.
    await selectCard(page, 'MSB Bravo');
    await expect(page.getByTestId('batch-count')).toHaveText('2 selected');
    await alpha.hover();
    await alpha.getByTestId('card-select-checkbox').click();
    await expect(alpha.getByTestId('card-select-checkbox')).not.toBeChecked();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');

    // A plain (unmodified) click on a different card opens the drawer AS BEFORE
    // and leaves the selection untouched.
    await cardByTitle(page, 'MSB Charlie').locator('h3').click();
    await expect(page.locator('aside')).toBeVisible();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');
  });

  test('AC1: Ctrl+Click toggles a card; Shift+Click range-selects within the column', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    // Ctrl+Click — anchor + single toggle, no drawer.
    await cardByTitle(page, 'MSB Alpha').locator('h3').click({ modifiers: ['Control'] });
    await expect(page.locator('aside')).toBeHidden();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');

    // Ctrl+Click a non-adjacent card (skipping Bravo) — additive selection.
    await cardByTitle(page, 'MSB Charlie').locator('h3').click({ modifiers: ['Control'] });
    await expect(page.getByTestId('batch-count')).toHaveText('2 selected');

    // Shift+Click Delta extends from the anchor (Charlie) to Delta: additive,
    // Bravo stays out.
    await cardByTitle(page, 'MSB Delta').locator('h3').click({ modifiers: ['Shift'] });
    await expect(page.getByTestId('batch-count')).toHaveText('3 selected');
    for (const [title, checked] of [
      ['MSB Alpha', true],
      ['MSB Bravo', false],
      ['MSB Charlie', true],
      ['MSB Delta', true],
    ] as const) {
      const cb = cardByTitle(page, title).getByTestId('card-select-checkbox');
      if (checked) await expect(cb).toBeChecked();
      else await expect(cb).not.toBeChecked();
    }
  });

  test('AC2: Select all visible honors the active title filter', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    // Seed one selection so the bar (and its Select-all-visible button) exists…
    await selectCard(page, 'MSB Alpha');
    // …then narrow the board to only the P2 todos' shared prefix minus Bravo…
    await searchInput(page).fill('Alph');
    await expect(boardCards(page)).toHaveCount(1); // only MSB Alpha survives

    // A broader query brings every seeded title back (all start with "MSB").
    await searchInput(page).fill('MSB');
    await expect(boardCards(page)).toHaveCount(6);
    await page.getByTestId('batch-select-all').click();
    await expect(page.getByTestId('batch-count')).toHaveText('6 selected');
    await expect(cardByTitle(page, 'MSB Delta').getByTestId('card-select-checkbox')).toBeChecked();

    // Clearing the filter keeps the selection (only project switches and the
    // archived view clear it).
    await searchInput(page).fill('');
    await expect(boardCards(page)).toHaveCount(6);
    await expect(page.getByTestId('batch-count')).toHaveText('6 selected');
  });

  test('AC2: batch set-priority updates every selected card and toasts a summary', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    await selectCard(page, 'MSB Alpha');
    await selectCard(page, 'MSB Bravo');

    await page.getByTestId('batch-priority').click();
    await page.getByTestId('batch-priority-4').click();

    await expect(page.getByTestId('toast-stack')).toContainText('Updated 2 tasks to priority P4');
    // The optimistic badge flips immediately and the settling reload keeps it.
    await expect(cardByTitle(page, 'MSB Alpha').getByText('P4', { exact: true })).toBeVisible();
    await expect(cardByTitle(page, 'MSB Bravo').getByText('P4', { exact: true })).toBeVisible();

    // Full success clears the selection and hides the bar.
    await expect(page.getByTestId('batch-action-bar')).toBeHidden();
  });

  test('AC3: batch move performs a legal hop; an illegal hop degrades to a per-row failure summary', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    // Legal hop: todo → doing.
    await selectCard(page, 'MSB Alpha');
    await selectCard(page, 'MSB Bravo');
    await page.getByTestId('batch-move').click();
    await page.getByTestId('batch-move-doing').click();
    await expect(page.getByTestId('toast-stack')).toContainText('Moved 2 tasks to Doing');
    await expect(lane(page, 'Doing').locator('article', { hasText: 'MSB Alpha' })).toBeVisible();
    await expect(lane(page, 'Doing').locator('article', { hasText: 'MSB Bravo' })).toBeVisible();
    await expect(page.getByTestId('batch-action-bar')).toBeHidden(); // selection cleared on success

    // Illegal hop: todo → done is not in the transition matrix. Each row fails
    // with a 400, nothing mutates, and ONE error summary names both failures.
    await selectCard(page, 'MSB Charlie');
    await selectCard(page, 'MSB Delta');
    await page.getByTestId('batch-move').click();
    await page.getByTestId('batch-move-done').click();
    await expect(page.getByTestId('toast-stack').locator('[role="alert"]')).toContainText('2 failed');

    // Both rows are untouched and STILL selected (failed ids stay in the set).
    // Note: the Todo column's heading is "To Do" (STATUS_LABEL), not "Todo".
    await expect(lane(page, 'To Do').locator('article', { hasText: 'MSB Charlie' })).toBeVisible();
    await expect(lane(page, 'To Do').locator('article', { hasText: 'MSB Delta' })).toBeVisible();
    await expect(page.getByTestId('batch-count')).toHaveText('2 selected');

    // Clear empties the bar again.
    await page.getByTestId('batch-clear').click();
    await expect(page.getByTestId('batch-action-bar')).toBeHidden();
  });

  test('AC2: batch owner offers existing assignees, assigns, clears, and falls back to free text', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    // The project already carries one durable assignee (msb-owner on MSB Owner One),
    // so the Owner menu lists it instead of showing the free-text form.
    await selectCard(page, 'MSB Alpha');
    await selectCard(page, 'MSB Owner One');
    await page.getByTestId('batch-assign').click();
    const ownerItem = page.getByTestId('batch-assign-owner');
    await expect(ownerItem).toHaveText('msb-owner');
    // The free-text form exists in the DOM but is x-show hidden while options exist.
    await expect(page.getByTestId('batch-custom-owner')).toBeHidden();

    await ownerItem.click();
    await expect(page.getByTestId('toast-stack')).toContainText('Assigned 2 tasks to msb-owner');
    await expect(cardByTitle(page, 'MSB Alpha').getByText('msb-owner', { exact: true })).toBeVisible();
    await expect(page.getByTestId('batch-action-bar')).toBeHidden(); // cleared on success

    // Clear ownership on both — the project then has NO assignees at all…
    await selectCard(page, 'MSB Alpha');
    await selectCard(page, 'MSB Owner One');
    await page.getByTestId('batch-assign').click();
    await page.getByTestId('batch-clear-owner').click();
    await expect(page.getByTestId('toast-stack')).toContainText('Cleared owner on 2 tasks');
    await expect(cardByTitle(page, 'MSB Alpha').getByText('msb-owner', { exact: true })).toHaveCount(0);

    // …so the menu now falls back to the free-text form.
    await selectCard(page, 'MSB Alpha');
    await page.getByTestId('batch-assign').click();
    const custom = page.getByTestId('batch-custom-owner');
    await expect(custom).toBeVisible();
    await custom.fill('e2e-custom');
    await page.getByTestId('batch-apply-custom-owner').click();
    await expect(page.getByTestId('toast-stack')).toContainText('Assigned 1 task to e2e-custom');
    await expect(cardByTitle(page, 'MSB Alpha').getByText('e2e-custom', { exact: true })).toBeVisible();
  });

  test('AC2: batch archive removes every selected card and toasts a summary', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    // Two survivors that no earlier test consumed: MSB Charlie (still todo) and
    // MSB Omega (done). Archive both.
    await selectCard(page, 'MSB Charlie');
    await selectCard(page, 'MSB Omega');
    await page.getByTestId('batch-archive').click();

    await expect(page.getByTestId('toast-stack')).toContainText('Archived 2 selected tasks');
    await expect(cardByTitle(page, 'MSB Charlie')).toHaveCount(0);
    await expect(cardByTitle(page, 'MSB Omega')).toHaveCount(0);
    await expect(boardCards(page)).toHaveCount(4); // Alpha+Bravo (doing), Delta (todo), Owner One (doing)
    await expect(page.getByTestId('batch-action-bar')).toBeHidden(); // cleared on success

    // Both cards are in the archive and visible via the archived board view.
    await page.getByTestId('show-archived-toggle').click();
    await expect(boardCards(page).filter({ hasText: 'MSB Charlie' })).toBeVisible();
    await expect(boardCards(page).filter({ hasText: 'MSB Omega' })).toBeVisible();
  });

  test('AC4: Escape closes an open batch dropdown first, then clears the selection; a drawer beats both', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${projectId}`);
    await expectProjectSelected(page, 'MSB Suite');

    const priorityMenu = page.getByRole('menu', { name: 'Set priority for selected tasks' });

    // 1) Open dropdown → Escape closes ONLY the dropdown (selection survives).
    await selectCard(page, 'MSB Delta');
    await page.getByTestId('batch-priority').click();
    await expect(priorityMenu).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(priorityMenu).toBeHidden();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');

    // 2) Second Escape clears the selection and hides the bar.
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('batch-action-bar')).toBeHidden();

    // 3) With a drawer open, Escape closes the drawer FIRST; the selection
    //    survives until the next press.
    await selectCard(page, 'MSB Delta');
    await cardByTitle(page, 'MSB Owner One').locator('h3').click();
    await expect(page.locator('aside')).toBeVisible();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');
    await page.keyboard.press('Escape');
    await expect(page.locator('aside')).toBeHidden();
    await expect(page.getByTestId('batch-count')).toHaveText('1 selected');
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('batch-action-bar')).toBeHidden();
  });
});
