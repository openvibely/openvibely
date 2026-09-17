package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentplugins"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmollama "github.com/openvibely/openvibely/internal/llm/ollama"
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
		name             string
		operation        llmcontracts.Operation
		authMethod       models.AuthMethod
		apiKey           string
		oauthAccessToken string
		workDir          string
		chatMode         models.ChatMode
		followup         bool
		history          []models.Execution
		chatContext      string
	}{
		{
			name:       "direct api key",
			operation:  llmcontracts.OperationDirect,
			authMethod: models.AuthMethodAPIKey,
			apiKey:     "test-api-key",
			workDir:    "/work/direct-api-key",
		},
		{
			name:             "direct oauth",
			operation:        llmcontracts.OperationDirect,
			authMethod:       models.AuthMethodOAuth,
			oauthAccessToken: "test-oauth-token",
			workDir:          "/work/direct-oauth",
		},
		{
			name:        "streaming chat first turn",
			operation:   llmcontracts.OperationStreaming,
			authMethod:  models.AuthMethodAPIKey,
			apiKey:      "test-api-key",
			workDir:     "/work/streaming-first-turn",
			chatMode:    models.ChatModeOrchestrate,
			chatContext: "first-turn chat context sentinel",
		},
		{
			name:        "streaming chat history",
			operation:   llmcontracts.OperationStreaming,
			authMethod:  models.AuthMethodAPIKey,
			apiKey:      "test-api-key",
			workDir:     "/work/streaming-history",
			chatMode:    models.ChatModePlan,
			history:     []models.Execution{{PromptSent: "previous prompt", Output: "previous output"}},
			chatContext: "history chat context sentinel",
		},
		{
			name:             "streaming chat followup oauth",
			operation:        llmcontracts.OperationStreaming,
			authMethod:       models.AuthMethodOAuth,
			oauthAccessToken: "test-oauth-token",
			workDir:          "/work/streaming-followup-oauth",
			chatMode:         models.ChatModeOrchestrate,
			followup:         true,
			history:          []models.Execution{{PromptSent: "previous prompt", Output: "previous output"}},
			chatContext:      "followup chat context sentinel",
		},
		{
			name:       "task api key",
			operation:  llmcontracts.OperationTask,
			authMethod: models.AuthMethodAPIKey,
			apiKey:     "test-api-key",
			workDir:    "/work/task-api-key",
		},
		{
			name:             "task oauth",
			operation:        llmcontracts.OperationTask,
			authMethod:       models.AuthMethodOAuth,
			oauthAccessToken: "test-oauth-token",
			workDir:          "/work/task-oauth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lowLevel := &recordingAnthropicAdapter{}
			adapter := &anthropicProviderAdapter{adapter: lowLevel}
			req := llmcontracts.AgentRequest{
				Ctx:       context.Background(),
				Operation: tt.operation,
				Message:   "preserve this request",
				Agent: models.LLMConfig{
					Provider:         models.ProviderAnthropic,
					AuthMethod:       tt.authMethod,
					APIKey:           tt.apiKey,
					OAuthAccessToken: tt.oauthAccessToken,
				},
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

func TestNativeCompactionCapabilityRequiresConcreteSupportedConfiguration(t *testing.T) {
	if !providerSupportsNativeCompaction(models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol", AuthMethod: models.AuthMethodAPIKey}) {
		t.Fatal("supported OpenAI configuration was rejected")
	}
	if providerSupportsNativeCompaction(models.LLMConfig{Provider: models.ProviderOpenAI, Model: "", AuthMethod: models.AuthMethodAPIKey}) {
		t.Fatal("blank model must not advertise native compaction")
	}
	if providerSupportsNativeCompaction(models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-sonnet-5", AuthMethod: models.AuthMethodAPIKey, Transport: "chat_completions"}) {
		t.Fatal("incompatible Anthropic transport must not advertise context management")
	}
	if providerSupportsNativeCompaction(models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-sonnet-5", AuthMethod: models.AuthMethodCLI}) {
		t.Fatal("retired CLI auth must not advertise native compaction")
	}
}

func TestProviderContextBudget_TestProviderFailsClosedWithoutArtifactReader(t *testing.T) {
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		return llmcontracts.AgentResult{}, nil
	})
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationTask,
		Message: strings.Repeat("!", 20000), ExecID: "test-provider-artifact", WorkDir: t.TempDir(),
		Agent: models.LLMConfig{Provider: models.ProviderTest, Model: "test", ContextWindow: 4096},
	}
	_, err := svc.callProviderWithContextCompactionFallback(adapter, req)
	if err == nil || !llmcontracts.ErrorIs(err, llmcontracts.ErrorPendingInputInfeasible) {
		t.Fatalf("error = %v, want pending input infeasible", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want zero", calls)
	}
}

func TestProviderContextBudget_OversizedPendingInputIsExternalizedWithoutCompactingSmallHistory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENVIBELY_APP_DATA_DIR", root)
	original := "diagnose this log\n" + strings.Repeat("x", 2_664_043-len("diagnose this log\n"))
	var got llmcontracts.AgentRequest
	var artifactData []byte
	var artifactMode os.FileMode
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		got = req
		artifact := filepath.Join(root, "task-inputs", "oversized-exec", "prompt.txt")
		artifactData, _ = os.ReadFile(artifact)
		if info, err := os.Stat(artifact); err == nil {
			artifactMode = info.Mode().Perm()
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming,
		Message: original, ExecID: "oversized-exec", WorkDir: t.TempDir(), Followup: true,
		Agent:       models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", ContextWindow: 272000},
		ChatHistory: []models.Execution{{ID: "old", PromptSent: "small history", Output: "small answer", Status: models.ExecCompleted}},
	}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want one ordinary request", calls)
	}
	if got.ForceNativeCompaction || got.Agent.ForceNativeCompaction {
		t.Fatal("small history must not be compacted because only pending input is oversized")
	}
	if got.Message == original || !strings.Contains(got.Message, "Full input:") || !strings.Contains(got.Message, "2664043") {
		t.Fatalf("model-facing prompt was not externalized: %q", got.Message)
	}
	artifact := filepath.Join(root, "task-inputs", "oversized-exec", "prompt.txt")
	if string(artifactData) != original {
		t.Fatalf("artifact bytes during provider call = %d, want complete original %d", len(artifactData), len(original))
	}
	if artifactMode != 0o600 {
		t.Fatalf("artifact mode = %v, want 0600", artifactMode)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact must be cleaned after provider call, stat err=%v", err)
	}
	if estimateModelVisibleRequestTokens(got) > requestBudgetForAgent(got.Agent).SafeInputLimit {
		t.Fatal("externalized request still exceeds safe input limit")
	}
}

