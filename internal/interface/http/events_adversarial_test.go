package httpapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
	"github.com/mrhollen/ravn/internal/infrastructure/pubsub"
)

// TestAdversarial_SSE_MassiveAbruptDisconnects tests 50 concurrent SSE clients
// connecting, receiving initial data, and abruptly disconnecting while
// mutations are actively being published to the event broker.
// Verifies: zero subscriber leaks, prompt termination.
func TestAdversarial_SSE_MassiveAbruptDisconnects(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Massive Disconnect Project")

	const numClients = 50
	var wg sync.WaitGroup
	wg.Add(numClients)

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	connectedCount := int32(0)

	for i := 0; i < numClients; i++ {
		go func(idx int) {
			defer wg.Done()

			reqCtx, reqCancel := context.WithCancel(clientCtx)
			defer reqCancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
			if err != nil {
				return
			}

			res, err := srv.Client().Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()

			reader := bufio.NewReader(res.Body)
			// Read until we see the connected comment
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.Contains(line, ": connected") {
					atomic.AddInt32(&connectedCount, 1)
					break
				}
			}

			// Half of clients disconnect immediately; half wait a moment
			if idx%2 == 0 {
				reqCancel()
				return
			}

			time.Sleep(30 * time.Millisecond)
			reqCancel()
		}(i)
	}

	// Wait until at least 40 clients have connected
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&connectedCount) >= 40 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Concurrently create tasks to trigger event broadcasts
	for j := 0; j < 15; j++ {
		newTask(t, stk, proj.ID, fmt.Sprintf("Disconnect Broadcast Task %d", j))
	}

	// Wait for all client goroutines to finish disconnecting
	wg.Wait()

	// Verify subscriber count drops to 0
	drainDeadline := time.Now().Add(3 * time.Second)
	cleaned := false
	for time.Now().Before(drainDeadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 0 {
			cleaned = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !cleaned {
		rem := broker.SubscriberCountForProject(domain.ID(proj.ID))
		t.Fatalf("subscriber count = %d, expected 0 after massive client disconnects (leak!)", rem)
	}
}

// TestAdversarial_SSE_RawTCPHardResetLingerZero connects via raw TCP, reads
// the SSE greeting, and abruptly resets the connection via TCP RST (SetLinger(0)).
// Verifies that when a subsequent event is written, the server detects the broken
// socket, aborts the stream, and releases the broker subscription.
func TestAdversarial_SSE_RawTCPHardResetLingerZero(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "TCP Reset Project")

	serverAddr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("dial raw tcp: %v", err)
	}

	// Send HTTP GET for project events
	reqStr := fmt.Sprintf("GET /api/v1/projects/%s/events HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", proj.ID, serverAddr)
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		conn.Close()
		t.Fatalf("write raw http request: %v", err)
	}

	// Read until we see the connected comment
	reader := bufio.NewReader(conn)
	connected := false
	for i := 0; i < 20; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read raw http response: %v", err)
		}
		if strings.Contains(line, ": connected") {
			connected = true
			break
		}
	}

	if !connected {
		conn.Close()
		t.Fatal("did not receive ': connected' header comment")
	}

	// Verify subscriber registered in broker
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 1 {
		conn.Close()
		t.Fatalf("subscriber count = %d, want 1", count)
	}

	// Abrupt TCP RST: SetLinger(0) causes Close() to immediately send RST instead of FIN
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.Close()

	// Small pause so OS resets the connection
	time.Sleep(30 * time.Millisecond)

	// Publish an event: server will attempt to write to the reset socket
	newTask(t, stk, proj.ID, "Post RST Task")

	// Verify server detects write failure and cleans up broker subscriber
	deadline := time.Now().Add(2 * time.Second)
	cleaned := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 0 {
			cleaned = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !cleaned {
		t.Errorf("subscriber count = %d, want 0 after TCP RST and event write", broker.SubscriberCountForProject(domain.ID(proj.ID)))
	}
}

