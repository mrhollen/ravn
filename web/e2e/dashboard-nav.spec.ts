import { randomUUID } from 'crypto';
import { test, expect } from './fixtures';
import { expectProjectSelected, selectProjectViaDropdown } from './helpers';

const SEED_ACTOR = { name: 'e2e-dashboard-nav-seed', type: 'agent' as const };

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

test.describe('Dashboard navigation, deep-linking & keyboard access (01M75VJ7GWS2M6TZ2K5MVPQ4P6)', () => {
  let serverUrl = '';
  let projA: { id: string; name: string };
  let projB: { id: string; name: string };
  let taskA: { id: string; title: string };
  let taskB: { id: string; title: string };
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    projA = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `DashNav Alpha ${randomUUID()}`,
      description: 'First seeded project for dashboard navigation',
      actor: SEED_ACTOR,
    });
    projB = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `DashNav Beta ${randomUUID()}`,
      description: 'Second seeded project for deep-link adoption',
      actor: SEED_ACTOR,
    });

    taskA = (
      await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
        project_id: projA.id,
        title: 'Alpha Nav Task',
        priority: 1,
        assignee: { name: 'ada', type: 'human' },
        actor: SEED_ACTOR,
      })
    ).task;

    taskB = (
      await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
        project_id: projB.id,
        title: 'Beta DeepLink Task',
        priority: 0,
        assignee: { name: 'grace', type: 'human' },
        actor: SEED_ACTOR,
      })
    ).task;
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

  test('round-trips board <-> dashboard across header and docked nav entries', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    const topBtn = page.getByTestId('dashboard-nav-btn');
    const dockedBoardBtn = page.getByTestId('docked-board-nav-btn');
    const dockedDashBtn = page.getByTestId('docked-dashboard-nav-btn');
    const dashView = page.getByTestId('dashboard-view');
    const boardMain = page.locator('main section', { has: page.getByRole('heading', { name: 'To Do' }) });

    // 1. Header nav -> dashboard -> header nav -> board
    await expect(topBtn).toBeEnabled();
    await expect(topBtn).toHaveAttribute('aria-pressed', 'false');
    await topBtn.click();

    await expect(dashView).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(topBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(dockedDashBtn).toHaveAttribute('aria-pressed', 'true');
    await expect(dockedBoardBtn).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('1 task');

    await topBtn.click();
    await expect(dashView).toBeHidden();
    await expect(boardMain).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(topBtn).toHaveAttribute('aria-pressed', 'false');

    // 2. Docked nav -> dashboard -> docked board/dashboard nav -> board
    await dockedDashBtn.click();
    await expect(dashView).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dockedDashBtn).toHaveAttribute('aria-pressed', 'true');

    await dockedBoardBtn.click();
    await expect(dashView).toBeHidden();
    await expect(boardMain).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(dockedBoardBtn).toHaveAttribute('aria-pressed', 'true');

    // Docked dashboard button also toggles cleanly when clicked twice
    await dockedDashBtn.click();
    await expect(dashView).toBeVisible();
    await dockedDashBtn.click();
    await expect(dashView).toBeHidden();
    await expect(boardMain).toBeVisible();
  });

  test('mobile 375px menu nav round-trips board <-> dashboard with >= 44px tap target and zero overflow', async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const page = await context.newPage();
    page.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtConsoleErrors.push(`[mobile console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[mobile pageerror] ${err.message}`);
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    const menuToggle = page.getByTestId('menu-toggle');
    await menuToggle.click();

    const mobileDashBtn = page.getByTestId('mobile-dashboard-nav-btn');
    await expect(mobileDashBtn).toBeVisible();
    const box = await mobileDashBtn.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.height, 'mobile-dashboard-nav-btn height >= 44px').toBeGreaterThanOrEqual(44);
    expect(box!.width, 'mobile-dashboard-nav-btn width >= 44px').toBeGreaterThanOrEqual(44);

    // Navigate to dashboard via mobile menu; menu auto-closes
    await mobileDashBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(scrollWidth, 'zero horizontal overflow on 375px dashboard').toBeLessThanOrEqual(375);

    // Navigate back to board via mobile menu
    await menuToggle.click();
    await expect(mobileDashBtn).toContainText('Back to board');
    await mobileDashBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    await context.close();
  });

  test('keyboard access: Tab + Enter/Space on nav buttons, "d" shortcut toggle, and Escape return to board', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    // Tab walk reaches dashboard-nav-btn and activates with Enter
    let hit = false;
    for (let i = 0; i < 15 && !hit; i++) {
      await page.keyboard.press('Tab');
      hit = await page.evaluate(
        () => document.activeElement?.getAttribute('data-testid') === 'dashboard-nav-btn',
      );
    }
    expect(hit, 'keyboard Tab reached dashboard-nav-btn').toBe(true);
    expect(
      await page.getByTestId('dashboard-nav-btn').evaluate((el) => el.matches(':focus-visible')),
      'dashboard-nav-btn matches :focus-visible on keyboard Tab',
    ).toBe(true);

    await page.keyboard.press('Enter');
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    // Escape closes dashboard back to board when no overlay is open
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    // Bare "d" shortcut is suppressed while typing in an input
    const searchInput = page.getByTestId('board-search-input');
    await searchInput.focus();
    await page.keyboard.type('d');
    await expect(searchInput).toHaveValue('d');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    await searchInput.fill('');
    await searchInput.blur();

    // Bare "d" shortcut opens dashboard when focus is not in an input, and toggles closed
    await page.keyboard.press('d');
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    await page.keyboard.press('d');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');
  });

  test('browser Back/Forward navigates cleanly across board <-> task detail (/tasks/<id> and modal) <-> dashboard', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    const dashView = page.getByTestId('dashboard-view');
    const drawer = page.locator('#cb-detail-drawer');
    const boardCol = page.locator('main section', { has: page.getByRole('heading', { name: 'To Do' }) });

    // 1. Board -> Dashboard
    await page.getByTestId('dashboard-nav-btn').click();
    await expect(dashView).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    // 2. Dashboard -> Task Modal (via workload/task click? Let's claim taskA or open from recently/claims — wait: taskA is in todo! Let's check: or seed a claim on taskA!)
    // Actually let's check board -> modal -> full page -> dashboard -> back/forward too, AND dashboard -> full page:
    // Let's seed an active claim on taskA's copy or just claim taskA right here so it appears in Active Claims on dashboard!
    await apiJson(`${serverUrl}/api/v1/tasks/${taskA.id}/claim`, 'POST', {
      actor: { name: 'ada', type: 'human' },
      ttl_sec: 300,
    });
    await page.getByTestId('dashboard-refresh-btn').click();
    const claimsSec = page.getByTestId('dashboard-active-claims');
    await expect(claimsSec).toContainText('Alpha Nav Task');

    // Open task modal from dashboard -> URL stays /projects/<id>/dashboard
    await claimsSec.getByRole('button', { name: 'Alpha Nav Task' }).click();
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    // Upgrade modal -> full-page (/tasks/<id>)
    await page.getByTestId('open-fullpage-btn').click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(dashView).toBeHidden();
    await expect(page.getByTestId('fullpage-back-btn')).toBeVisible();

    // Back: /tasks/<id> -> /projects/<id>/dashboard
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();
    await expect(drawer).toBeHidden();

    // Back: /projects/<id>/dashboard -> / (board)
    await page.goBack();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(dashView).toBeHidden();
    await expect(boardCol).toBeVisible();

    // Forward: / -> /projects/<id>/dashboard
    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();

    // Forward: /projects/<id>/dashboard -> /tasks/<id>
    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(dashView).toBeHidden();
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();

    // From full-page task detail, click header dashboard-nav-btn -> navigates to dashboard
    await page.getByTestId('dashboard-nav-btn').click();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();
    await expect(drawer).toBeHidden();

    // Back returns to /tasks/<id>
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();
  });

  test('direct server-served fresh load of /projects/<id>/dashboard adopts project and opens dashboard', async ({ page }) => {
    // Store projA as the previously selected project in localStorage
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, projA.name);

    // Direct fresh load of /projects/<projB.id>/dashboard via server SPA fallback
    await page.goto(`${serverUrl}/projects/${projB.id}/dashboard`);
    await expect(page).toHaveURL(new RegExp(`/projects/${projB.id}/dashboard$`));

    // Verify projB was adopted (overriding stored projA) and dashboard rendered
    await expectProjectSelected(page, projB.name);
    const dashView = page.getByTestId('dashboard-view');
    await expect(dashView).toBeVisible();
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('1 task');
    await expect(page).toHaveTitle(new RegExp(`^Dashboard .* ${projB.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`));

    // Dark mode renders cleanly on deep-linked dashboard
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('theme-dark').click();
    await page.keyboard.press('Escape');
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect(dashView).toBeVisible();

    // Switching project while on dashboard updates URL to /projects/<projA.id>/dashboard
    await selectProjectViaDropdown(page, projA.id);
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();

    // Back to board button from deep-linked dashboard replaces URL cleanly without leaving app
    await page.getByTestId('dashboard-back-btn').click();
    await expect(dashView).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.locator('article', { hasText: 'Alpha Nav Task' })).toBeVisible();
  });
});
