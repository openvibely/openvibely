package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupportsChatActionTools(t *testing.T) {
	tests := []struct {
		name  string
		agent models.LLMConfig
		want  bool
	}{
		{
			name: "openai oauth supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodOAuth,
			},
			want: true,
		},
		{
			name: "openai cli does not support runtime action tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodCLI,
			},
			want: false,
		},
		{
			name: "anthropic api key supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderAnthropic,
				AuthMethod: models.AuthMethodAPIKey,
			},
			want: true,
		},
		{
			name: "ollama does not support runtime action tools",
			agent: models.LLMConfig{
				Provider: models.ProviderOllama,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsChatActionTools(tt.agent); got != tt.want {
				t.Fatalf("supportsChatActionTools()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestHandlerSupportsChatActionToolsResolvesMixtureAggregator(t *testing.T) {
	h, _, repo := setupTestHandler(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		provider   models.LLMProvider
		authMethod models.AuthMethod
		want       bool
	}{
		{name: "openai api key", provider: models.ProviderOpenAI, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "openai oauth", provider: models.ProviderOpenAI, authMethod: models.AuthMethodOAuth, want: true},
		{name: "anthropic api key", provider: models.ProviderAnthropic, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "anthropic oauth", provider: models.ProviderAnthropic, authMethod: models.AuthMethodOAuth, want: true},
		{name: "openai compatible api key", provider: models.ProviderOpenAICompatible, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "openai compatible oauth", provider: models.ProviderOpenAICompatible, authMethod: models.AuthMethodOAuth, want: true},
		{name: "openai cli", provider: models.ProviderOpenAI, authMethod: models.AuthMethodCLI, want: false},
		{name: "anthropic cli", provider: models.ProviderAnthropic, authMethod: models.AuthMethodCLI, want: false},
		{name: "ollama", provider: models.ProviderOllama, authMethod: models.AuthMethodAPIKey, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggregator := &models.LLMConfig{Name: "Aggregator " + tt.name, Provider: tt.provider, Model: "model", AuthMethod: tt.authMethod}
			require.NoError(t, repo.Create(ctx, aggregator))
			mixture := models.LLMConfig{
				Provider:          models.ProviderMixture,
				MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
			}
			require.Equal(t, tt.want, h.supportsChatActionTools(ctx, mixture))
		})
	}

	require.False(t, h.supportsChatActionTools(ctx, models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{invalid`}))
	require.False(t, h.supportsChatActionTools(ctx, models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"missing"}}`}))
}

func TestFormatCapabilitiesIncludesSelectedMemoryHandles(t *testing.T) {
	out := formatCapabilities([]chatcontrol.ActionSummary{{Domain: "memory", Name: "memory_view", Description: "Load selected memory.", Access: "read"}}, []string{"usage_analytics.md"})
	if !strings.Contains(out, "Selected memories for this turn") || !strings.Contains(out, "usage_analytics.md") {
		t.Fatalf("expected selected memory handles in capabilities output, got:\n%s", out)
	}
}

func TestListCapabilitiesExecutorIncludesSelectedMemoryHandles(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{IsTaskFollowup: true},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true),
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	ctx := service.WithSelectedMemoryHandles(context.Background(), []string{"chat_memory.md"})
	out, handled, isErr, err := rt.Executor(ctx, "list_capabilities", nil)
	if !handled || isErr || err != nil {
		t.Fatalf("list_capabilities failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, want := range []string{"Selected memories for this turn", "chat_memory.md", "memory_view"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected list_capabilities output to contain %q, got:\n%s", want, out)
		}
	}
}

// TestViewTaskThreadResolvesCurrentTaskID reproduces the incident where an
// audit-only task-thread follow-up turn called view_task_thread with an
// explicit task_id of "current" (or omitted task_id/title entirely) and got
// "task current not found" instead of resolving to the persisted task
// backing the follow-up, matching resolveTaskIDForTool's handling for the
// goal and send_to_task runtime tools.
func TestViewTaskThreadResolvesCurrentTaskID(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "View Task Thread Current")
	task := &models.Task{ProjectID: project.ID, Title: "Audit target task", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "do the work", Priority: 2}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	t.Run("explicit current task_id", func(t *testing.T) {
		out, err := handlers["view_task_thread"](context.Background(), []byte(`{"task_id":"current"}`))
		if err != nil {
			t.Fatalf("view_task_thread with explicit current task_id failed: out=%q err=%v", out, err)
		}
	})

	t.Run("omitted task_id and title", func(t *testing.T) {
		out, err := handlers["view_task_thread"](context.Background(), []byte(`{}`))
		if err != nil {
			t.Fatalf("view_task_thread with omitted task_id/title failed: out=%q err=%v", out, err)
		}
	})

	t.Run("current outside a task-thread follow-up is rejected", func(t *testing.T) {
		nonFollowupHandlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		out, err := nonFollowupHandlers["view_task_thread"](context.Background(), []byte(`{"task_id":"current"}`))
		if err == nil || !strings.Contains(err.Error(), "only valid in a persisted task thread") {
			t.Fatalf("expected current task_id outside a follow-up to be rejected, out=%q err=%v", out, err)
		}
	})
}

