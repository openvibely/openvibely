package events

import "encoding/json"

// ChatEventType represents the type of chat event
type ChatEventType string

const (
	// ChatNewMessage is sent when a new chat message arrives (from Telegram, web, or API)
	ChatNewMessage ChatEventType = "chat_new_message"
	// ChatResponseDone is sent when the AI response is complete
	ChatResponseDone ChatEventType = "chat_response_done"
	// ChatTurnSteered is sent when a steering message is saved for the active turn.
	ChatTurnSteered ChatEventType = "chat_turn_steered"
	// ChatThreadInputApplied is sent when a queued/steering row is consumed by a running turn.
	ChatThreadInputApplied ChatEventType = "chat_thread_input_applied"
	// ChatThreadInputCancelled is sent when a pending queued/steering row is cancelled.
	ChatThreadInputCancelled ChatEventType = "chat_thread_input_cancelled"
)

// ChatEvent represents a chat event for real-time updates
type ChatEvent struct {
	Type            ChatEventType `json:"type"`
	ProjectID       string        `json:"project_id"`
	ExecID          string        `json:"exec_id"`
	TaskID          string        `json:"task_id,omitempty"`
	Message         string        `json:"message,omitempty"`
	Source          string        `json:"source,omitempty"` // "telegram", "web", "api"
	AgentName       string        `json:"agent_name,omitempty"`
	CompletedOutput string        `json:"completed_output,omitempty"` // Final assistant output for ChatResponseDone; enables plan-completion prompt without DOM scan
	Status          string        `json:"status,omitempty"`           // Authoritative terminal execution status for ChatResponseDone
	Queued          bool          `json:"queued,omitempty"`
	Steering        bool          `json:"steering,omitempty"`
	HasAttachments  bool          `json:"has_attachments,omitempty"`
	PendingInputID  string        `json:"pending_input_id,omitempty"`
	IsTaskFollowup  bool          `json:"is_task_followup,omitempty"`
}

// ToSSE converts a ChatEvent to SSE format
func (e ChatEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}

// ChatSubscriber is a channel that receives chat events
type ChatSubscriber chan ChatEvent

// ChatBroadcaster manages chat event subscribers and publishes events to them
type ChatBroadcaster struct {
	core broadcaster[ChatEvent, ChatSubscriber]
}

// NewChatBroadcaster creates a new chat event broadcaster
func NewChatBroadcaster() *ChatBroadcaster {
	return &ChatBroadcaster{core: newBroadcaster[ChatEvent, ChatSubscriber](10)}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *ChatBroadcaster) Subscribe() (ChatSubscriber, error) {
	return b.core.Subscribe()
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *ChatBroadcaster) Unsubscribe(sub ChatSubscriber) {
	b.core.Unsubscribe(sub)
}

// Publish sends an event to all subscribers.
func (b *ChatBroadcaster) Publish(event ChatEvent) {
	b.core.Publish(event)
}

// SubscriberCount returns the current number of subscribers.
func (b *ChatBroadcaster) SubscriberCount() int {
	return b.core.SubscriberCount()
}