// TestAdversarial_SSE_SlowClientChannelBufferDrop covers the graduated
// slow-client semantics (commit 0bfd7b9) at the SSE level. A burst that fills a
// subscriber's buffer does NOT evict it; eviction happens only on SUSTAINED
// saturation (consecutive full publishes reaching the tolerance, or the
// wall-clock grace window elapsing — see the deterministic broker unit tests in
// internal/infrastructure/pubsub). The legacy version of this test fired one
// 200-event burst at a stalled real-TCP client against the production-default
// broker (tolerance 16 / grace 1s) and asserted eviction within 2s — but kernel
// socket buffers let streamEvents keep draining the channel, resetting the fail
// streak, so the outcome was scheduling-dependent (the -race flake this task
// tracks). Neither outcome is correct to assert: it was stale semantics.
//
// This version makes every outcome a pure function of event counts, mirroring
// the broker unit tests' technique:
//   - a zero-buffer net.Pipe stands in for the client socket (same scaffold as
//     TestAdversarial_SSE_StalledClientPipeWriteDeadline): once the stalled
//     client stops reading, AT MOST ONE frame is ever written — the handler
//     then blocks inside that single write and can drain no further;
//   - a broker built with deterministic knobs (WithSlowClientTolerance +
//     WithSlowClientGrace(0) disabling the wall-clock dimension entirely) is
//     swapped into the stack through the same post-construction Deps seam as
//     the SSEWriteDeadline swap, before any client connects;
//   - publish counts are exact, so under -race both paths hold for every
//     interleaving: sustained saturation evicts exactly once (and logs the
//     AC-3 warning), while a one-shot burst within tolerance retains the client
//     with only bounded, replay-recoverable frame loss.
func TestAdversarial_SSE_SlowClientChannelBufferDrop(t *testing.T) {
	cases := []struct {
		name          string
		bufferSize    int // subscriber channel capacity (knob)
		tolerance     int // consecutive full publishes before eviction (count dimension)
		burst         int // events published at the stalled client
		writeDeadline time.Duration // injected per-write deadline (Deps.SSEWriteDeadline)
		wantEvicted   bool
	}{
		// Count dimension tripped: with tolerance 1 the first sustained full
		// publish evicts, exactly as under the pre-0bfd7b9 immediate-drop era —
		// but now driven deterministically instead of by wall-clock luck.
		{name: "sustained_saturation_evicts_stalled_sse_client", bufferSize: 4, tolerance: 1, burst: 6, writeDeadline: 250 * time.Millisecond, wantEvicted: true},
		// Burst within a production-style tolerance: worst-case fail streak is
		// burst-buffer-1 = 4 << tolerance 16, so the client must be RETAINED —
		// AC-2's "bursts must not kill clients" pinned end-to-end. The
		// sustained-saturation eviction path itself is owned by the broker unit
		// tests (TestBroker_GraduatedBurstHandling et al.).
		{name: "one_shot_burst_within_tolerance_retains_client", bufferSize: 4, tolerance: 16, burst: 9, writeDeadline: time.Second, wantEvicted: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf strings.Builder
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			stk, _, _ := newRealtimeStack(t)
			proj := createProject(t, stk, "Graduated Slow Client Project")

			// Deterministic-knob broker (wall-clock dimension disabled). Same seam
			// as the SSEWriteDeadline swap below; done before any client connects,
			// so no request can observe the default broker.
			broker := pubsub.NewBroker(
				pubsub.WithBufferSize(tc.bufferSize),
				pubsub.WithSlowClientTolerance(tc.tolerance),
				pubsub.WithSlowClientGrace(0),
				pubsub.WithLogger(logger),
			)
			stk.App.Broker = broker
			stk.App.SSEWriteDeadline = tc.writeDeadline

			serverEnd, clientEnd := net.Pipe()
			defer serverEnd.Close()
			defer clientEnd.Close()

			lis := &oneShotPipeListener{ch: make(chan net.Conn, 1)}
			lis.ch <- serverEnd
			pipeSrv := &http.Server{Handler: stk.Handler}
			go pipeSrv.Serve(lis)
			t.Cleanup(func() { _ = pipeSrv.Close() })

			pID := domain.ID(proj.ID)

			// Stalled client: drive reads through ": connected", then NEVER read again.
			if _, err := clientEnd.Write([]byte(fmt.Sprintf(
				"GET /api/v1/projects/%s/events HTTP/1.1\r\nHost: pipe\r\nAccept: text/event-stream\r\n\r\n", proj.ID))); err != nil {
				t.Fatalf("write request to pipe: %v", err)
			}
			reader := bufio.NewReader(clientEnd)
			connected := false
			for i := 0; i < 20 && !connected; i++ {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatalf("read pipe response: %v", err)
				}
				if strings.Contains(line, ": connected") {
					connected = true
				}
			}
			if !connected {
				t.Fatal("did not receive ': connected' over pipe")
			}

			waitForSub := func(within time.Duration, want int, what string) {
				t.Helper()
				deadline := time.Now().Add(within)
				for time.Now().Before(deadline) {
					if broker.SubscriberCountForProject(pID) == want {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("timed out waiting for %s (count = %d)", what, broker.SubscriberCountForProject(pID))
			}
			waitForSub(2*time.Second, 1, "subscriber registration")

			// Flood the stalled client with an exact burst. The zero-buffer pipe
			// guarantees at most one frame ever leaves the handler (it then blocks
			// inside that single write), so the broker's per-subscriber accounting
			// is decided by publish counts alone — no wall clock, no socket sizing.
			for i := 0; i < tc.burst; i++ {
				broker.Publish(domain.Event{
					ID:        domain.ID(fmt.Sprintf("SSE-GRAD-%d", i)),
					ProjectID: &pID,
					Kind:      domain.EventUpdated,
					At:        time.Now().UTC(),
				})
			}

			if tc.wantEvicted {
				// Eviction happens synchronously inside Publish — no polling needed.
				if got := broker.DroppedCount(); got != 1 {
					t.Fatalf("DroppedCount = %d, want 1 (stalled client evicted exactly once)", got)
				}
				// One frame dropped per sustained full publish until eviction:
				// exactly tolerance frames lost, all others replayable via Last-Event-ID.
				if got := broker.DroppedEventCount(); got != uint64(tc.tolerance) {
					t.Errorf("DroppedEventCount = %d, want %d", got, tc.tolerance)
				}
				logOut := logBuf.String()
				if !strings.Contains(logOut, "evicting subscriber") {
					t.Errorf("expected slow-client eviction warning, got: %q", logOut)
				}
				if !strings.Contains(logOut, "subscriber_project="+proj.ID) {
					t.Errorf("eviction warning should name the affected project, got: %q", logOut)
				}

				// streamEvents is blocked inside its single pipe write; the injected
				// per-write deadline pulls it out, then defer cancel() releases the
				// subscription (no zombie goroutine). Healthy run: ~writeDeadline.
				cleanDeadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(cleanDeadline) {
					if broker.SubscriberCountForProject(pID) == 0 {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("streamEvents did not terminate after eviction: subscriber still registered (count = %d)", broker.SubscriberCountForProject(pID))
			}

			// Retention path: the burst stayed within tolerance — nothing evicted,
			// the subscription survives, and no warning was logged for a merely
			// saturated client.
			if got := broker.DroppedCount(); got != 0 {
				t.Fatalf("DroppedCount = %d, want 0 (burst within tolerance must not evict)", got)
			}
			if got := broker.SubscriberCountForProject(pID); got != 1 {
				t.Errorf("subscriber count = %d, want 1 after a burst within tolerance", got)
			}
			// Loss is bounded deterministically: the stalled pipe guarantees at most
			// one drained frame, so drops are in [burst-buffer-1, burst-buffer].
			minDrops := uint64(tc.burst - tc.bufferSize - 1)
			maxDrops := uint64(tc.burst - tc.bufferSize)
			if got := broker.DroppedEventCount(); got < minDrops || got > maxDrops {
				t.Errorf("DroppedEventCount = %d, want in [%d, %d] (bounded, replay-recoverable loss)", got, minDrops, maxDrops)
			}
			if logOut := logBuf.String(); logOut != "" {
				t.Errorf("no eviction warning expected while the client is merely saturated, got: %q", logOut)
			}

			// Cleanup: close the stalled pipe; the handler's blocked write errors and
			// it exits via the write-error path, releasing the subscription.
			_ = clientEnd.Close()
			cleanDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(cleanDeadline) {
				if broker.SubscriberCountForProject(pID) == 0 {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("stalled client was not released after pipe close: subscriber still registered (count = %d)", broker.SubscriberCountForProject(pID))
		})
	}
}

// TestAdversarial_SSE_WriteChunkFailureScenarios tests various write failure
// paths in streamEvents (write error on connected comment, write error on ping,
// write error on event).
func TestAdversarial_SSE_WriteChunkFailureScenarios(t *testing.T) {
	t.Run("FailureOnInitialConnectedComment", func(t *testing.T) {
		stk, broker, _ := newRealtimeStack(t)
		proj := createProject(t, stk, "Initial Fail Project")

		rec := &faultyResponseWriter{
			failOnCount: 1, // fail on first writeChunk (connected comment)
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			req := httptest.NewRequest("GET", "/api/v1/projects/"+proj.ID+"/events", nil)
			stk.Handler.ServeHTTP(rec, req)
		}()

		select {
		case <-done:
			// Completed promptly
		case <-time.After(1 * time.Second):
			t.Fatal("handler did not exit promptly on initial write failure")
		}

		if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 0 {
			t.Errorf("subscriber count = %d, want 0 after initial write failure", count)
		}
	})
}

// TestAdversarial_SSE_GoroutineAndSubscriberZeroLeakStress runs an intensive
// stress cycle with 100 mixed clients and verifies zero subscriber leaks
// and zero zombie goroutines.
func TestAdversarial_SSE_GoroutineAndSubscriberZeroLeakStress(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Zero Leak Stress Project")

	// Allow runtime goroutines to settle
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	initialGoroutines := runtime.NumGoroutine()

	const workers = 80
	var wg sync.WaitGroup
	wg.Add(workers)

	// Barrier to synchronize start
	startBarrier := make(chan struct{})

	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
			if err != nil {
				return
			}

			res, err := srv.Client().Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()

			reader := bufio.NewReader(res.Body)

			switch idx % 4 {
			case 0:
				// Disconnect immediately after reading connected
				_, _ = reader.ReadString('\n')
				cancel()
			case 1:
				// Read connected, then read 1 event, then cancel
				_, _ = reader.ReadString('\n')
				_, _ = readSSEEvent(reader, 1*time.Second)
				cancel()
			case 2:
				// Abrupt body close without cancel
				_, _ = reader.ReadString('\n')
				res.Body.Close()
			case 3:
				// Never read, cancel context after short delay
				time.Sleep(20 * time.Millisecond)
				cancel()
			}
		}(i)
	}

	// Release all workers simultaneously
	close(startBarrier)

	// Simultaneously generate event load
	for k := 0; k < 20; k++ {
		newTask(t, stk, proj.ID, fmt.Sprintf("Zero Leak Task %d", k))
		time.Sleep(5 * time.Millisecond)
	}

	wg.Wait()

	// Wait for server goroutines and subscriber map to drain
	deadline := time.Now().Add(4 * time.Second)
	cleaned := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 0 && broker.SubscriberCount() == 0 {
			cleaned = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !cleaned {
		t.Fatalf("subscriber count = %d, want 0 (leaked subscriber!)", broker.SubscriberCount())
	}

	// Verify goroutines returned to baseline
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()

	// Allow a tiny margin (<= 5) for background testing/runtime daemon goroutines
	delta := finalGoroutines - initialGoroutines
	if delta > 5 {
		t.Errorf("goroutine leak detected: initial=%d, final=%d, delta=+%d (zombie goroutines!)",
			initialGoroutines, finalGoroutines, delta)
	}
}

// TestAdversarial_SSE_MultiProjectConcurrentIsolation ensures that multiple
// simultaneous project event streams remain completely isolated under load,
// and clean up independently without cross-project subscriber corruption.
func TestAdversarial_SSE_MultiProjectConcurrentIsolation(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)

	projA := createProject(t, stk, "Project Isolation A")
	projB := createProject(t, stk, "Project Isolation B")

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	// Connect client to A
	reqA, _ := http.NewRequestWithContext(ctxA, "GET", srv.URL+"/api/v1/projects/"+projA.ID+"/events", nil)
	resA, err := srv.Client().Do(reqA)
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	defer resA.Body.Close()
	readerA := bufio.NewReader(resA.Body)
	_, _ = readerA.ReadString('\n')

	// Connect client to B
	reqB, _ := http.NewRequestWithContext(ctxB, "GET", srv.URL+"/api/v1/projects/"+projB.ID+"/events", nil)
	resB, err := srv.Client().Do(reqB)
	if err != nil {
		t.Fatalf("connect B: %v", err)
	}
	defer resB.Body.Close()
	readerB := bufio.NewReader(resB.Body)

	// Verify initial subscriber counts
	time.Sleep(30 * time.Millisecond)
	if countA := broker.SubscriberCountForProject(domain.ID(projA.ID)); countA != 1 {
		t.Fatalf("subscriber count A = %d, want 1", countA)
	}
	if countB := broker.SubscriberCountForProject(domain.ID(projB.ID)); countB != 1 {
		t.Fatalf("subscriber count B = %d, want 1", countB)
	}

	// Create task in B only
	taskB := newTask(t, stk, projB.ID, "B Only Task")

	evB, err := readSSEEvent(readerB, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent B: %v", err)
	}
	if !strings.Contains(evB.Data, taskB.ID) {
		t.Errorf("evB does not contain taskB: %v", evB.Data)
	}

	// Disconnect client A
	cancelA()
	resA.Body.Close()

	// Verify A cleans up while B remains active
	deadline := time.Now().Add(1 * time.Second)
	cleanedA := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(projA.ID)) == 0 {
			cleanedA = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleanedA {
		t.Errorf("projA subscribers did not clean up: %d", broker.SubscriberCountForProject(domain.ID(projA.ID)))
	}
	if countB := broker.SubscriberCountForProject(domain.ID(projB.ID)); countB != 1 {
		t.Errorf("projB subscribers unexpectedly changed: %d", countB)
	}

	// Disconnect client B
	cancelB()
	resB.Body.Close()

	// Verify B also cleans up to 0
	deadline = time.Now().Add(1 * time.Second)
	cleanedB := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(projB.ID)) == 0 {
			cleanedB = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleanedB {
		t.Errorf("projB subscribers did not clean up: %d", broker.SubscriberCountForProject(domain.ID(projB.ID)))
	}
}

// faultyResponseWriter simulates write/flush errors on demand
type faultyResponseWriter struct {
	mu          sync.Mutex
	headers     http.Header
	statusCode  int
	writeCount  int
	failOnCount int
	deadlines   []time.Time
}

func (f *faultyResponseWriter) Header() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.headers == nil {
		f.headers = make(http.Header)
	}
	return f.headers
}

func (f *faultyResponseWriter) WriteHeader(statusCode int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCode = statusCode
}

func (f *faultyResponseWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCount++
	if f.failOnCount > 0 && f.writeCount >= f.failOnCount {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (f *faultyResponseWriter) Flush() {}

func (f *faultyResponseWriter) FlushError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnCount > 0 && f.writeCount >= f.failOnCount {
		return errors.New("flush failed: connection closed")
	}
	return nil
}

func (f *faultyResponseWriter) SetWriteDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadlines = append(f.deadlines, t)
	return nil
}

func (f *faultyResponseWriter) Deadlines() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]time.Time, len(f.deadlines))
	copy(cp, f.deadlines)
	return cp
}

