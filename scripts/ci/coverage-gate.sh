#!/usr/bin/env bash
# coverage-gate.sh — CI coverage gate with ratcheting floors.
#
# Invoked from BOTH .github/workflows/ci.yml and .forgejo/workflows/ci.yml
# (identical steps) so the two pipelines cannot drift. It does NOT run any
# tests; it parses the artifacts the test steps produced earlier in the same
# job:
#
#   scripts/ci/coverage-gate.sh <go-cover-profile> <web-coverage-log>
#
#     go-cover-profile  c.out, written by:
#         go test -race ./... -coverprofile=c.out -cover
#     web-coverage-log  full stdout of (run from web/):
#         npm run test:unit:coverage
#       which contains Node's plain coverage table (docs/TESTING.md §2).
#
# Floors (ratcheting baselines — how to raise them: docs/TESTING.md §2,
# "How to ratchet the floors"):
#
#     GO_FLOOR   total Go statement coverage % across all packages  default 80
#     WEB_FLOOR  web unit total line coverage %                     default 65
#
# Both are overridable via environment for verifying the gate itself without
# touching real coverage, e.g.:
#     GO_FLOOR=99 WEB_FLOOR=99 scripts/ci/coverage-gate.sh c.out web-unit-coverage.log
#
# Format caveat: the "all files" total row is prefixed "#" on Node v22 and
# "ℹ" on Node v26 (and v22 additionally includes the test files themselves in
# the table, so its total reads higher than on a modern local Node). The
# parser below accepts both row styles.
#
# Exit codes: 0 = both totals at/above their floors; 1 = below floor or
# unparseable (a gate that cannot read coverage fails safe, never passes).

set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: scripts/ci/coverage-gate.sh <go-cover-profile> <web-coverage-log>" >&2
  exit 1
fi

GO_PROFILE="$1"
WEB_LOG="$2"

# Ratcheting floors — the single source of truth for CI coverage minimums.
# Bump here (and document in docs/TESTING.md §2) only after raising actual
# coverage; see "How to ratchet the floors" there.
GO_FLOOR="${GO_FLOOR:-80}"
WEB_FLOOR="${WEB_FLOOR:-65}"

[ -f "$GO_PROFILE" ] || { echo "COVERAGE GATE FAIL: Go profile '$GO_PROFILE' not found (did the Go test step run?)" >&2; exit 1; }
[ -f "$WEB_LOG" ] || { echo "COVERAGE GATE FAIL: web coverage log '$WEB_LOG' not found (did the web unit step run?)" >&2; exit 1; }

# --- Parse total Go statement coverage -----------------------------------
# Last line of `go tool cover -func` is:  total:\t(statements)\t83.9%
GO_TOTAL="$(go tool cover -func "$GO_PROFILE" | tail -n 1 | awk '{ gsub(/%/, "", $3); print $3 }')"

# --- Parse web unit total line coverage ----------------------------------
# The plain reporter's "all files" row, tolerant of the '#' / 'ℹ' prefix and
# column padding that differ between Node versions:
#     # all files         |  82.35 |    84.44 |   81.80 |   (Node v22)
#     ℹ all files    |  69.35 |    79.03 |   67.07 |   (Node v26)
WEB_TOTAL="$(awk -F'|' '$1 ~ /all files/ { gsub(/[[:space:]]/, "", $2); print $2 }' "$WEB_LOG" | tail -n 1)"

# --- Guard: both values must be numbers ----------------------------------
for pair in "Go total statement coverage|${GO_TOTAL}" \
            "web unit total line coverage|${WEB_TOTAL}"; do
  name="${pair%%|*}"
  val="${pair##*|}"
  case "$val" in
    '' | *[!0-9.]*)
      echo "COVERAGE GATE FAIL: could not extract a numeric ${name} (got '${val}') — check the coverage log/profile above" >&2
      exit 1
      ;;
  esac
done

# --- Enforce floors (float compare) --------------------------------------
FAIL=0
check_floor() { # $1 = label, $2 = value, $3 = floor (exit 0 iff value >= floor)
  if awk -v v="$2" -v f="$3" 'BEGIN { exit (v+0 >= f+0) ? 0 : 1 }'; then
    echo "coverage gate: ${1} = ${2}% (floor ${3}%) — PASS"
  else
    echo "coverage gate: ${1} = ${2}% is BELOW the ratcheting floor of ${3}% — FAIL" >&2
    FAIL=1
  fi
}

check_floor "Go total statement coverage" "$GO_TOTAL" "$GO_FLOOR"
check_floor "web unit total line coverage" "$WEB_TOTAL" "$WEB_FLOOR"

if [ "$FAIL" -ne 0 ]; then
  echo "COVERAGE GATE FAILED — raise the failing suite's coverage, or ratchet per docs/TESTING.md §2." >&2
  exit 1
fi

echo "COVERAGE GATE PASS: Go ${GO_TOTAL}% >= ${GO_FLOOR}%, web unit lines ${WEB_TOTAL}% >= ${WEB_FLOOR}%"