func TestProviderContextBudget_LargeHistoryAndOversizedPendingAreHandledSeparately(t *testing.T) {
	t.Setenv("OPENVIBELY_APP_DATA_DIR", t.TempDir())
	original := strings.Repeat("L", 2_664_043)
	var requests []llmcontracts.AgentRequest
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		requests = append(requests, req)
		if err := ensureRequestFits(req, "test provider-bound request"); err != nil {
			t.Fatalf("oversized provider request %d: %v", len(requests), err)
		}
		if req.Operation == llmcontracts.OperationDirect {
			return llmcontracts.AgentResult{Output: "bounded history summary"}, nil
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming,
		Message: original, ExecID: "both-large", WorkDir: t.TempDir(), Followup: true,
		Agent:       models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "local-compatible", ContextWindow: 50000},
		ChatHistory: []models.Execution{{ID: "old", PromptSent: strings.Repeat("history", 12000), Output: strings.Repeat("tool", 12000), Status: models.ExecCompleted}},
	}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Operation != llmcontracts.OperationDirect || requests[1].Operation != llmcontracts.OperationStreaming {
		t.Fatalf("requests = %#v, want bounded summary then continuation", requests)
	}
	if requests[1].Message == original || !strings.Contains(requests[1].Message, "Full input:") {
		t.Fatal("oversized pending input was not handled independently after history compaction")
	}
	if requests[1].ForceNativeCompaction || requests[1].Agent.ForceNativeCompaction {
		t.Fatal("local summary retry retained stale native compaction force")
	}
}

