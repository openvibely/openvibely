package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentplugins"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProviderAdapter_TestProvider_UsesCanonicalRequest(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "adapter-output"
	mock.TextOnly = "adapter-text"
	mock.Tokens = 33
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	adapter, ok := svc.adapterFor(models.ProviderTest)
	if !ok {
		t.Fatal("expected test provider adapter")
	}

	res, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationTask,
		Message:   " hello from adapter ",
		Agent:     models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"},
		ExecID:    "exec-1",
		WorkDir:   "/tmp/work",
	})
	if err != nil {
		t.Fatalf("adapter.Call error: %v", err)
	}
	if res.Output != "adapter-output" {
		t.Fatalf("expected output adapter-output, got %q", res.Output)
	}
	if res.TextOnlyOutput != "adapter-text" {
		t.Fatalf("expected textOnly adapter-text, got %q", res.TextOnlyOutput)
	}
	if res.Usage.TotalTokens != 33 {
		t.Fatalf("expected usage total 33, got %d", res.Usage.TotalTokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected one mock call, got %d", mock.CallCount())
	}
	if mock.LastCall().ExecID != "exec-1" {
		t.Fatalf("expected exec id propagated, got %q", mock.LastCall().ExecID)
	}
}

func TestLLMService_CallAgentDirect_NormalizesMessageWhitespace(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "ok"
	mock.TextOnly = "ok"
	mock.Tokens = 1
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()
	svc.routing = newAgentRoutingStrategy(svc)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	_, _, err := svc.CallAgentDirect(context.Background(), "  hello world  ", nil, agent, "")
	if err != nil {
		t.Fatalf("CallAgentDirect error: %v", err)
	}

	if got := mock.LastCall().Prompt; got != "hello world" {
		t.Fatalf("expected normalized prompt 'hello world', got %q", got)
	}
}

func TestLLMService_CallAgentDirectNoTools_PropagatesDisableToolsFlag(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderTest: capture,
	}
	svc.routing = newAgentRoutingStrategy(svc)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	if _, _, err := svc.CallAgentDirectNoTools(context.Background(), "  json only  ", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentDirectNoTools error: %v", err)
	}

	if !capture.lastReq.DisableTools {
		t.Fatal("expected DisableTools=true in provider request")
	}
	if capture.lastReq.Message != "json only" {
		t.Fatalf("expected normalized message to propagate, got %q", capture.lastReq.Message)
	}
}

func TestLLMService_CallAgentRawDirectNoToolsIsolatesUtilityRequest(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderTest: capture,
	}
	svc.routing = newAgentRoutingStrategy(svc)

	runtimeTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), runtimeTools)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	if _, _, err := svc.CallAgentRawDirectNoTools(ctx, "commit summary", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentRawDirectNoTools error: %v", err)
	}

	if !capture.lastReq.DisableTools || !capture.lastReq.RawDirectPrompt {
		t.Fatalf("expected raw no-tools request, got disable=%v raw=%v", capture.lastReq.DisableTools, capture.lastReq.RawDirectPrompt)
	}
	if tools := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx); tools != nil {
		t.Fatalf("expected inherited runtime tools to be removed, got %#v", tools)
	}
	if capture.lastReq.AgentDefinition != nil || capture.lastReq.ProjectInstructions != "" || capture.lastReq.ChatSystemContext != "" {
		t.Fatalf("expected no interactive agent framing, got %#v", capture.lastReq)
	}
}

type recordingAnthropicAdapter struct {
	lastReq     llmcontracts.AgentRequest
	lastWorkDir string
	callCount   int
	writerIsNil bool
}

func (a *recordingAnthropicAdapter) Call(_ context.Context, req llmcontracts.AgentRequest, workDir string, writer *stream.Writer) (llmcontracts.AgentResult, error) {
	a.lastReq = req
	a.lastWorkDir = workDir
	a.callCount++
	a.writerIsNil = writer == nil
	return llmcontracts.AgentResult{Output: "forwarded"}, nil
}

func TestAnthropicProviderAdapter_ForwardsSupportedOperations(t *testing.T) {
	tests := []struct {
		name        string
		operation   llmcontracts.Operation
		workDir     string
		chatMode    models.ChatMode
		followup    bool
		history     []models.Execution
		chatContext string
	}{
		{
			name:      "direct",
			operation: llmcontracts.OperationDirect,
			workDir:   "/work/direct",
		},
		{
			name:        "streaming chat followup",
			operation:   llmcontracts.OperationStreaming,
			workDir:     "/work/streaming",
			chatMode:    models.ChatModeOrchestrate,
			followup:    true,
			history:     []models.Execution{{PromptSent: "previous prompt", Output: "previous output"}},
			chatContext: "chat context sentinel",
		},
		{
			name:      "task",
			operation: llmcontracts.OperationTask,
			workDir:   "/work/task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lowLevel := &recordingAnthropicAdapter{}
			adapter := &anthropicProviderAdapter{adapter: lowLevel}
			req := llmcontracts.AgentRequest{
				Ctx:               context.Background(),
				Operation:         tt.operation,
				Message:           "preserve this request",
				Agent:             models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key"},
				ChatMode:          tt.chatMode,
				Followup:          tt.followup,
				ChatHistory:       tt.history,
				ChatSystemContext: tt.chatContext,
				WorkDir:           tt.workDir,
			}

			result, err := adapter.Call(req)
			if err != nil {
				t.Fatalf("adapter.Call error: %v", err)
			}
			if result.Output != "forwarded" {
				t.Fatalf("result output = %q, want forwarded", result.Output)
			}
			if lowLevel.callCount != 1 {
				t.Fatalf("low-level call count = %d, want 1", lowLevel.callCount)
			}
			if !lowLevel.writerIsNil {
				t.Fatal("expected nil stream writer")
			}
			if lowLevel.lastReq.Operation != req.Operation {
				t.Fatalf("operation = %q, want %q", lowLevel.lastReq.Operation, req.Operation)
			}
			if lowLevel.lastReq.WorkDir != req.WorkDir || lowLevel.lastWorkDir != req.WorkDir {
				t.Fatalf("work directory request=%q argument=%q, want %q", lowLevel.lastReq.WorkDir, lowLevel.lastWorkDir, req.WorkDir)
			}
			if lowLevel.lastReq.Message != req.Message || lowLevel.lastReq.Followup != req.Followup || lowLevel.lastReq.ChatMode != req.ChatMode || lowLevel.lastReq.ChatSystemContext != req.ChatSystemContext {
				t.Fatalf("request context changed: got %#v, want message=%q followup=%v mode=%q context=%q", lowLevel.lastReq, req.Message, req.Followup, req.ChatMode, req.ChatSystemContext)
			}
			if len(lowLevel.lastReq.ChatHistory) != len(req.ChatHistory) || (len(req.ChatHistory) > 0 && (lowLevel.lastReq.ChatHistory[0].PromptSent != req.ChatHistory[0].PromptSent || lowLevel.lastReq.ChatHistory[0].Output != req.ChatHistory[0].Output)) {
				t.Fatalf("chat history changed: got %#v, want %#v", lowLevel.lastReq.ChatHistory, req.ChatHistory)
			}
		})
	}
}

