package preparedhistory

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplayStateOwnershipReplacementAndClear(t *testing.T) {
	ctx := WithScope(context.Background())
	replay := models.ExecutionReplayMessage{Rows: &models.ReplayRows{Format: "array", Items: []json.RawMessage{json.RawMessage(`"a"`)}}}
	calls := 0
	decode := func(r models.ExecutionReplayMessage) ([]string, error) {
		calls++
		var v []string
		err := r.DecodeTranscript(&v)
		return v, err
	}
	first, err := ReadReplayState(ctx, replay, decode)
	require.NoError(t, err)
	taken, err := TakeReplayState(ctx, replay, decode)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, &first[0], &taken[0])
	taken[0] = "changed"
	restored, err := ReadReplayState(ctx, replay, decode)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, restored)
	replaced := replay
	replaced.TranscriptJSON = `["replacement"]`
	got, err := ReadReplayState(ctx, replaced, decode)
	require.NoError(t, err)
	require.Equal(t, []string{"replacement"}, got)
	// Clearing state must evict the old value, even when decoding empty JSON fails.
	_, err = TakeReplayState(ctx, models.ExecutionReplayMessage{}, decode)
	require.Error(t, err)
	got, err = ReadReplayState(ctx, replay, decode)
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, got)
	require.Equal(t, 5, calls)
	other := models.ExecutionReplayMessage{Rows: &models.ReplayRows{Format: "array", Items: []json.RawMessage{json.RawMessage(`"new"`)}}}
	got, err = TakeReplayState(ctx, other, decode)
	require.NoError(t, err)
	require.Equal(t, []string{"new"}, got)
}
