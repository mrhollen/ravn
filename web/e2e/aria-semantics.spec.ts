// ARIA semantics fixes (filed as one a11y batch, verified here end-to-end):
//   Task A — 01M3MMVPRGDF20NF81BRYJSYCA: AC checkboxes derive their accessible
//            name from the wrapping <label> (criterion text + completion
//            attribution) instead of a hardcoded "Mark done:" aria-label that
//            overrode it and silenced the attribution (WCAG 4.1.2).
//   Task B — 01M3MMVV92639YJ5822824NTV1: document.title follows the active
//            view — "<task> – <project> – Rävn" while a detail is open,
//            "<project> – Rävn" on a user-driven project switch, reverting on
//            close (WCAG 2.4.2). Boot keeps the static "Rävn" title.
//   Task C — 01M3MMW18VTX81KQM2GRHM1ZTW: collapsible toggles expose
//            aria-expanded reflecting their state ("New project", card move
//            menu, "+ Attach artifact"), the card menu declares aria-haspopup,
//            and decorative SVGs are hidden from the accessibility tree.
import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown } from './helpers';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');

const ACTOR = { name: 'e2e-aria-semantics-seed', type: 'agent' as const };
const PROJECT_A = 'Aria Semantics Alpha'; // created first → boot auto-select
const PROJECT_B = 'Aria Semantics Beta';
const AC_TASK_TITLE = 'AC Naming Fixture Task';
const AC_UNDID = 'Keep the board green'; // starts undone, no note → exact name
const AC_DONE = 'Ship the accessible name fix'; // pre-completed via REST
const TITLE_TASK_TITLE = 'Title Fixture Task';

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

/** The new-project form bar (static markup under an x-show div). */
function newProjectBar(page: import('@playwright/test').Page): import('@playwright/test').Locator {
  return page.locator('form', { has: page.locator('#cb-new-project-name-input') });
}

/** Open the detail drawer for a task by its card title (desktop viewport →
 *  wide centered modal rendered as <aside>, same contract the other specs use). */
async function openTaskDrawer(
  page: import('@playwright/test').Page,
  title: string,
): Promise<import('@playwright/test').Locator> {
  const card = page.locator('article', { hasText: title });
  await expect(card).toBeVisible();
  await card.getByRole('heading', { name: title }).click();
  const drawer = page.locator('aside');
  await expect(drawer.getByRole('heading', { name: title })).toBeVisible();
  return drawer;
}

