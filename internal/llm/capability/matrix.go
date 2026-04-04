package capability

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// Features captures coarse provider/model capabilities used for routing and docs.
type Features struct {
	Vision    bool
	Streaming bool
	Tools     bool
}

// ForAgent returns a conservative capability profile for the configured agent.
func ForAgent(agent models.LLMConfig) Features {
	f := Features{Streaming: true, Tools: true}

	switch agent.Provider {
	case models.ProviderAnthropic:
		// Anthropic CLI cannot send multimodal image blocks, API/OAuth can.
		f.Vision = !agent.IsAnthropicCLI()
	case models.ProviderOpenAI:
		// Direct OpenAI API/OAuth supports multimodal attachments; Codex CLI path is file-based.
		f.Vision = agent.IsOpenAIAPIKey() || agent.IsOpenAIOAuth()
	case models.ProviderOllama:
		// We support image attachments via /api/chat images field.
		f.Vision = true
	default:
		f.Vision = false
	}

	// Defensive model hint: if model name explicitly suggests no vision, downgrade.
	m := strings.ToLower(strings.TrimSpace(agent.Model))
	if strings.Contains(m, "text-only") || strings.Contains(m, "-text") {
		f.Vision = false
	}

	return f
}
