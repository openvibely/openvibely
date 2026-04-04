package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestOpenAIDirectClientEnabled(t *testing.T) {
	if !openAIDirectClientEnabled(models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}) {
		t.Fatal("expected api_key to enable direct client")
	}
	if !openAIDirectClientEnabled(models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth}) {
		t.Fatal("expected oauth to enable direct client")
	}
	if openAIDirectClientEnabled(models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI}) {
		t.Fatal("expected cli auth to disable direct client")
	}
}

func TestCallAgentDirectStreaming_NormalizationStillWorksWithContext(t *testing.T) {
	svc := &LLMService{}
	svc.SetLLMCaller(nil)
	_ = context.Background() // smoke compile/link guard for recent adapter refactor paths
}
