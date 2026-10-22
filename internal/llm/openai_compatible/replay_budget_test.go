package openai_compatible

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReplayBudgetCountsOutgoingReasoningOnce(t *testing.T) {
	reasoning := strings.Repeat("reasoning ", 1000)
	messages := []openaiclient.CompletionsHistoryMessage{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", ReasoningContent: reasoning}}
	raw, err := json.Marshal(messages)
	require.NoError(t, err)
	history := []models.Execution{{PromptSent: "question", Output: "answer", ReasoningContent: reasoning, ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "question", AssistantContent: "answer", ReasoningContent: reasoning, TranscriptJSON: string(raw)}}}}
	wire, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	expected, err := tokenestimate.FromMessagesJSON(wire, "image_url", 2000)
	require.NoError(t, err)
	require.Equal(t, expected, EstimateHistoryTokens(history))
	require.Equal(t, EstimateHistoryTokens([]models.Execution{{ReplayMessages: history[0].ReplayMessages}}), EstimateHistoryTokens(history))
	messages[1].ReasoningContent = ""
	noReasoning, err := json.Marshal(messages)
	require.NoError(t, err)
	require.Greater(t, EstimateHistoryTokens(history), EstimateHistoryTokens([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(noReasoning)}}}}))
}
func TestReplayBudgetDoesNotCountImageBytesAsText(t *testing.T) {
	makeHistory := func(size int) []models.Execution {
		raw, _ := json.Marshal([]openaiclient.CompletionsHistoryMessage{{Role: "user", ImageContent: []map[string]interface{}{{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64," + strings.Repeat("A", size)}}}}})
		return []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
	}
	require.Equal(t, EstimateHistoryTokens(makeHistory(10)), EstimateHistoryTokens(makeHistory(1000000)))
}

func TestEmptyReplayHistoryHasNoBudgetCost(t *testing.T) {
	require.Zero(t, EstimateHistoryTokens(nil))
}
