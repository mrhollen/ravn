// Blocked-state cache scoping across project switches
// (task 01M3MMZASQB2RBB2Q4RS3GM0JK).
//
// The card "blocked" badge is backed by client-side caches (derived board diff
// + detail-fetched knowledge) that are scoped to the project they were derived
// under. This spec exercises the real UI in a single SPA session: seed project
// A with an open blocked-by relation, verify the badge there, switch to
// project B and assert no card shows a blocked indicator (and a drawer opened
// on a B task is clean), then switch back to A and assert the badge returns —
// proving the reset does not over-clear.
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

test.describe('Blocked-state cache never leaks across a project switch', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;

  let projA: any;
  let projB: any;
  let projC: any;
  let blockedTaskA: any; // has an open blocked-by relation
  let blockerTaskA: any;
  let plainTaskB1: any;
  let plainTaskB2: any;
  let blockedTaskC104: any;

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-blocked-cache-'));
    const dbPath = path.join(tempDir, 'ravn.db');
    serverPort = await getFreePort();
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

    // Project A: one task blocked-by another.
    projA = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Blocked Cache Alpha', actor: ACTOR });
    blockerTaskA = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'AC Blocker Task',
      priority: 1,
      actor: ACTOR,
    })).task;
    blockedTaskA = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'AC Blocked Task',
      priority: 2,
      actor: ACTOR,
    })).task;
    await postJson(`${serverUrl}/api/v1/tasks/${blockedTaskA.id}/relations`, {
      other_id: blockerTaskA.id,
      kind: 'blocked-by',
      actor: ACTOR,
    });

    // Project B: plain tasks with no relations.
    projB = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Blocked Cache Beta', actor: ACTOR });
    plainTaskB1 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projB.id,
      title: 'Beta Plain Task One',
      priority: 1,
      actor: ACTOR,
    })).task;
    plainTaskB2 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projB.id,
      title: 'Beta Plain Task Two',
      priority: 2,
      actor: ACTOR,
    })).task;

    // Project C: >100 tasks (105 tasks) where task 104 is blocked by task 0.
    projC = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Blocked Scale Gamma', actor: ACTOR });
    const cTasks: any[] = [];
    for (let i = 0; i < 105; i++) {
      const created = (await postJson(`${serverUrl}/api/v1/tasks`, {
        project_id: projC.id,
        title: i === 0 ? 'Gamma Blocker Root' : i === 104 ? 'Gamma Blocked High Index' : i === 5 ? 'Gamma Target Five' : `Gamma Task ${i}`,
        priority: 3,
        actor: ACTOR,
      })).task;
      cTasks.push(created);
    }
    blockedTaskC104 = cTasks[104];
    await postJson(`${serverUrl}/api/v1/tasks/${cTasks[104].id}/relations`, {
      other_id: cTasks[0].id,
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

  test('project B shows no blocked indicator from project A; the badge returns on switch back', async ({ page }) => {
    await page.goto(serverUrl);

    // --- Project A: baseline — the blocked badge IS shown (feature works). --
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, 'Blocked Cache Alpha');

    const cardA = page.locator('article', { hasText: 'AC Blocked Task' });
    await expect(cardA).toBeVisible();
    // Badge appears once refreshBlockedSet's board diff has run.
    await expect(cardA.getByText('blocked', { exact: true })).toBeVisible();

    // --- Switch to Project B in the SAME SPA session (no reload). ----------
    await selectProjectViaDropdown(page, projB.id);
    await expectProjectSelected(page, 'Blocked Cache Beta');

    const cardB1 = page.locator('article', { hasText: 'Beta Plain Task One' });
    const cardB2 = page.locator('article', { hasText: 'Beta Plain Task Two' });
    await expect(cardB1).toBeVisible();
    await expect(cardB2).toBeVisible();

    // No card in B may carry a blocked indicator based on A's tasks (AC3).
    await expect(cardB1.getByText('blocked', { exact: true })).not.toBeVisible();
    await expect(cardB2.getByText('blocked', { exact: true })).not.toBeVisible();

    // A drawer opened on a B task is clean: no "Blocked by:" banner.
    await cardB1.click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByText('Beta Plain Task One')).toBeVisible();
    await expect(drawer.getByText('Blocked by:')).not.toBeVisible();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    // --- Switch back to A in the same session. ------------------------------
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, 'Blocked Cache Alpha');

    const cardA2 = page.locator('article', { hasText: 'AC Blocked Task' });
    await expect(cardA2).toBeVisible();
    // The badge is re-derived for A after the round-trip — the reset on switch
    // must not over-clear (AC1 + feature intact).
    await expect(cardA2.getByText('blocked', { exact: true })).toBeVisible();

    // …and it points at the right blocker inside the drawer too.
    await cardA2.click();
    const drawerA = page.locator('#cb-detail-drawer');
    await expect(drawerA).toBeVisible();
    await expect(drawerA.getByText('Blocked by:')).toBeVisible();
  });

  test('board load fires zero list_tasks requests and >100 task project accurately renders blocked badges and filters (AC1-AC3)', async ({ page }, testInfo) => {
    const listTaskRequests: string[] = [];
    page.on('request', (req) => {
      const url = req.url();
      if (url.includes('/api/v1/tasks?') && !url.includes('/tasks/')) {
        listTaskRequests.push(url);
      }
    });

    await page.goto(serverUrl);

    // Switch to Project C (>100 tasks)
    await selectProjectViaDropdown(page, projC.id);
    await expectProjectSelected(page, 'Blocked Scale Gamma');

    // AC1: loadBoard fires zero list_tasks queries (no redundant 2x listTasks polling)
    expect(listTaskRequests.length, 'zero listTasks requests on board load').toBe(0);

    // AC2/AC3: Blocked task 104 accurately renders the blocked badge even on >100 task board
    const blockedCard = page.locator('article', { hasText: 'Gamma Blocked High Index' });
    await expect(blockedCard).toBeVisible();
    await expect(blockedCard.getByText('blocked', { exact: true })).toBeVisible();

    // Verify unblocked card is visible
    const unblockedCard = page.locator('article', { hasText: 'Gamma Target Five' });
    await expect(unblockedCard).toBeVisible();

    // Take screenshot evidence of the blocked card on the >100 task board
    await page.screenshot({ path: path.join(testInfo.outputDir, 'blocked-over-100-tasks.png') });

    // AC3: Click "Unblocked only" filter: blocked task 104 is hidden, unblocked task 5 remains visible
    const unblockedFilter = page.getByTestId('filter-unblocked');
    await unblockedFilter.check();
    await expect(blockedCard).not.toBeVisible();
    await expect(unblockedCard).toBeVisible();

    // Uncheck filter: blocked task 104 returns
    await unblockedFilter.uncheck();
    await expect(blockedCard).toBeVisible();
  });
});
