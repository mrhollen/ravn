// Card 3-dot move menu — floating popover behaviour (task 01M3MMVYMDH70JSR07GS1G19ST).
//   AC1 — clicking outside the trigger + dropdown dismisses the menu
//   AC2 — the "Move to" picker renders as a floating dropdown below the ⋮ trigger
//   AC3 — opening the menu does not shift the card or any sibling card's layout
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
      // Retry until the server is listening.
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

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
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;
  let projectId: string;

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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-e2e-card-menu-'));
    const dbPath = path.join(tempDir, 'crowban.db');
    serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    expect(serverPort).toBeGreaterThan(1024);
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
      if (process.env.DEBUG_E2E) console.error('[server stderr]', d.toString());
    });

    await waitForServer(serverUrl);

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
      try {
        fs.rmSync(tempDir, { recursive: true, force: true });
      } catch {
        /* ignore */
      }
    }
  });

  test('AC1: outside click dismisses the move menu; toggle and label clicks still behave', async ({ page }) => {
    await page.goto(serverUrl);
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
