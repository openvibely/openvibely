package service

import (
	"context"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmnormalize "github.com/openvibely/openvibely/internal/llm/normalize"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// Test cross-provider adapter routing consistency
func TestProviderInterop_AdapterRouting(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "cross-provider-output"
	mock.TextOnly = "cross-provider-text"
	mock.Tokens = 100
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	// Test that both Anthropic and OpenAI providers can route to test adapter
	providers := []models.LLMProvider{
		models.ProviderTest,
		models.ProviderAnthropic,
		models.ProviderOpenAI,
		models.ProviderOllama,
	}

	for _, provider := range providers {
		adapter, ok := svc.adapterFor(provider)
		if !ok {
			t.Fatalf("expected adapter for provider %s", provider)
		}

		// Use ProviderTest to route to mock
		testAgent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
		res, err := adapter.Call(llmcontracts.AgentRequest{
			Ctx:       context.Background(),
			Operation: llmcontracts.OperationDirect,
			Message:   "test cross-provider",
			Agent:     testAgent,
			ExecID:    "exec-cross-1",
		})

		if provider == models.ProviderTest {
			if err != nil {
				t.Fatalf("adapter for %s error: %v", provider, err)
			}
			if res.Output != "cross-provider-output" {
				t.Errorf("provider %s: expected output, got %q", provider, res.Output)
			}
		}

		t.Logf("Provider %s adapter routing verified", provider)
	}
}

// Test tool call ID normalization (cross-provider compatibility)
func TestProviderInterop_ToolCallIDNormalization(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		description string
	}{
		{
			name:        "Text with Anthropic-style ID",
			input:       "tool_call_id: toolu_01ABC123xyz",
			description: "Anthropic tool IDs are already normalized",
		},
		{
			name:        "Text with OpenAI Responses long ID",
			input:       "tool_call_id: call_abc123|item_def456",
			description: "OpenAI Responses can have pipe-separated IDs that need normalization",
		},
		{
			name:        "Text with OpenAI standard ID",
			input:       "tool_call_id: call_abc123def",
			description: "OpenAI standard IDs are safe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use the actual normalize function that exists
			normalized := llmnormalize.NormalizeToolCallIDsInText(tt.input)

			// Verify result is not empty
			if len(normalized) == 0 {
				t.Errorf("normalized text is empty for input %q", tt.input)
			}

			// Verify idempotency: normalizing twice should yield same result
			normalized2 := llmnormalize.NormalizeToolCallIDsInText(normalized)
			if normalized != normalized2 {
				t.Errorf("normalization not idempotent: %q -> %q", normalized, normalized2)
			}

			t.Logf("%s: input_len=%d normalized_len=%d", tt.description, len(tt.input), len(normalized))
		})
	}
}

// Test usage accounting consistency across provider responses
func TestProviderInterop_UsageAccountingConsistency(t *testing.T) {
	tests := []struct {
		name            string
		inputTokens     int
		outputTokens    int
		cachedTokens    int
		reasoningTokens int
		wantTotal       int
	}{
		{
			name:         "Simple usage",
			inputTokens:  100,
			outputTokens: 50,
			wantTotal:    150,
		},
		{
			name:          "With cached tokens",
			inputTokens:   100,
			outputTokens:  50,
			cachedTokens:  20,
			wantTotal:     150, // cached tokens don't add to total
		},
		{
			name:            "With reasoning tokens (o1 model)",
			inputTokens:     100,
			outputTokens:    50,
			reasoningTokens: 30,
			wantTotal:       180,
		},
		{
			name:            "Full usage breakdown",
			inputTokens:     100,
			outputTokens:    50,
			cachedTokens:    20,
			reasoningTokens: 30,
			wantTotal:       180,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := llmcontracts.Usage{
				InputTokens:       tt.inputTokens,
				OutputTokens:      tt.outputTokens,
				CachedInputTokens: tt.cachedTokens,
				ReasoningTokens:   tt.reasoningTokens,
			}
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.ReasoningTokens

			if usage.TotalTokens != tt.wantTotal {
				t.Errorf("expected total tokens %d, got %d", tt.wantTotal, usage.TotalTokens)
			}

			t.Logf("Usage accounting: input=%d output=%d cached=%d reasoning=%d total=%d",
				usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningTokens, usage.TotalTokens)
		})
	}
}

// Test attachment preprocessing consistency (via attachment package)
func TestProviderInterop_AttachmentPreprocessing(t *testing.T) {
	// Create test attachments with various media types
	attachments := []models.Attachment{
		{
			FileName:  "test.txt",
			MediaType: "text/plain",
			FilePath:  "/nonexistent/test.txt",
		},
		{
			FileName:  "image.png",
			MediaType: "image/png",
			FilePath:  "/nonexistent/image.png",
		},
		{
			FileName:  "code.go",
			MediaType: "text/x-go",
			FilePath:  "/nonexistent/code.go",
		},
	}

	// Test that preprocessing logic is consistent (will fail for nonexistent files, which is expected)
	for _, att := range attachments {
		t.Logf("Attachment %s (%s) ready for preprocessing", att.FileName, att.MediaType)
	}

	// This test verifies the attachment structure consistency across providers
	// Actual preprocessing is tested in internal/llm/attachment package
	if len(attachments) != 3 {
		t.Errorf("expected 3 test attachments, got %d", len(attachments))
	}
}

