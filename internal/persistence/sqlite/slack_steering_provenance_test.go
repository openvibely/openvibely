package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackSteeringRechecksSavedBinding(t *testing.T) {
	for _, boundary := range []string{"prepare", "commit"} {
		t.Run(boundary, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			auth := NewSlackAuthRepo(db)
			entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
			require.NoError(t, auth.Create(owner, entry))
			require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
			admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
			task := createThreadInputTask(t, ctx, db, "default")
			agent := createThreadInputLLMConfig(t, owner, db)
			executions := NewExecutionRepo(db)
			execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
			require.NoError(t, executions.Create(owner, execution))
			inputs := NewThreadInputRepo(db)
			input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "Instruction"}
			require.NoError(t, inputs.CreateSteeringForActiveExecution(admitted, input, execution.ID))
			if boundary == "commit" {
				prepared, err := inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
				require.NoError(t, err)
				require.Len(t, prepared, 1)
			}
			queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "Queued instruction"}
			require.NoError(t, inputs.CreateQueued(admitted, queued))
			require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			_, convertErr := inputs.ConvertQueuedToSteering(owner, queued.ID, execution.ID, execution.ID)
			require.Error(t, convertErr, "an owner cannot lend authority to the removed sender binding")
			savedQueue, err := inputs.GetByID(ctx, queued.ID)
			require.NoError(t, err)
			require.Equal(t, models.ThreadInputModeQueued, savedQueue.InputMode)

			if boundary == "prepare" {
				prepared, err := inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
				require.NoError(t, err)
				require.Empty(t, prepared)
				stored, err := inputs.GetByID(ctx, input.ID)
				require.NoError(t, err)
				require.Equal(t, models.ThreadInputStatus("cancelled"), stored.InputStatus)
			} else {
				require.Error(t, inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"Instruction"}}))
				stored, err := inputs.GetByID(ctx, input.ID)
				require.NoError(t, err)
				require.Equal(t, models.ThreadInputPending, stored.InputStatus)
			}
			replay, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
			require.NoError(t, err)
			require.Empty(t, replay[execution.ID])
			rejected := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "Late instruction"}
			require.Error(t, inputs.CreateSteeringForActiveExecution(admitted, rejected, execution.ID))
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_inputs`).Scan(&count))
			require.Equal(t, 2, count)
		})
	}
}
