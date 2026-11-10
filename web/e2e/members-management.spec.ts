// E2E coverage for T9's membership management UI (phase 2):
//   AC4 — the Members panel lives in the project-settings modal and renders the owner
//         line + stored rows; WRITE controls (add / role select / remove) appear only
//         for a caller who can manage (owner or stored admin), while a read-only member
//         sees the same roster as plain badges with no write affordances.
//   AC5 (UI half) — the owner adds a second user via the panel → that user's session
//         suddenly sees the project; the upsert re-grant changes the role without
//         duplicating the row; removing the user revokes access (scoped list empties,
//         deep link shows the "No access" board banner).
//   Feature-off — a deployment WITHOUT RAVN_SCOPING=membership answers every members
//         verb with 403 reason=membership_scoping_disabled and the panel must not
//         render at all (anonymous / token callers see zero new affordances).
//
// Boot mirrors login-scoping.spec.ts: per-test isolatedServer on a random high port
// with a temp DB serving the pre-built web/dist. The feature-off block switches to a
// plain token-required server via a nested test.use().
import { randomUUID } from 'node:crypto';
import { test, expect } from './fixtures';
import type { Page } from './fixtures';

const SHARED_TOKEN = 'e2e-members-management-operator-token';

// --------------------------------------------------------------------------- API seed helpers (Node fetch)
async function createAccount(base: string, name: string): Promise<void> {
  const res = await fetch(`${base}/api/v1/auth/accounts`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${SHARED_TOKEN}` },
    body: JSON.stringify({ name, password: `${name}-password-1234` }),
  });
  expect(res.status, `create account ${name}`).toBe(201);
}

async function loginApi(base: string, username: string): Promise<string> {
  const res = await fetch(`${base}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: `${username}-password-1234` }),
  });
  expect(res.status, `login ${username}`).toBe(200);
  const out = (await res.json()) as { token: string };
  return out.token;
}

