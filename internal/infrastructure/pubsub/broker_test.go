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
	// Small buffer of size 2
	b := NewBroker(WithBufferSize(2))
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

	b := NewBroker(WithBufferSize(2), WithLogger(logger))
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
	if !strings.Contains(logOutput, "pubsub: slow subscriber buffer overflow, dropping subscriber") {
		t.Errorf("expected drop message in log output, got: %s", logOutput)
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
}

func TestBroker_SlowSubscriberDropDefaultLogger(t *testing.T) {
	var logBuf strings.Builder
	testLogger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	origDefault := slog.Default()
	slog.SetDefault(testLogger)
	defer slog.SetDefault(origDefault)

	// Broker constructed without WithLogger uses slog.Default()
	b := NewBroker(WithBufferSize(2))
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

	if !strings.Contains(logBuf.String(), "pubsub: slow subscriber buffer overflow, dropping subscriber") {
		t.Errorf("expected default logger to record drop warning, got: %s", logBuf.String())
	}
}
