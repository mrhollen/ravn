package pubsub

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// DefaultClientBufferSize is the default capacity of each subscriber's channel buffer.
// If a client's buffer fills up (e.g. client is stalled or slow), the broker applies
// graduated slow-client handling (see DefaultSlowClientTolerance / DefaultSlowClientGrace)
// so that bursts of writes never block mutating handlers and are never punished with
// an abrupt connection drop on the first full-buffer tick.
const DefaultClientBufferSize = 64

// DefaultSlowClientTolerance is how many CONSECUTIVE Publish calls may find a
// subscriber's buffer full before it is considered sustained-slow and evicted.
// Evicting past this point (rather than on the very first overflow) means a burst
// of writes that momentarily outruns an otherwise-healthy reader no longer tears
// down its SSE connection; events dropped while the buffer is saturated are
// recoverable by the client via Last-Event-ID replay from the event store.
const DefaultSlowClientTolerance = 16

// DefaultSlowClientGrace bounds, in wall time, how long a subscriber may remain
// continuously saturated (full buffer on every Publish) before eviction, so a
// slowly-trickling publish stream still evicts a genuinely stalled client
// promptly instead of waiting for the tolerance counter to accumulate.
const DefaultSlowClientGrace = time.Second

type subscriber struct {
	id        uint64
	ch        chan domain.Event
	projectID domain.ID // empty string matches all projects
	closed    bool

	// Graduated slow-client state. These counters are mutated by Publish while
	// holding only a shared RLock (multiple publishers may fan out concurrently),
	// so every field is atomic. They approximate "is this subscriber falling
	// behind" — reset to zero as soon as any send succeeds.
	failStreak   atomic.Uint64 // consecutive full-buffer publishes (0 = healthy)
	overdueSince atomic.Int64  // UnixNano of the first full-buffer publish in the current streak (0 = none)
	dropped      atomic.Uint64 // events dropped for this subscriber while it was saturated
}

// Broker implements application.EventBroker using an in-memory pub-sub model.
type Broker struct {
	mu                 sync.RWMutex
	subscribers        map[*subscriber]struct{}
	bufferSize         int
	slowTolerance      uint64        // consecutive full-buffer publishes tolerated before eviction (>= 1)
	slowGrace          time.Duration // wall-time saturation window before eviction (<= 0 disables the time dimension)
	closed             bool
	nextSubID          atomic.Uint64
	droppedSubscribers atomic.Uint64
	droppedEvents      atomic.Uint64
	logger             *slog.Logger
}

// Option configures a Broker instance.
type Option func(*Broker)

// WithBufferSize overrides the per-subscriber channel buffer size.
func WithBufferSize(size int) Option {
	return func(b *Broker) {
		if size > 0 {
			b.bufferSize = size
		}
	}
}

// WithLogger configures a custom logger for the Broker.
func WithLogger(logger *slog.Logger) Option {
	return func(b *Broker) {
		if logger != nil {
			b.logger = logger
		}
	}
}

// WithSlowClientTolerance overrides how many consecutive full-buffer Publish
// calls a subscriber tolerates before eviction. Smaller values evict sooner;
// 1 restores the legacy "evict on first overflow" behavior. Values below 1 are
// ignored (the tolerance is always at least 1 — eviction never disappears).
func WithSlowClientTolerance(n int) Option {
	return func(b *Broker) {
		if n >= 1 {
			b.slowTolerance = uint64(n)
		}
	}
}

// WithSlowClientGrace overrides the wall-time saturation window before
// eviction. d <= 0 disables the time dimension entirely (eviction then depends
// on the tolerance counter only).
func WithSlowClientGrace(d time.Duration) Option {
	return func(b *Broker) {
		if d <= 0 {
			b.slowGrace = 0 // disabled
			return
		}
		b.slowGrace = d
	}
}

// NewBroker constructs a thread-safe in-memory EventBroker.
func NewBroker(opts ...Option) *Broker {
	b := &Broker{
		subscribers:   make(map[*subscriber]struct{}),
		bufferSize:    DefaultClientBufferSize,
		slowTolerance: DefaultSlowClientTolerance,
		slowGrace:     DefaultSlowClientGrace,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

var _ application.EventBroker = (*Broker)(nil)

func (b *Broker) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// DroppedCount returns the cumulative count of slow subscribers evicted due to sustained buffer saturation.
func (b *Broker) DroppedCount() uint64 {
	return b.droppedSubscribers.Load()
}

// DroppedSubscribers is an alias for DroppedCount.
func (b *Broker) DroppedSubscribers() uint64 {
	return b.droppedSubscribers.Load()
}

// DroppedEventCount returns the cumulative count of events that were dropped to
// saturated-but-retained subscribers (and to evicted ones, on their final tick).
// These frames are recoverable by affected clients via Last-Event-ID replay.
func (b *Broker) DroppedEventCount() uint64 {
	return b.droppedEvents.Load()
}

// Subscribe registers a listener for events belonging to projectID.
// An empty projectID subscribes to events across all projects.
// If the broker is closed, it returns an already-closed channel and a no-op cancel function.
// The returned cancel function unregisters the subscriber and closes its channel.
func (b *Broker) Subscribe(projectID domain.ID) (<-chan domain.Event, func()) {
	sub := &subscriber{
		id:        b.nextSubID.Add(1),
		ch:        make(chan domain.Event, b.bufferSize),
		projectID: projectID,
	}

	b.mu.Lock()
	if b.closed {
		sub.closed = true
		close(sub.ch)
		b.mu.Unlock()
		return sub.ch, func() {}
	}
	b.subscribers[sub] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, sub)
			if !sub.closed {
				sub.closed = true
				close(sub.ch)
			}
			b.mu.Unlock()
		})
	}

	return sub.ch, cancel
}

