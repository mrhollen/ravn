// E2E coverage for Archiving a Project (Task 01M4YTXWRBC4Q0FEDJPYBERWPJ):
//   AC1 — Archive project button exists
//   AC2 — When clicking archive button, an input box opens instructing the user to type the project name
//   AC3 — The input box validates that the name is typed correctly (whitespace trimmed)
//   AC4 — The project is archived when a valid input is present and the user clicks "Archive"
//   AC5 — The user can click "Cancel" to dismiss the archive confirmation
import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { openNewProjectFromDropdown } from './helpers';

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
      // Retry until server is ready
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

test.describe('Project Archive in Settings Menu (01M4YTXWRBC4Q0FEDJPYBERWPJ)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async () => {
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/crowban-server ./cmd/crowban-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-archive-project-'));
    const dbPath = path.join(tempDir, 'crowban.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: dbPath,
        CROWBAN_STATIC_DIR: staticDir,
        CROWBAN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    await waitForServer(serverUrl);
  });

  test.afterAll(async () => {
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
  });

  test.beforeEach(async () => {
    uncaughtConsoleErrors = [];
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  async function setupPageAndProject(page: import('@playwright/test').Page, projectName: string) {
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('status of 409')) {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });

    await page.goto(serverUrl);
    await expect(page).toHaveTitle('crowban');
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeEnabled();

    // Create a new project via the UI
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill(projectName);
    await page.getByRole('button', { name: 'Create project' }).click();
    await expect(trigger).toContainText(projectName);

    const gear = page.getByTestId('project-settings-btn');
    await expect(gear).toBeEnabled();

    return { trigger, gear };
  }

  test('AC1, AC2, AC3, AC5: archive button exists, opens confirmation, validates input, and cancels cleanly', async ({
    page,
  }, testInfo) => {
    const projectName = 'Project For Validation';
    const { gear } = await setupPageAndProject(page, projectName);

    // Open project settings modal via gear button
    await gear.click();
    const modal = page.locator('#cb-project-settings');
    await expect(modal).toBeVisible();

    // AC1: Archive project button exists
    const archiveBtn = page.getByTestId('archive-project-btn');
    await expect(archiveBtn).toBeVisible();
    await expect(archiveBtn).toHaveText('Archive Project');
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac1-archive-button-exists.png') });

    // AC2: Clicking archive button opens an input box instructing user to type project name
    await archiveBtn.click();
    const confirmBox = page.getByTestId('archive-confirm-box');
    await expect(confirmBox).toBeVisible();
    await expect(confirmBox).toContainText('To archive this project, please type');
    await expect(confirmBox).toContainText(projectName);

    const input = page.getByTestId('archive-confirm-input');
    await expect(input).toBeVisible();
    await expect(input).toBeFocused();
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac2-archive-input-box-opened.png') });

    // AC3: The input box validates that the name is typed correctly (whitespace trimmed)
    const confirmBtn = page.getByTestId('confirm-archive-btn');
    await expect(confirmBtn).toBeVisible();
    await expect(confirmBtn).toBeDisabled();

    // Mismatched name -> disabled
    await input.fill('Wrong Name');
    await expect(confirmBtn).toBeDisabled();

    // Partial name -> disabled
    await input.fill('Project For');
    await expect(confirmBtn).toBeDisabled();

    // Whitespace trimmed matching name -> enabled
    await input.fill(`   ${projectName}   `);
    await expect(confirmBtn).toBeEnabled();
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac3-archive-input-validated-trimmed.png') });

    // Clearing input -> disabled again
    await input.fill('');
    await expect(confirmBtn).toBeDisabled();

    // AC5: The user can click "Cancel" to dismiss the archive confirmation
    await input.fill('Partial text');
    const cancelBtn = page.getByTestId('cancel-archive-btn');
    await expect(cancelBtn).toBeVisible();
    await cancelBtn.click();

    await expect(confirmBox).toBeHidden();
    await expect(archiveBtn).toBeVisible();
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac5-archive-confirmation-cancelled.png') });

    // Close the settings modal cleanly
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(modal).toBeHidden();
  });

  test('AC4: Project is archived when valid input is present and user clicks "Archive"', async ({
    page,
  }, testInfo) => {
    // Create Project A and Project B
    const projectNameA = 'Keep Active Alpha';
    const projectNameB = 'Archive Target Beta';
    const { gear } = await setupPageAndProject(page, projectNameA);

    // Create second project
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill(projectNameB);
    await page.getByRole('button', { name: 'Create project' }).click();

    const trigger = page.getByTestId('project-select');
    await expect(trigger).toContainText(projectNameB);

    // Verify both projects are initially present in the dropdown menu
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu.getByText(projectNameA)).toBeVisible();
    await expect(menu.getByText(projectNameB)).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();

    // Open settings for Project B
    await gear.click();
    const modal = page.locator('#cb-project-settings');
    await expect(modal).toBeVisible();

    // Click archive button
    await page.getByTestId('archive-project-btn').click();
    const confirmBox = page.getByTestId('archive-confirm-box');
    await expect(confirmBox).toBeVisible();

    // Fill the project name (whitespace trimmed)
    const input = page.getByTestId('archive-confirm-input');
    await input.fill(`  ${projectNameB}  `);

    const confirmBtn = page.getByTestId('confirm-archive-btn');
    await expect(confirmBtn).toBeEnabled();

    // AC4: Clicking "Archive" archives the project
    await confirmBtn.click();

    // Settings modal closes and toast appears
    await expect(modal).toBeHidden();
    const toast = page.getByTestId('toast-stack');
    await expect(toast).toContainText('Project archived');

    // Project selection automatically falls back to an active project (not Project B)
    await expect(trigger).not.toContainText(projectNameB);

    // Open dropdown: Project B must NO LONGER appear in the projects dropdown
    await trigger.click();
    await expect(menu.getByText(projectNameA)).toBeVisible();
    await expect(menu.getByText(projectNameB)).toHaveCount(0);
    await page.screenshot({ path: path.join(testInfo.outputDir, 'ac4-project-archived-removed-from-dropdown.png') });

    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
  });
});
