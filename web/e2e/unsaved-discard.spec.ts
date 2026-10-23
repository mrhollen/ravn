// E2E coverage for "confirm before discarding unsaved drafts" (task
// 01M3MMZ188XJBGKFSWBGESK79D): the detail drawer, the New Task dialog and the
// Project Settings dialog must all ask before backdrop clicks / Escape / Cancel
// throw away unsaved work.
//
// Native-dialog mechanics (Playwright): a page with NO 'dialog' listener has
// every window.confirm() auto-DISMISSed (= "Cancel" = stay open) — so each test
// first proves the stay-open path WITHOUT a listener, then registers an
// accepting watcher to prove the accept-discard path. A clean surface must never
// fire a dialog at all (asserted by count).
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
      // Retry until server starts listening
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

async function getJson(url: string): Promise<any> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`GET ${url} failed: ${res.status}`);
  return res.json();
}

const SEED_ACTOR = { name: 'e2e-unsaved-discard-seed', type: 'agent' };

/** Count native dialogs and ACCEPT each one. Register this only AFTER a test
 *  has proven its no-listener (auto-dismiss = "stay") step, or before an
 *  explicit user-chosen discard action (Cancel button). */
function watchAndAccept(page: import('@playwright/test').Page): () => number {
  let count = 0;
  page.on('dialog', (d) => {
    count += 1;
    void d.accept();
  });
  return () => count;
}

