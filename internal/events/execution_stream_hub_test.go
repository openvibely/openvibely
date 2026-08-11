package events

import (
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func receiveExecutionStreamEvent(t *testing.T, ch <-chan ExecutionStreamEvent) ExecutionStreamEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed before event")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for execution stream event")
		return ExecutionStreamEvent{}
	}
}

func assertNoExecutionStreamEvent(t *testing.T, ch <-chan ExecutionStreamEvent) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			return
		}
		t.Fatalf("unexpected execution stream event: %+v", ev)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestExecutionStreamHubPublishesByExactExecID(t *testing.T) {
	hub := NewExecutionStreamHub()
	exec1, unsub1, err := hub.Subscribe("exec-1")
	if err != nil {
		t.Fatalf("subscribe exec-1: %v", err)
	}
	defer unsub1()
	exec2, unsub2, err := hub.Subscribe("exec-2")
	if err != nil {
		t.Fatalf("subscribe exec-2: %v", err)
	}
	defer unsub2()

	hub.Publish(ExecutionStreamEvent{ExecID: "exec-1", Type: ExecutionStreamDelta, Delta: "hello", Offset: 5})

	got := receiveExecutionStreamEvent(t, exec1)
	if got.ExecID != "exec-1" || got.Type != ExecutionStreamDelta || got.Delta != "hello" || got.Offset != 5 {
		t.Fatalf("unexpected exec-1 event: %+v", got)
	}
	assertNoExecutionStreamEvent(t, exec2)
}

func TestExecutionStreamHubFansOutAndClosesTerminal(t *testing.T) {
	hub := NewExecutionStreamHub()
	sub1, unsub1, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatalf("subscribe 1: %v", err)
	}
	defer unsub1()
	sub2, unsub2, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatalf("subscribe 2: %v", err)
	}
	defer unsub2()

	hub.Publish(ExecutionStreamEvent{ExecID: "exec", Type: ExecutionStreamDelta, Delta: "x", Offset: 1})
	if ev := receiveExecutionStreamEvent(t, sub1); ev.Delta != "x" || ev.Offset != 1 {
		t.Fatalf("subscriber 1 did not receive delta: %+v", ev)
	}
	if ev := receiveExecutionStreamEvent(t, sub2); ev.Delta != "x" || ev.Offset != 1 {
		t.Fatalf("subscriber 2 did not receive delta: %+v", ev)
	}

	hub.Close("exec", ExecutionStreamEvent{Type: ExecutionStreamDone, Status: "completed"})
	if ev := receiveExecutionStreamEvent(t, sub1); ev.Type != ExecutionStreamDone || ev.Status != "completed" || ev.ExecID != "exec" {
		t.Fatalf("subscriber 1 did not receive terminal: %+v", ev)
	}
	if ev := receiveExecutionStreamEvent(t, sub2); ev.Type != ExecutionStreamDone || ev.Status != "completed" || ev.ExecID != "exec" {
		t.Fatalf("subscriber 2 did not receive terminal: %+v", ev)
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected subscribers cleaned up after close, got %d", hub.SubscriberCount())
	}
}

func TestExecutionStreamHubCloseTerminalMapsExecutionStatuses(t *testing.T) {
	tests := []struct {
		name       string
		status     models.ExecutionStatus
		errMsg     string
		wantType   ExecutionStreamEventType
		wantStatus string
		wantError  string
	}{
		{
			name:       "completed",
			status:     models.ExecCompleted,
			wantType:   ExecutionStreamDone,
			wantStatus: string(models.ExecCompleted),
		},
		{
			name:       "cancelled",
			status:     models.ExecCancelled,
			wantType:   ExecutionStreamDone,
			wantStatus: string(models.ExecCancelled),
		},
		{
			name:      "failed",
			status:    models.ExecFailed,
			errMsg:    "provider failed",
			wantType:  ExecutionStreamError,
			wantError: "provider failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := NewExecutionStreamHub()
			sub, _, err := hub.Subscribe("exec")
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}

			hub.CloseTerminal("exec", test.status, test.errMsg)

			event := receiveExecutionStreamEvent(t, sub)
			if event.ExecID != "exec" || event.Type != test.wantType || event.Status != test.wantStatus || event.Error != test.wantError {
				t.Fatalf("unexpected terminal event: %+v", event)
			}
			if _, ok := <-sub; ok {
				t.Fatal("subscriber remained open after terminal close")
			}
			if got := hub.SubscriberCount(); got != 0 {
				t.Fatalf("subscriber count after terminal close = %d", got)
			}
		})
	}
}

func TestExecutionStreamHubCloseTerminalIgnoresNonTerminalStatuses(t *testing.T) {
	hub := NewExecutionStreamHub()
	sub, unsubscribe, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	hub.CloseTerminal("exec", models.ExecRunning, "")

	assertNoExecutionStreamEvent(t, sub)
	if got := hub.SubscriberCount(); got != 1 {
		t.Fatalf("subscriber count after non-terminal status = %d", got)
	}
}

func TestExecutionStreamHubUnsubscribeStopsDelivery(t *testing.T) {
	hub := NewExecutionStreamHub()
	sub, unsubscribe, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	unsubscribe()

	hub.Publish(ExecutionStreamEvent{ExecID: "exec", Type: ExecutionStreamDelta, Delta: "x", Offset: 1})
	assertNoExecutionStreamEvent(t, sub)
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected subscriber count 0, got %d", hub.SubscriberCount())
	}
}

func TestExecutionStreamHubPublishDoesNotBlockSlowSubscriber(t *testing.T) {
	hub := NewExecutionStreamHub()
	sub, unsubscribe, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	for i := 0; i < cap(sub)+10; i++ {
		done := make(chan struct{})
		go func(i int) {
			hub.Publish(ExecutionStreamEvent{ExecID: "exec", Type: ExecutionStreamDelta, Delta: fmt.Sprintf("%d", i), Offset: i + 1})
			close(done)
		}(i)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("publish %d blocked on slow subscriber", i)
		}
	}
}

func TestExecutionStreamHubSubscriberLimitUsesTotalSubscribers(t *testing.T) {
	hub := NewExecutionStreamHub()
	unsubscribes := make([]func(), 0, MaxSubscribers)
	for i := 0; i < MaxSubscribers; i++ {
		_, unsubscribe, err := hub.Subscribe(fmt.Sprintf("exec-%d", i))
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	if _, _, err := hub.Subscribe("overflow"); err != ErrMaxSubscribers {
		t.Fatalf("expected ErrMaxSubscribers after %d total subscribers, got %v", MaxSubscribers, err)
	}
	for _, unsubscribe := range unsubscribes {
		unsubscribe()
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("expected subscriber count 0 after unsubscribe, got %d", hub.SubscriberCount())
	}
}