async function createProject(base: string, token: string, name: string): Promise<{ id: string }> {
  const res = await fetch(`${base}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ name, actor: { type: 'human', name: 'seed' } }),
  });
  expect(res.status, `create project ${name}`).toBe(201);
  return res.json();
}

/** Names of the projects a given credential principal can SEE (membership-scoped list). */
async function visibleProjectNames(base: string, token: string): Promise<string[]> {
  const res = await fetch(`${base}/api/v1/projects`, { headers: { Authorization: `Bearer ${token}` } });
  expect(res.status).toBe(200);
  const out = (await res.json()) as { projects: Array<{ name: string }> };
  return out.projects.map((p) => p.name);
}

// --------------------------------------------------------------------------- UI drivers
/** Fresh-context boot 401 opens the auth modal; switch to SIGN IN and log in. */
async function loginViaUI(page: Page, base: string, username: string): Promise<void> {
  await page.goto(base);
  await expect(page).toHaveTitle('Rävn');

  const authModal = page.locator('#cb-auth-modal');
  await expect(authModal).toBeVisible(); // auto-opened by the boot 401 (no stored credential)
  await page.getByTestId('auth-tab-login').click();

  const password = `${username}-password-1234`;
  await page.getByTestId('login-username').fill(username);
  await page.getByTestId('login-password').fill(password);
  // Belt-and-braces: a focus race that misdirects a fill into the wrong field must fail
  // here with a clear diff, not as a 10s "auth modal still visible" timeout after submit.
  await expect(page.getByTestId('login-username')).toHaveValue(username);
  await expect(page.getByTestId('login-password')).toHaveValue(password);
  await page.getByTestId('login-submit').click();
  await expect(authModal).toBeHidden(); // login succeeded and closed the modal
}

/** Attach console/page-error tracking that whitelists the EXPECTED auth/board 4xx
 *  resource errors (the app answers those by design: auth modal / "No access" banner). */
function trackErrors(page: Page, uncaught: string[]): void {
  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      if (msg.text().includes('status of 401')) return; // credential-less boot → auth modal
      if (msg.text().includes('status of 403')) return; // deep-linked project after removal → banner
      uncaught.push(`[console.error] ${msg.text()}`);
    }
  });
  page.on('pageerror', (err) => {
    uncaught.push(`[uncaught pageerror] ${err.message}`);
  });
}

test.describe('Members panel — membership scoping ON (multiuser + operator token)', () => {
  test.use({
    isolatedServerEnv: { RAVN_AUTH: 'multiuser', RAVN_TOKEN: SHARED_TOKEN, RAVN_SCOPING: 'membership' },
  });

  let uncaught: string[] = [];

  test.beforeEach(() => {
    uncaught = [];
  });

  test.afterEach(() => {
    expect(uncaught).toEqual([]); // dedicated server + temp DB are torn down by the fixture
  });

  test('AC5: owner adds a member via the panel → they see the project; upsert re-grants without duplicating; removal revokes access', async ({
    page,
    browser,
    isolatedServer,
  }) => {
    const base = isolatedServer.url;
    trackErrors(page, uncaught);
    const projectName = `Members Room-${randomUUID()}`;

    // --- seed: two accounts + a project alice (owner) created ---
    await createAccount(base, 'alice');
    await createAccount(base, 'bob');
    const tokAlice = await loginApi(base, 'alice');
    const tokBob = await loginApi(base, 'bob');
    const { id: projectId } = await createProject(base, tokAlice, projectName);

    // --- alice (owner) opens the Members panel ---
    await loginViaUI(page, base, 'alice'); // auto-selects her only project
    const gear = page.getByTestId('project-settings-btn');
    await expect(gear).toBeEnabled();
    await gear.click();
    await expect(page.locator('#cb-project-settings')).toBeVisible();

    // AC4: panel renders for the owner — roster section + owner line + write controls.
    const section = page.getByTestId('members-section');
    await expect(section).toBeVisible();
    const ownerRow = page.getByTestId('members-owner-row');
    await expect(ownerRow).toContainText('Owner · alice');
    await expect(page.getByTestId('members-add-form')).toBeVisible(); // she manages

    // --- add bob as editor (fresh row → 201 created:true → "added" toast) ---
    await page.getByTestId('member-add-name').fill('bob');
    await page.getByTestId('member-add-role').selectOption('editor');
    await page.getByTestId('member-add-btn').click();
    const bobRow = page.locator('[data-testid="member-row"][data-member-name="bob"]');
    await expect(bobRow).toBeVisible();
    await expect(page.getByTestId('toast-stack')).toContainText('bob added as editor');
    await expect(page.getByTestId('member-role-select-bob')).toHaveValue('editor');

    // --- bob's session now SEES the project (AC5 add half) and his view is READ-ONLY ---
    const bobCtx = await browser.newContext();
    try {
      const bobPage = await bobCtx.newPage();
      trackErrors(bobPage, uncaught);
      await loginViaUI(bobPage, base, 'bob'); // auto-selects the project he was just added to
      const bobTrigger = bobPage.getByTestId('project-select');
      await expect(bobTrigger).toContainText(projectName);

      // Same panel for bob: roster + owner line visible…
      const bobGear = bobPage.getByTestId('project-settings-btn');
      await bobGear.click();
      await expect(bobPage.locator('#cb-project-settings')).toBeVisible();
      await expect(bobPage.getByTestId('members-section')).toBeVisible();
      await expect(bobPage.getByTestId('members-owner-row')).toContainText('Owner · alice');

      // …but NO write affordances: no add form, no remove button, his role renders as a plain badge.
      await expect(bobPage.getByTestId('members-add-form')).not.toBeVisible();
      // x-show keeps the button in the DOM for non-managers — assert hidden, not absent.
      await expect(bobPage.locator('[data-testid^="member-remove-"]')).toBeHidden();
      await expect(bobPage.getByTestId('member-role-select-bob')).not.toBeVisible();
      // Scope to <span> — the row's hidden role <select> also contains an "editor" option.
      await expect(bobPage.locator('[data-testid="member-row"][data-member-name="bob"]').locator('span').filter({ hasText: /^editor$/ })).toBeVisible();

      // --- alice upserts bob (existing row → 200 created:false: role change, NO new row) ---
      await page.getByTestId('member-add-name').fill('bob');
      await page.getByTestId('member-add-role').selectOption('admin');
      await page.getByTestId('member-add-btn').click();
      await expect(page.getByTestId('toast-stack')).toContainText("bob's role is now admin");
      await expect(bobRow).toHaveCount(1); // still exactly one row for bob
      await expect(page.getByTestId('member-role-select-bob')).toHaveValue('admin');

      // --- alice demotes bob via the row's select (PATCH verb, 200) ---
      await page.getByTestId('member-role-select-bob').selectOption('viewer');
      await expect(page.getByTestId('toast-stack')).toContainText('bob is now viewer');
      await expect(page.getByTestId('member-role-select-bob')).toHaveValue('viewer');

      // --- alice removes bob (DELETE verb, 204) ---
      await page.getByTestId('member-remove-bob').click();
      await expect(page.getByTestId('toast-stack')).toContainText('bob removed');
      await expect(bobRow).toHaveCount(0);

      // --- bob loses access (AC5 remove half): scoped list, fresh boot, deep link ---
      expect(await visibleProjectNames(base, tokBob), 'membership row gone → project no longer listed').not.toContain(projectName);

      await bobPage.reload(); // membership changes emit no SSE — a reload is the user-visible truth
      // project-select is a persistent header button (menu trigger) that stays in the DOM with zero
      // projects — assert it never offers the revoked project, and the empty state is shown.
      await expect(bobPage.getByTestId('project-select')).not.toHaveText(projectName);
      await expect(bobPage.getByText('No projects yet.')).toBeVisible();

      // Stale deep link after revocation: continueBoot validates the selection against the
      // SCOPED list (empty for bob) and falls back — no board fetch, no 403, so no forbidden
      // banner either. The user-visible truth is the empty state with the project nowhere visible.
      await bobPage.goto(`${base}/?project=${projectId}`);
      await expect(bobPage.getByText(projectName)).toHaveCount(0);
      await expect(bobPage.getByText('No projects yet.')).toBeVisible();
    } finally {
      await bobCtx.close();
    }
  });
});

test.describe('Members panel — membership scoping OFF (token mode, default global scoping)', () => {
  test.use({ isolatedServerEnv: { RAVN_TOKEN: SHARED_TOKEN } });

  let uncaught: string[] = [];

  test.beforeEach(() => {
    uncaught = [];
  });

  test.afterEach(() => {
    expect(uncaught).toEqual([]);
  });

  test('panel does not render when the server refuses with membership_scoping_disabled', async ({ page, isolatedServer }) => {
    const base = isolatedServer.url;
    trackErrors(page, uncaught);
    const projectName = `NoMembers Project-${randomUUID()}`;
    const { id } = await createProject(base, SHARED_TOKEN, projectName);

    // Pin the wire contract this UI decision keys off: every verb is refused with the
    // machine-readable reason (NOT a 404 — the routes exist; the capability does not).
    const res = await fetch(`${base}/api/v1/projects/${id}/members`, {
      headers: { Authorization: `Bearer ${SHARED_TOKEN}` },
    });
    expect(res.status).toBe(403);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body.reason).toBe('membership_scoping_disabled');

    // Token-mode boot via ?token=; the single project auto-selects. The settings modal
    // opens and works — but the Members section must be entirely absent from it.
    await page.goto(`${base}/?token=${SHARED_TOKEN}`);
    await expect(page).toHaveTitle('Rävn');
    const gear = page.getByTestId('project-settings-btn');
    await expect(gear).toBeEnabled();
    await gear.click();
    await expect(page.locator('#cb-project-settings')).toBeVisible();

    // x-show="false" keeps the node in the DOM with display:none (axe ignores it) —
    // assert exactly that: present, hidden, and carrying no visible affordances.
    const section = page.getByTestId('members-section');
    await expect(section).toHaveCount(1);
    await expect(section).toBeHidden();
    await expect(page.getByTestId('members-add-form')).not.toBeVisible();
    await expect(page.getByTestId('members-owner-row')).not.toBeVisible();
  });
});
