package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

// Exercise the production initial-run builder, including its application handler
// provider, through a real provider request. Compare against the follow-up policy.
func TestInitialTaskRuntimeMatchesFollowupPolicy(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Runtime parity")
	foreignProject := createProject(t, h, "Foreign runtime")
	foreign := createTask(t, h, foreignProject.ID, "Foreign", func(task *models.Task) { task.Category = models.CategoryBacklog })
	recipient := createTask(t, h, project.ID, "Recipient", func(task *models.Task) { task.Status = models.StatusRunning })
	var names []string
	var results []string
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			for _, tool := range request.Tools {
				names = append(names, tool.Function.Name)
			}
			var tools []map[string]any
			for i, call := range []struct{ name, args string }{
				{"list_capabilities", `{}`},
				{"view_task_thread", `{"task_id":"current"}`},
				{"view_swarm", `{"task_id":"current"}`},
				{"send_to_task", fmt.Sprintf(`{"task_id":%q,"message":"must not send"}`, foreign.ID)},
				{"send_to_task", fmt.Sprintf(`{"task_id":%q,"message":"queued from initial task"}`, recipient.ID)},
			} {
				tools = append(tools, map[string]any{"index": i, "id": fmt.Sprintf("call_%d", i), "type": "function", "function": map[string]any{"name": call.name, "arguments": call.args}})
			}
			payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": tools}, "finish_reason": "tool_calls"}}})
			require.NoError(t, err)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
			return
		}
		for _, message := range request.Messages {
			if message.Role == "tool" {
				results = append(results, message.Content)
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	agent := createAgent(t, configs, func(config *models.LLMConfig) {
		config.Provider = models.ProviderOpenAICompatible
		config.Model = "fixture/model"
		config.AuthMethod = models.AuthMethodAPIKey
		config.APIKey = "test-key"
		config.BaseURL = server.URL + "/v1/"
		config.PresetSlug = "vllm"
		config.Transport = "chat_completions"
	})
	task := createTask(t, h, project.ID, "Initial runtime", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.AgentID = &agent.ID
		task.SwarmRole = models.SwarmRoleParent
		task.SwarmStatus = "planning"
	})
	_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, calls)
	for _, name := range names {
		if chatcontrol.Get(name) != nil {
			require.True(t, chatcontrol.TaskToolNames("")[name], name)
		}
	}
	for name := range taskThreadAllowedRuntimeToolNames(nil) {
		require.Contains(t, names, name)
	}
	require.Len(t, results, 5)
	require.Contains(t, results[4], `"ok":true`)
	for name := range chatcontrol.TaskToolNames("") {
		require.Contains(t, results[0], name)
	}
	require.Contains(t, results[1], task.Title)
	require.Contains(t, results[2], task.ID)
	require.NotContains(t, results[3], `"ok":true`)
	for _, name := range []string{"cancel_task", "merge_task", "mark_task_goal_achieved", "report_task_goal_blocked", "request_user_input"} {
		require.NotContains(t, names, name)
		require.NotContains(t, results[0], name)
	}
	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, foreign.ID)
	require.NoError(t, err)
	require.Empty(t, inputs)
	inputs, err = h.threadInputRepo.ListPendingForTask(ctx, recipient.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "queued from initial task", inputs[0].Content)
}

func TestTaskFollowupAppliesSharedAuditorPolicy(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Auditor policy")
	task := createTask(t, h, project.ID, "Auditor", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.CreatedVia = "automation:loop:auditor"
	})
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	rt := h.buildStreamingResponseActionRuntime(ctx, streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	capabilities, handled, isErr, err := rt.Executor(ctx, "list_capabilities", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	for name := range chatcontrol.TaskToolNames("") {
		if chatcontrol.TaskToolNames(task.CreatedVia)[name] {
			continue
		}
		require.False(t, rt.HasDefinition(name), name)
		require.NotContains(t, capabilities, "- "+name+" ")
		_, handled, _, _ := rt.Executor(ctx, name, json.RawMessage(`{}`))
		require.False(t, handled, name)
	}
}