func TestAnthropicProviderAdapter_RejectsRetiredCLITransport(t *testing.T) {
	for _, operation := range []llmcontracts.Operation{
		llmcontracts.OperationDirect,
		llmcontracts.OperationStreaming,
		llmcontracts.OperationTask,
	} {
		t.Run(string(operation), func(t *testing.T) {
			lowLevel := &recordingAnthropicAdapter{}
			adapter := &anthropicProviderAdapter{adapter: lowLevel}
			_, err := adapter.Call(llmcontracts.AgentRequest{
				Ctx:       context.Background(),
				Operation: operation,
				Message:   "generate JSON",
				Agent: models.LLMConfig{
					Provider:   models.ProviderAnthropic,
					AuthMethod: models.AuthMethodCLI,
					Model:      "claude-sonnet-4",
				},
				WorkDir: "/work/retired-cli",
			})
			if err == nil {
				t.Fatal("expected error for retired Anthropic CLI transport")
			}
			if !strings.Contains(err.Error(), "no longer supported") {
				t.Fatalf("unexpected error: %v", err)
			}
			if lowLevel.callCount != 0 {
				t.Fatalf("low-level call count = %d, want 0", lowLevel.callCount)
			}
		})
	}
}

func TestAnthropicProviderAdapter_RejectsUnknownOperationWithoutProviderCall(t *testing.T) {
	lowLevel := &recordingAnthropicAdapter{}
	adapter := &anthropicProviderAdapter{adapter: lowLevel}
	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.Operation("unknown"),
		Agent: models.LLMConfig{
			Provider:   models.ProviderAnthropic,
			AuthMethod: models.AuthMethodAPIKey,
			APIKey:     "test-key",
		},
	})
	if err == nil || err.Error() != "unsupported operation: unknown" {
		t.Fatalf("error = %v, want unsupported operation: unknown", err)
	}
	if lowLevel.callCount != 0 {
		t.Fatalf("low-level call count = %d, want 0", lowLevel.callCount)
	}
}

func TestOpenAIProviderAdapter_RejectsRetiredCLITransport(t *testing.T) {
	adapter := &openAIProviderAdapter{svc: &LLMService{}}
	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:          context.Background(),
		Operation:    llmcontracts.OperationDirect,
		Message:      "generate JSON",
		DisableTools: true,
		Agent: models.LLMConfig{
			Provider:   models.ProviderOpenAI,
			AuthMethod: models.AuthMethodCLI,
			Model:      "gpt-5.3-codex",
		},
	})
	if err == nil {
		t.Fatal("expected error for retired OpenAI CLI transport")
	}
	if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestUsesChatStreamingTreatsFirstTurnChatAsChat(t *testing.T) {
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate}) {
		t.Fatal("expected first-turn orchestrate chat with nil history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModePlan}) {
		t.Fatal("expected first-turn plan chat with nil history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Followup: true}) {
		t.Fatal("expected task followup without prior history or selected context to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate, Followup: true, ChatHistory: []models.Execution{{PromptSent: "previous", Output: "done"}}}) {
		t.Fatal("expected task followup with chat history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Followup: true, ChatSystemContext: "## Selected Memories For This Task"}) {
		t.Fatal("expected task followup with selected-memory system context to use chat streaming")
	}
	if requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming}) {
		t.Fatal("streaming task without explicit chat mode/history/context must not use chat streaming")
	}
}

func TestProviderContextCompactionFallback_RetriesStreamingRequestOnceForSupportedProviders(t *testing.T) {
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, provider := range providers {
		t.Run(string(provider), func(t *testing.T) {
			svc := NewLLMService(nil, nil, nil, nil, nil, nil)
			var requests []llmcontracts.AgentRequest
			adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				requests = append(requests, req)
				switch len(requests) {
				case 1:
					return llmcontracts.AgentResult{}, fmt.Errorf("context length exceeded: too many tokens")
				case 2:
					if req.Operation != llmcontracts.OperationDirect || !req.DisableTools || !req.RawDirectPrompt {
						t.Fatalf("summary request did not use raw no-tools direct semantics: %#v", req)
					}
					if llmcontracts.RuntimeToolsFromContext(req.Ctx) != nil {
						t.Fatal("summary request inherited runtime tools")
					}
					if !strings.Contains(req.Message, localContextCompactionInstruction) {
						t.Fatalf("summary request missing exact compaction instruction: %q", req.Message)
					}
					return llmcontracts.AgentResult{Output: "completed: old work\nremaining: next step"}, nil
				case 3:
					return llmcontracts.AgentResult{Output: "final", TextOnlyOutput: "final"}, nil
				default:
					t.Fatalf("unexpected provider call %d", len(requests))
					return llmcontracts.AgentResult{}, nil
				}
			})
			svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{provider: adapter}
			svc.routing = newAgentRoutingStrategy(svc)

			rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file", Access: llmcontracts.RuntimeToolAccessWrite}}}
			ctx := llmcontracts.WithRuntimeTools(context.Background(), rt)
			history := []models.Execution{
				{PromptSent: "old user", Output: "old assistant", Status: models.ExecCompleted},
				{PromptSent: "new user", Output: "new assistant", Status: models.ExecCompleted},
			}
			res, err := svc.CallAgentDirectStreamingDetailed(ctx, " pending user ", nil, models.LLMConfig{Provider: provider, Model: "model"}, "exec-1", history, "system context", "/tmp/work", nil)
			if err != nil {
				t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
			}
			if res.Output != "final" {
				t.Fatalf("output = %q, want final", res.Output)
			}
			if len(requests) != 3 {
				t.Fatalf("provider calls = %d, want 3", len(requests))
			}
			original, retry := requests[0], requests[2]
			if retry.Message != original.Message || retry.Operation != original.Operation || retry.ChatMode != original.ChatMode || retry.ChatSystemContext != original.ChatSystemContext || retry.ExecID != original.ExecID || retry.WorkDir != original.WorkDir {
				t.Fatalf("retry did not preserve request semantics\noriginal=%#v\nretry=%#v", original, retry)
			}
			if llmcontracts.RuntimeToolsFromContext(retry.Ctx) == nil {
				t.Fatal("retry dropped runtime tools")
			}
			if len(retry.ChatHistory) != 3 {
				t.Fatalf("compacted history length = %d, want retained users plus summary", len(retry.ChatHistory))
			}
			if retry.ChatHistory[0].PromptSent != "old user" || retry.ChatHistory[1].PromptSent != "new user" {
				t.Fatalf("retained user messages should remain chronological, got %#v", retry.ChatHistory)
			}
			if got := retry.ChatHistory[2].Output; !strings.Contains(got, compactedHistorySummaryPrefix) || !strings.Contains(got, "completed: old work") {
				t.Fatalf("retry history missing protected summary prefix/content: %q", got)
			}
		})
	}
}

