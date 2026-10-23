import { test, expect } from '@playwright/test';
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
      // Retry
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

const SEED_ACTOR = { name: 'e2e-enhancements-seed', type: 'agent' };

test.describe('Task Modal Enhancements (Discard Confirmation & Double Column)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
  let fixtureTask: any;

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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-modal-enhancements-'));
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

    await waitForServer(serverUrl);

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Modal Enhancements E2E project',
      description: 'Project for discard confirm and 2-column modal tests',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    fixtureTask = await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Modal Enhancements Fixture',
      description: 'Testing two-column modal view on large screens.',
      priority: 2,
      actor: SEED_ACTOR,
    });
  });

  test.afterAll(async () => {
    serverProcess?.kill('SIGTERM');
    if (tempDir && fs.existsSync(tempDir)) {
      fs.rmSync(tempDir, { recursive: true, force: true });
    }
  });

  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expectProjectSelected(page, 'Modal Enhancements E2E project');
  }

  // ---------------------------------------------------------------------------
  // Task 01M5K265HEVPWDG3FKS6N7V3FT: Discard Confirmation in Create Task Modal
  // ---------------------------------------------------------------------------

  test('Task 1 - AC1-AC4: Cancelling dirty Create Task modal uses inline confirmation without native dialog', async ({ page }) => {
    await openBoard(page);

    let nativeDialogFired = false;
    page.on('dialog', (d) => {
      nativeDialogFired = true;
      void d.dismiss();
    });

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task to discard');

    // Original buttons visible
    const cancelBtn = modal.getByTestId('new-task-cancel-btn');
    const createBtn = modal.getByTestId('new-task-create-btn');
    await expect(cancelBtn).toBeVisible();
    await expect(createBtn).toBeVisible();

    // User clicks Cancel
    await cancelBtn.click();

    // AC1: Native dialog is gone
    expect(nativeDialogFired).toBe(false, 'Native dialog must NOT be shown');

    // AC2: "Cancel" and "Create task" buttons are replaced with "Cancel" and "Discard" buttons
    await expect(createBtn).toBeHidden();
    const confirmCancelBtn = modal.getByTestId('new-task-confirm-cancel-btn');
    const confirmDiscardBtn = modal.getByTestId('new-task-confirm-discard-btn');
    await expect(confirmCancelBtn).toBeVisible();
    await expect(confirmDiscardBtn).toBeVisible();
    await expect(confirmDiscardBtn).toHaveText('Discard');

    // AC3: "Discard" button is red
    const discardColor = await confirmDiscardBtn.evaluate((el) => {
      const style = window.getComputedStyle(el);
      return {
        bg: style.backgroundColor,
      };
    });
    // bg-red-600 is oklch(0.577 0.245 27.325) or rgb(220, 38, 38)
    expect(discardColor.bg).toMatch(/oklch\(0\.577|rgb\(220,\s*38,\s*38\)/);

    // AC4: "Discard this task?" is in readable red letters to the left of the buttons
    const msg = modal.getByTestId('new-task-discard-confirm-msg');
    await expect(msg).toBeVisible();
    await expect(msg).toHaveText('Discard this task?');
    const msgColor = await msg.evaluate((el) => window.getComputedStyle(el).color);
    expect(msgColor).toMatch(/oklch\(0\.577|rgb\(220,\s*38,\s*38\)/);

    // Verify it is positioned to the left of the buttons
    const msgBox = await msg.boundingBox();
    const btnBox = await confirmCancelBtn.boundingBox();
    expect(msgBox).not.toBeNull();
    expect(btnBox).not.toBeNull();
    expect(msgBox!.x + msgBox!.width).toBeLessThanOrEqual(btnBox!.x);
  });

  test('Task 1 - AC5-AC7: Clicking cancel during confirmation restores original buttons and allows normal creation', async ({ page }) => {
    await openBoard(page);

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task created after cancel confirmation');

    // Click cancel to enter confirmation mode
    await modal.getByTestId('new-task-cancel-btn').click();
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeVisible();

    // AC5, AC6: Clicking Cancel during confirmation restores original buttons and removes confirmation message
    const confirmCancelBtn = modal.getByTestId('new-task-confirm-cancel-btn');
    await confirmCancelBtn.click();

    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeHidden();
    const cancelBtn = modal.getByTestId('new-task-cancel-btn');
    const createBtn = modal.getByTestId('new-task-create-btn');
    await expect(cancelBtn).toBeVisible();
    await expect(createBtn).toBeVisible();

    // AC7: Allows user to create the task like normal
    await createBtn.click();
    await expect(modal).toBeHidden();

    // Check card exists on board
    await expect(page.locator('article', { hasText: 'Task created after cancel confirmation' })).toBeVisible();
  });

  test('Task 1 - AC8: Clicking Discard during confirmation dismisses modal and clears all data', async ({ page }) => {
    await openBoard(page);

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task that will be discarded');

    // Click Cancel -> confirmation
    await modal.getByTestId('new-task-cancel-btn').click();
    const confirmDiscardBtn = modal.getByTestId('new-task-confirm-discard-btn');
    await expect(confirmDiscardBtn).toBeVisible();

    // Click Discard
    await confirmDiscardBtn.click();
    await expect(modal).toBeHidden();

    // Reopen modal and verify form is completely cleared
    await page.getByRole('button', { name: '+ New task' }).click();
    await expect(modal).toBeVisible();
    await expect(titleInput).toHaveValue('');
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeHidden();
    await expect(modal.getByTestId('new-task-create-btn')).toBeVisible();
  });

  // ---------------------------------------------------------------------------
  // Task 01M5K19665DKB9RJNS9KW52SZ5: Double Column View in Task Detail Modal
  // ---------------------------------------------------------------------------

  test('Task 2 - AC1: User screens >= 1080px show two columns in the task modal', async ({ page }) => {
    await openBoard(page);

    // Test at 1080px width
    await page.setViewportSize({ width: 1080, height: 800 });

    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();

    const modal = page.locator('#cb-detail-drawer');
    await expect(modal).toBeVisible();

    const leftCol = page.getByTestId('detail-left-column');
    const rightCol = page.getByTestId('detail-right-column');
    await expect(leftCol).toBeVisible();
    await expect(rightCol).toBeVisible();

    const leftBox = await leftCol.boundingBox();
    const rightBox = await rightCol.boundingBox();
    expect(leftBox).not.toBeNull();
    expect(rightBox).not.toBeNull();

    // Two columns side-by-side: left column's X is to the left of right column's X
    expect(leftBox!.x).toBeLessThan(rightBox!.x);
    expect(leftBox!.x + leftBox!.width).toBeLessThanOrEqual(rightBox!.x + 1);

    // Also test at 1280px width
    await page.setViewportSize({ width: 1280, height: 800 });
    const leftBox1280 = await leftCol.boundingBox();
    const rightBox1280 = await rightCol.boundingBox();
    expect(leftBox1280!.x).toBeLessThan(rightBox1280!.x);
    expect(leftBox1280!.x + leftBox1280!.width).toBeLessThanOrEqual(rightBox1280!.x + 1);
  });

  test('Task 2 - AC2: User screens < 1080px remain unchanged (single column view in modal)', async ({ page }) => {
    await openBoard(page);

    // Test just below 1080px: 1079px
    await page.setViewportSize({ width: 1079, height: 800 });

    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();

    const modal = page.locator('#cb-detail-drawer');
    await expect(modal).toBeVisible();

    const leftCol = page.getByTestId('detail-left-column');
    const rightCol = page.getByTestId('detail-right-column');
    await expect(leftCol).toBeVisible();
    await expect(rightCol).toBeVisible();

    const leftBox = await leftCol.boundingBox();
    const rightBox = await rightCol.boundingBox();
    expect(leftBox).not.toBeNull();
    expect(rightBox).not.toBeNull();

    // Single column: stacked vertically (left column above right column, aligned horizontally)
    expect(leftBox!.y + leftBox!.height).toBeLessThanOrEqual(rightBox!.y + 1);
    expect(Math.abs(leftBox!.x - rightBox!.x)).toBeLessThanOrEqual(5);

    // Also test at 768px (tablet)
    await page.setViewportSize({ width: 768, height: 800 });
    const leftBox768 = await leftCol.boundingBox();
    const rightBox768 = await rightCol.boundingBox();
    expect(leftBox768!.y + leftBox768!.height).toBeLessThanOrEqual(rightBox768!.y + 1);
  });
});
