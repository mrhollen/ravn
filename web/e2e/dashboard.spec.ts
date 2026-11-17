import { randomUUID } from 'crypto';
import { test, expect } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

const SEED_ACTOR = { name: 'e2e-dashboard-seed', type: 'agent' as const };

async function apiJson(url: string, method: string, body?: unknown): Promise<any> {
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${url} failed (${res.status}): ${await res.text()}`);
  }
  return res.status === 204 ? undefined : res.json();
}

test.describe('Project Dashboard view (/report)', () => {
  let serverUrl = '';
  let uncaughtConsoleErrors: string[] = [];

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

  test('renders all /report sections with accurate counts, task drill-down, and dark mode', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Rich ${randomUUID()}`,
      description: 'Seeded for dashboard E2E sections',
      actor: SEED_ACTOR,
    });

    // 1. Blocker task (todo, P0, assigned to alice)
    const blockerRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Auth Schema Blocker',
      priority: 0,
      assignee: { name: 'alice', type: 'human' },
      actor: SEED_ACTOR,
    });
    const blocker = blockerRes.task;

    // 2. Blocked todo task (todo, P1, assigned to bob, blocked-by blocker)
    const blockedRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Blocked Todo Task',
      priority: 1,
      assignee: { name: 'bob', type: 'agent' },
      actor: SEED_ACTOR,
    });
    const blockedTask = blockedRes.task;
    await apiJson(`${serverUrl}/api/v1/tasks/${blockedTask.id}/relations`, 'POST', {
      other_id: blocker.id,
      kind: 'blocked-by',
      actor: SEED_ACTOR,
    });

    // 3. Active claimed task (doing, P1, assigned to bob)
    const activeRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Wire Login Endpoint',
      priority: 1,
      assignee: { name: 'bob', type: 'agent' },
      actor: SEED_ACTOR,
    });
    const activeTask = activeRes.task;
    await apiJson(`${serverUrl}/api/v1/tasks/${activeTask.id}/claim`, 'POST', {
      actor: { name: 'bob', type: 'agent' },
      ttl_sec: 300,
    });

    // 4. Expired claim task (doing, P2, unassigned, ttl_sec=1)
    const expiredRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Zombie Lease Worker',
      priority: 2,
      actor: SEED_ACTOR,
    });
    const expiredTask = expiredRes.task;
    const lease = await apiJson(`${serverUrl}/api/v1/tasks/${expiredTask.id}/claim`, 'POST', {
      actor: { name: 'stale-bot', type: 'agent' },
      ttl_sec: 1,
    });

    // Wait for the ttl_sec=1 lease to actually expire as seen in the /report
    // digest — the exact data source this view renders — instead of a blind
    // sleep against claim_expires_at (which raced under worker load). A server
    // expiry never reactivates without a new claim, so once the zombie leaves
    // active_claims the first render below is guaranteed to show exactly one
    // active claim. Condition poll: no fixed wait duration involved.
    await expect
      .poll(async () => {
        const report = await apiJson(`${serverUrl}/api/v1/projects/${proj.id}/report`, 'GET');
        return {
          active: (report.active_claims as Array<{ id: string }>).some((c) => c.id === expiredTask.id),
          expired: (report.expired_claims as Array<{ id: string }>).some((t) => t.id === expiredTask.id),
        };
      }, { timeout: 15_000 })
      .toEqual({ active: false, expired: true });

    // 5. Recently completed task (done, P3, assigned to alice)
    const doneRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Migrate Initial Tables',
      priority: 3,
      assignee: { name: 'alice', type: 'human' },
      actor: SEED_ACTOR,
    });
    const doneTask = doneRes.task;
    const doingDone = await apiJson(`${serverUrl}/api/v1/tasks/${doneTask.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: doneTask.version,
      status: 'doing',
    });
    await apiJson(`${serverUrl}/api/v1/tasks/${doneTask.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: doingDone.version,
      status: 'done',
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    const navBtn = page.getByTestId('dashboard-nav-btn');
    await expect(navBtn).toBeEnabled();
    await navBtn.click();

    const dash = page.getByTestId('dashboard-view');
    await expect(dash).toBeVisible();
    await expect(dash.getByRole('heading', { name: 'Project Dashboard' })).toBeVisible();

    // Status summary: todo=2, doing=2, done=1, total=5
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('5 tasks');
    await expect(page.getByTestId('dashboard-status-todo')).toContainText('2');
    await expect(page.getByTestId('dashboard-status-doing')).toContainText('2');
    await expect(page.getByTestId('dashboard-status-done')).toContainText('1');

    // Priority breakdown P0..P5
    await expect(page.getByTestId('dashboard-priority-0')).toContainText('1');
    await expect(page.getByTestId('dashboard-priority-1')).toContainText('2');
    await expect(page.getByTestId('dashboard-priority-2')).toContainText('1');
    await expect(page.getByTestId('dashboard-priority-3')).toContainText('1');
    await expect(page.getByTestId('dashboard-priority-4')).toContainText('0');
    await expect(page.getByTestId('dashboard-priority-5')).toContainText('0');

    // Active claims & expired claims
    const claimsSec = page.getByTestId('dashboard-active-claims');
    await expect(claimsSec).toContainText('1 active');
    await expect(claimsSec).toContainText('Wire Login Endpoint');
    await expect(claimsSec).toContainText('bob (agent)');
    await expect(claimsSec).toContainText('left');
    const expiredSec = page.getByTestId('dashboard-expired-claims');
    await expect(expiredSec).toContainText('Zombie Lease Worker');
    await expect(expiredSec).toContainText('Expired');

    // Workload distribution (bob: 2, alice: 1, Unassigned: 1)
    const workloadSec = page.getByTestId('dashboard-workload');
    await expect(workloadSec).toContainText('bob');
    await expect(workloadSec).toContainText('alice');
    await expect(workloadSec).toContainText('Unassigned');

    // Stalled tasks (freshly seeded -> empty state)
    await expect(page.getByTestId('dashboard-stalled-empty')).toBeVisible();

    // Bottlenecks & blocked-by groups
    const bottlenecksSec = page.getByTestId('dashboard-bottlenecks');
    await expect(bottlenecksSec).toContainText('Auth Schema Blocker');
    await expect(bottlenecksSec).toContainText('Blocks 1');
    const blockedGroups = page.getByTestId('dashboard-blocked-by-groups');
    await expect(blockedGroups).toContainText('Blocker: Auth Schema Blocker');
    await expect(blockedGroups).toContainText('Blocked Todo Task');

    // Recently completed
    const completedSec = page.getByTestId('dashboard-recently-completed');
    await expect(completedSec).toContainText('Last 24h · 1');
    await expect(completedSec).toContainText('Migrate Initial Tables');
    await expect(completedSec).toContainText('alice');

    // Drill down to task detail modal and close back to dashboard
    await claimsSec.getByRole('button', { name: 'Wire Login Endpoint' }).click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Wire Login Endpoint' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
    await expect(dash).toBeVisible();

    // Verify dark mode rendering
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('theme-dark').click();
    await page.keyboard.press('Escape');
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect(dash).toBeVisible();

    // Back to board button closes dashboard
    await page.getByTestId('dashboard-back-btn').click();
    await expect(dash).toBeHidden();
    await expect(page.locator('main section', { has: page.getByRole('heading', { name: 'To Do' }) })).toBeVisible();
  });

  test('renders clean empty states for a project with zero tasks', async ({ page }) => {
    const emptyProj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Empty ${randomUUID()}`,
      actor: SEED_ACTOR,
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, emptyProj.id);

    await page.getByTestId('dashboard-nav-btn').click();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page.getByTestId('dashboard-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('0 tasks');
    await expect(page.getByTestId('dashboard-active-claims-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-expired-claims-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-workload-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-stalled-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-bottlenecks-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-recently-completed-empty')).toBeVisible();
  });

  test('updates live via existing SSE stream without opening a second EventSource', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard SSE ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    const taskRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'SSE Live Task',
      priority: 1,
      assignee: { name: 'ada', type: 'human' },
      actor: SEED_ACTOR,
    });
    const task = taskRes.task;

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    await page.waitForFunction(() => {
      const app = (window as any).Alpine?.$data(document.querySelector('[x-data]'));
      return app?.sseStatus === 'connected';
    }, { timeout: 15000 });

    await page.getByTestId('dashboard-nav-btn').click();
    await expect(page.getByTestId('dashboard-status-todo')).toContainText('1');
    await expect(page.getByTestId('dashboard-status-done')).toContainText('0');
    await expect(page.getByTestId('dashboard-recently-completed-empty')).toBeVisible();

    const openBefore = await page.evaluate(() => {
      const app = (window as any).Alpine?.$data(document.querySelector('[x-data]'));
      return app._sseTelemetry.openCount();
    });
    expect(openBefore).toBe(1);

    // Mutate project state via API while dashboard is open -> SSE pushes live update
    const doing = await apiJson(`${serverUrl}/api/v1/tasks/${task.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: task.version,
      status: 'doing',
    });
    await apiJson(`${serverUrl}/api/v1/tasks/${task.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: doing.version,
      status: 'done',
    });
    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Second Live Task',
      priority: 0,
      actor: SEED_ACTOR,
    });

    await expect(page.getByTestId('dashboard-total-count')).toHaveText('2 tasks', { timeout: 5000 });
    await expect(page.getByTestId('dashboard-status-todo')).toContainText('1');
    await expect(page.getByTestId('dashboard-status-done')).toContainText('1');
    await expect(page.getByTestId('dashboard-recently-completed')).toContainText('SSE Live Task');

    const openAfter = await page.evaluate(() => {
      const app = (window as any).Alpine?.$data(document.querySelector('[x-data]'));
      return app._sseTelemetry.openCount();
    });
    expect(openAfter, 'no second SSE connection opened').toBe(1);
  });

  test('renders stalled tasks and error/retry recovery cleanly', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Stalled Error ${randomUUID()}`,
      actor: SEED_ACTOR,
    });

    let failNext = true;
    await page.route(`**/api/v1/projects/${proj.id}/report*`, async (route) => {
      if (failNext) {
        failNext = false;
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'internal', message: 'Simulated report failure' }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          project_id: proj.id,
          counts: {
            by_status: { todo: 0, doing: 1, done: 0 },
            by_priority: { '0': 0, '1': 1, '2': 0, '3': 0, '4': 0, '5': 0 },
          },
          active_claims: [],
          expired_claims: [],
          stalled_tasks: [
            {
              id: 't-stalled-mock',
              title: 'Idle Refactor Task',
              last_activity_at: new Date(Date.now() - 95 * 60_000).toISOString(),
              idle_minutes: 95,
            },
          ],
          bottlenecks: [],
          open_blocked_by: [],
          recently_completed: [],
          workload: { ada: 1 },
        }),
      });
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    await page.getByTestId('dashboard-nav-btn').click();
    await expect(page.getByTestId('dashboard-error')).toBeVisible();
    await expect(page.getByTestId('dashboard-error')).toContainText('Simulated report failure');
    // Clear the browser's expected 500 resource-error log from the simulated failure.
    uncaughtConsoleErrors = uncaughtConsoleErrors.filter((e) => !e.includes('status of 500'));

    await page.getByTestId('dashboard-retry-btn').click();
    await expect(page.getByTestId('dashboard-error')).toBeHidden();
    const stalledSec = page.getByTestId('dashboard-stalled');
    await expect(stalledSec).toContainText('Idle Refactor Task');
    await expect(stalledSec).toContainText('1h 35m idle');
  });

  test('mobile 375px layout keeps single-row header, touch targets >= 44px, and zero overflow', async ({ browser }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Mobile ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Mobile Dashboard Task',
      priority: 1,
      assignee: { name: 'ada', type: 'human' },
      actor: SEED_ACTOR,
    });

    const context = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const page = await context.newPage();
    page.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    const headerBox = await page.locator('header').first().boundingBox();
    expect(headerBox).not.toBeNull();
    expect(headerBox!.height, '375px header stays single-row').toBeLessThanOrEqual(96);

    const navBtn = page.getByTestId('dashboard-nav-btn');
    const navBox = await navBtn.boundingBox();
    expect(navBox).not.toBeNull();
    expect(navBox!.width).toBeGreaterThanOrEqual(44);
    expect(navBox!.height).toBeGreaterThanOrEqual(44);

    await navBtn.click();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('1 task');

    for (const id of ['dashboard-refresh-btn', 'dashboard-back-btn']) {
      const box = await page.getByTestId(id).boundingBox();
      expect(box).not.toBeNull();
      expect(box!.height, `${id} height >= 44px`).toBeGreaterThanOrEqual(44);
    }

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth, 'zero horizontal overflow on 375px dashboard').toBeLessThanOrEqual(375);

    await context.close();
  });
});
