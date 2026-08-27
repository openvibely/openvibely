package handler

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
)

func TestLiveEventsSSE_ReceivesTaskChatAndFileEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive SSE test in short mode")
	}
	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()

	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
	}

	e := echo.New()
	e.GET("/events/live", h.LiveEventsSSE)

	srv := httptest.NewServer(e)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events/live")
	if err != nil {
		t.Fatalf("GET /events/live: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream content type, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatal("expected initial ping line")
	}
	if line := scanner.Text(); line != ": ping" {
		t.Fatalf("expected ': ping', got %q", line)
	}
	// Empty separator line after ping.
	scanner.Scan()

	// Subscriptions are established before the ping is written, so events published
	// after the client reads the ping are guaranteed to be delivered — no sleep needed.
	go func() {
		taskBroadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    "task-1",
			ProjectID: "proj-1",
			Status:    "running",
		})
		taskBroadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadExecutionStarted,
			TaskID:         "task-1",
			ProjectID:      "proj-1",
			ExecID:         "exec-thread-1",
			PendingInputID: "input-1",
		})
		chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ExecID:    "exec-1",
			ProjectID: "proj-1",
			Message:   "hello",
			Source:    "telegram",
		})
		chatBroadcaster.Publish(events.ChatEvent{
			Type:           events.ChatThreadInputApplied,
			ExecID:         "exec-1",
			ProjectID:      "proj-1",
			PendingInputID: "input-chat-1",
		})
		fileBroadcaster.Publish(events.FileChangeEvent{
			Type:      events.DiffSnapshot,
			TaskID:    "task-1",
			ExecID:    "exec-1",
			FilePath:  "main.go",
			Timestamp: time.Now().UnixMilli(),
		})
	}()

	received := map[string]string{}
	currentType := ""
	timeout := time.After(3 * time.Second)

	for len(received) < 5 {
		select {
		case <-timeout:
			t.Fatalf("timeout waiting for multiplexed events, got: %#v", received)
		default:
		}

		if !scanner.Scan() {
			t.Fatal("unexpected end of SSE stream")
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && currentType != "":
			received[currentType] = strings.TrimPrefix(line, "data: ")
		case line == "":
			currentType = ""
		}
	}

	if _, ok := received["task_status_changed"]; !ok {
		t.Fatal("expected task_status_changed event from live stream")
	}
	if _, ok := received["task_thread_execution_started"]; !ok {
		t.Fatal("expected task_thread_execution_started event from live stream")
	}
	if _, ok := received["chat_new_message"]; !ok {
		t.Fatal("expected chat_new_message event from live stream")
	}
	if _, ok := received["chat_thread_input_applied"]; !ok {
		t.Fatal("expected chat_thread_input_applied event from live stream")
	}
	if got, ok := received["diff_snapshot"]; !ok {
		t.Fatal("expected diff_snapshot event from live stream")
	} else if strings.Contains(got, "diff_output") {
		t.Fatalf("diff_snapshot live event should not include diff_output, got %q", got)
	}
}

func TestLiveEventsSSE_AppliesProjectAndTaskFilters(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive SSE test in short mode")
	}
	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()

	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
	}

	e := echo.New()
	e.GET("/events/live", h.LiveEventsSSE)

	srv := httptest.NewServer(e)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events/live?project_id=proj-1&task_id=task-1")
	if err != nil {
		t.Fatalf("GET /events/live with filters: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Scan() // : ping
	scanner.Scan() // empty line

	go func() {
		// Filtered out by project.
		taskBroadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    "task-x",
			ProjectID: "proj-2",
			Status:    "completed",
		})
		// Allowed by project.
		taskBroadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    "task-1",
			ProjectID: "proj-1",
			Status:    "running",
		})

		// Filtered out by project.
		chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ExecID:    "exec-x",
			ProjectID: "proj-2",
			Message:   "ignore me",
		})
		// Allowed by project.
		chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatResponseDone,
			ExecID:    "exec-1",
			ProjectID: "proj-1",
		})

		// Filtered out by task.
		fileBroadcaster.Publish(events.FileChangeEvent{
			Type:      events.DiffSnapshot,
			TaskID:    "task-2",
			ExecID:    "exec-x",
			Timestamp: time.Now().UnixMilli(),
		})
		// Allowed by task.
		fileBroadcaster.Publish(events.FileChangeEvent{
			Type:      events.DiffSnapshot,
			TaskID:    "task-1",
			ExecID:    "exec-1",
			Timestamp: time.Now().UnixMilli(),
		})
	}()

	received := map[string]string{}
	currentType := ""
	timeout := time.After(3 * time.Second)

	for len(received) < 3 {
		select {
		case <-timeout:
			t.Fatalf("timeout waiting for filtered live events, got: %#v", received)
		default:
		}

		if !scanner.Scan() {
			t.Fatal("unexpected end of filtered SSE stream")
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && currentType != "":
			received[currentType] = strings.TrimPrefix(line, "data: ")
		case line == "":
			currentType = ""
		}
	}

	if got := received["task_status_changed"]; !strings.Contains(got, `"project_id":"proj-1"`) {
		t.Fatalf("expected filtered task event for proj-1, got %q", got)
	}
	if got := received["chat_response_done"]; !strings.Contains(got, `"project_id":"proj-1"`) {
		t.Fatalf("expected filtered chat event for proj-1, got %q", got)
	}
	if got := received["diff_snapshot"]; !strings.Contains(got, `"task_id":"task-1"`) {
		t.Fatalf("expected filtered file event for task-1, got %q", got)
	}
}

