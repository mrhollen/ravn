// E2E coverage for the "Expired / Stalled" board capability (epic child
// 01M5XSNR8VWZTP66GT7ABNSJC1, frontend half): a toolbar alert pill counting
// doing tasks with a lapsed claim lease or >60 min of idleness, an
// "Expired / stalled" toggle in the filter bar that restricts the board to
// exactly that set, URL deep-linking (?stalled=1), and Reset-filters clearing.
//
// Real server state, no mocking: task A is claimed with the default 15-minute
// TTL (active throughout), task B with ttl_sec=1 — a genuine lease that
// expires one second later (same technique as claim-expired.spec.ts) — and a
// todo bystander that can never be stalled. Each test drives the real UI
// against its own ephemeral ravn-server on a random high port (never :8080).

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
      // retry until the server is listening
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

const ACTOR = { name: 'e2e-seed', type: 'agent' as const };
const EXPIRED_ACTOR = { name: 'e2e-expired', type: 'agent' as const };

const HEALTHY_TITLE = 'Healthy Doing Task';
const STALLED_TITLE = 'Stalled Doing Task';
const TODO_TITLE = 'Todo Bystander Task';

test.describe('Expired/stalled board alert and filter (01M5XSNR8VWZTP66GT7ABNSJC1)', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-stalled-e2e-'));
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
      if (msg.type() === 'error') uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`));
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  async function postJson(url: string, body: unknown): Promise<any> {
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(`POST ${url} → ${res.status}`);
    return res.json();
  }

  /** Seed the shared project with one healthy doing task (default TTL lease),
   *  one stalled doing task (ttl_sec=1 lease, already lapsed by the time the
   *  page loads), and one todo bystander. Returns the project row. */
  async function seedStalledProject(): Promise<{ id: string }> {
    const project = await postJson(`${serverUrl}/api/v1/projects`, {
      name: `Stalled E2E ${Date.now()}`,
      description: 'Seeded for the expired/stalled filter spec',
      actor: ACTOR,
    });

    // A: default 15-minute lease → ACTIVE for the whole suite.
    const healthy = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: HEALTHY_TITLE,
      description: 'Seeded healthy doing task',
      priority: 2,
      actor: ACTOR,
    })).task;
    const claimA = await fetch(`${serverUrl}/api/v1/tasks/${healthy.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: ACTOR }),
    });
    expect(claimA.ok).toBe(true);

    // B: ttl_sec=1 lease → genuinely EXPIRED ~1s after the claim response.
    const stalled = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: STALLED_TITLE,
      description: 'Seeded stalled doing task',
      priority: 2,
      actor: ACTOR,
    })).task;
    const claimB = await fetch(`${serverUrl}/api/v1/tasks/${stalled.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: EXPIRED_ACTOR, ttl_sec: 1 }),
    });
    expect(claimB.ok).toBe(true);
    const leaseB = await claimB.json();
    expect(leaseB.claimed_by?.name).toBe(EXPIRED_ACTOR.name);

    // Wait until B's lease has truly crossed its expiry (small margin avoids
    // flakiness on the exact boundary instant).
    while (Date.now() < Date.parse(leaseB.claim_expires_at) + 250) {
      await new Promise((r) => setTimeout(r, 50));
    }

    // C: plain todo — never claimable into "stalled" by status alone.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: TODO_TITLE,
      priority: 2,
      actor: ACTOR,
    });

    return project;
  }

  async function openProject(page: import('@playwright/test').Page, projectId: string): Promise<void> {
    await page.goto(serverUrl);
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeEnabled();
    await trigger.click();
    const option = page.getByTestId('project-menu').locator(`[data-project-id="${projectId}"]`);
    await expect(option).toBeVisible();
    await option.click();
  }

  test('pill counts only the expired lease, toggle filters to it, Reset restores all', async ({ page }) => {
    const project = await seedStalledProject();
    await openProject(page, project.id);

    const healthyCard = page.locator('article', { hasText: HEALTHY_TITLE });
    const stalledCard = page.locator('article', { hasText: STALLED_TITLE });
    const todoCard = page.locator('article', { hasText: TODO_TITLE });
    await expect(healthyCard).toBeVisible({ timeout: 10_000 });
    await expect(stalledCard).toBeVisible();
    await expect(todoCard).toBeVisible();

    // ---------------------------------------------------------------- (a) pill
    const pill = page.getByTestId('stalled-alert-pill');
    await expect(pill).toBeVisible();
    // Exact count: exactly ONE stalled task (the expired lease) — the healthy
    // doing task and the todo bystander must not be counted.
    await expect(pill).toHaveText(/1\s*stalled/);
    await expect(pill.getByText('1', { exact: true })).toBeVisible();
    // Accessible title per AC: "N abandoned/stalled task(s)".
    await expect(pill).toHaveAttribute('aria-label', '1 abandoned/stalled task');
    await expect(pill).toHaveAttribute('title', '1 abandoned/stalled task');
    // Inactive filter state at rest.
    await expect(pill).toHaveAttribute('aria-pressed', 'false');

    // ------------------------------------------------------ (b) toggle ON via checkbox
    const stalledCheckbox = page.getByTestId('filter-stalled');
    await expect(stalledCheckbox).toBeVisible();
    await expect(stalledCheckbox).not.toBeChecked();
    await stalledCheckbox.check();

    // Exactly the stalled set stays visible; healthy + todo are hidden.
    await expect(stalledCard).toBeVisible();
    await expect(healthyCard).not.toBeVisible();
    await expect(todoCard).not.toBeVisible();
    await expect(page.locator('article')).toHaveCount(1);
    // The pill mirrors the active state via aria-pressed…
    await expect(pill).toHaveAttribute('aria-pressed', 'true');
    // …and the filter is deep-linkable: ?stalled=1 lands in the URL.
    await expect.poll(() => page.url()).toContain('stalled=1');

    // Screenshot evidence: amber pill + stalled-only board state.
    const shotDir = path.resolve(__dirname, '../test-results');
    fs.mkdirSync(shotDir, { recursive: true });
    await page.screenshot({ path: path.join(shotDir, 'stalled-filter-active.png') });

    // ------------------------------------------------- keyboard: toggle OFF via pill
    // The pill is a native button — focus + Enter must work (WCAG 2.1.1).
    await pill.focus();
    await page.keyboard.press('Enter');
    await expect(pill).toHaveAttribute('aria-pressed', 'false');
    await expect(stalledCheckbox).not.toBeChecked();
    await expect(healthyCard).toBeVisible();
    await expect(todoCard).toBeVisible();
    await expect.poll(() => page.url()).not.toContain('stalled=1');

    // -------------------------------------------------------- (c) Reset filters
    await stalledCheckbox.check();
    const resetBtn = page.getByRole('button', { name: 'Reset filters' });
    await expect(resetBtn).toBeVisible();
    await resetBtn.click();
    await expect(stalledCheckbox).not.toBeChecked();
    // Both doing tasks (and the todo) are restored; URL is clean.
    await expect(healthyCard).toBeVisible();
    await expect(stalledCard).toBeVisible();
    await expect(todoCard).toBeVisible();
    await expect(page.locator('article')).toHaveCount(3);
    await expect.poll(() => page.url()).not.toContain('stalled=1');
  });

  test('deep link ?stalled=1 initializes the filter to the stalled set', async ({ page }) => {
    const project = await seedStalledProject();

    // Direct load with the filter parameter — no clicks involved.
    await page.goto(`${serverUrl}/?project=${project.id}&stalled=1`);
    const triggerLabel = page.getByTestId('project-select');
    await expect(triggerLabel).toContainText('Stalled E2E', { timeout: 10_000 });

    const healthyCard = page.locator('article', { hasText: HEALTHY_TITLE });
    const stalledCard = page.locator('article', { hasText: STALLED_TITLE });
    await expect(stalledCard).toBeVisible();
    await expect(healthyCard).not.toBeVisible();

    // The checkbox and pill reflect the restored state.
    await expect(page.getByTestId('filter-stalled')).toBeChecked();
    const pill = page.getByTestId('stalled-alert-pill');
    await expect(pill).toHaveAttribute('aria-pressed', 'true');
  });

  test('pill stays hidden on a project with no stalled tasks', async ({ page }) => {
    // Fresh project with only a todo task — zero stalled-expired by definition.
    const project = await postJson(`${serverUrl}/api/v1/projects`, {
      name: `Stalled Free E2E ${Date.now()}`,
      actor: ACTOR,
    });
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Fresh Todo Only Task',
      priority: 2,
      actor: ACTOR,
    });
    await openProject(page, project.id);
    await expect(page.locator('article')).toHaveCount(1);

    // Hidden — not merely zero-labeled.
    const pill = page.getByTestId('stalled-alert-pill');
    await expect(pill).toBeHidden();
  });
});
