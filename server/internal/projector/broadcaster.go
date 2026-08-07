package projector

import (
	"sync"

	"github.com/meow-sci/catlog/server/internal/store"
)

// subscriberBuffer is how many batches a slow subscriber may fall behind before
// it starts losing them. The feed is a "what is happening right now" panel, not
// a log: a client that cannot keep up is better served by a gap than by
// back-pressuring the projector, which would stall every projection write behind
// one wedged browser tab.
const subscriberBuffer = 8

// Broadcaster fans committed feed rows out to the SSE handlers (§5.6).
//
// WP5 attaches datastar's SSE handler to this; nothing here knows about HTTP.
// The contract is: rows arrive only after the transaction that inserted them
// committed, in projector order, and a subscriber that stops reading is dropped
// from rather than allowed to block the projector.
type Broadcaster struct {
	mu   sync.Mutex
	next int64
	subs map[int64]chan []store.FeedRow
}

// NewBroadcaster returns a broadcaster with no subscribers.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[int64]chan []store.FeedRow{}}
}

// Subscribe registers a listener and returns its channel plus the function that
// unregisters and closes it. Calling cancel twice is safe.
func (b *Broadcaster) Subscribe() (<-chan []store.FeedRow, func()) {
	ch := make(chan []store.FeedRow, subscriberBuffer)

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	n := len(b.subs)
	b.mu.Unlock()
	metricSSEClients.Set(int64(n))

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			n := len(b.subs)
			b.mu.Unlock()
			metricSSEClients.Set(int64(n))
			close(ch)
		})
	}
}

// Publish delivers a committed batch of feed rows to every subscriber, dropping
// the batch for any subscriber whose buffer is full.
func (b *Broadcaster) Publish(rows []store.FeedRow) {
	if len(rows) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- rows:
		default:
			// Dropped on purpose — see subscriberBuffer.
		}
	}
}

// Clients reports the number of live subscribers, which is also the `sse_clients`
// expvar (§5.9).
func (b *Broadcaster) Clients() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
