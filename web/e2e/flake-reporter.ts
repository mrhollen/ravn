// E2E flake-surfacing reporter — see the "Flake policy" comment in
// playwright.config.ts (task 01M66ZGEAXJV3KENXW07CXJVY9).
//
// The suite runs with bounded retries in CI (retries: 1) so a single transient
// failure does not force a full-suite re-run. But a test that "passes" only
// because of its retry is a real-regression candidate and must NOT be silently
// green. This reporter walks the finished suite at run end and logs every test
// whose final attempt passed after one or more earlier failed attempts (a
// Playwright "flaky" pass). It writes nothing to disk — it only adds a warning
// line. Paired with `list` in playwright.config.ts, so on a clean run it is
// silent and the normal output is unchanged.
//
// Not collected as a test: its name does not match the *.spec.ts testMatch
// pattern (same convention as fixtures.ts / helpers.ts).

import type { FullConfig, Reporter, Suite } from '@playwright/test/reporter';

export default class FlakeSurfacer implements Reporter {
  private rootSuite: Suite | undefined;

  onBegin(_config: FullConfig, suite: Suite): void {
    this.rootSuite = suite;
  }

  onEnd(): void {
    const tests = this.rootSuite?.allTests() ?? [];
    // A test is a "passed-via-retry" flake when it ran more than once and its
    // LAST attempt passed. Playwright only schedules a retry after a failure, so
    // `results.length >= 2 && last.status === 'passed'` ⇔ one or more failed then
    // a passing retry. A test that fails on every attempt ends 'failed' (surfaced
    // as red by `list`) and is correctly NOT listed here.
    const flaky = tests.filter((t) => {
      const results = t.results;
      return results.length >= 2 && results[results.length - 1].status === 'passed';
    });
    if (flaky.length === 0) return;

    // Locate each flagged test by file:line + title. The suite has a single
    // project (chromium), so the path is unambiguous; make it relative when we
    // can to keep CI log lines short.
    const cwd = process.cwd();
    const rel = (p: string) => (cwd && p.startsWith(cwd) ? p.slice(cwd.length + 1) : p);
    const lines = flaky.map((t) => `  - ${rel(t.location.file)}:${t.location.line} › ${t.title}`);
    console.error(
      `\n[flake-policy] ⚠ ${flaky.length} test(s) PASSED ONLY VIA RETRY — potential regression, investigate before treating as green:\n${lines.join('\n')}\n`,
    );
  }
}
