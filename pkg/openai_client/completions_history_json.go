package openaiclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"strings"
)

// Persist the API's content union, while accepting older image_content records.
func (m CompletionsHistoryMessage) MarshalJSON() ([]byte, error) {
	type plain CompletionsHistoryMessage
	var content any = m.contentValue()
	if len(m.ImageContent) > 0 {
		content = m.ImageContent
	}
	return json.Marshal(struct {
		plain
		Content any `json:"content"`
	}{plain: plain(m), Content: content})
}
func (m *CompletionsHistoryMessage) UnmarshalJSON(data []byte) error {
	var next CompletionsHistoryMessage
	var content json.RawMessage
	if string(data) == "null" {
		*m = next
		return nil
	}
	err := requestjson.Object(data, func(key string, raw json.RawMessage) error {
		switch strings.ToLower(key) {
		case "role":
			return json.Unmarshal(raw, &next.Role)
		case "content":
			content = raw
		case "reasoning_content":
			return json.Unmarshal(raw, &next.ReasoningContent)
		case "tool_calls":
			return json.Unmarshal(raw, &next.ToolCalls)
		case "tool_call_id":
			return json.Unmarshal(raw, &next.ToolCallID)
		case "image_content":
			return json.Unmarshal(raw, &next.ImageContent)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(content) > 0 && string(content) != "null" {
		if content[0] == '"' {
			err = json.Unmarshal(content, &next.Content)
		} else {
			err = json.Unmarshal(content, &next.ImageContent)
		}
	}
	if err == nil {
		*m = next
	}
	return err
}

// contentValue is used only by immutable prepared histories. Public history
// decoding still populates Content normally.
func (m CompletionsHistoryMessage) contentValue() any {
	if m.rawContent != nil && m.Content == "" {
		return *m.rawContent
	}
	return m.Content
}
func (m CompletionsHistoryMessage) contentText() string {
	if view, ok := m.contentValue().(jsonview.Value); ok {
		var text string
		_ = view.Decode(&text)
		return text
	}
	return m.Content
}