func TestWebRuntimeToolsUseSharedInputDecoder(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Shared Web Runtime Decoder")
	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: "shared-web-runtime-decoder", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	ctx := context.Background()

	for _, input := range []json.RawMessage{nil, json.RawMessage(" \n\t ")} {
		out, err := handlers["set_chat_mode"](ctx, input)
		require.NoError(t, err)
		require.Contains(t, out, "Chat mode set to orchestrate")
	}

	for name, handler := range map[string]chatcontrol.RuntimeActionHandler{
		"create_swarm_task": handlers["create_swarm_task"],
		"github_get_issue":  handlers["github_get_issue"],
		"send_to_task":      handlers["send_to_task"],
		"set_chat_mode":     handlers["set_chat_mode"],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler(ctx, json.RawMessage(`{"broken":`))
			require.ErrorContains(t, err, "invalid tool input JSON:")
			require.ErrorContains(t, err, "unexpected end of JSON input")
		})
	}
}

func TestCreateTaskRuntimeToolNormalizesAndDecodesInput(t *testing.T) {
	tests := []struct {
		name      string
		input     json.RawMessage
		wantError string
	}{
		{name: "empty", input: nil, wantError: "create_task requires title and prompt"},
		{name: "whitespace", input: json.RawMessage(" \n\t "), wantError: "create_task requires title and prompt"},
		{name: "valid", input: json.RawMessage(`{"title":"Decoded task","prompt":"Create this task","category":"backlog"}`)},
		{name: "malformed", input: json.RawMessage(`{"title":`), wantError: "invalid tool input JSON:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _, _ := setupTestHandlerWithDB(t)
			project := createProject(t, h, "Runtime input "+tt.name)
			handler := h.chatActionHandlers(
				streamingResponseParams{ExecID: "runtime-input-exec", ProjectID: project.ID},
				nil,
				models.ChatModeOrchestrate,
				chatcontrol.SurfaceWeb,
			)["create_task"]

			out, err := handler(context.Background(), tt.input)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				if tt.name == "malformed" {
					require.ErrorContains(t, err, "unexpected end of JSON input")
				}
				return
			}
			require.NoError(t, err)
			require.Contains(t, out, "Decoded task")
		})
	}
}

func TestCreateTaskRuntimeToolDecodesTypedChainConfigDirectly(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Typed Create Task Project")
	handler := h.chatActionHandlers(
		streamingResponseParams{ExecID: "typed-create-exec", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)["create_task"]

	out, err := handler(ctx, json.RawMessage(`{
		"title":"Typed chained task",
		"prompt":"Create the parent directly",
		"category":"backlog",
		"chain":{
			"enabled":true,
			"trigger":"on_completion",
			"child_title":"Typed child",
			"child_prompt_prefix":"Continue from the parent",
			"child_category":"active"
		}
	}`))
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1)

	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	chain, err := created.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, chain.Enabled)
	require.Equal(t, "on_completion", chain.Trigger)
	require.Equal(t, "Typed child", chain.ChildTitle)
	require.Equal(t, "Continue from the parent", chain.ChildPromptPrefix)
	require.Equal(t, "active", chain.ChildCategory)
}

