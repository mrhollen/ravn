// URL Query Parameter Synchronization E2E tests
// (task 01M5XTAXGDEHFNKFGH9MTG65VK)

import { test, expect } from '@playwright/test';
import { selectProjectViaDropdown, expectProjectSelected } from './helpers';
import { spawn, type ChildProcess } from 'child_process';
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
      // Retry
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

test.describe('Board filters, sort, and project URL query sync (01M5XTAXGDEHFNKFGH9MTG65VK)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;

  let projA: any;
  let taskA1: any;
  let taskA2: any;
  let taskA3: any;

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-url-sync-'));
    const dbPath = path.join(tempDir, 'test.db');
    const port = await getFreePort();
    serverUrl = `http://127.0.0.1:${port}`;

    serverProcess = spawn(
      serverBin,
      ['-addr', `127.0.0.1:${port}`, '-data', dbPath, '-static', staticDir],
      { stdio: ['ignore', 'inherit', 'inherit'] }
    );

    await waitForServer(serverUrl);

    // Seed Project A with tasks
    projA = await postJson(`${serverUrl}/api/v1/projects`, { name: 'URL Sync Alpha', actor: ACTOR });
    taskA1 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Alpha Needle P4',
      priority: 4,
      actor: ACTOR,
    })).task;

    taskA2 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Alpha Haystack P2',
      priority: 2,
      actor: ACTOR,
    })).task;

    taskA3 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Alpha Blocked P4',
      priority: 4,
      actor: ACTOR,
    })).task;

    // Make taskA3 blocked by taskA2
    await postJson(`${serverUrl}/api/v1/tasks/${taskA3.id}/relations`, {
      other_id: taskA2.id,
      kind: 'blocked-by',
      actor: ACTOR,
    });
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

  test('direct URL load with project, priority, unblocked, and search initializes filters (AC2)', async ({ page }) => {
    // AC2: Navigate with query parameters
    const targetUrl = `${serverUrl}/?project=${projA.id}&priority=4&unblocked=1&q=Needle`;
    await page.goto(targetUrl);

    // Verify project is automatically selected
    await expectProjectSelected(page, 'URL Sync Alpha');

    // Verify taskA1 (P4 unblocked with "Needle") is visible
    const card1 = page.locator('article', { hasText: 'Alpha Needle P4' });
    await expect(card1).toBeVisible();

    // Verify taskA2 (P2) is filtered out
    const card2 = page.locator('article', { hasText: 'Alpha Haystack P2' });
    await expect(card2).not.toBeVisible();

    // Verify taskA3 (P4 but blocked) is filtered out by unblocked=1
    const card3 = page.locator('article', { hasText: 'Alpha Blocked P4' });
    await expect(card3).not.toBeVisible();

    // Verify UI control values match URL
    const prioritySelect = page.getByRole('combobox', { name: 'Filter by priority' }).first();
    await expect(prioritySelect).toHaveValue('4');

    const unblockedCheckbox = page.getByTestId('filter-unblocked');
    await expect(unblockedCheckbox).toBeChecked();

    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await expect(searchInput).toHaveValue('Needle');
  });

  test('UI filter changes reflect in URL, and Reset filters clears query parameters (AC1, AC3)', async ({ page }, testInfo) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, 'URL Sync Alpha');

    // Filter by priority 2
    const prioritySelect = page.getByRole('combobox', { name: 'Filter by priority' }).first();
    await prioritySelect.selectOption('2');

    // Filter by unblocked
    const unblockedCheckbox = page.getByTestId('filter-unblocked');
    await unblockedCheckbox.check();

    // Search for Haystack
    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await searchInput.fill('Haystack');

    // AC1: URL reflects the active filters without polluting history
    await expect.poll(() => page.url()).toContain(`priority=2`);
    await expect.poll(() => page.url()).toContain(`unblocked=1`);
    await expect.poll(() => page.url()).toContain(`q=Haystack`);

    // Only card 2 should be visible
    await expect(page.locator('article', { hasText: 'Alpha Haystack P2' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Alpha Needle P4' })).not.toBeVisible();

    // Take screenshot evidence of active URL query sync and filtered view
    await page.screenshot({ path: path.join(testInfo.outputDir, 'url-sync-filtered-view.png') });

    // AC3: Click Reset filters
    const resetBtn = page.getByRole('button', { name: 'Reset filters' }).first();
    await expect(resetBtn).toBeVisible();
    await resetBtn.click();

    // Verify URL no longer contains filter parameters
    await expect.poll(() => page.url()).not.toContain('priority=');
    await expect.poll(() => page.url()).not.toContain('unblocked=');
    await expect.poll(() => page.url()).not.toContain('q=');

    // Project is preserved
    expect(page.url()).toContain(`project=${projA.id}`);

    // Both unblocked tasks are now visible
    await expect(page.locator('article', { hasText: 'Alpha Needle P4' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Alpha Haystack P2' })).toBeVisible();
  });

  test('deep-link /tasks/<id> works seamlessly alongside query parameters (AC4)', async ({ page }) => {
    // AC4: Load /tasks/<id> deep link with query parameters
    const deepLinkUrl = `${serverUrl}/tasks/${taskA1.id}?project=${projA.id}&priority=4`;
    await page.goto(deepLinkUrl);

    // Detail drawer/modal is open for taskA1
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByText('Alpha Needle P4')).toBeVisible();

    // Close the drawer using Escape key
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    // Returns to board URL with project and filters intact
    await expect.poll(() => page.url()).toContain(`project=${projA.id}`);
    await expect.poll(() => page.url()).toContain(`priority=4`);
    expect(new URL(page.url()).pathname).toBe('/');
  });
});
