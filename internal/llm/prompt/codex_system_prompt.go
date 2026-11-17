package prompt

import (
	"embed"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// Model-specific coding instructions are embedded without permission or tool overlays.
// Identical templates share the reserve file. Unknown models use that fallback.
//
//go:embed codex/*.md
var codexPrompts embed.FS

// Keys come only from the bundled model registry (unknown names use reserve),
// so this cache cannot grow with arbitrary user-provided model names.
var adaptedCodexPrompts profileCache
var codexToolNames = strings.NewReplacer("`apply_patch`", "`edit_file` or `write_file`", "exec_command", "shell")

func CodexSystemPrompt(model string) string {
	profile := codexProfile(model)
	return adaptedCodexPrompts.get(profile, func() string {
		base := codexSnapshot(model)
		switch models.PromptProfile(profile) {
		case models.PromptProfileCodexGPT62Astra, models.PromptProfileCodexGPT62Sol:
			base = adaptGPT62ToolSections(base)
		}
		return adaptCodexTools(base)
	})
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

// Replace the skill and connector instructions with guidance for the tools attached to the turn.
func adaptGPT62ToolSections(base string) string {
	sections := strings.Split(base, "\n# ")
	adapted := make([]string, 0, len(sections))
	for _, section := range sections {
		heading, _, _ := strings.Cut(section, "\n")
		switch heading {
		case "Using skills":
			section = "Using skills\n\nUse a relevant skill when requested or when it helps with the task. " +
				"Use the permitted skill index, or call skills_list if it is available. " +
				"When skill_view is available, pass the selected view_handle as handle. " +
				"For a supporting file, use the same handle and its relative file_path. " +
				"Follow the user's instructions when applying a skill.\n"
		case "Apps (Connectors)", "Plugins and Apps":
			section = "Plugins and apps\n\nUse the relevant connector or plugin tools attached to this request. " +
				"Use their advertised names and argument schemas."
		case "Plugins":
			continue // Connector and plugin instructions are combined above.
		}
		adapted = append(adapted, section)
	}
	return strings.ReplaceAll(strings.Join(adapted, "\n# "), "the `cmd` argument", "the `command` argument")
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
