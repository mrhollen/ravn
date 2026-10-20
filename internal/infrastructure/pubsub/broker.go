package pubsub

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// DefaultClientBufferSize is the default capacity of each subscriber's channel buffer.
// If a client's buffer fills up (e.g. client is stalled or slow), the broker drops
// the client immediately so that bursts of writes never block mutating handlers.
const DefaultClientBufferSize = 64

type subscriber struct {
	id        uint64
	ch        chan domain.Event
	projectID domain.ID // empty string matches all projects
	closed    bool
}

// Broker implements application.EventBroker using an in-memory pub-sub model.
type Broker struct {
	mu                 sync.RWMutex
	subscribers        map[*subscriber]struct{}
	bufferSize         int
	closed             bool
	nextSubID          atomic.Uint64
	droppedSubscribers atomic.Uint64
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

// NewBroker constructs a thread-safe in-memory EventBroker.
func NewBroker(opts ...Option) *Broker {
	b := &Broker{
		subscribers: make(map[*subscriber]struct{}),
		bufferSize:  DefaultClientBufferSize,
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

// DroppedCount returns the cumulative count of slow subscribers dropped due to buffer overflow.
func (b *Broker) DroppedCount() uint64 {
	return b.droppedSubscribers.Load()
}

// DroppedSubscribers is an alias for DroppedCount.
func (b *Broker) DroppedSubscribers() uint64 {
	return b.droppedSubscribers.Load()
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

// Publish delivers an event to all matching subscribers.
// Fan-out is strictly non-blocking: if a subscriber's channel buffer is full,
// the subscriber is considered stalled/slow and is dropped immediately
// so that a burst of writes never blocks the caller.
// Dropping a slow subscriber emits a structured warning log and increments the drop metric.
func (b *Broker) Publish(e domain.Event) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	var slowSubs []*subscriber
	for sub := range b.subscribers {
		// Filter by project ID when the subscriber is scoped to a specific project
		if sub.projectID != "" {
			if e.ProjectID == nil || *e.ProjectID != sub.projectID {
				continue
			}
		}

		select {
		case sub.ch <- e:
		default:
			// Client channel buffer full: slow client detected
			slowSubs = append(slowSubs, sub)
		}
	}
	b.mu.RUnlock()

	// Clean up slow subscribers if any were detected
	if len(slowSubs) > 0 {
		var dropped []*subscriber
		b.mu.Lock()
		for _, sub := range slowSubs {
			if _, exists := b.subscribers[sub]; exists {
				delete(b.subscribers, sub)
				if !sub.closed {
					sub.closed = true
					close(sub.ch)
				}
				dropped = append(dropped, sub)
			}
		}
		b.mu.Unlock()

		if len(dropped) > 0 {
			b.droppedSubscribers.Add(uint64(len(dropped)))
			for _, sub := range dropped {
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
				b.log().Warn("pubsub: slow subscriber buffer overflow, dropping subscriber",
					"subscriber_id", sub.id,
					"subscriber_project", subProj,
					"buffer_capacity", cap(sub.ch),
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
