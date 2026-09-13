package events

import (
	"sync"
	"sync/atomic"
)

// subscriberGuard serializes subscriber channel closure and records its terminal state.
type subscriberGuard struct {
	mu     sync.Mutex
	closed bool
}

type broadcasterEntry[T any, S ~chan T] struct {
	ch    S
	guard *subscriberGuard
}

type broadcasterSubscription struct {
	guard *subscriberGuard
	scope string
}

// broadcaster owns the shared subscriber lifecycle for typed event channels.
type broadcaster[T any, S ~chan T] struct {
	mu                sync.RWMutex
	subscribers       map[S]broadcasterSubscription
	scopedSubscribers map[string]map[S]*subscriberGuard
	scopedSnapshots   map[string][]broadcasterEntry[T, S]
	bufferSize        int
	deliveryAttempts  atomic.Uint64
}

func newBroadcaster[T any, S ~chan T](bufferSize int) broadcaster[T, S] {
	return broadcaster[T, S]{
		subscribers:       make(map[S]broadcasterSubscription),
		scopedSubscribers: make(map[string]map[S]*subscriberGuard),
		scopedSnapshots:   make(map[string][]broadcasterEntry[T, S]),
		bufferSize:        bufferSize,
	}
}

// Subscribe adds a global subscriber and returns a channel for receiving every event.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *broadcaster[T, S]) Subscribe() (S, error) {
	return b.SubscribeScoped("")
}

// SubscribeScoped adds a subscriber that receives events for one scope. An empty scope
// is the deliberate global subscription bucket.
func (b *broadcaster[T, S]) SubscribeScoped(scope string) (S, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= MaxSubscribers {
		var zero S
		return zero, ErrMaxSubscribers
	}

	sub := make(S, b.bufferSize)
	guard := &subscriberGuard{}
	b.subscribers[sub] = broadcasterSubscription{guard: guard, scope: scope}
	if b.scopedSubscribers[scope] == nil {
		b.scopedSubscribers[scope] = make(map[S]*subscriberGuard)
	}
	b.scopedSubscribers[scope][sub] = guard
	b.rebuildScopedSnapshotLocked(scope)
	return sub, nil
}

// rebuildScopedSnapshotLocked replaces one scope's immutable subscriber snapshot.
// Callers must hold b.mu for writing. Existing snapshots are never modified; publishers
// hold b.mu for reading through their nonblocking sends so a channel cannot close while
// it is being offered an event.
func (b *broadcaster[T, S]) rebuildScopedSnapshotLocked(scope string) {
	members := b.scopedSubscribers[scope]
	if len(members) == 0 {
		delete(b.scopedSnapshots, scope)
		return
	}

	snapshot := make([]broadcasterEntry[T, S], 0, len(members))
	for sub, guard := range members {
		snapshot = append(snapshot, broadcasterEntry[T, S]{ch: sub, guard: guard})
	}
	b.scopedSnapshots[scope] = snapshot
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *broadcaster[T, S]) Unsubscribe(sub S) {
	b.mu.Lock()
	subscription, exists := b.subscribers[sub]
	if exists {
		delete(b.subscribers, sub)
		delete(b.scopedSubscribers[subscription.scope], sub)
		if len(b.scopedSubscribers[subscription.scope]) == 0 {
			delete(b.scopedSubscribers, subscription.scope)
		}
		b.rebuildScopedSnapshotLocked(subscription.scope)

		subscription.guard.mu.Lock()
		subscription.guard.closed = true
		close(sub)
		subscription.guard.mu.Unlock()
	}
	b.mu.Unlock()
}

// Publish sends an event to deliberate global subscribers without blocking the publisher.
func (b *broadcaster[T, S]) Publish(event T) {
	b.PublishScoped("", event)
}

// PublishScoped sends an event to global subscribers and subscribers for the matching
// scope without blocking the publisher. The read lock remains held during fan-out so
// Unsubscribe cannot close a channel until all in-flight sends have completed.
func (b *broadcaster[T, S]) PublishScoped(scope string, event T) {
	b.mu.RLock()
	globalSnapshot := b.scopedSnapshots[""]
	scopedSnapshot := b.scopedSnapshots[scope]

	deliveryAttempts := len(globalSnapshot)
	if scope != "" {
		deliveryAttempts += len(scopedSnapshot)
	}
	b.deliveryAttempts.Add(uint64(deliveryAttempts))

	for _, entry := range globalSnapshot {
		if !entry.guard.closed {
			select {
			case entry.ch <- event:
			default:
				// Drop events for slow subscribers rather than blocking producers.
			}
		}
	}
	if scope != "" {
		for _, entry := range scopedSnapshot {
			if !entry.guard.closed {
				select {
				case entry.ch <- event:
				default:
					// Drop events for slow subscribers rather than blocking producers.
				}
			}
		}
	}
	b.mu.RUnlock()
}

// DeliveryAttempts returns the total number of subscriber delivery attempts.
func (b *broadcaster[T, S]) DeliveryAttempts() uint64 {
	return b.deliveryAttempts.Load()
}

// SubscriberCount returns the current number of subscribers.
func (b *broadcaster[T, S]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
