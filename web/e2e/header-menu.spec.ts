// E2E coverage for the consolidated header settings menu
// (task 01M47NPF5YK99MVGD8K4ZRS81R): theme selector, actor identity
// ("Who are you?"), auto-refresh and the auth-token entry live in ONE compact
// dropdown anchored at the right edge of the header. The SSE liveness indicator
// stays OUT of it (immediately left of the trigger on md+, click-to-reconnect
// preserved).
//   AC1 — all four controls reachable from the single menu; old inline widgets
//         REMOVED from the strip; compact unambiguous avatar-circle trigger
//         (deliberately NOT a gear glyph)
//   AC2 — live indicator positioned immediately left of the trigger + behavior
//   AC3 — light AND dark theme rendering; 375px trigger reachable with no
//         horizontal overflow (asserted: visible in the compact header row)
//   AC4 — behavior parity: theme persists via setTheme, polling flips, actor
//         saves on commit, token item opens the existing auth modal with a
//         set/none label; keyboard contract matching the Projects dropdown
// Self-contained: ephemeral ravn-server on a random high port + temp DB,
// serving the pre-built web/dist (mirrors project-dropdown.spec.ts).
import { test, expect } from './fixtures';
import path from 'path'; // path.join(testInfo.outputDir, …) for evidence screenshots

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`);
  return res.json();
}

function hasDarkClass(page: import('@playwright/test').Page) {
  return page.evaluate(() => document.documentElement.classList.contains('dark'));
}

async function lsTheme(page: import('@playwright/test').Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('ravn.theme') ?? localStorage.getItem('ravn_theme') ?? localStorage.getItem('crowban.theme'));
}

async function lsActor(page: import('@playwright/test').Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('ravn.actor') ?? localStorage.getItem('crowban.actor'));
}

test.describe('Consolidated header settings menu', () => {
  let serverUrl = ''; // worker-scoped shared server (assigned in beforeAll)
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    // One project so the app has a board on load (no new-project bar). Header-menu
    // assertions are independent of WHICH project is selected, so no explicit select.
    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Header Menu Project',
      description: 'Seeded for the header settings menu e2e spec',
      actor: { name: 'e2e-hm-seed', type: 'agent' },
    });
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: proj.id,
      title: 'Header Menu Seed Task',
      priority: 2,
      actor: { name: 'e2e-hm-seed', type: 'agent' },
    });
  });

  test.beforeEach(async () => {
    uncaughtConsoleErrors = [];
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Goto the board and wait for app boot (the dropdown trigger enables when
   *  init() completes — same convention as the Projects dropdown spec). */
  async function freshPage(page: import('@playwright/test').Page) {
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expect(page.getByTestId('project-select')).toBeEnabled();
  }

  test('AC1: avatar-circle trigger at the right edge; old inline widgets removed from the strip', async ({
    page,
  }, testInfo) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();

    // Compact and unambiguous: an avatar circle with the actor's initial
    // ("You" when unset) — deliberately NOT a gear/cog glyph (no svg inside).
    await expect(trigger.locator('svg')).toHaveCount(0);
    await expect(trigger).toContainText('You');
    await expect(trigger).toHaveAttribute('aria-haspopup', 'true');
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    // AC1 core: the old inline widgets are REMOVED from the strip — while the
    // menu is closed, none of them is visible anywhere on the page.
    for (const testid of ['theme-toggle', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await expect(page.getByTestId(testid)).not.toBeVisible();
    }

    // The liveness indicator has been removed from the header
    // (task 01M5K1HDAHCNRCJA322P1YF8HR — replaced by 30s disconnect notification)
    await expect(page.locator('[data-testid="live-indicator"]')).toHaveCount(0);

    // Initial is hidden on desktop view (task 01M5K1PD4C3NNM2VSEKEPT9KT8); name displays as normal
    await expect(trigger.getByTestId('header-menu-initial')).toBeHidden();
    await expect(trigger.getByTestId('header-menu-name')).toBeVisible();
    await expect(trigger.getByTestId('header-menu-name')).toContainText('You');

    const panelBox = await page.getByTestId('header-menu').boundingBox();
    expect(panelBox, 'panel is closed (x-show hidden) at this point').toBeNull();
  });

  test('AC2: desktop (1024px) — trigger is a standalone rounded button; liveness indicator is removed', async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await freshPage(page);

    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();

    // The live indicator is removed
    await expect(page.locator('[data-testid="live-indicator"]')).toHaveCount(0);

    // Initial is hidden on desktop view; name displays as normal
    await expect(trigger.getByTestId('header-menu-initial')).toBeHidden();
    await expect(trigger.getByTestId('header-menu-name')).toBeVisible();

    // Shape parity with the sibling top-bar controls (.field / .btn-primary /
    // .btn-ghost all use rounded-md): the trigger's COMPUTED border-radius must
    // match a visible sibling (the header CTA uses .btn-primary).
    const radii = await page.evaluate(() => {
      const r = (sel: string) => getComputedStyle(document.querySelector(sel)!).borderRadius;
      return {
        trigger: r('[data-testid="header-menu-trigger"]'),
        cta: r('header button.btn-primary'),
      };
    });
    expect(Number.parseFloat(radii.trigger), 'trigger has a non-zero radius (not square)').toBeGreaterThan(0);
    expect(radii.trigger, 'trigger radius matches the sibling .btn-* rounded-md treatment').toBe(radii.cta);

    // Evidence: close-up of the desktop trigger button.
    const tbox = (await trigger.boundingBox())!;
    const hbox = (await page.locator('header').first().boundingBox())!;
    const clipX = Math.max(0, tbox.x - 24);
    await page.screenshot({
      path: path.join(testInfo.outputDir, 'desktop-user-button-no-initial.png'),
      clip: { x: clipX, y: hbox.y + 2, width: tbox.x + tbox.width + 10 - clipX, height: hbox.height - 4 },
    });
  });

  test('AC1: menu opens and groups all four controls (light theme — screenshot evidence)', async ({
    page,
  }) => {
    await freshPage(page);

    const trigger = page.getByTestId('header-menu-trigger');
    await trigger.click();
    const panel = page.getByTestId('header-menu');
    await expect(panel).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');

    // Grouped sections with small headings — a unified menu, not a grab-bag.
    for (const name of ['Appearance', 'Identity', 'Data']) {
      await expect(panel.getByRole('heading', { name, level: 3 })).toBeVisible();
    }

    // All four controls reachable from the single menu…
    const group = page.locator('[data-testid="theme-toggle"]');
    await expect(group).toBeVisible();
    await expect(page.locator('[data-testid="theme-light"]')).toBeVisible();
    await expect(page.locator('[data-testid="theme-dark"]')).toBeVisible();
    // Default mode is System → its button is the only pressed one.
    await expect(page.locator('[data-testid="theme-system"]')).toHaveAttribute('aria-pressed', 'true');

    const actorInput = page.getByPlaceholder('you');
    await expect(actorInput).toBeVisible();
    await expect(actorInput).toHaveValue('');

    // Auto-refresh reflects the polling state (default on)…
    const toggle = page.getByTestId('auto-refresh-toggle');
    await expect(toggle).toBeChecked();
    const appState = await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      // pollTimer moved to the SSE module closure (Phase 2) — read it via the TEST-SUPPORT accessor
      return { polling: app.polling, pollTimer: app._sseModule().pollTimerId() };
    });
    expect(appState.polling).toBe(true);

    // …and the token entry opens from here, labelled by set/unset state.
    await expect(page.getByTestId('token-item')).toContainText('Token: none');

    // The menu is intentionally left open so the auto-captured test screenshot
    // is the light-theme evidence shot for this AC.
  });

  test('AC4: keyboard — Enter/Arrows/Home/End navigate, Escape returns focus to the trigger', async ({
    page,
  }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    /** What's focused inside the menu panel (null when focus is outside it). */
    const focusInfo = () =>
      page.evaluate(() => {
        const p = document.querySelector('[data-testid="header-menu"]');
        const el = document.activeElement;
        if (!p || !(el instanceof Element) || !p.contains(el)) return null;
        return el.getAttribute('data-testid') ?? el.tagName;
      });

    // Enter on the focused trigger opens it (native button semantics); focus
    // lands on the first control (the Light theme button).
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(panel).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');

    // Arrows walk the six controls in DOM order — section by section: theme
    // buttons, actor input (Identity), auto-refresh + token (Data)…
    for (const next of ['theme-dark', 'theme-system', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await page.keyboard.press('ArrowDown');
      await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe(next);
    }

    // …Home/End jump to the ends.
    await page.keyboard.press('Home');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');
    await page.keyboard.press('End');
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('token-item');

    // Escape closes and returns focus to the trigger.
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // ArrowDown from a closed trigger opens on the FIRST control…
    await page.keyboard.press('ArrowDown');
    await expect(panel).toBeVisible();
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('theme-light');
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // …ArrowUp opens on the LAST control (the token item), same as the project
    // dropdown's contract.
    await page.keyboard.press('ArrowUp');
    await expect(panel).toBeVisible();
    await expect.poll(() => focusInfo(), { timeout: 3000 }).toBe('token-item');
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    // Outside click closes WITHOUT restoring focus (matches the project menu):
    // a click on neutral header chrome dismisses the panel. The wordmark is now
    // a mascot <img> with only an sr-only h1, so target the visible logo image
    // (same neutral header chrome the old text wordmark occupied).
    await trigger.click();
    await expect(panel).toBeVisible();
    await page.locator('header img[alt="Rävn — MCP-first kanban · visibility UI"]').click();
    await expect(panel).toBeHidden();
  });

  test('AC4: theme choice made in the menu persists across reload (setTheme path unchanged)', async ({
    page,
  }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    await trigger.click();
    await expect(panel).toBeVisible();
    await page.locator('[data-testid="theme-dark"]').click();
    expect(await hasDarkClass(page)).toBe(true); // applied to <html> right away
    expect(await lsTheme(page)).toBe('dark'); // persisted via setTheme → localStorage

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await expect(trigger).toBeFocused();

    await page.reload();
    await expect(page.getByTestId('project-select')).toBeEnabled();
    expect(await lsTheme(page)).toBe('dark'); // survives the reload
    expect(await hasDarkClass(page)).toBe(true);
  });

  test('AC3: menu renders in the dark theme (screenshot evidence)', async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem('ravn.theme', 'dark');
      localStorage.setItem('ravn_theme', 'dark');
      localStorage.setItem('crowban.theme', 'dark');
    });
    await freshPage(page);
    expect(await hasDarkClass(page)).toBe(true); // stored explicit choice wins

    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    await trigger.click();
    await expect(panel).toBeVisible();

    // All four controls render on the dark elevated surface…
    for (const testid of ['theme-toggle', 'actor-name-input', 'auto-refresh-toggle', 'token-item']) {
      await expect(page.getByTestId(testid)).toBeVisible();
    }
    // …and the selected theme button shows its dark-mode pressed styling.
    await expect(page.locator('[data-testid="theme-dark"]')).toHaveAttribute('aria-pressed', 'true');

    // Intentionally left open: the auto-captured screenshot is the dark-theme
    // evidence shot for this AC.
  });

  test('AC4: auto-refresh toggle from the menu flips polling on/off', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    const toggle = page.getByTestId('auto-refresh-toggle');

    const appState = () =>
      page.evaluate(() => {
        const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
        // pollTimer moved to the SSE module closure (Phase 2) — read it via the TEST-SUPPORT accessor
      return { polling: app.polling, pollTimer: app._sseModule().pollTimerId() };
      });

    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(toggle).toBeChecked();
    expect((await appState()).polling).toBe(true); // booted with polling on

    // Uncheck from inside the menu…
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    const off = await appState();
    expect(off.polling).toBe(false, 'x-model flipped the state');
    expect(off.pollTimer).toBe(0, 'pollingChanged() stopped the timer');

    // …and re-check starts it again.
    await toggle.click();
    await expect(toggle).toBeChecked();
    const on = await appState();
    expect(on.polling).toBe(true);
    expect(on.pollTimer).not.toBe(0, 'pollingChanged() (re)started the timer');

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC4: actor name saves on commit and the trigger glyph follows', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');

    await trigger.click();
    await expect(panel).toBeVisible();
    const actorInput = page.getByPlaceholder('you');
    await actorInput.fill('Ada Lovelace');
    await actorInput.dispatchEvent('change'); // x-model write + saveActor()

    expect(await lsActor(page)).toBe('Ada Lovelace'); // persisted (same LS key as before)
    await expect(trigger).toContainText('A'); // avatar glyph follows the saved name

    // The panel's identity summary shows the full name too.
    await expect(panel.getByText('Ada Lovelace')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC4: token item opens the existing auth modal; its label tracks set/unset', async ({ page }) => {
    await freshPage(page);
    const trigger = page.getByTestId('header-menu-trigger');
    const panel = page.getByTestId('header-menu');
    const tokenItem = page.getByTestId('token-item');

    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(tokenItem).toContainText('Token: none');

    // Clicking the item closes the menu and opens the EXISTING auth modal…
    await tokenItem.click();
    await expect(page.locator('#cb-auth-modal')).toBeVisible();
    await expect(panel).toBeHidden();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');

    // …configured through the modal's own form (no contract change)…
    await page.locator('#cb-auth-modal input[type="password"]').fill('e2e-secret-token');
    await page.locator('#cb-auth-modal').getByRole('button', { name: 'Save token' }).click();
    await expect(page.locator('#cb-auth-modal')).toBeHidden();

    // …and the menu's label reflects the set state on re-open.
    await trigger.click();
    await expect(panel).toBeVisible();
    await expect(tokenItem).toContainText('Token: set');

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('AC3: 375px — trigger reachable in row 1, no overflow, menu fits (screenshot evidence)', async ({
    browser,
  }, testInfo) => {
    const context = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const page = await context.newPage();
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expect(page.getByTestId('project-select')).toBeEnabled();

    // Chosen mobile mapping: the trigger stays VISIBLE in the compact row-1
    // header (not hidden behind the hamburger) — reachable at 375px with no
    // horizontal overflow…
    const trigger = page.getByTestId('header-menu-trigger');
    await expect(trigger).toBeVisible();
    const tbox = (await trigger.boundingBox())!;
    expect(tbox.x).toBeGreaterThanOrEqual(0);
    expect(tbox.x + tbox.width).toBeLessThanOrEqual(375);
    // …meets the 44x44px tap-target minimum (mobile CSS min-size rule)…
    expect(tbox.height).toBeGreaterThanOrEqual(44);
    expect(tbox.width).toBeGreaterThanOrEqual(44);
    // …and adds no horizontal overflow to the document.
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

    const header = page.locator('header').first();
    const hbox = (await header.boundingBox())!;
    expect(hbox.height, 'row 1 + padding stays in the two-row budget').toBeLessThanOrEqual(96);
    expect(tbox.y >= hbox.y && tbox.y + tbox.height <= hbox.y + hbox.height).toBe(
      true,
      'trigger sits inside the collapsed header row',
    );

    // Evidence: a 375px shot of the header with the menu trigger.
    await header.screenshot({ path: path.join(testInfo.outputDir, 'header-row-375.png') });

    // The menu opens from row 1 and stays within the viewport; every control
    // is usable from inside it at this width.
    await trigger.click();
    const panel = page.getByTestId('header-menu');
    await expect(panel).toBeVisible();
    const mbox = (await panel.boundingBox())!;
    expect(mbox.x).toBeGreaterThanOrEqual(0);
    expect(mbox.x + mbox.width).toBeLessThanOrEqual(375, 'panel must not overflow the 375px viewport');
    // Scoped: at <md a second actor input exists in the hamburger's slide-down
    // panel (mobile-* testids) — strict mode requires one match per locator.
    await expect(panel.getByPlaceholder('you')).toBeVisible();
    await expect(panel.getByTestId('theme-toggle')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
    await context.close();
  });

  test('task 01M5K1HDAHCNRCJA322P1YF8HR: SSE disconnect notification triggers after delay, persists, dismisses, and clears on reconnect', async ({
    page,
  }, testInfo) => {
    await freshPage(page);

    // Ensure no live-indicator
    await expect(page.locator('[data-testid="live-indicator"]')).toHaveCount(0);

    // Fast disconnect test: the module's 30s toast delay is a constant now (Phase 2),
    // so compress it in-page — capture the scheduled callback and fire it immediately.
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      const m = app._sseModule();
      let captured: (() => void) | null = null;
      const orig = globalThis.setTimeout as any;
      (globalThis as any).setTimeout = function (fn: () => void, delay?: number) {
        if (delay === 30000) { captured = fn; return 1; }
        return orig.apply(this, arguments);
      };
      m.handleSseDisconnect();
      (globalThis as any).setTimeout = orig;
      captured!();
    });

    const toast = page.locator('[data-testid="sse-disconnect-toast"]');
    await expect(toast).toBeVisible({ timeout: 2000 });
    await expect(toast).toContainText('Disconnected from server');
    await expect(toast).toHaveClass(/border-red-200/);

    // Screenshot evidence of disconnect notification with red border
    await toast.screenshot({
      path: path.join(testInfo.outputDir, 'sse-disconnect-toast.png'),
    });

    // Dismiss manually (AC 3)
    const dismissBtn = toast.locator('button[aria-label="Dismiss notification"]');
    await dismissBtn.click();
    await expect(toast).toHaveCount(0);

    // Trigger again (AC 4: clear on restore)
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app._sseModule().showSseDisconnectNotification();
    });
    await expect(page.locator('[data-testid="sse-disconnect-toast"]')).toBeVisible();

    // Reconnect restores connection and clears notification
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.connectSse();
    });
    await expect(page.locator('[data-testid="sse-disconnect-toast"]')).toHaveCount(0, { timeout: 5000 });
  });

  test('task 01M5K1PD4C3NNM2VSEKEPT9KT8: Redundant initial removed on desktop, initial shown on mobile', async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1024, height: 768 });
    await freshPage(page);

    // Set actor name to 'DevTester'
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.actorName = 'DevTester';
      app.saveActor();
    });

    const trigger = page.getByTestId('header-menu-trigger');
    const initialSpan = trigger.getByTestId('header-menu-initial');
    const nameSpan = trigger.getByTestId('header-menu-name');

    // Desktop view: initial is hidden, full name is visible
    await expect(initialSpan).toBeHidden();
    await expect(nameSpan).toBeVisible();
    await expect(nameSpan).toContainText('DevTester');

    await trigger.screenshot({
      path: path.join(testInfo.outputDir, 'desktop-user-button-name-only.png'),
    });

    // Switch to mobile viewport (375x812)
    await page.setViewportSize({ width: 375, height: 812 });

    // Mobile view: initial is visible ("D"), full name is hidden
    await expect(initialSpan).toBeVisible();
    await expect(initialSpan).toHaveText('D');
    await expect(nameSpan).toBeHidden();

    await trigger.screenshot({
      path: path.join(testInfo.outputDir, 'mobile-user-button-initial-only.png'),
    });
  });
});
