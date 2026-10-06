import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import { randomUUID } from 'crypto';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import zlib from 'zlib';
import { selectProjectViaDropdown } from './helpers';

// E2E test suite for task 01M3WA51KRSQ1AKYSE9MZEJJ8P:
// Artifacts: store file uploads on a mountable volume; images inline (click for bigger view),
// other files in a new tab.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'crowban-server');
const staticDir = path.resolve(__dirname, '../dist');

function crc32(buf: Buffer): number {
  let c = ~0;
  for (let i = 0; i < buf.length; i++) {
    c ^= buf[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (-(c & 1) & 0xedb88320);
  }
  return ~c >>> 0;
}

function makeChunk(type: string, data: Buffer): Buffer {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeAndData = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(typeAndData), 0);
  return Buffer.concat([len, typeAndData, crc]);
}

function generatePng(w: number, h: number, r: number, g: number, b: number): Buffer {
  const sig = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;
  ihdr[9] = 2; // RGB
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;
  const raw = Buffer.alloc(h * (1 + w * 3));
  let off = 0;
  for (let y = 0; y < h; y++) {
    raw[off++] = 0;
    for (let x = 0; x < w; x++) {
      raw[off++] = r;
      raw[off++] = g;
      raw[off++] = b;
    }
  }
  return Buffer.concat([
    sig,
    makeChunk('IHDR', ihdr),
    makeChunk('IDAT', zlib.deflateSync(raw)),
    makeChunk('IEND', Buffer.alloc(0)),
  ]);
}

const PNG_BYTES = generatePng(180, 100, 56, 189, 248);

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

async function seedProjectAndTask(serverUrl: string): Promise<{ projectId: string; taskId: string }> {
  const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: `Artifacts E2E ${randomUUID()}`,
      description: 'Project for artifact upload and display tests',
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
      title: 'Task with Artifacts',
      description: 'Testing file upload, inline images, and back-compat',
      priority: 3,
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(taskRes.ok).toBe(true);
  const task = await taskRes.json();

  // Attach back-compat artifacts: one URL and one filesystem path
  const urlArtRes = await fetch(`${serverUrl}/api/v1/tasks/${task.task.id}/artifacts`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      kind: 'url',
      ref: 'https://example.com/docs/spec.pdf',
      summary: 'Legacy URL artifact',
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(urlArtRes.ok).toBe(true);

  const pathArtRes = await fetch(`${serverUrl}/api/v1/tasks/${task.task.id}/artifacts`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      kind: 'log',
      ref: '/var/log/legacy.log',
      summary: 'Legacy path artifact',
      actor: { name: 'e2e-seed', type: 'agent' },
    }),
  });
  expect(pathArtRes.ok).toBe(true);

  return { projectId: project.id, taskId: task.task.id };
}

