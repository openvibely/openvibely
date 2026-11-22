package gemini

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCheckpointHistoryReusesImageAndIsolatesMutations(t *testing.T) {
	raw := `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + strings.Repeat("a", 2<<20) + `"}}]}]}`
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: raw}}}}
	owner := preparedhistory.NewReusableState()
	owner.BindHistory(history)
	first := buildPreparedHistory(owner.Context(context.Background()), history)
	require.Len(t, first[0], 1)
	first[0][0].Parts[0].InlineData.Data = "changed"
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 3 {
		next := buildPreparedHistory(owner.Context(context.Background()), history)
		require.Len(t, next[0][0].Parts[0].InlineData.Data, 2<<20)
	}
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<10), "checkpoint image must not be decoded again")
}

func TestCheckpointHistoryKeepsCurrentExchangesSeparate(t *testing.T) {
	history := []models.Execution{{PromptSent: "saved user", Output: "saved answer", Status: models.ExecCompleted}}
	owner := preparedhistory.NewReusableState()
	owner.BindHistory(history)
	for _, latest := range []string{"first continuation", "different continuation"} {
		outgoing := append(append([]models.Execution(nil), history...), models.Execution{PromptSent: latest})
		got := buildPreparedHistory(owner.Context(context.Background()), outgoing)[0]
		require.Len(t, got, 3)
		require.Equal(t, latest, got[2].Parts[0].Text)
	}
	replaced := buildPreparedHistory(owner.Context(context.Background()), []models.Execution{{PromptSent: "replacement"}})[0]
	require.Len(t, replaced, 1)
	require.Equal(t, "replacement", replaced[0].Parts[0].Text)
	restored := buildPreparedHistory(owner.Context(context.Background()), history)[0]
	require.Len(t, restored, 2)
	require.Equal(t, "saved user", restored[0].Parts[0].Text)
}

func TestCheckpointHistoryBudgetAndSendSharePreparedMessages(t *testing.T) {
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"tool","response":{"output":"original"}}}]}]}`}}}}
	owner := preparedhistory.NewReusableState()
	owner.BindHistory(history)
	ctx := owner.Context(context.Background())
	require.Positive(t, EstimateHistoryTokensContext(ctx, history))
	a := New(nil, nil, nil)
	got := a.prepareHistory(ctx, history)
	got[0].Parts[0].FunctionResponse.Response["output"] = "changed"
	next := a.prepareHistory(owner.Context(context.Background()), history)
	encoded, err := json.Marshal(next)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "original")
	require.NotContains(t, string(encoded), "changed")
}
