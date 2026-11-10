// Multi-user epic T7 — session identity in the browser (login / logout / per-actor
// board scoping / attribution), end-to-end against the REAL ravn-server binary.
//
// Boots a DEDICATED (isolatedServer, test-scoped) ravn-server in MULTIUSER + MEMBERSHIP
// mode with a shared operator token, provisions identities through the HTTP API (Node
// fetch), then drives the PRE-BUILT UI through Playwright to prove:
//
//   1. A fresh visitor hits the boot 401, switches the auth modal to its SIGN IN tab,
//      logs in with username/password, and the session identity surfaces in the header
//      (top-bar sign-out) — then signing out clears it and re-locks the app.
//   2. The board "Scope" select round-trips the server-side mine= param: a task alice
//      created shows under "All tasks" and "Created by me" but is HIDDEN under
//      "Assigned to me" (she isn't its assignee), proving per-actor scoping through
//      the real UI — not just the API. The same card also renders its creator chip.
//   3. Membership scoping at the list level: a NON-MEMBER of alice's project never sees
//      it in his (scoped) project list after logging in, while the owner still does.
//      (The dedicated "no access" board banner is defensive: membership hides the id from
//      the list before it can be selected, so it isn't reachable via a plain deep link.)
//
// Mirrors sse-scoping.spec.ts' provisioning pattern and first-visit-autoselect.spec.ts'
// UI-driving pattern; every test opts into the fixtures' test-scoped isolatedServer.
import { test, expect } from './fixtures';
import type { Page } from './fixtures';

const SHARED_TOKEN = 'e2e-login-scoping-operator-token';

// Multiuser membership deployment. RAVN_IDENTITY_OPEN defaults to open (auto), so a
// credential principal may create projects and gets created_by stamped (→ owner/member).
test.use({ isolatedServerEnv: { RAVN_AUTH: 'multiuser', RAVN_TOKEN: SHARED_TOKEN, RAVN_SCOPING: 'membership' } });

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