func TestProviderContextBudget_NoProviderCallWhenPendingInputCannotBeExternalized(t *testing.T) {
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		return llmcontracts.AgentResult{}, nil
	})
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	_, err := svc.callProviderWithContextCompactionFallback(adapter, llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming,
		Message: strings.Repeat("!", 20000), ExecID: "no-artifact", DisableTools: true,
		Agent: models.LLMConfig{Provider: models.ProviderOllama, ContextWindow: 4096},
	})
	if !llmcontracts.ErrorIs(err, llmcontracts.ErrorPendingInputInfeasible) {
		t.Fatalf("error = %v, want pending-input infeasible", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want zero", calls)
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
	if got := logBuf.String(); !strings.Contains(got, "strategy=last_resort") || !strings.Contains(got, "failure_category=context_window_exceeded") {
		t.Fatalf("structured last-resort decision log missing: %s", got)
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
		t.Fatalf("UTF-8 token estimate = %d, want rounded bytes/4", got)
	}
	if got := estimatedUTF8Tokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII token estimate = %d, want rounded bytes/4", got)
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
	if got := retained[0].PromptSent; !strings.Contains(got, "omitted") {
		t.Fatalf("boundary message was not visibly middle-truncated: %q", got)
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
	checkpointAgent := models.LLMConfig{ID: "model", Provider: models.ProviderOpenAI}
	if err := repo.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{
		ScopeType: "chat_project", ScopeID: "fallback", ModelConfigID: "model", CompatibilityKey: providerCompatibilityKey(checkpointAgent),
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

	openAIBudget := requestBudgetForAgent(models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex"})
	if openAIBudget.ReservedOutputTokens != 16384 || openAIBudget.SafeInputLimit != 244800 {
		t.Fatalf("OpenAI request budget = %+v", openAIBudget)
	}
	anthropicBudget := requestBudgetForAgent(models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-5", ContextWindow: 200000})
	if anthropicBudget.ReservedOutputTokens != 20000 || anthropicBudget.SafetyMargin != 3000 || anthropicBudget.SafeInputLimit != 177000 {
		t.Fatalf("Anthropic request budget = %+v, want Claude-compatible compaction headroom", anthropicBudget)
	}
	if limits := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-5", ContextWindow: 200000}); limits.AutoLimit != 167000 || limits.TriggerLimit != 167000 || limits.EffectiveHardLimit != 177000 {
		t.Fatalf("Anthropic compaction limits = %+v, want trigger=167000 hard=177000", limits)
	}
	if limits := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-5", ContextWindow: 200000, CompactionThreshold: 20000}); limits.TriggerLimit != 50000 {
		t.Fatalf("Anthropic configured threshold = %+v, want minimum 50000", limits)
	}
	attachmentBudget := calculateRequestBudget(llmcontracts.AgentRequest{Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 10000}, Attachments: []models.Attachment{{FileSize: 4000}}})
	if attachmentBudget.AttachmentTokens != 1000 {
		t.Fatalf("attachment tokens = %d, want rounded bytes/4", attachmentBudget.AttachmentTokens)
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
		t.Fatalf("trigger=%v used=%d, want bytes/4 estimate", triggered, used)
	}
	req.Message = strings.Repeat("a", 3800)
	req.Agent.CompactionThreshold = 2000
	triggered, _, used = shouldTriggerContextCompaction(req)
	if !triggered || used != 950 {
		t.Fatalf("trigger=%v used=%d, want bytes/4 hard-limit estimate", triggered, used)
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file", Description: strings.Repeat("tool", 200), Parameters: []byte(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessWrite}}}
	req = llmcontracts.AgentRequest{Ctx: llmcontracts.WithRuntimeTools(context.Background(), rt), Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate, Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 1000}, Message: strings.Repeat("a", 2400)}
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
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "small pending", Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test", ContextWindow: 10000, CompactionThreshold: 12000}, ChatHistory: []models.Execution{{PromptSent: strings.Repeat("old", 8000), Output: "done"}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("callProviderWithContextCompactionFallback: %v", err)
	}
	if got.Operation != llmcontracts.OperationStreaming || got.Agent.DisableNativeCompaction {
		t.Fatalf("native provider should be called directly with native compaction enabled, got %#v", got)
	}
	if !got.ForceNativeCompaction || !got.Agent.ForceNativeCompaction {
		t.Fatalf("native full-request trigger should force provider-native compaction, got request=%v agent=%v", got.ForceNativeCompaction, got.Agent.ForceNativeCompaction)
	}
	if got.Agent.CompactionThreshold != 7300 || got.NativeCompactionTokenThreshold != 7300 {
		t.Fatalf("native threshold = agent:%d request:%d, want safe input limit 7300", got.Agent.CompactionThreshold, got.NativeCompactionTokenThreshold)
	}

	got = llmcontracts.AgentRequest{}
	req = llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "small pending", ContextTokenEstimate: 170000, Agent: models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-5", ContextWindow: 200000}, ChatHistory: []models.Execution{{PromptSent: "old context", Output: "done"}}}
	if _, err := svc.callProviderWithContextCompactionFallback(adapter, req); err != nil {
		t.Fatalf("Anthropic native compaction call: %v", err)
	}
	if !got.ForceNativeCompaction || got.Agent.CompactionThreshold != 167000 || got.NativeCompactionTokenThreshold != 167000 {
		t.Fatalf("Anthropic native compaction request = %#v, want 167k provider trigger", got)
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
		{ID: "old", PromptSent: strings.Repeat("o", 28000), Output: "old", Status: models.ExecCompleted},
		{ID: "source", PromptSent: strings.Repeat("n", 28000), Output: "new", Status: models.ExecCompleted},
	}
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: strings.Repeat("m", 80), Agent: models.LLMConfig{ID: "model-1", Provider: models.ProviderOpenAICompatible, Model: "compat", ContextWindow: 20000}, ProjectID: "project-1", ChatHistory: history}
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

func TestProviderSessionState_PersistsWithoutReplacingCompactionCheckpoint(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewExecutionRepo(db)
	checkpointHistory := []models.Execution{{Output: compactedHistorySummaryPrefix + "\n\nsummary", Status: models.ExecCompleted}}
	agent := models.LLMConfig{ID: "astra-model", Provider: models.ProviderOpenAI, Model: "gpt-6-astra"}
	if err := repo.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{
		ScopeType: "chat_project", ScopeID: "astra-project", ModelConfigID: agent.ID,
		CompatibilityKey: providerCompatibilityKey(agent), SourceExecutionID: "source",
		History: checkpointHistory, Summary: "summary", Strategy: "local_summary", ProviderStateJSON: `[{"type":"compaction"}]`,
	}); err != nil {
		t.Fatalf("UpsertChatCompactionCheckpoint: %v", err)
	}

	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	state := `{"version":1,"model":"gpt-6-astra","request_effort":"medium","configured_effort":"high"}`
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ProjectID: "astra-project", Agent: agent}
	svc.persistProviderSessionState(req, state)

	checkpoint, err := repo.GetChatCompactionCheckpoint(context.Background(), "chat_project", "astra-project")
	if err != nil {
		t.Fatalf("GetChatCompactionCheckpoint: %v", err)
	}
	if checkpoint == nil || checkpoint.ProviderSessionStateJSON != state || checkpoint.ProviderStateJSON != `[{"type":"compaction"}]` || len(checkpoint.History) != 1 || checkpoint.Summary != "summary" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	restored := svc.restoreCompactionCheckpoint(req)
	if restored.ProviderSessionStateJSON != state || restored.NativeCompactionStateJSON != `[{"type":"compaction"}]` || len(restored.ChatHistory) != 1 {
		t.Fatalf("restored request = %#v", restored)
	}

	otherAgent := models.LLMConfig{ID: "other-astra-model", Provider: models.ProviderOpenAI, Model: "gpt-6-astra"}
	otherReq := req
	otherReq.Agent = otherAgent
	svc.persistProviderSessionState(otherReq, `{"version":1,"model":"gpt-6-astra","request_effort":"low","configured_effort":"low"}`)
	checkpoint, err = repo.GetChatCompactionCheckpoint(context.Background(), "chat_project", "astra-project")
	if err != nil {
		t.Fatalf("GetChatCompactionCheckpoint after incompatible state: %v", err)
	}
	if checkpoint == nil || checkpoint.ProviderStateJSON != "" || len(checkpoint.History) != 0 || checkpoint.Summary != "" {
		t.Fatalf("incompatible session state retained compaction checkpoint: %#v", checkpoint)
	}
}

