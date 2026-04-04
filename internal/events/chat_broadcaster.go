package events

import (
	"encoding/json"
	"sync"
)

// ChatEventType represents the type of chat event
type ChatEventType string

const (
	// ChatNewMessage is sent when a new chat message arrives (from Telegram, web, or API)
	ChatNewMessage ChatEventType = "chat_new_message"
	// ChatResponseDone is sent when the AI response is complete
	ChatResponseDone ChatEventType = "chat_response_done"
)

// ChatEvent represents a chat event for real-time updates
type ChatEvent struct {
	Type      ChatEventType `json:"type"`
	ProjectID string        `json:"project_id"`
	ExecID    string        `json:"exec_id"`
	TaskID    string        `json:"task_id,omitempty"`
	Message   string        `json:"message,omitempty"`
	Source    string        `json:"source,omitempty"` // "telegram", "web", "api"
	AgentName string        `json:"agent_name,omitempty"`
}

// ToSSE converts a ChatEvent to SSE format
func (e ChatEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}

// ChatSubscriber is a channel that receives chat events
type ChatSubscriber chan ChatEvent

// chatSubGuard protects a chat subscriber channel from concurrent send/close races.
type chatSubGuard struct {
	mu     sync.Mutex
	closed bool
}

// ChatBroadcaster manages chat event subscribers and publishes events to them
type ChatBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[ChatSubscriber]*chatSubGuard
}

// NewChatBroadcaster creates a new chat event broadcaster
func NewChatBroadcaster() *ChatBroadcaster {
	return &ChatBroadcaster{
		subscribers: make(map[ChatSubscriber]*chatSubGuard),
	}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *ChatBroadcaster) Subscribe() (ChatSubscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= MaxSubscribers {
		return nil, ErrMaxSubscribers
	}

	sub := make(ChatSubscriber, 10) // buffered to prevent blocking
	b.subscribers[sub] = &chatSubGuard{}
	return sub, nil
}

// Unsubscribe removes a subscriber and closes its channel
func (b *ChatBroadcaster) Unsubscribe(sub ChatSubscriber) {
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
func (b *ChatBroadcaster) Publish(event ChatEvent) {
	b.mu.RLock()
	type entry struct {
		ch    ChatSubscriber
		guard *chatSubGuard
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
func (b *ChatBroadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
