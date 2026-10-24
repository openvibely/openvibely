package anthropic

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/replayjson"
	client "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplayEncodingReflectsContentChanges(t *testing.T) {
	value := anthropicEnvironmentReplay{Environment: "env", Messages: []client.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"text":"a"}]`)}}}
	first, err := replayjson.Encode(value)
	require.NoError(t, err)
	again, err := replayjson.Encode(value)
	require.NoError(t, err)
	require.Equal(t, first, again)
	for i, c := range value.Messages[0].ContentBlocks {
		if c == 'a' {
			value.Messages[0].ContentBlocks[i] = 'b'
		}
	}
	changed, err := replayjson.Encode(value)
	require.NoError(t, err)
	require.NotEqual(t, first, changed)
	expected, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
	value.Environment = "different"
	changed, err = replayjson.Encode(value)
	require.NoError(t, err)
	expected, err = json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
}
