package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestChatProjectSelection(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	first := createProject(t, h, "First project")
	second := createProject(t, h, "Second project")
	for _, project := range []*models.Project{first, second} {
		task := &models.Task{ProjectID: project.ID, Title: project.Name + " task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Implement it"}
		require.NoError(t, h.taskRepo.Create(ctx, task))
	}
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		t.Run(string(surface), func(t *testing.T) {
			executor := h.chatActionExecutor(streamingResponseParams{ProjectID: first.ID}, nil, models.ChatModeOrchestrate, surface)
			input, err := json.Marshal(map[string]string{"project_id": second.ID})
			require.NoError(t, err)
			out, handled, failed, err := executor(ctx, "list_tasks", input)
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, failed, out)
			require.Contains(t, out, second.Name+" task")
			require.Contains(t, out, `"project_id":"`+second.ID+`"`)
			require.NotContains(t, out, first.Name+" task")
			out, _, failed, err = executor(ctx, "list_tasks", json.RawMessage(`{}`))
			require.NoError(t, err)
			require.False(t, failed, out)
			require.Contains(t, out, first.Name+" task")
			require.NotContains(t, out, second.Name+" task", "project selection must not change subsequent tool defaults")
			out, _, failed, err = executor(ctx, "list_tasks", json.RawMessage(`{"project_id":"missing"}`))
			require.ErrorContains(t, err, "unknown project_id")
			require.True(t, failed, out)
		})
	}
	empty := createProject(t, h, "Empty project")
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: empty.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	out, _, failed, err := executor(ctx, "list_tasks", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, failed, out)
	require.Contains(t, out, `"total":0`)
	input, err := json.Marshal(map[string]string{"project_id": second.ID})
	require.NoError(t, err)
	out, _, failed, err = executor(ctx, "list_tasks", input)
	require.NoError(t, err)
	require.False(t, failed, out)
	require.Contains(t, out, second.Name+" task", "an empty result from one project must not suppress discovery in another")
	projects := h.executeListProjects(ctx, first.ID)
	require.Contains(t, projects, first.ID)
	require.Contains(t, projects, second.ID)
}

func TestChatProjectSelectionCannotEscapePersistedOrChannelScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		params  streamingResponseParams
		surface chatcontrol.Surface
	}{
		{"task", streamingResponseParams{ProjectID: "owner", IsTaskFollowup: true}, chatcontrol.SurfaceWeb},
		{"api task", streamingResponseParams{ProjectID: "owner", IsTaskFollowup: true}, chatcontrol.SurfaceAPI},
		{"slack", streamingResponseParams{ProjectID: "owner"}, chatcontrol.SurfaceSlack},
		{"email", streamingResponseParams{ProjectID: "owner"}, chatcontrol.SurfaceEmail},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &Handler{}
			handlers := h.projectSelectedChatHandlers(test.params, nil, models.ChatModeOrchestrate, test.surface)
			for _, name := range []string{"create_task", "create_swarm_task", "list_tasks", "execute_tasks", "send_message"} {
				_, err := handlers[name](context.Background(), json.RawMessage(`{"project_id":"other"}`))
				require.ErrorContains(t, err, "restricted to its current project", name)
			}
		})
	}
}

func TestDecodeChatProjectSelection(t *testing.T) {
	for _, input := range []string{`{"project_id":null}`, `{"project_id":1}`, `{"project_id":{}}`} {
		_, _, err := decodeChatProjectSelection("list_tasks", json.RawMessage(input))
		require.Error(t, err)
	}
	id, payload, err := decodeChatProjectSelection("list_tasks", json.RawMessage(`{"project_id":" p ","query":"a","unexpected":true}`))
	require.NoError(t, err)
	require.Equal(t, "p", id)
	require.JSONEq(t, `{"query":"a","unexpected":true}`, string(payload), "unknown fields must survive for the handler's strict validation")
	id, payload, err = decodeChatProjectSelection("create_swarm_task", json.RawMessage(`{"project_id":" p ","title":"a"}`))
	require.NoError(t, err)
	require.Equal(t, "p", id)
	require.JSONEq(t, `{"project_id":"p","title":"a"}`, string(payload))
}
