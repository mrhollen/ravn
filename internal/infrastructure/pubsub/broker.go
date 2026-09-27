package pubsub

import (
	"sync"

	"github.com/mrhollen/crowban/internal/application"
	"github.com/mrhollen/crowban/internal/domain"
)

// DefaultClientBufferSize is the default capacity of each subscriber's channel buffer.
// If a client's buffer fills up (e.g. client is stalled or slow), the broker drops
// the client immediately so that bursts of writes never block mutating handlers.
const DefaultClientBufferSize = 64

type subscriber struct {
	ch        chan domain.Event
	projectID domain.ID // empty string matches all projects
	closed    bool
}

// Broker implements application.EventBroker using an in-memory pub-sub model.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[*subscriber]struct{}
	bufferSize  int
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

// Subscribe registers a listener for events belonging to projectID.
// An empty projectID subscribes to events across all projects.
// The returned cancel function unregisters the subscriber and closes its channel.
func (b *Broker) Subscribe(projectID domain.ID) (<-chan domain.Event, func()) {
	sub := &subscriber{
		ch:        make(chan domain.Event, b.bufferSize),
		projectID: projectID,
	}

	b.mu.Lock()
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
func (b *Broker) Publish(e domain.Event) {
	b.mu.RLock()
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
		b.mu.Lock()
		for _, sub := range slowSubs {
			if _, exists := b.subscribers[sub]; exists {
				delete(b.subscribers, sub)
				if !sub.closed {
					sub.closed = true
					close(sub.ch)
				}
			}
		}
		b.mu.Unlock()
	}
}

// SubscriberCount returns the current count of active subscribers.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// SubscriberCountForProject returns active subscribers scoped to projectID.
func (b *Broker) SubscriberCountForProject(projectID domain.ID) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	count := 0
	for sub := range b.subscribers {
		if sub.projectID == projectID {
			count++
		}
	}
	return count
}
