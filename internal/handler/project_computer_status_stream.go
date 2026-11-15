package handler

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/events"
)

// Status snapshots have editor-only authorization; never put private desktop
// state on the project's general task notification subscription.
func (h *Handler) streamComputerStatus(ctx context.Context, cmd liveCommand, send func(liveFrame) bool) {
	if h.liveSubscriptionCount.Add(1) > int64(h.liveLimits.WithDefaults().LiveSubscriptions) {
		h.liveSubscriptionCount.Add(-1)
		send(liveFrame{Event: "retry", Data: "live subscription limit reached"})
		return
	}
	defer h.liveSubscriptionCount.Add(-1)
	var updates events.Subscriber
	var missed <-chan struct{}
	if h.broadcaster != nil {
		var err error
		updates, err = h.broadcaster.SubscribeComputer(cmd.ProjectID, cmd.TaskID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.broadcaster.Unsubscribe(updates)
		missed = h.broadcaster.Missed(updates)
	}
	// Subscribe before the initial snapshot so mutations during the read are kept.
	snapshot := func() bool {
		if !h.liveSubscriptionAllowed(ctx, cmd) {
			send(liveFrame{Event: "error", Data: "access blocked"})
			return false
		}
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		svc := h.computerSvc
		if cmd.TaskID != "" {
			var err error
			svc, err = svc.ForTask(readCtx, cmd.ProjectID, cmd.TaskID)
			if err != nil {
				send(liveFrame{Event: "error", Data: "Task not found"})
				return false
			}
		}
		var status map[string]any
		var err error
		if cmd.TaskID == "" && cmd.ViewerID == "" {
			// Tab availability needs configuration, not a running desktop.
			status, err = h.computerAvailability(readCtx, svc, cmd.ProjectID)
		} else {
			status, err = h.computerStatus(readCtx, svc, cmd.ProjectID, cmd.TaskID, cmd.ViewerID, cmd.ViewerMode, cmd.Native)
		}
		if err != nil {
			send(liveFrame{Event: "retry", Data: "Computer status unavailable"})
			return false
		}
		status["project_id"], status["task_id"] = cmd.ProjectID, cmd.TaskID
		status["viewer_mode"] = cmd.ViewerMode
		return send(liveFrame{Event: "computer_status", Data: status})
	}
	if !send(liveFrame{Event: "subscribed"}) || !snapshot() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-missed:
			discardLiveBacklog(updates)
			if !send(liveFrame{Event: "reconcile"}) {
				return
			}
		case ev, ok := <-updates:
			if !ok {
				return
			}
			if ev.Type == events.TaskDeleted && ev.TaskID == cmd.TaskID {
				send(liveFrame{Event: "task_deleted", Data: ev})
				send(liveFrame{Event: "error", Data: "Task deleted"})
				return
			}
			if ev.Type == events.ComputerStatusChanged || ev.Type == events.TaskStatusChanged {
				if !snapshot() {
					return
				}
			}
		}
	}
}
