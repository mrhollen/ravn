# AGENTS.md

## E2E test artifacts & QA evidence

The browser E2E suite (`web/e2e/`, Playwright) is **self-contained**: each spec spins up its own ephemeral crowban-server on a random high port (never :8080) with a temp DB, and serves the pre-built `web/dist`. You do not need a running server or fixed port.

### Run it

```bash
cd web && npm run test:e2e   # or from repo root: make e2e
```

### Screenshots

Every test leaves one screenshot under `web/test-results/`, in a per-test folder named after the spec + full test title path (describe block › test). Playwright sanitizes and truncates long titles, appending a short hash suffix, so find folders by grepping a distinctive part of the test name. Real examples from a green run:

```
web/test-results/theme-theme-dark-mode-head-86d82-rk-System-theme-toggle-AC1--chromium/test-finished-1.png
web/test-results/board-Web-E2E-test-suite-s-83d10-sk-and-posts-comment-via-UI-chromium/test-finished-1.png
web/test-results/mobile-Mobile-experience-3-9d523-tap-target-minimum-at-375px-chromium/test-finished-1.png
```

The Playwright config (`web/playwright.config.ts`) sets `use.screenshot: 'on'`, so screenshots are produced for **passing** tests too (not just failures). These artifacts are **ephemeral**: `web/.gitignore` ignores `test-results/` and `playwright-report/`. Never commit anything under them; if `git status --porcelain` lists files there, that's a bug.

### QA evidence workflow

After a green run:

1. Review the generated screenshots in `web/test-results/`. Open only the ones relevant to what you verified — folder names map to spec + test case, so find them by name instead of opening every PNG.
2. Attach task-relevant screenshot(s) as evidence on the CrowBan task:
   - tool `attach_artifact`
   - `kind`: `screenshot`
   - `ref`: absolute path to the `.png` (e.g. `/home/hollen/Code/crowban/web/test-results/theme-theme-dark-mode-head-86d82-rk-System-theme-toggle-AC1--chromium/test-finished-1.png`)
   - `summary`: one line — what the shot shows and which AC it evidences.

Applies to QA agents (`frontend-qa`, `backend-qa`) and devs verifying their own work: leave visual evidence the UI looks right, not just that tests passed. If per-agent prompt files are added to this repo later, move this requirement there.
