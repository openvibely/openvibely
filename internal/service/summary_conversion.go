package service

import (
	"encoding/json"
	"strings"

	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// Replay records carry their original provider's schema. Translate foreign
// records before decoding them for the summary provider; never silently fall
// back to just the execution's prompt and final answer.
func normalizeSummaryReplay(provider models.LLMProvider, history []models.Execution) []models.Execution {
	return normalizeProviderReplay(provider, history, true)
}

// Normalize foreign history before either continuation or summary preparation.
// The source records stay unchanged; only the outgoing copy is converted.
func normalizeProviderReplay(provider models.LLMProvider, history []models.Execution, forSummary bool) []models.Execution {
	history = llmopenai.EffectiveReplayHistory(history)
	out := append([]models.Execution(nil), history...)
	for i := range out {
		converted := false
		out[i].ReplayMessages = append([]models.ExecutionReplayMessage(nil), out[i].ReplayMessages...)
		for j, replay := range out[i].ReplayMessages {
			var envelope struct {
				Input       []json.RawMessage         `json:"responses_input"`
				Messages    []anthropicclient.Message `json:"messages"`
				Environment string                    `json:"anthropic_execution_environment"`
			}
			_ = json.Unmarshal([]byte(replay.TranscriptJSON), &envelope)
			var messages []openaiclient.CompletionsHistoryMessage
			switch {
			case len(envelope.Input) > 0:
				if provider == models.ProviderOpenAI {
					continue
				}
				messages = llmopenai.ResponsesSummaryMessages(envelope.Input)
			case len(envelope.Messages) > 0:
				if provider == models.ProviderAnthropic {
					continue
				}
				if envelope.Environment != "" {
					messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "user", Content: envelope.Environment})
				}
				messages = append(messages, anthropicReplayMessages(envelope.Messages, forSummary)...)
			default:
				if provider != models.ProviderAnthropic {
					continue
				}
				if json.Unmarshal([]byte(replay.TranscriptJSON), &messages) != nil {
					continue
				}
			}
			if len(messages) == 0 {
				continue
			}
			if j == len(out[i].ReplayMessages)-1 && out[i].Status == models.ExecFailed {
				last := messages[len(messages)-1]
				if last.Role != "assistant" || (last.Content == "" && len(last.ImageContent) == 0 && len(last.ToolCalls) == 0) {
					if output := llmprompt.ReplayAssistantContent(out[i]); output != "" {
						messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "assistant", Content: output})
					}
				}
			}
			var raw []byte
			if provider == models.ProviderAnthropic {
				raw, _ = json.Marshal(map[string]any{"messages": completionsAnthropicSummaryMessages(messages)})
			} else {
				raw, _ = json.Marshal(messages)
			}
			out[i].ReplayMessages[j].TranscriptJSON = string(raw)
			converted = true
		}
		if converted && provider == models.ProviderAnthropic {
			var merged []anthropicclient.Message
			for _, replay := range out[i].ReplayMessages {
				var saved struct{ Messages []anthropicclient.Message }
				if json.Unmarshal([]byte(replay.TranscriptJSON), &saved) == nil && len(saved.Messages) > 0 {
					merged = append(merged, saved.Messages...)
				} else {
					if replay.UserContent != "" {
						merged = append(merged, anthropicclient.Message{Role: "user", Content: replay.UserContent})
					}
					if replay.AssistantContent != "" {
						merged = append(merged, anthropicclient.Message{Role: "assistant", Content: replay.AssistantContent})
					}
				}
			}
			raw, _ := json.Marshal(map[string]any{"messages": merged})
			out[i].ReplayMessages = []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}
		}
	}
	return out
}

