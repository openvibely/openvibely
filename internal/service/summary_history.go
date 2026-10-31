package service

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"strings"

	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func summaryHistoryInput(provider models.LLMProvider, history []models.Execution) ([]json.RawMessage, error) {
	if provider != models.ProviderOpenAI {
		var err error
		history, err = readableCompactedHistory(history)
		if err != nil {
			return nil, err
		}
	}
	history = normalizeSummaryReplay(provider, history)
	switch provider {
	case models.ProviderOpenAI:
		return summaryRows(llmopenai.HistoryInputItems(history))
	case models.ProviderAnthropic:
		return summaryRows(llmanthropic.SummaryHistory(history))
	default:
		return summaryRows(compatible.SummaryHistory(history))
	}
}

func summaryRows[T any](messages []T) ([]json.RawMessage, error) {
	snapshot, err := recovery.Capture(messages, func(m T) ([]byte, error) { return json.Marshal(m) })
	if err != nil {
		return nil, err
	}
	return snapshot.Rows("")
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
	if len(input) == 0 {
		return 0
	}
	imageType := "image_url"
	if provider == models.ProviderAnthropic {
		imageType = "image"
	}
	tokens, _ := tokenestimate.FromRows(input, imageType, 2000, "content_blocks", "image_content")
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
	if jsonview.Object(raw, func(k string, v json.RawMessage) error {
		switch strings.ToLower(k) {
		case "content":
			item.Content = v
		case "content_blocks":
			var err error
			item.ContentBlocks, err = jsonview.Array(v)
			return err
		case "type":
			return json.Unmarshal(v, &item.Type)
		case "id":
			return json.Unmarshal(v, &item.ID)
		case "call_id":
			return json.Unmarshal(v, &item.CallID)
		case "tool_call_id":
			return json.Unmarshal(v, &item.ToolCallID)
		case "tool_use_id":
			return json.Unmarshal(v, &item.ToolUseID)
		case "tool_calls":
			return json.Unmarshal(v, &item.ToolCalls)
		}
		return nil
	}) != nil {
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
	content, _ := jsonview.Array(item.Content)
	for _, block := range append(content, item.ContentBlocks...) {
		ids = append(ids, summaryToolIDs(block)...)
	}
	return ids
}
