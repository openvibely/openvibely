package openaiclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/stretchr/testify/require"
)

func TestBorrowedMessageTextAcrossConsumers(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			typ := "input_text"
			if role == "assistant" {
				typ = "output_text"
			}
			raw, err := json.Marshal(map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": typ, "text": strings.Repeat("hello <world>\n", 100)}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}})
			require.NoError(t, err)
			projected, err := jsonview.ToolOutput(raw)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))
			borrowed := projected.(map[string]any)
			require.Equal(t, estimateAgenticInputItemTokens(decoded), estimateAgenticInputItemTokens(borrowed))
			require.Equal(t, openAIInputItemContentText(decoded["content"]), openAIInputItemContentText(borrowed["content"]))
			require.Equal(t, visibleSearchMessageText(decoded["content"], role), visibleSearchMessageText(borrowed["content"], role))
			require.Equal(t, messageTextTokenCountForOpenAIRemoteCompactionV2(decoded), messageTextTokenCountForOpenAIRemoteCompactionV2(borrowed))
			expected, ok := truncateRetainedMessageForOpenAIRemoteCompactionV2(decoded, 10)
			actual, gotOK := truncateRetainedMessageForOpenAIRemoteCompactionV2(borrowed, 10)
			require.Equal(t, ok, gotOK)
			wantJSON, err := json.Marshal(expected)
			require.NoError(t, err)
			gotJSON, err := json.Marshal(actual)
			require.NoError(t, err)
			require.JSONEq(t, string(wantJSON), string(gotJSON))
			body, err := marshalResponsesWebsocketRequest(map[string]any{"type": "response.create", "input": []any{borrowed}}, "")
			require.NoError(t, err)
			var sent map[string]any
			require.NoError(t, json.Unmarshal(body, &sent))
			require.Equal(t, decoded, sent["input"].([]any)[0])
			// Truncation must not mutate the borrowed checkpoint.
			require.IsType(t, jsonview.Value{}, borrowed["content"].([]any)[0].(map[string]any)["text"])
		})
	}
}

func TestBorrowedImageBudgets(t *testing.T) {
	for _, detail := range []string{"auto", "low", "high", "original"} {
		for _, url := range []string{"data:image/png;base64,AAAA", "data:image/png;base64,abc\"def", " data:image/png;base64,AAAA ", "https://example.test/image.png", ""} {
			t.Run(detail+url, func(t *testing.T) {
				decoded := map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": url, "detail": detail}}}
				raw, err := json.Marshal(decoded)
				require.NoError(t, err)
				borrowed, err := jsonview.ToolOutput(raw)
				require.NoError(t, err)
				require.Equal(t, estimateAgenticInputItemTokens(decoded), estimateAgenticInputItemTokens(borrowed))
			})
		}
	}
}

func BenchmarkMessageCheckpointProjection(b *testing.B) {
	for _, kind := range []string{"text", "image"} {
		block := map[string]any{"type": "input_text", "text": strings.Repeat("checkpoint text\n", 700000)}
		if kind == "image" {
			block = map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("AAAA", 2621440), "detail": "auto"}
		}
		raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{block}})
		for _, mode := range []string{"decoded", "borrowed", "raw"} {
			b.Run(kind+"/"+mode, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var item any
					var err error
					if mode == "decoded" {
						err = json.Unmarshal(raw, &item)
					} else if mode == "raw" {
						item, err = jsonview.SavedResponse(raw)
					} else {
						item, err = jsonview.ToolOutput(raw)
					}
					if err != nil {
						b.Fatal(err)
					}
					_ = estimateAgenticInputItemTokens(item)
					if _, err = marshalResponsesWebsocketRequest(map[string]any{"type": "response.create", "input": []any{item}}, ""); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
