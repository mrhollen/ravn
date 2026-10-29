// Full-text board search (description/notes) with match-source indicator
// (task 01M5XTAW3RZ4QJCSW9C3CS255R).
//
// The board renders an instant client-side title/id substring filter; a
// debounced server query (GET /tasks?search=, which matches title+description+
// notes) contributes extra matched ids to the visible set. Cards whose TITLE
// and ID do not contain the term but which matched via description/notes show
// a subtle "matched in description/notes" chip (data-testid="match-source-chip").

import { test, expect } from '@playwright/test';
import { selectProjectViaDropdown, expectProjectSelected } from './helpers';
import { spawn, type ChildProcess } from 'child_process';
import fs from 'fs';
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
      // Retry
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

const ACTOR = { name: 'e2e', type: 'human' as const };

/** Unique terms — deliberately NOT present in any title. */
const DESC_TERM = 'quixotic-larkspur-9371'; // seeded into a task DESCRIPTION
const NOTES_TERM = 'velvet-cassowary-4821'; // seeded into a task's NOTES blob
const TITLE_TERM = 'quine-title-term'; // seeded into a task TITLE
const DONE_TERM = 'burly-zinnia-1734'; // seeded into the OLDEST done task's description

async function postJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${url} → ${res.status}`);
  return res.json();
}

async function putJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PUT ${url} → ${res.status}`);
  return res.json();
}

async function patchJson(url: string, body: unknown): Promise<any> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`PATCH ${url} → ${res.status}`);
  return res.json();
}

/** All VISIBLE board cards (scoped to the kanban grid so modal/full-page
 *  <article> nodes elsewhere in the DOM can't leak into the count). */
function boardCards(page: import('@playwright/test').Page) {
  return page.locator('.bg-dots article');
}

