import { test, expect } from './fixtures';

/**
 * Ticket 01M3VJ5PQEAJ60B0QGPNZT8NKP — SSE singleton proof (E2E).
 *
 * Alpine invokes the root component's init() TWICE per page load (x-data
 * auto-runs a data object's init method, then x-init="init()" runs it again —
 * verified empirically against the shipped bundle: two invocation stacks ~6ms
 * apart, the second carrying Alpine's `[Alpine] init()` eval frame). Before the
 * init() guard, that double-run built TWO EventSource streams on boot; the first
 * was closed by the second connectSse()'s disconnectSse(false) — an open-then-
 * immediately-closed connection on every single page load.
 *
 * This spec proves, end to end in a real browser against a real server:
 *   1. exactly ONE EventSource instance is constructed at boot and stays the
 *      only non-CLOSED stream once SSE reports "Live";
 *   2. the app's TEST-SUPPORT telemetry (sseTelemetry, reached through the
 *      component reference Alpine.$data(root)._sseTelemetry) shows opened=1,
 *      closed=0, openCount()=1;
 *   3. forcing a SECOND init() invocation from the page context re-asserts all
 *      of the above: no new EventSource is constructed (telemetry `opened`
 *      stays 1), still exactly one open stream, sseStatus unchanged, and no
 *      console errors / uncaught page errors.
 *
 * Serves the pre-built web/dist against the shared worker-scoped ravn-server
 * fixture (./fixtures) on an ephemeral temp DB — no per-spec server lifecycle.
 */

test.describe('SSE singleton (boot init idempotence)', () => {
  let serverUrl = ''; // worker-scoped shared server (seeded per-test; board via ?project= deep link)
  let uncaughtConsoleErrors: string[] = [];

  test.beforeAll(async ({ serverUrl: sharedServerUrl }) => {
    serverUrl = sharedServerUrl;
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

    // Track every EventSource the page constructs: wrap the constructor so each
    // instance is recorded with a monotonic sequence number. A factory wrapper
    // (not a subclass) keeps the real instances intact — readyState, url and
    // close() all behave natively, which the assertions below rely on.
    await page.addInitScript(() => {
      (window as any).__ES_INSTANCES__ = [] as EventSource[];
      const NativeES = window.EventSource;
      window.EventSource = function trackedEventSource(this: unknown, url: string | URL, options?: EventSourceInit) {
        const es = new NativeES(url, options);
        (window as any).__ES_SEQ__ = ((window as any).__ES_SEQ__ ?? 0) + 1;
        es.__esSeq__ = (window as any).__ES_SEQ__;
        (window as any).__ES_INSTANCES__.push(es);
        return es; // `new` adopts the returned instance
      } as typeof EventSource;
    });
  });

  test.afterEach(() => {
    expect(uncaughtConsoleErrors).toEqual([]);
  });

  /** Open (not yet CLOSED) instances recorded by the init-script wrapper. */
  async function openInstances(page: import('@playwright/test').Page) {
    return page.evaluate(
      () =>
        ((window as any).__ES_INSTANCES__ as EventSource[]).filter((es) => es.readyState !== 2 /* CLOSED */),
    );
  }

  /** Plain-number snapshot of the app's TEST-SUPPORT SSE counters. */
  async function telemetrySnapshot(page: import('@playwright/test').Page) {
    return page.evaluate(() => {
      const root = document.querySelector('[x-data]');
      if (!root || !(window as any).Alpine) throw new Error('Alpine not started');
      const t = (window as any).Alpine.$data(root)._sseTelemetry;
      return { opened: t.opened, closed: t.closed, openCount: t.openCount(), sseStatus: (window as any).Alpine.$data(root).sseStatus };
    });
  }

  test('boot builds exactly ONE EventSource (stays one after a forced second init())', async ({ page }) => {
    // -------------------------------------------------------------------------
    // Step 1: Seed a project + task via API so boot follows the success path.
    // -------------------------------------------------------------------------
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'SSE Singleton Project',
        description: 'Seeded for the SSE singleton e2e proof',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
    const project = await projRes.json();
    expect(project.id).toBeTruthy();

    const taskRes = await fetch(`${serverUrl}/api/v1/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project_id: project.id,
        title: 'Singleton Watch Task',
        description: 'Present so the board renders content.',
        priority: 1,
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(taskRes.ok).toBe(true);

    // -------------------------------------------------------------------------
    // Step 2: Boot the UI and wait until SSE reports connected.
    // -------------------------------------------------------------------------
    // Deep-link (?project=) so boot adopts the seeded project; on the shared
    // worker DB a bare goto would auto-select a foreign projects[0] and the
    // board-content check at the end ('Singleton Watch Task') would miss it.
    await page.goto(`${serverUrl}/?project=${project.id}`);
    await expect(page).toHaveTitle(/^(crowban|Rävn)$/);

    await page.waitForFunction(() => {
      const app = (window as any).Alpine?.$data(document.querySelector('[x-data="app()"]'));
      return app?.sseStatus === 'connected';
    }, { timeout: 15000 });

    // -------------------------------------------------------------------------
    // Step 3: Steady state — exactly ONE constructed AND exactly ONE open
    // EventSource; telemetry agrees (opened=1, closed=0, openCount()=1).
    // The pre-fix double init built TWO instances (seq 1 immediately CLOSED);
    // this assertion would fail under that behavior.
    // -------------------------------------------------------------------------
    const constructed = await page.evaluate(() => ((window as any).__ES_INSTANCES__ as EventSource[]).length);
    expect(constructed, 'exactly one EventSource was ever constructed at boot').toBe(1);

    const open = await openInstances(page);
    expect(open.length, 'exactly one OPEN (non-CLOSED) stream at steady state').toBe(1);
    expect((open[0] as any).__esSeq__).toBe(1, 'the open stream is the only one that was ever built');

    const t0 = await telemetrySnapshot(page);
    expect(t0.opened).toBe(1);
    expect(t0.closed).toBe(0, 'no premature close of a first stream (the old double-init signature)');
    expect(t0.openCount).toBe(1);
    expect(t0.sseStatus).toBe('connected');

    // -------------------------------------------------------------------------
    // Step 4: Force the SECOND init() invocation from the page context — the
    // exact call Alpine's x-init directive would make on top of x-data's own
    // auto-run. With the guard in place this returns the memoized boot promise
    // and re-runs NOTHING.
    // -------------------------------------------------------------------------
    await page.evaluate(() => {
      const root = document.querySelector('[x-data]');
      if (!root || !(window as any).Alpine) throw new Error('Alpine not started');
      void (window as any).Alpine.$data(root).init();
    });

    // Sample over a short window: a regression that re-ran boot would construct
    // another EventSource within this call (synchronously), and any straggler
    // close+reconnect churn must also settle inside it.
    await expect
      .poll(
        async () => {
          const stillOne = await openInstances(page);
          const built = await page.evaluate(() => ((window as any).__ES_INSTANCES__ as EventSource[]).length);
          return { built, open: stillOne.length };
        },
        { timeout: 5000 },
      )
      .toEqual({ built: 1, open: 1 });

    const t1 = await telemetrySnapshot(page);
    expect(t1.opened, 'no new EventSource constructed by the second init()').toBe(1);
    expect(t1.closed, 'no stream was torn down by the second init()').toBe(0);
    expect(t1.openCount).toBe(1);
    expect(t1.sseStatus, 'SSE status unchanged after the forced re-init').toBe('connected');

    // The board is still alive and serving the seeded content.
    await expect(page.locator('article', { hasText: 'Singleton Watch Task' })).toBeVisible();
  });
});
