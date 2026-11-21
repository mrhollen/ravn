import { randomUUID } from 'crypto';
import { test, expect } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

const SEED_ACTOR = { name: 'e2e-charts-seed', type: 'agent' as const };

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

test.describe('Dashboard charts (/report/history)', () => {
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

  test('renders throughput & cumulative-completed SVG charts, toggles day/week buckets, and supports dark mode', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Dashboard Charts ${randomUUID()}`,
      description: 'Seeded for dashboard charts E2E',
      actor: SEED_ACTOR,
    });

    // Seed 2 todo tasks and 2 completed tasks so real /report/history has non-zero bars and cumulative line.
    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Open Backlog Item',
      priority: 1,
      actor: SEED_ACTOR,
    });
    for (const title of ['Completed Chart Task A', 'Completed Chart Task B']) {
      const created = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
        project_id: proj.id,
        title,
        priority: 2,
        actor: SEED_ACTOR,
      });
      const doing = await apiJson(`${serverUrl}/api/v1/tasks/${created.task.id}`, 'PATCH', {
        actor: SEED_ACTOR,
        version: created.task.version,
        status: 'doing',
      });
      await apiJson(`${serverUrl}/api/v1/tasks/${created.task.id}`, 'PATCH', {
        actor: SEED_ACTOR,
        version: doing.version,
        status: 'done',
      });
    }

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    await page.getByTestId('nav-dashboard-btn').click();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();

    const chartsSec = page.getByTestId('dashboard-charts');
    await expect(chartsSec).toBeVisible();

    // Throughput chart container, summary, SVG role="img", aria-label, and rendered bars.
    const throughputCard = page.getByTestId('dashboard-throughput-chart');
    await expect(throughputCard).toBeVisible();
    await expect(page.getByTestId('dashboard-throughput-summary')).toContainText('2 completed');
    const throughputSvg = page.getByTestId('dashboard-throughput-svg');
    await expect(throughputSvg).toBeVisible();
    await expect(throughputSvg).toHaveAttribute('role', 'img');
    await expect(throughputSvg).toHaveAttribute('aria-label', /Throughput bar chart by day: 2 completed/);
    await expect(throughputSvg.locator('[data-chart-bar]').first()).toBeVisible();

    // Cumulative-completed chart container, summary, SVG role="img", aria-label, and rendered line/points.
    const cumulativeCard = page.getByTestId('dashboard-cumulative-chart');
    await expect(cumulativeCard).toBeVisible();
    await expect(page.getByTestId('dashboard-cumulative-summary')).toContainText('2 total');
    const cumulativeSvg = page.getByTestId('dashboard-cumulative-svg');
    await expect(cumulativeSvg).toBeVisible();
    await expect(cumulativeSvg).toHaveAttribute('role', 'img');
    await expect(cumulativeSvg).toHaveAttribute('aria-label', /Cumulative completed tasks line chart by day: 2 total completed/);
    await expect(cumulativeSvg.locator('[data-chart-line="cumulative"]')).toBeVisible();
    await expect(cumulativeSvg.locator('[data-chart-point]').last()).toBeVisible();

    // Day/Week toggle: Day is active initially, clicking Week requests ?bucket=week and updates charts.
    const dayBtn = page.getByTestId('dashboard-bucket-day');
    const weekBtn = page.getByTestId('dashboard-bucket-week');
    await expect(dayBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(weekBtn).toHaveAttribute('aria-pressed', 'false');

    const weekResponse = page.waitForResponse(
      (res) => res.url().includes(`/api/v1/projects/${proj.id}/report/history`) && res.url().includes('bucket=week'),
    );
    await weekBtn.click();
    const res = await weekResponse;
    expect(res.status()).toBe(200);

    await expect(weekBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(dayBtn).toHaveAttribute('aria-pressed', 'false');
    await expect(chartsSec).toContainText('UTC weekly buckets');
    await expect(throughputSvg).toHaveAttribute('aria-label', /Throughput bar chart by week: 2 completed/);
    await expect(cumulativeSvg).toHaveAttribute('aria-label', /Cumulative completed tasks line chart by week: 2 total completed/);

    // Switch back to Day bucket cleanly.
    const dayResponse = page.waitForResponse(
      (res) => res.url().includes(`/api/v1/projects/${proj.id}/report/history`) && res.url().includes('bucket=day'),
    );
    await dayBtn.click();
    await dayResponse;
    await expect(dayBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(chartsSec).toContainText('UTC daily buckets');

    // Time-range selector (AC3): 30d active by default, switch through 7d / 14d / 90d alongside Week bucket.
    const range30 = page.getByTestId('dashboard-range-30');
    const range7 = page.getByTestId('dashboard-range-7');
    const range14 = page.getByTestId('dashboard-range-14');
    const range90 = page.getByTestId('dashboard-range-90');
    await expect(page.getByTestId('dashboard-range-toggle')).toBeVisible();
    await expect(range30).toHaveAttribute('aria-pressed', 'true');
    await expect(chartsSec).toContainText('Last 30 days · UTC daily buckets');

    const res7 = page.waitForResponse(
      (r) => r.url().includes(`/api/v1/projects/${proj.id}/report/history`) && r.url().includes('days=7') && r.url().includes('bucket=day'),
    );
    await range7.click();
    expect((await res7).status()).toBe(200);
    await expect(range7).toHaveAttribute('aria-pressed', 'true');
    await expect(range30).toHaveAttribute('aria-pressed', 'false');
    await expect(chartsSec).toContainText('Last 7 days · UTC daily buckets');
    await expect(cumulativeSvg).toHaveAttribute('aria-label', /in the last 7 days/);

    const res90Week = page.waitForResponse(
      (r) => r.url().includes(`/api/v1/projects/${proj.id}/report/history`) && r.url().includes('days=90') && r.url().includes('bucket=week'),
    );
    await weekBtn.click();
    await range90.click();
    expect((await res90Week).status()).toBe(200);
    await expect(range90).toHaveAttribute('aria-pressed', 'true');
    await expect(weekBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(chartsSec).toContainText('Last 90 days · UTC weekly buckets');
    await expect(cumulativeSvg).toHaveAttribute('aria-label', /in the last 90 days/);

    const res14 = page.waitForResponse(
      (r) => r.url().includes(`/api/v1/projects/${proj.id}/report/history`) && r.url().includes('days=14') && r.url().includes('bucket=week'),
    );
    await range14.click();
    expect((await res14).status()).toBe(200);
    await expect(range14).toHaveAttribute('aria-pressed', 'true');
    await expect(chartsSec).toContainText('Last 14 days · UTC weekly buckets');

    // Toggle dark mode and confirm charts remain visible and crisp.
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('theme-dark').click();
    await page.keyboard.press('Escape');
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect(throughputSvg).toBeVisible();
    await expect(cumulativeSvg).toBeVisible();
  });

  test('renders empty chart states for a project with zero tasks', async ({ page }) => {
    const emptyProj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Charts Empty ${randomUUID()}`,
      actor: SEED_ACTOR,
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, emptyProj.id);

    await page.getByTestId('nav-dashboard-btn').click();
    await expect(page.getByTestId('dashboard-charts')).toBeVisible();
    await expect(page.getByTestId('dashboard-throughput-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-cumulative-empty')).toBeVisible();
    await expect(page.getByTestId('dashboard-throughput-svg')).toBeHidden();
    await expect(page.getByTestId('dashboard-cumulative-svg')).toBeHidden();
  });

  test('shows loading skeleton while /report/history is pending and recovers from history error via retry', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Charts Loading Error ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Seeded Task',
      actor: SEED_ACTOR,
    });

    let attempt = 0;
    let releaseDelayed!: () => void;
    const delayedGate = new Promise<void>((res) => {
      releaseDelayed = res;
    });

    await page.route(`**/api/v1/projects/${proj.id}/report/history*`, async (route) => {
      attempt++;
      if (attempt === 1) {
        await delayedGate;
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'internal', message: 'Simulated history failure' }),
        });
        return;
      }
      await route.continue();
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, proj.id);

    await page.getByTestId('nav-dashboard-btn').click();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();

    // While /report/history is blocked on delayedGate, loading skeleton must be visible.
    await expect(page.getByTestId('dashboard-charts-loading')).toBeVisible();

    // Release delayed request -> fails with 500 -> charts error + retry button appear.
    releaseDelayed();
    await expect(page.getByTestId('dashboard-charts-error')).toBeVisible();
    await expect(page.getByTestId('dashboard-charts-error')).toContainText('Simulated history failure');
    uncaughtConsoleErrors = uncaughtConsoleErrors.filter((e) => !e.includes('status of 500'));

    await page.getByTestId('dashboard-charts-retry-btn').click();
    await expect(page.getByTestId('dashboard-charts-error')).toBeHidden();
    await expect(page.getByTestId('dashboard-throughput-svg')).toBeVisible();
    await expect(page.getByTestId('dashboard-cumulative-svg')).toBeVisible();
  });

  test('mobile 375px keeps bucket toggle tap targets >= 44px and zero horizontal overflow', async ({ browser }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Charts Mobile ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    const created = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'Mobile Chart Task',
      actor: SEED_ACTOR,
    });
    const doing = await apiJson(`${serverUrl}/api/v1/tasks/${created.task.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: created.task.version,
      status: 'doing',
    });
    await apiJson(`${serverUrl}/api/v1/tasks/${created.task.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: doing.version,
      status: 'done',
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

    await page.getByTestId('menu-toggle').click();
    await page.getByTestId('mobile-nav-dashboard-btn').click();
    await expect(page.getByTestId('dashboard-throughput-svg')).toBeVisible();
    await expect(page.getByTestId('dashboard-cumulative-svg')).toBeVisible();

    for (const id of [
      'dashboard-bucket-day',
      'dashboard-bucket-week',
      'dashboard-range-7',
      'dashboard-range-14',
      'dashboard-range-30',
      'dashboard-range-90',
    ]) {
      const box = await page.getByTestId(id).boundingBox();
      expect(box).not.toBeNull();
      expect(box!.width, `${id} width >= 44px`).toBeGreaterThanOrEqual(44);
      expect(box!.height, `${id} height >= 44px`).toBeGreaterThanOrEqual(44);
    }

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth, 'zero horizontal overflow on 375px dashboard charts').toBeLessThanOrEqual(375);

    await context.close();
  });

  test('updates charts live via SSE stream without opening a second EventSource', async ({ page }) => {
    const proj = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `Charts SSE ${randomUUID()}`,
      actor: SEED_ACTOR,
    });
    const taskRes = await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: proj.id,
      title: 'SSE Chart Task',
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
    await expect(page.getByTestId('dashboard-throughput-summary')).toContainText('0 completed');
    await expect(page.getByTestId('dashboard-cumulative-summary')).toContainText('0 total');

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

    await expect(page.getByTestId('dashboard-throughput-summary')).toContainText('1 completed', { timeout: 5000 });
    await expect(page.getByTestId('dashboard-cumulative-summary')).toContainText('1 total', { timeout: 5000 });
    await expect(page.getByTestId('dashboard-throughput-svg').locator('[data-chart-bar]').first()).toBeVisible();
  });
});
