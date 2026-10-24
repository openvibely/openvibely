package openai_compatible

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	client "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

type replayRetryStore struct {
	persistence.ExecutionStore
	calls int
	raw   string
}

func (s *replayRetryStore) SaveConversationReplay(ctx context.Context, id string, history any, metadata models.ExecutionReplayMessage) (string, error) {
	raw, err := json.Marshal(history)
	if err != nil {
		return "", &persistence.ReplayEncodingError{Err: err}
	}
	encoded := string(raw)
	metadata.TranscriptJSON = encoded
	if err := s.ReplaceReasoningReplay(ctx, id, metadata.ReasoningContent, []models.ExecutionReplayMessage{metadata}); err != nil {
		return "", err
	}
	return encoded, nil
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
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "Replay mutation", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(context.Background(), task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(context.Background(), execution))
	save := func(history any) (string, error) {
		return stores.Executions.SaveConversationReplay(context.Background(), execution.ID, history, models.ExecutionReplayMessage{})
	}

	messages := []client.CompletionsHistoryMessage{{Role: "assistant", Content: "text", ToolCalls: []client.CompletionsToolCall{{ID: "original"}}}}
	first, err := save(messages)
	require.NoError(t, err)
	messages[0].ToolCalls[0].ID = "changed"
	second, err := save(messages)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	messages[0].ImageContent = []map[string]interface{}{{"image_url": map[string]interface{}{"url": "before"}}}
	_, err = save(messages)
	require.NoError(t, err)
	messages[0].ImageContent[0]["image_url"].(map[string]interface{})["url"] = "after"
	result, err := save(messages)
	require.NoError(t, err)
	expected, err := json.Marshal(messages)
	require.NoError(t, err)
	require.Equal(t, string(expected), result)
	messages[0].ImageContent = nil
	result, err = save(messages)
	require.NoError(t, err)
	store := &replayRetryStore{}
	a := New(store, nil)
	retry := a.replaySaver("exec")
	require.Error(t, retry(context.Background(), messages))
	require.NoError(t, retry(context.Background(), messages))
	require.Equal(t, 2, store.calls)
	require.Equal(t, result, store.raw)
}
