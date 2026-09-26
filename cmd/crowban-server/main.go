// Command crowban-server is composition root 1 for crowban (ARCHITECTURE §2):
// env/flag config → SQLite open + migrations → shared Clock/IDGenerator/
// UnitOfWork → ALL use cases → the HTTP adapter (internal/interface/http) on
// a stdlib net/http ServeMux.
//
// This is the ONLY place that imports infrastructure; the http layer receives
// ready-made use-case structs and never sees the database. Slice 3a routes
// Projects + Task-core + the board snapshot; Comments/Artifacts/Acceptance/
// Relations are already constructed so slice 3b only adds routes. CROWBAN_TOKEN
// is accepted but not enforced until the auth middleware lands.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/infrastructure/clock"
	"github.com/mrhollen/crowban/internal/infrastructure/idgen"
	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
	httpapi "github.com/mrhollen/crowban/internal/interface/http"
)

// version is stamped at build time: -ldflags "-X main.version=..." (see the
// Dockerfile blueprint in ARCHITECTURE §8).
var version = "dev"

func main() {
	var (
		addr  = flag.String("addr", envOr("CROWBAN_ADDR", ":8080"), "listen address, e.g. :8080")
		data  = flag.String("data", envOr("CROWBAN_DATA", "/data/crowban.db"), "path of the SQLite database file")
		token = flag.String("token", os.Getenv("CROWBAN_TOKEN"), "bearer token; accepted but unused until the auth middleware lands")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	logger.Info("crowban-server starting",
		"version", version, "addr", *addr, "data", *data, "token_set", *token != "")

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
		Handler:           newRouter(db),
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
// /api/v1 routes alongside /healthz and the root placeholder.
func newRouter(db *sql.DB) http.Handler {
	mux := http.NewServeMux()

	// Slice 3 composition: one shared transactional seam, clock, and idgen —
	// every use case mints IDs and stamps timestamps before touching ports
	// (ADR 2026-09-25). Comments/Artifacts/Acceptance/Relations are wired now
	// even though slice 3b is what routes them.
	uow := sqlite.NewUnitOfWork(db)
	now := clock.New()
	mint := idgen.New()

	app := httpapi.NewApp(httpapi.Deps{
		Projects:   application.NewProjects(uow, now, mint),
		Tasks:      application.NewTasks(uow, now, mint),
		Comments:   application.NewComments(uow, now, mint),
		Artifacts:  application.NewArtifacts(uow, now, mint),
		Acceptance: application.NewAcceptance(uow, now, mint),
		Relations:  application.NewRelations(uow, now, mint),
	})
	httpapi.RegisterRoutes(mux, app)

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

	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "crowban server ok — UI ships later (static serving lands in a later slice)")
	})

	return mux
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
