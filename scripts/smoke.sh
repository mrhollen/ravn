#!/bin/sh
# crowban smoke test (slice 6).
#
# Expected setup: the full stack is up — normally via `make up` (Docker
# compose, which interpolates these same port variables). The script just
# needs reachable HTTP endpoints, so it ALSO works against direct-binary
# servers started by hand (see README "Direct binaries (development)"):
# point CROWBAN_SERVER_PORT / CROWBAN_MCP_PORT at whatever ports you ran
# crowban-server / crowban-mcp on.
#
# Ports: the overridable HOST-side variables docker-compose.yml interpolates —
#   CROWBAN_SERVER_PORT (default 8080), CROWBAN_MCP_PORT (default 9080).
# When CROWBAN_TOKEN is set, requests carry it as a Bearer header.
# Exits non-zero on any failure so it can gate deploys/CI.
#
#   make up && make smoke                                        # Docker mode
#   CROWBAN_SERVER_PORT=18432 CROWBAN_MCP_PORT=19432 make smoke  # direct binaries on custom ports

set -u

SERVER_BASE="http://127.0.0.1:${CROWBAN_SERVER_PORT:-8080}"
MCP_BASE="http://127.0.0.1:${CROWBAN_MCP_PORT:-9080}"
TOKEN="${CROWBAN_TOKEN:-}"

BODY=$(mktemp) || exit 1
trap 'rm -f "$BODY"' EXIT INT TERM

# fetch <url> — body into $BODY, HTTP status into $CODE (000 = no connection).
fetch() {
    if [ -n "$TOKEN" ]; then
        CODE=$(curl -s -o "$BODY" -w '%{http_code}' \
            -H "Authorization: Bearer $TOKEN" "$1") || CODE=000
    else
        CODE=$(curl -s -o "$BODY" -w '%{http_code}' "$1") || CODE=000
    fi
}

PASS=0
FAIL=0

# check <label> <expected-status> <url>
check() {
    fetch "$3"
    if [ "$CODE" = "$2" ]; then
        printf 'PASS  %-28s %s -> %s\n' "$1" "$3" "$CODE"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %-28s %s -> %s (wanted %s)\n' "$1" "$3" "$CODE" "$2"
        if [ -s "$BODY" ]; then
            sed 's/^/      /' "$BODY" | head -n 5
        elif [ "$CODE" = "000" ]; then
            echo "      no connection — is the stack up? try: make up && docker compose ps"
        fi
        FAIL=$((FAIL + 1))
    fi
}

echo "crowban smoke test (server: $SERVER_BASE, mcp: $MCP_BASE)"
check "server /healthz"          200 "$SERVER_BASE/healthz"
check "mcp /healthz (proxied)"   200 "$MCP_BASE/healthz"
check "api GET /api/v1/projects" 200 "$SERVER_BASE/api/v1/projects"

echo
if [ "$FAIL" -eq 0 ]; then
    echo "SMOKE PASSED ($PASS/$((PASS + FAIL)) checks)"
    exit 0
fi
echo "SMOKE FAILED ($FAIL of $((PASS + FAIL)) checks failed)"
exit 1
