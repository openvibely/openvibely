package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestImageAttachmentBudgetIgnoresEncodedSize(t *testing.T) {
	for _, tc := range []struct {
		provider models.LLMProvider
		tokens   int
	}{{models.ProviderOpenAI, 1844}, {models.ProviderAnthropic, 2000}, {models.ProviderOpenAICompatible, 2000}, {models.ProviderOllama, 2000}} {
		for _, size := range []int64{0, 713753, 794795, 10000000} {
			att := models.Attachment{FileName: "screen.png", FilePath: "/tmp/screen.png", MediaType: "image/png", FileSize: size}
			want := tc.tokens + estimatedUTF8Tokens(att.FileName) + estimatedUTF8Tokens(att.FilePath) + estimatedUTF8Tokens(att.MediaType)
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: tc.provider}}
			base := estimateModelVisibleRequestTokens(req)
			req.Attachments = []models.Attachment{att}
			if got := calculateRequestBudget(req).AttachmentTokens; got != want {
				t.Fatalf("provider=%s size=%d tokens=%d want=%d", tc.provider, size, got, want)
			}
			if got := estimateModelVisibleRequestTokens(req) - base; got != want {
				t.Fatalf("provider=%s total estimate delta=%d want=%d", tc.provider, got, want)
			}
		}

	}
}

func TestScreenshotFollowupReachesNativeCompaction(t *testing.T) {
	svc := &LLMService{}
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
		Message:     "Align the details panel below the menu bar.",
		Agent:       models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-6-astra", AuthMethod: models.AuthMethodOAuth},
		ChatHistory: []models.Execution{{PromptSent: strings.Repeat("history ", 150000), Output: "previous"}},
		Attachments: []models.Attachment{
			{MediaType: "image/png", FileSize: 794795},
			{MediaType: "image/png", FileSize: 713753},
		},
	}
	called := false
	adapter := providerAdapterFunc(func(got llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		called = true
		if !got.ForceNativeCompaction || len(got.Attachments) != 2 || got.Message != req.Message {
			t.Fatal("expected native history compaction with unchanged screenshots and prompt")
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("screenshot request never reached provider")
	}
}

func TestImageAttachmentBudgetStillEnforcesContextLimit(t *testing.T) {
	req := llmcontracts.AgentRequest{
		Ctx:   context.Background(),
		Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-6-astra"},
	}
	for range 200 {
		req.Attachments = append(req.Attachments, models.Attachment{MediaType: "image/png", FileSize: 1})
	}
	if !pendingInputInfeasible(calculateRequestBudget(req)) {
		t.Fatal("image token budget must still enforce the context limit")
	}
}

func TestCompatibleScreenshotFollowupReachesProvider(t *testing.T) {
	svc := &LLMService{}
	req := llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
		Message: "Compare these screenshots.",
		Agent:   models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "vision-model", ContextWindow: 32768},
		Attachments: []models.Attachment{
			{MediaType: "image/png", FileSize: 794795},
			{MediaType: "image/png", FileSize: 713753},
		},
	}
	called := false
	adapter := providerAdapterFunc(func(got llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		called = true
		if len(got.Attachments) != 2 || got.Message != req.Message {
			t.Fatal("screenshots and prompt must remain unchanged")
		}
		return llmcontracts.AgentResult{Output: "ok"}, nil
	})
	if _, err := svc.callProviderWithCompaction(adapter, req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("compatible screenshot request never reached provider")
	}
	for range 20 {
		req.Attachments = append(req.Attachments, models.Attachment{MediaType: "image/png", FileSize: 1})
	}
	if !pendingInputInfeasible(calculateRequestBudget(req)) {
		t.Fatal("compatible images must still count toward the context limit")
	}
}

func TestNonImageAttachmentsBudgetReferencesOnly(t *testing.T) {
	for _, ext := range []string{"pdf", "doc", "ppt", "docx", "pptx", "xlsx", "xls", "xlsb", "ods", "txt", "csv", "svg", "mp4", "mp3", "zip"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file."+ext)
			att := models.Attachment{FileName: filepath.Base(path), FilePath: path, FileSize: 200 << 20}
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "test", ContextWindow: 32768}, Attachments: []models.Attachment{att}}
			called := false
			svc := &LLMService{}
			_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(got llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				called = true
				require.Less(t, calculateRequestBudget(got).AttachmentTokens, 1000)
				parts, err := llmattachment.ModelInputs(got.Ctx, got.Attachments)
				require.NoError(t, err, "references must not read or parse the original")
				require.Len(t, parts, 1)
				require.Equal(t, "text/plain", parts[0].MediaType)
				require.Contains(t, string(parts[0].Data), path)
				return llmcontracts.AgentResult{}, nil
			}), req)
			require.NoError(t, err)
			require.True(t, called)
		})
	}
}

func TestToolImageReplayBudgetIgnoresEncodedBytes(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible} {
		imageType := "image_url"
		if provider == models.ProviderAnthropic {
			imageType = "image"
		}
		raw := `[{"role":"user","content":[{"type":"` + imageType + `","source":{"data":"` + strings.Repeat("A", 1500000) + `"},"image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", 1500000) + `"}}]}]`
		if provider == models.ProviderOpenAICompatible {
			raw = strings.Replace(raw, `"content":`, `"image_content":`, 1)
		}
		tokens := estimateHistoryTokens(provider, []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: raw}}}})
		require.GreaterOrEqual(t, tokens, 2000)
		require.Less(t, tokens, 3000, "persisted image bytes must use the image estimate on follow-ups")
	}
}
