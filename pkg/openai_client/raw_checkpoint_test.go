package openaiclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/stretchr/testify/require"
)

func TestRawCheckpointMessageConsumers(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			textType := "input_text"
			if role == "assistant" {
				textType = "output_text"
			}
			original := map[string]any{"type": "message", "role": role, "id": "saved", "content": []any{map[string]any{"type": textType, "text": strings.Repeat("keep me ", 10000)}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("AAAA", 20000), "detail": "auto"}}}
			raw, err := json.Marshal(original)
			require.NoError(t, err)
			saved, err := jsonview.SavedResponse(raw)
			require.NoError(t, err)
			require.IsType(t, &jsonview.ResponseItem{}, saved)
			header, ok := responseItemHeader(saved)
			require.True(t, ok)
			require.Equal(t, role, header["role"])
			require.NotContains(t, header, "content")
			require.Equal(t, EstimateResponsesInputTokens([]any{original}), EstimateResponsesInputTokens([]any{saved}))
			require.Equal(t, openAIInputItemsTranscript([]any{original}), openAIInputItemsTranscript([]any{saved}))
			decoded, ok := responseItemObject(saved)
			require.True(t, ok)
			require.Equal(t, visibleSearchMessageText(original["content"], role), visibleSearchMessageText(decoded["content"], role))
			expected := buildAgenticRemoteCompactionV2History([]any{original}, map[string]any{"type": "compaction", "encrypted_content": "c"})
			actual := buildAgenticRemoteCompactionV2History([]any{saved}, map[string]any{"type": "compaction", "encrypted_content": "c"})
			a, err := json.Marshal(expected)
			require.NoError(t, err)
			b, err := json.Marshal(actual)
			require.NoError(t, err)
			require.JSONEq(t, string(a), string(b))
			body, err := marshalResponsesWebsocketRequest(map[string]any{"type": "response.create", "input": []any{saved, map[string]any{"type": "message", "role": "user", "content": "next"}}}, "")
			require.NoError(t, err)
			var sent struct {
				Input []json.RawMessage `json:"input"`
			}
			require.NoError(t, json.Unmarshal(body, &sent))
			require.Len(t, sent.Input, 2)
			require.JSONEq(t, string(raw), string(sent.Input[0]))
			decoded["content"] = "changed"
			unchanged, err := json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(unchanged))
		})
	}
}

func TestBorrowedResizedImageBudgetFallbacks(t *testing.T) {
	for _, url := range []string{
		"data:image/png;base64," + strings.Repeat("AAAA", 20000),
		"data:image/png;base64,AAA=", "data:image/png;base64,AA==", "data:image/png;base64,A===",
		"DATA:IMAGE/png;BASE64,AAAA", "data:image/png;charset=x;base64,AAAA",
		"data:image/png;base64,AA\\AA", "data:image/png;base64,AAAA ", "https://example.test/image",
	} {
		for _, detail := range []string{"auto", "original"} {
			raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": url, "detail": detail}}})
			var decoded any
			require.NoError(t, json.Unmarshal(raw, &decoded))
			saved, err := jsonview.SavedResponse(raw)
			require.NoError(t, err)
			require.Equal(t, EstimateResponsesInputTokens([]any{decoded}), EstimateResponsesInputTokens([]any{saved}), "detail=%s url length=%d", detail, len(url))
		}
	}
}

func TestRawCheckpointLiteImageNormalization(t *testing.T) {
	for _, model := range []string{"gpt-6-luna", "gpt-5.6-luna"} {
		for _, detail := range []string{"auto", "original"} {
			raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("AAAA", 20000), "detail": detail}}})
			saved, err := jsonview.SavedResponse(raw)
			require.NoError(t, err)
			var ordinary any
			require.NoError(t, json.Unmarshal(raw, &ordinary))
			original := []any{saved}
			actual := buildResponsesLiteWebsocketPayload(map[string]any{"model": model, "input": original}, "", "")
			expected := buildResponsesLiteWebsocketPayload(map[string]any{"model": model, "input": []any{ordinary}}, "", "")
			a, err := json.Marshal(actual)
			require.NoError(t, err)
			b, err := json.Marshal(expected)
			require.NoError(t, err)
			require.JSONEq(t, string(b), string(a))
			require.Same(t, saved, original[0], "format normalization must not replace the checkpoint entry")
			unchanged, err := json.Marshal(saved)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(unchanged))
		}
	}
}
