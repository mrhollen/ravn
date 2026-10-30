// E2E test suite for Task 01M3JK1BTTDB830AYBB7FFX8KC:
// Visual blocker indicators & parent epic progress on cards.
//
// Acceptance Criteria:
// - AC 1: Blocked task cards display a prominent blocker badge with tooltip/popover
//         showing which tasks are blocking it.
// - AC 2: Parent tasks with child relations show a subtask completion counter and
//         mini progress bar on their board card (e.g. "3/5 subtasks done").
// - AC 3: Opening a task highlights its relations and blockers clearly in the detail panel.

import { test, expect } from './fixtures';
import { selectProjectViaDropdown, expectProjectSelected } from './helpers';

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} → ${res.status}`);
  return res.json();
}

async function patchJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${url} → ${res.status}`);
  return res.json();
}

const ACTOR = { name: 'qa-tester', type: 'human' as const };

test.describe('Visual blocker indicators & parent epic progress on cards', () => {
  let serverUrl = ''; // worker-scoped shared server (assigned in beforeAll)

  let project: any;
  let blockerTask1: any;
  let blockerTask2: any;
  let blockedTask: any;
  let parentEpic: any;
  let subtask1: any;
  let subtask2: any;
  let subtask3: any;

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    // Create Project
    project = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Blockers and Progress Project',
      actor: ACTOR,
    });

    // Create Tasks for Blocker Indicator test
    blockerTask1 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Database Schema Migration',
      priority: 1,
      actor: ACTOR,
    })).task;

    blockerTask2 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'API Authentication Service',
      priority: 2,
      actor: ACTOR,
    })).task;

    blockedTask = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'User Profile Dashboard',
      priority: 3,
      actor: ACTOR,
    })).task;

    // Relate blockedTask to blockerTask1 and blockerTask2
    await postJson(`${serverUrl}/api/v1/tasks/${blockedTask.id}/relations`, {
      other_id: blockerTask1.id,
      kind: 'blocked-by',
      actor: ACTOR,
    });
    await postJson(`${serverUrl}/api/v1/tasks/${blockedTask.id}/relations`, {
      other_id: blockerTask2.id,
      kind: 'blocked-by',
      actor: ACTOR,
    });

    // Create Tasks for Parent Epic Progress test
    parentEpic = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Security Hardening Epic',
      priority: 1,
      actor: ACTOR,
    })).task;

    subtask1 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Audit Password Hashing',
      priority: 2,
      actor: ACTOR,
    })).task;

    subtask2 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Rate Limit Login Route',
      priority: 2,
      actor: ACTOR,
    })).task;

    subtask3 = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: project.id,
      title: 'Add CSRF Protection Headers',
      priority: 2,
      actor: ACTOR,
    })).task;

    // Relate subtasks to parentEpic
    await postJson(`${serverUrl}/api/v1/tasks/${subtask1.id}/relations`, {
      other_id: parentEpic.id,
      kind: 'child',
      actor: ACTOR,
    });
    await postJson(`${serverUrl}/api/v1/tasks/${subtask2.id}/relations`, {
      other_id: parentEpic.id,
      kind: 'child',
      actor: ACTOR,
    });
    await postJson(`${serverUrl}/api/v1/tasks/${subtask3.id}/relations`, {
      other_id: parentEpic.id,
      kind: 'child',
      actor: ACTOR,
    });

    // Mark subtask1 as Done so initial progress is 1/3
    await patchJson(`${serverUrl}/api/v1/tasks/${subtask1.id}`, {
      actor: ACTOR,
      version: subtask1.version,
      status: 'doing',
    });
    await patchJson(`${serverUrl}/api/v1/tasks/${subtask1.id}`, {
      actor: ACTOR,
      version: subtask1.version + 1,
      status: 'done',
    });
  });

  test('AC 1: Blocked task card displays prominent blocker badge with tooltip & interactive popover', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);
    await expectProjectSelected(page, 'Blockers and Progress Project');

    const blockedCard = page.locator('article', { hasText: 'User Profile Dashboard' });
    await expect(blockedCard).toBeVisible();

    // 1. Badge contains "blocked" exact text (for backward compatibility and clear semantics)
    await expect(blockedCard.getByText('blocked', { exact: true })).toBeVisible();

    // 2. Badge is prominent with blocker count (2)
    const badge = blockedCard.locator('[data-testid="card-blocker-badge"]');
    await expect(badge).toBeVisible();
    await expect(badge).toContainText('(2)');

    // 3. Tooltip text attribute outlines blocking tasks
    const tooltip = await badge.getAttribute('title');
    expect(tooltip).toContain('Database Schema Migration');
    expect(tooltip).toContain('API Authentication Service');

    // 4. Clicking the badge opens interactive popover
    await badge.click();
    const popover = blockedCard.locator('[data-testid="card-blocker-popover"]');
    await expect(popover).toBeVisible();
    await expect(popover.getByText('Blocked by', { exact: true })).toBeVisible();
    await expect(popover.getByText('Database Schema Migration')).toBeVisible();
    await expect(popover.getByText('API Authentication Service')).toBeVisible();

    // 5. Clicking a blocker in the popover opens its detail drawer
    await popover.getByText('Database Schema Migration').click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByText('Database Schema Migration')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
  });

  test('AC 2: Parent tasks with child relations show a subtask completion counter and mini progress bar on their board card', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);
    await expectProjectSelected(page, 'Blockers and Progress Project');

    const epicCard = page.locator('article', { hasText: 'Security Hardening Epic' });
    await expect(epicCard).toBeVisible();

    // 1. Subtask progress container exists
    const progressContainer = epicCard.locator('[data-testid="card-subtask-progress"]');
    await expect(progressContainer).toBeVisible();

    // 2. Completion counter shows "1/3 subtasks done"
    await expect(progressContainer).toContainText('1/3 subtasks done');
    await expect(progressContainer).toContainText('33%');

    // 3. Mini progress bar exists and has partial width
    const progressBar = epicCard.locator('[data-testid="card-subtask-bar"]');
    await expect(progressBar).toBeVisible();

    // Plain tasks without children do not display subtask progress container
    const plainCard = page.locator('article').filter({ has: page.getByRole('heading', { name: 'API Authentication Service', exact: true }) });
    await expect(plainCard.locator('[data-testid="card-subtask-progress"]')).not.toBeVisible();
  });

  test('AC 3: Opening a task highlights its relations and blockers clearly in the detail panel', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);
    await expectProjectSelected(page, 'Blockers and Progress Project');

    // --- Part A: Blocked task detail highlights blockers and relations ---
    const blockedCard = page.locator('article', { hasText: 'User Profile Dashboard' });
    await blockedCard.click();

    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    // Prominent blocker banner at top of detail
    const blockedBanner = drawer.locator('[data-testid="detail-blocked-banner"]');
    await expect(blockedBanner).toBeVisible();
    await expect(blockedBanner.getByText('Blocked by:', { exact: true })).toBeVisible();
    await expect(blockedBanner.getByText('Database Schema Migration')).toBeVisible();
    await expect(blockedBanner.getByText('API Authentication Service')).toBeVisible();

    // Relations section highlights blocker relation rows with distinct status pills
    const relationsSection = drawer.locator('[data-testid="detail-relations-section"]');
    await expect(relationsSection).toBeVisible();
    const relationsList = drawer.locator('[data-testid="detail-relations-list"]');
    await expect(relationsList).toBeVisible();
    await expect(relationsList.getByText('Blocked by').first()).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    // --- Part B: Parent epic task detail highlights subtask progress & child relations ---
    const epicCard = page.locator('article', { hasText: 'Security Hardening Epic' });
    await epicCard.click();
    await expect(drawer).toBeVisible();

    // Detail relations section shows subtask progress overview
    const detailSubtaskProgress = drawer.locator('[data-testid="detail-subtask-progress"]');
    await expect(detailSubtaskProgress).toBeVisible();
    await expect(detailSubtaskProgress).toContainText('1/3 done');

    // Child relations are clearly displayed
    const epicRelationsList = drawer.locator('[data-testid="detail-relations-list"]');
    await expect(epicRelationsList.getByText('Child').first()).toBeVisible();
    await expect(epicRelationsList.getByText('Audit Password Hashing')).toBeVisible();
    await expect(epicRelationsList.getByText('Done', { exact: true })).toBeVisible(); // subtask 1 status pill

    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
  });
});
