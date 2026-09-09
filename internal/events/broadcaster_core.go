package events

import (
	"sync"
	"sync/atomic"
)

// subscriberGuard protects a subscriber channel from concurrent send/close races.
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
	bufferSize        int
	deliveryAttempts  atomic.Uint64
}

func newBroadcaster[T any, S ~chan T](bufferSize int) broadcaster[T, S] {
	return broadcaster[T, S]{
		subscribers:       make(map[S]broadcasterSubscription),
		scopedSubscribers: make(map[string]map[S]*subscriberGuard),
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
	return sub, nil
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
	}
	b.mu.Unlock()

	if exists {
		subscription.guard.mu.Lock()
		subscription.guard.closed = true
		close(sub)
		subscription.guard.mu.Unlock()
	}
}

// Publish sends an event to deliberate global subscribers without blocking the publisher.
func (b *broadcaster[T, S]) Publish(event T) {
	b.PublishScoped("", event)
}

// PublishScoped sends an event to global subscribers and subscribers for the matching
// scope without blocking the publisher.
func (b *broadcaster[T, S]) PublishScoped(scope string, event T) {
	b.mu.RLock()
	capacity := len(b.scopedSubscribers[""])
	if scope != "" {
		capacity += len(b.scopedSubscribers[scope])
	}
	subs := make([]broadcasterEntry[T, S], 0, capacity)
	for sub, guard := range b.scopedSubscribers[""] {
		subs = append(subs, broadcasterEntry[T, S]{ch: sub, guard: guard})
	}
	if scope != "" {
		for sub, guard := range b.scopedSubscribers[scope] {
			subs = append(subs, broadcasterEntry[T, S]{ch: sub, guard: guard})
		}
	}
	b.mu.RUnlock()

	b.deliveryAttempts.Add(uint64(len(subs)))
	for _, entry := range subs {
		entry.guard.mu.Lock()
		if !entry.guard.closed {
			select {
			case entry.ch <- event:
			default:
				// Drop events for slow subscribers rather than blocking producers.
			}
		}
		entry.guard.mu.Unlock()
	}
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