test.describe('Full-text board search across description and notes (01M5XTAW3RZ4QJCSW9C3CS255R)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;

  let projA: any;
  let taskDesc: any; // term in DESCRIPTION only
  let taskNotes: any; // term in NOTES only
  let taskTitle: any; // term in TITLE only
  let taskNone: any; // matches nothing

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-fulltext-'));
    const dbPath = path.join(tempDir, 'test.db');
    const port = await getFreePort();
    serverUrl = `http://127.0.0.1:${port}`;

    serverProcess = spawn(
      serverBin,
      ['-addr', `127.0.0.1:${port}`, '-data', dbPath, '-static', staticDir],
      { stdio: ['ignore', 'inherit', 'inherit'] },
    );

    await waitForServer(serverUrl);

    projA = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Full Text Alpha', actor: ACTOR });
    taskDesc = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Zeta Card D',
      description: `Body copy with ${DESC_TERM} deep in the prose.`,
      actor: ACTOR,
    })).task;

    taskNotes = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Zeta Card N',
      description: 'Generic body copy.',
      actor: ACTOR,
    })).task;
    // The NOTES blob carries the term (whole-blob replace, no version field).
    await putJson(`${serverUrl}/api/v1/tasks/${taskNotes.id}/notes`, {
      notes: `Findings so far: ${NOTES_TERM} is the key marker.`,
      actor: ACTOR,
    });

    taskTitle = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: `Zeta Card T ${TITLE_TERM}`,
      description: 'Generic body copy.',
      actor: ACTOR,
    })).task;

    taskNone = (await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projA.id,
      title: 'Zeta Card O',
      description: 'No special terms in here at all.',
      actor: ACTOR,
    })).task;
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

  test('AC1/AC2: typing a term found only in a task DESCRIPTION shows exactly that card, with the match-source chip', async ({ page }, testInfo) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projA.id);
    await expectProjectSelected(page, 'Full Text Alpha');

    // All four cards visible unfiltered.
    const cards = boardCards(page);
    await expect(cards).toHaveCount(4);

    // No VISIBLE chips before searching (the chip spans stay in the DOM,
    // hidden by x-show — assert on visibility, not raw DOM count).
    await expect(page.locator('.bg-dots [data-testid="match-source-chip"]:visible')).toHaveCount(0);

    // Type the description-only term (aria-label is stable across both inputs;
    // only one instance exists at desktop width).
    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await searchInput.fill(DESC_TERM);

    // Debounced server query lands (~250ms + fetch) → exactly ONE card visible.
    await expect(cards).toHaveCount(1);
    const only = cards.first();
    await expect(only).toContainText('Zeta Card D');

    // The chip is present on that card and carries the match-source label…
    const chip = only.getByTestId('match-source-chip');
    await expect(chip).toBeVisible();
    await expect(chip).toContainText('matched in description/notes');

    // …and the non-matching cards are gone.
    for (const hidden of ['Zeta Card N', 'Zeta Card T', 'Zeta Card O']) {
      await expect(page.locator('.bg-dots article', { hasText: hidden })).toHaveCount(0);
    }

    // URL round-trips the query while typing.
    await expect.poll(() => page.url()).toContain(`q=${DESC_TERM}`);

    // Evidence for the task artifact (chip visible on a card).
    await page.screenshot({ path: path.join(testInfo.outputDir, 'match-source-chip-evidence.png') });
  });

  test('AC3: clearing the search input immediately restores the full board and drops all chips', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projA.id);

    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await searchInput.fill(DESC_TERM);
    await expect(boardCards(page)).toHaveCount(1);
    await expect(page.getByTestId('match-source-chip')).toBeVisible();

    // Clear → instant local filter restores the unfiltered board; the
    // in-flight/stale server result must never repopulate the match set.
    await searchInput.fill('');
    await expect(boardCards(page)).toHaveCount(4);
    await expect(page.locator('.bg-dots [data-testid="match-source-chip"]:visible')).toHaveCount(0);
    for (const title of ['Zeta Card D', 'Zeta Card N', 'Zeta Card T', 'Zeta Card O']) {
      await expect(page.locator('.bg-dots article', { hasText: title })).toBeVisible();
    }
    // The q param leaves the URL with it.
    await expect.poll(() => page.url()).not.toContain('q=');
  });

  test('AC2: a TITLE match shows its card WITHOUT the match-source chip', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projA.id);

    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await searchInput.fill(TITLE_TERM);

    // Exactly the title-matched card survives (instant local match; the server
    // query returns it too, but the chip must NOT render for title hits).
    const cards = boardCards(page);
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('Zeta Card T');
    // No chip may surface for a title match — not even hidden-then-visible;
    // assert every chip on the visible card is hidden.
    await expect(cards.first().getByTestId('match-source-chip')).toBeHidden();
  });

  test('URL sync: booting with ?q=<notes term> runs the debounced full-text search on load', async ({ page }) => {
    // Direct URL load carrying a NOTES-only term (title/id filters alone would
    // show nothing) — the debounced server query must surface the card.
    const targetUrl = `${serverUrl}/?project=${projA.id}&q=${NOTES_TERM}`;
    await page.goto(targetUrl);
    await expectProjectSelected(page, 'Full Text Alpha');

    const cards = boardCards(page);
    await expect(cards).toHaveCount(1);
    await expect(cards.first()).toContainText('Zeta Card N');
    const chip = cards.first().getByTestId('match-source-chip');
    await expect(chip).toBeVisible();
    await expect(chip).toContainText('matched in description/notes');

    // The input shows the restored term.
    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await expect(searchInput).toHaveValue(NOTES_TERM);
  });
});

