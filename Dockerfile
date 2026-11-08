# syntax=docker/dockerfile:1
#
# ravn — one multi-stage build producing TWO runtime images
# (docs/ARCHITECTURE.md §8). docker-compose.yml selects the stage per service:
#   ravn-server → build.target server-runtime
#   ravn-mcp    → build.target mcp-runtime
#
# Stage order matters: web-build runs exactly once and both Go binaries come
# from a single dependency download, so `docker compose up --build` builds the
# UI one time per context change regardless of which targets are requested.

# --- Base images are PINNED to immutable digests (task 01M3MN0E36QM3NWMCWZRGH9C88) so
#     upstream re-tags never change what gets built. To bump ONE image, re-resolve
#     the tag's manifest digest and edit exactly one FROM line below:
#       node:22-alpine        @ sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202
#       golang:1.25.14-alpine @ sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
#       alpine:3.21.10        @ sha256:769c0a0cb2a64409697b54551072ee46ee1e36d4b369ddafd671b02a5d16e0e5
#     All three digests verified resolvable against registry-1.docker.io on 2026-11-08.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# 1) web-build — build the Vite/TS UI exactly once (output: /web/dist).
#    The lockfile is copied first so npm ci hits the dependency cache when
#    only source files change. web/package-lock.json stays IN the build
#    context (it matches no .dockerignore rule).
# ---------------------------------------------------------------------------
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS web-build
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# script "build" = tsc --noEmit && vite build → /web/dist
RUN npm run build

# ---------------------------------------------------------------------------
# 2) go-build — CGO-free static binaries for BOTH services, one download.
#    Base matches go.mod's `go 1.25.x` line (ADR 2026-09-25: modernc.org/sqlite
#    v1.59.0 forces ≥1.25; any newer golang:*-alpine also works). The alpine
#    image ships no C compiler → pure-Go builds only, which is exactly the
#    guarantee we want from CGO_ENABLED=0 (no accidental CGO SQLite).
# ---------------------------------------------------------------------------
FROM golang:1.25.14-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS go-build
WORKDIR /src
# Module files FIRST, then the module download: the download layer stays cached
# while source changes. (task 01M3MN0AHKZW0FX7C7MC7GEEFN)
COPY go.mod go.sum ./
RUN go mod download
# Go-ONLY context — deliberately NOT `COPY . .`: web/ is excluded so UI edits
# never invalidate this stage (the runtime stages still get /web/dist from
# web-build). Copy exactly the top-level dirs that contain .go sources: cmd/
# and internal/ (internal/infrastructure/sqlite also carries //go:embed
# migrations/*.sql, covered by the whole-dir copy below). Add a COPY line here
# if a future top-level Go package appears.
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/ravn-server ./cmd/ravn-server && \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/ravn-mcp     ./cmd/ravn-mcp

# ---------------------------------------------------------------------------
# 3) server-runtime — main server + baked-in UI (compose target #1).
#    alpine, not scratch: busybox wget powers the in-image HEALTHCHECK.
#    The built dist is copied from web-build to /app/public and pointed at by
#    RAVN_STATIC_DIR. /data holds ravn.db (+ -wal/-shm) on a named
#    volume; it is pre-created and owned by the app user HERE so an empty
#    named volume inherits that ownership/mode on first mount (otherwise the
#    unprivileged user cannot write the database).
# ---------------------------------------------------------------------------
FROM alpine:3.21.10@sha256:769c0a0cb2a64409697b54551072ee46ee1e36d4b369ddafd671b02a5d16e0e5 AS server-runtime
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 app \
 && adduser -D -u 10001 -G app app \
 && mkdir -p /data /artifacts \
 && chown app:app /data /artifacts
COPY --from=go-build  /out/ravn-server /usr/local/bin/
COPY --from=go-build  /out/ravn-mcp    /usr/local/bin/
COPY --from=web-build /web/dist           /app/public
USER app
ENV RAVN_ADDR=:8080 \
    RAVN_DATA=/data/ravn.db \
    RAVN_STATIC_DIR=/app/public \
    RAVN_ARTIFACTS_DIR=/artifacts
VOLUME ["/data", "/artifacts"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["ravn-server"]

# ---------------------------------------------------------------------------
# 4) mcp-runtime — stateless MCP protocol adapter (compose target #2).
#    No volumes, no state: trivially restartable/scaleable. RAVN_TOKEN and
#    RAVN_AGENT_NAME are runtime pass-throughs (injected by compose), not
#    baked in here; only the upstream URL gets a default. /healthz proxies the
#    main server's healthcheck, so compose `service_healthy` chains through it.
# ---------------------------------------------------------------------------
FROM alpine:3.21.10@sha256:769c0a0cb2a64409697b54551072ee46ee1e36d4b369ddafd671b02a5d16e0e5 AS mcp-runtime
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 app \
 && adduser -D -u 10001 -G app app
COPY --from=go-build /out/ravn-mcp /usr/local/bin/
USER app
ENV MCP_ADDR=:9080 \
    RAVN_API_URL=http://ravn-server:8080
EXPOSE 9080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:9080/healthz || exit 1
ENTRYPOINT ["ravn-mcp"]
