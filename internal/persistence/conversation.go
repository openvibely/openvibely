package persistence

import (
	"context"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
)

var ErrConversationConflict = errors.New("conversation revision conflict")

// ConversationStore serves provider recovery and public display from the same
// durable journal. Callers must authorize the owner before exposing display data.
type ConversationStore interface {
	ConversationRevision(context.Context, models.ConversationOwner, models.ConversationLane, string, int64) (int64, error)
	AppendConversationChanges(context.Context, models.ConversationOwner, []models.ConversationChange) ([]models.ConversationChange, error)
	ReadConversationChanges(context.Context, models.ConversationOwner, models.ConversationLane, int64, int) ([]models.ConversationChange, error)
	ReadConversationPage(context.Context, models.ConversationOwner, models.ConversationLane, int64, int64, string, int) (models.ConversationPage, error)
	ReadConversationHeads(context.Context, models.ConversationOwner, models.ConversationLane, int64) ([]models.ConversationChange, error)
}
