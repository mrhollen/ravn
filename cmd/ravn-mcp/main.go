// ravn-mcp exposes the ravn REST API as MCP tools over stateless
// Streamable HTTP plus legacy SSE for GET-only clients (docs/ARCHITECTURE.md §6).
// It is a pure protocol adapter — no SQLite, no use cases: env config →
// apiclient → MCP server → http.Server.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
	"github.com/mrhollen/ravn/internal/mcp"
)

// version is set at build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	var (
		addr      = flag.String("addr", envOr("MCP_ADDR", ":9080"), "listen address for the MCP HTTP server, e.g. :9080")
		apiURL    = flag.String("api-url", envOr("RAVN_API_URL", "http://ravn-server:8080"), "upstream ravn-server API URL")
		token     = flag.String("token", os.Getenv("RAVN_TOKEN"), "bearer token forwarded to ravn-server; gates incoming MCP requests when set")
		agentName = flag.String("agent-name", envOr("RAVN_AGENT_NAME", "agent"), "default actor name stamped on tool calls when omitted")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	handler, streamable, sse := buildHandlerStack(logger, *apiURL, *token, *agentName)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("ravn-mcp listening", "addr", *addr, "upstream", *apiURL, "default_actor", *agentName)
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

	shutdownServer(logger, srv, streamable, sse, 10*time.Second)
	logger.Info("stopped")
}

// buildHandlerStack wires the MCP protocol adapter exactly as production does:
// tool/prompt/resource registration on a shared MCPServer behind both the
// streamable-HTTP and legacy SSE transports, plus the auth/logging/compat
// middleware. It returns the composed http.Handler and the two streaming
// transports so callers can close their sessions on shutdown (see
// shutdownServer).
func buildHandlerStack(logger *slog.Logger, apiURL, token, agentName string) (http.Handler, *mcpsrv.StreamableHTTPServer, *mcpsrv.SSEServer) {
	// The container default actor. RAVN_AGENT_NAME names THIS agent so its
	// claims/attribution survive even on calls where the model forgot to pass
	// an explicit actor (claims key on the name).
	defaultActor := apiclient.Actor{Type: "agent", Name: agentName}

	client := apiclient.New(apiURL, token)

	mcs := mcpsrv.NewMCPServer("ravn", version, mcp.ServerOptions()...)
	mcp.RegisterTools(mcs, client, defaultActor)
	mcp.RegisterPrompts(mcs, client, defaultActor)
	mcp.RegisterResources(mcs, client)

	streamable := mcpsrv.NewStreamableHTTPServer(mcs,
		mcpsrv.WithStateLess(true), // no session state: every request is self-contained — restartable/scaleable
		mcpsrv.WithEndpointPath("/mcp"),
		// ravn is explicitly single-tenant and LAN-local (ARCHITECTURE §1).
		// Agents commonly run on the same host as the server and dial it through
		// a non-localhost Host (LAN IP or hostname) on a loopback listen; the
		// SDK's DNS-rebinding guard would 403 exactly that traffic, so we take
		// the documented escape hatch.
		mcpsrv.WithDisableLocalhostProtection(true),
	)

	// Legacy SSE transport for clients that only speak the pre-streamable MCP
	// protocol (opening the session with an HTTP GET to /sse). It shares the same
	// MCPServer/tool registry:
	//   GET  /sse     → open an SSE event stream (canonical legacy endpoint)
	//   POST /message → JSON-RPC into the session advertised by the `endpoint` event
	sse := mcpsrv.NewSSEServer(mcs,
		mcpsrv.WithKeepAlive(true),
		mcpsrv.WithKeepAliveInterval(30*time.Second),   // ping keeps idle streams through proxies
		mcpsrv.WithSSEDisableLocalhostProtection(true), // same LAN-local rationale as the streamable server above
		mcpsrv.WithAppendQueryToMessageEndpoint(),
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable) // streamable handles both POST (requests/notifs) and GET (listening stream)
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", messageHandlerWithSessionFallback(sse.MessageHandler()))
	mux.HandleFunc("GET /healthz", mcp.HealthzHandler(client))

	return withLogging(logger, withAuth(token, withModernMCPCompat(mux))), streamable, sse
}