func TestScheduleTaskRuntimeToolExecutesTypedRequest(t *testing.T) {
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		t.Run(string(surface), func(t *testing.T) {
			h, _, _, _ := setupTestHandlerWithDB(t)
			ctx := context.Background()
			project := createProject(t, h, "Typed Schedule Project "+string(surface))
			foreignProject := createProject(t, h, "Foreign Schedule Project "+string(surface))
			task := createTask(t, h, project.ID, "Schedule directly")
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted))
			task.Status = models.StatusCompleted
			foreignTask := createTask(t, h, foreignProject.ID, "Foreign schedule")
			handlers := h.chatActionHandlers(
				streamingResponseParams{ExecID: "typed-schedule-exec", ProjectID: project.ID},
				nil,
				models.ChatModeOrchestrate,
				surface,
			)

			out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"`+task.ID+`","time":"09:30","repeat":"weekly","days":["wed"],"interval":2,"clear_context_on_start":false}`))
			require.NoError(t, err)
			require.Contains(t, out, "Scheduled task")

			schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Len(t, schedules, 1)
			require.Equal(t, models.RepeatWeekly, schedules[0].RepeatType)
			require.Equal(t, 2, schedules[0].RepeatInterval)
			require.Equal(t, time.Wednesday, schedules[0].RunAt.Local().Weekday())
			require.False(t, schedules[0].ClearContextOnStart)
			require.NotNil(t, schedules[0].NextRun)
			require.WithinDuration(t, schedules[0].RunAt, *schedules[0].NextRun, time.Second)
			updated, err := h.taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryScheduled, updated.Category)
			require.Equal(t, models.StatusPending, updated.Status)

			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":0}`))
			require.NoError(t, err)
			require.Contains(t, out, "between 1 and 365")
			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":365,"days":["fri"],"enabled":false,"clear_context_on_start":true}`))
			require.NoError(t, err)
			require.Contains(t, out, "Updated schedule")
			modified, err := h.scheduleRepo.GetByID(ctx, schedules[0].ID)
			require.NoError(t, err)
			require.Equal(t, 365, modified.RepeatInterval)
			require.Equal(t, time.Friday, modified.RunAt.Local().Weekday())
			require.False(t, modified.Enabled)
			require.True(t, modified.ClearContextOnStart)
			require.NotNil(t, modified.NextRun)
			require.WithinDuration(t, modified.RunAt, *modified.NextRun, time.Second)

			out, err = handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"`+foreignTask.ID+`","time":"09:30"}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")
			foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
			require.NoError(t, h.scheduleRepo.Create(ctx, foreignSchedule))
			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+foreignSchedule.ID+`","enabled":false}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")
			out, err = handlers["delete_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+foreignSchedule.ID+`"}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")

			out, err = handlers["delete_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`"}`))
			require.NoError(t, err)
			require.Contains(t, out, "Deleted schedule")
			remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, remaining)
			updated, err = h.taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, updated.Category)
		})
	}
}

func TestScheduleRuntimeToolsResolveCurrentTaskInTaskThread(t *testing.T) {
	ctx := context.Background()

	t.Run("schedule omitted task reference", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Schedule current task omitted")
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"time":"09:30"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Scheduled task")

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.Equal(t, task.ID, schedules[0].TaskID)
	})

	t.Run("schedule explicit current task_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Explicit Project")
		task := createTask(t, h, project.ID, "Schedule current task explicit")
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"current","time":"09:30"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Scheduled task")

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.Equal(t, task.ID, schedules[0].TaskID)
	})

	t.Run("modify explicit current task_id without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Modify Schedule Project")
		task := createTask(t, h, project.ID, "Modify current task schedule")
		schedule := createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"task_id":"current","enabled":false}`))
		require.NoError(t, err)
		require.Contains(t, out, "Updated schedule")

		modified, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
		require.NoError(t, err)
		require.False(t, modified.Enabled)
	})

	t.Run("modify omitted task reference without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Modify Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Modify current task schedule omitted")
		schedule := createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"enabled":false}`))
		require.NoError(t, err)
		require.Contains(t, out, "Updated schedule")

		modified, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
		require.NoError(t, err)
		require.False(t, modified.Enabled)
	})

	t.Run("delete explicit current task_id without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Delete Schedule Project")
		task := createTask(t, h, project.ID, "Delete current task schedule")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["delete_schedule"](ctx, json.RawMessage(`{"task_id":"current"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Deleted schedule")

		remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Empty(t, remaining)
	})

	t.Run("delete omitted task reference without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Delete Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Delete current task schedule omitted")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["delete_schedule"](ctx, json.RawMessage(`{}`))
		require.NoError(t, err)
		require.Contains(t, out, "Deleted schedule")

		remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Empty(t, remaining)
	})

	t.Run("current outside task follow-up is rejected", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Outside Project")
		task := createTask(t, h, project.ID, "Outside current task schedule")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		for name, input := range map[string]json.RawMessage{
			"schedule_task":   json.RawMessage(`{"task_id":"current","time":"09:30"}`),
			"modify_schedule": json.RawMessage(`{"task_id":"current","enabled":false}`),
			"delete_schedule": json.RawMessage(`{"task_id":"current"}`),
		} {
			out, err := handlers[name](ctx, input)
			require.ErrorContains(t, err, "only valid in a persisted task thread", "tool=%s out=%q", name, out)
		}

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.True(t, schedules[0].Enabled)
	})
}

func TestChatActionSummaryCollector_AppendsCreatedAndEdited(t *testing.T) {
	collector := newChatActionSummaryCollector()
	collector.addCreated("\n---\nCreated 1 task(s):\n- \"Fix login\" (active) [TASK_ID:abc123]")
	collector.addEdited("\n---\nEdited 1 task(s):\n- \"Fix login\" (backlog, updated: category) [TASK_EDITED:abc123]")

	out := collector.appendToOutput("Done.")
	if !strings.Contains(out, "Created 1 task(s):") {
		t.Fatalf("expected created summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_ID:abc123]") {
		t.Fatalf("expected task id marker, got %q", out)
	}
	if !strings.Contains(out, "Edited 1 task(s):") {
		t.Fatalf("expected edited summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_EDITED:abc123]") {
		t.Fatalf("expected task edited marker, got %q", out)
	}
}

func TestChatActionHandlers_CoverageWebAndAPI(t *testing.T) {
	h := &Handler{}
	params := streamingResponseParams{ExecID: "e", ProjectID: "p"}

	webHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true, webHandlers); err != nil {
		t.Fatalf("web handler coverage mismatch: %v", err)
	}

	apiHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceAPI, true, apiHandlers); err != nil {
		t.Fatalf("api handler coverage mismatch: %v", err)
	}
}

func TestListTasksRuntimeTool_WebAndAPIExecutableAndPlanExposed(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "List Tasks Handler Project")
	target := createTask(t, h, project.ID, "Implement issue 25")
	createTask(t, h, project.ID, "Unrelated handler task")

	// Canonical registry exposes list_tasks read-only in both Plan and Orchestrate.
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
			defs := chatcontrol.ToolDefsForContext(mode, surface, false)
			found := false
			for _, def := range defs {
				if def.Name == "list_tasks" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected list_tasks definition for mode=%s surface=%s", mode, surface)
			}
		}
	}

	params := streamingResponseParams{ExecID: "exec-list-tasks", ProjectID: project.ID}
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, surface)
		handler := handlers["list_tasks"]
		if handler == nil {
			t.Fatalf("list_tasks handler missing on surface=%s", surface)
		}
		out, err := handler(context.Background(), json.RawMessage(`{"query":"issue 25"}`))
		if err != nil {
			t.Fatalf("list_tasks handler failed on surface=%s: %v", surface, err)
		}
		if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, target.ID) {
			t.Fatalf("expected list_tasks to return target id on surface=%s, got %s", surface, out)
		}
	}

	// Plan-mode handler map still wires an executable handler (never handler_missing).
	planHandlers := h.chatActionHandlers(params, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)
	if planHandlers["list_tasks"] == nil {
		t.Fatal("expected list_tasks handler in plan mode")
	}
}

