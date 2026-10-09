import { defineConfig, devices } from '@playwright/test';
import fs from 'fs';

const chromiumPath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ||
  ['/usr/bin/chromium', '/usr/bin/chromium-browser'].find((p) => fs.existsSync(p));

export default defineConfig({
  testDir: './e2e',
  timeout: 30000,
  expect: {
    timeout: 10000,
  },
  fullyParallel: false,
  workers: process.env.CI ? 2 : undefined,
  reporter: 'list',
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
