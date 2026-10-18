package service

import (
	"encoding/json"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/managedcontext"
)

// Rebuild current application context before committing replacement history.
// Adapters still refresh dynamic context at dispatch and deduplicate these rows.
func restoreCompactedApplicationContext(req llmcontracts.AgentRequest, history []models.Execution) []models.Execution {
	out := make([]models.Execution, 0, len(history)+3)
	var saved []models.Execution
	for _, execution := range history {
		if llmprompt.IsApplicationContext(execution.PromptSent) {
			saved = append(saved, execution)
		} else {
			out = append(out, execution)
		}
	}
	read := func(execution models.Execution) (string, string) { return "user", execution.PromptSent }
	makeContext := func(text string) models.Execution {
		return models.Execution{PromptSent: text, Status: models.ExecCompleted, ContextKind: "application"}
	}
	// A missing source (for example while restoring a checkpoint) keeps its
	// saved context. An explicitly disabled managed index removes the old one.
	saved = managedcontext.Ensure(req.Ctx, saved, read, makeContext)
	saved = attachmentcontext.Ensure(req.Ctx, saved, read, makeContext)
	var restored []models.Execution
	if !req.RawDirectPrompt && !req.LifecycleHookCall {
		restored = append(restored, makeContext(executionenv.Context(req.WorkDir)))
	}
	for _, marker := range []string{managedcontext.Marker, attachmentcontext.Marker} {
		for i := len(saved) - 1; i >= 0; i-- {
			if strings.HasPrefix(saved[i].PromptSent, marker+"\n") {
				restored = append(restored, makeContext(saved[i].PromptSent))
				break
			}
		}
	}

	// Match Codex placement: before the last real user message, or the summary
	// when there is no retained user message.
	at := len(out)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].PromptSent != "" {
			at = i
			if !llmprompt.IsCompactionContext(out[i].PromptSent) {
				break
			}
		}
	}
	result := append([]models.Execution(nil), out[:at]...)
	result = append(result, restored...)
	return append(result, out[at:]...)
}

// Injected context already saved in history must not consume the budget twice.
func savedApplicationContextTokens(req llmcontracts.AgentRequest) int {
	texts := []string{managedcontext.Text(req.Ctx), attachmentcontext.Text(req.Ctx)}
	if !req.RawDirectPrompt && !req.LifecycleHookCall {
		texts = append(texts, executionenv.Context(req.WorkDir))
	}
	saved := map[string]bool{}
	for _, execution := range req.ChatHistory {
		if len(execution.ReplayMessages) == 0 {
			saved[execution.PromptSent] = true
		}
	}
	for _, raw := range req.SummaryInput {
		var item struct{ Role, Content string }
		if json.Unmarshal(raw, &item) == nil && item.Role == "user" {
			saved[item.Content] = true
		}
	}
	total := 0
	for _, text := range texts {
		if text != "" && saved[text] {
			total += estimatedUTF8Tokens(text)
		}
	}
	return total
}

// Keep only complete linked tool groups after count/budget truncation. Recovery
// can store a call and its result in different execution records.
func retainCompleteToolGroups(original, retained []models.Execution) []models.Execution {
	originalCalls, originalResults := historyToolSides(original)
	out := append([]models.Execution(nil), retained...)
	for {
		calls, results := historyToolSides(out)
		incomplete := map[string]bool{}
		for id := range originalCalls {
			if originalResults[id] && calls[id] != results[id] {
				incomplete[id] = true
			}
		}
		if len(incomplete) == 0 {
			break
		}
		next := make([]models.Execution, 0, len(out))
		for _, execution := range out {
			remove := false
			for _, replay := range execution.ReplayMessages {
				for _, id := range replayToolIDs(replay.TranscriptJSON) {
					if incomplete[id] {
						remove = true
						break
					}
				}
				if remove {
					break
				}
			}
			if !remove {
				next = append(next, execution)
			}
		}
		if len(next) == len(out) {
			break
		}
		out = next
	}

	return out
}

func replayToolIDs(transcript string) []string {
	var messages []json.RawMessage
	if json.Unmarshal([]byte(transcript), &messages) != nil {
		var saved struct {
			Messages []json.RawMessage
			Input    []json.RawMessage `json:"responses_input"`
		}
		if json.Unmarshal([]byte(transcript), &saved) != nil {
			return nil
		}
		messages = append(saved.Messages, saved.Input...)
	}
	var ids []string
	for _, message := range messages {
		ids = append(ids, summaryToolIDs(message)...)
	}
	return ids
}

// Sets, rather than transcript identities, allow a newer Responses checkpoint
// to retain a complete exchange also present in an older discarded checkpoint.
func historyToolSides(history []models.Execution) (map[string]bool, map[string]bool) {
	calls, results := map[string]bool{}, map[string]bool{}
	add := func(set map[string]bool, value any) {
		if id, ok := value.(string); ok && id != "" {
			set[id] = true
		}
	}
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			switch item["type"] {
			case "function_call", "custom_tool_call":
				add(calls, item["call_id"])
			case "function_call_output", "custom_tool_call_output":
				add(results, item["call_id"])
			case "tool_use", "server_tool_use":
				add(calls, item["id"])
			case "tool_result":
				add(results, item["tool_use_id"])
			}
			if item["role"] == "tool" {
				add(results, item["tool_call_id"])
			}
			if tools, ok := item["tool_calls"].([]any); ok {
				for _, raw := range tools {
					if call, ok := raw.(map[string]any); ok {
						add(calls, call["id"])
					}
				}
			}
			for _, key := range []string{"messages", "responses_input", "content", "content_blocks"} {
				visit(item[key])
			}
		}
	}
	for _, execution := range history {
		for _, replay := range execution.ReplayMessages {
			var raw any
			if json.Unmarshal([]byte(replay.TranscriptJSON), &raw) == nil {
				visit(raw)
			}
		}
	}
	return calls, results
}

// Truncating text invalidates content annotations, but not the source message's
// ID. Keep its identity and message metadata while replacing the content parts.
func retainedUserTextFallback(raw json.RawMessage, text string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var message map[string]any
	if json.Unmarshal(raw, &message) != nil {
		return nil
	}
	message["content"] = []any{map[string]any{"type": "input_text", "text": text}}
	for _, key := range []string{"internal_chat_message_metadata", "internal_chat_message_metadata_passthrough"} {
		if metadata, ok := message[key].(map[string]any); ok {
			if _, exists := metadata["content_item_kinds"]; exists {
				metadata["content_item_kinds"] = []string{"user.text"}
			}
		}
	}
	encoded, _ := json.Marshal(message)
	return encoded
}
