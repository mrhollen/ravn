import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

// E2E coverage for Rävn task 01M5XT8TFD7CJFZB4502WEGD7E:
// - AC1: Done column caps initial rendered cards to 20 and provides Show more / Show all / Collapse controls
// - AC2: Card density toggle (Comfortable vs Compact) in toolbar & preferences persisted in localStorage
// - AC3: Condensed card layout in compact mode fitting more tasks per screen height
// - AC4: Playwright verification of pagination/expansion and density persistence

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

test.describe('Done column pagination and card density', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-done-pagination-e2e-'));
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

  async function seedProject(name: string): Promise<{ id: string }> {
    const res = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, actor: { name: 'e2e-seed', type: 'agent' } }),
    });
    expect(res.ok).toBe(true);
    const project = await res.json();
    return project;
  }

  async function seedTask(projectId: string, title: string): Promise<{ id: string; version: number }> {
    const res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: projectId,
        title,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(res.ok).toBe(true);
    const task = (await res.json()).task;
    return task;
  }

  async function setStatus(taskId: string, status: 'doing' | 'done'): Promise<void> {
    const getRes = await fetch(`${serverUrl}/api/v1/tasks/${taskId}`);
    const task = await getRes.json();
    if (status === 'done') {
      const resDoing = await fetch(`${serverUrl}/api/v1/tasks/${taskId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: 'e2e-seed', type: 'agent' },
          version: task.version,
          status: 'doing',
        }),
      });
      expect(resDoing.ok).toBe(true);
      const doingJson = await resDoing.json();
      const versionDoing = doingJson.task?.version ?? doingJson.version;
      const resDone = await fetch(`${serverUrl}/api/v1/tasks/${taskId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: 'e2e-seed', type: 'agent' },
          version: versionDoing,
          status: 'done',
        }),
      });
      expect(resDone.ok).toBe(true);
    } else {
      const res = await fetch(`${serverUrl}/api/v1/tasks/${taskId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: 'e2e-seed', type: 'agent' },
          version: task.version,
          status,
        }),
      });
      expect(res.ok).toBe(true);
    }
  }

  async function openProject(page: import('@playwright/test').Page, projectId: string): Promise<void> {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
  }

  test('AC1: Done column caps initial rendered cards to 20, supports Show more, Show all and Collapse', async ({ page }, testInfo) => {
    const project = await seedProject('Done Pagination Project');
    // Seed 2 todo tasks and 25 done tasks
    await seedTask(project.id, 'To Do Task 1');
    await seedTask(project.id, 'To Do Task 2');

    for (let i = 1; i <= 25; i++) {
      const t = await seedTask(project.id, `Completed Task ${String(i).padStart(2, '0')}`);
      await setStatus(t.id, 'done');
    }

    await openProject(page, project.id);

    // Locate the 3 lanes (To Do, Doing, Done)
    const lanes = page.locator('.col-lane');
    await expect(lanes).toHaveCount(3);
    const doneLane = lanes.nth(2);

    // Verify column header pill shows total count (25)
    const headerPill = doneLane.locator('header span.rounded-full').first();
    await expect(headerPill).toHaveText('25');

    // Verify Done column initially renders exactly 20 task cards
    const doneCards = doneLane.locator('article');
    await expect(doneCards).toHaveCount(20);

    // Verify pagination controls container
    const pagination = doneLane.getByTestId('done-pagination');
    await expect(pagination).toBeVisible();
    await expect(pagination).toContainText('Showing 20 of 25 done tasks');
    await expect(pagination).toContainText('(5 older hidden)');

    const showMoreBtn = doneLane.getByTestId('done-show-more');
    const showAllBtn = doneLane.getByTestId('done-show-all');
    const collapseBtn = doneLane.getByTestId('done-collapse');

    await expect(showMoreBtn).toBeVisible();
    await expect(showMoreBtn).toHaveText('Show more (5 older)');
    await expect(showAllBtn).toBeVisible();
    await expect(showAllBtn).toHaveText('Show all (25)');
    await expect(collapseBtn).toBeHidden();

    // Click "Show more (5 older)" -> expands all 25
    await showMoreBtn.click();
    await expect(doneCards).toHaveCount(25);
    await expect(showMoreBtn).toBeHidden();
    await expect(showAllBtn).toBeHidden();
    await expect(collapseBtn).toBeVisible();

    // Click "Collapse" -> collapses back to 20
    await collapseBtn.click();
    await expect(doneCards).toHaveCount(20);
    await expect(showMoreBtn).toBeVisible();
    await expect(showAllBtn).toBeVisible();
    await expect(collapseBtn).toBeHidden();

    // Click "Show all (25)" -> expands all 25
    await showAllBtn.click();
    await expect(doneCards).toHaveCount(25);
    await expect(collapseBtn).toBeVisible();

    // Collapse back to 20 again
    await collapseBtn.click();
    await expect(doneCards).toHaveCount(20);

    // Take evidence screenshot of capped Done column with pagination bar
    await page.screenshot({ path: path.join(testInfo.outputDir, 'done-pagination-capped.png') });
  });

  test('AC2 & AC3: Card density toggle switches Comfortable vs Compact, reduces card height, and persists across reloads', async ({ page }, testInfo) => {
    const project = await seedProject('Card Density Project');
    await seedTask(project.id, 'Spacious Task with Detailed Title for Density Testing');
    await openProject(page, project.id);

    const card = page.locator('article', { hasText: 'Spacious Task with Detailed Title' });
    await expect(card).toBeVisible();

    // Initial state: comfortable
    const densityToggle = page.getByTestId('card-density-toggle');
    await expect(densityToggle).toBeVisible();
    await expect(densityToggle).toContainText('Density: Comfortable');
    await expect(card).toHaveAttribute('data-card-density', 'comfortable');

    const comfortableBox = (await card.boundingBox())!;
    expect(comfortableBox.height).toBeGreaterThan(0);

    // Switch to Compact density via toolbar button
    await densityToggle.click();
    await expect(densityToggle).toContainText('Density: Compact');
    await expect(card).toHaveAttribute('data-card-density', 'compact');
    await expect(card).toHaveClass(/(^|\s)card-compact(\s|$)/);

    const compactBox = (await card.boundingBox())!;
    expect(compactBox.height, 'compact card height must be smaller than comfortable card height').toBeLessThan(comfortableBox.height);

    // Evidence screenshot in compact mode
    await page.screenshot({ path: path.join(testInfo.outputDir, 'card-density-compact.png') });

    // Verify persistence across page reload
    await page.reload();
    await expect(densityToggle).toContainText('Density: Compact');
    const reloadedCard = page.locator('article', { hasText: 'Spacious Task with Detailed Title' });
    await expect(reloadedCard).toHaveAttribute('data-card-density', 'compact');
    const reloadedBox = (await reloadedCard.boundingBox())!;
    expect(reloadedBox.height).toBeLessThan(comfortableBox.height);

    // Switch back to Comfortable via toolbar toggle button
    await densityToggle.click();
    await expect(densityToggle).toContainText('Density: Comfortable');
    await expect(card).toHaveAttribute('data-card-density', 'comfortable');
    const restoredBox = (await card.boundingBox())!;
    expect(restoredBox.height).toBeGreaterThan(compactBox.height);

    // Verify comfortable persistence across page reload
    await page.reload();
    await expect(densityToggle).toContainText('Density: Comfortable');
    const restoredCard = page.locator('article', { hasText: 'Spacious Task with Detailed Title' });
    await expect(restoredCard).toHaveAttribute('data-card-density', 'comfortable');

    // Evidence screenshot in comfortable mode
    await page.screenshot({ path: path.join(testInfo.outputDir, 'card-density-comfortable.png') });
  });
});