func TestProviderContextCompactionFallback_CompactedRetryOverflowUsesLastResortOnce(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	var logBuf bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLogWriter)

	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		if req.Operation == llmcontracts.OperationDirect {
			return llmcontracts.AgentResult{Output: "summary"}, nil
		}
		if len(requests) == 4 {
			return llmcontracts.AgentResult{Output: "last resort ok"}, nil
		}
		return llmcontracts.AgentResult{}, fmt.Errorf("maximum context length exceeded")
	})
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAICompatible: adapter}
	svc.routing = newAgentRoutingStrategy(svc)

	history := make([]models.Execution, 25)
	for i := range history {
		history[i] = models.Execution{PromptSent: fmt.Sprintf("prompt-%02d", i), Output: "out", Status: models.ExecCompleted}
	}
	res, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "current", nil, models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "model"}, "exec-1", history, "", "/tmp/work", nil)
	if err != nil || res.Output != "last resort ok" {
		t.Fatalf("result=%#v err=%v, want last resort success", res, err)
	}
	if len(requests) != 4 {
		t.Fatalf("provider calls = %d, want original + summary + compacted retry + last resort", len(requests))
	}
	if len(requests[3].ChatHistory) != 20 || requests[3].ChatHistory[0].PromptSent != "prompt-05" {
		t.Fatalf("last resort history = %#v", requests[3].ChatHistory)
	}
	if got := logBuf.String(); !strings.Contains(got, "WARNING: context compaction failed; using last-resort latest-20-turn truncation") {
		t.Fatalf("last resort warning log missing: %s", got)
	}
}

func TestProviderContextCompactionFallback_SummaryOverflowDropsOldestAndRetries(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	var summaryPrompts []string
	var summaryScope string
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		if req.Operation == llmcontracts.OperationDirect {
			if !strings.HasPrefix(req.TransportScope, "compaction:") || llmcontracts.TransportScopeFromContext(req.Ctx) != req.TransportScope {
				t.Fatalf("summary must have its own scoped transport: %q", req.TransportScope)
			}
			if summaryScope != "" && summaryScope != req.TransportScope {
				t.Fatal("summary retry replaced the compaction session")
			}
			summaryScope = req.TransportScope
			if !req.DisableTools || !req.Agent.DisableNativeCompaction {
				t.Fatal("summary must disable tools and recursive compaction")
			}
			summaryPrompts = append(summaryPrompts, req.Message)
			if len(summaryPrompts) == 1 {
				return llmcontracts.AgentResult{}, fmt.Errorf("prompt is too long for context window")
			}
			return llmcontracts.AgentResult{Output: "summary"}, nil
		}
		if strings.HasPrefix(req.TransportScope, "compaction:") {
			t.Fatal("summary session leaked into normal continuation")
		}
		if len(summaryPrompts) == 0 {
			return llmcontracts.AgentResult{}, fmt.Errorf("context length exceeded")
		}
		return llmcontracts.AgentResult{Output: "ok", TextOnlyOutput: "ok"}, nil
	})
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAICompatible: adapter}
	svc.routing = newAgentRoutingStrategy(svc)

	history := []models.Execution{{PromptSent: "drop me", Output: "old", Status: models.ExecCompleted}, {PromptSent: "keep me", Output: "new", Status: models.ExecCompleted}}
	if _, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "current", nil, models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "model"}, "exec-1", history, "", "/tmp/work", nil); err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	if len(summaryPrompts) != 2 {
		t.Fatalf("summary attempts = %d, want 2", len(summaryPrompts))
	}
	if !strings.Contains(summaryPrompts[0], "drop me") || strings.Contains(summaryPrompts[1], "drop me") || !strings.Contains(summaryPrompts[1], "keep me") {
		t.Fatalf("summary overflow retry did not drop only oldest history: %#v", summaryPrompts)
	}
}

func TestProviderContextCompactionFallback_RetainsUserMessagesWithinUTF8Budget(t *testing.T) {
	if got := estimatedUTF8Tokens("éé"); got != 1 {
		t.Fatalf("estimated UTF-8 tokens = %d, want ceil(4 bytes / 4)=1", got)
	}
	oversized := "prefix-" + strings.Repeat("x", 200) + "-suffix"
	history := []models.Execution{
		{PromptSent: "too old"},
		{PromptSent: oversized},
		{ID: "execution-with-stored-replay", PromptSent: "tail"},
	}
	retained := retainedUserMessageHistory(history, 20)
	if len(retained) != 2 {
		t.Fatalf("retained = %#v, want boundary plus newest", retained)
	}
	if retained[1].PromptSent != "tail" {
		t.Fatalf("newest retained message should be last = %#v", retained)
	}
	if retained[1].ID != "" {
		t.Fatal("retained prompt must not hydrate the original execution's tool replay")
	}
	if got := retained[0].PromptSent; !strings.Contains(got, "[Middle of user message omitted") || !strings.HasPrefix(got, "prefix-") || !strings.HasSuffix(got, "-suffix") {
		t.Fatalf("boundary message was not middle-truncated with prefix/suffix preserved: %q", got)
	}
}