func TestCreateSwarmTaskRuntimeTool_StartFlagDoesNotDeferActiveSwarm(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Deferred Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm-deferred", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}
	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Plan export","prompt":"Plan export with workers","max_workers":3,"worker_isolation":"worktree","start_immediately":false}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	if !strings.Contains(out, "Planner starts when the swarm parent is Active.") {
		t.Fatalf("expected category-driven planner summary, got %q", out)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), ids[0], models.SwarmRolePlanner)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if planner == nil {
		t.Fatal("expected planner child for active runtime tool swarm")
	}
	if planner.Category != models.CategoryActive || planner.Status != models.StatusPending {
		t.Fatalf("planner not runnable: category=%s status=%s", planner.Category, planner.Status)
	}
}

func TestCreateSwarmTaskRuntimeTool_BacklogCategoryDefersPlanner(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Backlog Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm-backlog", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}

	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Backlog plan","prompt":"Plan later","category":"backlog","max_workers":2}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	if !strings.Contains(out, `(backlog)`) {
		t.Fatalf("expected backlog summary, got %q", out)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.Category != models.CategoryBacklog {
		t.Fatalf("parent category=%s, want backlog", parent.Category)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), ids[0], models.SwarmRolePlanner)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if planner != nil {
		t.Fatalf("expected backlog runtime swarm to defer planner, got %#v", planner)
	}
}

func TestCreateSwarmTaskRuntimeTool_ChannelSurfaceUsesActiveProject(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	authorizedProject := createProject(t, h, "Email Authorized Swarm Project")
	foreignProject := createProject(t, h, "Email Foreign Swarm Project")

	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-email-swarm", ProjectID: authorizedProject.ID, Surface: chatcontrol.SurfaceEmail}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceEmail)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}

	payload := `{"title":"Email swarm","prompt":"Split this email work","project_id":"` + foreignProject.ID + `","start_immediately":false}`
	out, err := createHandler(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.ProjectID != authorizedProject.ID {
		t.Fatalf("channel swarm should use active project %s, got %s", authorizedProject.ID, parent.ProjectID)
	}
	foreignTasks, err := h.taskRepo.ListByProject(context.Background(), foreignProject.ID, "")
	if err != nil {
		t.Fatalf("list foreign tasks: %v", err)
	}
	if len(foreignTasks) != 0 {
		t.Fatalf("expected no tasks in foreign project, got %d", len(foreignTasks))
	}
}

func TestCreateSwarmTaskRuntimeTool_CreatesParentAndPlanner(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}
	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Build export","prompt":"Build export with workers","max_workers":3,"worker_isolation":"worktree","start_immediately":true}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.SwarmRole != models.SwarmRoleParent {
		t.Fatalf("expected swarm parent role, got %q", parent.SwarmRole)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner child not created: %v", err)
	}
}

func TestCreateTaskRuntimeTool_OmittedCategoryAutoStartActivatesAfterAttachmentConversion(t *testing.T) {
	h, _, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Runtime Attachment Deferral Project")
	modelConfig := createAgent(t, llmConfigRepo, func(c *models.LLMConfig) {
		c.AutoStartTasks = true
	})
	chatHostTask := createTask(t, h, project.ID, "runtime attachment host", func(tk *models.Task) {
		tk.Category = models.CategoryChat
	})
	exec := createExec(t, h, chatHostTask.ID, modelConfig.ID)

	fileName := "Screenshot 2026-07-10 at 9.49.00\u202fPM.png"
	sourceDir := filepath.Join(uploadsDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	sourcePath := filepath.Join(sourceDir, fileName)
	require.NoError(t, os.WriteFile(sourcePath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644))
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    fileName,
		FilePath:    sourcePath,
		MediaType:   "image/png",
		FileSize:    4,
	}))

	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: exec.ID, ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	output, err := handlers["create_task"](ctx, json.RawMessage(`{"title":"Auto-start runtime task","prompt":"Use the screenshot"}`))
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(output)
	require.Len(t, ids, 1)

	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, models.CategoryActive, created.Category)
	attachments, err := h.attachmentRepo.ListByTask(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.Equal(t, fileName, attachments[0].FileName)
	require.FileExists(t, attachments[0].FilePath)
}

