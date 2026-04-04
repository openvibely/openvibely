package contracts

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

type chatModeContextKey struct{}

// WithChatMode annotates context with /chat mode for provider routing decisions.
func WithChatMode(ctx context.Context, mode models.ChatMode) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, chatModeContextKey{}, mode)
}

// ChatModeFromContext extracts the current chat mode from context.
func ChatModeFromContext(ctx context.Context) models.ChatMode {
	if ctx == nil {
		return models.ChatModeOrchestrate
	}
	if mode, ok := ctx.Value(chatModeContextKey{}).(models.ChatMode); ok && mode != "" {
		return mode
	}
	return models.ChatModeOrchestrate
}