func TestProviderContextCompactionFallback_NativeFailureFallsBackAndCachesUnsupported(t *testing.T) {
	model := "native-unsupported-test"
	knownUnsupportedNativeCompaction.Delete(nativeCompactionSessionKey(models.LLMConfig{Provider: models.ProviderOpenAI, Model: model}))
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
	checkpointAgent := models.LLMConfig{ID: "model-1", Provider: models.ProviderOpenAI}
	if err := execRepo.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{
		ScopeType: "chat_project", ScopeID: "project-native-restore", ModelConfigID: "model-1", CompatibilityKey: providerCompatibilityKey(checkpointAgent),
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

func TestPrepareAgentRuntimeRequest_NilAgentPreservesRequest(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{}, nil
	}

	req := llmcontracts.AgentRequest{
		Ctx:                 context.Background(),
		ChatSystemContext:   "chat context",
		ProjectInstructions: "project instructions",
	}
	got := prepareAgentRuntimeRequest(req)
	if got.AgentDefinition != nil {
		t.Fatalf("AgentDefinition = %#v, want nil", got.AgentDefinition)
	}
	if got.ChatSystemContext != req.ChatSystemContext || got.ProjectInstructions != req.ProjectInstructions {
		t.Fatalf("prompt contexts changed: got chat=%q project=%q", got.ChatSystemContext, got.ProjectInstructions)
	}
	if called {
		t.Fatal("plugin resolver should not be called for a nil agent definition")
	}
}

func TestPrepareAgentRuntimeRequest_MergesRuntimeAndInjectsBothContexts(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		if len(pluginIDs) != 1 || pluginIDs[0] != "selected-plugin@market" {
			t.Fatalf("plugin IDs = %v, want selected-plugin@market", pluginIDs)
		}
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "selected-skill", Content: "selected skill content"}},
		}, nil
	}

	agentDef := &models.Agent{
		Name:         "runtime-agent",
		SystemPrompt: "agent system prompt",
		Skills:       []models.SkillConfig{{Name: "saved-skill", Content: "saved skill content"}},
		Plugins:      []string{"selected-plugin@market"},
	}
	req := llmcontracts.AgentRequest{
		Ctx:                 context.Background(),
		AgentDefinition:     agentDef,
		ChatSystemContext:   "chat context",
		ProjectInstructions: "project instructions",
	}

	got := prepareAgentRuntimeRequest(req)
	if got.AgentDefinition == agentDef {
		t.Fatal("expected runtime resolution to provide a merged agent definition")
	}
	if got.AgentDefinition == nil || len(got.AgentDefinition.Skills) != 2 || got.AgentDefinition.Skills[1].Name != "selected-skill" {
		t.Fatalf("merged AgentDefinition = %#v", got.AgentDefinition)
	}

	wantChat := "agent system prompt\n\n---\n\n## Skill: saved-skill\n\nsaved skill content\n\n---\n\n## Skill: selected-skill\n\nselected skill content\n\n---\n\nchat context"
	wantProject := "agent system prompt\n\n---\n\n## Skill: saved-skill\n\nsaved skill content\n\n---\n\n## Skill: selected-skill\n\nselected skill content\n\n---\n\nproject instructions"
	if got.ChatSystemContext != wantChat {
		t.Fatalf("ChatSystemContext = %q, want %q", got.ChatSystemContext, wantChat)
	}
	if got.ProjectInstructions != wantProject {
		t.Fatalf("ProjectInstructions = %q, want %q", got.ProjectInstructions, wantProject)
	}
}

