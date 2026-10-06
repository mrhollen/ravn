// E2E coverage for the project-settings GEAR attached to the Projects dropdown
// (task 01M47NPQHY5CR2VPG14HVDM02P):
//   AC1 — the spelled-out "Settings" ghost button is gone from the header
//   AC2 — the gear sits at the right edge of div[data-testid=project-dropdown]
//         as a split control (shared border unit, same height); it opens the
//         EXISTING project settings modal and is disabled (muted, unclickable)
//         while no project is selected; boot gating mirrors the trigger (!appBooted)
//   AC3 — keyboard: Tab reaches the gear, Enter/Space open the modal, Escape
//         closes it (existing modal behavior) and returns focus to the gear
// Self-contained: ephemeral crowban-server on a random high port + temp DB,
// serving the pre-built web/dist (mirrors project-dropdown.spec.ts).
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
      // Retry until the server is up.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

test.describe('Project settings gear (split control on the Projects dropdown)', () => {
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-settings-gear-'));
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

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);
    // NOTE: deliberately NO projects are seeded — booting with zero projects is
    // exactly the state where the gear must be disabled (nothing to configure).
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

  /** Load a fresh page and wait for app boot (the trigger enables when init()
   *  finishes). Returns the split-control locators. */
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
    await expect(page).toHaveTitle('crowban');
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeEnabled(); // appBooted === true
    return { trigger, gear: page.getByTestId('project-settings-btn') };
  }

  test('AC1: spelled-out "Settings" button is gone; the gear is disabled with no project', async ({
    page,
  }, testInfo) => {
    const { trigger, gear } = await freshPage(page);
    const topHeader = page.locator('header').first();

    // AC1 — no button labelled exactly "Settings" anywhere in the top bar.
    await expect(topHeader.getByRole('button', { name: 'Settings', exact: true })).toHaveCount(0);
    // The strip (live indicator cluster) carries no settings affordance either:
    // the word itself is gone from the header's visible text.
    const headerText = await topHeader.innerText();
    expect(headerText, 'header must not spell out "Settings" anymore').not.toMatch(/\bSettings\b/);

    // The gear exists, is attached to the dropdown unit, and its accessible
    // name survives the icon-only redesign.
    await expect(page.locator('[data-testid="project-dropdown"]').getByTestId('project-settings-btn')).toHaveCount(1);
    await expect(gear).toBeVisible();
    await expect(gear).toHaveAttribute('aria-label', 'Project settings');

    // AC2 (disabled state) — booted, but NOTHING is selected: the gear must be
    // disabled (visually muted + not clickable) while the sibling trigger is
    // already enabled. Clicking it changes nothing.
    await expect(trigger).toBeEnabled();
    await expect(gear).toBeDisabled(), 'no project selected → gear must be disabled';
    // Disabled buttons are inert in the browser: even a forced synthetic click
    // at their position can't fire the handler, so the modal stays closed.
    const modal = page.locator('#cb-project-settings');
    await expect(modal).toBeHidden();
    await gear.click({ force: true });
    await expect(modal, 'disabled gear must not open the modal').toBeHidden();

    // Boot gating lives on the same unit: pre-boot BOTH halves are inert (the
    // trigger is !appBooted-gated, the gear additionally requires a selection).
    // At boot with an empty list, selectedProjectId === "" is what keeps the
    // gear disabled while the trigger is already enabled — verified above.

    // Evidence: compact header with the muted gear, no project selected yet.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'gear-disabled-no-project.png') });
  });

  test('AC2: split control geometry + panel spans the whole unit; creating a project enables the gear', async ({
    page,
  }, testInfo) => {
    const { trigger, gear } = await freshPage(page);
    const wrapper = page.getByTestId('project-dropdown');

    // Split control: one bordered unit — the gear is attached to the right edge
    // of the select (no gap), shares its height and vertical position, and the
    // divider sits between them.
    const tbox = await trigger.boundingBox();
    const gbox = await gear.boundingBox();
    expect(tbox).not.toBeNull();
    expect(gbox).not.toBeNull();
    expect(Math.abs(gbox!.x - (tbox!.x + tbox!.width)), 'gear starts at the select\'s right edge').toBeLessThanOrEqual(1);
    expect(Math.abs(gbox!.y - tbox!.y), 'shared top').toBeLessThanOrEqual(1);
    expect(Math.abs(gbox!.height - tbox!.height), 'shared height').toBeLessThanOrEqual(1);
    // The unit fits the row: ends before the "+ New task" button starts.
    const newTaskBox = (await page.getByRole('button', { name: '+ New task' }).boundingBox())!;
    expect(gbox!.x + gbox!.width, 'unit stays left of the rest of the header').toBeLessThanOrEqual(newTaskBox.x);

    // Create a project from inside the dropdown — auto-selects it, which is
    // what unlocks the gear (selectedProjectId becomes non-empty).
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill('Gear Probe Project');
    await page.getByRole('button', { name: 'Create project' }).click();
    await expect(trigger).toContainText('Gear Probe Project');
    await expect(gear).toBeEnabled(), 'gear enables once a project is selected';

    // Menu panel geometry: anchored to the WHOLE split unit — it spans select +
    // gear (deliberate: matches the compound control's footprint), stays in the
    // dropdown z-40 tier, and remains within the viewport.
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu).toBeVisible();
    const mbox = await menu.boundingBox();
    const wbox = await wrapper.boundingBox();
    expect(mbox).not.toBeNull();
    expect(wbox).not.toBeNull();
    expect(Math.abs(mbox!.x - wbox!.x), 'panel left edge aligns with the unit').toBeLessThanOrEqual(1);
    expect(Math.abs(mbox!.width - wbox!.width), 'panel spans select + gear').toBeLessThanOrEqual(2);
    expect(mbox!.x).toBeGreaterThanOrEqual(0);
    expect(mbox!.x + mbox!.width).toBeLessThanOrEqual(page.viewportSize()!.width);

    // Leave the menu open so the auto-captured test screenshot documents the
    // alignment, then close it for a clean mid-flow shot of the unit.
    const hbox = (await page.locator('header').first().boundingBox())!;
    await page.screenshot({
      path: path.join(testInfo.outputDir, 'split-control-menu-open.png'),
      clip: { x: Math.max(0, wbox!.x - 16), y: hbox.y + 2, width: Math.min(wbox!.width + 32, page.viewportSize()!.width - wbox!.x), height: mbox!.y + mbox!.height - hbox.y },
    });
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
  });

  test('AC2/AC3: gear opens the existing settings modal; Enter and Space work; Escape closes and restores focus', async ({
    page,
  }, testInfo) => {
    const { trigger, gear } = await freshPage(page);

    // Give the project a name worth seeing in the modal.
    await openNewProjectFromDropdown(page);
    await page.locator('#cb-new-project-name-input').fill('Keyboard Project');
    await page.getByRole('button', { name: 'Create project' }).click();
    await expect(trigger).toContainText('Keyboard Project');
    await expect(gear).toBeEnabled();

    const modal = page.locator('#cb-project-settings');
    const modalHeading = page.getByRole('heading', { name: 'Project settings' });
    const nameInput = page.getByPlaceholder('Project name');

    // Keyboard: the gear is a native button — Tab reaches it in document order
    // (right after the select trigger), Enter opens the modal.
    await trigger.focus();
    await page.keyboard.press('Tab');
    await expect(gear).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(modal).toBeVisible();
    await expect(modalHeading).toBeVisible();
    await expect(nameInput).toHaveValue('Keyboard Project');

    // Evidence: the existing modal, opened from the gear.
    await page.screenshot({ path: path.join(testInfo.outputDir, 'settings-modal-from-gear.png') });

    // Escape closes it (existing top-level dismiss chain) and focus returns to
    // the gear — it behaves like any modal button, not a header menu.
    await page.keyboard.press('Escape');
    await expect(modal).toBeHidden();
    await expect(gear).toBeFocused();

    // Space is an equally valid activation key.
    await page.keyboard.press('Space');
    await expect(modal).toBeVisible();
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(modal).toBeHidden();

    // Dropdown-menu interplay: with the Projects menu OPEN, activating the gear
    // closes the menu AND opens the modal (the gear sits inside the control's
    // wrapper, so its handler must not leave the panel open under the modal).
    await trigger.click();
    const menu = page.getByTestId('project-menu');
    await expect(menu).toBeVisible();
    await gear.click();
    await expect(menu).toBeHidden(), 'dropdown must close when the gear activates';
    await expect(modal).toBeVisible(), '…and the modal must open on the same click';
    await page.keyboard.press('Escape');
    await expect(modal).toBeHidden();
  });
});
