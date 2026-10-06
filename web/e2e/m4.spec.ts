import { test, expect } from '@playwright/test';
import { selectProjectViaDropdown } from './helpers';
import { spawn, type ChildProcess } from 'child_process';
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
      // Retry
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

test.describe('Milestone 4: R8, R9, R10 Frontend Verification', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverPort: number;
  let serverUrl: string;

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-m4-'));
    const dbPath = path.join(tempDir, 'crowban.db');
    serverPort = await getFreePort();
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
    }
    if (tempDir && fs.existsSync(tempDir)) {
      try {
        fs.rmSync(tempDir, { recursive: true, force: true });
      } catch {
        /* ignore */
      }
    }
  });

  test('R8: App initializes and functions when window.localStorage throws SecurityError', async ({ page }) => {
    const uncaughtErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        uncaughtErrors.push(msg.text());
      }
    });
    page.on('pageerror', (err) => {
      uncaughtErrors.push(err.message);
    });

    // Emulate sandboxed iframe or private browsing where accessing localStorage throws SecurityError
    await page.addInitScript(() => {
      Object.defineProperty(window, 'localStorage', {
        get() {
          const err = new DOMException('The operation is insecure.', 'SecurityError');
          throw err;
        },
        configurable: true,
      });
    });

    await page.goto(serverUrl);

    // App should load without uncaught DOMException
    const header = page.locator('header').first();
    await expect(header).toBeVisible();

    // Verify no SecurityError or DOMException was logged as uncaught
    const hasSecurityError = uncaughtErrors.some((e) =>
      e.includes('SecurityError') || e.includes('The operation is insecure')
    );
    expect(hasSecurityError).toBe(false);

    // Create a project in the UI to confirm reactive state works with in-memory safeStorage
    const createProjectBtn = page.getByRole('button', { name: 'Create project' });
    if (await createProjectBtn.isVisible()) {
      await page.locator('input[x-model="newProject.name"]').fill('Restricted Env Project');
      await createProjectBtn.click();
    }

    // Verify project name appears in the custom Projects dropdown trigger
    await expect(page.getByTestId('project-select')).toContainText('Restricted Env Project');
  });

  test('R9: renderMarkdown renders blockquotes, code blocks, and sanitizes XSS in UI', async ({ page }) => {
    // 1. Seed project with tasks exercising markdown features
    const pRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'Markdown Test Project',
        actor: { name: 'test', type: 'human' },
      }),
    });
    const proj = await pRes.json();

    // Task 1: Blockquote
    await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: proj.id,
        title: 'Task With Blockquote',
        description: '> Note: verify claim lease carefully',
        priority: 2,
        actor: { name: 'test', type: 'human' },
      }),
    });

    // Task 2: Code block with special characters (<, >, &)
    await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: proj.id,
        title: 'Task With Code Block',
        description: '```javascript\nif (x < 10 && y > 20) { return true; }\n```',
        priority: 2,
        actor: { name: 'test', type: 'human' },
      }),
    });

    // Task 3: XSS attack payload
    await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: proj.id,
        title: 'Task With XSS Payload',
        description: '<script>alert("xss")</script><svg><animate onbegin=alert(1)>svg</svg><form><input value="hack"></form>Safe text here',
        priority: 2,
        actor: { name: 'test', type: 'human' },
      }),
    });

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, proj.id);

    // Verify Blockquote rendering
    const bqCard = page.locator('article', { hasText: 'Task With Blockquote' });
    await expect(bqCard).toBeVisible();
    await bqCard.click();

    const drawer = page.locator('aside');
    await expect(drawer).toBeVisible();
    const bq = drawer.locator('.md-body blockquote');
    await expect(bq).toBeVisible();
    await expect(bq).toContainText('Note: verify claim lease carefully');

    // Close drawer
    await drawer.locator('button[aria-label="Close"]').first().click();

    // Verify Code block rendering
    const codeCard = page.locator('article', { hasText: 'Task With Code Block' });
    await expect(codeCard).toBeVisible();
    await codeCard.click();

    await expect(drawer).toBeVisible();
    const code = drawer.locator('.md-body pre code');
    await expect(code).toBeVisible();
    // Inner text must contain unescaped characters (< and &)
    await expect(code).toContainText('if (x < 10 && y > 20) { return true; }');
    // Inner HTML must NOT contain double-escaped entities
    const innerHtml = await code.innerHTML();
    expect(innerHtml).not.toContain('&amp;lt;');
    expect(innerHtml).not.toContain('&amp;gt;');

    // Close drawer
    await drawer.locator('button[aria-label="Close"]').first().click();

    // Verify XSS sanitization
    const xssCard = page.locator('article', { hasText: 'Task With XSS Payload' });
    await expect(xssCard).toBeVisible();
    await xssCard.click();

    await expect(drawer).toBeVisible();
    const mdBody = drawer.locator('.md-body');
    await expect(mdBody).toContainText('Safe text here');
    // Forbidden tags must not exist in DOM
    await expect(drawer.locator('.md-body script')).toHaveCount(0);
    await expect(drawer.locator('.md-body svg')).toHaveCount(0);
    await expect(drawer.locator('.md-body input')).toHaveCount(0);
    await expect(drawer.locator('.md-body form')).toHaveCount(0);
  });

  test('R10: Kanban cards use <article> container, no role="button", and scoped keyboard handlers', async ({ page }) => {
    // 1. Seed project with a task
    const pRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'A11y Test Project',
        actor: { name: 'test', type: 'human' },
      }),
    });
    const proj = await pRes.json();

    await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: proj.id,
        title: 'A11y Semantic Card Task',
        priority: 1,
        actor: { name: 'test', type: 'human' },
      }),
    });

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, proj.id);

    // 1. Locate the card container: must be an <article>
    const card = page.locator('article', { hasText: 'A11y Semantic Card Task' });
    await expect(card).toBeVisible();

    // 2. Outer card container must NOT have role="button"
    const cardRole = await card.getAttribute('role');
    expect(cardRole).toBeNull();

    // 3. Card has tabindex="0" and aria-label
    expect(await card.getAttribute('tabindex')).toBe('0');
    const ariaLabel = await card.getAttribute('aria-label');
    expect(ariaLabel).toContain('Task: A11y Semantic Card Task');

    // 4. Inner button exists and is an interactive child of <article> (valid ARIA structure)
    const menuToggle = card.locator('[data-testid="card-menu-toggle"]');
    await expect(menuToggle).toBeVisible();

    // 5. Scoped keyboard navigation (.self):
    // Focusing the inner menu button and pressing Enter should NOT trigger the parent card's openTask drawer
    await menuToggle.focus();
    await page.keyboard.press('Enter');

    // Menu dropdown row should be visible
    await expect(card.locator('[data-testid="card-move-doing"]')).toBeVisible();

    // The task detail drawer (aside) must NOT be open
    const drawer = page.locator('aside');
    await expect(drawer).toBeHidden();

    // 6. Focusing the article card itself and pressing Enter SHOULD open the detail drawer
    await card.focus();
    await page.keyboard.press('Enter');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByRole('heading', { name: 'A11y Semantic Card Task' })).toBeVisible();
  });
});
