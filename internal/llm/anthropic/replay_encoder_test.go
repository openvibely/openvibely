package anthropic

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
	client "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplayEncodingReflectsContentChanges(t *testing.T) {
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "Replay mutation", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(context.Background(), task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(context.Background(), execution))
	recorder := transcript.NewRecorder(stores.Executions, models.ConversationOwner{ExecutionID: execution.ID})
	save := func(history anthropicEnvironmentReplay) (string, error) {
		env, _ := json.Marshal(history.Environment)
		if err := recorder.Save(context.Background(), transcript.ReplayHeader{Format: "envelope", ItemsKey: "messages", Envelope: map[string]json.RawMessage{"anthropic_execution_environment": env}}, transcript.Values(history.Messages)); err != nil {
			return "", err
		}
		saved, err := stores.Executions.ReplayMessagesByExecutionIDs(context.Background(), []string{execution.ID})
		if err != nil {
			return "", err
		}
		return saved[execution.ID][0].TranscriptJSON, nil
	}

	value := anthropicEnvironmentReplay{Environment: "env", Messages: []client.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"text":"a"}]`)}}}
	first, err := save(value)
	require.NoError(t, err)
	again, err := save(value)
	require.NoError(t, err)
	require.Equal(t, first, again)
	for i, c := range value.Messages[0].ContentBlocks {
		if c == 'a' {
			value.Messages[0].ContentBlocks[i] = 'b'
		}
	}
	changed, err := save(value)
	require.NoError(t, err)
	require.NotEqual(t, first, changed)
	expected, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
	value.Environment = "different"
	changed, err = save(value)
	require.NoError(t, err)
	expected, err = json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
}
