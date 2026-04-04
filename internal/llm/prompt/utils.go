package prompt

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
)

const CodexDefaultModel = "gpt-5.3-codex"

var CodexSupportedReasoningEffortsByModel = map[string][]string{
	"gpt-5.4":            {"low", "medium", "high", "xhigh"},
	"gpt-5.3-codex":      {"low", "medium", "high", "xhigh"},
	"gpt-5.2-codex":      {"low", "medium", "high", "xhigh"},
	"gpt-5.1-codex-max":  {"low", "medium", "high", "xhigh"},
	"gpt-5.1-codex":      {"low", "medium", "high"},
	"gpt-5.1-codex-mini": {"low", "medium", "high"},
	"gpt-5-codex":        {"low", "medium", "high"},
	"gpt-5-codex-mini":   {"low", "medium", "high"},
}

func CodexModelOrDefault(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return CodexDefaultModel
	}
	if _, ok := CodexSupportedReasoningEffortsByModel[model]; ok {
		return model
	}
	log.Printf("[agent-svc] unsupported codex model %q requested, falling back to %q", model, CodexDefaultModel)
	return CodexDefaultModel
}

func CodexReasoningEffort(model, configuredEffort string) string {
	effort := NormalizeReasoningEffortValue(configuredEffort)
	if effort == "" {
		effort = NormalizeReasoningEffortValue(os.Getenv("OPENVIBELY_CODEX_REASONING_EFFORT"))
	}
	if effort == "" {
		effort = "high"
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
	case "low", "medium", "high", "xhigh":
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
// prepended to every task prompt. Project instructions (AGENTS.md) are now
// injected via the system prompt, not here.
func BuildTaskPromptHeader() string {
	return "IMPORTANT: Do not use plan mode. Take direct action immediately. Do not ask for approval or create plans — execute the task directly.\n\n"
}

// BuildAttachmentInstructions builds the text block that tells CLI-based agents
// about attached files with their absolute paths. Returns an empty string if
// there are no attachments.
// 
// NOTE: This function separates image files from text files. CLI agents cannot
// view images natively (no vision support), so we provide a clear message that
// images are listed but cannot be analyzed. Text files can be read normally.
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
	
	// Warn about image files that cannot be viewed
	if len(imageFiles) > 0 {
		if len(textFiles) > 0 {
			sb.WriteString("---\n\n")
		}
		sb.WriteString("NOTE: The following image files were attached, but you cannot view them directly because you are running in CLI mode without vision support:\n")
		for _, att := range imageFiles {
			absPath := AttachmentAbsPath(att)
			sb.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
		}
		sb.WriteString("\nIf image analysis is required for this task, ask the user to reconfigure the task with a vision-capable model (e.g., Anthropic API or OpenAI API with an API key or OAuth).\n\n")
	}
	
	return sb.String()
}

// BuildChatHistoryText formats chat history as a text block with "User:" and
// "Assistant:" prefixes, suitable for CLI-based agents. It limits history to
// MaxChatHistoryTurns, cleans output, and includes both completed and failed turns.
// Returns an empty string if history is empty.
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
		// Include outputs from completed and failed executions to preserve
		// conversation context. Without this, if a prior exchange fails, the
		// assistant's response is lost and follow-up messages like "Create the task"
		// lack the context of what was discussed.
		if exec.Output != "" && (exec.Status == models.ExecCompleted || exec.Status == models.ExecFailed) {
			cleaned := output.CleanChatOutput(exec.Output)
			if cleaned != "" {
				// Minimize action-heavy responses to prevent re-execution
				if strings.Contains(exec.Output, "[CREATE_TASK]") || strings.Contains(exec.Output, "[EDIT_TASK]") ||
					strings.Contains(exec.Output, "[SCHEDULE_TASK]") || strings.Contains(exec.Output, "[DELETE_SCHEDULE]") {
					cleaned = "(Handled the user's request.)"
				}
				sb.WriteString("Assistant: ")
				sb.WriteString(cleaned)
				sb.WriteString("\n\n")
			}
		}
	}
	sb.WriteString("---\n\n")
	return sb.String()
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

// FilteredEnvWithoutClaudeCode returns os.Environ() with the CLAUDECODE
// variable stripped, so spawned CLI subprocesses don't think they're nested.
func FilteredEnvWithoutClaudeCode() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
