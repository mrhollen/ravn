// Command ravn-server is composition root 1 for ravn (ARCHITECTURE §2):
// env/flag config → SQLite open + migrations → shared Clock/IDGenerator/
// UnitOfWork → ALL use cases → the HTTP adapter (internal/interface/http) on
// a stdlib net/http ServeMux.
//
// This is the ONLY place that imports infrastructure; the http layer receives
// ready-made use-case structs and never sees the database. Slice 3a routes
// Projects + Task-core + the board snapshot; Comments/Artifacts/Acceptance/
// Relations are already constructed so slice 3b only adds routes. RAVN_TOKEN
// gates the /api/v1 routes with bearer auth when set (auth is disabled when unset). Slice 5a
// serves the built web UI from RAVN_STATIC_DIR (stdlib fs, SPA fallback);
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

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/ravn/internal/interface/http"
)

// version is stamped at build time: -ldflags "-X main.version=..." (see the
// Dockerfile blueprint in ARCHITECTURE §8).
var version = "dev"

func main() {
	var (
		addr   = flag.String("addr", envOr("RAVN_ADDR", ":8080"), "listen address, e.g. :8080")
		data   = flag.String("data", envOr("RAVN_DATA", "/data/ravn.db"), "path of the SQLite database file")
		token  = flag.String("token", os.Getenv("RAVN_TOKEN"), "bearer token gating /api/v1 routes; empty disables auth (dev default)")
		static = flag.String("static", envOr("RAVN_STATIC_DIR", "./web/dist"),
			"directory holding the built web UI (Vite dist); placeholder root is served when it has no index.html")
		artifacts = flag.String("artifacts-dir", envOr("RAVN_ARTIFACTS_DIR", defaultArtifactsDir()),
			"directory holding stored artifact uploads")
		logLevel = flag.String("log-level", envOr("LOG_LEVEL", envOr("RAVN_LOG_LEVEL", "info")),
			"log level: debug, info, warn, error")
		corsOrigins = flag.String("cors-origins", envOr("RAVN_CORS_ORIGINS", "*"),
			"comma-separated list of allowed CORS origins or '*' for any origin")
	)
	flag.Parse()

	lvl := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	}))
	slog.SetDefault(logger)
	logger.Info("ravn-server starting",
		"version", version, "addr", *addr, "data", *data, "token_set", *token != "", "static_dir", *static, "artifacts_dir", *artifacts, "log_level", lvl.String(), "cors_origins", *corsOrigins)

	if err := os.MkdirAll(*artifacts, 0o755); err != nil {
		logger.Error("failed to create artifacts dir", "dir", *artifacts, "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, closePool, err := sqlite.OpenPool(ctx, *data)
	if err != nil {
		logger.Error("database open failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := closePool(); err != nil {
			logger.Error("closing database", "err", err)
		}
	}()

	broker := pubsub.NewBroker()
	srv := newHTTPServer(*addr, newRouterWithPoolAndCORS(pool, *static, *token, *artifacts, broker, *corsOrigins))

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

	// Close the event broker immediately upon shutdown signal to unblock active SSE streaming loops.
	if err := broker.Close(); err != nil {
		logger.Error("closing broker", "err", err)
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
func newRouter(db *sql.DB, staticDir, token, artifactsDir string) http.Handler {
	return newRouterWithBroker(db, staticDir, token, artifactsDir, pubsub.NewBroker())
}

func newRouterWithBroker(db *sql.DB, staticDir, token, artifactsDir string, broker application.EventBroker) http.Handler {
	corsOrigins := os.Getenv("RAVN_CORS_ORIGINS")
	if corsOrigins == "" {
		corsOrigins = "*"
	}
	return newRouterWithBrokerAndCORS(db, staticDir, token, artifactsDir, broker, corsOrigins)
}

func newRouterWithBrokerAndCORS(db *sql.DB, staticDir, token, artifactsDir string, broker application.EventBroker, corsOrigins string) http.Handler {
	var pool *sqlite.DBPool
	if db != nil {
		pool = &sqlite.DBPool{Writer: db, Reader: db}
	}
	return newRouterWithPoolAndCORS(pool, staticDir, token, artifactsDir, broker, corsOrigins)
}

func newRouterWithPool(pool *sqlite.DBPool, staticDir, token, artifactsDir string) http.Handler {
	return newRouterWithPoolAndBroker(pool, staticDir, token, artifactsDir, pubsub.NewBroker())
}

func newRouterWithPoolAndBroker(pool *sqlite.DBPool, staticDir, token, artifactsDir string, broker application.EventBroker) http.Handler {
	corsOrigins := os.Getenv("RAVN_CORS_ORIGINS")
	if corsOrigins == "" {
		corsOrigins = "*"
	}
	return newRouterWithPoolAndCORS(pool, staticDir, token, artifactsDir, broker, corsOrigins)
}

func newRouterWithPoolAndCORS(pool *sqlite.DBPool, staticDir, token, artifactsDir string, broker application.EventBroker, corsOrigins string) http.Handler {
	mux := http.NewServeMux()

	if broker == nil {
		broker = pubsub.NewBroker()
	}

	var (
		writerDB *sql.DB
		readerDB *sql.DB
	)
	if pool != nil {
		writerDB = pool.Writer
		readerDB = pool.Reader
		if readerDB == nil {
			readerDB = writerDB
		}
	}

	var uow application.UnitOfWork
	if writerDB != nil {
		uow = sqlite.NewUnitOfWork(writerDB, sqlite.WithPublisher(broker))
	}
	now := clock.New()
	mint := idgen.New()

	metrics := httpapi.NewMetrics()
	var events application.EventStore // SSE Last-Event-ID replay (nil = live-only)
	if readerDB != nil {
		events = sqlite.NewEventStore(readerDB)
		metrics.SetTaskStatsFunc(func(ctx context.Context) (map[string]int64, int64, error) {
			return sqlite.CollectTaskMetrics(ctx, readerDB, time.Now())
		})
	} else if writerDB != nil {
		events = sqlite.NewEventStore(writerDB)
		metrics.SetTaskStatsFunc(func(ctx context.Context) (map[string]int64, int64, error) {
			return sqlite.CollectTaskMetrics(ctx, writerDB, time.Now())
		})
	}

	app := httpapi.NewApp(httpapi.Deps{
		Projects:     application.NewProjects(uow, now, mint),
		Tasks:        application.NewTasks(uow, now, mint),
		Comments:     application.NewComments(uow, now, mint),
		Artifacts:    application.NewArtifacts(uow, now, mint),
		Acceptance:   application.NewAcceptance(uow, now, mint),
		Relations:    application.NewRelations(uow, now, mint),
		Broker:       broker,
		Events:       events,
		ArtifactsDir: artifactsDir,
	})
	httpapi.RegisterRoutes(mux, app)

	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /api/v1/metrics", metrics.Handler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		dbStatus := sqlite.HealthPool(r.Context(), pool)
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
			fmt.Fprintln(w, "ravn server ok — API is up; no built web UI found (set RAVN_STATIC_DIR to a Vite dist)")
		})
	}

	corsOpts := httpapi.DefaultCORSOptions()
	corsOpts.AllowedOrigins = httpapi.ParseCORSOrigins(corsOrigins)

	apiHandler := httpapi.AuthMiddleware(token)(mux)
	meteredHandler := metrics.Middleware(apiHandler)
	corsHandler := httpapi.CORSMiddleware(corsOpts)(meteredHandler)
	return httpapi.SecurityHeadersMiddleware()(corsHandler)
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
		if p == "/api" || strings.HasPrefix(p, "/api/") || p == "/healthz" || strings.HasPrefix(p, "/healthz/") || p == "/metrics" || strings.HasPrefix(p, "/metrics/") || p == "/artifacts" || strings.HasPrefix(p, "/artifacts/") {
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

// defaultArtifactsDir returns "/artifacts" if present as a directory (e.g. In Docker),
// else "./artifacts" for local development.
func defaultArtifactsDir() string {
	if fi, err := os.Stat("/artifacts"); err == nil && fi.IsDir() {
		return "/artifacts"
	}
	return "./artifacts"
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

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

// newHTTPServer creates an *http.Server configured with robust timeouts to prevent
// connection exhaustion attacks (Slowloris headers/body and indefinite idle keep-alives).
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return newHTTPServerWithTimeouts(addr, handler, defaultReadHeaderTimeout, defaultReadTimeout, defaultIdleTimeout)
}

// newHTTPServerWithTimeouts allows constructing an *http.Server with custom timeout durations (e.g. for testing).
func newHTTPServerWithTimeouts(addr string, handler http.Handler, readHeaderTimeout, readTimeout, idleTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}
}
