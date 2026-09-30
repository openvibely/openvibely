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
	return adaptCodexTools(codexSnapshot(model))
}

func codexSnapshot(model string) string {
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

func adaptCodexTools(base string) string {
	lines := strings.Split(base, "\n")
	for i, line := range lines {
		switch {
		case strings.Contains(line, "functions.exec"):
			lines[i] = "- Call the tools supplied with this request directly. Run independent calls in parallel when supported; keep dependent calls sequential."
		case strings.Contains(line, "request_user_input_async"):
			lines[i] = "When clarification is needed, use request_user_input if it is provided. Follow its schema and wait for the user's answer; otherwise ask the user in your response. Never assume an unanswered question grants approval."
		default:
			lines[i] = strings.NewReplacer("`apply_patch`", "`edit_file` or `write_file`", "exec_command", "bash").Replace(line)
		}
	}
	return strings.Join(lines, "\n") + "\n\nUse only tools attached to this request, with their actual names and schemas. For file inspection use read_file, list_files, and grep_search when available. Do not assume Codex-specific tools, connectors, or plugin discovery are installed."
}

func BuildCodexAgentSystemPrompt(model, projectInstructions string, workDir ...string) string {
	return buildAgentSystemPrompt(CodexSystemPrompt(model), projectInstructions, workDir...)
}

func BuildCodexChatSystemPrompt(model string, isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	return buildChatSystemPrompt(CodexSystemPrompt(model), isTaskFollowup, chatMode, chatSystemContext, restrictTools)
}