func TestPrepareAgentRuntimeRequest_ResolverErrorKeepsRawAgent(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		return nil, fmt.Errorf("resolver unavailable")
	}

	agentDef := &models.Agent{
		SystemPrompt: "raw system prompt",
		Plugins:      []string{"selected-plugin@market"},
	}
	got := prepareAgentRuntimeRequest(llmcontracts.AgentRequest{
		Ctx:                 context.Background(),
		AgentDefinition:     agentDef,
		ChatSystemContext:   "chat context",
		ProjectInstructions: "project instructions",
	})
	if got.AgentDefinition != agentDef {
		t.Fatalf("AgentDefinition = %#v, want raw agent definition", got.AgentDefinition)
	}
	if got.ChatSystemContext != "raw system prompt\n\n---\n\nchat context" || got.ProjectInstructions != "raw system prompt\n\n---\n\nproject instructions" {
		t.Fatalf("resolver-error prompt contexts = chat=%q project=%q", got.ChatSystemContext, got.ProjectInstructions)
	}
}

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

func TestRequestPreflightUsesFinalResolvedAgentRuntime(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()
	resolvePluginRuntimeBundleFn = func(context.Context, []string) (*agentplugins.RuntimeBundle, error) {
		return &agentplugins.RuntimeBundle{Skills: []models.SkillConfig{{Name: "large", Content: strings.Repeat("x", 900)}}, PluginIDs: []string{"plugin@test"}}, nil
	}
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		return llmcontracts.AgentResult{Output: "unexpected"}, nil
	})
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationTask, Message: "small",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "tiny", ContextWindow: 1000},
		AgentDefinition: &models.Agent{Plugins: []string{"plugin@test"}, SystemPrompt: strings.Repeat("s", 600)},
	}
	_, err := (&LLMService{}).callProviderWithCompaction(adapter, req)
	if err == nil || calls != 0 || (!llmcontracts.ErrorIs(err, llmcontracts.ErrorContextWindowExceeded) && !llmcontracts.ErrorIs(err, llmcontracts.ErrorPendingInputInfeasible)) {
		t.Fatalf("err=%v calls=%d, want typed local rejection before adapter", err, calls)
	}
}

func TestOversizedInputArtifactHasAuthorizedBoundedReaderAndIsCleaned(t *testing.T) {
	root := t.TempDir()
	svc := &LLMService{globalSkillRoot: root}
	var artifactPath string
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
		if rt == nil || !rt.HasDefinition(oversizedInputReaderTool) || rt.Executor == nil {
			t.Fatal("oversized input reader was not authorized")
		}
		artifactPath = filepath.Join(root, "task-inputs", req.ExecID, "prompt.txt")
		out, handled, isError, err := rt.Executor(req.Ctx, oversizedInputReaderTool, []byte(`{"offset":10,"limit":32}`))
		if err != nil || !handled || isError || out != strings.Repeat("z", 32) {
			t.Fatalf("reader output=%q handled=%v isError=%v err=%v", out, handled, isError, err)
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationTask, ExecID: "exec-artifact", WorkDir: t.TempDir(), Message: strings.Repeat("z", 60000), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 20000}}
	if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("artifact must be removed after provider/tool loop, stat err=%v", err)
	}
}

