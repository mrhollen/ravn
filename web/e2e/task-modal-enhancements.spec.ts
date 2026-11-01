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

const SEED_ACTOR = { name: 'e2e-enhancements-seed', type: 'agent' };

test.describe('Task Modal Enhancements (Discard Confirmation & Double Column)', () => {
  let serverUrl: string;
  let projectId: string;
  let fixtureTask: any;

  // Server owned by the shared worker fixture. Seed this spec's own uniquely-named
  // project + fixture task into it; openBoard() selects it explicitly before asserting.
  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: 'Modal Enhancements E2E project',
      description: 'Project for discard confirm and 2-column modal tests',
      actor: SEED_ACTOR,
    });
    projectId = proj.id;

    fixtureTask = await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: 'Modal Enhancements Fixture',
      description: 'Testing two-column modal view on large screens.',
      priority: 2,
      actor: SEED_ACTOR,
    });
  });

  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    // Shared DB may auto-select a foreign project — pick this spec's own.
    await selectProjectViaDropdown(page, projectId);
  }

  // ---------------------------------------------------------------------------
  // Task 01M5K265HEVPWDG3FKS6N7V3FT: Discard Confirmation in Create Task Modal
  // ---------------------------------------------------------------------------

  test('Task 1 - AC1-AC4: Cancelling dirty Create Task modal uses inline confirmation without native dialog', async ({ page }) => {
    await openBoard(page);

    let nativeDialogFired = false;
    page.on('dialog', (d) => {
      nativeDialogFired = true;
      void d.dismiss();
    });

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task to discard');

    // Original buttons visible
    const cancelBtn = modal.getByTestId('new-task-cancel-btn');
    const createBtn = modal.getByTestId('new-task-create-btn');
    await expect(cancelBtn).toBeVisible();
    await expect(createBtn).toBeVisible();

    // User clicks Cancel
    await cancelBtn.click();

    // AC1: Native dialog is gone
    expect(nativeDialogFired).toBe(false, 'Native dialog must NOT be shown');

    // AC2: "Cancel" and "Create task" buttons are replaced with "Cancel" and "Discard" buttons
    await expect(createBtn).toBeHidden();
    const confirmCancelBtn = modal.getByTestId('new-task-confirm-cancel-btn');
    const confirmDiscardBtn = modal.getByTestId('new-task-confirm-discard-btn');
    await expect(confirmCancelBtn).toBeVisible();
    await expect(confirmDiscardBtn).toBeVisible();
    await expect(confirmDiscardBtn).toHaveText('Discard');

    // AC3: "Discard" button is red
    const discardColor = await confirmDiscardBtn.evaluate((el) => {
      const style = window.getComputedStyle(el);
      return {
        bg: style.backgroundColor,
      };
    });
    // bg-red-600 is oklch(0.577 0.245 27.325) or rgb(220, 38, 38)
    expect(discardColor.bg).toMatch(/oklch\(0\.577|rgb\(220,\s*38,\s*38\)/);

    // AC4: "Discard this task?" is in readable red letters to the left of the buttons
    const msg = modal.getByTestId('new-task-discard-confirm-msg');
    await expect(msg).toBeVisible();
    await expect(msg).toHaveText('Discard this task?');
    const msgColor = await msg.evaluate((el) => window.getComputedStyle(el).color);
    expect(msgColor).toMatch(/oklch\(0\.577|rgb\(220,\s*38,\s*38\)/);

    // Verify it is positioned to the left of the buttons
    const msgBox = await msg.boundingBox();
    const btnBox = await confirmCancelBtn.boundingBox();
    expect(msgBox).not.toBeNull();
    expect(btnBox).not.toBeNull();
    expect(msgBox!.x + msgBox!.width).toBeLessThanOrEqual(btnBox!.x);
  });

  test('Task 1 - AC5-AC7: Clicking cancel during confirmation restores original buttons and allows normal creation', async ({ page }) => {
    await openBoard(page);

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task created after cancel confirmation');

    // Click cancel to enter confirmation mode
    await modal.getByTestId('new-task-cancel-btn').click();
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeVisible();

    // AC5, AC6: Clicking Cancel during confirmation restores original buttons and removes confirmation message
    const confirmCancelBtn = modal.getByTestId('new-task-confirm-cancel-btn');
    await confirmCancelBtn.click();

    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeHidden();
    const cancelBtn = modal.getByTestId('new-task-cancel-btn');
    const createBtn = modal.getByTestId('new-task-create-btn');
    await expect(cancelBtn).toBeVisible();
    await expect(createBtn).toBeVisible();

    // AC7: Allows user to create the task like normal
    await createBtn.click();
    await expect(modal).toBeHidden();

    // Check card exists on board
    await expect(page.locator('article', { hasText: 'Task created after cancel confirmation' })).toBeVisible();
  });

  test('Task 1 - AC8: Clicking Discard during confirmation dismisses modal and clears all data', async ({ page }) => {
    await openBoard(page);

    await page.getByRole('button', { name: '+ New task' }).click();
    const modal = page.locator('#cb-new-task');
    await expect(modal).toBeVisible();

    const titleInput = modal.locator('input').first();
    await titleInput.fill('Task that will be discarded');

    // Click Cancel -> confirmation
    await modal.getByTestId('new-task-cancel-btn').click();
    const confirmDiscardBtn = modal.getByTestId('new-task-confirm-discard-btn');
    await expect(confirmDiscardBtn).toBeVisible();

    // Click Discard
    await confirmDiscardBtn.click();
    await expect(modal).toBeHidden();

    // Reopen modal and verify form is completely cleared
    await page.getByRole('button', { name: '+ New task' }).click();
    await expect(modal).toBeVisible();
    await expect(titleInput).toHaveValue('');
    await expect(modal.getByTestId('new-task-discard-confirm-msg')).toBeHidden();
    await expect(modal.getByTestId('new-task-create-btn')).toBeVisible();
  });

  // ---------------------------------------------------------------------------
  // Task 01M5K19665DKB9RJNS9KW52SZ5: Double Column View in Task Detail Modal
  // ---------------------------------------------------------------------------

  test('Task 2 - AC1: User screens >= 1080px show two columns in the task modal', async ({ page }) => {
    await openBoard(page);

    // Test at 1080px width
    await page.setViewportSize({ width: 1080, height: 800 });

    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();

    const modal = page.locator('#cb-detail-drawer');
    await expect(modal).toBeVisible();

    const leftCol = page.getByTestId('detail-left-column');
    const rightCol = page.getByTestId('detail-right-column');
    await expect(leftCol).toBeVisible();
    await expect(rightCol).toBeVisible();

    const leftBox = await leftCol.boundingBox();
    const rightBox = await rightCol.boundingBox();
    expect(leftBox).not.toBeNull();
    expect(rightBox).not.toBeNull();

    // Two columns side-by-side: left column's X is to the left of right column's X
    expect(leftBox!.x).toBeLessThan(rightBox!.x);
    expect(leftBox!.x + leftBox!.width).toBeLessThanOrEqual(rightBox!.x + 1);

    // Also test at 1280px width
    await page.setViewportSize({ width: 1280, height: 800 });
    const leftBox1280 = await leftCol.boundingBox();
    const rightBox1280 = await rightCol.boundingBox();
    expect(leftBox1280!.x).toBeLessThan(rightBox1280!.x);
    expect(leftBox1280!.x + leftBox1280!.width).toBeLessThanOrEqual(rightBox1280!.x + 1);
  });

  test('Task 2 - AC2: User screens < 1080px remain unchanged (single column view in modal)', async ({ page }) => {
    await openBoard(page);

    // Test just below 1080px: 1079px
    await page.setViewportSize({ width: 1079, height: 800 });

    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();

    const modal = page.locator('#cb-detail-drawer');
    await expect(modal).toBeVisible();

    const leftCol = page.getByTestId('detail-left-column');
    const rightCol = page.getByTestId('detail-right-column');
    await expect(leftCol).toBeVisible();
    await expect(rightCol).toBeVisible();

    const leftBox = await leftCol.boundingBox();
    const rightBox = await rightCol.boundingBox();
    expect(leftBox).not.toBeNull();
    expect(rightBox).not.toBeNull();

    // Single column: stacked vertically (left column above right column, aligned horizontally)
    expect(leftBox!.y + leftBox!.height).toBeLessThanOrEqual(rightBox!.y + 1);
    expect(Math.abs(leftBox!.x - rightBox!.x)).toBeLessThanOrEqual(5);

    // Also test at 768px (tablet)
    await page.setViewportSize({ width: 768, height: 800 });
    const leftBox768 = await leftCol.boundingBox();
    const rightBox768 = await rightCol.boundingBox();
    expect(leftBox768!.y + leftBox768!.height).toBeLessThanOrEqual(rightBox768!.y + 1);
  });

  test('Inline quick-edit: Priority & Due Date popovers and Cmd/Ctrl+Enter submission (01M5XTB45H0SXWRAXS3490M00W)', async ({ page }) => {
    await openBoard(page);
    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();

    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    // AC1: Inline priority popover
    const priTrigger = drawer.getByTestId('priority-popover-trigger');
    await expect(priTrigger).toBeVisible();
    await priTrigger.click();

    const priOption0 = drawer.getByTestId('priority-option-0');
    await expect(priOption0).toBeVisible();
    await priOption0.click();

    // Verify priority updated to P0
    await expect(priTrigger).toContainText('P0');

    // AC1: Inline due date popover
    const dueTrigger = drawer.getByTestId('due-popover-trigger');
    await expect(dueTrigger).toBeVisible();
    await dueTrigger.click();

    const dateInput = drawer.getByTestId('due-date-input');
    await expect(dateInput).toBeVisible();
    await dateInput.fill('2026-12-25');

    const saveDueBtn = drawer.getByTestId('save-due-date-btn');
    await saveDueBtn.click();

    await expect(dueTrigger).toContainText('Dec 25');

    // Clear due date
    await dueTrigger.click();
    const clearDueBtn = drawer.getByTestId('clear-due-date-btn');
    await clearDueBtn.click();
    await expect(dueTrigger).toContainText('+ Set due date');

    // AC2: Cmd/Ctrl+Enter comment submission
    const commentInput = drawer.getByTestId('comment-textarea');
    await commentInput.fill('Quick comment submission via hotkey');
    await commentInput.press('ControlOrMeta+Enter');

    // Verify comment posted
    await expect(drawer.getByText('Quick comment submission via hotkey')).toBeVisible();
  });

  test('Notes Markdown preview, Activity feed diffs, subtask quick-create, and global shortcuts (01M5XSNPVT0ETYQH9R8Q5HNQSE)', async ({ page }) => {
    await openBoard(page);

    // 1. AC4: Global shortcuts on the board
    // '/' focuses search
    await page.keyboard.press('/');
    const searchInput = page.getByTestId('board-search-input').first();
    await expect(searchInput).toBeFocused();

    // Blur search input and press '?' to open shortcut help modal
    await searchInput.blur();
    await page.keyboard.press('?');
    const helpModal = page.getByTestId('shortcut-help-modal');
    await expect(helpModal).toBeVisible();
    await expect(helpModal).toContainText('Keyboard Shortcuts');

    // Escape closes shortcut help modal
    await page.keyboard.press('Escape');
    await expect(helpModal).not.toBeVisible();

    // 2. Open task detail
    const card = page.locator('article', { hasText: 'Modal Enhancements Fixture' });
    await card.click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    // AC1: Notes Write / Preview toggle
    const notesTextarea = drawer.getByTestId('notes-textarea');
    await expect(notesTextarea).toBeVisible();
    await notesTextarea.fill('### Scratchpad Notes\n- [ ] Item 1\n- [x] Item 2');

    const previewBtn = drawer.getByTestId('notes-preview-btn');
    await previewBtn.click();
    const previewContainer = drawer.getByTestId('notes-preview');
    await expect(previewContainer).toBeVisible();
    await expect(previewContainer).toContainText('Scratchpad Notes');

    const writeBtn = drawer.getByTestId('notes-write-btn');
    await writeBtn.click();
    await expect(notesTextarea).toBeVisible();

    // Save notes
    const saveNotesBtn = drawer.getByRole('button', { name: 'Save notes' });
    await saveNotesBtn.click();
    await expect(saveNotesBtn).toBeDisabled();

    // AC2: Activity feed shows concise diffs
    const activityFeed = drawer.getByTestId('activity-feed');
    await expect(activityFeed).toBeVisible();
    await expect(drawer.getByTestId('event-detail-diff').first()).toBeVisible();

    // AC3: Subtask quick-create
    const addSubtaskBtn = drawer.getByTestId('add-subtask-btn');
    await expect(addSubtaskBtn).toBeVisible();
    await addSubtaskBtn.click();

    // New task modal opens with subtask banner
    const newTaskModal = page.locator('#cb-new-task');
    await expect(newTaskModal).toBeVisible();
    const subtaskBanner = newTaskModal.getByTestId('new-task-subtask-banner');
    await expect(subtaskBanner).toBeVisible();
    await expect(subtaskBanner).toContainText('Modal Enhancements Fixture');

    // Create the subtask
    const newTitle = newTaskModal.locator('input').first();
    await newTitle.fill('Automated Child Subtask');
    const createBtn = newTaskModal.getByTestId('new-task-create-btn');
    await createBtn.click();
    await expect(newTaskModal).not.toBeVisible();

    // Verify subtask relation appears in parent task's relations
    const relationsSection = drawer.getByTestId('detail-relations-section');
    await expect(relationsSection.getByRole('button', { name: 'Automated Child Subtask' })).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(drawer).not.toBeVisible();

    // 'n' shortcut opens New task modal
    await page.keyboard.press('n');
    await expect(newTaskModal).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(newTaskModal).not.toBeVisible();
  });
});
