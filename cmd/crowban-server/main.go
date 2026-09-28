// Command crowban-server is composition root 1 for crowban (ARCHITECTURE §2):
// env/flag config → SQLite open + migrations → shared Clock/IDGenerator/
// UnitOfWork → ALL use cases → the HTTP adapter (internal/interface/http) on
// a stdlib net/http ServeMux.
//
// This is the ONLY place that imports infrastructure; the http layer receives
// ready-made use-case structs and never sees the database. Slice 3a routes
// Projects + Task-core + the board snapshot; Comments/Artifacts/Acceptance/
// Relations are already constructed so slice 3b only adds routes. CROWBAN_TOKEN
// gates the /api/v1 routes with bearer auth when set (auth is disabled when unset). Slice 5a
// serves the built web UI from CROWBAN_STATIC_DIR (stdlib fs, SPA fallback);
// a missing dist degrades to the placeholder root instead of failing to boot.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
	"github.com/mrhollen/crowban/internal/infrastructure/pubsub"
	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/crowban/internal/interface/http"
)

// version is stamped at build time: -ldflags "-X main.version=..." (see the
// Dockerfile blueprint in ARCHITECTURE §8).
var version = "dev"

func main() {
	var (
		addr   = flag.String("addr", envOr("CROWBAN_ADDR", ":8080"), "listen address, e.g. :8080")
		data   = flag.String("data", envOr("CROWBAN_DATA", "/data/crowban.db"), "path of the SQLite database file")
		token  = flag.String("token", os.Getenv("CROWBAN_TOKEN"), "bearer token gating /api/v1 routes; empty disables auth (dev default)")
		static = flag.String("static", envOr("CROWBAN_STATIC_DIR", "./web/dist"),
			"directory holding the built web UI (Vite dist); placeholder root is served when it has no index.html")
		logLevel = flag.String("log-level", envOr("LOG_LEVEL", envOr("CROWBAN_LOG_LEVEL", "info")),
			"log level: debug, info, warn, error")
	)
	flag.Parse()

	lvl := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	}))
	slog.SetDefault(logger)
	logger.Info("crowban-server starting",
		"version", version, "addr", *addr, "data", *data, "token_set", *token != "", "static_dir", *static, "log_level", lvl.String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, closeDB, err := sqlite.Open(ctx, *data)
	if err != nil {
		logger.Error("database open failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := closeDB(); err != nil {
			logger.Error("closing database", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           newRouter(db, *static, *token),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("server error", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed; forcing close", "err", err)
		srv.Close()
	}
	logger.Info("stopped")
}

// healthResponse is the /healthz body (ARCHITECTURE §8): 200 with db "ok" when
// the SELECT 1 ping succeeds, 503 degraded otherwise.
type healthResponse struct {
	Status string `json:"status"` // "ok" | "degraded"
	DB     string `json:"db"`     // "ok" | "error: …"
}

// newRouter builds the stdlib ServeMux with Go 1.22 method+wildcard patterns.
// It composes the shared Clock/IDGenerator/UnitOfWork and every use case,
// hands them to the HTTP adapter (internal/interface/http), and mounts the
// /api/v1 routes alongside /healthz and — when staticDir holds a built UI
// (an index.html) — the static assets at "/" with an SPA fallback. The API
// and healthz patterns are exact paths, therefore MORE SPECIFIC than the
// "GET /" catch-all; Go 1.22 mux resolution always prefers the specific one
// regardless of registration order, so static serving can never shadow them
// (proved by TestStaticServingDoesNotShadowAPIAndHealthz).
func newRouter(db *sql.DB, staticDir, token string) http.Handler {
	mux := http.NewServeMux()

	// Slice 3 composition: one shared transactional seam, clock, and idgen —
	// every use case mints IDs and stamps timestamps before touching ports
	// (ADR 2026-09-25). Comments/Artifacts/Acceptance/Relations are wired now
	// even though slice 3b is what routes them.
	broker := pubsub.NewBroker()
	uow := sqlite.NewUnitOfWork(db, sqlite.WithPublisher(broker))
	now := clock.New()
	mint := idgen.New()

	metrics := httpapi.NewMetrics()
	if db != nil {
		metrics.SetTaskStatsFunc(func(ctx context.Context) (map[string]int64, int64, error) {
			return sqlite.CollectTaskMetrics(ctx, db, time.Now())
		})
	}

	app := httpapi.NewApp(httpapi.Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
		Broker:     broker,
	})
	httpapi.RegisterRoutes(mux, app)

	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /api/v1/metrics", metrics.Handler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		dbStatus := sqlite.Health(r.Context(), db)
		res := healthResponse{Status: "ok", DB: dbStatus}
		code := http.StatusOK
		if strings.HasPrefix(dbStatus, "error:") {
			res.Status = "degraded"
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, res)
	})

	if hasIndexHTML(staticDir) {
		// Slice 5a (ARCHITECTURE §7): the Go binary serves the Vite build with
		// stdlib only — no web container. "GET /" is the least specific pattern
		// on this mux, so it only ever sees GET/HEAD requests that no API or
		// healthz pattern claimed.
		public := os.DirFS(staticDir)
		mux.Handle("GET /", spaFallback(public))
		slog.Info("serving built web UI", "static_dir", staticDir)
	} else {
		// Graceful degradation (backend-only run, no built UI): keep the
		// placeholder root; never fail to boot.
		slog.Warn("no index.html in static dir; serving placeholder root instead of the web UI", "static_dir", staticDir)
		mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "crowban server ok — API is up; no built web UI found (set CROWBAN_STATIC_DIR to a Vite dist)")
		})
	}

	return metrics.Middleware(httpapi.AuthMiddleware(token)(mux))
}

// hasIndexHTML reports whether dir exists and directly contains an index.html
// file — the marker that a built web UI is present to serve.
func hasIndexHTML(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !info.IsDir()
}

// spaFallback serves the built web UI from static via stdlib (FileServerFS).
// A GET/HEAD whose path maps to a real file or directory is served as-is; any
// other request that is NOT under /api or /healthz falls back to index.html
// so client-side routing deep links work. Paths under /api, /healthz, or /metrics
// that still reach this handler are unknown routes — 404, never index.html (the
// real patterns for them are more specific than "GET /" and win first).
//
// The fallback rewrites the path to "/" rather than "/index.html": FileServer
// itself redirects any request whose path ends in "/index.html" to "./" (see
// serveFile in net/http/fs.go), which would loop on deep links. Serving the
// dist root is how FileServer delivers index.html anyway — 200, no redirect.
func spaFallback(static fs.FS) http.Handler {
	fileServer := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(static, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			fileServer.ServeHTTP(w, r) // real file or directory: served as-is
			return
		}
		p := r.URL.Path
		if p == "/api" || strings.HasPrefix(p, "/api/") || p == "/healthz" || strings.HasPrefix(p, "/healthz/") || p == "/metrics" || strings.HasPrefix(p, "/metrics/") {
			http.NotFound(w, r)
			return
		}
		spa := r.Clone(r.Context())
		u := *spa.URL
		u.Path = "/" // no file here: serve the dist index and let the app route client-side
		spa.URL = &u
		fileServer.ServeHTTP(w, spa)
	})
}

// parseLogLevel parses a log level string into a slog.Level.
// Supported values (case-insensitive): debug, info, warn, error.
// Defaults to slog.LevelInfo on unrecognized input.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal health response: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(body) //nolint:errcheck
}

// envOr returns the environment variable key if set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
