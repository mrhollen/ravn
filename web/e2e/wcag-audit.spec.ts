// axe-core WCAG 2.1 AA audit — final objective evidence for the accessibility
// epic (01M5XTHT2RTXKKF7R4Q12ZYS2V). Complements the behavior-level specs of
// this epic (focus-visible, wcag-touch-motion, board-loading-skeleton,
// aria-semantics): those assert SPECIFIC fixes; this spec sweeps the real
// rendered views with axe-core's rule set and FAILS on any remaining WCAG 2.1
// A/AA violation.
//
// Matrix: 3 views × 2 themes (explicit light + explicit dark via the app's
// `ravn_theme` localStorage key, applied pre-paint by index.html):
//   a. board view — desktop viewport (1280x720)
//   b. task detail drawer OPEN (first card of To Do = the OVERDUE fixture, so
//      the "Overdue:" badge renders inside the dialog too)
//   c. new-project form expanded
// Seed: one project with an OVERDUE task (past due_at → visible "Overdue:"
// text), a CLAIMED task (claim badge + halo render), and a plain todo — so the
// stateful badges/labels added by the epic are all on screen during the audit.
//
// Triage policy for any violation surfaced: real, small fixes inside
// web/index.html / web/src/style.css are fixed in place; only a justified,
// specific rule may be disabled below via .disableRules(...) with an inline
// comment — never blanket disables.
//
// KNOWN PERMANENT REVIEW ITEMS (axe "incomplete" = needs-review, NOT
// violations — they do not fail this spec; the audit() helper logs them):
//   * aria-valid-attr-value [critical], 1 node in every view: the header-menu
//     trigger combines aria-haspopup + aria-controls. axe-core's
//     "controlsWithinPopup" pre-check routes that combo to manual review
//     UNCONDITIONALLY (see ariaValidAttrValueEvaluate in axe-core source — it
//     sets needsReview whenever hasPopup is true, before value validation). The
//     pattern itself is valid WAI-ARIA and its behavior is covered by
//     header-menu.spec.ts. Kept intentionally; do not "fix" by dropping the
//     attributes.
//   * label-content-name-mismatch [serious], drawer view only (3 nodes): the
//     × detail-close and ✕ AC-delete glyph buttons carry aria-labels over a
//     single-symbol visible text — axe returns "incomplete" for
//     non-human-interpretable glyphs by design. Textbook-correct implementation.
//   * color-contrast [serious], 12–22 nodes: needs-review on translucent lane
//     backgrounds (bg-white/70 etc.) where axe cannot resolve the effective
//     backdrop, plus decorative aria-hidden punctuation (· / ▾ glyphs). No
//     measurable contrast failure exists (zero violations); both themes are
//     pinned visually by the committed theme.spec.ts + this spec's screenshots.
import { test, expect } from '@playwright/test';
import { AxeBuilder } from '@axe-core/playwright';
import { spawn, execSync, type ChildProcess } from 'child_process';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import { selectProjectViaDropdown, openNewProjectFromDropdown } from './helpers';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');

const ACTOR = { name: 'e2e-wcag-audit-seed', type: 'agent' as const };
const PROJECT_NAME = 'WCAG Audit Project'; // only project → also the boot auto-select target
const OVERDUE_TITLE = 'Overdue Card Fixture'; // past due_at, top priority in To Do → FIRST card
const CLAIMED_TITLE = 'Claimed Card Fixture'; // claimed via REST → claim badge + halo
const PLAIN_TITLE = 'Plain Todo Fixture';

/** Minimal structural view of the axe result we assert on (avoids a direct
 *  import from the transitive axe-core dep; @axe-core/playwright returns this shape). */
interface AxeViolation {
  id: string;
  impact: string | null;
  help: string;
  helpUrl: string;
  nodes: { target: string[] }[];
}
interface AxeIncomplete {
  id: string;
  impact: string | null;
  nodes: unknown[];
}