func TestReportedContextUsagePersistsAndTriggersNextTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewExecutionRepo(db)
	ctx := context.Background()
	first := llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate, ProjectID: "usage-project", ExecID: "previous", Agent: models.LLMConfig{ID: "usage-model", Provider: models.ProviderOpenAICompatible, ContextWindow: 10000}, Message: "short"}
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{Output: "done", Usage: llmcontracts.Usage{LastContextTokens: 9500, TotalTokens: 900000}}, nil
	})
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, first); err != nil {
		t.Fatal(err)
	}
	// New service simulates a restart: the baseline must come from the database.
	svc = NewLLMService(nil, repo, nil, nil, nil, nil)
	second := first
	second.ExecID = "next"
	second.ChatHistory = []models.Execution{{ID: "previous", PromptSent: "short", Output: "done", Status: models.ExecCompleted}}
	calls := 0
	adapter = providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		if calls == 1 && req.Operation != llmcontracts.OperationDirect {
			t.Fatal("reported usage should trigger proactive summary despite tiny text history")
		}
		if req.ContextTokenEstimate >= 900000 {
			t.Fatal("used cumulative billing usage")
		}
		return llmcontracts.AgentResult{Output: "summary"}, nil
	})
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, second); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want summary and continuation", calls)
	}
}

func TestReportedContextUsageAddsOnlyNewContent(t *testing.T) {
	req := llmcontracts.AgentRequest{Message: "12345678", ChatHistory: []models.Execution{{ID: "source", Output: strings.Repeat("x", 10000)}}}
	baseline := models.ChatContextUsage{SourceExecutionID: "source", ContextTokens: 120000}
	if got := estimateContextFromReportedUsage(req, baseline); got != 120002 {
		t.Fatalf("got %d, want 120002", got)
	}
	baseline.ContextTokens = 0
	if got := estimateContextFromReportedUsage(req, baseline); got != 0 {
		t.Fatalf("missing usage must use full estimate: %d", got)
	}
	baseline.ContextTokens = 120000
	baseline.SourceExecutionID = "missing"
	if got := estimateContextFromReportedUsage(req, baseline); got != 0 {
		t.Fatalf("missing source must use full estimate: %d", got)
	}
}

func TestNativeCheckpointFailureSummarizesOriginalTranscript(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewExecutionRepo(db)
	ctx := context.Background()
	state := `[{"type":"compaction","encrypted_content":"opaque"}]`
	if err := repo.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{
		ScopeType: "chat_project", ScopeID: "fallback", ModelConfigID: "model",
		SourceExecutionID: "source", Strategy: "openai_responses", ProviderStateJSON: state,
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		switch calls {
		case 1:
			if req.NativeCompactionStateJSON != state || len(req.ChatHistory) != 0 {
				t.Fatalf("expected restored native checkpoint: %#v", req)
			}
			return llmcontracts.AgentResult{}, fmt.Errorf("maximum context length exceeded")
		case 2:
			if req.Operation != llmcontracts.OperationDirect || !strings.Contains(req.Message, "original answer") {
				t.Fatalf("summary must use original transcript: %#v", req)
			}
		case 3:
			if len(req.ChatHistory) != 2 || req.ChatHistory[0].ID != "" || !strings.Contains(req.ChatHistory[1].Output, "text summary") {
				t.Fatalf("expected synthetic summary context: %#v", req)
			}
		default:
			t.Fatalf("unexpected call %d", calls)
		}
		if req.NativeCompactionStateJSON != "" || llmcontracts.NativeCompactionStateJSONFromContext(req.Ctx) != "" {
			t.Fatal("text compaction leaked native encrypted state")
		}
		return llmcontracts.AgentResult{Output: "text summary"}, nil
	})
	_, err := svc.callProviderWithContextCompactionFallback(adapter, llmcontracts.AgentRequest{
		Ctx: llmcontracts.WithNativeCompactionStateJSON(ctx, state), Operation: llmcontracts.OperationStreaming,
		ProjectID: "fallback", Agent: models.LLMConfig{ID: "model", Provider: models.ProviderOpenAI},
		ChatHistory: []models.Execution{{ID: "source", PromptSent: "original question", Output: "original answer", Status: models.ExecCompleted}},
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	checkpoint, err := repo.GetChatCompactionCheckpoint(ctx, "chat_project", "fallback")
	if err != nil || checkpoint == nil || checkpoint.ProviderStateJSON != "" || checkpoint.Strategy != "local_summary" {
		t.Fatalf("checkpoint=%#v err=%v", checkpoint, err)
	}
}

func TestProviderContextCompactionLimits_TriggerMathAndConfiguredClamp(t *testing.T) {
	openAILimits := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex"})
	if openAILimits.ContextWindow != 272000 || openAILimits.AutoLimit != 244800 {
		t.Fatalf("OpenAI Codex limits = %+v, want W=272000 auto=244800", openAILimits)
	}

	limits := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "custom", ContextWindow: 1000})
	if limits.AutoLimit != 900 || limits.TriggerLimit != 900 || limits.EffectiveHardLimit != 950 {
		t.Fatalf("limits = %+v, want auto=900 trigger=900 hard=950", limits)
	}
	limits = compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "custom", ContextWindow: 1000, CompactionThreshold: 1200})
	if limits.TriggerLimit != 900 {
		t.Fatalf("configured threshold should clamp to 90%% auto limit, got %+v", limits)
	}
	limits = compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "custom", ContextWindow: 1000, CompactionThreshold: 700})
	if limits.TriggerLimit != 700 {
		t.Fatalf("configured lower threshold should be honored, got %+v", limits)
	}

	req := llmcontracts.AgentRequest{Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 1000}, Message: strings.Repeat("a", 3600)}
	triggered, _, used := shouldTriggerContextCompaction(req)
	if !triggered || used != 900 {
		t.Fatalf("trigger=%v used=%d, want trigger at 90%%", triggered, used)
	}
	req.Message = strings.Repeat("a", 3800)
	req.Agent.CompactionThreshold = 2000
	triggered, _, used = shouldTriggerContextCompaction(req)
	if !triggered || used != 950 {
		t.Fatalf("trigger=%v used=%d, want hard-limit trigger at 95%% even when threshold clamps", triggered, used)
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file", Description: strings.Repeat("tool", 200), Parameters: []byte(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessWrite}}}
	req = llmcontracts.AgentRequest{Ctx: llmcontracts.WithRuntimeTools(context.Background(), rt), Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate, Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 1000}, Message: strings.Repeat("a", 2000)}
	triggered, _, used = shouldTriggerContextCompaction(req)
	if !triggered || used <= 500 {
		t.Fatalf("trigger=%v used=%d, want model-visible system/runtime tool context included", triggered, used)
	}
}

