package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
)

const CodexDefaultModel = "gpt-5.6-sol"

var CodexSupportedReasoningEffortsByModel = map[string][]string{
	"gpt-6-astra":         {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-sol":         {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-terra":       {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-luna":        {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5.5":             {"low", "medium", "high", "xhigh"},
	"gpt-5.5-pro":         {"low", "medium", "high", "xhigh"},
	"gpt-5.4":             {"low", "medium", "high", "xhigh"},
	"gpt-5.4-mini":        {"low", "medium", "high", "xhigh"},
	"gpt-5.3-codex":       {"low", "medium", "high", "xhigh"},
	"gpt-5.3-codex-spark": {"low", "medium", "high", "xhigh"},
	"gpt-5.2-codex":       {"low", "medium", "high", "xhigh"},
	"gpt-5.1-codex-max":   {"low", "medium", "high", "xhigh"},
	"gpt-5.1-codex":       {"low", "medium", "high"},
	"gpt-5.1-codex-mini":  {"low", "medium", "high"},
	"gpt-5-codex":         {"low", "medium", "high"},
	"gpt-5-codex-mini":    {"low", "medium", "high"},
}

func CodexModelOrDefault(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return CodexDefaultModel
	}
	if _, ok := CodexSupportedReasoningEffortsByModel[model]; ok {
		return model
	}
	applog.Infof("[agent-svc] unsupported codex model %q requested, falling back to %q", model, CodexDefaultModel)
	return CodexDefaultModel
}

func CodexReasoningEffort(model, configuredEffort string) string {
	effort := NormalizeReasoningEffortValue(configuredEffort)
	if effort == "" {
		effort = NormalizeReasoningEffortValue(os.Getenv("OPENVIBELY_CODEX_REASONING_EFFORT"))
	}
	if effort == "" {
		effort = CodexDefaultReasoningEffort(model)
	}

	supported := CodexSupportedReasoningEfforts(model)
	if StringInSlice(effort, supported) {
		return effort
	}

	// Fallback preference when selected effort isn't supported by the chosen model.
	for _, candidate := range []string{"high", "medium", "low", "xhigh"} {
		if StringInSlice(candidate, supported) {
			return candidate
		}
	}
	if len(supported) > 0 {
		return supported[0]
	}
	return "high"
}

func CodexDefaultReasoningEffort(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.4-mini":
		return "medium"
	case "gpt-5.3-codex-spark":
		return "high"
	default:
		return "high"
	}
}

func CodexSupportedReasoningEfforts(model string) []string {
	model = strings.TrimSpace(model)
	if supported, ok := CodexSupportedReasoningEffortsByModel[model]; ok && len(supported) > 0 {
		return supported
	}
	// Safe default for unknown/custom models.
	return []string{"low", "medium", "high"}
}

func NormalizeReasoningEffortValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func StringInSlice(value string, values []string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func AttachmentAbsPath(att models.Attachment) string {
	absPath := att.FilePath
	if !filepath.IsAbs(absPath) {
		if abs, err := filepath.Abs(absPath); err == nil {
			absPath = abs
		}
	}
	return absPath
}

// BuildTaskPromptHeader returns the standard task execution directives that are
// prepended to every task prompt. Managed project instructions from lifecycle
// skills and memory are injected via the system prompt, not here.
func BuildTaskPromptHeader() string {
	return "IMPORTANT: Do not use plan mode. Take direct action immediately. Do not ask for approval or create plans — execute the task directly.\n\n"
}

// BuildAttachmentInstructions builds the text block that tells text-only model
// calls about attached files with their absolute paths. Returns an empty string
// if there are no attachments.
func BuildAttachmentInstructions(attachments []models.Attachment) string {
	if len(attachments) == 0 {
		return ""
	}

	var textFiles []models.Attachment
	var imageFiles []models.Attachment

	for _, att := range attachments {
		if output.IsImageMediaType(att.MediaType) {
			imageFiles = append(imageFiles, att)
		} else {
			textFiles = append(textFiles, att)
		}
	}

	var sb strings.Builder

	// List text files that can be read
	if len(textFiles) > 0 {
		sb.WriteString("You have been provided with the following attached files:\n")
		for _, att := range textFiles {
			absPath := AttachmentAbsPath(att)
			sb.WriteString(fmt.Sprintf("- %s (absolute path: %s)\n", att.FileName, absPath))
		}
		sb.WriteString("\nPlease examine these files as part of your task. Use the absolute paths above to access them.\n\n")
	}

	// Warn about image files that cannot be viewed.
	if len(imageFiles) > 0 {
		if len(textFiles) > 0 {
			sb.WriteString("---\n\n")
		}
		sb.WriteString("NOTE: The following image files were attached, but this model call cannot view them directly because it does not have vision support:\n")
		for _, att := range imageFiles {
			absPath := AttachmentAbsPath(att)
			sb.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
		}
		sb.WriteString("\nIf image analysis is required for this task, ask the user to reconfigure the task with a vision-capable model.\n\n")
	}

	return sb.String()
}

// BuildChatHistoryText formats chat history as a text block with "User:" and
// "Assistant:" prefixes. It limits history to MaxChatHistoryTurns, cleans output,
// and includes both completed and failed turns. Returns an empty string if history is empty.
func BuildChatHistoryText(history []models.Execution) string {
	history = LimitChatHistory(history)
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Previous conversation:\n\n")
	for _, exec := range history {
		if exec.PromptSent != "" {
			sb.WriteString("User: ")
			sb.WriteString(exec.PromptSent)
			sb.WriteString("\n\n")
		}
		if replay := ReplayAssistantContent(exec); replay != "" {
			sb.WriteString("Assistant: ")
			sb.WriteString(replay)
			sb.WriteString("\n\n")
		}
	}
	sb.WriteString("---\n\n")
	return sb.String()
}

func ReplayAssistantContent(exec models.Execution) string {
	if exec.Status != models.ExecCompleted && exec.Status != models.ExecFailed {
		return ""
	}
	if exec.Output != "" {
		if cleaned := output.CleanChatOutput(exec.Output); cleaned != "" {
			return cleaned
		}
	}
	if exec.Status == models.ExecFailed {
		if errMsg := strings.TrimSpace(exec.ErrorMessage); errMsg != "" {
			return "Previous execution failed before producing output: " + errMsg
		}
		return "Previous execution failed before producing output."
	}
	return ""
}

// MaxChatHistoryTurns is the maximum number of chat history turns to include
// in LLM context. Centralised here to avoid magic numbers scattered across methods.
const MaxChatHistoryTurns = 20

// LimitChatHistory truncates chat history to the most recent MaxChatHistoryTurns entries.
func LimitChatHistory(history []models.Execution) []models.Execution {
	if len(history) > MaxChatHistoryTurns {
		return history[len(history)-MaxChatHistoryTurns:]
	}
	return history
}
