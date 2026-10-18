package service

import (
	"encoding/json"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func completionsSummaryText(raw string) string {
	var messages []openaiclient.CompletionsHistoryMessage
	if json.Unmarshal([]byte(raw), &messages) != nil {
		return raw
	}
	changed := false
	for i := range messages {
		for j, block := range messages[i].ImageContent {
			if block["type"] == "image_url" {
				messages[i].ImageContent[j] = map[string]interface{}{"type": "text", "text": "[Image omitted from text summary input]"}
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return raw
	}
	return string(encoded)
}
