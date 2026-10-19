// E2E regression coverage for first-visit auth-token auto-select:
// On a first visit (no stored token) to a TOKEN-REQUIRED server, saving a valid
// token in the auth modal must leave the board immediately usable:
//   AC1 — the first project (canonical server return order) is auto-selected
//         and its board loads WITHOUT any manual interaction;
//   AC2 — manual selection via the Projects dropdown still works afterwards;
//   AC2 — a zero-project server keeps the empty state (no phantom selection).
// Self-contained: each test spins up its OWN ephemeral crowban-server on a
// random high port (never :8080) with a fresh temp DB in TOKEN-REQUIRED mode
// (mirrors board.spec.ts's spawn pattern — plus CROWBAN_TOKEN set to a real
// value, and every REST seed call carries the Bearer header). Per-test server
// restart is required because the three cases need mutually exclusive server
// state (1 project / 2 projects / none); browser-side isolation comes for free
// from Playwright's default per-test context (no stored crowban_token).
import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { expectProjectSelected, selectProjectViaDropdown } from './helpers';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

// A real non-empty token: the server runs in TOKEN-REQUIRED mode, so a fresh
// visitor's boot fetch gets 401 and the auth modal opens.
const TOKEN = 'super-secret-e2e-token-12345';

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

test.describe('First-visit auth token auto-select', () => {
  let serverProcess: ChildProcess | null = null;
  let tempDir = '';
  let serverPort: number;
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    // Ensure static dist is built (once for the whole suite)
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }
    // Ensure server binary is built
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }
  });

  // One fresh ephemeral server + temp DB per test (see file header for why the
  // cases cannot share a single DB).
  test.beforeEach(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-first-visit-'));
    const dbPath = path.join(tempDir, 'crowban.db');

    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    // Boot server in TOKEN-REQUIRED mode with ephemeral DB and UI static dir
    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: TOKEN,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);
  });

  test.afterEach(async () => {
    // Fail on any uncaught console error or unhandled page error
    expect(uncaughtConsoleErrors).toEqual([]);
    // Clean shutdown of this test's background server and removal of its temp DB
    if (serverProcess) {
      serverProcess.kill('SIGTERM');
      await new Promise<void>((resolve) => {
        const proc = serverProcess!;
        const timeout = setTimeout(() => {
          try {
            proc.kill('SIGKILL');
          } catch {}
          resolve();
        }, 2000);
        proc.on('exit', () => {
          clearTimeout(timeout);
          resolve();
        });
      });
      serverProcess = null;
    }
    if (tempDir && fs.existsSync(tempDir)) {
      fs.rmSync(tempDir, { recursive: true, force: true });
    }
  });

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        // Every first visit in this suite boot-fetches /api/v1/projects with NO
        // stored token against a token-required server → 401. Chrome logs that
        // HTTP error as a resource error. Expected noise, not an app bug (the
        // app handles it by opening the auth modal).
        if (msg.text().includes('status of 401')) return;
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  // --- seed helpers (auth is REQUIRED: every call carries the Bearer token) ---

  const authHeaders = { 'Content-Type': 'application/json', Authorization: `Bearer ${TOKEN}` };

  async function seedProject(name: string, description = ''): Promise<{ id: string; name: string }> {
    const res = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify({
        name,
        ...(description ? { description } : {}),
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(res.ok).toBe(true);
    return res.json();
  }

  async function seedTask(projectId: string, title: string): Promise<{ id: string }> {
    const res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify({
        project_id: projectId,
        title,
        priority: 2,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(res.ok).toBe(true);
    const data = await res.json();
    return data.task;
  }

  /** Canonical server order of projects — the same list+order the browser app
   *  receives. Used to derive which project SHOULD be auto-selected without
   *  assuming creation order. */
  async function canonicalProjects(): Promise<Array<{ id: string; name: string }>> {
    const res = await fetch(`${serverUrl}/api/v1/projects`, { headers: authHeaders });
    expect(res.ok).toBe(true);
    const data = await res.json();
    return data.projects;
  }

  // --- shared first-visit driver ---------------------------------------------

  /** Fresh visitor → boot 401 opens the auth modal → save TOKEN. Playwright's
   *  default `page` fixture is a brand-new browser context per test, so
   *  localStorage (crowban_token) starts unset in every case below. */
  async function firstVisitSaveToken(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');

    const authModal = page.locator('#cb-auth-modal');
    await expect(authModal).toBeVisible(); // auto-opened by the 401 during boot
    await authModal.locator('input[type="password"]').fill(TOKEN);
    await authModal.getByRole('button', { name: 'Save token' }).click();
    await expect(authModal).toBeHidden();
  }

  // ---------------------------------------------------------------------------

  test('AC1: first-visit token save auto-selects the first project and loads its board with no manual interaction', async ({
    page,
  }) => {
    const project = await seedProject('AutoSelect Project');
    const task = await seedTask(project.id, 'First Visit Task');

    // Fresh context (no stored token) → auth modal opens on the boot 401…
    await firstVisitSaveToken(page);

    // …and saving the token auto-selected the project WITHOUT touching the
    // dropdown: the trigger is enabled and shows it (name + task counts).
    await expectProjectSelected(page, 'AutoSelect Project');

    // The board actually loaded: the seeded task renders in "To Do".
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const card = todoCol.locator('h3', { hasText: 'First Visit Task' });
    await expect(card).toBeVisible();

    // The 401-boot error overlay from the first visit is gone (cleared now that
    // projects loaded), so nothing hides the freshly auto-selected board.
    await expect(page.getByTestId('boot-error')).not.toBeVisible();
  });

  test('AC2: manual project selection still works after first-visit auto-select', async ({ page }) => {
    const pA = await seedProject('Manual Switch Project A');
    await seedTask(pA.id, 'Alpha Board Task');
    const pB = await seedProject('Manual Switch Project B');
    await seedTask(pB.id, 'Beta Board Task');

    // Canonical server order — do NOT assume creation order.
    const canonical = await canonicalProjects();
    expect(canonical).toHaveLength(2);
    const first = canonical[0];
    const other = canonical.find((p) => p.id !== first.id)!;
    const taskTitle: Record<string, string> = {
      [pA.id]: 'Alpha Board Task',
      [pB.id]: 'Beta Board Task',
    };

    await firstVisitSaveToken(page);

    // The canonical FIRST project is auto-selected and its board loaded…
    await expectProjectSelected(page, first.name);
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    await expect(todoCol.locator('h3', { hasText: taskTitle[first.id] })).toBeVisible();

    // …and a MANUAL switch to the other project still works through the real
    // dropdown: it becomes selected and its board loads instead.
    await selectProjectViaDropdown(page, other.id);
    await expectProjectSelected(page, other.name);
    await expect(todoCol.locator('h3', { hasText: taskTitle[other.id] })).toBeVisible();
    // The auto-selected project's card is gone — the board tracked the switch.
    await expect(page.locator('h3', { hasText: taskTitle[first.id] })).not.toBeVisible();
  });

  test('AC2: zero-project server keeps the empty state after token save (no phantom selection)', async ({
    page,
  }) => {
    // Deliberately seed NO projects on this (token-required) server.

    await firstVisitSaveToken(page);

    // Nothing got auto-selected — the trigger falls back to its placeholder…
    await expect(page.getByTestId('project-select')).toContainText('Select a project…');
    // …the genuine empty state renders, and no board cards are drawn at all.
    await expect(page.getByText('No projects yet.', { exact: true })).toBeVisible();
    await expect(page.locator('#cb-new-project-name-input')).toBeVisible(); // New-project bar open (showNewProject)
    await expect(page.locator('main article')).toHaveCount(0);

    // The stale boot-error overlay from the 401 is gone too.
    await expect(page.getByTestId('boot-error')).not.toBeVisible();
  });
});
