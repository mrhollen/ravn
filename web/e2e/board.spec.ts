import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
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

    // Header and project dropdown should reflect seeded project
    const projectSelect = page.locator('select').first();
    await expect(projectSelect).toBeVisible();
    await expect(projectSelect).toHaveValue(project.id);

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

    const projectSelect = page.locator('select').first();
    await projectSelect.selectOption(project.id);

    // Set actor name
    const actorInput = page.getByPlaceholder('you');
    await actorInput.fill('Human Tester');
    await actorInput.dispatchEvent('change');

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
    const projectSelect = page.locator('select').first();
    await projectSelect.selectOption(project.id);
    await expect(page.locator('h3', { hasText: 'Alpha Critical Task' })).toBeVisible();

    // -------------------------------------------------------------------------
    // AC 1: Project settings modal / edit
    // -------------------------------------------------------------------------
    const settingsBtn = page.locator('button[aria-label="Project settings"]');
    await expect(settingsBtn).toBeVisible();
    await settingsBtn.click();

    // Check modal opens with existing name
    const nameInput = page.getByPlaceholder('Project name');
    await expect(nameInput).toBeVisible();
    await expect(nameInput).toHaveValue('Settings & Filter Project');

    // Update name and description
    await nameInput.fill('Renamed Project E2E');
    await page.getByPlaceholder('Project description…').fill('Updated project description');
    await page.getByRole('button', { name: 'Save changes' }).click();

    // Toast and selector update
    await expect(page.getByText('Project updated')).toBeVisible();
    await expect(projectSelect).toContainText('Renamed Project E2E');

    // Reload page and verify persisted
    await page.reload();
    await expect(projectSelect).toHaveValue(project.id);
    await expect(projectSelect).toContainText('Renamed Project E2E');

    // -------------------------------------------------------------------------
    // AC 2: Assignee selector & durable owner chip distinct from claimer
    // -------------------------------------------------------------------------
    // Verify Task 1 has distinct amber Owner chip on card
    const card1 = page.locator('div[role="button"]', { hasText: 'Alpha Critical Task' });
    const ownerChip1 = card1.locator('span.bg-amber-50');
    await expect(ownerChip1).toBeVisible();
    await expect(ownerChip1).toContainText('alice');

    // Open detail drawer for Task 3 (unassigned)
    const card3 = page.locator('div[role="button"]', { hasText: 'Gamma Low Task' });
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
    const card3Updated = page.locator('div[role="button"]', { hasText: 'Gamma Low Task' });
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

    const deltaCard = page.locator('div[role="button"]', { hasText: 'Delta Assigned Task' });
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
});

