package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
)

func writeSSEEvent(w http.ResponseWriter, eventType string, payload any) error {
	_, err := writeSSEEventWithBytes(w, eventType, payload)
	return err
}

func writeSSEEventWithBytes(w http.ResponseWriter, eventType string, payload any) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	written := 0
	if eventType != "" {
		n, err := fmt.Fprintf(w, "event: %s\n", eventType)
		written += n
		if err != nil {
			return written, err
		}
	}
	n, err := fmt.Fprintf(w, "data: %s\n\n", string(data))
	written += n
	return written, err
}

func (h *Handler) writeLiveSSEEvent(w http.ResponseWriter, eventType string, payload any) error {
	written, err := writeSSEEventWithBytes(w, eventType, payload)
	if err == nil && h.liveSSEWriteObserver != nil {
		h.liveSSEWriteObserver(written)
	}
	return err
}

// LiveEventsSSE handles a single multiplexed SSE stream for task, chat, and file-change events.
// Optional filters:
//   - project_id: limits task/chat events to one project
//   - task_id: limits file-change events to one task; project-only streams do not
//     subscribe to file changes because they cannot be routed by project.
func (h *Handler) LiveEventsSSE(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	taskID := c.QueryParam("task_id")

	var taskSub events.Subscriber
	var taskCount int
	if h.broadcaster != nil {
		var sub events.Subscriber
		var err error
		if projectID == "" {
			sub, err = h.broadcaster.Subscribe()
		} else {
			sub, err = h.broadcaster.SubscribeProject(projectID)
		}
		if err == events.ErrMaxSubscribers {
			applog.Infof("[sse-live] task subscriber limit reached, rejecting connection")
			return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
		}
		if err != nil {
			applog.Infof("[sse-live] task subscribe error: %v", err)
			return c.String(http.StatusInternalServerError, "SSE subscribe failed")
		}
		taskSub = sub
		taskCount = h.broadcaster.SubscriberCount()
		defer h.broadcaster.Unsubscribe(taskSub)
	}

	var chatSub events.ChatSubscriber
	var chatCount int
	if h.chatBroadcaster != nil {
		var sub events.ChatSubscriber
		var err error
		if projectID == "" {
			sub, err = h.chatBroadcaster.Subscribe()
		} else {
			sub, err = h.chatBroadcaster.SubscribeProject(projectID)
		}
		if err == events.ErrMaxSubscribers {
			applog.Infof("[sse-live] chat subscriber limit reached, rejecting connection")
			return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
		}
		if err != nil {
			applog.Infof("[sse-live] chat subscribe error: %v", err)
			return c.String(http.StatusInternalServerError, "SSE subscribe failed")
		}
		chatSub = sub
		chatCount = h.chatBroadcaster.SubscriberCount()
		defer h.chatBroadcaster.Unsubscribe(chatSub)
	}

	var fileSub events.FileChangeSubscriber
	var fileCount int
	if h.fileChangeBroadcaster != nil && (projectID == "" || taskID != "") {
		var sub events.FileChangeSubscriber
		var err error
		if taskID == "" {
			sub, err = h.fileChangeBroadcaster.Subscribe()
		} else {
			sub, err = h.fileChangeBroadcaster.SubscribeTask(taskID)
		}
		if err == events.ErrMaxSubscribers {
			applog.Infof("[sse-live] file subscriber limit reached, rejecting connection")
			return c.String(http.StatusServiceUnavailable, "Too many SSE connections")
		}
		if err != nil {
			applog.Infof("[sse-live] file subscribe error: %v", err)
			return c.String(http.StatusInternalServerError, "SSE subscribe failed")
		}
		fileSub = sub
		fileCount = h.fileChangeBroadcaster.SubscriberCount()
		defer h.fileChangeBroadcaster.Unsubscribe(fileSub)
	}

	if taskSub == nil && chatSub == nil && fileSub == nil {
		return c.String(http.StatusServiceUnavailable, "Live updates not available")
	}

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")

	applog.Infof(
		"[sse-live] client connected project=%s task=%s subscribers(tasks=%d chat=%d files=%d)",
		projectID,
		taskID,
		taskCount,
		chatCount,
		fileCount,
	)

	if _, err := fmt.Fprint(c.Response(), ": ping\n\n"); err != nil {
		return err
	}
	c.Response().Flush()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			applog.Infof("[sse-live] client disconnected project=%s task=%s", projectID, taskID)
			return nil
		case event := <-taskSub:
			if projectID != "" && event.ProjectID != projectID {
				continue
			}
			if err := h.writeLiveSSEEvent(c.Response(), string(event.Type), event); err != nil {
				applog.Infof("[sse-live] error sending task event: %v", err)
				return err
			}
			c.Response().Flush()
		case event := <-chatSub:
			if projectID != "" && event.ProjectID != projectID {
				continue
			}
			if err := h.writeLiveSSEEvent(c.Response(), string(event.Type), event); err != nil {
				applog.Infof("[sse-live] error sending chat event: %v", err)
				return err
			}
			c.Response().Flush()
		case event := <-fileSub:
			if taskID != "" && event.TaskID != taskID {
				continue
			}
			if err := h.writeLiveSSEEvent(c.Response(), string(event.Type), event); err != nil {
				applog.Infof("[sse-live] error sending file event: %v", err)
				return err
			}
			c.Response().Flush()
		}
	}
}
