package prompt

import (
	"embed"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

// Snapshotted from Codex's models_cache.json on 2026-09-29. These are the
// model_messages.instructions_template values, not permission/tool overlays.
// Astra 6, Terra 6.1, and Sol 6.1 were refreshed on 2026-10-29 (Codex 0.164.0).
// Identical templates share the reserve file. Unknown models use that fallback.
//
//go:embed codex/*.md
var codexPrompts embed.FS

// Keys come only from the bundled model registry (unknown names use reserve),
// so this cache cannot grow with arbitrary user-provided model names.
var adaptedCodexPrompts sync.Map
var codexToolNames = strings.NewReplacer("`apply_patch`", "`edit_file` or `write_file`", "exec_command", "shell")

func CodexSystemPrompt(model string) string {
	profile := codexProfile(model)
	if cached, ok := adaptedCodexPrompts.Load(profile); ok {
		return cached.(func() string)()
	}
	cached, _ := adaptedCodexPrompts.LoadOrStore(profile, sync.OnceValue(func() string { return adaptCodexTools(codexSnapshot(model)) }))
	return cached.(func() string)()
}

func codexProfile(model string) string {
	profile := models.PromptProfileCodexReserve
	if spec, ok := models.LookupModel(models.ProviderOpenAI, model); ok && spec.PromptProfile != "" {
		profile = spec.PromptProfile
	}
	return string(profile)
}

func codexSnapshot(model string) string {
	data, err := codexPrompts.ReadFile(codexProfile(model))
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(data))
}

func adaptCodexTools(base string) string {
	lines := strings.Split(base, "\n")
	for i, line := range lines {
		switch {
		case strings.Contains(line, "multi_tool_use.parallel"):
			lines[i] = strings.ReplaceAll(line, "You use `multi_tool_use.parallel` for that parallelism, and only that.", "Call the tools supplied with this request directly; run independent calls in parallel when supported.")
		case strings.Contains(line, "ALL_TOOLS"):
			lines[i] = "Use the tools supplied with this request to find relevant capabilities. Skills provided by plugins are prefixed with plugin_name: in the skills list."
		case strings.Contains(line, "functions.exec"):
			lines[i] = "- Call the tools supplied with this request directly. Run independent calls in parallel when supported; keep dependent calls sequential."
		case strings.Contains(line, "request_user_input_async"):
			lines[i] = "When clarification is needed, use request_user_input if it is provided. Follow its schema and wait for the user's answer; otherwise ask the user in your response. Never assume an unanswered question grants approval."
		default:
			lines[i] = codexToolNames.Replace(line)
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
