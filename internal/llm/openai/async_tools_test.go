package openai

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func TestRuntimeOpenAIToolsMarksOnlyAllowlistedReadToolsAsync(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: "memory_view", Description: "read memory", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "create_task", Description: "write", Access: llmcontracts.RuntimeToolAccessWrite, Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "unknown_read", Description: "not allowlisted", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
	}}
	tools := runtimeOpenAITools(rt, true)
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	asyncByName := map[string]bool{}
	for _, tool := range tools {
		asyncByName[tool.Name] = tool.Async
	}
	if !asyncByName["memory_view"] {
		t.Fatalf("memory_view should be async: %#v", tools)
	}
	if asyncByName["create_task"] || asyncByName["unknown_read"] {
		t.Fatalf("write or unallowlisted tool was async: %#v", tools)
	}

	tools = runtimeOpenAITools(rt, false)
	for _, tool := range tools {
		if tool.Async {
			t.Fatalf("async disabled but tool was async: %#v", tools)
		}
	}
}

func TestAsyncRuntimeToolCallbacksRecoverAndTerminalizeDurableRows(t *testing.T) {
	for _, topology := range []struct {
		name string
		opts []teststores.Option
	}{
		{name: "isolated"}, {name: "production", opts: []teststores.Option{teststores.WithProductionTopology()}},
	} {
		t.Run(topology.name, func(t *testing.T) {
			opts := topology.opts
			stores := teststores.New(t, opts...)
			ctx := context.Background()
			project := &models.Project{Name: "Async Recover Project"}
			if err := stores.Projects.Create(ctx, project); err != nil {
				t.Fatalf("insert project: %v", err)
			}
			task := &models.Task{ProjectID: project.ID, Title: "Async Recover Task", Status: models.StatusRunning, Category: models.CategoryActive, Prompt: "prompt"}
			if err := stores.Tasks.Create(ctx, task); err != nil {
				t.Fatalf("insert task: %v", err)
			}
			execution := &models.Execution{ID: "async-recover-exec", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "prompt"}
			if err := stores.Executions.Create(ctx, execution); err != nil {
				t.Fatalf("insert execution: %v", err)
			}
			projectID, taskID, execID := project.ID, task.ID, execution.ID
			repo := stores.OpenAIAsyncToolCalls
			now := time.Now().UTC()
			create := func(callID, toolName string, deadline time.Time) *models.OpenAIAsyncToolCall {
				call, err := repo.CreatePending(ctx, &models.OpenAIAsyncToolCall{
					ProjectID: projectID, TaskID: taskID, ExecutionID: execID, ResponseID: "resp_" + callID,
					CallID: callID, ToolName: toolName, ArgumentsJSON: `{"handle":"provider_architecture.md"}`, DeadlineAt: deadline,
				})
				if err != nil {
					t.Fatalf("CreatePending %s: %v", callID, err)
				}
				return call
			}
			completed := create("call_completed", "memory_view", now.Add(time.Hour))
			if err := repo.MarkCompleted(ctx, completed.ID, "completed output", false, now); err != nil {
				t.Fatalf("MarkCompleted: %v", err)
			}
			pending := create("call_pending", "memory_view", now.Add(time.Hour))
			running := create("call_running", "memory_view", now.Add(time.Hour))
			if claimed, err := repo.ClaimForRun(ctx, running.ID, now); err != nil || !claimed {
				t.Fatalf("ClaimForRun running claimed=%v err=%v", claimed, err)
			}
			stale := create("call_stale", "list_tasks", now.Add(time.Hour))
			expired := create("call_expired", "memory_view", now.Add(-time.Minute))

			adapter := &Adapter{execRepo: stores.Executions, asyncToolCalls: repo}
			var executed []string
			callbacks, err := adapter.asyncRuntimeToolCallbacks(ctx, projectID, execID, true,
				[]openaiclient.ToolDefinition{{Type: "function", Name: "memory_view", Async: true}},
				func(_ context.Context, name string, input json.RawMessage) (string, bool, error) {
					executed = append(executed, name+":"+string(input))
					return "recovered " + name, false, nil
				},
				func(name string) bool { return name == "memory_view" },
				".",
			)
			if err != nil {
				t.Fatalf("asyncRuntimeToolCallbacks: %v", err)
			}
			if !callbacks.enabled {
				t.Fatal("callbacks disabled")
			}
			if len(callbacks.initialResults) != 3 {
				t.Fatalf("initialResults = %#v, want completed plus pending/running", callbacks.initialResults)
			}
			byCallID := map[string]openaiclient.AsyncToolResult{}
			for _, result := range callbacks.initialResults {
				byCallID[result.Record.CallID] = result
			}
			if byCallID["call_completed"].Output != "completed output" || byCallID["call_pending"].Output != "recovered memory_view" || byCallID["call_running"].Output != "recovered memory_view" {
				t.Fatalf("unexpected recovered outputs: %#v", byCallID)
			}
			if len(executed) != 2 {
				t.Fatalf("executed = %#v, want pending and running only", executed)
			}
			for _, tt := range []struct {
				id     string
				status string
			}{
				{pending.ID, models.OpenAIAsyncToolCallCompleted},
				{running.ID, models.OpenAIAsyncToolCallCompleted},
				{stale.ID, models.OpenAIAsyncToolCallStale},
				{expired.ID, models.OpenAIAsyncToolCallExpired},
			} {
				status, err := stores.Fixtures.AsyncToolCallStatus(ctx, tt.id)
				if err != nil {
					t.Fatalf("load status: %v", err)
				}
				if status != tt.status {
					t.Fatalf("status for %s = %s, want %s", tt.id, status, tt.status)
				}
			}

		})
	}
}

func TestOpenAIAsyncRuntimeToolsDisabledForResponsesLiteModels(t *testing.T) {
	tests := []struct {
		name  string
		agent models.LLMConfig
		want  bool
	}{
		{name: "astra api key", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra", APIKey: "sk"}},
		{name: "astra oauth", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, Model: " GPT-6-ASTRA ", OAuthAccessToken: "tok"}},
		{name: "sol api key", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-sol", APIKey: "sk"}},
		{name: "luna oauth", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, Model: " GPT-6-LUNA ", OAuthAccessToken: "tok"}},
		{name: "non gpt6", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5.6-sol", APIKey: "sk"}},
		{name: "openai compatible", agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra", APIKey: "sk"}},
		{name: "unsupported auth", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: "unsupported", Model: "gpt-6-astra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIAsyncRuntimeToolsEnabled(tt.agent); got != tt.want {
				t.Fatalf("openAIAsyncRuntimeToolsEnabled = %v, want %v", got, tt.want)
			}
			if got := openAIAsyncRuntimeToolsEnabledForRequest(context.Background(), tt.agent); got != tt.want {
				t.Fatalf("openAIAsyncRuntimeToolsEnabledForRequest = %v, want %v", got, tt.want)
			}
		})
	}
	if openAIAsyncRuntimeToolsEnabledForRequest(llmcontracts.WithProviderAsyncToolsDisabled(context.Background()), models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra", APIKey: "sk"}) {
		t.Fatal("async tools enabled despite request suppressor")
	}
}
