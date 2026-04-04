package capability

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestForAgent_Vision(t *testing.T) {
	cases := []struct {
		name     string
		agent    models.LLMConfig
		visionOK bool
	}{
		{"anthropic cli", models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI}, false},
		{"anthropic apikey", models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey}, true},
		{"openai oauth", models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth}, true},
		{"openai cli-like", models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI}, false},
		{"ollama", models.LLMConfig{Provider: models.ProviderOllama}, true},
	}

	for _, tc := range cases {
		got := ForAgent(tc.agent)
		if got.Vision != tc.visionOK {
			t.Fatalf("%s: expected vision=%v got %v", tc.name, tc.visionOK, got.Vision)
		}
	}
}
