package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// writeSSEEvent pure-function tests.

func TestWriteSSEEvent_NoEventType(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeSSEEvent(rec, "", map[string]string{"key": "val"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"key":"val"`)) {
		t.Errorf("expected JSON in body, got: %s", body)
	}
	if bytes.Contains(body, []byte("event:")) {
		t.Errorf("unexpected event: line in body: %s", body)
	}
}

func TestWriteSSEEvent_WithEventType(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeSSEEvent(rec, "task_update", map[string]string{"id": "123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("event: task_update\n")) {
		t.Errorf("expected event line, got: %s", body)
	}
	if !bytes.Contains(body, []byte(`"id":"123"`)) {
		t.Errorf("expected JSON data, got: %s", body)
	}
}

func TestWriteSSEEvent_DataLine(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeSSEEvent(rec, "ping", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty body")
	}
	// Must end with double newline (SSE event terminator)
	if !bytes.HasSuffix(rec.Body.Bytes(), []byte("\n\n")) {
		t.Errorf("SSE event must end with \\n\\n, got: %q", body)
	}
}

// LiveEventsSSE with nil broadcasters returns 503.
func TestLiveEventsSSE_NoBroadcasters(t *testing.T) {
	tc := NewStoreTestContext(t)
	rec := tc.HTTP().Get("/events/live").Execute()
	tc.Assert(rec).StatusCode(http.StatusServiceUnavailable)
}
