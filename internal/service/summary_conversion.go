package service

import (
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/llm/requestjson"

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
			// Native row histories already declare their format. No conversion is
			// needed for the same provider; avoid decoding/copying all payloads
			// merely to discover their format before the client decodes them.
			if replay.TranscriptJSON == "" && replay.Rows != nil {
				r := replay.Rows
				if (r.Format == "envelope" && len(r.Items) > 0 && ((r.ItemsKey == "responses_input" && provider == models.ProviderOpenAI) || (r.ItemsKey == "messages" && provider == models.ProviderAnthropic))) || (r.Format == "array" && provider != models.ProviderAnthropic) {
					continue
				}
			}
			var envelope struct {
				Input       []json.RawMessage `json:"responses_input"`
				Messages    []json.RawMessage `json:"messages"`
				Environment string            `json:"anthropic_execution_environment"`
			}
			_ = replay.DecodeTranscript(&envelope)
			var messages []openaiclient.CompletionsHistoryMessage
			switch {
			case len(envelope.Input) > 0:
				if provider == models.ProviderOpenAI {
					continue
				}
				llmopenai.PreparedResponsesSummaryMessages(envelope.Input).Visit(func(m openaiclient.CompletionsHistoryMessage, _ any) { messages = append(messages, m) })
			case len(envelope.Messages) > 0:
				if provider == models.ProviderAnthropic {
					continue
				}
				if envelope.Environment != "" {
					messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "user", Content: envelope.Environment})
				}
				var native []anthropicclient.Message
				for _, row := range envelope.Messages {
					m, err := anthropicclient.DecodeReplayMessage(row)
					if err != nil {
						// Unusual rows retain the old decoder's partial-result behavior.
						var legacy struct {
							Messages []anthropicclient.Message `json:"messages"`
						}
						_ = replay.DecodeTranscript(&legacy)
						native = legacy.Messages
						break
					}
					native = append(native, m)
				}
				messages = append(messages, preparedAnthropicReplayMessages(native, forSummary)...)
			default:
				if provider != models.ProviderAnthropic {
					continue
				}
				prepared, err := openaiclient.PrepareCompletionsHistory(replay)
				if err != nil {
					continue
				}
				prepared.Visit(func(m openaiclient.CompletionsHistoryMessage, _ any) { messages = append(messages, m) })
			}
			messages = repairConvertedToolResults(messages)
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
			var next models.ExecutionReplayMessage
			if provider == models.ProviderAnthropic {
				rows, _ := summaryRows(completionsAnthropicSummaryMessages(messages))
				next = models.ReplayFromRows("messages", rows)
			} else {
				rows, _ := openaiclient.PreparedCompletionsMessages(messages).SnapshotRows()
				next = models.ReplayFromRows("", rows)
			}
			out[i].ReplayMessages[j].TranscriptJSON = ""
			out[i].ReplayMessages[j].Rows = next.Rows
			converted = true
		}
		if converted && provider == models.ProviderAnthropic {
			var merged []json.RawMessage
			for _, replay := range out[i].ReplayMessages {
				var saved struct{ Messages []json.RawMessage }
				valid := replay.DecodeTranscript(&saved) == nil && len(saved.Messages) > 0
				if valid {
					for _, row := range saved.Messages {
						if _, err := anthropicclient.DecodeReplayMessage(row); err != nil {
							valid = false
							break
						}
					}
				}
				if valid {
					merged = append(merged, saved.Messages...)
					continue
				}
				if replay.UserContent != "" {
					row, _ := anthropicclient.EncodeReplayMessage(anthropicclient.Message{Role: "user", Content: replay.UserContent})
					merged = append(merged, row)
				}
				if replay.AssistantContent != "" {
					row, _ := anthropicclient.EncodeReplayMessage(anthropicclient.Message{Role: "assistant", Content: replay.AssistantContent})
					merged = append(merged, row)
				}
			}
			out[i].ReplayMessages = []models.ExecutionReplayMessage{models.ReplayFromRows("messages", merged)}
		}
	}
	return out
}

func anthropicReplayMessages(messages []anthropicclient.Message, forSummary bool) []openaiclient.CompletionsHistoryMessage {
	return openaiclient.PreparedCompletionsMessages(preparedAnthropicReplayMessages(messages, forSummary)).DecodedMessages()
}

