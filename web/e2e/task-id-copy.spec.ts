import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import { randomUUID } from 'crypto';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

// E2E coverage for the "click to copy task ID" feature: clicking the truncated
// task-ID badge in the detail drawer header copies the FULL id to the clipboard.
// Mirrors the ephemeral-server scaffolding of board.spec.ts / theme.spec.ts.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
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

/** Seed a project + one task via the API; returns { projectId, taskId }.
 *  The server enforces unique project names, so use a unique name per call. */
async function seedTask(serverUrl: string): Promise<{ projectId: string; taskId: string }> {
  const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: `Copy-ID E2E ${randomUUID()}`,
      description: 'Seeded for click-to-copy e2e tests',
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
      title: 'Task for Copy ID',
      description: 'Its truncated ID badge should copy the full id on click',
      priority: 2,
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(taskRes.ok).toBe(true);
  const task = await taskRes.json();
  return { projectId: project.id, taskId: task.task.id };
}

test.describe('click-to-copy task ID badge', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-copyid-e2e-'));
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

  test('clicking the badge copies the full task ID to the clipboard', async ({ page, context }) => {
    const { projectId, taskId } = await seedTask(serverUrl);

    // Clipboard access from readText() back below needs explicit permission.
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const taskCard = page.locator('article', { hasText: 'Task for Copy ID' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Task for Copy ID' })).toBeVisible();

    // The badge renders the TRUNCATED id, with a copy affordance in its tooltip.
    const badge = drawer.locator('[data-testid="task-id-badge"]');
    await expect(badge).toBeVisible();
    expect(taskId.length).toBeGreaterThan(10); // ULIDs are always truncated
    await expect(badge).toHaveText(`${taskId.slice(0, 8)}…`);
    await expect(badge).toHaveAttribute('title', `Copy full ID: ${taskId}`);

    // Put a sentinel in the clipboard so the copy must actually happen to change it.
    await page.evaluate((sentinel) => navigator.clipboard.writeText(sentinel), 'not-copied-yet');

    await badge.click();

    // Success toast appears (the app's standard feedback channel).
    const toast = page.locator('div[role="status"], .fixed', { hasText: 'Copied task ID to clipboard' });
    await expect(toast.first()).toBeVisible({ timeout: 5000 });

    // The FULL id — not the truncated one — is now on the clipboard.
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(copied).toBe(taskId);
  });
});
