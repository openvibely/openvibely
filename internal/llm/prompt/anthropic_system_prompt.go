package prompt

import (
	"embed"

	"github.com/openvibely/openvibely/internal/models"
)

// AnthropicAgentSystemPrompt snapshots Claude Code 2.1.283's standard coding
// sections (kXn, vXn, TXn, CXn, AXn, and the default rXn branch). Tool names are
// mapped to our tools; Claude CLI help/feedback instructions are omitted.
// Model/feature-flag sections and Claude-only harness claims are not included.
// Source: anthropic.claude-code-2.1.283-darwin-arm64/resources/native-binary/claude.
//
//go:embed anthropic_system_prompt.md
var AnthropicAgentSystemPrompt string

//go:embed *.md
var anthropicPrompts embed.FS

func anthropicSnapshot(model string) string {
	profile := models.PromptProfileClaudeCode
	if spec, ok := models.LookupModel(models.ProviderAnthropic, model); ok && spec.PromptProfile != "" {
		profile = spec.PromptProfile
	}
	data, err := anthropicPrompts.ReadFile(string(profile))
	if err != nil {
		panic(err)
	}
	return string(data)
}

func BuildAnthropicAgentSystemPrompt(model, projectInstructions string, workDir ...string) string {
	return buildAgentSystemPrompt(anthropicSnapshot(model), projectInstructions, workDir...)
}

func BuildAnthropicChatSystemPrompt(model string, isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	return buildChatSystemPrompt(anthropicSnapshot(model), isTaskFollowup, chatMode, chatSystemContext, restrictTools)
}
