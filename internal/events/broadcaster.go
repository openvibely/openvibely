package events

import (
	"encoding/json"
	"errors"
	"sync"
)

// MaxSubscribers is the maximum number of concurrent SSE subscribers.
// With multi-tab usage, each tab creates one SSE connection. This limit
// prevents resource exhaustion from excessive open connections.
const MaxSubscribers = 50

// ErrMaxSubscribers is returned when the subscriber limit is reached
var ErrMaxSubscribers = errors.New("maximum subscriber limit reached")

// TaskEventType represents the type of task event
type TaskEventType string

const (
	TaskStatusChanged   TaskEventType = "task_status_changed"
	TaskCategoryChanged TaskEventType = "task_category_changed"
	AlertCreated        TaskEventType = "alert_created"
)

// TaskEvent represents a task state change event
type TaskEvent struct {
	Type        TaskEventType `json:"type"`
	TaskID      string        `json:"task_id"`
	TaskName    string        `json:"task_name,omitempty"`
	ProjectID   string        `json:"project_id,omitempty"`
	Status      string        `json:"status,omitempty"`
	Category    string        `json:"category,omitempty"`
	OldStatus   string        `json:"old_status,omitempty"`
	OldCategory string        `json:"old_category,omitempty"`
	AlertID     string        `json:"alert_id,omitempty"`
}

// Subscriber is a channel that receives task events
type Subscriber chan TaskEvent

// subGuard protects a subscriber channel from concurrent send/close races.
type subGuard struct {
	mu     sync.Mutex
	closed bool
}

// Broadcaster manages event subscribers and publishes events to them
type Broadcaster struct {
	mu          sync.RWMutex
	subscribers map[Subscriber]*subGuard
}

// NewBroadcaster creates a new event broadcaster
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subscribers: make(map[Subscriber]*subGuard),
	}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *Broadcaster) Subscribe() (Subscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= MaxSubscribers {
		return nil, ErrMaxSubscribers
	}

	sub := make(Subscriber, 10) // buffered to prevent blocking
	b.subscribers[sub] = &subGuard{}
	return sub, nil
}

// Unsubscribe removes a subscriber and closes its channel
func (b *Broadcaster) Unsubscribe(sub Subscriber) {
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

// Publish sends an event to all subscribers
func (b *Broadcaster) Publish(event TaskEvent) {
	b.mu.RLock()
	type entry struct {
		ch    Subscriber
		guard *subGuard
	}
	subs := make([]entry, 0, len(b.subscribers))
	for sub, guard := range b.subscribers {
		subs = append(subs, entry{sub, guard})
	}
	b.mu.RUnlock()

	for _, e := range subs {
		e.guard.mu.Lock()
		if !e.guard.closed {
			select {
			case e.ch <- event:
			default:
			}
		}
		e.guard.mu.Unlock()
	}
}

// SubscriberCount returns the current number of subscribers
func (b *Broadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// ToSSE converts a TaskEvent to SSE format
func (e TaskEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}
