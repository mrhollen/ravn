// crowban-mcp exposes the crowban REST API as MCP tools over stateless
// Streamable HTTP plus legacy SSE for GET-only clients (docs/ARCHITECTURE.md §6).
// It is a pure protocol adapter — no SQLite, no use cases: env config →
// apiclient → MCP server → http.Server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/crowban/internal/infrastructure/apiclient"
	"github.com/mrhollen/crowban/internal/mcp"
)

// version is set at build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	addr := flag.String("addr", envOr("MCP_ADDR", ":9080"), "listen address for the MCP HTTP server, e.g. :9080")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	apiURL := envOr("CROWBAN_API_URL", "http://crowban-server:8080")
	token := os.Getenv("CROWBAN_TOKEN") // optional; forwarded as Bearer when set
	// The container default actor. CROWBAN_AGENT_NAME names THIS agent so its
	// claims/attribution survive even on calls where the model forgot to pass
	// an explicit actor (claims key on the name).
	defaultActor := apiclient.Actor{Type: "agent", Name: envOr("CROWBAN_AGENT_NAME", "agent")}

	client := apiclient.New(apiURL, token)

	mcs := mcpsrv.NewMCPServer("crowban", version, mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)

	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true), // no session state: every request is self-contained — restartable/scaleable
		mcpsrv.WithEndpointPath("/mcp"),
		// crowban is explicitly single-tenant and LAN-local (ARCHITECTURE §1).
		// Agents commonly run on the same host as the server and dial it through
		// a non-localhost Host (LAN IP or hostname) on a loopback listen; the
		// SDK's DNS-rebinding guard would 403 exactly that traffic, so we take
		// the documented escape hatch.
		mcpsrv.WithDisableLocalhostProtection(true),
	)

	// Legacy SSE transport for clients that only speak the pre-streamable MCP
	// protocol (e.g. Antigravity's remote-MCP integration, which opens its
	// session with an HTTP GET). It shares the same MCPServer/tool registry:
	//   GET  /mcp     → open an SSE event stream (dual-mode at the canonical URL)
	//   GET  /sse     → open an SSE event stream (canonical legacy endpoint)
	//   POST /message → JSON-RPC into the session advertised by the `endpoint` event
	sse := mcpsrv.NewSSEServer(mcs,
		mcpsrv.WithKeepAlive(true),
		mcpsrv.WithKeepAliveInterval(30*time.Second),   // ping keeps idle streams through proxies
		mcpsrv.WithSSEDisableLocalhostProtection(true), // same LAN-local rationale as the streamable server above
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp", sse.SSEHandler().ServeHTTP) // dual-mode: GET = SSE, POST/other = below
	mux.Handle("/mcp", streamable)
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", sse.MessageHandler())
	mux.HandleFunc("GET /healthz", mcp.HealthzHandler(client))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           withLogging(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("crowban-mcp listening", "addr", *addr, "upstream", apiURL, "default_actor", defaultActor.Name)
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// withLogging is a minimal slog request logger. Healthy /healthz polls are
// suppressed so compose healthchecks don't drown the log; everything else logs
// method, path, status and duration.
func withLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" && rec.status < http.StatusBadRequest {
			return
		}
		logger.Info("http request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the wrapped writer when it supports streaming, so
// SSE handlers can assert http.Flusher on this wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
