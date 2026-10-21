import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { expectProjectSelected } from './helpers';

// "Write / Preview" toggle + draft auto-save for the two markdown textareas in
// the task-detail drawer (description in edit mode, comment always). Verifies:
//   1. Preview renders SANITIZED markdown via the shared .md-body/md() pipeline
//      (bold survives; <script>/<img> are stripped).
//   2. Comment drafts auto-save to localStorage (debounced) and survive close,
//      re-open AND a full page reload — and never leak across tasks.
//   3. The draft key is REMOVED only on submit success (postComment).
// Self-contained like the other specs: ephemeral ravn-server on a random high
// port (never :8080), temp DB, pre-built dist.

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

const SEED_ACTOR = { name: 'e2e-draft-seed', type: 'agent' };

// The exact localStorage keys the app writes (mirrors src/drafts.ts).
const commentDraftKey = (taskId: string) => `ravn.draft.task.${taskId}.comment`;
const descriptionDraftKey = (taskId: string) => `ravn.draft.task.${taskId}.description`;

test.describe('Write/Preview toggle + draft auto-save', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
  let fixtureTask: any; // plain fixture (no ACs) — keeps the drawer's "Edit" button unique
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-draft-'));
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
      name: 'Draft Preview E2E project',
      description: 'Seeded for the write/preview + draft auto-save spec',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    // Plain fixture (no ACs): the only "Edit" button in the drawer belongs to
    // the description section.
    fixtureTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Draft fixture',
      description: 'Fixture for the draft/preview spec.',
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
        if (msg.text().includes('status of 409')) return; // version-conflict noise is tolerated suite-wide
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

  /** Load the board route and wait until the seeded project + fixture card render. */
  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expectProjectSelected(page, 'Draft Preview E2E project');
    await expect(page.locator('article', { hasText: 'Draft fixture' })).toBeVisible();
  }

  /** Open the fixture task's detail drawer and wait for its heading. */
  async function openTaskDrawer(page: import('@playwright/test').Page) {
    const card = page.locator('article', { hasText: 'Draft fixture' });
    await card.click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();
    await expect(page.locator('#task-detail-title')).toHaveText('Draft fixture');
    return drawer;
  }

  test('AC1: description Preview renders sanitized markdown (bold kept, script/img stripped)', async ({
    page,
  }) => {
    await openBoard(page);
    await openTaskDrawer(page);
    const panel = page.locator('#cb-detail-drawer aside');

    // Enter edit mode — the drawer's Edit button (unique: fixture has no ACs).
    await panel.getByRole('button', { name: 'Edit', exact: true }).first().click();
    const descArea = page.getByTestId('desc-textarea');
    await expect(descArea).toBeVisible();

    // Markdown + dangerous content in one go.
    await descArea.fill(
      [
        '**bold** emphasis survives',
        '',
        '<script>alert(1)</script>',
        '',
        '<img src=x onerror="alert(2)">',
      ].join('\n'),
    );

    // Switch to Preview.
    await page.getByTestId('desc-preview-btn').click();
    const preview = page.getByTestId('desc-preview');
    await expect(preview).toBeVisible();
    // The textarea is hidden (x-show) while the read-only preview is shown.
    await expect(descArea).toBeHidden();

    // Sanitization: bold renders as a real <strong>; script/img never survive.
    await expect(preview.locator('strong')).toHaveText('bold');
    await expect(preview.getByText('emphasis survives')).toBeVisible();
    await expect(preview.locator('script')).toHaveCount(0);
    await expect(preview.locator('img')).toHaveCount(0);

    // Back to Write: textarea returns, preview goes away.
    await page.getByTestId('desc-write-btn').click();
    await expect(descArea).toBeVisible();
    await expect(preview).toBeHidden();
  });

  test('AC1: Preview shows a friendly empty state for a blank value', async ({ page }) => {
    await openBoard(page);
    await openTaskDrawer(page);

    // Comment field: start the fixture with an EMPTY comment box.
    const commentArea = page.getByTestId('comment-textarea');
    await expect(commentArea).toBeVisible();
    await commentArea.fill('');
    await page.getByTestId('comment-preview-btn').click();

    // The friendly empty state shows INSTEAD of the (empty) md-body.
    await expect(page.getByTestId('comment-preview-empty')).toBeVisible();
    await expect(page.getByTestId('comment-preview')).toBeHidden();
  });

  test('AC2: comment draft autosaves (debounced) and survives close, re-open and page reload', async ({
    page,
  }) => {
    const draft = 'autosaved e2e comment draft';
    await openBoard(page);
    await openTaskDrawer(page);

    // Type into the comment box; wait past the 400ms debounce for the save.
    const commentArea = page.getByTestId('comment-textarea');
    await commentArea.fill(draft);
    await page.waitForTimeout(700);
    let stored = await page.evaluate((key) => localStorage.getItem(key), commentDraftKey(fixtureTask.id));
    expect(stored, 'debounced save must have written the key').not.toBeNull();
    expect(stored).toContain(draft);

    // Close the drawer — the draft must survive (it lives in storage, not state).
    await page.keyboard.press('Escape');
    await expect(page.locator('#cb-detail-drawer')).toBeHidden();

    // Re-open the SAME task: the typed text comes back.
    await openTaskDrawer(page);
    await expect(commentArea).toHaveValue(draft);

    // A full page reload (new document, fresh Alpine state) restores it too —
    // only localStorage survives, which is exactly the point of this feature.
    await page.reload();
    // The project selection persists via ravn.project, so the seeded board comes
    // back without any interaction.
    await expect(page.locator('article', { hasText: 'Draft fixture' })).toBeVisible();
    stored = await page.evaluate((key) => localStorage.getItem(key), commentDraftKey(fixtureTask.id));
    expect(stored, 'draft must survive a full page reload').not.toBeNull();
    await openTaskDrawer(page);
    await expect(commentArea).toHaveValue(draft);

    // No leak check: a DIFFERENT task's key was never written.
    const other = await page.evaluate((key) => localStorage.getItem(key), commentDraftKey('01SOMEOTHERTASKID'));
    expect(other, 'no draft may exist for any other task').toBeNull();
  });

  test('AC3: posting the comment removes its draft key from localStorage', async ({ page }) => {
    const body = '**posted** e2e comment';
    await openBoard(page);
    await openTaskDrawer(page);

    const commentArea = page.getByTestId('comment-textarea');
    await commentArea.fill(body);
    await page.waitForTimeout(700);
    expect(
      await page.evaluate((key) => localStorage.getItem(key), commentDraftKey(fixtureTask.id)),
      'precondition: the debounced draft must be stored before posting',
    ).not.toBeNull();

    // Submit.
    await page.getByRole('button', { name: 'Post comment' }).click();

    // The comment lands in the thread…
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer.locator('.md-body strong', { hasText: 'posted' })).toBeVisible();
    // …the form empties…
    await expect(commentArea).toHaveValue('');
    // …and the draft key is GONE — re-opening this task must not resurrect it.
    expect(
      await page.evaluate((key) => localStorage.getItem(key), commentDraftKey(fixtureTask.id)),
    ).toBeNull();

    // (Also confirm the description key for this task was never touched by a
    //  comment post — it stays absent because we never typed a description.)
    expect(
      await page.evaluate((key) => localStorage.getItem(key), descriptionDraftKey(fixtureTask.id)),
    ).toBeNull();
  });
});