test.describe('ARIA semantics (AC names, dynamic title, expanded states + decorative SVGs)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
  let projectA: string;
  let projectB: string;

  test.beforeAll(async () => {
    if (!fs.existsSync(path.join(staticDir, 'index.html'))) {
      execSync('npm run build', { cwd: path.resolve(__dirname, '..'), stdio: 'inherit' });
    }
    if (!fs.existsSync(serverBin)) {
      execSync('go build -trimpath -o bin/ravn-server ./cmd/ravn-server', {
        cwd: repoRoot,
        stdio: 'inherit',
      });
    }

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-aria-semantics-'));
    const port = await getFreePort();
    expect(port).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${port}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${port}`,
        RAVN_DATA: path.join(tempDir, 'ravn.db'),
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      if (process.env.DEBUG_E2E) console.error('[server stderr]', d.toString());
    });

    await waitForServer(serverUrl);

    const projA = await postJson(`${serverUrl}/api/v1/projects`, {
      name: PROJECT_A,
      description: 'Seeded for the ARIA semantics e2e spec',
      actor: ACTOR,
    });
    projectA = projA.id;
    const projB = await postJson(`${serverUrl}/api/v1/projects`, {
      name: PROJECT_B,
      description: 'Second project for title switch assertions',
      actor: ACTOR,
    });
    projectB = projB.id;

    // Project A: the AC naming fixture with one pre-completed criterion (its
    // completion attribution must surface in the checkbox accessible name) and
    // one open criterion whose name must be EXACTLY its text.
    const acTask = await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectA,
      title: AC_TASK_TITLE,
      priority: 2,
      acceptance_criteria: [AC_DONE, AC_UNDID],
      actor: ACTOR,
    });
    const acTaskId = acTask.task.id;
    const completeRes = await fetch(
      `${serverUrl}/api/v1/tasks/${acTaskId}/acceptance-criteria/1`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ actor: ACTOR, done: true }),
      },
    );
    expect(completeRes.ok).toBe(true);

    // Project B: a plain task for the title assertions.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectB,
      title: TITLE_TASK_TITLE,
      priority: 2,
      actor: ACTOR,
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

  // ------------------------------------------------------------------ Task A

  test('AC checkboxes are named by their criterion text, with completion attribution included (01M3MMVPRGDF20NF81BRYJSYCA)', async ({ page }) => {
    await page.goto(serverUrl);
    // Boot auto-selects the first seeded project (A) — the fixture card is there.
    const drawer = await openTaskDrawer(page, AC_TASK_TITLE);

    // Open criterion: its accessible name is EXACTLY the criterion text — no
    // "Mark done:" prefix (the old aria-label override) and no attribution yet.
    // getByRole's `name` is substring/case-insensitive by default; exact:true
    // pins a full-match, so the two assertions together prove strict equality.
    const undoneBox = drawer.locator('label', { hasText: AC_UNDID }).locator('input[type="checkbox"]');
    await expect(undoneBox).toBeVisible();
    await expect(undoneBox).not.toBeChecked();
    await expect(drawer.getByRole('checkbox', { name: AC_UNDID, exact: true })).toHaveCount(1);
    await expect(drawer.getByRole('checkbox', { name: /mark done/i })).toHaveCount(0);

    // Pre-completed criterion (seeded via REST): checked; its name is NOT the
    // bare text anymore (attribution appended) but still contains it, plus the
    // completion attribution. actorLabel renders agents as "<name> (agent)".
    const doneBox = drawer.locator('label', { hasText: AC_DONE }).locator('input[type="checkbox"]');
    await expect(doneBox).toBeChecked();
    await expect(drawer.getByRole('checkbox', { name: AC_DONE, exact: true })).toHaveCount(0);
    await expect(drawer.getByRole('checkbox', { name: AC_DONE })).toHaveCount(1);
    await expect(drawer.getByRole('checkbox', { name: `${ACTOR.name} (agent)` })).toHaveCount(1);

    // Toggling the open criterion on via the UI: it loses its exact text-only
    // name, still contains the text, and gains a completion-attribution line.
    // (The attribution records the browser client's actor identity — not the
    // REST seed actor — so we assert the "… · just now" line structurally.)
    await undoneBox.click();
    await expect(undoneBox).toBeChecked();
    await expect(drawer.getByRole('checkbox', { name: AC_UNDID, exact: true })).toHaveCount(0);
    await expect(drawer.getByRole('checkbox', { name: AC_UNDID })).toHaveCount(1);
    await expect(drawer.locator('label', { hasText: AC_UNDID }).getByText(/·\s+just now$/)).toBeVisible();

    // Un-checking reverts to the plain text-only name.
    await undoneBox.click();
    await expect(undoneBox).not.toBeChecked();
    await expect(drawer.getByRole('checkbox', { name: AC_UNDID, exact: true })).toHaveCount(1);
    await expect(drawer.getByRole('checkbox', { name: `${ACTOR.name} (agent)` })).toHaveCount(1);
  });

  // ------------------------------------------------------------------ Task B

  test('document.title follows project switch and task open/close, reverting on close (01M3MMVV92639YJ5822824NTV1)', async ({ page }) => {
    // Boot: static branding title (no user interaction yet).
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');

    // Explicit project switch → "<project> – Rävn".
    await selectProjectViaDropdown(page, projectB);
    await expect(page).toHaveTitle(`${PROJECT_B} – Rävn`);

    // Opening a task detail (drawer) → "<task> – <project> – Rävn".
    await openTaskDrawer(page, TITLE_TASK_TITLE);
    await expect(page).toHaveTitle(`${TITLE_TASK_TITLE} – ${PROJECT_B} – Rävn`);

    // Closing the drawer reverts to the board title for the active project.
    await page.keyboard.press('Escape');
    await expect(page.locator('aside')).toBeHidden();
    await expect(page).toHaveTitle(`${PROJECT_B} – Rävn`);

    // Switching back switches the title again (both directions covered).
    await selectProjectViaDropdown(page, projectA);
    await expect(page).toHaveTitle(`${PROJECT_A} – Rävn`);
  });

  // ------------------------------------------------------------------ Task C

  test('collapsible toggles expose aria-expanded (and the card menu aria-haspopup) (01M3MMW18VTX81KQM2GRHM1ZTW)', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projectB);

    // --- "New project" (desktop: the trailing item of the Projects menu) ----
    // openNewProject() is open-only (no toggle): the bar closes via its Cancel.
    const trigger = page.getByTestId('project-select');
    await expect(trigger).toBeEnabled();
    await trigger.click();
    const newItem = page.getByTestId('new-project-item');
    await expect(newItem).toHaveAttribute('aria-expanded', 'false');
    await newItem.click();
    await expect(newProjectBar(page).locator('#cb-new-project-name-input')).toBeVisible();
    // Re-open the menu to read the item's updated expanded state.
    await trigger.click();
    await expect(newItem).toHaveAttribute('aria-expanded', 'true');
    await page.keyboard.press('Escape'); // close the menu (does not affect the bar)
    await newProjectBar(page).getByRole('button', { name: 'Cancel' }).click();
    await expect(newProjectBar(page).locator('#cb-new-project-name-input')).toBeHidden();
    await trigger.click();
    await expect(newItem).toHaveAttribute('aria-expanded', 'false');

    // --- Card ⋮ move menu: aria-haspopup + aria-expanded -------------------
    const card = page.locator('article', { hasText: TITLE_TASK_TITLE });
    const menuToggle = card.locator('[data-testid="card-menu-toggle"]');
    await expect(menuToggle).toHaveAttribute('aria-haspopup', 'true');
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'false');
    await menuToggle.click();
    await expect(card.locator('[data-testid="card-move-menu"]')).toBeVisible();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'true');
    await page.keyboard.press('Escape');
    await expect(card.locator('[data-testid="card-move-menu"]')).toBeHidden();
    await expect(menuToggle).toHaveAttribute('aria-expanded', 'false');

    // --- "+ Attach artifact" in the drawer ---------------------------------
    const drawer = await openTaskDrawer(page, TITLE_TASK_TITLE);
    const attachToggle = drawer.locator('[data-testid="artifact-attach-toggle"]');
    await expect(attachToggle).toHaveAttribute('aria-expanded', 'false');
    await attachToggle.click();
    await expect(page.locator('#cb-artifact-ref-input')).toBeVisible();
    await expect(attachToggle).toHaveAttribute('aria-expanded', 'true');
    // The form's own Cancel closes it (the toggle relabels to "Cancel" too, so
    // scope strictly to the form).
    const form = page.locator('form', { has: page.locator('#cb-artifact-ref-input') });
    await form.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.locator('#cb-artifact-ref-input')).toBeHidden();
    await expect(attachToggle).toHaveAttribute('aria-expanded', 'false');
  });

  test('"New project" in the mobile slide-down panel exposes aria-expanded (01M3MMW18VTX81KQM2GRHM1ZTW)', async ({ browser }) => {
    const context = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const page = await context.newPage();
    await page.goto(serverUrl);

    // Mobile board menu (hamburger) hosts the "New project" action button. The
    // panel does not auto-close on action clicks, so state is read in place.
    await page.getByRole('button', { name: 'Board menu' }).click();
    const panel = page.getByTestId('mobile-menu-panel');
    await expect(panel).toBeVisible();
    const btn = panel.locator('[data-testid="mobile-new-project-btn"]');
    await expect(btn).toHaveAttribute('aria-expanded', 'false');

    await btn.click();
    // The new-project bar renders under the header even at narrow widths.
    await expect(newProjectBar(page).locator('#cb-new-project-name-input')).toBeVisible();
    await expect(btn).toHaveAttribute('aria-expanded', 'true');

    // Cancel closes the bar and clears the expanded state (panel stays open).
    await newProjectBar(page).getByRole('button', { name: 'Cancel' }).click();
    await expect(newProjectBar(page).locator('#cb-new-project-name-input')).toBeHidden();
    await expect(btn).toHaveAttribute('aria-expanded', 'false');

    await context.close();
  });

  test('every decorative SVG is aria-hidden; informative ones keep role="img" + aria-label (01M3MMW18VTX81KQM2GRHM1ZTW)', async ({ page }) => {
    await page.goto(serverUrl);
    // Project A's task carries acceptance criteria, so its drawer also renders
    // the ONE allowed informative SVG (the AC progress ring).
    await selectProjectViaDropdown(page, projectA);
    const drawer = await openTaskDrawer(page, AC_TASK_TITLE);

    const violations = await page.evaluate(() => {
      const bad: string[] = [];
      for (const svg of Array.from(document.querySelectorAll('svg'))) {
        const hidden = svg.getAttribute('aria-hidden') === 'true';
        const informative =
          svg.getAttribute('role') === 'img' && !!svg.getAttribute('aria-label');
        if (!hidden && !informative) {
          bad.push((svg as SVGElement).outerHTML.slice(0, 120));
        }
      }
      return bad;
    });
    expect(violations, `SVGs missing aria-hidden/role=img: ${violations.join(' | ')}`).toEqual([]);

    // Sanity: the sweep is not vacuous — the page actually renders SVG icons.
    const svgCount = await page.locator('svg').count();
    expect(svgCount).toBeGreaterThan(10);

    // The AC progress ring stays INFORMATIVE (the one allowed exception): it
    // must keep role="img" with its label and must NOT be aria-hidden.
    const ring = drawer.locator('svg[role="img"]').first();
    await expect(ring).toBeVisible();
    await expect(ring).not.toHaveAttribute('aria-hidden', 'true');
    const ringLabel = await ring.getAttribute('aria-label');
    expect(ringLabel ?? '').toMatch(/\d/); // "n of m … criteria complete"
  });
});