func waitForLiveSubscriberCount(t *testing.T, name string, count func() int, want int) {
	t.Helper()

	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	for {
		if got := count(); got == want {
			return
		}

		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %s subscriber count to become %d (got %d)", name, want, count())
		}
	}
}

func fillLiveSubscribers[T any, S ~chan T](t *testing.T, subscribe func() (S, error), unsubscribe func(S)) {
	t.Helper()

	subscribers := make([]S, 0, events.MaxSubscribers)
	for i := 0; i < events.MaxSubscribers; i++ {
		sub, err := subscribe()
		if err != nil {
			t.Fatalf("subscribe %d/%d: %v", i+1, events.MaxSubscribers, err)
		}
		subscribers = append(subscribers, sub)
	}
	t.Cleanup(func() {
		for _, sub := range subscribers {
			unsubscribe(sub)
		}
	})

	extra, err := subscribe()
	if err == nil {
		unsubscribe(extra)
	}
	if err != events.ErrMaxSubscribers {
		t.Fatalf("expected ErrMaxSubscribers after %d subscribers, got %v", events.MaxSubscribers, err)
	}
}

func TestLiveEventsSSE_CleansUpAllSubscriptionsOnCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive SSE test in short mode")
	}

	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()
	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
	}

	e := echo.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events/live", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() {
		done <- h.LiveEventsSSE(e.NewContext(req, rec))
	}()

	waitForLiveSubscriberCount(t, "task", taskBroadcaster.SubscriberCount, 1)
	waitForLiveSubscriberCount(t, "chat", chatBroadcaster.SubscriberCount, 1)
	waitForLiveSubscriberCount(t, "file-change", fileBroadcaster.SubscriberCount, 1)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LiveEventsSSE returned an error after request cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for LiveEventsSSE to return after request cancellation")
	}

	waitForLiveSubscriberCount(t, "task", taskBroadcaster.SubscriberCount, 0)
	waitForLiveSubscriberCount(t, "chat", chatBroadcaster.SubscriberCount, 0)
	waitForLiveSubscriberCount(t, "file-change", fileBroadcaster.SubscriberCount, 0)
}

func TestLiveEventsSSE_CleansUpPriorSubscriptionsOnSubscriberLimit(t *testing.T) {
	tests := []struct {
		name                string
		wantTaskSubscribers int
		wantChatSubscribers int
		wantFileSubscribers int
	}{
		{name: "task", wantTaskSubscribers: events.MaxSubscribers},
		{name: "chat", wantChatSubscribers: events.MaxSubscribers},
		{name: "file-change", wantFileSubscribers: events.MaxSubscribers},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskBroadcaster := events.NewBroadcaster()
			chatBroadcaster := events.NewChatBroadcaster()
			fileBroadcaster := events.NewFileChangeBroadcaster()

			switch tt.name {
			case "task":
				fillLiveSubscribers[events.TaskEvent, events.Subscriber](t, taskBroadcaster.Subscribe, taskBroadcaster.Unsubscribe)
			case "chat":
				fillLiveSubscribers[events.ChatEvent, events.ChatSubscriber](t, chatBroadcaster.Subscribe, chatBroadcaster.Unsubscribe)
			case "file-change":
				fillLiveSubscribers[events.FileChangeEvent, events.FileChangeSubscriber](t, fileBroadcaster.Subscribe, fileBroadcaster.Unsubscribe)
			default:
				t.Fatalf("unsupported subscriber-limit stage %q", tt.name)
			}

			h := &Handler{
				broadcaster:           taskBroadcaster,
				chatBroadcaster:       chatBroadcaster,
				fileChangeBroadcaster: fileBroadcaster,
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/events/live", nil)
			rec := httptest.NewRecorder()

			if err := h.LiveEventsSSE(e.NewContext(req, rec)); err != nil {
				t.Fatalf("LiveEventsSSE returned an error: %v", err)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Too many SSE connections") {
				t.Fatalf("expected subscriber-limit response, got %q", rec.Body.String())
			}

			if got := taskBroadcaster.SubscriberCount(); got != tt.wantTaskSubscribers {
				t.Fatalf("expected %d task subscribers after %s limit failure, got %d", tt.wantTaskSubscribers, tt.name, got)
			}
			if got := chatBroadcaster.SubscriberCount(); got != tt.wantChatSubscribers {
				t.Fatalf("expected %d chat subscribers after %s limit failure, got %d", tt.wantChatSubscribers, tt.name, got)
			}
			if got := fileBroadcaster.SubscriberCount(); got != tt.wantFileSubscribers {
				t.Fatalf("expected %d file-change subscribers after %s limit failure, got %d", tt.wantFileSubscribers, tt.name, got)
			}
		})
	}
}