func TestProviderContextCompactionFallback_NativeProvidersReceiveProactiveThreshold(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	var got llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		got = req
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: strings.Repeat("m", 3600), Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test", ContextWindow: 1000, CompactionThreshold: 1200}, ChatHistory: []models.Execution{{PromptSent: "old", Output: "done"}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	if got.Operation != llmcontracts.OperationStreaming || got.Agent.DisableNativeCompaction {
		t.Fatalf("native provider should be called directly with native compaction enabled, got %#v", got)
	}
	if !got.ForceNativeCompaction || !got.Agent.ForceNativeCompaction {
		t.Fatalf("native full-request trigger should force provider-native compaction, got request=%v agent=%v", got.ForceNativeCompaction, got.Agent.ForceNativeCompaction)
	}
	if got.Agent.CompactionThreshold != 900 || got.NativeCompactionTokenThreshold != 900 {
		t.Fatalf("native threshold = agent:%d request:%d, want 900", got.Agent.CompactionThreshold, got.NativeCompactionTokenThreshold)
	}
}

func TestProviderContextCompactionFallback_OpenAICompatibleProactiveSummaryAndPersistence(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	svc := NewLLMService(nil, execRepo, nil, nil, nil, nil)
	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		if req.Operation == llmcontracts.OperationDirect {
			if !req.DisableTools || !req.RawDirectPrompt || !req.Agent.DisableNativeCompaction {
				t.Fatalf("summary request semantics = %#v", req)
			}
			return llmcontracts.AgentResult{Output: "completed work; no authorization to write"}, nil
		}
		return llmcontracts.AgentResult{Output: "ok", TextOnlyOutput: "ok"}, nil
	})
	history := []models.Execution{
		{ID: "old", PromptSent: strings.Repeat("o", 2400), Output: "old", Status: models.ExecCompleted},
		{ID: "source", PromptSent: strings.Repeat("n", 2400), Output: "new", Status: models.ExecCompleted},
	}
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: strings.Repeat("m", 80), Agent: models.LLMConfig{ID: "model-1", Provider: models.ProviderOpenAICompatible, Model: "compat", ContextWindow: 1000}, ProjectID: "project-1", ChatHistory: history}
	res, err := svc.callProviderWithContextCompactionFallback(adapter, req)
	if err != nil || res.Output != "ok" {
		t.Fatalf("call result=%#v err=%v", res, err)
	}
	if len(requests) != 2 || requests[0].Operation != llmcontracts.OperationDirect || requests[1].Operation != llmcontracts.OperationStreaming {
		t.Fatalf("requests = %#v, want proactive summary then original operation", requests)
	}
	checkpoint, err := execRepo.GetChatCompactionCheckpoint(context.Background(), "chat_project", "project-1")
	if err != nil {
		t.Fatalf("GetChatCompactionCheckpoint: %v", err)
	}
	if checkpoint == nil || checkpoint.SourceExecutionID != "source" || !strings.Contains(checkpoint.Summary, "completed work") {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
}

func TestProviderContextCompactionFallback_RestoresDurableCheckpointOnFollowingTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	checkpointHistory := []models.Execution{{Output: compactedHistorySummaryPrefix + "\n\nsummary", Status: models.ExecCompleted}}
	if err := execRepo.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{ScopeType: "chat_project", ScopeID: "project-restore", SourceExecutionID: "source", History: checkpointHistory, Summary: "summary", Strategy: "local_summary"}); err != nil {
		t.Fatalf("UpsertChatCompactionCheckpoint: %v", err)
	}
	svc := NewLLMService(nil, execRepo, nil, nil, nil, nil)
	var got llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		got = req
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "next", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "compat", ContextWindow: 100000}, ProjectID: "project-restore", ChatHistory: []models.Execution{{ID: "old", PromptSent: "old"}, {ID: "source", PromptSent: "source"}, {ID: "new", PromptSent: "new"}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	if len(got.ChatHistory) != 2 || !strings.Contains(got.ChatHistory[0].Output, "summary") || got.ChatHistory[1].PromptSent != "new" {
		t.Fatalf("restored history = %#v", got.ChatHistory)
	}
}

func TestProviderContextCompactionFallback_NativeFailureFallsBackAndCachesUnsupported(t *testing.T) {
	model := "native-unsupported-test"
	knownUnsupportedNativeCompaction.Delete(string(models.ProviderOpenAI) + "\x00" + model)
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		if len(requests) == 1 {
			if req.Operation != llmcontracts.OperationStreaming || req.Agent.DisableNativeCompaction {
				t.Fatalf("first native request = %#v", req)
			}
			return llmcontracts.AgentResult{}, fmt.Errorf("pre-turn compaction: unsupported compaction endpoint")
		}
		if len(requests) == 2 {
			if req.Operation != llmcontracts.OperationDirect || !req.Agent.DisableNativeCompaction {
				t.Fatalf("summary fallback request = %#v", req)
			}
			return llmcontracts.AgentResult{Output: "summary"}, nil
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "current", Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: model, ContextWindow: 100000}, ChatHistory: []models.Execution{{PromptSent: "old", Output: "done"}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	if len(requests) != 3 || !requests[2].Agent.DisableNativeCompaction {
		t.Fatalf("requests = %#v, want native failure, local summary, retry with native disabled", requests)
	}
	if !knownNativeCompactionUnsupported(req.Agent) {
		t.Fatal("native unsupported state was not cached")
	}
}

func TestProviderContextCompactionFallback_LastResortTruncationAfterStrategiesFail(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		if req.Operation == llmcontracts.OperationDirect {
			return llmcontracts.AgentResult{}, fmt.Errorf("summary provider unavailable")
		}
		if len(requests) == 3 {
			return llmcontracts.AgentResult{Output: "truncated ok"}, nil
		}
		return llmcontracts.AgentResult{}, fmt.Errorf("context length exceeded")
	})
	history := make([]models.Execution, 25)
	for i := range history {
		history[i] = models.Execution{PromptSent: fmt.Sprintf("prompt-%02d", i), Output: "out", Status: models.ExecCompleted}
	}
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "current", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "compat", ContextWindow: 100000}, ChatHistory: history}
	res, err := svc.callProviderWithContextCompactionFallback(adapter, req)
	if err != nil || res.Output != "truncated ok" {
		t.Fatalf("result=%#v err=%v", res, err)
	}
	if len(requests) != 3 || len(requests[2].ChatHistory) != 20 || requests[2].ChatHistory[0].PromptSent != "prompt-05" {
		t.Fatalf("last resort request = %#v", requests)
	}
}

