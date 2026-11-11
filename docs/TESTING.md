# Testing Strategy & Best Practices

Audited 2026-10-30; re-measured post test-quality epic
(`01M6735WRK6XC46HQCSYGAEYVC`) on 2026-10-31, again after the multi-user
epic (T1–T9) on 2026-11-09, and a web-unit baseline refresh for the
frontend-modularization Phase 0 on 2026-11-11. This document is the source of
truth for how ravn is tested, the measured baselines, and the conventions new
tests must follow.
Improvements are tracked as tasks on the Ravn board (see
[Findings index](#findings-index-task-traceability)).

## 1. The three suites

| Suite | What it covers | Runner | Size (2026-10-31, post-epic) | Wall time (local, 12-core) | Coverage |
|---|---|---|---|---|---|
| Go unit + integration (`internal/`, `cmd/`) | domain rules, application use-cases, SQLite repos, HTTP handlers, MCP tools | `go test` (+ `-race` in CI) | 137 files, **50,546** LOC (re-measured post-multiuser epic, 2026-11-09; was 91 / 38,080 on 2026-10-31) | **~7.1s** total; `-race` **24.2s**; slowest package `internal/interface/http` ~3.3s wall (re-measured 2026-11-09) | **84.5%** statements total |
| Web unit (`web/test/`) | pure logic of `web/src/*.ts` (dates, markdown, drafts, API client, board state/scope, identity, members, moves, refresh, realtime) + the file-size guardrail (§3) | `node:test` via `jiti` (`npm run test:unit`; coverage: `npm run test:unit:coverage`) | 13 per-module files + `test-helpers.ts`, **6,801** LOC, **265** tests (re-measured 2026-11-11) | **~8.0s** (suite 7.96s; re-measured 2026-11-11) | **70.55%** lines / 75.97% branches / 67.81% functions (re-measured 2026-11-11, Node v26.13.0) |
| Playwright E2E (`web/e2e/`) | full UI against a real `ravn-server` + prebuilt `web/dist` | `playwright test` (`npm run test:e2e`) | 41 spec files + shared `fixtures.ts`/`helpers.ts`, **14,149** LOC, **189** tests (re-measured 2026-11-09) | **~47s** wall (default workers); one shared `ravn-server` per worker (+ a few isolated spins); prereqs built once per run by the dedicated `e2e/global-setup.ts` globalSetup — mtime-staleness check, and the server binary is staged via temp copy + atomic rename so a worker can never start a mid-write binary (no ETXTBSY race) | not measured (E2E is the integration proof) |

All three run in CI on every push/PR (`.github/workflows/ci.yml`,
`.forgejo/workflows/ci.yml`): `go test -race ./... -coverprofile=c.out -cover`
(via the shared `scripts/ci/go-test.sh`), `npm run test:unit:coverage`, then the
E2E suite after the web build and server binary are produced. The two
instrumented runs print their coverage reports and feed a shared ratcheting gate
(`scripts/ci/coverage-gate.sh`, floors in §2) that fails the pipeline on breach.

Race-detector caveat: `go-test.sh` keeps `-race` unconditionally EXCEPT on
linux/arm64, where it first probes with a trivial `-race` program and falls back
to a plain instrumented run (with a loud warning) when TSan aborts at process
startup — some ARM kernels/VA layouts reject the shadow-memory layout with
`FATAL: ThreadSanitizer: unsupported VMA range`, in which case NO Go test can
run there at all. amd64 runners never skip `-race`.

### Run them

```bash
make test        # Go tests (CGO off, no race) — fast local loop
go test -race ./...   # what CI runs; needs cgo + gcc/clang
cd web && npm run test:unit
cd web && npm run build && cd ..   # E2E serves web/dist
cd web && npx playwright test      # or: make e2e (builds as needed)
```

## 2. Coverage baselines (re-measured post-multiuser epic, 2026-11-09; previous pass 2026-10-31)

### Go — per package, statements (`go test ./... -cover`)

| Package | Coverage | Notes |
|---|---|---|
| internal/infrastructure/clock, idgen | 100% | |
| internal/domain | 91.8% | state machine + invariants + visibility/authorization policy well covered |
| cmd/ravn-exec | 90.8% | flag/env parse + spawn covered via `parseCLIArgs` seam (C2, f430f84) |
| cmd/ravn-server | 90.9% | wiring/flags/shutdown + multiuser boot-validation matrix on the `run()` seam (`main_multiuser_test.go`, eca9c1f) |
| internal/infrastructure/pubsub | 92.7% | |
| internal/application | 85.9% | batch rollups covered (C1, e5bc919); multiuser identity/tokens/memberships use cases added in the epic |
| internal/infrastructure/apiclient | 85.2% | auth + membership client paths now covered |
| internal/interface/http | 82.2% | handlers + security middleware + tiered auth chain & scoping (T3–T9) |
| internal/mcp | 82.9% | tools/resources/prompts |
| cmd/ravn-mcp | 80.8% | per-agent credential forwarding + strict identity mode (T6) |
| internal/infrastructure/pwhash | 82.7% | new package (epic T3): Argon2id PHC-format hash emit/verify + parameter parsing |
| internal/infrastructure/sqlite | 84.0% | migration 0011 + adversarial identity/member tests lifted it from 77.5% (T1/T9) |

The previously-known **0%-covered production functions** (all confirmed in use,
not dead code) are all at ≥80% now — measured post-epic: `ListWithCounts` 92.6%,
`ProjectReport` (apiclient) 100.0% (+ handler 95.2%), `CountByTask` 100.0%,
`OpenBlockersByTasks` 90.0%, `SubtaskProgressByParents` 88.9%,
`MaxAtByTasks` 85.7%, `OpenTaskCounts` 84.6% (C1, e5bc919).

### Web unit — per file (`npm run test:unit:coverage`)

Re-measured **2026-11-11** (frontend-modularization Phase 0; 13 per-module
test files + `test-helpers.ts`, 265 tests) with Node's built-in plain coverage
reporter on Node 26 (v26.13.0) — zero extra deps. Values are the reporter's
exact two-decimal output, descending by line %. Only `app.ts` shifted since
the 2026-11-09 pass (it grew past the then-audited size); every other row is
byte-identical to that pass.

| File | Lines | Branches | Functions |
|---|---|---|---|
| src/drafts.ts | 100.00% | 100.00% | 100.00% |
| src/board-scope.ts | 100.00% | 100.00% | 100.00% |
| src/moves.ts | 97.44% | 94.74% | 88.89% |
| src/members.ts | 95.83% | 95.16% | 100.00% |
| src/refresh.ts | 94.34% | 100.00% | 89.47% |
| src/identity.ts | 94.05% | 91.67% | 100.00% |
| src/focus.ts | 93.85% | 90.91% | 100.00% |
| src/api.ts | 85.89% | 95.71% | 89.66% |
| src/util.ts | 84.40% | 87.36% | 66.67% |
| src/realtime.ts | 79.38% | 95.45% | 100.00% |
| src/md.ts | 68.63% | 87.50% | 100.00% |
| src/app.ts | 67.54% | 68.16% | 59.61% |
| src/types.ts | 40.12% | 100.00% | 100.00% |
| **All files (total)** | **70.55%** | **75.97%** | **67.81%** |

The new `web/test/file-size.test.ts` guardrail test adds no covered production
code (it reads the file system only), so it is invisible to this table.

The epic's new pure modules land covered by construction — `board-scope.ts`
(project visibility per identity) at 100%, `members.ts` (T9 roster helpers) and
`identity.ts` (login/session/tokens client + display-name rules) at ≥94% lines.
`api.ts` dipped 88.21% → 85.89% because the multiuser client surface grew
(auth/members endpoints); `focus.ts` stays at 93.85%.

The total line coverage is the ratcheting baseline: it must stay ≥ 65% (the web
unit floor) and the Go total must stay ≥ 80% (the Go statement floor). Both are
enforced in CI by `scripts/ci/coverage-gate.sh` — see "How to ratchet the
floors" below.

Notes:

- `web/src/app.ts` is a **6,448-line** monolith (measured 2026-11-11) holding
  most UI state; its low *unit* coverage is partly structural — large DOM/init
  sections are only reachable through E2E. Extracting pure logic from app.ts
  (as was done for moves/refresh/realtime/drafts) is the lever that keeps
  raising unit coverage without adding brittle DOM tests; the full extraction
  plan (target layout, per-concern ownership map, contracts, checklist, and
  the §6 file-size guardrail design) lives in
  [`docs/FRONTEND_ARCHITECTURE.md`](./FRONTEND_ARCHITECTURE.md).
- `src/types.ts` is the wire-contract module: mostly `interface`/type
  declarations plus a handful of const tables (`STATUSES`, `COMMENT_KINDS`, …).
  Every runtime line it has executes (100% functions); the uncovered lines are
  type-declaration residue, so its low line % is expected and not a target.

#### Extracting the total for CI gates

The plain reporter writes the table to **stdout** with a stable total line
(exactly as printed, including spacing):

    ℹ all files    |  70.58 |    75.97 |   67.81 |

Pipe-friendly one-liner (POSIX awk; prints `69.35` = total line %; the suite's
own pass/fail exit code is preserved by piping only after the command). The
match is on row *content*, not the prefix, because the prefix differs between
Node versions:

    npm run test:unit:coverage 2>/dev/null \
      | awk -F'|' '$1 ~ /all files/ { gsub(/[[:space:]]/, "", $2); print $2 }'

Caveat (measured on Node 26.12.1): `--test-reporter=json` is currently broken —
Node mis-parses the value and exits 7 with
`ERR_MODULE_NOT_FOUND: Cannot find package 'json'` (`tap`/`spec` work fine).
Parse the plain table instead; revisit if a future Node release fixes it.

Caveat (measured 2026-10-31, Node v22 vs v26): the CI runners pin **Node v22**
(`setup-node` in both `ci.yml` files), whose plain reporter prefixes report
rows with `#` instead of `ℹ` and — more importantly — **includes the test files
themselves** (`web/test/*.test.ts`, `test-helpers.ts`) in the table. So the CI
total reads higher than a modern local Node (v26, which reports `src/` only):
post-multiuser the v26 total is 70.58% (re-measure on your runner's Node if you
need its exact number). Both comfortably clear the 65% floor; the gate matches the total
row by content so it works on both. If you bump the runner's Node major, re-check
which file set is reported before ratcheting (see below).

### How to ratchet the floors

CI enforces two floors via `scripts/ci/coverage-gate.sh`, called from an
identical "Coverage gate (ratcheting floors)" step in **both**
`.github/workflows/ci.yml` and `.forgejo/workflows/ci.yml`. The script is the
single source of truth — the workflows only invoke it — so the two pipelines
cannot drift:

- `GO_FLOOR=80` — total Go statement coverage, parsed from
  `go tool cover -func=c.out | tail -1` (profile written by
  `go test -race ./... -coverprofile=c.out -cover`).
- `WEB_FLOOR=65` — web unit total line coverage, parsed from the "all files"
  row of `npm run test:unit:coverage` (captured to `web-unit-coverage.log`).

Ratchet procedure (only once actual coverage has gone up *and stayed up*):

1. Raise real coverage first — land the new tests and let CI show the new
   totals for a few runs.
2. Bump the matching constant at the top of `scripts/ci/coverage-gate.sh` to
   the new measured total (or slightly under it, so unrelated work isn't
   blocked).
3. Update the baseline note in this section so doc and gate agree, and refresh
   the per-package / per-file tables above with a re-measurement.

Never lower a floor; never bump one without the matching coverage change — the
point is that unrelated work never has to touch coverage, but covered ground is
never re-lost. To verify the gate itself locally without touching real
coverage, override the env floors (e.g. `GO_FLOOR=99 WEB_FLOOR=99 scripts/ci/
coverage-gate.sh c.out web-unit-coverage.log` must FAIL; defaults must PASS).

Measured totals as of 2026-11-09 (post multi-user epic): Go **84.5%**
statements (`go tool cover -func` total line); web unit total lines **70.58%**
on Node 26 (`npm run test:unit:coverage` all-files row) — both floors have
headroom. Previous pass (2026-10-31, post test-quality epic): Go 83.9% / web
69.35% lines on Node 26. Latest re-measurement (2026-11-11, modularization
Phase 0): web unit total lines **70.55%** on Node v26.13.0 — a −0.03 drift as
`app.ts` grew past the audited size; floors untouched, headroom intact.

### How to measure

```bash
# Go: per-package totals
go test ./... -count=1 -cover
# Go: detailed profile
go test ./... -count=1 -coverprofile=/tmp/c.out && go tool cover -func=/tmp/c.out | tail -1

# Web unit (Node >= 23; repo CI uses Node 26) — no extra deps
cd web && node --import jiti/register --test --experimental-test-coverage test/*.test.ts
# ...or the identical invocation via npm script:
cd web && npm run test:unit:coverage
```

## 3. Conventions

### Go tests

- Table-driven tests with named cases; keep one concern per `func Test...`.
- Adversarial/hostile-input tests live in `*_adversarial_test.go`
  (timestamp boundaries, lease races, auth gating, body limits). Do not use
  milestone-style names like `stress_rN` — the legacy stress/challenger files
  are consolidated into the per-package adversarial test files.
- Shared setup per package: sqlite tests use `openTestDB(t)` from db_test.go;
  http tests use the `newStack`/`doReq` helpers in http_test.go. Reuse these —
  do not re-create pools or clients per test file.
- `t.Parallel()` is fine and used throughout the http package; note that
  parallel tests make `-json` wall-time accounting misleading when profiling.
  To find a slow test, run it standalone: `go test -run '^TestName$'`.
- The SSE handler's per-write deadline is injectable via
  `Deps.SSEWriteDeadline` (`internal/interface/http/app.go`), defaulting to the
  production 10s constant (`DefaultSSEWriteDeadline = 10 * time.Second`; landed
  in 41a6479). Tests that depend on it must inject a short value — the
  stalled-client adversarial test uses 250ms, and `TestEvents_SSE_DefaultWriteDeadline`
  pins the production default — never add `time.Sleep(10 * time.Second)` style waits.

### Web unit tests

- `node:test` + `assert/strict`, run via jiti for TS support; no other test
  framework or DOM library. Keep it that way: the suite is dependency-light by
  design (only `alpinejs` and `marked` are runtime deps).
- Test titles carry a module prefix (`"MultiSelect: ..."`, `"Notes: ..."`)
  because the files historically were one blob; keep prefixes when adding tests.
- Browser globals (`window`, `localStorage`) are faked manually per test with
  save/restore in `finally`. If you find yourself copying that scaffolding a
  third time, extract a shared helper in `web/test/`.
- Prefer testing the *extracted* pure functions (util, moves, refresh,
  realtime, drafts) over reaching into app.ts DOM code; E2E covers the DOM.
- File-size guardrail: `web/test/file-size.test.ts` caps every `.ts` under
  `web/src/` at **800 lines**, with an explicit allowlist that currently holds
  only `app.ts` (6,448 lines) and can only shrink — a second test fails on
  stale or phantom entries. It runs inside the normal `npm run test:unit`
  glob, so both CI pipelines catch violations automatically. Design and
  rules: [`docs/FRONTEND_ARCHITECTURE.md`](./FRONTEND_ARCHITECTURE.md) §6.

### E2E tests (Playwright)

- Every spec imports `{ test, expect }` from `./fixtures` (not directly from
  `@playwright/test`). The fixture boots **one shared `bin/ravn-server` per
  worker** on a random free port with a temp DB and serves prebuilt `web/dist`
  (never :8080), exposed as the worker-scoped `serverUrl`. Read it by
  destructuring — `test.beforeAll(async ({ serverUrl }) => { … })` for seeding,
  or directly in a test body. No per-spec server scaffolding remains in any spec.
- Need an **empty/untouched** DB, a **global** count, **token-required** mode, or
  disk access to the data dir? Opt that single test into the fixture's
  test-scoped `isolatedServer` (`{ url, dataDir, artifactsDir }`) — it boots a
  private server+DB for just that test. Override its env with
  `test.use({ isolatedServerEnv: { RAVN_TOKEN } })`. Reserve the imported
  `getFreePort()` for non-ravn scratch/mock servers only (used by
  `board.spec.ts` for its mock OpenCode provider).
- **Data isolation contract**: specs must never assume an empty database. Seed,
  then select your own project by the id your seed returned (via
  `selectProjectViaDropdown(page, id)`) or deep-link `?project=<id>`. Any test
  asserting a **global** count, a **first-visit/empty** state, or token-required
  behaviour MUST use `isolatedServer` — do not rely on the shared worker DB being
  pristine. (Fixed project names are fine here because selection is by id and the
  board is scoped to the selected project; duplicates from sibling specs in the
  same worker never leak into your assertions.)
- Screenshot/artifact output dirs may still import `fs`/`path`/`fileURLToPath`
  (e.g. `hero-screenshot`, `claim-expired`, `md-table-overflow`,
  `attention-badges`, `stalled-filter`). These are the only remaining module
  imports in specs — never server scaffolding.
- `use.screenshot: 'on'` captures a screenshot per test (passing included)
  into `web/test-results/` for QA evidence. These artifacts are ephemeral —
  never commit them; attach task-relevant ones to Rävn tasks via
  `attach_artifact` with `data_base64` (see AGENTS.md).
- Prefer auto-retrying `expect()` assertions over `setTimeout` sleeps when
  waiting for UI state. **E2E flake policy** (task 01M66ZGEAXJV3KENXW07CXJVY9):
  `playwright.config.ts` sets `retries: process.env.CI ? 1 : 0` — CI retries a test
  once so a single transient failure does not force a full-suite re-run (the
  most expensive run in the repo after Go); locally retries stay 0 so failures
  surface immediately (run a failed spec twice before assuming regression). A pass
  *via* its retry is NOT silently green: `e2e/flake-reporter.ts` (wired as the
  second reporter) walks the finished suite in `onEnd` and logs every test whose
  final attempt passed after ≥1 failed attempt. Any remaining `setTimeout` in a
   spec must be intentional pacing (simulated API latency, gesture/sample cadence),
   never an assertion wait — audit with
   `rg "setTimeout|new Promise" e2e/*.spec.ts`. Live UI state that reads the app's
   reactive 1s `nowTick` (stalled count/filter, muted capsule styling) is observed
   via bounded ≤3s auto-retrying `expect()s` — no pre-sleep. One exception: a card's
   claim-hint TEXT (`claimHint()` in util.ts) reads `Date.now()` directly and does
   NOT re-render on `nowTick`, so `claim-expired` reaches its KNOWN expiry instant
   with a single bounded sleep before first paint (replacing the old unbounded
   busy-poll), then still guards the flip with a ≤3s timeout.
- Strict locators: exact-match `getByText(..., { exact: true })` or scoping to
  a container/role — Playwright strict-mode violations are a common CI failure.
- The deterministic hero screenshot is `e2e/hero-screenshot.spec.ts`;
  refresh workflow for `docs/assets/hero.png` is in AGENTS.md (do not commit
  it without explicit instruction).

## 4. Performance budgets

Measured on a 12-core / 64GB Linux box, 2026-10-30; "Current" actuals
re-measured fresh post multi-user epic (2026-11-09). CI targets 2 workers for E2E.

| Budget | Current (measured 2026-11-09) | Target |
|---|---|---|
| Go full suite (no race) | **~7.1s**; slowest package `internal/interface/http` ~3.3s wall | ≤ 30s; `internal/interface/http` ≤ 4s — met via the §3 SSE deadline seam (A1, 41a6479) |
| Go full suite (`-race`, CI) | **24.2s** | ≤ 60s |
| Web unit | **~8.0s** (263 tests) | ≤ 15s |
| E2E (local, default workers) | **~47s** (189 passed) | ≤ 60s; one shared `ravn-server` per worker (+ a few isolated spins) — realized by the §3 fixture; prereqs built once in the globalSetup hook (§1) |
| E2E memory | ~6GB peak chromium; `ravn-server` count bounded by worker count, not spec count (fixture) | keep total < 8GB on a 16GB runner box |

Rules for keeping the budgets:

- No test may wait longer than 3s for an *injected* timeout; production
  deadlines belong behind configurable seams. The stalled-SSE-client zombie
  check stays end-to-end but runs against a short injected deadline (the
  10s production default is pinned separately — see §3).
- E2E specs must not rebuild `web/dist` or the server binary per test —
  build once in `beforeAll` (existing pattern) or let the fixture handle it.
- Keep the Go test:source LOC ratio near 2:1; large additions need justification
  (adversarial value) and a note in the commit message.

## 5. Findings index (task traceability)

Audit results from 2026-10-30, each mapped to a Ravn board task:

All ten audit findings are **resolved** as of the post-epic verification (2026-10-31):

| # | Finding | Task |
|---|---|---|
| 1 | **Resolved (2026-10-30, 41a6479)**: `TestAdversarial_SSE_StalledClientPipeWriteDeadline` took 10.3s on the hard-coded 10s SSE write deadline; the deadline is now injectable via `Deps.SSEWriteDeadline` (production default stays 10s, pinned by `TestEvents_SSE_DefaultWriteDeadline`), the stalled-client test uses an injected 250ms (10.3s → 0.26s), the http package fell 12.7s → ~3.2s wall and the full Go suite 23s → 3.6s | [Perf/Test] Make SSE per-write deadline configurable (P5) — done; see §3 Go |
| 2 | **Resolved (2026-10-30, 33e1255)**: per-spec scaffolding (~1,000 LOC, 79 hooks, 38 startups/run) replaced by a worker-scoped Playwright server fixture; `web/e2e/` total LOC fell 15,727 → 12,848 (−2,879) despite the shared fixture/helpers being added on top | [E2E] Worker-scoped Playwright server fixture (P5) — done; see §3 E2E |
| 3 | **Resolved (2026-10-31, ad85bb8)**: the 4,965-line monolith was split into 9 per-module files + `test-helpers.ts`; all original test titles byte-identical | [Unit/Web] Split into per-module files (P3) — done; see §3 Web unit |
| 4 | **Resolved (2026-10-31, 7a93084)**: no coverage measurement existed for web unit; `npm run test:unit:coverage` added, per-file baselines re-measured post-split (§2, incl. the stable total line + parse recipe for CI) | [Unit/Web] Coverage script + baselines (P3) — done; unblocks #8 |
| 5 | **Resolved (2026-10-31, e5bc919)**: all seven batch rollup queries that sat at 0% despite production use now measure ≥80% (§2 lists the exact values); Go total rose to 83.9% | [Unit/Go] Cover zero-coverage batch aggregates (P3) — done; see §2 |
| 6 | **Resolved (2026-10-31, f430f84)**: cmd/ravn-exec 45.2% → 90.8%, cmd/ravn-server 62.5% → 90.1%; flag/env/shutdown paths covered via the `parseCLIArgs`/`run()` seams with real end-to-end fakes and full-boot tests | [Unit/Go] Raise cmd package coverage (P3) — done; see §2 |
| 7 | **Resolved (2026-10-31, 44bfcfd)**: api.ts lines 71.37% → 88.21% (funcs 47.92% → 97.92%), focus.ts lines 52.31% → 93.85% (funcs 41.67% → 100%) via mocked-fetch + hand-rolled DOM-fake unit tests | [Unit/Web] Close api.ts/focus.ts gaps (P3) — done; see §2 |
| 8 | **Resolved (2026-10-31, 53e53d3)**: CI now runs both instrumented suites once each, prints per-package (Go) and per-file (web unit) coverage plus totals, and enforces ratcheting floors (Go ≥ 80% statements, web unit ≥ 65% lines) via a shared `scripts/ci/coverage-gate.sh` step present in both workflows — see §2 "How to ratchet the floors" | [CI] Coverage reporting + ratcheting gates (P3; blocked by #4) — done |
| 9 | **Resolved (2026-10-31, 2d9f294)**: E2E flake policy landed — `retries: process.env.CI ? 1 : 0` in `playwright.config.ts`, plus an `e2e/flake-reporter.ts` `onEnd` hook that surfaces pass-via-retry (flaky) tests; the wall-clock expiry waits in `claim-expired`/`stalled-filter` (`ttl_sec=1`) were replaced by bounded ≤3s auto-retrying `expect()s` (the app re-renders expired/stalled state live via a 1s `nowTick`; the one exception is the claim-hint TEXT, which reads `Date.now()` directly — see §3 E2E) | [E2E] Flake policy: bounded retries + timing audit (P3; blocked by #2) — done; see §3 E2E |
| 10 | **Resolved (2026-10-31, 7f976e2 + 2954d06)**: −814 LOC of verified pure duplication removed from the legacy "challenger/stress_rN" files with ZERO coverage regression (per-function diff of before/after coverprofiles: 463 functions, none dropped); Go test LOC 38,894 → 38,080 (net growth vs the pre-epic 37,182 baseline is real added coverage tests, §2). Naming standardized: hostile-input tests live in `*_adversarial_test.go` per package (`cmd/ravn-mcp/adversarial_test.go` kept its descriptive name), no milestone-style names remain repo-wide | [Test] Deduplicate adversarial Go tests (P2) — done; see §3 Go |

Not actioned (deliberate, documented): per-test screenshots on passing tests
(QA evidence workflow, AGENTS.md); `fullyParallel: false` with file-level
parallelism (current wall time is fine); absence of a vitest/jest framework
(node:test + jiti is intentionally dependency-light).

## Post-epic state (2026-10-30)

Epic `01M6735WRK6XC46HQCSYGAEYVC` (test-suite audit remediation, 10 children)
is complete. Final verification pass (fresh full-suite run, 2026-10-31,
12-core / 64GB Linux):

- **Go total statement coverage: 83.9%** (floor ≥80%). All seven former
  zero-coverage batch rollup functions now measure ≥80% (§2). Per-package
  minimum is sqlite at 77.5%.
- **Web unit line coverage: 69.35%** locally on Node 26 (`src/` only); the
  CI-pinned Node 22 reporter reads ~82.35% (it includes `web/test/*.test.ts`)
  — both clear the ≥65% floor. api.ts 88.21% / focus.ts 93.85% lines; app.ts
  (66.54%) is now the largest remaining gap.
- **Go test LOC: 38,080** across 91 `*_test.go` files (pre-epic baseline
  37,182). The dedupe removed −814 lines of verified-safe pure duplication with
  zero coverage regression; the net growth over the baseline is real added
  coverage tests (C1/C2/B1–B3). Naming convention in effect: hostile-input
  tests live in `*_adversarial_test.go`; no `stress_rN`/milestone-style names
  remain (a repo-wide grep finds only a descriptive actor name `"challenger"`
  in fixture data, not milestone naming).
- **CI floors active**: Go ≥80% statements and web unit ≥65% lines are enforced
  in both `.github/workflows/ci.yml` and `.forgejo/workflows/ci.yml` via the
  shared `scripts/ci/coverage-gate.sh` step (E1, 53e53d3). Ratchet procedure:
  §2.
- **E2E flake policy**: `retries: process.env.CI ? 1 : 0` + `e2e/flake-reporter.ts`
  surfacing pass-only-via-retry tests (D2, 2d9f294). Suite: 175 tests green in
  ~43s wall; one shared `ravn-server` per worker with a bounded
  `isolatedServer` opt-out for the few global-state specs.
- All ten child tasks are status=done with linked/attached evidence (commit
  SHAs in the findings index above).

## 6. What "good" looks like for new tests

1. **One behavior per test**, named after the behavior, not the mechanism.
   Reference the AC id when the test exists to verify a task's acceptance
   criterion (pattern already used: `AC1`, `AC2` in E2E titles).
2. **Deterministic**: no reliance on wall-clock except where the production
   behavior *is* time-based — then make the clock/TTL injectable or bound the
   wait with a polling assertion.
3. **Fast path first**: unit-test the pure function; let E2E be the thin
   integration proof. If a DOM interaction needs 50 lines of assertions in two
   places, extract it into `web/e2e/helpers.ts`.
4. **No sleep-for-UI**. Poll with `expect()`, or drive state through the API.
5. **Leave evidence**: E2E screenshots are automatic; attach what proves the
   AC to the Rävn task before closing it.
6. **Keep budgets**: if your change adds > 5s to any suite, fix the wait or
   split the test — don't raise the budget silently.
