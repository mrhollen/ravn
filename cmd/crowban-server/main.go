// Command crowban-server is composition root 1 for crowban (ARCHITECTURE §2):
// env/flag config → SQLite open + migrations → stdlib net/http ServeMux.
//
// This slice wires only what exists yet: the database and the HTTP server
// exposing GET /healthz (the exact contract from ARCHITECTURE §8) plus a
// placeholder root. The REST API (internal/api), bearer-token auth, and the
// built web UI land in later slices — CROWBAN_TOKEN is accepted here but not
// enforced yet.
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

	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

// version is stamped at build time: -ldflags "-X main.version=..." (see the
// Dockerfile blueprint in ARCHITECTURE §8).
var version = "dev"

func main() {
	var (
		addr  = flag.String("addr", envOr("CROWBAN_ADDR", ":8080"), "listen address, e.g. :8080")
		data  = flag.String("data", envOr("CROWBAN_DATA", "/data/crowban.db"), "path of the SQLite database file")
		token = flag.String("token", os.Getenv("CROWBAN_TOKEN"), "bearer token; accepted but unused until the api layer lands")
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
// Later slices will add the /api/v1 routes (internal/api) here.
func newRouter(db *sql.DB) http.Handler {
	mux := http.NewServeMux()

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
	w.Write(body)
}

// envOr returns the environment variable key if set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