// TestCreateTaskRuntimeTool_FailsLoudlyOnPersistenceFailure is the regression
// test for the phantom create_task bug: the runtime tool handler used to
// always return (summary, nil), so even if the direct task creation action failed to
// persist any task (empty project context, malformed input, or DB error) the
// model would receive isError=false and report a fake successful create_task
// to the user. The fix returns an error when no [TASK_ID:...] markers appear
// in the summary or when a referenced task ID cannot be verified in the
// current project's task store.
func TestCreateTaskRuntimeTool_FailsLoudlyOnPersistenceFailure(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Create Task Failure Project")
	input := json.RawMessage(`{"title":"Fix bug","prompt":"do it"}`)

	t.Run("empty project id", func(t *testing.T) {
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-empty-project", ProjectID: ""}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		createHandler := handlers["create_task"]
		if createHandler == nil {
			t.Fatal("create_task handler missing")
		}
		if _, err := createHandler(ctx, input); err == nil {
			t.Fatal("expected create_task with empty project_id to return an error")
		}
	})

	t.Run("system Memory Curator assignment", func(t *testing.T) {
		agentRepo := repository.NewAgentRepo(db)
		h.SetAgentRepo(agentRepo)
		memoryCurator := &models.Agent{
			Name:       "System: Memory Curator",
			Key:        models.AgentSystemKindMemoryCurator,
			SystemKind: models.AgentSystemKindMemoryCurator,
			Enabled:    true,
			// System identity must win even if persisted selectability drifts.
			SelectableAsPrimary: true,
			GeneratedStatus:     models.AgentStatusProtected,
		}
		if err := agentRepo.Create(ctx, memoryCurator); err != nil {
			t.Fatalf("create Memory Curator: %v", err)
		}
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-memory-curator", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		output, err := handlers["create_task"](ctx, json.RawMessage(`{"title":"Reconcile memory","prompt":"Update managed memory","agent":"Memory Curator"}`))
		if err == nil {
			t.Fatalf("expected create_task to reject system Memory Curator, got output %q", output)
		}
		if !strings.Contains(output, `Agent "Memory Curator" is not one unique enabled, selectable primary Agent definition`) {
			t.Fatalf("expected rejected Agent assignment in tool output, got %q", output)
		}
		tasks, listErr := h.taskRepo.ListByProject(ctx, project.ID, "")
		if listErr != nil {
			t.Fatalf("list project tasks: %v", listErr)
		}
		if len(tasks) != 0 {
			t.Fatalf("rejected Memory Curator assignment created fallback tasks: %#v", tasks)
		}
	})

	t.Run("summary without persisted task id", func(t *testing.T) {
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-db-failure", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		createHandler := handlers["create_task"]
		if createHandler == nil {
			t.Fatal("create_task handler missing")
		}

		if _, err := h.taskRepo.GetByID(ctx, "sanity-check"); err != nil {
			t.Fatalf("task repo should work before closing db: %v", err)
		}
		h.execRepo = nil // Avoid best-effort execution-output updates after the DB is closed.
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}

		output, err := createHandler(ctx, input)
		if err == nil {
			t.Fatalf("expected create_task to fail when persistence fails, got nil error and output %q", output)
		}
		if !strings.Contains(output, "Failed to create") {
			t.Fatalf("expected failure summary in tool output, got %q", output)
		}
	})
}

// TestWebAPISwitchProject_IsInformationalOnly is the non-regression guard ensuring that
// the web/API switch_project tool never writes to any channel-specific persistence
// table (discord_user_projects, slack_user_projects, telegram_user_projects,
// email_sender_projects). The web/API path is informational: the frontend manages
// the active project_id, so the handler must not touch channel tables.
func TestWebAPISwitchProject_IsInformationalOnly(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()

	project1 := createProject(t, h, "Alpha")
	project2 := createProject(t, h, "Beta")
	_ = project1

	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: "e1", ProjectID: project2.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)

	switchHandler, ok := handlers["switch_project"]
	if !ok {
		t.Fatal("switch_project handler missing from web surface handlers")
	}

	result, err := switchHandler(ctx, json.RawMessage(`{"project":"Alpha"}`))
	if err != nil {
		t.Fatalf("switch_project returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "Alpha") {
		t.Fatalf("expected informational response mentioning Alpha, got: %q", result)
	}

	// Assert no channel-specific persistence rows were written.
	channelTables := []string{
		"discord_user_projects",
		"slack_user_projects",
		"telegram_user_projects",
		"email_sender_projects",
	}
	for _, table := range channelTables {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count(%s): %v", table, err)
		}
		if count != 0 {
			t.Errorf("web/API switch_project must not write to %s: found %d row(s)", table, count)
		}
	}
}

