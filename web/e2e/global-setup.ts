// Playwright globalSetup (task: E2E fixture hardening, multi-user docs ticket).
//
// Runs ONCE in the runner's own process BEFORE any worker starts, so the
// freshness-aware build-checks (rebuild web/dist + bin/ravn-server when their
// sources are newer) happen exactly once per suite run — never concurrently
// from two workers. Without this, each worker's first bootServer() could
// independently decide the binary is stale and `go build` over it while another
// worker is spawning/executing it → `spawn ETXTBSY`. The per-boot check in
// fixtures.ts remains as a cheap no-op safety net (its artifacts are fresh by
// the time any worker runs).
import { ensurePrereqs } from './fixtures';

export default function globalSetup(): void {
  ensurePrereqs();
}
