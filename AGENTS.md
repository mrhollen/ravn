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

## Local CI/CD, Container Registry & Infrastructure Runbook

This repository runs workflows both on GitHub (`.github/workflows/`) and a local Forgejo instance (`.forgejo/workflows/`). The local pipeline tests and automatically builds and publishes the `server-runtime` image to Forgejo's container registry on pushes to `main`.

When working on CI workflows, testing, or container publishing, be aware of the following local infrastructure constraints and lessons learned:

### 1. Local Network Topology & Port Mappings
* **Host IP**: `10.0.0.150` (`forgejo.lan`).
* **Port 80 (HTTP)**: Forgejo Web UI and OCI Container Registry (`http://forgejo.lan:80/v2/`).
* **Port 443 (HTTPS)**: Runs Open WebUI (FastAPI / Uvicorn), **NOT** Forgejo! Connecting to `https://forgejo.lan:443/v2/` will fail with HTTP `405 Method Not Allowed`. Always use plain HTTP on Port 80 for Forgejo and its registry.
* **Port 2222**: Forgejo Git SSH endpoint (`ssh://git@forgejo.lan:2222/mrhollen/crowban.git`).
* **Port 8081**: Nginx Proxy Manager Web UI.

### 2. DNS Resolution in Runner Containers
* Forgejo's `act_runner` executes workflows inside Docker containers whose DNS resolver is the Docker embedded daemon (`127.0.0.11:53`).
* This resolver cannot resolve `.lan` local hostnames (like `forgejo.lan`).
* **Rule**: Any step in a container requiring network access to `forgejo.lan` must inject the host entry:
  ```bash
  grep -q "forgejo.lan" /etc/hosts || echo "10.0.0.150 forgejo.lan" >> /etc/hosts
  ```

### 3. Container Registry Pushing: Docker Daemon vs `crane`
* The runner container mounts the host Docker socket (`/var/run/docker.sock`) to allow `docker build` and `docker save`.
* **The Problem with `docker push`**: The Docker daemon enforces HTTPS by default. Pushing to plain HTTP `forgejo.lan:80` fails with `http: server gave HTTP response to HTTPS client` unless the host Docker daemon is configured with `insecure-registries` in `/etc/docker/daemon.json` and restarted (which disrupts host services).
* **The Solution (`crane`)**: Use Google's `crane` CLI (`go-containerregistry`). It runs completely daemonless in user space, downloads in under a second, and natively supports plain HTTP via `--insecure`:
  ```bash
  # 1. Install crane (takes ~0.8s)
  curl -sL https://github.com/google/go-containerregistry/releases/download/v0.22.1/go-containerregistry_Linux_x86_64.tar.gz | tar -xz -C /usr/local/bin crane

  # 2. Build image
  docker build --target server-runtime -t crowban:latest .

  # 3. Log in with crane
  echo "${PUSH_TOKEN}" | crane auth login "forgejo.lan:80" -u "${{ github.actor }}" --password-stdin --insecure

  # 4. Save to tarball and push (crane push requires a file path, not stdin)
  docker save crowban:latest -o /tmp/crowban.tar
  crane push /tmp/crowban.tar "forgejo.lan:80/mrhollen/crowban:latest" --insecure
  rm -f /tmp/crowban.tar
  ```

### 4. Authentication & Package Scopes (`PACKAGE_TOKEN`)
* Forgejo's default ephemeral `GITHUB_TOKEN` does not have package write permissions. Pushing images with it will fail with `401 Unauthorized: reqPackageAccess`.
* A Personal Access Token (PAT) with `package:write` scope is stored in repository secrets as `PACKAGE_TOKEN`.
* The workflow step uses:
  ```yaml
  env:
    PUSH_TOKEN: ${{ secrets.PACKAGE_TOKEN || secrets.FORGEJO_TOKEN || secrets.GITHUB_TOKEN }}
  ```

### 5. Nginx Reverse Proxy & HTTP 413 "Request Entity Too Large"
* Forgejo sits behind an Nginx reverse proxy on port 80.
* Nginx's default `client_max_body_size` is **1MB**. Container layer uploads are typically 10–30+ MB, causing Nginx to reject `PATCH /v2/.../blobs/uploads/...` with `413 Request Entity Too Large (nginx/1.28.0)`.
* **Requirement**: The Nginx proxy host for `forgejo.lan` must have `client_max_body_size 512M;` (or `client_max_body_size 0;`) configured in its host settings / Advanced tab.

### 6. Pipeline Structure & Test Performance
* **Split Jobs**:
  - `ci` (`Test and Build`): Runs linters, Go unit tests, web build, and Playwright E2E suite.
  - `publish` (`Build and Push Image`): Depends on `ci` (`needs: ci`), running only on pushes to `main`.
* **CI Build & Install Parallelization**:
  - In `ci.yml`, the Vite frontend build and Playwright Chromium dependency download run concurrently in subshells:
    ```bash
    npm run build &
    npx playwright install --with-deps chromium &
    wait
    ```
* **Playwright E2E Concurrency**:
  - Configured with 2 workers in CI (`workers: process.env.CI ? 2 : undefined`), completing all 57 E2E tests in under 30 seconds.
* **Strict Locators**:
  - When asserting text in the UI (especially inside drawers or popovers where options and status pills may share labels like "Claimed"), always use exact matching (`getByText('Claimed', { exact: true })`) or scope to the specific container/role to prevent strict-mode violations.

