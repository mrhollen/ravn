// E2E coverage for the Projects DROPDOWN control (task 01M47NDGZR47NR1Z6SKQJ3BDP2):
//   AC1 — no standalone "New project" button in the header
//   AC2 — creation is initiated from inside the dropdown ("+ New project…")
//   AC3 — behavior parity: create auto-selects, cancel/dismiss keeps the draft,
//         no phantom projects
//   AC4 — keyboard operability (open/arrows/Enter/Escape/focus return) + 375px
// Self-contained: ephemeral ravn-server on a random high port + temp DB,
// serving the pre-built web/dist (mirrors m4.spec.ts / mobile.spec.ts).
import { test, expect } from './fixtures';
import path from 'path';
import { openNewProjectFromDropdown, selectProjectViaDropdown } from './helpers';

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`);
  return res.json();
}

async function listProjects(serverUrl: string): Promise<{ total: number; projects: { id: string; name: string }[] }> {
  const res = await fetch(`${serverUrl}/api/v1/projects`);
  if (!res.ok) throw new Error(`GET projects failed: ${res.status}`);
  return res.json();
}

test.describe('Projects dropdown (custom control)', () => {
  let serverUrl: string;
  let alphaId: string;
  let betaId: string;
  let uncaughtConsoleErrors: string[] = [];

  // Server is owned by the shared worker fixture; seed this spec's own uniquely
  // named projects into its DB.
  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    const alpha = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Alpha Project',
      description: 'Seeded with one task for the dropdown spec',
      actor: { name: 'e2e-dd-seed', type: 'agent' },
    });
    const beta = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Beta Project',
      actor: { name: 'e2e-dd-seed', type: 'agent' },
    });
    alphaId = alpha.id;
    betaId = beta.id;
    // One task in Alpha so its option label carries real counts.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: alphaId,
      title: 'Alpha Seed Task',
      priority: 2,
      actor: { name: 'e2e-dd-seed', type: 'agent' },
    });
  });

  test.beforeEach(async () => {
    uncaughtConsoleErrors = [];
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

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
    // App booted — the dropdown trigger enables when init() completes.
    await expect(page.getByTestId('project-select')).toBeEnabled();
  }

  test('AC1/AC2: no standalone header button; "+ New project…" lives inside the panel with a selected-check', async ({ page }) => {
    await freshPage(page);

    // AC1 — the old standalone ghost button is gone from the header (exact: the
    // "+ New project…" menu item has a different accessible name).
    await expect(page.getByRole('button', { name: 'New project', exact: true })).toHaveCount(0);

    // Select Alpha through the dropdown so the "selected" state is deterministic.
    await selectProjectViaDropdown(page, alphaId);
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toContainText('Alpha Project');

    // AC2 — open the panel: both seeded options (with count labels) and the
    // trailing "+ New project…" action are present.
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');

    const alphaOpt = menu.locator(`[data-project-id="${alphaId}"]`);
    const betaOpt = menu.locator(`[data-project-id="${betaId}"]`);
    await expect(alphaOpt).toContainText('Alpha Project  (1·0·0)');
    await expect(betaOpt).toContainText('Beta Project  (0·0·0)');

    // Exactly one option carries the selected check glyph — Alpha's. (The
    // unselected check stays in the DOM hidden by x-show.)
    await expect(alphaOpt.locator('svg')).toBeVisible();
    await expect(betaOpt.locator('svg')).toBeHidden();

    const newItem = page.getByTestId('new-project-item');
    await expect(newItem).toContainText('+ New project…');
  });

  test('AC2/AC3: creating from the dropdown auto-selects the new project (no phantoms)', async ({
    page,
  }, testInfo) => {
    await freshPage(page);

    const before = await listProjects(serverUrl);
    expect(before.projects.some((p) => p.name === 'Gamma Dropdown Project')).toBe(false);

    // Creation is initiated FROM WITHIN the open dropdown.
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill('Gamma Dropdown Project');
    await page.getByPlaceholder('MVP landing page work').fill('made via the dropdown');
    await page.getByRole('button', { name: 'Create project' }).click();

    // The form bar closes and the NEW project is auto-selected (parity with the
    // old flow) — trigger label flips to it, board renders its empty columns.
    await expect(page.locator('#cb-new-project-name-input')).not.toBeVisible();
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toContainText('Gamma Dropdown Project');
    await expect(page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) })).toBeVisible();
    // Mid-flow visual evidence: the just-created project auto-selected in the trigger.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'after-create-autoselect.png') });

    // No phantom creation: exactly one new project exists server-side.
    const after = await listProjects(serverUrl);
    expect(after.total).toBe(before.total + 1);
    const gamma = after.projects.find((p) => p.name === 'Gamma Dropdown Project');
    expect(gamma, 'created project must exist').toBeTruthy();
    expect(after.projects.filter((p) => p.name === 'Gamma Dropdown Project')).toHaveLength(1);

    // Switch back through the dropdown — Alpha's board (with its task) returns.
    await selectProjectViaDropdown(page, alphaId);
    await expect(trigger).toContainText('Alpha Project');
    await expect(page.locator('h3', { hasText: 'Alpha Seed Task' })).toBeVisible();
  });

  test('AC3: dismiss/cancel keeps the typed draft and creates nothing', async ({ page }) => {
    await freshPage(page);

    const before = await listProjects(serverUrl);

    // Start typing, then dismiss with Escape (top-level chain closes the bar
    // WITHOUT clearing newProject).
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill('Half Baked');
    await page.getByPlaceholder('MVP landing page work').fill('draft description');
    await page.keyboard.press('Escape');
    const nameInput = page.locator('#cb-new-project-name-input');
    await expect(nameInput).not.toBeVisible();

    // Re-open from the dropdown — the draft is still there (no loss, no reset).
    await openNewProjectFromDropdown(page);
    await expect(nameInput).toHaveValue('Half Baked');
    await expect(page.getByPlaceholder('MVP landing page work')).toHaveValue('draft description');

    // Cancel closes; nothing was ever created.
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(nameInput).not.toBeVisible();
    const after = await listProjects(serverUrl);
    expect(after.total).toBe(before.total);
  });

  test('AC4: keyboard — open, arrows, Enter, Escape, focus returns to the trigger', async ({ page }) => {
    /** What's focused inside the menu panel (null when focus is outside it). */
    const focusInfo = () =>
      page.evaluate(() => {
        const menu = document.querySelector('[data-testid="project-menu"]');
        const el = document.activeElement;
        if (!menu || !(el instanceof Element) || !menu.contains(el)) return null;
        return {
          testid: el.getAttribute('data-testid'),
          projectId: el.getAttribute('data-project-id'),
        };
      });

    /** Wait until the menu's focused item is STABLE (two samples ~120ms apart,
     *  ~7 frames even at a slow rate). Opening the menu schedules an app-side
     *  frame-polling focus helper that dies after its first successful focus;
     *  under CPU load its landing frame can post-date our explicit .focus() or
     *  key navigation, and while still armed it re-focuses its target — stealing
     *  focus from the next End/Escape (reproduced locally: focus on
     *  "+ New project…" yanked back to a project option ~3ms after End). A
     *  stable reading proves that poll has already fired or given up, so the
     *  keypresses below cannot lose the race. Coverage is unchanged — every
     *  focus position below is still asserted exactly. */
    const settleMenuFocus = (expectedTestid: string): Promise<void> =>
      expect(async () => {
        const first = await focusInfo().then((i) => i?.testid);
        await new Promise((r) => setTimeout(r, 120));
        const second = await focusInfo().then((i) => i?.testid);
        expect(first).toBe(expectedTestid);
        expect(second).toBe(expectedTestid);
      }).toPass({ timeout: 5000 });

    await freshPage(page);

    // Pick Beta explicitly so the "selected" initial focus is known regardless
    // of list order (earlier tests may have added projects to this server).
    await selectProjectViaDropdown(page, betaId);
    const trigger = page.getByTestId('project-select');
    const menu = page.getByTestId('project-menu');
    const betaOpt = menu.locator(`[data-project-id="${betaId}"]`);
    const newItem = page.getByTestId('new-project-item');

    // Enter on a focused button opens it (native button semantics), and focus
    // lands on the currently selected option.
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(menu).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect(betaOpt).toBeFocused();
    await settleMenuFocus('project-option');

    // Escape closes and returns focus to the trigger.
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
    await expect(trigger).toBeFocused();

    // ArrowDown from a closed trigger opens on the FIRST item (a project
    // option — options always precede the action item in DOM order). Focus
    // lands once Alpine's display flush catches up, so poll rather than sample
    // a single frame.
    await expect(trigger).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(menu).toBeVisible();
    await expect.poll(() => focusInfo().then((i) => i?.testid), { timeout: 3000 }).toBe('project-option');
    await settleMenuFocus('project-option');

    // End jumps to the LAST item: the "+ New project…" action. Enter activates
    // it — the form bar opens with focus already in the name field.
    await page.keyboard.press('End');
    await expect(newItem).toBeFocused();
    await settleMenuFocus('new-project-item');
    await page.keyboard.press('Enter');
    const nameInput = page.locator('#cb-new-project-name-input');
    await expect(nameInput).toBeVisible();
    await expect(nameInput).toBeFocused();
    await expect(menu).toBeHidden();

    // Esc dismisses the bar (top-level chain); nothing was created.
    await page.keyboard.press('Escape');
    await expect(nameInput).not.toBeVisible();

    // Escape with focus inside an OPEN menu closes it and returns focus to the
    // trigger (End first, so the Escape target is the action item). The focus
    // assertions double as waits: each keypress below must land on a panel
    // item, never on the still-transitioning trigger.
    await trigger.click();
    await expect(menu).toBeVisible();
    await betaOpt.focus();
    await expect(betaOpt).toBeFocused();
    // The click-open schedules the app's frame-polled menu focus; prove it has
    // landed (stable focus) before any keypress, or the poll can steal focus
    // from End/Escape under load.
    await settleMenuFocus('project-option');
    await page.keyboard.press('End');
    await expect(newItem).toBeFocused();
    await settleMenuFocus('new-project-item');
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
    await expect(trigger).toBeFocused();

    // ArrowUp from a closed trigger opens on the LAST item.
    await expect(trigger).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await expect(menu).toBeVisible();
    await expect(newItem).toBeFocused();
  });
});
