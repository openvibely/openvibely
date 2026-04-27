package service

import (
	"encoding/json"
	"fmt"
	"log"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

const anthropicDirectOutputBudget = 4096
const anthropicAgenticOutputBudget = 16384

// errMaxTokens is returned when the API response was truncated due to max_tokens.
var errMaxTokens = fmt.Errorf("response truncated: max_tokens limit reached (output budget exhausted before task completed)")

// toolSecondaryInfo extracts a short secondary label from tool input (e.g. filename, command).
// Returns empty string if nothing useful can be extracted.
// Used by legacy callAnthropic paths for streaming event markers.
func toolSecondaryInfo(name string, input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch name {
	case "read_file", "write_file", "edit_file", "Read", "Write", "Edit":
		if fp, ok := m["file_path"].(string); ok {
			// Return just the filename, not full path
			parts := splitPath(fp)
			return parts[len(parts)-1]
		}
	case "bash", "Bash":
		if cmd, ok := m["command"].(string); ok {
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			return "$ " + cmd
		}
	case "grep_search", "Grep":
		if p, ok := m["pattern"].(string); ok {
			if len(p) > 40 {
				p = p[:40] + "…"
			}
			return p
		}
	case "list_files", "Glob":
		if p, ok := m["path"].(string); ok {
			return p
		}
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range []byte(p) {
		if s == '/' {
			parts = append(parts, "")
		} else if len(parts) == 0 {
			parts = append(parts, string(s))
		} else {
			parts[len(parts)-1] += string(s)
		}
	}
	if len(parts) == 0 {
		return []string{p}
	}
	return parts
}

// buildAnthropicClientHistory converts chat execution history into anthropicclient.Message
// slices, applying the same limiting and cleaning as buildChatHistoryText.
// The Anthropic API requires strictly alternating user/assistant roles.
// Consecutive same-role messages are merged, and a trailing user message
// is dropped (since SendAgentic appends the current user message).
// Used by legacy callAnthropicChat paths.
func buildAnthropicClientHistory(chatHistory []models.Execution) []anthropicclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	var messages []anthropicclient.Message
	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = appendMergedMessage(messages, "user", exec.PromptSent)
		}
		if exec.Output != "" && (exec.Status == models.ExecCompleted || exec.Status == models.ExecFailed) {
			messages = appendMergedMessage(messages, "assistant", exec.Output)
		}
	}
	// Drop trailing user message — SendAgentic always appends the current
	// user message, so a trailing user here would create consecutive user messages.
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}
	return messages
}

// appendMergedMessage appends a message, merging with the previous message
// if it has the same role (to maintain alternating user/assistant roles).
func appendMergedMessage(messages []anthropicclient.Message, role, content string) []anthropicclient.Message {
	if len(messages) > 0 && messages[len(messages)-1].Role == role {
		messages[len(messages)-1].Content += "\n\n" + content
		return messages
	}
	return append(messages, anthropicclient.Message{Role: role, Content: content})
}

// convertAttachments converts internal models.Attachment to anthropicclient.FileAttachment format.
// Used by legacy callAnthropic and callAnthropicChat paths.
func convertAttachments(attachments []models.Attachment) ([]*anthropicclient.FileAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	prepared, err := llmattachment.Preprocess(attachments)
	if err != nil {
		return nil, fmt.Errorf("preprocess attachments: %w", err)
	}

	result := make([]*anthropicclient.FileAttachment, 0, len(prepared))
	for _, att := range prepared {
		mcAtt, err := anthropicclient.NewFileAttachment(att.FilePath)
		if err != nil {
			log.Printf("[agent-svc] convertAttachments error loading %s: %v", att.FilePath, err)
			return nil, fmt.Errorf("load attachment %s: %w", att.FileName, err)
		}
		result = append(result, mcAtt)
	}
	return result, nil
}
