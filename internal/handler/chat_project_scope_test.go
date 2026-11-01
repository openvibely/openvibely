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
	for _, p := range []*models.Project{first, second} {
		require.NoError(t, h.taskRepo.Create(ctx, &models.Task{ProjectID: p.ID, Title: p.Name + " task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "Implement it"}))
	}
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		t.Run(string(surface), func(t *testing.T) {
			executor := h.chatActionExecutor(streamingResponseParams{ProjectID: first.ID}, nil, models.ChatModeOrchestrate, surface)
			input, _ := json.Marshal(map[string]string{"project_id": second.ID})
			_, _, failed, err := executor(ctx, "list_tasks", input)
			require.ErrorContains(t, err, "restricted to its current project")
			require.True(t, failed)
			for _, id := range []string{"", first.ID} {
				same, _ := json.Marshal(map[string]string{"project_id": id})
				out, _, failed, err := executor(ctx, "list_tasks", same)
				require.NoError(t, err)
				require.False(t, failed, out)
				require.Contains(t, out, first.Name+" task")
				require.NotContains(t, out, second.Name+" task")
			}
			all := h.chatActionExecutor(streamingResponseParams{ProjectID: models.AllProjectsID}, nil, models.ChatModeOrchestrate, surface)
			out, _, failed, err := all(ctx, "list_tasks", input)
			require.NoError(t, err)
			require.False(t, failed, out)
			require.Contains(t, out, second.Name+" task")
			require.NotContains(t, out, first.Name+" task")
			_, _, failed, err = all(ctx, "list_tasks", json.RawMessage(`{"project_id":"missing"}`))
			require.ErrorContains(t, err, "unknown project_id")
			require.True(t, failed)
		})
	}
}

func TestAllProjectsEmptyTaskSearchDoesNotHideOtherProjects(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	empty := createProject(t, h, "Empty")
	populated := createProject(t, h, "Populated")
	require.NoError(t, h.taskRepo.Create(t.Context(), &models.Task{ProjectID: populated.ID, Title: "Visible task", Category: models.CategoryBacklog, Status: models.StatusPending}))
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: models.AllProjectsID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, p := range []*models.Project{empty, populated} {
		input, _ := json.Marshal(map[string]string{"project_id": p.ID})
		out, _, failed, err := executor(t.Context(), "list_tasks", input)
		require.NoError(t, err)
		require.False(t, failed, out)
		if p == populated {
			require.Contains(t, out, "Visible task")
		} else {
			require.Contains(t, out, `"total":0`)
		}
	}
}

func TestChatProjectSelectionCannotEscapePersistedScope(t *testing.T) {
	for _, test := range []struct {
		name    string
		params  streamingResponseParams
		surface chatcontrol.Surface
	}{
		{"project chat", streamingResponseParams{ProjectID: "owner"}, chatcontrol.SurfaceWeb},
		{"api project chat", streamingResponseParams{ProjectID: "owner"}, chatcontrol.SurfaceAPI},
		{"task", streamingResponseParams{ProjectID: "owner", IsTaskFollowup: true}, chatcontrol.SurfaceWeb},
		{"api task", streamingResponseParams{ProjectID: "owner", IsTaskFollowup: true}, chatcontrol.SurfaceAPI},
		{"automation", streamingResponseParams{ProjectID: "owner", AutomationContext: &models.AutomationContext{ProjectID: "owner"}}, chatcontrol.SurfaceWeb},
		{"api automation", streamingResponseParams{ProjectID: "owner", AutomationContext: &models.AutomationContext{ProjectID: "owner"}}, chatcontrol.SurfaceAPI},
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

func TestChatProjectToolDefinitions(t *testing.T) {
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		for _, tc := range []struct {
			name    string
			params  streamingResponseParams
			routing bool
		}{
			{name: "ordinary", params: streamingResponseParams{ProjectID: "default"}},
			{name: "all", params: streamingResponseParams{ProjectID: models.AllProjectsID}, routing: true},
			{name: "followup", params: streamingResponseParams{ProjectID: models.AllProjectsID, IsTaskFollowup: true}},
		} {
			t.Run(string(surface)+"/"+tc.name, func(t *testing.T) {
				defs := chatToolDefsForProject(tc.params, models.ChatModeOrchestrate, surface, true)
				for _, def := range defs {
					require.NotEqual(t, "switch_project", def.Name)
					if def.Name == "create_task" || def.Name == "list_tasks" {
						require.Equal(t, tc.routing, def.Schema.Properties["project_id"] != nil)
					}
					if !tc.routing {
						require.Equal(t, chatcontrol.Get(def.Name).Schema, def.Schema)
					}
				}
			})
		}
	}
}
