// Task & project archive management (Rävn task 01M5XSNN2KA876TR0QGE79YFPT):
//   AC1 — Archive/Unarchive a single task from the card ⋮ menu AND the task
//         detail header; archiving removes the card and offers a ~5s Undo toast.
//   AC2 — Bulk "Archive all Done" in the Done column with an inline two-step
//         confirm (cancel path + confirm path) and a summary toast.
//   AC3 — "Show archived" board toggle: swaps to the archived data source, each
//         card offers Unarchive, and toggling OFF restores the live board exactly.
//   AC4 — Archived projects appear in an "Archived" section of the project menu
//         with a Restore action that reactivates them as normal options.

import { test, expect } from '@playwright/test';
import { selectProjectViaDropdown, expectProjectSelected } from './helpers';
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
      // Retry until the server is up.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

const ACTOR = { name: 'e2e', type: 'human' as const };

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} → ${res.status}`);
  return res.json();
}

async function patchJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${url} → ${res.status}`);
  return res.json();
}

/** Complete a todo task via the legal todo→doing→done chain. */
async function completeTask(serverUrl: string, t: any): Promise<void> {
  const doing = await patchJson(`${serverUrl}/api/v1/tasks/${t.id}`, { status: 'doing', version: t.version, actor: ACTOR });
  await patchJson(`${serverUrl}/api/v1/tasks/${t.id}`, { status: 'done', version: doing.version, actor: ACTOR });
}

/** All VISIBLE board cards (scoped to the kanban grid). */
function boardCards(page: import('@playwright/test').Page) {
  return page.locator('.bg-dots article');
}

function cardByTitle(page: import('@playwright/test').Page, title: string) {
  return boardCards(page).filter({ hasText: title });
}

