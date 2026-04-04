package events

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestTaskEvent_ToSSE_IncludesTaskName(t *testing.T) {
	event := TaskEvent{
		Type:      TaskStatusChanged,
		TaskID:    "abc123",
		TaskName:  "My Test Task",
		ProjectID: "proj1",
		Status:    "completed",
		OldStatus: "running",
	}

	sse := event.ToSSE()

	// Verify SSE format
	if !strings.HasPrefix(sse, "data: ") {
		t.Error("SSE should start with 'data: '")
	}
	if !strings.HasSuffix(sse, "\n\n") {
		t.Error("SSE should end with double newline")
	}

	// Parse the JSON data
	jsonStr := strings.TrimPrefix(sse, "data: ")
	jsonStr = strings.TrimSuffix(jsonStr, "\n\n")

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("failed to parse SSE JSON: %v", err)
	}

	if parsed["task_name"] != "My Test Task" {
		t.Errorf("expected task_name 'My Test Task', got %v", parsed["task_name"])
	}
	if parsed["task_id"] != "abc123" {
		t.Errorf("expected task_id 'abc123', got %v", parsed["task_id"])
	}
	if parsed["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", parsed["status"])
	}
}

func TestTaskEvent_ToSSE_OmitsEmptyTaskName(t *testing.T) {
	event := TaskEvent{
		Type:   AlertCreated,
		TaskID: "abc123",
		// TaskName intentionally empty
	}

	sse := event.ToSSE()
	jsonStr := strings.TrimPrefix(sse, "data: ")
	jsonStr = strings.TrimSuffix(jsonStr, "\n\n")

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("failed to parse SSE JSON: %v", err)
	}

	// task_name should be omitted when empty (omitempty)
	if _, exists := parsed["task_name"]; exists {
		t.Error("task_name should be omitted when empty")
	}
}

func TestBroadcaster_PublishSubscribe(t *testing.T) {
	b := NewBroadcaster()
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer b.Unsubscribe(sub)

	event := TaskEvent{
		Type:     TaskStatusChanged,
		TaskID:   "task1",
		TaskName: "Test Task",
		Status:   "completed",
	}

	b.Publish(event)

	received := <-sub
	if received.TaskName != "Test Task" {
		t.Errorf("expected TaskName 'Test Task', got %s", received.TaskName)
	}
}

func TestBroadcaster_MaxSubscribers(t *testing.T) {
	b := NewBroadcaster()

	// Create subscribers up to the limit
	subs := make([]Subscriber, 0, MaxSubscribers)
	for i := 0; i < MaxSubscribers; i++ {
		sub, err := b.Subscribe()
		if err != nil {
			t.Fatalf("Subscribe #%d should succeed, got: %v", i, err)
		}
		subs = append(subs, sub)
	}

	// Next subscribe should fail
	_, err := b.Subscribe()
	if err != ErrMaxSubscribers {
		t.Errorf("expected ErrMaxSubscribers, got: %v", err)
	}

	// After unsubscribing one, should be able to subscribe again
	b.Unsubscribe(subs[0])
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe after unsubscribe should succeed, got: %v", err)
	}
	b.Unsubscribe(sub)

	// Clean up remaining subscribers
	for _, s := range subs[1:] {
		b.Unsubscribe(s)
	}

	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after cleanup, got %d", b.SubscriberCount())
	}
}

func TestBroadcaster_UnsubscribeFreeSlot(t *testing.T) {
	b := NewBroadcaster()

	sub1, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if b.SubscriberCount() != 1 {
		t.Errorf("expected 1 subscriber, got %d", b.SubscriberCount())
	}

	b.Unsubscribe(sub1)
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", b.SubscriberCount())
	}

	// Double unsubscribe should be safe (no panic)
	b.Unsubscribe(sub1)
}

func TestBroadcaster_ConcurrentSubscribeUnsubscribePublish(t *testing.T) {
	b := NewBroadcaster()
	var wg sync.WaitGroup

	// Concurrent publishers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish(TaskEvent{
					Type:   TaskStatusChanged,
					TaskID: "task1",
					Status: "running",
				})
			}
		}()
	}

	// Concurrent subscribe/unsubscribe cycles
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub, err := b.Subscribe()
				if err != nil {
					continue // max subscribers reached, retry
				}
				// Drain a few events to exercise the send path
				for k := 0; k < 3; k++ {
					select {
					case <-sub:
					default:
					}
				}
				b.Unsubscribe(sub)
			}
		}()
	}

	wg.Wait()
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after concurrent test, got %d", b.SubscriberCount())
	}
}
