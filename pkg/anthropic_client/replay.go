package anthropicclient

import "encoding/json"

// Repair only restored history, before starting the live tool loop. A missing
// result does not mean the tool never ran; never execute it again here.
func repairInterruptedToolCalls(messages []agenticMessage) []agenticMessage {
	out := make([]agenticMessage, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		out = append(out, message)
		if message.Role != "assistant" {
			continue
		}
		blocks, err := contentBlocksJSON(message.Content)
		if err != nil {
			continue
		}
		var calls []string
		for _, raw := range blocks {
			var b struct{ Type, ID string }
			if json.Unmarshal(raw, &b) == nil && b.Type == "tool_use" && b.ID != "" {
				calls = append(calls, b.ID)
			}
		}
		if len(calls) == 0 {
			continue
		}
		var next []json.RawMessage
		hasUser := i+1 < len(messages) && messages[i+1].Role == "user"
		if hasUser {
			content := messages[i+1].Content
			if text, ok := content.(string); ok {
				if text != "" {
					raw, _ := json.Marshal(map[string]string{"type": "text", "text": text})
					next = append(next, raw)
				}
			} else {
				next, err = contentBlocksJSON(content)
				if err != nil {
					// Do not replace undecodable content.
					continue
				}
			}
		}
		seen := map[string]bool{}
		for _, raw := range next {
			var b struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
			}
			if json.Unmarshal(raw, &b) == nil && b.Type == "tool_result" {
				seen[b.ToolUseID] = true
			}
		}
		// Keep unrelated follow-ups and managed context in their original format.
		// Only extend an existing tool-result message; otherwise insert a new one.
		if len(seen) == 0 {
			hasUser = false
			next = nil
		}
		var missing []json.RawMessage
		for _, id := range calls {
			if seen[id] {
				continue
			}
			seen[id] = true
			raw, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": true, "content": "Execution interrupted; outcome unknown."})
			missing = append(missing, raw)
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, agenticMessage{Role: "user", Content: append(missing, next...)})
		if hasUser {
			i++
		}
	}
	return out
}
