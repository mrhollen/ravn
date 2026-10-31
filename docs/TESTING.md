# Testing Strategy & Best Practices

Audited 2026-10-30. This document is the source of truth for how ravn is tested,
the measured baselines, and the conventions new tests must follow. Improvements
are tracked as tasks on the Ravn board (see [Findings index](#findings-index-task-traceability)).

## 1. The three suites

| Suite | What it covers | Runner | Size (2026-10-30) | Wall time (local, 12-core) | Coverage |
|---|---|---|---|---|---|
| Go unit + integration (`internal/`, `cmd/`) | domain rules, application use-cases, SQLite repos, HTTP handlers, MCP tools | `go test` (+ `-race` in CI) | 86 files, 37,182 LOC (vs 16,948 source LOC → 2.2:1) | **23s** total; `-race` **22s**; slowest package `internal/interface/http` 12.7s | **80.9%** statements total |
| Web unit (`web/test/`) | pure logic of `web/src/*.ts` (dates, markdown, drafts, API client, board state, moves, refresh, realtime) | `node:test` via `jiti` (`npm run test:unit`; coverage: `npm run test:unit:coverage`) | 9 module files + `test-helpers.ts`, 4,834 LOC, 190 tests | **~8s** | **67.2%** lines / 76.4% branches / 60.5% functions (re-measured post-split) |
| Playwright E2E (`web/e2e/`) | full UI against a real `ravn-server` + prebuilt `web/dist` | `playwright test` (`npm run test:e2e`) | 38 spec files + shared `fixtures.ts`/`helpers.ts`, **12,848** LOC (was 15,727), 175 tests | **~41s** wall (default workers); one shared `ravn-server` per worker (+ a few isolated spins) | not measured (E2E is the integration proof) |

All three run in CI on every push/PR (`.github/workflows/ci.yml`,
`.forgejo/workflows/ci.yml`): `go test -race ./...`, `npm run test:unit`,
then the E2E suite after the web build and server binary are produced.

### Run them

```bash
make test        # Go tests (CGO off, no race) — fast local loop
go test -race ./...   # what CI runs; needs cgo + gcc/clang
cd web && npm run test:unit
cd web && npm run build && cd ..   # E2E serves web/dist
cd web && npx playwright test      # or: make e2e (builds as needed)
```

## 2. Coverage baselines (2026-10-30)

### Go — per package, statements (`go test ./... -cover`)

| Package | Coverage | Notes |
|---|---|---|
| internal/infrastructure/clock, idgen | 100% | |
| internal/domain | 91.0% | state machine + invariants well covered |
| internal/infrastructure/pubsub | 88.8% | |
| internal/application | 84.9% | hides untested batch rollups (see findings) |
| internal/interface/http | 83.4% | handlers + security middleware |
| internal/mcp | 82.4% | tools/resources/prompts |
| cmd/ravn-mcp | 80.7% | |
| internal/infrastructure/apiclient | 79.8% | |
| internal/infrastructure/sqlite | 70.3% | |
| cmd/ravn-server | 62.5% | wiring/flags untested (see findings) |
| cmd/ravn-exec | 45.2% | `runCLI`/`runForeground` at 0% (see findings) |

Known **0%-covered production functions** (all confirmed in use, not dead code):
`OpenTaskCounts`, `ListWithCounts`, `ProjectReport` aggregation,
`OpenBlockersByTasks`, `SubtaskProgressByParents`, `CountByTask`, `MaxAtByTasks`.

### Web unit — per file (`npm run test:unit:coverage`)

Re-measured post-split (9 per-module test files + `test-helpers.ts`, 190 tests)
with Node's built-in plain coverage reporter on Node 26 — zero extra deps.
Values are the reporter's exact two-decimal output, descending by line %.

| File | Lines | Branches | Functions |
|---|---|---|---|
| src/drafts.ts | 100.00% | 100.00% | 100.00% |
| src/moves.ts | 97.44% | 94.74% | 88.89% |
| src/refresh.ts | 94.34% | 100.00% | 89.47% |
| src/util.ts | 80.06% | 86.76% | 54.84% |
| src/realtime.ts | 79.25% | 95.45% | 100.00% |
| src/api.ts | 71.37% | 77.46% | 47.92% |
| src/md.ts | 68.63% | 87.50% | 100.00% |
| src/app.ts | 66.54% | 73.10% | 59.28% |
| src/focus.ts | 52.31% | 100.00% | 41.67% |
| src/types.ts | 37.98% | 100.00% | 100.00% |
| **All files (total)** | **67.18%** | **76.44%** | **60.48%** |

The total line coverage is the ratcheting baseline: it must stay ≥ 65%.

Notes:

- `web/src/app.ts` is a ~5,300-line module holding most UI state; its
  low *unit* coverage is partly structural — large DOM/init sections are only
  reachable through E2E. Extracting pure logic from app.ts (as was done for
  moves/refresh/realtime/drafts) is the lever that keeps raising unit coverage
  without adding brittle DOM tests.
- `src/types.ts` is the wire-contract module: mostly `interface`/type
  declarations plus a handful of const tables (`STATUSES`, `COMMENT_KINDS`, …).
  Every runtime line it has executes (100% functions); the uncovered lines are
  type-declaration residue, so its low line % is expected and not a target.

#### Extracting the total for CI gates

The plain reporter writes the table to **stdout** with a stable total line
(exactly as printed, including spacing):

    ℹ all files    |  67.18 |    76.44 |   60.48 |

Pipe-friendly one-liner (POSIX awk; prints `67.18` = total line %; the suite's
own pass/fail exit code is preserved by piping only after the command):

    npm run test:unit:coverage 2>/dev/null \
      | awk -F'|' '/^ℹ all files/ { gsub(/ /, "", $2); print $2 }'

Caveat (measured on Node 26.12.1): `--test-reporter=json` is currently broken —
Node mis-parses the value and exits 7 with
`ERR_MODULE_NOT_FOUND: Cannot find package 'json'` (`tap`/`spec` work fine).
Parse the plain table instead; revisit if a future Node release fixes it.

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
- The SSE handler's per-write deadline is a 10s production constant
  (`internal/interface/http/handlers_events.go`). Tests that depend on it
  should use a short injected value once the deadline becomes configurable
  (tracked as a task) — never add `time.Sleep(10 * time.Second)` style waits.

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

Measured on a 12-core / 64GB Linux box, 2026-10-30. CI targets 2 workers for E2E.

| Budget | Current | Target |
|---|---|---|
| Go full suite (no race) | 23s | ≤ 30s; `internal/interface/http` ≤ 4s once the SSE deadline task lands |
| Go full suite (`-race`, CI) | 22s | ≤ 60s |
| Web unit | ~8s | ≤ 15s |
| E2E (local, default workers) | ~41s | ≤ 60s; one shared `ravn-server` per worker (+ a few isolated spins) — realized by the §3 fixture |
| E2E memory | ~6GB peak chromium; server count now bounded by worker count, not spec count | keep total < 8GB on a 16GB runner box |
| E2E memory | ~6GB peak chromium; `ravn-server` count bounded by worker count, not spec count (fixture) | keep total < 8GB on a 16GB runner box |

Rules for keeping the budgets:

- No test may wait longer than 3s for an *injected* timeout; production
  deadlines belong behind configurable seams. The single exception is the
  stalled-SSE-client zombie check, which must stay end-to-end but with a short
  deadline (tracked).
- E2E specs must not rebuild `web/dist` or the server binary per test —
  build once in `beforeAll` (existing pattern) or let the fixture handle it.
- Keep the Go test:source LOC ratio near 2:1; large additions need justification
  (adversarial value) and a note in the commit message.

## 5. Findings index (task traceability)

Audit results from 2026-10-30, each mapped to a Ravn board task:

| # | Finding | Task |
|---|---|---|
| 1 | `TestAdversarial_SSE_StalledClientPipeWriteDeadline` takes 10.3s on the hard-coded 10s SSE write deadline; dominates the http package (12.7s of 23s total) | [Perf/Test] Make SSE per-write deadline configurable (P5) |
| 2 | **Resolved (2026-10-30)**: per-spec scaffolding (~1,000 LOC, 79 hooks, 38 startups/run) replaced by a worker-scoped Playwright server fixture; `web/e2e/` total LOC fell 15,727 → 12,848 (−2,879) despite the shared fixture/helpers being added on top | [E2E] Worker-scoped Playwright server fixture (P5) — done; see §3 E2E |
| 3 | Web unit suite is one 4,965-line file; failures hard to localize | [Unit/Web] Split into per-module files (P3) |
| 4 | **Resolved**: no coverage measurement existed for web unit; `npm run test:unit:coverage` added, per-file baselines re-measured post-split (§2, incl. the stable total line + parse recipe for CI) | [Unit/Web] Coverage script + baselines (P3) — done; unblocks #8 |
| 5 | Batch rollup queries (`OpenTaskCounts`, `ProjectReport` agg, `OpenBlockersByTasks`, …) at 0% despite production use | [Unit/Go] Cover zero-coverage batch aggregates (P3) |
| 6 | cmd/ravn-exec 45.2%, cmd/ravn-server 62.5% — lowest packages; flag/env/shutdown paths untested | [Unit/Go] Raise cmd package coverage (P3) |
| 7 | api.ts 69.7% lines (SSE + error paths), focus.ts 41.7% functions | [Unit/Web] Close api.ts/focus.ts gaps (P3) |
| 8 | CI reports no coverage and enforces no floor | [CI] Coverage reporting + ratcheting gates (P3; blocked by #4) |
| 9 | **Resolved (2026-10-30)**: E2E flake policy landed — `retries: process.env.CI ? 1 : 0` in `playwright.config.ts`, plus an `e2e/flake-reporter.ts` `onEnd` hook that surfaces pass-via-retry (flaky) tests; the wall-clock expiry waits in `claim-expired`/`stalled-filter` (`ttl_sec=1`) were replaced by bounded ≤3s auto-retrying `expect()s` (the app re-renders expired/stalled state live via a 1s `nowTick`) | [E2E] Flake policy: bounded retries + timing audit (P3) — done; see §3 E2E |
| 10 | Test LOC ratio 2.2:1 with legacy "challenger/stress_rN" files overlapping regular tests; inconsistent naming | [Test] Deduplicate adversarial Go tests (P2) |

Not actioned (deliberate, documented): per-test screenshots on passing tests
(QA evidence workflow, AGENTS.md); `fullyParallel: false` with file-level
parallelism (current wall time is fine); absence of a vitest/jest framework
(node:test + jiti is intentionally dependency-light).

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
