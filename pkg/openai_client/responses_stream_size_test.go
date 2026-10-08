package openaiclient

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResponsesStreamParsersAcceptLargeEvents(t *testing.T) {
	largeText := strings.Repeat("x", 10*1024*1024+1)
	outputItemEvent := responsesTestSSEEvent(t, map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"type":    "message",
			"content": []any{map[string]any{"type": "output_text", "text": largeText}},
		},
	})
	smallCompletionEvent := responsesTestSSEEvent(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "resp_small", "status": "completed", "model": "test", "output": []any{}},
	})
	largeCompletionEvent := responsesTestSSEEvent(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_large", "status": "completed", "model": "test",
			"output": []any{map[string]any{
				"type":    "message",
				"content": []any{map[string]any{"type": "output_text", "text": largeText}},
			}},
		},
	})

	parsers := []struct {
		name  string
		parse func(string) (string, error)
	}{
		{
			name: "standard",
			parse: func(stream string) (string, error) {
				response, err := parseStreamingResponse(strings.NewReader(stream), nil, false)
				if err != nil {
					return "", err
				}
				return response.Text, nil
			},
		},
		{
			name: "agentic",
			parse: func(stream string) (string, error) {
				result, err := (&Client{}).parseAgenticStreamWithToolCallbacks(strings.NewReader(stream), nil, nil, nil, nil)
				if err != nil {
					return "", err
				}
				return result.text, nil
			},
		},
	}

	for _, parser := range parsers {
		t.Run(parser.name, func(t *testing.T) {
			t.Run("large output_item.done", func(t *testing.T) {
				text, err := parser.parse(outputItemEvent + smallCompletionEvent)
				if err != nil {
					t.Fatalf("parse large output item: %v", err)
				}
				if text != "" {
					t.Fatalf("text length = %d, want no output text", len(text))
				}
			})

			t.Run("large response.completed", func(t *testing.T) {
				text, err := parser.parse(largeCompletionEvent)
				if err != nil {
					t.Fatalf("parse large completion: %v", err)
				}
				if len(text) != len(largeText) {
					t.Fatalf("text length = %d, want %d", len(text), len(largeText))
				}
			})
		})
	}
}

func TestResponsesStreamParsersRejectEventAboveConfiguredLimit(t *testing.T) {
	payloadSize := responsesWebsocketReadLimit + 1
	event := `{"type":"response.completed","response":{}}`
	if len(event) >= payloadSize {
		t.Fatal("configured event limit is too small for test payload")
	}
	event += strings.Repeat(" ", payloadSize-len(event))
	if len(event) != payloadSize {
		t.Fatalf("event size = %d, want %d", len(event), payloadSize)
	}
	stream := "data: " + event + "\n\n"

	_, standardErr := parseStreamingResponse(strings.NewReader(stream), nil, false)
	if !errors.Is(standardErr, errResponsesStreamEventTooLarge) {
		t.Fatalf("standard parser error = %v, want configured-size error", standardErr)
	}
	_, agenticErr := (&Client{}).parseAgenticStreamWithToolCallbacks(strings.NewReader(stream), nil, nil, nil, nil)
	if !errors.Is(agenticErr, errResponsesStreamEventTooLarge) {
		t.Fatalf("agentic parser error = %v, want configured-size error", agenticErr)
	}
}

func responsesTestSSEEvent(t *testing.T, event map[string]any) string {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal Responses event: %v", err)
	}
	return "data: " + string(data) + "\n\n"
}
