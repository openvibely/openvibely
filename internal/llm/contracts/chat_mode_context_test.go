package contracts

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestChatModeContextRoundTrip(t *testing.T) {
	ctx := WithChatMode(context.Background(), models.ChatModePlan)
	if got := ChatModeFromContext(ctx); got != models.ChatModePlan {
		t.Fatalf("ChatModeFromContext = %q, want %q", got, models.ChatModePlan)
	}
}

func TestChatModeContextDefault(t *testing.T) {
	if got := ChatModeFromContext(context.Background()); got != models.ChatModeOrchestrate {
		t.Fatalf("ChatModeFromContext default = %q, want %q", got, models.ChatModeOrchestrate)
	}
}
