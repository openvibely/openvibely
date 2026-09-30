package prompt

import (
	"embed"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// Snapshotted from Codex's models_cache.json on 2026-09-29. These are the
// model_messages.instructions_template values, not permission/tool overlays.
// Identical templates share the reserve file. Unknown models use that fallback.
//
//go:embed codex/*.md
var codexPrompts embed.FS

func CodexSystemPrompt(model string) string {
	name := "gpt-reserve"
	model = strings.ToLower(strings.TrimSpace(model))
	for _, candidate := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.5"} {
		if model == candidate {
			name = candidate
			break
		}
	}
	data, err := codexPrompts.ReadFile("codex/" + name + ".md")
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(data))
}

func BuildCodexAgentSystemPrompt(model, projectInstructions string, workDir ...string) string {
	return buildAgentSystemPrompt(CodexSystemPrompt(model), projectInstructions, workDir...)
}

func BuildCodexChatSystemPrompt(model string, isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	return buildChatSystemPrompt(CodexSystemPrompt(model), isTaskFollowup, chatMode, chatSystemContext, restrictTools)
}