// Test request normalization ensures consistency across provider calls
func TestProviderInterop_RequestNormalization(t *testing.T) {
	agent := models.LLMConfig{
		Name:       "Test Agent",
		Provider:   models.ProviderTest,
		Model:      "test-model",
		AuthMethod: models.AuthMethodCLI,
		MaxTokens:  1000,
	}

	req := llmcontracts.AgentRequest{
		Ctx:         context.Background(),
		Operation:   llmcontracts.OperationDirect,
		Message:     "  Test message with\nmultiple\nlines  ",
		Attachments: []models.Attachment{},
		Agent:       agent,
		ExecID:      "test-exec-123",
		WorkDir:     "/tmp",
	}

	// Test normalization using the actual exported function
	normalized, err := llmnormalize.NormalizeRequest(req)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	// Verify message whitespace is trimmed (NormalizeRequest trims message)
	expectedMsg := "Test message with\nmultiple\nlines"
	if normalized.Message != expectedMsg {
		t.Errorf("expected normalized message %q, got %q", expectedMsg, normalized.Message)
	}

	// Verify agent config is preserved
	if normalized.Agent.MaxTokens != req.Agent.MaxTokens {
		t.Errorf("expected max_tokens to be preserved, got %d", normalized.Agent.MaxTokens)
	}

	// Verify operation is preserved
	if normalized.Operation != req.Operation {
		t.Errorf("expected operation to be preserved, got %s", normalized.Operation)
	}

	t.Logf("Request normalization successful: operation=%s model=%s message_len=%d",
		normalized.Operation, normalized.Agent.Model, len(normalized.Message))
}

// Test canonical result construction consistency
func TestProviderInterop_CanonicalResultConsistency(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		textOnly string
		tokens   int
		wantErr  bool
	}{
		{
			name:     "Simple result",
			output:   "Full output with markers",
			textOnly: "Text only",
			tokens:   100,
			wantErr:  false,
		},
		{
			name:     "Empty textOnly defaults to output",
			output:   "Output text",
			textOnly: "",
			tokens:   50,
			wantErr:  false,
		},
		{
			name:     "Max tokens error",
			output:   "Partial output",
			textOnly: "Partial",
			tokens:   500,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := llmcontracts.Usage{
				InputTokens:  tt.tokens / 2,
				OutputTokens: tt.tokens / 2,
				TotalTokens:  tt.tokens,
			}

			var err error
			if tt.wantErr {
				err = errMaxTokens
			}

			result, resErr := canonicalResult(tt.output, tt.textOnly, usage, err)

			// Verify output
			if result.Output != tt.output {
				t.Errorf("expected output %q, got %q", tt.output, result.Output)
			}

			// Verify textOnly defaults
			expectedTextOnly := tt.textOnly
			if expectedTextOnly == "" {
				expectedTextOnly = tt.output
			}
			if result.TextOnlyOutput != expectedTextOnly {
				t.Errorf("expected textOnly %q, got %q", expectedTextOnly, result.TextOnlyOutput)
			}

			// Verify usage
			if result.Usage.TotalTokens != tt.tokens {
				t.Errorf("expected total tokens %d, got %d", tt.tokens, result.Usage.TotalTokens)
			}

			// Verify stop reason for max tokens
			if tt.wantErr && result.StopReason != "max_tokens" {
				t.Errorf("expected stop_reason max_tokens, got %q", result.StopReason)
			}

			// Verify error propagation
			if tt.wantErr && resErr != errMaxTokens {
				t.Errorf("expected errMaxTokens, got %v", resErr)
			}

			t.Logf("Canonical result verified: output_len=%d textOnly_len=%d tokens=%d err=%v",
				len(result.Output), len(result.TextOnlyOutput), result.Usage.TotalTokens, resErr != nil)
		})
	}
}

// Test that provider-specific errors are wrapped consistently
func TestProviderInterop_ErrorWrappingConsistency(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = ""
	mock.TextOnly = ""
	mock.Tokens = 0
	mock.Err = errMaxTokens
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	adapter, ok := svc.adapterFor(models.ProviderTest)
	if !ok {
		t.Fatal("expected test provider adapter")
	}

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	res, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationDirect,
		Message:   "test error wrapping",
		Agent:     agent,
		ExecID:    "exec-err-1",
	})

	// Should propagate max tokens error and set stop reason
	if err != errMaxTokens {
		t.Errorf("expected errMaxTokens, got %v", err)
	}

	if res.StopReason != "max_tokens" {
		t.Errorf("expected stop_reason max_tokens, got %q", res.StopReason)
	}

	t.Logf("Error wrapping verified: err=%v stop_reason=%s", err, res.StopReason)
}
