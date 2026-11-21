import { randomUUID } from 'crypto';
import { AxeBuilder } from '@axe-core/playwright';
import { test, expect, type Page } from './fixtures';
import { selectProjectViaDropdown } from './helpers';

const SEED_ACTOR = { name: 'e2e-overview-seed', type: 'agent' as const };

async function apiJson(url: string, method: string, body?: unknown, token?: string): Promise<any> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${url} failed (${res.status}): ${await res.text()}`);
  }
  return res.status === 204 ? undefined : res.json();
}

test.describe('Cross-project Progress Overview (/projects) (01M7XY9TGKCSV75PR672W21HEJ)', () => {
  let serverUrl = '';
  const prefix = randomUUID().slice(0, 8);
  let projAlpha: { id: string; name: string };
  let projBeta: { id: string; name: string };
  let projGamma: { id: string; name: string };
  let projArchived: { id: string; name: string };
  let uncaughtErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;

    // 1. Alpha: 1 todo, 2 doing (1 active claim + 1 stalled via expired claim), 1 done => 25% completion, 3 open work
    projAlpha = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `${prefix}-Alpha Core`,
      description: 'Gateway platform with active and stalled workstreams',
      actor: SEED_ACTOR,
    });
    // 2. Beta: 0 todo, 0 doing, 2 done => 100% completion, 0 open work, Healthy
    projBeta = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `${prefix}-Beta Docs`,
      description: 'Release handbook and documentation',
      actor: SEED_ACTOR,
    });
    // 3. Gamma: 2 todo (1 needs_attention question comment), 0 doing, 0 done => 0% completion, 2 open work
    projGamma = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `${prefix}-Gamma Search`,
      description: 'Index tuning for Gateway queries',
      actor: SEED_ACTOR,
    });
    // 4. Archived project
    projArchived = await apiJson(`${serverUrl}/api/v1/projects`, 'POST', {
      name: `${prefix}-Archived Legacy`,
      description: 'Retired service archive',
      actor: SEED_ACTOR,
    });
    await apiJson(`${serverUrl}/api/v1/projects/${projArchived.id}`, 'PUT', {
      actor: SEED_ACTOR,
      archived: true,
    });

    // Alpha tasks
    const aTodo = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projAlpha.id,
      title: 'Alpha Todo Task',
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${aTodo.id}/comments`, 'POST', {
      actor: SEED_ACTOR,
      kind: 'progress',
      body: 'Queued',
    });

    const aActive = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projAlpha.id,
      title: 'Alpha Active Claimed',
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${aActive.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: aActive.version,
      status: 'doing',
    });
    await apiJson(`${serverUrl}/api/v1/tasks/${aActive.id}/claim`, 'POST', {
      actor: { name: 'worker-1', type: 'agent' },
      ttl_sec: 300,
    });

    const aStalled = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projAlpha.id,
      title: 'Alpha Stalled Claim',
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${aStalled.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: aStalled.version,
      status: 'doing',
    });
    const stalledClaimed = await apiJson(`${serverUrl}/api/v1/tasks/${aStalled.id}/claim`, 'POST', {
      actor: { name: 'worker-stale', type: 'agent' },
      ttl_sec: 1,
    });

    const aDone = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projAlpha.id,
      title: 'Alpha Done Task',
      actor: SEED_ACTOR,
    })).task;
    const aDoneDoing = await apiJson(`${serverUrl}/api/v1/tasks/${aDone.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: aDone.version,
      status: 'doing',
    });
    await apiJson(`${serverUrl}/api/v1/tasks/${aDone.id}`, 'PATCH', {
      actor: SEED_ACTOR,
      version: aDoneDoing.version,
      status: 'done',
    });

    // Advance to the next whole UTC second so Beta's last_activity_at > Alpha's
    const alphaSec = Math.floor(Date.now() / 1000);
    while (Math.floor(Date.now() / 1000) <= alphaSec) {
      await new Promise((r) => setTimeout(r, 100));
    }

    // Beta tasks (2 done -> 100%)
    for (const title of ['Beta Done 1', 'Beta Done 2']) {
      const t = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
        project_id: projBeta.id,
        title,
        actor: SEED_ACTOR,
      })).task;
      const doing = await apiJson(`${serverUrl}/api/v1/tasks/${t.id}`, 'PATCH', {
        actor: SEED_ACTOR,
        version: t.version,
        status: 'doing',
      });
      await apiJson(`${serverUrl}/api/v1/tasks/${t.id}`, 'PATCH', {
        actor: SEED_ACTOR,
        version: doing.version,
        status: 'done',
      });
    }

    // Advance to the next whole UTC second so Gamma's last_activity_at > Beta's
    const betaSec = Math.floor(Date.now() / 1000);
    while (Math.floor(Date.now() / 1000) <= betaSec) {
      await new Promise((r) => setTimeout(r, 100));
    }

    // Gamma tasks (2 todo, 1 with question comment -> needs_attention = 1; created last -> most recent)
    const g1 = (await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projGamma.id,
      title: 'Gamma Needs Answer',
      actor: SEED_ACTOR,
    })).task;
    await apiJson(`${serverUrl}/api/v1/tasks/${g1.id}/comments`, 'POST', {
      actor: { name: 'worker-2', type: 'agent' },
      kind: 'question',
      body: 'Which schema version should we target?',
    });
    await apiJson(`${serverUrl}/api/v1/tasks`, 'POST', {
      project_id: projGamma.id,
      title: 'Gamma Second Todo',
      actor: SEED_ACTOR,
    });

    // Ensure the 1s claim lease has elapsed so Alpha reports stalled = 1 cleanly
    const expiresMs = Date.parse(stalledClaimed.claim_expires_at);
    const waitMs = Number.isNaN(expiresMs) ? 1100 : Math.max(0, expiresMs - Date.now() + 150);
    if (waitMs > 0 && waitMs < 3000) {
      await new Promise((r) => setTimeout(r, waitMs));
    }
  });

  test.beforeEach(async ({ page }) => {
    uncaughtErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtErrors.push(`[console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => {
      uncaughtErrors.push(`[pageerror] ${err.message}`);
    });
  });

  test.afterEach(() => {
    expect(uncaughtErrors).toEqual([]);
  });

  test('AC1/AC2/AC4: deep-link /projects fires a single overview request (zero N+1 /report calls) and renders cards, progress, counts & health', async ({ page }) => {
    const overviewUrls: string[] = [];
    const reportUrls: string[] = [];
    page.on('request', (req) => {
      const url = req.url();
      if (url.includes('/api/v1/projects/overview')) overviewUrls.push(url);
      if (/\/api\/v1\/projects\/[^/]+\/report/.test(url)) reportUrls.push(url);
    });

    await page.goto(`${serverUrl}/projects`);
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    await expect(page.getByTestId('projects-overview-count')).toContainText('project');

    const alphaCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projAlpha.id}"]`);
    const betaCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projBeta.id}"]`);
    const gammaCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projGamma.id}"]`);

    await expect(alphaCard).toBeVisible();
    await expect(betaCard).toBeVisible();
    await expect(gammaCard).toBeVisible();

    // Zero N+1 /report calls and exactly one /api/v1/projects/overview call
    expect(overviewUrls.length).toBe(1);
    expect(reportUrls).toEqual([]);

    // Alpha card content: name, description, progress %, counts, active claims, stalled
    await expect(alphaCard).toContainText(projAlpha.name);
    await expect(alphaCard).toContainText('Gateway platform with active and stalled workstreams');
    const alphaBar = alphaCard.getByRole('progressbar');
    await expect(alphaBar).toHaveAttribute('aria-valuenow', '25');
    await expect(alphaCard).toContainText('25%');
    await expect(alphaCard.getByTestId('project-card-counts')).toContainText('1 todo');
    await expect(alphaCard.getByTestId('project-card-counts')).toContainText('2 doing');
    await expect(alphaCard.getByTestId('project-card-counts')).toContainText('1 done');
    await expect(alphaCard.getByTestId('project-card-health')).toContainText('1 active claim');
    await expect(alphaCard.getByTestId('project-card-health')).toContainText('1 stalled');

    // Beta card: 100% completion, Healthy pill
    await expect(betaCard.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100');
    await expect(betaCard).toContainText('100%');
    await expect(betaCard.getByTestId('project-card-health')).toContainText('Healthy');

    // Gamma card: needs_attention badge
    await expect(gammaCard.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0');
    await expect(gammaCard.getByTestId('project-card-health')).toContainText('1 needs attention');

    // Entry via nav-projects-btn and brand-logo-btn
    await page.getByTestId('nav-board-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    await page.getByTestId('nav-projects-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();

    await page.getByTestId('nav-board-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    await page.getByTestId('brand-logo-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
  });

  test('AC3/AC4: card Board & Dashboard buttons navigate directly into that project context', async ({ page }) => {
    await page.goto(serverUrl);
    await selectProjectViaDropdown(page, projAlpha.id);

    await page.getByTestId('nav-projects-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();

    // Click Dashboard on Gamma card -> opens /projects/<gamma>/dashboard with Gamma's data
    const gammaCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projGamma.id}"]`);
    await gammaCard.getByTestId('project-card-dashboard-btn').click();

    await expect(page.getByTestId('dashboard-view')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/projects/${projGamma.id}/dashboard$`));
    await expect(page.getByTestId('dashboard-total-count')).toHaveText('2 tasks');

    // Back to overview, click Board on Beta card -> opens Board (/) for Beta
    await page.getByTestId('nav-projects-btn').click();
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();

    const betaCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projBeta.id}"]`);
    await betaCard.getByTestId('project-card-board-btn').click();

    await expect(page.getByTestId('projects-overview-view')).toBeHidden();
    expect(new URL(page.url()).pathname).toBe('/');
    await expect(page.locator('article', { hasText: 'Beta Done 1' })).toBeVisible();
  });

  test('AC4: search filter, active vs archived toggle, and sort order (recent, open_work, name, completion)', async ({ page }) => {
    await page.goto(`${serverUrl}/projects`);
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();

    const search = page.getByTestId('projects-overview-search');
    const sort = page.getByTestId('projects-overview-sort');
    const archivedToggle = page.getByTestId('projects-overview-archived-toggle');

    // Scope down to our 3 seeded active projects via prefix search
    await search.fill(prefix);
    const cards = page.getByTestId('project-overview-card');
    await expect(cards).toHaveCount(3);

    // 1. Sort by open_work: Alpha (3 open) -> Gamma (2 open) -> Beta (0 open)
    await sort.selectOption('open_work');
    await expect
      .poll(async () => cards.evaluateAll((els) => els.map((e) => e.getAttribute('data-project-id'))))
      .toEqual([projAlpha.id, projGamma.id, projBeta.id]);

    // 2. Sort by name: Alpha -> Beta -> Gamma
    await sort.selectOption('name');
    await expect
      .poll(async () => cards.evaluateAll((els) => els.map((e) => e.getAttribute('data-project-id'))))
      .toEqual([projAlpha.id, projBeta.id, projGamma.id]);

    // 3. Sort by completion: Beta (100%) -> Alpha (25%) -> Gamma (0%)
    await sort.selectOption('completion');
    await expect
      .poll(async () => cards.evaluateAll((els) => els.map((e) => e.getAttribute('data-project-id'))))
      .toEqual([projBeta.id, projAlpha.id, projGamma.id]);

    // 4. Sort by recent: Gamma was updated last -> Beta -> Alpha
    await sort.selectOption('recent');
    await expect
      .poll(async () => cards.evaluateAll((els) => els.map((e) => e.getAttribute('data-project-id'))))
      .toEqual([projGamma.id, projBeta.id, projAlpha.id]);

    // 5. Description substring search ("Gateway" matches Alpha + Gamma, excludes Beta)
    await search.fill('Gateway');
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${projAlpha.id}"]`)).toBeVisible();
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${projGamma.id}"]`)).toBeVisible();
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${projBeta.id}"]`)).toHaveCount(0);

    // 6. Empty search match state
    await search.fill(`no-match-${prefix}`);
    await expect(page.getByTestId('projects-overview-empty')).toBeVisible();
    await expect(page.getByTestId('projects-overview-empty')).toContainText('No matching projects');

    // 7. Archived toggle shows archived project
    await search.fill(prefix);
    await archivedToggle.click();
    await expect(archivedToggle).toHaveAttribute('aria-checked', 'true');
    const archivedCard = page.locator(`[data-testid="project-overview-card"][data-project-id="${projArchived.id}"]`);
    await expect(archivedCard).toBeVisible();
    await expect(archivedCard).toContainText('Archived');
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${projAlpha.id}"]`)).toHaveCount(0);

    // Toggle back to Active
    await archivedToggle.click();
    await expect(archivedToggle).toHaveAttribute('aria-checked', 'false');
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${projAlpha.id}"]`)).toBeVisible();
  });

  test('AC5: WCAG 2.1 AA audit in light & dark mode + 375px mobile tap targets & zero overflow', async ({ browser }) => {
    // 1. Desktop light & dark mode axe-core WCAG 2.1 AA audit (reducedMotion avoids mid-transition color reads)
    const desktopCtx = await browser.newContext({
      viewport: { width: 1280, height: 800 },
      reducedMotion: 'reduce',
    });
    const page = await desktopCtx.newPage();
    page.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtErrors.push(`[wcag console.error] ${msg.text()}`);
    });
    page.on('pageerror', (err) => {
      uncaughtErrors.push(`[wcag pageerror] ${err.message}`);
    });

    await page.goto(`${serverUrl}/projects`);
    await page.getByTestId('projects-overview-search').fill(prefix);
    await expect(page.getByTestId('project-overview-card')).toHaveCount(3);

    const lightAudit = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    expect(lightAudit.violations, `Light mode WCAG violations: ${JSON.stringify(lightAudit.violations)}`).toEqual([]);

    // Switch to dark mode
    await page.getByTestId('header-menu-trigger').click();
    await page.getByTestId('theme-dark').click();
    await page.keyboard.press('Escape');
    await expect(page.locator('html')).toHaveClass(/dark/);

    const darkAudit = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    expect(darkAudit.violations, `Dark mode WCAG violations: ${JSON.stringify(darkAudit.violations)}`).toEqual([]);
    await desktopCtx.close();

    // 2. Mobile 375px viewport: zero horizontal overflow & >= 44x44px tap targets
    const mobileCtx = await browser.newContext({ viewport: { width: 375, height: 812 }, hasTouch: true });
    const mPage = await mobileCtx.newPage();
    mPage.on('console', (msg) => {
      if (msg.type() === 'error') uncaughtErrors.push(`[mobile console.error] ${msg.text()}`);
    });
    mPage.on('pageerror', (err) => {
      uncaughtErrors.push(`[mobile pageerror] ${err.message}`);
    });

    await mPage.goto(`${serverUrl}/projects`);
    await mPage.getByTestId('projects-overview-search').fill(prefix);
    await expect(mPage.getByTestId('project-overview-card')).toHaveCount(3);

    expect(await mPage.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

    const alphaCard = mPage.locator(`[data-testid="project-overview-card"][data-project-id="${projAlpha.id}"]`);
    const controls = [
      ['projects-overview-refresh', mPage.getByTestId('projects-overview-refresh')],
      ['projects-overview-new-btn', mPage.getByTestId('projects-overview-new-btn')],
      ['projects-overview-search', mPage.getByTestId('projects-overview-search')],
      ['projects-overview-archived-toggle', mPage.getByTestId('projects-overview-archived-toggle')],
      ['projects-overview-sort', mPage.getByTestId('projects-overview-sort')],
      ['project-card-board-btn', alphaCard.getByTestId('project-card-board-btn')],
      ['project-card-dashboard-btn', alphaCard.getByTestId('project-card-dashboard-btn')],
    ] as const;

    for (const [name, loc] of controls) {
      await expect(loc).toBeVisible();
      const box = await loc.boundingBox();
      expect(box, `${name} has boundingBox`).not.toBeNull();
      expect(box!.height, `${name} height >= 44px`).toBeGreaterThanOrEqual(44);
      expect(box!.width, `${name} width >= 44px`).toBeGreaterThanOrEqual(44);
    }

    await mobileCtx.close();
  });
});

