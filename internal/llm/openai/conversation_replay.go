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

type responsesReplay struct {
	Input   []json.RawMessage            `json:"responses_input"`
	Sources map[string][]json.RawMessage `json:"responses_compaction_sources,omitempty"`
}

type responsesRecorder struct {
	history func(context.Context, []any) error
	archive func(context.Context, []any, []any) error
}

func (a *Adapter) newResponsesRecorder(execID string) responsesRecorder {
	if a.execRepo == nil || execID == "" {
		return responsesRecorder{}
	}
	saved := responsesReplay{}
	loaded := false
	load := func(ctx context.Context) error {
		if loaded {
			return nil
		}
		rows, err := a.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{execID})
		if err != nil {
			return err
		}
		for _, row := range rows[execID] {
			_ = json.Unmarshal([]byte(row.TranscriptJSON), &saved)
		}
		loaded = true
		return nil
	}
	persist := func(ctx context.Context) error {
		raw, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		if err := a.saveConversation(ctx, execID, json.RawMessage(raw)); err != nil {
			return err
		}
		// Recovery requests need the model input, not the archive.
		recovery, err := json.Marshal(responsesReplay{Input: saved.Input})
		if err != nil {
			return err
		}
		llmcontracts.RecordResponsesRecovery(ctx, string(recovery))
		return nil
	}
	return responsesRecorder{
		history: func(ctx context.Context, items []any) error {
			if err := load(ctx); err != nil {
				return err
			}
			raw, err := json.Marshal(items)
			if err != nil {
				return err
			}
			var input []json.RawMessage
			if err := json.Unmarshal(raw, &input); err != nil {
				return err
			}
			saved.Input = input
			return persist(ctx)
		},
		archive: func(ctx context.Context, before, after []any) error {
			if err := load(ctx); err != nil {
				return err
			}
			raw, err := json.Marshal(before)
			if err != nil {
				return err
			}
			var source []json.RawMessage
			if err := json.Unmarshal(raw, &source); err != nil {
				return err
			}
			if saved.Sources == nil {
				saved.Sources = make(map[string][]json.RawMessage)
			}
			existing := make(map[string]bool)
			for _, raw := range source {
				var item struct {
					Type string
					Key  string `json:"encrypted_content"`
				}
				if json.Unmarshal(raw, &item) == nil && item.Type == "compaction" {
					existing[item.Key] = true
				}
			}
			for _, item := range after {
				block, ok := item.(map[string]any)
				if !ok || block["type"] != "compaction" {
					continue
				}
				key, _ := block["encrypted_content"].(string)
				if key != "" && !existing[key] {
					saved.Sources[key] = source
				}
			}
			// Keep the original request durable until the compacted request is saved.
			saved.Input = source
			return persist(ctx)
		},
	}
}

// CompactionSources returns durable readable snapshots, never model input.
func CompactionSources(history []models.Execution) map[string][]json.RawMessage {
	sources := make(map[string][]json.RawMessage)
	for _, exec := range history {
		for _, replay := range exec.ReplayMessages {
			var saved responsesReplay
			if json.Unmarshal([]byte(replay.TranscriptJSON), &saved) == nil {
				for key, source := range saved.Sources {
					sources[key] = source
				}
			}
		}
	}
	return sources
}

// ExpandCompactedHistory substitutes readable snapshots for native opaque state.
// A compaction item replaces its preceding prefix, including retained user items.
func ExpandCompactedHistory(history []models.Execution, sources map[string][]json.RawMessage) ([]models.Execution, error) {
	var expand func([]json.RawMessage, map[string]bool) ([]json.RawMessage, error)
	expand = func(items []json.RawMessage, visiting map[string]bool) ([]json.RawMessage, error) {
		for i := len(items) - 1; i >= 0; i-- {
			var item struct {
				Type string
				Key  string `json:"encrypted_content"`
			}
			if json.Unmarshal(items[i], &item) != nil || item.Type != "compaction" {
				continue
			}
			source, ok := sources[item.Key]
			if !ok || len(source) == 0 || visiting[item.Key] {
				return nil, fmt.Errorf("cannot switch providers: readable history for native OpenAI compaction is unavailable; continue with OpenAI or start a new context")
			}
			visiting[item.Key] = true
			prefix, err := expand(source, visiting)
			if err != nil {
				return nil, err
			}
			delete(visiting, item.Key)
			return append(append([]json.RawMessage(nil), prefix...), items[i+1:]...), nil
		}
		return items, nil
	}
	out := append([]models.Execution(nil), EffectiveReplayHistory(history)...)
	for i := range out {
		out[i].ReplayMessages = append([]models.ExecutionReplayMessage(nil), out[i].ReplayMessages...)
		for j := range out[i].ReplayMessages {
			var saved responsesReplay
			if json.Unmarshal([]byte(out[i].ReplayMessages[j].TranscriptJSON), &saved) != nil || len(saved.Input) == 0 {
				continue
			}
			input, err := expand(saved.Input, make(map[string]bool))
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(responsesReplay{Input: input})
			if err != nil {
				return nil, err
			}
			out[i].ReplayMessages[j].TranscriptJSON = string(raw)
		}
	}
	return out, nil
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
	pending := make(map[string]bool)
	var deferred []openaiclient.CompletionsHistoryMessage
	flushDeferred := func() {
		result = append(result, deferred...)
		deferred = nil
	}
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
			pending[item.CallID] = true
			call := openaiclient.CompletionsToolCall{ID: item.CallID, Type: "function"}
			call.Function.Name = item.Name
			call.Function.Arguments = item.Arguments
			// Responses emits parallel calls as separate items. Completions
			// requires them in one assistant message before their tool results.
			if n := len(result); n > 0 && result[n-1].Role == "assistant" && len(result[n-1].ToolCalls) > 0 {
				result[n-1].ToolCalls = append(result[n-1].ToolCalls, call)
				continue
			}
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "assistant", ToolCalls: []openaiclient.CompletionsToolCall{call}})
		case "function_call_output":
			result = append(result, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: item.CallID, Content: item.Output})
			delete(pending, item.CallID)
			if len(pending) == 0 {
				flushDeferred()
			}
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
			// Responses may place view_image user messages between parallel
			// results. Chat Completions requires all results before user messages.
			if message.Role == "user" && len(pending) > 0 {
				deferred = append(deferred, message)
				continue
			}
			if message.Role == "assistant" {
				// Do not move deferred content across an assistant turn, even
				// when the saved transcript has missing tool results.
				flushDeferred()
				clear(pending)
			}
			result = append(result, message)
		}
	}
	// Preserve images even when the saved transcript ends mid-tool-round.
	flushDeferred()
	return result
}

// ResponsesSummaryMessages translates a saved Responses conversation for a
// summary call after switching providers. Tool calls and results stay paired.
func ResponsesSummaryMessages(items []json.RawMessage) []openaiclient.CompletionsHistoryMessage {
	return responsesCompletionsMessages(items)
}