func TestNativeCompactionCapabilityCacheIsConfigurationScoped(t *testing.T) {
	a := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "same", BaseURL: "https://one.example/v1", AuthMethod: models.AuthMethodAPIKey, APIKey: "key-one"}
	b := a
	b.BaseURL = "https://two.example/v1"
	b.APIKey = "key-two"
	if nativeCompactionSessionKey(a) == nativeCompactionSessionKey(b) {
		t.Fatal("different endpoint/auth configurations shared native capability key")
	}
}

func TestNativeCheckpointRequiresCompleteCompatibilityIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewExecutionRepo(db)
	base := models.LLMConfig{ID: "same-id", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", BaseURL: "https://one.example/v1", AuthMethod: models.AuthMethodAPIKey, APIKey: "one"}
	if err := repo.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{ScopeType: "chat_project", ScopeID: "identity", ModelConfigID: base.ID, CompatibilityKey: providerCompatibilityKey(base), ProviderStateJSON: `[{"type":"compaction"}]`}); err != nil {
		t.Fatal(err)
	}
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	changed := base
	changed.BaseURL = "https://two.example/v1"
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ProjectID: "identity", Agent: changed, ChatHistory: []models.Execution{{ID: "source", PromptSent: "original"}}}
	got := svc.restoreCompactionCheckpoint(req)
	if got.NativeCompactionStateJSON != "" || len(got.ChatHistory) != 1 {
		t.Fatalf("incompatible opaque state restored: %#v", got)
	}
}

func TestProviderContextWindowsIncludeSparkAndConservativeOllamaDefault(t *testing.T) {
	if got := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex-spark"}).ContextWindow; got != 128000 {
		t.Fatalf("spark context window = %d, want 128000", got)
	}
	if got := compactionLimitsForAgent(models.LLMConfig{Provider: models.ProviderOllama, Model: "arbitrary-local"}).ContextWindow; got != llmollama.DefaultContextWindow {
		t.Fatalf("default Ollama context window = %d, want %d", got, llmollama.DefaultContextWindow)
	}
}

func TestLocalSummaryCompactionEmptyOutputIsCategorized(t *testing.T) {
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{}, nil
	})
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "compatible"},
	}
	_, err := (&LLMService{}).localSummaryCompaction(adapter, req, []models.Execution{{PromptSent: "history"}}, nil)
	if !llmcontracts.ErrorIs(err, llmcontracts.ErrorLocalCompactionFailed) {
		t.Fatalf("empty summary error = %v, want %s", err, llmcontracts.ErrorLocalCompactionFailed)
	}
}

func TestContextRecoveryDispatchesEmitCompleteStructuredDecisions(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodAPIKey, APIKey: "fixture", ContextWindow: 50000}
	knownUnsupportedNativeCompaction.Delete(nativeCompactionSessionKey(agent))
	defer knownUnsupportedNativeCompaction.Delete(nativeCompactionSessionKey(agent))
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		switch calls {
		case 1:
			return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionUnsupported, "fixture native", errors.New("private native detail"))
		case 2:
			if req.Operation != llmcontracts.OperationDirect {
				t.Fatalf("call 2 operation = %s, want textual summary", req.Operation)
			}
			return llmcontracts.AgentResult{Output: "bounded summary", TextOnlyOutput: "bounded summary"}, nil
		case 3:
			return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "fixture retry", errors.New("private retry detail"))
		default:
			return llmcontracts.AgentResult{Output: "recovered"}, nil
		}
	})
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "current", Followup: true,
		RetrySourceExecutionID: "failed-source", Agent: agent, DisableTools: true,
		ChatHistory: []models.Execution{{ID: "old", PromptSent: strings.Repeat("history", 7000), Output: "done", Status: models.ExecCompleted}},
	}
	if _, err := (&LLMService{}).callProviderWithCompaction(adapter, req); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("provider calls = %d, want native, summary, compacted retry, last resort", calls)
	}
	got := logs.String()
	for _, fields := range [][]string{
		{"strategy=textual_summary", "failure_category=native_compaction_unsupported"},
		{"strategy=local_summary_retry", "failure_category=native_compaction_unsupported"},
		{"strategy=last_resort", "failure_category=context_window_exceeded"},
	} {
		for _, field := range fields {
			if !strings.Contains(got, field) {
				t.Fatalf("recovery observability missing %q: %s", field, got)
			}
		}
	}
	for _, field := range []string{"transport=responses_http", "context_window=50000", "safe_input_limit=", "fixed_tokens=", "history_tokens=", "pending_tokens=", "attachment_tokens=", "reserved_output_tokens=", "safety_margin=", "externalized=false", "history_retained=", "history_removed=", "retry_source_execution_id=failed-source"} {
		if !strings.Contains(got, field) {
			t.Fatalf("recovery observability missing %q: %s", field, got)
		}
	}
	if strings.Contains(got, "private native detail") || strings.Contains(got, "private retry detail") {
		t.Fatalf("recovery observability leaked provider errors: %s", got)
	}
}

