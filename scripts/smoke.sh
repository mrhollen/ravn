#!/bin/sh
# ravn end-to-end smoke test (slice 6, extended).
#
# What it verifies (docs/ARCHITECTURE.md §9 flow):
#   healthz (server + mcp) → GET /api/v1/projects → create project →
#   create task → claim as agent-a → DUPLICATE claim by agent-b expect 409 →
#   add comment → PATCH with a STALE version expect 409 → MCP JSON-RPC
#   initialize + tools/list (expect 2xx and a non-empty "tools" array) →
#   (only when RAVN_TOKEN is set) POST /api/v1/admin/backup WITHOUT a token,
#   expect 401 — proves the route exists and is auth-gated.
#
# Modes: if $SERVER_BASE/healthz is already reachable (e.g. `make up`), the
# script uses that stack as-is. Otherwise it SELF-STARTS bin/ravn-server +
# bin/ravn-mcp on 127.0.0.1 with a fresh mktemp data dir (no Docker, no image
# builds) and kills both + removes temp artifacts on exit via trap. Run
# `make build` first in self-start mode.
#
# Ports/env: the host-side variables docker-compose.yml interpolates —
#   RAVN_SERVER_PORT (default 3311), RAVN_MCP_PORT (default 9311),
#   RAVN_TOKEN (optional; when set, requests carry it as a Bearer header and
#   self-started servers gate auth the same way).
#   RAVN_BIN_DIR    — where to find ravn-server/ravn-mcp (default: <repo>/bin)
#   RAVN_SMOKE_READY_WAIT — seconds to wait for an already-starting external
#                           stack before falling back to self-start (def 5)
# Every curl invocation carries --connect-timeout 5 --max-time 15 so nothing
# hangs if a service is locked or unreachable. Exits non-zero on ANY failure.
#
#   make up && make smoke                                        # Docker mode
#   RAVN_SERVER_PORT=18432 RAVN_MCP_PORT=19432 make smoke  # direct binaries / custom ports

set -u

SERVER_BASE="http://127.0.0.1:${RAVN_SERVER_PORT:-3311}"
MCP_BASE="http://127.0.0.1:${RAVN_MCP_PORT:-9311}"
TOKEN="${RAVN_TOKEN:-}"
READY_WAIT="${RAVN_SMOKE_READY_WAIT:-5}"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
BIN_DIR="${RAVN_BIN_DIR:-$(dirname "$SCRIPT_DIR")/bin}"

# AC3: every curl in this script goes through these two helpers and always
# carries the same explicit timeouts (no silent hangs on locked ports).
CURL_TIMEOUTS="--connect-timeout 5 --max-time 15"

TMP=""
SERVER_PID=""
MCP_PID=""
CLEANED=0
cleanup() {
    [ "$CLEANED" = 1 ] && return 0
    CLEANED=1
    PIDS=""
    [ -n "$SERVER_PID" ] && PIDS="$PIDS $SERVER_PID"
    [ -n "$MCP_PID" ] && PIDS="$PIDS $MCP_PID"
    if [ -n "$PIDS" ]; then
        for p in $PIDS; do kill "$p" 2>/dev/null || :; done
        sleep 1
        for p in $PIDS; do kill -9 "$p" 2>/dev/null || :; done
    fi
    if [ -n "$TMP" ]; then rm -rf -- "$TMP"; fi
}
trap 'cleanup' EXIT INT TERM

TMP=$(mktemp -d) || exit 1
BODY_FILE="$TMP/body"
: > "$BODY_FILE"

PASS=0
FAIL=0
SKIP=0

