package transcript_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestExchangeBoundaryAcrossProviderFormats(t *testing.T) {
	for _, format := range []string{"responses_delta", "messages", "array"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			task := &models.Task{ProjectID: "default", Title: "exchange", Category: models.CategoryCompleted, Status: models.StatusCompleted}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			current := models.Execution{TaskID: task.ID, Status: models.ExecRunning}
			require.NoError(t, stores.Executions.Create(ctx, &current))
			owner := models.ConversationOwner{ExecutionID: current.ID}
			header := transcript.ReplayHeader{Format: "envelope", ItemsKey: format}
			if format == "array" {
				header.Format = "array"
				header.ItemsKey = ""
			}
			prefix := []any{map[string]any{"role": "user", "content": "ORIGINAL"}, map[string]any{"role": "assistant", "content": "COMPLETED_TOOL"}}
			require.NoError(t, transcript.NewRecorder(stores.Executions, owner).Save(ctx, header, prefix))
			for invocation := 0; invocation < 3; invocation++ {
				recorder, err := transcript.OpenExchangeRecorder(ctx, stores.Executions, owner, false)
				require.NoError(t, err)
				items := []any{map[string]any{"role": "user", "content": "LATE_STEER"}}
				for turn := 0; turn < 2; turn++ {
					if turn == 1 {
						items = append(items, map[string]any{"role": "assistant", "content": "NEW_TOOL"})
					}
					require.NoError(t, recorder.SaveChanged(ctx, header, items, turn))
					// Read immediately, without handler finalization. A crash now preserves
					// every previous invocation, and only one frame for the running one.
					saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
					require.NoError(t, err)
					require.Len(t, saved[current.ID], invocation+2)
					require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "COMPLETED_TOOL")
					require.Contains(t, saved[current.ID][invocation+1].FullTranscriptJSON(), "LATE_STEER")
				}
			}
			replacement, err := transcript.OpenExchangeRecorder(ctx, stores.Executions, owner, true)
			require.NoError(t, err)
			require.NoError(t, replacement.Save(ctx, header, []any{map[string]any{"role": "user", "content": "SUMMARY"}}))
			saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
			require.NoError(t, err)
			require.Len(t, saved[current.ID], 1)
			require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "SUMMARY")
			require.NotContains(t, saved[current.ID][0].FullTranscriptJSON(), "ORIGINAL")
		})
	}
}

func TestExchangePreservesCapturedHistoryAcrossSteeringCommit(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "steering", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	current := models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(ctx, &current))
	owner := models.ConversationOwner{ExecutionID: current.ID}
	header := transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_delta"}
	old := map[string]any{"type": "message", "role": "user", "content": "OLD_CURRENT_WORK"}
	next := map[string]any{"type": "message", "role": "user", "content": "NEW_STEER"}
	require.NoError(t, transcript.NewRecorder(stores.Executions, owner).Save(ctx, header, []any{old}))
	recorder, err := transcript.OpenExchangeRecorder(ctx, stores.Executions, owner, false)
	require.NoError(t, err)
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: current.ID, ResponsesHistory: []any{old, next}}))
	require.NoError(t, recorder.Save(ctx, header, []any{next}))
	saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
	require.NoError(t, err)
	require.Len(t, saved[current.ID], 2)
	require.NotContains(t, saved[current.ID][0].FullTranscriptJSON(), "NEW_STEER")
	require.NotContains(t, saved[current.ID][1].FullTranscriptJSON(), "OLD_CURRENT_WORK")
}

// Failure to capture existing work must stop setup, not silently drop the prefix.
type failingExchangeStore struct {
	persistence.ConversationStore
	err error
}

func (s failingExchangeStore) ReadConversationHeads(context.Context, models.ConversationOwner, models.ConversationLane, int64) ([]models.ConversationChange, error) {
	return nil, s.err
}
func TestExchangeSetupFailsClosed(t *testing.T) {
	cause := errors.New("cannot read existing work")
	recorder, err := transcript.OpenExchangeRecorder(context.Background(), failingExchangeStore{err: cause}, models.ConversationOwner{ExecutionID: "current"}, false)
	require.ErrorIs(t, err, cause)
	require.Nil(t, recorder)
}