test.describe('Task archive management (01M5XSNN2KA876TR0QGE79YFPT)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
  let project: any;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-archive-task-'));
    const dbPath = path.join(tempDir, 'test.db');
    serverUrl = `http://127.0.0.1:${await getFreePort()}`;

    serverProcess = spawn(
      serverBin,
      ['-addr', serverUrl.replace('http://', ''), '-data', dbPath, '-static', staticDir],
      { stdio: ['ignore', 'inherit', 'inherit'] },
    );
    await waitForServer(serverUrl);

    project = (await postJson(`${serverUrl}/api/v1/projects`, { name: 'Archive Suite', actor: ACTOR }));
    const mk = async (title: string) =>
      (await postJson(`${serverUrl}/api/v1/tasks`, {
        project_id: project.id,
        title,
        description: 'archive suite seed task',
        actor: ACTOR,
      })).task;

    // Live board at boot: 2 todo, 1 doing, 2 done. Tests mutate this shared set
    // in FILE ORDER (see the numbered steps inside each test).
    await mk('Solo Card');
    await mk('Other Todo');
    const gamma = await mk('Moving Gamma');
    // ONE legal hop only: todo→doing. A second doing→done hop would succeed too
    // and silently add a 3rd done task, breaking the exact "Archive all done (2)"
    // counts below — Moving Gamma must STAY in doing for the AC3 restore check.
    await patchJson(`${serverUrl}/api/v1/tasks/${gamma.id}`, { status: 'doing', version: gamma.version, actor: ACTOR });
    for (const title of ['Bulk One', 'Bulk Two']) {
      const t = await mk(title);
      await completeTask(serverUrl, t);
    }
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
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
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

  test('AC1: card menu archives the task, removes it, and the Undo toast restores it', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expectProjectSelected(page, 'Archive Suite');

    const solo = cardByTitle(page, 'Solo Card');
    await expect(solo).toBeVisible();

    // Open the ⋮ menu on that exact card and use its Archive item.
    await solo.getByTestId('card-menu-toggle').click();
    const menu = solo.getByTestId('card-move-menu');
    await expect(menu).toBeVisible();
    await menu.getByTestId('card-menu-archive').click();

    // Optimistic removal: the card leaves its column; the other todo card stays.
    await expect(cardByTitle(page, 'Solo Card')).toHaveCount(0);
    await expect(cardByTitle(page, 'Other Todo')).toBeVisible();

    // The toast carries a local Undo action (a <button>, not a link).
    const undo = page.getByTestId('toast-stack').getByRole('button', { name: 'Undo' });
    await expect(undo).toBeVisible();
    await expect(page.getByTestId('toast-stack')).toContainText('Task archived');

    // Undo re-applies archived:false and the card returns to the board.
    await undo.click();
    await expect(cardByTitle(page, 'Solo Card')).toBeVisible();
  });

  test('AC1: detail-header Archive archives an open task; Unarchive label follows the archived flag', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expectProjectSelected(page, 'Archive Suite');

    await cardByTitle(page, 'Solo Card').click();
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel).toBeVisible();

    // The header button reads "Archive" for a live task.
    const archBtn = panel.getByTestId('task-detail-archive');
    await expect(archBtn).toContainText('Archive');
    await archBtn.click();
    await expect(page.getByTestId('toast-stack')).toContainText('Task archived');

    // The OPEN detail flips its label to "Unarchive" (detail.archived synced).
    await expect(archBtn).toContainText('Unarchive');

    await page.getByTestId('detail-close-btn').click();
    await expect(page.locator('#cb-detail-drawer')).toBeHidden();

    // The card is gone from the live board — and it STAYS archived for the AC3 test.
    await expect(cardByTitle(page, 'Solo Card')).toHaveCount(0);
  });

  /** Scope to the Done lane: the archive-all footer exists in all three column
   * instances in the DOM (x-show hides two), so unscoped testid locators hit a
   * strict-mode violation. Same pattern as full-text-search.spec.ts. */
  const doneLane = (page: import('@playwright/test').Page) =>
    page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });

  test('AC2: bulk "Archive all Done" confirm row opens and CANCEL leaves every done task in place', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expectProjectSelected(page, 'Archive Suite');

    await expect(cardByTitle(page, 'Bulk One')).toBeVisible();
    await expect(cardByTitle(page, 'Bulk Two')).toBeVisible();

    const lane = doneLane(page);
    const trigger = lane.getByTestId('archive-all-done');
    await expect(trigger).toBeVisible();
    await expect(trigger).toHaveText('Archive all done (2)');

    // Step 1: the inline confirm row appears.
    await trigger.click();
    const row = lane.getByTestId('archive-all-done-confirm-row');
    await expect(row).toBeVisible();
    await expect(row).toContainText('Archive all 2 done tasks?');

    // AC2 cancel path: nothing is archived, the footer returns to its trigger.
    await row.getByTestId('archive-all-done-cancel').click();
    await expect(row).toBeHidden();
    await expect(trigger).toHaveText('Archive all done (2)');
    await expect(cardByTitle(page, 'Bulk One')).toBeVisible();
    await expect(cardByTitle(page, 'Bulk Two')).toBeVisible();
  });

  test('AC2: confirming the bulk action archives EVERY done task and toasts a summary', async ({ page }) => {
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expectProjectSelected(page, 'Archive Suite');
    await expect(cardByTitle(page, 'Bulk One')).toBeVisible();

    const lane = doneLane(page);
    await lane.getByTestId('archive-all-done').click();
    const row = lane.getByTestId('archive-all-done-confirm-row');
    await expect(row).toBeVisible();
    await row.getByTestId('archive-all-done-confirm').click();

    // Both done cards leave the board and a summary toast names the exact count.
    await expect(cardByTitle(page, 'Bulk One')).toHaveCount(0);
    await expect(cardByTitle(page, 'Bulk Two')).toHaveCount(0);
    await expect(page.getByTestId('toast-stack')).toContainText('Archived 2 done tasks');

    // The bulk trigger disappears with the empty done column (no more done tasks).
    await expect(lane.getByTestId('archive-all-done')).toBeHidden();
  });

  test('AC3: "Show archived" swaps the board to the archived set, unarchives in place, and OFF restores it', async ({
    page,
  }, testInfo) => {
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expectProjectSelected(page, 'Archive Suite');

    // Live board right now: Other Todo + Moving Gamma (Solo Card & both Bulk rows archived).
    const toggle = page.getByTestId('show-archived-toggle');
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');

    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'true');

    // The ARCHIVED data source renders: 3 cards from their archived statuses.
    const archivedSolo = cardByTitle(page, 'Solo Card');
    const archivedOne = cardByTitle(page, 'Bulk One');
    const archivedTwo = cardByTitle(page, 'Bulk Two');
    await expect(archivedSolo).toBeVisible();
    await expect(archivedOne).toBeVisible();
    await expect(archivedTwo).toBeVisible();
    // The live cards are NOT in this view.
    await expect(cardByTitle(page, 'Other Todo')).toHaveCount(0);

    // Archived-view chrome: per-card Unarchive replaces the ⋮ move menu, and
    // "+ Add task" is suppressed on every column. x-show keeps the three footer
    // buttons in the DOM (hidden), so assert HIDDEN per button, not count 0.
    await expect(archivedSolo.getByTestId('unarchive-action')).toBeVisible();
    await expect(archivedSolo.getByTestId('card-menu-anchor')).toBeHidden();
    const addBtns = page.locator('button', { hasText: 'Add task to' });
    for (let i = 0; i < (await addBtns.count()); i++) {
      await expect(addBtns.nth(i)).toBeHidden();
    }

    // Unarchive in place: the card leaves the archived set with a restore toast.
    await archivedSolo.getByTestId('unarchive-action').click();
    await expect(page.getByTestId('toast-stack')).toContainText('Task restored from archive');
    await expect(cardByTitle(page, 'Solo Card')).toHaveCount(0);
    await expect(archivedOne).toBeVisible();
    await expect(archivedTwo).toBeVisible();
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac3-archived-view-with-unarchive.png') });

    // OFF is an exact restore of the live board: the unarchived Solo Card is back
    // on the todo column; the two archived Bulk rows stay out.
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await expect(cardByTitle(page, 'Solo Card')).toBeVisible();
    await expect(cardByTitle(page, 'Other Todo')).toBeVisible();
    await expect(cardByTitle(page, 'Moving Gamma')).toBeVisible();
    await expect(archivedOne).toHaveCount(0);
    await expect(archivedTwo).toHaveCount(0);
  });
});