# http <method> <url> [data] → $CODE (HTTP status, 000 = no connection), body at $BODY_FILE.
http() {
    h_method=$1; h_url=$2; h_data=${3-}
    CODE=000
    : > "$BODY_FILE"
    case $h_method in
        GET)
            if [ -n "$TOKEN" ]; then
                CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
                    -H "Authorization: Bearer $TOKEN" "$h_url") || CODE=000
            else
                CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS "$h_url") || CODE=000
            fi ;;
        POST|PATCH)
            if [ -n "$TOKEN" ]; then
                CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
                    -X "$h_method" -H "Authorization: Bearer $TOKEN" \
                    -H 'Content-Type: application/json' --data "$h_data" "$h_url") || CODE=000
            else
                CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
                    -X "$h_method" -H 'Content-Type: application/json' --data "$h_data" "$h_url") || CODE=000
            fi ;;
        *)
            echo "internal error: unsupported http method '$h_method'" >&2
            exit 2 ;;
    esac
}

# mcp_post <json-rpc-body> → $CODE / $BODY_FILE. Streamable HTTP POST to /mcp;
# Accept: application/json forces a plain JSON answer (no SSE wrapping). The
# server runs stateless (WithStateLess), so initialize returns no session id
# and none has to be forwarded to tools/list.
mcp_post() {
    CODE=000
    : > "$BODY_FILE"
    if [ -n "$TOKEN" ]; then
        CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
            -X POST -H "Authorization: Bearer $TOKEN" \
            -H 'Content-Type: application/json' -H 'Accept: application/json' \
            --data "$1" "$MCP_BASE/mcp") || CODE=000
    else
        CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
            -X POST -H 'Content-Type: application/json' -H 'Accept: application/json' \
            --data "$1" "$MCP_BASE/mcp") || CODE=000
    fi
}

fail_step() { # <label> <wanted-status>
    printf 'FAIL  %-28s -> %s (wanted %s)\n' "$1" "$CODE" "$2"
    if [ -s "$BODY_FILE" ]; then
        sed 's/^/      /' "$BODY_FILE" | head -n 5
    elif [ "$CODE" = "000" ]; then
        if [ -n "$MCP_PID" ] || [ -n "$SERVER_PID" ]; then
            echo "      no connection — self-started process died; see logs in $TMP before cleanup"
        else
            echo "      no connection — is the stack up? try: make up && docker compose ps"
        fi
    fi
    FAIL=$((FAIL + 1))
}

# expect <label> <wanted-status> [fixed-string-must-appear-in-body]
expect() {
    e_label=$1; e_want=$2; e_pat=${3-}
    if [ "$CODE" != "$e_want" ]; then
        fail_step "$e_label" "$e_want"
        return 0
    fi
    if [ -n "$e_pat" ] && ! grep -qF -- "$e_pat" "$BODY_FILE"; then
        printf 'FAIL  %-28s -> %s OK but body missing: %s\n' "$e_label" "$CODE" "$e_pat"
        sed 's/^/      /' "$BODY_FILE" | head -n 5
        FAIL=$((FAIL + 1))
        return 0
    fi
    printf 'PASS  %-28s -> %s\n' "$e_label" "$CODE"
    PASS=$((PASS + 1))
}

# expect_body <label> <fixed-string> — second assertion on the already-fetched body.
expect_body() {
    if grep -qF -- "$2" "$BODY_FILE"; then
        printf 'PASS  %-28s (body check)\n' "$1"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %-28s (body missing: %s)\n' "$1" "$2"
        sed 's/^/      /' "$BODY_FILE" | head -n 5
        FAIL=$((FAIL + 1))
    fi
}

# first_id — the first "id":"ULID" in the response body (Go JSON is compact;
# both create responses lead with our resource's id).
first_id() { sed -n 's/.*"id":"\([^"]*\)".*/\1/p' "$BODY_FILE" | head -n 1; }

# wait_healthz <url> <max_seconds> → 0 once it answers 200 (bounded poll).
wait_healthz() {
    w_url=$1; w_max=$2; w_i=0
    while [ "$w_i" -lt "$w_max" ]; do
        CODE=$(curl -s -o /dev/null -w '%{http_code}' $CURL_TIMEOUTS "$w_url") || CODE=000
        [ "$CODE" = 200 ] && return 0
        w_i=$((w_i + 1))
        sleep 1
    done
    return 1
}

