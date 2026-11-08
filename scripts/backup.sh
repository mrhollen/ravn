#!/bin/sh
# ravn backup helper (slice B2). See README "Backups" and docs/ARCHITECTURE.md.
#
# Produces a zero-downtime, point-in-time, SINGLE-FILE SQLite backup of the
# database (via VACUUM INTO) and verifies it with PRAGMA integrity_check
# before declaring success. It NEVER overwrites an existing file — this
# matches server behaviour for both entry points below.
#
# Why not just `cp ravn.db`? The live DB runs in WAL mode: data lives split
# across ravn.db + ravn.db-wal (+ ravn.db-shm) until a checkpoint, and the
# -wal file is only safely merged on a clean shutdown. Copying only .db — or
# even all three files from a running process — can yield a corrupt/tepid
# copy. VACUUM INTO always writes a complete, consistent, self-contained
# rollback-journal file (no sidecars) regardless of source journal mode.
#
# Modes (first match wins):
#   1. HTTP — RAVN_API_URL set and curl available: POST /api/v1/admin/backup
#      to the RUNNING server (Bearer $RAVN_TOKEN when set). Fully hot — the
#      serving process writes the file. The dest dir must live on a
#      filesystem the SERVER can reach (e.g. the ravn-data volume in Docker).
#   2. CLI  — a ravn-server binary is available: runs the one-shot
#      `ravn-server -data <db> -backup <dest>` (no HTTP server started).
#      Best for local/dev DBs; briefly contends with the writer via busy-wait.
#
# Env overrides:
#   RAVN_DATA        data file          (default: ravn.db)
#   RAVN_BACKUP_DIR  destination dir    (default: <data dir>/backups)
#   RAVN_API_URL     live server URL, e.g. http://127.0.0.1:3311 (enables HTTP mode)
#   RAVN_TOKEN       bearer token (sent when set; required if the server enforces auth)
#   RAVN_BIN         ravn-server binary path (CLI mode / integrity fallback)
#
#   make backup                                   # CLI mode on ./ravn.db
#   RAVN_DATA=/data/ravn.db make backup           # data file elsewhere
#   RAVN_API_URL=http://127.0.0.1:3311 RAVN_TOKEN=... make backup   # hot, live server

set -u

fail() { printf 'BACKUP FAIL  %s\n' "$1"; exit 1; }

DATA="${RAVN_DATA:-${1:-}}"
[ -n "$DATA" ] || fail "no data path — set RAVN_DATA or pass it as \$1 (e.g. ravn.db / /data/ravn.db)"

STAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_DIR="${RAVN_BACKUP_DIR:-$(dirname "$DATA")/backups}"
DEST="$BACKUP_DIR/ravn-backup-$STAMP.db"
[ -e "$DEST" ] && fail "target already exists: $DEST (refusing to overwrite)"
case "$DEST" in
    *\"*|*\\*) fail "backup path must not contain double-quotes or backslashes: $DEST" ;;
esac

# --- choose the mechanism --------------------------------------------------
MODE=""
BIN=""
if [ -n "${RAVN_API_URL:-}" ] && command -v curl >/dev/null 2>&1; then
    MODE=http
fi
if [ -z "$MODE" ]; then
    if [ -n "${RAVN_BIN:-}" ] && [ -x "${RAVN_BIN:-}" ]; then
        BIN="$RAVN_BIN"; MODE=cli
    else
        for c in ./bin/ravn-server bin/ravn-server "$(command -v ravn-server 2>/dev/null || true)"; do
            if [ -n "$c" ] && [ -x "$c" ]; then BIN="$c"; MODE=cli; break; fi
        done
    fi
fi
[ -n "$MODE" ] || fail "no mechanism — set RAVN_API_URL for a live server, or provide a ravn-server binary (RAVN_BIN / PATH / ./bin/ravn-server)"

printf 'ravn backup: data=%s mode=%s dest=%s\n' "$DATA" "$MODE" "$DEST"

# --- perform the hot backup -------------------------------------------------
if [ "$MODE" = http ]; then
    BODY=$(mktemp) || fail "mktemp"
    trap 'rm -f "$BODY"' EXIT INT TERM
    PAYLOAD=$(printf '{"dest":"%s"}' "$DEST")
    URL="${RAVN_API_URL%/}/api/v1/admin/backup"
    if [ -n "${RAVN_TOKEN:-}" ]; then
        CODE=$(curl -s -o "$BODY" -w '%{http_code}' \
            -H "Authorization: Bearer ${RAVN_TOKEN}" \
            -H 'Content-Type: application/json' \
            -X POST --data "$PAYLOAD" "$URL") || CODE=000
    else
        CODE=$(curl -s -o "$BODY" -w '%{http_code}' \
            -H 'Content-Type: application/json' \
            -X POST --data "$PAYLOAD" "$URL") || CODE=000
    fi
    [ "$CODE" = 200 ] || fail "server returned HTTP $CODE: $(cat "$BODY")"
else
    [ -f "$DATA" ] || fail "data file not found here: $DATA (CLI mode runs locally — set RAVN_API_URL if the live server is remote)"
    mkdir -p "$BACKUP_DIR" || fail "cannot create backup dir $BACKUP_DIR"
    "$BIN" -data "$DATA" -backup "$DEST" || fail "ravn-server -backup exited non-zero"
fi

[ -s "$DEST" ] || fail "backup file missing or empty after run: $DEST"

# --- verify with PRAGMA integrity_check -------------------------------------
if command -v sqlite3 >/dev/null 2>&1; then
    RESULT=$(sqlite3 "$DEST" 'PRAGMA integrity_check;' 2>&1) || fail "integrity_check errored on $DEST"
elif [ -n "$BIN" ]; then
    if "$BIN" -integrity-check "$DEST" >/dev/null 2>&1; then RESULT=ok; else RESULT=fail; fi
else
    printf 'WARN  no sqlite3 CLI or ravn-server binary available — integrity check SKIPPED (verify manually!)\n'
    RESULT=skipped
fi

if [ "$RESULT" = ok ]; then
    printf 'BACKUP OK    %s (%s bytes) integrity_check=ok\n' "$DEST" "$(wc -c < "$DEST" | tr -d ' ')"
elif [ "$RESULT" = skipped ]; then
    exit 0
else
    printf 'BACKUP FAIL  integrity_check=%s on %s\n' "$RESULT" "$DEST"
    exit 1
fi
