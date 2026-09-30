package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestHasPendingSteeringDoesNotClaimInput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(ctx, execution))
	repo := NewThreadInputRepo(db)
	check := func(want bool) {
		t.Helper()
		pending, err := repo.HasPendingSteering(ctx, execution.ID)
		require.NoError(t, err)
		require.Equal(t, want, pending)
	}
	check(false)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "steer"}
	require.NoError(t, repo.CreateSteeringForActiveExecution(ctx, input, execution.ID))
	check(true)
	check(true)
	stored, err := repo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, execution.ID, stored.ExpectedTurnID)
	pending, err := repo.HasPendingSteering(ctx, "other-execution")
	require.NoError(t, err)
	require.False(t, pending)
	claimed, err := repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	check(false)
	require.NoError(t, repo.CommitLocalSteering(ctx, execution.ID, []string{input.ID}, []any{map[string]any{"type": "message", "role": "user", "content": "steer"}}))
	check(false)
}
