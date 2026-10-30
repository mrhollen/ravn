import { test, expect } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

// E2E coverage for the full-board "filtered empty state" (Rävn task
// 01M3MMXFXNYK8VS82HHA07VHGT): a banner across the columns when active filters
// hide every task while the project still has some (AC1), its one-click Reset
// button (AC2), and the hidden-task count with correct pluralization (AC3). A
// truly empty project must keep its plain empty look (no banner), so that state
// is asserted too. Mirrors the ephemeral-server scaffolding of board.spec.ts —
// each test seeds its own project + tasks via REST, then drives the real UI.

test.describe('filtered empty state', () => {
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  // The ephemeral ravn-server (random port + temp DB) is owned by the shared worker fixture.
  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;
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
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Seed a project via REST and return its row. */
  async function seedProject(name: string): Promise<{ id: string }> {
    const res = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, actor: { name: 'e2e-seed', type: 'agent' } }),
    });
    expect(res.ok).toBe(true);
    const project = await res.json();
    expect(project.id).toBeTruthy();
    return project;
  }

  /** Seed one task into `projectId` via REST and return its row. */
  async function seedTask(projectId: string, title: string): Promise<{ id: string }> {
    const res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: projectId,
        title,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(res.ok).toBe(true);
    const task = (await res.json()).task;
    expect(task?.id).toBeTruthy();
    return task;
  }

  /** Open the app and select the freshly seeded project via the real dropdown. */
  async function openProject(page: import('@playwright/test').Page, projectId: string): Promise<void> {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
  }

  const nonsenseSearch = 'zzz-no-such-task-xyz';

  test('shows the filtered empty state when search hides all tasks', async ({ page }) => {
    const project = await seedProject('Filtered Empty Project');
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await seedTask(project.id, t);
    }
    await openProject(page, project.id);

    // Sanity: all three cards start visible (banner must be absent pre-filter).
    const banner = page.locator('[data-testid="filtered-empty-state"]');
    await expect(banner).toBeHidden();
    await expect(page.locator('article')).toHaveCount(3);

    // A search matching nothing hides every card in every column.
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    // AC1: the dedicated banner renders across the columns with its wording…
    await expect(banner).toBeVisible();
    await expect(banner).toHaveAttribute('role', 'status');
    await expect(banner).toContainText('No tasks match the active filters');
    // …and AC3: it reports the total number of hidden tasks (all 3 seeded).
    await expect(banner.getByTestId('filtered-empty-count')).toContainText('3 tasks hidden');

    // The per-column placeholders stay in place beneath the banner.
    await expect(page.getByText('No tasks', { exact: true })).toHaveCount(3);
  });

  test('reset button clears filters and reveals tasks', async ({ page }) => {
    const project = await seedProject('Reset Filters Project');
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await seedTask(project.id, t);
    }
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);
    await expect(banner).toBeVisible();

    // AC2: the reset button INSIDE the empty state clears every filter in one click.
    await banner.getByTestId('filtered-empty-reset').click();

    await expect(banner).toBeHidden();
    await expect(searchInput).toHaveValue('');
    // All three seeded cards (todo status) are visible again.
    for (const t of ['First Seeded Task', 'Second Seeded Task', 'Third Seeded Task']) {
      await expect(page.locator('article', { hasText: t })).toBeVisible();
    }
    await expect(page.locator('article')).toHaveCount(3);
  });

  test('does not show the banner on a truly empty project', async ({ page }) => {
    // Project with zero tasks — active filters still change nothing visible,
    // and the board must keep its plain empty-project look (no banner).
    const project = await seedProject('Truly Empty Project');
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    await expect(banner).toBeHidden();
    // Plain per-column placeholders are what the user sees.
    await expect(page.getByText('No tasks', { exact: true })).toHaveCount(3);
  });

  test('reports a singular count for exactly one hidden task', async ({ page }) => {
    const project = await seedProject('Singular Count Project');
    await seedTask(project.id, 'Lone Seeded Task');
    await openProject(page, project.id);

    const banner = page.locator('[data-testid="filtered-empty-state"]');
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill(nonsenseSearch);

    // AC3 (singular form): "1 task hidden", not "1 tasks hidden".
    await expect(banner).toBeVisible();
    const countSpan = banner.getByTestId('filtered-empty-count');
    await expect(countSpan).toContainText('1 task hidden');
    // The plural 's' span stays display:none, so the plural phrasing is absent.
    await expect(banner).not.toHaveText(/tasks hidden/);
  });
});