// evictInfo captures, at detection time, everything needed to evict a
// sustained-slow subscriber: the event that triggered eviction plus the
// saturation counters for structured logging.
type evictInfo struct {
	sub           *subscriber
	trigger       domain.Event
	streak        uint64 // consecutive full-buffer publishes at eviction
	eventsDropped uint64 // events dropped to this subscriber across its saturated streak
}

// Publish delivers an event to all matching subscribers.
// Fan-out is strictly non-blocking so that a burst of writes never blocks the
// caller: when a subscriber's channel buffer is full the broker applies
// GRADUATED slow-client handling instead of evicting on the first overflow.
//
//  1. Any single full-buffer tick drops ONLY that event for that subscriber —
//     the subscriber is retained, its fail streak starts (or grows), and the
//     drop is counted. Dropped frames are recoverable by an affected client via
//     Last-Event-ID replay from the durable event store on reconnect.
//  2. A successful send resets the streak: a reader that keeps up (even if it
//     briefly falls behind during a burst) is never evicted.
//  3. The subscriber is evicted only once saturation is SUSTAINED — either its
//     fail streak reaches slowTolerance (count dimension; covers fast bursts)
//     or the buffer has been continuously full for at least slowGrace of wall
//     time (time dimension; covers slow-trickle streams). Eviction closes the
//     channel (the SSE client reconnects and resyncs via replay), emits a
//     structured WARNING naming the subscriber and event, and increments both
//     drop metrics.
func (b *Broker) Publish(e domain.Event) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	nowNano := time.Now().UnixNano()
	var graceNano int64 // wall-time dimension; 0 disables it (int64 math avoids uint wrap if the clock moves backwards)
	if b.slowGrace > 0 {
		graceNano = int64(b.slowGrace)
	}
	var toEvict []evictInfo
	for sub := range b.subscribers {
		// Filter by project ID when the subscriber is scoped to a specific project
		if sub.projectID != "" {
			if e.ProjectID == nil || *e.ProjectID != sub.projectID {
				continue
			}
		}

		select {
		case sub.ch <- e:
			// Send succeeded: the subscriber is keeping up again — reset its
			// saturation streak so a burst-induced hiccup never counts as sustained.
			sub.failStreak.Store(0)
			sub.overdueSince.Store(0)
		default:
			// Client channel buffer full: drop this one event for this
			// subscriber only (retained — recoverable via replay) and track saturation.
			streak := sub.failStreak.Add(1)
			// Record when this saturated streak began (wins only on the first failure).
			sub.overdueSince.CompareAndSwap(0, nowNano)
			sub.dropped.Add(1)
			b.droppedEvents.Add(1)

			evictNow := streak >= b.slowTolerance
			if !evictNow && graceNano > 0 {
				if since := sub.overdueSince.Load(); since != 0 && nowNano-since >= graceNano {
					evictNow = true // continuously saturated beyond the wall-time grace window
				}
			}
			if evictNow {
				toEvict = append(toEvict, evictInfo{sub: sub, trigger: e, streak: streak, eventsDropped: sub.dropped.Load()})
			}
		}
	}
	b.mu.RUnlock()

	// Evict sustained-slow subscribers, if any were detected. Runs under the
	// exclusive lock so a concurrent Publish (shared RLock) can never send on a
	// channel while it is being closed here.
	if len(toEvict) > 0 {
		var evicted []evictInfo
		b.mu.Lock()
		for _, info := range toEvict {
			if _, exists := b.subscribers[info.sub]; exists {
				delete(b.subscribers, info.sub)
				if !info.sub.closed {
					info.sub.closed = true
					close(info.sub.ch)
				}
				evicted = append(evicted, info)
			}
		}
		b.mu.Unlock()

		if len(evicted) > 0 {
			b.droppedSubscribers.Add(uint64(len(evicted)))
			for _, info := range evicted {
				sub, e := info.sub, info.trigger
				subProj := "*"
				if sub.projectID != "" {
					subProj = sub.projectID.String()
				}
				var evProj string
				if e.ProjectID != nil {
					evProj = e.ProjectID.String()
				}
				var taskID string
				if e.TaskID != nil {
					taskID = e.TaskID.String()
				}
				b.log().Warn("pubsub: slow subscriber buffer saturation sustained, evicting subscriber",
					"subscriber_id", sub.id,
					"subscriber_project", subProj,
					"buffer_capacity", cap(sub.ch),
					"consecutive_full", info.streak,
					"events_dropped", info.eventsDropped,
					"slow_tolerance", b.slowTolerance,
					"event_id", e.ID.String(),
					"event_kind", string(e.Kind),
					"event_project", evProj,
					"task_id", taskID,
				)
			}
		}
	}
}

// Close shuts down the broker and closes all active subscriber channels,
// unblocking any listening SSE goroutines immediately.
// Subsequent Subscribe calls return an already-closed channel.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for sub := range b.subscribers {
		if !sub.closed {
			sub.closed = true
			close(sub.ch)
		}
	}
	b.subscribers = make(map[*subscriber]struct{})
	return nil
}

// IsClosed reports whether the broker has been closed.
func (b *Broker) IsClosed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.closed
}

// SubscriberCount returns the current count of active subscribers.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}
	return len(b.subscribers)
}

// SubscriberCountForProject returns active subscribers scoped to projectID.
func (b *Broker) SubscriberCountForProject(projectID domain.ID) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}
	count := 0
	for sub := range b.subscribers {
		if sub.projectID == projectID {
			count++
		}
	}
	return count
}
