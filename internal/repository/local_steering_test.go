package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCommitLocalSteeringIsAtomicAndSurvivesRequeue(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	executions := NewExecutionRepo(db)
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, executions.Create(ctx, execution))
	repo := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "steer"}
	require.NoError(t, repo.CreateSteeringForActiveExecution(ctx, input, execution.ID))
	history := []any{map[string]any{"type": "message", "role": "user", "content": "steer"}}
	require.Error(t, repo.CommitLocalSteering(ctx, execution.ID, []string{input.ID, "missing"}, history))
	stored, err := repo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	require.NoError(t, repo.CommitLocalSteering(ctx, execution.ID, []string{input.ID}, history))
	require.NoError(t, repo.CommitLocalSteering(ctx, execution.ID, []string{input.ID}, history))
	requeued, err := repo.RequeuePendingSteeringForExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Empty(t, requeued)
	replay, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Len(t, replay[execution.ID], 1)
	require.Contains(t, replay[execution.ID][0].TranscriptJSON, "steer")
}