func TestContextDecisionObservabilityIncludesRequiredFields(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorTransportFailure, "fixture", errors.New("private failure detail"))
	})
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "current", Followup: true,
		RetrySourceExecutionID: "failed-source", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "compatible", ContextWindow: 128000},
		ChatHistory: []models.Execution{{ID: "older", PromptSent: "old", Output: "done"}},
	}
	_, _ = (&LLMService{}).callProviderWithCompaction(adapter, req)
	got := logs.String()
	for _, field := range []string{"transport=chat_completions", "history_retained=1", "history_removed=0", "retry_source_execution_id=failed-source", "failure_category=transport_failure"} {
		if !strings.Contains(got, field) {
			t.Fatalf("context observability missing %q: %s", field, got)
		}
	}
	if strings.Contains(got, "private failure detail") {
		t.Fatalf("context decision logs included raw provider error: %s", got)
	}
}

// BenchmarkProviderContextBudgetPreflight compares the historical repeated-budget
// preflight with the optimized production path without involving a provider or
// network. Both paths receive the same request fixture; history_bytes_traversed
// reports the bytes visited by the budget scans rather than the fixture size.
func BenchmarkProviderContextBudgetPreflight(b *testing.B) {
	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogWriter)

	for _, historySize := range []int{0, 20, 100} {
		fixture := newProviderContextBudgetBenchmarkFixture(historySize)
		b.Run(fmt.Sprintf("%d_history/baseline", historySize), func(b *testing.B) {
			adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				return llmcontracts.AgentResult{Output: "ok"}, nil
			})
			svc := &LLMService{}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchmarkProviderContextBudgetBaseline(svc, adapter, fixture.req)
			}
			b.StopTimer()
			b.ReportMetric(float64(fixture.historyBytes*5), "history_bytes_traversed/op")
		})
		b.Run(fmt.Sprintf("%d_history/optimized", historySize), func(b *testing.B) {
			adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				return llmcontracts.AgentResult{Output: "ok"}, nil
			})
			svc := &LLMService{}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.callProviderWithCompaction(adapter, fixture.req); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(fixture.historyBytes), "history_bytes_traversed/op")
		})
	}
}

type providerContextBudgetBenchmarkFixture struct {
	req          llmcontracts.AgentRequest
	historyBytes int
}

func newProviderContextBudgetBenchmarkFixture(historySize int) providerContextBudgetBenchmarkFixture {
	const (
		promptSize = 512
		outputSize = 512
		errorSize  = 256
		reasonSize = 256
		replaySize = 256
	)
	const repeatedHistoryFields = promptSize + outputSize + errorSize + reasonSize + 4*replaySize
	text := func(size int, ch byte) string { return strings.Repeat(string(ch), size) }
	history := make([]models.Execution, historySize)
	historyBytes := 0
	for i := range history {
		exec := models.Execution{
			ID:               fmt.Sprintf("benchmark-execution-%03d", i),
			PromptSent:       text(promptSize, 'p'),
			Output:           text(outputSize, 'o'),
			ErrorMessage:     text(errorSize, 'e'),
			ReasoningContent: text(reasonSize, 'r'),
			Status:           models.ExecCompleted,
			ReplayMessages: []models.ExecutionReplayMessage{{
				UserContent:      text(replaySize, 'u'),
				AssistantContent: text(replaySize, 'a'),
				ReasoningContent: text(replaySize, 'q'),
				TranscriptJSON:   text(replaySize, 't'),
			}},
		}
		history[i] = exec
		historyBytes += repeatedHistoryFields
	}
	runtimeTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{
		Name:        "read_file",
		Description: strings.Repeat("read-only runtime tool description ", 8),
		Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Access:      llmcontracts.RuntimeToolAccessRead,
	}}}
	return providerContextBudgetBenchmarkFixture{
		req: llmcontracts.AgentRequest{
			Ctx:       llmcontracts.WithRuntimeTools(context.Background(), runtimeTools),
			Operation: llmcontracts.OperationStreaming,
			Followup:  true,
			Message:   "Continue the current request with the next bounded step.",
			Agent: models.LLMConfig{
				Provider:      models.ProviderOpenAICompatible,
				Model:         "benchmark-model",
				ContextWindow: 2000000,
			},
			Attachments: []models.Attachment{{
				FileName: "context.txt", FilePath: "/tmp/context.txt", MediaType: "text/plain", FileSize: 4096,
			}},
			ChatHistory: history,
		},
		historyBytes: historyBytes,
	}
}

