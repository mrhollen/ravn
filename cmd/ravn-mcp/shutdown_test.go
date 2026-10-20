package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestShutdownClosesStreamingSessions proves the shutdown fix for task
// 01M3MN2T0JJ0K7RDCPAVT3V6HK: an established /sse stream is terminated by
// closing streaming sessions before srv.Shutdown, so the bounded Shutdown
// completes in milliseconds instead of hanging on the indefinite SSE handler.
//
// Pre-fix behavior (no session close before Shutdown): Shutdown blocks for the
// full 2s test timeout on the live /sse handler, returns
// context.DeadlineExceeded, and shutdownServer falls back to srv.Close() with
// a "graceful shutdown failed" log line. This test asserts none of that:
// shutdown completes well under the timeout and without force-close, and the
// SSE body read ends with io.EOF promptly.
func TestShutdownClosesStreamingSessions(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	// Real handler stack exactly as main builds it: shared MCPServer + tools/
	// prompts/resources, streamable HTTP, legacy SSE, mux, middleware. The
	// upstream API URL is a dummy — the SSE endpoint never dials upstream.
	handler, streamable, sse := buildHandlerStack(logger, "http://127.0.0.1:9", "", "test-agent")

	ln, err := net.Listen("tcp", "127.0.0.1:0") // ephemeral port; never 8080
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	baseURL := "http://" + ln.Addr().String()

	// Open a real SSE connection and wait for the first event (the endpoint
	// advertisement, written+flushed before the handler's event loop starts)
	// to confirm the stream is established.
	req, err := http.NewRequest(http.MethodGet, baseURL+"/sse", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open /sse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sse status = %d, want 200", resp.StatusCode)
	}
	first := make([]byte, 4096)
	n, err := resp.Body.Read(first)
	if err != nil {
		t.Fatalf("read first SSE event: %v", err)
	}
	if !strings.Contains(string(first[:n]), "event: endpoint") {
		t.Fatalf("SSE stream not established; got: %q", string(first[:n]))
	}

	// Drain the rest of the body in a goroutine; record how it terminates and
	// when, so we can assert the stream ends with a clean EOF after shutdown.
	type readResult struct {
		err     error
		elapsed time.Duration
	}
	readDone := make(chan readResult, 1)
	start := time.Now()
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		readDone <- readResult{err: err, elapsed: time.Since(start)}
	}()

	// Trigger the shutdown path exactly as main does now: close streaming
	// sessions first, then bounded Shutdown — here with a 2s test timeout in
	// place of production's 10s.
	shutdownStart := time.Now()
	shutdownServer(logger, srv, streamable, sse, 2*time.Second)
	shutdownElapsed := time.Since(shutdownStart)

	if shutdownElapsed >= 2*time.Second {
		t.Fatalf("shutdown took %v — hung on the live SSE handler (pre-fix failure mode), want well under 2s", shutdownElapsed)
	}
	if strings.Contains(logBuf.String(), "graceful shutdown failed") {
		t.Fatalf("force-close fallback was triggered; Shutdown did not complete gracefully: %q", logBuf.String())
	}

	var res readResult
	select {
	case res = <-readDone:
		// io.Copy reports a plain EOF (stream cleanly terminated when the
		// server's SSE handler returned) as nil; anything else — e.g. a
		// connection reset from force-closing the listener — is a failure.
		if res.err != nil && !errors.Is(res.err, io.EOF) {
			t.Fatalf("SSE body read ended with %v, want clean EOF after session close", res.err)
		}
		if res.elapsed >= 2*time.Second {
			t.Fatalf("SSE stream took %v to terminate, want well under 2s", res.elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("SSE stream did not terminate within 1s after shutdown")
	}

	t.Logf("shutdown completed in %v; SSE stream ended with EOF after %v", shutdownElapsed, res.elapsed)
}
