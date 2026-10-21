import { test, expect } from '@playwright/test';
import { spawn, execSync, type ChildProcess } from 'child_process';
import { randomUUID } from 'crypto';
import fs from 'fs';
import net from 'net';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

// WCAG 2.4.7 focus-visible coverage for the .btn-primary / .btn-ghost ring fix
// (Rävn task 01M3MMTFYQN962AS3ZFF384RRE). The CSS change adds
// `focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-1
// focus-visible:ring-zinc-400 … dark:focus-visible:ring-zinc-300
// dark:focus-visible:ring-offset-zinc-900` to both button classes; Tailwind
// compiles ring + offset to an ADDITIONAL box-shadow layer pair on top of the
// buttons' existing `shadow-sm`, so a keyboard-focused button must:
//   1. match :focus-visible, and
//   2. have a computed box-shadow that GAINED layers vs. its unfocused value.
// The spec drives real keyboard Tab (the app's dialog focus trap intercepts
// Tab keydowns and calls el.focus() itself — keyboard modality is preserved, so
// :focus-visible still matches), with a bounded tab loop keyed off
// document.activeElement instead of hard-coded stop counts (deterministic even
// if the tab order shifts).
//
// Header-button tests run at 375px: below md the header's .btn-ghost control
// is the hamburger toggle (`md:hidden`) — at desktop width no .btn-ghost exists
// in <header>, only the .btn-primary "+ New task" CTA. Modal tests use the
// default Desktop Chrome viewport (1280x720).
//
// Self-contained like the other specs: ephemeral ravn-server on a random high
// port (never :8080), temp DB, pre-built dist.

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, '../..');
const serverBin = path.join(repoRoot, 'bin', 'ravn-server');
const staticDir = path.resolve(__dirname, '../dist');

// Same mobile viewport as mobile.spec.ts; below the md breakpoint (768px).
const MOBILE_VIEWPORT = { width: 375, height: 812 };

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
      // Retry until server starts listening
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`Timed out waiting for server at ${url}`);
}

