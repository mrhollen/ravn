# syntax=docker/dockerfile:1
#
# crowban — one multi-stage build producing TWO runtime images
# (docs/ARCHITECTURE.md §8). docker-compose.yml selects the stage per service:
#   crowban-server → build.target server-runtime
#   crowban-mcp    → build.target mcp-runtime
#
# Stage order matters: web-build runs exactly once and both Go binaries come
# from a single dependency download, so `docker compose up --build` builds the
# UI one time per context change regardless of which targets are requested.

# ---------------------------------------------------------------------------
# 1) web-build — build the Vite/TS UI exactly once (output: /web/dist).
#    The lockfile is copied first so npm ci hits the dependency cache when
#    only source files change. web/package-lock.json stays IN the build
#    context (it matches no .dockerignore rule).
# ---------------------------------------------------------------------------
FROM node:22-alpine AS web-build
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
FROM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/crowban-server ./cmd/crowban-server && \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/crowban-mcp     ./cmd/crowban-mcp

# ---------------------------------------------------------------------------
# 3) server-runtime — main server + baked-in UI (compose target #1).
#    alpine, not scratch: busybox wget powers the in-image HEALTHCHECK.
#    The built dist is copied from web-build to /app/public and pointed at by
#    CROWBAN_STATIC_DIR. /data holds crowban.db (+ -wal/-shm) on a named
#    volume; it is pre-created and owned by the app user HERE so an empty
#    named volume inherits that ownership/mode on first mount (otherwise the
#    unprivileged user cannot write the database).
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS server-runtime
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 app \
 && adduser -D -u 10001 -G app app \
 && mkdir -p /data /artifacts \
 && chown app:app /data /artifacts
COPY --from=go-build  /out/crowban-server /usr/local/bin/
COPY --from=go-build  /out/crowban-mcp    /usr/local/bin/
COPY --from=web-build /web/dist           /app/public
USER app
ENV CROWBAN_ADDR=:8080 \
    CROWBAN_DATA=/data/crowban.db \
    CROWBAN_STATIC_DIR=/app/public \
    CROWBAN_ARTIFACTS_DIR=/artifacts
VOLUME ["/data", "/artifacts"]
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["crowban-server"]

# ---------------------------------------------------------------------------
# 4) mcp-runtime — stateless MCP protocol adapter (compose target #2).
#    No volumes, no state: trivially restartable/scaleable. CROWBAN_TOKEN and
#    CROWBAN_AGENT_NAME are runtime pass-throughs (injected by compose), not
#    baked in here; only the upstream URL gets a default. /healthz proxies the
#    main server's healthcheck, so compose `service_healthy` chains through it.
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS mcp-runtime
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 app \
 && adduser -D -u 10001 -G app app
COPY --from=go-build /out/crowban-mcp /usr/local/bin/
USER app
ENV MCP_ADDR=:9080 \
    CROWBAN_API_URL=http://crowban-server:8080
EXPOSE 9080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:9080/healthz || exit 1
ENTRYPOINT ["crowban-mcp"]
