// Card 3-dot move menu — floating popover behaviour (task 01M3MMVYMDH70JSR07GS1G19ST).
//   AC1 — clicking outside the trigger + dropdown dismisses the menu
//   AC2 — the "Move to" picker renders as a floating dropdown below the ⋮ trigger
//   AC3 — opening the menu does not shift the card or any sibling card's layout
import { test, expect } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`);
  return res.json();
}

/** Locate a card's ⋮ trigger and open its move menu. */
async function openCardMenu(page: import('@playwright/test').Page, title: string): Promise<void> {
  const card = page.locator('article', { hasText: title });
  await expect(card).toBeVisible();
  await card.getByRole('button', { name: 'Move task to another status' }).click();
  await expect(card.locator('[data-testid="card-move-menu"]')).toBeVisible();
}

test.describe('Card move menu (floating popover)', () => {
  let serverUrl: string;
  let projectId: string;

  // Server owned by the shared worker fixture. Seed this spec's own uniquely-named
  // project + cards into it; tests select it explicitly before asserting its board.
  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Card Move Menu Project',
      description: 'Seeded for the floating move-menu e2e spec',
      actor: { name: 'e2e-card-menu-seed', type: 'agent' },
    });
    projectId = proj.id;

    // Two todo cards so the column has a sibling BELOW the anchor card (AC3).
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'CMM top card',
      priority: 2,
      actor: { name: 'e2e-card-menu-seed', type: 'agent' },
    });
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'CMM bottom card',
      priority: 2,
      actor: { name: 'e2e-card-menu-seed', type: 'agent' },
    });
  });

  test('AC1: outside click dismisses the move menu; toggle and label clicks still behave', async ({ page }) => {
    await page.goto(serverUrl);
    // Shared DB may auto-select a foreign project — pick this spec's own.
    await selectProjectViaDropdown(page, projectId);
    const card = page.locator('article', { hasText: 'CMM top card' });
    await expect(card).toBeVisible();
    const menu = card.locator('[data-testid="card-move-menu"]');

    // Open, then dismiss by clicking the empty board background (outside both
    // the trigger and the dropdown).
    await openCardMenu(page, 'CMM top card');
    const mainBox = await page.locator('main').boundingBox();
    if (!mainBox) throw new Error('main bounding box missing');
    // Bottom-left corner of <main>: inside its padding gutter, below the columns.
    await page.mouse.click(mainBox.x + 8, mainBox.y + mainBox.height - 8);
    await expect(menu).toBeHidden();

    // Re-open; a second click on the ⋮ toggle itself must still close it.
    await openCardMenu(page, 'CMM top card');
    await card.getByRole('button', { name: 'Move task to another status' }).click();
    await expect(menu).toBeHidden();

    // Re-open; clicking the "Move to" label INSIDE the popover must not close
    // the menu nor open the card's detail drawer.
    await openCardMenu(page, 'CMM top card');
    await menu.locator('span', { hasText: 'Move to' }).click();
    await expect(menu).toBeVisible();
    await expect(page.locator('aside')).toBeHidden();
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
  });

  test('AC2: the move menu renders as a floating dropdown below the ⋮ trigger', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
    await openCardMenu(page, 'CMM top card');

    const card = page.locator('article', { hasText: 'CMM top card' });
    // The popover is still a DOM descendant of its card (existing specs scope
    // [data-testid=card-move-*] through the article locator).
    await expect(card.locator('[data-testid="card-move-menu"]')).toBeVisible();

    const menu = card.locator('[data-testid="card-move-menu"]');
    const trigger = card.locator('[data-testid="card-menu-toggle"]');
    const pos = await menu.evaluate((el) => getComputedStyle(el).position);
    expect(pos, 'dropdown must be taken out of flow').toBe('absolute');

    const tBox = (await trigger.boundingBox())!;
    const mBox = (await menu.boundingBox())!;
    // Anchored below the trigger…
    expect(mBox.y, 'dropdown top just under the trigger bottom').toBeGreaterThanOrEqual(tBox.y + tBox.height - 12);
    expect(mBox.y).toBeLessThanOrEqual(tBox.y + tBox.height + 24);
    // …and right-aligned to it.
    expect(Math.abs(mBox.x + mBox.width - (tBox.x + tBox.width)), 'right edges aligned').toBeLessThanOrEqual(10);
  });

  test('AC3: opening the move menu shifts neither the card nor its siblings', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);
    const top = page.locator('article', { hasText: 'CMM top card' });
    const bottom = page.locator('article', { hasText: 'CMM bottom card' });
    await expect(top).toBeVisible();
    await expect(bottom).toBeVisible();

    const beforeTop = (await top.boundingBox())!;
    const beforeBottom = (await bottom.boundingBox())!;

    await openCardMenu(page, 'CMM top card');

    // The floating popover must not change the anchor card's box AT ALL…
    const afterTop = (await top.boundingBox())!;
    expect(Math.abs(afterTop.height - beforeTop.height), 'anchor card height stable').toBeLessThanOrEqual(1);
    expect(Math.abs(afterTop.y - beforeTop.y), 'anchor card position stable').toBeLessThanOrEqual(1);

    // …nor push the card below it down.
    const afterBottom = (await bottom.boundingBox())!;
    expect(Math.abs(afterBottom.y - beforeBottom.y), 'sibling card not shifted').toBeLessThanOrEqual(1);
  });
});