echo "ravn smoke test (server: $SERVER_BASE, mcp: $MCP_BASE)"

# --- mode selection: reuse a live stack, else self-start the local binaries ---
http GET "$SERVER_BASE/healthz"
if [ "$CODE" != "200" ]; then
    echo "server not reachable yet — waiting up to ${READY_WAIT}s for an external stack…"
    wait_healthz "$SERVER_BASE/healthz" "$READY_WAIT" || CODE=000
fi

MODE="external"
if [ "$CODE" != "200" ]; then
    if [ -x "$BIN_DIR/ravn-server" ] && [ -x "$BIN_DIR/ravn-mcp" ]; then
        MODE="self-start"
        echo "no live stack found — self-starting $BIN_DIR/ravn-server + ravn-mcp with a temp data dir ($TMP)"
        mkdir -p "$TMP/data" "$TMP/artifacts" "$TMP/static"
        RAVN_ADDR="127.0.0.1:${RAVN_SERVER_PORT:-3311}" \
        RAVN_DATA="$TMP/data/ravn.db" \
        RAVN_ARTIFACTS_DIR="$TMP/artifacts" \
        RAVN_STATIC_DIR="$TMP/static" \
        RAVN_TOKEN="$TOKEN" \
            "$BIN_DIR/ravn-server" >>"$TMP/server.log" 2>&1 &
        SERVER_PID=$!
        if ! wait_healthz "$SERVER_BASE/healthz" 15; then
            echo "self-started ravn-server did not become healthy within 15s — last log lines:" >&2
            tail -n 10 "$TMP/server.log" 2>/dev/null | sed 's/^/      /' >&2
            exit 1
        fi
        MCP_ADDR="127.0.0.1:${RAVN_MCP_PORT:-9311}" \
        RAVN_API_URL="$SERVER_BASE" \
        RAVN_TOKEN="$TOKEN" \
        RAVN_AGENT_NAME="smoke-agent" \
            "$BIN_DIR/ravn-mcp" >>"$TMP/mcp.log" 2>&1 &
        MCP_PID=$!
        if ! wait_healthz "$MCP_BASE/healthz" 15; then
            echo "self-started ravn-mcp did not become healthy within 15s — last log lines:" >&2
            tail -n 10 "$TMP/mcp.log" 2>/dev/null | sed 's/^/      /' >&2
            exit 1
        fi
    else
        echo "server not reachable at $SERVER_BASE and no usable binaries in $BIN_DIR" >&2
        echo "(start a stack with: make up — or build locally with: make build)" >&2
        exit 1
    fi
fi

RUN_TAG="smoke-$(date +%Y%m%d%H%M%S)-$$"
echo "mode: $MODE — using run tag $RUN_TAG (unique names, safe against non-empty DBs)"
echo

# --- the flow (docs/ARCHITECTURE.md §9) ---
http GET "$SERVER_BASE/healthz"
expect "server /healthz" 200 '"status":"ok"'

http GET "$MCP_BASE/healthz"
expect "mcp /healthz (proxied)" 200 '"status":"ok"'

http GET "$SERVER_BASE/api/v1/projects"
expect "api GET /api/v1/projects" 200

http POST "$SERVER_BASE/api/v1/projects" \
    "{\"actor\":{\"name\":\"$RUN_TAG\",\"type\":\"agent\"},\"name\":\"$RUN_TAG-project\",\"description\":\"created by scripts/smoke.sh — safe to delete\"}"
expect "create project (201)" 201
PROJECT_ID=$(first_id)
if [ -z "$PROJECT_ID" ]; then
    printf 'FAIL  %-28s no id in create-project response\n' "create project (id)"
    sed 's/^/      /' "$BODY_FILE" | head -n 5
    FAIL=$((FAIL + 1))
