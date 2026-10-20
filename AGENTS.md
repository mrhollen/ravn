# AGENTS.md

## E2E test artifacts & QA evidence

The browser E2E suite (`web/e2e/`, Playwright) is **self-contained**: each spec spins up its own ephemeral ravn-server on a random high port (never :8080) with a temp DB, and serves the pre-built `web/dist`. You do not need a running server or fixed port.

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
2. Attach task-relevant evidence (screenshot, video, PDF, audio, command output, or log) on the Rävn task:
   - tool `attach_artifact`
   - `id`: task ID
   - `kind`: `screenshot` (or `other` for videos/PDFs/audio, `command-output`, `log`)
   - `data_base64`: base64-encoded file bytes (e.g. `base64 -w 0 <path>`). **MANDATORY for screenshots and media files.** This reliably uploads binary data to the server storage volume so the web UI renders an inline clickable image preview.
   - `filename`: filename with extension (e.g. `test-finished-1.png`, `demo.mp4`, `report.pdf`) to preserve MIME type for inline preview/playback.
   - `ref`: optional filename (e.g. `test-finished-1.png`) or URL. For compatibility with older MCP server schemas that require `ref`, pass the filename here as well.
   - `summary`: one line — what the evidence shows and which AC it evidences.

> [!CAUTION]
> **NEVER pass local filesystem paths (e.g. `/home/...`) in `ref`.**
> The Rävn server may run in a remote container environment (a different host/network than your client). It cannot read files from your local machine. Passing a local filesystem path will cause the UI to render raw text rather than an inline image/video preview. You MUST read the file and pass `data_base64`.

Applies to QA agents (`frontend-qa`, `backend-qa`) and devs verifying their own work: leave visual evidence the UI looks right, not just that tests passed. If per-agent prompt files are added to this repo later, move this requirement there.

### README hero screenshot (`docs/assets/hero.png`)

The README shows a single committed screenshot at `docs/assets/hero.png`, linked with a host-agnostic relative path so it renders on both the Forgejo host and the GitHub mirror. It is **not** auto-refreshed by CI.

To refresh it when you want a new shot:
1. Run the deterministic spec (writes `web/test-results/hero.png`): `cd web && npx playwright test e2e/hero-screenshot.spec.ts` — or a full `npm run test:e2e`.
2. Copy it into place: `cp web/test-results/hero.png docs/assets/hero.png`.

> [!CAUTION]
> **Do NOT commit a refreshed `docs/assets/hero.png` unless you have explicit instructions to update the README screenshot.** Refreshing the file locally is fine; committing binary churn on every run is not. Leave `docs/assets/hero.png` untouched and uncommitted unless a human explicitly asks for a new hero image.

## Local CI/CD, Container Registry & Infrastructure Runbook

This repository runs workflows both on GitHub (`.github/workflows/`) and a local Forgejo instance (`.forgejo/workflows/`). The local pipeline tests and can build + publish the `server-runtime` image to a container registry. **No host-specific values are hardcoded in this repo** — all topology specifics come from repository variables/secrets and `.env` overrides, so the repo stays safe for public release.

### 1. Registry & Host Configuration (repository variables / secrets)
* Set these on the Forgejo repository (Settings → Actions variables & secrets). On a fresh fork with none set, the `publish` job is **skipped cleanly** (it is gated on `vars.REGISTRY_HOST`).
  * `REGISTRY_HOST` — registry host:port, e.g. `<REGISTRY_HOST>`.
  * `REGISTRY_IMAGE` — full image reference to push, e.g. `<REGISTRY_HOST>/<org>/ravn:latest`. If omitted, CI derives it from `REGISTRY_HOST` + the lowercased repository name.
  * Secret `REGISTRY_HOSTS_ENTRY` — optional `<ip> <hostname>` pair injected into `/etc/hosts` in the runner for plain-HTTP LAN registries (alternatively configure `extra_hosts` / `--add-host=<hostname>:<ip>` in the runner's `config.yaml`).

### 2. DNS Resolution in Runner Containers
* Some runner containers (e.g. Forgejo `act_runner`) use a DNS resolver that cannot resolve local hostnames like `<REGISTRY_HOST>`.
* **Rule**: inject the host entry via the `REGISTRY_HOSTS_ENTRY` secret or the runner's `extra_hosts`, e.g.:
  ```bash
  echo "<ip> <hostname>" >> /etc/hosts   # idempotent-gated in ci.yml; only applied when the secret is set
  ```

### 3. Container Registry Pushing: Docker Daemon vs `crane`
* The runner mounts the host Docker socket to allow `docker build` and `docker save`.
* **The Problem with `docker push`**: the Docker daemon enforces HTTPS by default; pushing to a plain-HTTP registry fails with `http: server gave HTTP response to HTTPS client` unless the host Docker daemon is configured with `insecure-registries` and restarted (disruptive).
* **The Solution (`crane`)**: Google's `crane` CLI runs daemonless in user space and natively supports plain HTTP via `--insecure`:
  ```bash
  # Values come from vars.REGISTRY_HOST / vars.REGISTRY_IMAGE
  docker build --target server-runtime -t ravn:latest .
  echo "${PUSH_TOKEN}" | crane auth login "${REGISTRY}" -u "${{ github.actor }}" --password-stdin --insecure
  docker save ravn:latest -o /tmp/ravn.tar
  crane push /tmp/ravn.tar "${IMAGE_NAME}" --insecure
  ```

### 4. Authentication & Package Scopes (`PACKAGE_TOKEN`)
* The default ephemeral workflow token may lack package write permission (push → `401 reqPackageAccess`). Store a Personal Access Token with `package:write` scope in repo secrets as `PACKAGE_TOKEN`.
* The workflow resolves the push token as `${{ secrets.PACKAGE_TOKEN || secrets.FORGEJO_TOKEN || secrets.GITHUB_TOKEN }}`.

### 5. Reverse-Proxy Body-Size Limit ("413 Request Entity Too Large")
* If the registry sits behind Nginx, remember its default `client_max_body_size` is **1MB**; container layer uploads are much larger. Set `client_max_body_size 512M;` (or `0`) for the registry host in the reverse proxy configuration.

### 6. Pipeline Structure & Test Performance
* **Split Jobs**:
  - `ci` (`Test and Build`): linters, Go unit tests, web build, Playwright E2E suite, compose validation, image build.
  - `publish` (`Build and Push Image`): depends on `ci`, runs only on pushes to `main` **and** only when `vars.REGISTRY_HOST` is set.
* **CI Build & Install Parallelization**: the Vite frontend build and the Playwright Chromium download run concurrently in subshells (see `ci.yml`).
* **Playwright E2E Concurrency**: 2 workers in CI; the suite completes well under a minute.
* **Strict Locators**: when asserting text in the UI, use exact matching (`getByText('Claimed', { exact: true })`) or scope to the specific container/role to avoid Playwright strict-mode violations.

