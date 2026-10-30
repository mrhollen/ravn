import { test, expect } from './fixtures';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

// CI hero screenshot: renders the Rävn board in an attractive, populated state
// (dark theme) and writes ONE canonical image to the stable path CI uploads —
// web/test-results/hero.png. Self-contained like the other specs: it owns its
// own ephemeral ravn-server on a random high port (never :8080) with a temp DB,
// serving the pre-built web/dist. All seed data is FIXED (no randomness in any
// visible content) so the composition is identical run to run.

// Artifact-path anchor (NOT server scaffolding): pin the canonical hero.png at
// <repo>/web/test-results/ regardless of Playwright's invoking CWD.
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Stable artifact location — CI uploads exactly this repo-relative path.
const HERO_SCREENSHOT_PATH = 'web/test-results/hero.png';
// Playwright resolves screenshot paths against the PROCESS CWD, and both CI
// (`cd web && npm run test:e2e`) and local runs execute with CWD=web/ — so the
// relative call above alone would land the file at <repo>/web/web/test-results/.
// To pin the canonical artifact at <repo>/web/test-results/hero.png no matter
// where playwright is invoked from, also anchor it on this spec's location.
const HERO_SCREENSHOT_ABS = path.resolve(__dirname, '..', 'test-results', 'hero.png');

const SEED_ACTOR = { name: 'hero-seed', type: 'agent' };

/** Small REST helpers (server runs with RAVN_TOKEN='' → zero-config local mode). */
async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed (${res.status}): ${await res.text()}`);
  return res.json();
}

async function patchJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${url} failed (${res.status}): ${await res.text()}`);
  return res.json();
}

async function getJson(url: string): Promise<any> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`GET ${url} failed (${res.status}): ${await res.text()}`);
  return res.json();
}

/** Create a task via REST and return the fresh task row (carries .id/.version). */
async function seedTask(projectId: string, fields: Record<string, unknown>): Promise<any> {
  const data = await postJson(`${serverUrl}/api/v1/tasks`, {
    project_id: projectId,
    actor: SEED_ACTOR,
    ...fields,
  });
  return data.task;
}

/** Version-guarded status move: re-reads the row first so the optimistic-
 *  concurrency version is always fresh (the done transition also requires all
 *  ACs complete — seed ACs are therefore finished BEFORE this call). */
async function setStatus(taskId: string, status: 'doing' | 'done'): Promise<void> {
  const task = await getJson(`${serverUrl}/api/v1/tasks/${taskId}`);
  await patchJson(`${serverUrl}/api/v1/tasks/${taskId}`, {
    actor: SEED_ACTOR,
    version: task.version,
    status,
  });
}

/** Mark one acceptance criterion (1-based seq) complete. */
async function completeAc(taskId: string, seq: number): Promise<void> {
  await patchJson(`${serverUrl}/api/v1/tasks/${taskId}/acceptance-criteria/${seq}`, {
    actor: SEED_ACTOR,
    done: true,
  });
}

// Mutable per-run state at module scope so the REST helpers above (which are
// defined next to the other pure helpers) can reach the shared worker URL.
let serverUrl = '';

// Dark theme + an explicit marketing-frame viewport for the hero shot.
test.use({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } });

