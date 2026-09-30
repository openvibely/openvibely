package openai

import (
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// EffectiveReplayHistory removes prefixes replaced by a complete checkpoint.
// Keep the execution's text fields for last-resort truncation, but don't count
// them alongside the checkpoint in EstimateHistoryTokens.
func EffectiveReplayHistory(history []models.Execution) []models.Execution {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].ReplayMessages) - 1; j >= 0; j-- {
			var saved struct {
				Input []any `json:"responses_input"`
			}
			if json.Unmarshal([]byte(history[i].ReplayMessages[j].TranscriptJSON), &saved) == nil && len(saved.Input) > 0 {
				result := append([]models.Execution(nil), history[i:]...)
				result[0].ReplayMessages = result[0].ReplayMessages[j:]
				return result
			}
		}
	}
	return history
}

// EstimateHistoryTokens budgets exactly the history representation built for
// OpenAI, including checkpoint replacement and image-specific token estimates.
func EstimateHistoryTokens(history []models.Execution) int {
	return openaiclient.EstimateResponsesInputTokens(HistoryInputItems(history))
}

// HistoryInputItems is the canonical replay representation for chat and task
// recovery requests alike.
func HistoryInputItems(history []models.Execution) []any {
	var items []any
	for _, message := range buildClientHistory(history) {
		if len(message.ResponsesInputItems) > 0 {
			items = append([]any(nil), message.ResponsesInputItems...)
		} else {
			items = append(items, map[string]any{"type": "message", "role": message.Role, "content": message.Content})
		}
	}
	return items
}

// UserMessageHistory extracts the user turns from the effective native replay.
// A checkpoint replaces its prefix, just as it does when building a request.
// Non-native histories retain their existing representation.
func UserMessageHistory(history []models.Execution) []models.Execution {
	messages := buildClientHistory(history)
	hasCheckpoint := false
	for _, message := range messages {
		if len(message.ResponsesInputItems) > 0 {
			hasCheckpoint = true
			break
		}
	}
	if !hasCheckpoint {
		return history
	}
	var users []models.Execution
	for _, raw := range HistoryInputItems(history) {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "message" || item["role"] != "user" {
			continue
		}
		var text string
		switch content := item["content"].(type) {
		case string:
			text = content
		case []any:
			var parts []string
			for _, rawPart := range content {
				part, ok := rawPart.(map[string]any)
				if !ok || (part["type"] != "input_text" && part["type"] != "output_text") {
					continue
				}
				if value, ok := part["text"].(string); ok && value != "" {
					parts = append(parts, value)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if strings.TrimSpace(text) != "" {
			// No execution ID or replay: these are retained user messages, not
			// database executions whose full history should be hydrated again.
			users = append(users, models.Execution{PromptSent: text, Status: models.ExecCompleted})
		}
	}
	return users
}
