package events

import "encoding/json"

// FileChangeEventType represents the type of file change event
type FileChangeEventType string

const (
	// FileModified is sent when a file is written or edited
	FileModified FileChangeEventType = "file_modified"
	// FileDeleted is sent when a file is deleted
	FileDeleted FileChangeEventType = "file_deleted"
	// DiffSnapshot is sent when the current git diff changes.
	DiffSnapshot FileChangeEventType = "diff_snapshot"
)

// FileChangeEvent represents a file modification or diff invalidation during task execution.
type FileChangeEvent struct {
	Type      FileChangeEventType `json:"type"`
	TaskID    string              `json:"task_id"`
	ExecID    string              `json:"exec_id"`
	FilePath  string              `json:"file_path,omitempty"`
	ToolName  string              `json:"tool_name,omitempty"` // "write_file", "edit_file", etc.
	Timestamp int64               `json:"timestamp"`           // Unix milliseconds
}

// FileChangeSubscriber is a channel that receives file change events
type FileChangeSubscriber chan FileChangeEvent

// FileChangeBroadcaster manages file change event subscribers and publishes events to them
type FileChangeBroadcaster struct {
	core broadcaster[FileChangeEvent, FileChangeSubscriber]
}

// NewFileChangeBroadcaster creates a new file change event broadcaster
func NewFileChangeBroadcaster() *FileChangeBroadcaster {
	return &FileChangeBroadcaster{core: newBroadcaster[FileChangeEvent, FileChangeSubscriber](50)}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *FileChangeBroadcaster) Subscribe() (FileChangeSubscriber, error) {
	return b.core.Subscribe()
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *FileChangeBroadcaster) Unsubscribe(sub FileChangeSubscriber) {
	b.core.Unsubscribe(sub)
}

// Publish sends an event to all subscribers.
func (b *FileChangeBroadcaster) Publish(event FileChangeEvent) {
	b.core.Publish(event)
}

// SubscriberCount returns the current number of subscribers.
func (b *FileChangeBroadcaster) SubscriberCount() int {
	return b.core.SubscriberCount()
}

// ToSSE converts a FileChangeEvent to SSE format
func (e FileChangeEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}
