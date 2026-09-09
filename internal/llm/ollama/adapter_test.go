package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
)

type recordingHTTPDoer struct {
	request chatRequest
}

func instantOllamaRetry(t *testing.T) {
	t.Helper()
	original := retryAfter
	retryAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { retryAfter = original })
}

type retryingOllamaDoer struct {
	attempts    int
	firstErr    error
	firstStatus int
}

func (d *retryingOllamaDoer) Do(*http.Request) (*http.Response, error) {
	d.attempts++
	if d.attempts == 1 && d.firstErr != nil {
		return nil, d.firstErr
	}
	if d.attempts == 1 && d.firstStatus != 0 {
		return &http.Response{StatusCode: d.firstStatus, Body: io.NopCloser(strings.NewReader(`{"error":"temporarily unavailable"}`)), Header: make(http.Header)}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"message":{"role":"assistant","content":"retry succeeded"},"done":true,"eval_count":2}` + "\n",
		)),
		Header: make(http.Header),
	}, nil
}

func TestCallDirectRetriesTransientHTTPStatus(t *testing.T) {
	instantOllamaRetry(t)
	doer := &retryingOllamaDoer{firstStatus: http.StatusServiceUnavailable}
	adapter := New(nil, nil)
	adapter.SetHTTPClient(doer)
	result, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationDirect,
		Message:   "hello",
		Agent:     models.LLMConfig{Provider: models.ProviderOllama, Model: "test-model", OllamaBaseURL: "http://ollama.invalid"},
	}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	if doer.attempts != 2 || result.Output != "retry succeeded" {
		t.Fatalf("attempts/text = %d/%q, want 2/retry succeeded", doer.attempts, result.Output)
	}
}

func TestCallDirectRetriesNetworkTimeout(t *testing.T) {
	instantOllamaRetry(t)
	doer := &retryingOllamaDoer{firstErr: errors.New("read: operation timed out")}
	adapter := New(nil, nil)
	adapter.SetHTTPClient(doer)

	result, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationDirect,
		Message:   "hello",
		Agent: models.LLMConfig{
			Provider:      models.ProviderOllama,
			Model:         "test-model",
			OllamaBaseURL: "http://ollama.invalid",
		},
	}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	if doer.attempts != 2 || result.Output != "retry succeeded" {
		t.Fatalf("attempts/text = %d/%q, want 2/retry succeeded", doer.attempts, result.Output)
	}
}

func (d *recordingHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &d.request); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"message":{"role":"assistant","content":"follow-up response"},"done":true,"eval_count":2}` + "\n",
		)),
		Header: make(http.Header),
	}, nil
}

func TestCallStreamingTaskIncludesWorktreeRootContext(t *testing.T) {
	doer := &recordingHTTPDoer{}
	adapter := New(nil, nil)
	adapter.SetHTTPClient(doer)
	workDir := "/tmp/.worktrees/task_ollama"

	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation:           llmcontracts.OperationTask,
		Message:             "Implement the task",
		ProjectInstructions: "PROJECT_RULES_SENTINEL",
		Agent: models.LLMConfig{
			Name:          "Ollama",
			Provider:      models.ProviderOllama,
			Model:         "test-model",
			OllamaBaseURL: "http://ollama.invalid",
		},
	}, workDir, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if len(doer.request.Messages) == 0 || doer.request.Messages[0].Role != "system" {
		t.Fatalf("task request missing system message: %#v", doer.request.Messages)
	}
	systemPrompt := doer.request.Messages[0].Content
	want := "You are operating in an isolated git worktree at " + workDir + ". Treat this path as the repository root for this run."
	if !strings.Contains(systemPrompt, want) {
		t.Fatalf("task system prompt missing worktree root context %q: %q", want, systemPrompt)
	}
	if !strings.Contains(systemPrompt, "PROJECT_RULES_SENTINEL") {
		t.Fatalf("task system prompt dropped project instructions: %q", systemPrompt)
	}
}

func TestCallStreamingZeroHistoryFollowupUsesChatAssembly(t *testing.T) {
	doer := &recordingHTTPDoer{}
	adapter := New(nil, nil)
	adapter.SetHTTPClient(doer)

	workDir := "/tmp/.worktrees/task_ollama_followup"
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation:         llmcontracts.OperationStreaming,
		Message:           "Continue the task",
		Followup:          true,
		ChatMode:          models.ChatModeOrchestrate,
		ChatSystemContext: "FOLLOWUP_CONTEXT_SENTINEL",
		Agent: models.LLMConfig{
			Name:          "Ollama",
			Provider:      models.ProviderOllama,
			Model:         "test-model",
			OllamaBaseURL: "http://ollama.invalid",
		},
	}, workDir, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if len(doer.request.Messages) == 0 || doer.request.Messages[0].Role != "system" {
		t.Fatalf("zero-history follow-up missing system message: %#v", doer.request.Messages)
	}
	systemPrompt := doer.request.Messages[0].Content
	if !strings.Contains(systemPrompt, "# Task Follow-up Constraints") || !strings.Contains(systemPrompt, "FOLLOWUP_CONTEXT_SENTINEL") {
		t.Fatalf("zero-history follow-up did not use Chat assembly: %q", systemPrompt)
	}
	wantWorktreeContext := "You are operating in an isolated git worktree at " + workDir + ". Treat this path as the repository root for this run."
	if !strings.Contains(systemPrompt, wantWorktreeContext) {
		t.Fatalf("zero-history follow-up missing worktree root context %q: %q", wantWorktreeContext, systemPrompt)
	}
	if !strings.Contains(systemPrompt, llmprompt.ChatActionUnavailableInstructions) {
		t.Fatalf("zero-history follow-up missing capability limitation: %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, "TASK CREATION TOOL MODE") {
		t.Fatalf("zero-history follow-up received initial-task guidance: %q", systemPrompt)
	}
	if len(doer.request.Tools) != 0 {
		t.Fatalf("Ollama follow-up advertised unavailable tools: %#v", doer.request.Tools)
	}
	if doer.request.Options == nil || doer.request.Options.Temperature == nil || *doer.request.Options.Temperature != 0 {
		t.Fatalf("Ollama request did not preserve explicit zero temperature: %#v", doer.request.Options)
	}
}