func TestGitHubOpenPullRequestRuntimeToolCreatesAndPersistsPR(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR Runtime", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Open PR", Prompt: "prompt", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/runtime-open-pr", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	var createdReq service.GitHubCreatePullRequestRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) error {
			if publishReq.Branch != task.WorktreeBranch {
				t.Fatalf("unexpected published branch %q", publishReq.Branch)
			}
			return nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			createdReq = req
			return &service.GitHubPullRequest{Number: 123, URL: "https://github.com/openvibely/openvibely/pull/123", State: "open"}, nil
		},
	})
	params := streamingResponseParams{ProjectID: project.ID}
	out, err := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_open_pull_request"](ctx, json.RawMessage(`{"task_id":"`+task.ID+`","pr_title":"Runtime PR","pr_body":"Runtime body","base":"develop","draft":true,"issue_number":77,"issue_url":"https://github.com/openvibely/openvibely/issues/77"}`))
	if err != nil {
		t.Fatalf("github_open_pull_request returned error: %v", err)
	}
	if !strings.Contains(out, `"created":true`) || !strings.Contains(out, `"pull_request"`) {
		t.Fatalf("expected created PR output, got %s", out)
	}
	if createdReq.Title != "Runtime PR" || createdReq.Body != "Runtime body" || createdReq.Base != "develop" || !createdReq.Draft || createdReq.Head != task.WorktreeBranch {
		t.Fatalf("unexpected create PR request: %#v", createdReq)
	}
	record, err := h.taskPullRequestRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("lookup task PR: %v", err)
	}
	if record == nil || record.PRNumber != 123 || record.IssueNumber == nil || *record.IssueNumber != 77 {
		t.Fatalf("unexpected persisted PR record: %#v", record)
	}

	titleOut, err := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_open_pull_request"](ctx, json.RawMessage(`{"title":"`+task.Title+`"}`))
	if err != nil {
		t.Fatalf("github_open_pull_request by title returned error: %v", err)
	}
	if !strings.Contains(titleOut, `"task_id":"`+task.ID+`"`) || !strings.Contains(titleOut, `"reused_existing_record":true`) {
		t.Fatalf("expected title selector to reuse task PR %s, got %s", task.ID, titleOut)
	}
}

func TestGitHubReplacePullRequestBranchRuntimeToolUsesLeaseGuard(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR Replacement", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Replace PR branch", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/runtime-replace-pr"}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	if err := h.taskPullRequestRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}

	var got service.GitHubReplaceBranchHeadRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, _, _ string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		getPullRequestFn: func(_ context.Context, _ *service.GitHubRepoRef, number int) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: number, HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely"}, nil
		},
		replaceBranchHeadFn: func(_ context.Context, _ *service.GitHubRepoRef, req service.GitHubReplaceBranchHeadRequest) error {
			got = req
			return nil
		},
	})
	expected := strings.Repeat("a", 40)
	params := streamingResponseParams{ProjectID: project.ID}
	handler := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_replace_pull_request_branch"]
	if _, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`","expected_head_sha":"`+expected+`"}`)); err == nil || !strings.Contains(err.Error(), "confirm_history_rewrite") {
		t.Fatalf("expected explicit history rewrite confirmation error, got %v", err)
	}
	out, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`","expected_head_sha":"`+expected+`","confirm_history_rewrite":true}`))
	if err != nil {
		t.Fatalf("github_replace_pull_request_branch returned error: %v", err)
	}
	if got.WorktreePath != task.WorktreePath || got.Branch != task.WorktreeBranch || got.ExpectedHead != expected {
		t.Fatalf("unexpected replacement request: %#v", got)
	}
	if !strings.Contains(out, `"replaced_branch":"`+task.WorktreeBranch+`"`) || !strings.Contains(out, `"expected_head_sha":"`+expected+`"`) {
		t.Fatalf("unexpected tool output: %s", out)
	}

	titleOut, err := handler(ctx, json.RawMessage(`{"title":"`+task.Title+`","expected_head_sha":"`+expected+`","confirm_history_rewrite":true}`))
	if err != nil {
		t.Fatalf("github_replace_pull_request_branch by title returned error: %v", err)
	}
	if !strings.Contains(titleOut, `"task_id":"`+task.ID+`"`) || !strings.Contains(titleOut, `"replaced_branch":"`+task.WorktreeBranch+`"`) {
		t.Fatalf("expected title selector to replace task PR branch %s, got %s", task.ID, titleOut)
	}
}

