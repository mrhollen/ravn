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
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
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
      execSync('go build -trimpath -o bin/ravn-server ./cmd/ravn-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    // 3. Ephemeral temp DB directory (acceptance criterion 3)
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-'));
    const dbPath = path.join(tempDir, 'ravn.db');

    // 4. Free high-numbered port (never :8080) (acceptance criterion 3)
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);

    serverUrl = `http://127.0.0.1:${serverPort}`;

    // 5. Boot server with ephemeral DB and UI static dir
    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${serverPort}`,
        RAVN_DATA: dbPath,
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '', // Zero-config local mode
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
    const title = test.info().title;
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        if (msg.text().includes('status of 409')) return;
        // The boot-failure test deliberately ABORTS the projects fetch via
        // page.route — Chrome logs that as a resource error. Expected noise,
        // not an app bug (the app handles it with its boot-error screen).
        // Scoped to that test so real failures in other tests stay visible.
        if (title.includes('transient network failure') && msg.text().includes('net::ERR_FAILED')) return;
        // The failed-AC-toggle test deliberately answers the AC PATCH with a
        // 500 via page.route — Chrome logs that HTTP error as a resource
        // error. Expected noise, not an app bug (the app handles it with the
        // checkbox revert + error toast). Scoped to that test so real failures
        // in other tests stay visible.
        if (title.includes('failed AC toggle') && msg.text().includes('status of 500')) return;
        // The done-gate test deliberately trips the AC rejection, which the
        // API surfaces as a 400 invalid input (no override exists) — Chrome
        // logs that HTTP error as a resource error. Expected noise, not an app
        // bug (asserted via the toast text). Scoped to that test so real
        // failures in other tests stay visible.
        if (title.includes('transitioning to done') && msg.text().includes('status of 400')) return;
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
    await expect(page).toHaveTitle('Rävn');

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

    // Move task to "Doing" via the status dropdown
    const statusGroup = page.getByRole('group', { name: 'Status' });
    const statusTrigger = statusGroup.getByTestId('status-dropdown-trigger');
    await statusTrigger.click();
    const doingBtn = statusGroup.getByRole('button', { name: 'Doing' });
    await doingBtn.click();

    // Verify status trigger shows active
    await expect(statusTrigger).toContainText('Doing');

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

  test('card metadata badges: short ID, AC progress, and comment count', async ({
    page,
  }, testInfo) => {
    // -------------------------------------------------------------------------
    // Seed one "rich" task (3 criteria, 1 done, 2 comments) and one plain task
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Card Badge Project',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const richRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Rich Badge Task',
        priority: 2,
        acceptance_criteria: ['First criterion', 'Second criterion', 'Third criterion'],
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(richRes.ok).toBe(true);
    const rich = (await richRes.json()).task;

    // Mark AC 1 done, then post two comments.
    const acToggleRes = await fetch(`${serverUrl}/api/v1/tasks/${rich.id}/acceptance-criteria/1`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: { name: 'e2e-seed', type: 'agent' }, done: true }),
    });
    expect(acToggleRes.ok).toBe(true);
    for (let i = 1; i <= 2; i++) {
      const cRes = await fetch(`${serverUrl}/api/v1/tasks/${rich.id}/comments`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          actor: { name: `badge-agent-${i}`, type: 'agent' },
          kind: 'progress',
          body: `Badge comment ${i}`,
        }),
      });
      expect(cRes.ok).toBe(true);
    }

    const plainRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Plain Badge Task',
        priority: 3,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(plainRes.ok).toBe(true);
    const plain = (await plainRes.json()).task;

    // -------------------------------------------------------------------------
    // Load the board and assert the per-card badges
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const shortOf = (id: string) => (id.length > 10 ? `${id.slice(0, 8)}…` : id);

    const richCard = page.locator('article', { hasText: 'Rich Badge Task' });
    await expect(richCard).toBeVisible();

    // AC1: the card carries a muted monospace short-ID chip (full ULID in title)
    const richIdChip = richCard.getByTestId('card-short-id');
    await expect(richIdChip).toHaveText(shortOf(rich.id));
    await expect(richIdChip).toHaveAttribute('title', `Task ${rich.id}`);

    // AC2: AC progress badge reads 1/3 with an accessible tooltip
    const acBadge = richCard.getByTestId('card-ac-progress');
    await expect(acBadge).toContainText('1/3');
    await expect(acBadge).toHaveAttribute('title', '1 of 3 acceptance criteria complete');

    // AC3: comment count badge reads 2
    const commentBadge = richCard.getByTestId('card-comment-count');
    await expect(commentBadge).toContainText('2');
    await expect(commentBadge).toHaveAttribute('title', '2 comments');

    // Plain task: short-ID chip present, AC and comment badges hidden.
    const plainCard = page.locator('article', { hasText: 'Plain Badge Task' });
    await expect(plainCard).toBeVisible();
    await expect(plainCard.getByTestId('card-short-id')).toHaveText(shortOf(plain.id));
    await expect(plainCard.getByTestId('card-ac-progress')).not.toBeVisible();
    await expect(plainCard.getByTestId('card-comment-count')).not.toBeVisible();

    // QA evidence for the ticket: both cards with their badges in one shot.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'card-metadata-badges.png') });
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
    await expect(page).toHaveTitle('Rävn');

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

    // Check initial unclaimed state in claim controls (AC 5, AC 6)
    const claimControls = drawer.locator('[role="group"][aria-label="Claim Controls"]');
    const claimBtn = claimControls.getByRole('button', { name: 'Claim', exact: true });
    await expect(claimBtn).toBeVisible();
    await expect(claimBtn).toBeEnabled();

    // Click Claim
    await claimBtn.click();

    // Live update check: split capsule shows holder and live countdown, release button enabled
    await expect(claimControls.getByText('Human Tester')).toBeVisible();
    await expect(claimControls.locator('.live-countdown')).toBeVisible();
    const releaseBtn = claimControls.getByRole('button', { name: 'Release', exact: true });
    await expect(releaseBtn).toBeVisible();
    await expect(releaseBtn).toBeEnabled();

    // Click Release
    await releaseBtn.click();
    await expect(page.getByText('Released claim on task')).toBeVisible();
    await expect(claimBtn).toBeVisible();
    await expect(claimBtn).toBeEnabled();
    await expect(releaseBtn).not.toBeVisible();

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

    // Set assignee via owner popover
    const ownerTrigger = drawer.getByTestId('owner-popover-trigger');
    await ownerTrigger.click();
    const assigneeInput = drawer.getByPlaceholder('Assignee name (e.g. alice, agent-1)');
    await expect(assigneeInput).toBeVisible();
    await assigneeInput.fill('charlie-human');
    await drawer.getByRole('button', { name: 'Assign' }).click();
    await expect(page.getByText('Assigned to charlie-human')).toBeVisible();
    await expect(drawer.getByText('charlie-human').first()).toBeVisible();

    // Now claim the task to verify ephemeral claim lease is distinct from durable owner
    const claimControls = drawer.locator('[role="group"][aria-label="Claim Controls"]');
    await claimControls.getByRole('button', { name: 'Claim', exact: true }).click();
    await expect(page.getByText('Claimed task as')).toBeVisible();
    await expect(claimControls.locator('.live-countdown')).toBeVisible();
    await expect(drawer.getByText('charlie-human').first()).toBeVisible();

    // Close drawer and verify card shows both owner and claimer distinctly
    const closeBtn = drawer.locator('button[aria-label="Close"]').first();
    await closeBtn.click();
    const card3Updated = page.locator('article', { hasText: 'Gamma Low Task' });
    await expect(card3Updated.locator('span.bg-amber-50')).toContainText('charlie-human');

    // Reopen drawer, clear assignee via owner popover
    await card3Updated.click();
    await ownerTrigger.click();
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

  test('priority filter dropdown labels are identical to creation modal labels (P0..P5)', async ({
    page,
  }) => {
    // Regression lock: the board FILTER select and the task CREATION modal must
    // render their priority option labels from the same single source of truth
    // (priorityName in src/util.ts). The filter once hardcoded stale names
    // ("P3 (normal)", "P4 (low)", "P5 (backlog)") while the modals used
    // dynamic ones ("P3 — low", "P4 — background", "P5 — trivial"). This test
    // reads both rendered option lists and deep-compares them, so any drift in
    // wording, ordering, or format fails here.

    // Seed a fresh project (self-contained per repo convention).
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Priority Label Consistency Project',
        description: 'Seeded for the filter/modal priority label parity regression test',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    // Load UI and select the seeded project via the real dropdown.
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await selectProjectViaDropdown(page, project.id);
    await expectProjectSelected(page, 'Priority Label Consistency Project');

    // Board filter bar: read all priority option labels (scoped to the filter
    // select — the only select[x-model="filterPriority"] on the page).
    const filterSelect = page.locator('select[x-model="filterPriority"]');
    await expect(filterSelect).toBeVisible();
    const filterLabels = (await filterSelect.locator('option').allTextContents()).map((s) => s.trim());

    // The first option is the leading "All" sentinel; P0..P5 follow in order.
    expect(filterLabels[0]).toBe('All');
    const filterPriorityLabels = filterLabels.slice(1);

    // Creation modal: open it, then read its priority option labels.
    await page.getByRole('button', { name: '+ New task' }).click();
    const createSelect = page.locator('select[x-model\\.number="newTask.priority"]');
    await expect(createSelect).toBeVisible();
    const createLabels = (await createSelect.locator('option').allTextContents()).map((s) => s.trim());

    // Exactly six P0..P5 options, deep-equal between the two dropdowns.
    expect(createLabels).toHaveLength(6);
    expect(filterPriorityLabels).toEqual(createLabels);

    // Lock in the shared label format: "P<digit> — <name>" (em dash), so a
    // silent switch back to the old "P# (name)" style also fails this test.
    for (let p = 0; p <= 5; p++) {
      const labelShape = new RegExp(`^P${p} — `);
      expect(createLabels[p]).toMatch(labelShape);
      expect(filterPriorityLabels[p]).toMatch(labelShape);
    }
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

  // -----------------------------------------------------------------------
  // Ticket 01M3MMZ5VSPXWS9RYP1D24H5AG: the native AC checkbox flips its
  // .checked on click BEFORE the server answers. When the toggle request
  // fails, the DOM must revert to the model (checkbox unchecked), an error
  // toast must appear, and re-arming the route lets a retry succeed.

  test('failed AC toggle reverts checkbox and shows error toast', async ({ page }, testInfo) => {
    // -------------------------------------------------------------------------
    // Seed project and task with one UNCHECKED AC
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'AC Toggle Failure Project',
        description: 'Seeded for the failed-AC-toggle e2e test',
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
        title: 'Task for failed toggle',
        priority: 2,
        acceptance_criteria: ['Fails then succeeds'],
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    // Load UI and open the task detail drawer
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const taskCard = page.locator('article', { hasText: 'Task for failed toggle' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Task for failed toggle' })).toBeVisible();

    const acItem = drawer.locator('li', { hasText: 'Fails then succeeds' });
    const box = acItem.locator('input[type="checkbox"]');
    await expect(box).not.toBeChecked();

    // -------------------------------------------------------------------------
    // AC1 + AC2: the toggle PATCH is intercepted and answered with 500.
    // The checkbox must NOT remain checked after the failure, an error toast
    // (role=alert in the aria-live toast stack) must appear, and the server
    // row must still be unchecked.
    // -------------------------------------------------------------------------
    let failing = true;
    const acToggleUrl = new RegExp(`/api/v1/tasks/${task.id}/acceptance-criteria/1$`);
    await page.route(acToggleUrl, (route) => {
      if (!failing || route.request().method() !== 'PATCH') return route.continue();
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'internal', message: 'simulated AC toggle failure' }),
      });
    });

    await box.click();

    const errorToast = page.getByTestId('toast-stack').locator('[role="alert"]');
    await expect(errorToast).toContainText('simulated AC toggle failure');
    await expect(box).not.toBeChecked();

    // QA evidence for the ticket: reverted (unchecked) box + error toast in one shot.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac-toggle-failure-reverted.png') });

    const getRes1 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    expect(getRes1.ok).toBe(true);
    const detail1 = await getRes1.json();
    expect(detail1.acceptance_criteria[0].done).toBe(false);
    expect(detail1.acceptance_criteria[0].completed_by).toBeUndefined();

    // -------------------------------------------------------------------------
    // Re-arm the route: the same toggle now succeeds end to end.
    // -------------------------------------------------------------------------
    failing = false;
    await box.click();
    await expect(box).toBeChecked();

    const getRes2 = await fetch(`${serverUrl}/api/v1/tasks/${task.id}`);
    const detail2 = await getRes2.json();
    expect(detail2.acceptance_criteria[0].done).toBe(true);
    expect(detail2.acceptance_criteria[0].completed_by).toBeDefined();

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
    const claimControls = drawer.locator('[role="group"][aria-label="Claim Controls"]');
    await expect(claimControls.getByText('agent-worker')).toBeVisible({ timeout: 10000 });
    await expect(claimControls.locator('.live-countdown')).toBeVisible();
    await expect(claimControls.getByRole('button', { name: 'Release', exact: true })).toBeVisible();

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

    // Drawer status dropdown reflects "Done" as active
    const statusGroup = drawer.locator('[role="group"][aria-label="Status"]');
    const statusTrigger = statusGroup.getByTestId('status-dropdown-trigger');
    await expect(statusTrigger).toContainText('Done', { timeout: 10000 });

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
    let sessionBody: any = null;
    let promptBody: any = null;

    const mockOpenCodeServer = http.createServer((req, res) => {
      if (req.method === 'GET' && req.url?.startsWith('/agent')) {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify([
          { name: 'build', mode: 'all' },
          { name: 'plan', mode: 'plan' },
          { name: 'qa', mode: 'review' },
        ]));
        return;
      }
      if (req.method === 'GET' && req.url?.startsWith('/config/providers')) {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          providers: [
            {
              id: 'mockprov',
              name: 'Mock Provider',
              models: {
                'mock-model': {
                  name: 'Mock Model',
                  variants: { low: {}, medium: {}, high: {} },
                },
              },
            },
          ],
        }));
        return;
      }
      if (req.method === 'POST' && req.url?.startsWith('/session') && !req.url.includes('/prompt_async')) {
        sessionCreated = true;
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
          try { sessionBody = JSON.parse(body); } catch {}
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ id: 'mock-opencode-session-456' }));
        });
        return;
      }
      if (req.method === 'POST' && req.url?.includes('/prompt_async')) {
        promptDispatched = true;
        let body = '';
        req.on('data', (c) => { body += c; });
        req.on('end', () => {
          try { promptBody = JSON.parse(body); } catch {}
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ status: 'dispatched' }));
        });
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
      // AC 1 & AC 4: Project settings modal allows configuring OpenCode Server URL,
      // dir, default agent, model, and model variant
      // -----------------------------------------------------------------------
      const settingsBtn = page.getByTestId('project-settings-btn');
      await expect(settingsBtn).toBeVisible();
      await settingsBtn.click();

      const serverUrlInput = page.getByPlaceholder('http://localhost:4444');
      const dirInput = page.getByPlaceholder('/home/hollen/Code/...');
      const agentInput = page.getByTestId('project-opencode-agent-input');
      const modelInput = page.getByTestId('project-opencode-model-input');
      const variantInput = page.getByTestId('project-opencode-variant-input');
      await expect(serverUrlInput).toBeVisible();
      await expect(dirInput).toBeVisible();
      await expect(agentInput).toBeVisible();
      await expect(modelInput).toBeVisible();
      await expect(variantInput).toBeVisible();

      await serverUrlInput.fill(`http://127.0.0.1:${mockPort}`);
      await dirInput.fill('/tmp/mock-opencode-worktree');
      await agentInput.fill('plan');
      await modelInput.fill('mockprov/mock-model');
      await variantInput.fill('high');

      await page.screenshot({ path: 'test-results/opencode-project-settings.png' });
      await page.getByRole('button', { name: 'Save changes' }).click();
      await expect(serverUrlInput).not.toBeVisible();

      // Reopen to verify settings persisted
      await settingsBtn.click();
      await expect(serverUrlInput).toHaveValue(`http://127.0.0.1:${mockPort}`);
      await expect(dirInput).toHaveValue('/tmp/mock-opencode-worktree');
      await expect(agentInput).toHaveValue('plan');
      await expect(modelInput).toHaveValue('mockprov/mock-model');
      await expect(variantInput).toHaveValue('high');
      await page.getByRole('button', { name: 'Cancel' }).click();

      // -----------------------------------------------------------------------
      // AC 2 & AC 5: Task detail drawer renders 'Start Task in OpenCode' with
      // split-button config toggle, pre-populated defaults, and per-task override
      // -----------------------------------------------------------------------
      const taskCard = page.locator('h3', { hasText: 'Implement Payment Gateway' });
      await taskCard.click();

      const drawer = page.locator('aside');
      await expect(drawer).toBeVisible();

      const startOpenCodeBtn = drawer.locator('[data-testid="start-opencode-btn"]');
      await expect(startOpenCodeBtn).toBeVisible();
      await expect(startOpenCodeBtn).toContainText('Start Task in OpenCode');

      const configToggle = drawer.getByTestId('opencode-config-toggle');
      await expect(configToggle).toBeVisible();
      await configToggle.click();

      const configPanel = drawer.getByTestId('opencode-config-panel');
      await expect(configPanel).toBeVisible();

      const taskAgentInput = drawer.getByTestId('task-opencode-agent-input');
      const taskModelInput = drawer.getByTestId('task-opencode-model-input');
      const taskVariantInput = drawer.getByTestId('task-opencode-variant-input');

      // Pre-filled with project defaults
      await expect(taskAgentInput).toHaveValue('plan');
      await expect(taskModelInput).toHaveValue('mockprov/mock-model');
      await expect(taskVariantInput).toHaveValue('high');

      // Override agent and variant before dispatching
      await taskAgentInput.fill('qa');
      await taskVariantInput.fill('low');

      await page.screenshot({ path: 'test-results/opencode-config-panel.png' });

      // -----------------------------------------------------------------------
      // AC 3 & AC 5: Clicking button launches session with overrides, confirms,
      // attaches link & reflects opencode agent claim
      // -----------------------------------------------------------------------
      // Intercept window.open
      await page.evaluate(() => {
        (window as any).__openedUrls = [];
        window.open = (url: string) => {
          (window as any).__openedUrls.push(url);
          return null;
        };
      });

      const configStartBtn = drawer.getByTestId('opencode-config-start-btn');
      await expect(configStartBtn).toBeVisible();
      await configStartBtn.click();

      // Feedback toast appears with session ID and actionable link
      const expectedSessionUrl = `http://127.0.0.1:${mockPort}/session/mock-opencode-session-456`;
      const toast = page.locator('text=OpenCode session started');
      await expect(toast).toBeVisible({ timeout: 5000 });
      await expect(toast).toContainText('mock-opencode-session-456');
      const toastActionLink = page.locator('[data-testid="toast-action-link"]');
      await expect(toastActionLink).toBeVisible();
      await expect(toastActionLink).toHaveAttribute('href', expectedSessionUrl);
      await expect(toastActionLink).toHaveAttribute('target', '_blank');

      // Verify mock server received the requests with overridden agent and variant
      expect(sessionCreated).toBe(true);
      expect(promptDispatched).toBe(true);
      expect(sessionBody?.agent).toBe('qa');
      expect(sessionBody?.model?.variant).toBe('low');
      expect(promptBody?.agent).toBe('qa');
      expect(promptBody?.variant).toBe('low');

      // Verify window.open was NOT called (task starts silently server-side without navigating away)
      const openedUrls = await page.evaluate(() => (window as any).__openedUrls);
      expect(openedUrls).toHaveLength(0);

      // Verify OpenCode session banner in detail drawer
      await expect(drawer.getByTestId('opencode-session-banner')).toBeVisible({ timeout: 5000 });
      await expect(drawer.getByTestId('opencode-session-status')).toHaveText('OpenCode session active');
      await expect(drawer.getByTestId('opencode-session-id')).toHaveText('mock-opencode-session-456');
      const sessionLink = drawer.getByTestId('opencode-session-link');
      await expect(sessionLink).toBeVisible();
      await expect(sessionLink).toContainText('Open Session');
      await expect(sessionLink).toHaveAttribute('target', '_blank');
      await expect(sessionLink).toHaveAttribute('href', expectedSessionUrl);

      // Verify session URL artifact appears in detail drawer
      await expect(drawer.locator('a', { hasText: 'mock-opencode-session-456' }).first()).toBeVisible({ timeout: 5000 });
      await expect(drawer.locator('text=OpenCode autonomous session')).toBeVisible();

      // Verify task claim reflects opencode
      await expect(drawer.locator('[role="group"][aria-label="Claim Controls"]').getByText('opencode')).toBeVisible();
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
    await expect(page).toHaveTitle('Rävn');
    // Project selection persists across reload (localStorage), shown on the trigger.
    await expect(page.getByTestId('project-select')).toContainText('Sorting Test Project');
    await expect(sortKeySelect).toHaveValue('created_at');
    await expect(sortDirBtn).toContainText('↓ Descending');
    await expect.poll(cardTitles).toEqual(['Gamma', 'Beta', 'Alpha']);

    // Blocked badges & card counts still intact on sorted columns
    const countBadge = todoCol.locator('header span.rounded-full');
    await expect(countBadge).toHaveText('3');
  });

  test('AC1-3: transient network failure on boot preserves the stored project and offers Retry', async ({
    page,
  }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed a project + task via API so the board has content to render
    // after the connection recovers.
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Boot Failure Fixture',
        description: 'Seeded for the boot-failure e2e test',
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
        title: 'Survives the glitch',
        description: 'Seeded so the board renders after Retry.',
        priority: 2,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);

    // -------------------------------------------------------------------------
    // Step 2: Store the project id BEFORE page load (returning visitor) and make
    // the boot-time projects fetch fail hard — a transient network blip.
    // -------------------------------------------------------------------------
    await page.addInitScript((id: string) => { localStorage.setItem('ravn.project', id); }, project.id);
    const projectsUrl = `${serverUrl}/api/v1/projects`;
    await page.route(projectsUrl, (route) => route.abort());

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');

    // -------------------------------------------------------------------------
    // Step 3: The boot-error screen with Retry appears — NOT the New Project bar.
    // -------------------------------------------------------------------------
    const bootError = page.getByTestId('boot-error');
    await expect(bootError).toBeVisible();
    await expect(bootError).toContainText('Could not load projects', { exact: false });

    // The generic board-load error block is superseded by the boot-error screen.
    await expect(page.getByTestId('board-error')).not.toBeVisible();

    const retryBtn = bootError.getByRole('button', { name: 'Retry', exact: true });
    // Focus lands on Retry when the boot error shows (focus.ts convention).
    await expect(retryBtn).toBeFocused();

    // AC1 + AC2: stored selection intact in state AND localStorage, no modal.
    await expect(page.locator('#cb-new-project-name-input')).toBeHidden();
    expect(await page.evaluate(() => localStorage.getItem('ravn.project'))).toBe(project.id);

    // -------------------------------------------------------------------------
    // Step 4: The connection recovers; Retry re-runs the load + continuation.
    // -------------------------------------------------------------------------
    await page.unroute(projectsUrl);
    await retryBtn.click();

    // AC3: the stored project's board renders — the selection survived the glitch.
    await expectProjectSelected(page, 'Boot Failure Fixture');
    await expect(page.locator('article', { hasText: 'Survives the glitch' })).toBeVisible();
    expect(await page.evaluate(() => localStorage.getItem('ravn.project'))).toBe(project.id);

    // Both error screens are gone once the board is up.
    await expect(bootError).not.toBeVisible();
    await expect(page.getByTestId('board-error')).not.toBeVisible();
  });

  test('validates open blockers and acceptance criteria before transitioning to done', async ({ page }) => {
    // Step 1: Create project with a blocker and a dependent task that has ACs
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Done Gate Verification',
        description: 'Verifies blocked and AC completion invariant',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const blockerRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Prerequisite Blocker',
        priority: 3,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(blockerRes.ok).toBe(true);
    const blocker = (await blockerRes.json()).task;

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Guarded Work Task',
        priority: 2,
        acceptance_criteria: ['Must be tested in browser', 'Docs updated'],
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);
    const task = (await taskRes.json()).task;

    // Relate: task is blocked-by blocker
    const relRes = await fetch(`${serverUrl}/api/v1/tasks/${task.id}/relations`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        other_id: blocker.id,
        kind: 'blocked-by',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(relRes.ok).toBe(true);

    // Step 2: Open project on board
    await page.addInitScript((id: string) => { localStorage.setItem('ravn.project', id); }, project.id);
    await page.goto(serverUrl);
    await expectProjectSelected(page, 'Done Gate Verification');

    const card = page.locator('article', { hasText: 'Guarded Work Task' });
    await expect(card).toBeVisible();

    // Step 3: Open drawer and move to Doing
    await card.click();
    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    const statusGroup = page.getByRole('group', { name: 'Status' });
    const statusTrigger = statusGroup.getByTestId('status-dropdown-trigger');
    await statusTrigger.click();
    const doingBtn = statusGroup.getByRole('button', { name: 'Doing' });
    await doingBtn.click();
    await expect(statusTrigger).toContainText('Doing');

    // Step 4: Attempt to move to Done while blocked -> rejected with blocker error toast
    await statusTrigger.click();
    const doneBtn = statusGroup.getByRole('button', { name: 'Done' });
    await doneBtn.click();

    // Toast error appears explaining unresolved blockers
    const toast = page.getByTestId('toast-stack');
    await expect(toast).toContainText('blocked by 1 unresolved blocker(s)');
    // Status must remain Doing
    await expect(statusTrigger).toContainText('Doing');

    // Step 5: Resolve blocker via API (move blocker to doing then done)
    const moveB1 = await fetch(`${serverUrl}/api/v1/tasks/${blocker.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: blocker.version, status: 'doing', actor: { name: 'e2e', type: 'agent' } }),
    });
    expect(moveB1.ok).toBe(true);
    const bDoing = await moveB1.json();
    const moveB2 = await fetch(`${serverUrl}/api/v1/tasks/${blocker.id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: bDoing.version, status: 'done', actor: { name: 'e2e', type: 'agent' } }),
    });
    expect(moveB2.ok).toBe(true);

    // Step 6: Attempt to move to Done while ACs are incomplete -> rejected with the "incomplete acceptance criteria" invalid-input toast
    await statusTrigger.click();
    await doneBtn.click();
    await expect(toast).toContainText('incomplete acceptance criteria');
    await expect(statusTrigger).toContainText('Doing');

    // Step 7: Complete all acceptance criteria via UI checkboxes
    const ac1Box = drawer.locator('li', { hasText: 'Must be tested in browser' }).locator('input[type="checkbox"]');
    const ac2Box = drawer.locator('li', { hasText: 'Docs updated' }).locator('input[type="checkbox"]');
    await ac1Box.click();
    await expect(ac1Box).toBeChecked();
    await ac2Box.click();
    await expect(ac2Box).toBeChecked();

    // Step 8: Move to Done now that blockers are resolved and ACs are checked -> succeeds!
    await statusTrigger.click();
    await doneBtn.click();
    await expect(statusTrigger).toContainText('Done');
  });

  // -----------------------------------------------------------------------
  // Ticket 01M3MMTSSZ4F40EH4F30JA8TN7: on long tasks the drawer header must not
  // scroll out of view. The view-mode header and the edit-mode bar are shrink-0
  // flex siblings of the scroll body, so after scrolling deep into the content
  // Close / Edit / OpenCode (view) and Cancel / Save / × (edit) stay pinned at
  // the top — asserted here in BOTH modes with a long seeded task.

  test('pinned drawer header: title/close/edit stay visible while scrolling long task detail', async ({ page }, testInfo) => {
    // -------------------------------------------------------------------------
    // Seed a project with one LONG task (30 paragraphs + 8 ACs) so the drawer
    // body far exceeds its viewport and genuinely overflows.
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Pinned Header Project',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const longDescription = [
      '## Long task used to verify the pinned drawer header',
      '',
      ...Array.from(
        { length: 30 },
        (_, i) =>
          `Paragraph ${i + 1}: Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.`,
      ),
    ].join('\n');

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Long Task for Pinned Header',
        description: longDescription,
        priority: 2,
        acceptance_criteria: Array.from({ length: 8 }, (_, i) => `Pinned header criterion ${i + 1}`),
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);

    // -------------------------------------------------------------------------
    // Open the task drawer (modal skin at the default 1280x720 viewport)
    // -------------------------------------------------------------------------
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, project.id);

    const taskCard = page.locator('article', { hasText: 'Long Task for Pinned Header' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Long Task for Pinned Header' })).toBeVisible();

    // The ONLY scrollable region of the drawer is the body below the pinned header.
    const scroller = page.locator('#cb-detail-drawer aside .overflow-y-auto');
    await expect(scroller).toBeVisible();

    const viewCloseBtn = page.getByTestId('detail-close-btn');
    const editBtn = drawer.locator('button[title="Edit task details"]');
    const opencodeBtn = page.getByTestId('start-opencode-btn');
    for (const loc of [viewCloseBtn, editBtn, opencodeBtn]) {
      await expect(loc).toBeVisible();
    }

    // -------------------------------------------------------------------------
    // View mode: scroll the body well past its initial position — title, Close,
    // Edit and Start-Task-in-OpenCode must remain pinned in the viewport.
    // -------------------------------------------------------------------------
    const scrolled = await scroller.evaluate((el) => {
      el.scrollBy(0, 600);
      return el.scrollTop;
    });
    expect(scrolled).toBeGreaterThan(0, 'seeded long content must overflow the drawer body');

    await expect(viewCloseBtn).toBeVisible();
    const closeBox = await viewCloseBtn.boundingBox();
    expect(closeBox).not.toBeNull();
    expect(closeBox!.y).toBeGreaterThanOrEqual(0);
    await expect(editBtn).toBeVisible();
    await expect(opencodeBtn).toBeVisible();

    // QA evidence: long drawer scrolled down, header still pinned at the top.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'pinned-header-view-scrolled.png') });

    // -------------------------------------------------------------------------
    // Edit mode: switch to the edit bar and scroll to the very bottom — its
    // Cancel / Save / × row must stay pinned too (exactly one header pins per
    // mode; the view header is hidden while editing).
    // -------------------------------------------------------------------------
    await editBtn.click();
    const editBarHeading = drawer.getByRole('heading', { name: 'Edit task details' });
    await expect(editBarHeading).toBeVisible();
    await expect(viewCloseBtn).not.toBeVisible();

    const scrolledToBottom = await scroller.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
      return el.scrollTop;
    });
    expect(scrolledToBottom).toBeGreaterThan(scrolled, 'edit-mode content must still overflow');

    await expect(editBarHeading).toBeVisible();
    const editCloseBtn = page.getByTestId('detail-close-edit-btn');
    await expect(editCloseBtn).toBeVisible();
    await expect(drawer.getByRole('button', { name: 'Save', exact: true })).toBeVisible();
    const barBox = await editBarHeading.boundingBox();
    expect(barBox).not.toBeNull();
    expect(barBox!.y).toBeGreaterThanOrEqual(0);

    // QA evidence: scrolled to the bottom in edit mode, edit bar still pinned.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'pinned-header-edit-scrolled.png') });

    // Cancel from the pinned bar returns to view mode with the header intact.
    await drawer.getByRole('button', { name: 'Cancel' }).click();
    await expect(drawer.getByRole('heading', { name: 'Long Task for Pinned Header' })).toBeVisible();

    // Close via the (still scrolled) pinned view header — the exact workflow
    // the ticket was about.
    await viewCloseBtn.click();
    await expect(drawer).toBeHidden();
  });
});

