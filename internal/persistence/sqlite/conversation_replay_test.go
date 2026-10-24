package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type countedReplay struct {
	calls int
	text  string
	err   error
}

func (h *countedReplay) MarshalJSON() ([]byte, error) {
	h.calls++
	if h.err != nil {
		return nil, h.err
	}
	return json.Marshal(h.text)
}

func TestSaveConversationReplayEncodesOnceAndPreservesMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	tasks := NewTaskRepo(db, nil)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "Replay", Category: models.CategoryActive, Status: models.StatusPending}
	require.NoError(t, tasks.Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, execution))
	h := &countedReplay{text: "<>& café\n"}
	meta := models.ExecutionReplayMessage{UserContent: "user", AssistantContent: "answer", ReasoningContent: "reason", TranscriptJSON: "must be replaced"}
	raw, err := repo.SaveConversationReplay(ctx, execution.ID, h, meta)
	require.NoError(t, err)
	require.Equal(t, 1, h.calls)
	expected, err := json.Marshal(h.text)
	require.NoError(t, err)
	require.Equal(t, string(expected), raw)
	rows, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Len(t, rows[execution.ID], 1)
	row := rows[execution.ID][0]
	require.Equal(t, raw, row.TranscriptJSON)
	require.Equal(t, meta.UserContent, row.UserContent)
	require.Equal(t, meta.AssistantContent, row.AssistantContent)
	require.Equal(t, meta.ReasoningContent, row.ReasoningContent)
	require.Equal(t, "must be replaced", meta.TranscriptJSON, "caller metadata is not mutated")
	saved, err := repo.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, "reason", saved.ReasoningContent)

	// Encoding and DB failures must not return a usable recovery snapshot or
	// overwrite the preceding durable checkpoint.
	failure := errors.New("encoding failed")
	h.err = failure
	raw, err = repo.SaveConversationReplay(ctx, execution.ID, h, meta)
	require.ErrorIs(t, err, failure)
	var encoding *persistence.ReplayEncodingError
	require.ErrorAs(t, err, &encoding)
	require.Empty(t, raw)
	h.err = nil
	h.text = "changed"
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	raw, err = repo.SaveConversationReplay(canceled, execution.ID, h, meta)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, raw)
	rows, err = repo.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Equal(t, string(expected), rows[execution.ID][0].TranscriptJSON)
	// The provider can explicitly detach cancellation, and retries still write.
	meta.AssistantContent = "final answer"
	raw, err = repo.SaveConversationReplay(context.WithoutCancel(canceled), execution.ID, h, meta)
	require.NoError(t, err)
	require.Equal(t, `"changed"`, raw)
	require.Equal(t, 4, h.calls)
	rows, err = repo.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Equal(t, raw, rows[execution.ID][0].TranscriptJSON)
	require.Equal(t, "final answer", rows[execution.ID][0].AssistantContent)
}