test.describe('Focus visible rings (WCAG 2.4.7)', () => {
  let serverProcess: ChildProcess;
  let tempDir: string;
  let serverUrl: string;
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

    tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ravn-focus-e2e-'));
    const serverPort = await getFreePort();
    expect(serverPort).not.toBe(8080);
    serverUrl = `http://127.0.0.1:${serverPort}`;

    serverProcess = spawn(serverBin, [], {
      env: {
        ...process.env,
        RAVN_ADDR: `127.0.0.1:${serverPort}`,
        RAVN_DATA: path.join(tempDir, 'ravn.db'),
        RAVN_STATIC_DIR: staticDir,
        RAVN_TOKEN: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stderr?.on('data', (d) => {
      const msg = d.toString();
      if (process.env.DEBUG_E2E) console.error('[server stderr]', msg);
    });

    await waitForServer(serverUrl);

    // Exactly ONE project: first visit auto-selects projects[0] (continueBoot),
    // which enables the otherwise-disabled "+ New task" header CTA. No tasks are
    // seeded — the board stays uncluttered for the evidence screenshots.
    const projRes = await fetch(`${serverUrl}/api/v1/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: `Focus E2E ${randomUUID()}`,
        description: 'Seeded for focus-visible e2e tests',
        actor: { name: 'e2e-seed', type: 'agent' },
      }),
    });
    expect(projRes.ok).toBe(true);
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

  /** Load the app and wait until boot finished: the seeded project is
   *  auto-selected, which is what enables the "+ New task" CTA. */
  async function openBoard(page: import('@playwright/test').Page): Promise<void> {
    await page.goto(serverUrl);
    await expect(page).toHaveTitle('Rävn');
    await expect(page.getByRole('button', { name: '+ New task' })).toBeEnabled({ timeout: 15_000 });
  }

  /** Switch to an explicit theme via the consolidated header settings menu —
   *  the same technique theme.spec.ts uses. Closes the menu with Escape so a
   *  dropdown keydown handler can't interfere with the later Tab sequences;
   *  Escape restores focus to the trigger (app.closeHeaderMenu(restoreFocus)). */
  async function setThemeViaMenu(page: import('@playwright/test').Page, mode: 'light' | 'dark'): Promise<void> {
    await page.getByTestId('header-menu-trigger').click();
    await expect(page.getByTestId('header-menu')).toBeVisible();
    await page.locator(`[data-testid="theme-${mode}"]`).click();
    const expectedDark = mode === 'dark';
    expect(
      await page.evaluate(() => document.documentElement.classList.contains('dark')),
      `<html> .dark class after selecting ${mode}`,
    ).toBe(expectedDark);
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('header-menu')).toBeHidden();
    await expect(page.getByTestId('header-menu-trigger')).toBeFocused({ timeout: 3000 });
  }

  /** Split a serialized box-shadow at TOP-LEVEL commas (colors like
   *  rgba(0, 0, 0, 0.1) contain commas of their own). */
  function splitShadows(box: string): string[] {
    if (box === 'none') return [];
    const parts: string[] = [];
    let depth = 0;
    let cur = '';
    for (const ch of box) {
      if (ch === '(') depth++;
      if (ch === ')') depth--;
      if (ch === ',' && depth === 0) {
        parts.push(cur.trim());
        cur = '';
      } else {
        cur += ch;
      }
    }
    if (cur.trim()) parts.push(cur.trim());
    return parts;
  }

  /** true when a serialized shadow segment is fully transparent.
   *  Tailwind v4 preflight gives every element zero-alpha placeholder values for
   *  --tw-inset-shadow / --tw-ring(-offset)-shadow, so an UNFOCUSED .btn-*
   *  already serializes several "rgba(0, 0, 0, 0) 0px 0px 0px 0px" slots — raw
   *  segment counts are equal before/after focus and must not be the metric. */
  function isTransparentSegment(seg: string): boolean {
    if (seg === 'none') return true; // a `none` shadow layer contributes nothing
    const rgba = seg.match(/rgba?\(\s*[\d.]+\s*,\s*[\d.]+\s*,\s*[\d.]+\s*(?:,\s*([\d.]+%?)\s*)?\)/);
    if (rgba) return rgba[1] !== undefined ? Number(rgba[1].replace('%', '')) === 0 : false;
    const oklch = seg.match(/oklch\([^()]*\/\s*([\d.]+%?)\s*\)/);
    if (oklch) return Number(oklch[1].replace('%', '')) === 0;
    return false; // opaque rgb()/oklch()/hex/named → visible
  }

  /** Count of VISIBLE shadow layers (non-transparent segments). The focus fix
   *  adds ring-offset + ring = two extra visible layers on top of shadow-sm. */
  function effectiveShadowLayers(box: string): number {
    return splitShadows(box).filter((s) => !isTransparentSegment(s)).length;
  }

  interface TabOptions {
    shift?: boolean; // Shift+Tab (search backwards through the tab order)
    maxSteps?: number;
  }

  /** Sample a button's computed box-shadow while it is NOT focused. */
  async function unfocusedBoxShadow(page: import('@playwright/test').Page, selector: string): Promise<string> {
    return page.evaluate((sel) => {
      const el = document.querySelector(sel);
      if (!el) throw new Error(`no element matching ${sel}`);
      if (document.activeElement === el) {
        throw new Error(`${sel} is already focused before the Tab sequence started`);
      }
      return getComputedStyle(el as HTMLElement).boxShadow;
    }, selector);
  }

  /** Press Tab (or Shift+Tab) in a bounded loop until `selector` is
   *  document.activeElement, then let the box-shadow ring transition settle
   *  (.btn-* carries a 150ms `transition` that includes box-shadow) and sample
   *  :focus-visible + computed box-shadow from the focused element. */
  async function tabTo(
    page: import('@playwright/test').Page,
    selector: string,
    opts: TabOptions = {},
  ): Promise<{ focusVisible: boolean; boxShadow: string }> {
    const maxSteps = opts.maxSteps ?? 25;
    let hit = false;
    for (let i = 1; i <= maxSteps && !hit; i++) {
      await page.keyboard.press(opts.shift ? 'Shift+Tab' : 'Tab');
      hit = await page.evaluate((sel) => {
        const ae = document.activeElement;
        return !!ae && ae instanceof HTMLElement && ae.matches(sel);
      }, selector);
    }
    expect(hit, `keyboard Tab never reached ${selector} within ${maxSteps} stops`).toBe(true);
    await page.waitForTimeout(300); // 150ms box-shadow transition must fully settle first

    const sample = await page.evaluate((sel) => {
      const ae = document.activeElement;
      if (!ae || !(ae instanceof HTMLElement) || !ae.matches(sel)) {
        return null; // focus escaped the expected element — fail loudly below
      }
      return {
        focusVisible: ae.matches(':focus-visible'),
        boxShadow: getComputedStyle(ae).boxShadow,
      };
    }, selector);
    expect(sample, `${selector} lost focus between Tab and sampling`).not.toBeNull();
    return sample!;
  }

  /** Full assertion: keyboard-tab into `selector` (bounded loop) and prove the
   *  focus-visible ring appeared — :focus-visible matches AND the computed
   *  box-shadow gained layer(s) vs. the unfocused baseline (Tailwind's
   *  ring-2 + ring-offset-1 add two shadow layers on top of shadow-sm). */
  async function assertRingOnTab(
    page: import('@playwright/test').Page,
    selector: string,
    label: string,
    opts: TabOptions = {},
  ): Promise<void> {
    const before = await unfocusedBoxShadow(page, selector);
    const after = await tabTo(page, selector, opts);
    expect(after.focusVisible, `${label}: focused element does not match :focus-visible`).toBe(true);
    expect(
      after.boxShadow !== before && effectiveShadowLayers(after.boxShadow) > effectiveShadowLayers(before),
      `${label}: no visible focus ring — box-shadow did not gain a VISIBLE layer on keyboard focus ` +
        `(before: "${before}" [${effectiveShadowLayers(before)} visible], ` +
        `after: "${after.boxShadow}" [${effectiveShadowLayers(after.boxShadow)} visible])`,
    ).toBe(true);
  }

  // ------------------------------------------------------- header buttons, light
  test.describe('header buttons, light mode', () => {
    // The header's .btn-ghost control is the hamburger toggle (md:hidden): it is
    // only tabbable below the md breakpoint, so pin the mobile viewport here.
    test.use({ viewport: MOBILE_VIEWPORT });

    test('Tab reaches header .btn-primary and .btn-ghost; each shows a focus ring', async ({ page }) => {
      await openBoard(page);
      expect(
        await page.evaluate(() => document.documentElement.classList.contains('dark')),
        'starts in light mode (no stored theme, light OS default)',
      ).toBe(false);

      // Header tab order at 375px: [project-select][gear]["+ New task"][hamburger]…
      // Focus starts on <body> after load, so forward Tab lands on the primary first.
      await assertRingOnTab(page, 'header button.btn-primary', 'light header "+ New task"');

      // Continue from there: the next .btn-ghost in the header is the hamburger.
      await assertRingOnTab(page, 'header button.btn-ghost', 'light header hamburger (.btn-ghost)');

      // Leave the test with the PRIMARY focused so Playwright's auto-captured
      // test-finished screenshot (QA evidence) shows its ring: one Shift+Tab back.
      await page.keyboard.press('Shift+Tab');
      const backOnPrimary = await page.evaluate(
        () => !!document.activeElement && document.activeElement instanceof HTMLElement &&
          document.activeElement.matches('header button.btn-primary'),
      );
      expect(backOnPrimary, 'final state for the evidence screenshot: primary refocused').toBe(true);
    });
  });

  // --------------------------------------------------------- modal actions, light
  test.describe('modal actions, light mode', () => {
    test('modal Cancel (.btn-ghost) and Save/form-submit (.btn-primary) show a focus ring on Tab', async ({ page }) => {
      await openBoard(page);

      // Open the New task modal from its header trigger. The app's dialog focus
      // session moves focus into the title input (openNewTask → focusWhenReady).
      await page.getByRole('button', { name: '+ New task' }).click();
      const dialog = page.locator('#cb-new-task');
      await expect(dialog).toBeVisible();
      const titleInput = page.locator('#cb-new-task input[placeholder="Fix login bug"]');
      await expect(titleInput).toBeFocused({ timeout: 5000 });

      // The submit Save button is disabled until a title exists — type one so it
      // becomes part of the tab order (the trap skips disabled buttons).
      await titleInput.fill('Focus ring check');
      const save = page.locator('#cb-new-task .btn-primary'); // form submit "Create task"
      await expect(save).toBeEnabled();

      // Cancel (.btn-ghost) first…
      await assertRingOnTab(page, '#cb-new-task .btn-ghost', 'light modal "Cancel" (.btn-ghost)');
      // …then the form's own submit Save (.btn-primary): the same element is the
      // form-submit button required by the task — one assertion covers both.
      await assertRingOnTab(page, '#cb-new-task .btn-primary', 'light modal "Create task" (form submit)');

      // Focus stays on Save at test end → the auto-captured screenshot shows the
      // modal open with the ring around Save.
    });
  });

  // ------------------------------------------------------------- dark mode
  test.describe('dark mode', () => {
    // Forced-light OS preference so the explicit Dark pick is unambiguous
    // (theme.spec.ts's technique).
    test.use({ colorScheme: 'light' });

    test.describe('header buttons (mobile width)', () => {
      test.use({ viewport: MOBILE_VIEWPORT });

      test('Tab reaches header .btn-primary and .btn-ghost; each shows a focus ring', async ({ page }) => {
        await openBoard(page);
        expect(
          await page.evaluate(() => document.documentElement.classList.contains('dark')),
          'light before the explicit switch',
        ).toBe(false);
        await setThemeViaMenu(page, 'dark');

        // After Escape, focus sits on the header-menu trigger (last header control),
        // so SHIFT+Tab walks backwards: [hamburger][ "+ New task"][gear][select]…
        await assertRingOnTab(
          page,
          'header button.btn-ghost',
          'dark header hamburger (.btn-ghost)',
          { shift: true },
        );
        await assertRingOnTab(page, 'header button.btn-primary', 'dark header "+ New task"', { shift: true });

        // Ends with the dark-mode primary focused → auto screenshot is the
        // "header .btn-primary focused in dark mode" evidence shot.
      });
    });

    test('modal Cancel (.btn-ghost) and Save/form-submit (.btn-primary) show a focus ring on Tab', async ({ page }) => {
      await openBoard(page);
      await setThemeViaMenu(page, 'dark');

      await page.getByRole('button', { name: '+ New task' }).click();
      const dialog = page.locator('#cb-new-task');
      await expect(dialog).toBeVisible();
      const titleInput = page.locator('#cb-new-task input[placeholder="Fix login bug"]');
      await expect(titleInput).toBeFocused({ timeout: 5000 });

      await titleInput.fill('Focus ring check dark');
      const save = page.locator('#cb-new-task .btn-primary');
      await expect(save).toBeEnabled();

      await assertRingOnTab(page, '#cb-new-task .btn-ghost', 'dark modal "Cancel" (.btn-ghost)');
      await assertRingOnTab(page, '#cb-new-task .btn-primary', 'dark modal "Create task" (form submit)');

      // Ends with Save focused in the dark-themed modal → evidence screenshot.
    });
  });
});
