package openai

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResponsesRecorderAfterSteeringSavesOnlyCurrentContext(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "recorder", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect", Output: "ORIGINAL_TOOL_FACT"}
	require.NoError(t, stores.Executions.Create(ctx, exec))
	require.NoError(t, stores.Executions.AppendOutput(ctx, exec.ID, exec.Output))
	// Existing archives are ignored and removed on the next normal transcript write.
	require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, exec.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[],"responses_compaction_sources":{"old":[]}}`}}))
	compacted := []any{map[string]any{"type": "compaction", "encrypted_content": "opaque"}}
	a := New(nil, stores.Executions, nil, nil)
	require.NoError(t, a.responsesHistoryRecorder(exec.ID)(ctx, compacted))
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: exec.ID, ResponsesHistory: compacted}))
	require.NoError(t, a.responsesHistoryRecorder(exec.ID)(ctx, compacted))
	rows, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Contains(t, rows[exec.ID][0].TranscriptJSON, "opaque")
	require.NotContains(t, rows[exec.ID][0].TranscriptJSON, "responses_compaction_sources")
	saved, err := stores.Executions.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, "ORIGINAL_TOOL_FACT", saved.Output)
}
