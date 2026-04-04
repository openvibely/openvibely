package service

import (
	"github.com/openvibely/openvibely/internal/models"
)

// openAIDirectClientEnabled returns true if this agent config should use direct OpenAI API client
// (both API key and OAuth auth methods). Used by provider adapter for routing logic.
func openAIDirectClientEnabled(agent models.LLMConfig) bool {
	return agent.IsOpenAIAPIKey() || agent.IsOpenAIOAuth()
}