// nonFlusherResponseWriter hides the Flush method to test unsupported streaming
type nonFlusherResponseWriter struct {
	http.ResponseWriter
}

func (n *nonFlusherResponseWriter) Write(p []byte) (int, error) {
	return n.ResponseWriter.Write(p)
}

func (n *nonFlusherResponseWriter) Header() http.Header {
	return n.ResponseWriter.Header()
}

func (n *nonFlusherResponseWriter) WriteHeader(statusCode int) {
	n.ResponseWriter.WriteHeader(statusCode)
}

// TestAdversarial_SSE_WriteDeadlineTimeoutExceeded verifies that if a write deadline
// expires during Flush (simulating a slow/hung network pipe), the handler detects
// the deadline error, exits the loop immediately, and unregisters the subscriber.
func TestAdversarial_SSE_WriteDeadlineTimeoutExceeded(t *testing.T) {
	stk, broker, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Deadline Timeout Project")

	rec := &faultyDeadlineRecorder{
		ResponseWriter: httptest.NewRecorder(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/projects/"+proj.ID+"/events", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		stk.Handler.ServeHTTP(rec, req)
	}()

	// Wait for subscriber registration
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(domain.ID(proj.ID)) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 1 {
		t.Fatalf("subscriber count = %d, want 1", count)
	}

	// Arm recorder to return os.ErrDeadlineExceeded on next flush (simulating 10s write deadline expiration)
	rec.SetFlushError(errors.New("i/o timeout: write deadline exceeded"))

	// Trigger a task creation event to initiate a write
	newTask(t, stk, proj.ID, "Deadline Timeout Task")

	select {
	case <-done:
		// Clean exit
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not terminate promptly after write deadline exceeded")
	}

	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 0 {
		t.Errorf("subscriber count = %d, want 0 after write deadline timeout", count)
	}
}

// TestAdversarial_SSE_NonFlusherStreamingUnsupported verifies that if ResponseWriter
// does not implement http.Flusher, the handler rejects the request with 500
// and does not register any subscriber in the broker.
func TestAdversarial_SSE_NonFlusherStreamingUnsupported(t *testing.T) {
	stk, broker, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Non Flusher Project")

	rec := &nonFlusherResponseWriter{
		ResponseWriter: httptest.NewRecorder(),
	}

	req := httptest.NewRequest("GET", "/api/v1/projects/"+proj.ID+"/events", nil)
	stk.Handler.ServeHTTP(rec, req)

	res := rec.ResponseWriter.(*httptest.ResponseRecorder)
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status code = %d, want 500 for non-flusher", res.Code)
	}

	if !strings.Contains(res.Body.String(), "streaming unsupported") {
		t.Errorf("body = %q, want streaming unsupported error", res.Body.String())
	}

	if count := broker.SubscriberCountForProject(domain.ID(proj.ID)); count != 0 {
		t.Errorf("subscriber count = %d, want 0 when streaming unsupported", count)
	}
}

