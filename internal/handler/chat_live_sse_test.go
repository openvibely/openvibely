package handler

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
)

func TestChatLiveSSE_NoBroadcaster(t *testing.T) {
	h := &Handler{} // No chatBroadcaster set
	e := echo.New()
	e.GET("/events/chat/live", h.ChatLiveSSE)

	req := httptest.NewRequest(http.MethodGet, "/events/chat/live", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

func TestChatLiveSSE_ReceivesEvents(t *testing.T) {
	cb := events.NewChatBroadcaster()
	h := &Handler{}
	h.SetChatBroadcaster(cb)

	e := echo.New()
	e.GET("/events/chat/live", h.ChatLiveSSE)

	// Use a test server for real HTTP connections (SSE requires streaming)
	srv := httptest.NewServer(e)
	defer srv.Close()

	// Connect to SSE endpoint
	resp, err := http.Get(srv.URL + "/events/chat/live?project_id=proj1")
	if err != nil {
		t.Fatalf("GET /events/chat/live: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type 'text/event-stream', got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)

	// Read initial ping
	if !scanner.Scan() {
		t.Fatal("expected initial ping line")
	}
	if line := scanner.Text(); line != ": ping" {
		t.Errorf("expected ': ping', got %q", line)
	}
	// Read empty line after ping
	scanner.Scan()

	// Publish an event for this project
	cb.Publish(events.ChatEvent{
		Type:      events.ChatNewMessage,
		ProjectID: "proj1",
		ExecID:    "exec1",
		TaskID:    "task1",
		Message:   "Hello from Telegram",
		Source:    "telegram",
		AgentName: "Claude",
	})

	// Read the SSE event
	var eventType, eventData string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event")
		default:
		}

		if !scanner.Scan() {
			t.Fatal("unexpected end of SSE stream")
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		} else if line == "" && eventType != "" {
			break // End of event
		}
	}

	if eventType != "chat_new_message" {
		t.Errorf("expected event type 'chat_new_message', got %q", eventType)
	}

	var parsed events.ChatEvent
	if err := json.Unmarshal([]byte(eventData), &parsed); err != nil {
		t.Fatalf("failed to parse event data: %v", err)
	}
	if parsed.ExecID != "exec1" {
		t.Errorf("expected exec_id 'exec1', got %q", parsed.ExecID)
	}
	if parsed.Message != "Hello from Telegram" {
		t.Errorf("expected message 'Hello from Telegram', got %q", parsed.Message)
	}
	if parsed.Source != "telegram" {
		t.Errorf("expected source 'telegram', got %q", parsed.Source)
	}
}

func TestChatLiveSSE_FiltersProjectID(t *testing.T) {
	cb := events.NewChatBroadcaster()
	h := &Handler{}
	h.SetChatBroadcaster(cb)

	e := echo.New()
	e.GET("/events/chat/live", h.ChatLiveSSE)

	srv := httptest.NewServer(e)
	defer srv.Close()

	// Connect filtering for proj1
	resp, err := http.Get(srv.URL + "/events/chat/live?project_id=proj1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// Skip initial ping
	scanner.Scan()
	scanner.Scan()

	// Publish event for proj2 (should be filtered out)
	cb.Publish(events.ChatEvent{
		Type:      events.ChatNewMessage,
		ProjectID: "proj2",
		ExecID:    "exec-other",
	})

	// Publish event for proj1 (should be received)
	cb.Publish(events.ChatEvent{
		Type:      events.ChatNewMessage,
		ProjectID: "proj1",
		ExecID:    "exec-match",
		Message:   "Matching project",
	})

	// Read events with timeout
	var receivedExecID string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			if receivedExecID == "" {
				t.Fatal("timeout waiting for filtered SSE event")
			}
			return
		default:
		}

		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var parsed events.ChatEvent
			if json.Unmarshal([]byte(data), &parsed) == nil {
				receivedExecID = parsed.ExecID
				break
			}
		}
	}

	if receivedExecID != "exec-match" {
		t.Errorf("expected exec_id 'exec-match' (filtered), got %q", receivedExecID)
	}
}
