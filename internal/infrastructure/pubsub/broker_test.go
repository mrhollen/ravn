package pubsub

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestBroker_ProjectScopingAndFanOut(t *testing.T) {
	b := NewBroker()

	p1 := domain.ID("P1")
	p2 := domain.ID("P2")

	// Two subscribers for P1, one for P2, one for all
	subP1A, cancelP1A := b.Subscribe(p1)
	defer cancelP1A()

	subP1B, cancelP1B := b.Subscribe(p1)
	defer cancelP1B()

	subP2, cancelP2 := b.Subscribe(p2)
	defer cancelP2()

	subAll, cancelAll := b.Subscribe("")
	defer cancelAll()

	if total := b.SubscriberCount(); total != 4 {
		t.Fatalf("SubscriberCount = %d, want 4", total)
	}

	// Publish event for P1
	t1 := domain.ID("T1")
	evP1 := domain.Event{
		ID:        domain.ID("E1"),
		ProjectID: &p1,
		TaskID:    &t1,
		Kind:      domain.EventCreated,
		At:        time.Now().UTC(),
	}
	b.Publish(evP1)

	// subP1A and subP1B must receive evP1
	select {
	case got := <-subP1A:
		if got.ID != evP1.ID {
			t.Errorf("subP1A got event %s, want %s", got.ID, evP1.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subP1A timed out waiting for event")
	}

	select {
	case got := <-subP1B:
		if got.ID != evP1.ID {
			t.Errorf("subP1B got event %s, want %s", got.ID, evP1.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subP1B timed out waiting for event")
	}

	// subAll must also receive evP1
	select {
	case got := <-subAll:
		if got.ID != evP1.ID {
			t.Errorf("subAll got event %s, want %s", got.ID, evP1.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subAll timed out waiting for event")
	}

	// subP2 must NOT receive evP1
	select {
	case got := <-subP2:
		t.Fatalf("subP2 received unexpected event %s for project P1", got.ID)
	case <-time.After(50 * time.Millisecond):
		// Expected: no event received
	}

	// Publish event for P2
	t2 := domain.ID("T2")
	evP2 := domain.Event{
		ID:        domain.ID("E2"),
		ProjectID: &p2,
		TaskID:    &t2,
		Kind:      domain.EventUpdated,
		At:        time.Now().UTC(),
	}
	b.Publish(evP2)

	select {
	case got := <-subP2:
		if got.ID != evP2.ID {
			t.Errorf("subP2 got event %s, want %s", got.ID, evP2.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subP2 timed out waiting for event")
	}

	// subP1A must NOT receive evP2
	select {
	case got := <-subP1A:
		t.Fatalf("subP1A received unexpected event %s for project P2", got.ID)
	case <-time.After(50 * time.Millisecond):
		// Expected: no event received
	}
}

func TestBroker_NonBlockingSlowClientDropped(t *testing.T) {
	// Small buffer of size 2 and a tight eviction tolerance: this test exercises
	// the EVICTION PATH (sustained saturation), so it opts into short grace
	// instead of the production defaults (16 consecutive full publishes / 1s).
	b := NewBroker(WithBufferSize(2), WithSlowClientTolerance(2))
	p1 := domain.ID("P1")

	// Slow client: never reads from channel
	slowSub, cancelSlow := b.Subscribe(p1)
	defer cancelSlow()

	// Fast client: actively reads from channel
	fastSub, cancelFast := b.Subscribe(p1)
	defer cancelFast()

	receivedFast := 0
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for range fastSub {
			receivedFast++
			if receivedFast == 5 {
				return
			}
		}
	}()

	// Send 5 events with small intervals to allow fast consumer to drain
	for i := 1; i <= 5; i++ {
		ev := domain.Event{
			ID:        domain.ID(string(rune('0' + i))),
			ProjectID: &p1,
			Kind:      domain.EventStatusChanged,
			At:        time.Now().UTC(),
		}
		// Must NEVER block regardless of slow client
		b.Publish(ev)
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for fast client to receive events
	select {
	case <-fastDone:
		if receivedFast != 5 {
			t.Fatalf("fastSub received %d events, want 5", receivedFast)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("fastSub received %d events, want 5 (timed out)", receivedFast)
	}

	// Slow client should be dropped after buffer filled
	// Drain slowSub buffer to verify it gets closed
	closed := false
	for i := 0; i < 5; i++ {
		select {
		case _, ok := <-slowSub:
			if !ok {
				closed = true
				break
			}
		case <-time.After(100 * time.Millisecond):
			break
		}
		if closed {
			break
		}
	}
	if !closed {
		t.Error("expected slow subscriber channel to be closed after dropping")
	}

	// Active subscribers count should now be 1 (only fastSub remains)
	if total := b.SubscriberCount(); total != 1 {
		t.Errorf("SubscriberCount = %d, want 1 (slow client should be unlinked)", total)
	}
}

func TestBroker_CancelUnregistersAndClosesPromptly(t *testing.T) {
	b := NewBroker()
	p := domain.ID("P1")

	ch, cancel := b.Subscribe(p)
	if b.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", b.SubscriberCount())
	}

	cancel()

	// Second cancel must be safe/idempotent
	cancel()

	if b.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount = %d, want 0 after cancel", b.SubscriberCount())
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed, but received value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestBroker_ConcurrentPublishAndSubscribe(t *testing.T) {
	b := NewBroker(WithBufferSize(16))
	p := domain.ID("P_CONCURRENT")

	const numSubscribers = 20
	const numEvents = 50

	var wg sync.WaitGroup
	wg.Add(numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe(p)
			defer cancel()

			count := 0
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
					count++
					if count >= numEvents {
						return
					}
				case <-time.After(500 * time.Millisecond):
					return
				}
			}
		}()
	}

	// Concurrently publish events
	for i := 0; i < numEvents; i++ {
		b.Publish(domain.Event{
			ID:        domain.ID("EV"),
			ProjectID: &p,
			Kind:      domain.EventUpdated,
			At:        time.Now().UTC(),
		})
	}

	wg.Wait()
}

func TestBroker_CloseClosesAllSubscribersAndPreventsNew(t *testing.T) {
	b := NewBroker()
	p1 := domain.ID("P1")
	p2 := domain.ID("P2")

	sub1, _ := b.Subscribe(p1)
	sub2, _ := b.Subscribe(p2)
	sub3, _ := b.Subscribe("")

	if count := b.SubscriberCount(); count != 3 {
		t.Fatalf("SubscriberCount = %d, want 3", count)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	if !b.IsClosed() {
		t.Error("expected IsClosed to be true")
	}
	if count := b.SubscriberCount(); count != 0 {
		t.Errorf("SubscriberCount after close = %d, want 0", count)
	}

	// All subscriber channels must be closed
	for i, ch := range []<-chan domain.Event{sub1, sub2, sub3} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("sub%d received value instead of channel close", i+1)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("sub%d channel was not closed after broker Close()", i+1)
		}
	}

	// Subscribing after close should return an already-closed channel
	subPostClose, cancelPostClose := b.Subscribe(p1)
	defer cancelPostClose()
	select {
	case _, ok := <-subPostClose:
		if ok {
			t.Error("expected channel from Subscribe after Close to be closed, but received value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel from Subscribe after Close was not closed")
	}

	// Idempotent close
	if err := b.Close(); err != nil {
		t.Errorf("second Close error: %v", err)
	}
}

func TestBroker_GracefulShutdownUnblocksActiveReaders(t *testing.T) {
	b := NewBroker()
	p := domain.ID("P_SHUTDOWN")

	const numReaders = 10
	var startedWg sync.WaitGroup
	var doneWg sync.WaitGroup

	startedWg.Add(numReaders)
	doneWg.Add(numReaders)

	readCounts := make([]int, numReaders)
	for i := 0; i < numReaders; i++ {
		idx := i
		ch, _ := b.Subscribe(p)
		go func() {
			defer doneWg.Done()
			startedWg.Done()
			for range ch {
				readCounts[idx]++
			}
		}()
	}

	// Wait for all reader goroutines to start listening
	startedWg.Wait()

	if count := b.SubscriberCount(); count != numReaders {
		t.Fatalf("SubscriberCount = %d, want %d", count, numReaders)
	}

	// Publish one event to ensure readers are active
	b.Publish(domain.Event{
		ID:        domain.ID("EV1"),
		ProjectID: &p,
		Kind:      domain.EventCreated,
		At:        time.Now().UTC(),
	})

	// Wait briefly for delivery
	time.Sleep(20 * time.Millisecond)

	// Close broker - must unblock all readers immediately
	closeStart := time.Now()
	if err := b.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		doneWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if elapsed := time.Since(closeStart); elapsed > 500*time.Millisecond {
			t.Errorf("Shutdown took too long to unblock readers: %v", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for active readers to be unblocked by Close()")
	}

	if !b.IsClosed() {
		t.Error("expected IsClosed to be true")
	}
	if count := b.SubscriberCount(); count != 0 {
		t.Errorf("SubscriberCount after close = %d, want 0", count)
	}
}

func TestBroker_ConcurrentCloseWithPublishAndSubscribe(t *testing.T) {
	b := NewBroker(WithBufferSize(8))
	p := domain.ID("P_CONCURRENT_CLOSE")

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 5 Publishers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; ; j++ {
				select {
				case <-ctx.Done():
					return
				default:
					b.Publish(domain.Event{
						ID:        domain.ID(fmt.Sprintf("EV-%d-%d", id, j)),
						ProjectID: &p,
						Kind:      domain.EventUpdated,
						At:        time.Now().UTC(),
					})
					time.Sleep(1 * time.Millisecond)
				}
			}
		}(i)
	}

	// 5 Subscribers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					ch, cancelSub := b.Subscribe(p)
					timer := time.NewTimer(5 * time.Millisecond)
					select {
					case <-ch:
					case <-timer.C:
					}
					timer.Stop()
					cancelSub()
				}
			}
		}()
	}

	// Let them run for 30ms
	time.Sleep(30 * time.Millisecond)

	// Concurrently call Close from multiple goroutines
	var closeWg sync.WaitGroup
	for i := 0; i < 4; i++ {
		closeWg.Add(1)
		go func() {
			defer closeWg.Done()
			if err := b.Close(); err != nil {
				t.Errorf("Close returned error: %v", err)
			}
		}()
	}
	closeWg.Wait()

	// Stop workers
	cancel()
	wg.Wait()

	if !b.IsClosed() {
		t.Error("expected broker to be closed")
	}
	if count := b.SubscriberCount(); count != 0 {
		t.Errorf("SubscriberCount after close = %d, want 0", count)
	}
}

func TestBroker_SlowSubscriberDropLoggingAndMetrics(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Tight tolerance: this test pins the EVICTION path (warning + metrics), not
	// the production default of 16 consecutive full publishes before eviction.
	b := NewBroker(WithBufferSize(2), WithSlowClientTolerance(2), WithLogger(logger))
	p1 := domain.ID("P_SLOW_LOG")

	// Slow client: never reads
	slowSub, cancelSlow := b.Subscribe(p1)
	defer cancelSlow()

	// Fast client: actively reads
	fastSub, cancelFast := b.Subscribe(p1)
	defer cancelFast()

	receivedFast := 0
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for range fastSub {
			receivedFast++
			if receivedFast == 5 {
				return
			}
		}
	}()

	taskID := domain.ID("T_TASK_1")
	for i := 1; i <= 5; i++ {
		ev := domain.Event{
			ID:        domain.ID(fmt.Sprintf("EV-%d", i)),
			ProjectID: &p1,
			TaskID:    &taskID,
			Kind:      domain.EventStatusChanged,
			At:        time.Now().UTC(),
		}
		b.Publish(ev)
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-fastDone:
		if receivedFast != 5 {
			t.Fatalf("fastSub received %d events, want 5", receivedFast)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatalf("fastSub timed out; received %d events", receivedFast)
	}

	// Verify slowSub channel is closed
	select {
	case _, ok := <-slowSub:
		if ok {
			for range slowSub {
			}
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for slow subscriber channel to close")
	}

	// Verify metric
	if drops := b.DroppedCount(); drops != 1 {
		t.Errorf("DroppedCount = %d, want 1", drops)
	}
	if drops := b.DroppedSubscribers(); drops != 1 {
		t.Errorf("DroppedSubscribers = %d, want 1", drops)
	}

	// Verify structured warning log
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("expected level=WARN in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "pubsub: slow subscriber buffer saturation sustained, evicting subscriber") {
		t.Errorf("expected eviction message in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "buffer_capacity=2") {
		t.Errorf("expected buffer_capacity=2 in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "subscriber_project=P_SLOW_LOG") {
		t.Errorf("expected subscriber_project in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "task_id=T_TASK_1") {
		t.Errorf("expected task_id in log output, got: %s", logOutput)
	}
	// AC-3 detail: the warning must also carry saturation + event-drop counters.
	if !strings.Contains(logOutput, "consecutive_full=") {
		t.Errorf("expected consecutive_full in eviction warning, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "events_dropped=") {
		t.Errorf("expected events_dropped in eviction warning, got: %s", logOutput)
	}
}

func TestBroker_SlowSubscriberDropDefaultLogger(t *testing.T) {
	var logBuf strings.Builder
	testLogger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	origDefault := slog.Default()
	slog.SetDefault(testLogger)
	defer slog.SetDefault(origDefault)

	// Broker constructed without WithLogger uses slog.Default(). Tight tolerance:
	// pins the eviction path (see TestBroker_NonBlockingSlowClientDropped).
	b := NewBroker(WithBufferSize(2), WithSlowClientTolerance(2))
	p := domain.ID("P_DEFAULT_LOG")

	slowSub, cancel := b.Subscribe(p)
	defer cancel()

	for i := 1; i <= 5; i++ {
		b.Publish(domain.Event{
			ID:        domain.ID(fmt.Sprintf("EV-%d", i)),
			ProjectID: &p,
			Kind:      domain.EventCreated,
			At:        time.Now().UTC(),
		})
	}

	// Verify channel closed
	select {
	case _, ok := <-slowSub:
		if ok {
			for range slowSub {
			}
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow subscriber channel not closed")
	}

	if drops := b.DroppedCount(); drops != 1 {
		t.Errorf("DroppedCount = %d, want 1", drops)
	}

	if !strings.Contains(logBuf.String(), "pubsub: slow subscriber buffer saturation sustained, evicting subscriber") {
		t.Errorf("expected default logger to record eviction warning, got: %s", logBuf.String())
	}
}

// --- Graduated slow-client handling (bursts must not evict abruptly) -----------

// soleSubscriber returns the broker's only active subscriber (whitebox test helper).
func soleSubscriber(b *Broker) *subscriber {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for sub := range b.subscribers {
		return sub
	}
	return nil
}

// drainNonBlocking reads every event currently pending on ch without ever blocking.
func drainNonBlocking(ch <-chan domain.Event) []domain.Event {
	var out []domain.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestBroker_GraduatedBurstHandling is the AC-2 core proof: a burst that fills a
// subscriber's buffer does NOT close+delete it on the first full tick. The
// subscriber is retained (with per-event drops) until saturation is sustained —
// either the fail streak reaches the tolerance (count dimension) — at which point
// it is evicted with a warning — and only then. No sleeps: the time dimension is
// disabled, so every outcome is decided by event counts alone.
func TestBroker_GraduatedBurstHandling(t *testing.T) {
	p := domain.ID("P_GRAD")
	cases := []struct {
		name          string
		bufferSize    int
		tolerance     int
		burst         int // events published to a never-reading subscriber
		wantEvicted   bool
		wantDroppedEv uint64
	}{
		{name: "first_full_tick_retains_subscriber", bufferSize: 4, tolerance: 16, burst: 5, wantDroppedEv: 1},
		{name: "burst_just_within_tolerance_retains_subscriber", bufferSize: 4, tolerance: 16, burst: 19, wantDroppedEv: 15},
		{name: "burst_reaching_tolerance_evicts_subscriber", bufferSize: 4, tolerance: 16, burst: 20, wantEvicted: true, wantDroppedEv: 16},
		{name: "tolerance_1_restores_legacy_immediate_eviction", bufferSize: 2, tolerance: 1, burst: 3, wantEvicted: true, wantDroppedEv: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf strings.Builder
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			b := NewBroker(WithBufferSize(tc.bufferSize), WithSlowClientTolerance(tc.tolerance),
				WithSlowClientGrace(0), WithLogger(logger))

			sub, cancel := b.Subscribe(p)
			defer cancel()

			for i := 1; i <= tc.burst; i++ {
				b.Publish(domain.Event{
					ID:        domain.ID(fmt.Sprintf("EV-%d", i)),
					ProjectID: &p,
					Kind:      domain.EventUpdated,
					At:        time.Now().UTC(),
				})
			}

			if got := b.DroppedEventCount(); got != tc.wantDroppedEv {
				t.Errorf("DroppedEventCount = %d, want %d", got, tc.wantDroppedEv)
			}
			if evicted := b.DroppedCount() == 1; evicted != tc.wantEvicted {
				t.Fatalf("evicted = %v, want %v", evicted, tc.wantEvicted)
			}
			wantSubs := 1
			if tc.wantEvicted {
				wantSubs = 0
			}
			if got := b.SubscriberCount(); got != wantSubs {
				t.Errorf("SubscriberCount = %d, want %d", got, wantSubs)
			}

			pending := drainNonBlocking(sub)
			if tc.wantEvicted {
				// Channel must be closed; only what fit the buffer was delivered.
				select {
				case _, ok := <-sub:
					if ok {
						t.Errorf("expected closed channel, got an event after draining %d", len(pending))
					}
				case <-time.After(20 * time.Millisecond):
					t.Error("expected closed channel, but reads still block")
				}
				if got := len(pending); got != tc.bufferSize {
					t.Errorf("delivered before eviction = %d, want %d (full buffer)", got, tc.bufferSize)
				}
				// AC-3: the warning names the subscriber AND carries event + drop details.
				logOut := logBuf.String()
				if !strings.Contains(logOut, "pubsub: slow subscriber buffer saturation sustained, evicting subscriber") {
					t.Errorf("expected eviction warning, got: %q", logOut)
				}
				for _, want := range []string{"subscriber_id=1", "event_id=EV-", "events_dropped=", "consecutive_full="} {
					if !strings.Contains(logOut, want) {
						t.Errorf("eviction warning missing %q: %q", want, logOut)
					}
				}
			} else {
				// Retained: exactly the buffered events are pending, nothing dropped-and-lost
				// beyond that, and the channel is provably OPEN (a new publish delivers).
				if got := len(pending); got != tc.bufferSize {
					t.Errorf("pending = %d, want %d (full buffer, nothing evicted)", got, tc.bufferSize)
				}
				b.Publish(domain.Event{ID: domain.ID("EV-PROBE"), ProjectID: &p, Kind: domain.EventUpdated, At: time.Now().UTC()})
				if len(drainNonBlocking(sub)) != 1 {
					t.Error("probe event not delivered — channel appears closed on a retained subscriber")
				}
				if logOut := logBuf.String(); logOut != "" {
					t.Errorf("no warning expected while a subscriber is merely saturated, got: %q", logOut)
				}
			}
		})
	}
}

// TestBroker_SustainedSaturationEvictsAfterGraceWindow proves the TIME dimension
// of graduated handling: with the count tolerance effectively unreachable, a
// subscriber whose buffer has been continuously full for longer than slowGrace is
// evicted — even though its fail streak is tiny. The grace window is simulated by
// rewinding the whitebox overdueSince clock, so the test needs no real sleep.
func TestBroker_SustainedSaturationEvictsAfterGraceWindow(t *testing.T) {
	const grace = 500 * time.Millisecond // explicit option (also exercises the positive WithSlowClientGrace branch)
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	b := NewBroker(WithBufferSize(2), WithSlowClientTolerance(1_000_000), WithSlowClientGrace(grace), WithLogger(logger))
	p := domain.ID("P_GRACE")

	sub, cancel := b.Subscribe(p)
	defer cancel()
	mk := func(n int) domain.Event {
		return domain.Event{ID: domain.ID(fmt.Sprintf("G-%d", n)), ProjectID: &p, Kind: domain.EventUpdated, At: time.Now().UTC()}
	}

	b.Publish(mk(1)) // buffered
	b.Publish(mk(2)) // buffer full
	b.Publish(mk(3)) // fail #1 (streak 1 << tolerance); overdueSince starts now

	state := soleSubscriber(b)
	if state == nil {
		t.Fatal("expected one active subscriber")
	}
	// Rewind the saturation start past the configured grace window: wall time has
	// passed with the buffer continuously full, streak still far below tolerance.
	state.overdueSince.Store(time.Now().Add(-2 * grace).UnixNano())

	b.Publish(mk(4)) // still full; elapsed >= slowGrace => evicted via TIME dimension

	if got := b.DroppedCount(); got != 1 {
		t.Fatalf("DroppedCount = %d, want 1 (time-dimension eviction)", got)
	}
	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0", got)
	}
	drainNonBlocking(sub) // consume the 2 buffered events before probing for close
	select {
	case _, ok := <-sub:
		if ok {
			t.Error("expected closed channel after time-dimension eviction")
		}
	case <-time.After(20 * time.Millisecond):
		t.Error("channel not closed after time-dimension eviction")
	}
	logOut := logBuf.String()
	if !strings.Contains(logOut, "evicting subscriber") {
		t.Errorf("expected eviction warning, got: %q", logOut)
	}
	if !strings.Contains(logOut, "consecutive_full=2") {
		t.Errorf("warning should show the low streak (time, not count, tripped it); got: %q", logOut)
	}
}

// TestBroker_SlowClientRecoversWhenItDrains proves the graduated design is
// RECOVERABLE: a subscriber that falls behind during a burst but then drains
// again has its fail streak reset — it survives what would have been eviction
// under a non-resetting counter (tolerance here is deliberately 2). No sleeps.
func TestBroker_SlowClientRecoversWhenItDrains(t *testing.T) {
	b := NewBroker(WithBufferSize(2), WithSlowClientTolerance(2), WithSlowClientGrace(0))
	p := domain.ID("P_RECOVER")

	sub, cancel := b.Subscribe(p)
	defer cancel()
	mk := func(n string) domain.Event {
		return domain.Event{ID: domain.ID("EV-" + n), ProjectID: &p, Kind: domain.EventUpdated, At: time.Now().UTC()}
	}
	readOne := func(want string) bool {
		select {
		case ev := <-sub:
			if string(ev.ID) != want {
				return false
			}
			return true
		default:
			return false
		}
	}

	b.Publish(mk("1")) // buffered
	b.Publish(mk("2")) // buffer full
	b.Publish(mk("3")) // full => fail #1 (dropped; recoverable via Last-Event-ID replay)
	if !readOne("EV-1") || !readOne("EV-2") {
		t.Fatal("subscriber failed to drain after the hiccup")
	}
	b.Publish(mk("4")) // delivered => streak resets to 0
	b.Publish(mk("5")) // buffered (room available)
	b.Publish(mk("6")) // full => fail #1 of a FRESH streak; without the reset above this would be #2 => eviction
	if !readOne("EV-4") || !readOne("EV-5") {
		t.Fatal("drain failed after recovery")
	}

	if got := b.DroppedCount(); got != 0 {
		t.Errorf("DroppedCount = %d, want 0 (recovered subscriber must not be evicted)", got)
	}
	if got := b.SubscriberCount(); got != 1 {
		t.Errorf("SubscriberCount = %d, want 1", got)
	}
	if got := b.DroppedEventCount(); got != 2 {
		t.Errorf("DroppedEventCount = %d, want 2 (EV-3 and EV-6 were the only drops)", got)
	}
	// Probe that the channel is still open and delivering.
	b.Publish(domain.Event{ID: domain.ID("EV-PROBE"), ProjectID: &p, Kind: domain.EventUpdated, At: time.Now().UTC()})
	if !readOne("EV-PROBE") {
		t.Error("probe event not delivered — subscriber no longer functional")
	}
}

// TestBroker_ConcurrentPublishGraduatedHandling is a -race smoke test for the
// graduated path: many concurrent publishers hammer one starved (never-reading)
// subscriber. It must be evicted exactly once — never double-evicted, never
// re-sent to after close — and metrics must stay consistent under concurrent
// atomic counter churn (every publisher may detect saturation on the same tick).
func TestBroker_ConcurrentPublishGraduatedHandling(t *testing.T) {
	b := NewBroker(WithBufferSize(8)) // production-style defaults for tolerance/grace
	p := domain.ID("P_CHURN")

	slow, cancelSlow := b.Subscribe(p)
	defer cancelSlow()

	const workers = 8
	const perWorker = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				b.Publish(domain.Event{
					ID:        domain.ID(fmt.Sprintf("W%d-E%d", w, i)),
					ProjectID: &p,
					Kind:      domain.EventUpdated,
					At:        time.Now().UTC(),
				})
			}
		}(w)
	}
	wg.Wait()

	if got := b.DroppedCount(); got != 1 {
		t.Errorf("DroppedCount = %d, want 1 (starved subscriber evicted exactly once)", got)
	}
	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("SubscriberCount = %d, want 0 (the only subscriber was evicted)", got)
	}
	if got := b.DroppedEventCount(); got < 16 {
		t.Errorf("DroppedEventCount = %d, want >= 16 (buffer 8 + tolerance 16 before eviction)", got)
	}
	drainNonBlocking(slow) // consume the 8 buffered events before probing for close
	select {
	case _, ok := <-slow:
		if ok {
			t.Error("starved subscriber's channel should be closed")
		}
	case <-time.After(20 * time.Millisecond):
		t.Error("starved subscriber's channel not closed")
	}
}
