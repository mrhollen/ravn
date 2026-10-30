// Regression + QA-evidence spec for the EXPIRED claim-lease treatment.
//
// A claim lease that has crossed its expiry must render INERT, never healthy:
//   card capsule  → muted zinc border/bg, grey icon (no pulse), dimmed
//                   struck-through claimant name, "lease expired" hint in grey;
//   drawer bar    → same muted capsule, explicit "lease expired" cue, NO pulse,
//                   live countdown hidden.
// An ACTIVE lease keeps the indigo styling, pulsing icon and live countdown.
//
// This spec seeds both real states against its own ephemeral server: task A is
// claimed with the default 15-minute TTL (active), task B with ttl_sec=1 — a
// genuine lease that expires one second later, so the UI must flip it to the
// muted state on load. Screenshots are written to web/test-results/ as QA
// evidence (ephemeral; never committed).

import { test, expect } from './fixtures';
import fs from 'fs'; // write QA-evidence screenshots to web/test-results/ (gitignored)
import path from 'path';
import { fileURLToPath } from 'url';

// QA-evidence artifact anchor (NOT server scaffolding): screenshots land in
// web/test-results/ (ephemeral, gitignored).
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const testResultsDir = path.resolve(__dirname, '../test-results');

test.describe('claim lease expiry (active vs expired contrast)', () => {
  const ACTIVE_TITLE = 'Active Lease Task';
  const EXPIRED_TITLE = 'Expired Lease Task';
  const ACTIVE_ACTOR = 'e2e-seed';
  const EXPIRED_ACTOR = 'e2e-expired';

  // Seeds its own unique project and forces selection by id (below), so the
  // shared worker server + DB are safe to use here.
  test('active lease stays indigo+pulsing; expired lease renders muted with cue', async ({ page, serverUrl }) => {
    // ---------------------------------------------------------------- seed
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: `Claim Expired E2E ${Date.now()}`,
        description: 'Seeded for the lease-expiry contrast spec',
        actor: { name: ACTIVE_ACTOR, type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();

    const makeTask = async (title: string) => {
      const res = await fetch(`${serverUrl}/api/v1/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          project_id: project.id,
          title,
          description: `Seeded lease state: ${title}`,
          priority: 2,
          actor: { name: ACTIVE_ACTOR, type: 'agent' },
        }),
      });
      expect(res.ok).toBe(true);
      return (await res.json()).task;
    };
    const activeTask = await makeTask(ACTIVE_TITLE);
    const expiredTask = await makeTask(EXPIRED_TITLE);

    // A: default 15-minute lease → ACTIVE throughout the test.
    const claimA = await fetch(`${serverUrl}/api/v1/tasks/${activeTask.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: { name: ACTIVE_ACTOR, type: 'agent' } }),
    });
    expect(claimA.ok).toBe(true);

    // B: ttl_sec=1 lease → genuinely EXPIRED ~1s later (real server state, no mocking).
    const claimB = await fetch(`${serverUrl}/api/v1/tasks/${expiredTask.id}/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ actor: { name: EXPIRED_ACTOR, type: 'agent' }, ttl_sec: 1 }),
    });
    expect(claimB.ok).toBe(true);
    const leaseB = await claimB.json();
    expect(leaseB.claimed_by?.name).toBe(EXPIRED_ACTOR);
    expect(typeof leaseB.claim_expires_at).toBe('string');

    // Wait until B's lease has truly crossed its expiry (server clock == host clock;
    // a small margin avoids flakiness on the exact boundary instant).
    const expiryMs = Date.parse(leaseB.claim_expires_at);
    expect(Number.isNaN(expiryMs)).toBe(false);
    while (Date.now() < expiryMs + 250) {
      await new Promise((r) => setTimeout(r, 50));
    }

    // ---------------------------------------------------------------- board
    fs.mkdirSync(testResultsDir, { recursive: true });
    await page.goto(serverUrl);
    // Explicitly force the seeded project (fresh context auto-selects it anyway,
    // but never rely on that ordering across runs).
    await page.locator('[data-testid="project-select"]').click();
    await page.locator(`[data-testid="project-option"][data-project-id="${project.id}"]`).click();

    const activeCard = page.locator('article', { hasText: ACTIVE_TITLE });
    const expiredCard = page.locator('article', { hasText: EXPIRED_TITLE });
    await expect(activeCard).toBeVisible({ timeout: 10_000 });
    await expect(expiredCard).toBeVisible();

    // ACTIVE card: indigo capsule, live "until HH:MM (…)" hint from claimHint().
    const activeCapsule = activeCard.getByTestId('card-claim-capsule');
    await expect(activeCapsule).toHaveClass(/border-indigo-200/);
    await expect(activeCapsule.getByText(ACTIVE_ACTOR, { exact: true })).toBeVisible();
    await expect(activeCapsule.getByText(ACTIVE_ACTOR, { exact: true })).not.toHaveClass(/line-through/);
    await expect(activeCapsule.getByText(/until \d{2}:\d{2}/)).toBeVisible();

    // EXPIRED card: muted zinc capsule, struck-through dim name, grey "lease expired".
    const expiredCapsule = expiredCard.getByTestId('card-claim-capsule');
    await expect(expiredCapsule).toHaveClass(/border-zinc-300/);
    await expect(expiredCapsule).not.toHaveClass(/border-indigo-200/);
    await expect(expiredCapsule.getByText(EXPIRED_ACTOR, { exact: true })).toHaveClass(/line-through/);
    await expect(expiredCapsule.getByText('lease expired', { exact: true })).toBeVisible();

    // Both cards side by side: indigo active capsule vs muted expired one.
    await page.screenshot({ path: path.join(testResultsDir, 'claim-cards-board.png') });

    // ---------------------------------------------------------------- drawer: active
    await activeCard.click();
    const claimGroup = page.locator('[role="group"][aria-label="Claim Controls"]');
    const drawerCapsule = claimGroup.getByTestId('drawer-claim-capsule');
    // The activity icon is the FIRST svg INSIDE the claimed capsule (the group's
    // outer .first() would be the hidden unclaimed "Claim" button's lock icon).
    const activityIcon = drawerCapsule.locator('svg').first();
    await expect(drawerCapsule).toBeVisible();
    await expect(drawerCapsule).toHaveClass(/border-indigo-200/);
    // Pulsing indigo icon + live countdown, no expired cue.
    await expect(activityIcon).toHaveClass(/animate-pulse/);
    await expect(activityIcon).toHaveClass(/text-indigo-500/);
    await expect(claimGroup.locator('.live-countdown')).toBeVisible();
    await expect(claimGroup.getByTestId('drawer-claim-expired')).toBeHidden();
    await page.screenshot({ path: path.join(testResultsDir, 'claim-active-drawer.png') });

    // ---------------------------------------------------------------- drawer: expired
    await page.keyboard.press('Escape');
    await expect(page.locator('aside')).toBeHidden();
    await expiredCard.click();
    await expect(drawerCapsule).toBeVisible();
    // Muted capsule: zinc border, dimmed struck-through name, explicit grey cue,
    // NO pulse, countdown hidden.
    await expect(drawerCapsule).toHaveClass(/border-zinc-300/);
    await expect(drawerCapsule.getByText(EXPIRED_ACTOR, { exact: true })).toHaveClass(/line-through/);
    await expect(claimGroup.getByTestId('drawer-claim-expired')).toBeVisible();
    await expect(claimGroup.locator('.live-countdown')).toBeHidden();
    await expect(activityIcon).not.toHaveClass(/animate-pulse/);
    await expect(activityIcon).toHaveClass(/text-zinc-400/);
    // The board behind the drawer still shows both states side by side — the contrast.
    await page.screenshot({ path: path.join(testResultsDir, 'claim-expired-contrast.png') });
  });
});
