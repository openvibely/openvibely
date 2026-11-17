package prompt

import (
	"embed"

	"github.com/openvibely/openvibely/internal/models"
)

// AnthropicAgentSystemPrompt provides coding instructions for Anthropic models.
// Tool names are mapped to the tools available in this application.
//
//go:embed anthropic_system_prompt.md
var AnthropicAgentSystemPrompt string

//go:embed *.md
var anthropicPrompts embed.FS

var anthropicSnapshots profileCache

func anthropicSnapshot(model string) string {
	profile := models.PromptProfileClaudeCode
	if spec, ok := models.LookupModel(models.ProviderAnthropic, model); ok && spec.PromptProfile != "" {
		profile = spec.PromptProfile
	}
	return anthropicSnapshots.get(string(profile), func() string {
		data, err := anthropicPrompts.ReadFile(string(profile))
		if err != nil {
			panic(err)
		}
		return string(data)
	})
}

func BuildAnthropicAgentSystemPrompt(model, projectInstructions string, workDir ...string) string {
	return buildAgentSystemPrompt(anthropicSnapshot(model), projectInstructions, workDir...)
}

func BuildAnthropicChatSystemPrompt(model string, isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	return buildChatSystemPrompt(anthropicSnapshot(model), isTaskFollowup, chatMode, chatSystemContext, restrictTools)
}
