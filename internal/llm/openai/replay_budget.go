package openai

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"strings"

	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// EffectiveReplayHistory removes prefixes replaced by a complete checkpoint.
// Keep the execution's text fields for last-resort truncation, but don't count
// them alongside the checkpoint in EstimateHistoryTokens.
func EffectiveReplayHistory(history []models.Execution) []models.Execution {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].ReplayMessages) - 1; j >= 0; j-- {
			replay := history[i].ReplayMessages[j]
			hasInput := false
			if replay.TranscriptJSON == "" && replay.Rows != nil && replay.Rows.Format == "envelope" && replay.Rows.ItemsKey == "responses_input" {
				// The row header already identifies a complete Responses history.
				// Inspecting that boundary must not decode every message again.
				hasInput = len(replay.Rows.Items) > 0
			} else {
				var saved struct {
					Input []any `json:"responses_input"`
				}
				hasInput = replay.DecodeTranscript(&saved) == nil && len(saved.Input) > 0
			}
			if hasInput {
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

// EstimateContinuationTokens follows SendAgentic's input assembly: a complete
// replay replaces the initial native checkpoint; plain messages append to it.
// Native items use the same image-aware estimator as replay items.
func EstimateContinuationTokens(history []models.Execution, nativeJSON string) (historyTokens, nativeTokens int) {
	return EstimateContinuationTokensContext(context.Background(), history, nativeJSON)
}

func EstimateContinuationTokensContext(ctx context.Context, history []models.Execution, nativeJSON string) (historyTokens, nativeTokens int) {
	native, err := preparedhistory.ReadState(ctx, strings.TrimSpace(nativeJSON), decodeNativeInput)
	if err == nil {
		nativeTokens = openaiclient.EstimateResponsesInputTokens(native)
	}
	for _, message := range preparedhistory.Read(ctx, history, buildClientHistory) {
		if len(message.ResponsesInputItems) > 0 {
			nativeTokens = 0
			historyTokens = openaiclient.EstimateResponsesInputTokens(message.ResponsesInputItems)
		} else {
			historyTokens += openaiclient.EstimateResponsesInputTokens([]any{map[string]any{"type": "message", "role": message.Role, "content": message.Content}})
		}
	}
	return historyTokens, nativeTokens
}

// HistoryInputItems is the canonical replay representation for chat and task
// recovery requests alike.
func HistoryInputItems(history []models.Execution) []any {
	return TakeHistoryInputItems(context.Background(), history)
}

// TakeHistoryInputItems hands prepared history to task/direct recovery, just as
// chat sending does. The cache must not retain a second decoded representation.
func TakeHistoryInputItems(ctx context.Context, history []models.Execution) []any {
	var items []any
	for _, message := range preparedhistory.Take(ctx, history, buildClientHistory) {
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
		if !ok || (item["type"] != nil && item["type"] != "message") || item["role"] != "user" {
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
		if strings.TrimSpace(text) != "" && !llmprompt.IsApplicationContext(text) && !llmprompt.IsCompactionContext(text) {
			// No execution ID or replay: these are retained user messages, not
			// database executions whose full history should be hydrated again.
			// Preserve text annotations and provider message IDs. Drop media from
			// retained user context, as it is not part of the 20k text budget.
			retained := make(map[string]any, len(item))
			for key, value := range item {
				retained[key] = value
			}
			if parts, ok := item["content"].([]any); ok {
				for _, value := range parts {
					part, ok := value.(map[string]any)
					if !ok || part["type"] != "input_text" {
						retained["content"] = []any{map[string]any{"type": "input_text", "text": text}}
						// The replacement has one text part, even when no text
						// truncation is needed. Keep its classifications aligned.
						for _, key := range []string{"internal_chat_message_metadata", "internal_chat_message_metadata_passthrough"} {
							if metadata, ok := item[key].(map[string]any); ok {
								if _, exists := metadata["content_item_kinds"]; exists {
									updated := make(map[string]any, len(metadata))
									for field, value := range metadata {
										updated[field] = value
									}
									updated["content_item_kinds"] = []string{"user.text"}
									retained[key] = updated
								}
							}
						}
						break
					}
				}
			}
			raw, _ := json.Marshal(retained)
			users = append(users, models.Execution{PromptSent: text, Status: models.ExecCompleted, ContextKind: "user", RetainedUserMessage: raw})
		}
	}
	return users
}

func decodeNativeInput(raw string) ([]any, error) {
	var items []any
	err := json.Unmarshal([]byte(raw), &items)
	return items, err
}