// TestAdversarial_SSE_ConcurrentPublishChurnRace fires 500 events across 10 concurrent
// publishers while 20 clients connect and disconnect at random intervals.
// Executed under -race to detect any concurrency hazards in Broker or SSE handlers.
func TestAdversarial_SSE_ConcurrentPublishChurnRace(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Concurrent Race Project")

	const numPublishers = 10
	const eventsPerPublisher = 50
	const numClients = 20

	var clientWg sync.WaitGroup
	clientWg.Add(numClients)

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	// Launch concurrent subscribers
	for c := 0; c < numClients; c++ {
		go func(idx int) {
			defer clientWg.Done()

			reqCtx, reqCancel := context.WithCancel(clientCtx)
			defer reqCancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/api/v1/projects/"+proj.ID+"/events", nil)
			if err != nil {
				return
			}

			res, err := srv.Client().Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()

			reader := bufio.NewReader(res.Body)
			// Read connected line
			_, _ = reader.ReadString('\n')

			// Stagger disconnect times
			time.Sleep(time.Duration(10+(idx*5)) * time.Millisecond)
			reqCancel()
		}(c)
	}

	// Launch concurrent publishers
	var pubWg sync.WaitGroup
	pubWg.Add(numPublishers)
	pID := domain.ID(proj.ID)

	for p := 0; p < numPublishers; p++ {
		go func(pIdx int) {
			defer pubWg.Done()
			for e := 0; e < eventsPerPublisher; e++ {
				broker.Publish(domain.Event{
					ID:        domain.ID(fmt.Sprintf("PUB-%d-EV-%d", pIdx, e)),
					ProjectID: &pID,
					Kind:      domain.EventUpdated,
					At:        time.Now().UTC(),
				})
				if e%10 == 0 {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}(p)
	}

	pubWg.Wait()
	clientWg.Wait()

	// All subscribers must eventually drain to 0
	deadline := time.Now().Add(3 * time.Second)
	cleaned := false
	for time.Now().Before(deadline) {
		if broker.SubscriberCountForProject(pID) == 0 {
			cleaned = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !cleaned {
		t.Fatalf("subscribers remaining = %d, want 0 after concurrent churn", broker.SubscriberCountForProject(pID))
	}
}

// TestAdversarial_SSE_PerWriteDeadlineDurationAndClearVerification checks the exact
// duration and clearing semantics of SetWriteDeadline on every writeChunk invocation.
func TestAdversarial_SSE_PerWriteDeadlineDurationAndClearVerification(t *testing.T) {
	stk, _, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Deadline Duration Verification Project")

	rec := &faultyDeadlineRecorder{
		ResponseWriter: httptest.NewRecorder(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, "GET", "/api/v1/projects/"+proj.ID+"/events", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		stk.Handler.ServeHTTP(rec, req)
	}()

	// Wait for connected comment write (sets deadline + clears deadline = 2 calls)
	deadline := time.Now().Add(1 * time.Second)
	var dls []time.Time
	for time.Now().Before(deadline) {
		dls = rec.Deadlines()
		if len(dls) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(dls) < 2 {
		t.Fatalf("expected at least 2 deadline calls for connected comment, got %d", len(dls))
	}

	// 1st call: time.Now() + 10s
	setDL := dls[0]
	timeUntil := time.Until(setDL)
	if timeUntil < 9*time.Second || timeUntil > 11*time.Second {
		t.Errorf("set deadline duration = %v, expected ~10s", timeUntil)
	}

	// 2nd call: cleared (time.Time{})
	clearDL := dls[1]
	if !clearDL.IsZero() {
		t.Errorf("expected clear deadline to be zero time, got %v", clearDL)
	}

	// Trigger another event and check that deadlines are again set and cleared
	newTask(t, stk, proj.ID, "Another Deadline Task")

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		dls = rec.Deadlines()
		if len(dls) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(dls) < 4 {
		t.Fatalf("expected at least 4 deadline calls after event write, got %d", len(dls))
	}

	// 3rd call: set ~10s in future
	setDL2 := dls[2]
	timeUntil2 := time.Until(setDL2)
	if timeUntil2 < 9*time.Second || timeUntil2 > 11*time.Second {
		t.Errorf("second set deadline duration = %v, expected ~10s", timeUntil2)
	}

	// 4th call: cleared
	clearDL2 := dls[3]
	if !clearDL2.IsZero() {
		t.Errorf("expected second clear deadline to be zero time, got %v", clearDL2)
	}

	cancel()
	<-done
}

type faultyDeadlineRecorder struct {
	http.ResponseWriter
	mu        sync.Mutex
	deadlines []time.Time
	flushErr  error
}

func (r *faultyDeadlineRecorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, t)
	return nil
}

func (r *faultyDeadlineRecorder) Flush() {}

func (r *faultyDeadlineRecorder) FlushError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushErr
}

func (r *faultyDeadlineRecorder) SetFlushError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushErr = err
}

func (r *faultyDeadlineRecorder) Deadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]time.Time, len(r.deadlines))
	copy(cp, r.deadlines)
	return cp
}

