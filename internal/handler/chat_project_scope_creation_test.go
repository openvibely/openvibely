package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestChatProjectSelectionCreatesOnlyInDestination(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	first := createProject(t, h, "Conversation project")
	second := createProject(t, h, "Destination project")
	model := &models.LLMConfig{Name: "Project selection model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, configs.Create(ctx, model))
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: first.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	input, err := json.Marshal(map[string]string{
		"project_id": second.ID, "title": "Routed task", "prompt": "Implement in the destination", "category": "backlog", "agent_id": model.ID,
	})
	require.NoError(t, err)
	out, handled, failed, err := executor(ctx, "create_task", input)
	require.NoError(t, err, out)
	require.True(t, handled)
	require.False(t, failed, out)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1, out)
	task, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.Equal(t, second.ID, task.ProjectID)
	firstTasks, err := h.taskRepo.ListByProject(ctx, first.ID, "")
	require.NoError(t, err)
	require.Empty(t, firstTasks)

	// Plan mode still blocks mutations even when a destination is explicit.
	plan := h.chatActionExecutor(streamingResponseParams{ProjectID: first.ID}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)
	out, _, failed, err = plan(ctx, "create_task", input)
	require.NoError(t, err)
	require.True(t, failed, out)
	secondTasks, err := h.taskRepo.ListByProject(ctx, second.ID, "")
	require.NoError(t, err)
	require.Len(t, secondTasks, 1)
}
