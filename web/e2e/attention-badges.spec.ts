// E2E coverage for the Question & Handoff badges + "Needs attention" board
// filter (epic child 01M5XTAYKJGP878ZEZB7TJW4ZB, frontend half). The board API
// returns last_comment_kind / last_comment_actor on each task summary (omitted
// entirely when a task has no comments); this spec seeds REAL comment threads
// against the ephemeral ravn-server and verifies:
//   (a) open tasks whose NEWEST comment is `question`/`handoff` render the
//       amber "? Question" / indigo "⇄ Handoff" badges — never on routine or
//       done cards;
//   (b) the "Needs attention" toggle keeps exactly that set and hides routine
//       + done tasks, with ?attention=1 deep-linking;
//   (c) Reset filters restores the full board;
//   (d) the filter AND-composes with another active filter (priority).
// Each test spins up its own seeded project; the server itself is shared per
// spec file on a random high port (never :8080).

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
const HUMAN_ACTOR = { name: 'e2e-human', type: 'human' as const };

const QUESTION_TITLE = 'Question Awaiting Answer Task';
const HANDOFF_TITLE = 'Handoff Review Task';
const ROUTINE_TITLE = 'Routine Progress Task';
const DONE_TITLE = 'Done Question Task';

test.describe('Question/handoff badges + Needs attention filter (01M5XTAYKJGP878ZEZB7TJW4ZB)', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-attention-e2e-'));
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

  async function patchJson(url: string, body: unknown): Promise<any> {
    const res = await fetch(url, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(`PATCH ${url} → ${res.status}`);
    return res.json();
  }

  async function addComment(taskId: string, kind: string, body: string): Promise<void> {
    await postJson(`${serverUrl}/api/v1/tasks/${taskId}/comments`, {
      actor: HUMAN_ACTOR,
      kind,
      body,
    });
  }

  /** Seed the per-test project with four tasks exercising every branch of the
   *  needsAttention predicate on real server state:
   *   - QUESTION_TITLE (todo): a `progress` comment THEN a `question` — newest
   *     is question (also proves only the NEWEST comment drives the badge);
   *   - HANDOFF_TITLE (todo): newest comment is `handoff`;
   *   - ROUTINE_TITLE (todo): exactly one `progress` comment;
   *   - DONE_TITLE: a `question` comment, then moved to done — proves done
   *     tasks are excluded from badges AND the attention filter. */
  async function seedAttentionProject(): Promise<{ id: string }> {
    const project = await postJson(`${serverUrl}/api/v1/projects`, {
      name: `Attention E2E ${Date.now()}`,
      description: 'Seeded for the question/handoff badge + needs-attention spec',
      actor: ACTOR,
    });

    const questionTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: QUESTION_TITLE,
      description: 'Seeded task with a newest-question comment thread',
      priority: 1,
      actor: ACTOR,
    })).task;
    await addComment(questionTask.id, 'progress', 'Working on it…');
    await addComment(questionTask.id, 'question', 'Should the retry use exponential backoff or a fixed 5s delay?');

    const handoffTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: HANDOFF_TITLE,
      description: 'Seeded task with a newest-handoff comment',
      priority: 2,
      actor: ACTOR,
    })).task;
    await addComment(handoffTask.id, 'handoff', 'Phase 1 done — notes in the resume block, ready for review.');

    const routineTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: ROUTINE_TITLE,
      description: 'Seeded task with a routine comment only',
      priority: 2,
      actor: ACTOR,
    })).task;
    await addComment(routineTask.id, 'progress', 'Routine status update.');

    const doneTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: DONE_TITLE,
      description: 'Seeded task that finishes with an open question comment',
      priority: 2,
      actor: ACTOR,
    })).task;
    await addComment(doneTask.id, 'question', 'Follow-up question that outlived the task.');
    // todo → done is NOT a legal transition — route through doing (same
    // convention as done-pagination-and-density.spec.ts#setStatus).
    const fresh = await (await fetch(`${serverUrl}/api/v1/tasks/${doneTask.id}`)).json();
    const doing = await patchJson(`${serverUrl}/api/v1/tasks/${doneTask.id}`, {
      actor: ACTOR,
      version: fresh.version,
      status: 'doing',
    });
    // Accept either the wrapped `{ task: … }` or a bare summary row shape.
    const doingVersion = doing.task?.version ?? doing.version;
    await patchJson(`${serverUrl}/api/v1/tasks/${doneTask.id}`, {
      actor: ACTOR,
      version: doingVersion,
      status: 'done',
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

  test('(a) badges render on question+handoff cards only — not routine, not done', async ({ page }) => {
    const project = await seedAttentionProject();
    await openProject(page, project.id);

    const qCard = page.locator('article', { hasText: QUESTION_TITLE });
    const hCard = page.locator('article', { hasText: HANDOFF_TITLE });
    const rCard = page.locator('article', { hasText: ROUTINE_TITLE });
    const dCard = page.locator('article', { hasText: DONE_TITLE });
    await expect(qCard).toBeVisible({ timeout: 10_000 });
    await expect(hCard).toBeVisible();
    await expect(rCard).toBeVisible();
    await expect(dCard).toBeVisible();

    // Question card: amber "? Question" badge with a clear accessible label;
    // the handoff badge must NOT appear on it (newest comment is question).
    const qBadge = qCard.getByTestId('card-question-badge');
    await expect(qBadge).toBeVisible();
    await expect(qBadge).toHaveText('? Question');
    await expect(qBadge).toHaveAttribute('aria-label', 'Latest comment is an open question');
    // title names the newest comment's actor.
    await expect(qBadge).toHaveAttribute('title', `Latest comment is a question from ${HUMAN_ACTOR.name}`);
    await expect(qCard.getByTestId('card-handoff-badge')).toBeHidden();

    // Handoff card: indigo "⇄ Handoff" badge; the question badge must NOT.
    const hBadge = hCard.getByTestId('card-handoff-badge');
    await expect(hBadge).toBeVisible();
    await expect(hBadge).toHaveText('⇄ Handoff');
    await expect(hBadge).toHaveAttribute('aria-label', 'Latest comment is a handoff awaiting review');
    await expect(hCard.getByTestId('card-question-badge')).toBeHidden();

    // Routine card: has its sky comment-count badge but NEITHER attention badge.
    await expect(rCard.getByTestId('card-comment-count')).toBeVisible();
    await expect(rCard.getByTestId('card-question-badge')).toBeHidden();
    await expect(rCard.getByTestId('card-handoff-badge')).toBeHidden();

    // Done card: a `question` comment exists (count badge proves it) yet done
    // tasks never show the attention badges.
    await expect(dCard.getByTestId('card-comment-count')).toBeVisible();
    await expect(dCard.getByTestId('card-question-badge')).toBeHidden();
    await expect(dCard.getByTestId('card-handoff-badge')).toBeHidden();

    // Exactly two cards badge across the whole board.
    await expect(page.locator('[data-testid="card-question-badge"]:visible')).toHaveCount(1);
    await expect(page.locator('[data-testid="card-handoff-badge"]:visible')).toHaveCount(1);
  });

  test('(b)/(c) "Needs attention" filter keeps exactly question+handoff; Reset restores all', async ({ page }) => {
    const project = await seedAttentionProject();
    await openProject(page, project.id);

    const qCard = page.locator('article', { hasText: QUESTION_TITLE });
    const hCard = page.locator('article', { hasText: HANDOFF_TITLE });
    const rCard = page.locator('article', { hasText: ROUTINE_TITLE });
    const dCard = page.locator('article', { hasText: DONE_TITLE });
    await expect(qCard).toBeVisible({ timeout: 10_000 });
    await expect(hCard).toBeVisible();
    await expect(rCard).toBeVisible();
    await expect(dCard).toBeVisible();

    // ------------------------------------------------- (b) toggle ON via the checkbox
    const attentionCheckbox = page.getByTestId('filter-needs-attention');
    await expect(attentionCheckbox).toBeVisible();
    await expect(attentionCheckbox).not.toBeChecked();
    // Keyboard: native checkbox — focus + Space must work (WCAG 2.1.1).
    await attentionCheckbox.focus();
    await page.keyboard.press('Space');
    await expect(attentionCheckbox).toBeChecked();

    // Exactly the question+handoff set stays; routine AND done are hidden.
    await expect(qCard).toBeVisible();
    await expect(hCard).toBeVisible();
    await expect(rCard).not.toBeVisible();
    await expect(dCard).not.toBeVisible();
    await expect(page.locator('article')).toHaveCount(2);
    // The filter is deep-linkable: ?attention=1 lands in the URL.
    await expect.poll(() => page.url()).toContain('attention=1');

    // Screenshot evidence: badges + attention-filtered board state.
    const shotDir = path.resolve(__dirname, '../test-results');
    fs.mkdirSync(shotDir, { recursive: true });
    await page.screenshot({ path: path.join(shotDir, 'attention-filter-active.png') });

    // ------------------------------------------------- (c) Reset filters
    const resetBtn = page.getByRole('button', { name: 'Reset filters' });
    await expect(resetBtn).toBeVisible();
    await resetBtn.click();
    await expect(attentionCheckbox).not.toBeChecked();
    await expect(qCard).toBeVisible();
    await expect(hCard).toBeVisible();
    await expect(rCard).toBeVisible();
    await expect(dCard).toBeVisible();
    await expect(page.locator('article')).toHaveCount(4);
    await expect.poll(() => page.url()).not.toContain('attention=1');
  });

  test('deep link ?attention=1 initializes the filter to the attention set', async ({ page }) => {
    const project = await seedAttentionProject();

    // Direct load with the filter parameter — no clicks involved.
    await page.goto(`${serverUrl}/?project=${project.id}&attention=1`);
    const triggerLabel = page.getByTestId('project-select');
    await expect(triggerLabel).toContainText('Attention E2E', { timeout: 10_000 });

    const qCard = page.locator('article', { hasText: QUESTION_TITLE });
    const hCard = page.locator('article', { hasText: HANDOFF_TITLE });
    const rCard = page.locator('article', { hasText: ROUTINE_TITLE });
    await expect(qCard).toBeVisible();
    await expect(hCard).toBeVisible();
    await expect(rCard).not.toBeVisible();

    // The checkbox reflects the restored state.
    await expect(page.getByTestId('filter-needs-attention')).toBeChecked();
  });

  test('(d) attention filter AND-composes with the priority filter', async ({ page }) => {
    const project = await seedAttentionProject();
    await openProject(page, project.id);

    const qCard = page.locator('article', { hasText: QUESTION_TITLE }); // priority 1
    const hCard = page.locator('article', { hasText: HANDOFF_TITLE }); // priority 2
    const rCard = page.locator('article', { hasText: ROUTINE_TITLE }); // priority 2
    await expect(qCard).toBeVisible({ timeout: 10_000 });
    await expect(hCard).toBeVisible();

    // Attention ON first → q + h visible (routine/done hidden by attention).
    const attentionCheckbox = page.getByTestId('filter-needs-attention');
    await attentionCheckbox.check();
    await expect(qCard).toBeVisible();
    await expect(hCard).toBeVisible();
    await expect(page.locator('article')).toHaveCount(2);

    // …then PRIORITY P1 on top: only the card matching BOTH predicates (q)
    // survives — handoff is hidden by priority, routine stays hidden by attention.
    const prioritySelect = page.getByLabel('Filter by priority');
    await prioritySelect.selectOption('1');
    await expect(qCard).toBeVisible();
    await expect(hCard).not.toBeVisible();
    await expect(rCard).not.toBeVisible();
    await expect(page.locator('article')).toHaveCount(1);
    // Both parameters coexist in the URL.
    await expect.poll(() => page.url()).toContain('attention=1');
    await expect.poll(() => page.url()).toContain('priority=1');

    // Reset clears BOTH at once.
    const resetBtn = page.getByRole('button', { name: 'Reset filters' });
    await resetBtn.click();
    await expect(attentionCheckbox).not.toBeChecked();
    await expect(prioritySelect).toHaveValue('');
    await expect(page.locator('article')).toHaveCount(4);
  });
});
