# Testing Strategy & Best Practices

Audited 2026-10-30. This document is the source of truth for how ravn is tested,
the measured baselines, and the conventions new tests must follow. Improvements
are tracked as tasks on the Ravn board (see [Findings index](#findings-index-task-traceability)).

## 1. The three suites

| Suite | What it covers | Runner | Size (2026-10-30) | Wall time (local, 12-core) | Coverage |
|---|---|---|---|---|---|
| Go unit + integration (`internal/`, `cmd/`) | domain rules, application use-cases, SQLite repos, HTTP handlers, MCP tools | `go test` (+ `-race` in CI) | 86 files, 37,182 LOC (vs 16,948 source LOC → 2.2:1) | **23s** total; `-race` **22s**; slowest package `internal/interface/http` 12.7s | **80.9%** statements total |
| Web unit (`web/test/`) | pure logic of `web/src/*.ts` (dates, markdown, drafts, API client, board state, moves, refresh, realtime) | `node:test` via `jiti` (`npm run test:unit`) | 1 file, 4,965 LOC, 190 tests | **~8s** | **67.0%** lines / 75.9% branches / 60.5% functions |
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

### Web unit — per file (`node --import jiti/register --test --experimental-test-coverage`)

| File | Lines | Branches | Functions |
|---|---|---|---|
| src/drafts.ts | 100% | 100% | 100% |
| src/moves.ts | 97.4% | 94.7% | 88.9% |
| src/refresh.ts | 94.3% | 100% | 89.5% |
| src/realtime.ts | 78.6% | 95.5% | 100% |
| src/util.ts | 79.8% | 85.3% | 54.8% |
| src/api.ts | 69.7% | 69.1% | 47.9% |
| src/md.ts | 68.6% | 87.5% | 100% |
| src/app.ts | 66.5% | 73.1% | 59.3% |
| src/focus.ts | 52.3% | 100% | 41.7% |

Note: `web/src/app.ts` is a ~5,300-line module holding most UI state; its
low *unit* coverage is partly structural — large DOM/init sections are only
reachable through E2E. Extracting pure logic from app.ts (as was done for
moves/refresh/realtime/drafts) is the lever that keeps raising unit coverage
without adding brittle DOM tests.

### How to measure

```bash
# Go: per-package totals
go test ./... -count=1 -cover
# Go: detailed profile
go test ./... -count=1 -coverprofile=/tmp/c.out && go tool cover -func=/tmp/c.out | tail -1

# Web unit (Node >= 23; repo CI uses Node 26) — no extra deps
cd web && node --import jiti/register --test --experimental-test-coverage test/*.test.ts
```

## 3. Conventions

### Go tests

- Table-driven tests with named cases; keep one concern per `func Test...`.
- Adversarial/hostile-input tests live in `*_adversarial_test.go`
  (timestamp boundaries, lease races, auth gating, body limits). Do not use
  milestone-style names like `stress_rN` for new files (legacy ones are being
  consolidated — see findings index).
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
  waiting for UI state. Flakes: CI uses bounded retries once the flake policy
  task lands; locally run a failed spec twice before assuming regression.
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
| 4 | No coverage measurement for web unit anywhere; baseline now recorded above | [Unit/Web] Coverage script + baselines (P3) |
| 5 | Batch rollup queries (`OpenTaskCounts`, `ProjectReport` agg, `OpenBlockersByTasks`, …) at 0% despite production use | [Unit/Go] Cover zero-coverage batch aggregates (P3) |
| 6 | cmd/ravn-exec 45.2%, cmd/ravn-server 62.5% — lowest packages; flag/env/shutdown paths untested | [Unit/Go] Raise cmd package coverage (P3) |
| 7 | api.ts 69.7% lines (SSE + error paths), focus.ts 41.7% functions | [Unit/Web] Close api.ts/focus.ts gaps (P3) |
| 8 | CI reports no coverage and enforces no floor | [CI] Coverage reporting + ratcheting gates (P3; blocked by #4) |
| 9 | No retry policy in Playwright config; wall-clock-dependent specs (`ttl_sec=1` lease, transition waits) can race on slow CI | [E2E] Flake policy: bounded retries + timing audit (P3) |
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
