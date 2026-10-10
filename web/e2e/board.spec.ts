import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import http from 'http';
import { expectProjectSelected, selectProjectViaDropdown } from './helpers';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

async function getFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
    srv.on('error', reject);
  });
}

async function waitForServer(url: string, timeoutMs = 15000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const res = await fetch(`${url}/healthz`);
      if (res.ok) return;
    } catch {
      // Retry until server starts listening
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

test.describe('Web E2E test suite', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    // 1. Ensure static dist is built
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }

    // 2. Ensure server binary is built
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    // 3. Ephemeral temp DB directory (acceptance criterion 3)
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-'));
    const dbPath = path.join(tempDir, 'crowban.db');

    // 4. Free high-numbered port (never :8080) (acceptance criterion 3)
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);

    serverUrl = `http://127.0.0.1:${serverPort}`;

    // 5. Boot server with ephemeral DB and UI static dir
    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '', // Zero-config local mode
        OPENCODE_ALLOW_PRIVATE_IPS: 'true',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);
  });

  test.afterAll(async () => {
    // Clean shutdown of background server and removal of temp DB
    if (serverProcess && !serverProcess.killed) {
      serverProcess.kill('SIGTERM');
      await new Promise<void>((resolve) => {
        serverProcess.on('exit', () => resolve());
        setTimeout(() => {
          try {
            serverProcess.kill('SIGKILL');
          } catch {}
          resolve();
        }, 2000);
      });
    }

    if (tempDir && fs.existsSync(tempDir)) {
      fs.rmSync(tempDir, { recursive: true, force: true });
    }
    // Verify cleanup
    expect(fs.existsSync(tempDir)).toBe(false);
  });

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        if (msg.text().includes('status of 409')) return;
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    // Fail on any uncaught console error or unhandled page error (acceptance criterion 2)
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  test('seeds via API, renders board, creates task, moves task, and posts comment via UI', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed project and tasks via API (acceptance criterion 1)
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Browser E2E Project',
        description: 'Seeded via API for browser E2E test',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();
    expect(project.id).toBeTruthy();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Task To Move',
        description: 'Seeded task that will be moved via real UI flow',
        priority: 2,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const taskData = await taskRes.json();
    const initialTask = taskData.task;
    expect(initialTask?.id).toBeTruthy();

    // -------------------------------------------------------------------------
    // Step 2: Load real UI and assert board render
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');

    // Header: the custom Projects dropdown reflects the auto-selected seeded project
    await expectProjectSelected(page, 'Browser E2E Project');

    // Columns should be present
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const doingCol = page.locator('section', { has: page.getByRole('heading', { name: 'Doing' }) });
    const doneCol = page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });

    await expect(todoCol).toBeVisible();
    await expect(doingCol).toBeVisible();
    await expect(doneCol).toBeVisible();

    // Seeded task card must render in "To do" column
    const initialCard = todoCol.locator('h3', { hasText: 'Task To Move' });
    await expect(initialCard).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 3: Create task flow via real UI
    // -------------------------------------------------------------------------
    const newTaskBtn = page.getByRole('button', { name: '+ New task' });
    await expect(newTaskBtn).toBeEnabled();
    await newTaskBtn.click();

    // Modal opens
    const titleInput = page.getByPlaceholder('Fix login bug');
    await expect(titleInput).toBeVisible();
    await titleInput.fill('UI Created Task');

    const descInput = page.getByPlaceholder('Users cannot log in after the password reset flow…');
    await descInput.fill('Task created directly through the user interface.');

    const createBtn = page.getByRole('button', { name: 'Create task' });
    await createBtn.click();

    // Modal closes and newly created card renders in To do
    await expect(titleInput).not.toBeVisible();
    const createdCard = todoCol.locator('h3', { hasText: 'UI Created Task' });
    await expect(createdCard).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 4: Move task flow via real UI (acceptance criterion 1)
    // -------------------------------------------------------------------------
    // Click initial task card to open task detail drawer
    await initialCard.click();

    // Detail drawer opens with title
    const drawerTitle = page.locator('aside h2', { hasText: 'Task To Move' });
    await expect(drawerTitle).toBeVisible();

    // Move task to "Doing" via the status button group
    const statusGroup = page.getByRole('group', { name: 'Status' });
    const doingBtn = statusGroup.getByRole('button', { name: 'Doing' });
    await doingBtn.click();

    // Verify status button shows active
    await expect(doingBtn).toHaveAttribute('aria-pressed', 'true');

    // Close detail drawer
    const closeBtn = page.locator('aside button[aria-label="Close"]').first();
    await closeBtn.click();
    await expect(drawerTitle).not.toBeVisible();

    // Assert card is now moved to "In progress" column
    const movedCard = doingCol.locator('h3', { hasText: 'Task To Move' });
    await expect(movedCard).toBeVisible();
    await expect(todoCol.locator('h3', { hasText: 'Task To Move' })).not.toBeVisible();

    // -------------------------------------------------------------------------
    // Step 5: Post comment flow via real UI (acceptance criterion 1)
    // -------------------------------------------------------------------------
    // Reopen task drawer for the moved card
    await movedCard.click();
    await expect(drawerTitle).toBeVisible();

    // Post a comment
    const commentInput = page.getByPlaceholder('Markdown… posted as a typed comment');
    await expect(commentInput).toBeVisible();
    const testComment = 'Automated verification comment posted via browser E2E test';
    await commentInput.fill(testComment);

    const postCommentBtn = page.getByRole('button', { name: 'Post comment' });
    await postCommentBtn.click();

    // Verify comment appears in the drawer's comment thread
    const commentArticle = page.locator('article', { hasText: testComment });
    await expect(commentArticle).toBeVisible();

    // Close detail drawer
    await closeBtn.click();
    await expect(drawerTitle).not.toBeVisible();
  });

  test('relate & unrelate tasks, second-parent conflict, and claim/release controls', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed 3 tasks in the project via API
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Relations & Claims Test Project',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const makeTask = async (title: string) => {
      const res = await fetch(`${serverUrl}/api/v1/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          project_id: project.id,
          title,
          priority: 2,
          actor: { name: 'e2e-seed', type: 'agent' },
        }),
      });
      expect(res.ok).toBe(true);
      const data = await res.json();
      return data.task;
    };

    const taskChild = await makeTask('Child Task');
    const taskParent1 = await makeTask('Parent 1 Task');
    const taskParent2 = await makeTask('Parent 2 Task');

    // -------------------------------------------------------------------------
    // Step 2: Open UI and select new project
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');

    await selectProjectViaDropdown(page, project.id);

    // Set actor name (lives in the consolidated header settings menu now)
    await page.getByTestId('header-menu-trigger').click();
    const actorInput = page.getByPlaceholder('you');
    await expect(actorInput).toBeVisible();
    await actorInput.fill('Human Tester');
    await actorInput.dispatchEvent('change');
    await page.keyboard.press('Escape'); // close the menu (focus returns to the trigger)

    // -------------------------------------------------------------------------
    // Step 3: Test Claim, Heartbeat, and Release controls (AC 3)
    // -------------------------------------------------------------------------
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const childCard = todoCol.locator('h3', { hasText: 'Child Task' });
    await expect(childCard).toBeVisible();
    await childCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Child Task' })).toBeVisible();

    // Check initial unclaimed state
    await expect(drawer.getByText('Unclaimed')).toBeVisible();
    const claimBtn = drawer.getByRole('button', { name: 'Claim', exact: true });
    const releaseBtn = drawer.getByRole('button', { name: 'Release', exact: true });
    const heartbeatBtn = drawer.getByRole('button', { name: 'Heartbeat', exact: true });

    await expect(claimBtn).toBeEnabled();
    await expect(releaseBtn).toBeDisabled();
    await expect(heartbeatBtn).toBeDisabled();
    await expect(releaseBtn).toHaveClass(/btn-secondary/);
    await expect(heartbeatBtn).toHaveClass(/btn-secondary/);

    // Click Claim
    await claimBtn.click();

    // Live update check: should now be Claimed by Human Tester
    await expect(drawer.getByText('Claimed', { exact: true })).toBeVisible();
    await expect(drawer.locator('b', { hasText: 'Human Tester' })).toBeVisible();
    await expect(drawer.getByText('Expires:')).toBeVisible();
    await expect(releaseBtn).toBeEnabled();
    await expect(heartbeatBtn).toBeEnabled();

    // Click Heartbeat
    await heartbeatBtn.click();
    await expect(page.getByText('Heartbeat recorded — lease extended')).toBeVisible();

    // Click Release
    await releaseBtn.click();
    await expect(page.getByText('Released claim on task')).toBeVisible();
    await expect(drawer.getByText('Unclaimed')).toBeVisible();
    await expect(releaseBtn).toBeDisabled();
    await expect(heartbeatBtn).toBeDisabled();

    // -------------------------------------------------------------------------
    // Step 4: Add relation and verify both views (AC 1)
    // -------------------------------------------------------------------------
    // Set Parent 1 as parent of Child Task
    const kindSelect = drawer.locator('select[name="kind"]');
    await kindSelect.selectOption('parent');

    const targetInput = drawer.locator('input[name="other_id"]');
    await targetInput.fill(taskParent1.id);

    const addRelBtn = drawer.getByRole('button', { name: 'Add relation' });
    await addRelBtn.click();

    await expect(page.getByText('Relation added')).toBeVisible();

    // Verify Child Task shows "[Parent] Parent 1 Task"
    const relItem = drawer.locator('li', { hasText: 'Parent 1 Task' });
    await expect(relItem).toBeVisible();
    await expect(relItem.getByText('Parent', { exact: true })).toBeVisible();

    // Close drawer and open Parent 1 Task drawer
    const closeBtn = drawer.locator('button[aria-label="Close"]').first();
    await closeBtn.click();

    const parent1Card = todoCol.locator('h3', { hasText: 'Parent 1 Task' });
    await parent1Card.click();
    await expect(drawer.getByRole('heading', { name: 'Parent 1 Task' })).toBeVisible();

    // Verify Parent 1 Task shows "[Child] Child Task"
    const relItemOnParent = drawer.locator('li', { hasText: 'Child Task' });
    await expect(relItemOnParent).toBeVisible();
    await expect(relItemOnParent.getByText('Child', { exact: true })).toBeVisible();

    // Close drawer and return to Child Task
    await closeBtn.click();
    await childCard.click();
    await expect(drawer.getByRole('heading', { name: 'Child Task' })).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 5: Second-parent conflict (AC 2)
    // -------------------------------------------------------------------------
    // Attempt to set Parent 2 as a second parent for Child Task
    await kindSelect.selectOption('parent');
    await targetInput.fill(taskParent2.id);
    await addRelBtn.click();

    // 409 parent-conflict message must be surfaced verbatim
    const conflictToast = page.locator('div[role="status"], .fixed', {
      hasText: `task ${taskChild.id} already has a parent`,
    });
    await expect(conflictToast).toBeVisible();

    // Ensure state is NOT corrupted: Child Task still has only Parent 1
    await expect(drawer.locator('li', { hasText: 'Parent 2 Task' })).not.toBeVisible();
    await expect(drawer.locator('li', { hasText: 'Parent 1 Task' })).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 6: Remove relation and verify revert (AC 1)
    // -------------------------------------------------------------------------
    const removeBtn = drawer.locator('button[aria-label="Remove relation"]');
    await removeBtn.click();
    await expect(page.getByText('Relation removed')).toBeVisible();
    await expect(drawer.getByText('No relations')).toBeVisible();

    // Reopen Parent 1 Task and verify it is reverted there too
    await closeBtn.click();
    await parent1Card.click();
    await expect(drawer.getByRole('heading', { name: 'Parent 1 Task' })).toBeVisible();
    await expect(drawer.getByText('No relations')).toBeVisible();
    await closeBtn.click();
  });

  test('project settings modal, durable assignee picker with distinct chip, and board filters', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Seed project with tasks
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Settings & Filter Project',
        description: 'Testing project settings and filters',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    // Task 1: P0, assigned to alice, overdue
    const t1Res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Alpha Critical Task',
        priority: 0,
        assignee: { name: 'alice', type: 'human' },
        due_at: '2020-01-01T00:00:00Z',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(t1Res.ok).toBe(true);

    // Task 2: P3, assigned to bob, due future
    const t2Res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Beta Normal Task',
        priority: 3,
        assignee: { name: 'bob', type: 'agent' },
        due_at: '2099-01-01T00:00:00Z',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(t2Res.ok).toBe(true);

    // Task 3: P4, unassigned
    const t3Res = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Gamma Low Task',
        priority: 4,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(t3Res.ok).toBe(true);

    // Load UI and select the project
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();

    // -------------------------------------------------------------------------
    // AC 1: Project settings modal / edit — opened from the gear icon attached
    // to the Projects dropdown (the old spelled-out "Settings" button is gone)
    // -------------------------------------------------------------------------
    const settingsBtn = page.getByTestId('project-settings-btn');
    await expect(settingsBtn).toBeVisible();
    await expect(settingsBtn).toBeEnabled();
    await settingsBtn.click();

    // Check modal opens with existing name
    const nameInput = page.getByPlaceholder('Project name');
    await expect(nameInput).toBeVisible();
    await expect(nameInput).toHaveValue('Settings & Filter Project');

    // Update name and description
    await nameInput.fill('Renamed Project E2E');
    await page.getByPlaceholder('Project description…').fill('Updated project description');
    await page.getByRole('button', { name: 'Save changes' }).click();

    // Toast and selector update (the dropdown trigger now shows the new name)
    await expect(page.getByText('Project updated')).toBeVisible();
    await expect(page.getByTestId('project-select')).toContainText('Renamed Project E2E');

    // Reload page and verify persisted
    await page.reload();
    await expect(page.getByTestId('project-select')).toContainText('Renamed Project E2E');

    // -------------------------------------------------------------------------
    // AC 2: Assignee selector & durable owner chip distinct from claimer
    // -------------------------------------------------------------------------
    // Verify Task 1 has distinct amber Owner chip on card
    const card1 = page.locator('article', { hasText: 'Alpha Critical Task' });
    const ownerChip1 = card1.locator('span.bg-amber-50');
    await expect(ownerChip1).toBeVisible();
    await expect(ownerChip1).toContainText('alice');

    // Open detail drawer for Task 3 (unassigned)
    const card3 = page.locator('article', { hasText: 'Gamma Low Task' });
    await card3.click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Gamma Low Task' })).toBeVisible();
    await expect(drawer.getByText('Unassigned')).toBeVisible();

    // Set assignee via inline drawer picker
    const assigneeInput = drawer.getByPlaceholder('Assignee name (e.g. alice, agent-1)');
    await assigneeInput.fill('charlie-human');
    await drawer.getByRole('button', { name: 'Assign' }).click();
    await expect(page.getByText('Assigned to charlie-human')).toBeVisible();
    await expect(drawer.locator('span', { hasText: 'Owner' }).first()).toBeVisible();
    await expect(drawer.getByText('charlie-human').first()).toBeVisible();

    // Now claim the task to verify ephemeral claim lease is distinct from durable owner
    await drawer.getByRole('button', { name: 'Claim', exact: true }).click();
    await expect(page.getByText('Claimed task as')).toBeVisible();
    await expect(drawer.getByText('Claimed', { exact: true })).toBeVisible();
    await expect(drawer.getByText('Owner', { exact: true }).first()).toBeVisible();

    // Close drawer and verify card shows both owner and claimer distinctly
    const closeBtn = drawer.locator('button[aria-label="Close"]').first();
    await closeBtn.click();
    const card3Updated = page.locator('article', { hasText: 'Gamma Low Task' });
    await expect(card3Updated.locator('span.bg-amber-50')).toContainText('charlie-human');

    // Reopen drawer, clear assignee
    await card3Updated.click();
    await drawer.getByRole('button', { name: 'Clear' }).click();
    await expect(page.getByText('Assignee cleared')).toBeVisible();
    await expect(drawer.getByText('Unassigned')).toBeVisible();
    await closeBtn.click();
    await expect(card3Updated.locator('span.bg-amber-50')).not.toBeVisible();

    // -------------------------------------------------------------------------
    // AC 3: Board filters live filtering
    // -------------------------------------------------------------------------
    // Search filter
    const searchInput = page.getByPlaceholder('Filter title or ID…');
    await searchInput.fill('Alpha');
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).not.toBeVisible();
    await expect(page.locator('h3', { hasText: 'Gamma Low Task' })).not.toBeVisible();

    // Clear search using reset button
    const resetBtn = page.getByRole('button', { name: 'Reset filters' });
    await resetBtn.click();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).toBeVisible();

    // Priority filter (P0)
    const prioritySelect = page.locator('select[x-model="filterPriority"]');
    await prioritySelect.selectOption('0');
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).not.toBeVisible();
    await expect(page.locator('h3', { hasText: 'Gamma Low Task' })).not.toBeVisible();

    // Reset priority
    await prioritySelect.selectOption('');
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).toBeVisible();

    // Assignee filter: 'bob'
    const assigneeFilter = page.locator('select[x-model="filterAssignee"]');
    await assigneeFilter.selectOption('bob');
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).not.toBeVisible();
    await expect(page.locator('h3', { hasText: 'Gamma Low Task' })).not.toBeVisible();

    // Assignee filter: 'unassigned'
    await assigneeFilter.selectOption('unassigned');
    await expect(page.locator('h3', { hasText: 'Gamma Low Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).not.toBeVisible();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).not.toBeVisible();

    // Due filter: 'overdue'
    await assigneeFilter.selectOption('');
    const dueSelect = page.locator('select[x-model="filterDue"]');
    await dueSelect.selectOption('overdue');
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).not.toBeVisible();

    // Reset all filters
    await resetBtn.click();
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Beta Normal Task' })).toBeVisible();
    await expect(page.locator('h3', { hasText: 'Gamma Low Task' })).toBeVisible();

    // -------------------------------------------------------------------------
    // Create task with assignee & Edit task assignee
    // -------------------------------------------------------------------------
    const newTaskBtn = page.getByRole('button', { name: '+ New task' });
    await newTaskBtn.click();
    await page.getByPlaceholder('Fix login bug').fill('Delta Assigned Task');
    await page.locator('input[x-model="newTask.assigneeName"]').fill('diana-agent');
    await page.locator('select[x-model="newTask.assigneeType"]').selectOption('agent');
    await page.getByRole('button', { name: 'Create task' }).click();

    const deltaCard = page.locator('article', { hasText: 'Delta Assigned Task' });
    await expect(deltaCard).toBeVisible();
    await expect(deltaCard.locator('span.bg-amber-50')).toContainText('diana-agent');

    // Edit task assignee via drawer edit mode
    await deltaCard.click();
    await drawer.getByRole('button', { name: 'Edit' }).first().click();
    await page.locator('input[x-model="editDraft.assigneeName"]').fill('elena-human');
    await page.locator('select[x-model="editDraft.assigneeType"]').selectOption('human');
    await drawer.getByRole('button', { name: 'Save', exact: true }).click();

    await expect(drawer.getByText('elena-human').first()).toBeVisible();
    await closeBtn.click();
    await expect(deltaCard.locator('span.bg-amber-50')).toContainText('elena-human');
  });

  test('acceptance criteria management: add, inline edit, delete, and toggle', async ({ page }) => {
    // -------------------------------------------------------------------------
    // Seed project and task with 1 initial AC
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'AC Management Project',
        description: 'Testing AC add, edit, delete, and toggle',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Task with ACs',
        priority: 2,
        acceptance_criteria: ['Initial criterion 1'],
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    // Load UI and open the task detail drawer
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const taskCard = page.locator('article', { hasText: 'Task with ACs' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Task with ACs' })).toBeVisible();
    await expect(drawer.getByText('Initial criterion 1')).toBeVisible();

    // -------------------------------------------------------------------------
    // AC 1: Add new AC row to existing task (appears with next seq)
    // -------------------------------------------------------------------------
    const addInput = drawer.getByPlaceholder('Add acceptance criterion…');
    await addInput.fill('Second criterion added via UI');
    await drawer.getByRole('button', { name: '+ Add criterion' }).click();

    // Verify it appears in the UI
    await expect(drawer.getByText('Second criterion added via UI')).toBeVisible();
    await expect(addInput).toHaveValue('');

    // Verify in backend get_task
    const getRes1 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    expect(getRes1.ok).toBe(true);
    const taskDetail1 = await getRes1.json();
    expect(taskDetail1.acceptance_criteria).toHaveLength(2);
    expect(taskDetail1.acceptance_criteria[1].seq).toBe(2);
    expect(taskDetail1.acceptance_criteria[1].text).toBe('Second criterion added via UI');
    expect(taskDetail1.acceptance_criteria[1].done).toBe(false);

    // -------------------------------------------------------------------------
    // AC 2: Edit AC row text
    // -------------------------------------------------------------------------
    const ac2Item = drawer.locator('li', { hasText: 'Second criterion added via UI' });
    await ac2Item.getByRole('button', { name: 'Edit' }).click();

    const editTextarea = ac2Item.locator('textarea');
    await expect(editTextarea).toBeVisible();
    await expect(editTextarea).toHaveValue('Second criterion added via UI');

    await editTextarea.fill('Second criterion edited and reworded');
    await ac2Item.getByRole('button', { name: 'Save', exact: true }).click();

    // Verify UI reflects edited text
    await expect(drawer.getByText('Second criterion edited and reworded')).toBeVisible();
    await expect(drawer.getByText('Second criterion added via UI')).not.toBeVisible();

    // Verify backend get_task persists edited text
    const getRes2 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    const taskDetail2 = await getRes2.json();
    expect(taskDetail2.acceptance_criteria[1].text).toBe('Second criterion edited and reworded');

    // -------------------------------------------------------------------------
    // AC 3: Toggling done still works for new/edited rows with completed_by/at tracked
    // -------------------------------------------------------------------------
    const ac2UpdatedItem = drawer.locator('li', { hasText: 'Second criterion edited and reworded' });
    const ac2Checkbox = ac2UpdatedItem.locator('input[type="checkbox"]');
    await expect(ac2Checkbox).not.toBeChecked();

    await ac2Checkbox.click();
    await expect(ac2Checkbox).toBeChecked();

    // Verify backend get_task has done=true and completed_by tracked
    const getRes3 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    const taskDetail3 = await getRes3.json();
    expect(taskDetail3.acceptance_criteria[1].done).toBe(true);
    expect(taskDetail3.acceptance_criteria[1].completed_by).toBeDefined();
    expect(taskDetail3.acceptance_criteria[1].completed_at).toBeDefined();

    // Uncheck and verify cleared
    await ac2Checkbox.click();
    await expect(ac2Checkbox).not.toBeChecked();
    const getRes4 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    const taskDetail4 = await getRes4.json();
    expect(taskDetail4.acceptance_criteria[1].done).toBe(false);
    expect(taskDetail4.acceptance_criteria[1].completed_by).toBeUndefined();

    // -------------------------------------------------------------------------
    // AC 2: Delete AC row
    // -------------------------------------------------------------------------
    // Add a 3rd AC to delete
    await addInput.fill('Third criterion to delete');
    await drawer.getByRole('button', { name: '+ Add criterion' }).click();
    await expect(drawer.getByText('Third criterion to delete')).toBeVisible();

    const ac3Item = drawer.locator('li', { hasText: 'Third criterion to delete' });
    await ac3Item.locator('button[title="Delete criterion"]').click();

    await expect(page.getByText('Acceptance criterion removed')).toBeVisible();
    await expect(drawer.getByText('Third criterion to delete')).not.toBeVisible();

    // Verify backend get_task no longer has criterion 3
    const getRes5 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    const taskDetail5 = await getRes5.json();
    expect(taskDetail5.acceptance_criteria).toHaveLength(2);
    expect(taskDetail5.acceptance_criteria.map((x: any) => x.seq)).toEqual([1, 2]);

    // Close drawer
    await drawer.locator('button[aria-label="Close"]').first().click();
  });

  test('full comment thread loading, pagination, chronological ordering, and live poll updates', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Seed project and task with 55 comments via API
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Thread Pagination Project',
        description: 'Testing comment thread loading and live updates',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Task with 55 Comments',
        priority: 1,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    // Seed 55 comments
    for (let i = 1; i <= 55; i++) {
      const pad = String(i).padStart(2, '0');
      const isAgent = i % 2 === 0;
      const res = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/comments`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: {
            name: isAgent ? `agent-${pad}` : `human-${pad}`,
            type: isAgent ? 'agent' : 'human',
          },
          kind: i === 1 ? 'decision' : i % 2 === 0 ? 'progress' : 'note',
          body: `Comment body ${pad} of 55`,
        }),
      });
      expect(res.ok).toBe(true);
    }

    // -------------------------------------------------------------------------
    // AC 1 & 2: Open drawer - verify default tail (50), count, order, badges
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const taskCard = page.locator('article', { hasText: 'Task with 55 Comments' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Task with 55 Comments' })).toBeVisible();

    // Verify counter shows "(50 of 55)"
    await expect(drawer.getByText('(50 of 55)')).toBeVisible();

    // Verify "Load older comments (5 remaining)" button is exposed
    const loadOlderBtn = drawer.getByRole('button', {
      name: 'Load older comments (5 remaining)',
    });
    await expect(loadOlderBtn).toBeVisible();

    // Verify 50 comments rendered
    const articles = drawer.locator('article');
    await expect(articles).toHaveCount(50);

    // Oldest of the 50 loaded is comment 06, newest is comment 55
    await expect(articles.first()).toContainText('Comment body 06 of 55');
    await expect(articles.first()).toContainText('agent-06');
    await expect(articles.first().locator('span', { hasText: 'agent' })).toBeVisible();
    await expect(articles.first().locator('span', { hasText: 'progress' })).toBeVisible();

    await expect(articles.last()).toContainText('Comment body 55 of 55');
    await expect(articles.last()).toContainText('human-55');
    await expect(articles.last().locator('span', { hasText: 'note' })).toBeVisible();

    // Comment 01 is not yet loaded
    await expect(drawer.locator('article', { hasText: 'Comment body 01 of 55' })).not.toBeVisible();

    // -------------------------------------------------------------------------
    // AC 1: Click "Load older comments" -> loads remaining 5 older comments
    // -------------------------------------------------------------------------
    await loadOlderBtn.click();

    // Button disappears once all comments are loaded
    await expect(loadOlderBtn).not.toBeVisible();
    await expect(drawer.getByText('(55)')).toBeVisible();
    await expect(articles).toHaveCount(55);

    // Now comment 01 is at the top (oldest), chronological order maintained
    await expect(articles.first()).toContainText('Comment body 01 of 55');
    await expect(articles.first()).toContainText('decision');
    await expect(articles.last()).toContainText('Comment body 55 of 55');

    // -------------------------------------------------------------------------
    // Post new comment via UI -> renders immediately at bottom
    // -------------------------------------------------------------------------
    const commentInput = drawer.getByPlaceholder('Markdown… posted as a typed comment');
    await commentInput.fill('Comment body 56 posted from UI');
    await drawer.getByRole('button', { name: 'Post comment' }).click();

    await expect(articles).toHaveCount(56);
    await expect(articles.last()).toContainText('Comment body 56 posted from UI');

    // -------------------------------------------------------------------------
    // AC 3: Live updates via background poll without full page reload
    // -------------------------------------------------------------------------
    // Add comment 57 externally via API
    const extCommentRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/comments`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'external-bot', type: 'agent' },
        kind: 'handoff',
        body: 'Comment body 57 posted externally via API',
      }),
    });
    expect(extCommentRes.ok).toBe(true);

    // Trigger pollTick in the running Alpine app
    await page.evaluate(async () => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      await app.pollTick();
    });

    // Verify comment 57 appears at the bottom without full page reload
    await expect(articles).toHaveCount(57);
    await expect(articles.last()).toContainText('Comment body 57 posted externally via API');
    await expect(articles.last()).toContainText('external-bot');
    await expect(articles.last().locator('span', { hasText: 'handoff' })).toBeVisible();

    // Verify oldest comments (e.g. comment 01) are still intact at the top
    await expect(articles.first()).toContainText('Comment body 01 of 55');

    // Close drawer
    await drawer.locator('button[aria-label="Close"]').first().click();
  });

  test('realtime SSE event stream updates board, detail drawer live, and handles reconnect backoff', async ({
    page,
  }) => {
    // 1. Create a project via API
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'admin', type: 'human' },
        name: 'Realtime SSE Project',
      }),
    });
    const proj = await projRes.json();

    await page.goto(`${serverUrl}`);
    await selectProjectViaDropdown(page, proj.id);

    // Verify live status indicator is connected
    const liveIndicator = page.locator('[data-testid="live-indicator"]');
    await expect(liveIndicator).toBeVisible();
    await expect(liveIndicator).toContainText('Live');
    await expect(liveIndicator).toHaveAttribute('title', 'Live');

    // -------------------------------------------------------------------------
    // AC 1: Realtime card / column updates without waiting for poll interval
    // -------------------------------------------------------------------------
    // Create task externally via API
    const createRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'agent-worker', type: 'agent' },
        project_id: proj.id,
        title: 'Task Created via API for SSE',
        priority: 1,
      }),
    });
    expect(createRes.ok).toBe(true);
    const { task } = await createRes.json();

    // Without polling tick or page reload, card appears in To Do column
    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    const doingCol = page.locator('section', { has: page.getByRole('heading', { name: 'Doing' }) });
    const doneCol = page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });

    const taskCard = todoCol.locator('h3', { hasText: 'Task Created via API for SSE' });
    await expect(taskCard).toBeVisible({ timeout: 10000 });

    // Move task externally to doing via API
    const moveRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'agent-worker', type: 'agent' },
        version: task.version,
        status: 'doing',
      }),
    });
    expect(moveRes.ok).toBe(true);
    const moved = await moveRes.json();

    // Card moves into Doing column in real time
    const doingCard = doingCol.locator('h3', { hasText: 'Task Created via API for SSE' });
    await expect(doingCard).toBeVisible({ timeout: 10000 });
    await expect(todoCol.locator('h3', { hasText: 'Task Created via API for SSE' })).not.toBeVisible();

    // -------------------------------------------------------------------------
    // AC 2: Open detail drawer reflects comments, status, claims live
    // -------------------------------------------------------------------------
    // Click card to open detail drawer
    await doingCard.click();
    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByRole('heading', { level: 2 }).first()).toContainText('Task Created via API for SSE');

    // External agent claims the task via API
    const claimRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'agent-worker', type: 'agent' },
      }),
    });
    expect(claimRes.ok).toBe(true);
    const claimed = await claimRes.json();

    // Drawer reflects claim live
    await expect(drawer.getByText('Claimed', { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(drawer.locator('text=Held by agent-worker')).toBeVisible();

    // External agent posts a comment via API
    const commentRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/comments`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'reviewer-agent', type: 'agent' },
        kind: 'progress',
        body: 'Realtime SSE comment arrived!',
      }),
    });
    expect(commentRes.ok).toBe(true);

    // Drawer displays comment live
    await expect(drawer.locator('text=Realtime SSE comment arrived!')).toBeVisible({ timeout: 10000 });
    await expect(drawer.getByText('reviewer-agent', { exact: true })).toBeVisible();

    // External agent moves task to done via API
    const finishRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'agent-worker', type: 'agent' },
        version: claimed.version,
        status: 'done',
      }),
    });
    expect(finishRes.ok).toBe(true);

    // Drawer status button reflects "Done" as active
    const doneBtn = drawer.getByRole('button', { name: 'Done' });
    await expect(doneBtn).toHaveAttribute('aria-pressed', 'true', { timeout: 10000 });

    // Close drawer
    await drawer.locator('button[aria-label="Close"]').first().click();

    // -------------------------------------------------------------------------
    // AC 3: Reconnection with backoff & timer polling fallback
    // -------------------------------------------------------------------------
    // Disconnect SSE intentionally via app method and verify backoff progression
    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.handleSseDisconnect();
    });

    await expect(liveIndicator).toContainText('Reconnecting');
    const backoff1 = await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      return app.sseBackoffMs;
    });
    expect(backoff1).toBe(4000); // 2000 -> 4000

    await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      app.handleSseDisconnect();
    });
    const backoff2 = await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      return app.sseBackoffMs;
    });
    expect(backoff2).toBe(8000); // 4000 -> 8000

    // Verify polling fallback works even while SSE is disconnected
    // Create another task via API while disconnected
    const fallbackCreateRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        actor: { name: 'agent-worker', type: 'agent' },
        project_id: proj.id,
        title: 'Task Created During Disconnected State',
        priority: 2,
      }),
    });
    expect(fallbackCreateRes.ok).toBe(true);

    // Trigger timer pollTick fallback
    await page.evaluate(async () => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      await app.pollTick();
    });

    // Verify board updated via pollTick fallback
    const fallbackCard = todoCol.locator('h3', { hasText: 'Task Created During Disconnected State' });
    await expect(fallbackCard).toBeVisible({ timeout: 10000 });

    // Trigger manual reconnect
    await liveIndicator.click();
    await expect(liveIndicator).toContainText('Live', { timeout: 10000 });
    const resetBackoff = await page.evaluate(() => {
      const app = (window as any).Alpine.$data(document.querySelector('[x-data="app()"]'));
      return app.sseBackoffMs;
    });
    expect(resetBackoff).toBe(2000);
  });

  test('OpenCode autonomous agent integration: project settings and task start launcher', async ({
    page,
  }) => {
    // 1. Start a mock OpenCode server on a free port
    const mockPort = await getFreePort();
    let sessionCreated = false;
    let promptDispatched = false;

    const mockOpenCodeServer = http.createServer((req, res) => {
      if (req.method === 'POST' && req.url?.startsWith('/session') && !req.url.includes('/prompt_async')) {
        sessionCreated = true;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ id: 'mock-opencode-session-456' }));
        return;
      }
      if (req.method === 'POST' && req.url?.includes('/prompt_async')) {
        promptDispatched = true;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ status: 'dispatched' }));
        return;
      }
      res.writeHead(404);
      res.end();
    });

    await new Promise<void>((resolve) => mockOpenCodeServer.listen(mockPort, '127.0.0.1', resolve));

    try {
      // 2. Create a project and task via API
      const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: 'admin', type: 'human' },
          name: 'OpenCode Test Project',
        }),
      });
      const proj = await projRes.json();

      const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: 'admin', type: 'human' },
          project_id: proj.id,
          title: 'Implement Payment Gateway',
          description: 'Needs integration with Stripe API',
          acceptance_criteria: ['Stripe webhook handled', 'Checkout session created'],
        }),
      });
      const { task } = await taskRes.json();

      await page.goto(`${serverUrl}`);
      await selectProjectViaDropdown(page, proj.id);

      // -----------------------------------------------------------------------
      // AC 1: Project settings modal allows configuring OpenCode Server URL & dir
      // (opened from the gear icon attached to the Projects dropdown)
      // -----------------------------------------------------------------------
      const settingsBtn = page.getByTestId('project-settings-btn');
      await expect(settingsBtn).toBeVisible();
      await settingsBtn.click();

      const serverUrlInput = page.getByPlaceholder('http://localhost:4444');
      const dirInput = page.getByPlaceholder('/home/hollen/Code/...');
      await expect(serverUrlInput).toBeVisible();
      await expect(dirInput).toBeVisible();

      await serverUrlInput.fill(`http://127.0.0.1:${mockPort}`);
      await dirInput.fill('/tmp/mock-opencode-worktree');

      await page.getByRole('button', { name: 'Save changes' }).click();
      await expect(serverUrlInput).not.toBeVisible();

      // Reopen to verify settings persisted
      await settingsBtn.click();
      await expect(serverUrlInput).toHaveValue(`http://127.0.0.1:${mockPort}`);
      await expect(dirInput).toHaveValue('/tmp/mock-opencode-worktree');
      await page.getByRole('button', { name: 'Cancel' }).click();

      // -----------------------------------------------------------------------
      // AC 2: Task detail drawer renders 'Start Task in OpenCode' button
      // -----------------------------------------------------------------------
      const taskCard = page.locator('h3', { hasText: 'Implement Payment Gateway' });
      await taskCard.click();

      const drawer = page.locator('aside');
      await expect(drawer).toBeVisible();

      const startOpenCodeBtn = drawer.locator('[data-testid="start-opencode-btn"]');
      await expect(startOpenCodeBtn).toBeVisible();
      await expect(startOpenCodeBtn).toContainText('Start Task in OpenCode');

      // -----------------------------------------------------------------------
      // AC 3: Clicking button launches session, confirms, attaches link & opens tab
      // -----------------------------------------------------------------------
      // Intercept window.open
      await page.evaluate(() => {
        (window as any).__openedUrls = [];
        window.open = (url: string) => {
          (window as any).__openedUrls.push(url);
          return null;
        };
      });

      await startOpenCodeBtn.click();

      // Feedback toast appears
      const toast = page.locator('text=OpenCode session started');
      await expect(toast).toBeVisible({ timeout: 5000 });

      // Verify mock server received the requests
      expect(sessionCreated).toBe(true);
      expect(promptDispatched).toBe(true);

      // Verify window.open was NOT called (task starts silently server-side without navigating away)
      const openedUrls = await page.evaluate(() => (window as any).__openedUrls);
      expect(openedUrls).toHaveLength(0);

      // Verify session URL artifact appears in detail drawer
      await expect(drawer.locator('a', { hasText: 'mock-opencode-session-456' })).toBeVisible({ timeout: 5000 });
      await expect(drawer.locator('text=OpenCode autonomous session')).toBeVisible();

      // Verify task claim reflects opencode
      await expect(drawer.getByText('Claimed', { exact: true })).toBeVisible();
      await expect(drawer.locator('text=Held by opencode')).toBeVisible();

      // Close drawer
      await drawer.locator('button[aria-label="Close"]').first().click();
    } finally {
      await new Promise<void>((resolve) => mockOpenCodeServer.close(() => resolve()));
    }
  });

  test('sorts column cards by criterion and direction, clears to server order, persists across reload', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed a project with three tasks whose fields discriminate every sort
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Sorting Test Project',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const hourMs = 3600 * 1000;
    const iso = (offsetMs: number) => new Date(Date.now() + offsetMs).toISOString();
    // >1s apart so RFC3339 timestamps never collide regardless of storage precision
    const tick = () => new Promise((r) => setTimeout(r, 1100));

    const mkTask = async (body: Record<string, unknown>) => {
      const res = await fetch(`${serverUrl}/api/v1/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ project_id: project.id, priority: 2, actor: { name: 'e2e-seed', type: 'agent' }, ...body }),
      });
      expect(res.ok).toBe(true);
      return (await res.json()).task;
    };

    const tAlpha = await mkTask({ title: 'Alpha', priority: 2, due_at: iso(48 * hourMs) });
    await tick();
    const tBeta = await mkTask({ title: 'Beta', priority: 0, due_at: iso(1 * hourMs) });
    await tick();
    const tGamma = await mkTask({ title: 'Gamma', priority: 4 }); // no due date
    await tick();

    // Touch Gamma so it is the most-recently-updated task (created third AND updated last)
    const touchRes = await fetch(`${serverUrl}/api/v1/tasks/${tGamma.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: { name: 'e2e-seed', type: 'agent' }, version: tGamma.version, description: 'Touched for sort test' }),
    });
    expect(touchRes.ok).toBe(true);

    // -------------------------------------------------------------------------
    // Step 2: Open UI, select project, verify baseline server order (priority DESC)
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const todoCol = page.locator('section', { has: page.getByRole('heading', { name: 'To Do' }) });
    await expect(todoCol).toBeVisible();
    const cardTitles = async () => (await todoCol.locator('h3').allTextContents()).map((t) => t.trim());

    // Canonical server order: priority DESC → Gamma(4), Alpha(2), Beta(0)
    await expect.poll(cardTitles, { timeout: 10000 }).toEqual(['Gamma', 'Alpha', 'Beta']);

    // Default state: no sort active
    const sortKeySelect = page.locator('[data-testid="sort-key"]');
    const sortDirBtn = page.locator('[data-testid="sort-dir"]');
    const sortClearBtn = page.locator('[data-testid="sort-clear"]');
    const sortIndicator = page.locator('[data-testid="sort-indicator"]');
    await expect(sortKeySelect).toHaveValue('');
    await expect(sortDirBtn).toBeDisabled();
    await expect(sortClearBtn).not.toBeVisible();
    await expect(sortIndicator.first()).not.toBeVisible();

    // -------------------------------------------------------------------------
    // Step 3: Sort by Last Modified — desc default, then toggle to asc
    // -------------------------------------------------------------------------
    await sortKeySelect.selectOption('updated_at');
    await expect(sortDirBtn).toBeEnabled();
    await expect(sortClearBtn).toBeVisible();
    await expect(sortIndicator.first()).toBeVisible();
    await expect(sortIndicator.first()).toContainText('Last Modified ↓');

    // desc: Gamma (touched last) first, then Beta, Alpha
    await expect.poll(cardTitles).toEqual(['Gamma', 'Beta', 'Alpha']);

    // Toggle direction → asc: Alpha, Beta, Gamma
    await sortDirBtn.click();
    await expect(sortDirBtn).toContainText('↑ Ascending');
    await expect(page.locator('[data-testid="sort-indicator"]').first()).toContainText('Last Modified ↑');
    await expect.poll(cardTitles).toEqual(['Alpha', 'Beta', 'Gamma']);

    // -------------------------------------------------------------------------
    // Step 4: Other criteria — Priority asc, Due Date asc (nulls last)
    // -------------------------------------------------------------------------
    await sortKeySelect.selectOption('priority');
    await expect(sortDirBtn).toContainText('↑ Ascending'); // default dir for priority
    await expect.poll(cardTitles).toEqual(['Beta', 'Alpha', 'Gamma']);

    await sortKeySelect.selectOption('due_at');
    await expect.poll(cardTitles).toEqual(['Beta', 'Alpha', 'Gamma']); // Gamma (no due) last

    // -------------------------------------------------------------------------
    // Step 5: Clear sort → canonical server order restored, controls reset
    // -------------------------------------------------------------------------
    await sortClearBtn.click();
    await expect(sortKeySelect).toHaveValue('');
    await expect(sortDirBtn).toBeDisabled();
    await expect(sortClearBtn).not.toBeVisible();
    await expect(sortIndicator.first()).not.toBeVisible();
    await expect.poll(cardTitles).toEqual(['Gamma', 'Alpha', 'Beta']);

    // -------------------------------------------------------------------------
    // Step 6: Sort choice persists across a full page reload
    // -------------------------------------------------------------------------
    await sortKeySelect.selectOption('created_at');
    await expect(sortDirBtn).toContainText('↓ Descending'); // default dir for created_at
    await expect.poll(cardTitles).toEqual(['Gamma', 'Beta', 'Alpha']);

    await page.reload();
    await expect(page).toHaveTitle('crowban');
    // Project selection persists across reload (localStorage), shown on the trigger.
    await expect(page.getByTestId('project-select')).toContainText('Sorting Test Project');
    await expect(sortKeySelect).toHaveValue('created_at');
    await expect(sortDirBtn).toContainText('↓ Descending');
    await expect.poll(cardTitles).toEqual(['Gamma', 'Beta', 'Alpha']);

    // Blocked badges & card counts still intact on sorted columns
    const countBadge = todoCol.locator('header span.rounded-full');
    await expect(countBadge).toHaveText('3');
  });
});

