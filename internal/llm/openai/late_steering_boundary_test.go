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
	// Same synthetic history emitted by prepareClaimedSteeringInputs. It belongs
	// to the active execution, not to another independently durable execution.
	history := []models.Execution{{ID: current.ID + "-steering-context-1", TaskID: task.ID, Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: prior}}}}
	prefix := []any{map[string]any{"type": "message", "role": "user", "content": "CURRENT_ORIGINAL"}, map[string]any{"type": "message", "role": "assistant", "content": "TOOL_WORK_ALREADY_COMPLETED"}}
	a := New(nil, stores.Executions, nil, nil)

	begin, save := a.responsesExchangeRecorder(current.ID, history)
	durable, err := begin(ctx, prefix)
	require.NoError(t, err)
	require.False(t, durable)
	require.NoError(t, save(ctx, client.ResponsesReplayUpdate{Items: append(append([]any(nil), prefix...), map[string]any{"type": "message", "role": "user", "content": "LATE_STEER"}), ReplacesHistory: !durable}))
	// A crash here must not erase the already saved work from the active execution.
	saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
	require.NoError(t, err)
	require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "CURRENT_ORIGINAL")
	require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "TOOL_WORK_ALREADY_COMPLETED")
}
