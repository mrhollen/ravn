// Command ravn-server is composition root 1 for ravn (ARCHITECTURE §2):
// env/flag config → SQLite open + migrations → shared Clock/IDGenerator/
// UnitOfWork → ALL use cases → the HTTP adapter (internal/interface/http) on
// a stdlib net/http ServeMux.
//
// This is the ONLY place that imports infrastructure; the http layer receives
// ready-made use-case structs and never sees the database. Slice 3a routes
// Projects + Task-core + the board snapshot; Comments/Artifacts/Acceptance/
// Relations are already constructed so slice 3b only adds routes. RAVN_TOKEN
// gates the /api/v1 routes AND root /metrics with bearer auth when set (auth
// is disabled when unset); RAVN_PUBLIC_METRICS=true opts root /metrics back out
// of that gate for public Prometheus scraping. Slice 5a
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
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/infrastructure/clock"
	"github.com/mrhollen/ravn/internal/infrastructure/idgen"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
	"github.com/mrhollen/ravn/internal/infrastructure/pwhash"
	"github.com/mrhollen/ravn/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/ravn/internal/interface/http"
)

// version is stamped at build time: -ldflags "-X main.version=..." (see the
// Dockerfile blueprint in ARCHITECTURE §8).
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is the injectable server entry point: argv + a stderr writer in, exit code
// out. It is split from main so flag/env resolution, DB-open and artifacts-dir
// failure paths, static-dir detection and the graceful-shutdown wiring can be
// tested without os.Exit or real process stdio. Behavior (defaults, env/flag
// precedence, log messages, exit codes) is unchanged from the former inline
// main body; the only structural difference is that flag parsing uses a
// dedicated FlagSet writing to errW instead of the global flag package.
func run(args []string, errW io.Writer) int {
	fs := flag.NewFlagSet("ravn-server", flag.ContinueOnError)
	fs.SetOutput(errW)

	var (
		addr  = fs.String("addr", envOr("RAVN_ADDR", ":8080"), "listen address, e.g. :8080")
		data  = fs.String("data", envOr("RAVN_DATA", "/data/ravn.db"), "path of the SQLite database file")
		token = fs.String("token", os.Getenv("RAVN_TOKEN"),
			"bearer token gating /api/v1 routes and root /metrics (opt the latter back out with RAVN_PUBLIC_METRICS=true); empty disables auth (dev default)")
		static = fs.String("static", envOr("RAVN_STATIC_DIR", "./web/dist"),
			"directory holding the built web UI (Vite dist); placeholder root is served when it has no index.html")
		artifacts = fs.String("artifacts-dir", envOr("RAVN_ARTIFACTS_DIR", defaultArtifactsDir()),
			"directory holding stored artifact uploads")
		logLevel = fs.String("log-level", envOr("LOG_LEVEL", envOr("RAVN_LOG_LEVEL", "info")),
			"log level: debug, info, warn, error")
		corsOrigins = fs.String("cors-origins", envOr("RAVN_CORS_ORIGINS", "*"),
			"comma-separated list of allowed CORS origins or '*' for any origin")
		backup = fs.String("backup", "",
			"one-shot mode: write a zero-downtime hot backup (VACUUM INTO) of the database to <path> — must be an absolute path inside RAVN_BACKUP_DIR (default <data dir>/backups) — print a one-line result and exit WITHOUT starting the HTTP server; mutually exclusive with -integrity-check")
		integrityCheck = fs.String("integrity-check", "",
			"one-shot mode: run PRAGMA integrity_check on <db file> with a fresh read-only connection, print ok/FAIL and exit 0/1 without starting the HTTP server; mutually exclusive with -backup")
	)

	if err := fs.Parse(args); err != nil { // bad flag value / unknown flag: usage already printed to errW
		return 2
	}

	// One-shot maintenance modes: -backup and -integrity-check operate on the
	// database file directly and exit — no HTTP server, no artifacts dir. They
	// are checked before any server wiring so a misconfigured deployment still
	// answers to the operator with exactly one line of result.
	if *backup != "" || *integrityCheck != "" {
		return runBackupOrIntegrityCheck(*data, *backup, *integrityCheck, errW)
	}

	lvl := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(errW, &slog.HandlerOptions{
		Level: lvl,
	}))
	slog.SetDefault(logger)
	logger.Info("ravn-server starting",
		"version", version, "addr", *addr, "data", *data, "token_set", *token != "",
		"public_metrics", envBool("RAVN_PUBLIC_METRICS"), "static_dir", *static, "artifacts_dir", *artifacts, "log_level", lvl.String(), "cors_origins", *corsOrigins)

	// Multi-user auth config (T3): validate RAVN_AUTH + its dependencies up front so a
	// misconfigured deployment fails fast with one clear line instead of booting into an
	// inconsistent gate. RAVN_AUTH=token requires a non-empty token; the session TTL and
	// identity-open knobs must parse. The resolved values are re-derived in
	// newRouterWithPoolAndCORS (env is the single source of truth, so nothing crosses over).
	if am, err := httpapi.ResolveAuthMode(os.Getenv("RAVN_AUTH"), *token != ""); err != nil {
		logger.Error("invalid RAVN_AUTH", "err", err)
		return 1
	} else if am == httpapi.AuthModeToken && *token == "" {
		logger.Error("RAVN_AUTH=token requires a non-empty token (flag -token or env RAVN_TOKEN)")
		return 1
	}
	if _, err := httpapi.ParseSessionTTL(os.Getenv("RAVN_SESSION_TTL")); err != nil {
		logger.Error("invalid RAVN_SESSION_TTL", "err", err)
		return 1
	}
	if _, err := httpapi.ParseIdentityOpen(os.Getenv("RAVN_IDENTITY_OPEN")); err != nil {
		logger.Error("invalid RAVN_IDENTITY_OPEN", "err", err)
		return 1
	}

	if err := os.MkdirAll(*artifacts, 0o755); err != nil {
		logger.Error("failed to create artifacts dir", "dir", *artifacts, "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, closePool, err := sqlite.OpenPool(ctx, *data)
	if err != nil {
		logger.Error("database open failed", "err", err)
		return 1
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
	return 0
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

	// RAVN_PUBLIC_METRICS=true opts root /metrics back out of the bearer gate
	// when a token is set (Prometheus scrapers that cannot send Authorization
	// headers). Empty/false/unset → default: gated whenever a token is set.
	publicMetrics := envBool("RAVN_PUBLIC_METRICS")

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

	// Multi-user auth (T3): resolve the gate mode + session TTL from env. run() already
	// validated these and exits on error, so the errors here are non-fatal — on any surprise
	// we fall back to safe defaults (token mode / 24h sessions) rather than fail to boot.
	// The auth use case is only built when a writer DB exists; otherwise Deps.Auth stays nil
	// and the /auth/* routes answer 404 ("not enabled").
	authMode := httpapi.AuthModeToken
	if m, e := httpapi.ResolveAuthMode(os.Getenv("RAVN_AUTH"), token != ""); e == nil {
		authMode = m
	}
	sessionTTL, _ := httpapi.ParseSessionTTL(os.Getenv("RAVN_SESSION_TTL")) // 0/invalid → NewAuth default 24h
	var auth *application.Auth
	if uow != nil {
		auth = application.NewAuth(uow, now, mint, pwhash.New(pwhash.DefaultParams()), sessionTTL)
	}

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
		Auth:         auth,
		Broker:       broker,
		Events:       events,
		ArtifactsDir: artifactsDir,
	})
	httpapi.RegisterRoutes(mux, app)

	metricsHandler := metrics.Handler()
	// Root /metrics lives OUTSIDE the /api prefix, so AuthMiddleware never sees
	// it: gate it explicitly with the same bearer requirement as /api/v1/metrics
	// whenever a token is set. RAVN_PUBLIC_METRICS=true (explicit opt-in) keeps
	// it publicly scrapable for Prometheus clients that cannot send an
	// Authorization header; the empty-token local default stays open, as before.
	mux.Handle("GET /metrics", gateRootMetrics(token, publicMetrics)(metricsHandler))
	mux.Handle("GET /api/v1/metrics", metricsHandler)

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

	// POST /api/v1/admin/backup — zero-downtime hot backup (VACUUM INTO).
	// Security: the route sits under the /api prefix, so the standard
	// AuthMiddleware bearer-gates it whenever RAVN_TOKEN is set (401 on
	// missing/mismatched token) — same posture as every other API route. The
	// destination is additionally restricted by sqlite.ResolveBackupDest to an
	// absolute path strictly inside the backup base directory
	// (RAVN_BACKUP_DIR, default <data dir>/backups) — traversal and symlink
	// escapes are rejected with 400, existing targets with 409 (never
	// overwritten). Body: {"dest":"<absolute path>"}. Answer:
	// 200 {"path":...,"bytes":...}; 400 invalid_input; 409 conflict; 500 on DB failure.
	var poolPath string
	if pool != nil {
		poolPath = pool.Path
	}
	backupBase := resolveBackupBase(poolPath)
	mux.HandleFunc("POST /api/v1/admin/backup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Dest string `json:"dest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   httpapi.ErrCodeInvalidInput,
				"message": fmt.Sprintf("expected JSON body {\"dest\":\"<absolute path inside the backup base directory %s>\"} — e.g. {\"dest\":\"%s\"}", backupBase, filepath.Join(backupBase, "ravn-2026.db")),
			})
			return
		}
		if writerDB == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   httpapi.ErrCodeInternal,
				"message": "no database connected to this server",
			})
			return
		}
		out, n, err := sqlite.BackupTo(r.Context(), writerDB, backupBase, req.Dest)
		// Each branch writes EXACTLY ONE response (no fall-through — the first
		// WriteHeader of a request is the only legal one).
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{"path": out, "bytes": n})
		case errors.Is(err, sqlite.ErrBackupPathInvalid):
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   httpapi.ErrCodeInvalidInput,
				"message": err.Error(),
			})
		case errors.Is(err, sqlite.ErrBackupExists):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   httpapi.ErrCodeConflict,
				"message": err.Error(),
			})
		default:
			slog.Error("admin hot backup failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   httpapi.ErrCodeInternal,
				"message": "hot backup failed (VACUUM INTO error — see server log)",
			})
		}
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

	apiHandler := httpapi.AuthGate(authMode, token, auth)(mux)
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
// A GET/HEAD whose path maps to a real file is served as-is with its content
// type; any other request that is NOT under /api or /healthz falls back to
// index.html so client-side routing deep links work. Paths under /api,
// /healthz, or /metrics that still reach this handler are unknown routes — 404,
// never index.html (the real patterns for them are more specific than "GET /"
// and win first).
//
// Only real files are delegated to FileServer. Directories are deliberately NOT
// served as-is: for any subdirectory without an index.html the stdlib emits an
// auto-generated HTML listing — information disclosure of the asset tree.
// Directory requests therefore fall through like any other unmatched route:
// /api, /healthz, /metrics, or /artifacts prefixes 404; everything else gets
// the SPA root index (client-side routing). This is intentional even if a
// future subdirectory contains its own index.html — this handler never surfaces
// subdirectory content via the file server.
//
// The fallback rewrites the path to "/" rather than "/index.html": FileServer
// itself redirects any request whose path ends in "/index.html" to "./" (see
// serveFile in net/http/fs.go), which would loop on deep links. Serving the
// dist root is how FileServer delivers index.html anyway — 200, no redirect.
func spaFallback(static fs.FS) http.Handler {
	fileServer := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Real files only — never directories: delegating a directory to
		// FileServer would let the stdlib emit an auto-generated HTML listing
		// for any subdirectory lacking index.html (information disclosure).
		if info, err := fs.Stat(static, strings.TrimPrefix(r.URL.Path, "/")); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r) // real file: served as-is with its content type
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