func TestGitHubPRRuntimeToolsShareTargetRejections(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR target", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create current project: %v", err)
	}
	otherProject := &models.Project{Name: "Other GitHub PR target", RepoURL: "https://github.com/example/other"}
	if err := h.projectSvc.Create(ctx, otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherTask := &models.Task{ProjectID: otherProject.ID, Title: "Cross-project PR task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := h.taskRepo.Create(ctx, otherTask); err != nil {
		t.Fatalf("create cross-project task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	params := streamingResponseParams{ProjectID: project.ID}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, action := range []struct {
		name  string
		input func(taskID string) json.RawMessage
	}{
		{name: "github_open_pull_request", input: func(taskID string) json.RawMessage {
			return json.RawMessage(`{"task_id":"` + taskID + `"}`)
		}},
		{name: "github_replace_pull_request_branch", input: func(taskID string) json.RawMessage {
			return json.RawMessage(`{"task_id":"` + taskID + `","confirm_history_rewrite":true}`)
		}},
	} {
		t.Run(action.name+"/missing_task", func(t *testing.T) {
			_, err := handlers[action.name](ctx, action.input("missing-task"))
			if err == nil || err.Error() != "task missing-task not found" {
				t.Fatalf("expected shared missing-task rejection, got %v", err)
			}
		})
		t.Run(action.name+"/cross_project_task", func(t *testing.T) {
			_, err := handlers[action.name](ctx, action.input(otherTask.ID))
			want := "task " + otherTask.ID + " belongs to a different project"
			if err == nil || err.Error() != want {
				t.Fatalf("expected shared cross-project rejection %q, got %v", want, err)
			}
		})
	}

	automationProject := &models.Project{Name: "Automation PR target"}
	if err := h.projectSvc.Create(ctx, automationProject); err != nil {
		t.Fatalf("create Automation project: %v", err)
	}
	automationTask := &models.Task{ProjectID: automationProject.ID, Title: "Automation PR task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := h.taskRepo.Create(ctx, automationTask); err != nil {
		t.Fatalf("create Automation task: %v", err)
	}
	automationCtx := service.WithAutomationContext(ctx, models.AutomationContext{ProjectID: automationProject.ID, OriginTask: true})
	automationHandlers := h.chatActionHandlers(streamingResponseParams{ProjectID: automationProject.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, action := range []struct {
		name  string
		input json.RawMessage
	}{
		{name: "github_open_pull_request", input: json.RawMessage(`{"task_id":"` + automationTask.ID + `"}`)},
		{name: "github_replace_pull_request_branch", input: json.RawMessage(`{"task_id":"` + automationTask.ID + `","confirm_history_rewrite":true}`)},
	} {
		t.Run(action.name+"/automation_repository_required", func(t *testing.T) {
			_, err := automationHandlers[action.name](automationCtx, action.input)
			if err == nil || err.Error() != "Automation GitHub runtime requires a project repository URL or local Git checkout" {
				t.Fatalf("expected shared Automation repository rejection, got %v", err)
			}
		})
	}

	_, err := handlers["github_replace_pull_request_branch"](ctx, json.RawMessage(`{"task_id":"missing-task"}`))
	if err == nil || err.Error() != "confirm_history_rewrite must be true to replace pull request branch history" {
		t.Fatalf("expected confirmation rejection before target resolution, got %v", err)
	}
}

func TestInteractiveGitHubRuntimeExplicitRepositoryUsesProjectEndpointByParsedHost(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	const enterpriseEndpoint = "https://github.example.com/api/v3"
	project := &models.Project{
		Name:    "Enterprise Chat runtime",
		RepoURL: "https://github.example.com/acme/widgets.git",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	providerCalls := 0
	h.SetGitHubService(&fakeGitHubService{
		globalAPIEndpoint: enterpriseEndpoint,
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			if strings.TrimSpace(repoPath) != "" {
				t.Fatalf("explicit repo_url unexpectedly used local path %q", repoPath)
			}
			parsed, err := service.ParseGitHubRepoURL(repoURL)
			if err != nil {
				return nil, err
			}
			return &parsed, nil
		},
		createIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			providerCalls++
			if repo.FullName != "acme/sibling" || repo.APIBaseURL != enterpriseEndpoint {
				t.Fatalf("unexpected resolved repository: %+v", repo)
			}
			return &service.GitHubIssue{Number: 1, Title: req.Title}, nil
		},
	})
	handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	if _, err := handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Sibling issue","repo_url":"git@github.example.com:acme/sibling.git"}`)); err != nil {
		t.Fatalf("same-host Enterprise repository failed: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls)
	}

	if _, err := handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Unsafe issue","repo_url":"https://github.com/acme/widgets.git"}`)); err == nil || !strings.Contains(err.Error(), "repository host") {
		t.Fatalf("expected cross-host repository rejection, got %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("cross-host request reached provider; calls = %d", providerCalls)
	}
}

func TestGitHubAuthAndInboxRuntimeToolsUseConfiguredRepository(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub Inbox", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	authRepo := repository.NewGitHubAuthRepo(db)
	h.SetGitHubAuthRepo(authRepo)
	if err := authRepo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "Dev-Bot", Enabled: true}); err != nil {
		t.Fatalf("configure github inbox: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Alice", Permission: "approve"}); err != nil {
		t.Fatalf("configure authorized actor: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Dev-Bot", Permission: "triage"}); err != nil {
		t.Fatalf("configure dev-bot authorized actor: %v", err)
	}
	var sawMyAssignedIssues bool
	var createdRepo, commentedRepo, labeledRepo string
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			switch repoURL {
			case project.RepoURL:
				return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
			case "https://github.com/example/other":
				if strings.TrimSpace(repoPath) != "" {
					t.Fatalf("expected explicit repo_url lookup to avoid local repo path, got %q", repoPath)
				}
				return &service.GitHubRepoRef{Owner: "example", Name: "other", FullName: "example/other", HTMLURL: "https://github.com/example/other"}, nil
			default:
				t.Fatalf("unexpected repo URL %q", repoURL)
				return nil, nil
			}
		},
		listMyAssignedIssuesFn: func(_ context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error) {
			sawMyAssignedIssues = true
			if repo.Owner == "example" && repo.Name == "other" {
				return &service.GitHubAuthenticatedUser{Login: "channel-user", Source: service.GitHubAuthModePAT}, []service.GitHubIssue{{Number: 7, URL: "https://github.com/example/other/issues/7", Title: "Explicit URL", State: "open", Assignees: []string{"channel-user"}}}, nil
			}
			return &service.GitHubAuthenticatedUser{Login: "channel-user", Source: service.GitHubAuthModePAT}, []service.GitHubIssue{{Number: 5, URL: "https://github.com/openvibely/openvibely/issues/5", Title: "Testing", State: "open", Assignees: []string{"channel-user"}}}, nil
		},
		listAssignedIssuesFn: func(_ context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssue, error) {
			if assignee != "Dev-Bot" {
				t.Fatalf("expected explicit assignee Dev-Bot, got %q", assignee)
			}
			if repo.Owner == "example" && repo.Name == "other" {
				return []service.GitHubIssue{{Number: 8, URL: "https://github.com/example/other/issues/8", Title: "Explicit assignee URL", State: "open", Assignees: []string{"dev-bot"}}}, nil
			}
			return []service.GitHubIssue{{Number: 6, URL: "https://github.com/openvibely/openvibely/issues/6", Title: "Override", State: "open", Assignees: []string{"dev-bot"}}}, nil
		},
		createIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			createdRepo = repo.FullName
			return &service.GitHubIssue{Number: 9, URL: "https://github.com/example/other/issues/9", Title: req.Title, State: "open", Labels: req.Labels, Assignees: req.Assignees}, nil
		},
		commentOnIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, issueNumber int, body string) error {
			commentedRepo = repo.FullName
			if issueNumber != 9 || body != "Looks good" {
				t.Fatalf("unexpected comment input issue=%d body=%q", issueNumber, body)
			}
			return nil
		},
		addLabelsToIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, issueNumber int, labels []string) error {
			labeledRepo = repo.FullName
			if issueNumber != 9 || len(labels) != 1 || labels[0] != "approved" {
				t.Fatalf("unexpected labels input issue=%d labels=%v", issueNumber, labels)
			}
			return nil
		},
	})

	params := streamingResponseParams{ProjectID: project.ID}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	out, err := handlers["github_get_project_inbox"](ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("github_get_project_inbox returned error: %v", err)
	}
	if !strings.Contains(out, `"configured":true`) || !strings.Contains(out, `"alice"`) || !strings.Contains(out, `"dev-bot"`) || !strings.Contains(out, `"legacy_inbox"`) || !strings.Contains(out, `"github_login":"dev-bot"`) {
		t.Fatalf("expected authorized-user assignee output with legacy inbox metadata, got %s", out)
	}
	out, err = handlers["github_is_actor_authorized"](ctx, json.RawMessage(`{"github_login":"ALICE"}`))
	if err != nil {
		t.Fatalf("github_is_actor_authorized returned error: %v", err)
	}
	if !strings.Contains(out, `"authorized":true`) || !strings.Contains(out, `"github_login":"alice"`) {
		t.Fatalf("expected authorized actor output, got %s", out)
	}
	out, err = handlers["github_is_actor_authorized"](ctx, json.RawMessage(`{"github_login":"bob"}`))
	if err != nil {
		t.Fatalf("github_is_actor_authorized unknown returned error: %v", err)
	}
	if !strings.Contains(out, `"authorized":false`) {
		t.Fatalf("expected deny-by-default output for unknown actor, got %s", out)
	}
	out, err = handlers["github_list_my_assigned_issues"](ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("github_list_my_assigned_issues returned error: %v", err)
	}
	if !sawMyAssignedIssues || !strings.Contains(out, `"login":"channel-user"`) || !strings.Contains(out, `"Number":5`) {
		t.Fatalf("expected authenticated assigned issues output, saw=%v out=%s", sawMyAssignedIssues, out)
	}
	out, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"Dev-Bot"}`))
	if err != nil {
		t.Fatalf("github_list_assigned_issues returned error: %v", err)
	}
	if !strings.Contains(out, `"assignee":"dev-bot"`) || !strings.Contains(out, `"Number":6`) {
		t.Fatalf("expected explicit-assignee assigned issues output, got %s", out)
	}
	if _, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"mallory"}`)); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected unauthorized explicit-assignee error, got %v", err)
	}
	out, err = handlers["github_list_my_assigned_issues"](ctx, json.RawMessage(`{"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_list_my_assigned_issues with repo_url returned error: %v", err)
	}
	if !strings.Contains(out, `"Number":7`) || !strings.Contains(out, `"https://github.com/example/other/issues/7"`) {
		t.Fatalf("expected explicit repo_url my-assigned issues output, got %s", out)
	}
	out, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_list_assigned_issues with repo_url returned error: %v", err)
	}
	if !strings.Contains(out, `"Number":8`) || !strings.Contains(out, `"https://github.com/example/other/issues/8"`) {
		t.Fatalf("expected explicit repo_url assigned issues output, got %s", out)
	}
	out, err = handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"URL issue","body":"Created by URL","labels":["bug"],"assignees":["dev-bot"],"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_create_issue with repo_url returned error: %v", err)
	}
	if createdRepo != "example/other" || !strings.Contains(out, `"Number":9`) || !strings.Contains(out, `"https://github.com/example/other/issues/9"`) {
		t.Fatalf("expected explicit repo_url create output repo=%q out=%s", createdRepo, out)
	}
	out, err = handlers["github_comment_on_issue"](ctx, json.RawMessage(`{"issue_number":9,"body":"Looks good","repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_comment_on_issue with repo_url returned error: %v", err)
	}
	if commentedRepo != "example/other" || !strings.Contains(out, `"issue_number":9`) {
		t.Fatalf("expected explicit repo_url comment output repo=%q out=%s", commentedRepo, out)
	}
	out, err = handlers["github_add_issue_labels"](ctx, json.RawMessage(`{"issue_number":9,"labels":["approved"],"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_add_issue_labels with repo_url returned error: %v", err)
	}
	if labeledRepo != "example/other" || !strings.Contains(out, `"labels":["approved"]`) {
		t.Fatalf("expected explicit repo_url label output repo=%q out=%s", labeledRepo, out)
	}
}
