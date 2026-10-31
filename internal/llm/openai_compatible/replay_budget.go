package openai_compatible

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// EstimateHistoryTokens uses the same replay representation as the adapter,
// avoiding duplicate execution fields and counting images as images, not base64 text.
func EstimateHistoryTokens(history []models.Execution) int {
	return EstimateHistoryTokensContext(context.Background(), history)
}

func EstimateHistoryTokensContext(ctx context.Context, history []models.Execution) int {
	messages := preparedhistory.Read(ctx, history, buildClientHistory)
	if len(messages) == 0 {
		return 0
	}
	// Text-only messages can be counted without encoding and then decoding all
	// their text again. Keep multimodal/nested image semantics on the existing path.
	textOnly := true
	for _, message := range messages {
		if len(message.ImageContent) > 0 {
			textOnly = false
			break
		}
	}
	if textOnly {
		size := len(`{"messages":[]}`) + len(messages) - 1
		for _, message := range messages {
			content, reasoning := message.Content, message.ReasoningContent
			message.Content = ""
			if reasoning != "" {
				message.ReasoningContent = "x"
			}
			argumentBytes := 0
			if len(message.ToolCalls) > 0 {
				message.ToolCalls = append([]openaiclient.CompletionsToolCall(nil), message.ToolCalls...)
				for i := range message.ToolCalls {
					argumentBytes += requestjson.StringSize(message.ToolCalls[i].Function.Arguments) - 2
					message.ToolCalls[i].Function.Arguments = ""
				}
			}
			raw, err := json.Marshal(message)
			if err != nil {
				return 0
			}
			size += len(raw) + argumentBytes + jsonStringBytes(content) - 2
			if reasoning != "" {
				size += jsonStringBytes(reasoning) - 3
			}
		}
		return tokenestimate.FromByteCount(size)
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	tokens, _ := tokenestimate.FromMessageArray(raw, "image_url", 2000)
	return tokens
}

// Match encoding/json's default string escaping, including HTML, invalid UTF-8,
// and JavaScript line separators. Count bytes without allocating escaped text.
func jsonStringBytes(s string) int { return requestjson.StringSize(s) }
