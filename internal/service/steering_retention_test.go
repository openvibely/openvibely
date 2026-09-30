package service

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCompactionRetainsNativeUserInstructionsSeparately(t *testing.T) {
	history := []models.Execution{
		{ID: "old", PromptSent: "obsolete prefix"},
		{ID: "current", PromptSent: "duplicate prompt", ReplayMessages: []models.ExecutionReplayMessage{
			{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"obsolete snapshot"}]}`},
			{TranscriptJSON: `{"responses_input":[
				{"type":"message","role":"user","content":"original request"},
				{"type":"message","role":"assistant","content":"assistant text"},
				{"type":"function_call_output","call_id":"tool","output":"tool output"},
				{"type":"message","role":"developer","content":"developer text"},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"do not deploy"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_text","text":"only run tests"}]}
			]}`},
			{UserContent: "late continuation", AssistantContent: "another answer"},
		}},
		{PromptSent: "latest followup", Status: models.ExecCompleted},
	}
	compacted := buildCompactedReplacementHistory(history, "work completed")
	require.Len(t, compacted, 5)
	for i, expected := range []string{"original request", "do not deploy\nonly run tests", "late continuation", "latest followup"} {
		require.Equal(t, expected, compacted[i].PromptSent)
		require.Empty(t, compacted[i].ID)
		require.Empty(t, compacted[i].ReplayMessages)
		require.Empty(t, compacted[i].Output)
	}
	require.Contains(t, compacted[4].Output, "work completed")
	require.Len(t, history[1].ReplayMessages, 3, "do not mutate original replay")
}

func TestNativeUserRetentionAppliesNewestFirstBudget(t *testing.T) {
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"` + strings.Repeat("old ", 1000) + `"},{"type":"message","role":"user","content":"keep this instruction"}]}`}}}}
	budget := 20
	retained := retainedUserMessageHistory(history, budget)
	require.NotEmpty(t, retained)
	require.Equal(t, "keep this instruction", retained[len(retained)-1].PromptSent)
	total := 0
	for _, item := range retained {
		total += estimatedUTF8Tokens(item.PromptSent)
	}
	require.LessOrEqual(t, total, budget)
	require.Empty(t, retainedUserMessageHistory(history, 0))
}

func TestNonNativeRetentionRemainsUnchanged(t *testing.T) {
	history := []models.Execution{{ID: "old", PromptSent: "original", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"messages":[{"role":"user","content":"provider-specific replay"}]}`}}}}
	retained := retainedUserMessageHistory(history, 100)
	require.Equal(t, []models.Execution{{PromptSent: "original", Status: models.ExecCompleted}}, retained)
}
