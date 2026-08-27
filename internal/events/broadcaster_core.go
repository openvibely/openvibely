package events

import "sync"

// subscriberGuard protects a subscriber channel from concurrent send/close races.
type subscriberGuard struct {
	mu     sync.Mutex
	closed bool
}

type broadcasterEntry[T any, S ~chan T] struct {
	ch    S
	guard *subscriberGuard
}

// broadcaster owns the shared subscriber lifecycle for typed event channels.
type broadcaster[T any, S ~chan T] struct {
	mu          sync.RWMutex
	subscribers map[S]*subscriberGuard
	bufferSize  int
}

func newBroadcaster[T any, S ~chan T](bufferSize int) broadcaster[T, S] {
	return broadcaster[T, S]{
		subscribers: make(map[S]*subscriberGuard),
		bufferSize:  bufferSize,
	}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *broadcaster[T, S]) Subscribe() (S, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= MaxSubscribers {
		var zero S
		return zero, ErrMaxSubscribers
	}

	sub := make(S, b.bufferSize)
	b.subscribers[sub] = &subscriberGuard{}
	return sub, nil
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *broadcaster[T, S]) Unsubscribe(sub S) {
	b.mu.Lock()
	guard, exists := b.subscribers[sub]
	if exists {
		delete(b.subscribers, sub)
	}
	b.mu.Unlock()

	if exists {
		guard.mu.Lock()
		guard.closed = true
		close(sub)
		guard.mu.Unlock()
	}
}

// Publish sends an event to all subscribers without blocking the publisher.
func (b *broadcaster[T, S]) Publish(event T) {
	b.mu.RLock()
	subs := make([]broadcasterEntry[T, S], 0, len(b.subscribers))
	for sub, guard := range b.subscribers {
		subs = append(subs, broadcasterEntry[T, S]{ch: sub, guard: guard})
	}
	b.mu.RUnlock()

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

// SubscriberCount returns the current number of subscribers.
func (b *broadcaster[T, S]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
