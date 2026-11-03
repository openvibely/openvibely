package openai

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	client "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLateSteeringKeepsDurableCurrentExchange(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "audit", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(ctx, current))
	prior := `{"responses_delta":[{"type":"message","role":"user","content":"CURRENT_ORIGINAL"},{"type":"message","role":"assistant","content":"TOOL_WORK_ALREADY_COMPLETED"}]}`
	require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, current.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: prior}}))
	a := New(nil, stores.Executions, nil, nil)
	save, err := a.responsesExchangeRecorder(ctx, current.ID)
	require.NoError(t, err)
	require.NoError(t, save(ctx, client.ResponsesReplayUpdate{Items: []any{map[string]any{"type": "message", "role": "user", "content": "LATE_STEER"}}}))
	// A crash here must not erase the already saved work from the active execution.
	saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
	require.NoError(t, err)
	require.Len(t, saved[current.ID], 2)
	require.Contains(t, saved[current.ID][1].FullTranscriptJSON(), "LATE_STEER")
	require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "CURRENT_ORIGINAL")
	require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "TOOL_WORK_ALREADY_COMPLETED")
}