// resolveBackupBase returns the directory hot backups may be written into:
// env RAVN_BACKUP_DIR wins; otherwise a "backups" subdirectory of the data
// directory (RAVN_DATA=/data/ravn.db → /data/backups, i.e. on the ravn-data
// volume in Docker). When no data path is known (raw *sql.DB pools built in
// tests) it degrades to CWD-relative "backups" — still absolute-anchored and
// validated by sqlite.ResolveBackupDest before any write.
func resolveBackupBase(dataPath string) string {
	if v := strings.TrimSpace(os.Getenv("RAVN_BACKUP_DIR")); v != "" {
		return filepath.Clean(v)
	}
	if dataPath != "" && dataPath != ":memory:" {
		return filepath.Join(filepath.Dir(dataPath), "backups")
	}
	return "backups"
}

// runBackupOrIntegrityCheck implements the one-shot maintenance modes and
// returns the process exit code. Exactly one of backupDest / checkPath must be
// non-empty (enforced before the call). Each mode prints EXACTLY ONE line of
// result to w and never starts the HTTP server:
//
//	-backup <dest>            open the DB, VACUUM INTO a hot snapshot at dest
//	                          (validated against the backup base dir), verify it
//	                          with PRAGMA integrity_check → 0 on ok.
//	-integrity-check <file>   fresh read-only PRAGMA integrity_check → 0 on "ok".
func runBackupOrIntegrityCheck(dataPath, backupDest, checkPath string, w io.Writer) int {
	if backupDest != "" && checkPath != "" {
		fmt.Fprintln(w, "ravn-server: -backup and -integrity-check are mutually exclusive")
		return 2
	}
	ctx := context.Background()

	if checkPath != "" {
		if err := sqlite.IntegrityCheck(ctx, checkPath); err != nil {
			fmt.Fprintf(w, "integrity_check FAIL: %v\n", err)
			return 1
		}
		fmt.Fprintf(w, "integrity_check ok: %s\n", checkPath)
		return 0
	}

	db, closeDB, err := sqlite.Open(ctx, dataPath)
	if err != nil {
		fmt.Fprintf(w, "backup failed: open database %q: %v\n", dataPath, err)
		return 1
	}
	defer func() { _ = closeDB() }()

	out, n, err := sqlite.BackupTo(ctx, db, resolveBackupBase(dataPath), backupDest)
	if err != nil {
		fmt.Fprintf(w, "backup failed: %v\n", err)
		return 1
	}
	if iErr := sqlite.IntegrityCheck(ctx, out); iErr != nil {
		fmt.Fprintf(w, "backup written but integrity_check FAIL: %s (%d bytes) — %v\n", out, n, iErr)
		return 1
	}
	fmt.Fprintf(w, "backup ok: %s (%d bytes) integrity_check=ok\n", out, n)
	return 0
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

// writeJSON serializes v as a JSON response with the given status. A marshal
// failure is a bug (healthResponse cannot fail to marshal); it degrades to a
// 500 with the standard JSON error envelope — raw bytes with Content-Type
// application/json, NOT http.Error's text/plain + trailing newline, because
// every server response body is JSON (API.md contract).
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal response failed; writing JSON fallback", "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal","message":"failed to marshal response"}`)) //nolint:errcheck
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

// envBool reports whether the environment variable key is set to a truthy
// boolean value (strconv.ParseBool semantics: "1", "t", "true" etc., case
// insensitive). Unset, empty, or unparseable values yield false — fail closed.
func envBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// gateRootMetrics returns the middleware applied to root /metrics: bearer auth
// (the same 401 contract as AuthMiddleware) whenever a token is configured,
// unless RAVN_PUBLIC_METRICS explicitly opted the route in for public scraping.
// With no token it returns next untouched — the zero-config local default keeps
// /metrics openly scrapable, preserving existing local Prometheus setups.
func gateRootMetrics(token string, publicMetrics bool) func(http.Handler) http.Handler {
	if token == "" || publicMetrics {
		return func(next http.Handler) http.Handler { return next }
	}
	return httpapi.BearerGate(token)
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