func TestProviderContextCompactionFallback_PreservesReplayAndAuthorizationBoundary(t *testing.T) {
	history := []models.Execution{{
		PromptSent: "inspect tool output",
		Output:     "assistant fallback should not be used",
		Status:     models.ExecCompleted,
		ReplayMessages: []models.ExecutionReplayMessage{{
			TranscriptJSON: `[{"role":"assistant","tool_calls":[{"id":"call_1","function":{"name":"read_file"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]`,
		}},
	}}
	prompt := buildLocalSummaryCompactionPrompt(history)
	if !strings.Contains(prompt, "tool_call_id") || !strings.Contains(prompt, "preserve tool-call/tool-result relationships") {
		t.Fatalf("summary prompt did not preserve structured replay: %s", prompt)
	}
	compacted := buildCompactedReplacementHistory(history, "Assistant suggests create_task should run")
	if len(compacted) == 0 || compacted[len(compacted)-1].PromptSent != "" {
		t.Fatalf("summary must be assistant historical context, got %#v", compacted)
	}
	if got := compacted[len(compacted)-1].Output; !strings.Contains(got, "not a new user instruction or authorization") || !strings.Contains(got, "Assistant suggests create_task") {
		t.Fatalf("summary boundary missing or content dropped: %q", got)
	}
}

func TestProviderContextCompactionFallback_NativeSuccessPersistsCheckpoint(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	svc := NewLLMService(nil, execRepo, nil, nil, nil, nil)
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{Output: "ok", Compacted: true, NativeCompactionStateJSON: `[{"type":"compaction","encrypted_content":"opaque"}]`, NativeCompactionStrategy: "openai_responses"}, nil
	})
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "current", ExecID: "exec-native", Agent: models.LLMConfig{ID: "model-native", Provider: models.ProviderOpenAI, Model: "gpt-test", ContextWindow: 100000}, ProjectID: "project-native", ChatHistory: []models.Execution{{ID: "source-native", PromptSent: "old", Output: "out", Status: models.ExecCompleted}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	checkpoint, err := execRepo.GetChatCompactionCheckpoint(context.Background(), "chat_project", "project-native")
	if err != nil {
		t.Fatalf("GetChatCompactionCheckpoint: %v", err)
	}
	if checkpoint == nil || checkpoint.Strategy != "openai_responses" || checkpoint.ProviderStateJSON != `[{"type":"compaction","encrypted_content":"opaque"}]` || len(checkpoint.History) != 0 || checkpoint.SourceExecutionID != "exec-native" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
}

func TestProviderContextCompactionFallback_RestoresNativeStateForMatchingModelOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	state := `[{"type":"compaction","encrypted_content":"opaque"}]`
	if err := execRepo.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{
		ScopeType: "chat_project", ScopeID: "project-native-restore", ModelConfigID: "model-1",
		SourceExecutionID: "source", Strategy: "openai_responses", ProviderStateJSON: state,
	}); err != nil {
		t.Fatalf("UpsertChatCompactionCheckpoint: %v", err)
	}

	svc := NewLLMService(nil, execRepo, nil, nil, nil, nil)
	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	history := []models.Execution{{ID: "old"}, {ID: "source"}, {ID: "new", PromptSent: "new"}}

	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ProjectID: "project-native-restore", Agent: models.LLMConfig{ID: "model-1", Provider: models.ProviderOpenAI}, ChatHistory: history}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatal(err)
	}
	if requests[0].NativeCompactionStateJSON != state || len(requests[0].ChatHistory) != 1 || requests[0].ChatHistory[0].ID != "new" {
		t.Fatalf("matching-model restore = %#v", requests[0])
	}

	req.Agent.ID = "model-2"
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatal(err)
	}
	if requests[1].NativeCompactionStateJSON != "" || len(requests[1].ChatHistory) != len(history) {
		t.Fatalf("mismatched-model restore = %#v", requests[1])
	}
}

func TestResolveAgentRuntime_PerAgentPluginIsolation(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-a@alpha-market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs: []string{"plugin-a@alpha-market"},
				Skills:    []models.SkillConfig{{Name: "skill-a", Content: "a"}},
			}, nil
		}
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-b@beta-market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs: []string{"plugin-b@beta-market"},
				Skills:    []models.SkillConfig{{Name: "skill-b", Content: "b"}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	agentA := &models.Agent{Name: "agent-a", Plugins: []string{"plugin-a@alpha-market"}}
	agentB := &models.Agent{Name: "agent-b", Plugins: []string{"plugin-b@beta-market"}}

	_, mergedA := resolveAgentRuntime(context.Background(), agentA)
	_, mergedB := resolveAgentRuntime(context.Background(), agentB)

	if len(mergedA.Skills) == 0 || !strings.EqualFold(strings.TrimSpace(mergedA.Skills[0].Name), "skill-a") {
		t.Fatalf("expected agent A to include only plugin A skill, got %+v", mergedA.Skills)
	}
	if len(mergedB.Skills) == 0 || !strings.EqualFold(strings.TrimSpace(mergedB.Skills[0].Name), "skill-b") {
		t.Fatalf("expected agent B to include only plugin B skill, got %+v", mergedB.Skills)
	}
}