test.describe('Full-text search bypasses the done-column pagination cap (01M5XTAW3RZ4QJCSW9C3CS255R)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;

  let projB: any;

  test.beforeAll(async () => {
    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-fulltext-done-'));
    const dbPath = path.join(tempDir, 'test.db');
    const port = await getFreePort();
    serverUrl = `http://127.0.0.1:${port}`;

    serverProcess = spawn(
      serverBin,
      ['-addr', `127.0.0.1:${port}`, '-data', dbPath, '-static', staticDir],
      { stdio: ['ignore', 'inherit', 'inherit'] },
    );

    await waitForServer(serverUrl);

    // 25 done tasks in a fresh project; task 0 (completed FIRST → oldest) is
    // the one whose description carries the unique search term. Sequential
    // completion keeps strictly increasing completed_at timestamps.
    projB = await postJson(`${serverUrl}/api/v1/projects`, { name: 'Full Text Beta', actor: ACTOR });
    const created: any[] = [];
    for (let i = 0; i < 25; i++) {
      const t = (
        await postJson(`${serverUrl}/api/v1/tasks`, {
          project_id: projB.id,
          title: `Beta Done ${i}`,
          description: i === 0 ? `Archive notes mentioning ${DONE_TERM}.` : 'Routine record.',
          actor: ACTOR,
        })
      ).task;
      created.push(t);
    }
    // Workflow matrix is todo→doing→done (direct todo→done is rejected with a
    // TransitionError). The server stamps completed_at at SECOND precision, so a
    // tight completion loop would tie every task on the same second and fall back
    // to snapshot order. Complete task 0 FIRST, pause >1s, then complete the rest:
    // task 0's completed_at is strictly the oldest — deterministically the tail of
    // the newest-first Done sort, i.e. behind the default 20-card cap.
    const complete = async (t: any) => {
      const doing = await patchJson(`${serverUrl}/api/v1/tasks/${t.id}`, { status: 'doing', version: t.version, actor: ACTOR });
      await patchJson(`${serverUrl}/api/v1/tasks/${t.id}`, { status: 'done', version: doing.version, actor: ACTOR });
    };
    await complete(created[0]);
    await new Promise((r) => setTimeout(r, 1200));
    for (const t of created.slice(1)) {
      await complete(t);
    }
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

  test('a matched OLDEST done task surfaces despite the 20-card cap; clearing restores the cap and footer', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projB.id);
    await expectProjectSelected(page, 'Full Text Beta');

    // Normal state: cap of 20 applied, footer visible with expansion controls.
    // Column scoping by heading text — the same convention board.spec.ts uses.
    const doneCol = page.locator('section', { has: page.getByRole('heading', { name: 'Done' }) });
    const doneCards = doneCol.locator('article');
    await expect(doneCards).toHaveCount(20, 'default cap renders the 20 most-recent done tasks');
    // The footer node is rendered in every column (x-for) — only Done's is
    // visible; scope to the done lane to satisfy strict mode.
    const pagination = doneCol.getByTestId('done-pagination');
    await expect(pagination).toBeVisible();
    await expect(doneCol.getByTestId('done-show-more')).toContainText('Show more (5 older)');
    // The term-bearing task is the OLDEST — behind the cut, not visible.
    await expect(doneCol.locator('article', { hasText: 'Beta Done 0' })).toHaveCount(0);

    // Search the description-only term on the done task → it MUST surface even
    // though it sits beyond the default cap, and with the match-source chip.
    const searchInput = page.getByRole('textbox', { name: 'Filter cards by title or ID' }).first();
    await searchInput.fill(DONE_TERM);

    const cards = boardCards(page);
    await expect(cards).toHaveCount(1);
    const only = cards.first();
    await expect(only).toContainText('Beta Done 0');
    const chip = only.getByTestId('match-source-chip');
    await expect(chip).toBeVisible();

    // The pagination footer is hidden while a search is active (nothing to
    // expand/collapse — every matched done card is rendered).
    await expect(pagination).toBeHidden();

    // Clear → cap and footer come back; the oldest task hides behind the cut again.
    await searchInput.fill('');
    await expect(doneCards).toHaveCount(20);
    await expect(pagination).toBeVisible();
    await expect(doneCol.locator('article', { hasText: 'Beta Done 0' })).toHaveCount(0);
  });
});