test.describe('Archived-project restore section (01M5XSNN2KA876TR0QGE79YFPT)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
  let alpha: any;
  let beta: any;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-archive-project-restore-'));
    const dbPath = path.join(tempDir, 'test.db');
    serverUrl = `http://127.0.0.1:${await getFreePort()}`;

    serverProcess = spawn(
      serverBin,
      ['-addr', serverUrl.replace('http://', ''), '-data', dbPath, '-static', staticDir],
      { stdio: ['ignore', 'inherit', 'inherit'] },
    );
    await waitForServer(serverUrl);

    alpha = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Alpha Keep', actor: ACTOR });
    beta = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Beta Archive', actor: ACTOR });
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
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
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

  test('AC4: archived project appears in the menu "Archived" section and Restore reactivates it', async ({
    page,
  }, testInfo) => {
    await page.goto(serverUrl);
    // Start on Beta (the archive target).
    await selectProjectViaDropdown(page, beta.id);
    await expectProjectSelected(page, 'Beta Archive');

    // Archive Beta through the settings modal (same flow as the project-archive suite).
    const gear = page.getByTestId('project-settings-btn');
    await expect(gear).toBeEnabled();
    await gear.click();
    const modal = page.locator('#cb-project-settings');
    await expect(modal).toBeVisible();
    await page.getByTestId('archive-project-btn').click();
    const confirmBox = page.getByTestId('archive-confirm-box');
    await expect(confirmBox).toBeVisible();
    await page.getByTestId('archive-confirm-input').fill('Beta Archive');
    await page.getByTestId('confirm-archive-btn').click();
    await expect(modal).toBeHidden();
    await expect(page.getByTestId('toast-stack')).toContainText('Project archived');

    // The selection falls back to a live project.
    const trigger = page.getByTestId('project-select');
    await expect(trigger).not.toContainText('Beta Archive');

    // Open the menu: Beta is GONE from the pickable options but PRESENT in the
    // labeled Archived section with its Restore action.
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu).toBeVisible();
    await expect(menu.locator('[data-project-id]')).toHaveCount(1, 'only Alpha remains pickable');
    await expect(menu.locator('[data-project-id]').getByText('Alpha Keep')).toBeVisible();

    const restore = menu.getByTestId('project-restore');
    await expect(restore).toHaveCount(1);
    await expect(restore).toContainText('Restore · Beta Archive');
    // The row is actionable by its dedicated id, not a project option.
    await expect(restore).toHaveAttribute('data-restore-project-id', beta.id);
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac4-archived-section-with-restore.png') });

    // Restore: the row leaves the archived section and Beta reappears as a normal option.
    await restore.click();
    await expect(page.getByTestId('toast-stack')).toContainText('Project restored');
    await expect(menu.getByTestId('project-restore')).toHaveCount(0);
    await expect(menu.locator('[data-project-id]').getByText('Beta Archive')).toBeVisible();

    // And the restored project is selectable again: it reappears as a normal
    // option IN THE STILL-OPEN menu (restoreProject keeps the dropdown open by
    // design — app.ts documents this), so pick Beta straight from here. Do NOT
    // toggle the trigger: with the menu already open that would CLOSE it, and
    // a click on the then-hidden option would hang until timeout.
    const betaOption = menu.locator('[data-project-id]').getByText('Beta Archive');
    await expect(betaOption).toBeVisible();
    await betaOption.click();
    await expect(menu).toBeHidden();
    await expectProjectSelected(page, 'Beta Archive');
  });
});
