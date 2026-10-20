package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestUserMessageHistoryUpdatesMetadataWhenRemovingImages(t *testing.T) {
	for _, key := range []string{"internal_chat_message_metadata", "internal_chat_message_metadata_passthrough"} {
		for _, withImage := range []bool{false, true} {
			name := key + "/text"
			if withImage {
				name += "_and_image"
			}
			t.Run(name, func(t *testing.T) {
				content := []any{map[string]any{"type": "input_text", "text": "Describe this.", "annotations": []any{}}}
				kinds := []string{"user.text"}
				if withImage {
					content = append(content, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"})
					kinds = append(kinds, "user.image")
				}
				item := map[string]any{
					"type": "message", "role": "user", "id": "msg_original", "content": content,
					key: map[string]any{"turn_id": "turn_original", "content_item_kinds": kinds},
				}
				transcript, err := json.Marshal(map[string]any{"responses_input": []any{item}})
				require.NoError(t, err)
				history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(transcript)}}}}

				users := UserMessageHistory(history)
				require.Len(t, users, 1)
				require.Equal(t, "Describe this.", users[0].PromptSent)
				var retained map[string]any
				require.NoError(t, json.Unmarshal(users[0].RetainedUserMessage, &retained))
				require.Equal(t, "msg_original", retained["id"])
				metadata := retained[key].(map[string]any)
				require.Equal(t, "turn_original", metadata["turn_id"])
				require.Equal(t, []any{"user.text"}, metadata["content_item_kinds"])
				parts := retained["content"].([]any)
				require.Len(t, parts, 1)
				require.Equal(t, "input_text", parts[0].(map[string]any)["type"])
				if !withImage {
					require.Contains(t, parts[0], "annotations", "preserve unchanged text parts")
				}
				require.Equal(t, string(transcript), history[0].ReplayMessages[0].TranscriptJSON)
			})
		}
	}
}

func TestReplayBudgetCountsOnlyEffectiveHistory(t *testing.T) {
	checkpoint := models.Execution{PromptSent: strings.Repeat("duplicate", 1000), Output: "duplicate output", ReplayMessages: []models.ExecutionReplayMessage{
		{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"replaced checkpoint"}]}`},
		{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"current history"}]}`},
		{UserContent: "late steer", AssistantContent: "late answer"},
	}}
	history := []models.Execution{{PromptSent: strings.Repeat("old", 2000)}, checkpoint, {PromptSent: "follow up"}}
	canonical := EffectiveReplayHistory(history)
	require.Len(t, canonical, 2)
	require.Len(t, canonical[0].ReplayMessages, 2)
	require.Equal(t, EstimateHistoryTokens(canonical), EstimateHistoryTokens(history))
	require.Less(t, EstimateHistoryTokens(history), 100)
	require.Len(t, history[1].ReplayMessages, 3, "do not mutate the caller's history")
}

func TestReplayBudgetDoesNotCountImageBase64AsText(t *testing.T) {
	makeHistory := func(size int) []models.Execution {
		return []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":[{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,` + strings.Repeat("A", size) + `"}]}]}`}}}}
	}
	small, large := EstimateHistoryTokens(makeHistory(100)), EstimateHistoryTokens(makeHistory(1000000))
	require.Equal(t, small, large)
	require.InDelta(t, 1844, large, 100)
}

func TestContinuationBudgetReplacesNativeOnlyForStructuredReplay(t *testing.T) {
	native := `[{"type":"message","role":"user","content":"saved native context"}]`
	plain := []models.Execution{{PromptSent: "new question", Output: "new answer"}}
	historyTokens, nativeTokens := EstimateContinuationTokens(plain, native)
	require.Equal(t, EstimateHistoryTokens(plain), historyTokens)
	require.Positive(t, nativeTokens)
	for _, replay := range []string{
		`{"responses_input":[{"type":"message","role":"user","content":"full replacement"}]}`,
		`[{"role":"user","content":"converted message"}]`,
	} {
		history := append(append([]models.Execution(nil), plain...), models.Execution{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: replay}}}, models.Execution{PromptSent: "after replacement"})
		h, n := EstimateContinuationTokens(history, native)
		require.Zero(t, n, "match the client's replacement behavior for structured history")
		require.Equal(t, EstimateHistoryTokens(history), h)
	}
	_, invalid := EstimateContinuationTokens(plain, `invalid JSON`)
	require.Zero(t, invalid, "the adapter ignores invalid native JSON")
}
