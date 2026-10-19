package service

import (
	"encoding/json"

	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func summaryHistoryInput(provider models.LLMProvider, history []models.Execution) ([]json.RawMessage, error) {
	if provider != models.ProviderOpenAI {
		var err error
		history, err = llmopenai.ExpandCompactedHistory(history, llmopenai.CompactionSources(history))
		if err != nil {
			return nil, err
		}
	}
	history = normalizeSummaryReplay(provider, history)
	var messages any
	switch provider {
	case models.ProviderOpenAI:
		messages = llmopenai.HistoryInputItems(history)
	case models.ProviderAnthropic:
		messages = llmanthropic.SummaryHistory(history)
	default:
		messages = compatible.SummaryHistory(history)
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	var input []json.RawMessage
	err = json.Unmarshal(raw, &input)
	return input, err
}

func estimateSummaryInputTokens(provider models.LLMProvider, input []json.RawMessage) int {
	if provider == models.ProviderOpenAI {
		items := make([]any, 0, len(input))
		for _, raw := range input {
			var item any
			if json.Unmarshal(raw, &item) == nil {
				items = append(items, item)
			}
		}
		return openaiclient.EstimateResponsesInputTokens(items)
	}
	// Normalize the provider's content field before using the image-aware
	// estimator. Base64 bytes are not text tokens.
	var messages []map[string]json.RawMessage
	for _, raw := range input {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if blocks := message["content_blocks"]; len(blocks) > 0 {
			message["content"] = blocks
			delete(message, "content_blocks")
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return 0
	}
	raw, _ := json.Marshal(messages)
	imageType := "image_url"
	if provider == models.ProviderAnthropic {
		imageType = "image"
	}
	tokens, _ := estimateReplayTranscriptImageTokens(string(raw), imageType, 2000)
	return tokens
}

// Remove a whole message and every message connected by a tool-call ID.
// A message can contain several parallel calls/results, so follow the complete
// connected group rather than leaving an orphan from a second call in that row.
// The input and its raw messages are never mutated.
func removeOldestSummaryMessage(input []json.RawMessage) []json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	ids := make([][]string, len(input))
	for i, raw := range input {
		ids[i] = summaryToolIDs(raw)
	}
	removed := make([]bool, len(input))
	removed[0] = true
	linked := map[string]bool{}
	for _, id := range ids[0] {
		linked[id] = true
	}
	for changed := true; changed; {
		changed = false
		for i := range input {
			if removed[i] {
				continue
			}
			for _, id := range ids[i] {
				if linked[id] {
					removed[i] = true
					changed = true
					for _, partner := range ids[i] {
						linked[partner] = true
					}
					break
				}
			}
		}
	}
	out := make([]json.RawMessage, 0, len(input)-1)
	for i, raw := range input {
		if !removed[i] {
			out = append(out, raw)
		}
	}
	return out
}

func summaryToolIDs(raw json.RawMessage) []string {
	var item struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
		ToolUseID  string `json:"tool_use_id"`
		ToolCalls  []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Content       json.RawMessage   `json:"content"`
		ContentBlocks []json.RawMessage `json:"content_blocks"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return nil
	}
	var ids []string
	for _, id := range []string{item.CallID, item.ToolCallID, item.ToolUseID} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if (item.Type == "tool_use" || item.Type == "server_tool_use") && item.ID != "" {
		ids = append(ids, item.ID)
	}
	for _, call := range item.ToolCalls {
		if call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	var content []json.RawMessage
	_ = json.Unmarshal(item.Content, &content)
	for _, block := range append(content, item.ContentBlocks...) {
		ids = append(ids, summaryToolIDs(block)...)
	}
	return ids
}