// shutdownServer stops the MCP server in two stages. First, active streaming
// sessions (legacy SSE event streams and streamable-HTTP GET notification
// streams) are closed — these handlers run indefinitely and would otherwise
// block srv.Shutdown for the entire drain window. Both CloseSessions calls
// only signal termination; their in-flight handlers exit asynchronously. Then
// any remaining in-flight handlers drain within timeout, force-closing as a
// last resort.
func shutdownServer(logger *slog.Logger, srv *http.Server, streamable *mcpsrv.StreamableHTTPServer, sse *mcpsrv.SSEServer, timeout time.Duration) {
	logger.Info("closing active SSE/streaming sessions")
	sse.CloseSessions()
	streamable.CloseSessions(context.Background())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed; forcing close", "err", err)
		srv.Close()
	}
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
	if logger == nil {
		logger = slog.Default()
	}
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

// messageHandlerWithSessionFallback ensures that if a client forwards sessionId
// via Mcp-Session-Id header or session_id query param instead of sessionId query param,
// it is mapped into the query param so mark3labs SSEServer accepts it.
func messageHandlerWithSessionFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sessionId") == "" {
			if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
				q := r.URL.Query()
				q.Set("sessionId", sid)
				r.URL.RawQuery = q.Encode()
			} else if sid := r.URL.Query().Get("session_id"); sid != "" {
				q := r.URL.Query()
				q.Set("sessionId", sid)
				r.URL.RawQuery = q.Encode()
			}
		}
		next.ServeHTTP(w, r)
	})
}

const defaultMaxMCPPayloadBytes = 4 << 20 // 4MB

func maxMCPPayloadBytes() int64 {
	if v := os.Getenv("MCP_MAX_PAYLOAD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxMCPPayloadBytes
}

// withModernMCPCompat ensures that incoming requests carrying Mcp-Protocol-Version
// header have the corresponding _meta protocolVersion populated in params if missing,
// so strict mark3labs/mcp-go modern validation does not reject client notifications
// (such as notifications/roots/list_changed from modelcontextprotocol/go_sdk) with 400.
// It also bounds memory consumption by enforcing maxMCPPayloadBytes on incoming bodies
// using io.LimitReader, rejecting oversized requests with HTTP 413 Payload Too Large.
func withModernMCPCompat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			limit := maxMCPPayloadBytes()
			if r.ContentLength > limit {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":   "payload_too_large",
					"message": fmt.Sprintf("request payload exceeds limit of %d bytes", limit),
				})
				return
			}

			protoVer := r.Header.Get("Mcp-Protocol-Version")
			if protoVer != "" && r.Body != nil {
				bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error":   "bad_request",
						"message": "failed to read request body",
					})
					return
				}
				if int64(len(bodyBytes)) > limit {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error":   "payload_too_large",
						"message": fmt.Sprintf("request payload exceeds limit of %d bytes", limit),
					})
					return
				}

				if len(bodyBytes) > 0 {
					var raw map[string]any
					if err := json.Unmarshal(bodyBytes, &raw); err == nil {
						params, _ := raw["params"].(map[string]any)
						if params == nil {
							params = make(map[string]any)
							raw["params"] = params
						}
						meta, _ := params["_meta"].(map[string]any)
						if meta == nil {
							meta = make(map[string]any)
							params["_meta"] = meta
						}
						if _, ok := meta["io.modelcontextprotocol/protocolVersion"]; !ok {
							meta["io.modelcontextprotocol/protocolVersion"] = protoVer
							if modified, err := json.Marshal(raw); err == nil {
								bodyBytes = modified
							}
						}
					}
					r.ContentLength = int64(len(bodyBytes))
					r.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
				}
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			} else if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth gates endpoints (/mcp, /sse, /message) with Bearer token authentication
// when expectedToken is non-empty. When expectedToken is empty, all requests pass
// through untouched (local dev mode). /healthz is never gated.
func withAuth(expectedToken string, next http.Handler) http.Handler {
	if expectedToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMCPRoute(r.URL.Path) {
			auth := r.Header.Get("Authorization")
			if !validBearerToken(auth, expectedToken) {
				qToken := r.URL.Query().Get("token")
				if qToken == "" || subtle.ConstantTimeCompare([]byte(qToken), []byte(expectedToken)) != 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error":   "unauthorized",
						"message": "missing or invalid auth token",
					})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMCPRoute(p string) bool {
	cleaned := path.Clean(p)
	return cleaned == "/mcp" || cleaned == "/sse" || cleaned == "/message"
}

func validBearerToken(header, expected string) bool {
	if header == "" || expected == "" {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