func benchmarkProviderContextBudgetBaseline(svc *LLMService, adapter ProviderAdapter, req llmcontracts.AgentRequest) {
	req = resolveProviderRequestForBudget(req)
	originalHistory := append([]models.Execution(nil), req.ChatHistory...)
	budget := calculateRequestBudget(req)
	if req.ContextTokenEstimate > 0 {
		reportedHistory := req.ContextTokenEstimate - budget.FixedTokens - budget.PendingTokens - budget.AttachmentTokens
		if reportedHistory > budget.HistoryTokens {
			budget.HistoryTokens = reportedHistory
		}
	}
	_ = historyNeedsCompaction(budget, compactionLimitsForAgent(req.Agent).TriggerLimit)
	prepared, _, cleanup, err := svc.preparePendingInput(req)
	if err != nil {
		return
	}
	cleanup()
	req = prepared
	_ = calculateRequestBudget(req)
	if err := ensureRequestFits(req, "provider request"); err != nil {
		return
	}
	logContextDecision(originalHistory, req, "none", false, nil)
	_, _ = adapter.Call(req)
}

// BenchmarkProviderContextBudgetRecoveryPaths keeps the request mutations that
// must invalidate a snapshot measurable without mixing their provider calls
// with network latency. Each adapter is an in-process fixture.
func BenchmarkProviderContextBudgetRecoveryPaths(b *testing.B) {
	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogWriter)

	b.Run("pending_externalization", func(b *testing.B) {
		svc := &LLMService{globalSkillRoot: b.TempDir()}
		adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			return llmcontracts.AgentResult{Output: "ok"}, nil
		})
		req := llmcontracts.AgentRequest{
			Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
			Message: strings.Repeat("pending input ", 4000), ExecID: "benchmark-pending", WorkDir: "/tmp/work",
			Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", ContextWindow: 32768},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(len(req.Message)), "pending_input_bytes/op")
	})

	b.Run("native_compaction", func(b *testing.B) {
		svc := &LLMService{}
		adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			if !req.ForceNativeCompaction || !req.Agent.ForceNativeCompaction {
				return llmcontracts.AgentResult{}, errors.New("native compaction was not triggered")
			}
			return llmcontracts.AgentResult{Output: "ok"}, nil
		})
		req := llmcontracts.AgentRequest{
			Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
			Message: "continue", Agent: models.LLMConfig{
				Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol", AuthMethod: models.AuthMethodAPIKey, ContextWindow: 20000,
			},
			ChatHistory: []models.Execution{{PromptSent: strings.Repeat("history ", 30000), Output: "previous"}},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(len(req.ChatHistory[0].PromptSent)), "history_bytes/op")
	})

	b.Run("local_summary_retry", func(b *testing.B) {
		svc := &LLMService{}
		calls := 0
		adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			phase := calls % 3
			calls++
			if phase == 0 {
				return llmcontracts.AgentResult{}, errors.New("context length exceeded")
			}
			if req.Operation == llmcontracts.OperationDirect {
				return llmcontracts.AgentResult{Output: "summary"}, nil
			}
			return llmcontracts.AgentResult{Output: "ok"}, nil
		})
		req := llmcontracts.AgentRequest{
			Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
			Message: "continue", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "benchmark"},
			ChatHistory: []models.Execution{{PromptSent: strings.Repeat("history ", 200), Output: "previous"}},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(len(req.ChatHistory[0].PromptSent)), "history_bytes/op")
	})

	b.Run("last_resort_truncation", func(b *testing.B) {
		svc := &LLMService{}
		calls := 0
		adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			phase := calls % 3
			calls++
			if phase == 0 {
				return llmcontracts.AgentResult{}, errors.New("context length exceeded")
			}
			if req.Operation == llmcontracts.OperationDirect {
				return llmcontracts.AgentResult{}, errors.New("summary provider unavailable")
			}
			return llmcontracts.AgentResult{Output: "ok"}, nil
		})
		history := make([]models.Execution, 25)
		for i := range history {
			history[i] = models.Execution{PromptSent: fmt.Sprintf("prompt-%02d %s", i, strings.Repeat("history ", 100)), Output: "previous"}
		}
		req := llmcontracts.AgentRequest{
			Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
			Message: "continue", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "benchmark", ContextWindow: 100000},
			ChatHistory: history,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(len(history)), "history_entries/op")
	})
}
