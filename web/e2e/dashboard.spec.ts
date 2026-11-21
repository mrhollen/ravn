import { randomUUID } from 'crypto';
import { AxeBuilder } from '@axe-core/playwright';
import { test, expect } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

// Contrast helper (WCAG 1.4.3) — composites translucent ancestor backgrounds.
function srgbChannel(v: number): number {
  const c = v / 255;
  return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
}
function luminance(rgb: readonly [number, number, number]): number {
  return 0.2126 * srgbChannel(rgb[0]) + 0.7152 * srgbChannel(rgb[1]) + 0.0722 * srgbChannel(rgb[2]);
}
function contrastRatio(fg: readonly [number, number, number], bg: readonly [number, number, number]): number {
  const l1 = luminance(fg);
  const l2 = luminance(bg);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}
function parseRgb(s: string): [number, number, number] {
  const m = s.match(/rgb\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)/);
  if (!m) throw new Error(`unparseable color: ${s}`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}
const sampleFgBgAndBorder = (el: Element): [string, string, string] => {
  const toRgb = (cssColor: string): number[] | null => {
    if (cssColor === 'transparent') return null;
    const c = document.createElement('canvas');
    c.width = c.height = 1;
    const ctx = c.getContext('2d')!;
    ctx.clearRect(0, 0, 1, 1);
    try {
      ctx.fillStyle = cssColor;
    } catch {
      return null;
    }
    ctx.fillRect(0, 0, 1, 1);
    const d = ctx.getImageData(0, 0, 1, 1).data;
    return [d[0], d[1], d[2], d[3] / 255];
  };
  const style = getComputedStyle(el);
  const fg = toRgb(style.color) ?? [0, 0, 0, 1];
  const border = toRgb(style.borderTopColor) ?? [0, 0, 0, 1];
  const over = (top: number[], under: number[]): number[] => {
    const a = top[3];
    return [
      Math.round(top[0] * a + under[0] * (1 - a)),
      Math.round(top[1] * a + under[1] * (1 - a)),
      Math.round(top[2] * a + under[2] * (1 - a)),
    ];
  };
  const layers: number[][] = [];
  let node: Element | null = el;
  while (node && node.nodeType === 1) {
    const rgba = toRgb(getComputedStyle(node).backgroundColor);
    if (rgba && !(rgba[3] === 0)) layers.push(rgba);
    node = node.parentElement;
  }
  let under: number[] = [255, 255, 255];
  for (let i = layers.length - 1; i >= 0; i--) {
    under = over(layers[i], under);
  }
  return [
    `rgb(${fg[0]}, ${fg[1]}, ${fg[2]})`,
    `rgb(${under[0]}, ${under[1]}, ${under[2]})`,
    `rgb(${border[0]}, ${border[1]}, ${border[2]})`,
  ];
};
async function expectButtonContrast(
  name: string,
  locator: import('@playwright/test').Locator,
  min = 4.5,
): Promise<{ fg: string; bg: string; border: string; ratio: number }> {
  const [fg, bg, border] = await locator.evaluate((el, src) => {
    const f = new Function(`return (${src})`)() as (e: Element) => [string, string, string];
    return f(el);
  }, sampleFgBgAndBorder.toString());
  const ratio = contrastRatio(parseRgb(fg), parseRgb(bg));
  expect(
    ratio,
    `${name}: contrast ${ratio.toFixed(2)} (${fg} on effective bg ${bg}) is below ${min}`,
  ).toBeGreaterThanOrEqual(min);
  return { fg, bg, border, ratio };
}

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

    const navBtn = page.getByTestId('nav-dashboard-btn');
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

    await page.getByTestId('nav-dashboard-btn').click();
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

    await page.getByTestId('nav-dashboard-btn').click();
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

    await page.getByTestId('nav-dashboard-btn').click();
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

    await page.getByTestId('menu-toggle').click();
    const navBtn = page.getByTestId('mobile-nav-dashboard-btn');
    await expect(navBtn).toBeVisible();
    const navBox = await navBtn.boundingBox();
    expect(navBox).not.toBeNull();
    expect(navBox!.width).toBeGreaterThanOrEqual(44);
    expect(navBox!.height).toBeGreaterThanOrEqual(44);

    await navBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('1 task');

    for (const id of [
      'dashboard-refresh-btn',
      'dashboard-back-btn',
      'dashboard-kpi-completion',
      'dashboard-kpi-active-claims',
      'dashboard-kpi-blocked',
      'dashboard-kpi-stalled',
      'dashboard-stalled-drilldown-btn',
      'dashboard-blocked-drilldown-btn',
    ]) {
      const box = await page.getByTestId(id).boundingBox();
      expect(box).not.toBeNull();
      expect(box!.height, `${id} height >= 44px`).toBeGreaterThanOrEqual(44);
      expect(box!.width, `${id} width >= 44px`).toBeGreaterThanOrEqual(44);
    }

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth, 'zero horizontal overflow on 375px dashboard').toBeLessThanOrEqual(375);

    await context.close();
  });

  test('KPI summary strip & dashboard widget drill-downs filter the board and sync URL (AC1, AC2)', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Drilldown ${randomUUID()}`,
      actor: SEED_ACTOR,
    });

    const blocker = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Drill Blocker P0',
      priority: 0,
      assignee: { name: 'alice', type: 'human' },
      actor: SEED_ACTOR,
    })).task;

    const blockedTask = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Drill Blocked P1',
      priority: 1,
      assignee: { name: 'bob', type: 'agent' },
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${blockedTask.id}/relations`, 'POST', {
      other_id: blocker.id,
      kind: 'blocked-by',
      actor: SEED_ACTOR,
    });

    const activeTask = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Drill Active Doing P1',
      priority: 1,
      assignee: { name: 'bob', type: 'agent' },
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${activeTask.id}/claim`, 'POST', {
      actor: { name: 'bob', type: 'agent' },
      ttl_sec: 300,
    });

    const expiredTask = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Drill Expired Doing P2',
      priority: 2,
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${expiredTask.id}/claim`, 'POST', {
      actor: { name: 'stale-bot', type: 'agent' },
      ttl_sec: 1,
    });

    await expect
      .poll(async () => {
        const report = await apiJson(`${serverUrl}/api/v1/projects/${proj.id}/report`, 'GET');
        return (report.expired_claims as Array<{ id: string }>).some((t) => t.id === expiredTask.id);
      }, { timeout: 15_000 })
      .toBe(true);

    const doneTask = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Drill Done P3',
      priority: 3,
      assignee: { name: 'alice', type: 'human' },
      actor: SEED_ACTOR,
    })).task;
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
    await selectProjectViaDropdown(page, proj.id);

    const dash = page.getByTestId('dashboard-view');
    await page.getByTestId('nav-dashboard-btn').click();
    await expect(dash).toBeVisible();

    // AC2: KPI strip values and amber highlight on Stalled > 0
    const kpiStrip = page.getByTestId('dashboard-kpi-strip');
    await expect(kpiStrip).toBeVisible();
    await expect(page.getByTestId('dashboard-kpi-completion')).toContainText('20%');
    await expect(page.getByTestId('dashboard-kpi-completion')).toContainText('1 / 5 done');
    await expect(page.getByTestId('dashboard-kpi-active-claims')).toContainText('1');
    await expect(page.getByTestId('dashboard-kpi-blocked')).toContainText('1');
    const kpiStalled = page.getByTestId('dashboard-kpi-stalled');
    await expect(kpiStalled).toContainText('1');
    await expect(kpiStalled).toHaveClass(/border-amber-300/);

    // 1. Status card drill-down (Doing) -> URL + active-drilldown-bar + dismiss chip
    await page.getByTestId('dashboard-status-doing').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain(`project=${proj.id}`);
    await expect.poll(() => page.url()).toContain('status=doing');
    const drillBar = page.getByTestId('active-drilldown-bar');
    await expect(drillBar).toBeVisible();
    await expect(drillBar).toContainText('Status: Doing');
    await expect(page.locator('article', { hasText: 'Drill Active Doing P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Expired Doing P2' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Blocked P1' })).toBeHidden();
    await expect(page.locator('article', { hasText: 'Drill Done P3' })).toBeHidden();

    await page.getByTestId('drilldown-chip-status').click();
    await expect(drillBar).toBeHidden();
    await expect.poll(() => page.url()).not.toContain('status=');
    await expect(page.locator('article', { hasText: 'Drill Done P3' })).toBeVisible();

    // 2. Priority row drill-down (P1)
    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-priority-1').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain('priority=1');
    await expect(page.locator('article', { hasText: 'Drill Blocked P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Active Doing P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Blocker P0' })).toBeHidden();
    await page.getByRole('button', { name: 'Reset filters' }).first().click();
    await expect.poll(() => page.url()).not.toContain('priority=');

    // 3. Workload owner drill-down (bob)
    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-workload-bob').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain('owner=bob');
    await expect(page.locator('article', { hasText: 'Drill Blocked P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Active Doing P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Blocker P0' })).toBeHidden();
    await page.getByRole('button', { name: 'Reset filters' }).first().click();

    // 4. Blocked KPI card & Bottlenecks CTA drill-down -> ?blocked=1 + Clear button
    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-kpi-blocked').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain('blocked=1');
    await expect(drillBar).toBeVisible();
    await expect(drillBar).toContainText('Blocked only');
    await expect(page.locator('article', { hasText: 'Drill Blocked P1' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Blocker P0' })).toBeHidden();
    await page.getByTestId('drilldown-clear-btn').click();
    await expect(drillBar).toBeHidden();
    await expect.poll(() => new URL(page.url()).searchParams.has('blocked')).toBe(false);

    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-blocked-drilldown-btn').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain('blocked=1');
    await page.getByTestId('drilldown-chip-blocked').click();
    await expect(drillBar).toBeHidden();

    // 5. Stalled KPI & section CTA drill-down -> ?stalled=1
    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-stalled-drilldown-btn').click();
    await expect(dash).toBeHidden();
    await expect.poll(() => page.url()).toContain('stalled=1');
    await expect(page.locator('article', { hasText: 'Drill Expired Doing P2' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Active Doing P1' })).toBeHidden();
    await page.getByRole('button', { name: 'Reset filters' }).first().click();

    // 6. Completion KPI -> ?status=done, Active Claims KPI -> ?status=doing
    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-kpi-completion').click();
    await expect.poll(() => page.url()).toContain('status=done');
    await expect(page.locator('article', { hasText: 'Drill Done P3' })).toBeVisible();
    await expect(page.locator('article', { hasText: 'Drill Blocker P0' })).toBeHidden();

    await page.getByTestId('nav-dashboard-btn').click();
    await page.getByTestId('dashboard-kpi-active-claims').click();
    await expect.poll(() => page.url()).toContain('status=doing');
  });

  test('dashboard meets WCAG 2.1 AA (axe) and >= 4.5:1 dark-mode header/toggle contrast (AC4)', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Contrast ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    const blocker = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Contrast Blocker',
      priority: 0,
      assignee: { name: 'alice', type: 'human' },
      actor: SEED_ACTOR,
    })).task;
    const blocked = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Contrast Blocked',
      priority: 1,
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${blocked.id}/relations`, 'POST', {
      other_id: blocker.id,
      kind: 'blocked-by',
      actor: SEED_ACTOR,
    });

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, proj.id);
    await page.getByTestId('nav-dashboard-btn').click();
    await expect(page.getByTestId('dashboard-kpi-strip')).toBeVisible();
    await expect(page.getByTestId('dashboard-throughput-svg')).toBeVisible();

    // Light theme axe audit + button contrast
    const lightAxe = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    expect(lightAxe.violations).toEqual([]);
    await expectButtonContrast('light dashboard-refresh-btn', page.getByTestId('dashboard-refresh-btn'));
    await expectButtonContrast('light dashboard-back-btn', page.getByTestId('dashboard-back-btn'));

    // Switch to dark mode and wait for CSS color transitions to settle
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('theme-dark').click();
    await page.keyboard.press('Escape');
    await expect(page.locator('html')).toHaveClass(/dark/);
    await page.waitForTimeout(250);

    // Dark theme axe audit
    const darkAxe = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    expect(darkAxe.violations).toEqual([]);

    // Dark theme computed contrast >= 4.5:1 on header controls, toggles, and drilldown buttons
    const refreshColors = await expectButtonContrast('dark dashboard-refresh-btn', page.getByTestId('dashboard-refresh-btn'));
    const backColors = await expectButtonContrast('dark dashboard-back-btn', page.getByTestId('dashboard-back-btn'));
    expect(refreshColors.bg).not.toBe(refreshColors.border);
    expect(backColors.bg).not.toBe(backColors.border);

    await expectButtonContrast('dark dashboard-range-7', page.getByTestId('dashboard-range-7'));
    await expectButtonContrast('dark dashboard-range-30', page.getByTestId('dashboard-range-30'));
    await expectButtonContrast('dark dashboard-bucket-day', page.getByTestId('dashboard-bucket-day'));
    await expectButtonContrast('dark dashboard-bucket-week', page.getByTestId('dashboard-bucket-week'));
    await expectButtonContrast('dark dashboard-stalled-drilldown-btn', page.getByTestId('dashboard-stalled-drilldown-btn'));
    await expectButtonContrast('dark dashboard-blocked-drilldown-btn', page.getByTestId('dashboard-blocked-drilldown-btn'));
  });
});
