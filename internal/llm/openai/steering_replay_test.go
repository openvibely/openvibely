package openai

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBuildClientHistoryRestoresConsumedSteering(t *testing.T) {
	history := buildClientHistory([]models.Execution{
		{PromptSent: "old", Output: "old answer", Status: models.ExecCompleted},
		{PromptSent: "original", Output: "already recorded", Status: models.ExecFailed, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"old"},{"type":"message","role":"user","content":"steer"}]}`}}},
	})
	require.Len(t, history, 1, "checkpoint replaces its history prefix without duplicating output")
	require.Len(t, history[0].ResponsesInputItems, 2)
	require.Equal(t, "steer", history[0].ResponsesInputItems[1].(map[string]any)["content"])
}

func TestBuildClientHistoryPreservesContinuationAfterSteeringCheckpoint(t *testing.T) {
	history := buildClientHistory([]models.Execution{{
		PromptSent: "original", Output: "combined output",
		ReplayMessages: []models.ExecutionReplayMessage{
			{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"first steer"}]}`},
			{UserContent: "late steer", AssistantContent: "late answer"},
		},
	}})
	require.Len(t, history, 3)
	require.Len(t, history[0].ResponsesInputItems, 1)
	require.Equal(t, "late steer", history[1].Content)
	require.Equal(t, "user", history[1].Role)
	require.Equal(t, "late answer", history[2].Content)
}
