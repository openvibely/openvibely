package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func (a *Adapter) saveConversation(ctx context.Context, execID string, transcript any) error {
	if a.execRepo == nil || execID == "" {
		return nil
	}
	raw, err := json.Marshal(transcript)
	if err != nil {
		return fmt.Errorf("encode OpenAI conversation: %w", err)
	}
	if err := a.execRepo.ReplaceReasoningReplay(context.WithoutCancel(ctx), execID, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}); err != nil {
		return fmt.Errorf("save OpenAI conversation: %w", err)
	}
	return nil
}

func (a *Adapter) responsesHistoryRecorder(execID string) func(context.Context, []any) error {
	if a.execRepo == nil || execID == "" {
		return nil
	}
	return func(ctx context.Context, items []any) error {
		raw, err := json.Marshal(struct {
			Input []any `json:"responses_input"`
		}{items})
		if err != nil {
			return err
		}
		if err := a.saveConversation(ctx, execID, json.RawMessage(raw)); err != nil {
			return err
		}
		llmcontracts.RecordResponsesRecovery(ctx, string(raw))
		return nil
	}
}

func (a *Adapter) saveCompletionsHistory(ctx context.Context, execID string, client *openaiclient.Client, callErr error) error {
	callErr = client.ConversationError(callErr)
	transcript := client.LastCompletionsTranscript()
	if len(transcript) == 0 {
		return callErr
	}
	return errors.Join(callErr, a.saveConversation(ctx, execID, transcript))
}

func buildCompletionsHistory(history []models.Execution) []openaiclient.CompletionsHistoryMessage {
	var result []openaiclient.CompletionsHistoryMessage
	for _, exec := range history {
		restored := false
		for _, replay := range exec.ReplayMessages {
			var checkpoint struct {
				Input []json.RawMessage `json:"responses_input"`
			}
			if json.Unmarshal([]byte(replay.TranscriptJSON), &checkpoint) == nil && len(checkpoint.Input) > 0 {
				result = responsesCompletionsMessages(checkpoint.Input)
				restored = true
				continue
			}
			var messages []openaiclient.CompletionsHistoryMessage
			if json.Unmarshal([]byte(replay.TranscriptJSON), &messages) == nil && len(messages) > 0 {
				result = append(result, messages...)
				restored = true
			}
		}
		if restored {
			continue
		}
		if exec.PromptSent != "" {
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "user", Content: exec.PromptSent})
		}
		if content := llmprompt.ReplayAssistantContent(exec); content != "" {
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "assistant", Content: content})
		}
	}
	return result
}

func historyMessageItems(messages []openaiclient.Message) []any {
	var items []any
	for _, m := range messages {
		if len(m.ResponsesInputItems) > 0 {
			items = append([]any(nil), m.ResponsesInputItems...)
		} else {
			items = append(items, map[string]any{"type": "message", "role": m.Role, "content": m.Content})
		}
	}
	return items
}

func completionsReplayInput(raw string) []any {
	var messages []openaiclient.CompletionsHistoryMessage
	if json.Unmarshal([]byte(raw), &messages) != nil {
		return nil
	}
	var items []any
	for _, m := range messages {
		if m.Role == "tool" {
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
			continue
		}
		if m.Content != "" {
			items = append(items, map[string]any{"type": "message", "role": m.Role, "content": m.Content})
		}
		if len(m.ImageContent) > 0 {
			var content []any
			for _, part := range m.ImageContent {
				if part["type"] == "text" {
					content = append(content, map[string]any{"type": "input_text", "text": part["text"]})
				}
				if part["type"] == "image_url" {
					if image, ok := part["image_url"].(map[string]interface{}); ok {
						content = append(content, map[string]any{"type": "input_image", "image_url": image["url"]})
					}
				}
			}
			items = append(items, map[string]any{"type": "message", "role": m.Role, "content": content})
		}
		for _, call := range m.ToolCalls {
			items = append(items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	return items
}

// Translate conversation items when a Responses execution is followed by the
// Completions fallback. Provider-only reasoning/compaction items are not chat messages.
func responsesCompletionsMessages(items []json.RawMessage) []openaiclient.CompletionsHistoryMessage {
	var result []openaiclient.CompletionsHistoryMessage
	for _, raw := range items {
		var item struct {
			Type, Role, Name, Arguments, Output string
			CallID                              string `json:"call_id"`
			Content                             json.RawMessage
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch item.Type {
		case "function_call":
			call := openaiclient.CompletionsToolCall{ID: item.CallID, Type: "function"}
			call.Function.Name = item.Name
			call.Function.Arguments = item.Arguments
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "assistant", ToolCalls: []openaiclient.CompletionsToolCall{call}})
		case "function_call_output":
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: item.CallID, Content: item.Output})
		case "message", "":
			if item.Role == "" {
				continue
			}
			message := openaiclient.CompletionsHistoryMessage{Role: item.Role}
			if json.Unmarshal(item.Content, &message.Content) != nil {
				var blocks []struct {
					Type, Text string
					ImageURL   string `json:"image_url"`
				}
				if json.Unmarshal(item.Content, &blocks) != nil {
					continue
				}
				for _, block := range blocks {
					switch block.Type {
					case "input_text", "output_text":
						message.ImageContent = append(message.ImageContent, map[string]interface{}{"type": "text", "text": block.Text})
					case "input_image":
						message.ImageContent = append(message.ImageContent, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": block.ImageURL}})
					}
				}
			}
			result = append(result, message)
		}
	}
	return result
}

// ResponsesSummaryMessages translates a saved Responses conversation for a
// summary call after switching providers. Tool calls and results stay paired.
func ResponsesSummaryMessages(items []json.RawMessage) []openaiclient.CompletionsHistoryMessage {
	return responsesCompletionsMessages(items)
}
