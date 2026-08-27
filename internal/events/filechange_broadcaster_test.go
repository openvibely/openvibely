package events

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileChangeBroadcaster_PublishAndReceive(t *testing.T) {
	b := NewFileChangeBroadcaster()

	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer b.Unsubscribe(sub)

	// Publish event
	event := FileChangeEvent{
		Type:     FileModified,
		TaskID:   "task123",
		ExecID:   "exec456",
		FilePath: "main.go",
		ToolName: "write_file",
	}

	go b.Publish(event)

	// Receive event
	select {
	case received := <-sub:
		if received.TaskID != "task123" {
			t.Errorf("expected task_id task123, got %s", received.TaskID)
		}
		if received.FilePath != "main.go" {
			t.Errorf("expected file_path main.go, got %s", received.FilePath)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestFileChangeBroadcaster_MaxSubscribers(t *testing.T) {
	b := NewFileChangeBroadcaster()

	// Subscribe up to the limit
	subs := make([]FileChangeSubscriber, MaxSubscribers)
	for i := 0; i < MaxSubscribers; i++ {
		sub, err := b.Subscribe()
		if err != nil {
			t.Fatalf("Subscribe %d failed: %v", i, err)
		}
		subs[i] = sub
	}

	// Next subscribe should fail
	_, err := b.Subscribe()
	if err != ErrMaxSubscribers {
		t.Errorf("expected ErrMaxSubscribers, got %v", err)
	}

	// Unsubscribe all
	for _, sub := range subs {
		b.Unsubscribe(sub)
	}

	// Should be able to subscribe again
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe after unsubscribe failed: %v", err)
	}
	b.Unsubscribe(sub)
}

func TestFileChangeBroadcaster_MultipleSubscribers(t *testing.T) {
	b := NewFileChangeBroadcaster()

	sub1, _ := b.Subscribe()
	sub2, _ := b.Subscribe()
	defer b.Unsubscribe(sub1)
	defer b.Unsubscribe(sub2)

	event := FileChangeEvent{
		Type:   DiffSnapshot,
		TaskID: "task789",
		ExecID: "exec012",
	}

	go b.Publish(event)

	// Both subscribers should receive the event
	received1 := false
	received2 := false

	timeout := time.After(1 * time.Second)
	for !received1 || !received2 {
		select {
		case e := <-sub1:
			if e.TaskID == "task789" {
				received1 = true
			}
		case e := <-sub2:
			if e.TaskID == "task789" {
				received2 = true
			}
		case <-timeout:
			t.Fatalf("timeout: received1=%v received2=%v", received1, received2)
		}
	}
}

func TestFileChangeBroadcasterSubscriberCount(t *testing.T) {
	b := NewFileChangeBroadcaster()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("initial SubscriberCount = %d, want 0", got)
	}
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount after subscribe = %d, want 1", got)
	}
	b.Unsubscribe(sub)
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after unsubscribe = %d, want 0", got)
	}
}

func TestFileChangeBroadcaster_NonBlockingPublish(t *testing.T) {
	b := NewFileChangeBroadcaster()
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer b.Unsubscribe(sub)

	for i := 0; i < 55; i++ {
		b.Publish(FileChangeEvent{Type: DiffSnapshot, TaskID: "task", ExecID: "exec"})
	}
	if got := cap(sub); got != 50 {
		t.Fatalf("subscriber capacity = %d, want 50", got)
	}
	if got := len(sub); got != 50 {
		t.Fatalf("buffered events = %d, want 50 after dropping full-buffer events", got)
	}
}

func TestFileChangeBroadcaster_UnsubscribeIdempotent(t *testing.T) {
	b := NewFileChangeBroadcaster()
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	b.Unsubscribe(sub)
	b.Unsubscribe(sub)
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after double unsubscribe = %d, want 0", got)
	}
}

func TestFileChangeBroadcaster_ConcurrentSubscribeUnsubscribePublish(t *testing.T) {
	b := NewFileChangeBroadcaster()
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish(FileChangeEvent{Type: DiffSnapshot, TaskID: "task", ExecID: "exec"})
			}
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub, err := b.Subscribe()
				if err != nil {
					continue
				}
				select {
				case <-sub:
				default:
				}
				b.Unsubscribe(sub)
				b.Unsubscribe(sub)
			}
		}()
	}

	wg.Wait()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after concurrent lifecycle = %d, want 0", got)
	}
}

func TestFileChangeEvent_ToSSE(t *testing.T) {
	event := FileChangeEvent{
		Type:      FileModified,
		TaskID:    "task123",
		ExecID:    "exec456",
		FilePath:  "test.go",
		ToolName:  "edit_file",
		Timestamp: 1234567890000,
	}

	sse := event.ToSSE()

	// Check SSE format
	if sse[:6] != "data: " {
		t.Error("SSE should start with 'data: '")
	}
	if sse[len(sse)-2:] != "\n\n" {
		t.Error("SSE should end with double newline")
	}

	// Check JSON content
	if !contains(sse, `"task_id":"task123"`) {
		t.Error("SSE should contain task_id")
	}
	if !contains(sse, `"file_path":"test.go"`) {
		t.Error("SSE should contain file_path")
	}
	if !contains(sse, `"tool_name":"edit_file"`) {
		t.Error("SSE should contain tool_name")
	}
}

func TestFileChangeEvent_DiffSnapshotSSEOmitsDiffOutput(t *testing.T) {
	event := FileChangeEvent{
		Type:      DiffSnapshot,
		TaskID:    "task123",
		ExecID:    "exec456",
		Timestamp: 1234567890000,
	}

	sse := event.ToSSE()
	if strings.Contains(sse, "diff_output") {
		t.Fatalf("diff_snapshot SSE should not contain diff_output: %s", sse)
	}
	if !contains(sse, `"type":"diff_snapshot"`) {
		t.Fatalf("SSE should contain diff_snapshot type: %s", sse)
	}
	if !contains(sse, `"task_id":"task123"`) || !contains(sse, `"exec_id":"exec456"`) {
		t.Fatalf("SSE should contain task and execution metadata: %s", sse)
	}

	payload := strings.TrimSuffix(strings.TrimPrefix(sse, "data: "), "\n\n")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("unmarshal SSE payload: %v", err)
	}
	if _, ok := decoded["diff_output"]; ok {
		t.Fatalf("diff_snapshot payload should not include diff_output: %#v", decoded)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
