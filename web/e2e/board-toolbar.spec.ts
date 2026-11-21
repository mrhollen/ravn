import { test, expect } from './fixtures';
import { AxeBuilder } from '@axe-core/playwright';
import { selectProjectViaDropdown } from './helpers';

const ACTOR = { name: 'e2e-toolbar', type: 'agent' as const };

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}: ${await res.text()}`);
  return res.json();
}

test.describe('Board toolbar declutter & Display popover (AC1-AC4)', () => {
  let serverUrl = '';
  let projectId = '';
  let cleanProjectId = '';
  let uncaughtErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;
    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Toolbar Declutter Project',
      description: 'Seeded for board-toolbar E2E tests',
      actor: ACTOR,
    });
    projectId = proj.id;

    // Seed a claimed task that immediately shows as expired/stalled so the
    // stalled-alert-pill is rendered alongside the Tier-2 toolbar controls.
    const t1 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Stalled Toolbar Task',
      priority: 3,
      actor: ACTOR,
    })).task;
    const claimRes = await fetch(`${serverUrl}/api/v1/tasks/${t1.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: ACTOR, ttl_sec: 1 }),
    });
    expect(claimRes.ok).toBe(true);

    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Active Toolbar Task',
      priority: 2,
      actor: ACTOR,
    });

    const cleanProj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Toolbar Clean Audit Project',
      description: 'Seeded for WCAG audit with Display popover open',
      actor: ACTOR,
    });
    cleanProjectId = cleanProj.id;
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: cleanProjectId,
      title: 'Clean Audit Task',
      priority: 2,
      actor: ACTOR,
    });
  });

  test.beforeEach(async ({ page }) => {
    uncaughtErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtErrors.push(`[pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtErrors).toEqual([]);
  });

  test('AC1: header + Tier-2 toolbar render in <= 2 total rows at 1280x800 with all triage controls on one row', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const toolbar = page.getByTestId('board-toolbar');
    await expect(toolbar).toBeVisible();

    // Wait for the stalled alert pill (ttl_sec: 1) so we verify worst-case row packing too.
    const stalledPill = page.getByTestId('stalled-alert-pill');
    await expect(stalledPill).toBeVisible({ timeout: 5000 });

    const controls = [
      page.getByTestId('board-search-input'),
      page.getByTestId('board-filter-capsule'),
      page.getByTestId('board-scope-select'),
      stalledPill,
      page.getByTestId('display-popover-btn'),
    ];
    for (const loc of controls) {
      await expect(loc).toBeVisible();
    }

    const boxes = await Promise.all(controls.map((c) => c.boundingBox()));
    for (const b of boxes) {
      expect(b).not.toBeNull();
    }

    // All Tier-2 controls share a single horizontal row (centers within <= 4px).
    const centers = boxes.map((b) => b!.y + b!.height / 2);
    const minCenter = Math.min(...centers);
    const maxCenter = Math.max(...centers);
    expect(maxCenter - minCenter, `Tier-2 centers diverged: ${JSON.stringify(centers)}`).toBeLessThanOrEqual(4);

    // Header (Row 1 + Tier-2 Row 2) stays compact within 2 total rows (<= 104px) and zero horizontal overflow.
    const headerBox = (await page.locator('header').first().boundingBox())!;
    expect(headerBox.height).toBeLessThanOrEqual(104);
    const toolbarBox = (await toolbar.boundingBox())!;
    expect(toolbarBox.height).toBeLessThanOrEqual(44);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1280);
  });

  test('AC2: Display popover keyboard/outside-click behavior and active non-default indicator badge', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const btn = page.getByTestId('display-popover-btn');
    const panel = page.getByTestId('display-popover');
    const badge = page.getByTestId('display-active-indicator');

    await expect(btn).toBeVisible();
    await expect(btn).toHaveAttribute('aria-haspopup', 'true');
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    await expect(panel).toBeHidden();
    await expect(badge).toBeHidden();

    // Open via click
    await btn.click();
    await expect(panel).toBeVisible();
    await expect(btn).toHaveAttribute('aria-expanded', 'true');

    // Tab navigates naturally through visible controls inside the popover
    const sortKey = page.getByTestId('sort-key');
    await expect(sortKey).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.getByTestId('sort-hoist-parents')).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.getByTestId('card-density-toggle')).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.getByTestId('show-archived-toggle')).toBeFocused();

    // Escape closes popover and returns focus to display-popover-btn
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    await expect(btn).toBeFocused();

    // Outside click closes popover without stealing focus back to display-popover-btn
    await btn.click();
    await expect(panel).toBeVisible();
    const searchInput = page.getByTestId('board-search-input');
    await searchInput.click();
    await expect(panel).toBeHidden();
    await expect(searchInput).toBeFocused();

    // Active non-default indicator badge tracks sort, hoist-parents, card-density, and show-archived
    await btn.click();
    await expect(badge).toBeHidden();

    await sortKey.selectOption('priority');
    await expect(badge).toBeVisible();
    await expect(badge).toContainText('1');

    await page.getByTestId('sort-hoist-parents').click();
    await expect(badge).toContainText('2');

    await page.getByTestId('card-density-toggle').click();
    await expect(badge).toContainText('3');

    await page.getByTestId('show-archived-toggle').click();
    await expect(badge).toContainText('4');

    // Restore defaults -> badge disappears again
    await page.getByTestId('show-archived-toggle').click();
    await page.getByTestId('card-density-toggle').click();
    await page.getByTestId('sort-hoist-parents').click();
    await page.getByTestId('sort-clear').click();
    await expect(badge).toBeHidden();
  });

  test('AC3: sort-key options are not truncated and board-scope-select aligns with Tier-2 filter controls', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const filterBox = (await page.getByTestId('board-filter-capsule').boundingBox())!;
    const scopeSelect = page.getByTestId('board-scope-select');
    const scopeBox = (await scopeSelect.boundingBox())!;
    const displayBtnBox = (await page.getByTestId('display-popover-btn').boundingBox())!;

    // Height and vertical alignment match adjacent Tier-2 filter controls
    expect(Math.abs(scopeBox.height - filterBox.height)).toBeLessThanOrEqual(2);
    expect(Math.abs(scopeBox.height - displayBtnBox.height)).toBeLessThanOrEqual(2);
    expect(Math.abs(scopeBox.y - filterBox.y)).toBeLessThanOrEqual(2);

    // Sort select inside Display popover has generous width (no max-w-[8rem] truncation)
    await page.getByTestId('display-popover-btn').click();
    const sortKey = page.getByTestId('sort-key');
    await expect(sortKey).toBeVisible();
    const sortBox = (await sortKey.boundingBox())!;
    expect(sortBox.width).toBeGreaterThan(160);
    const notTruncated = await sortKey.evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
    expect(notTruncated).toBe(true);
  });

  test('AC4: light and dark mode WCAG 2.1 AA audit with Display popover open + 375px mobile zero overflow', async ({
    page,
  }) => {
    for (const theme of ['light', 'dark'] as const) {
      await page.addInitScript((t) => {
        localStorage.setItem('ravn_theme', t);
      }, theme);
      await page.setViewportSize({ width: 1280, height: 800 });
      await page.goto(serverUrl);
      await selectProjectViaDropdown(page, cleanProjectId);

      // Activate one display option so the badge renders too, and open the popover
      await page.getByTestId('display-popover-btn').click();
      await page.getByTestId('sort-key').selectOption('updated_at');
      await expect(page.getByTestId('display-active-indicator')).toBeVisible();

      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
        .analyze();
      const summary = results.violations
        .map((v) => `${v.id}: ${v.nodes.map((n) => `${JSON.stringify(n.target)} (${n.failureSummary ?? ''})`).join('; ')}`)
        .join('\n');
      expect(results.violations.map((v) => v.id), `${theme} WCAG violations:\n${summary}`).toEqual([]);

      // Reset sort so subsequent iteration starts clean
      await page.getByTestId('sort-clear').click();
    }

    // 375px mobile: zero horizontal overflow with mobile Filters & sort open
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
    await page.getByTestId('menu-toggle').click();
    await expect(page.getByTestId('mobile-menu-panel')).toBeVisible();
    await expect(page.getByTestId('sort-key')).toBeVisible();
    await expect(page.getByTestId('board-scope-select')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  });
});
