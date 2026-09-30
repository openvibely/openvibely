package prompt

import (
	_ "embed"

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

func BuildAnthropicAgentSystemPrompt(projectInstructions string, workDir ...string) string {
	return buildAgentSystemPrompt(AnthropicAgentSystemPrompt, projectInstructions, workDir...)
}

func BuildAnthropicChatSystemPrompt(isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	return buildChatSystemPrompt(AnthropicAgentSystemPrompt, isTaskFollowup, chatMode, chatSystemContext, restrictTools)
}