fi

http POST "$SERVER_BASE/api/v1/tasks" \
    "{\"actor\":{\"name\":\"$RUN_TAG\",\"type\":\"agent\"},\"title\":\"$RUN_TAG-task\",\"description\":\"created by scripts/smoke.sh — safe to delete\",\"project_id\":\"$PROJECT_ID\",\"priority\":2,\"acceptance_criteria\":[\"smoke step\"]}"
expect "create task (201)" 201
TASK_ID=$(first_id)
if [ -z "$TASK_ID" ]; then
    printf 'FAIL  %-28s no id in create-task response\n' "create task (id)"
    sed 's/^/      /' "$BODY_FILE" | head -n 5
    FAIL=$((FAIL + 1))
fi

http POST "$SERVER_BASE/api/v1/tasks/$TASK_ID/claim" \
    "{\"actor\":{\"name\":\"$RUN_TAG-claimer-a\",\"type\":\"agent\"},\"ttl_sec\":900}"
expect "claim as agent-a (200)" 200 '"status":"doing"'

http POST "$SERVER_BASE/api/v1/tasks/$TASK_ID/claim" \
    "{\"actor\":{\"name\":\"$RUN_TAG-claimer-b\",\"type\":\"agent\"}}"
expect "duplicate claim → 409" 409 'claim_conflict'

http POST "$SERVER_BASE/api/v1/tasks/$TASK_ID/comments" \
    "{\"actor\":{\"name\":\"$RUN_TAG-claimer-a\",\"type\":\"agent\"},\"kind\":\"progress\",\"body\":\"smoke test comment — safe to delete\"}"
expect "add comment (201)" 201 '"kind":"progress"'

# The task is now at version ≥2 (the claim bumped it), so version=1 is stale.
http PATCH "$SERVER_BASE/api/v1/tasks/$TASK_ID" \
    "{\"actor\":{\"name\":\"$RUN_TAG-claimer-a\",\"type\":\"agent\"},\"version\":1,\"title\":\"stale-version probe\"}"
expect "stale-version update → 409" 409 'version_conflict'

mcp_post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"ravn-smoke","version":"1.0"}}}'
expect "mcp initialize (JSON-RPC)" 200 '"serverInfo":{"name":"ravn"'

mcp_post '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
expect "mcp tools/list (JSON-RPC)" 200 '"tools":[{'
expect_body "mcp tools non-empty & known" '"name":"create_project"'

# Hardening probe (only meaningful when auth is on): the admin backup route must
# exist and answer 401 to an unauthenticated caller. Skipped — never failed —
# when no token is configured, since the route is deliberately ungated then.
if [ -n "$TOKEN" ]; then
    CODE=000
    : > "$BODY_FILE"
    CODE=$(curl -s -o "$BODY_FILE" -w '%{http_code}' $CURL_TIMEOUTS \
        -X POST -H 'Content-Type: application/json' \
        --data '{"dest":"smoke-probe"}' "$SERVER_BASE/api/v1/admin/backup") || CODE=000
    expect "admin backup w/o token → 401" 401 'unauthorized'
else
    SKIP=$((SKIP + 1))
    printf 'SKIP  %-28s (RAVN_TOKEN not set — route ungated)\n' "admin backup probe"
fi

echo
TOTAL=$((PASS + FAIL))
if [ "$FAIL" -eq 0 ]; then
    if [ "$SKIP" -gt 0 ]; then
        echo "SMOKE PASSED ($TOTAL/$TOTAL checks, $SKIP skipped)"
    else
        echo "SMOKE PASSED ($TOTAL/$TOTAL checks)"
    fi
    exit 0
fi
if [ "$SKIP" -gt 0 ]; then
    echo "SMOKE FAILED ($FAIL of $TOTAL checks failed, $SKIP skipped)"
else
    echo "SMOKE FAILED ($FAIL of $TOTAL checks failed)"
fi
exit 1
