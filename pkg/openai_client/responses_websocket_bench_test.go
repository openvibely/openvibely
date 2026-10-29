package openaiclient

import (
	"encoding/json"
	"fmt"
	"testing"
)

var responsesLiteBenchmarkSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Path to operate on"},
    "options":{"type":"object","properties":{"recursive":{"type":"boolean"}},"required":["recursive"],"additionalProperties":false},
    "items":{"type":"array","items":{"type":"string"}}
  },
  "required":["path","options"],
  "additionalProperties":false
}`)

func responsesLiteBenchmarkTools(count int) []ToolDefinition {
	tools := make([]ToolDefinition, count)
	for i := range tools {
		strict := i%2 == 0
		toolType := "function"
		switch i % 10 {
		case 3:
			toolType = "web_search"
		case 6:
			toolType = "WEB_SEARCH_PREVIEW"
		case 9:
			toolType = "Image_Generation"
		case 5:
			toolType = "namespace"
		}
		tools[i] = ToolDefinition{
			Type:        toolType,
			Name:        fmt.Sprintf("tool_%02d", i),
			Description: "A representative tool definition used for Responses Lite benchmarks.",
			Parameters:  responsesLiteBenchmarkSchema,
			Async:       i%3 == 0,
			Strict:      &strict,
		}
	}
	return tools
}

func BenchmarkResponsesLiteToolConversion(b *testing.B) {
	for _, count := range []int{1, 40, 80} {
		tools := responsesLiteBenchmarkTools(count)
		for _, turns := range []int{1, 4, 8} {
			b.Run(fmt.Sprintf("tools=%d/turns=%d", count, turns), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for turn := 0; turn < turns; turn++ {
						_ = responsesLiteTools(tools)
					}
				}
			})
		}
	}
}

func BenchmarkResponsesLitePayloadConstruction(b *testing.B) {
	for _, count := range []int{1, 40, 80} {
		tools := responsesLiteBenchmarkTools(count)
		for _, turns := range []int{1, 4, 8} {
			b.Run(fmt.Sprintf("tools=%d/turns=%d", count, turns), func(b *testing.B) {
				payload := map[string]any{
					"model": "gpt-5.6-sol",
					"input": []any{map[string]any{
						"type":    "message",
						"role":    "user",
						"content": []any{map[string]any{"type": "input_text", "text": "Inspect the project."}},
					}},
					"tools":     tools,
					"reasoning": map[string]any{"effort": "medium"},
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for turn := 0; turn < turns; turn++ {
						_ = buildResponsesLiteWebsocketPayload(payload, "Follow the user request.", "benchmark-session")
					}
				}
			})
		}
	}
}
