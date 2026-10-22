import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

// E2E coverage for the full-board "filtered empty state" (Rävn task
// 01M3MMXFXNYK8VS82HHA07VHGT): a banner across the columns when active filters
// hide every task while the project still has some (AC1), its one-click Reset
// button (AC2), and the hidden-task count with correct pluralization (AC3). A
// truly empty project must keep its plain empty look (no banner), so that state
// is asserted too. Mirrors the ephemeral-server scaffolding of board.spec.ts —
// each test seeds its own project + tasks via REST, then drives the real UI.

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

test.describe('filtered empty state', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-filtered-empty-e2e-'));
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

  /** Seed a project via REST and return its row. */
  async function seedProject(name: string): Promise<{ id: string }> {
    const res = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, actor: { name: 'e2e-seed', type: 'agent' } }),
    });
    expect(res.ok).toBe(true);
    const project = await res.json();
    expect(project.id).toBeTruthy();
    return project;
  }

  /** Seed one task into `projectId` via REST and return its row. */
  async function seedTask(projectId: string, title: string): Promise<{ id: string }> {
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
    expect(task?.id).toBeTruthy();
    return task;
  }

  /** Open the app and select the freshly seeded project via the real dropdown. */
  async function openProject(page: import('@playwright/test').Page, projectId: string): Promise<void> {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
  }

  const nonsenseSearch = 'zzz-no-such-task-xyz';

  test('shows the filtered empty state when search hides all tasks', async ({ page }) => {
    const project = await seedProject('Filtered Empty Project');
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await seedTask(project.id, t);
    }
    await openProject(page, project.id);

    // Sanity: all three cards start visible (banner must be absent pre-filter).
    const banner = page.locator('[data-testid="filtered-empty-state"]');
    await expect(banner).toBeHidden();
    await expect(page.locator('article')).toHaveCount(3);

    // A search matching nothing hides every card in every column.
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    // AC1: the dedicated banner renders across the columns with its wording…
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute('role', 'status');
    await expect(banner).toContainText('No tasks match the active filters');
    // …and AC3: it reports the total number of hidden tasks (all 3 seeded).
    await expect(banner.getByTestId('filtered-empty-count')).toContainText('3 tasks hidden');

    // The per-column placeholders stay in place beneath the banner.
    await expect(page.getByText('No tasks', { exact: true })).toHaveCount(3);
  });

  test('reset button clears filters and reveals tasks', async ({ page }) => {
    const project = await seedProject('Reset Filters Project');
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await seedTask(project.id, t);
    }
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);
    await expect(banner).toBeVisible();

    // AC2: the reset button INSIDE the empty state clears every filter in one click.
    await banner.getByTestId('filtered-empty-reset').click();

    await expect(banner).toBeHidden();
    await expect(searchInput).toHaveValue('');
    // All three seeded cards (todo status) are visible again.
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await expect(page.locator('article', { hasText: t })).toBeVisible();
    }
    await expect(page.locator('article')).toHaveCount(3);
  });

  test('does not show the banner on a truly empty project', async ({ page }) => {
    // Project with zero tasks — active filters still change nothing visible,
    // and the board must keep its plain empty-project look (no banner).
    const project = await seedProject('Truly Empty Project');
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    await expect(banner).toBeHidden();
    // Plain per-column placeholders are what the user sees.
    await expect(page.getByText('No tasks', { exact: true })).toHaveCount(3);
  });

  test('reports a singular count for exactly one hidden task', async ({ page }) => {
    const project = await seedProject('Singular Count Project');
    await seedTask(project.id, 'Lone Seeded Task');
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    // AC3 (singular form): "1 task hidden", not "1 tasks hidden".
    await expect(banner).toBeVisible();
    const countSpan = banner.getByTestId('filtered-empty-count');
    await expect(countSpan).toContainText('1 task hidden');
    // The plural 's' span stays display:none, so the plural phrasing is absent.
    await expect(banner).not.toHaveText(/tasks hidden/);
  });
});