test.describe('artifacts storage, inline images and new-tab files', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let artifactsDir: string;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'crowban-artifacts-e2e-'));
    artifactsDir = path.join(tempDir, 'artifacts');
    fs.mkdirSync(artifactsDir, { recursive: true });

    const serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        CROWBAN_ADDR: `127.0.0.1:${serverPort}`,
        CROWBAN_DATA: path.join(tempDir, 'crowban.db'),
        CROWBAN_ARTIFACTS_DIR: artifactsDir,
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

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  test('uploads image, renders inline, click opens preview, other file renders as link, back-compat preserved', async ({ page }) => {
    const { projectId, taskId } = await seedProjectAndTask(serverUrl);

    // Prepare temp upload files
    const imageFilePath = path.join(tempDir, 'sample-screenshot.png');
    fs.writeFileSync(imageFilePath, PNG_BYTES);

    const textFilePath = path.join(tempDir, 'sample-log.txt');
    fs.writeFileSync(textFilePath, 'Sample log content line 1\nline 2\n');

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    // Open task detail drawer
    const taskCard = page.locator('article', { hasText: 'Task with Artifacts' });
    await expect(taskCard).toBeVisible();
    await taskCard.click();

    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: 'Task with Artifacts' })).toBeVisible();

    // 1. Back-compat: check legacy URL link and legacy path code
    const legacyUrlLink = drawer.locator('a[href="https://example.com/docs/spec.pdf"]');
    await expect(legacyUrlLink).toBeVisible();
    await expect(legacyUrlLink).toHaveAttribute('target', '_blank');

    const legacyPathCode = drawer.locator('code', { hasText: '/var/log/legacy.log' });
    await expect(legacyPathCode).toBeVisible();

    // 2. Upload image artifact via UI
    const attachBtn = drawer.getByRole('button', { name: '+ Attach artifact' });
    await expect(attachBtn).toBeVisible();
    await attachBtn.click();

    // Form is visible
    const fileInput = drawer.locator('#artifactFileInput');
    await expect(fileInput).toBeVisible();
    await fileInput.setInputFiles(imageFilePath);

    // Add summary
    const summaryInput = drawer.getByPlaceholder('One-line summary (optional)');
    await summaryInput.fill('UI test screenshot');

    // Submit attachment
    const submitBtn = drawer.getByRole('button', { name: 'Attach', exact: true });
    await submitBtn.click();

    // Wait for the new inline image to appear
    const inlineImg = drawer.locator('img[alt="UI test screenshot"]');
    await expect(inlineImg).toBeVisible({ timeout: 5000 });

    const imgSrc = await inlineImg.getAttribute('src');
    expect(imgSrc).toMatch(/^\/artifacts\/[0-9A-HJKMNP-TV-Z]{26}$/);

    // Extract artifact ULID from src
    const imageArtifactId = imgSrc!.replace('/artifacts/', '');

    // Verify file exists on disk in the configured artifactsDir
    const storedImageFiles = fs.readdirSync(artifactsDir).filter((f) => f.startsWith(imageArtifactId));
    expect(storedImageFiles.length).toBe(1);
    expect(storedImageFiles[0]).toBe(`${imageArtifactId}.png`);
    expect(fs.readFileSync(path.join(artifactsDir, storedImageFiles[0]))).toEqual(PNG_BYTES);

    // 3. Verify serving route GET /artifacts/{id} returns correct headers & bytes
    const serveRes = await fetch(`${serverUrl}${imgSrc}`);
    expect(serveRes.status).toBe(200);
    expect(serveRes.headers.get('content-type')).toContain('image/png');
    expect(serveRes.headers.get('x-content-type-options')).toBe('nosniff');
    expect(serveRes.headers.get('content-disposition')).toBe('inline');
    const servedBytes = Buffer.from(await serveRes.arrayBuffer());
    expect(servedBytes).toEqual(PNG_BYTES);

    // 4. Click inline image -> opens larger preview dialog
    const previewDialog = page.locator('#artifactPreviewDialog');
    await expect(previewDialog).not.toHaveAttribute('open', '');

    await inlineImg.click();
    await expect(previewDialog).toHaveAttribute('open', '');
    const previewImg = previewDialog.locator('img');
    await expect(previewImg).toBeVisible();
    await expect(previewImg).toHaveAttribute('src', imgSrc!);

    // Dismiss preview modal via Escape key
    await page.keyboard.press('Escape');
    await expect(previewDialog).not.toHaveAttribute('open', '');

    // Reopen and dismiss via close button
    await inlineImg.click();
    await expect(previewDialog).toHaveAttribute('open', '');
    const closeBtn = previewDialog.getByRole('button', { name: 'Close preview' });
    await closeBtn.click();
    await expect(previewDialog).not.toHaveAttribute('open', '');

    // 5. Upload non-image file (.txt) via UI
    await attachBtn.click();
    const kindSelect = drawer.locator('select[x-model="artifactForm.kind"]');
    await expect(kindSelect).toBeVisible();
    await kindSelect.selectOption('log');
    await fileInput.setInputFiles(textFilePath);
    await summaryInput.fill('Console log dump');
    await submitBtn.click();

    // Verify non-image appears as a new-tab link (not inline img)
    const logSummary = drawer.locator('text=Console log dump');
    await expect(logSummary).toBeVisible({ timeout: 5000 });

    const logLink = drawer.locator('a', { hasText: '/artifacts/' });
    await expect(logLink).toBeVisible();
    await expect(logLink).toHaveAttribute('target', '_blank');

    const logHref = await logLink.getAttribute('href');
    const textArtifactId = logHref!.replace('/artifacts/', '');

    // Verify file on disk
    const storedTextFiles = fs.readdirSync(artifactsDir).filter((f) => f.startsWith(textArtifactId));
    expect(storedTextFiles.length).toBe(1);
    expect(storedTextFiles[0]).toBe(`${textArtifactId}.txt`);

    // Verify serving route GET /artifacts/{id} for non-image forces download/attachment
    const serveTextRes = await fetch(`${serverUrl}${logHref}`);
    expect(serveTextRes.status).toBe(200);
    expect(serveTextRes.headers.get('content-type')).toContain('text/plain');
    expect(serveTextRes.headers.get('x-content-type-options')).toBe('nosniff');
    expect(serveTextRes.headers.get('content-disposition')).toContain(`attachment; filename="${textArtifactId}.txt"`);
    const textBody = await serveTextRes.text();
    expect(textBody).toContain('Sample log content line 1');

    // 6. Security: path traversal check returns 400 or 404
    const traversalRes = await fetch(`${serverUrl}/artifacts/..%2Fetc%2Fpasswd`);
    expect(traversalRes.status).toBeGreaterThanOrEqual(400);

    const notFoundRes = await fetch(`${serverUrl}/artifacts/01HZZZZZZZZZZZZZZZZZZZZZZZ`);
    expect(notFoundRes.status).toBe(404);
  });

  test('clicking inline image opens lightbox preview modal', async ({ page }) => {
    const { projectId, taskId } = await seedProjectAndTask(serverUrl);

    const imageFilePath = path.join(tempDir, 'sample-screenshot-2.png');
    fs.writeFileSync(imageFilePath, PNG_BYTES);

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const taskCard = page.locator('article', { hasText: 'Task with Artifacts' });
    await taskCard.click();

    const drawer = page.locator('aside');
    const attachBtn = drawer.getByRole('button', { name: '+ Attach artifact' });
    await attachBtn.click();

    const fileInput = drawer.locator('#artifactFileInput');
    await fileInput.setInputFiles(imageFilePath);

    const summaryInput = drawer.getByPlaceholder('One-line summary (optional)');
    await summaryInput.fill('Lightbox preview test');

    const submitBtn = drawer.getByRole('button', { name: 'Attach', exact: true });
    await submitBtn.click();

    const inlineImg = drawer.locator('img[alt="Lightbox preview test"]');
    await expect(inlineImg).toBeVisible({ timeout: 5000 });

    const previewDialog = page.locator('#artifactPreviewDialog');
    await inlineImg.click();

    // Verify preview modal is open with the image displayed
    await expect(previewDialog).toHaveAttribute('open', '');
    const previewImg = previewDialog.locator('img');
    await expect(previewImg).toBeVisible();
  });
});
