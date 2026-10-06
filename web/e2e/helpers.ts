// Shared helpers for the e2e suite. Kept free of per-spec server boilerplate
// (each spec still owns its own ephemeral server) — just the small UI-driving
// utilities specs need after page.goto().
import { expect, type Page } from '@playwright/test';

/** Open the custom Projects dropdown (trigger button + Alpine-managed menu)
 *  and pick the project with `projectId`. Drives the real UI — trigger click,
 *  then the option item — so every spec that switches projects exercises the
 *  dropdown control itself. Auto-waits for app boot (the trigger enables when
 *  init() finishes) and for the project list to render. */
export async function selectProjectViaDropdown(page: Page, projectId: string): Promise<void> {
  const trigger = page.getByTestId('project-select');
  await expect(trigger).toBeEnabled();
  await trigger.click();
  const option = page.getByTestId('project-menu').locator(`[data-project-id="${projectId}"]`);
  await expect(option).toBeVisible();
  await option.click();
  await expect(page.getByTestId('project-menu')).toBeHidden();
}

/** Assert the dropdown trigger shows `name` as the currently selected project
 *  (the trigger label renders the full option label: name + task counts). */
export async function expectProjectSelected(page: Page, name: string): Promise<void> {
  const trigger = page.getByTestId('project-select');
  await expect(trigger).toBeEnabled();
  await expect(trigger).toContainText(name);
}

/** Open the dropdown and click the trailing "+ New project…" action item. */
export async function openNewProjectFromDropdown(page: Page): Promise<void> {
  const trigger = page.getByTestId('project-select');
  await expect(trigger).toBeEnabled();
  await trigger.click();
  const item = page.getByTestId('new-project-item');
  await expect(item).toBeVisible();
  await item.click();
  // The form bar below the header must be open with focus in the name field.
  const nameInput = page.locator('#cb-new-project-name-input');
  await expect(nameInput).toBeVisible();
  await expect(nameInput).toBeFocused();
}
