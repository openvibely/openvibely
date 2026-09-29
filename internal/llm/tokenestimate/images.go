package tokenestimate

import "encoding/json"

// FromMessagesJSON estimates a compact JSON request, replacing image content
// blocks with a fixed token cost. Only message content and nested tool-result
// content are inspected; tools, text, and arbitrary tool arguments remain text.
// The serialized request is never modified.
func FromMessagesJSON(encoded []byte, imageType string, imageTokens int) (int, error) {
	var request struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		return 0, err
	}
	bytes := len(encoded)
	var countContent func(json.RawMessage)
	countContent = func(content json.RawMessage) {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			return // Plain text content is already included in the byte count.
		}
		for _, block := range blocks {
			var fields struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(block, &fields) != nil {
				continue
			}
			if fields.Type == imageType {
				bytes += ByteBudget(imageTokens) - len(block)
			} else if fields.Type == "tool_result" {
				countContent(fields.Content)
			}
		}
	}
	for _, message := range request.Messages {
		countContent(message.Content)
	}
	return FromByteCount(bytes), nil
}
