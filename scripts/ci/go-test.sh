#!/usr/bin/env bash
# go-test.sh — CI Go test step.
#
# Invoked from BOTH .github/workflows/ci.yml and .forgejo/workflows/ci.yml
# (identical steps) so the two pipelines cannot drift. Runs the instrumented
# suite: per-package statement coverage on screen + merged profile in c.out
# for the Coverage gate step (docs/TESTING.md §2).
#
# Race detector policy: -race is always used, EXCEPT where the runtime's
# TSan-based race detector cannot start. On some linux/arm64 kernels the
# sanitizer aborts every test binary at process startup with
#   FATAL: ThreadSanitizer: unsupported VMA range
# (kernel VA_BITS/shadow-memory layout mismatch) — no Go program, let alone
# a test suite, can run there. For those hosts we probe once with a trivial
# -race program and fall back to a plain instrumented run with a loud
# warning instead of failing the pipeline. The probe only runs on arm64:
# amd64 runners keep an unconditional -race so a missing cgo toolchain
# still fails loudly rather than silently weakening the suite.

set -euo pipefail

RACE="-race"
if [ "$(go env GOARCH)" = "arm64" ]; then
  PROBE="$(mktemp -d)"
  trap 'rm -rf "$PROBE"' EXIT
  printf 'package main\nfunc main() {}\n' > "${PROBE}/main.go"
  if ! (cd "$PROBE" && go mod init ci-race-probe >/dev/null 2>&1 && go run -race . >/dev/null 2>&1); then
    RACE=""
    echo "WARNING: race detector probe FAILED on this linux/arm64 runner (TSan aborts at startup here);" >&2
    echo "         running the Go suite WITHOUT -race. Coverage and test results are still valid." >&2
  fi
fi

# shellcheck disable=SC2086  # RACE is deliberately empty or "-race"
go test $RACE ./... -coverprofile=c.out -cover

echo ""
echo "--- Go coverage total (statements, all packages) ---"
go tool cover -func=c.out | tail -1