// TestResolveAgentRuntime_NilAgentDefinition verifies that a nil agent
// definition produces zero plugin context (no skills, no dirs, no MCP).
func TestResolveAgentRuntime_NilAgentDefinition(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked-skill", Content: "leaked"}},
		}, nil
	}

	raw, merged := resolveAgentRuntime(context.Background(), nil)
	if raw != nil {
		t.Fatalf("expected nil raw agent, got %+v", raw)
	}
	if merged != nil {
		t.Fatalf("expected nil merged agent, got %+v", merged)
	}
	if called {
		t.Fatal("plugin resolver should not be called for nil agent definition")
	}
}

// TestResolveAgentRuntime_AgentWithNoPlugins verifies that an agent with an
// empty Plugins list does not trigger plugin resolution.
func TestResolveAgentRuntime_AgentWithNoPlugins(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked-skill", Content: "leaked"}},
		}, nil
	}

	agentDef := &models.Agent{
		Name:         "no-plugins-agent",
		SystemPrompt: "I have no plugins",
		Plugins:      nil,
	}

	raw, merged := resolveAgentRuntime(context.Background(), agentDef)
	if raw == nil || merged == nil {
		t.Fatal("expected non-nil raw/merged agent")
	}
	if len(merged.Skills) != 0 {
		t.Fatalf("expected zero skills, got %+v", merged.Skills)
	}
	if len(merged.MCPServers) != 0 {
		t.Fatalf("expected zero MCP servers, got %+v", merged.MCPServers)
	}
	if called {
		t.Fatal("plugin resolver should not be called when agent has no plugins")
	}

	// Also test with explicit empty slice (not nil)
	agentDef.Plugins = []string{}
	called = false
	_, merged2 := resolveAgentRuntime(context.Background(), agentDef)
	if len(merged2.Skills) != 0 {
		t.Fatalf("expected zero skills for empty slice, got %+v", merged2.Skills)
	}
	if called {
		t.Fatal("plugin resolver should not be called for empty plugin list")
	}
}

// TestResolveAgentRuntime_NoCrossAgentLeakage verifies that resolving plugins
// for one agent does not leak skills, MCP servers, or dirs into another agent.
func TestResolveAgentRuntime_NoCrossAgentLeakage(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		// Return unique resources per plugin set to verify isolation
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-x@market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"plugin-x@market"},
				Skills:     []models.SkillConfig{{Name: "skill-x", Content: "x-content"}},
				MCPServers: []models.MCPServerConfig{{Name: "mcp-x", Type: "stdio", Command: []string{"echo"}}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	agentWithPlugins := &models.Agent{Name: "agent-with-plugins", Plugins: []string{"plugin-x@market"}}
	agentNoPlugins := &models.Agent{Name: "agent-no-plugins", Plugins: nil}

	// Resolve agent with plugins first
	_, mergedWith := resolveAgentRuntime(context.Background(), agentWithPlugins)
	// Then resolve agent without plugins
	_, mergedWithout := resolveAgentRuntime(context.Background(), agentNoPlugins)

	// Verify agent WITH plugins has them
	if len(mergedWith.Skills) != 1 || mergedWith.Skills[0].Name != "skill-x" {
		t.Fatalf("agent with plugins should have skill-x, got %+v", mergedWith.Skills)
	}
	if len(mergedWith.MCPServers) != 1 || mergedWith.MCPServers[0].Name != "mcp-x" {
		t.Fatalf("agent with plugins should have mcp-x, got %+v", mergedWith.MCPServers)
	}
	// Verify agent WITHOUT plugins has none
	if len(mergedWithout.Skills) != 0 {
		t.Fatalf("agent without plugins should have zero skills, got %+v", mergedWithout.Skills)
	}
	if len(mergedWithout.MCPServers) != 0 {
		t.Fatalf("agent without plugins should have zero MCP servers, got %+v", mergedWithout.MCPServers)
	}
}

// TestAdapterCall_NoAgentDefinition_NoPluginContext verifies that adapter calls
// without an agent definition produce zero plugin context in the request.
func TestAdapterCall_NoAgentDefinition_NoPluginContext(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked", Content: "should not appear"}},
		}, nil
	}

	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "ok"
	mock.TextOnly = "ok"
	mock.Tokens = 1
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	// Call with nil AgentDefinition (task has no assigned agent definition)
	_, err := svc.providerAdapters[models.ProviderTest].Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "do something",
		Agent:           models.LLMConfig{Provider: models.ProviderTest, Model: "test"},
		AgentDefinition: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("plugin resolver should not be called when no agent definition is set")
	}
}

// pluginResolvingAdapter wraps a captureProviderAdapter and calls
// resolveAgentRuntime before delegating, mimicking what the real provider
// adapters (openAI/anthropic) do.
type pluginResolvingAdapter struct {
	inner *captureProviderAdapter
}

func (a *pluginResolvingAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	return a.inner.Call(req)
}

// TestAdapterCall_AgentWithPlugins_OnlyThosePlugins verifies that adapter calls
// with an agent definition containing plugins receive exactly those plugins'
// skills and MCP servers, and no others.
func TestAdapterCall_AgentWithPlugins_OnlyThosePlugins(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	var resolvedIDs []string
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		resolvedIDs = pluginIDs
		return &agentplugins.RuntimeBundle{
			PluginIDs:  pluginIDs,
			Skills:     []models.SkillConfig{{Name: "target-skill", Content: "target content"}},
			MCPServers: []models.MCPServerConfig{{Name: "target-mcp", Type: "stdio", Command: []string{"echo"}}},
		}, nil
	}

	capture := &captureProviderAdapter{}
	adapter := &pluginResolvingAdapter{inner: capture}

	agentDef := &models.Agent{
		ID:           "agent-def-target",
		Name:         "target-agent",
		SystemPrompt: "Use target tools",
		Plugins:      []string{"target-plugin@market"},
	}

	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "run task",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"},
		AgentDefinition: agentDef,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the plugin resolver was called with exactly the agent's plugins
	if len(resolvedIDs) != 1 || resolvedIDs[0] != "target-plugin@market" {
		t.Fatalf("expected resolver to be called with [target-plugin@market], got %v", resolvedIDs)
	}

	// Verify the agent definition passed through has the merged plugins
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition to be propagated")
	}
	if len(capture.lastReq.AgentDefinition.Skills) != 1 || capture.lastReq.AgentDefinition.Skills[0].Name != "target-skill" {
		t.Fatalf("expected merged skills to contain target-skill, got %+v", capture.lastReq.AgentDefinition.Skills)
	}
	if len(capture.lastReq.AgentDefinition.MCPServers) != 1 || capture.lastReq.AgentDefinition.MCPServers[0].Name != "target-mcp" {
		t.Fatalf("expected merged MCP servers to contain target-mcp, got %+v", capture.lastReq.AgentDefinition.MCPServers)
	}
}

