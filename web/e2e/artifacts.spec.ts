import { test, expect } from './fixtures';
import { randomUUID } from 'crypto';
import fs from 'fs'; // write upload source files + inspect server-side artifact store
import path from 'path';
import zlib from 'zlib';
import { selectProjectViaDropdown } from './helpers';

// E2E test suite for task 01M3WA51KRSQ1AKYSE9MZEJJ8P:
// Artifacts: store file uploads on a mountable volume; images inline (click for bigger view),
// other files in a new tab.

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
  // Each test gets its own dedicated server + empty artifact store (isolatedServer)
  // so it can inspect uploads on disk at isolatedServer.artifactsDir without other
  // specs' data; each seeds a unique project, so the temp DB is otherwise irrelevant.
  let serverUrl = ''; // per-test dedicated isolatedServer URL
  let uncaughtConsoleErrors: string[] = [];

  test.beforeEach(async ({ page }) => {
    uncaughtConsoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        uncaughtConsoleErrors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      console.log('PAGEERROR STACK:', err.stack);
      uncaughtConsoleErrors.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  test('uploads image, renders inline, click opens preview, other file renders as link, back-compat preserved', async ({ page, isolatedServer }) => {
    serverUrl = isolatedServer.url;
    const tempDir = isolatedServer.dataDir; // scratch for upload source files
    const artifactsDir = isolatedServer.artifactsDir; // where the server stores uploads
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

  test('clicking inline image opens lightbox preview modal', async ({ page, isolatedServer }) => {
    serverUrl = isolatedServer.url;
    const tempDir = isolatedServer.dataDir; // scratch for upload source files
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

  test('uploading video artifact displays inline video player and serves inline', async ({ page, isolatedServer }) => {
    serverUrl = isolatedServer.url;
    const tempDir = isolatedServer.dataDir; // scratch for upload source files
    const { projectId, taskId } = await seedProjectAndTask(serverUrl);

    const videoFilePath = path.join(tempDir, 'sample-test-run.mp4');
    fs.writeFileSync(videoFilePath, Buffer.from('fake-mp4-test-bytes'));

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const taskCard = page.locator('article', { hasText: 'Task with Artifacts' });
    await taskCard.click();

    const drawer = page.locator('aside');
    const attachBtn = drawer.getByRole('button', { name: '+ Attach artifact' });
    await attachBtn.click();

    const fileInput = drawer.locator('#artifactFileInput');
    await fileInput.setInputFiles(videoFilePath);

    const summaryInput = drawer.getByPlaceholder('One-line summary (optional)');
    await summaryInput.fill('Playwright test run');

    const submitBtn = drawer.getByRole('button', { name: 'Attach', exact: true });
    await submitBtn.click();

    const inlineVideo = drawer.locator('video');
    await expect(inlineVideo).toBeVisible({ timeout: 5000 });

    const videoSource = inlineVideo.locator('source');
    const src = await videoSource.getAttribute('src');
    expect(src).toMatch(/^\/artifacts\/[0-9A-HJKMNP-TV-Z]{26}$/);

    // Verify GET returns Content-Disposition: inline
    const serveRes = await fetch(`${serverUrl}${src}`);
    expect(serveRes.status).toBe(200);
    expect(serveRes.headers.get('content-type')).toContain('video/mp4');
    expect(serveRes.headers.get('content-disposition')).toBe('inline');
  });

  test('copy button copies artifact path with visual feedback and external URL has icon (01M3MMX4P8K9VCFC4MZKHQ2404)', async ({ page, isolatedServer, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    serverUrl = isolatedServer.url;
    const { projectId } = await seedProjectAndTask(serverUrl);

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const taskCard = page.locator('article', { hasText: 'Task with Artifacts' });
    await taskCard.click();

    const drawer = page.locator('aside');
    // AC2: URL artifact has target="_blank" and external link icon
    const urlLink = drawer.locator('a[href="https://example.com/docs/spec.pdf"]');
    await expect(urlLink).toBeVisible();
    await expect(urlLink).toHaveAttribute('target', '_blank');
    await expect(urlLink.locator('svg')).toBeVisible();

    // AC1: Copy button on non-web artifact
    const pathRow = drawer.locator('li', { hasText: '/var/log/legacy.log' });
    const copyBtn = pathRow.locator('[data-testid="copy-artifact-btn"]');
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();

    // AC3: Visual confirmation "Copied!" checkmark
    await expect(pathRow.getByText('Copied!')).toBeVisible();
    const clipText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipText).toBe('/var/log/legacy.log');
  });

  test('clipboard image paste and drag-and-drop file upload (01M5XTB1F8GFTEYPC2RDW6RFRQ)', async ({ page, isolatedServer }) => {
    serverUrl = isolatedServer.url;
    const { projectId } = await seedProjectAndTask(serverUrl);

    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectId);

    const taskCard = page.locator('article', { hasText: 'Task with Artifacts' });
    await taskCard.click();

    const drawer = page.locator('#cb-detail-drawer');
    await expect(drawer).toBeVisible();

    // 1. AC2: Drag-and-drop file onto artifacts section
    const artifactsSection = drawer.locator('[data-testid="detail-artifacts-section"]');
    await expect(artifactsSection).toBeVisible();

    // Dispatch drop event with a mock PNG file
    const dropFilePayload = {
      name: 'dropped-snapshot.png',
      mimeType: 'image/png',
      buffer: PNG_BYTES.toString('base64'),
    };
    await artifactsSection.evaluate((el, { name, mimeType, buffer }) => {
      const byteChars = atob(buffer);
      const byteNumbers = new Array(byteChars.length);
      for (let i = 0; i < byteChars.length; i++) {
        byteNumbers[i] = byteChars.charCodeAt(i);
      }
      const byteArray = new Uint8Array(byteNumbers);
      const blob = new Blob([byteArray], { type: mimeType });
      const file = new File([blob], name, { type: mimeType });
      const dataTransfer = new DataTransfer();
      dataTransfer.items.add(file);
      const dropEvt = new DragEvent('drop', {
        bubbles: true,
        cancelable: true,
        dataTransfer,
      });
      el.dispatchEvent(dropEvt);
    }, dropFilePayload);

    // Verify artifact is attached and visible
    await expect(drawer.locator('img[alt="dropped-snapshot.png"]')).toBeVisible({ timeout: 5000 });

    // 2. AC1 & AC3: Clipboard image paste inside comment textarea
    const commentInput = drawer.getByTestId('comment-textarea');
    await commentInput.focus();

    const pasteFilePayload = {
      name: 'pasted-capture.png',
      mimeType: 'image/png',
      buffer: PNG_BYTES.toString('base64'),
    };
    await commentInput.evaluate((el, { name, mimeType, buffer }) => {
      const byteChars = atob(buffer);
      const byteNumbers = new Array(byteChars.length);
      for (let i = 0; i < byteChars.length; i++) {
        byteNumbers[i] = byteChars.charCodeAt(i);
      }
      const byteArray = new Uint8Array(byteNumbers);
      const blob = new Blob([byteArray], { type: mimeType });
      const file = new File([blob], name, { type: mimeType });
      const dataTransfer = new DataTransfer();
      dataTransfer.items.add(file);
      const pasteEvt = new Event('paste', {
        bubbles: true,
        cancelable: true,
      });
      Object.defineProperty(pasteEvt, 'clipboardData', { value: dataTransfer });
      el.dispatchEvent(pasteEvt);
    }, pasteFilePayload);

    // Verify pasted image is attached and markdown reference is inserted into the textarea
    await expect(drawer.locator('img[alt="pasted-capture.png"]')).toBeVisible({ timeout: 5000 });
    await expect(commentInput).toHaveValue(/!\[screenshot\]\(\/artifacts\//);
  });
});