func anthropicReplayMessages(messages []anthropicclient.Message, forSummary bool) []openaiclient.CompletionsHistoryMessage {
	var out []openaiclient.CompletionsHistoryMessage
	for _, message := range messages {
		if len(message.ContentBlocks) == 0 {
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: message.Role, Content: message.Content})
			continue
		}
		var blocks []map[string]any
		_ = json.Unmarshal(message.ContentBlocks, &blocks)
		current := openaiclient.CompletionsHistoryMessage{Role: message.Role}
		flush := func() {
			if current.Content != "" || len(current.ToolCalls) > 0 || len(current.ImageContent) > 0 {
				out = append(out, current)
			}
			current = openaiclient.CompletionsHistoryMessage{Role: message.Role}
		}
		for _, block := range blocks {
			switch block["type"] {
			case "text":
				text, _ := block["text"].(string)
				current.ImageContent = append(current.ImageContent, map[string]interface{}{"type": "text", "text": text})
			case "tool_use":
				input, _ := json.Marshal(block["input"])
				call := openaiclient.CompletionsToolCall{Type: "function"}
				call.ID, _ = block["id"].(string)
				call.Function.Name, _ = block["name"].(string)
				call.Function.Arguments = string(input)
				current.ToolCalls = append(current.ToolCalls, call)
			case "tool_result":
				flush()
				id, _ := block["tool_use_id"].(string)
				content, ok := block["content"].(string)
				if !ok {
					raw, _ := json.Marshal(block["content"])
					content = string(raw)
				}
				out = append(out, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: id, Content: content})
			case "image":
				source, _ := block["source"].(map[string]any)
				url, _ := source["url"].(string)
				if source["type"] == "base64" {
					media, _ := source["media_type"].(string)
					data, _ := source["data"].(string)
					url = "data:" + media + ";base64," + data
				}
				current.ImageContent = append(current.ImageContent, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}})
			default:
				if !forSummary && (block["type"] == "thinking" || block["type"] == "redacted_thinking") {
					// Provider-specific reasoning/signatures are not portable
					// assistant content for an ordinary continuation.
					continue
				}
				// Provider-specific blocks cannot be replayed as native blocks elsewhere.
				// Preserve their readable content as historical data.
				raw, _ := json.Marshal(block)
				current.ImageContent = append(current.ImageContent, map[string]interface{}{"type": "text", "text": "Historical provider block: " + string(raw)})
			}
		}
		flush()
	}
	return out
}

func completionsAnthropicSummaryMessages(messages []openaiclient.CompletionsHistoryMessage) []anthropicclient.Message {
	var out []anthropicclient.Message
	var toolResults []map[string]any
	flushToolResults := func() {
		if len(toolResults) == 0 {
			return
		}
		raw, _ := json.Marshal(toolResults)
		out = append(out, anthropicclient.Message{Role: "user", ContentBlocks: raw})
		toolResults = nil
	}
	for _, message := range messages {
		// Anthropic expects parallel results together in the next user message.
		// Flush at any non-tool message so separate rounds are never combined.
		if message.Role == "tool" {
			toolResults = append(toolResults, map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content})
			continue
		}
		flushToolResults()
		role := message.Role
		if role == "system" || role == "developer" {
			role = "user"
		}
		var blocks []map[string]any
		if message.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
		}
		for _, part := range message.ImageContent {
			if part["type"] == "text" {
				blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
			}
			if part["type"] == "image_url" {
				image, _ := part["image_url"].(map[string]interface{})
				url, _ := image["url"].(string)
				source := map[string]any{"type": "url", "url": url}
				if strings.HasPrefix(url, "data:") {
					header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,")
					if ok {
						source = map[string]any{"type": "base64", "media_type": header, "data": data}
					}
				}
				blocks = append(blocks, map[string]any{"type": "image", "source": source})
			}
		}
		for _, call := range message.ToolCalls {
			var input any
			if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
				input = map[string]any{"raw_arguments": call.Function.Arguments}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
		}
		if len(blocks) > 0 {
			raw, _ := json.Marshal(blocks)
			out = append(out, anthropicclient.Message{Role: role, ContentBlocks: raw})
		}
	}
	flushToolResults()
	return out
}
