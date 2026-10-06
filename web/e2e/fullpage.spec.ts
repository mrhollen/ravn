import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { expectProjectSelected } from './helpers';

// Full-page task view (/tasks/<id>) — CrowBan task 01M47MWHWW0M1Q3WZYFGFHXTYZ.
// Self-contained like the other specs: ephemeral crowban-server on a random high
// port (never :8080) with a temp DB, serving the pre-built web/dist. The server
// serves index.html for /tasks/<id> via its SPA fallback, so these are REAL
// deep links end to end.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

const MOBILE_VIEWPORT = { width: 375, height: 812 };

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

async function putJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json'},
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PUT ${url} failed: ${res.status}`);
  return res.json();
}

async function getJson(url: string): Promise<any> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`GET ${url} failed: ${res.status}`);
  return res.json();
}

const SEED_ACTOR = { name: 'e2e-fullpage-seed', type: 'agent' };

test.describe('Full-page task view (/tasks/<id>)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;
  let taskA: any; // read-only fixture carrying every sidebar section
  let taskB: any; // mutation fixture for the edit-ops test
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-fullpage-'));
    const dbPath = path.join(tempDir, 'crowban.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Full page E2E project',
      description: 'Seeded for the full-page view e2e spec',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    // Task A: carries every section the sidebar shows (description, owner,
    // notes, 2 ACs, 2 comments, an artifact, a relation, activity events).
    taskA = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Full page fixture A',
      description: 'Fixture **description** for the full-page spec.\n\n- line one\n- line two',
      priority: 2,
      assignee: { name: 'fixture-owner', type: 'human' },
      acceptance_criteria: ['AC one ships', 'AC two ships'],
      actor: SEED_ACTOR,
    })).task; // POST /tasks → { task, possible_duplicates }
    await putJson(`${serverUrl}/api/v1/tasks/${taskA.id}/notes`, {
      actor: SEED_ACTOR,
      notes: 'Fixture notes for A\n- found the cause\n- next: fix it',
    });
    await postJson(`${serverUrl}/api/v1/tasks/${taskA.id}/comments`, {
      actor: SEED_ACTOR,
      kind: 'note',
      body: 'Seeded comment one on A',
    });
    await postJson(`${serverUrl}/api/v1/tasks/${taskA.id}/comments`, {
      actor: SEED_ACTOR,
      kind: 'progress',
      body: 'Seeded comment two on A',
    });
    await postJson(`${serverUrl}/api/v1/tasks/${taskA.id}/artifacts`, {
      actor: SEED_ACTOR,
      kind: 'url',
      ref: 'https://example.com/a-evidence',
      summary: 'Fixture artifact for A',
    });

    // Task B: the mutation fixture. Also blocked-by A so both tasks show a
    // relations row (B: "Blocked by", A: "Blocking").
    taskB = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Full page fixture B',
      description: 'Fixture for full-page edit operations.',
      priority: 3,
      acceptance_criteria: ['Edit works from page', 'Notes save from page'],
      actor: SEED_ACTOR,
    })).task; // POST /tasks → { task, possible_duplicates }
    await postJson(`${serverUrl}/api/v1/tasks/${taskB.id}/comments`, {
      actor: SEED_ACTOR,
      kind: 'note',
      body: 'Seeded comment one on B',
    });
    await postJson(`${serverUrl}/api/v1/tasks/${taskB.id}/relations`, {
      actor: SEED_ACTOR,
      other_id: taskA.id,
      kind: 'blocked-by',
    });
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

  /** Load the board route and wait until the seeded project + cards render. */
  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');
    // The custom Projects dropdown trigger shows the auto-selected seeded project.
    await expectProjectSelected(page, 'Full page E2E project');
    await expect(page.locator('article', { hasText: 'Full page fixture A' })).toBeVisible();
  }

  test('AC1+AC3: card click keeps the centered modal; "Open full page" renders every section at /tasks/<id>', async ({
    page,
  }) => {
    await openBoard(page);
    const cardA = page.locator('article', { hasText: 'Full page fixture A' });

    // Regression: a plain card click still opens the detail overlay (now the
    // WIDE CENTERED MODAL — modal.spec.ts asserts its geometry) and never
    // changes the URL.
    await cardA.click();
    const wrapper = page.locator('#cb-detail-drawer');
    await expect(wrapper).toBeVisible();
    await expect(wrapper).toHaveCSS('position', 'fixed');
    // A plain card click never touches the URL (modal behavior is unchanged by this task).
    expect(new URL(page.url()).pathname).toBe('/');
    // The board stays visible behind the modal.
    await expect(page.locator('main')).toBeVisible();

    // AC3: clearly visible "Open full page" affordance from the task detail view.
    const openBtn = page.locator('[data-testid="open-fullpage-btn"]');
    await expect(openBtn).toBeVisible();
    await openBtn.click();

    // AC1: the route exists and the panel re-skins to a full page.
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel).toBeVisible();
    await expect(wrapper).toHaveCSS('position', 'relative');
    await expect(panel).toHaveCSS('position', 'static');
    // The board region is hidden while the task page owns it.
    await expect(page.locator('main')).toBeHidden();

    // Task fields: title + markdown description render.
    await expect(panel.locator('h2').first()).toHaveText('Full page fixture A');
    const desc = panel.locator('.md-body', { hasText: 'line one' });
    await expect(desc).toBeVisible();

    // Status segmented control (all three destinations, legal ones enabled).
    const statusGroup = panel.locator('[role="group"][aria-label="Status"]');
    for (const label of ['To Do', 'Doing', 'Done']) {
      await expect(statusGroup.getByRole('button', { name: label })).toBeVisible();
    }

    // Owner (durable assignee) chip.
    await expect(panel.getByText('fixture-owner').first()).toBeVisible();

    // Claim controls + lease status section.
    await expect(panel.locator('[role="group"][aria-label="Claim Controls"]')).toBeVisible();

    // Notes (seeded blob in the scratchpad).
    const notes = panel.locator('textarea[placeholder*="Last run"]');
    await expect(notes).toHaveValue(new RegExp('Fixture notes for A'));

    // Acceptance criteria with check-offs + counter.
    await expect(panel.locator('h3', { hasText: 'Acceptance criteria' })).toContainText('(0/2)');
    const acBoxes = panel.locator('input[type="checkbox"][aria-label^="Mark done"]');
    await expect(acBoxes).toHaveCount(2);

    // Comments (both seeded) + the post-comment form.
    await expect(panel.getByText('Seeded comment one on A')).toBeVisible();
    await expect(panel.getByText('Seeded comment two on A')).toBeVisible();
    await expect(panel.locator('textarea[placeholder="Markdown… posted as a typed comment"]')).toBeVisible();

    // Artifacts (seeded URL artifact + its summary).
    await expect(panel.getByText('https://example.com/a-evidence')).toBeVisible();
    await expect(panel.getByText('Fixture artifact for A')).toBeVisible();

    // Relations: A blocks B, so its row reads "Blocking" → fixture B. The
    // "Remove relation" button is unique to relation rows (AC rows use
    // "Delete criterion N"), so anchor on it and walk up to the row.
    const removeRelBtn = panel.getByRole('button', { name: 'Remove relation' });
    await expect(removeRelBtn).toHaveCount(1);
    const relRow = removeRelBtn.locator('xpath=..');
    await expect(relRow).toContainText('Blocking');
    await expect(relRow).toContainText('Full page fixture B');

    // Activity (events exist from seeding; the section renders when non-empty).
    // Kind codes live only in activity list items — the header id badge is a
    // <code> too, but it is not inside an <li>.
    const eventKinds = await panel.locator('li code').allTextContents();
    expect(eventKinds).toContain('created');

    // The modal-only entry point disappears on the page; back affordance appears.
    await expect(page.locator('[data-testid="open-fullpage-btn"]')).toBeHidden();
    await expect(page.locator('[data-testid="fullpage-back-btn"]')).toBeVisible();
  });

  test('AC1: deep link /tasks/<id> renders the full page on a fresh load', async ({ page }) => {
    // Straight to the route — no board interaction at all. The server's SPA
    // fallback serves index.html and the app reconciles the URL on boot.
    await page.goto(`${serverUrl}/tasks/${taskA.id}`);
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel.locator('h2').first()).toHaveText('Full page fixture A');

    // The task's project was adopted as the board context (dropdown trigger label).
    await expectProjectSelected(page, 'Full page E2E project');

    // Page chrome: back affordance present, drawer-only button absent.
    await expect(page.locator('[data-testid="fullpage-back-btn"]')).toBeVisible();
    await expect(page.locator('[data-testid="open-fullpage-btn"]')).toBeHidden();
    await expect(page.locator('main')).toBeHidden();

    // Notes + ACs from the seeded payload rendered too.
    await expect(panel.locator('textarea[placeholder*="Last run"]')).toHaveValue(
      new RegExp('Fixture notes for A'),
    );
  });

  test('AC2: fields, status, notes, ACs and comments all edit from the full page', async ({ page }) => {
    await page.goto(`${serverUrl}/tasks/${taskB.id}`);
    const panel = page.locator('#cb-detail-drawer aside');
    await expect(panel.locator('h2').first()).toHaveText('Full page fixture B');

    // --- fields (title + priority) via the edit form -----------------------
    await panel.getByRole('button', { name: 'Edit', exact: true }).first().click();
    // The edit form is the ancestor <form> of its "Edit task details" heading.
    const editForm = panel.locator(
      'xpath=//h3[normalize-space()="Edit task details"]/ancestor::form',
    );
    await expect(editForm).toBeVisible();
    await editForm.locator('input').first().fill('B renamed from full page');
    await editForm.locator('select').first().selectOption('4'); // priority P4
    await editForm.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(panel.locator('h2').first()).toHaveText('B renamed from full page');
    const b1 = await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`);
    expect(b1.title).toBe('B renamed from full page');
    expect(b1.priority).toBe(4);

    // --- status via the segmented control ----------------------------------
    const statusGroup = panel.locator('[role="group"][aria-label="Status"]');
    await statusGroup.getByRole('button', { name: 'Doing' }).click();
    await expect(async () => {
      const t = await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`);
      expect(t.status).toBe('doing');
    }).toPass({ timeout: 10_000 });

    // --- notes ---------------------------------------------------------------
    const notes = panel.locator('textarea[placeholder*="Last run"]');
    await notes.fill('Notes saved from full page\nNext: ship it');
    await panel.getByRole('button', { name: 'Save notes' }).click();
    const b2 = await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`);
    expect(b2.notes).toBe('Notes saved from full page\nNext: ship it');

    // --- acceptance criterion check-off --------------------------------------
    const acBox = panel.locator('input[aria-label="Mark done: Edit works from page"]');
    await acBox.check();
    const b3 = await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`);
    const ac1 = b3.acceptance_criteria.find((a: any) => a.seq === 1);
    expect(ac1.done).toBe(true);

    // --- comment ---------------------------------------------------------------
    const commentsBefore = (await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`)).comments.length;
    await panel
      .locator('textarea[placeholder="Markdown… posted as a typed comment"]')
      .fill('Posted from the full page');
    await panel.getByRole('button', { name: 'Post comment' }).click();
    await expect(panel.getByText('Posted from the full page')).toBeVisible();
    const b4 = await getJson(`${serverUrl}/api/v1/tasks/${taskB.id}`);
    expect(b4.comments.length).toBeGreaterThan(commentsBefore);
  });

  test('AC3: the back button and browser back both return to the board', async ({ page }) => {
    await openBoard(page);
    const cardA = page.locator('article', { hasText: 'Full page fixture A' });

    // In-app back affordance.
    await cardA.click();
    await page.locator('[data-testid="open-fullpage-btn"]').click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await page.locator('[data-testid="fullpage-back-btn"]').click();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.locator('main')).toBeVisible();
    await expect(page.locator('#cb-detail-drawer')).toBeHidden();

    // Browser back after a fresh full-page open.
    await cardA.click();
    await page.locator('[data-testid="open-fullpage-btn"]').click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await page.goBack();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.locator('main')).toBeVisible();
    await expect(page.locator('#cb-detail-drawer')).toBeHidden();
  });

  test('AC4: the full page is usable at 375px mobile width', async ({ page, browser }) => {
    // The full page doubles as the planned mobile fallback for the future
    // modal view — verify it holds up at the AC reference width.
    const context = await browser.newContext({ viewport: MOBILE_VIEWPORT, hasTouch: true });
    const m = await context.newPage();
    m.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[mobile console.error] ${msg.text()}`);
      }
    });
    m.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[mobile pageerror] ${err.message}`);
    });

    await m.goto(`${serverUrl}/tasks/${taskA.id}`);
    const panel = m.locator('#cb-detail-drawer aside');
    await expect(panel.locator('h2').first()).toHaveText('Full page fixture A');

    // No horizontal overflow at the reference width.
    const overflow = await m.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
    }));
    expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.innerWidth + 1);

    // Header controls fit (back affordance present and on-screen).
    const backBtn = m.locator('[data-testid="fullpage-back-btn"]');
    await expect(backBtn).toBeVisible();
    const box = await backBtn.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);

    // Functional at this width: toggle an AC and save notes from the page.
    const acBox = panel.locator('input[type="checkbox"][aria-label^="Mark done"]').first();
    await acBox.check();
    await expect(async () => {
      const t = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
      expect(t.acceptance_criteria[0].done).toBe(true);
    }).toPass({ timeout: 10_000 });

    const notes = panel.locator('textarea[placeholder*="Last run"]');
    await notes.fill('Notes saved at 375px');
    await panel.getByRole('button', { name: 'Save notes' }).click();
    const t2 = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
    expect(t2.notes).toBe('Notes saved at 375px');

    // Status control is tappable at this width too (todo → doing).
    const statusGroup = panel.locator('[role="group"][aria-label="Status"]');
    await statusGroup.getByRole('button', { name: 'Doing' }).click();
    await expect(async () => {
      const t = await getJson(`${serverUrl}/api/v1/tasks/${taskA.id}`);
      expect(t.status).toBe('doing');
    }).toPass({ timeout: 10_000 });

    await context.close();
  });
});
