package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
)

// TaskEventsSSE handles Server-Sent Events for real-time task updates
func (h *Handler) TaskEventsSSE(c echo.Context) error {
	// Subscribe to task events (with limit check)
	sub, err := h.broadcaster.Subscribe()
	if err == events.ErrMaxSubscribers {
		log.Printf("[sse] subscriber limit reached (%d), rejecting connection", events.MaxSubscribers)
		return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
	}
	if err != nil {
		log.Printf("[sse] subscribe error: %v", err)
		return c.String(http.StatusInternalServerError, "SSE subscribe failed")
	}
	defer h.broadcaster.Unsubscribe(sub)

	// Set headers for SSE
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	log.Printf("[sse] client connected, total subscribers: %d", h.broadcaster.SubscriberCount())

	// Send initial ping to establish connection
	if _, err := fmt.Fprint(c.Response(), ": ping\n\n"); err != nil {
		log.Printf("[sse] error sending initial ping: %v", err)
		return err
	}
	c.Response().Flush()

	// Listen for events and client disconnect
	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			log.Printf("[sse] client disconnected, remaining subscribers: %d", h.broadcaster.SubscriberCount())
			return nil

		case event := <-sub:
			// Send event to client
			if _, err := fmt.Fprint(c.Response(), event.ToSSE()); err != nil {
				log.Printf("[sse] error sending event: %v", err)
				return err
			}
			c.Response().Flush()
			log.Printf("[sse] sent event: type=%s task=%s", event.Type, event.TaskID)
		}
	}
}

// ChatLiveSSE handles Server-Sent Events for real-time chat updates.
// Clients filter by project_id query param to receive only relevant chat events.
func (h *Handler) ChatLiveSSE(c echo.Context) error {
	if h.chatBroadcaster == nil {
		return c.String(http.StatusServiceUnavailable, "Chat live updates not available")
	}

	projectID := c.QueryParam("project_id")

	sub, err := h.chatBroadcaster.Subscribe()
	if err == events.ErrMaxSubscribers {
		log.Printf("[sse-chat] subscriber limit reached, rejecting connection")
		return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
	}
	if err != nil {
		log.Printf("[sse-chat] subscribe error: %v", err)
		return c.String(http.StatusInternalServerError, "SSE subscribe failed")
	}
	defer h.chatBroadcaster.Unsubscribe(sub)

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")

	log.Printf("[sse-chat] client connected project=%s, total subscribers: %d", projectID, h.chatBroadcaster.SubscriberCount())

	if _, err := fmt.Fprint(c.Response(), ": ping\n\n"); err != nil {
		return err
	}
	c.Response().Flush()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[sse-chat] client disconnected project=%s", projectID)
			return nil

		case event := <-sub:
			// Filter by project if specified
			if projectID != "" && event.ProjectID != projectID {
				continue
			}

			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", event.Type, string(data)); err != nil {
				log.Printf("[sse-chat] error sending event: %v", err)
				return err
			}
			c.Response().Flush()
			log.Printf("[sse-chat] sent event: type=%s exec=%s project=%s", event.Type, event.ExecID, event.ProjectID)
		}
	}
}

// FileChangesSSE handles Server-Sent Events for real-time file change updates during task execution.
// Clients filter by task_id query param to receive only file changes for that specific task.
func (h *Handler) FileChangesSSE(c echo.Context) error {
	if h.fileChangeBroadcaster == nil {
		return c.String(http.StatusServiceUnavailable, "File change updates not available")
	}

	taskID := c.QueryParam("task_id")
	if taskID == "" {
		return c.String(http.StatusBadRequest, "task_id required")
	}

	sub, err := h.fileChangeBroadcaster.Subscribe()
	if err == events.ErrMaxSubscribers {
		log.Printf("[sse-files] subscriber limit reached, rejecting connection")
		return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
	}
	if err != nil {
		log.Printf("[sse-files] subscribe error: %v", err)
		return c.String(http.StatusInternalServerError, "SSE subscribe failed")
	}
	defer h.fileChangeBroadcaster.Unsubscribe(sub)

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")

	log.Printf("[sse-files] client connected task=%s, total subscribers: %d", taskID, h.fileChangeBroadcaster.SubscriberCount())

	if _, err := fmt.Fprint(c.Response(), ": ping\n\n"); err != nil {
		return err
	}
	c.Response().Flush()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[sse-files] client disconnected task=%s", taskID)
			return nil

		case event := <-sub:
			// Filter by task ID
			if event.TaskID != taskID {
				continue
			}

			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", event.Type, string(data)); err != nil {
				log.Printf("[sse-files] error sending event: %v", err)
				return err
			}
			c.Response().Flush()
			log.Printf("[sse-files] sent event: type=%s task=%s file=%s", event.Type, event.TaskID, event.FilePath)
		}
	}
}