// TestAdapterCall_MultipleAgents_NoPluginCrossLeak exercises the full adapter
// call path with two different agents sequentially, verifying that plugin
// context from one agent does not leak into calls for the other.
func TestAdapterCall_MultipleAgents_NoPluginCrossLeak(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		if len(pluginIDs) == 1 && pluginIDs[0] == "alpha-plugin@market-a" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"alpha-plugin@market-a"},
				Skills:     []models.SkillConfig{{Name: "alpha-skill", Content: "alpha"}},
				MCPServers: []models.MCPServerConfig{{Name: "alpha-mcp", Type: "stdio", Command: []string{"alpha-cmd"}}},
			}, nil
		}
		if len(pluginIDs) == 1 && pluginIDs[0] == "beta-plugin@market-b" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"beta-plugin@market-b"},
				Skills:     []models.SkillConfig{{Name: "beta-skill", Content: "beta"}},
				MCPServers: []models.MCPServerConfig{{Name: "beta-mcp", Type: "stdio", Command: []string{"beta-cmd"}}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	capture := &captureProviderAdapter{}
	adapter := &pluginResolvingAdapter{inner: capture}

	agentA := &models.Agent{
		ID:      "agent-alpha",
		Name:    "alpha-agent",
		Plugins: []string{"alpha-plugin@market-a"},
	}
	agentB := &models.Agent{
		ID:      "agent-beta",
		Name:    "beta-agent",
		Plugins: []string{"beta-plugin@market-b"},
	}

	// Call for agent A
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task for alpha",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: agentA,
	})
	reqA := capture.lastReq
	if reqA.AgentDefinition == nil {
		t.Fatal("expected agent definition for agent A")
	}
	if len(reqA.AgentDefinition.Skills) != 1 || reqA.AgentDefinition.Skills[0].Name != "alpha-skill" {
		t.Fatalf("agent A should have alpha-skill only, got %+v", reqA.AgentDefinition.Skills)
	}
	if len(reqA.AgentDefinition.MCPServers) != 1 || reqA.AgentDefinition.MCPServers[0].Name != "alpha-mcp" {
		t.Fatalf("agent A should have alpha-mcp only, got %+v", reqA.AgentDefinition.MCPServers)
	}
	// Call for agent B
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task for beta",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: agentB,
	})
	reqB := capture.lastReq
	if reqB.AgentDefinition == nil {
		t.Fatal("expected agent definition for agent B")
	}
	if len(reqB.AgentDefinition.Skills) != 1 || reqB.AgentDefinition.Skills[0].Name != "beta-skill" {
		t.Fatalf("agent B should have beta-skill only, got %+v", reqB.AgentDefinition.Skills)
	}
	if len(reqB.AgentDefinition.MCPServers) != 1 || reqB.AgentDefinition.MCPServers[0].Name != "beta-mcp" {
		t.Fatalf("agent B should have beta-mcp only, got %+v", reqB.AgentDefinition.MCPServers)
	}
	// Verify no cross-contamination: A's resources don't appear in B
	for _, s := range reqB.AgentDefinition.Skills {
		if s.Name == "alpha-skill" {
			t.Fatal("agent B should NOT have alpha-skill (cross-agent leak)")
		}
	}
	for _, s := range reqB.AgentDefinition.MCPServers {
		if s.Name == "alpha-mcp" {
			t.Fatal("agent B should NOT have alpha-mcp (cross-agent leak)")
		}
	}

	// Call with nil agent definition (task with no agent) — verify zero plugin context
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task without agent",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: nil,
	})
	reqNone := capture.lastReq
	if reqNone.AgentDefinition != nil {
		t.Fatalf("task with no agent definition should have nil AgentDefinition, got %+v", reqNone.AgentDefinition)
	}
}

// TestApplyAgentToSystemPrompt_NilAgent verifies that a nil agent produces
// no plugin content in the system prompt.
func TestApplyAgentToSystemPrompt_NilAgent(t *testing.T) {
	result := ApplyAgentToSystemPrompt("base prompt", nil)
	if result != "base prompt" {
		t.Fatalf("expected unmodified base prompt, got %q", result)
	}
}

// TestApplyAgentToSystemPrompt_AgentNoSkills verifies that an agent with a
// system prompt but no skills only adds the system prompt.
func TestApplyAgentToSystemPrompt_AgentNoSkills(t *testing.T) {
	agent := &models.Agent{
		SystemPrompt: "I am a test agent",
		Skills:       nil,
	}
	result := ApplyAgentToSystemPrompt("base", agent)
	if !strings.Contains(result, "I am a test agent") {
		t.Fatalf("expected system prompt in result, got %q", result)
	}
	if !strings.Contains(result, "base") {
		t.Fatalf("expected base prompt preserved, got %q", result)
	}
	if strings.Contains(result, "Skill:") {
		t.Fatalf("should not contain skill sections, got %q", result)
	}
}

// TestApplyAgentToSystemPrompt_AgentWithSkills verifies that plugin-resolved
// skills are included in the system prompt.
func TestApplyAgentToSystemPrompt_AgentWithSkills(t *testing.T) {
	agent := &models.Agent{
		SystemPrompt: "I use plugins",
		Skills: []models.SkillConfig{
			{Name: "test-skill", Content: "Do the test thing"},
		},
	}
	result := ApplyAgentToSystemPrompt("base", agent)
	if !strings.Contains(result, "I use plugins") {
		t.Fatalf("expected system prompt, got %q", result)
	}
	if !strings.Contains(result, "test-skill") {
		t.Fatalf("expected skill name in prompt, got %q", result)
	}
	if !strings.Contains(result, "Do the test thing") {
		t.Fatalf("expected skill content in prompt, got %q", result)
	}
}