test.describe('hero screenshot (CI artifact web/test-results/hero.png)', () => {
  let uncaughtConsoleErrors: string[] = [];

    // Server owned by the shared worker fixture; this spec seeds a fixed 'Rävn'
    // project into it and selects it explicitly (a shared DB won't auto-select it).
    test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
      serverUrl = sharedServerUrl;
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

  test('renders the populated dark-theme board and writes web/test-results/hero.png', async ({
    context,
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: seed one project + six fixed tasks via REST (no auth — empty token)
    // -------------------------------------------------------------------------
    const projRes = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Rävn',
      description: '',
      actor: SEED_ACTOR,
    });
    expect(projRes.id).toBeTruthy();
    const project = projRes;

    // --- To Do (3) -----------------------------------------------------------
    await seedTask(project.id, {
      title: 'Draft onboarding doc',
      priority: 2,
      due_at: '2026-10-24T00:00:00Z',
    });
    await seedTask(project.id, {
      title: 'Add dark mode to settings',
      priority: 3,
    });
    await seedTask(project.id, {
      title: 'Design empty-state illustrations',
      priority: 4,
    });

    // --- Doing (2) -----------------------------------------------------------
    const sseTask = await seedTask(project.id, {
      title: 'Ship realtime SSE board sync',
      description: 'Cards move live — no reloads, no polling.',
      priority: 1,
      due_at: '2026-10-28T00:00:00Z',
      assignee: { name: 'atlas-agent', type: 'agent' },
      acceptance_criteria: [
        'Board stays live without reloads',
        'Reconnect with exponential backoff',
      ],
    });
    await setStatus(sseTask.id, 'doing');
    // 1/2 AC progress badge + a comment-count badge on the hero card.
    await completeAc(sseTask.id, 1);
    await postJson(`${serverUrl}/api/v1/tasks/${sseTask.id}/comments`, {
      actor: SEED_ACTOR,
      kind: 'progress',
      body: 'Handshake + reconnect paths verified against the staging server.',
    });

    const contrastTask = await seedTask(project.id, {
      title: 'Polish task detail drawer contrast',
      priority: 2,
    });
    await setStatus(contrastTask.id, 'doing');

    // --- Done (1) -------------------------------------------------------------
    // The transition map has no todo→done edge (goes through doing), and the
    // done gate requires ALL ACs complete — finish them before the final move.
    const focusTrapTask = await seedTask(project.id, {
      title: 'Fix header menu focus trap',
      priority: 1,
      acceptance_criteria: [
        'Focus stays inside the menu while open',
        'Escape closes and restores trigger focus',
      ],
    });
    await setStatus(focusTrapTask.id, 'doing');
    await completeAc(focusTrapTask.id, 1);
    await completeAc(focusTrapTask.id, 2);
    await setStatus(focusTrapTask.id, 'done');

    // -------------------------------------------------------------------------
    // Step 2: dark theme BEFORE boot (init script + colorScheme → no flash), then load
    // -------------------------------------------------------------------------
    await context.addInitScript(() => {
      try {
        localStorage.setItem('ravn_theme', 'dark');
      } catch {}
    });
    await page.goto(serverUrl);

    // App-boot signal: the Projects trigger enables when init finishes.
    await expect(page.getByTestId('project-select')).toBeEnabled();
    // Shared DB holds other specs' projects too — auto-select won't pick 'Rävn',
    // so select it explicitly to render the deterministic hero composition.
    await selectProjectViaDropdown(page, project.id);

    // The dark class is on <html> (poll — theme application is async).
    await expect.poll(
      async () => page.evaluate(() => document.documentElement.classList.contains('dark')),
      { timeout: 5000 },
    ).toBeTruthy();

    // -------------------------------------------------------------------------
    // Step 3: wait for full render — every seeded card visible in its column
    // -------------------------------------------------------------------------
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const doingCol = page.locator('section', { has: page.getByRole('heading', { name: 'Doing' }) });
    const doneCol = page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });

    for (const col of [todoCol, doingCol, doneCol]) await expect(col).toBeVisible();

    for (const title of ['Draft onboarding doc', 'Add dark mode to settings', 'Design empty-state illustrations']) {
      await expect(todoCol.locator('h3', { hasText: title })).toBeVisible({ timeout: 10_000 });
    }
    for (const title of ['Ship realtime SSE board sync', 'Polish task detail drawer contrast']) {
      await expect(doingCol.locator('h3', { hasText: title })).toBeVisible({ timeout: 10_000 });
    }
    await expect(doneCol.locator('h3', { hasText: 'Fix header menu focus trap' })).toBeVisible({ timeout: 10_000 });

    // The hero card carries its live badges (AC progress + comment count).
    const heroCard = doingCol.locator('article', { hasText: 'Ship realtime SSE board sync' });
    await expect(heroCard.getByTestId('card-ac-progress')).toBeVisible();
    await expect(heroCard.getByTestId('card-comment-count')).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 4: capture the hero at the stable CI path (1440×900 set via test.use)
    // -------------------------------------------------------------------------
    await page.screenshot({ path: HERO_SCREENSHOT_PATH });

    // Pin the canonical copy (see HERO_SCREENSHOT_ABS): guaranteed to be
    // <repo>/web/test-results/hero.png from any invoking CWD. Idempotent —
    // skipped when the relative call already wrote straight there.
    const written = path.resolve(process.cwd(), HERO_SCREENSHOT_PATH);
    if (written !== HERO_SCREENSHOT_ABS) {
      fs.mkdirSync(path.dirname(HERO_SCREENSHOT_ABS), { recursive: true });
      fs.copyFileSync(written, HERO_SCREENSHOT_ABS);
    }
    expect(fs.existsSync(HERO_SCREENSHOT_ABS)).toBe(true);
  });
});
