package events

import (
	"encoding/json"
	"sync"
)

// FileChangeEventType represents the type of file change event
type FileChangeEventType string

const (
	// FileModified is sent when a file is written or edited
	FileModified FileChangeEventType = "file_modified"
	// FileDeleted is sent when a file is deleted
	FileDeleted FileChangeEventType = "file_deleted"
	// DiffSnapshot is sent periodically with the current git diff
	DiffSnapshot FileChangeEventType = "diff_snapshot"
)

// FileChangeEvent represents a file modification during task execution
type FileChangeEvent struct {
	Type       FileChangeEventType `json:"type"`
	TaskID     string              `json:"task_id"`
	ExecID     string              `json:"exec_id"`
	FilePath   string              `json:"file_path,omitempty"`
	ToolName   string              `json:"tool_name,omitempty"`   // "write_file", "edit_file", etc.
	DiffOutput string              `json:"diff_output,omitempty"` // git diff output
	Timestamp  int64               `json:"timestamp"`             // Unix milliseconds
}

// FileChangeSubscriber is a channel that receives file change events
type FileChangeSubscriber chan FileChangeEvent

// fileChangeSubGuard protects a file change subscriber channel from concurrent send/close races.
type fileChangeSubGuard struct {
	mu     sync.Mutex
	closed bool
}

// FileChangeBroadcaster manages file change event subscribers and publishes events to them
type FileChangeBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[FileChangeSubscriber]*fileChangeSubGuard
}

// NewFileChangeBroadcaster creates a new file change event broadcaster
func NewFileChangeBroadcaster() *FileChangeBroadcaster {
	return &FileChangeBroadcaster{
		subscribers: make(map[FileChangeSubscriber]*fileChangeSubGuard),
	}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *FileChangeBroadcaster) Subscribe() (FileChangeSubscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= MaxSubscribers {
		return nil, ErrMaxSubscribers
	}

	sub := make(FileChangeSubscriber, 50) // larger buffer for frequent file changes
	b.subscribers[sub] = &fileChangeSubGuard{}
	return sub, nil
}

// Unsubscribe removes a subscriber and closes its channel
func (b *FileChangeBroadcaster) Unsubscribe(sub FileChangeSubscriber) {
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
func (b *FileChangeBroadcaster) Publish(event FileChangeEvent) {
	b.mu.RLock()
	type entry struct {
		ch    FileChangeSubscriber
		guard *fileChangeSubGuard
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
				// Drop event if channel is full (subscriber is slow)
			}
		}
		e.guard.mu.Unlock()
	}
}

// SubscriberCount returns the current number of subscribers
func (b *FileChangeBroadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// ToSSE converts a FileChangeEvent to SSE format
func (e FileChangeEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}