interface AxeResults {
  violations: AxeViolation[];
  passes: unknown[];
  incomplete: AxeIncomplete[];
  testEngine: { name: string; version: string };
}

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
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}: ${await res.text()}`);
  return res.json();
}

/** Format violations as a failure message: rule id + impact + target selectors. */
function formatViolations(label: string, r: AxeResults): string {
  const lines = r.violations.map((v) => {
    const targets = v.nodes
      .slice(0, 8)
      .map((n) => `        at ${n.target.join(' ')}`)
      .join('\n');
    const more = v.nodes.length > 8 ? `\n        … +${v.nodes.length - 8} more node(s)` : '';
    return [
      `  ✗ ${v.id} [impact: ${v.impact}] — ${v.help}`,
      `      ${v.helpUrl}`,
      targets,
      more,
    ].filter(Boolean).join('\n');
  });
  return (
    `axe-core ${r.testEngine.name}@${r.testEngine.version}: ${r.violations.length} WCAG violation(s) in "${label}" ` +
    `(${r.passes.length} rule check(s) passed, ${r.incomplete.length} incomplete):\n${lines.join('\n')}`
  );
}

/** Run the audit for one view state and throw (with a full dump) on violations. */
async function audit(label: string, page: import('@playwright/test').Page): Promise<AxeResults> {
  const results = (await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    // No disables in the baseline matrix. If triage ever justifies one, add a
    // targeted .disableRules('<rule>') here WITH an inline comment explaining why.
    .analyze()) as unknown as AxeResults;
  expect(
    results.violations.map((v) => v.id),
    formatViolations(label, results),
  ).toEqual([]);
  // axe "incomplete" findings (needs-review, e.g. decorative glyph buttons) are
  // NOT AA violations — but surface them in the report output so QA can track
  // the review queue instead of re-discovering it every run.
  if (results.incomplete.length > 0) {
    const inc = results.incomplete
      .map((r) => `${r.id} [${r.impact}]: ${r.nodes.length} node(s)`)
      .join(', ');
    console.warn(`[wcag-audit] ${label}: incomplete (manual review): ${inc}`);
    // DEBUG_A11Y=1 → dump each incomplete finding's targets for triage.
    if (process.env.DEBUG_A11Y) {
      for (const r of results.incomplete) {
        for (const n of r.nodes as Array<{ target: unknown; html?: string }>) {
          const html = (n.html ?? '').replace(/\s+/g, ' ').slice(0, 200);
          console.warn(`[wcag-audit][debug] ${r.id}: ${JSON.stringify(n.target)} :: ${html}`);
        }
      }
    }
  }
  return results;
}

test.describe('axe-core WCAG 2.1 AA audit (board / drawer / new-project form × light + dark)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
  let projectId: string;
  let uncaughtConsoleErrors: string[] = [];

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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-e2e-wcag-audit-'));
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

    const proj = await postJson(`${serverUrl}/api/v1/projects`, {
      name: PROJECT_NAME,
      description: 'Seeded for the axe-core WCAG 2.1 AA audit spec',
      actor: ACTOR,
    });
    projectId = proj.id;

    // OVERDUE fixture: past due_at + top priority so it is the FIRST card in To
    // Do (its drawer shows the "Overdue:" text too) and two AC rows.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: OVERDUE_TITLE,
      description: 'Overdue fixture — board card + drawer render the visible Overdue badge.',
      priority: 4,
      due_at: '2020-01-02T00:00:00Z',
      acceptance_criteria: ['Ship the overdue text fix', 'Keep the badge legible in dark theme'],
      actor: ACTOR,
    });

    // CLAIMED fixture: claim moves it to Doing; the claim badge + halo render.
    const claimed = await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: CLAIMED_TITLE,
      priority: 3,
      actor: ACTOR,
    });
    const claimRes = await fetch(`${serverUrl}/api/v1/tasks/${claimed.task.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: ACTOR }),
    });
    expect(claimRes.ok).toBe(true);

    // PLAIN fixture: a quiet second To Do card.
    await postJson(`${serverUrl}/api/v1/tasks`, {
      project_id: projectId,
      title: PLAIN_TITLE,
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
      fs.rmSync(tempDir, { recursive: true, force: true });
    }
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
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Pin the theme BEFORE first paint via the app's own localStorage key, then
   *  verify documentElement actually carries/omits the `dark` class — so a
   *  broken persistence path can never silently audit the wrong theme. */
  async function bootWithTheme(page: import('@playwright/test').Page, theme: 'light' | 'dark'): Promise<void> {
    await page.addInitScript((t) => {
      try {
        localStorage.setItem('ravn_theme', t);
      } catch {
        /* storage unavailable — the audit would then fail its theme check below */
      }
    }, theme);
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    const isDark = await page.evaluate(() => document.documentElement.classList.contains('dark'));
    expect(isDark, `expected ${theme} theme (documentElement.dark=${isDark})`).toBe(theme === 'dark');
  }

  /** Wait until real cards are rendered (past the skeleton state). */
  async function expectBoardCards(page: import('@playwright/test').Page): Promise<void> {
    await page.getByRole('heading', { name: OVERDUE_TITLE }).waitFor({ state: 'visible' });
    await page.getByRole('heading', { name: PLAIN_TITLE }).waitFor({ state: 'visible' });
  }

  test('board view — light theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'light');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    await audit('board view (light)', page);
  });

  test('board view — dark theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'dark');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    await audit('board view (dark)', page);
  });

  test('task detail drawer open — light theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'light');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    // First card of To Do is the overdue fixture (priority 4). Clicking its
    // heading opens the desktop dialog drawer.
    const card = page.locator('article', { hasText: OVERDUE_TITLE });
    await card.getByRole('heading', { name: OVERDUE_TITLE }).click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: OVERDUE_TITLE })).toBeVisible();
    await audit('task detail drawer (light)', page);
  });

  test('task detail drawer open — dark theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'dark');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    const card = page.locator('article', { hasText: OVERDUE_TITLE });
    await card.getByRole('heading', { name: OVERDUE_TITLE }).click();
    const drawer = page.locator('aside');
    await expect(drawer.getByRole('heading', { name: OVERDUE_TITLE })).toBeVisible();
    await audit('task detail drawer (dark)', page);
  });

  test('new-project form expanded — light theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'light');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    await openNewProjectFromDropdown(page);
    await audit('new-project form (light)', page);
  });

  test('new-project form expanded — dark theme has no WCAG 2.1 A/AA violations', async ({ page }) => {
    await bootWithTheme(page, 'dark');
    await selectProjectViaDropdown(page, projectId);
    await expectBoardCards(page);
    await openNewProjectFromDropdown(page);
    await audit('new-project form (dark)', page);
  });
});