// ---------------------------------------------------------------------------
// Membership isolation on /projects (dedicated isolatedServer)
// ---------------------------------------------------------------------------
test.describe('Projects Overview membership isolation (RAVN_SCOPING=membership)', () => {
  const SHARED_TOKEN = 'e2e-overview-scoping-operator-token';
  test.use({
    isolatedServerEnv: {
      RAVN_AUTH: 'multiuser',
      RAVN_TOKEN: SHARED_TOKEN,
      RAVN_SCOPING: 'membership',
    },
  });

  async function loginViaUI(page: Page, base: string, username: string): Promise<void> {
    await page.goto(`${base}/projects`);
    const authModal = page.locator('#cb-auth-modal');
    await expect(authModal).toBeVisible();
    await page.getByTestId('auth-tab-login').click();
    await page.getByTestId('login-username').fill(username);
    await page.getByTestId('login-password').fill(`${username}-password-1234`);
    await page.getByTestId('login-submit').click();
    await expect(authModal).toBeHidden();
  }

  test('AC5: non-member cannot see foreign project or its metrics on /projects', async ({ page, isolatedServer }) => {
    const base = isolatedServer.url;
    const errors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        if (msg.text().includes('status of 401')) return;
        errors.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => errors.push(`[pageerror] ${err.message}`));

    for (const name of ['alice', 'bob']) {
      await apiJson(
        `${base}/api/v1/auth/accounts`,
        'POST',
        { name, password: `${name}-password-1234` },
        SHARED_TOKEN,
      );
    }

    const tokAlice = (await apiJson(`${base}/api/v1/auth/login`, 'POST', {
      username: 'alice',
      password: 'alice-password-1234',
    })).token as string;
    const tokBob = (await apiJson(`${base}/api/v1/auth/login`, 'POST', {
      username: 'bob',
      password: 'bob-password-1234',
    })).token as string;

    const aliceProj = await apiJson(
      `${base}/api/v1/projects`,
      'POST',
      { name: 'Alice Secret Project', description: 'Private secret metrics', actor: { name: 'alice', type: 'human' } },
      tokAlice,
    );
    await apiJson(
      `${base}/api/v1/tasks`,
      'POST',
      { project_id: aliceProj.id, title: 'Alice Secret Task', actor: { name: 'alice', type: 'human' } },
      tokAlice,
    );

    const bobProj = await apiJson(
      `${base}/api/v1/projects`,
      'POST',
      { name: 'Bob Visible Project', description: 'Bob own workspace', actor: { name: 'bob', type: 'human' } },
      tokBob,
    );
    await apiJson(
      `${base}/api/v1/tasks`,
      'POST',
      { project_id: bobProj.id, title: 'Bob Task', actor: { name: 'bob', type: 'human' } },
      tokBob,
    );

    // Log in as bob on /projects -> sees ONLY Bob Visible Project
    await loginViaUI(page, base, 'bob');
    await expect(page.getByTestId('projects-overview-view')).toBeVisible();
    await expect(page.getByTestId('projects-overview-count')).toHaveText('1 project');
    await expect(page.getByTestId('project-overview-card')).toHaveCount(1);
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${bobProj.id}"]`)).toBeVisible();
    await expect(page.locator(`[data-testid="project-overview-card"][data-project-id="${aliceProj.id}"]`)).toHaveCount(0);
    await expect(page.getByTestId('projects-overview-view')).not.toContainText('Alice Secret Project');

    expect(errors).toEqual([]);
  });
});
