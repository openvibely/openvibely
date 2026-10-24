package openai_compatible

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	client "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

type replayRetryStore struct {
	persistence.ExecutionStore
	calls int
	raw   string
}

func (s *replayRetryStore) ReplaceReasoningReplay(_ context.Context, _ string, _ string, m []models.ExecutionReplayMessage) error {
	s.calls++
	if s.calls == 1 {
		return errors.New("transient write failure")
	}
	s.raw = m[0].TranscriptJSON
	return nil
}
func TestReplayEncoderMutationAndRetry(t *testing.T) {
	e := newReplayEncoder()
	messages := []client.CompletionsHistoryMessage{{Role: "assistant", Content: "text", ToolCalls: []client.CompletionsToolCall{{ID: "original"}}}}
	first, err := e.Encode(messages)
	require.NoError(t, err)
	messages[0].ToolCalls[0].ID = "changed"
	second, err := e.Encode(messages)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	messages[0].ImageContent = []map[string]interface{}{{"image_url": map[string]interface{}{"url": "before"}}}
	_, err = e.Encode(messages)
	require.NoError(t, err)
	messages[0].ImageContent[0]["image_url"].(map[string]interface{})["url"] = "after"
	result, err := e.Encode(messages)
	require.NoError(t, err)
	expected, err := json.Marshal(messages)
	require.NoError(t, err)
	require.Equal(t, string(expected), result)
	messages[0].ImageContent = nil
	result, err = e.Encode(messages)
	require.NoError(t, err)
	store := &replayRetryStore{}
	a := New(store, nil)
	save := a.replaySaver("exec", e)
	require.Error(t, save(context.Background(), messages))
	require.NoError(t, save(context.Background(), messages))
	require.Equal(t, 2, store.calls)
	require.Equal(t, result, store.raw)
}
