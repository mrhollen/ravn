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

function relativeLuminance(rgb: [number, number, number]): number {
  const [r, g, b] = rgb.map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrastRatio(fg: [number, number, number], bg: [number, number, number]): number {
  const l1 = relativeLuminance(fg);
  const l2 = relativeLuminance(bg);
  const hi = Math.max(l1, l2);
  const lo = Math.min(l1, l2);
  return (hi + 0.05) / (lo + 0.05);
}

async function measureButtonContrast(locator: import('@playwright/test').Locator): Promise<number> {
  const { fg, bg } = await locator.evaluate((el) => {
    const parseColor = (cssVal: string): [number, number, number] => {
      const c = document.createElement('canvas');
      c.width = 1;
      c.height = 1;
      const ctx = c.getContext('2d')!;
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = cssVal;
      ctx.fillRect(0, 0, 1, 1);
      const data = ctx.getImageData(0, 0, 1, 1).data;
      return [data[0], data[1], data[2]];
    };
    const cs = getComputedStyle(el as HTMLElement);
    return {
      fg: parseColor(cs.color),
      bg: parseColor(cs.backgroundColor),
    };
  });
  return contrastRatio(fg, bg);
}

test.describe('Unified navigation, deep-linking & keyboard access (01M7XY9RJZDASZ1771YFFZEJMF)', () => {
  let serverUrl = '';
  let projA: { id: string; name: string };
  let projB: { id: string; name: string };
  let taskA: { id: string; title: string };
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    projA = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `DashNav Alpha ${randomUUID()}`,
      description: 'First seeded project for unified navigation',
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

    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projB.id,
      title: 'Beta DeepLink Task',
      priority: 0,
      assignee: { name: 'grace', type: 'human' },
      actor: SEED_ACTOR,
    });
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

  test('AC1/AC2: unified top-bar switches Projects | Board | Dashboard, hides Tier-2 toolbar on non-board views, and removes duplicates', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    // Zero legacy duplicate controls
    for (const oldId of ['dashboard-nav-btn', 'docked-board-nav-btn', 'docked-dashboard-nav-btn', 'mobile-dashboard-nav-btn']) {
      await expect(page.getByTestId(oldId)).toHaveCount(0);
    }
    await expect(page.locator('nav[aria-label="Primary navigation"]')).toHaveCount(1);

    const projectsBtn = page.getByTestId('nav-projects-btn');
    const boardBtn = page.getByTestId('nav-board-btn');
    const dashBtn = page.getByTestId('nav-dashboard-btn');
    const brandBtn = page.getByTestId('brand-logo-btn');
    const toolbar = page.getByTestId('board-toolbar');
    const projectsView = page.getByTestId('projects-overview-view');
    const dashView = page.getByTestId('dashboard-view');
    const boardMain = page.locator('main section', { has: page.getByRole('heading', { name: 'To Do' }) });

    // Initial Board state
    await expect(boardBtn).toHaveAttribute('aria-current', 'page');
    await expect(projectsBtn).not.toHaveAttribute('aria-current', 'page');
    await expect(dashBtn).not.toHaveAttribute('aria-current', 'page');
    await expect(toolbar).toBeVisible();
    await expect(boardMain).toBeVisible();
    await expect(projectsView).toBeHidden();
    await expect(dashView).toBeHidden();

    // 1. Board -> Projects Overview via nav-projects-btn
    await projectsBtn.click();
    await expect(projectsView).toBeVisible();
    await expect(projectsView.getByRole('heading', { name: 'Projects' })).toBeVisible();
    await expect(boardMain).toBeHidden();
    await expect(dashView).toBeHidden();
    await expect(toolbar).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/projects');
    await expect(page).toHaveTitle('Projects – Rävn');
    await expect(projectsBtn).toHaveAttribute('aria-current', 'page');
    await expect(boardBtn).not.toHaveAttribute('aria-current', 'page');
    await expect(dashBtn).not.toHaveAttribute('aria-current', 'page');

    // 2. Projects Overview -> Dashboard via nav-dashboard-btn
    await dashBtn.click();
    await expect(dashView).toBeVisible();
    await expect(projectsView).toBeHidden();
    await expect(boardMain).toBeHidden();
    await expect(toolbar).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashBtn).toHaveAttribute('aria-current', 'page');
    await expect(projectsBtn).not.toHaveAttribute('aria-current', 'page');
    await expect(boardBtn).not.toHaveAttribute('aria-current', 'page');
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('1 task');

    // 3. Dashboard -> Board via nav-board-btn
    await boardBtn.click();
    await expect(boardMain).toBeVisible();
    await expect(toolbar).toBeVisible();
    await expect(dashView).toBeHidden();
    await expect(projectsView).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(boardBtn).toHaveAttribute('aria-current', 'page');

    // 4. Brand logo button -> Projects Overview -> Board
    await brandBtn.click();
    await expect(projectsView).toBeVisible();
    await expect(toolbar).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/projects');

    await boardBtn.click();
    await expect(boardMain).toBeVisible();
    await expect(toolbar).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/');
  });

  test('AC3: mobile 375px menu nav round-trips Projects | Board | Dashboard with >= 44px tap targets, zero overflow, and dark contrast', async ({ browser }) => {
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

    // Desktop nav is hidden at 375px; legacy mobile button removed
    await expect(page.getByTestId('primary-nav')).toBeHidden();
    await expect(page.getByTestId('mobile-dashboard-nav-btn')).toHaveCount(0);

    const menuToggle = page.getByTestId('menu-toggle');
    await menuToggle.click();

    const mobileProjectsBtn = page.getByTestId('mobile-nav-projects-btn');
    const mobileBoardBtn = page.getByTestId('mobile-nav-board-btn');
    const mobileDashBtn = page.getByTestId('mobile-nav-dashboard-btn');

    for (const [label, btn] of [
      ['mobile-nav-projects-btn', mobileProjectsBtn],
      ['mobile-nav-board-btn', mobileBoardBtn],
      ['mobile-nav-dashboard-btn', mobileDashBtn],
    ] as const) {
      await expect(btn).toBeVisible();
      const box = await btn.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.height, `${label} height >= 44px`).toBeGreaterThanOrEqual(44);
      expect(box!.width, `${label} width >= 44px`).toBeGreaterThanOrEqual(44);
    }
    await expect(mobileBoardBtn).toHaveAttribute('aria-current', 'page');

    // Navigate to Projects Overview via mobile menu; menu auto-closes & filters hide
    await mobileProjectsBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/projects');
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

    await menuToggle.click();
    await expect(mobileProjectsBtn).toHaveAttribute('aria-current', 'page');
    await expect(page.getByTestId('mobile-group-filters')).toBeHidden();

    // Switch to dark mode and verify contrast >= 4.5:1 on active & inactive mobile nav buttons
    await page.getByTestId('mobile-theme-dark').click();
    await expect(page.locator('html')).toHaveClass(/dark/);
    await expect
      .poll(() => measureButtonContrast(mobileProjectsBtn), { timeout: 2000 })
      .toBeGreaterThanOrEqual(4.5);
    await expect
      .poll(() => measureButtonContrast(mobileBoardBtn), { timeout: 2000 })
      .toBeGreaterThanOrEqual(4.5);

    // Navigate to Dashboard via mobile menu; menu auto-closes
    await mobileDashBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

    // Navigate back to Board via mobile menu
    await menuToggle.click();
    await expect(mobileDashBtn).toHaveAttribute('aria-current', 'page');
    await expect(page.getByTestId('mobile-group-filters')).toBeHidden();
    await mobileBoardBtn.click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeHidden();
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    await menuToggle.click();
    await expect(page.getByTestId('mobile-group-filters')).toBeVisible();

    await context.close();
  });

  test('keyboard access: Tab + Enter on nav buttons, "d" shortcut across views, and Escape return to board', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    // Tab walk reaches nav-projects-btn, nav-board-btn, nav-dashboard-btn with :focus-visible
    for (const targetId of ['nav-projects-btn', 'nav-board-btn', 'nav-dashboard-btn']) {
      let hit = false;
      for (let i = 0; i < 15 && !hit; i++) {
        await page.keyboard.press('Tab');
        hit = await page.evaluate(
          (id) => document.activeElement?.getAttribute('data-testid') === id,
          targetId,
        );
      }
      expect(hit, `keyboard Tab reached ${targetId}`).toBe(true);
      expect(
        await page.getByTestId(targetId).evaluate((el) => el.matches(':focus-visible')),
        `${targetId} matches :focus-visible on keyboard Tab`,
      ).toBe(true);
    }

    // Activate nav-dashboard-btn with Enter
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    // Escape closes dashboard back to board when no overlay is open
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    // Open Projects Overview and press Escape -> returns to Board
    await page.getByTestId('nav-projects-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    // Bare "d" shortcut is suppressed while typing in an input
    const searchInput = page.getByTestId('board-search-input');
    await searchInput.focus();
    await page.keyboard.type('d');
    await expect(searchInput).toHaveValue('d');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    await searchInput.fill('');
    await searchInput.blur();

    // Bare "d" toggles Board <-> Dashboard, and from Projects Overview opens Dashboard
    await page.keyboard.press('d');
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    await page.keyboard.press('d');
    await expect(page.getByTestId('dashboard-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');

    await page.getByTestId('nav-projects-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    await page.keyboard.press('d');
    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
  });

  test('browser Back/Forward navigates cleanly across / <-> /projects <-> /projects/<id>/dashboard <-> /tasks/<id>', async ({ page }) => {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, projA.id);

    const projectsView = page.getByTestId('projects-overview-view');
    const dashView = page.getByTestId('dashboard-view');
    const drawer = page.locator('#cb-detail-drawer');
    const boardCol = page.locator('main section', { has: page.getByRole('heading', { name: 'To Do' }) });

    // 1. Board -> Projects Overview -> Dashboard
    await page.getByTestId('nav-projects-btn').click();
    await expect(projectsView).toBeVisible();
    expect(new URL(page.url()).pathname).toBe('/projects');

    await page.getByTestId('nav-dashboard-btn').click();
    await expect(dashView).toBeVisible();
    await expect(projectsView).toBeHidden();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    // 2. Dashboard -> Task Modal -> Full-page (/tasks/<id>)
    await apiJson(`${serverUrl}/api/v1/tasks/${taskA.id}/claim`, 'POST', {
      actor: { name: 'ada', type: 'human' },
      ttl_sec: 300,
    });
    await page.getByTestId('dashboard-refresh-btn').click();
    const claimsSec = page.getByTestId('dashboard-active-claims');
    await expect(claimsSec).toContainText('Alpha Nav Task');

    await claimsSec.getByRole('button', { name: 'Alpha Nav Task' }).click();
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));

    await page.getByTestId('open-fullpage-btn').click();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(dashView).toBeHidden();
    await expect(page.getByTestId('fullpage-back-btn')).toBeVisible();

    // Back: /tasks/<id> -> /projects/<id>/dashboard -> /projects -> /
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();
    await expect(drawer).toBeHidden();

    await page.goBack();
    expect(new URL(page.url()).pathname).toBe('/projects');
    await expect(projectsView).toBeVisible();
    await expect(dashView).toBeHidden();

    await page.goBack();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(projectsView).toBeHidden();
    await expect(boardCol).toBeVisible();

    // Forward: / -> /projects -> /projects/<id>/dashboard -> /tasks/<id>
    await page.goForward();
    expect(new URL(page.url()).pathname).toBe('/projects');
    await expect(projectsView).toBeVisible();

    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();

    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(dashView).toBeHidden();
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();

    // From full-page task detail, click nav-dashboard-btn -> navigates to dashboard
    await page.getByTestId('nav-dashboard-btn').click();
    await expect(page).toHaveURL(new RegExp(`/projects/${projA.id}/dashboard$`));
    await expect(dashView).toBeVisible();
    await expect(drawer).toBeHidden();

    // Back returns to /tasks/<id>
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();

    // From /tasks/<id>, click nav-projects-btn -> navigates to /projects
    await page.getByTestId('nav-projects-btn').click();
    expect(new URL(page.url()).pathname).toBe('/projects');
    await expect(projectsView).toBeVisible();
    await expect(drawer).toBeHidden();

    // Back returns to /tasks/<id>
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`/tasks/${taskA.id}$`));
    await expect(drawer.getByRole('heading', { name: 'Alpha Nav Task' })).toBeVisible();
  });

  test('direct server-served fresh loads of /projects, /projects/, and /projects/<id>/dashboard', async ({ page }) => {
    // 1. Direct fresh load of /projects/ normalizes to /projects and renders Projects Overview
    await page.goto(`${serverUrl}/projects/`);
    await expect(page).toHaveURL(/\/projects$/);
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    await expect(page.getByTestId('board-toolbar')).toBeHidden();
    await expect(page).toHaveTitle('Projects – Rävn');
    await expect(page.getByTestId('nav-projects-btn')).toHaveAttribute('aria-current', 'page');

    // 2. Store projA as selected project, then deep-link to /projects/<projB.id>/dashboard
    await page.getByTestId('nav-board-btn').click();
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, projA.name);

    await page.goto(`${serverUrl}/projects/${projB.id}/dashboard`);
    await expect(page).toHaveURL(new RegExp(`/projects/${projB.id}/dashboard$`));
    await expectProjectSelected(page, projB.name);

    const dashView = page.getByTestId('dashboard-view');
    await expect(dashView).toBeVisible();
    await expect(page.getByTestId('board-toolbar')).toBeHidden();
    await expect(page.getByTestId('nav-dashboard-btn')).toHaveAttribute('aria-current', 'page');
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
