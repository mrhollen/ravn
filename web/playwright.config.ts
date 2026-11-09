import { defineConfig, devices } from '@playwright/test';
import fs from 'fs';

const chromiumPath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ||
  ['/usr/bin/chromium', '/usr/bin/chromium-browser'].find((p) => fs.existsSync(p));

export default defineConfig({
  testDir: './e2e',
  // Build-checks (fresh web/dist + bin/ravn-server) run ONCE here, before any
  // worker exists — see e2e/global-setup.ts for why workers must not race the
  // binary rebuild themselves.
  globalSetup: './e2e/global-setup.ts',
  timeout: 30000,
  expect: {
    timeout: 10000,
  },
  fullyParallel: false,
  workers: process.env.CI ? 2 : undefined,
  // ---- Flake policy (task 01M66ZGEAXJV3KENXW07CXJVY9) ------------------------
  // Bounded retries so ONE transient failure (sandbox restart, CI scheduling
  // jitter, a flaky worker-DB race) does not force a full-suite re-run — the
  // most expensive test run in the repo after Go. Retries fire only on a real
  // failure, so a clean run is ~unchanged in wall time and the added cost is
  // capped at one extra attempt per flaky test. Locally we keep retries at 0 so
  // failures surface immediately while iterating (run a failed spec twice before
  // assuming a regression — see docs/TESTING.md §3 "E2E tests").
  //
  // A test that passes only via its retry is NOT silently green: the second
  // reporter below (e2e/flake-reporter.ts) walks the finished suite in onEnd and
  // logs every test whose final attempt passed after ≥1 failed attempt, so a
  // flaky pass is surfaced for review instead of masked by the retry.
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['./e2e/flake-reporter.ts']],
  use: {
    headless: true,
    // Capture a screenshot for every test (passing included) into the default
    // outputDir (test-results/) under a per-test folder named after the spec
    // + full test title. Ephemeral QA evidence; never commit it.
    screenshot: 'on',
    launchOptions: {
      ...(chromiumPath ? { executablePath: chromiumPath } : {}),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    },
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        launchOptions: {
          ...(chromiumPath ? { executablePath: chromiumPath } : {}),
          args: ['--no-sandbox', '--disable-setuid-sandbox'],
        },
      },
    },
  ],
});
