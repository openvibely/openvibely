package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

// Resolve the audience once when publishing a lifecycle event, never for output chunks.
func (h *Handler) publishChatEvent(event events.ChatEvent) {
	if h.chatBroadcaster == nil {
		return
	}
	if event.ProjectID == models.AllProjectsID && h.projectAccessSvc != nil {
		kind, id := "execution", event.ExecID
		if event.PendingInputID != "" {
			kind, id = "input", event.PendingInputID
		} else if event.Queued {
			kind = "input"
		}
		if id == "" {
			kind, id = "task", event.TaskID
		}
		if id == "" {
			kind, id = "input", event.PendingInputID
		}
		owner, err := h.projectAccessSvc.ChatResourceOwner(context.Background(), kind, id)
		if err != nil {
			return
		}
		event.AudienceUserID = owner
	}
	h.chatBroadcaster.Publish(event)
}