// TestAdversarial_SSE_StalledClientFullTCPSocket covers a stalled real-TCP
// client under an event flood: the client connects and then stops reading
// entirely while the broker is flooded. The handler must terminate promptly —
// either via its per-write deadline (if a socket write blocks) or via the
// broker's slow-subscriber drop (channel closed → !ok) — and in both cases the
// broker subscription must be released (defer cancel). The deterministic
// proof that the write deadline itself interrupts a blocked write is
// TestAdversarial_SSE_StalledClientPipeWriteDeadline.
func TestAdversarial_SSE_StalledClientFullTCPSocket(t *testing.T) {
	stk, broker, srv := newRealtimeStack(t)
	proj := createProject(t, stk, "Stalled Client Project")

	serverAddr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("dial stalled client: %v", err)
	}
	defer conn.Close()

	// Cap the client receive buffer so only a few KB of un-read data is needed
	// to fill the path and stall the connection (default socket buffers would
	// absorb far too much for the test to be quick or portable).
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetReadBuffer(2 * 1024)
	}

	reqStr := fmt.Sprintf("GET /api/v1/projects/%s/events HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", proj.ID, serverAddr)
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		t.Fatalf("write raw http request: %v", err)
	}

	pID := domain.ID(proj.ID)

	waitFor := func(within time.Duration, what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}

	waitFor(2*time.Second, "subscriber registration",
		func() bool { return broker.SubscriberCountForProject(pID) == 1 })

	// From here on the client NEVER reads from conn. Flood the broker directly;
	// fan-out is non-blocking, so once the subscriber's channel buffer fills the
	// broker drops it and closes its channel. Whichever exit path fires (write
	// deadline vs. closed-channel !ok), the subscription must be released.
	go func() {
		for i := 0; i < 200000; i++ {
			broker.Publish(domain.Event{
				ID:        domain.ID(fmt.Sprintf("STALL-%d", i)),
				ProjectID: &pID,
				Kind:      domain.EventUpdated,
				At:        time.Now().UTC(),
			})
			if broker.SubscriberCountForProject(pID) == 0 {
				return
			}
		}
	}()

	// The subscription must be released promptly. Budget is generous (15s,
	// above the 10s per-write deadline) so CI is not flaky, but an infinite
	// zombie hang is still caught; a healthy run finishes well under it.
	cleanDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(cleanDeadline) {
		if broker.SubscriberCountForProject(pID) == 0 {
			return // handler exited, subscription released — no zombie
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("stalled client wedged the handler: subscriber still registered after 15s (zombie goroutine + leaked broker subscription)")
}

// oneShotPipeListener is a net.Listener that serves exactly one connection —
// the end of a net.Pipe handed to it — then reports closed.
type oneShotPipeListener struct {
	ch chan net.Conn
}

func (l *oneShotPipeListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *oneShotPipeListener) Close() error {
	close(l.ch)
	return nil
}

func (l *oneShotPipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// TestAdversarial_SSE_StalledClientPipeWriteDeadline is the deterministic AC3
// proof that a stalled client cannot wedge the handler goroutine. streamEvents
// runs on a real net/http server whose TCP socket is replaced by a zero-buffer
// net.Pipe: every byte written must be read immediately or the write blocks.
// The client reads through ": connected" and then stops reading forever (the
// stalled client). The next event write therefore blocks INSIDE the write —
// outside the select, where r.Context().Done() can never be observed. Only the
// per-write deadline can interrupt it: ~250ms later (we inject a SHORT
// deadline through Deps.SSEWriteDeadline; production keeps its 10s default,
// pinned by TestEvents_SSE_DefaultWriteDeadline) the blocked write surfaces
// os.ErrDeadlineExceeded, the handler returns, and deferred cancel() releases
// the broker subscription. Without per-write deadlines the pipe write blocks
// forever and this test times out (zombie goroutine + leaked sub).
func TestAdversarial_SSE_StalledClientPipeWriteDeadline(t *testing.T) {
	stk, broker, _ := newRealtimeStack(t)
	proj := createProject(t, stk, "Pipe Stalled Project")

	// Inject a short per-write deadline via the Deps seam so this end-to-end
	// stalled-client path finishes in ~300ms instead of waiting out the 10s
	// production default. The assertions are otherwise unchanged: a truly
	// stalled client must still be released, exactly as in production.
	stk.App.SSEWriteDeadline = 250 * time.Millisecond

	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	lis := &oneShotPipeListener{ch: make(chan net.Conn, 1)}
	lis.ch <- serverEnd
	pipeSrv := &http.Server{Handler: stk.Handler}
	go pipeSrv.Serve(lis)
	t.Cleanup(func() { _ = pipeSrv.Close() })

	pID := domain.ID(proj.ID)

	// Stalled client: send the request, drive reads until ": connected" appears,
	// then never read again.
	if _, err := clientEnd.Write([]byte(fmt.Sprintf(
		"GET /api/v1/projects/%s/events HTTP/1.1\r\nHost: pipe\r\nAccept: text/event-stream\r\n\r\n", proj.ID))); err != nil {
		t.Fatalf("write request to pipe: %v", err)
	}
	reader := bufio.NewReader(clientEnd)
	connected := false
	for i := 0; i < 20 && !connected; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read pipe response: %v", err)
		}
		if strings.Contains(line, ": connected") {
			connected = true
		}
	}
	if !connected {
		t.Fatal("did not receive ': connected' over pipe")
	}

	waitForSub := func(within time.Duration, want int, what string) {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if broker.SubscriberCountForProject(pID) == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (count = %d)", what, broker.SubscriberCountForProject(pID))
	}
	waitForSub(2*time.Second, 1, "subscriber registration")

	// Publish one event: the handler's write blocks on the unread pipe and only
	// its ~10s per-write deadline can pull it out.
	broker.Publish(domain.Event{
		ID:        domain.ID("PIPE-STALL-1"),
		ProjectID: &pID,
		Kind:      domain.EventUpdated,
		At:        time.Now().UTC(),
	})

	// The handler must exit via the write-deadline error and release its
	// subscription. Budget is generous (5s >> injected 250ms deadline + margin)
	// so CI is not flaky, but a permanent zombie hang is still caught. Healthy
	// runs finish in ~300ms.
	cleanDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(cleanDeadline) {
		if broker.SubscriberCountForProject(pID) == 0 {
			return // handler exited via deadline error, subscription released — no zombie
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stalled pipe client wedged the handler: subscriber still registered after 5s (zombie goroutine + leaked broker subscription)")
}
