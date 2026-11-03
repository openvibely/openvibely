package openai_compatible

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
	client "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

type replayRetryStore struct {
	persistence.ExecutionStore
	calls int
	raw   string
}

func (s *replayRetryStore) AppendConversationChanges(ctx context.Context, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errors.New("transient write failure")
	}
	return s.ExecutionStore.AppendConversationChanges(ctx, owner, changes)
}
func TestReplayEncoderMutationAndRetry(t *testing.T) {
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "Replay mutation", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(context.Background(), task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(context.Background(), execution))
	recorder := transcript.NewRecorder(stores.Executions, models.ConversationOwner{ExecutionID: execution.ID})
	save := func(history []client.CompletionsHistoryMessage) (string, error) {
		if err := recorder.Save(context.Background(), transcript.ReplayHeader{Format: "array"}, transcript.Values(history)); err != nil {
			return "", err
		}
		saved, err := stores.Executions.ReplayMessagesByExecutionIDs(context.Background(), []string{execution.ID})
		if err != nil {
			return "", err
		}
		return saved[execution.ID][0].TranscriptJSON, nil
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
	store := &replayRetryStore{ExecutionStore: stores.Executions}
	a := New(store, nil)
	retry := a.replayChangesSaver(context.Background(), execution.ID)
	messages[0].Content = "force write"
	require.Error(t, retry(context.Background(), messages, 0))
	require.NoError(t, retry(context.Background(), messages, 0))
	require.Equal(t, 2, store.calls)
	saved, err := stores.Executions.ReplayMessagesByExecutionIDs(context.Background(), []string{execution.ID})
	require.NoError(t, err)
	expected, err = json.Marshal(messages)
	require.NoError(t, err)
	require.Len(t, saved[execution.ID], 2)
	require.Equal(t, result, saved[execution.ID][0].TranscriptJSON)
	require.Equal(t, string(expected), saved[execution.ID][1].TranscriptJSON)
}
