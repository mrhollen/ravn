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

test.describe('Web E2E test suite', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    // 1. Ensure static dist is built
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }

    // 2. Ensure server binary is built
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    // 3. Ephemeral temp DB directory (acceptance criterion 3)
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-'));
    const dbPath = path.join(tempDir, 'crowban.db');

    // 4. Free high-numbered port (never :8080) (acceptance criterion 3)
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);

    serverUrl = `http://127.0.0.1:${serverPort}`;

    // 5. Boot server with ephemeral DB and UI static dir
    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '', // Zero-config local mode
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);
  });

  test.afterAll(async () => {
    // Clean shutdown of background server and removal of temp DB
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
    // Verify cleanup
    expect(fs.existsSync(tempDir)).toBe(false);
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
    // Fail on any uncaught console error or unhandled page error (acceptance criterion 2)
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  test('seeds via API, renders board, creates task, moves task, and posts comment via UI', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed project and tasks via API (acceptance criterion 1)
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Browser E2E Project',
        description: 'Seeded via API for browser E2E test',
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
        title: 'Task To Move',
        description: 'Seeded task that will be moved via real UI flow',
        priority: 2,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const taskData = await taskRes.json();
    const initialTask = taskData.task;
    expect(initialTask?.id).toBeTruthy();

    // -------------------------------------------------------------------------
    // Step 2: Load real UI and assert board render
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');

    // Header and project dropdown should reflect seeded project
    const projectSelect = page.locator('select').first();
    await expect(projectSelect).toBeVisible();
    await expect(projectSelect).toHaveValue(project.id);

    // Columns should be present
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const doingCol = page.locator('section', { has: page.getByRole('heading', { name: 'Doing' }) });
    const doneCol = page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });

    await expect(todoCol).toBeVisible();
    await expect(doingCol).toBeVisible();
    await expect(doneCol).toBeVisible();

    // Seeded task card must render in "To do" column
    const initialCard = todoCol.locator('h3', { hasText: 'Task To Move' });
    await expect(initialCard).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 3: Create task flow via real UI
    // -------------------------------------------------------------------------
    const newTaskBtn = page.getByRole('button', { name: '+ New task' });
    await expect(newTaskBtn).toBeEnabled();
    await newTaskBtn.click();

    // Modal opens
    const titleInput = page.getByPlaceholder('Fix login bug');
    await expect(titleInput).toBeVisible();
    await titleInput.fill('UI Created Task');

    const descInput = page.getByPlaceholder('Users cannot log in after the password reset flow…');
    await descInput.fill('Task created directly through the user interface.');

    const createBtn = page.getByRole('button', { name: 'Create task' });
    await createBtn.click();

    // Modal closes and newly created card renders in To do
    await expect(titleInput).not.toBeVisible();
    const createdCard = todoCol.locator('h3', { hasText: 'UI Created Task' });
    await expect(createdCard).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 4: Move task flow via real UI (acceptance criterion 1)
    // -------------------------------------------------------------------------
    // Click initial task card to open task detail drawer
    await initialCard.click();

    // Detail drawer opens with title
    const drawerTitle = page.locator('aside h2', { hasText: 'Task To Move' });
    await expect(drawerTitle).toBeVisible();

    // Move task to "Doing" via the status button group
    const statusGroup = page.getByRole('group', { name: 'Status' });
    const doingBtn = statusGroup.getByRole('button', { name: 'Doing' });
    await doingBtn.click();

    // Verify status button shows active
    await expect(doingBtn).toHaveAttribute('aria-pressed', 'true');

    // Close detail drawer
    const closeBtn = page.locator('aside button[aria-label="Close"]').first();
    await closeBtn.click();
    await expect(drawerTitle).not.toBeVisible();

    // Assert card is now moved to "In progress" column
    const movedCard = doingCol.locator('h3', { hasText: 'Task To Move' });
    await expect(movedCard).toBeVisible();
    await expect(todoCol.locator('h3', { hasText: 'Task To Move' })).not.toBeVisible();

    // -------------------------------------------------------------------------
    // Step 5: Post comment flow via real UI (acceptance criterion 1)
    // -------------------------------------------------------------------------
    // Reopen task drawer for the moved card
    await movedCard.click();
    await expect(drawerTitle).toBeVisible();

    // Post a comment
    const commentInput = page.getByPlaceholder('Markdown… posted as a typed comment');
    await expect(commentInput).toBeVisible();
    const testComment = 'Automated verification comment posted via browser E2E test';
    await commentInput.fill(testComment);

    const postCommentBtn = page.getByRole('button', { name: 'Post comment' });
    await postCommentBtn.click();

    // Verify comment appears in the drawer's comment thread
    const commentArticle = page.locator('article', { hasText: testComment });
    await expect(commentArticle).toBeVisible();

    // Close detail drawer
    await closeBtn.click();
    await expect(drawerTitle).not.toBeVisible();
  });
});