async function createTask(base: string, token: string, projectId: string, title: string): Promise<void> {
  const res = await fetch(`${base}/api/v1/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ project_id: projectId, title, actor: { type: 'human', name: 'seed' } }),
  });
  expect(res.status, `create task ${title}`).toBe(201);
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

  // A fresh context has no cached session, so the modal defaults to the TOKEN tab; the
  // epic's login path is one click away on the Sign in tab.
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

test.describe('T7 session identity (multiuser + membership)', () => {
  let base = '';
  let uncaught: string[] = [];

  test.beforeEach(async ({ page }) => {
    uncaught = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        // A credential-less first-visit boot fetches /api/v1/projects → 401; Chrome logs
        // that as a resource error. Expected (the app answers by opening the auth modal).
        if (msg.text().includes('status of 401')) return;
        uncaught.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaught.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(async () => {
    expect(uncaught).toEqual([]); // dedicated server + temp DB are torn down by the fixture
  });

  test('login via the UI surfaces the session identity in the header; sign-out clears it', async ({ page, isolatedServer }) => {
    base = isolatedServer.url;
    await createAccount(base, 'alice');
    const tokAlice = await loginApi(base, 'alice'); // API token: seed content + assert identity is hers
    await createProject(base, tokAlice, 'Identity Room');

    await loginViaUI(page, base, 'alice');

    // The acting identity surfaced in the top bar (sign-out is gated on a live session).
    const signOut = page.getByTestId('sign-out-button');
    await expect(signOut).toBeVisible();
    await expect(signOut).toContainText('alice');

    // Signing out drops the credential + identity: the button vanishes and, with no
    // credential left, the app re-locks (the auth modal reopens on the follow-up 401).
    await signOut.click();
    await expect(signOut).not.toBeVisible();
    await expect(page.locator('#cb-auth-modal')).toBeVisible();
  });

  test('board Scope select round-trips server-side mine= through the real UI', async ({ page, isolatedServer }) => {
    base = isolatedServer.url;
    await createAccount(base, 'alice');
    const tokAlice = await loginApi(base, 'alice');
    const project = await createProject(base, tokAlice, 'Scope Room');
    await createTask(base, tokAlice, project.id, 'Alice seeded task');

    await loginViaUI(page, base, 'alice'); // auto-selects her only project; board loads

    const card = page
      .locator('section', { has: page.getByRole('heading', { name: 'To Do' }) })
      .locator('h3', { hasText: 'Alice seeded task' });

    // Default scope is "all": the full live board shows her task, attributed to her.
    await expect(card).toBeVisible();
    await expect(page.getByTestId('card-creator-chip')).toContainText('alice');

    const scope = page.locator('[data-testid="board-scope-select"]');
    await expect(scope).toHaveValue('all');

    // "Assigned to me": she created it but is NOT its assignee → server returns nothing → gone.
    await scope.selectOption({ value: 'assigned' });
    await expect(card).not.toBeVisible();

    // "Created by me": the mine=created query returns it again.
    await scope.selectOption({ value: 'created' });
    await expect(card).toBeVisible();
  });

  test('membership scoping hides a project from a non-member scoped list (not its owner)', async ({ page, isolatedServer }) => {
    base = isolatedServer.url;
    await createAccount(base, 'alice');
    await createAccount(base, 'bob');
    const tokAlice = await loginApi(base, 'alice');
    const tokBob = await loginApi(base, 'bob');

    const project = await createProject(base, tokAlice, 'Secret Room');
    await createTask(base, tokAlice, project.id, 'Hidden work');

    // Server-level ground truth (same scoped list the browser renders): alice sees it, bob doesn't.
    expect(await visibleProjectNames(base, tokAlice), 'owner can see her project').toContain('Secret Room');
    expect(await visibleProjectNames(base, tokBob), 'non-member is filtered out of the list').not.toContain('Secret Room');

    // Prove it THROUGH THE UI: bob logs in and his (membership-scoped) project list has no trace of it.
    await loginViaUI(page, base, 'bob');
    // bob has zero visible projects → the genuine empty state renders, not a foreign board.
    await expect(page.getByText('No projects yet.', { exact: true })).toBeVisible();
    await expect(page.locator('#cb-new-project-name-input')).toBeVisible(); // new-project bar open
    // …and "Secret Room" appears nowhere in his UI (dropdown trigger + any card).
    await expect(page.locator('body', { hasText: 'Secret Room' })).toHaveCount(0);
  });
});

test.describe('T7 "My projects" filter (multiuser + global scoping)', () => {
  // GLOBAL scoping (not membership): every project is visible to everyone, so the
  // ?mine=created filter — NOT per-project visibility — is what isolates a user's own
  // created projects. This proves AC3's "My projects" half end-to-end AND that the
  // unfiltered ("global") list still shows everything (the filter, not visibility, narrows).
  test.use({ isolatedServerEnv: { RAVN_AUTH: 'multiuser', RAVN_TOKEN: SHARED_TOKEN, RAVN_SCOPING: 'global' } });

  let base = '';
  let uncaught: string[] = [];

  test.beforeEach(async ({ page }) => {
    uncaught = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        // A credential-less first-visit boot fetches /api/v1/projects → 401; expected.
        if (msg.text().includes('status of 401')) return;
        uncaught.push(`[console.error] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      uncaught.push(`[uncaught pageerror] ${err.message}`);
    });
  });

  test.afterEach(async () => {
    expect(uncaught).toEqual([]);
  });

  // Names from the projects list for a token, optionally mine-scoped (AC3 ground truth).
  async function projectNames(token: string, mine = false): Promise<string[]> {
    const url = `${base}/api/v1/projects${mine ? '?mine=created' : ''}`;
    const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } });
    expect(res.status).toBe(200);
    const out = (await res.json()) as { projects: Array<{ name: string }> };
    return out.projects.map((p) => p.name);
  }

  test('the "My projects" filter shows only the signed-in user\'s created projects; global shows all', async ({ page, isolatedServer }) => {
    base = isolatedServer.url; // fresh empty DB per test — plain distinct names suffice
    await createAccount(base, 'alice');
    await createAccount(base, 'bob');
    const tokAlice = await loginApi(base, 'alice');
    const tokBob = await loginApi(base, 'bob');
    await createProject(base, tokAlice, 'Alice Global Proj'); // created_by stamped with alice
    await createProject(base, tokBob, 'Bob Global Proj'); // created_by stamped with bob

    // Server ground truth (global mode): unscoped list shows BOTH; mine=created narrows to own.
    expect(await projectNames(tokAlice), 'global: unscoped sees all projects').toEqual(
      expect.arrayContaining(['Alice Global Proj', 'Bob Global Proj']),
    );
    expect(await projectNames(tokAlice, true), 'global: ?mine=created shows only own created').toEqual(['Alice Global Proj']);

    await loginViaUI(page, base, 'alice');

    const menu = page.getByTestId('project-menu');
    const trigger = page.getByTestId('project-select');
    const toggle = page.getByTestId('my-projects-toggle');

    // Open the dropdown: global mode lists every project to alice. The signed-in control renders.
    await trigger.click();
    await expect(menu).toBeVisible();
    await expect(toggle).toBeVisible();
    await expect(menu.getByText('Alice Global Proj')).toBeVisible();
    await expect(menu.getByText('Bob Global Proj')).toBeVisible();

    // Toggle ON: only alice's created project remains (bob's filtered out server-side).
    await toggle.click();
    await expect(menu.getByText('Alice Global Proj')).toBeVisible();
    await expect(menu.getByText('Bob Global Proj')).not.toBeVisible();

    // Toggle OFF: both return — global mode shows all when unfiltered.
    await toggle.click();
    await expect(menu.getByText('Alice Global Proj')).toBeVisible();
    await expect(menu.getByText('Bob Global Proj')).toBeVisible();
  });
});
