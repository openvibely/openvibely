package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSaveInTurnCompactionCheckpointAtomicReplayRetirement(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	tasks, executions := NewTaskRepo(db, nil), NewExecutionRepo(db)
	task := &models.Task{ProjectID: "default", Title: "atomic compaction", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, tasks.Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "original prompt"}
	require.NoError(t, executions.Create(ctx, execution))
	require.NoError(t, executions.Complete(ctx, execution.ID, models.ExecFailed, "UI output", "", 0, 0))
	originalReplay := []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"assistant","content":"old replay"}]`}}
	require.NoError(t, executions.ReplaceReasoningReplay(ctx, execution.ID, "UI reasoning", originalReplay))
	checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: task.ID, SourceExecutionID: execution.ID, Strategy: "local_summary", Summary: "old summary"}
	require.NoError(t, executions.UpsertChatCompactionCheckpoint(ctx, checkpoint))
	before, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	// Fail after the checkpoint upsert and replay delete, during sentinel insertion.
	_, err = db.Exec(`CREATE TRIGGER fail_replay_insert BEFORE INSERT ON execution_replay_messages BEGIN SELECT RAISE(ABORT, 'injected replay failure'); END`)
	require.NoError(t, err)
	checkpoint.Strategy = "local_summary_inflight"
	checkpoint.Summary = "new summary"
	checkpoint.History = []models.Execution{{ContextKind: "summary", PromptSent: "new summary"}}
	require.ErrorContains(t, executions.SaveInTurnCompactionCheckpoint(ctx, checkpoint), "injected replay failure")
	saved, err := executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
	require.NoError(t, err)
	require.Equal(t, "old summary", saved.Summary)
	replay, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Equal(t, before, replay)
	_, err = db.Exec(`DROP TRIGGER fail_replay_insert`)
	require.NoError(t, err)
	require.NoError(t, executions.SaveInTurnCompactionCheckpoint(ctx, checkpoint))
	saved, err = executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
	require.NoError(t, err)
	require.Equal(t, "new summary", saved.Summary)
	replay, err = executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Len(t, replay[execution.ID], 1)
	require.Equal(t, "[]", replay[execution.ID][0].TranscriptJSON)
	unchanged, err := executions.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, "original prompt", unchanged.PromptSent)
	require.Equal(t, "UI output", unchanged.Output)
	reasoning, err := executions.ReasoningContentByIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Equal(t, "UI reasoning", reasoning[execution.ID])
}
