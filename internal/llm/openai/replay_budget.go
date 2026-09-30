package openai

import (
	"encoding/json"

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
	var items []any
	for _, message := range buildClientHistory(history) {
		if len(message.ResponsesInputItems) > 0 {
			items = append([]any(nil), message.ResponsesInputItems...)
		} else {
			items = append(items, map[string]any{"type": "message", "role": message.Role, "content": message.Content})
		}
	}
	return openaiclient.EstimateResponsesInputTokens(items)
}
