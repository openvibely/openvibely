package handler

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
)

func TestLiveEventsSSE_ReceivesTaskChatAndFileEvents(t *testing.T) {
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

	go func() {
		time.Sleep(100 * time.Millisecond)
		taskBroadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    "task-1",
			ProjectID: "proj-1",
			Status:    "running",
		})
		chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ExecID:    "exec-1",
			ProjectID: "proj-1",
			Message:   "hello",
			Source:    "telegram",
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

	for len(received) < 3 {
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
	if _, ok := received["chat_new_message"]; !ok {
		t.Fatal("expected chat_new_message event from live stream")
	}
	if _, ok := received["diff_snapshot"]; !ok {
		t.Fatal("expected diff_snapshot event from live stream")
	}
}

func TestLiveEventsSSE_AppliesProjectAndTaskFilters(t *testing.T) {
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
		time.Sleep(100 * time.Millisecond)
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