func preparedAnthropicReplayMessages(messages []anthropicclient.Message, forSummary bool) []openaiclient.CompletionsHistoryMessage {
	var out []openaiclient.CompletionsHistoryMessage
	// Keep consecutive tool results together, even when Anthropic stores them
	// in separate messages. Emit their images before the next ordinary message.
	var toolImages []map[string]interface{}
	flushImages := func() {
		if len(toolImages) > 0 {
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: "user", ImageContent: toolImages})
			toolImages = nil
		}
	}
	for _, message := range messages {
		if len(message.ContentBlocks) == 0 {
			flushImages()
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: message.Role, Content: message.Content})
			continue
		}
		rawBlocks, arrayErr := jsonview.Array(message.ContentBlocks)
		var blocks []map[string]any
		if arrayErr != nil {
			_ = json.Unmarshal(message.ContentBlocks, &blocks)
		}
		for _, raw := range rawBlocks {
			block, err := jsonview.ProjectObject(raw, func(fields map[string]json.RawMessage, key string) bool {
				var typ string
				_ = json.Unmarshal(fields["type"], &typ)
				value := fields[key]
				return typ == "tool_result" && key == "content" && len(value) > 0 && value[0] == '"'
			})
			if err != nil {
				// Preserve the decoded API's handling of null or unusual blocks.
				blocks = nil
				_ = json.Unmarshal(message.ContentBlocks, &blocks)
				break
			}
			blocks = append(blocks, block)
		}
		current := openaiclient.CompletionsHistoryMessage{Role: message.Role}
		flush := func() {
			if current.Content != "" || len(current.ToolCalls) > 0 || len(current.ImageContent) > 0 {
				out = append(out, current)
			}
			current = openaiclient.CompletionsHistoryMessage{Role: message.Role}
		}
		for _, block := range blocks {
			if block["type"] != "tool_result" {
				flushImages()
			}
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
				if value, ok := block["content"].(jsonview.Value); ok {
					tool, err := openaiclient.PreparedToolMessage(id, value.Raw())
					if err == nil {
						out = append(out, tool)
						continue
					}
				}
				content, ok := block["content"].(string)
				if !ok {
					if parts, array := block["content"].([]any); array {
						var text []string
						for _, part := range parts {
							item, _ := part.(map[string]any)
							switch item["type"] {
							case "image":
								toolImages = append(toolImages, anthropicImagePart(item))
							case "text":
								value, _ := item["text"].(string)
								text = append(text, value)
							default:
								raw, _ := json.Marshal(part)
								text = append(text, string(raw))
							}
						}
						content = strings.Join(text, "\n")
					} else {
						raw, _ := json.Marshal(block["content"])
						content = string(raw)
					}
				}
				out = append(out, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: id, Content: content})
			case "image":
				current.ImageContent = append(current.ImageContent, anthropicImagePart(block))
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
	flushImages()
	return out
}

// Images returned by tools use the same portable image shape as user uploads.
func anthropicImagePart(block map[string]any) map[string]interface{} {
	source, _ := block["source"].(map[string]any)
	url, _ := source["url"].(string)
	if source["type"] == "base64" {
		media, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		url = "data:" + media + ";base64," + data
	}
	return map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}}
}

func completionsAnthropicSummaryMessages(messages []openaiclient.CompletionsHistoryMessage) []anthropicclient.Message {
	var out []anthropicclient.Message
	var toolResults []map[string]any
	flushToolResults := func() {
		if len(toolResults) == 0 {
			return
		}
		items := make([]any, len(toolResults))
		for i := range toolResults {
			items[i] = toolResults[i]
		}
		field, err := requestjson.PrepareItems("content", items)
		if err != nil {
			return
		}
		raw := field.JSON()
		out = append(out, anthropicclient.Message{Role: "user", ContentBlocks: raw})
		toolResults = nil
	}
	for _, message := range messages {
		// Anthropic expects parallel results together in the next user message.
		// Flush at any non-tool message so separate rounds are never combined.
		if message.Role == "tool" {
			content := message.ReadOnlyContent()
			if view, ok := content.(jsonview.Value); ok {
				if value, err := view.CanonicalTextValue(); err == nil {
					content = value
				}
			}
			toolResults = append(toolResults, map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": content})
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

// Restored history can end after a persisted call but before execution finished.
// Close those exchanges without running the tool again.
func repairConvertedToolResults(messages []openaiclient.CompletionsHistoryMessage) []openaiclient.CompletionsHistoryMessage {
	var out []openaiclient.CompletionsHistoryMessage
	var pending []string
	done := make(map[string]bool)
	flush := func() {
		for _, id := range pending {
			if !done[id] {
				out = append(out, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: id, Content: "aborted"})
			}
		}
		pending = nil
		clear(done)
	}
	for _, m := range messages {
		if m.Role != "tool" {
			flush()
		}
		out = append(out, m)
		for _, call := range m.ToolCalls {
			pending = append(pending, call.ID)
		}
		if m.Role == "tool" {
			done[m.ToolCallID] = true
		}
	}
	flush()
	return out
}

func preparedResponsesMessages(rows []json.RawMessage) []openaiclient.CompletionsHistoryMessage {
	var messages []openaiclient.CompletionsHistoryMessage
	llmopenai.PreparedResponsesSummaryMessages(rows).Visit(func(m openaiclient.CompletionsHistoryMessage, _ any) { messages = append(messages, m) })
	return messages
}
