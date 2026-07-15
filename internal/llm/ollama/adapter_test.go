package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
)

type recordingHTTPDoer struct {
	request chatRequest
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

func TestCallStreamingZeroHistoryFollowupUsesChatAssembly(t *testing.T) {
	doer := &recordingHTTPDoer{}
	adapter := New(nil, nil)
	adapter.SetHTTPClient(doer)

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
	}, ".", nil)
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
	if !strings.Contains(systemPrompt, llmprompt.ChatActionUnavailableInstructions) {
		t.Fatalf("zero-history follow-up missing capability limitation: %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, "TASK CREATION TOOL MODE") {
		t.Fatalf("zero-history follow-up received initial-task guidance: %q", systemPrompt)
	}
	if len(doer.request.Tools) != 0 {
		t.Fatalf("Ollama follow-up advertised unavailable tools: %#v", doer.request.Tools)
	}
}
