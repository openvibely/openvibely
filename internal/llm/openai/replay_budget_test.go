package openai

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

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
