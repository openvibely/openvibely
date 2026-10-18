package openaiclient

import "encoding/json"

// Persist the API's content union, while accepting older image_content records.
func (m CompletionsHistoryMessage) MarshalJSON() ([]byte, error) {
	type plain CompletionsHistoryMessage
	var content any = m.Content
	if len(m.ImageContent) > 0 {
		content = m.ImageContent
	}
	return json.Marshal(struct {
		plain
		Content any `json:"content"`
	}{plain: plain(m), Content: content})
}
func (m *CompletionsHistoryMessage) UnmarshalJSON(data []byte) error {
	type plain CompletionsHistoryMessage
	var raw struct {
		plain
		Content json.RawMessage          `json:"content"`
		Legacy  []map[string]interface{} `json:"image_content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = CompletionsHistoryMessage(raw.plain)
	m.ImageContent = raw.Legacy
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	if raw.Content[0] == '"' {
		return json.Unmarshal(raw.Content, &m.Content)
	}
	return json.Unmarshal(raw.Content, &m.ImageContent)
}
