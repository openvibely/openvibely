package anthropicclient

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeRowsDecodeBudgetAndTake(t *testing.T) {
	replay := models.ExecutionReplayMessage{Rows: &models.ReplayRows{Format: "envelope", ItemsKey: "messages", Envelope: map[string]json.RawMessage{"version": json.RawMessage(`1`)}, Items: []json.RawMessage{json.RawMessage(`{"role":"assistant","content":[{"type":"compaction","content":"summary"},{"type":"text","text":"answer"}]}`)}}}
	ctx := preparedhistory.WithScope(context.Background())
	want, err := decodeNativeCompactionState(replay.FullTranscriptJSON())
	require.NoError(t, err)
	require.Equal(t, EstimateNativeCompactionTokens(replay.FullTranscriptJSON()), EstimateNativeCompactionReplayTokens(ctx, replay))
	got, err := preparedhistory.TakeReplayState(ctx, replay, decodeNativeCompactionReplay)
	require.NoError(t, err)
	require.Equal(t, want, got)
	got[0].Role = "user"
	again, err := preparedhistory.TakeReplayState(ctx, replay, decodeNativeCompactionReplay)
	require.NoError(t, err)
	require.Equal(t, "assistant", again[0].Role)
	require.Equal(t, `{"role":"assistant","content":[{"type":"compaction","content":"summary"},{"type":"text","text":"answer"}]}`, string(replay.Rows.Items[0]))
}
