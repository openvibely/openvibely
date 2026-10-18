package service

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// Keep image-bearing user messages out of the textual summary and replay them
// as actual image inputs after compaction.
func retainedCompletionsImages(history []models.Execution) []models.Execution {
	var out []models.Execution
	seen := map[string]bool{}
	for _, execution := range history {
		for _, replay := range execution.ReplayMessages {
			var messages []openaiclient.CompletionsHistoryMessage
			if json.Unmarshal([]byte(replay.TranscriptJSON), &messages) != nil {
				continue
			}
			for _, message := range messages {
				if message.Role != "user" {
					continue
				}
				hasImage := false
				for _, block := range message.ImageContent {
					if block["type"] == "image_url" {
						hasImage = true
					}
				}
				if !hasImage {
					continue
				}
				raw, err := json.Marshal([]openaiclient.CompletionsHistoryMessage{message})
				if err != nil || seen[string(raw)] {
					continue
				}
				seen[string(raw)] = true
				out = append(out, models.Execution{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}})
			}
		}
	}
	return out
}

func completionsSummaryText(raw string) string {
	var messages []openaiclient.CompletionsHistoryMessage
	if json.Unmarshal([]byte(raw), &messages) != nil {
		return raw
	}
	changed := false
	for i := range messages {
		for j, block := range messages[i].ImageContent {
			if block["type"] == "image_url" {
				messages[i].ImageContent[j] = map[string]interface{}{"type": "text", "text": "[Image retained separately for visual inspection]"}
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