test.describe('Unsaved-draft discard guards (01M3MMZ188XJBGKFSWBGESK79D)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
  let taskA: any;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-unsaved-discard-'));
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

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Discard guards E2E project',
      description: 'Seeded for the unsaved-draft discard spec',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    taskA = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Discard fixture task',
      description: 'Fixture for the discard-guard spec.',
      priority: 2,
      actor: SEED_ACTOR,
    })).task; // POST /tasks → { task, possible_duplicates }
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
    expect(fs.existsSync(tempDir)).toBe(false);
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

  /** Load the board and wait until the seeded card is clickable. */
  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expectProjectSelected(page, 'Discard guards E2E project');
    await expect(page.locator('article', { hasText: 'Discard fixture task' })).toBeVisible();
  }

  test('AC1: drawer with dirty NOTES — Escape prompts, dismiss keeps the draft, accept discards & closes', async ({ page }) => {
    await openBoard(page);
    const card = page.locator('article', { hasText: 'Discard fixture task' });
    await card.click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();

    // Dirty the notes scratchpad.
    const notes = page.locator('textarea[placeholder*="Last run"]');
    await notes.fill('wip note draft — must not be lost silently');

    // Escape (NO dialog listener, no native dialog on this path): the existing
    // in-app unsaved-notes confirm appears and the drawer stays open.
    await page.keyboard.press('Escape');
    const prompt = page.locator('#cb-notes-confirm');
    await expect(prompt).toBeVisible();
    await expect(wrapper).toBeVisible();

    // Dismissing the prompt ("keep editing") preserves the draft untouched.
    await page.keyboard.press('Escape');
    await expect(prompt).toBeHidden();
    await expect(wrapper).toBeVisible();
    await expect(notes).toHaveValue('wip note draft — must not be lost silently');

    // The draft is still dirty, so a further close attempt re-prompts…
    await page.keyboard.press('Escape');
    await expect(prompt).toBeVisible();

    // …and accepting ("Discard & close") drops the draft and closes the drawer.
    await prompt.getByRole('button', { name: 'Discard & close' }).click();
    await expect(wrapper).toBeHidden();
    const stored = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
    expect(stored.notes ?? '').toBe('', 'nothing was ever saved — server notes stay empty');
  });

  test('AC2: drawer with dirty TASK EDIT — Escape prompts; dismiss stays, accept discards the edit only', async ({ page }) => {
    await openBoard(page);
    const card = page.locator('article', { hasText: 'Discard fixture task' });
    await card.click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();

    // Enter edit mode and dirty the draft.
    const panel = page.locator('#cb-detail-drawer aside');
    await panel.getByRole('button', { name: 'Edit', exact: true }).first().click();
    const editForm = panel.locator('#task-edit-form');
    await expect(editForm).toBeVisible();
    const titleInput = editForm.locator('input').first();
    await titleInput.fill('Dirty WIP title');

    // Escape with NO listener: Playwright auto-dismisses the native confirm
    // (= "Cancel" = stay) — the app must remain in edit mode, draft intact.
    await page.keyboard.press('Escape');
    await expect(editForm).toBeVisible();
    await expect(titleInput).toHaveValue('Dirty WIP title');

    // Now accept: the next confirm is dismissed as accepted → cancelEditing()
    // exits edit mode WITHOUT closing the drawer (the edit form was topmost).
    const dialogs = watchAndAccept(page);
    await page.keyboard.press('Escape');
    expect(dialogs()).toBe(1, 'exactly one discard prompt fired');
    await expect(editForm).toBeHidden();
    await expect(wrapper).toBeVisible();
    await expect(panel.locator('h2').first()).toHaveText('Discard fixture task');

    // The edit was discarded, never persisted.
    const stored = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
    expect(stored.title).toBe('Discard fixture task');
  });

  test('AC2: drawer with dirty TASK EDIT — backdrop click confirms; accept closes the whole drawer', async ({ page }) => {
    await openBoard(page);
    const card = page.locator('article', { hasText: 'Discard fixture task' });
    await card.click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();

    const panel = page.locator('#cb-detail-drawer aside');
    await panel.getByRole('button', { name: 'Edit', exact: true }).first().click();
    const editForm = panel.locator('#task-edit-form');
    await expect(editForm).toBeVisible();
    await editForm.locator('input').first().fill('Dirty WIP title 2');

    // Backdrop click on a dirty drawer (acceptor registered up front: this test
    // exercises the accept-discard outcome directly).
    const dialogs = watchAndAccept(page);
    await page.locator('#cb-detail-drawer').click({ position: { x: 5, y: 5 } });
    expect(dialogs()).toBe(1, 'backdrop click on a dirty drawer must prompt');
    await expect(wrapper).toBeHidden();

    const stored = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
    expect(stored.title).toBe('Discard fixture task');
  });

  test('AC2: NEW TASK dialog with typed title — Escape stays, backdrop triggers inline discard, Discard closes', async ({ page }) => {
    await openBoard(page);
    const listUrl = `${serverUrl}/api/v1/tasks?project_id=${projectId}`;
    const before = (await getJson(listUrl)).total;

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();
    const titleInput = modal.locator('input').first();
    await titleInput.fill('WIP new task draft');

    // Escape with NO listener: auto-dismiss = stay — dialog and draft intact.
    await page.keyboard.press('Escape');
    await expect(modal).toBeVisible();
    await expect(titleInput).toHaveValue('WIP new task draft');

    // Backdrop click: native dialog is gone (0 prompts), inline confirmation appears.
    const dialogs = watchAndAccept(page);
    await page.locator('#cb-new-task').click({ position: { x: 5, y: 5 } });
    expect(dialogs()).toBe(0, 'native dialog is gone');
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeVisible();

    // Clicking Discard discards draft and closes modal.
    await modal.getByTestId('new-task-confirm-discard-btn').click();
    await expect(modal).toBeHidden();
    const after = (await getJson(listUrl)).total;
    expect(after).toBe(before, 'no task may have been created');
  });

  test('AC2: NEW TASK Cancel button on a dirty form triggers inline confirm; accept discards without native dialog', async ({ page }) => {
    await openBoard(page);
    const listUrl = `${serverUrl}/api/v1/tasks?project_id=${projectId}`;
    const before = (await getJson(listUrl)).total;

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();
    await modal.locator('input').first().fill('WIP via cancel button');

    // Explicit user-chosen discard: Cancel triggers inline confirmation without native dialog.
    const dialogs = watchAndAccept(page);
    await modal.getByTestId('new-task-cancel-btn').click();
    expect(dialogs()).toBe(0, 'native dialog is gone');
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeVisible();
    await modal.getByTestId('new-task-confirm-discard-btn').click();
    await expect(modal).toBeHidden();
    expect((await getJson(listUrl)).total).toBe(before);
  });

  test('AC2: PROJECT SETTINGS with changed name — Escape stays, backdrop accept discards', async ({ page }) => {
    await openBoard(page);

    await page.getByTestId('project-settings-btn').click();
    const modal = page.locator('#cb-project-settings');
    await expect(modal).toBeVisible();
    const nameInput = modal.locator('input').first();
    await nameInput.fill('WIP rename');

    // Escape with NO listener: auto-dismiss = stay — dialog and value intact.
    await page.keyboard.press('Escape');
    await expect(modal).toBeVisible();
    await expect(nameInput).toHaveValue('WIP rename');

    // Backdrop click, accepted: discards the change and closes.
    const dialogs = watchAndAccept(page);
    await page.locator('#cb-project-settings').click({ position: { x: 5, y: 5 } });
    expect(dialogs()).toBe(1, 'backdrop click on a dirty settings dialog must prompt');
    await expect(modal).toBeHidden();

    const stored = await getJson(`${serverUrl}/api/v1/projects/${projectId}`);
    expect(stored.name).toBe('Discard guards E2E project');
  });

  test('AC3: clean drawer close fires NO confirm dialog at all', async ({ page }) => {
    await openBoard(page);
    const card = page.locator('article', { hasText: 'Discard fixture task' });
    await card.click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();

    // Watcher from the start: a clean surface must never trigger a dialog.
    const dialogs = watchAndAccept(page);
    await page.keyboard.press('Escape');
    expect(dialogs()).toBe(0, 'clean drawer close must not prompt');
    await expect(wrapper).toBeHidden();
  });

  test('AC3: clean NEW TASK and PROJECT SETTINGS closes fire no dialog', async ({ page }) => {
    await openBoard(page);
    const dialogs = watchAndAccept(page);

    // New task, untouched.
    await page.getByRole('button', { name: '+ New task' }).click();
    await expect(page.locator('#cb-new-task')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('#cb-new-task')).toBeHidden();

    // Project settings, untouched.
    await page.getByTestId('project-settings-btn').click();
    await expect(page.locator('#cb-project-settings')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('#cb-project-settings')).toBeHidden();

    expect(dialogs()).toBe(0, 'clean closes must never prompt');
  });
});
